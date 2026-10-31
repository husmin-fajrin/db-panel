package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// catchingUp scripts waitSynced: not synced while the applier runs, the
// executed set growing with every call when grow, synced from call
// syncAt on (0: never).
func catchingUp(r *fakeAgent, grow bool, syncAt int64) *atomic.Int64 {
	var calls atomic.Int64
	r.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		n := calls.Add(1)
		if syncAt > 0 && n >= syncAt {
			return proto.WaitSyncedResult{Synced: true, Executed: masterGTID}, nil
		}
		executed := masterUUID + ":1-10"
		if grow {
			executed = masterUUID + ":1-" + strings.Repeat("1", int(n))
		}
		behind := int64(3600)
		return proto.WaitSyncedResult{Executed: executed,
			Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", SecondsBehind: &behind}}, nil
	})
	return &calls
}

// TestVerifyWaitsWhileCatchingUp: a replica still catching up after the
// first window, with its threads running and its executed set growing, is
// waited for (progress logged) instead of failing.
func TestVerifyWaitsWhileCatchingUp(t *testing.T) {
	e := newEnv(t)
	r, id := startSubsetReplicaWith(t, e, func(r *fakeAgent) { catchingUp(r, true, 3) })
	e.waitJob(id, store.JobSucceeded)
	if n := r.count("mysql.waitSynced"); n != 3 {
		t.Fatalf("waitSynced called %d times, want 3", n)
	}
	if log := e.jobLog(id); strings.Count(log, "Replika masih menyusul master (tertinggal 3600 detik)") != 2 {
		t.Fatalf("progress not logged twice:\n%s", log)
	}
}

func startSubsetReplicaWith(t *testing.T, e *env, script func(r *fakeAgent)) (*fakeAgent, string) {
	t.Helper()
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	script(r)
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

// TestVerifyNoProgressKeepsReplication: a window without progress fails the
// step, but replication keeps running (no stopReplica).
func TestVerifyNoProgressKeepsReplication(t *testing.T) {
	e := newEnv(t)
	r, id := startSubsetReplicaWith(t, e, func(r *fakeAgent) { catchingUp(r, false, 0) })
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 6 || !strings.Contains(j.Error, "tidak bertambah maju") {
		t.Fatalf("job = %+v", j)
	}
	if n := r.count("mysql.stopReplica"); n != 0 {
		t.Fatalf("stopReplica called %d times after a catch-up timeout", n)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Replikasi tetap berjalan") {
		t.Fatalf("job log:\n%s", log)
	}
	if n := r.count("mysql.waitSynced"); n != 2 {
		t.Fatalf("waitSynced called %d times, want 2 (the first window counts as progress)", n)
	}
}

// TestVerifyBoundedOverall: even with progress the verify ends at the
// overall bound, again without stopping replication.
func TestVerifyBoundedOverall(t *testing.T) {
	e := newEnv(t)
	e.r.catchUpMax = 0
	r, id := startSubsetReplicaWith(t, e, func(r *fakeAgent) { catchingUp(r, true, 0) })
	j, _ := e.waitJob(id, store.JobFailed)
	if !strings.Contains(j.Error, "masih menyusul master setelah") {
		t.Fatalf("job = %+v", j)
	}
	if n := r.count("mysql.stopReplica"); n != 0 {
		t.Fatalf("stopReplica called %d times", n)
	}
}

// TestVerifyThreadErrorStillStops: a replication error is not catching up:
// the step fails and replication is stopped as before.
func TestVerifyThreadErrorStillStops(t *testing.T) {
	e := newEnv(t)
	r, id := startSubsetReplicaWith(t, e, func(r *fakeAgent) {
		r.reply("mysql.waitSynced", proto.WaitSyncedResult{Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "No",
			LastSQLErrno: 1062, LastSQLError: "Duplicate entry"}})
	})
	j, _ := e.waitJob(id, store.JobFailed)
	if !strings.Contains(j.Error, "1062") || r.count("mysql.stopReplica") != 1 {
		t.Fatalf("job = %+v, stopReplica %d", j, r.count("mysql.stopReplica"))
	}
}
