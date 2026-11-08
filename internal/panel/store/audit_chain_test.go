package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

func chainEntry(i int) AuditEntry {
	return AuditEntry{TS: time.Date(2026, 11, 5, 10, 0, i, 0, time.UTC), Username: "admin", IP: "10.0.0.1",
		Action: "server_check", TargetKind: "server", TargetID: int64(i), DetailJSON: `{"n":1}`, Outcome: "ok"}
}

// TestAuditHashDeterministic: the hash is the plan's canonical form, the
// same for the same input, and changes with every field and the previous
// hash.
func TestAuditHashDeterministic(t *testing.T) {
	e := AuditEntry{TS: time.Date(2026, 11, 5, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600)), Username: "admin",
		IP: "10.0.0.1", Action: "replica_stop", TargetKind: "replica", TargetID: 42, DetailJSON: `{"x":1}`, Outcome: "ok"}
	canon := "prev\n2026-11-05T03:00:00Z\nadmin\n10.0.0.1\nreplica_stop\nreplica\n42\n{\"x\":1}\nok"
	sum := sha256.Sum256([]byte(canon))
	want := hex.EncodeToString(sum[:])
	if got := AuditHash("prev", e); got != want {
		t.Fatalf("AuditHash = %s, want %s", got, want)
	}
	if AuditHash("prev", e) != AuditHash("prev", e) {
		t.Fatal("AuditHash not deterministic")
	}
	seen := map[string]string{want: "original"}
	variants := map[string]func(*AuditEntry){
		"ts":          func(x *AuditEntry) { x.TS = x.TS.Add(time.Second) },
		"username":    func(x *AuditEntry) { x.Username = "admin2" },
		"ip":          func(x *AuditEntry) { x.IP = "10.0.0.2" },
		"action":      func(x *AuditEntry) { x.Action = "replica_start" },
		"target_kind": func(x *AuditEntry) { x.TargetKind = "server" },
		"target_id":   func(x *AuditEntry) { x.TargetID = 43 },
		"detail_json": func(x *AuditEntry) { x.DetailJSON = `{"x":2}` },
		"outcome":     func(x *AuditEntry) { x.Outcome = "gagal" },
		// Moving text across a field boundary changes the hash too.
		"boundary": func(x *AuditEntry) { x.Username, x.IP = "admin\n10.0.0.1", "" },
	}
	for name, mod := range variants {
		v := e
		mod(&v)
		h := AuditHash("prev", v)
		if other, dup := seen[h]; dup {
			t.Errorf("changing %s gives the same hash as %s", name, other)
		}
		seen[h] = name
	}
	if AuditHash("", e) == want {
		t.Error("previous hash does not change the hash")
	}
}

// chainRow is a row's stored chain columns.
type chainRow struct {
	id         int64
	prev, hash string
}

func chainRows(t *testing.T, s *Store) []chainRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, prev_hash, hash FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []chainRow
	for rows.Next() {
		var r chainRow
		if err := rows.Scan(&r.id, &r.prev, &r.hash); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAddAuditChains: each entry stores the previous entry's hash and its
// own hash over it; 20 concurrent writers still leave one intact chain.
func TestAddAuditChains(t *testing.T) {
	s := openTemp(t)
	var entries []AuditEntry
	for i := range 3 {
		e := chainEntry(i)
		if _, err := s.AddAudit(e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	rows := chainRows(t, s)
	prev := ""
	for i, r := range rows {
		if r.prev != prev {
			t.Errorf("row %d prev_hash = %q, want %q", r.id, r.prev, prev)
		}
		if want := AuditHash(prev, entries[i]); r.hash != want {
			t.Errorf("row %d hash = %q, want %q", r.id, r.hash, want)
		}
		prev = r.hash
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AddAudit(chainEntry(100 + i)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent AddAudit: %v", err)
	}
	lastOK, brokenAt, err := s.VerifyAuditChain()
	if err != nil || brokenAt != 0 || lastOK != 23 {
		t.Fatalf("VerifyAuditChain = %d, %d, %v; want 23, 0, nil", lastOK, brokenAt, err)
	}
}

// tamper runs q on the audit log with the append-only triggers lifted, the
// way someone with the database file could.
func tamper(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	for _, stmt := range []string{`DROP TRIGGER IF EXISTS audit_log_no_update`, `DROP TRIGGER IF EXISTS audit_log_no_delete`} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestVerifyDetectsTamper: a row changed or removed directly in SQLite
// breaks the chain at the first affected row, even when the tamperer
// recomputes that row's own hash.
func TestVerifyDetectsTamper(t *testing.T) {
	fill := func(t *testing.T) *Store {
		s := openTemp(t)
		for i := range 5 {
			if _, err := s.AddAudit(chainEntry(i)); err != nil {
				t.Fatal(err)
			}
		}
		if lastOK, brokenAt, err := s.VerifyAuditChain(); err != nil || lastOK != 5 || brokenAt != 0 {
			t.Fatalf("intact chain: %d, %d, %v", lastOK, brokenAt, err)
		}
		return s
	}
	cases := []struct {
		name         string
		do           func(t *testing.T, s *Store)
		lastOK, want int64
	}{
		{"field changed", func(t *testing.T, s *Store) {
			tamper(t, s, `UPDATE audit_log SET outcome = 'ok (diubah)' WHERE id = 3`)
		}, 2, 3},
		{"first row changed", func(t *testing.T, s *Store) {
			tamper(t, s, `UPDATE audit_log SET username = 'orang-lain' WHERE id = 1`)
		}, 0, 1},
		{"row hash recomputed", func(t *testing.T, s *Store) {
			rows := chainRows(t, s)
			e := chainEntry(2)
			e.Outcome = "ok (diubah)"
			tamper(t, s, `UPDATE audit_log SET outcome = ?, hash = ? WHERE id = 3`, e.Outcome, AuditHash(rows[1].hash, e))
		}, 3, 4},
		{"row deleted", func(t *testing.T, s *Store) {
			tamper(t, s, `DELETE FROM audit_log WHERE id = 2`)
		}, 1, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fill(t)
			c.do(t, s)
			lastOK, brokenAt, err := s.VerifyAuditChain()
			if err != nil || brokenAt != c.want || lastOK != c.lastOK {
				t.Fatalf("VerifyAuditChain = %d, %d, %v; want %d, %d", lastOK, brokenAt, err, c.lastOK, c.want)
			}
		})
	}
}

// TestVerifyEmptyChain: an empty log is intact up to #0.
func TestVerifyEmptyChain(t *testing.T) {
	s := openTemp(t)
	if lastOK, brokenAt, err := s.VerifyAuditChain(); err != nil || lastOK != 0 || brokenAt != 0 {
		t.Fatalf("VerifyAuditChain = %d, %d, %v", lastOK, brokenAt, err)
	}
}
