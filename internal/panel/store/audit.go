package store

import (
	"fmt"
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

// AddAudit appends e to the audit log and returns its id.
func (s *Store) AddAudit(e AuditEntry) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO audit_log (ts, username, ip, action, target_kind, target_id, detail_json, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(e.TS), e.Username, e.IP, e.Action, e.TargetKind, e.TargetID, e.DetailJSON, e.Outcome)
	if err != nil {
		return 0, fmt.Errorf("add audit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add audit: %w", err)
	}
	return id, nil
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
