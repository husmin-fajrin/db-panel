package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// replicaCmds is what a target with MySQL receives for a full run.
// A CLONE is followed by setReadOnly (the agent lifted the lock for it).
func replicaCmds(snapshot string) []string {
	cmds := []string{"probe", "mysql.preflight", "mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.status", snapshot}
	if snapshot == "mysql.cloneFrom" {
		cmds = append(cmds, "mysql.setReadOnly")
	}
	return append(cmds, "mysql.tlsRequest", "mysql.tlsInstall", "mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced")
}

func allDone(install string) []string {
	return []string{store.StepDone, store.StepDone, install, store.StepDone, store.StepDone, store.StepDone, store.StepDone,
		store.StepDone}
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
	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	j, steps = e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepSkipped)) {
		t.Fatalf("steps = %v", got)
	}
	// The approval of the CLONE starts a new run: the preflight runs again
	// before CLONE replaces the target's data (spec 5.6).
	want := replicaCmds("mysql.cloneFrom")
	clone := slices.Index(want, "mysql.cloneFrom")
	wantCmds(t, "replica", r.cmds(0), slices.Insert(want, clone, "mysql.preflight"))
	// Siapkan replika sweeps leftover dump accounts also before a CLONE.
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.sweepDumpUsers", "mysql.status", "mysql.recordReplicas"})

	var pre proto.PreflightParams
	r.params("mysql.preflight", &pre)
	if pre.Role != "replica" || pre.PeerHost != "10.0.0.1" || pre.PeerPort != 3306 || pre.PeerVersion != "8.4.3" ||
		pre.PeerServerUUID != masterUUID || pre.PeerDataBytes != 300 || !slices.Equal(pre.PeerDatabases, []string{"a", "b"}) ||
		len(pre.Databases) != 0 || pre.ConfirmWipe {
		t.Fatalf("replica preflight params = %+v", pre)
	}
	// The master's lower_case_table_names goes to the replica's checks.
	if pre.PeerLowerCaseTableNames == nil || *pre.PeerLowerCaseTableNames != 0 {
		t.Fatalf("replica preflight lower_case_table_names = %v", pre.PeerLowerCaseTableNames)
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
	// CLONE reads with the replication account (BACKUP_ADMIN), over TLS as
	// the master offers it.
	if snap.Host != "10.0.0.1" || snap.Port != 3306 || snap.User != "dbpanel_repl" || snap.Password != replPW ||
		len(snap.Databases) != 0 || snap.EstimateBytes != 300 || !snap.SSL {
		t.Fatalf("cloneFrom params = %+v", snap)
	}
	var src proto.ReplicationSourceParams
	r.params("mysql.configureReplication", &src)
	if src.Host != "10.0.0.1" || src.Port != 3306 || src.User != "dbpanel_repl" || src.Password != replPW || !src.SSL ||
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

// wantDumpAccount checks that the dump read the master with a temporary
// account (SEC-27): dbpanel_dump_<8 hex> with a fresh 24-character password,
// created on the master for the replica's address before the dump and
// dropped after it; never the replication account.
func wantDumpAccount(t *testing.T, m *fakeAgent, snap proto.SnapshotParams, replicaAddr string) {
	t.Helper()
	if !regexp.MustCompile(`^dbpanel_dump_[0-9a-f]{8}$`).MatchString(snap.User) ||
		!regexp.MustCompile(`^[A-Za-z0-9]{24}$`).MatchString(snap.Password) || snap.Password == replPW {
		t.Fatalf("dump account = %q / %q, want a temporary one", snap.User, snap.Password)
	}
	var cu proto.DumpUserParams
	m.params("mysql.createDumpUser", &cu)
	if cu.User != snap.User || cu.Password != snap.Password || cu.FromHost != replicaAddr || cu.RequireSSL != snap.SSL {
		t.Fatalf("createDumpUser params = %+v, dump with %+v", cu, snap)
	}
	var du proto.DropUserParams
	m.params("mysql.dropUser", &du)
	if du.User != snap.User || du.FromHost != replicaAddr {
		t.Fatalf("dropUser params = %+v", du)
	}
	if m.count("mysql.createDumpUser") != m.count("mysql.dropUser") {
		t.Fatalf("dump accounts created %d, dropped %d", m.count("mysql.createDumpUser"), m.count("mysql.dropUser"))
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
	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
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
		"mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.status", "mysql.dropDatabases", "mysql.dumpFrom",
		"mysql.tlsRequest", "mysql.tlsInstall", "mysql.status", "mysql.configureReplication", "mysql.startReplica",
		"mysql.startReplica", "mysql.waitSynced"})
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
	if !slices.Equal(snap.Databases, []string{"a"}) || snap.EstimateBytes != 100 || snap.DropExisting || !snap.SSL {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
	wantDumpAccount(t, m, snap, "10.0.0.2")
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
		"mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.status", "mysql.cloneFrom", "mysql.setReadOnly",
		"mysql.tlsRequest", "mysql.tlsInstall", "mysql.status", "mysql.configureReplication", "mysql.startReplica",
		"mysql.startReplica", "mysql.waitSynced"})
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
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, steps = e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, allDone(store.StepSkipped)) {
		t.Fatalf("steps = %v", got)
	}
	wantCmds(t, "replica after retry", r.cmds(mark), []string{"mysql.dumpFrom", "mysql.tlsRequest", "mysql.tlsInstall",
		"mysql.status", "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced"})
	// The same dump, each attempt with its own temporary account; the
	// failed attempt's account was dropped when it failed.
	dumps := r.allParams("mysql.dumpFrom")
	if len(dumps) != 2 {
		t.Fatalf("dumpFrom calls: %v", dumps)
	}
	var d0, d1 proto.SnapshotParams
	json.Unmarshal([]byte(dumps[0]), &d0)
	json.Unmarshal([]byte(dumps[1]), &d1)
	if d0.User == d1.User || d0.Password == d1.Password {
		t.Fatalf("retry reused the dump account: %s", dumps)
	}
	d0.User, d0.Password, d1.User, d1.Password = "", "", "", ""
	if !reflect.DeepEqual(d0, d1) {
		t.Fatalf("dumpFrom params differ on retry: %+v vs %+v", d0, d1)
	}
	if c, d := m.count("mysql.createDumpUser"), m.count("mysql.dropUser"); c != 2 || d != 2 {
		t.Fatalf("dump accounts created %d, dropped %d; want 2 and 2", c, d)
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
	wantDumpAccount(t, m, snap, "10.0.0.2")
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
	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
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

	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
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
// admin account may no longer log in; the job waits for a reconnect at the
// first step that logs in (the replica's certificate), and a retry
// re-checks.
func TestConnectWaitsWhenReplicaAdminReplaced(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	// The stored admin account works until the CLONE brings the master's accounts.
	var cloned, reconnected atomic.Bool
	r.job("mysql.cloneFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		cloned.Store(true)
		return proto.SnapshotResult{Method: "clone", GTIDExecuted: masterGTID}, nil
	})
	const refused = "login MySQL gagal: 1045 Access denied for user 'adm'@'localhost' (using password: YES)"
	r.on("mysql.status", func(json.RawMessage) (any, error) {
		if cloned.Load() && !reconnected.Load() {
			return nil, &commands.ParamError{Msg: refused}
		}
		return proto.MySQLStatus{Version: "8.4.3", ServerUUID: targetUUID}, nil
	})
	install := r.script["mysql.tlsInstall"]
	r.on("mysql.tlsInstall", func(raw json.RawMessage) (any, error) {
		if cloned.Load() && !reconnected.Load() {
			return nil, &commands.ParamError{Msg: refused}
		}
		return install(raw)
	})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, ConfirmWipe: true})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 5 || !strings.Contains(steps[5].Message, "membawa akun-akun MySQL dari master db-master") ||
		!strings.Contains(j.ParamsJSON, `"wait_reason":"reconnect"`) {
		t.Fatalf("job %+v step %+v", j, steps[5])
	}
	if n := r.count("mysql.configureReplication"); n != 0 {
		t.Fatal("replication configured without a working admin account")
	}
	if _, err := e.st.GetServerTLS(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("server_tls recorded for a refused install: %v", err)
	}
	reconnected.Store(true)
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	if _, err := e.st.GetServerTLS(r.id); err != nil {
		t.Fatalf("server_tls after the reconnect: %v", err)
	}
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
	if j.CurrentStep != 7 || !strings.Contains(j.Error, "1146") {
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
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica after retry", r.cmds(mark), []string{"mysql.startReplica", "mysql.waitSynced"})
	wantCmds(t, "master after retry", m.cmds(mmark), []string{"mysql.status", "mysql.recordReplicas"})
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

// TestNewAttemptResetsStaleReplication: an attempt that failed after the
// source was configured leaves it on the target; the next attempt resets
// replication before its snapshot, so the dump starts the GTID history
// afresh. A target without a source is never reset.
func TestNewAttemptResetsStaleReplication(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	var source atomic.Bool // the target has a replication source configured
	r.on("mysql.status", func(json.RawMessage) (any, error) {
		st := proto.MySQLStatus{Version: "8.4.3", ServerUUID: targetUUID}
		if source.Load() {
			st.Replica = &proto.ReplicaStatus{IORunning: "No", SQLRunning: "No", SourceHost: "10.0.0.1", SourcePort: 3306}
		}
		return st, nil
	})
	r.on("mysql.resetReplica", func(json.RawMessage) (any, error) {
		source.Store(false)
		return nil, nil
	})
	// CHANGE SOURCE succeeded, then the filter failed.
	r.on("mysql.configureReplication", func(json.RawMessage) (any, error) {
		source.Store(true)
		return nil, &commands.ParamError{Msg: "CHANGE REPLICATION FILTER: Error 1234"}
	})
	first, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := e.waitJob(first, store.JobFailed); j.CurrentStep != 6 {
		t.Fatalf("first attempt failed at step %d, want 6", j.CurrentStep)
	}
	if n := r.count("mysql.resetReplica"); n != 0 {
		t.Fatalf("a target without a source got %d resetReplica", n)
	}

	r.reply("mysql.configureReplication", proto.ReplicaStatus{})
	mark := r.mark()
	second, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(second, store.JobSucceeded)
	wantCmds(t, "replica, second attempt", r.cmds(mark), []string{"probe", "mysql.preflight",
		"mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.status", "mysql.resetReplica", "mysql.dumpFrom",
		"mysql.tlsRequest", "mysql.tlsInstall", "mysql.status", "mysql.configureReplication", "mysql.startReplica",
		"mysql.startReplica", "mysql.waitSynced"})
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if snap.DropExisting || !slices.Equal(snap.Databases, []string{"b"}) {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
	if log := e.jobLog(second); !strings.Contains(log, "Pengaturan replikasi lama") {
		t.Fatalf("job log does not mention the reset:\n%s", log)
	}
}

// TestCancelCreateReplicaReleasesRole: a canceled create_replica removes
// the replica row and releases the role the target's agent pinned.
func TestCancelCreateReplicaReleasesRole(t *testing.T) {
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
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobCanceled)
	if n := r.count("mysql.releaseRole"); n != 1 {
		t.Fatalf("releaseRole called %d times; commands %v", n, r.cmds(0))
	}
	if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replica row after cancel: %v", err)
	}
	e.waitFor("JobEnded on cancel", func() bool {
		_, ended := e.alerts.lists()
		return slices.Contains(ended, fmt.Sprintf("%d %s", r.id, id))
	})

	// A refused release is only logged.
	r.fail("mysql.releaseRole", "replikasi masih dikonfigurasi")
	r.blockingJob("mysql.dumpFrom")
	id, err = e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("dumpFrom job", func() bool { return r.startedJobs() == 2 })
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobCanceled)
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after cancel = %q", srv.Role)
	}
}

// TestJobFailedReachesAlerts: a failed job opens job_failed with the
// title of the step that failed; its successful retry closes it.
func TestJobFailedReachesAlerts(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckFail, Method: "dump",
		Checks: []proto.Check{{ID: "disk", Status: proto.CheckFail, Title: "Ruang disk", Detail: "penuh"}}})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	// The Monitor hears it right after the job is stored as failed.
	e.waitFor("JobFailed", func() bool { f, _ := e.alerts.lists(); return len(f) > 0 })
	failed, ended := e.alerts.lists()
	if want := fmt.Sprintf("%d %s create_replica Periksa server tujuan", r.id, id); !slices.Equal(failed, []string{want}) {
		t.Fatalf("JobFailed = %v, want %q", failed, want)
	}
	if len(ended) != 0 {
		t.Fatalf("JobEnded on a failure: %v", ended)
	}
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckOK, Method: "dump"})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitFor("JobEnded", func() bool { _, ended := e.alerts.lists(); return len(ended) > 0 })
	if _, ended := e.alerts.lists(); !slices.Equal(ended, []string{fmt.Sprintf("%d %s", r.id, id)}) {
		t.Fatalf("JobEnded = %v", ended)
	}
}

// TestCancelAfterConnectResetsReplication: a create_replica canceled once
// replication was configured leaves no replica following the master: the
// target stops and resets replication before releasing its role.
func TestCancelAfterConnectResetsReplication(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.reply("mysql.resetReplica", nil)
	verifying, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		once.Do(func() { close(verifying) })
		<-release
		return proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
			Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}}, nil
	})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	<-verifying
	mark := r.mark()
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.waitJob(id, store.JobCanceled)
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.stopReplica", "mysql.resetReplica", "mysql.releaseRole"})
	if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replica row after cancel: %v", err)
	}

	// A failing reset is only logged; the release is still tried.
	r.fail("mysql.resetReplica", "RESET REPLICA gagal")
	verifying, release = make(chan struct{}), make(chan struct{})
	once = sync.Once{}
	id, err = e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	<-verifying
	mark = r.mark()
	if err := e.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.waitJob(id, store.JobCanceled)
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.stopReplica", "mysql.resetReplica", "mysql.releaseRole"})
}

// TestStartRebuildRechecksConnections: starting the rebuild of the old
// master reads its connections again; anything but a complete, empty,
// typed list is refused without AcceptConnections (spec 5.5), and an
// agent that cannot be read counts as not clear.
func TestStartRebuildRechecksConnections(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if err := e.st.SetCutoverPending(g.topo.ID, false); err != nil {
		t.Fatal(err)
	}
	row := proto.ConnectionRow{Host: "10.0.0.9", User: "app", DB: "simrs", Count: 1, Kind: "sql"}
	typed := func(rows ...proto.ConnectionRow) proto.ConnectionsResult {
		return proto.ConnectionsResult{Rows: rows, Typed: true, TakenAt: time.Now()}
	}
	states := map[string]func(){
		"one connection": func() { g.m.reply("mysql.connections", typed(row)) },
		"only local admin sessions": func() {
			g.m.reply("mysql.connections", typed(proto.ConnectionRow{Host: "localhost", User: "dbpanel_admin", Count: 1, Kind: "sql", LocalAdmin: true}))
		},
		"old agent (not typed)": func() {
			g.m.reply("mysql.connections", proto.ConnectionsResult{TakenAt: time.Now()})
		},
		"incomplete": func() {
			r := typed()
			r.Incomplete = "thread hilang"
			g.m.reply("mysql.connections", r)
		},
		"unreadable": func() {
			r := typed()
			r.Unreadable = "tidak bisa membaca"
			g.m.reply("mysql.connections", r)
		},
		"command error": func() { g.m.fail("mysql.connections", "login admin gagal") },
	}
	for name, set := range states {
		set()
		_, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
			RebuildOldMaster: true, ConfirmWipe: true})
		if !errors.Is(err, ErrConnectionsRemain) {
			t.Errorf("%s: StartCreateReplica = %v, want ErrConnectionsRemain", name, err)
		}
	}
	// An offline agent is not "no connections" either.
	g.m.detach()
	if _, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
		RebuildOldMaster: true, ConfirmWipe: true}); !errors.Is(err, ErrConnectionsRemain) {
		t.Errorf("offline agent: StartCreateReplica = %v, want ErrConnectionsRemain", err)
	}
	g.m.attach()
	if n := g.m.count("mysql.preflight"); n != 0 {
		t.Fatalf("%d preflights after refusals: nothing may run on the old master", n)
	}
	if _, err := e.st.GetReplicaByServer(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused rebuild registered a replica: %v", err)
	}

	// Acknowledged: the job is created and keeps the acknowledgement.
	g.m.reply("mysql.connections", typed(row))
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.c.reply("mysql.status", masterStatus())
	g.c.reply("mysql.createDumpUser", nil)
	g.c.reply("mysql.dropUser", nil)
	g.c.reply("mysql.sweepDumpUsers", proto.SweepDumpUsersResult{})
	reads := g.m.count("mysql.connections")
	rebuildID, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
		RebuildOldMaster: true, ConfirmWipe: true, AcceptConnections: true})
	if err != nil {
		t.Fatalf("acknowledged rebuild: %v", err)
	}
	// The acknowledged start does not read the connections: the
	// acknowledgement covers whatever is there (ruling 14).
	if n := g.m.count("mysql.connections"); n != reads {
		t.Fatalf("acknowledged start read the connections %d times, want 0", n-reads)
	}
	j, _, err := e.st.GetJob(rebuildID)
	if err != nil || !strings.Contains(j.ParamsJSON, `"accept_connections":true`) {
		t.Fatalf("job params = %q, %v; want the acknowledgement kept", j.ParamsJSON, err)
	}
	e.waitJob(rebuildID, store.JobSucceeded)
}
