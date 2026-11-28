package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var confNow = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func newConf() Confirmation {
	return Confirmation{TokenHash: "h1", Username: "admin", Action: "delete", TargetKind: "server", TargetID: 7, ExpiresAt: confNow.Add(time.Minute)}
}

func TestConfirmationConsumeOnce(t *testing.T) {
	s := openTemp(t)
	if err := s.AddConfirmation(newConf()); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, false); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, false); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("second consume: %v, want ErrConfirmInvalid", err)
	}
	if err := s.ConsumeConfirmation("nope", "admin", "delete", "server", 7, confNow, false); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("unknown token: %v, want ErrConfirmInvalid", err)
	}
}

func TestConfirmationBoundToUserActionTarget(t *testing.T) {
	s := openTemp(t)
	if err := s.AddConfirmation(newConf()); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][4]any{
		"user":   {"other", "delete", "server", int64(7)},
		"action": {"admin", "other", "server", int64(7)},
		"kind":   {"admin", "delete", "other", int64(7)},
		"id":     {"admin", "delete", "server", int64(8)},
	} {
		err := s.ConsumeConfirmation("h1", args[0].(string), args[1].(string), args[2].(string), args[3].(int64), confNow, false)
		if !errors.Is(err, ErrConfirmInvalid) {
			t.Errorf("different %s: %v, want ErrConfirmInvalid", name, err)
		}
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, false); err != nil {
		t.Fatalf("original after mismatches: %v", err)
	}
}

func TestConfirmationExpires(t *testing.T) {
	s := openTemp(t)
	c := newConf()
	if err := s.AddConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, c.ExpiresAt, false); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("now == ExpiresAt: %v, want ErrConfirmInvalid", err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, c.ExpiresAt.Add(-time.Second), false); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
}

func TestPruneConfirmations(t *testing.T) {
	s := openTemp(t)
	for i, exp := range []time.Duration{-2 * time.Hour, -time.Minute, time.Hour} {
		c := newConf()
		c.TokenHash = string(rune('a' + i))
		c.ExpiresAt = confNow.Add(exp)
		if err := s.AddConfirmation(c); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneConfirmations(confNow.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	n, err = s.PruneConfirmations(confNow.Add(2 * time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("prune all = %d, %v; want 2", n, err)
	}
}

// TestConfirmationViaWindow: rows from before 0014 are not window tokens;
// a heavy consumer (allowWindow false) refuses a window token without
// using it, a light one takes it.
func TestConfirmationViaWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	db := openAtVersion(t, path, 13)
	if _, err := db.Exec(`INSERT INTO confirmations (token_hash, username, action, target_kind, target_id, expires_at)
		VALUES ('old', 'admin', 'delete', 'server', 7, ?)`, formatTime(confNow.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	var via int
	if err := s.db.QueryRow(`SELECT via_window FROM confirmations WHERE token_hash = 'old'`).Scan(&via); err != nil || via != 0 {
		t.Fatalf("old row via_window = %d, %v; want 0", via, err)
	}
	if err := s.ConsumeConfirmation("old", "admin", "delete", "server", 7, confNow, false); err != nil {
		t.Fatalf("old row, heavy consumer: %v", err)
	}

	c := newConf()
	c.ViaWindow = true
	if err := s.AddConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, false); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("window token, heavy consumer: %v, want ErrConfirmInvalid", err)
	}
	var used sql.NullString
	if err := s.db.QueryRow(`SELECT via_window, used_at FROM confirmations WHERE token_hash = 'h1'`).Scan(&via, &used); err != nil {
		t.Fatal(err)
	}
	if via != 1 || used.Valid {
		t.Fatalf("window token after refusal: via_window %d, used_at %v; want 1 and empty", via, used)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, true); err != nil {
		t.Fatalf("window token, light consumer: %v", err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow, true); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("window token used twice: %v, want ErrConfirmInvalid", err)
	}
}
