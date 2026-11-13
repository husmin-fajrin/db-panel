package store

import "testing"

func TestStartMarksReplaceAndCascade(t *testing.T) {
	s := openTemp(t)
	srv, err := s.CreateServer("db1")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := s.StartMarks(srv.ID); err != nil || len(m) != 0 {
		t.Fatalf("marks of a new server = %v, %v", m, err)
	}
	// An id is an identity: the last one set stands, whatever its order.
	for _, id := range []string{"2026-11-02T10:00:00Z", "2026-11-02T11:00:00Z", "2026-11-02T09:00:00Z"} {
		if err := s.SetStartMark(srv.ID, "master_start_late", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetStartMark(srv.ID, "master_start_blocked", "x"); err != nil {
		t.Fatal(err)
	}
	m, err := s.StartMarks(srv.ID)
	if err != nil || len(m) != 2 || m["master_start_late"] != "2026-11-02T09:00:00Z" || m["master_start_blocked"] != "x" {
		t.Fatalf("marks = %v, %v", m, err)
	}
	if err := s.DeleteServer(srv.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := s.StartMarks(srv.ID); err != nil || len(m) != 0 {
		t.Fatalf("marks after the server was deleted = %v, %v", m, err)
	}
}
