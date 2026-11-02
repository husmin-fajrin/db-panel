package store

import (
	"errors"
	"testing"
	"time"
)

func TestOpenAlertRefusesDuplicateOpen(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	b := mustServer(t, s, "b")
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	id, err := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, TopologyID: 3, OpenedAt: t0, Message: "server a mati"})
	if err != nil || id == 0 {
		t.Fatalf("OpenAlert: %d, %v", id, err)
	}
	if _, err := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0.Add(time.Minute)}); !errors.Is(err, ErrAlertOpen) {
		t.Fatalf("duplicate OpenAlert err = %v, want ErrAlertOpen", err)
	}
	// Another kind or another server is independent.
	if _, err := s.OpenAlert(Alert{Kind: "lag", ServerID: a.ID, OpenedAt: t0}); err != nil {
		t.Fatalf("other kind: %v", err)
	}
	if _, err := s.OpenAlert(Alert{Kind: "down", ServerID: b.ID, OpenedAt: t0}); err != nil {
		t.Fatalf("other server: %v", err)
	}

	got, err := s.GetOpenAlert("down", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Kind != "down" || got.ServerID != a.ID || got.TopologyID != 3 || !got.OpenedAt.Equal(t0) ||
		got.ClosedAt != nil || got.LastNotifiedAt != nil || got.NotifyCount != 0 ||
		got.Message != "server a mati" || got.TelegramState != TelegramPending || got.TelegramError != "" {
		t.Fatalf("alert = %+v", got)
	}
	if _, err := s.GetOpenAlert("disk", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetOpenAlert missing: %v", err)
	}
	open, err := s.ListOpenAlerts()
	if err != nil || len(open) != 3 {
		t.Fatalf("ListOpenAlerts = %+v, %v", open, err)
	}
}

func TestCloseAlertThenReopen(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	id, err := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseAlert(id, t0.Add(time.Hour)); err != nil {
		t.Fatalf("CloseAlert: %v", err)
	}
	if err := s.CloseAlert(id, t0.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing a closed alert: %v", err)
	}
	if err := s.CloseAlert(999, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing a missing alert: %v", err)
	}
	if _, err := s.GetOpenAlert("down", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetOpenAlert after close: %v", err)
	}
	if open, _ := s.ListOpenAlerts(); len(open) != 0 {
		t.Fatalf("open alerts = %+v", open)
	}

	id2, err := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0.Add(3 * time.Hour)})
	if err != nil || id2 == id {
		t.Fatalf("reopen = %d, %v", id2, err)
	}

	all, err := s.ListAlerts(t0, 10)
	if err != nil || len(all) != 2 || all[0].ID != id2 || all[1].ID != id {
		t.Fatalf("ListAlerts (newest first) = %+v, %v", all, err)
	}
	if all[1].ClosedAt == nil || !all[1].ClosedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("closed_at = %v", all[1].ClosedAt)
	}
	if got, _ := s.ListAlerts(t0.Add(time.Hour), 10); len(got) != 1 || got[0].ID != id2 {
		t.Fatalf("ListAlerts since = %+v", got)
	}
	if got, _ := s.ListAlerts(t0, 1); len(got) != 1 {
		t.Fatalf("ListAlerts limit = %+v", got)
	}
}

func TestSetAlertNotifiedAndMessage(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	id, err := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0, Message: "old"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetAlertNotified(id, t0.Add(time.Minute), TelegramFailed, "timeout"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOpenAlert("down", a.ID)
	if got.NotifyCount != 0 || got.TelegramState != TelegramFailed || got.TelegramError != "timeout" ||
		got.LastNotifiedAt == nil || !got.LastNotifiedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after failed = %+v", got)
	}
	if err := s.SetAlertNotified(id, t0.Add(2*time.Minute), TelegramSent, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlertNotified(id, t0.Add(3*time.Minute), TelegramSent, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetOpenAlert("down", a.ID)
	if got.NotifyCount != 2 || got.TelegramState != TelegramSent || got.TelegramError != "" ||
		!got.LastNotifiedAt.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("after sent = %+v", got)
	}
	if err := s.SetAlertMessage(id, "new"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetOpenAlert("down", a.ID); got.Message != "new" {
		t.Fatalf("message = %q", got.Message)
	}
	if err := s.SetAlertNotified(999, t0, TelegramSent, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAlertNotified missing: %v", err)
	}
	if err := s.SetAlertMessage(999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAlertMessage missing: %v", err)
	}
}

func TestListAlertsToNotifyAndSetTelegramState(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	open := func(kind string, at time.Time) int64 {
		t.Helper()
		id, err := s.OpenAlert(Alert{Kind: kind, ServerID: a.ID, OpenedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	stillOpen := open("down", t0.Add(time.Minute))
	closedPending := open("lag", t0)
	recovered := open("disk", t0.Add(2*time.Minute))
	for _, id := range []int64{closedPending, recovered} {
		if err := s.CloseAlert(id, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAlertTelegramState(recovered, TelegramRecovered, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListAlertsToNotify()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != closedPending || got[1].ID != stillOpen {
		t.Fatalf("ListAlertsToNotify (oldest first, no recovered_sent) = %+v", got)
	}

	// SetAlertTelegramState touches neither the count nor the time.
	if err := s.SetAlertTelegramState(stillOpen, TelegramSent, "x"); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetOpenAlert("down", a.ID)
	if g.TelegramState != TelegramSent || g.TelegramError != "x" || g.NotifyCount != 0 || g.LastNotifiedAt != nil {
		t.Fatalf("after SetAlertTelegramState = %+v", g)
	}
	if err := s.SetAlertTelegramState(999, TelegramSent, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAlertTelegramState missing: %v", err)
	}
}
