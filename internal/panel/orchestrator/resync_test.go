package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
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
	return []string{"Periksa master", "Periksa server tujuan", "Siapkan replika", "Salin data", "Pasang sertifikat MySQL",
		"Sambungkan replikasi", "Verifikasi"}
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
	// Where the sweep runs: what the replica had received by then.
	var sweptAfter []string
	m.on("mysql.sweepDumpUsers", func(json.RawMessage) (any, error) {
		sweptAfter = r.cmds(0)
		return proto.SweepDumpUsersResult{Dropped: []string{"'dbpanel_dump_0badc0de'@'10.0.0.2'"}}, nil
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
	want := slices.Repeat([]string{store.StepDone}, 7)
	if got := stepStatuses(steps); !slices.Equal(got, want) {
		t.Fatalf("step statuses = %v (no step may wait)", got)
	}
	// The Monitor hears of resyncing, ready and the cleared pause.
	if n := e.alerts.changes(r.id); n < 3 {
		t.Fatalf("ReplicaChanged called %d times, want at least 3", n)
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
		"mysql.tlsRequest", "mysql.tlsInstall", "mysql.status", "mysql.configureReplication", "mysql.startReplica",
		"mysql.startReplica", "mysql.waitSynced"})
	var dr proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &dr)
	if !slices.Equal(dr.Databases, []string{"a", "x"}) {
		t.Fatalf("dropDatabases = %v", dr.Databases)
	}
	// Leftover dump accounts are swept at the start of Siapkan replika,
	// before the target is configured and before the dump's own account.
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.sweepDumpUsers", "mysql.createDumpUser",
		"mysql.dropUser", "mysql.status", "mysql.recordReplicas"})
	wantCmds(t, "replica before the sweep", sweptAfter, []string{"probe", "mysql.preflight"})
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
	// The Monitor hears it right after the job is stored as finished.
	e.waitFor("JobEnded", func() bool {
		_, ended := e.alerts.lists()
		return slices.Contains(ended, fmt.Sprintf("%d %s", r.id, id))
	})
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
	if got := stepStatuses(steps); !slices.Equal(got, slices.Repeat([]string{store.StepDone}, 7)) {
		t.Fatalf("step statuses = %v", got)
	}
	if r.count("mysql.cloneFrom") != 1 || r.count("mysql.dumpFrom") != 0 || r.count("mysql.dropDatabases") != 0 {
		t.Fatalf("replica commands = %v", r.cmds(0))
	}
	// A CLONE resync sweeps leftover dump accounts too.
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.sweepDumpUsers", "mysql.status", "mysql.recordReplicas"})
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

// TestJobFailedClosesOnLaterJob wires the real Monitor: a failed resync
// opens job_failed; a later resync that succeeds closes it, and so does
// detaching the replica after another failure.
func TestJobFailedClosesOnLaterJob(t *testing.T) {
	e := newEnv(t)
	mon := healthmon.New(healthmon.Deps{Store: e.st, Hub: e.hub, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(mon.Close)
	e.r.d.Alerts = mon
	_, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}}, "a")
	ctx := context.Background()
	isOpen := func() bool {
		_, err := e.st.GetOpenAlert(healthmon.KindJobFailed, r.id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	failPreflight := func() {
		r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckFail, Method: "dump",
			Checks: []proto.Check{{ID: "disk", Status: proto.CheckFail, Title: "Ruang disk", Detail: "penuh"}}})
	}

	failPreflight()
	a, err := e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(a, store.JobFailed)
	e.waitFor("job_failed opened by the failed resync", isOpen)
	// A new resync B (not a retry of A) succeeds: the alert closes.
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckOK, Method: "dump"})
	b, err := e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(b, store.JobSucceeded)
	e.waitFor("job_failed closed by a later resync succeeding", func() bool { return !isOpen() })

	// Detaching a replica whose resync failed closes it too.
	failPreflight()
	c, err := e.r.StartResync(ctx, ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(c, store.JobFailed)
	e.waitFor("job_failed opened by the second failed resync", isOpen)
	if err := e.r.DetachReplica(ctx, r.id, false); err != nil {
		t.Fatal(err)
	}
	if isOpen() {
		t.Fatal("job_failed still open after the replica was detached")
	}
}

// TestResyncRefusedWhileLockMayStand (4b final review, parked Minor): after
// a failed planned promotion whose unlock was never confirmed, the
// master's guard may still hold the lock and, at its deadline, quarantine
// the master if a replica it has seen is writable. Resync ulang, Buat
// replika and Tambah database each make a replica writable for a while,
// so each is refused, saying from when it is possible and naming
// unlock-master, until the lock's deadline plus the guard's grace; then
// each is accepted again (here it goes on to its next check).
func TestResyncRefusedWhileLockMayStand(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.emergencyAfterFailedPlanned(func(json.RawMessage) (any, error) {
		return nil, &proto.Error{Code: proto.CodeOffline, Message: "agent offline"}
	})
	deadline := g.promotion().LockAt.Add(30 * time.Minute) // the rig's lock: LockedAt + 30 min
	until := deadline.Add(proto.PromoteLockGrace).Local().Format("02-01-2006 15:04:05")
	// A replica following a subset, for Tambah database.
	sub := e.agent("rep-sub", "10.0.0.5")
	if _, err := e.st.AddReplica(g.topo.ID, sub.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(sub.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	target := e.agent("baru", "10.0.0.6")
	ctx := context.Background()
	// Failed jobs of b and sub, for Ulangi.
	failedJob := func(id, kind string, serverID int64, params string) string {
		t.Helper()
		if err := e.st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: serverID, TopologyID: g.topo.ID, ParamsJSON: params,
			CreatedAt: time.Now()}, []string{"x"}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(id, store.JobFailed, 0, "gagal", time.Now()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failedResync := failedJob("failed-resync", KindResyncReplica, g.b.id, "{}")
	failedDrop := failedJob("failed-drop", KindRemoveDatabases, sub.id, `{"drop_data":true}`)
	starts := []struct {
		what  string
		start func() error
		// after is what refuses it once the lock has decided.
		after string
	}{
		{"Resync ulang", func() error {
			_, err := e.r.StartResync(ctx, ResyncParams{ServerID: g.b.id})
			return err
		}, "pekerjaan"},
		{"Buat replika", func() error {
			_, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.m.id, TargetServerID: target.id,
				Databases: []string{"tidak-ada"}})
			return err
		}, "tidak-ada"},
		{"Tambah database", func() error {
			_, err := e.r.StartAddDatabases(ctx, sub.id, []string{"tidak-ada"})
			return err
		}, "tidak ada di master"},
		// Dropping the data lifts read-only on the replica (fix round 1).
		{"Lepas database", func() error {
			_, err := e.r.StartRemoveDatabases(ctx, sub.id, []string{"tidak-ada"}, true)
			return err
		}, "tidak diikuti replika ini"},
		// Ulangi of a failed job of those kinds loads data the same way.
		{"Resync ulang", func() error { return e.r.Retry(failedResync) }, "pekerjaan lain"},
		{"Lepas database", func() error { return e.r.Retry(failedDrop) }, "pekerjaan lain"},
	}
	check := func(when string, now time.Time, refused bool) {
		t.Helper()
		e.r.now = func() time.Time { return now }
		for _, s := range starts {
			err := s.start()
			if refused {
				if err == nil {
					t.Fatalf("%s %s: accepted while the lock may stand", s.what, when)
				}
				for _, part := range []string{"mungkin masih terkunci", s.what + " baru bisa dijalankan mulai " + until,
					"memuat data membuat replika bisa ditulis sebentar dan penjaga kunci bisa mengkarantina master",
					"`sudo dbpanel agent unlock-master`, lalu tekan Ulangi pada promote terencana itu (panel memastikan kunci terbuka)"} {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("%s %s: refusal %q lacks %q", s.what, when, err, part)
					}
				}
				continue
			}
			if err == nil || strings.Contains(err.Error(), "mungkin masih terkunci") || !strings.Contains(err.Error(), s.after) {
				t.Errorf("%s %s: %v, want past the lock check to %q", s.what, when, err, s.after)
			}
		}
	}
	check("now", time.Now(), true)
	// Lepas database without dropping the data lifts nothing: not refused.
	if _, err := e.r.StartRemoveDatabases(ctx, sub.id, []string{"tidak-ada"}, false); err == nil ||
		!strings.Contains(err.Error(), "tidak diikuti replika ini") {
		t.Fatalf("Lepas database without drop: %v", err)
	}
	// Past the deadline but within the grace: still refused.
	check("within the grace", deadline.Add(30*time.Second), true)
	// Past both: the guard has decided. A busy b shows the resync got past
	// the lock check without starting a job.
	for _, srv := range []int64{g.b.id, sub.id} {
		if err := e.st.CreateJob(store.Job{ID: fmt.Sprintf("busy-%d", srv), Kind: KindMySQLTLS, ServerID: srv, ParamsJSON: "{}",
			CreatedAt: time.Now()}, []string{"x"}); err != nil {
			t.Fatal(err)
		}
	}
	// Tambah and Lepas database now meet sub's busy job first.
	starts[2].after, starts[3].after = "pekerjaan", "pekerjaan"
	check("after the grace", deadline.Add(proto.PromoteLockGrace+time.Second), false)
	if rep := e.replica(g.b.id); rep.State != store.ReplicaReady {
		t.Fatalf("b after the refusals = %s, want ready", rep.State)
	}
}
