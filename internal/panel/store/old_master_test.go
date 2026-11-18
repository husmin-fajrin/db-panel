package store

import (
	"errors"
	"testing"
	"time"
)

// An old master stays quarantined until its agent ends the quarantine
// (spec 5.1): the replica row of its rebuild comes and goes without
// touching the role, and the settle changes the job's params, the
// old-master marker and the role together.

// oldMasterTopology is a topology whose master was swapped: oldM is its
// quarantined old master, newM its master.
func oldMasterTopology(t *testing.T, s *Store) (Topology, Server, Server) {
	t.Helper()
	topo, oldM, newM := mustTopology(t, s)
	if err := s.SwapMaster(topo.ID, oldM.ID, newM.ID, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	return topo, oldM, newM
}

// TestAddAndRemoveReplicaKeepQuarantinedRole: the row of a rebuild is added
// to and removed from a quarantined server without changing its role; any
// other server becomes a replica and then unassigned, as before.
func TestAddAndRemoveReplicaKeepQuarantinedRole(t *testing.T) {
	s := openTemp(t)
	topo, oldM, _ := oldMasterTopology(t, s)
	if _, err := s.AddReplica(topo.ID, oldM.ID, nil); err != nil {
		t.Fatalf("AddReplica(quarantined): %v", err)
	}
	if got := mustRole(t, s, oldM.ID); got != RoleQuarantined {
		t.Fatalf("role after AddReplica = %s, want quarantined", got)
	}
	if rep, err := s.GetReplicaByServer(oldM.ID); err != nil || rep.State != ReplicaCreating {
		t.Fatalf("row after AddReplica = %+v, %v", rep, err)
	}
	if err := s.RemoveReplica(oldM.ID); err != nil {
		t.Fatalf("RemoveReplica(quarantined): %v", err)
	}
	if got := mustRole(t, s, oldM.ID); got != RoleQuarantined {
		t.Fatalf("role after RemoveReplica = %s, want quarantined", got)
	}
	if _, err := s.GetReplicaByServer(oldM.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row after RemoveReplica: %v", err)
	}

	plain := mustServer(t, s, "baru")
	if _, err := s.AddReplica(topo.ID, plain.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := mustRole(t, s, plain.ID); got != RoleReplica {
		t.Fatalf("role of an unassigned server after AddReplica = %s, want replica", got)
	}
	if err := s.RemoveReplica(plain.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustRole(t, s, plain.ID); got != RoleUnassigned {
		t.Fatalf("role after RemoveReplica = %s, want unassigned", got)
	}
}

// settled reports the three things a settle writes: the job's params, the
// topology's old-master marker and the server's role.
func settled(t *testing.T, s *Store, jobID string, topoID, serverID int64) (params string, marker int64, role string) {
	t.Helper()
	j, _, err := s.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	topo, err := s.GetTopology(topoID)
	if err != nil {
		t.Fatal(err)
	}
	return j.ParamsJSON, topo.OldMasterServerID, mustRole(t, s, serverID)
}

// TestStoreSettleRebuildIsOneTransaction: SettleRebuild writes the job's
// params, clears the marker and sets the role replica together; an error
// at the third write leaves all three as they were, and a second call
// gives the same result as the first.
func TestStoreSettleRebuildIsOneTransaction(t *testing.T) {
	s := openTemp(t)
	topo, oldM, _ := oldMasterTopology(t, s)
	if _, err := s.AddReplica(topo.ID, oldM.ID, nil); err != nil {
		t.Fatal(err)
	}
	const before, after = `{"rebuild_old_master":true}`, `{"rebuild_old_master":true,"quarantine_ended":true}`
	if err := s.CreateJob(Job{ID: "rebuild", Kind: "create_replica", ServerID: oldM.ID, TopologyID: topo.ID,
		ParamsJSON: before, CreatedAt: time.Now()}, []string{"s"}); err != nil {
		t.Fatal(err)
	}

	// The role write (the third) fails.
	if _, err := s.db.Exec(`CREATE TRIGGER settle_fault BEFORE UPDATE OF role ON servers
		WHEN NEW.role = 'replica' BEGIN SELECT RAISE(ABORT, 'disuntik'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleRebuild("rebuild", after, oldM.ID); err == nil {
		t.Fatal("SettleRebuild passed although its role write failed")
	}
	if p, m, r := settled(t, s, "rebuild", topo.ID, oldM.ID); p != before || m != oldM.ID || r != RoleQuarantined {
		t.Fatalf("after a failed settle: params %s, marker %d, role %s; want all unchanged", p, m, r)
	}
	if _, err := s.db.Exec(`DROP TRIGGER settle_fault`); err != nil {
		t.Fatal(err)
	}

	for i := range 2 {
		if err := s.SettleRebuild("rebuild", after, oldM.ID); err != nil {
			t.Fatalf("SettleRebuild #%d: %v", i+1, err)
		}
		if p, m, r := settled(t, s, "rebuild", topo.ID, oldM.ID); p != after || m != 0 || r != RoleReplica {
			t.Fatalf("after settle #%d: params %s, marker %d, role %s", i+1, p, m, r)
		}
	}
}

// TestNewerJobExistsExcept: jobs of the excepted kinds created after the
// job do not count; any other kind does.
func TestNewerJobExistsExcept(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	now := time.Now()
	for _, j := range []Job{{ID: "rebuild", Kind: "create_replica"}, {ID: "check", Kind: "leftover"},
		{ID: "save", Kind: "salvage"}} {
		j.ServerID, j.CreatedAt = srv.ID, now
		if err := s.CreateJob(j, []string{"s"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.NewerJobExistsExcept("rebuild", srv.ID, "leftover", "salvage"); err != nil || got {
		t.Fatalf("NewerJobExistsExcept(leftover, salvage) = %v, %v; want false", got, err)
	}
	if got, err := s.NewerJobExistsExcept("rebuild", srv.ID, "leftover"); err != nil || !got {
		t.Fatalf("NewerJobExistsExcept(leftover) = %v, %v; want true", got, err)
	}
	if got, err := s.NewerJobExistsExcept("rebuild", srv.ID); err != nil || !got {
		t.Fatalf("NewerJobExistsExcept() = %v, %v; want true", got, err)
	}
}
