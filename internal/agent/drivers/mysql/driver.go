package mysql

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// ErrNoHost is returned by commands while the agent found no MySQL installation.
var ErrNoHost = errors.New("MySQL tidak ditemukan di server ini")

// Driver implements the mysql.* commands on one host. Host is nil when the
// agent found no MySQL installation at startup; mysql.install sets it once
// it has installed MySQL. Set the fields before Register.
type Driver struct {
	Host Host
	// Creds stores the admin account mysql.connect verifies; nil when the
	// agent has no place to keep one.
	Creds  CredentialStore
	Jobs   *jobs.Runner
	Logger *slog.Logger
	// Sys and Cfg are what mysql.install needs to find the MySQL it
	// installed (NewRealHost); without Sys it cannot install. Snapshot
	// reads the host's disk, memory and load through Sys.
	Sys probe.System
	Cfg config.Config
	// PinDir holds the pinned role and source (pins.go): the agent
	// directory, /etc/dbpanel. Empty means no pins (tests only).
	PinDir string
	// SeenPath is the replicas-seen file (RecordReplicas); empty means
	// SeenFile. Tests point it elsewhere.
	SeenPath string

	mu sync.RWMutex // guards Host once Register has run

	// Snapshot's connection, kept between readings with the Host and
	// account it was opened for.
	monMu    sync.Mutex
	monDB    *sql.DB
	monHost  Host
	monCreds proto.ConnectParams

	pins *Pins // nil without PinDir; set by Register

	// KillDumpLocks: when each dump session was first seen holding the
	// global read lock.
	lockMu   sync.Mutex
	lockSeen map[uint64]time.Time

	tlsMu sync.Mutex // one mysql.tls* command at a time

	// Test hooks; nil means the real installer and NewRealHost.
	inst    installer
	newHost hostFactory
}

// Register adds the driver's commands to reg. Each command group lives in
// its own file and registers itself through a register<Name> method.
func (d *Driver) Register(reg *commands.Registry) {
	if d.PinDir != "" && d.pins == nil {
		p, err := LoadPins(d.PinDir)
		if err != nil && d.Logger != nil {
			d.Logger.Error("pin files unusable; guarded mysql commands are refused until they are fixed", "err", err)
		}
		p.logger = d.Logger
		d.pins = p
	}
	d.registerStatus(reg)
	d.registerConnect(reg)
	d.registerPreflight(reg)
	d.registerConfigure(reg)
	d.registerUsers(reg)
	d.registerSnapshot(reg)
	d.registerReplication(reg)
	d.registerInstall(reg)
	d.registerDatabases(reg)
	d.registerTLS(reg)
}

// Pins is the driver's pinned role and source; nil without PinDir. The
// health monitor bootstraps the role through it.
func (d *Driver) Pins() *Pins { return d.pins }

// allow checks cmd against the pins (Pins.Allow); everything is allowed
// without them.
func (d *Driver) allow(cmd string, params any) error {
	if d.pins == nil {
		return nil
	}
	return d.pins.Allow(cmd, params)
}

func (d *Driver) host() (Host, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
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
