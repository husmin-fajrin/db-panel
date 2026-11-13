package store

import (
	"testing"
	"time"
)

func TestStartMarksOnlyMoveForwardAndCascade(t *testing.T) {
	s := openTemp(t)
	srv, err := s.CreateServer("db1")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if m, err := s.StartMarks(srv.ID); err != nil || len(m) != 0 {
		t.Fatalf("marks of a new server = %v, %v", m, err)
	}
	for _, at := range []time.Time{t0, t0.Add(time.Hour), t0.Add(time.Minute)} {
		if err := s.RaiseStartMark(srv.ID, "master_start_late", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RaiseStartMark(srv.ID, "master_start_blocked", t0); err != nil {
		t.Fatal(err)
	}
	m, err := s.StartMarks(srv.ID)
	if err != nil || len(m) != 2 || !m["master_start_late"].Equal(t0.Add(time.Hour)) || !m["master_start_blocked"].Equal(t0) {
		t.Fatalf("marks = %v, %v; want late at +1h (never back), blocked at t0", m, err)
	}
	if err := s.DeleteServer(srv.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := s.StartMarks(srv.ID); err != nil || len(m) != 0 {
		t.Fatalf("marks after the server was deleted = %v, %v", m, err)
	}
}
