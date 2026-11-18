package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// failedJob stores a failed job of kind on serverID at step with params.
func (e *env) failedJob(id, kind string, serverID, topologyID int64, params string, steps []string, step int) {
	e.t.Helper()
	if err := e.st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: serverID, TopologyID: topologyID, ParamsJSON: params,
		CreatedAt: e.r.now()}, steps); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, step, "gagal", e.r.now()); err != nil {
		e.t.Fatal(err)
	}
}

// wantRefused checks that Retry refuses the job naming reason and starts
// nothing, and that Superseded gives the same reason for the job page.
func (e *env) wantRefused(fa *fakeAgent, id, reason string) {
	e.t.Helper()
	mark := fa.mark()
	err := e.r.Retry(id, JobAck{})
	if err == nil || !strings.Contains(err.Error(), "tidak berlaku") || !strings.Contains(err.Error(), reason) {
		e.t.Fatalf("Retry(%s) = %v, want refused for %q", id, err, reason)
	}
	if got := e.r.Superseded(id); !strings.Contains(got, reason) {
		e.t.Fatalf("Superseded(%s) = %q, want %q", id, got, reason)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		e.t.Fatalf("job %s reopened: %+v", id, j)
	}
	if cmds := fa.cmds(mark); len(cmds) != 0 {
		e.t.Fatalf("agent called after a refused retry: %v", cmds)
	}
}

// TestRetryRefusesSupersededCreateReplica (SEC-31): an old failed attempt
// is not retried over what replaced it.
func TestRetryRefusesSupersededCreateReplica(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")

	// The failed attempt's row was replaced by a newer attempt that made
	// the target a ready replica.
	if _, err := e.st.AddReplica(topo.ID, r.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	e.failedJob("old-attempt", KindCreateReplica, r.id, topo.ID, `{"master_server_id":1,"databases":["a"]}`, createReplicaStepNames(), 4)
	if got := e.r.Superseded("old-attempt"); got != "" {
		t.Fatalf("a current failed attempt is superseded: %q", got)
	}
	e.failedJob("new-attempt", KindCreateReplica, r.id, topo.ID, `{"master_server_id":1}`, createReplicaStepNames(), 1)
	if err := e.st.SetReplicaState(r.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	e.wantRefused(r, "old-attempt", "pekerjaan yang lebih baru")
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaReady {
		t.Fatalf("the healthy replica changed: %+v", rep)
	}
	// The newest attempt itself: its replica is ready now.
	e.wantRefused(r, "new-attempt", "sudah siap")

	// The row is gone and the server was made a master meanwhile.
	if err := e.st.RemoveReplica(r.id); err != nil {
		t.Fatal(err)
	}
	e.wantRefused(r, "new-attempt", "tidak lagi terdaftar sebagai replika")

	// A replica of another master.
	_, otherTopo := e.readyMaster("db-master-2")
	if _, err := e.st.AddReplica(otherTopo.ID, r.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(r.id, store.ReplicaFailed, "x"); err != nil {
		t.Fatal(err)
	}
	e.wantRefused(r, "new-attempt", "master lain")
}

// TestRetryRefusesStaleDatabaseChange: a failed add job is not retried once
// the replica follows other databases than when the job was made.
func TestRetryRefusesStaleDatabaseChange(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	e.failedJob("add-b", KindAddDatabases, r.id, topo.ID, `{"master_server_id":1,"databases":["a"],"change_databases":["b"]}`,
		addDatabasesStepNames(), 4)
	if got := e.r.Superseded("add-b"); got != "" {
		t.Fatalf("a current change job is superseded: %q", got)
	}
	if err := e.st.SetReplicaDatabases(r.id, []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	e.wantRefused(r, "add-b", "database yang diikuti replika sudah berubah")
}

// TestApproveRefusesSupersededPrepareMaster: a waiting prepare_master of a
// server that became a replica meanwhile is not approved (no restart).
func TestApproveRefusesSupersededPrepareMaster(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-x")
	if err := e.st.CreateJob(store.Job{ID: "prep", Kind: KindPrepareMaster, ServerID: fa.id, ParamsJSON: `{"needs_restart":true}`,
		CreatedAt: e.r.now()}, prepareMasterStepNames()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob("prep", store.JobWaiting, 2, "", e.r.now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(fa.id, store.RoleReplica); err != nil {
		t.Fatal(err)
	}
	err := e.r.Approve("prep", JobAck{})
	if err == nil || !strings.Contains(err.Error(), "sekarang menjadi replika") {
		t.Fatalf("Approve = %v", err)
	}
	if n := fa.count("mysql.restart"); n != 0 {
		t.Fatalf("restart called %d times", n)
	}
}

// TestRetryReopensBeforeNewJobs: Retry reopens a failed job before it
// returns, so a new job for the same server is refused at once, even while
// the job's goroutine has not run yet; only one job ever runs there.
func TestRetryReopensBeforeNewJobs(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	var roFails atomic.Bool
	roFails.Store(true)
	// While hold is set, setReadOnly (the retried step's command) waits
	// for gate, so the retried job cannot fail again before the checks.
	var hold atomic.Bool
	gate := make(chan struct{})
	var openGate sync.Once
	release := func() { openGate.Do(func() { close(gate) }) }
	defer release()
	r.on("mysql.setReadOnly", func(json.RawMessage) (any, error) {
		if hold.Load() {
			<-gate
		}
		if roFails.Load() {
			return nil, &commands.ParamError{Msg: "read_only gagal"}
		}
		return nil, nil
	})
	newJob := CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}}
	id, err := e.r.StartCreateReplica(context.Background(), newJob)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	gone := func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return e.r.live[id] == nil
	}
	wantRefusedNew := func() {
		t.Helper()
		if a, busy, err := e.st.ActiveJobForServer(r.id); err != nil || !busy || a.ID != id {
			t.Fatalf("ActiveJobForServer after Retry = %+v, %v, %v", a, busy, err)
		}
		if _, err := e.r.StartCreateReplica(context.Background(), newJob); err == nil || !strings.Contains(err.Error(), "belum selesai") {
			t.Fatalf("StartCreateReplica right after Retry = %v, want refused", err)
		}
	}

	// Real scheduling: a Start right after Retry returns is refused.
	e.waitFor("job goroutine gone", gone)
	hold.Store(true)
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	wantRefusedNew()
	release()
	e.waitFor("retried step failed again", func() bool { return r.count("mysql.setReadOnly") == 2 })
	e.waitJob(id, store.JobFailed)

	// The goroutine held back: the job is a live one still finishing, so
	// Retry only queues the action; the store already shows it running.
	e.waitFor("job goroutine gone", gone)
	ctx, cancel := context.WithCancel(e.r.base)
	held := &liveJob{id: id, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	e.r.mu.Lock()
	e.r.live[id] = held
	e.r.mu.Unlock()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	wantRefusedNew()

	// Let the goroutine take the retry: it runs the reopened job.
	roFails.Store(false)
	e.r.mu.Lock()
	act := *held.pending
	held.pending = nil
	e.r.wg.Add(1)
	e.r.mu.Unlock()
	go e.r.loop(held, act)
	e.waitJob(id, store.JobSucceeded)
	if n := r.count("mysql.setReadOnly"); n != 3 {
		t.Fatalf("setReadOnly ran %d times", n)
	}
	jobs, err := e.st.ListJobs(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ServerID == r.id && j.ID != id {
			t.Fatalf("a second job was created for the server: %+v", j)
		}
	}
}

// TestCancelBetweenReopenAndLaunchIsCanceled (M2): a Cancel landing in
// Retry between ReopenJob and launch ends the job canceled, with a normal
// cancel's cleanup (the replica row goes, the target's role is released),
// and the retried step never runs; also when the job's old goroutine is
// still finishing, so Retry's launch finds the Cancel queued.
func TestCancelBetweenReopenAndLaunchIsCanceled(t *testing.T) {
	for _, finishing := range []bool{false, true} {
		t.Run(fmt.Sprintf("goroutine finishing %v", finishing), func(t *testing.T) {
			e := newEnv(t)
			m, _ := e.readyMaster("db-master")
			r := e.replicaAgent("replica1", "dump")
			r.fail("mysql.setReadOnly", "read_only gagal")
			id, err := e.r.StartCreateReplica(context.Background(),
				CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
			if err != nil {
				t.Fatal(err)
			}
			e.waitJob(id, store.JobFailed)
			e.waitFor("job goroutine gone", func() bool {
				e.r.mu.Lock()
				defer e.r.mu.Unlock()
				return e.r.live[id] == nil
			})
			var held *liveJob
			if finishing {
				ctx, cancel := context.WithCancel(e.r.base)
				held = &liveJob{id: id, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
				e.r.mu.Lock()
				e.r.live[id] = held
				e.r.mu.Unlock()
			}
			readOnly := r.count("mysql.setReadOnly")

			var cancelErr error
			e.r.afterReopen = func(string) { cancelErr = e.r.Cancel(id) }
			retryErr := e.r.Retry(id, JobAck{Confirmed: true})
			e.r.afterReopen = nil
			if cancelErr != nil {
				t.Fatalf("Cancel = %v", cancelErr)
			}
			t.Logf("Retry = %v", retryErr)
			if finishing {
				// The old goroutine finishes and takes the queued action.
				e.r.mu.Lock()
				if held.pending == nil {
					e.r.mu.Unlock()
					t.Fatal("no action queued on the finishing goroutine")
				}
				act := *held.pending
				held.pending = nil
				e.r.wg.Add(1)
				e.r.mu.Unlock()
				go e.r.loop(held, act)
			}

			j, _ := e.waitJob(id, store.JobCanceled)
			if j.Error != "dibatalkan oleh pengguna" {
				t.Fatalf("job error = %q", j.Error)
			}
			if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("replica row after cancel: %v", err)
			}
			e.waitFor("role released", func() bool { return r.count("mysql.releaseRole") > 0 })
			if n := r.count("mysql.setReadOnly"); n != readOnly {
				t.Fatalf("the retried step ran: setReadOnly %d -> %d", readOnly, n)
			}
		})
	}
}

// TestRetryRefusedAfterMasterSwapped (SP-4): a job made before a
// promotion keeps the old master's id; once the topology's master has
// changed, its Retry would check wipe_only against the old master and
// copy from the new one: it is superseded instead.
func TestRetryRefusedAfterMasterSwapped(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}}, "a")
	if err := e.st.SetReplicaState(r.id, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	const id = "ffffffffffffffffffffffffffffff01"
	b, _ := json.Marshal(jobState{MasterServerID: topo.MasterServerID, Databases: []string{"a"}, ConfirmWipe: true,
		Resync: true, WipeList: []string{"a"}, ConfirmedAt: time.Now()})
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindResyncReplica, ServerID: r.id, TopologyID: topo.ID,
		ParamsJSON: string(b), CreatedAt: time.Now()}, resyncSteps()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, 0, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := e.r.Superseded(id); got != "" {
		t.Fatalf("Superseded before the swap = %q", got)
	}
	other := e.replicaAgent("db-baru", "dump")
	if err := e.st.SwapMaster(topo.ID, topo.MasterServerID, other.id, "10.0.0.9", 3306); err != nil {
		t.Fatal(err)
	}
	mark := r.mark()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err == nil {
		t.Fatal("Retry after the master changed was accepted")
	}
	if got := e.r.Superseded(id); !strings.Contains(got, "master") {
		t.Fatalf("Superseded after the swap = %q", got)
	}
	wantCmds(t, "replica", r.cmds(mark), nil)
}

// TestSupersededRequiresQuarantinedWhileUnsettled: a rebuild job whose
// agent has not ended the quarantine needs the role quarantined (another
// role means "peran server ini sudah berubah"); once the flag is set, or
// for an ordinary create_replica job, the role must be replica.
func TestSupersededRequiresQuarantinedWhileUnsettled(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	stored := func(name, role, params string) string {
		t.Helper()
		fa := e.agent(name, "10.0.0.9")
		if _, err := e.st.AddReplica(topo.ID, fa.id, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetReplicaState(fa.id, store.ReplicaFailed, "gagal"); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetServerRole(fa.id, role); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%032x", fa.id)
		e.failedJob(id, KindCreateReplica, fa.id, topo.ID, fmt.Sprintf(params, m.id), createReplicaStepNames(), 1)
		return id
	}
	const changed = "peran server ini sudah berubah"
	unsettled := stored("q-unsettled", store.RoleReplica, `{"master_server_id":%d,"rebuild_old_master":true}`)
	if got := e.r.Superseded(unsettled); got != changed {
		t.Fatalf("unsettled rebuild with role replica: Superseded = %q, want %q", got, changed)
	}
	settled := stored("q-settled", store.RoleReplica, `{"master_server_id":%d,"rebuild_old_master":true,"quarantine_ended":true}`)
	if got := e.r.Superseded(settled); got != "" {
		t.Fatalf("settled rebuild with role replica: Superseded = %q, want accepted", got)
	}
	ordinary := stored("q-ordinary", store.RoleQuarantined, `{"master_server_id":%d}`)
	if got := e.r.Superseded(ordinary); got != changed {
		t.Fatalf("ordinary create_replica with role quarantined: Superseded = %q, want %q", got, changed)
	}
}

// TestRetryStillRefusedWhenSuperseded: a failed rebuild of the old master
// is still refused when a newer rebuild or a certificate job exists, or
// the topology's master changed; the role does not change.
func TestRetryStillRefusedWhenSuperseded(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		after        func(t *testing.T, g *promoteRig)
	}{
		{"newer rebuild", "lebih baru", func(t *testing.T, g *promoteRig) {
			failedRebuild(t, g, "mysql.preflight")
		}},
		{"certificate job", "lebih baru", func(t *testing.T, g *promoteRig) {
			g.e.failedJob("4444444444444444444444444444444e", KindMySQLTLS, g.m.id, 0, "{}", mysqlTLSStepNames(), 0)
		}},
		{"master changed", "master topologi ini sudah berganti", func(t *testing.T, g *promoteRig) {
			if err := g.e.st.SwapMaster(g.topo.ID, g.c.id, g.b.id, "10.0.0.3", 3306); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			id := failedRebuild(t, g, "mysql.preflight")
			tc.after(t, g)
			if got := e.r.Superseded(id); !strings.Contains(got, tc.reason) {
				t.Fatalf("Superseded = %q, want %q", got, tc.reason)
			}
			err := e.r.Retry(id, JobAck{Confirmed: true})
			if err == nil || !strings.Contains(err.Error(), "tidak berlaku") {
				t.Fatalf("Retry = %v, want refused", err)
			}
			if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
				t.Fatalf("job after the refused retry = %+v", j)
			}
			if got := roleNow(e, g.m.id); got != store.RoleQuarantined {
				t.Fatalf("role after the refused retry = %s", got)
			}
		})
	}
}

// TestLeftoverDoesNotSupersedeFailedRebuild: after Periksa and Amankan data
// tertinggal on an old master left by a failed rebuild, the rebuild is
// still retried; a Periksa job does supersede a create_replica job that is
// not a rebuild, or one whose agent ended the quarantine.
func TestLeftoverDoesNotSupersedeFailedRebuild(t *testing.T) {
	ctx := context.Background()
	leftover := func(t *testing.T, g *promoteRig, salvage bool) {
		t.Helper()
		e := g.e
		g.m.reply("mysql.leftover", proto.LeftoverResult{GTIDSet: masterUUID + ":121-123", Transactions: 3})
		id, err := e.r.StartLeftover(ctx, g.m.id)
		if err != nil {
			t.Fatalf("Periksa: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		e.waitIdle(id)
		if !salvage {
			return
		}
		g.m.reply("mysql.salvage", proto.SalvageFile{Name: "20261105T094500Z-1.sql", Size: 2048,
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour)})
		id, err = e.r.StartSalvage(ctx, g.m.id)
		if err != nil {
			t.Fatalf("Amankan: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		e.waitIdle(id)
	}
	t.Run("failed rebuild", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		id := failedRebuild(t, g, "mysql.preflight")
		leftover(t, g, true)
		if got := e.r.Superseded(id); got != "" {
			t.Fatalf("Superseded after Periksa and Amankan = %q", got)
		}
		scriptReplicaTarget(g.m, "db-master", "clone")
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		e.waitIdle(id)
	})
	for name, params := range map[string]string{
		"not a rebuild":    `{"master_server_id":%d}`,
		"quarantine ended": `{"master_server_id":%d,"rebuild_old_master":true,"quarantine_ended":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			if _, err := e.st.AddReplica(g.topo.ID, g.m.id, nil); err != nil {
				t.Fatal(err)
			}
			if err := e.st.SetReplicaState(g.m.id, store.ReplicaFailed, "gagal"); err != nil {
				t.Fatal(err)
			}
			const id = "4444444444444444444444444444444f"
			e.failedJob(id, KindCreateReplica, g.m.id, g.topo.ID, fmt.Sprintf(params, g.c.id), createReplicaStepNames(), 1)
			leftover(t, g, false)
			if got := e.r.Superseded(id); !strings.Contains(got, "lebih baru") {
				t.Fatalf("Superseded = %q, want superseded by the newer Periksa job", got)
			}
		})
	}
}
