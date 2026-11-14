package store

import (
	"errors"
	"testing"
)

// TestReturnToQuarantine (G-2): the failed replica row of an old master
// goes and its role becomes quarantined in one transaction; anything else
// is refused and changes nothing.
func TestReturnToQuarantine(t *testing.T) {
	s := openTemp(t)
	topo, oldM, newM := mustTopology(t, s)
	if err := s.SwapMaster(topo.ID, oldM.ID, newM.ID, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	other := mustServer(t, s, "lain")
	if _, err := s.AddReplica(topo.ID, other.ID, nil); err != nil {
		t.Fatal(err)
	}
	// The rebuild of the old master registered it, then failed.
	if _, err := s.AddReplica(topo.ID, oldM.ID, nil); err != nil {
		t.Fatal(err)
	}

	unchanged := func(what string, id int64, state, role string) {
		t.Helper()
		rep, err := s.GetReplicaByServer(id)
		if err != nil || rep.State != state {
			t.Fatalf("%s: row = %+v, %v; want state %s", what, rep, err, state)
		}
		if got := mustRole(t, s, id); got != role {
			t.Fatalf("%s: role = %s, want %s", what, got, role)
		}
	}
	// A row that is not failed (the rebuild still running).
	if err := s.ReturnToQuarantine(oldM.ID); !errors.Is(err, ErrNotFailedRebuild) {
		t.Fatalf("running rebuild: %v, want ErrNotFailedRebuild", err)
	}
	unchanged("running rebuild", oldM.ID, ReplicaCreating, RoleReplica)
	// A failed replica that is not the old master.
	if err := s.SetReplicaState(other.ID, ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReturnToQuarantine(other.ID); !errors.Is(err, ErrNotFailedRebuild) {
		t.Fatalf("ordinary failed replica: %v, want ErrNotFailedRebuild", err)
	}
	unchanged("ordinary failed replica", other.ID, ReplicaFailed, RoleReplica)
	// No row at all.
	if err := s.ReturnToQuarantine(newM.ID); !errors.Is(err, ErrNotFailedRebuild) {
		t.Fatalf("no row: %v, want ErrNotFailedRebuild", err)
	}

	if err := s.SetReplicaState(oldM.ID, ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReturnToQuarantine(oldM.ID); err != nil {
		t.Fatalf("ReturnToQuarantine: %v", err)
	}
	if _, err := s.GetReplicaByServer(oldM.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row after: %v", err)
	}
	if got := mustRole(t, s, oldM.ID); got != RoleQuarantined {
		t.Fatalf("role after = %s", got)
	}
	if got, err := s.GetTopologyByOldMaster(oldM.ID); err != nil || got.ID != topo.ID {
		t.Fatalf("old master after = %+v, %v", got, err)
	}

	// The marker cleared (the agent ended the quarantine, G-1): refused.
	if _, err := s.AddReplica(topo.ID, oldM.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReplicaState(oldM.ID, ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearOldMaster(oldM.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReturnToQuarantine(oldM.ID); !errors.Is(err, ErrNotFailedRebuild) {
		t.Fatalf("after the settle: %v, want ErrNotFailedRebuild", err)
	}
	unchanged("after the settle", oldM.ID, ReplicaFailed, RoleReplica)
}
