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

// TestAlertCloseReasonAndNeighbours: a close reason is kept; the previous
// alert's close time, a later alert and the count opened since a time are
// read per kind and server.
func TestAlertCloseReasonAndNeighbours(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	b := mustServer(t, s, "b")
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	id1, _ := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0})
	if prev, err := s.PreviousAlertClosedAt("down", a.ID, id1); err != nil || prev != nil {
		t.Fatalf("previous of the first = %v, %v", prev, err)
	}
	if err := s.CloseAlertWithReason(id1, t0.Add(time.Minute), "Resync ulang dimulai"); err != nil {
		t.Fatal(err)
	}
	s.OpenAlert(Alert{Kind: "down", ServerID: b.ID, OpenedAt: t0.Add(2 * time.Minute)})
	id2, _ := s.OpenAlert(Alert{Kind: "down", ServerID: a.ID, OpenedAt: t0.Add(3 * time.Minute)})
	prev, err := s.PreviousAlertClosedAt("down", a.ID, id2)
	if err != nil || prev == nil || !prev.Equal(t0.Add(time.Minute)) {
		t.Fatalf("previous closed at = %v, %v", prev, err)
	}
	if later, err := s.LaterAlertExists("down", a.ID, id1, t0.Add(time.Hour)); err != nil || !later {
		t.Fatalf("later after the first = %v, %v", later, err)
	}
	if later, _ := s.LaterAlertExists("down", a.ID, id1, t0.Add(3*time.Minute)); later {
		t.Fatal("later after the first, opened before 10:03")
	}
	if later, _ := s.LaterAlertExists("down", a.ID, id2, t0.Add(time.Hour)); later {
		t.Fatal("later after the newest")
	}
	if n, err := s.CountAlertsOpenedSince("down", a.ID, t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("count since = %d, %v", n, err)
	}
	all, _ := s.ListAlerts(t0, 10)
	for _, x := range all {
		want := ""
		if x.ID == id1 {
			want = "Resync ulang dimulai"
		}
		if x.CloseReason != want {
			t.Errorf("alert %d close reason = %q, want %q", x.ID, x.CloseReason, want)
		}
	}
}

// TestPruneAlertsClosedBefore: only alerts closed before the cutoff go;
// open alerts stay however old they are.
func TestPruneAlertsClosedBefore(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	add := func(kind string, opened time.Time, closed *time.Time) int64 {
		t.Helper()
		id, err := s.OpenAlert(Alert{Kind: kind, ServerID: a.ID, OpenedAt: opened})
		if err != nil {
			t.Fatal(err)
		}
		if closed != nil {
			if err := s.CloseAlert(id, *closed); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	at := func(d time.Duration) *time.Time { ts := now.Add(-d); return &ts }
	oldClosed := add("down", now.Add(-100*day), at(91*day))
	recentClosed := add("lag", now.Add(-100*day), at(89*day))
	oldOpen := add("disk", now.Add(-400*day), nil)

	n, err := s.PruneAlertsClosedBefore(now.Add(-90 * day))
	if err != nil || n != 1 {
		t.Fatalf("PruneAlertsClosedBefore = %d, %v; want 1", n, err)
	}
	left, err := s.ListAlerts(time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, al := range left {
		ids[al.ID] = true
	}
	if ids[oldClosed] || !ids[recentClosed] || !ids[oldOpen] || len(left) != 2 {
		t.Fatalf("alerts left = %+v; want the recent closed and the old open one", left)
	}
}
