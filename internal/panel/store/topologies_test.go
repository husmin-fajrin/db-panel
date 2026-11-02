package store

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

func mustServer(t *testing.T, s *Store, name string) Server {
	t.Helper()
	srv, err := s.CreateServer(name)
	if err != nil {
		t.Fatalf("CreateServer %s: %v", name, err)
	}
	return srv
}

func mustRole(t *testing.T, s *Store, id int64) string {
	t.Helper()
	srv, err := s.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	return srv.Role
}

func TestTopologyLifecycle(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	r := mustServer(t, s, "replica")

	enc := []byte{0x00, 0x01, 0xff}
	topo, err := s.CreateTopology(m.ID, "repl", enc, "10.0.0.1", 3306)
	if err != nil {
		t.Fatalf("CreateTopology: %v", err)
	}
	if topo.ID == 0 || topo.MasterServerID != m.ID || topo.ReplUser != "repl" ||
		!bytes.Equal(topo.ReplPasswordEnc, enc) || topo.SourceHost != "10.0.0.1" || topo.SourcePort != 3306 ||
		topo.CreatedAt.IsZero() {
		t.Fatalf("topology = %+v", topo)
	}
	if got := mustRole(t, s, m.ID); got != RoleMaster {
		t.Fatalf("master role = %q", got)
	}
	if _, err := s.CreateTopology(m.ID, "repl", enc, "h", 1); err == nil || err.Error() != "server sudah menjadi master" {
		t.Fatalf("second CreateTopology err = %v", err)
	}

	byID, err := s.GetTopology(topo.ID)
	if err != nil || !reflect.DeepEqual(byID, topo) {
		t.Fatalf("GetTopology = %+v, %v", byID, err)
	}
	byMaster, err := s.GetTopologyByMaster(m.ID)
	if err != nil || !reflect.DeepEqual(byMaster, topo) {
		t.Fatalf("GetTopologyByMaster = %+v, %v", byMaster, err)
	}
	if _, err := s.GetTopology(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTopology missing: %v", err)
	}
	if _, err := s.GetTopologyByMaster(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTopologyByMaster missing: %v", err)
	}
	list, err := s.ListTopologies()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTopologies = %v, %v", list, err)
	}

	// A master cannot be added as a replica; a replica can be added once.
	if _, err := s.AddReplica(topo.ID, m.ID, nil); err == nil || err.Error() != "server sudah berperan sebagai master" {
		t.Fatalf("AddReplica master err = %v", err)
	}
	rep, err := s.AddReplica(topo.ID, r.ID, []string{"app"})
	if err != nil {
		t.Fatalf("AddReplica: %v", err)
	}
	if rep.State != ReplicaCreating || rep.TopologyID != topo.ID || rep.ServerID != r.ID ||
		!reflect.DeepEqual(rep.Databases, []string{"app"}) || rep.CreatedAt.IsZero() {
		t.Fatalf("replica = %+v", rep)
	}
	if got := mustRole(t, s, r.ID); got != RoleReplica {
		t.Fatalf("replica role = %q", got)
	}
	if _, err := s.AddReplica(topo.ID, r.ID, nil); err == nil || err.Error() != "server sudah berperan sebagai replika" {
		t.Fatalf("AddReplica twice err = %v", err)
	}

	if err := s.SetReplicaState(r.ID, ReplicaFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReplicaByServer(r.ID)
	if err != nil || got.State != ReplicaFailed || got.LastError != "boom" {
		t.Fatalf("GetReplicaByServer = %+v, %v", got, err)
	}
	if err := s.SetReplicaState(999, ReplicaReady, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetReplicaState missing: %v", err)
	}
	reps, err := s.ListReplicas(topo.ID)
	if err != nil || len(reps) != 1 || reps[0].ServerID != r.ID {
		t.Fatalf("ListReplicas = %v, %v", reps, err)
	}

	if err := s.RemoveReplica(r.ID); err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
	if got := mustRole(t, s, r.ID); got != RoleUnassigned {
		t.Fatalf("role after remove = %q", got)
	}
	if _, err := s.GetReplicaByServer(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReplicaByServer after remove: %v", err)
	}
	if err := s.RemoveReplica(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemoveReplica twice: %v", err)
	}
	if err := s.SetServerRole(999, RoleMaster); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetServerRole missing: %v", err)
	}
}

func TestReplicaDatabasesEmptyMeansAll(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	r := mustServer(t, s, "replica")
	topo, err := s.CreateTopology(m.ID, "repl", []byte("x"), "h", 3306)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.AddReplica(topo.ID, r.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Databases != nil {
		t.Fatalf("Databases = %#v, want nil", rep.Databases)
	}
	got, err := s.GetReplicaByServer(r.ID)
	if err != nil || got.Databases != nil {
		t.Fatalf("GetReplicaByServer = %+v, %v", got, err)
	}
	if err := s.SetReplicaDatabases(r.ID, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetReplicaByServer(r.ID)
	if !reflect.DeepEqual(got.Databases, []string{"a", "b"}) {
		t.Fatalf("Databases = %#v", got.Databases)
	}
	if err := s.SetReplicaDatabases(r.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetReplicaByServer(r.ID)
	if got.Databases != nil {
		t.Fatalf("Databases after clear = %#v, want nil", got.Databases)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT databases_json FROM replicas WHERE server_id = ?`, r.ID).Scan(&raw); err != nil || raw != "[]" {
		t.Fatalf("stored = %q, %v", raw, err)
	}
	if err := s.SetReplicaDatabases(999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetReplicaDatabases missing: %v", err)
	}
}

func TestCreateTopologyRejectsReplicaServer(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	r := mustServer(t, s, "replica")
	topo, err := s.CreateTopology(m.ID, "repl", []byte("x"), "h", 3306)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReplica(topo.ID, r.ID, nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateTopology(r.ID, "repl", []byte("x"), "h", 3306)
	if err == nil || err.Error() != "server sudah menjadi replika" {
		t.Fatalf("err = %v", err)
	}
	if got := mustRole(t, s, r.ID); got != RoleReplica {
		t.Fatalf("role changed to %q", got)
	}
	if _, err := s.CreateTopology(999, "repl", []byte("x"), "h", 3306); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server err = %v", err)
	}
}

func TestSetTopologySource(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	topo, err := s.CreateTopology(m.ID, "repl", nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTopologySource(topo.ID, "10.0.0.9", 3307); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTopology(topo.ID)
	if err != nil || got.SourceHost != "10.0.0.9" || got.SourcePort != 3307 {
		t.Fatalf("topology = %+v, %v", got, err)
	}
	if err := s.SetTopologySource(999, "h", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: err = %v, want ErrNotFound", err)
	}
}

func TestReplicaPausedRoundTrip(t *testing.T) {
	s := openTemp(t)
	m := mustServer(t, s, "master")
	r := mustServer(t, s, "replica")
	topo, err := s.CreateTopology(m.ID, "repl", nil, "h", 3306)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.AddReplica(topo.ID, r.ID, nil)
	if err != nil || rep.Paused {
		t.Fatalf("new replica = %+v, %v", rep, err)
	}
	if err := s.SetReplicaPaused(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReplicaByServer(r.ID); !got.Paused {
		t.Fatalf("paused = false after SetReplicaPaused(true)")
	}
	if list, _ := s.ListReplicas(topo.ID); len(list) != 1 || !list[0].Paused {
		t.Fatalf("ListReplicas = %+v", list)
	}
	if err := s.SetReplicaPaused(r.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReplicaByServer(r.ID); got.Paused {
		t.Fatal("paused = true after SetReplicaPaused(false)")
	}
	if err := s.SetReplicaPaused(m.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetReplicaPaused non-replica: %v", err)
	}
	if ReplicaResyncing != "resyncing" {
		t.Fatalf("ReplicaResyncing = %q", ReplicaResyncing)
	}
}

func TestSnoozeRoundTrip(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db")
	if srv.SnoozeUntil != nil {
		t.Fatalf("new server snooze = %v", srv.SnoozeUntil)
	}
	until := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	if err := s.SetSnooze(srv.ID, &until); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(srv.ID)
	if err != nil || got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(until) {
		t.Fatalf("GetServer = %+v, %v", got, err)
	}
	if list, _ := s.ListServers(); len(list) != 1 || list[0].SnoozeUntil == nil {
		t.Fatalf("ListServers = %+v", list)
	}
	if err := s.SetSnooze(srv.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetServer(srv.ID); got.SnoozeUntil != nil {
		t.Fatalf("snooze after clear = %v", got.SnoozeUntil)
	}
	if err := s.SetSnooze(999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSnooze missing: %v", err)
	}
}

func TestGetSettings(t *testing.T) {
	s := openTemp(t)
	if err := s.SetSetting(SettingLagThreshold, "30"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(SettingDiskWarn, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSettings(SettingLagThreshold, SettingDiskWarn, SettingDiskCrit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[SettingLagThreshold] != "30" {
		t.Fatalf("GetSettings = %v", got)
	}
	if _, ok := got[SettingDiskCrit]; ok {
		t.Fatal("missing key present in map")
	}
	if got, err := s.GetSettings(); err != nil || len(got) != 0 {
		t.Fatalf("GetSettings() = %v, %v", got, err)
	}
}
