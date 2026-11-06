package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Promotion is one promotion of a replica to master. ReplicasJSON and
// LeftoverJSON are opaque to the store; the orchestrator defines them.
type Promotion struct {
	ID, TopologyID, OldMasterServerID, NewMasterServerID int64
	Mode                                                 string // planned | emergency
	Status                                               string // running | partial | done | failed
	JobID                                                string
	LastMasterGTID, NewMasterGTID                        string
	ReplicasJSON, LeftoverJSON                           string
	StartedAt                                            time.Time
	FinishedAt, CutoverConfirmedAt, LockAt               *time.Time
}

const promotionColumns = `id, topology_id, mode, old_master_server_id, new_master_server_id, started_at, finished_at,
	cutover_confirmed_at, lock_at, last_master_gtid, new_master_gtid, replicas_json, leftover_json, status, job_id`

func scanPromotion(r rowScanner) (Promotion, error) {
	var (
		p                         Promotion
		started                   string
		finished, confirmed, lock sql.NullString
	)
	if err := r.Scan(&p.ID, &p.TopologyID, &p.Mode, &p.OldMasterServerID, &p.NewMasterServerID, &started, &finished,
		&confirmed, &lock, &p.LastMasterGTID, &p.NewMasterGTID, &p.ReplicasJSON, &p.LeftoverJSON, &p.Status, &p.JobID); err != nil {
		return Promotion{}, err
	}
	var err error
	if p.StartedAt, err = parseTime(started); err != nil {
		return Promotion{}, err
	}
	for _, f := range []struct {
		in  sql.NullString
		out **time.Time
	}{{finished, &p.FinishedAt}, {confirmed, &p.CutoverConfirmedAt}, {lock, &p.LockAt}} {
		if !f.in.Valid {
			continue
		}
		t, err := parseTime(f.in.String)
		if err != nil {
			return Promotion{}, err
		}
		*f.out = &t
	}
	return p, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// AddPromotion records a new promotion and returns its id. An empty Status
// is stored as "running" and an empty ReplicasJSON as "[]".
func (s *Store) AddPromotion(p Promotion) (int64, error) {
	if p.Status == "" {
		p.Status = "running"
	}
	if p.ReplicasJSON == "" {
		p.ReplicasJSON = "[]"
	}
	res, err := s.db.Exec(`INSERT INTO promotions (topology_id, mode, old_master_server_id, new_master_server_id, started_at,
		finished_at, cutover_confirmed_at, lock_at, last_master_gtid, new_master_gtid, replicas_json, leftover_json, status, job_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.TopologyID, p.Mode, p.OldMasterServerID, p.NewMasterServerID, formatTime(p.StartedAt),
		nullableTime(p.FinishedAt), nullableTime(p.CutoverConfirmedAt), nullableTime(p.LockAt),
		p.LastMasterGTID, p.NewMasterGTID, p.ReplicasJSON, p.LeftoverJSON, p.Status, p.JobID)
	if err != nil {
		return 0, fmt.Errorf("insert promotion: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert promotion: %w", err)
	}
	return id, nil
}

// UpdatePromotion overwrites every field of the promotion with p.ID except
// the id; ErrNotFound when it does not exist.
func (s *Store) UpdatePromotion(p Promotion) error {
	return s.execOne(`UPDATE promotions SET topology_id = ?, mode = ?, old_master_server_id = ?, new_master_server_id = ?,
		started_at = ?, finished_at = ?, cutover_confirmed_at = ?, lock_at = ?, last_master_gtid = ?, new_master_gtid = ?,
		replicas_json = ?, leftover_json = ?, status = ?, job_id = ? WHERE id = ?`,
		p.TopologyID, p.Mode, p.OldMasterServerID, p.NewMasterServerID, formatTime(p.StartedAt),
		nullableTime(p.FinishedAt), nullableTime(p.CutoverConfirmedAt), nullableTime(p.LockAt),
		p.LastMasterGTID, p.NewMasterGTID, p.ReplicasJSON, p.LeftoverJSON, p.Status, p.JobID, p.ID)
}

func (s *Store) onePromotion(where string, arg int64) (Promotion, error) {
	p, err := scanPromotion(s.db.QueryRow(`SELECT `+promotionColumns+` FROM promotions WHERE `+where+` ORDER BY id DESC LIMIT 1`, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Promotion{}, ErrNotFound
	}
	if err != nil {
		return Promotion{}, fmt.Errorf("get promotion: %w", err)
	}
	return p, nil
}

// GetPromotion returns the promotion with the given id, or ErrNotFound.
func (s *Store) GetPromotion(id int64) (Promotion, error) { return s.onePromotion(`id = ?`, id) }

// LatestPromotion returns the newest promotion of the topology, or
// ErrNotFound.
func (s *Store) LatestPromotion(topologyID int64) (Promotion, error) {
	return s.onePromotion(`topology_id = ?`, topologyID)
}

// PromotionByOldMaster returns the newest promotion that replaced serverID
// as master, or ErrNotFound.
func (s *Store) PromotionByOldMaster(serverID int64) (Promotion, error) {
	return s.onePromotion(`old_master_server_id = ?`, serverID)
}
