package store

import (
	"strings"
	"testing"
	"time"
)

func TestAuditIsAppendOnly(t *testing.T) {
	s := openTemp(t)
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	id, err := s.AddAudit(AuditEntry{TS: t0, Username: "admin", IP: "10.0.0.1", Action: "replica.pause",
		TargetKind: "server", TargetID: 5, DetailJSON: `{"x":1}`, Outcome: "ok"})
	if err != nil || id == 0 {
		t.Fatalf("AddAudit: %d, %v", id, err)
	}
	if _, err := s.db.Exec(`UPDATE audit_log SET outcome = 'x' WHERE id = ?`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE err = %v, want append-only", err)
	}
	if _, err := s.db.Exec(`DELETE FROM audit_log WHERE id = ?`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE err = %v, want append-only", err)
	}
	got, err := s.ListAudit(0, time.Time{}, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListAudit = %+v, %v", got, err)
	}
	want := AuditEntry{ID: id, TS: t0, Username: "admin", IP: "10.0.0.1", Action: "replica.pause",
		TargetKind: "server", TargetID: 5, DetailJSON: `{"x":1}`, Outcome: "ok"}
	if got[0] != want {
		t.Fatalf("entry = %+v, want %+v", got[0], want)
	}
}

func TestListAuditFilterOrderLimit(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, target := range []int64{1, 2, 1, 1} {
		if _, err := s.AddAudit(AuditEntry{TS: base.Add(time.Duration(i) * time.Hour), Action: "a", TargetID: target, Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListAudit(0, time.Time{}, 10)
	if err != nil || len(all) != 4 || !all[0].TS.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("all = %+v, %v", all, err)
	}
	one, err := s.ListAudit(1, base.Add(time.Hour), 10)
	if err != nil || len(one) != 2 || !one[0].TS.After(one[1].TS) {
		t.Fatalf("server 1 since = %+v, %v", one, err)
	}
	if lim, _ := s.ListAudit(0, time.Time{}, 2); len(lim) != 2 {
		t.Fatalf("limit = %+v", lim)
	}
}
