package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Server roles.
const (
	RoleUnassigned = "unassigned"
	RoleMaster     = "master"
	RoleReplica    = "replica"
)

// Server is a managed host running a dbpanel agent.
type Server struct {
	ID           int64
	Name         string
	Address      string
	Role         string
	Paired       bool
	CertSerial   string
	ProbeJSON    string // JSON proto.ProbeResult, "" if none yet
	AgentVersion string
	LastSeenAt   *time.Time
	CreatedAt    time.Time
}

const serverColumns = `id, name, address, role, paired, cert_serial, probe_json, agent_version, last_seen_at, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanServer(r rowScanner) (Server, error) {
	var (
		srv      Server
		paired   int
		lastSeen sql.NullString
		created  string
	)
	if err := r.Scan(&srv.ID, &srv.Name, &srv.Address, &srv.Role, &paired, &srv.CertSerial,
		&srv.ProbeJSON, &srv.AgentVersion, &lastSeen, &created); err != nil {
		return Server{}, err
	}
	srv.Paired = paired != 0
	var err error
	if srv.LastSeenAt, err = parseNullTime(lastSeen); err != nil {
		return Server{}, err
	}
	if srv.CreatedAt, err = parseTime(created); err != nil {
		return Server{}, err
	}
	return srv, nil
}

// CreateServer adds an unpaired server with a unique, non-empty name.
func (s *Store) CreateServer(name string) (Server, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Server{}, errors.New("nama server tidak boleh kosong")
	}
	res, err := s.db.Exec(`INSERT INTO servers (name, role, created_at) VALUES (?, ?, ?)`,
		name, RoleUnassigned, formatTime(time.Now()))
	if err != nil {
		if isUniqueViolation(err, "servers.name") {
			return Server{}, errors.New("nama server sudah dipakai")
		}
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	return s.GetServer(id)
}

// ListServers returns all servers ordered by name.
func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(`SELECT ` + serverColumns + ` FROM servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// GetServer returns the server with the given id, or ErrNotFound.
func (s *Store) GetServer(id int64) (Server, error) {
	srv, err := scanServer(s.db.QueryRow(`SELECT `+serverColumns+` FROM servers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, fmt.Errorf("get server: %w", err)
	}
	return srv, nil
}

// execOne runs a statement that must affect exactly one row, else ErrNotFound.
func (s *Store) execOne(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkPaired records a completed pairing and deletes the server's unused
// pairing tokens, so none of them can later pair over this certificate.
func (s *Store) MarkPaired(id int64, certSerial string, address string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE servers SET paired = 1, cert_serial = ?, address = ? WHERE id = ?`,
		certSerial, address, id)
	if err != nil {
		return fmt.Errorf("mark paired: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("mark paired: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM pair_tokens WHERE server_id = ? AND used_at IS NULL`, id); err != nil {
		return fmt.Errorf("delete unused tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// UpdateProbe stores the latest probe result and marks the server as seen.
func (s *Store) UpdateProbe(id int64, probe proto.ProbeResult, agentVersion string, seen time.Time) error {
	b, err := json.Marshal(probe)
	if err != nil {
		return fmt.Errorf("marshal probe: %w", err)
	}
	return s.execOne(`UPDATE servers SET probe_json = ?, agent_version = ?, last_seen_at = ? WHERE id = ?`,
		string(b), agentVersion, formatTime(seen), id)
}

// TouchSeen updates last_seen_at only.
func (s *Store) TouchSeen(id int64, seen time.Time) error {
	return s.execOne(`UPDATE servers SET last_seen_at = ? WHERE id = ?`, formatTime(seen), id)
}

// DeleteServer removes a server (and, by cascade, its pair tokens).
func (s *Store) DeleteServer(id int64) error {
	return s.execOne(`DELETE FROM servers WHERE id = ?`, id)
}
