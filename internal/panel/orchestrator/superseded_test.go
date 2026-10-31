package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
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
	err := e.r.Retry(id)
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
	err := e.r.Approve("prep")
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
	if err := e.r.Retry(id); err != nil {
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
	if err := e.r.Retry(id); err != nil {
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
