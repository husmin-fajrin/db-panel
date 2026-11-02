package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Health is the latest health snapshot the panel holds for a server.
type Health struct {
	ServerID     int64
	ReceivedAt   time.Time
	SnapshotJSON string
	Status       string
	StatusSince  time.Time
	LockOK       bool
	AgentVersion string
}

const healthColumns = `server_id, received_at, snapshot_json, status, status_since, lock_ok, agent_version`

func scanHealth(r rowScanner) (Health, error) {
	var (
		h        Health
		received string
		since    string
		lockOK   int
	)
	if err := r.Scan(&h.ServerID, &received, &h.SnapshotJSON, &h.Status, &since, &lockOK, &h.AgentVersion); err != nil {
		return Health{}, err
	}
	h.LockOK = lockOK != 0
	var err error
	if h.ReceivedAt, err = parseTime(received); err != nil {
		return Health{}, err
	}
	if h.StatusSince, err = parseTime(since); err != nil {
		return Health{}, err
	}
	return h, nil
}

// SetHealth stores h as the server's latest health, replacing the previous
// one.
func (s *Store) SetHealth(h Health) error {
	lock := 0
	if h.LockOK {
		lock = 1
	}
	_, err := s.db.Exec(`INSERT INTO server_health (`+healthColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET received_at = excluded.received_at,
			snapshot_json = excluded.snapshot_json, status = excluded.status,
			status_since = excluded.status_since, lock_ok = excluded.lock_ok,
			agent_version = excluded.agent_version`,
		h.ServerID, formatTime(h.ReceivedAt), h.SnapshotJSON, h.Status, formatTime(h.StatusSince), lock, h.AgentVersion)
	if err != nil {
		return fmt.Errorf("set health: %w", err)
	}
	return nil
}

// GetHealth returns the server's latest health, or ErrNotFound.
func (s *Store) GetHealth(serverID int64) (Health, error) {
	h, err := scanHealth(s.db.QueryRow(`SELECT `+healthColumns+` FROM server_health WHERE server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Health{}, ErrNotFound
	}
	if err != nil {
		return Health{}, fmt.Errorf("get health: %w", err)
	}
	return h, nil
}

// ListHealth returns the latest health of every server that has one,
// ordered by server id.
func (s *Store) ListHealth() ([]Health, error) {
	rows, err := s.db.Query(`SELECT ` + healthColumns + ` FROM server_health ORDER BY server_id`)
	if err != nil {
		return nil, fmt.Errorf("list health: %w", err)
	}
	defer rows.Close()
	var out []Health
	for rows.Next() {
		h, err := scanHealth(rows)
		if err != nil {
			return nil, fmt.Errorf("scan health: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// StatusEvent is one recorded change of a server's status (or another
// noteworthy event, named by Kind).
type StatusEvent struct {
	ID, ServerID, TopologyID int64
	TS                       time.Time
	Kind, FromStatus         string
	ToStatus, Detail         string
}

const statusEventColumns = `id, server_id, topology_id, ts, kind, from_status, to_status, detail`

func scanStatusEvent(r rowScanner) (StatusEvent, error) {
	var (
		e  StatusEvent
		ts string
	)
	if err := r.Scan(&e.ID, &e.ServerID, &e.TopologyID, &ts, &e.Kind, &e.FromStatus, &e.ToStatus, &e.Detail); err != nil {
		return StatusEvent{}, err
	}
	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return StatusEvent{}, err
	}
	return e, nil
}

func (s *Store) queryStatusEvents(query string, args ...any) ([]StatusEvent, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list status events: %w", err)
	}
	defer rows.Close()
	var out []StatusEvent
	for rows.Next() {
		e, err := scanStatusEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan status event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddStatusEvent records e and returns its id.
func (s *Store) AddStatusEvent(e StatusEvent) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO status_events (server_id, topology_id, ts, kind, from_status, to_status, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ServerID, e.TopologyID, formatTime(e.TS), e.Kind, e.FromStatus, e.ToStatus, e.Detail)
	if err != nil {
		return 0, fmt.Errorf("add status event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add status event: %w", err)
	}
	return id, nil
}

// ListStatusEvents returns the server's events at or after since, newest
// first, at most limit.
func (s *Store) ListStatusEvents(serverID int64, since time.Time, limit int) ([]StatusEvent, error) {
	return s.queryStatusEvents(`SELECT `+statusEventColumns+` FROM status_events
		WHERE server_id = ? AND ts >= ? ORDER BY ts DESC, id DESC LIMIT ?`,
		serverID, formatTime(since), limit)
}

// ListStatusEventsAll returns every server's events at or after since,
// oldest first (for the weekly summary).
func (s *Store) ListStatusEventsAll(since time.Time) ([]StatusEvent, error) {
	return s.queryStatusEvents(`SELECT `+statusEventColumns+` FROM status_events
		WHERE ts >= ? ORDER BY ts, id`, formatTime(since))
}

// PruneStatusEvents deletes events older than olderThan and returns how
// many it deleted.
func (s *Store) PruneStatusEvents(olderThan time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM status_events WHERE ts < ?`, formatTime(olderThan))
	if err != nil {
		return 0, fmt.Errorf("prune status events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune status events: %w", err)
	}
	return n, nil
}
