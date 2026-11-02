package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// resyncReplica is a ready replica of topo following dbs (none = all) that
// replicates from the master and holds onTarget (with their sizes in its
// inventory). Its preflight answers as the agent does on a resync: the
// databases on WipeList are confirmed, any other one the master has is a
// leftover to confirm, and CLONE is offered for all databases when
// nothing else is left.
func (e *env) resyncReplica(topo store.Topology, onTarget []proto.DatabaseInfo, dbs ...string) *fakeAgent {
	e.t.Helper()
	r := e.followingReplica(topo, dbs...)
	if err := e.st.SetInventory(store.Inventory{ServerID: r.id, Connected: true, Version: "8.4.3", Series: "8.4",
		Databases: onTarget, UpdatedAt: time.Now()}); err != nil {
		e.t.Fatal(err)
	}
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: onTarget})
	r.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Series: "8.4", ServerID: 77, ServerUUID: targetUUID,
		Replica: &proto.ReplicaStatus{SourceHost: "10.0.0.1", SourcePort: 3306, IORunning: "Yes", SQLRunning: "Yes"}})
	r.reply("mysql.resetReplica", nil)
	r.on("mysql.configure", func(raw json.RawMessage) (any, error) {
		var p proto.ConfigureParams
		json.Unmarshal(raw, &p)
		id := p.ServerID
		if id == 0 {
			id = 77 // kept
		}
		return proto.ConfigureResult{ServerID: id}, nil
	})
	r.on("mysql.preflight", func(raw json.RawMessage) (any, error) {
		var p proto.PreflightParams
		json.Unmarshal(raw, &p)
		var left []string
		for _, d := range onTarget {
			if !slices.Contains(p.WipeList, d.Name) {
				left = append(left, d.Name)
			}
		}
		method := "dump"
		if p.Resync && len(p.Databases) == 0 && len(left) == 0 {
			method = "clone"
		}
		if len(left) > 0 && !p.ConfirmWipe {
			return proto.PreflightResult{Worst: proto.CheckNeedsConfirm, Method: method, Checks: []proto.Check{
				{ID: "leftover", Status: proto.CheckNeedsConfirm, Title: "Database bernama sama dengan di master"}}}, nil
		}
		return proto.PreflightResult{Worst: proto.CheckOK, Method: method}, nil
	})
	return r
}

func resyncSteps() []string {
	return []string{"Periksa master", "Periksa server tujuan", "Siapkan replika", "Salin data", "Sambungkan replikasi", "Verifikasi"}
}

func TestResyncHappyPathOneConfirmation(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}, {Name: "x", Bytes: 50}}, "a")
	if err := e.st.SetReplicaPaused(r.id, true); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		<-release
		return proto.SnapshotResult{Method: "dump", Bytes: 100, GTIDExecuted: masterGTID}, nil
	})

	id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"x", "a"}})
	if err != nil {
		t.Fatalf("StartResync: %v", err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	if rep := e.replica(r.id); rep.State != store.ReplicaResyncing {
		t.Fatalf("replica state during resync = %q", rep.State)
	}
	close(release)
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindResyncReplica {
		t.Fatalf("job kind = %q", j.Kind)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, resyncSteps()) {
		t.Fatalf("steps = %v", names)
	}
	want := slices.Repeat([]string{store.StepDone}, 6)
	if got := stepStatuses(steps); !slices.Equal(got, want) {
		t.Fatalf("step statuses = %v (no step may wait)", got)
	}

	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if !pre.Resync || !slices.Equal(pre.WipeList, []string{"a", "x"}) || pre.ReclaimBytes != 150 ||
		!slices.Equal(pre.Databases, []string{"a"}) || pre.ConfirmWipe {
		t.Fatalf("preflight params = %+v", pre)
	}
	if n := len(r.allParams("mysql.preflight")); n != 1 {
		t.Fatalf("preflight called %d times, want 1", n)
	}
	// The replica keeps its own server_id: no new one, no restart.
	var conf proto.ConfigureParams
	r.params("mysql.configure", &conf)
	if conf.ServerID != 0 || conf.Role != "replica" {
		t.Fatalf("configure params = %+v", conf)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"probe", "mysql.preflight", "mysql.configure", "mysql.setReadOnly",
		"mysql.status", "mysql.resetReplica", "mysql.dropDatabases", "mysql.dumpFrom",
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	var dr proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &dr)
	if !slices.Equal(dr.Databases, []string{"a", "x"}) {
		t.Fatalf("dropDatabases = %v", dr.Databases)
	}
	// Leftover dump accounts are swept before the dump's own is created.
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.sweepDumpUsers", "mysql.createDumpUser",
		"mysql.dropUser", "mysql.status"})
	var sw proto.SweepDumpUsersParams
	m.params("mysql.sweepDumpUsers", &sw)
	if sw.OlderThanSec != 86400 {
		t.Fatalf("sweep older_than_sec = %d", sw.OlderThanSec)
	}

	rep := e.replica(r.id)
	if rep.State != store.ReplicaReady || rep.Paused || rep.LastError != "" || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica after resync = %+v", rep)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleReplica {
		t.Fatalf("role = %q", srv.Role)
	}
	if _, ended := e.alerts.lists(); !slices.Contains(ended, fmt.Sprintf("%d %s", r.id, id)) {
		t.Fatalf("JobEnded not reported: %v", ended)
	}
}

func TestResyncStopsForUnknownLeftover(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	// b showed up on the replica after the modal listed what is dropped.
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}, {Name: "b", Bytes: 200}}, "a")

	id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 1 || !strings.Contains(j.ParamsJSON, `"wait_reason":"wipe"`) {
		t.Fatalf("job = %+v", j)
	}
	if msg := steps[1].Message; !strings.Contains(msg, "b") || strings.Contains(msg, "a,") || strings.Contains(msg, ", a") {
		t.Fatalf("waiting message = %q, want only b", msg)
	}
	if n := r.count("mysql.dropDatabases"); n != 0 {
		t.Fatal("databases dropped before approval")
	}
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	var dr proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &dr)
	if !slices.Equal(dr.Databases, []string{"a", "b"}) {
		t.Fatalf("dropDatabases = %v", dr.Databases)
	}
	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if !pre.Resync || !pre.ConfirmWipe || !slices.Equal(pre.WipeList, []string{"a", "b"}) {
		t.Fatalf("preflight after approval = %+v", pre)
	}
}

func TestResyncUsesCloneForAllDatabases(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}, {Name: "b", Bytes: 200}})

	id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, slices.Repeat([]string{store.StepDone}, 6)) {
		t.Fatalf("step statuses = %v", got)
	}
	if r.count("mysql.cloneFrom") != 1 || r.count("mysql.dumpFrom") != 0 || r.count("mysql.dropDatabases") != 0 {
		t.Fatalf("replica commands = %v", r.cmds(0))
	}
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.status"})
	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if !pre.Resync || len(pre.Databases) != 0 || pre.ReclaimBytes != 300 {
		t.Fatalf("preflight params = %+v", pre)
	}

	// ForceDump still dumps.
	id, err = e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"a", "b"}, ForceDump: true})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	if r.count("mysql.dumpFrom") != 1 || r.count("mysql.cloneFrom") != 1 {
		t.Fatalf("replica commands = %v", r.cmds(0))
	}
}

func TestResyncRefusedWhileBusy(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}}, "a")
	r.blockingJob("mysql.dumpFrom")
	ctx := context.Background()

	if _, err := e.r.StartResync(ctx, ResyncParams{ServerID: m.id}); err == nil || !strings.Contains(err.Error(), "bukan replika") {
		t.Fatalf("StartResync(master) = %v", err)
	}
	id, err := e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	if _, err := e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}}); err == nil {
		t.Fatal("second StartResync succeeded")
	}
	// No control runs while the job does.
	mark := r.mark()
	for name, fn := range map[string]func() error{
		"StopReplica":   func() error { return e.r.StopReplica(ctx, r.id) },
		"StartReplica":  func() error { return e.r.StartReplica(ctx, r.id) },
		"Relock":        func() error { return e.r.Relock(ctx, r.id) },
		"DetachReplica": func() error { return e.r.DetachReplica(ctx, r.id, true) },
	} {
		if err := fn(); err == nil {
			t.Errorf("%s during a resync succeeded", name)
		}
	}
	if got := r.cmds(mark); len(got) != 0 {
		t.Fatalf("commands sent during a resync: %v", got)
	}

	// Canceled: the server stays a replica, failed, and can be resynced
	// again.
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobCanceled)
	rep := e.replica(r.id)
	if rep.State != store.ReplicaFailed {
		t.Fatalf("replica after cancel = %+v", rep)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleReplica {
		t.Fatalf("role after cancel = %q", srv.Role)
	}
	if n := r.count("mysql.releaseRole"); n != 0 {
		t.Fatal("role released on a canceled resync")
	}
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "dump", Bytes: 100, GTIDExecuted: masterGTID}, nil
	})
	id, err = e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatalf("StartResync after cancel: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}
