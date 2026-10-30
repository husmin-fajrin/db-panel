package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Inventory is the latest MySQL inventory of a server, as last reported by
// its agent.
type Inventory struct {
	ServerID      int64
	Connected     bool
	Version       string
	Series        string
	Databases     []proto.DatabaseInfo
	MissingGrants []string
	MasterReady   bool
	PreflightJSON string // opaque JSON, "" if none
	UpdatedAt     time.Time
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetInventory stores inv as the server's inventory, replacing any earlier
// one.
func (s *Store) SetInventory(inv Inventory) error {
	dbs, err := json.Marshal(inv.Databases)
	if err != nil {
		return fmt.Errorf("marshal databases: %w", err)
	}
	if inv.Databases == nil {
		dbs = []byte("[]")
	}
	grants, err := json.Marshal(inv.MissingGrants)
	if err != nil {
		return fmt.Errorf("marshal grants: %w", err)
	}
	if inv.MissingGrants == nil {
		grants = []byte("[]")
	}
	_, err = s.db.Exec(`INSERT INTO mysql_inventory
		(server_id, connected, version, series, databases_json, missing_grants_json, master_ready, preflight_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET
			connected = excluded.connected,
			version = excluded.version,
			series = excluded.series,
			databases_json = excluded.databases_json,
			missing_grants_json = excluded.missing_grants_json,
			master_ready = excluded.master_ready,
			preflight_json = excluded.preflight_json,
			updated_at = excluded.updated_at`,
		inv.ServerID, boolInt(inv.Connected), inv.Version, inv.Series, string(dbs), string(grants),
		boolInt(inv.MasterReady), inv.PreflightJSON, formatTime(inv.UpdatedAt))
	if err != nil {
		return fmt.Errorf("set inventory: %w", err)
	}
	return nil
}

// GetInventory returns the server's inventory, or ErrNotFound if none was
// stored yet.
func (s *Store) GetInventory(serverID int64) (Inventory, error) {
	var (
		inv                 Inventory
		connected, ready    int
		dbsJSON, grantsJSON string
		updated             string
	)
	err := s.db.QueryRow(`SELECT server_id, connected, version, series, databases_json, missing_grants_json,
		master_ready, preflight_json, updated_at FROM mysql_inventory WHERE server_id = ?`, serverID).
		Scan(&inv.ServerID, &connected, &inv.Version, &inv.Series, &dbsJSON, &grantsJSON, &ready, &inv.PreflightJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Inventory{}, ErrNotFound
	}
	if err != nil {
		return Inventory{}, fmt.Errorf("get inventory: %w", err)
	}
	inv.Connected = connected != 0
	inv.MasterReady = ready != 0
	if err := json.Unmarshal([]byte(dbsJSON), &inv.Databases); err != nil {
		return Inventory{}, fmt.Errorf("parse databases: %w", err)
	}
	if err := json.Unmarshal([]byte(grantsJSON), &inv.MissingGrants); err != nil {
		return Inventory{}, fmt.Errorf("parse grants: %w", err)
	}
	if inv.UpdatedAt, err = parseTime(updated); err != nil {
		return Inventory{}, err
	}
	return inv, nil
}
