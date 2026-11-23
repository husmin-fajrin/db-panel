package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// fullDisk scripts fa's last report with its disk full (red at any
// critical threshold).
func (e *env) fullDisk(fa *fakeAgent) {
	e.health.set(fa.id, monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: time.Now(),
		Snap: &proto.HealthSnapshot{MySQLOK: true, DiskTotal: 100, DiskFree: 0}})
}

// rootAccount makes fa's agent log in to MySQL as root.
func (e *env) rootAccount(fa *fakeAgent) {
	e.t.Helper()
	inv, err := e.st.GetInventory(fa.id)
	if errors.Is(err, store.ErrNotFound) {
		inv = store.Inventory{ServerID: fa.id, Connected: true, Version: "8.4.3", Series: "8.4", UpdatedAt: time.Now()}
	} else if err != nil {
		e.t.Fatal(err)
	}
	inv.Connected, inv.Account = true, "root@localhost"
	if err := e.st.SetInventory(inv); err != nil {
		e.t.Fatal(err)
	}
}

// noJob fails when serverID has a job of any status.
func noJob(t *testing.T, e *env, serverID int64) {
	t.Helper()
	jobs, err := e.st.ListJobsForServer(serverID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) > 0 {
		t.Fatalf("refused start left jobs on server %d: %+v", serverID, jobs)
	}
}

// onlyCmds fails when an agent received, since its mark, a command other
// than those allowed (the reads before the start lock).
func onlyCmds(t *testing.T, marks map[*fakeAgent]int, allowed ...string) {
	t.Helper()
	for fa, n := range marks {
		for _, c := range fa.cmds(n) {
			if !slices.Contains(allowed, c) {
				t.Errorf("%s received %s after a refused start (allowed: %v)", fa.name, c, allowed)
			}
		}
	}
}

func markAll(e *env) map[*fakeAgent]int {
	out := map[*fakeAgent]int{}
	for _, fa := range e.agents {
		out[fa] = fa.mark()
	}
	return out
}

func wantGateRed(t *testing.T, err error, labels string) {
	t.Helper()
	if !errors.Is(err, ErrGateRed) {
		t.Fatalf("err = %v, want ErrGateRed", err)
	}
	var ge *GateError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %T, want *GateError", err)
	}
	if want := "prasyarat belum terpenuhi: " + labels; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// Each gated start, with one row red that applies to it, is refused before
// anything is written: no job, no replica row, no promotion, and no agent
// command after the reads the start makes before its lock.
func TestStartRefusesOnRedRow(t *testing.T) {
	ctx := context.Background()
	t.Run("prepare_master", func(t *testing.T) {
		e := newEnv(t)
		m := e.connectedMaster("db-master")
		e.rootAccount(m)
		marks := markAll(e)
		_, err := e.r.StartPrepareMaster(ctx, m.id)
		wantGateRed(t, err, "Akun panel")
		noJob(t, e, m.id)
		onlyCmds(t, marks)
	})
	t.Run("create_replica", func(t *testing.T) {
		e := newEnv(t)
		m, _ := e.readyMaster("db-master")
		tgt := e.replicaAgent("db-target", "clone")
		e.fullDisk(tgt)
		marks := markAll(e)
		_, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: m.id, TargetServerID: tgt.id, ConfirmWipe: true})
		wantGateRed(t, err, "Ruang disk")
		if _, err := e.st.GetReplicaByServer(tgt.id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused start registered a replica: %v", err)
		}
		noJob(t, e, tgt.id)
		onlyCmds(t, marks)
	})
	t.Run("resync", func(t *testing.T) {
		e := newEnv(t)
		_, topo := e.readyMaster("db-master")
		rep := e.resyncReplica(topo, nil)
		e.fullDisk(rep)
		marks := markAll(e)
		_, err := e.r.StartResync(ctx, ResyncParams{ServerID: rep.id})
		wantGateRed(t, err, "Ruang disk")
		if got := e.replica(rep.id).State; got != store.ReplicaReady {
			t.Fatalf("replica state after a refused resync = %s", got)
		}
		noJob(t, e, rep.id)
		onlyCmds(t, marks)
	})
	t.Run("add_databases", func(t *testing.T) {
		e := newEnv(t)
		m, topo := e.readyMaster("db-master")
		rep := e.followingReplica(topo, "a")
		off := false
		e.health.set(m.id, monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: time.Now(),
			Snap: &proto.HealthSnapshot{MySQLOK: true, PerfSchema: &off}})
		marks := markAll(e)
		_, err := e.r.StartAddDatabases(ctx, rep.id, []string{"b"})
		wantGateRed(t, err, "performance_schema")
		noJob(t, e, rep.id)
		onlyCmds(t, marks)
	})
	t.Run("remove_databases_drop", func(t *testing.T) {
		e := newEnv(t)
		_, topo := e.readyMaster("db-master")
		rep := e.followingReplica(topo, "a", "b")
		e.rootAccount(rep)
		marks := markAll(e)
		_, err := e.r.StartRemoveDatabases(ctx, rep.id, []string{"b"}, true)
		wantGateRed(t, err, "Akun panel")
		noJob(t, e, rep.id)
		onlyCmds(t, marks)
	})
	t.Run("promote_planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		if err := e.st.SetReplicaTLSVerified(g.b.id, false); err != nil {
			t.Fatal(err)
		}
		marks := markAll(e)
		_, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		wantGateRed(t, err, "Pemeriksaan sertifikat TLS")
		if _, err := e.st.LatestPromotion(g.topo.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused promote recorded a promotion: %v", err)
		}
		noJob(t, e, g.m.id)
		onlyCmds(t, marks, "mysql.binlogTail", "mysql.probeSource")
	})
	t.Run("rebuild_old_master", func(t *testing.T) {
		g := rebuildRig(t)
		e := g.e
		e.r.d.Version = "9.9.9" // no agent reported it
		marks := markAll(e)
		_, err := g.startRebuild()
		wantGateRed(t, err, "Versi agent")
		noRebuildStarted(t, g)
		onlyCmds(t, marks, "mysql.connections", "mysql.rebuildApproval", "job.cancel")
	})
}

// Yellow rows are shown, never refused: the start goes on.
func TestStartPassesWithYellowRows(t *testing.T) {
	ctx := context.Background()
	t.Run("create_replica", func(t *testing.T) {
		e := newEnv(t)
		m, _ := e.readyMaster("db-master")
		tgt := e.replicaAgent("db-target", "clone")
		e.noReport(tgt)
		res, err := e.r.Gate(GateInput{Action: GateCreateReplica, MasterID: m.id, TargetID: tgt.id})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Red()) > 0 || !slices.ContainsFunc(res.Rows, func(r GateRow) bool { return r.Status == GateYellow }) {
			t.Fatalf("rows = %+v, want yellow and no red", res.Rows)
		}
		if _, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: m.id, TargetServerID: tgt.id,
			ConfirmWipe: true}); err != nil {
			t.Fatalf("StartCreateReplica with yellow rows: %v", err)
		}
	})
	t.Run("promote_planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		// The marks of Pengaturan unset and the master's port open to the
		// panel host: yellow only.
		e.r.d.PortProbe = func(context.Context, string) bool { return true }
		res, err := e.r.Gate(GateInput{Action: GatePromotePlanned, MasterID: g.m.id, TargetID: g.c.id})
		if err != nil {
			t.Fatal(err)
		}
		yellow := 0
		for _, r := range res.Rows {
			if r.Status == GateYellow {
				yellow++
			}
		}
		if len(res.Red()) > 0 || yellow < 3 {
			t.Fatalf("rows = %+v, want three yellow and no red", res.Rows)
		}
		id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatalf("StartPromote with yellow rows: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
	})
}

// noReport scripts fa's last report without a snapshot (its disk not
// reported yet).
func (e *env) noReport(fa *fakeAgent) {
	e.health.set(fa.id, monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: time.Now()})
}

// An emergency promote is never refused by a row.
func TestEmergencyPromoteNotGated(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.offline(g.m)
	g.offline(g.d)
	if err := e.st.SetReplicaTLSVerified(g.b.id, false); err != nil {
		t.Fatal(err)
	}
	for _, fa := range []*fakeAgent{g.c, g.b} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	res, err := e.r.Gate(GateInput{Action: GatePromoteEmergency, MasterID: g.m.id, TargetID: g.c.id})
	if err != nil || len(res.Red()) == 0 {
		t.Fatalf("gate = %+v, %v; want red rows", res.Rows, err)
	}
	id, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.c.id,
		Mode: proto.PromoteEmergency, AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("emergency promote with red rows: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

func TestPlannedPromoteNeedsBackupAck(t *testing.T) {
	ctx := context.Background()
	// A folder set and a fresh backup in it: the tick is not asked.
	g := newPromoteRig(t)
	id, err := g.e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote with a fresh backup: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)

	// No folder set: refused without the tick, nothing written; with it,
	// the promote runs.
	g = newPromoteRig(t)
	e := g.e
	if err := e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	marks := markAll(e)
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}); !errors.Is(err, ErrBackupAck) {
		t.Fatalf("StartPromote without the tick = %v, want ErrBackupAck", err)
	}
	if _, err := e.st.LatestPromotion(g.topo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused promote recorded a promotion: %v", err)
	}
	noJob(t, e, g.m.id)
	onlyCmds(t, marks, "mysql.binlogTail", "mysql.probeSource")
	id, err = e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, BackupAck: true})
	if err != nil {
		t.Fatalf("StartPromote with the tick: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

func TestRebuildNeedsBackupAck(t *testing.T) {
	// A folder set and a fresh backup in it: the tick is not asked.
	g := rebuildRig(t)
	id, err := g.startRebuild()
	if err != nil {
		t.Fatalf("rebuild with a fresh backup: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)

	g = rebuildRig(t)
	e := g.e
	if err := e.st.SetServerBackup(g.c.id, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := g.startRebuild(); !errors.Is(err, ErrBackupAck) {
		t.Fatalf("rebuild without the tick = %v, want ErrBackupAck", err)
	}
	noRebuildStarted(t, g)
	id, err = e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: g.c.id,
		TargetServerID: g.m.id, RebuildOldMaster: true, ConfirmWipe: true, BackupAck: true})
	if err != nil {
		t.Fatalf("rebuild with the tick: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

func TestRetryGatedOnlyWhenDataIsDropped(t *testing.T) {
	t.Run("resync", func(t *testing.T) {
		e := newEnv(t)
		m, topo := e.readyMaster("db-master")
		rep := e.resyncReplica(topo, nil)
		m.fail("mysql.status", "MySQL master tidak menjawab")
		id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: rep.id})
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		m.reply("mysql.status", masterStatus())
		e.fullDisk(rep)
		marks := markAll(e)
		confirmed := e.storedState(id).ConfirmedAt
		time.Sleep(10 * time.Millisecond)
		wantGateRed(t, e.r.Retry(id, JobAck{Confirmed: true}), "Ruang disk")
		if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
			t.Fatalf("job after a refused retry = %+v", j)
		}
		onlyCmds(t, marks)
		if got := e.storedState(id).ConfirmedAt; !got.Equal(confirmed) {
			t.Fatalf("refused retry stored a confirmation: %v, was %v", got, confirmed)
		}
		// The disk freed: its report is green again.
		e.health.mu.Lock()
		delete(e.health.views, rep.id)
		e.health.mu.Unlock()
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("retry once the row is green: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
	})
	t.Run("promote", func(t *testing.T) {
		// K4: a promote job is never judged again.
		g := newPromoteRig(t)
		e := g.e
		e.r.promoteCatchUp = 100 * time.Millisecond
		g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
			Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		if err := e.st.SetReplicaTLSVerified(g.b.id, false); err != nil {
			t.Fatal(err)
		}
		if res, _ := e.r.Gate(GateInput{Action: GatePromotePlanned, MasterID: g.m.id, TargetID: g.c.id}); len(res.Red()) == 0 {
			t.Fatalf("gate = %+v, want a red row", res.Rows)
		}
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry of a failed promote with a red row: %v", err)
		}
		e.waitJob(id, store.JobFailed)
	})
	t.Run("rebuild_approval", func(t *testing.T) {
		// Approve of a rebuild without root's approval is refused as before
		// (the approval is read before the gate).
		g := rebuildRig(t)
		e := g.e
		g.m.fail("mysql.preflight", "pemeriksaan gagal")
		id, err := g.startRebuild()
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		j, _, _ := e.st.GetJob(id)
		if err := e.st.ReopenJob(id, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(id, store.JobWaiting, j.CurrentStep, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		e.setState(id, func(st *jobState) { st.WaitReason = WaitWipe })
		g.m.reply("mysql.rebuildApproval", notApproved("123456"))
		if err := e.r.Approve(id, JobAck{Confirmed: true}); !errors.Is(err, ErrRebuildNeedsApproval) {
			t.Fatalf("Approve = %v, want ErrRebuildNeedsApproval", err)
		}
		// Approved, but a row red: refused by the gate.
		scriptReplicaTarget(g.m, "db-master", "clone")
		e.fullDisk(g.m)
		wantGateRed(t, e.r.Approve(id, JobAck{Confirmed: true}), "Ruang disk")
		if j, _, _ := e.st.GetJob(id); j.Status != store.JobWaiting {
			t.Fatalf("job after a refused approval = %+v", j)
		}
	})
}

func TestRemoveDatabasesWithoutDropNotGated(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	rep := e.followingReplica(topo, "a", "b")
	e.rootAccount(rep)
	_, err := e.r.StartRemoveDatabases(context.Background(), rep.id, []string{"b"}, true)
	wantGateRed(t, err, "Akun panel")
	id, err := e.r.StartRemoveDatabases(context.Background(), rep.id, []string{"b"}, false)
	if err != nil {
		t.Fatalf("remove without drop with a red row: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

func TestGateActionForJob(t *testing.T) {
	for _, c := range []struct {
		kind   string
		st     jobState
		action GateAction
		ok     bool
	}{
		{KindCreateReplica, jobState{}, GateCreateReplica, true},
		{KindCreateReplica, jobState{RebuildOldMaster: true}, GateRebuildOldMaster, true},
		{KindResyncReplica, jobState{}, GateResync, true},
		{KindAddDatabases, jobState{}, GateAddDatabases, true},
		{KindRemoveDatabases, jobState{DropData: true}, GateRemoveWithDrop, true},
		{KindPromotePlanned, jobState{}, "", false},
		{KindPromoteEmergency, jobState{}, "", false},
		{KindPrepareMaster, jobState{}, "", false},
	} {
		a, ok := gateActionForJob(c.kind, c.st)
		if a != c.action || ok != c.ok {
			t.Errorf("gateActionForJob(%s, %+v) = %q, %v; want %q, %v", c.kind, c.st, a, ok, c.action, c.ok)
		}
	}
}
