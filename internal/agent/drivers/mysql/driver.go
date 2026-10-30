package mysql

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
)

// ErrNoHost is returned by commands while the agent found no MySQL installation.
var ErrNoHost = errors.New("MySQL tidak ditemukan di server ini")

// Driver implements the mysql.* commands on one host. Host is nil when the
// agent found no MySQL installation at startup.
type Driver struct {
	Host Host
	// Creds stores the admin account mysql.connect verifies; nil when the
	// agent has no place to keep one.
	Creds  CredentialStore
	Jobs   *jobs.Runner
	Logger *slog.Logger
}

// Register adds the driver's commands to reg. Each command group lives in
// its own file and registers itself through a register<Name> method.
func (d *Driver) Register(reg *commands.Registry) {
	d.registerStatus(reg)
	d.registerConnect(reg)
	d.registerPreflight(reg)
	d.registerConfigure(reg)
	d.registerUsers(reg)
	d.registerSnapshot(reg)
	d.registerReplication(reg)
}

func (d *Driver) host() (Host, error) {
	if d.Host == nil {
		return nil, ErrNoHost
	}
	return d.Host, nil
}

// openAdmin is Host.Open with the failures the user can fix (a refused
// stored account, no account at all) turned into ParamErrors.
func openAdmin(ctx context.Context, h Host) (*sql.DB, error) {
	db, err := h.Open(ctx)
	var le *LoginError
	switch {
	case errors.As(err, &le):
		return nil, &commands.ParamError{Msg: loginFailure(le.Err)}
	case errors.Is(err, ErrNotConnected):
		return nil, &commands.ParamError{Msg: err.Error()}
	}
	return db, err
}
