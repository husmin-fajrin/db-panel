package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// replicaCmds is what a target with MySQL receives for a full run.
func replicaCmds(snapshot string) []string {
	return []string{"probe", "mysql.preflight", "mysql.configure", "mysql.restart", "mysql.setReadOnly", snapshot,
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"}
}

func allDone(install string) []string {
	return []string{store.StepDone, store.StepDone, install, store.StepDone, store.StepDone, store.StepDone, store.StepDone}
}

func TestCreateReplicaHappyPathClone(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")

	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("StartCreateReplica: %v", err)
	}
	rep, err := e.st.GetReplicaByServer(r.id)
	if err != nil || rep.TopologyID != topo.ID {
		t.Fatalf("replica row = %+v, %v", rep, err)
	}

	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 4 || steps[4].Message != "CLONE akan menghapus SEMUA database di replica1. Lanjutkan?" {
		t.Fatalf("job %+v, snapshot step %+v", j, steps[4])
	}
	time.Sleep(50 * time.Millisecond)
	if n := r.count("mysql.cloneFrom"); n != 0 {
		t.Fatal("CLONE started before Approve")
	}
	if err := e.r.Approve(id); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	j, steps = e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepSkipped)) {
		t.Fatalf("steps = %v", got)
	}
	wantCmds(t, "replica", r.cmds(0), replicaCmds("mysql.cloneFrom"))
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.status"})

	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if pre.Role != "replica" || pre.PeerHost != "10.0.0.1" || pre.PeerPort != 3306 || pre.PeerVersion != "8.4.3" ||
		pre.PeerServerUUID != masterUUID || pre.PeerDataBytes != 300 || !slices.Equal(pre.PeerDatabases, []string{"a", "b"}) ||
		len(pre.Databases) != 0 || pre.ConfirmWipe {
		t.Fatalf("replica preflight params = %+v", pre)
	}
	var conf proto.ConfigureParams
	r.params("mysql.configure", &conf)
	if conf.Role != "replica" || conf.ServerID == 0 || conf.ServerID == 1 || !conf.EnableClone || !conf.GTID {
		t.Fatalf("configure params = %+v", conf)
	}
	var ro proto.ReadOnlyParams
	r.params("mysql.setReadOnly", &ro)
	if !ro.On {
		t.Fatal("setReadOnly off")
	}
	var snap proto.SnapshotParams
	r.params("mysql.cloneFrom", &snap)
	if snap.Host != "10.0.0.1" || snap.Port != 3306 || snap.User != "dbpanel_repl" || snap.Password != replPW ||
		len(snap.Databases) != 0 || snap.EstimateBytes != 300 {
		t.Fatalf("cloneFrom params = %+v", snap)
	}
	var src proto.ReplicationSourceParams
	r.params("mysql.configureReplication", &src)
	if src.Host != "10.0.0.1" || src.Port != 3306 || src.User != "dbpanel_repl" || src.Password != replPW || src.SSL ||
		len(src.Databases) != 0 {
		t.Fatalf("configureReplication params = %+v", src)
	}
	var ws proto.WaitSyncedParams
	r.params("mysql.waitSynced", &ws)
	if ws.GTIDSet != masterGTID || ws.TimeoutSec < 1 {
		t.Fatalf("waitSynced params = %+v", ws)
	}

	rep, _ = e.st.GetReplicaByServer(r.id)
	if rep.State != store.ReplicaReady || rep.Databases != nil || rep.LastError != "" {
		t.Fatalf("replica = %+v", rep)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleReplica {
		t.Fatalf("role = %q", srv.Role)
	}
	log := e.jobLog(id)
	for _, want := range []string{"clone dari master dimulai", "akun"} {
		if !strings.Contains(log, want) {
			t.Errorf("job log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, replPW) {
		t.Fatalf("job log holds the replication password:\n%s", log)
	}
}

// scriptLeftovers makes the target hold left (databases the master has
// too): its preflight asks for confirmation until ConfirmWipe is sent.
func scriptLeftovers(r *fakeAgent, method string, left ...string) {
	r.on("mysql.preflight", func(raw json.RawMessage) (any, error) {
		var p proto.PreflightParams
		json.Unmarshal(raw, &p)
		if p.ConfirmWipe {
			return proto.PreflightResult{Worst: proto.CheckOK, Method: method}, nil
		}
		return proto.PreflightResult{Worst: proto.CheckNeedsConfirm, Method: method, Checks: []proto.Check{
			{ID: "leftover", Status: proto.CheckNeedsConfirm, Title: "Database bernama sama dengan di master"}}}, nil
	})
	var dbs []proto.DatabaseInfo
	for _, d := range left {
		dbs = append(dbs, proto.DatabaseInfo{Name: d})
	}
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: dbs})
}

func TestCreateReplicaSubsetUsesDumpWithFilter(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	scriptLeftovers(r, "dump", "a", "b") // both left by an earlier attempt; only a is selected now

	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobWaiting)
	if want := "database dari percobaan sebelumnya akan dihapus lalu disalin ulang dari master: a, b"; steps[1].Message != want {
		t.Fatalf("waiting message = %q, want %q", steps[1].Message, want)
	}
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	_, steps = e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepSkipped)) {
		t.Fatalf("steps = %v", got)
	}
	// Approve runs the waiting step again: probe, preflight, the list, the
	// confirmed preflight.
	wantCmds(t, "replica", r.cmds(0), []string{"probe", "mysql.preflight", "mysql.inventory",
		"probe", "mysql.preflight", "mysql.inventory", "mysql.preflight",
		"mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.dropDatabases", "mysql.dumpFrom",
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if !slices.Equal(drop.Databases, []string{"a", "b"}) {
		t.Fatalf("dropDatabases = %v, want the full confirmed leftover list [a b]", drop.Databases)
	}
	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if !slices.Equal(pre.Databases, []string{"a"}) || pre.PeerDataBytes != 100 {
		t.Fatalf("preflight params = %+v", pre)
	}
	var conf proto.ConfigureParams
	r.params("mysql.configure", &conf)
	if conf.EnableClone {
		t.Fatal("clone plugin enabled for a dump")
	}
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if !slices.Equal(snap.Databases, []string{"a"}) || snap.User != "dbpanel_repl" || snap.Password != replPW ||
		snap.EstimateBytes != 100 || snap.DropExisting {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
	var src proto.ReplicationSourceParams
	r.params("mysql.configureReplication", &src)
	if !slices.Equal(src.Databases, []string{"a"}) {
		t.Fatalf("configureReplication databases = %v", src.Databases)
	}
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaReady || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica = %+v", rep)
	}
	if log := e.jobLog(id); !strings.Contains(log, "master dikunci sesaat") {
		t.Fatalf("job log lacks the FTWRL note:\n%s", log)
	}
}

func TestCreateReplicaRefusesNonDedicatedTarget(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "")
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckFail, Checks: []proto.Check{
		{ID: "dedicated", Status: proto.CheckFail, Title: "Server replika khusus",
			Detail: "server ini masih berisi database lain (toko); versi ini hanya mendukung server replika khusus"},
	}})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 1 || steps[1].Status != store.StepFailed || !strings.Contains(j.Error, "masih berisi database lain (toko)") {
		t.Fatalf("job %+v steps %+v", j, steps)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"probe", "mysql.preflight"})
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaFailed || !strings.Contains(rep.LastError, "toko") {
		t.Fatalf("replica = %+v", rep)
	}
}

// TestCreateReplicaRefusals: what StartCreateReplica refuses before any job.
func TestCreateReplicaRefusals(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	notReady := e.connectedMaster("db-plain")
	busy := e.replicaAgent("replica-busy", "clone")
	if err := e.st.CreateJob(store.Job{ID: "other", Kind: "x", ServerID: busy.id}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]CreateReplicaParams{
		"self":           {MasterServerID: m.id, TargetServerID: m.id},
		"not ready":      {MasterServerID: notReady.id, TargetServerID: r.id},
		"unknown target": {MasterServerID: m.id, TargetServerID: notReady.id + 1000},
		"busy target":    {MasterServerID: m.id, TargetServerID: busy.id},
		"unknown db":     {MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"nope"}},
	} {
		if _, err := e.r.StartCreateReplica(context.Background(), p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := e.st.SetServerRole(r.id, store.RoleMaster); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id}); err == nil {
		t.Error("target with a role accepted")
	}
	if jobs, _ := e.st.ListJobs(0); len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestCreateReplicaInstallsMySQLWhenMissing(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	var installed atomic.Bool
	r.on("probe", func(json.RawMessage) (any, error) {
		return proto.ProbeResult{Hostname: "replica1", MySQL: proto.MySQLInfo{Installed: installed.Load()}}, nil
	})
	r.job("mysql.install", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Logf("info", "memasang paket mysql-server")
		installed.Store(true)
		return proto.InstallResult{Version: "8.4.3", Source: "mysql-repo"}, nil
	})
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"},
		Account: "dbpanel_admin@localhost"})

	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepDone)) {
		t.Fatalf("steps = %v", got)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"probe", "probe", "mysql.install", "mysql.inventory", "mysql.preflight",
		"mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.cloneFrom",
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	var ins proto.InstallParams
	r.params("mysql.install", &ins)
	if ins.Series != "8.4" || ins.ExactVersion != "8.4.3" {
		t.Fatalf("install params = %+v", ins)
	}
	if inv, err := e.st.GetInventory(r.id); err != nil || !inv.Connected || inv.Version != "8.4.3" {
		t.Fatalf("target inventory = %+v, %v", inv, err)
	}
	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if pre.ConfirmWipe {
		t.Fatal("the form's ConfirmWipe reached the preflight; only an in-job approval confirms a wipe")
	}
	if r.count("mysql.cloneFrom") != 1 {
		t.Fatal("the form's ConfirmWipe did not pre-approve the CLONE")
	}
	if log := e.jobLog(id); !strings.Contains(log, "memasang paket mysql-server") {
		t.Fatalf("agent job log not copied:\n%s", log)
	}
}

func TestFailsWhenAgentDropsDuringSnapshot(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.blockingJob("mysql.dumpFrom")

	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("dumpFrom job", func() bool { return r.startedJobs() == 1 })
	r.detach()
	j, steps := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 4 || steps[4].Status != store.StepFailed || !strings.Contains(j.Error, "agent replica1 terputus saat Salin data") {
		t.Fatalf("job %+v steps %+v", j, steps)
	}
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaFailed || rep.LastError == "" {
		t.Fatalf("replica = %+v", rep)
	}

	// The agent comes back; its old job is gone (failed). Retry resumes at
	// the snapshot with the same params.
	r.stopJobs()
	e.waitFor("old agent job ended", func() bool {
		st, _ := r.jobs.Status(r.lastJob())
		return st.State != "running"
	})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "dump", GTIDExecuted: masterGTID}, nil
	})
	r.attach()
	mark := r.mark()
	if err := e.r.Retry(id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, steps = e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepSkipped)) {
		t.Fatalf("steps = %v", got)
	}
	wantCmds(t, "replica after retry", r.cmds(mark), []string{"mysql.dumpFrom", "mysql.status",
		"mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	dumps := r.allParams("mysql.dumpFrom")
	if len(dumps) != 2 || dumps[0] != dumps[1] {
		t.Fatalf("dumpFrom params differ on retry:\n%v", dumps)
	}
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaReady || rep.LastError != "" {
		t.Fatalf("replica = %+v", rep)
	}
}

func TestForceDumpSkipsClone(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ForceDump: true})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(0), replicaCmds("mysql.dumpFrom"))
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if len(snap.Databases) != 0 {
		t.Fatalf("dump of all databases sent %v", snap.Databases)
	}
	var conf proto.ConfigureParams
	r.params("mysql.configure", &conf)
	if conf.EnableClone {
		t.Fatal("clone plugin enabled for a forced dump")
	}
}

func TestResumeMarksRunningFailed(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r1 := e.agent("replica1", "10.0.0.2")
	r2 := e.agent("replica2", "10.0.0.3")
	now := time.Now()
	for _, s := range []struct {
		id     string
		server int64
		status string
		step   int
	}{{"running-job", r1.id, store.JobRunning, 3}, {"waiting-job", r2.id, store.JobWaiting, 4}} {

		if _, err := e.st.AddReplica(topo.ID, s.server, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.st.CreateJob(store.Job{ID: s.id, Kind: KindCreateReplica, ServerID: s.server, TopologyID: topo.ID,
			ParamsJSON: `{}`}, createReplicaStepNames()); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(s.id, s.status, s.step, "", now); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetStep(s.id, s.step, store.StepRunning, "", now); err != nil {
			t.Fatal(err)
		}
	}
	_ = m
	r3 := e.agent("replica3", "10.0.0.4")
	if err := e.st.CreateJob(store.Job{ID: "queued-job", Kind: KindCreateReplica, ServerID: r3.id, TopologyID: topo.ID,
		ParamsJSON: `{}`}, createReplicaStepNames()); err != nil {
		t.Fatal(err)
	}

	e.r.Resume(context.Background())

	if j, steps, _ := e.st.GetJob("queued-job"); j.Status != store.JobFailed || j.Error != resumeError || steps[0].Status != store.StepFailed {
		t.Fatalf("queued job after resume = %+v, step %+v", j, steps[0])
	}
	if _, busy, _ := e.st.ActiveJobForServer(r3.id); busy {
		t.Fatal("the queued job still blocks its server")
	}

	j, steps, _ := e.st.GetJob("running-job")
	if j.Status != store.JobFailed || j.Error != "panel dimulai ulang saat job berjalan" || j.CurrentStep != 3 ||
		steps[3].Status != store.StepFailed {
		t.Fatalf("running job = %+v, step %+v", j, steps[3])
	}
	if rep, _ := e.st.GetReplicaByServer(r1.id); rep.State != store.ReplicaFailed {
		t.Fatalf("replica of the running job = %+v", rep)
	}
	if j, _, _ := e.st.GetJob("waiting-job"); j.Status != store.JobWaiting {
		t.Fatalf("waiting job = %+v", j)
	}
	if rep, _ := e.st.GetReplicaByServer(r2.id); rep.State != store.ReplicaCreating {
		t.Fatalf("replica of the waiting job = %+v", rep)
	}
}

func TestCancelSendsJobCancel(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.blockingJob("mysql.dumpFrom")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("dumpFrom job", func() bool { return r.startedJobs() == 1 })
	agentJob := r.lastJob()
	e.waitFor("progress published", func() bool {
		_, steps, _ := e.st.GetJob(id)
		return strings.Contains(steps[4].Message, "menyalin")
	})

	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	e.waitJob(id, store.JobCanceled)
	var c proto.JobIDParams
	r.params("job.cancel", &c)
	if c.JobID != agentJob {
		t.Fatalf("job.cancel for %q, want %q", c.JobID, agentJob)
	}
	e.waitFor("agent job canceled", func() bool {
		st, _ := r.jobs.Status(agentJob)
		return st.State == "canceled"
	})
	// The target is free for a new attempt.
	if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replica row after cancel: err = %v, want ErrNotFound", err)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after cancel = %q", srv.Role)
	}
	if err := e.r.Cancel(id); err == nil {
		t.Fatal("second Cancel succeeded")
	}
}

// TestLeftoverConfirmationBoundToList: Approve confirms the databases that
// were shown; when the list changed meanwhile, the job asks again.
func TestLeftoverConfirmationBoundToList(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	var leftMu sync.Mutex
	left := []string{"a"}
	r.on("mysql.preflight", func(raw json.RawMessage) (any, error) {
		var p proto.PreflightParams
		json.Unmarshal(raw, &p)
		if p.ConfirmWipe {
			return proto.PreflightResult{Worst: proto.CheckOK, Method: "dump"}, nil
		}
		return proto.PreflightResult{Worst: proto.CheckNeedsConfirm, Method: "dump", Checks: []proto.Check{
			{ID: "leftover", Status: proto.CheckNeedsConfirm, Title: "Database bernama sama dengan di master"}}}, nil
	})
	r.on("mysql.inventory", func(json.RawMessage) (any, error) {
		leftMu.Lock()
		defer leftMu.Unlock()
		var dbs []proto.DatabaseInfo
		for _, d := range left {
			dbs = append(dbs, proto.DatabaseInfo{Name: d})
		}
		return proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: dbs}, nil
	})

	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 1 || !strings.HasSuffix(steps[1].Message, "disalin ulang dari master: a") {
		t.Fatalf("job %+v step %+v", j, steps[1])
	}

	leftMu.Lock()
	left = []string{"a", "b"} // changed before the user approved
	leftMu.Unlock()
	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("second waiting message", func() bool {
		_, steps, _ := e.st.GetJob(id)
		return strings.HasSuffix(steps[1].Message, "disalin ulang dari master: a, b")
	})
	j, _ = e.waitJob(id, store.JobWaiting)
	for _, raw := range r.allParams("mysql.preflight") {
		if strings.Contains(raw, `"confirm_wipe":true`) {
			t.Fatalf("ConfirmWipe sent for a list the user did not see: %s", raw)
		}
	}

	if err := e.r.Approve(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if !pre.ConfirmWipe {
		t.Fatal("ConfirmWipe not sent after approval")
	}
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if snap.DropExisting || !slices.Equal(drop.Databases, []string{"a", "b"}) {
		t.Fatalf("dumpFrom DropExisting %v, dropDatabases %v; want false and the approved [a b]", snap.DropExisting, drop.Databases)
	}
}

// TestConnectWaitsWhenReplicaAdminReplaced: after a CLONE the stored replica
// admin account may no longer log in; the job waits for a reconnect and a
// retry re-checks.
func TestConnectWaitsWhenReplicaAdminReplaced(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	r.fail("mysql.status", "login MySQL gagal: 1045 Access denied for user 'adm'@'localhost' (using password: YES)")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 5 || !strings.Contains(steps[5].Message, "akun admin replika ikut terganti oleh salinan dari master") {
		t.Fatalf("job %+v step %+v", j, steps[5])
	}
	if n := r.count("mysql.configureReplication"); n != 0 {
		t.Fatal("replication configured without a working admin account")
	}
	r.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", ServerUUID: targetUUID})
	if err := e.r.Retry(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// TestHandleEventIgnoresUnknownJobs: events for no live job do nothing.
func TestHandleEventIgnoresUnknownJobs(t *testing.T) {
	e := newEnv(t)
	payload, _ := json.Marshal(proto.JobLog{JobID: "x", Message: "hi"})
	e.r.HandleEvent(42, proto.Event{Type: proto.EventJobLog, JobID: "x", Payload: payload})
	e.r.HandleEvent(42, proto.Event{Type: "other"})
	if n := e.published("job_log"); n != 0 {
		t.Fatalf("published %d job_log for an unknown job", n)
	}
}

// TestRetryFromVerifyRestartsReplication: a failed verify stops replication;
// Retry from that step starts it again before waiting for the sync.
func TestRetryFromVerifyRestartsReplication(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.reply("mysql.waitSynced", proto.WaitSyncedResult{Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "No",
		LastSQLErrno: 1146, LastSQLError: "Table 'a.x' doesn't exist"}})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 6 || !strings.Contains(j.Error, "1146") {
		t.Fatalf("job = %+v", j)
	}
	if n := r.count("mysql.stopReplica"); n != 1 {
		t.Fatalf("stopReplica after the failed verify: %d calls", n)
	}

	// Waiting for the sync after a stop would fail at once without a start.
	var started atomic.Bool
	r.on("mysql.startReplica", func(json.RawMessage) (any, error) {
		started.Store(true)
		return proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}, nil
	})
	r.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		if !started.Load() {
			return proto.WaitSyncedResult{Status: proto.ReplicaStatus{IORunning: "No", SQLRunning: "No"}}, nil
		}
		return proto.WaitSyncedResult{Synced: true, Executed: masterGTID}, nil
	})
	mark, mmark := r.mark(), m.mark()
	if err := e.r.Retry(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica after retry", r.cmds(mark), []string{"mysql.startReplica", "mysql.waitSynced"})
	wantCmds(t, "master after retry", m.cmds(mmark), []string{"mysql.status"})
	if rep, _ := e.st.GetReplicaByServer(r.id); rep.State != store.ReplicaReady {
		t.Fatalf("replica = %+v", rep)
	}
}

// TestFailedTargetCanStartAgain: a target whose attempt failed takes a new
// create_replica job; the failed attempt's replica row is replaced.
func TestFailedTargetCanStartAgain(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.fail("mysql.configure", "tulis konfigurasi gagal: disk penuh")
	first, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(first, store.JobFailed)
	old, err := e.st.GetReplicaByServer(r.id)
	if err != nil || old.State != store.ReplicaFailed {
		t.Fatalf("replica after failure = %+v, %v", old, err)
	}

	r.on("mysql.configure", func(raw json.RawMessage) (any, error) {
		var p proto.ConfigureParams
		json.Unmarshal(raw, &p)
		return proto.ConfigureResult{NeedsRestart: true, ServerID: p.ServerID}, nil
	})
	second, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"b"}})
	if err != nil {
		t.Fatalf("second StartCreateReplica: %v", err)
	}
	e.waitJob(second, store.JobSucceeded)
	rep, err := e.st.GetReplicaByServer(r.id)
	if err != nil || rep.ID == old.ID || rep.State != store.ReplicaReady || !slices.Equal(rep.Databases, []string{"b"}) {
		t.Fatalf("replica = %+v (old id %d), %v", rep, old.ID, err)
	}
	// Only a failed attempt is replaced: a ready replica still refuses.
	if _, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id}); err == nil {
		t.Fatal("a ready replica accepted a new create_replica")
	}
}

// TestFormConfirmWipeStillShowsLeftovers: the form's ConfirmWipe does not
// confirm dropping databases the job has not shown yet.
func TestFormConfirmWipeStillShowsLeftovers(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	scriptLeftovers(r, "dump", "a")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 1 || !strings.HasSuffix(steps[1].Message, "disalin ulang dari master: a") {
		t.Fatalf("job %+v, step %+v", j, steps[1])
	}
	for _, raw := range r.allParams("mysql.preflight") {
		if strings.Contains(raw, `"confirm_wipe":true`) {
			t.Fatalf("ConfirmWipe sent before the list was shown: %s", raw)
		}
	}
	if r.count("mysql.dropDatabases") != 0 {
		t.Fatal("databases dropped before the approval")
	}
}
