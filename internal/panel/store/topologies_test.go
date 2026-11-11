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
	if err := s.SetSnooze(srv.ID, &until, until.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(srv.ID)
	if err != nil || got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(until) {
		t.Fatalf("GetServer = %+v, %v", got, err)
	}
	if list, _ := s.ListServers(); len(list) != 1 || list[0].SnoozeUntil == nil {
		t.Fatalf("ListServers = %+v", list)
	}
	cancelAt := until.Add(-time.Hour)
	if err := s.SetSnooze(srv.ID, nil, cancelAt); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetServer(srv.ID); got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(cancelAt) {
		t.Fatalf("snooze after cancel = %v, want it ended at %v", got.SnoozeUntil, cancelAt)
	}
	if err := s.SetSnooze(999, nil, cancelAt); !errors.Is(err, ErrNotFound) {
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

var snoozeT0 = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// TestSetSnoozeKeepsFromWhenExtended: a snooze that starts records its
// start; extending or shortening it while it runs keeps the start; a snooze
// begun after the previous one ended starts anew.
func TestSetSnoozeKeepsFromWhenExtended(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db")
	until := snoozeT0.Add(2 * time.Hour)
	if err := s.SetSnooze(srv.ID, &until, snoozeT0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetServer(srv.ID)
	if got.SnoozeFrom == nil || !got.SnoozeFrom.Equal(snoozeT0) || !got.SnoozeUntil.Equal(until) {
		t.Fatalf("after start: from %v until %v", got.SnoozeFrom, got.SnoozeUntil)
	}

	// Extended an hour in: until moves, from stays.
	extended := snoozeT0.Add(10 * time.Hour)
	if err := s.SetSnooze(srv.ID, &extended, snoozeT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetServer(srv.ID)
	if got.SnoozeFrom == nil || !got.SnoozeFrom.Equal(snoozeT0) || !got.SnoozeUntil.Equal(extended) {
		t.Fatalf("after extend: from %v until %v; want from %v until %v", got.SnoozeFrom, got.SnoozeUntil, snoozeT0, extended)
	}

	// Once it ended, a new one starts at its own now.
	later := snoozeT0.Add(12 * time.Hour)
	next := later.Add(time.Hour)
	if err := s.SetSnooze(srv.ID, &next, later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetServer(srv.ID)
	if got.SnoozeFrom == nil || !got.SnoozeFrom.Equal(later) || !got.SnoozeUntil.Equal(next) {
		t.Fatalf("after a new snooze: from %v until %v; want from %v until %v", got.SnoozeFrom, got.SnoozeUntil, later, next)
	}
}

// TestCancelSnoozeEndsNowAndKeepsFrom: cancelling a running snooze ends it
// now and keeps snooze_from, so its digest is still owed; cancelling when
// none runs changes nothing, not even for a snooze whose digest is owed.
func TestCancelSnoozeEndsNowAndKeepsFrom(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db")
	until := snoozeT0.Add(8 * time.Hour)
	if err := s.SetSnooze(srv.ID, &until, snoozeT0); err != nil {
		t.Fatal(err)
	}
	cancelAt := snoozeT0.Add(time.Hour)
	if err := s.SetSnooze(srv.ID, nil, cancelAt); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetServer(srv.ID)
	if got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(cancelAt) || got.SnoozeFrom == nil || !got.SnoozeFrom.Equal(snoozeT0) {
		t.Fatalf("after cancel: from %v until %v; want from %v until %v", got.SnoozeFrom, got.SnoozeUntil, snoozeT0, cancelAt)
	}
	spans, err := s.EndedSnoozes(cancelAt)
	if err != nil || len(spans) != 1 || spans[0].ServerID != srv.ID || !spans[0].From.Equal(snoozeT0) || !spans[0].Until.Equal(cancelAt) {
		t.Fatalf("EndedSnoozes after cancel = %+v, %v", spans, err)
	}

	// Cancelling again, later: nothing is running, nothing changes.
	if err := s.SetSnooze(srv.ID, nil, cancelAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetServer(srv.ID)
	if !again.SnoozeUntil.Equal(cancelAt) || again.SnoozeFrom == nil || !again.SnoozeFrom.Equal(snoozeT0) {
		t.Fatalf("second cancel changed the server: from %v until %v", again.SnoozeFrom, again.SnoozeUntil)
	}
	// A server never snoozed: still no error, still nothing.
	other := mustServer(t, s, "db2")
	if err := s.SetSnooze(other.ID, nil, snoozeT0); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetServer(other.ID); o.SnoozeUntil != nil || o.SnoozeFrom != nil {
		t.Fatalf("cancel on an unsnoozed server set %v / %v", o.SnoozeUntil, o.SnoozeFrom)
	}
}

// TestEndedSnoozesAndClear: a snooze is listed once it ended and until its
// digest is cleared; a running one and one with no digest owed are not.
func TestEndedSnoozesAndClear(t *testing.T) {
	s := openTemp(t)
	a, b := mustServer(t, s, "a"), mustServer(t, s, "b")
	ua, ub := snoozeT0.Add(time.Hour), snoozeT0.Add(5*time.Hour)
	if err := s.SetSnooze(a.ID, &ua, snoozeT0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSnooze(b.ID, &ub, snoozeT0); err != nil {
		t.Fatal(err)
	}
	if spans, err := s.EndedSnoozes(snoozeT0.Add(time.Hour - time.Second)); err != nil || len(spans) != 0 {
		t.Fatalf("before the end: %+v, %v", spans, err)
	}
	spans, err := s.EndedSnoozes(snoozeT0.Add(time.Hour))
	if err != nil || len(spans) != 1 || spans[0].ServerID != a.ID || !spans[0].Until.Equal(ua) {
		t.Fatalf("at the end of a: %+v, %v", spans, err)
	}
	if err := s.ClearSnoozeFrom(a.ID); err != nil {
		t.Fatal(err)
	}
	if spans, err := s.EndedSnoozes(snoozeT0.Add(2 * time.Hour)); err != nil || len(spans) != 0 {
		t.Fatalf("after clearing: %+v, %v", spans, err)
	}
	if err := s.ClearSnoozeFrom(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClearSnoozeFrom missing: %v", err)
	}
}

// TestAlertsOpenedBetween: opened_at in [from, to), closed alerts
// included, only the server's own, oldest first.
func TestAlertsOpenedBetween(t *testing.T) {
	s := openTemp(t)
	srv, other := mustServer(t, s, "a"), mustServer(t, s, "b")
	open := func(id int64, at time.Time, kind string) int64 {
		aid, err := s.OpenAlert(Alert{Kind: kind, ServerID: id, OpenedAt: at, Message: "m"})
		if err != nil {
			t.Fatal(err)
		}
		return aid
	}
	open(srv.ID, snoozeT0.Add(-time.Second), "disk_warn") // before
	in2 := open(srv.ID, snoozeT0.Add(30*time.Minute), "agent_offline")
	in1 := open(srv.ID, snoozeT0, "master_down")       // from is inclusive
	open(srv.ID, snoozeT0.Add(time.Hour), "disk_crit") // to is exclusive
	open(other.ID, snoozeT0.Add(time.Minute), "master_down")
	if err := s.CloseAlert(in2, snoozeT0.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := s.AlertsOpenedBetween(srv.ID, snoozeT0, snoozeT0.Add(time.Hour))
	if err != nil || len(got) != 2 || got[0].ID != in1 || got[1].ID != in2 || got[1].ClosedAt == nil {
		t.Fatalf("AlertsOpenedBetween = %+v, %v", got, err)
	}
}
