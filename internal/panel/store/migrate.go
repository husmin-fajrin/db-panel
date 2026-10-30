package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNewerSchema is Open's error for a database that a newer dbpanel has
// migrated past the last migration this binary ships (a downgrade). Running
// on a schema the code does not know could corrupt it, so nothing is applied.
var ErrNewerSchema = errors.New("database dibuat oleh versi dbpanel yang lebih baru")

type migration struct {
	name    string
	version int
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	latest := 0
	if n := len(migrations); n > 0 {
		latest = migrations[n-1].version
	}
	var applied sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if applied.Valid && applied.Int64 > int64(latest) {
		return fmt.Errorf("%w (skema versi %d, versi ini mengenal sampai %d)", ErrNewerSchema, applied.Int64, latest)
	}

	for _, m := range migrations {
		body, err := migrationsFS.ReadFile(path.Join("migrations", m.name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		if err := applyMigration(db, m.version, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
	}
	return nil
}

// embeddedMigrations lists the embedded migration files in ascending version
// order.
func embeddedMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version prefix: %w", e.Name(), err)
		}
		out = append(out, migration{name: e.Name(), version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// applyMigration runs body and records version in one transaction, unless
// version is already recorded; the check is inside the transaction so it
// sees the same state the migration is applied to.
func applyMigration(db *sql.DB, version int, body string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applied int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
		return fmt.Errorf("check applied: %w", err)
	}
	if applied > 0 {
		return nil
	}
	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}
