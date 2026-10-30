package mysql

import (
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
	Host   Host
	Jobs   *jobs.Runner
	Logger *slog.Logger
}

// Register adds the driver's commands to reg. Each command group lives in
// its own file and registers itself through a register<Name> method.
func (d *Driver) Register(reg *commands.Registry) {
	d.registerStatus(reg)
}

func (d *Driver) host() (Host, error) {
	if d.Host == nil {
		return nil, ErrNoHost
	}
	return d.Host, nil
}
