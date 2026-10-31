package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// scriptAdminReplaced makes the target's CLONE replace its accounts: the
// clone answers AdminReplaced, and mysql.status is refused until the user
// reconnects (reconnected), then reports the clone's state.
func scriptAdminReplaced(r *fakeAgent, reconnected *atomic.Bool, cloneState string) {
	r.job("mysql.cloneFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "clone", Bytes: 300, AdminReplaced: true}, nil
	})
	r.on("mysql.status", func(json.RawMessage) (any, error) {
		if r.count("mysql.cloneFrom") > 0 && !reconnected.Load() {
			return nil, &commands.ParamError{Msg: "login MySQL gagal: 1045 Access denied for user 'dbpanel_admin'@'localhost' (using password: YES)"}
		}
		return proto.MySQLStatus{Version: "8.4.3", ServerUUID: targetUUID, GTIDExecuted: masterGTID, CloneState: cloneState}, nil
	})
}

// TestCloneAdminReplacedWaitsInSnapshot: the copy brought the master's
// accounts; the snapshot step waits for the user to reconnect (with a
// message naming the master and the replica's page), a re-check before the
// reconnect keeps waiting, and after it the copy is verified and the job
// goes on to replication without copying again.
func TestCloneAdminReplacedWaitsInSnapshot(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	var reconnected atomic.Bool
	scriptAdminReplaced(r, &reconnected, "Completed")
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 4 || !strings.Contains(j.ParamsJSON, `"wait_reason":"reconnect"`) {
		t.Fatalf("job = %+v", j)
	}
	for _, want := range []string{"akun-akun MySQL dari master db-master", "Buka halaman server replica1 dan klik Hubungkan MySQL",
		"akun admin MySQL milik master db-master", "Data tidak disalin ulang"} {
		if !strings.Contains(steps[4].Message, want) {
			t.Errorf("waiting message lacks %q:\n%s", want, steps[4].Message)
		}
	}

	// "Lanjutkan" before reconnecting: it still waits.
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("re-check", func() bool { return r.count("mysql.status") >= 2 })
	if j, _ := e.waitJob(id, store.JobWaiting); j.CurrentStep != 4 {
		t.Fatalf("job = %+v", j)
	}

	reconnected.Store(true)
	mark := r.mark()
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	if n := r.count("mysql.cloneFrom"); n != 1 {
		t.Fatalf("cloneFrom ran %d times", n)
	}
	// Verify the copy, read-only again, the inventory, then step 5 on.
	wantCmds(t, "replica after reconnect", r.cmds(mark), []string{"mysql.status", "mysql.setReadOnly", "mysql.inventory",
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaReady {
		t.Fatalf("replica = %+v", rep)
	}
}

// TestCloneAdminReplacedIncompleteFails: after the reconnect a clone that
// did not complete fails the step instead of replicating from it.
func TestCloneAdminReplacedIncompleteFails(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	var reconnected atomic.Bool
	scriptAdminReplaced(r, &reconnected, "Failed")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobWaiting)
	reconnected.Store(true)
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 4 || !strings.Contains(j.Error, "CLONE tidak selesai") {
		t.Fatalf("job = %+v", j)
	}
	if r.count("mysql.configureReplication") != 0 {
		t.Fatal("replication configured from an incomplete clone")
	}
}

// TestSnapshotRetryWaitsForEarlierDump: a retried dump step whose earlier
// dump still runs on the agent waits for it instead of dropping the
// leftover databases under it and starting another.
func TestSnapshotRetryWaitsForEarlierDump(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	scriptLeftovers(r, "dump", "a")
	release := make(chan struct{})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		<-release
		return proto.SnapshotResult{Method: "dump", Bytes: 100, GTIDExecuted: masterGTID}, nil
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobWaiting)
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	r.detach() // the agent's link drops; its dump goes on
	if j, _ := e.waitJob(id, store.JobFailed); j.CurrentStep != 4 {
		t.Fatalf("job = %+v", j)
	}
	r.attach()
	if err := e.r.Retry(id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("retry polls the earlier dump", func() bool { return r.count("job.status") >= 2 })
	if n := r.count("mysql.dropDatabases"); n != 1 {
		t.Fatalf("dropDatabases ran %d times; the retry dropped under the running dump", n)
	}
	close(release)
	e.waitJob(id, store.JobSucceeded)
	if n := r.startedJobs(); n != 1 {
		t.Fatalf("%d dumps started; the retry did not wait for the earlier one", n)
	}
	if c, d := m.count("mysql.createDumpUser"), m.count("mysql.dropUser"); c != 1 || d != 1 {
		t.Fatalf("dump accounts created %d, dropped %d", c, d)
	}
}

// TestCancelDropsDumpAccount: canceling a dump drops the temporary account
// on the master too.
func TestCancelDropsDumpAccount(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.blockingJob("mysql.dumpFrom")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobCanceled)
	e.waitFor("dump account dropped", func() bool { return m.count("mysql.dropUser") == 1 })
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	wantDumpAccount(t, m, snap, "10.0.0.2")
	if j, _, _ := e.st.GetJob(id); strings.Contains(j.ParamsJSON, "dump_user") {
		t.Fatalf("params still name the dropped account: %s", j.ParamsJSON)
	}
}

// TestStartCreateReplicaRefusals: a target whose MySQL is installed but not
// connected, and a master still being prepared, are refused up front.
func TestStartCreateReplicaRefusalsBeforeAnyJob(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	if err := e.st.UpdateProbe(r.id, proto.ProbeResult{MySQL: proto.MySQLInfo{Installed: true, Version: "8.4.3"}}, "t", e.r.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id}); err != ErrTargetNotConnected {
		t.Fatalf("unconnected target: err = %v", err)
	}
	if err := e.st.SetInventory(store.Inventory{ServerID: r.id, Connected: true, UpdatedAt: e.r.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: "prep", Kind: KindPrepareMaster, ServerID: m.id, CreatedAt: e.r.now()}, prepareMasterStepNames()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob("prep", store.JobWaiting, 2, "", e.r.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id}); err == nil ||
		!strings.Contains(err.Error(), "Siapkan master") {
		t.Fatalf("master being prepared: err = %v", err)
	}
	if jobs, _ := e.st.ListJobs(0); len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
}

// TestForeignSourceNamedBeforeReset: a target replicating from another
// server is not reset silently: the check step waits with the source named
// and goes on once approved.
func TestForeignSourceNamedBeforeReset(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	foreign := &proto.ReplicaStatus{SourceHost: "10.9.9.9", SourcePort: 3306}
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckWarn, Method: "dump",
		Status: proto.MySQLStatus{Replica: foreign},
		Checks: []proto.Check{{ID: "existing_replication", Status: proto.CheckWarn, Title: "Replikasi yang sudah ada"}}})
	r.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", ServerUUID: targetUUID, Replica: foreign})
	r.reply("mysql.resetReplica", nil)
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 1 || !strings.Contains(steps[1].Message, "replika dari 10.9.9.9:3306") ||
		!strings.Contains(steps[1].Message, "akan dihapus") || !strings.Contains(j.ParamsJSON, `"wait_reason":"foreign_source"`) {
		t.Fatalf("job %+v step %+v", j, steps[1])
	}
	if r.count("mysql.resetReplica") != 0 {
		t.Fatal("reset before the approval")
	}
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	if r.count("mysql.resetReplica") != 1 || !strings.Contains(e.jobLog(id), "pengaturan replikasi dari 10.9.9.9:3306") {
		t.Fatalf("reset %d times; log:\n%s", r.count("mysql.resetReplica"), e.jobLog(id))
	}
}
