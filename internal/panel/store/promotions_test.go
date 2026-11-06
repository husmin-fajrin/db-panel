package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func mustTopology(t *testing.T, s *Store) (Topology, Server, Server) {
	t.Helper()
	m := mustServer(t, s, "old")
	r := mustServer(t, s, "new")
	topo, err := s.CreateTopology(m.ID, "repl", []byte{1}, "10.0.0.1", 3306)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReplica(topo.ID, r.ID, nil); err != nil {
		t.Fatal(err)
	}
	return topo, m, r
}

func TestSwapMasterAtomic(t *testing.T) {
	s := openTemp(t)
	topo, oldM, newM := mustTopology(t, s)

	// New server row missing: nothing changes.
	if err := s.SwapMaster(topo.ID, oldM.ID, 9999, "10.0.0.9", 3307); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SwapMaster missing new: %v", err)
	}
	if err := s.SwapMaster(topo.ID, 9999, newM.ID, "10.0.0.9", 3307); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SwapMaster missing old: %v", err)
	}
	got, err := s.GetTopology(topo.ID)
	if err != nil || !reflect.DeepEqual(got, topo) {
		t.Fatalf("topology changed: %+v, %v", got, err)
	}
	if mustRole(t, s, oldM.ID) != RoleMaster || mustRole(t, s, newM.ID) != RoleReplica {
		t.Fatal("roles changed after failed swap")
	}
	if _, err := s.GetReplicaByServer(newM.ID); err != nil {
		t.Fatalf("replica row lost: %v", err)
	}

	if err := s.SwapMaster(topo.ID, oldM.ID, newM.ID, "10.0.0.2", 3308); err != nil {
		t.Fatalf("SwapMaster: %v", err)
	}
	got, err = s.GetTopology(topo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MasterServerID != newM.ID || got.SourceHost != "10.0.0.2" || got.SourcePort != 3308 ||
		got.OldMasterServerID != oldM.ID || !got.CutoverPending {
		t.Fatalf("topology = %+v", got)
	}
	if mustRole(t, s, newM.ID) != RoleMaster || mustRole(t, s, oldM.ID) != RoleQuarantined {
		t.Fatal("roles not swapped")
	}
	if _, err := s.GetReplicaByServer(newM.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new master replica row: %v", err)
	}
	byOld, err := s.GetTopologyByOldMaster(oldM.ID)
	if err != nil || byOld.ID != topo.ID {
		t.Fatalf("GetTopologyByOldMaster = %+v, %v", byOld, err)
	}
	if _, err := s.GetTopologyByOldMaster(newM.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTopologyByOldMaster other: %v", err)
	}

	if err := s.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTopology(topo.ID)
	if got.CutoverPending {
		t.Fatal("cutover still pending")
	}
	if err := s.SetCutoverPending(999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetCutoverPending missing: %v", err)
	}
}

func TestPromotionRoundTrip(t *testing.T) {
	s := openTemp(t)
	topo, oldM, newM := mustTopology(t, s)
	if _, err := s.LatestPromotion(topo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LatestPromotion empty: %v", err)
	}
	if _, err := s.PromotionByOldMaster(oldM.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PromotionByOldMaster empty: %v", err)
	}
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	p := Promotion{TopologyID: topo.ID, Mode: "planned", OldMasterServerID: oldM.ID, NewMasterServerID: newM.ID,
		StartedAt: start, ReplicasJSON: `[{"server_id":3}]`, JobID: "j1"}
	id, err := s.AddPromotion(p)
	if err != nil || id == 0 {
		t.Fatalf("AddPromotion = %d, %v", id, err)
	}
	got, err := s.GetPromotion(id)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = id
	p.Status = "running"
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("got %+v, want %+v", got, p)
	}

	fin := start.Add(time.Minute)
	got.FinishedAt, got.CutoverConfirmedAt, got.LockAt = &fin, &fin, &fin
	got.LastMasterGTID, got.NewMasterGTID, got.LeftoverJSON, got.Status = "a:1-5", "a:1-5", `["x"]`, "done"
	if err := s.UpdatePromotion(got); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetPromotion(id)
	if err != nil || !reflect.DeepEqual(back, got) {
		t.Fatalf("after update %+v, %v", back, err)
	}
	if err := s.UpdatePromotion(Promotion{ID: 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePromotion missing: %v", err)
	}
	if _, err := s.GetPromotion(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPromotion missing: %v", err)
	}

	p2 := p
	p2.ID, p2.JobID, p2.StartedAt = 0, "j2", start.Add(time.Hour)
	id2, err := s.AddPromotion(p2)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := s.LatestPromotion(topo.ID); err != nil || l.ID != id2 {
		t.Fatalf("LatestPromotion = %+v, %v", l, err)
	}
	if l, err := s.PromotionByOldMaster(oldM.ID); err != nil || l.ID != id2 {
		t.Fatalf("PromotionByOldMaster = %+v, %v", l, err)
	}
}

func TestQuarantinedRoleAccepted(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "q")
	if err := s.SetServerRole(srv.ID, RoleQuarantined); err != nil {
		t.Fatalf("SetServerRole: %v", err)
	}
	if mustRole(t, s, srv.ID) != RoleQuarantined {
		t.Fatal("role not stored")
	}
}

func TestReplicaStatesAccepted(t *testing.T) {
	s := openTemp(t)
	topo, _, newM := mustTopology(t, s)
	_ = topo
	for _, st := range []string{ReplicaNeedsRepoint, ReplicaNeedsRebuild} {
		if err := s.SetReplicaState(newM.ID, st, "x"); err != nil {
			t.Fatalf("SetReplicaState %s: %v", st, err)
		}
		rep, err := s.GetReplicaByServer(newM.ID)
		if err != nil || rep.State != st {
			t.Fatalf("state = %q, %v; want %q", rep.State, err, st)
		}
	}
}
