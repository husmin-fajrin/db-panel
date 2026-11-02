package monitor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The tests below cover F2: the orchestrator changes MySQL and then the
// store within milliseconds, while the agent's last report predates the
// change. Until a report taken after the change arrives, the status judged
// before it stands, so no alert opens on stale data.

// noAlerts fails if any alert of kinds was ever opened for id.
func (r *rig) noAlerts(id int64, kinds ...string) {
	r.t.Helper()
	for _, k := range kinds {
		if as := r.alerts(id, k); len(as) != 0 {
			r.t.Fatalf("%s alerts for server %d = %+v, want none", k, id, as)
		}
	}
}

func stoppedNoError(s *proto.HealthSnapshot) {
	rs := *s.Replica
	rs.SQLRunning, rs.IORunning = "No", "No"
	s.Replica = &rs
}

// TestResyncReadyHoldsUntilFreshReport: the last report of a resync was
// taken mid-job (no replication configured, the lock lifted for the load).
// When the job sets the replica ready, the status stays Belum siap, no
// replica_stopped or replica_unlocked opens, and the fresh report the
// Monitor fetches makes it Sinkron.
func TestResyncReadyHoldsUntilFreshReport(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status %q, want Sinkron", v.Status.Code)
	}

	// Resync ulang starts: Belum siap.
	if err := r.st.SetReplicaState(rID, store.ReplicaResyncing, ""); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	if v := r.view(rID); v.Status.Code != StatusNotReady {
		t.Fatalf("status during the resync %q, want Belum siap", v.Status.Code)
	}
	// A report mid-job: replication reset, lock lifted.
	r.clk.Add(10 * time.Second)
	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica, s.SuperReadOnly, s.ReadOnly = nil, false, false })
	ra.send()

	// The job ends: MySQL replicates again (not yet reported), ready.
	release := ra.holdSnapshots()
	ra.setSnap(func(s *proto.HealthSnapshot) {
		*s = replicaSnap(srcUUID + ":1-100")
	})
	r.clk.Add(time.Second)
	if err := r.st.SetReplicaState(rID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	for range 3 {
		r.clk.Add(5 * time.Second)
		r.m.tick()
	}
	if v := r.view(rID); v.Status.Code != StatusNotReady || v.Status.LockLost {
		t.Fatalf("status before a fresh report %+v, want Belum siap kept", v.Status)
	}
	r.noAlerts(rID, KindReplicaStopped, KindReplicaUnlocked)

	release()
	r.settle()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status after the fresh report %q, want Sinkron", v.Status.Code)
	}
	r.m.tick()
	r.noAlerts(rID, KindReplicaStopped, KindReplicaUnlocked)
	for _, ev := range r.events(rID, kindLock) {
		t.Errorf("lock event %+v during a successful resync", ev)
	}
}

// TestStartFromPausedOpensNoAlert: Start replika clears paused while the
// last report still shows replication stopped. Dihentikan stays (also
// through a report taken before the change that arrives late) until a
// fresh report shows replication running; no replica_stopped opens.
func TestStartFromPausedOpensNoAlert(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	// Stop replika through the panel: paused is written first.
	if err := r.st.SetReplicaPaused(rID, true); err != nil {
		t.Fatal(err)
	}
	ra.setSnap(stoppedNoError)
	r.m.ReplicaChanged(rID)
	r.settle()
	if v := r.view(rID); v.Status.Code != StatusPaused {
		t.Fatalf("status after Stop %q, want Dihentikan", v.Status.Code)
	}

	release := ra.holdSnapshots()
	stale := ra.report() // taken now, before Start
	r.clk.Add(3 * time.Second)
	ra.setSnap(running)
	if err := r.st.SetReplicaPaused(rID, false); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	r.clk.Add(2 * time.Second)
	r.m.tick()
	// The stopped report reaches the panel late: it was taken before Start.
	stale.TakenAgoMS = 5000
	b, _ := json.Marshal(stale)
	r.hub.OnEvent(rID, proto.Event{Type: proto.EventHealth, Payload: b})
	r.m.tick()
	if v := r.view(rID); v.Status.Code != StatusPaused {
		t.Fatalf("status right after Start %q, want Dihentikan kept", v.Status.Code)
	}
	r.noAlerts(rID, KindReplicaStopped)

	release()
	r.settle()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status after the fresh report %q, want Sinkron", v.Status.Code)
	}
	r.m.tick()
	r.noAlerts(rID, KindReplicaStopped, KindReplicaDisconnected)
	if n := ra.count("monitor.snapshot"); n < 2 {
		t.Fatalf("monitor.snapshot calls = %d, want one per change", n)
	}
}

// TestFreshReportBoundFallsBack: when no report taken after the change
// comes (the fetch hangs), the usual rules apply once the bound passes.
func TestFreshReportBoundFallsBack(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	if err := r.st.SetReplicaPaused(rID, true); err != nil {
		t.Fatal(err)
	}
	ra.setSnap(stoppedNoError)
	r.m.ReplicaChanged(rID)
	r.settle()

	ra.holdSnapshots()
	r.clk.Add(time.Second)
	if err := r.st.SetReplicaPaused(rID, false); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	bound := r.m.freshBound()
	r.clk.Add(bound)
	r.m.tick()
	if v := r.view(rID); v.Status.Code != StatusPaused {
		t.Fatalf("status within the bound %q, want Dihentikan kept", v.Status.Code)
	}
	r.clk.Add(time.Second)
	r.m.tick()
	// The last report (replication stopped) is judged with paused cleared.
	if v := r.view(rID); v.Status.Code != StatusStoppedOutside {
		t.Fatalf("status after the bound %q, want Berhenti (di luar panel)", v.Status.Code)
	}
	r.openAlert(rID, KindReplicaStopped)
}
