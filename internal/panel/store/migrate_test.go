package store

import (
	"errors"
	"path/filepath"
	"testing"
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
	if n != 8 {
		t.Fatalf("schema_migrations rows = %d, want 8", n)
	}
}

// TestMigrate0004Applies: the monitoring tables and columns exist (0004
// to 0008) and the schema is at version 8.
func TestMigrate0004Applies(t *testing.T) {
	s := openTemp(t)
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 8 {
		t.Fatalf("schema version = %d, %v; want 8", v, err)
	}
	for _, table := range []string{"server_health", "status_events", "alerts", "audit_log", "confirmations", "server_tls"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}
	for _, col := range [][2]string{{"replicas", "paused"}, {"servers", "snooze_until"}, {"alerts", "close_reason"}, {"replicas", "tls_verified"}, {"server_tls", "hosts"}} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s.%s missing (n=%d, err=%v)", col[0], col[1], n, err)
		}
	}
}
