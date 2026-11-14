package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The state a rebuild of an old master leaves when it fails, and the ways
// forward and out of it (part G).

// jobStateOf reads the stored state of job id.
func jobStateOf(t *testing.T, e *env, id string) jobState {
	t.Helper()
	j, _, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// failedRebuild runs a rebuild of the old master that fails at cmd and
// returns the job id.
func failedRebuild(t *testing.T, g *promoteRig, cmd string) string {
	t.Helper()
	g.m.fail(cmd, "gagal di "+cmd)
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	g.e.waitJob(id, store.JobFailed)
	g.e.waitIdle(id)
	return id
}

// TestRebuildFailureBeforeSettleKeepsOldMaster (G-1): a failure before the
// agent's configureReplication answered OK leaves the old master waiting
// for its rebuild: flag unset, marker kept.
func TestRebuildFailureBeforeSettleKeepsOldMaster(t *testing.T) {
	g := rebuildRig(t)
	id := failedRebuild(t, g, "mysql.configureReplication")
	if jobStateOf(t, g.e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded set although configureReplication failed")
	}
	if _, err := g.e.st.GetTopologyByOldMaster(g.m.id); err != nil {
		t.Fatalf("old-master marker after a failure before the settle: %v", err)
	}
	if ok, err := AwaitingRebuild(g.e.st, mustServer(t, g.e, g.m.id)); err != nil || !ok {
		t.Fatalf("AwaitingRebuild = %v, %v; want true", ok, err)
	}
}

// TestRebuildFailureAfterSettleIsOrdinaryReplica (G-1): once the agent
// answered configureReplication OK it ended the quarantine; a failure after
// that (startReplica, verify) leaves an ordinary failed replica.
func TestRebuildFailureAfterSettleIsOrdinaryReplica(t *testing.T) {
	for _, cmd := range []string{"mysql.startReplica", "mysql.waitSynced"} {
		t.Run(cmd, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			id := failedRebuild(t, g, cmd)
			if !jobStateOf(t, e, id).QuarantineEnded {
				t.Fatal("QuarantineEnded not set after configureReplication answered OK")
			}
			if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("old-master marker kept after the settle: %v", err)
			}
			if ok, err := AwaitingRebuild(e.st, mustServer(t, e, g.m.id)); err != nil || ok {
				t.Fatalf("AwaitingRebuild = %v, %v; want false", ok, err)
			}
			if err := e.r.refuseAwaitingRebuild(g.m.id); err != nil {
				t.Fatalf("resync/detach refused for an ordinary failed replica: %v", err)
			}
			if err := e.r.DetachReplica(context.Background(), g.m.id, false); err != nil {
				t.Fatalf("DetachReplica: %v", err)
			}
		})
	}
}

// TestRetryConnectHealsQuarantineEnded (G-1): a connect step that failed
// without the flag (as after a panel stop between the agent's answer and
// the save) sets it on a Retry whose configureReplication answers OK.
func TestRetryConnectHealsQuarantineEnded(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	id := failedRebuild(t, g, "mysql.configureReplication")
	scriptReplicaTarget(g.m, "db-master", "clone")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	if !jobStateOf(t, e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded not set by the retried connect step")
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old-master marker after the retried rebuild: %v", err)
	}
}

// TestCancelAfterSettleIsOrdinaryCancel (G-1): a rebuild cancelled after
// the agent ended the quarantine is not put back to quarantined (the
// agent is a replica now): it is cancelled as any Buat replika, its agent
// asked to release the role.
func TestCancelAfterSettleIsOrdinaryCancel(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	g.m.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		started <- struct{}{}
		<-block
		return nil, errors.New("dibatalkan")
	})
	g.m.reply("mysql.resetReplica", nil)
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	<-started
	mark := g.m.mark()
	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	close(block)
	e.waitJob(id, store.JobCanceled)
	if srv := mustServer(t, e, g.m.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after a cancel after the settle = %s, want unassigned", srv.Role)
	}
	released := false
	for _, c := range g.m.cmds(mark) {
		released = released || c == "mysql.releaseRole"
	}
	if !released {
		t.Fatalf("cancel after the settle sent %v, want mysql.releaseRole", g.m.cmds(mark))
	}
}

// TestCancelRebuildAttempt (G-2): "Batalkan percobaan bangun ulang" makes a
// failed rebuild's old master a plain quarantined old master, with no agent
// command: the failed job is superseded, the server can be deleted (then
// the new master too), and a new rebuild runs.
func TestCancelRebuildAttempt(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	ctx := context.Background()
	id := failedRebuild(t, g, "mysql.preflight")
	mark := g.m.mark()
	if err := e.r.CancelRebuildAttempt(ctx, g.m.id); err != nil {
		t.Fatalf("CancelRebuildAttempt: %v", err)
	}
	if cmds := g.m.cmds(mark); len(cmds) != 0 {
		t.Fatalf("sent %v to the old master", cmds)
	}
	if _, err := e.st.GetReplicaByServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after: %v", err)
	}
	if srv := mustServer(t, e, g.m.id); srv.Role != store.RoleQuarantined {
		t.Fatalf("role after = %s", srv.Role)
	}
	if reason := e.r.Superseded(id); reason == "" {
		t.Fatal("the failed rebuild job is not superseded")
	}
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err == nil {
		t.Fatal("Retry of the failed rebuild accepted")
	}
	// A second time: nothing to cancel.
	const refusal = "server ini bukan master lama dengan percobaan bangun ulang yang gagal"
	if err := e.r.CancelRebuildAttempt(ctx, g.m.id); err == nil || err.Error() != refusal {
		t.Fatalf("second CancelRebuildAttempt = %v, want %q", err, refusal)
	}
	// An ordinary failed replica is refused the same way.
	if err := e.st.SetReplicaState(g.b.id, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	if err := e.r.CancelRebuildAttempt(ctx, g.b.id); err == nil || err.Error() != refusal {
		t.Fatalf("CancelRebuildAttempt(ordinary failed replica) = %v, want %q", err, refusal)
	}

	// A new rebuild runs from here.
	scriptReplicaTarget(g.m, "db-master", "clone")
	again, err := g.startRebuild()
	if err != nil {
		t.Fatalf("rebuild after the cancel: %v", err)
	}
	e.waitJob(again, store.JobSucceeded)
	e.waitIdle(again)
}

// TestCancelRebuildAttemptThenDelete (G-2): after the cancel the old master
// can be deleted, and once its row and the other replicas are gone the new
// master can be deleted too.
func TestCancelRebuildAttemptThenDelete(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	ctx := context.Background()
	failedRebuild(t, g, "mysql.preflight")
	for _, fa := range []*fakeAgent{g.b, g.d} {
		if err := e.r.DetachReplica(ctx, fa.id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.r.DeleteServer(ctx, g.c.id); !errors.Is(err, ErrServerIsMaster) {
		t.Fatalf("delete of the new master while the failed row exists = %v, want ErrServerIsMaster", err)
	}
	if err := e.r.CancelRebuildAttempt(ctx, g.m.id); err != nil {
		t.Fatal(err)
	}
	if err := e.r.DeleteServer(ctx, g.m.id); err != nil {
		t.Fatalf("delete of the old master after the cancel: %v", err)
	}
	g.c.reply("mysql.releaseRole", nil)
	if err := e.r.DeleteServer(ctx, g.c.id); err != nil {
		t.Fatalf("delete of the new master after: %v", err)
	}
}

// TestLeftoverAfterCopyStarted (G-3): once a rebuild of the old master
// reached "Salin data", Periksa and Amankan data tertinggal are refused
// with one sentence (its binary logs were reset, databases dropped), also
// on the plain quarantined old master after "Batalkan percobaan bangun
// ulang" or after a cancel; before the copy they run.
func TestLeftoverAfterCopyStarted(t *testing.T) {
	const refusal = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus " +
		"atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel."
	ctx := context.Background()
	t.Run("cancel attempt after copy", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		failedRebuild(t, g, "mysql.configureReplication")
		if err := e.r.CancelRebuildAttempt(ctx, g.m.id); err != nil {
			t.Fatal(err)
		}
		if state, err := RebuildCopyState(e.st, g.m.id); err != nil || state != CopyStarted {
			t.Fatalf("RebuildCopyState = %v, %v; want started", state, err)
		}
		for name, start := range map[string]func(context.Context, int64) (string, error){
			"leftover": e.r.StartLeftover, "salvage": e.r.StartSalvage} {
			if _, err := start(ctx, g.m.id); err == nil || err.Error() != refusal {
				t.Fatalf("%s after the copy = %v, want %q", name, err, refusal)
			}
		}
	})
	t.Run("cancel of a running rebuild after copy", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		block := make(chan struct{})
		started := make(chan struct{}, 1)
		g.m.on("mysql.configureReplication", func(json.RawMessage) (any, error) {
			started <- struct{}{}
			<-block
			return nil, errors.New("dibatalkan")
		})
		g.m.reply("mysql.resetReplica", nil)
		id, err := g.startRebuild()
		if err != nil {
			t.Fatal(err)
		}
		<-started
		if err := e.r.Cancel(id); err != nil {
			t.Fatal(err)
		}
		close(block)
		e.waitJob(id, store.JobCanceled)
		e.waitIdle(id)
		if srv := mustServer(t, e, g.m.id); srv.Role != store.RoleQuarantined {
			t.Fatalf("role after the cancel = %s", srv.Role)
		}
		if _, err := e.r.StartLeftover(ctx, g.m.id); err == nil || err.Error() != refusal {
			t.Fatalf("leftover after a cancel past the copy = %v, want %q", err, refusal)
		}
	})
	t.Run("before copy", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		failedRebuild(t, g, "mysql.preflight")
		if err := e.r.CancelRebuildAttempt(ctx, g.m.id); err != nil {
			t.Fatal(err)
		}
		if state, err := RebuildCopyState(e.st, g.m.id); err != nil || state != CopyNotStarted {
			t.Fatalf("RebuildCopyState = %v, %v; want not started", state, err)
		}
		g.m.reply("mysql.leftover", proto.LeftoverResult{})
		id, err := e.r.StartLeftover(ctx, g.m.id)
		if err != nil {
			t.Fatalf("leftover before the copy: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		e.waitIdle(id)
	})
}

// TestTLSSkipsFailedRebuild (G-5): an old master left by a failed rebuild
// (its agent still quarantined) is treated like a quarantined one by the
// certificate jobs: no renewal, StartMySQLTLS and a retry of a certificate
// job refused, Perketat leaves it out. No certificate job supersedes the
// failed rebuild.
func TestTLSSkipsFailedRebuild(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	ctx := context.Background()
	rebuildID := failedRebuild(t, g, "mysql.configureReplication")
	if err := e.st.SetServerTLS(store.ServerTLS{ServerID: g.m.id, CertSerial: "01", NotAfter: time.Now().Add(10 * 24 * time.Hour),
		InstalledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mark := g.m.mark()
	if ids := e.r.RenewMySQLTLS(ctx); len(ids) != 0 {
		t.Fatalf("renewal started %v for a failed rebuild's old master", ids)
	}
	const want = "Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika."
	if _, err := e.r.StartMySQLTLS(ctx, g.m.id); err == nil || err.Error() != want {
		t.Fatalf("StartMySQLTLS = %v, want %q", err, want)
	}
	if err := e.r.refuseQuarantinedTLS(store.Job{Kind: KindMySQLTLS, ServerID: g.m.id}); err == nil || err.Error() != want {
		t.Fatalf("refuseQuarantinedTLS = %v, want %q", err, want)
	}
	if cmds := g.m.cmds(mark); len(cmds) != 0 {
		t.Fatalf("sent %v to the old master", cmds)
	}
	if reason := e.r.Superseded(rebuildID); reason != "" {
		t.Fatalf("the failed rebuild is superseded: %s", reason)
	}
}

// TestListingRefusalReachesJobUnchanged (G-6): the agent's listing refusal,
// with its advised account, README section and button label, is stored as
// the job's error, step message and replica's last error unchanged.
func TestListingRefusalReachesJobUnchanged(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	const text = "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'panel_admin'@'10.0.0.5' yang " +
		"dipakai agent tidak punya hak SHOW DATABASES. Beri hak itu sebagai root MySQL di server ini: GRANT SHOW " +
		"DATABASES ON *.* TO 'panel_admin'@'10.0.0.5'; Server ini baca-saja, jadi perintah itu harus dijalankan dengan " +
		"langkah di README, bagian \"Memberi hak SHOW DATABASES di server yang dikarantina\". Sesudah itu tekan " +
		"\"Ulangi dari tahap ini\" pada pekerjaan di panel."
	id := failedRebuild(t, g, "mysql.configureReplication")
	g.m.fail("mysql.configureReplication", text)
	scriptRetry := func() string {
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		j, _ := e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		return j.Error
	}
	got := scriptRetry()
	if want := "Sambungkan replikasi: " + text; got != want {
		t.Fatalf("job error = %q, want %q", got, want)
	}
	if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || !strings.Contains(rep.LastError, text) {
		t.Fatalf("replica last error = %q, %v", rep.LastError, err)
	}
}
