package store

import (
	"fmt"
	"time"
)

// StartMarks returns the start marks of serverID by alert kind: the decided
// time of the newest start the panel has accounted for.
func (s *Store) StartMarks(serverID int64) (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT kind, decided_at FROM start_marks WHERE server_id = ?`, serverID)
	if err != nil {
		return nil, fmt.Errorf("start marks: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var kind, at string
		if err := rows.Scan(&kind, &at); err != nil {
			return nil, fmt.Errorf("scan start mark: %w", err)
		}
		t, err := parseTime(at)
		if err != nil {
			return nil, err
		}
		out[kind] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("start marks: %w", err)
	}
	return out, nil
}

// RaiseStartMark sets the mark of (serverID, kind) to t when t is later than
// the stored one (or there is none): a mark never moves back.
func (s *Store) RaiseStartMark(serverID int64, kind string, t time.Time) error {
	_, err := s.db.Exec(`INSERT INTO start_marks (server_id, kind, decided_at) VALUES (?, ?, ?)
		ON CONFLICT(server_id, kind) DO UPDATE SET decided_at = excluded.decided_at
		WHERE excluded.decided_at > start_marks.decided_at`, serverID, kind, formatTime(t))
	if err != nil {
		return fmt.Errorf("raise start mark: %w", err)
	}
	return nil
}
