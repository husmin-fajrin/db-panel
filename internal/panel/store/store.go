// Package store is the panel's persistence layer, backed by SQLite.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("tidak ditemukan")

// Store wraps the panel database.
type Store struct {
	db *sql.DB

	// plain holds, per server, the plaintext of the pairing token this
	// process created most recently, so the server page can show the
	// pairing command again. The database only stores the token's hash.
	plainMu sync.Mutex
	plain   map[int64]plainToken
}

type plainToken struct{ hash, token string }

// dbFileMode is the mode of the database file and of the -wal and -shm
// files SQLite creates next to it (with the database file's mode): the
// database holds password hashes and pairing token hashes.
const dbFileMode = 0o600

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations. The database files are made readable by their
// owner only, whatever the umask.
func Open(path string) (*Store, error) {
	// Create the file first so SQLite's -wal and -shm inherit 0600.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, dbFileMode)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	f.Close()
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	// Tighten files created by an earlier version with a looser mode.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, dbFileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
			db.Close()
			return nil, fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return &Store{db: db, plain: make(map[int64]plainToken)}, nil
}

// ErrSchemaTooOld is OpenReadOnly's error for a database this version of
// dbpanel has not migrated yet: the panel has not been started since the
// upgrade.
var ErrSchemaTooOld = errors.New("basis data belum diperbarui ke versi ini; jalankan panel sekali dulu")

// OpenReadOnly opens the database at path for reading only: no migration
// runs and nothing can be written. The schema must be the one this binary
// ships.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	ms, err := embeddedMigrations()
	if err != nil {
		db.Close()
		return nil, err
	}
	var applied sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&applied); err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	switch latest := int64(ms[len(ms)-1].version); {
	case applied.Int64 > latest:
		db.Close()
		return nil, fmt.Errorf("%w (skema versi %d, versi ini mengenal sampai %d)", ErrNewerSchema, applied.Int64, latest)
	case applied.Int64 < latest:
		db.Close()
		return nil, fmt.Errorf("%w (skema versi %d, versi ini %d)", ErrSchemaTooOld, applied.Int64, latest)
	}
	return &Store{db: db, plain: make(map[int64]plainToken)}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", v, err)
	}
	return t.UTC(), nil
}

func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure
// on the given qualified column, e.g. "servers.name".
func isUniqueViolation(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}
