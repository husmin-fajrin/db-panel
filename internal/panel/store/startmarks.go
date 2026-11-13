package store

import "fmt"

// StartMarks returns the start marks of serverID by alert kind: the
// start_id of the last start the panel has accounted for.
func (s *Store) StartMarks(serverID int64) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT kind, start_id FROM start_marks WHERE server_id = ?`, serverID)
	if err != nil {
		return nil, fmt.Errorf("start marks: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("scan start mark: %w", err)
		}
		out[kind] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("start marks: %w", err)
	}
	return out, nil
}

// SetStartMark sets the mark of (serverID, kind) to startID, replacing the
// stored one: ids are identities, not an order.
func (s *Store) SetStartMark(serverID int64, kind, startID string) error {
	_, err := s.db.Exec(`INSERT INTO start_marks (server_id, kind, start_id) VALUES (?, ?, ?)
		ON CONFLICT(server_id, kind) DO UPDATE SET start_id = excluded.start_id`, serverID, kind, startID)
	if err != nil {
		return fmt.Errorf("set start mark: %w", err)
	}
	return nil
}

// DeleteStartMark removes the mark of (serverID, kind), if any.
func (s *Store) DeleteStartMark(serverID int64, kind string) error {
	if _, err := s.db.Exec(`DELETE FROM start_marks WHERE server_id = ? AND kind = ?`, serverID, kind); err != nil {
		return fmt.Errorf("delete start mark: %w", err)
	}
	return nil
}
