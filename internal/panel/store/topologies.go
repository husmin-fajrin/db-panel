package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Topology is a master and the replicas that follow it.
type Topology struct {
	ID              int64
	MasterServerID  int64
	ReplUser        string
	ReplPasswordEnc []byte // sealed with the panel's secrets box
	SourceHost      string
	SourcePort      int
	CreatedAt       time.Time
	// CutoverPending: a promotion swapped the master and clients have not
	// yet been confirmed on the new one. OldMasterServerID is the master
	// it replaced (0 when never promoted).
	CutoverPending    bool
	OldMasterServerID int64
}

const topologyColumns = `id, master_server_id, repl_user, repl_password_enc, source_host, source_port, created_at, cutover_pending, old_master_server_id`

func scanTopology(r rowScanner) (Topology, error) {
	var (
		t       Topology
		created string
		pending int
	)
	if err := r.Scan(&t.ID, &t.MasterServerID, &t.ReplUser, &t.ReplPasswordEnc, &t.SourceHost, &t.SourcePort, &created, &pending, &t.OldMasterServerID); err != nil {
		return Topology{}, err
	}
	t.CutoverPending = pending != 0
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return Topology{}, err
	}
	return t, nil
}

// Replica states.
const (
	ReplicaCreating = "creating"
	ReplicaReady    = "ready"
	ReplicaChanging = "changing" // a job adds or removes databases
	// ReplicaAttention: a ready replica whose last change of databases
	// failed or was canceled (LastError says where). It still replicates,
	// possibly stopped; unlike ReplicaFailed it is never re-created.
	ReplicaAttention = "attention"
	ReplicaFailed    = "failed" // its creation failed
	// ReplicaResyncing: a job is re-seeding the replica from the master.
	ReplicaResyncing = "resyncing"
	// After a promotion: the replica still follows the old master and must
	// be pointed at the new one, or cannot (diverged) and must be rebuilt.
	ReplicaNeedsRepoint = "needs_repoint"
	ReplicaNeedsRebuild = "needs_rebuild"
)

// Replica is a server that follows a topology's master.
type Replica struct {
	ID, TopologyID, ServerID int64
	Databases                []string // empty = all databases
	State, LastError         string
	Paused                   bool // replication deliberately stopped by an operator
	TLSVerified              bool // the link to the master was verified over TLS
	CreatedAt                time.Time
}

const replicaColumns = `id, topology_id, server_id, databases_json, state, last_error, paused, tls_verified, created_at`

func scanReplica(r rowScanner) (Replica, error) {
	var (
		rep     Replica
		dbs     string
		paused  int
		tlsOK   int
		created string
	)
	if err := r.Scan(&rep.ID, &rep.TopologyID, &rep.ServerID, &dbs, &rep.State, &rep.LastError, &paused, &tlsOK, &created); err != nil {
		return Replica{}, err
	}
	rep.Paused = paused != 0
	rep.TLSVerified = tlsOK != 0
	if err := json.Unmarshal([]byte(dbs), &rep.Databases); err != nil {
		return Replica{}, fmt.Errorf("parse databases: %w", err)
	}
	if len(rep.Databases) == 0 {
		rep.Databases = nil
	}
	var err error
	if rep.CreatedAt, err = parseTime(created); err != nil {
		return Replica{}, err
	}
	return rep, nil
}

func marshalDatabases(databases []string) (string, error) {
	if len(databases) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(databases)
	if err != nil {
		return "", fmt.Errorf("marshal databases: %w", err)
	}
	return string(b), nil
}

func validRole(role string) bool {
	return role == RoleUnassigned || role == RoleMaster || role == RoleReplica || role == RoleQuarantined
}

// SetServerRole sets a server's role.
func (s *Store) SetServerRole(id int64, role string) error {
	if !validRole(role) {
		return fmt.Errorf("peran tidak dikenal: %q", role)
	}
	return s.execOne(`UPDATE servers SET role = ? WHERE id = ?`, role, id)
}

// serverRoleTx reads a server's role inside tx, or ErrNotFound.
func serverRoleTx(tx *sql.Tx, id int64) (string, error) {
	var role string
	err := tx.QueryRow(`SELECT role FROM servers WHERE id = ?`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read role: %w", err)
	}
	return role, nil
}

// CreateTopology makes the server a master with a new topology. It fails
// when the server is a replica or already a master.
func (s *Store) CreateTopology(masterServerID int64, replUser string, replPasswordEnc []byte, sourceHost string, sourcePort int) (Topology, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Topology{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	role, err := serverRoleTx(tx, masterServerID)
	if err != nil {
		return Topology{}, err
	}
	if role == RoleReplica {
		return Topology{}, errors.New("server sudah menjadi replika")
	}
	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM topologies WHERE master_server_id = ?`, masterServerID).Scan(&existing); err != nil {
		return Topology{}, fmt.Errorf("check topology: %w", err)
	}
	if role == RoleMaster || existing > 0 {
		return Topology{}, errors.New("server sudah menjadi master")
	}
	if replPasswordEnc == nil {
		replPasswordEnc = []byte{}
	}
	res, err := tx.Exec(`INSERT INTO topologies (master_server_id, repl_user, repl_password_enc, source_host, source_port, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		masterServerID, replUser, replPasswordEnc, sourceHost, sourcePort, formatTime(time.Now()))
	if err != nil {
		return Topology{}, fmt.Errorf("insert topology: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Topology{}, fmt.Errorf("insert topology: %w", err)
	}
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id = ?`, RoleMaster, masterServerID); err != nil {
		return Topology{}, fmt.Errorf("set role: %w", err)
	}
	t, err := scanTopology(tx.QueryRow(`SELECT `+topologyColumns+` FROM topologies WHERE id = ?`, id))
	if err != nil {
		return Topology{}, fmt.Errorf("read topology: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Topology{}, fmt.Errorf("commit: %w", err)
	}
	return t, nil
}

// GetTopology returns the topology with the given id, or ErrNotFound.
func (s *Store) GetTopology(id int64) (Topology, error) {
	t, err := scanTopology(s.db.QueryRow(`SELECT `+topologyColumns+` FROM topologies WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Topology{}, ErrNotFound
	}
	if err != nil {
		return Topology{}, fmt.Errorf("get topology: %w", err)
	}
	return t, nil
}

// GetTopologyByMaster returns the topology whose master is serverID, or
// ErrNotFound.
func (s *Store) GetTopologyByMaster(serverID int64) (Topology, error) {
	t, err := scanTopology(s.db.QueryRow(`SELECT `+topologyColumns+` FROM topologies WHERE master_server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Topology{}, ErrNotFound
	}
	if err != nil {
		return Topology{}, fmt.Errorf("get topology: %w", err)
	}
	return t, nil
}

// SetTopologySource sets the address and MySQL port replicas use to reach
// the topology's master.
func (s *Store) SetTopologySource(id int64, host string, port int) error {
	return s.execOne(`UPDATE topologies SET source_host = ?, source_port = ? WHERE id = ?`, host, port, id)
}

// ListTopologies returns all topologies ordered by id.
func (s *Store) ListTopologies() ([]Topology, error) {
	rows, err := s.db.Query(`SELECT ` + topologyColumns + ` FROM topologies ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list topologies: %w", err)
	}
	defer rows.Close()
	var out []Topology
	for rows.Next() {
		t, err := scanTopology(rows)
		if err != nil {
			return nil, fmt.Errorf("scan topology: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddReplica makes the server a replica of the topology, in state
// ReplicaCreating. It fails when the server is already a master or replica.
func (s *Store) AddReplica(topologyID, serverID int64, databases []string) (Replica, error) {
	dbs, err := marshalDatabases(databases)
	if err != nil {
		return Replica{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Replica{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM topologies WHERE id = ?`, topologyID).Scan(&n); err != nil {
		return Replica{}, fmt.Errorf("check topology: %w", err)
	}
	if n == 0 {
		return Replica{}, ErrNotFound
	}
	role, err := serverRoleTx(tx, serverID)
	if err != nil {
		return Replica{}, err
	}
	switch role {
	case RoleMaster:
		return Replica{}, errors.New("server sudah berperan sebagai master")
	case RoleReplica:
		return Replica{}, errors.New("server sudah berperan sebagai replika")
	}
	res, err := tx.Exec(`INSERT INTO replicas (topology_id, server_id, databases_json, state, last_error, created_at)
		VALUES (?, ?, ?, ?, '', ?)`, topologyID, serverID, dbs, ReplicaCreating, formatTime(time.Now()))
	if err != nil {
		return Replica{}, fmt.Errorf("insert replica: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Replica{}, fmt.Errorf("insert replica: %w", err)
	}
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id = ?`, RoleReplica, serverID); err != nil {
		return Replica{}, fmt.Errorf("set role: %w", err)
	}
	rep, err := scanReplica(tx.QueryRow(`SELECT `+replicaColumns+` FROM replicas WHERE id = ?`, id))
	if err != nil {
		return Replica{}, fmt.Errorf("read replica: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Replica{}, fmt.Errorf("commit: %w", err)
	}
	return rep, nil
}

// SetReplicaDatabases sets the databases a replica follows; empty means all.
func (s *Store) SetReplicaDatabases(serverID int64, databases []string) error {
	dbs, err := marshalDatabases(databases)
	if err != nil {
		return err
	}
	return s.execOne(`UPDATE replicas SET databases_json = ? WHERE server_id = ?`, dbs, serverID)
}

// SetReplicaState sets a replica's state and last error.
func (s *Store) SetReplicaState(serverID int64, state, lastError string) error {
	return s.execOne(`UPDATE replicas SET state = ?, last_error = ? WHERE server_id = ?`, state, lastError, serverID)
}

// SetReplicaPaused records whether an operator paused the replica's
// replication.
func (s *Store) SetReplicaPaused(serverID int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	return s.execOne(`UPDATE replicas SET paused = ? WHERE server_id = ?`, v, serverID)
}

// ListReplicas returns a topology's replicas ordered by id.
func (s *Store) ListReplicas(topologyID int64) ([]Replica, error) {
	rows, err := s.db.Query(`SELECT `+replicaColumns+` FROM replicas WHERE topology_id = ? ORDER BY id`, topologyID)
	if err != nil {
		return nil, fmt.Errorf("list replicas: %w", err)
	}
	defer rows.Close()
	var out []Replica
	for rows.Next() {
		rep, err := scanReplica(rows)
		if err != nil {
			return nil, fmt.Errorf("scan replica: %w", err)
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

// GetReplicaByServer returns the replica row of serverID, or ErrNotFound.
func (s *Store) GetReplicaByServer(serverID int64) (Replica, error) {
	rep, err := scanReplica(s.db.QueryRow(`SELECT `+replicaColumns+` FROM replicas WHERE server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Replica{}, ErrNotFound
	}
	if err != nil {
		return Replica{}, fmt.Errorf("get replica: %w", err)
	}
	return rep, nil
}

// RemoveReplica deletes the server's replica row and sets its role back to
// unassigned.
func (s *Store) RemoveReplica(serverID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM replicas WHERE server_id = ?`, serverID)
	if err != nil {
		return fmt.Errorf("delete replica: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete replica: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id = ?`, RoleUnassigned, serverID); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetTopologyByOldMaster returns the topology whose previous master is
// serverID, or ErrNotFound.
func (s *Store) GetTopologyByOldMaster(serverID int64) (Topology, error) {
	t, err := scanTopology(s.db.QueryRow(`SELECT `+topologyColumns+` FROM topologies WHERE old_master_server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Topology{}, ErrNotFound
	}
	if err != nil {
		return Topology{}, fmt.Errorf("get topology: %w", err)
	}
	return t, nil
}

// SetCutoverPending records whether clients still have to be cut over to
// the topology's master after a promotion.
func (s *Store) SetCutoverPending(topologyID int64, pending bool) error {
	v := 0
	if pending {
		v = 1
	}
	return s.execOne(`UPDATE topologies SET cutover_pending = ? WHERE id = ?`, v, topologyID)
}

// SwapMaster makes newServerID the topology's master and quarantines
// oldServerID, in one transaction: the topology points at the new master's
// address with cutover pending, the new master's replica row is deleted and
// both roles change. Nothing changes when a server or the topology is
// missing (ErrNotFound).
func (s *Store) SwapMaster(topologyID, oldServerID, newServerID int64, newHost string, newPort int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	for _, id := range []int64{oldServerID, newServerID} {
		if _, err := serverRoleTx(tx, id); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE topologies SET master_server_id = ?, source_host = ?, source_port = ?,
		old_master_server_id = ?, cutover_pending = 1 WHERE id = ?`,
		newServerID, newHost, newPort, oldServerID, topologyID)
	if err != nil {
		return fmt.Errorf("update topology: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("update topology: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM replicas WHERE server_id = ?`, newServerID); err != nil {
		return fmt.Errorf("delete replica: %w", err)
	}
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id = ?`, RoleMaster, newServerID); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	if _, err := tx.Exec(`UPDATE servers SET role = ? WHERE id = ?`, RoleQuarantined, oldServerID); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
