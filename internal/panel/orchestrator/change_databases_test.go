package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// dumpGTID is the master's GTID set at the dump of the new databases (P2).
const dumpGTID = masterUUID + ":1-125"

// followingReplica is a ready replica of topo following dbs (none = all),
// whose agent answers every command of the database jobs.
func (e *env) followingReplica(topo store.Topology, dbs ...string) *fakeAgent {
	e.t.Helper()
	r := e.replicaAgent("replica1", "dump")
	if _, err := e.st.AddReplica(topo.ID, r.id, dbs); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.SetReplicaState(r.id, store.ReplicaReady, ""); err != nil {
		e.t.Fatal(err)
	}
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Logf("info", "dump dari master dimulai")
		return proto.SnapshotResult{Method: "dump", Bytes: 200, GTIDExecuted: dumpGTID}, nil
	})
	r.reply("mysql.startUntil", proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "No"})
	r.reply("mysql.setReplicationFilter", proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"})
	// The fresh read of the replica's databases (addExisting): the ones it
	// follows.
	replicaHas(r, dbs...)
	return r
}

func (e *env) replica(serverID int64) store.Replica {
	e.t.Helper()
	rep, err := e.st.GetReplicaByServer(serverID)
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

func addDatabasesCmds() []string {
	return []string{"mysql.inventory", "mysql.setReplicationFilter", "mysql.waitSynced", "mysql.stopReplica", "mysql.dumpFrom", "mysql.startUntil",
		"mysql.setReplicationFilter", "mysql.startReplica", "mysql.waitSynced"}
}

func TestAddDatabasesSequence(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	release := make(chan struct{})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		<-release
		return proto.SnapshotResult{Method: "dump", Bytes: 200, GTIDExecuted: dumpGTID}, nil
	})

	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	if rep := e.replica(r.id); rep.State != store.ReplicaChanging || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica during the job = %+v", rep)
	}
	close(release)
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindAddDatabases || j.TopologyID != topo.ID {
		t.Fatalf("job = %+v", j)
	}
	want := []string{store.StepDone, store.StepDone, store.StepDone, store.StepDone, store.StepDone, store.StepDone}
	if got := stepStatuses(steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v", got)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"Menunggu replika menyusul", "Hentikan replikasi", "Salin database baru",
		"Putar ulang sampai titik dump", "Perluas filter", "Verifikasi"}) {
		t.Fatalf("step names = %v", names)
	}
	// The sweep of leftover dump accounts first, catch-up, TLS for the
	// dump, the dump account around the dump, verify.
	wantCmds(t, "master", m.cmds(0), []string{"mysql.sweepDumpUsers", "mysql.status", "mysql.status", "mysql.createDumpUser",
		"mysql.dropUser", "mysql.status", "mysql.recordReplicas"})
	wantCmds(t, "replica", r.cmds(0), addDatabasesCmds())

	// Step 0 first puts back the filter of the databases followed now.
	filters := r.allParams("mysql.setReplicationFilter")
	var f0 proto.ReplicationFilterParams
	json.Unmarshal([]byte(filters[0]), &f0)
	if len(filters) != 2 || !slices.Equal(f0.Databases, []string{"a"}) {
		t.Fatalf("setReplicationFilter calls = %v", filters)
	}
	waits := r.allParams("mysql.waitSynced")
	var w0 proto.WaitSyncedParams
	json.Unmarshal([]byte(waits[0]), &w0)
	if w0.GTIDSet != masterGTID || w0.TimeoutSec != 60 {
		t.Fatalf("catch-up waitSynced params = %+v", w0)
	}
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if snap.Host != "10.0.0.1" || snap.Port != 3306 || !snap.SSL ||
		!slices.Equal(snap.Databases, []string{"b"}) || snap.DropExisting || snap.EstimateBytes != 200 {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
	wantDumpAccount(t, m, snap, "10.0.0.2")
	var until proto.StartUntilParams
	r.params("mysql.startUntil", &until)
	if until.GTIDSet != dumpGTID || until.TimeoutSec < 1 {
		t.Fatalf("startUntil params = %+v", until)
	}
	var filter proto.ReplicationFilterParams
	r.params("mysql.setReplicationFilter", &filter)
	if !slices.Equal(filter.Databases, []string{"a", "b"}) {
		t.Fatalf("setReplicationFilter params = %+v", filter)
	}
	var w1 proto.WaitSyncedParams
	r.params("mysql.waitSynced", &w1)
	if w1.GTIDSet != masterGTID || w1.TimeoutSec < 1 {
		t.Fatalf("verify waitSynced params = %+v", w1)
	}

	rep := e.replica(r.id)
	if rep.State != store.ReplicaReady || rep.LastError != "" || !slices.Equal(rep.Databases, []string{"a", "b"}) {
		t.Fatalf("replica = %+v", rep)
	}
	if log := e.jobLog(id); strings.Contains(log, replPW) {
		t.Fatalf("job log holds the replication password:\n%s", log)
	}
}

func TestAddDatabasesFailsIfReplicaLagging(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: masterUUID + ":1-90",
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})

	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	const lag = "replika masih tertinggal; coba lagi setelah replika menyusul"
	if j.CurrentStep != 0 || steps[0].Status != store.StepFailed || !strings.Contains(j.Error, lag) {
		t.Fatalf("job = %+v, steps %+v", j, steps)
	}
	wantCmds(t, "master", m.cmds(0), []string{"mysql.sweepDumpUsers", "mysql.status"})
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.inventory", "mysql.setReplicationFilter", "mysql.waitSynced"})
	rep := e.replica(r.id)
	if rep.State != store.ReplicaAttention || !strings.HasPrefix(rep.LastError, "gagal saat Menunggu replika menyusul: ") ||
		!strings.Contains(rep.LastError, lag) || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica = %+v", rep)
	}
}

// A failure after the dump leaves the new database on the replica, says
// so, and a retry continues from the failed step with the dump's GTID set.
func TestAddDatabasesRetryAfterReplayFailure(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.fail("mysql.startUntil", "replika belum mencapai set GTID tujuan dalam 900 detik")

	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 3 {
		t.Fatalf("job = %+v", j)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Database b sudah tersalin ke replika tetapi belum direplikasi") {
		t.Fatalf("job log lacks the leftover note:\n%s", log)
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaAttention ||
		!strings.HasPrefix(rep.LastError, "gagal saat Putar ulang sampai titik dump: ") || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica = %+v", rep)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Replika mungkin masih memutar ulang sampai titik dump") {
		t.Fatalf("job log lacks the replication note:\n%s", log)
	}

	r.reply("mysql.startUntil", proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "No"})
	mr, rr := m.mark(), r.mark()
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(rr), []string{"mysql.startUntil", "mysql.setReplicationFilter", "mysql.startReplica", "mysql.waitSynced"})
	wantCmds(t, "master", m.cmds(mr), []string{"mysql.status", "mysql.recordReplicas"})
	var until proto.StartUntilParams
	r.params("mysql.startUntil", &until)
	if until.GTIDSet != dumpGTID {
		t.Fatalf("startUntil after retry = %+v", until)
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaReady || rep.LastError != "" || !slices.Equal(rep.Databases, []string{"a", "b"}) {
		t.Fatalf("replica = %+v", rep)
	}
}

func TestRemoveDatabasesWithDrop(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")

	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, true)
	if err != nil {
		t.Fatalf("StartRemoveDatabases: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindRemoveDatabases {
		t.Fatalf("job = %+v", j)
	}
	if got := stepStatuses(steps); !slices.Equal(got, []string{store.StepDone, store.StepDone, store.StepDone}) {
		t.Fatalf("steps = %v", got)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.setReplicationFilter", "mysql.startReplica",
		"mysql.stopReplica", "mysql.dropDatabases", "mysql.startReplica", "mysql.waitSynced"})
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status"})
	var filter proto.ReplicationFilterParams
	r.params("mysql.setReplicationFilter", &filter)
	if !slices.Equal(filter.Databases, []string{"a"}) {
		t.Fatalf("setReplicationFilter params = %+v", filter)
	}
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if !slices.Equal(drop.Databases, []string{"b"}) {
		t.Fatalf("dropDatabases params = %+v", drop)
	}
	var w proto.WaitSyncedParams
	r.params("mysql.waitSynced", &w)
	if w.GTIDSet != masterGTID || w.TimeoutSec != 60 {
		t.Fatalf("waitSynced params = %+v", w)
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaReady || !slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica = %+v", rep)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Database dihapus dari replika: b.") {
		t.Fatalf("job log:\n%s", log)
	}
}

func TestRemoveDatabasesKeepsData(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")

	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"a"}, false)
	if err != nil {
		t.Fatalf("StartRemoveDatabases: %v", err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); !slices.Equal(got, []string{store.StepDone, store.StepSkipped, store.StepDone}) {
		t.Fatalf("steps = %v", got)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"Kecilkan filter", "Hapus data", "Verifikasi"}) {
		t.Fatalf("step names = %v", names)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.setReplicationFilter", "mysql.startReplica",
		"mysql.waitSynced"})
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status"})
	var filter proto.ReplicationFilterParams
	r.params("mysql.setReplicationFilter", &filter)
	if !slices.Equal(filter.Databases, []string{"b"}) {
		t.Fatalf("setReplicationFilter params = %+v", filter)
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaReady || !slices.Equal(rep.Databases, []string{"b"}) {
		t.Fatalf("replica = %+v", rep)
	}
	if log := e.jobLog(id); !strings.Contains(log, "tetap ada di replika") {
		t.Fatalf("job log:\n%s", log)
	}
}

// A drop the agent refuses still starts replication again.
func TestRemoveDatabasesDropFailureRestartsReplication(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")
	r.fail("mysql.dropDatabases", "replikasi masih berjalan")

	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, true)
	if err != nil {
		t.Fatalf("StartRemoveDatabases: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 1 || !strings.Contains(j.Error, "replikasi masih berjalan") {
		t.Fatalf("job = %+v", j)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.setReplicationFilter", "mysql.startReplica",
		"mysql.stopReplica", "mysql.dropDatabases", "mysql.startReplica"})
	if rep := e.replica(r.id); rep.State != store.ReplicaAttention || !strings.HasPrefix(rep.LastError, "gagal saat Hapus data: ") ||
		!slices.Equal(rep.Databases, []string{"a", "b"}) {
		t.Fatalf("replica = %+v", rep)
	}
}

func TestChangeDatabasesRefusals(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	other := e.agent("lain", "10.0.0.3")
	all := e.agent("semua", "10.0.0.4")
	if _, err := e.st.AddReplica(topo.ID, all.id, nil); err != nil {
		t.Fatal(err)
	}
	e.st.SetReplicaState(all.id, store.ReplicaReady, "")
	two := e.agent("dua", "10.0.0.5")
	if _, err := e.st.AddReplica(topo.ID, two.id, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	e.st.SetReplicaState(two.id, store.ReplicaReady, "")
	ctx := context.Background()

	adds := []struct {
		server int64
		dbs    []string
		msg    string
	}{
		{all.id, []string{"b"}, "replika ini mengikuti semua database"},
		{other.id, []string{"b"}, "bukan replika"},
		{m.id, []string{"b"}, "bukan replika"},
		{r.id, nil, "pilih minimal satu database"},
		{r.id, []string{"x"}, `database "x" tidak ada di master`},
		{r.id, []string{"a"}, "database a sudah diikuti"},
	}
	for _, c := range adds {
		if _, err := e.r.StartAddDatabases(ctx, c.server, c.dbs); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("StartAddDatabases(%d, %v) = %v, want %q", c.server, c.dbs, err, c.msg)
		}
	}
	removes := []struct {
		server int64
		dbs    []string
		msg    string
	}{
		{all.id, []string{"a"}, "replika ini mengikuti semua database"},
		{r.id, []string{"a"}, "lepas semua database tidak didukung; hapus replika"},
		{two.id, []string{"a", "b"}, "lepas semua database tidak didukung; hapus replika"},
		{two.id, []string{"c"}, "database c tidak diikuti replika ini"},
		{two.id, nil, "pilih minimal satu database"},
	}
	for _, c := range removes {
		if _, err := e.r.StartRemoveDatabases(ctx, c.server, c.dbs, true); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("StartRemoveDatabases(%d, %v) = %v, want %q", c.server, c.dbs, err, c.msg)
		}
	}

	// Not ready: a replica being created, changed or failed.
	for state, label := range map[string]string{store.ReplicaCreating: "sedang dibuat", store.ReplicaChanging: "sedang diubah",
		store.ReplicaFailed: "gagal"} {
		e.st.SetReplicaState(two.id, state, "")
		if _, err := e.r.StartRemoveDatabases(ctx, two.id, []string{"b"}, false); err == nil ||
			!strings.Contains(err.Error(), "replika belum siap (status "+label+")") {
			t.Errorf("remove in state %s: %v", state, err)
		}
	}
	e.st.SetReplicaState(two.id, store.ReplicaReady, "")

	// A server with an unfinished job.
	if err := e.st.CreateJob(store.Job{ID: "busy", Kind: KindPrepareMaster, ServerID: two.id}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartRemoveDatabases(ctx, two.id, []string{"b"}, false); !errors.Is(err, ErrBusy) || err.Error() != "server ini masih punya pekerjaan yang belum selesai" {
		t.Errorf("busy replica: %v", err)
	}

	for _, fa := range []*fakeAgent{all, other, two} {
		if got := fa.cmds(0); len(got) != 0 {
			t.Errorf("%s received %v", fa.name, got)
		}
	}
	if got := e.replica(two.id); got.State != store.ReplicaReady || !slices.Equal(got.Databases, []string{"a", "b"}) {
		t.Errorf("replica dua = %+v", got)
	}
}

// A canceled add job leaves the replica stopped and in state attention; a
// new add job puts the old filter back, which starts replication again,
// and proceeds.
func TestAddDatabasesCancelMarksReplicaAttention(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.blockingJob("mysql.dumpFrom")

	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	e.waitFor("dump started", func() bool { return r.startedJobs() == 1 })
	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	e.waitJob(id, store.JobCanceled)
	if r.count("job.cancel") != 1 {
		t.Fatalf("agent job not canceled; calls %v", r.cmds(0))
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaAttention || rep.LastError != "dibatalkan saat Salin database baru" ||
		!slices.Equal(rep.Databases, []string{"a"}) {
		t.Fatalf("replica = %+v", rep)
	}

	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "dump", Bytes: 200, GTIDExecuted: dumpGTID}, nil
	})
	from := r.mark()
	id, err = e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases on an attention replica: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(from), addDatabasesCmds())
	filters := r.allParams("mysql.setReplicationFilter")
	var first proto.ReplicationFilterParams
	json.Unmarshal([]byte(filters[len(filters)-2]), &first)
	if !slices.Equal(first.Databases, []string{"a"}) {
		t.Fatalf("first filter of the new job = %+v", first)
	}
	if rep := e.replica(r.id); rep.State != store.ReplicaReady || rep.LastError != "" || !slices.Equal(rep.Databases, []string{"a", "b"}) {
		t.Fatalf("replica = %+v", rep)
	}
}

// The failure note describes the state the failed step left: a stop that
// failed may have left replication running, and nothing was copied.
func TestAddDatabasesStopFailureNote(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.fail("mysql.stopReplica", "STOP REPLICA gagal")

	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 1 {
		t.Fatalf("job = %+v", j)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Replikasi mungkin masih berjalan") || strings.Contains(log, "tersalin") {
		t.Fatalf("job log:\n%s", log)
	}
}

// A change job cut off by a panel restart leaves the replica in attention.
func TestResumeMarksChangingReplicaAttention(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	const id = "cccccccccccccccccccccccccccccccc"
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindAddDatabases, ServerID: r.id, ParamsJSON: "{}"},
		addDatabasesStepNames()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobRunning, 2, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	e.st.SetReplicaState(r.id, store.ReplicaChanging, "")
	e.r.Resume(context.Background())
	e.waitJob(id, store.JobFailed)
	if rep := e.replica(r.id); rep.State != store.ReplicaAttention ||
		rep.LastError != "gagal saat Salin database baru: "+resumeError {
		t.Fatalf("replica = %+v", rep)
	}
}

// existingText is the spec's 5.6 text for a database X that already
// exists on the replica.
func existingText(x string) string {
	return "Database " + x + " sudah ada di replika ini. Panel tidak bisa memastikan isinya sama dengan master. " +
		"Isinya di replika akan dibuang permanen, lalu diganti salinan dari master."
}

// masterHas adds dbs to the master's stored inventory.
func (e *env) masterHas(masterID int64, dbs ...string) {
	e.t.Helper()
	inv, err := e.st.GetInventory(masterID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, d := range dbs {
		inv.Databases = append(inv.Databases, proto.DatabaseInfo{Name: d, Bytes: 10})
	}
	if err := e.st.SetInventory(inv); err != nil {
		e.t.Fatal(err)
	}
}

// replicaHas scripts the replica's fresh inventory (mysql.inventory).
func replicaHas(r *fakeAgent, dbs ...string) {
	var infos []proto.DatabaseInfo
	for _, d := range dbs {
		infos = append(infos, proto.DatabaseInfo{Name: d})
	}
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: infos})
}

func (e *env) jobState(id string) jobState {
	e.t.Helper()
	j, _, err := e.st.GetJob(id)
	if err != nil {
		e.t.Fatal(err)
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

// wantNoDrop fails when the replica got a drop or a dump since mark.
func wantNoDrop(t *testing.T, r *fakeAgent, mark int) {
	t.Helper()
	for _, c := range r.cmds(mark) {
		if c == "mysql.dropDatabases" || c == "mysql.dumpFrom" {
			t.Fatalf("replica got %s while the job waits: %v", c, r.cmds(mark))
		}
	}
}

// waitingAddExisting starts adding rekap to a replica following a whose
// MySQL already has rekap; the job waits at step 0.
func waitingAddExisting(t *testing.T, e *env, add ...string) (m, r *fakeAgent, id string) {
	t.Helper()
	m, topo := e.readyMaster("db-master")
	e.masterHas(m.id, "rekap", "rekap2")
	r = e.followingReplica(topo, "a")
	replicaHas(r, "a", "rekap")
	if len(add) == 0 {
		add = []string{"rekap"}
	}
	id, err := e.r.StartAddDatabases(context.Background(), r.id, add)
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	e.waitJob(id, store.JobWaiting)
	return m, r, id
}

// TestAddExistingWaitsBoundToHash (spec 5.6, R-058): adding a database the
// replica already has (compared without case) waits before anything else,
// with the spec's text per database, bound to the hash of that list; a
// list that changed by the approval waits again with the new list.
func TestAddExistingWaitsBoundToHash(t *testing.T) {
	e := newEnv(t)
	_, r, id := waitingAddExisting(t, e, "rekap", "rekap2")
	j, steps, _ := e.st.GetJob(id)
	if j.CurrentStep != 0 || steps[0].Status != store.StepWaiting || steps[0].Message != existingText("rekap") {
		t.Fatalf("job = %+v, step 0 = %+v", j, steps[0])
	}
	st := e.jobState(id)
	if st.WaitReason != WaitAddExisting || !slices.Equal(st.AddExisting, []string{"rekap"}) ||
		st.AddExistingHash != hashList([]string{"rekap"}) || len(st.AddDrop) != 0 {
		t.Fatalf("state = %+v", st)
	}
	// Nothing but the fresh read reached the replica.
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.inventory"})
	if drops, err := e.r.DropsData(id); err != nil || !drops {
		t.Fatalf("DropsData = %v, %v; want true", drops, err)
	}

	// By the approval the replica also has Rekap2: the approval of [rekap]
	// does not cover it; the job waits again with both, in the requested
	// spelling.
	replicaHas(r, "a", "rekap", "Rekap2")
	mark := r.mark()
	if err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: []string{"rekap"}}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	e.waitFor("the second wait", func() bool { return len(e.jobState(id).AddExisting) == 2 })
	_, steps = e.waitJob(id, store.JobWaiting)
	if want := existingText("rekap") + "\n\n" + existingText("rekap2"); steps[0].Message != want {
		t.Fatalf("step 0 message = %q, want %q", steps[0].Message, want)
	}
	st = e.jobState(id)
	if !slices.Equal(st.AddExisting, []string{"rekap", "rekap2"}) || st.AddExistingHash != hashList([]string{"rekap", "rekap2"}) ||
		len(st.AddDrop) != 0 {
		t.Fatalf("state = %+v", st)
	}
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.inventory"})
	wantNoDrop(t, r, 0)
}

// TestApproveAddExistingNeedsTypedNames (spec 5.10): the approval needs a
// fresh token and every database of the list typed exactly; a refusal
// changes nothing.
func TestApproveAddExistingNeedsTypedNames(t *testing.T) {
	e := newEnv(t)
	_, r, id := waitingAddExisting(t, e)
	mark := r.mark()
	const wrong = "Nama database yang diketik tidak cocok dengan daftar; ketik nama setiap database persis"
	if err := e.r.Approve(id, JobAck{TypedDatabases: []string{"rekap"}}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Approve without a token = %v, want ErrJobNeedsConfirm", err)
	}
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without a token = %v, want ErrJobNeedsConfirm", err)
	}
	for _, typed := range [][]string{nil, {""}, {"Rekap"}, {"rekap", "lain"}, {"lain"}, {"rekap2"}} {
		err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: typed})
		if err == nil || err.Error() != wrong {
			t.Fatalf("Approve typing %q = %v, want %q", typed, err, wrong)
		}
	}
	j, _, _ := e.st.GetJob(id)
	if st := e.jobState(id); j.Status != store.JobWaiting || !st.ConfirmedAt.IsZero() || len(st.AddDrop) != 0 {
		t.Fatalf("after refusals: job %s, state %+v", j.Status, st)
	}
	if got := r.cmds(mark); len(got) != 0 {
		t.Fatalf("refused approvals reached the replica: %v", got)
	}

	if err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: []string{" rekap "}}); err != nil {
		t.Fatalf("Approve with the typed name: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if st := e.jobState(id); !slices.Equal(st.AddDrop, []string{"rekap"}) || st.ConfirmedAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

// TestAddExistingDropsBeforeDump: after the approval, replication stops,
// exactly the approved list is dropped, and the dump loads without
// DropExisting.
func TestAddExistingDropsBeforeDump(t *testing.T) {
	e := newEnv(t)
	_, r, id := waitingAddExisting(t, e)
	mark := r.mark()
	if err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: []string{"rekap"}}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.inventory", "mysql.setReplicationFilter", "mysql.waitSynced",
		"mysql.stopReplica", "mysql.dropDatabases", "mysql.dumpFrom", "mysql.startUntil", "mysql.setReplicationFilter",
		"mysql.startReplica", "mysql.waitSynced"})
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if !slices.Equal(drop.Databases, []string{"rekap"}) {
		t.Fatalf("dropDatabases = %+v", drop)
	}
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if snap.DropExisting || !slices.Equal(snap.Databases, []string{"rekap"}) {
		t.Fatalf("dumpFrom = %+v", snap)
	}
	if log := e.jobLog(id); !strings.Contains(log, "rekap") || !strings.Contains(log, "dibuang") {
		t.Fatalf("job log does not name the drop:\n%s", log)
	}
}

// TestAddExistingRetryRechecks: a retry from the dump reads the replica's
// databases again; a list other than the approved one waits again, also
// on a retry of that wait, and nothing is dropped meanwhile.
func TestAddExistingRetryRechecks(t *testing.T) {
	e := newEnv(t)
	_, r, id := waitingAddExisting(t, e, "rekap", "rekap2")
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return nil, errors.New("koneksi ke master terputus")
	})
	if err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: []string{"rekap"}}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != stepAddDump {
		t.Fatalf("job = %+v", j)
	}
	// The failed dump left rekap2 on the replica as well.
	replicaHas(r, "a", "rekap", "rekap2")
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "dump", Bytes: 200, GTIDExecuted: dumpGTID}, nil
	})
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without a token = %v", err)
	}
	mark := r.mark()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	want := existingText("rekap") + "\n\n" + existingText("rekap2")
	if j.CurrentStep != stepAddDump || steps[stepAddDump].Message != want {
		t.Fatalf("job = %+v, step = %+v", j, steps[stepAddDump])
	}
	wantNoDrop(t, r, mark)
	// A retry of that wait (no typed names) waits again.
	again := r.mark()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry of the wait: %v", err)
	}
	e.waitFor("the retried wait", func() bool { return slices.Contains(r.cmds(again), "mysql.inventory") })
	e.waitJob(id, store.JobWaiting)
	wantNoDrop(t, r, mark)

	if err := e.r.Approve(id, JobAck{Confirmed: true, TypedDatabases: []string{"rekap2", "rekap"}}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if !slices.Equal(drop.Databases, []string{"rekap", "rekap2"}) {
		t.Fatalf("dropDatabases = %+v", drop)
	}
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.inventory", "mysql.inventory", "mysql.inventory",
		"mysql.dropDatabases", "mysql.dumpFrom", "mysql.startUntil", "mysql.setReplicationFilter", "mysql.startReplica",
		"mysql.waitSynced"})
}

// TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile (FR-5): a job whose
// first run found nothing to drop fails at the dump; meanwhile a database
// of that name appears on the replica. The Retry from the dump step reads
// the replica again and waits for the typed approval: nothing is dumped
// over it.
func TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	e.masterHas(m.id, "rekap")
	r := e.followingReplica(topo, "a")
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return nil, errors.New("koneksi ke master terputus")
	})
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"rekap"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != stepAddDump {
		t.Fatalf("job = %+v", j)
	}
	e.waitIdle(id)
	replicaHas(r, "a", "rekap")
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.SnapshotResult{Method: "dump", Bytes: 200, GTIDExecuted: dumpGTID}, nil
	})
	mark := r.mark()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != stepAddDump || steps[stepAddDump].Message != existingText("rekap") {
		t.Fatalf("job = %+v, step = %+v", j, steps[stepAddDump])
	}
	wantNoDrop(t, r, mark)
}

// manyDatabases is n database infos db0000, db0001, ...
func manyDatabases(n int, also ...string) []proto.DatabaseInfo {
	var out []proto.DatabaseInfo
	for i := range n {
		out = append(out, proto.DatabaseInfo{Name: fmt.Sprintf("db%04d", i)})
	}
	for _, d := range also {
		out = append(out, proto.DatabaseInfo{Name: d})
	}
	return out
}

// TestAddExistingRefusesTruncatedInventory (FR-6): a replica inventory
// longer than the panel keeps is cut; the add job then cannot know whether
// a database it adds exists, and fails instead of loading over it.
func TestAddExistingRefusesTruncatedInventory(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	e.masterHas(m.id, "zzz")
	r := e.followingReplica(topo, "a")
	r.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"},
		Databases: manyDatabases(maxInventoryDBs, "a", "zzz")})
	mark := r.mark()
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"zzz"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if !strings.Contains(j.Error, "lebih dari") {
		t.Fatalf("job error = %q", j.Error)
	}
	wantNoDrop(t, r, mark)
}

// TestEnforceWipeOnlyRefusesTruncatedInventory (FR-6): a master inventory
// cut at the panel's limit refuses a job with a wipe list.
func TestEnforceWipeOnlyRefusesTruncatedInventory(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}}, "a")
	m.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"},
		Databases: manyDatabases(maxInventoryDBs, "a")})
	got := failedResync(t, e, r, []string{"a"}, nil)
	if !strings.Contains(got, "lebih dari") {
		t.Fatalf("job error = %q", got)
	}
}

// TestAddExistingRefusesFollowedNameIgnoringCase (ledger Task 13 m3):
// adding Rekap to a replica that follows rekap would drop rekap where
// names compare without case: the job fails and drops nothing.
func TestAddExistingRefusesFollowedNameIgnoringCase(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	e.masterHas(m.id, "rekap", "Rekap")
	r := e.followingReplica(topo, "rekap")
	replicaHas(r, "rekap")
	mark := r.mark()
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"Rekap"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if !strings.Contains(j.Error, "rekap") || !strings.Contains(j.Error, "sudah diikuti") {
		t.Fatalf("job error = %q", j.Error)
	}
	wantNoDrop(t, r, mark)
}
