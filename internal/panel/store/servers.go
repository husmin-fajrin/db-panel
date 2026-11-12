package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Server roles.
const (
	RoleUnassigned = "unassigned"
	RoleMaster     = "master"
	RoleReplica    = "replica"
	// RoleQuarantined: the former master after a promotion; it is kept out
	// of service until an operator rebuilds it.
	RoleQuarantined = "quarantined"
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
	SnoozeUntil  *time.Time // alerts for this server are muted until then
	// SnoozeFrom is when the snooze began; set until the digest of the
	// snooze has been sent (EndedSnoozes, ClearSnoozeFrom), nil otherwise.
	SnoozeFrom *time.Time
	CreatedAt  time.Time
}

const serverColumns = `id, name, address, role, paired, cert_serial, probe_json, agent_version, last_seen_at, snooze_until, snooze_from, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanServer(r rowScanner) (Server, error) {
	var (
		srv      Server
		paired   int
		lastSeen sql.NullString
		snooze   sql.NullString
		from     sql.NullString
		created  string
	)
	if err := r.Scan(&srv.ID, &srv.Name, &srv.Address, &srv.Role, &paired, &srv.CertSerial,
		&srv.ProbeJSON, &srv.AgentVersion, &lastSeen, &snooze, &from, &created); err != nil {
		return Server{}, err
	}
	srv.Paired = paired != 0
	var err error
	if srv.LastSeenAt, err = parseNullTime(lastSeen); err != nil {
		return Server{}, err
	}
	if srv.SnoozeUntil, err = parseNullTime(snooze); err != nil {
		return Server{}, err
	}
	if srv.SnoozeFrom, err = parseNullTime(from); err != nil {
		return Server{}, err
	}
	if srv.CreatedAt, err = parseTime(created); err != nil {
		return Server{}, err
	}
	return srv, nil
}

// serverNameRe is what a server name may contain. Names end up in pages,
// logs and later in file names and messages; no control or bidi
// characters, no long strings.
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,64}$`)

// ErrServerName is returned for a server name outside serverNameRe.
var ErrServerName = errors.New("nama server hanya boleh huruf, angka, spasi, titik, garis bawah dan strip (maks. 64)")

// CreateServer adds an unpaired server with a unique name: trimmed, 1-64
// characters from [A-Za-z0-9 ._-].
func (s *Store) CreateServer(name string) (Server, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Server{}, errors.New("nama server tidak boleh kosong")
	}
	if !serverNameRe.MatchString(name) {
		return Server{}, ErrServerName
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

// MarkUnpaired makes the server wait for an agent to pair again, at
// address: no agent certificate matches it any more.
func (s *Store) MarkUnpaired(id int64, address string) error {
	if err := s.execOne(`UPDATE servers SET paired = 0, cert_serial = '', address = ? WHERE id = ?`, address, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("mark unpaired: %w", err)
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
	s.forget(id)
	return nil
}

// MaxProbeJSON caps the stored probe result. A real one is a few KiB; an
// agent sending more would bloat panel.db and every page that shows it.
const MaxProbeJSON = 64 << 10

// ErrProbeTooLarge is returned by UpdateProbe for a result over MaxProbeJSON.
var ErrProbeTooLarge = errors.New("hasil probe terlalu besar")

// UpdateProbe stores the latest probe result and marks the server as seen.
// A result whose JSON exceeds MaxProbeJSON is refused (ErrProbeTooLarge)
// and the previous one kept.
func (s *Store) UpdateProbe(id int64, probe proto.ProbeResult, agentVersion string, seen time.Time) error {
	b, err := json.Marshal(probe)
	if err != nil {
		return fmt.Errorf("marshal probe: %w", err)
	}
	if len(b)+len(agentVersion) > MaxProbeJSON {
		return fmt.Errorf("%w: %d byte", ErrProbeTooLarge, len(b)+len(agentVersion))
	}
	return s.execOne(`UPDATE servers SET probe_json = ?, agent_version = ?, last_seen_at = ? WHERE id = ?`,
		string(b), agentVersion, formatTime(seen), id)
}

// TouchSeen updates last_seen_at only.
func (s *Store) TouchSeen(id int64, seen time.Time) error {
	return s.execOne(`UPDATE servers SET last_seen_at = ? WHERE id = ?`, formatTime(seen), id)
}

// SetSnooze mutes alerts for the server until until; now is the caller's
// clock. A snooze that is still running (snooze_until after now) when it
// is extended or shortened keeps its snooze_from; so does a new snooze
// while the digest of the last one is still owed (snooze_from set, FR-8
// a): that digest then covers both. Otherwise it starts at now. A nil
// until, or one that is not after now, ends a running snooze at now:
// snooze_from stays, so the digest of the snooze is still sent
// (EndedSnoozes); with no snooze running nothing changes.
func (s *Store) SetSnooze(id int64, until *time.Time, now time.Time) error {
	if until != nil && until.After(now) {
		return s.execOne(`UPDATE servers SET
			snooze_from = CASE WHEN snooze_from IS NOT NULL THEN snooze_from
				WHEN snooze_until IS NOT NULL AND snooze_until > ? THEN snooze_from ELSE ? END,
			snooze_until = ? WHERE id = ?`, formatTime(now), formatTime(now), formatTime(*until), id)
	}
	res, err := s.db.Exec(`UPDATE servers SET snooze_until = ? WHERE id = ? AND snooze_until IS NOT NULL AND snooze_until > ?`,
		formatTime(now), id, formatTime(now))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM servers WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	return nil
}

// SnoozeSpan is a snooze that has ended and whose digest is still owed.
type SnoozeSpan struct {
	ServerID    int64
	From, Until time.Time
}

// EndedSnoozes returns the snoozes that have ended by now and whose digest
// has not been sent: snooze_from set and snooze_until not after now.
func (s *Store) EndedSnoozes(now time.Time) ([]SnoozeSpan, error) {
	rows, err := s.db.Query(`SELECT id, snooze_from, snooze_until FROM servers
		WHERE snooze_from IS NOT NULL AND snooze_until IS NOT NULL AND snooze_until <= ? ORDER BY id`, formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("ended snoozes: %w", err)
	}
	defer rows.Close()
	var out []SnoozeSpan
	for rows.Next() {
		var (
			sp          SnoozeSpan
			from, until string
		)
		if err := rows.Scan(&sp.ServerID, &from, &until); err != nil {
			return nil, err
		}
		if sp.From, err = parseTime(from); err != nil {
			return nil, err
		}
		if sp.Until, err = parseTime(until); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ClearSnoozeFrom records that the digest of sp, the server's snooze, was
// sent: only while the server's snooze is still exactly sp (FR-8 b); a
// snooze started or changed since keeps its start, and the digest it owes
// is sent when it ends. ErrNotFound for a server that does not exist.
func (s *Store) ClearSnoozeFrom(sp SnoozeSpan) error {
	res, err := s.db.Exec(`UPDATE servers SET snooze_from = NULL WHERE id = ? AND snooze_from = ? AND snooze_until = ?`,
		sp.ServerID, formatTime(sp.From), formatTime(sp.Until))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM servers WHERE id = ?`, sp.ServerID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	return nil
}

// DeleteServer removes a server (and, by cascade, its pair tokens,
// inventory, jobs and, for a master, its topology). Replica servers of a
// deleted master are set back to unassigned.
func (s *Store) DeleteServer(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id IN (
		SELECT r.server_id FROM replicas r JOIN topologies t ON t.id = r.topology_id
		WHERE t.master_server_id = ?)`, RoleUnassigned, id); err != nil {
		return fmt.Errorf("unassign replicas: %w", err)
	}
	// A deleted old master is no topology's previous master any more.
	if _, err := tx.Exec(`UPDATE topologies SET old_master_server_id = 0 WHERE old_master_server_id = ?`, id); err != nil {
		return fmt.Errorf("clear old master: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete server: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	s.forget(id)
	return nil
}
