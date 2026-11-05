package store

import (
	"errors"
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
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("second consume: %v, want ErrConfirmInvalid", err)
	}
	if err := s.ConsumeConfirmation("nope", "admin", "delete", "server", 7, confNow); !errors.Is(err, ErrConfirmInvalid) {
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
		err := s.ConsumeConfirmation("h1", args[0].(string), args[1].(string), args[2].(string), args[3].(int64), confNow)
		if !errors.Is(err, ErrConfirmInvalid) {
			t.Errorf("different %s: %v, want ErrConfirmInvalid", name, err)
		}
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, confNow); err != nil {
		t.Fatalf("original after mismatches: %v", err)
	}
}

func TestConfirmationExpires(t *testing.T) {
	s := openTemp(t)
	c := newConf()
	if err := s.AddConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, c.ExpiresAt); !errors.Is(err, ErrConfirmInvalid) {
		t.Fatalf("now == ExpiresAt: %v, want ErrConfirmInvalid", err)
	}
	if err := s.ConsumeConfirmation("h1", "admin", "delete", "server", 7, c.ExpiresAt.Add(-time.Second)); err != nil {
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
