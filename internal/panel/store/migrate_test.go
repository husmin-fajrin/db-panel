package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenRefusesNewerSchema: a database migrated by a newer dbpanel (a
// schema version this binary does not ship) is refused before anything is
// applied, instead of being run against code that does not know its schema.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations (version) VALUES (9999)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if !errors.Is(err, ErrNewerSchema) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open newer database: err = %v, want ErrNewerSchema", err)
	}
	if s != nil {
		t.Fatal("Open returned a store together with an error")
	}
}

// TestMigrateSkipsApplied: running migrate again on a
// database that has every migration applied changes nothing.
func TestMigrateSkipsApplied(t *testing.T) {
	s := openTemp(t)
	if err := migrate(s.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("schema_migrations rows = %d, want 9", n)
	}
}

// TestMigrate0004Applies: the monitoring tables and columns exist (0004
// to 0009) and the schema is at version 9.
func TestMigrate0004Applies(t *testing.T) {
	s := openTemp(t)
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 9 {
		t.Fatalf("schema version = %d, %v; want 9", v, err)
	}
	for _, table := range []string{"server_health", "status_events", "alerts", "audit_log", "confirmations", "server_tls"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}
	for _, col := range [][2]string{{"replicas", "paused"}, {"servers", "snooze_until"}, {"alerts", "close_reason"}, {"replicas", "tls_verified"}, {"server_tls", "hosts"}, {"audit_log", "prev_hash"}, {"audit_log", "hash"}} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s.%s missing (n=%d, err=%v)", col[0], col[1], n, err)
		}
	}
}

// openAtVersion creates a database at path with the migrations up to
// version applied, the way an older dbpanel left it.
func openAtVersion(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ms, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > version {
			break
		}
		body, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(db, m.version, string(body)); err != nil {
			t.Fatalf("migration %s: %v", m.name, err)
		}
	}
	return db
}

// fillOldAudit inserts n audit rows into a schema-8 audit log (no chain
// columns), in batches.
func fillOldAudit(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO audit_log (ts, username, ip, action, target_kind, target_id, detail_json, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		if _, err := stmt.Exec(formatTime(base.Add(time.Duration(i)*time.Minute)), "admin", "10.0.0.1", "server_check",
			"server", i%7, fmt.Sprintf(`{"i":%d}`, i), "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestMigrate0009BackfillsEmptyAndLarge (Review Focus 2): upgrading a
// database with 0 and with 10,000 audit rows leaves an intact chain over
// every row, the append-only triggers back in place, and the schema at 9;
// opening it again does not run 0009 twice.
func TestMigrate0009BackfillsEmptyAndLarge(t *testing.T) {
	for _, n := range []int{0, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "panel.db")
			db := openAtVersion(t, path, 8)
			fillOldAudit(t, db, n)
			db.Close()

			start := time.Now()
			s, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Logf("migration 0009 over %d rows: %v", n, time.Since(start))
			defer s.Close()
			var v int
			if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 9 {
				t.Fatalf("schema version = %d, %v", v, err)
			}
			lastOK, brokenAt, err := s.VerifyAuditChain()
			if err != nil || brokenAt != 0 || lastOK != int64(n) {
				t.Fatalf("VerifyAuditChain = %d, %d, %v; want %d, 0", lastOK, brokenAt, err, n)
			}
			var empty int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE hash = ''`).Scan(&empty); err != nil || empty != 0 {
				t.Fatalf("rows without hash = %d, %v", empty, err)
			}
			// A new entry continues the backfilled chain.
			id, err := s.AddAudit(AuditEntry{TS: time.Now(), Action: "login", Outcome: "ok"})
			if err != nil {
				t.Fatal(err)
			}
			if lastOK, brokenAt, err := s.VerifyAuditChain(); err != nil || brokenAt != 0 || lastOK != id {
				t.Fatalf("after AddAudit: %d, %d, %v; want %d, 0", lastOK, brokenAt, err, id)
			}
			if _, err := s.db.Exec(`UPDATE audit_log SET outcome = 'x' WHERE id = ?`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("UPDATE after migration: err = %v, want append-only", err)
			}
			if _, err := s.db.Exec(`DELETE FROM audit_log WHERE id = ?`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("DELETE after migration: err = %v, want append-only", err)
			}
			s.Close()

			// Reopening does not backfill again (it would succeed silently,
			// so check the version rows instead).
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 9`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("version 9 rows = %d, %v", rows, err)
			}
		})
	}
}

// TestMigrate0009RollsBackOnFailure: a backfill that fails part way leaves
// the database as it was: no chain columns, triggers in place, schema 8;
// the next start applies 0009 whole.
func TestMigrate0009RollsBackOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	db := openAtVersion(t, path, 8)
	fillOldAudit(t, db, 50)
	db.Close()

	old := migrationHooks[9]
	migrationHooks[9] = func(tx *sql.Tx) error {
		if err := old(tx); err != nil {
			return err
		}
		return errors.New("simulated crash")
	}
	_, err := Open(path)
	migrationHooks[9] = old
	if err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("Open with failing hook: err = %v", err)
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var cols, triggers, v int
	raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name IN ('prev_hash', 'hash')`).Scan(&cols)
	raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'audit_log'`).Scan(&triggers)
	raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	raw.Close()
	if cols != 0 || triggers != 2 || v != 8 {
		t.Fatalf("after failed migration: chain columns %d, triggers %d, version %d; want 0, 2, 8", cols, triggers, v)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if lastOK, brokenAt, err := s.VerifyAuditChain(); err != nil || lastOK != 50 || brokenAt != 0 {
		t.Fatalf("VerifyAuditChain = %d, %d, %v", lastOK, brokenAt, err)
	}
}

// TestOpenReadOnly: the read-only store reads the chain but cannot write,
// and refuses a database the panel has not migrated yet.
func TestOpenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAudit(AuditEntry{TS: time.Now(), Action: "login", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	if lastOK, brokenAt, err := ro.VerifyAuditChain(); err != nil || lastOK != 1 || brokenAt != 0 {
		t.Fatalf("VerifyAuditChain = %d, %d, %v", lastOK, brokenAt, err)
	}
	if _, err := ro.AddAudit(AuditEntry{TS: time.Now(), Action: "login"}); err == nil {
		t.Fatal("AddAudit on a read-only store succeeded")
	}

	old := filepath.Join(t.TempDir(), "panel.db")
	openAtVersion(t, old, 8).Close()
	if _, err := OpenReadOnly(old); !errors.Is(err, ErrSchemaTooOld) {
		t.Fatalf("OpenReadOnly on schema 8: err = %v, want ErrSchemaTooOld", err)
	}
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("OpenReadOnly on a missing file succeeded")
	}
}
