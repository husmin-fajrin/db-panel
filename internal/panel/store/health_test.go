package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHealthUpsert(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")

	if _, err := s.GetHealth(srv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHealth before set: %v", err)
	}
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	h := Health{ServerID: srv.ID, ReceivedAt: t0, SnapshotJSON: `{"a":1}`, Status: "ok",
		StatusSince: t0.Add(-time.Hour), LockOK: true, AgentVersion: "1.0"}
	if err := s.SetHealth(h); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	got, err := s.GetHealth(srv.ID)
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("GetHealth = %+v, %v; want %+v", got, err, h)
	}

	h.Status, h.LockOK, h.ReceivedAt = "down", false, t0.Add(time.Minute)
	if err := s.SetHealth(h); err != nil {
		t.Fatalf("SetHealth update: %v", err)
	}
	got, err = s.GetHealth(srv.ID)
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("GetHealth after update = %+v, %v; want %+v", got, err, h)
	}

	other := mustServer(t, s, "db2")
	if err := s.SetHealth(Health{ServerID: other.ID, ReceivedAt: t0, StatusSince: t0, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListHealth()
	if err != nil || len(list) != 2 || list[0].ServerID != srv.ID || list[1].ServerID != other.ID {
		t.Fatalf("ListHealth = %+v, %v", list, err)
	}

	if err := s.SetHealth(Health{ServerID: 999, ReceivedAt: t0, StatusSince: t0}); err == nil {
		t.Fatal("SetHealth for a missing server succeeded")
	}
}

func TestStatusEventsOrderAndPrune(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	b := mustServer(t, s, "b")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	add := func(server int64, h int, to string) {
		t.Helper()
		id, err := s.AddStatusEvent(StatusEvent{ServerID: server, TopologyID: 7, TS: base.Add(time.Duration(h) * time.Hour),
			Kind: "status", FromStatus: "ok", ToStatus: to, Detail: "d"})
		if err != nil || id == 0 {
			t.Fatalf("AddStatusEvent: %d, %v", id, err)
		}
	}
	add(a.ID, 1, "e1")
	add(a.ID, 3, "e3")
	add(b.ID, 2, "e2")
	add(a.ID, 5, "e5")

	got, err := s.ListStatusEvents(a.ID, base.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ToStatus != "e5" || got[1].ToStatus != "e3" {
		t.Fatalf("ListStatusEvents (newest first, since) = %+v", got)
	}
	if got[0].TopologyID != 7 || got[0].Kind != "status" || got[0].FromStatus != "ok" || got[0].Detail != "d" || got[0].ServerID != a.ID {
		t.Fatalf("event fields = %+v", got[0])
	}
	got, err = s.ListStatusEvents(a.ID, base, 1)
	if err != nil || len(got) != 1 || got[0].ToStatus != "e5" {
		t.Fatalf("ListStatusEvents limit = %+v, %v", got, err)
	}

	all, err := s.ListStatusEventsAll(base.Add(2 * time.Hour))
	if err != nil || len(all) != 3 || all[0].ToStatus != "e2" || all[1].ToStatus != "e3" || all[2].ToStatus != "e5" {
		t.Fatalf("ListStatusEventsAll (ascending) = %+v, %v", all, err)
	}

	n, err := s.PruneStatusEvents(base.Add(3 * time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("PruneStatusEvents = %d, %v; want 2", n, err)
	}
	all, err = s.ListStatusEventsAll(base)
	if err != nil || len(all) != 2 || all[0].ToStatus != "e3" {
		t.Fatalf("after prune = %+v, %v", all, err)
	}
}

func TestDeleteServerCascadesHealthAndAlerts(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "gone")
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if err := s.SetHealth(Health{ServerID: srv.ID, ReceivedAt: now, StatusSince: now, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddStatusEvent(StatusEvent{ServerID: srv.ID, TS: now, Kind: "status"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenAlert(Alert{Kind: "down", ServerID: srv.ID, OpenedAt: now, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAudit(AuditEntry{TS: now, Username: "admin", Action: "x", TargetKind: "server", TargetID: srv.ID, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteServer(srv.ID); err != nil {
		t.Fatalf("DeleteServer must work although audit rows reference the server: %v", err)
	}
	if _, err := s.GetHealth(srv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("health after delete: %v", err)
	}
	if ev, _ := s.ListStatusEventsAll(time.Time{}); len(ev) != 0 {
		t.Fatalf("status events after delete = %+v", ev)
	}
	if _, err := s.GetOpenAlert("down", srv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("alert after delete: %v", err)
	}
	if al, _ := s.ListAlerts(time.Time{}, 10); len(al) != 0 {
		t.Fatalf("alerts after delete = %+v", al)
	}
	// The audit trail survives the server.
	if au, err := s.ListAudit(srv.ID, time.Time{}, 10); err != nil || len(au) != 1 {
		t.Fatalf("audit after delete = %+v, %v", au, err)
	}
}
