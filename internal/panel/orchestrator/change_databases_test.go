package orchestrator

import (
	"context"
	"encoding/json"
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
	return []string{"mysql.setReplicationFilter", "mysql.waitSynced", "mysql.stopReplica", "mysql.dumpFrom", "mysql.startUntil",
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
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status", "mysql.status"})
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
	if snap.Host != "10.0.0.1" || snap.Port != 3306 || snap.User != "dbpanel_repl" || snap.Password != replPW ||
		!slices.Equal(snap.Databases, []string{"b"}) || snap.DropExisting || snap.EstimateBytes != 200 {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
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
	wantCmds(t, "master", m.cmds(0), []string{"mysql.status"})
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.setReplicationFilter", "mysql.waitSynced"})
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
	if err := e.r.Retry(id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(rr), []string{"mysql.startUntil", "mysql.setReplicationFilter", "mysql.startReplica", "mysql.waitSynced"})
	wantCmds(t, "master", m.cmds(mr), []string{"mysql.status"})
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
	if _, err := e.r.StartRemoveDatabases(ctx, two.id, []string{"b"}, false); err == nil || !strings.Contains(err.Error(), "masih punya pekerjaan") {
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
