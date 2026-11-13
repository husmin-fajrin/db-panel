package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// rebuildRig is a promote rig after the promotion and the cutover: the old
// master g.m is quarantined, has no connections and its agent answers a
// rebuild like an empty target (approved by root on it); the new master
// g.c serves the copy.
func rebuildRig(t *testing.T) *promoteRig {
	t.Helper()
	g := newPromoteRig(t)
	e := g.e
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if err := e.st.SetCutoverPending(g.topo.ID, false); err != nil {
		t.Fatal(err)
	}
	g.m.reply("mysql.connections", proto.ConnectionsResult{Typed: true, TakenAt: time.Now()})
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.c.reply("mysql.status", masterStatus())
	g.c.reply("mysql.createDumpUser", nil)
	g.c.reply("mysql.dropUser", nil)
	g.c.reply("mysql.sweepDumpUsers", proto.SweepDumpUsersResult{})
	return g
}

func (g *promoteRig) startRebuild() (string, error) {
	return g.e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: g.c.id,
		TargetServerID: g.m.id, RebuildOldMaster: true, ConfirmWipe: true})
}

func notApproved(code string) proto.RebuildApprovalResult {
	return proto.RebuildApprovalResult{Required: true, Code: code, Source: "10.0.0.1:3306", RemainingSec: 1700}
}

// noRebuildStarted fails unless the old master has no replica row and no
// job for it exists.
func noRebuildStarted(t *testing.T, g *promoteRig) {
	t.Helper()
	if _, err := g.e.st.GetReplicaByServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused rebuild registered a replica: %v", err)
	}
	if j, busy, err := g.e.st.ActiveJobForServer(g.m.id); err != nil || busy {
		t.Fatalf("refused rebuild left a job: %+v, %v", j, err)
	}
}

// TestRebuildRefusedWithoutApproval: without root's approval on the old
// master its rebuild does not start; the refusal names the command and
// the code, and only the reads reach the old master.
func TestRebuildRefusedWithoutApproval(t *testing.T) {
	g := rebuildRig(t)
	g.m.reply("mysql.rebuildApproval", notApproved("123456"))
	mark := g.m.mark()
	_, err := g.startRebuild()
	if !errors.Is(err, ErrRebuildNeedsApproval) {
		t.Fatalf("StartCreateReplica = %v, want ErrRebuildNeedsApproval", err)
	}
	want := "Bangun ulang db-master butuh persetujuan di server itu. Jalankan di server itu sebagai root: " +
		"sudo dbpanel agent approve 123456, lalu coba lagi."
	if err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	noRebuildStarted(t, g)
	wantCmds(t, "old master", g.m.cmds(mark), []string{"mysql.connections", "mysql.rebuildApproval"})
}

// TestRebuildRefusedOnOldAgent: an agent that does not know the approval
// cannot be rebuilt by this panel (global constraints: panel new, agent
// old).
func TestRebuildRefusedOnOldAgent(t *testing.T) {
	g := rebuildRig(t)
	g.m.oldAgent("mysql.rebuildApproval")
	_, err := g.startRebuild()
	if !errors.Is(err, ErrAgentOutdated) {
		t.Fatalf("StartCreateReplica = %v, want ErrAgentOutdated", err)
	}
	if !strings.Contains(err.Error(), "Agent perlu dimutakhirkan") || !strings.Contains(err.Error(), "db-master") {
		t.Fatalf("refusal = %q", err)
	}
	noRebuildStarted(t, g)
}

// TestRebuildNotRequiredProceeds: an agent that does not hold the server
// quarantined needs no approval.
func TestRebuildNotRequiredProceeds(t *testing.T) {
	g := rebuildRig(t)
	g.m.reply("mysql.rebuildApproval", proto.RebuildApprovalResult{Required: false})
	id, err := g.startRebuild()
	if err != nil {
		t.Fatalf("StartCreateReplica: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)
}

// TestRebuildRetryAndApproveRecheckApproval: Ulangi and Setujui of the
// rebuild ask the old master's agent again; an approval that no longer
// holds refuses them and leaves the job as it was.
func TestRebuildRetryAndApproveRecheckApproval(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)

	g.m.reply("mysql.rebuildApproval", notApproved("123456"))
	mark := g.m.mark()
	err = e.r.Retry(id, JobAck{Confirmed: true})
	if !errors.Is(err, ErrRebuildNeedsApproval) || !strings.Contains(err.Error(), "sudo dbpanel agent approve 123456") {
		t.Fatalf("Retry = %v, want ErrRebuildNeedsApproval", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %+v", j)
	}
	for _, c := range g.m.cmds(mark) {
		if c != "mysql.connections" && c != "mysql.rebuildApproval" {
			t.Fatalf("refused retry sent %s to the old master", c)
		}
	}
	// An old agent: refused too.
	g.m.oldAgent("mysql.rebuildApproval")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrAgentOutdated) ||
		!strings.Contains(err.Error(), "Agent perlu dimutakhirkan") {
		t.Fatalf("Retry on an old agent = %v, want ErrAgentOutdated", err)
	}

	// Waiting to drop leftovers: Setujui asks again.
	g.m.mu.Lock()
	delete(g.m.unknown, "mysql.rebuildApproval")
	g.m.mu.Unlock()
	j, _, _ := e.st.GetJob(id)
	if err := e.st.ReopenJob(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobWaiting, j.CurrentStep, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	e.setState(id, func(st *jobState) { st.WaitReason = WaitWipe })
	if err := e.r.Approve(id, JobAck{Confirmed: true}); !errors.Is(err, ErrRebuildNeedsApproval) {
		t.Fatalf("Approve = %v, want ErrRebuildNeedsApproval", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobWaiting {
		t.Fatalf("job after a refused approval = %+v", j)
	}

	// Approved again: the retry runs.
	scriptReplicaTarget(g.m, "db-master", "clone")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("approved retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// TestNeedsLocalApprovalMidJobNamesCommand: a step refused by strict mode
// on the old master (its approval ran out meanwhile) fails the job with
// the command to run there and the code, read from the agent.
func TestNeedsLocalApprovalMidJobNamesCommand(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	g.m.on("mysql.configure", func(json.RawMessage) (any, error) {
		// The approval no longer holds once the job runs; the agent
		// made a new request.
		g.m.reply("mysql.rebuildApproval", notApproved("654321"))
		return nil, &commands.LocalActionError{Msg: "server ini dikarantina (quarantined) mengikuti sumber 10.0.0.1:3306; " +
			"perintah mysql.configure butuh persetujuan root di server ini. Jalankan di server ini: sudo dbpanel agent " +
			"approve 654321 (kode berlaku 30 menit lagi)"}
	})
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	want := "Server db-master dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Jalankan di server itu " +
		"sebagai root: sudo dbpanel agent approve 654321, lalu klik Ulangi."
	if j.Error != want {
		t.Fatalf("job error = %q, want %q", j.Error, want)
	}
}

// TestCallWithinLocalApprovalMapping: needs_local_approval of any command
// but mysql.releaseRole becomes P12 (P12b when the code cannot be read);
// releaseRole's own refusal names another local command and passes as
// the agent wrote it.
func TestCallWithinLocalApprovalMapping(t *testing.T) {
	e := newEnv(t)
	fa := e.agent("db-old", "10.0.0.9")
	jr := &jobRun{r: e.r, steps: []step{{title: "Langkah"}}}
	ctx := context.Background()

	release := "server ini dikarantina (quarantined); perintah mysql.releaseRole ditolak. Karantina hanya lepas lewat " +
		"bangun ulang yang selesai, atau dengan perintah di server ini: sudo dbpanel agent set-role unassigned"
	fa.failCode("mysql.releaseRole", release)
	fa.reply("mysql.rebuildApproval", notApproved("222222"))
	err := jr.call(ctx, fa.id, "mysql.releaseRole", nil, nil)
	if !errors.Is(err, &proto.Error{Code: proto.CodeNeedsLocalApproval}) || err.Error() != release {
		t.Fatalf("releaseRole refusal = %v, want the agent's own", err)
	}
	if n := fa.count("mysql.rebuildApproval"); n != 0 {
		t.Fatalf("releaseRole refusal read the approval %d times", n)
	}

	fa.failCode("mysql.stopReplica", "butuh persetujuan")
	err = jr.call(ctx, fa.id, "mysql.stopReplica", nil, nil)
	var se stepError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "sudo dbpanel agent approve 222222, lalu klik Ulangi.") {
		t.Fatalf("stopReplica refusal = %v, want P12", err)
	}

	for name, set := range map[string]func(){
		"old agent":     func() { fa.oldAgent("mysql.rebuildApproval") },
		"agent refuses": func() { fa.fail("mysql.rebuildApproval", "pin peran/sumber agent tidak bisa dipakai") },
	} {
		set()
		err = jr.call(ctx, fa.id, "mysql.stopReplica", nil, nil)
		want := "Server db-old dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form Bangun ulang " +
			"sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi."
		if !errors.As(err, &se) || err.Error() != want {
			t.Fatalf("%s: stopReplica refusal = %v, want P12b", name, err)
		}
	}

	// Other errors pass as they are.
	fa.fail("mysql.startReplica", "gagal")
	if err := jr.call(ctx, fa.id, "mysql.startReplica", nil, nil); err == nil || err.Error() != "gagal" {
		t.Fatalf("other error = %v", err)
	}
}

// TestTLSSkipsQuarantined: the daily renewal leaves a quarantined server
// alone, and a new certificate for it is refused (it gets one when it is
// rebuilt).
func TestTLSSkipsQuarantined(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	if srv, _ := e.st.GetServer(g.m.id); srv.Role != store.RoleQuarantined {
		t.Fatalf("old master role = %s", srv.Role)
	}
	if err := e.st.SetServerTLS(store.ServerTLS{ServerID: g.m.id, CertSerial: "01", NotAfter: time.Now().Add(10 * 24 * time.Hour),
		InstalledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if ids := e.r.RenewMySQLTLS(context.Background()); len(ids) != 0 {
		t.Fatalf("renewal started %v for a quarantined server", ids)
	}
	_, err := e.r.StartMySQLTLS(context.Background(), g.m.id)
	want := "Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika."
	if err == nil || err.Error() != want {
		t.Fatalf("StartMySQLTLS = %v, want %q", err, want)
	}
	if slices.Contains(g.m.cmds(0), "mysql.tlsRequest") {
		t.Fatal("a quarantined server was asked for a certificate request")
	}
}
