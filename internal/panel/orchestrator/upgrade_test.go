package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Jobs stored before the certificate step was added keep the step list
// they were created with: they resume, retry, approve and cancel as
// before.

var (
	oldCreateReplicaSteps = []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika",
		"Salin data", "Sambungkan replikasi", "Verifikasi"}
	oldPrepareMasterSteps = []string{"Periksa master", "Terapkan pengaturan", "Mulai ulang MySQL", "Verifikasi"}
)

// storeOldJob stores a job as an older panel left it: kind, steps and
// params, at step idx with status (running or waiting; earlier steps
// done).
func (e *env) storeOldJob(id, kind string, serverID, topoID int64, steps []string, st jobState, idx int, status string) {
	e.t.Helper()
	params, err := json.Marshal(st)
	if err != nil {
		e.t.Fatal(err)
	}
	now := time.Now()
	if err := e.st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: serverID, TopologyID: topoID, ParamsJSON: string(params),
		CreatedAt: now}, steps); err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i < idx; i++ {
		if err := e.st.SetStep(id, i, store.StepDone, "", now); err != nil {
			e.t.Fatal(err)
		}
	}
	stepStatus := store.StepRunning
	if status == store.JobWaiting {
		stepStatus = store.StepWaiting
	}
	if err := e.st.SetStep(id, idx, stepStatus, "", now); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, status, idx, "", now); err != nil {
		e.t.Fatal(err)
	}
}

func TestOldCreateReplicaJobResumesAndRetries(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	if _, err := e.st.AddReplica(topo.ID, r.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	st := jobState{MasterServerID: m.id, Databases: []string{"a"}, Method: "dump", MasterTLS: true}
	// Cut off by the upgrade at the old "Sambungkan replikasi" (index 5).
	e.storeOldJob("old-create", KindCreateReplica, r.id, topo.ID, oldCreateReplicaSteps, st, 5, store.JobRunning)

	e.r.Resume(context.Background())
	if j, _ := e.waitJob("old-create", store.JobFailed); j.CurrentStep != 5 {
		t.Fatalf("after Resume: %+v", j)
	}
	if err := e.r.Retry("old-create"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, steps := e.waitJob("old-create", store.JobSucceeded)
	if len(steps) != 7 || steps[5].Status != store.StepDone || steps[6].Status != store.StepDone {
		t.Fatalf("steps = %+v", steps)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.status", "mysql.configureReplication", "mysql.startReplica",
		"mysql.startReplica", "mysql.waitSynced"})
	if rep := e.replica(r.id); rep.State != store.ReplicaReady {
		t.Fatalf("replica = %+v", rep)
	}
}

func TestOldCreateReplicaJobCancelAfterConnect(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.reply("mysql.resetReplica", nil)
	if _, err := e.st.AddReplica(topo.ID, r.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	st := jobState{MasterServerID: m.id, Databases: []string{"a"}, Method: "dump", WaitReason: WaitReconnect}
	// Waiting at the old "Sambungkan replikasi" (index 5): replication
	// may be configured, so Cancel stops and resets it before the release.
	e.storeOldJob("old-wait", KindCreateReplica, r.id, topo.ID, oldCreateReplicaSteps, st, 5, store.JobWaiting)
	if err := e.r.Cancel("old-wait"); err != nil {
		t.Fatal(err)
	}
	e.waitJob("old-wait", store.JobCanceled)
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.resetReplica", "mysql.releaseRole"})
}

func TestOldPrepareMasterJobApproves(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	m.reply("mysql.restart", nil)
	st := jobState{MasterPort: 3306, NeedsRestart: true, RestartReasons: []string{"log_bin masih OFF"}, WaitReason: WaitRestart}
	// Waiting at the old "Mulai ulang MySQL" (index 2).
	e.storeOldJob("old-prepare", KindPrepareMaster, m.id, 0, oldPrepareMasterSteps, st, 2, store.JobWaiting)
	if err := e.r.Approve("old-prepare"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	_, steps := e.waitJob("old-prepare", store.JobSucceeded)
	if len(steps) != 4 {
		t.Fatalf("steps = %+v", steps)
	}
	wantCmds(t, "master", m.cmds(0), []string{"mysql.restart", "mysql.status"})
}

// TestUnknownStepListFailsVisibly: a job whose stored steps this panel
// cannot rebuild fails with the reason instead of staying running (which
// would block its server).
func TestUnknownStepListFailsVisibly(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	e.storeOldJob("odd", KindPrepareMaster, m.id, 0, []string{"a", "b"}, jobState{}, 1, store.JobRunning)
	e.r.Resume(context.Background())
	e.waitJob("odd", store.JobFailed)
	if err := e.r.Retry("odd"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitFor("job failed again", func() bool {
		j, _, _ := e.st.GetJob("odd")
		return j.Status == store.JobFailed && strings.Contains(j.Error, "pekerjaan tidak bisa dilanjutkan setelah pembaruan panel")
	})
	if _, err := e.r.StartMySQLTLS(context.Background(), m.id); errors.Is(err, ErrBusy) {
		t.Fatalf("server still busy: %v", err)
	}
}
