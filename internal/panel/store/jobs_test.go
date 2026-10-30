package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newJob(t *testing.T, s *Store, serverID int64, kind string, created time.Time, steps ...string) Job {
	t.Helper()
	j := Job{ID: uuid.NewString(), Kind: kind, ServerID: serverID, ParamsJSON: `{"a":1}`, CreatedAt: created}
	if err := s.CreateJob(j, steps); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return j
}

func TestJobLifecycle(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	j := newJob(t, s, srv.ID, "create-replica", t0, "preflight", "dump", "restore")

	got, steps, err := s.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != JobQueued || got.CurrentStep != 0 || got.Kind != "create-replica" || got.ServerID != srv.ID ||
		got.ParamsJSON != `{"a":1}` || !got.CreatedAt.Equal(t0) || got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("job = %+v", got)
	}
	if len(steps) != 3 || steps[0].Name != "preflight" || steps[2].Name != "restore" {
		t.Fatalf("steps = %+v", steps)
	}
	for i, st := range steps {
		if st.Idx != i || st.JobID != j.ID || st.Status != StepPending {
			t.Fatalf("step %d = %+v", i, st)
		}
	}
	if _, _, err := s.GetJob("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJob missing: %v", err)
	}

	t1 := t0.Add(time.Minute)
	if err := s.UpdateJob(j.ID, JobRunning, 1, "", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStep(j.ID, 0, StepDone, "ok", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStep(j.ID, 1, StepRunning, "dumping", t1); err != nil {
		t.Fatal(err)
	}
	t2 := t1.Add(time.Minute)
	if err := s.UpdateJob(j.ID, JobWaiting, 1, "", t2); err != nil {
		t.Fatal(err)
	}
	got, steps, _ = s.GetJob(j.ID)
	if got.Status != JobWaiting || got.CurrentStep != 1 || got.StartedAt == nil || !got.StartedAt.Equal(t1) || got.FinishedAt != nil {
		t.Fatalf("job after running/waiting = %+v", got)
	}
	if steps[0].Status != StepDone || steps[0].Message != "ok" || steps[0].StartedAt == nil || steps[0].FinishedAt == nil {
		t.Fatalf("step0 = %+v", steps[0])
	}
	if steps[1].Status != StepRunning || steps[1].StartedAt == nil || steps[1].FinishedAt != nil {
		t.Fatalf("step1 = %+v", steps[1])
	}

	t3 := t2.Add(time.Minute)
	if err := s.UpdateJob(j.ID, JobFailed, 1, "gagal", t3); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetJob(j.ID)
	if got.Status != JobFailed || got.Error != "gagal" || got.FinishedAt == nil || !got.FinishedAt.Equal(t3) || !got.StartedAt.Equal(t1) {
		t.Fatalf("job after failed = %+v", got)
	}
	if err := s.UpdateJob("nope", JobRunning, 0, "", t3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateJob missing: %v", err)
	}
	if err := s.SetStep(j.ID, 9, StepDone, "", t3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStep missing: %v", err)
	}

	// Log: ordering, afterID and limit.
	var ids []int64
	for _, m := range []string{"a", "b", "c"} {
		id, err := s.AppendJobLog(j.ID, "info", m, t3)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	lines, err := s.JobLog(j.ID, 0, 0)
	if err != nil || len(lines) != 3 || lines[0].Message != "a" || lines[2].Message != "c" || lines[0].Level != "info" || !lines[0].TS.Equal(t3) {
		t.Fatalf("JobLog = %+v, %v", lines, err)
	}
	lines, _ = s.JobLog(j.ID, ids[0], 1)
	if len(lines) != 1 || lines[0].ID != ids[1] {
		t.Fatalf("JobLog after/limit = %+v", lines)
	}

	// Listing: newest first, per-server filter, limit.
	other := mustServer(t, s, "db2")
	j2 := newJob(t, s, other.ID, "create-replica", t0.Add(time.Hour), "x")
	all, err := s.ListJobs(10)
	if err != nil || len(all) != 2 || all[0].ID != j2.ID || all[1].ID != j.ID {
		t.Fatalf("ListJobs = %+v, %v", all, err)
	}
	if one, _ := s.ListJobs(1); len(one) != 1 || one[0].ID != j2.ID {
		t.Fatalf("ListJobs(1) = %+v", one)
	}
	forSrv, err := s.ListJobsForServer(srv.ID, 10)
	if err != nil || len(forSrv) != 1 || forSrv[0].ID != j.ID {
		t.Fatalf("ListJobsForServer = %+v, %v", forSrv, err)
	}

	// RunningJobs: running|waiting only.
	running, err := s.RunningJobs()
	if err != nil || len(running) != 0 {
		t.Fatalf("RunningJobs = %+v, %v", running, err)
	}
	if err := s.UpdateJob(j2.ID, JobWaiting, 0, "", t3); err != nil {
		t.Fatal(err)
	}
	running, _ = s.RunningJobs()
	if len(running) != 1 || running[0].ID != j2.ID {
		t.Fatalf("RunningJobs = %+v", running)
	}
	if err := s.UpdateJob(j2.ID, JobCanceled, 0, "", t3); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetJob(j2.ID)
	if got.FinishedAt == nil {
		t.Fatalf("canceled job has no finished_at: %+v", got)
	}
}

func TestActiveJobForServer(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)

	if _, ok, err := s.ActiveJobForServer(srv.ID); err != nil || ok {
		t.Fatalf("no jobs: ok=%v err=%v", ok, err)
	}
	old := newJob(t, s, srv.ID, "k", t0, "a")
	if err := s.UpdateJob(old.ID, JobSucceeded, 0, "", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ActiveJobForServer(srv.ID); ok {
		t.Fatal("succeeded job reported active")
	}
	queued := newJob(t, s, srv.ID, "k", t0.Add(time.Minute), "a")
	got, ok, err := s.ActiveJobForServer(srv.ID)
	if err != nil || !ok || got.ID != queued.ID {
		t.Fatalf("queued: %+v ok=%v err=%v", got, ok, err)
	}
	newest := newJob(t, s, srv.ID, "k", t0.Add(2*time.Minute), "a")
	if err := s.UpdateJob(newest.ID, JobWaiting, 0, "", t0); err != nil {
		t.Fatal(err)
	}
	got, ok, _ = s.ActiveJobForServer(srv.ID)
	if !ok || got.ID != newest.ID {
		t.Fatalf("newest active = %+v ok=%v", got, ok)
	}
	if err := s.UpdateJob(newest.ID, JobFailed, 0, "x", t0); err != nil {
		t.Fatal(err)
	}
	got, ok, _ = s.ActiveJobForServer(srv.ID)
	if !ok || got.ID != queued.ID {
		t.Fatalf("after fail = %+v ok=%v", got, ok)
	}
	other := mustServer(t, s, "db2")
	if _, ok, _ := s.ActiveJobForServer(other.ID); ok {
		t.Fatal("other server reported active")
	}
}

func TestPruneJobLogs(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	j := newJob(t, s, srv.ID, "k", t0, "a")
	for i, ts := range []time.Time{t0.Add(-48 * time.Hour), t0.Add(-25 * time.Hour), t0.Add(-time.Hour)} {
		if _, err := s.AppendJobLog(j.ID, "info", string(rune('a'+i)), ts); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneJobLogs(t0.Add(-24 * time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("PruneJobLogs = %d, %v", n, err)
	}
	lines, _ := s.JobLog(j.ID, 0, 0)
	if len(lines) != 1 || lines[0].Message != "c" {
		t.Fatalf("remaining = %+v", lines)
	}
}

func TestDeleteServerCascadesInventoryReplicaJobs(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	r := mustServer(t, s, "replica")
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)

	topo, err := s.CreateTopology(m.ID, "repl", []byte("x"), "h", 3306)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReplica(topo.ID, r.ID, nil); err != nil {
		t.Fatal(err)
	}
	r2 := mustServer(t, s, "replica2")
	if _, err := s.AddReplica(topo.ID, r2.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInventory(Inventory{ServerID: r.ID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	j := newJob(t, s, r.ID, "k", now, "a", "b")
	if _, err := s.AppendJobLog(j.ID, "info", "hi", now); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteServer(r.ID); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		var n int
		if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for table, n := range map[string]int{
		"mysql_inventory": count(`SELECT COUNT(*) FROM mysql_inventory`),
		"replicas":        count(`SELECT COUNT(*) FROM replicas WHERE server_id = ?`, r.ID),
		"jobs":            count(`SELECT COUNT(*) FROM jobs`),
		"job_steps":       count(`SELECT COUNT(*) FROM job_steps`),
		"job_log":         count(`SELECT COUNT(*) FROM job_log`),
	} {
		if n != 0 {
			t.Errorf("%s has %d rows after DeleteServer", table, n)
		}
	}
	if _, err := s.GetTopology(topo.ID); err != nil {
		t.Fatalf("topology of master should remain: %v", err)
	}

	// Deleting the master removes its topology.
	if err := s.DeleteServer(m.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM topologies`); n != 0 {
		t.Fatalf("topologies = %d after deleting master", n)
	}
	if got := mustRole(t, s, r2.ID); got != RoleUnassigned {
		t.Fatalf("former replica role = %q, want unassigned", got)
	}
	if n := count(`SELECT COUNT(*) FROM replicas`); n != 0 {
		t.Fatalf("replicas = %d after deleting master", n)
	}
}

func TestUpdateJobRefusesFinishedJob(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	for _, terminal := range []string{JobSucceeded, JobFailed, JobCanceled} {
		j := newJob(t, s, srv.ID, "k", t0, "a")
		if err := s.UpdateJob(j.ID, terminal, 0, "", t0); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateJob(j.ID, JobRunning, 1, "x", t0.Add(time.Hour)); !errors.Is(err, ErrJobFinished) {
			t.Fatalf("%s -> running: err = %v, want ErrJobFinished", terminal, err)
		}
		if err := s.UpdateJob(j.ID, JobFailed, 1, "x", t0.Add(time.Hour)); !errors.Is(err, ErrJobFinished) {
			t.Fatalf("%s -> failed: err = %v, want ErrJobFinished", terminal, err)
		}
		got, _, _ := s.GetJob(j.ID)
		if got.Status != terminal || got.Error != "" || got.CurrentStep != 0 || got.FinishedAt == nil || !got.FinishedAt.Equal(t0) {
			t.Fatalf("%s job changed: %+v", terminal, got)
		}
	}
	if err := s.UpdateJob("nope", JobRunning, 0, "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: err = %v, want ErrNotFound", err)
	}
}

func TestCreateJobValidation(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := s.CreateJob(Job{ID: uuid.NewString(), Kind: "k", ServerID: 999, CreatedAt: now}, []string{"a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server: err = %v, want ErrNotFound", err)
	}
	if err := s.CreateJob(Job{Kind: "k", ServerID: srv.ID, CreatedAt: now}, []string{"a"}); err == nil || err.Error() != "id pekerjaan kosong" {
		t.Fatalf("empty id: err = %v", err)
	}
	if jobs, _ := s.ListJobs(10); len(jobs) != 0 {
		t.Fatalf("jobs created despite errors: %+v", jobs)
	}
}

func TestSetJobParams(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	j := newJob(t, s, srv.ID, "k", time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC), "a")
	if err := s.SetJobParams(j.ID, `{"b":2}`); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetJob(j.ID); got.ParamsJSON != `{"b":2}` {
		t.Fatalf("params = %q", got.ParamsJSON)
	}
	if err := s.SetJobParams("nope", `{}`); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: err = %v, want ErrNotFound", err)
	}
}

func TestReopenJob(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	j := newJob(t, s, srv.ID, "k", t0, "a", "b", "c")
	if err := s.UpdateJob(j.ID, JobRunning, 0, "", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenJob(j.ID, t0); err == nil || errors.Is(err, ErrJobFinished) {
		t.Fatalf("running job: err = %v, want a not-failed error", err)
	}
	if err := s.UpdateJob(j.ID, JobFailed, 2, "boom", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenJob(j.ID, t0.Add(time.Hour)); err != nil {
		t.Fatalf("ReopenJob: %v", err)
	}
	got, _, _ := s.GetJob(j.ID)
	if got.Status != JobRunning || got.CurrentStep != 2 || got.Error != "" || got.FinishedAt != nil ||
		got.StartedAt == nil || !got.StartedAt.Equal(t0) {
		t.Fatalf("reopened job: %+v", got)
	}
	for _, terminal := range []string{JobSucceeded, JobCanceled} {
		k := newJob(t, s, srv.ID, "k", t0, "a")
		if err := s.UpdateJob(k.ID, terminal, 0, "", t0); err != nil {
			t.Fatal(err)
		}
		if err := s.ReopenJob(k.ID, t0); !errors.Is(err, ErrJobFinished) {
			t.Fatalf("%s: err = %v, want ErrJobFinished", terminal, err)
		}
	}
	if err := s.ReopenJob("nope", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: err = %v, want ErrNotFound", err)
	}
}
