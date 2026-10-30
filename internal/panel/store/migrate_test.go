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
	if n != 3 {
		t.Fatalf("schema_migrations rows = %d, want 3", n)
	}
}
