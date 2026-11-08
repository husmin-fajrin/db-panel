package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// AuditEntry is one append-only record of an action in the panel. TargetID
// is not a foreign key: the record outlives its target.
type AuditEntry struct {
	ID                   int64
	TS                   time.Time
	Username, IP, Action string
	TargetKind           string
	TargetID             int64
	DetailJSON, Outcome  string
}

// AddAudit appends e to the audit log and returns its id. The entry is
// chained to the last one (SEC-45): reading the last hash and inserting
// happen in one BEGIN IMMEDIATE transaction, so the write lock is held
// before the read. Concurrent writers in this process serialize on the
// store's single connection; a writer in another process (a CLI command)
// is waited for (busy_timeout) instead of failing the insert with a stale
// snapshot, as a deferred transaction would.
func (s *Store) AddAudit(e AuditEntry) (id int64, err error) {
	rec, err := s.AddAuditRecord(e)
	return rec.ID, err
}

// AuditRecord is what AddAuditRecord stored for an entry: its id, the ts
// text and the chain hash exactly as in the row, for the journald copy
// (LogAudit).
type AuditRecord struct {
	ID       int64
	TS, Hash string
}

// AddAuditRecord is AddAudit returning the stored row's id, ts text and
// hash.
func (s *Store) AddAuditRecord(e AuditEntry) (rec AuditRecord, err error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return AuditRecord{}, fmt.Errorf("add audit: %w", err)
	}
	defer conn.Close()
	auditLockHook("begin")
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return AuditRecord{}, fmt.Errorf("add audit: %w", err)
	}
	auditLockHook("locked")
	committed := false
	defer func() {
		if !committed {
			if _, rbErr := conn.ExecContext(ctx, `ROLLBACK`); rbErr != nil {
				// Do not hand a connection with an open transaction back
				// to the pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	var prev string
	err = conn.QueryRowContext(ctx, `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AuditRecord{}, fmt.Errorf("add audit: last hash: %w", err)
	}
	ts, hash := formatTime(e.TS), AuditHash(prev, e)
	res, err := conn.ExecContext(ctx, `INSERT INTO audit_log (ts, username, ip, action, target_kind, target_id, detail_json, outcome, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, e.Username, e.IP, e.Action, e.TargetKind, e.TargetID, e.DetailJSON, e.Outcome, prev, hash)
	if err != nil {
		return AuditRecord{}, fmt.Errorf("add audit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return AuditRecord{}, fmt.Errorf("add audit: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return AuditRecord{}, fmt.Errorf("add audit: %w", err)
	}
	committed = true
	return AuditRecord{ID: id, TS: ts, Hash: hash}, nil
}

// auditLockHook is called by AddAuditRecord just before it asks for the
// write lock ("begin") and once it holds it ("locked"); tests replace it
// to see AddAudit wait for another writer.
var auditLockHook = func(stage string) {}

// LogAudit writes the second copy of an audit entry (SEC-45, SEC-77) to
// l, the panel's log, which goes to journald, out of reach of whoever
// can edit panel.db. It carries the stored fields, the ts text as stored
// and the row's chain hash, so the journal can anchor the chain: a row
// rewritten or removed no longer matches the hash logged for it. err is
// AddAuditRecord's error: the copy is written also when the database
// write failed (stored=false, id 0, no hash). Callers that keep a field
// out of the journal (a typed username's hash) pass e with it replaced.
func LogAudit(l *slog.Logger, e AuditEntry, rec AuditRecord, err error) {
	ts := rec.TS
	if ts == "" {
		ts = formatTime(e.TS)
	}
	l.Info("audit", "stored", err == nil, "id", rec.ID, "ts", ts, "hash", rec.Hash, "username", e.Username, "ip", e.IP,
		"action", e.Action, "target_kind", e.TargetKind, "target_id", e.TargetID,
		"detail_json", e.DetailJSON, "outcome", e.Outcome)
}

// ListAudit returns entries at or after since, newest first, at most limit.
// serverID filters on the entry's target id; 0 returns all entries.
func (s *Store) ListAudit(serverID int64, since time.Time, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, ts, username, ip, action, target_kind, target_id, detail_json, outcome
		FROM audit_log WHERE ts >= ? AND (? = 0 OR target_id = ?)
		ORDER BY ts DESC, id DESC LIMIT ?`, formatTime(since), serverID, serverID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var (
			e  AuditEntry
			ts string
		)
		if err := rows.Scan(&e.ID, &ts, &e.Username, &e.IP, &e.Action, &e.TargetKind, &e.TargetID, &e.DetailJSON, &e.Outcome); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		if e.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
