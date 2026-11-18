package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// An old master keeps the panel role quarantined until its agent ends the
// quarantine (the settle: mysql.configureReplication answered OK in its
// rebuild); a failed, retried, approved or canceled rebuild never changes
// it (spec 5.1).

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

// before runs f each time fa receives cmd, before cmd's scripted answer.
func (fa *fakeAgent) before(cmd string, f func()) {
	fa.mu.Lock()
	h := fa.script[cmd]
	fa.mu.Unlock()
	fa.on(cmd, func(raw json.RawMessage) (any, error) {
		f()
		return h(raw)
	})
}

// roleNow is the server's stored role, or the error's text (for a fake
// agent's goroutine, which cannot fail the test).
func roleNow(e *env, id int64) string {
	srv, err := e.st.GetServer(id)
	if err != nil {
		return err.Error()
	}
	return srv.Role
}

// wantRebuildPending fails unless the old master is in the state "failed
// before the settle": role quarantined, its replica row failed, still the
// topology's old master, AwaitingRebuild.
func wantRebuildPending(t *testing.T, g *promoteRig, what string) {
	t.Helper()
	e := g.e
	if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
		t.Fatalf("%s: role = %s, want quarantined", what, got)
	}
	if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || rep.State != store.ReplicaFailed {
		t.Fatalf("%s: replica row = %+v, %v; want failed", what, rep, err)
	}
	if topo, err := e.st.GetTopologyByOldMaster(g.m.id); err != nil || topo.ID != g.topo.ID {
		t.Fatalf("%s: old-master marker = %+v, %v", what, topo, err)
	}
	if ok, err := AwaitingRebuild(e.st, mustServer(t, e, g.m.id)); err != nil || !ok {
		t.Fatalf("%s: AwaitingRebuild = %v, %v; want true", what, ok, err)
	}
}

// storeRebuild stores a rebuild job of the old master with status at step
// (its replica row registered as a rebuild registers it) and returns its id.
func storeRebuild(t *testing.T, g *promoteRig, id, status string, step int, steps []string) string {
	t.Helper()
	e := g.e
	if _, err := e.st.AddReplica(g.topo.ID, g.m.id, nil); err != nil {
		t.Fatal(err)
	}
	params := fmt.Sprintf(`{"master_server_id":%d,"rebuild_old_master":true}`, g.c.id)
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindCreateReplica, ServerID: g.m.id, TopologyID: g.topo.ID,
		ParamsJSON: params, CreatedAt: time.Now()}, steps); err != nil {
		t.Fatal(err)
	}
	if status != store.JobQueued {
		if err := e.st.UpdateJob(id, status, step, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetStep(id, step, store.StepRunning, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestRebuildKeepsRoleQuarantinedUntilSettle: from the start of the rebuild
// to the agent's OK answer to mysql.configureReplication the old master's
// role is quarantined, its row creating and the marker kept; from that
// answer on its role is replica, the flag stored and the marker gone.
func TestRebuildKeepsRoleQuarantinedUntilSettle(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	defer open()
	g.m.before("mysql.preflight", func() { <-gate })
	var mu sync.Mutex
	roles := map[string]string{}
	record := func(at string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			if _, seen := roles[at]; !seen {
				roles[at] = roleNow(e, g.m.id)
			}
		}
	}
	g.m.before("mysql.cloneFrom", record("copy"))
	g.m.before("mysql.configureReplication", record("connect"))
	g.m.before("mysql.startReplica", record("after the settle"))
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
		t.Fatalf("role at the start = %s, want quarantined", got)
	}
	if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || rep.State != store.ReplicaCreating {
		t.Fatalf("row at the start = %+v, %v; want creating", rep, err)
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); err != nil {
		t.Fatalf("marker at the start: %v", err)
	}
	open()
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	mu.Lock()
	for at, want := range map[string]string{"copy": store.RoleQuarantined, "connect": store.RoleQuarantined,
		"after the settle": store.RoleReplica} {
		if roles[at] != want {
			t.Errorf("role at %s = %q, want %s", at, roles[at], want)
		}
	}
	mu.Unlock()
	if !jobStateOf(t, e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded not stored")
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("marker after the settle: %v", err)
	}
	if got := roleNow(e, g.m.id); got != store.RoleReplica {
		t.Fatalf("role at the end = %s", got)
	}
}

// TestSettleStoreFails: when the settle cannot be stored, the connect step
// fails with "simpan keadaan pekerjaan: …" and the role, the marker and the
// flag stay as they were; a Retry (the agent answers OK again) stores all
// three.
func TestSettleStoreFails(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	e.r.settleStore = func(string, string, int64) error { return errors.New("disk penuh") }
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if want := "Sambungkan replikasi: simpan keadaan pekerjaan: disk penuh"; j.Error != want {
		t.Fatalf("job error = %q, want %q", j.Error, want)
	}
	wantRebuildPending(t, g, "after the failed settle")
	if jobStateOf(t, e, id).QuarantineEnded {
		t.Fatal("flag stored although the settle failed")
	}
	e.r.settleStore = nil
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	if !jobStateOf(t, e, id).QuarantineEnded {
		t.Fatal("flag not set by the retry")
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("marker after the retry: %v", err)
	}
	if got := roleNow(e, g.m.id); got != store.RoleReplica {
		t.Fatalf("role after the retry = %s", got)
	}
}

// TestFailedRebuildStaysQuarantined: a failure before the copy, at the copy
// and at the connect step before the agent's answer leaves the old master
// quarantined with a failed row and its marker.
func TestFailedRebuildStaysQuarantined(t *testing.T) {
	for _, cmd := range []string{"mysql.preflight", "mysql.cloneFrom", "mysql.configureReplication"} {
		t.Run(cmd, func(t *testing.T) {
			g := rebuildRig(t)
			id := failedRebuild(t, g, cmd)
			wantRebuildPending(t, g, "after a failure at "+cmd)
			if jobStateOf(t, g.e, id).QuarantineEnded {
				t.Fatal("flag set")
			}
		})
	}
}

// TestEveryFailurePathLeavesQuarantinedWithFailedRow: every path that fails
// a rebuild job (a step error, a panel restart, a job this panel cannot
// run, a launch refused in Retry) leaves the role quarantined, the row
// failed and the marker.
func TestEveryFailurePathLeavesQuarantinedWithFailedRow(t *testing.T) {
	ctx := context.Background()
	t.Run("step error", func(t *testing.T) {
		g := rebuildRig(t)
		failedRebuild(t, g, "mysql.preflight")
		wantRebuildPending(t, g, "step error")
	})
	t.Run("panel restart", func(t *testing.T) {
		g := rebuildRig(t)
		id := storeRebuild(t, g, "88888888888888888888888888888881", store.JobRunning, 4, createReplicaStepNames())
		g.e.r.Resume(ctx)
		if j, _, _ := g.e.st.GetJob(id); j.Status != store.JobFailed {
			t.Fatalf("job after Resume = %+v", j)
		}
		wantRebuildPending(t, g, "Resume")
	})
	t.Run("unrunnable job", func(t *testing.T) {
		g := rebuildRig(t)
		// A step list this panel does not know: newRun refuses it.
		id := storeRebuild(t, g, "88888888888888888888888888888882", store.JobWaiting, 0, []string{"Tahap lama"})
		if err := g.e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		g.e.waitJob(id, store.JobFailed)
		g.e.waitIdle(id)
		wantRebuildPending(t, g, "failUnrunnable")
	})
	t.Run("launch refused at Retry", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		id := failedRebuild(t, g, "mysql.preflight")
		e.r.afterReopen = func(string) {
			e.r.mu.Lock()
			e.r.closed = true
			e.r.mu.Unlock()
		}
		err := e.r.Retry(id, JobAck{Confirmed: true})
		e.r.afterReopen = nil
		e.r.mu.Lock()
		e.r.closed = false
		e.r.mu.Unlock()
		if !errors.Is(err, errClosed) {
			t.Fatalf("Retry = %v, want the launch refused", err)
		}
		if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
			t.Fatalf("job after the refused launch = %+v", j)
		}
		wantRebuildPending(t, g, "launch refused")
	})
}

// TestResumeFailsRebuildRowBeforeJob: a rebuild running when the panel
// stopped is failed by Resume, its row before the job (a trigger on the
// store refuses the job's failure while the row is not failed), under
// startMu: a Retry, which takes startMu too, never sees the job failed with
// its row creating.
func TestResumeFailsRebuildRowBeforeJob(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	id := storeRebuild(t, g, "88888888888888888888888888888883", store.JobRunning, 4, createReplicaStepNames())
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER rebuild_row_first BEFORE UPDATE OF status ON jobs
		WHEN NEW.status = 'failed' AND NEW.kind = 'create_replica'
		AND EXISTS (SELECT 1 FROM replicas WHERE server_id = NEW.server_id AND state != 'failed')
		BEGIN SELECT RAISE(ABORT, 'job failed before its replica row'); END`); err != nil {
		t.Fatal(err)
	}
	e.r.startMu.Lock()
	done := make(chan struct{})
	go func() {
		e.r.Resume(context.Background())
		close(done)
	}()
	select {
	case <-done:
		e.r.startMu.Unlock()
		t.Fatal("Resume ran without startMu")
	case <-time.After(200 * time.Millisecond):
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobRunning {
		e.r.startMu.Unlock()
		t.Fatalf("job written while startMu was held: %+v", j)
	}
	e.r.startMu.Unlock()
	<-done
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed || j.Error != resumeError {
		t.Fatalf("job after Resume = %+v", j)
	}
	wantRebuildPending(t, g, "after Resume")
}

// TestFailedRebuildAfterSettleStaysReplica (G-1): once the agent answered
// configureReplication OK it ended the quarantine; a failure after that
// (startReplica, verify) leaves an ordinary failed replica, role replica.
func TestFailedRebuildAfterSettleStaysReplica(t *testing.T) {
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
			if got := roleNow(e, g.m.id); got != store.RoleReplica {
				t.Fatalf("role after a failure after the settle = %s, want replica", got)
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

// TestRebuildFailureBeforeSettleKeepsOldMaster (G-1): a failure before the
// agent's configureReplication answered OK leaves the old master waiting
// for its rebuild: flag unset, marker kept, role quarantined.
func TestRebuildFailureBeforeSettleKeepsOldMaster(t *testing.T) {
	g := rebuildRig(t)
	id := failedRebuild(t, g, "mysql.configureReplication")
	if jobStateOf(t, g.e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded set although configureReplication failed")
	}
	wantRebuildPending(t, g, "after a failure before the settle")
}

// TestRetryConnectHealsQuarantineEnded (G-1): a connect step that failed
// without the flag (as after a panel stop between the agent's answer and
// the save) sets it on a Retry whose configureReplication answers OK, and
// the role becomes replica.
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
	if got := roleNow(e, g.m.id); got != store.RoleReplica {
		t.Fatalf("role after the retried rebuild = %s, want replica", got)
	}
}

// TestNormalizeLegacyFailedRebuilds: a row from before this rule (role
// replica, failed row, still the topology's old master) becomes "failed
// before the settle" (role quarantined) unless its newest rebuild job has
// the flag: then the marker goes and it stays an ordinary failed replica.
// No job, or one that cannot be read, counts as not settled. Running it
// twice changes nothing more; an ordinary failed replica is left alone. A
// legacy rebuild still running at the restart is normalised too.
func TestNormalizeLegacyFailedRebuilds(t *testing.T) {
	// legacy gives the old master the shape an earlier panel left.
	legacy := func(t *testing.T, g *promoteRig, state string) {
		t.Helper()
		e := g.e
		if _, err := e.st.AddReplica(g.topo.ID, g.m.id, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetServerRole(g.m.id, store.RoleReplica); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetReplicaState(g.m.id, state, "gagal"); err != nil {
			t.Fatal(err)
		}
		// An ordinary failed replica beside it.
		if err := e.st.SetReplicaState(g.b.id, store.ReplicaFailed, "gagal"); err != nil {
			t.Fatal(err)
		}
	}
	// Every job in the same second: created_at cannot order them, only
	// the order they were stored in can, whatever their random ids.
	created := time.Now().Truncate(time.Second)
	job := func(t *testing.T, g *promoteRig, id, params string) {
		t.Helper()
		if err := g.e.st.CreateJob(store.Job{ID: id, Kind: KindCreateReplica, ServerID: g.m.id, TopologyID: g.topo.ID,
			ParamsJSON: params, CreatedAt: created}, createReplicaStepNames()); err != nil {
			t.Fatal(err)
		}
		if err := g.e.st.UpdateJob(id, store.JobFailed, 6, "gagal", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	ordinaryUntouched := func(t *testing.T, g *promoteRig) {
		t.Helper()
		if got := roleNow(g.e, g.b.id); got != store.RoleReplica {
			t.Fatalf("ordinary failed replica: role = %s", got)
		}
		if rep, err := g.e.st.GetReplicaByServer(g.b.id); err != nil || rep.State != store.ReplicaFailed {
			t.Fatalf("ordinary failed replica: row = %+v, %v", rep, err)
		}
	}
	const unsettled = `{"rebuild_old_master":true}`
	for _, tc := range []struct {
		name, params string // params "" = no job
		settled      bool
	}{
		{"unflagged job", unsettled, false},
		{"no job", "", false},
		{"unreadable job", `{"rebuild_old_master":`, false},
		{"flagged job", `{"rebuild_old_master":true,"quarantine_ended":true}`, true},
		{"older flagged, newer unflagged", "older-flagged", false},
		{"older flagged, newer unflagged, ids the other way", "older-flagged-reversed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			legacy(t, g, store.ReplicaFailed)
			switch tc.params {
			case "":
			case "older-flagged":
				job(t, g, "99999999999999999999999999999990", `{"rebuild_old_master":true,"quarantine_ended":true}`)
				job(t, g, "99999999999999999999999999999991", unsettled)
			case "older-flagged-reversed":
				job(t, g, "99999999999999999999999999999991", `{"rebuild_old_master":true,"quarantine_ended":true}`)
				job(t, g, "99999999999999999999999999999990", unsettled)
			default:
				job(t, g, "99999999999999999999999999999991", tc.params)
			}
			for i := range 2 {
				if err := e.r.normalizeFailedRebuilds(); err != nil {
					t.Fatalf("normalizeFailedRebuilds #%d: %v", i+1, err)
				}
				if tc.settled {
					if got := roleNow(e, g.m.id); got != store.RoleReplica {
						t.Fatalf("#%d: role = %s, want replica", i+1, got)
					}
					if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
						t.Fatalf("#%d: marker kept: %v", i+1, err)
					}
					if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || rep.State != store.ReplicaFailed {
						t.Fatalf("#%d: row = %+v, %v", i+1, rep, err)
					}
				} else {
					wantRebuildPending(t, g, fmt.Sprintf("normalised #%d", i+1))
				}
				ordinaryUntouched(t, g)
			}
		})
	}
	t.Run("legacy rebuild running at the restart", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		legacy(t, g, store.ReplicaCreating)
		id := "99999999999999999999999999999992"
		if err := e.st.CreateJob(store.Job{ID: id, Kind: KindCreateReplica, ServerID: g.m.id, TopologyID: g.topo.ID,
			ParamsJSON: unsettled, CreatedAt: time.Now()}, createReplicaStepNames()); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(id, store.JobRunning, 4, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		e.r.Resume(context.Background())
		wantRebuildPending(t, g, "after Resume")
		ordinaryUntouched(t, g)
	})
}

// TestRetryFromQuarantinedKeepsRole: a Retry of a failed rebuild runs the
// job with the role still quarantined; when it fails again, or its launch
// is refused, the row is failed and the role quarantined.
func TestRetryFromQuarantinedKeepsRole(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	id := failedRebuild(t, g, "mysql.preflight")
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	defer open()
	g.m.before("mysql.preflight", func() { <-gate })
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitFor("preflight retried", func() bool { return g.m.count("mysql.preflight") == 2 })
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobRunning {
		t.Fatalf("job while retried = %+v", j)
	}
	if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
		t.Fatalf("role while retried = %s", got)
	}
	open()
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	wantRebuildPending(t, g, "after the retry failed")

	e.r.afterReopen = func(string) {
		e.r.mu.Lock()
		e.r.closed = true
		e.r.mu.Unlock()
	}
	err := e.r.Retry(id, JobAck{Confirmed: true})
	e.r.afterReopen = nil
	e.r.mu.Lock()
	e.r.closed = false
	e.r.mu.Unlock()
	if err == nil {
		t.Fatal("Retry with its launch refused passed")
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after the refused launch = %+v", j)
	}
	wantRebuildPending(t, g, "after the refused launch")
}

// TestLeftoverRefusedWhileRebuildRuns: while the rebuild runs or waits,
// Periksa and Amankan data tertinggal are refused as for any busy server.
func TestLeftoverRefusedWhileRebuildRuns(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	ctx := context.Background()
	g.m.reply("mysql.leftover", proto.LeftoverResult{GTIDSet: masterUUID + ":121-123", Transactions: 3})
	check, err := e.r.StartLeftover(ctx, g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(check, store.JobSucceeded)
	e.waitIdle(check)
	busy := func(what string) {
		t.Helper()
		for name, start := range map[string]func(context.Context, int64) (string, error){
			"leftover": e.r.StartLeftover, "salvage": e.r.StartSalvage} {
			if _, err := start(ctx, g.m.id); !errors.Is(err, ErrBusy) {
				t.Fatalf("%s while the rebuild %s = %v, want busy", name, what, err)
			}
		}
	}
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	defer open()
	g.m.fail("mysql.preflight", "gagal")
	g.m.before("mysql.preflight", func() { <-gate })
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("rebuild at its preflight", func() bool { return g.m.count("mysql.preflight") == 1 })
	busy("runs")
	open()
	j, _ := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	// The same job waiting for the user.
	if err := e.st.ReopenJob(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobWaiting, j.CurrentStep, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(g.m.id, store.ReplicaCreating, ""); err != nil {
		t.Fatal(err)
	}
	busy("waits")
}

// TestCancelRunningRebuildStaysQuarantined: Batalkan of a running rebuild
// removes its row; the role stays quarantined throughout (a trigger on the
// store refuses any write of unassigned) and mysql.releaseRole is not
// sent.
func TestCancelRunningRebuildStaysQuarantined(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER never_unassigned BEFORE UPDATE OF role ON servers
		WHEN OLD.id = %d AND NEW.role != 'quarantined' BEGIN SELECT RAISE(ABORT, 'role left quarantined'); END`, g.m.id)); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	defer open()
	g.m.before("mysql.preflight", func() { <-gate })
	mark := g.m.mark()
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("rebuild at its preflight", func() bool { return g.m.count("mysql.preflight") == 1 })
	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	open()
	e.waitJob(id, store.JobCanceled)
	e.waitIdle(id)
	if _, err := e.st.GetReplicaByServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after the cancel: %v", err)
	}
	if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
		t.Fatalf("role after the cancel = %s", got)
	}
	for _, c := range g.m.cmds(mark) {
		if c == "mysql.releaseRole" {
			t.Fatalf("cancel sent mysql.releaseRole: %v", g.m.cmds(mark))
		}
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

// TestDeleteQuarantinedWithFailedRow: an old master left by a failed
// rebuild is deleted with its row; job.cancel goes first for the agent job
// stored on its rebuild, and its failure does not stop the delete. The new
// master is deleted once the other replicas are detached.
func TestDeleteQuarantinedWithFailedRow(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	ctx := context.Background()
	id := failedRebuild(t, g, "mysql.configureReplication")
	e.setState(id, func(st *jobState) {
		st.AgentServerID, st.AgentJobID, st.AgentCmd = g.m.id, "agent-copy-7", "mysql.cloneFrom"
	})
	if err := e.r.DeleteServer(ctx, g.c.id); !errors.Is(err, ErrServerIsMaster) {
		t.Fatalf("delete of the new master while the failed row exists = %v, want ErrServerIsMaster", err)
	}
	// job.cancel runs, but its answer is lost.
	g.m.mu.Lock()
	g.m.dropReply = map[string]bool{"job.cancel": true}
	g.m.mu.Unlock()
	mark := g.m.mark()
	if err := e.r.DeleteServer(ctx, g.m.id); err != nil {
		t.Fatalf("delete of the old master with its failed row: %v", err)
	}
	wantCmds(t, "old master", g.m.cmds(mark), []string{"job.cancel"})
	var p proto.JobIDParams
	g.m.params("job.cancel", &p)
	if p.JobID != "agent-copy-7" {
		t.Fatalf("job.cancel for %q", p.JobID)
	}
	if _, err := e.st.GetServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old master after the delete: %v", err)
	}
	if _, err := e.st.GetReplicaByServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after the delete: %v", err)
	}
	for _, fa := range []*fakeAgent{g.b, g.d} {
		if err := e.r.DetachReplica(ctx, fa.id, false); err != nil {
			t.Fatal(err)
		}
	}
	g.c.reply("mysql.releaseRole", nil)
	if err := e.r.DeleteServer(ctx, g.c.id); err != nil {
		t.Fatalf("delete of the new master after: %v", err)
	}
}

// TestNewRebuildFromQuarantinedWithRow: a new rebuild of an old master
// left by a failed one first cancels the agent job stored on the failed
// one, then runs to the end; the failed job is superseded, and the role is
// quarantined until the settle.
func TestNewRebuildFromQuarantinedWithRow(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	first := failedRebuild(t, g, "mysql.preflight")
	e.setState(first, func(st *jobState) {
		st.AgentServerID, st.AgentJobID, st.AgentCmd = g.m.id, "agent-copy-8", "mysql.cloneFrom"
	})
	scriptReplicaTarget(g.m, "db-master", "clone")
	var mu sync.Mutex
	roles := map[string]string{}
	record := func(at string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			if _, seen := roles[at]; !seen {
				roles[at] = roleNow(e, g.m.id)
			}
		}
	}
	g.m.before("mysql.preflight", record("checks"))
	g.m.before("mysql.cloneFrom", record("copy"))
	g.m.before("mysql.configureReplication", record("connect"))
	mark := g.m.mark()
	second, err := g.startRebuild()
	if err != nil {
		t.Fatalf("new rebuild: %v", err)
	}
	e.waitJob(second, store.JobSucceeded)
	e.waitIdle(second)
	cmds := g.m.cmds(mark)
	cancelAt, probeAt := -1, -1
	for i, c := range cmds {
		if c == "job.cancel" && cancelAt < 0 {
			cancelAt = i
		}
		if c == "probe" && probeAt < 0 {
			probeAt = i
		}
	}
	if cancelAt < 0 || probeAt < 0 || cancelAt > probeAt {
		t.Fatalf("job.cancel not sent before the new rebuild's work: %v", cmds)
	}
	var p proto.JobIDParams
	g.m.params("job.cancel", &p)
	if p.JobID != "agent-copy-8" {
		t.Fatalf("job.cancel for %q", p.JobID)
	}
	if reason := e.r.Superseded(first); reason == "" {
		t.Fatal("the failed rebuild is not superseded")
	}
	mu.Lock()
	for _, at := range []string{"checks", "copy", "connect"} {
		if roles[at] != store.RoleQuarantined {
			t.Errorf("role at %s = %q, want quarantined", at, roles[at])
		}
	}
	mu.Unlock()
	if got := roleNow(e, g.m.id); got != store.RoleReplica {
		t.Fatalf("role at the end = %s", got)
	}
}

// TestLeftoverFromQuarantinedWithRow: Periksa and Amankan data tertinggal
// run on an old master left by a failed rebuild whose copy did not start or
// is uncertain, and are refused once it started (G-3).
func TestLeftoverFromQuarantinedWithRow(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		failAt string
		sent   bool
		want   CopyState
	}{
		{"copy not started", "mysql.preflight", false, CopyNotStarted},
		{"copy uncertain", "mysql.preflight", true, CopyUncertain},
		{"copy started", "mysql.configureReplication", false, CopyStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			id := failedRebuild(t, g, tc.failAt)
			if tc.sent {
				e.setState(id, func(st *jobState) { st.CopySent = true })
			}
			if s := copyStateOf(t, g); s != tc.want {
				t.Fatalf("copy state = %v, want %v", s, tc.want)
			}
			g.m.reply("mysql.leftover", proto.LeftoverResult{GTIDSet: masterUUID + ":121-123", Transactions: 3})
			g.m.reply("mysql.salvage", proto.SalvageFile{Name: "20261105T094500Z-1.sql", Size: 2048,
				CreatedAt: time.Now(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour)})
			check, err := e.r.StartLeftover(ctx, g.m.id)
			if tc.want == CopyStarted {
				if err == nil || err.Error() != CopyStartedText {
					t.Fatalf("Periksa = %v, want %q", err, CopyStartedText)
				}
				if _, err := e.r.StartSalvage(ctx, g.m.id); err == nil || err.Error() != CopyStartedText {
					t.Fatalf("Amankan = %v, want %q", err, CopyStartedText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Periksa: %v", err)
			}
			e.waitJob(check, store.JobSucceeded)
			e.waitIdle(check)
			save, err := e.r.StartSalvage(ctx, g.m.id)
			if err != nil {
				t.Fatalf("Amankan: %v", err)
			}
			e.waitJob(save, store.JobSucceeded)
			e.waitIdle(save)
			wantRebuildPending(t, g, "after Periksa and Amankan")
		})
	}
}

// TestLeftoverAfterCopyStarted (G-3): once a rebuild of the old master
// reached "Salin data", Periksa and Amankan data tertinggal are refused
// with one sentence (its binary logs were reset, databases dropped), also
// after a cancel; before the copy they run.
func TestLeftoverAfterCopyStarted(t *testing.T) {
	const refusal = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus " +
		"atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel."
	ctx := context.Background()
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
	if want := "Sambungkan replikasi: Pesan dari agent db-master (bukan dari panel): " + text; got != want {
		t.Fatalf("job error = %q, want %q", got, want)
	}
	if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || !strings.Contains(rep.LastError, text) {
		t.Fatalf("replica last error = %q, %v", rep.LastError, err)
	}
}

// legacyRebuild gives the old master the shape an earlier panel left for a
// rebuild job stored without the flag: role replica, its row in state,
// the marker kept, and the job id with status at step (the steps before
// it done).
func legacyRebuild(t *testing.T, g *promoteRig, id, state, status string, step int) {
	t.Helper()
	e := g.e
	if _, err := e.st.AddReplica(g.topo.ID, g.m.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(g.m.id, store.RoleReplica); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(g.m.id, state, ""); err != nil {
		t.Fatal(err)
	}
	params := fmt.Sprintf(`{"master_server_id":%d,"rebuild_old_master":true,"confirm_wipe":true,"method":"clone"}`, g.c.id)
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindCreateReplica, ServerID: g.m.id, TopologyID: g.topo.ID,
		ParamsJSON: params, CreatedAt: time.Now()}, createReplicaStepNames()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := range step {
		if err := e.st.SetStep(id, i, store.StepDone, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.UpdateJob(id, store.JobRunning, step, "", now); err != nil {
		t.Fatal(err)
	}
	if status != store.JobRunning {
		if err := e.st.UpdateJob(id, status, step, "", now); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNormalizeLegacyWaitingRebuild: a rebuild that was waiting for the
// user when the earlier panel stopped (role replica, row creating, marker,
// no flag) is normalised too: role quarantined, row and job as they were.
// It is then approved and runs; or Batalkan leaves it quarantined, with no
// mysql.releaseRole.
func TestNormalizeLegacyWaitingRebuild(t *testing.T) {
	const id = "99999999999999999999999999999993"
	start := func(t *testing.T) *promoteRig {
		t.Helper()
		g := rebuildRig(t)
		e := g.e
		legacyRebuild(t, g, id, store.ReplicaCreating, store.JobWaiting, 0)
		e.r.Resume(context.Background())
		if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
			t.Fatalf("role after Resume = %s, want quarantined", got)
		}
		if rep, err := e.st.GetReplicaByServer(g.m.id); err != nil || rep.State != store.ReplicaCreating {
			t.Fatalf("row after Resume = %+v, %v; want creating", rep, err)
		}
		if j, _, _ := e.st.GetJob(id); j.Status != store.JobWaiting {
			t.Fatalf("job after Resume = %+v, want waiting", j)
		}
		if _, err := e.st.GetTopologyByOldMaster(g.m.id); err != nil {
			t.Fatalf("marker after Resume: %v", err)
		}
		// An ordinary replica, not an old master, in any state: untouched.
		if got := roleNow(e, g.d.id); got != store.RoleReplica {
			t.Fatalf("ordinary replica: role = %s", got)
		}
		if rep, err := e.st.GetReplicaByServer(g.d.id); err != nil || rep.State != store.ReplicaReady {
			t.Fatalf("ordinary replica: row = %+v, %v", rep, err)
		}
		// A second run changes nothing.
		if err := e.r.normalizeFailedRebuilds(); err != nil {
			t.Fatal(err)
		}
		if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
			t.Fatalf("role after a second run = %s", got)
		}
		return g
	}
	t.Run("approve", func(t *testing.T) {
		g := start(t)
		e := g.e
		if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		e.waitIdle(id)
		if got := roleNow(e, g.m.id); got != store.RoleReplica {
			t.Fatalf("role at the end = %s", got)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		g := start(t)
		e := g.e
		mark := g.m.mark()
		if err := e.r.Cancel(id); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		e.waitJob(id, store.JobCanceled)
		e.waitIdle(id)
		if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
			t.Fatalf("role after the cancel = %s, want quarantined", got)
		}
		for _, c := range g.m.cmds(mark) {
			if c == "mysql.releaseRole" {
				t.Fatalf("cancel sent mysql.releaseRole: %v", g.m.cmds(mark))
			}
		}
	})
}

// TestLegacyRebuildRetriedAfterConnectSettlesFirst: a rebuild job without
// the flag that failed at its verify step (connect recorded as done) runs
// the connect step again on Retry: mysql.configureReplication is sent
// again and, on the agent's OK, the settle stores the flag, the role
// replica and the end of the marker before verify runs. When the agent
// refuses, the job fails and the old master stays quarantined.
func TestLegacyRebuildRetriedAfterConnectSettlesFirst(t *testing.T) {
	const id = "99999999999999999999999999999994"
	verify := len(createReplicaStepNames()) - 1
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent refuses %v", refused), func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			legacyRebuild(t, g, id, store.ReplicaFailed, store.JobFailed, verify)
			e.r.Resume(context.Background())
			wantRebuildPending(t, g, "after Resume")
			var mu sync.Mutex
			var verifyRole string
			g.m.before("mysql.waitSynced", func() {
				mu.Lock()
				defer mu.Unlock()
				if verifyRole == "" {
					verifyRole = roleNow(e, g.m.id)
				}
			})
			if refused {
				g.m.fail("mysql.configureReplication", "ditolak")
			}
			mark := g.m.mark()
			if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
				t.Fatalf("Retry: %v", err)
			}
			if refused {
				e.waitJob(id, store.JobFailed)
				e.waitIdle(id)
				wantRebuildPending(t, g, "after the refused connect")
				if jobStateOf(t, e, id).QuarantineEnded {
					t.Fatal("flag stored although the agent refused")
				}
				if n := g.m.count("mysql.waitSynced"); n != 0 {
					t.Fatalf("verify ran after the refused connect (%d waitSynced)", n)
				}
				return
			}
			e.waitJob(id, store.JobSucceeded)
			e.waitIdle(id)
			if !slices.Contains(g.m.cmds(mark), "mysql.configureReplication") {
				t.Fatalf("connect not run again: %v", g.m.cmds(mark))
			}
			mu.Lock()
			if verifyRole != store.RoleReplica {
				t.Errorf("role when verify ran = %q, want replica (settled before)", verifyRole)
			}
			mu.Unlock()
			if !jobStateOf(t, e, id).QuarantineEnded {
				t.Fatal("flag not stored")
			}
			if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("marker after the settle: %v", err)
			}
			if got := roleNow(e, g.m.id); got != store.RoleReplica {
				t.Fatalf("role at the end = %s", got)
			}
		})
	}
}

// TestRebuildVerifyWritesNoRole: the verify step of a rebuild writes no
// role and clears no marker: with a settle that stores nothing (the seam
// answers OK), the job runs to the end and the old master is still
// quarantined with its marker.
func TestRebuildVerifyWritesNoRole(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	e.r.settleStore = func(string, string, int64) error { return nil }
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
		t.Fatalf("role after verify = %s, want quarantined (only the settle writes it)", got)
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); err != nil {
		t.Fatalf("marker cleared outside the settle: %v", err)
	}
}
