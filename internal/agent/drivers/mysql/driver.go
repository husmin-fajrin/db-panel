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
	// SeenPortsPath is the replica ports file (RecordReplicas); empty means
	// replica-ports-seen beside SeenPath, or SeenPortsFile when that is
	// empty too.
	SeenPortsPath string
	// Events, when set, sends the promote lock guard's events to the
	// panel (the agent's connection).
	Events Emitter
	// Now is the driver's clock (the promote lock's time); nil means
	// time.Now. Tests replace it.
	Now func() time.Time
	// SalvageDir holds the salvage files (salvage.go); empty means
	// DefaultSalvageDir. Tests point it elsewhere.
	SalvageDir string

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

	// replUserMu: one mysql.createReplUser at a time, from reading the
	// existing rows to putting read-only back, so two concurrent calls
	// never both see no account and both CREATE, ALTER and GRANT it.
	replUserMu sync.Mutex

	// RecordReplicas: the hosts and ports it last wrote and when.
	seenMu      sync.Mutex
	seenHosts   []string
	seenAt      time.Time
	seenPorts   []int
	seenPortsAt time.Time

	// warnLimited: when each limited warning was last logged.
	warnMu sync.Mutex
	warnAt map[string]time.Time

	// The promote lock (promote_lock.go): lockForPromote, unlockPromote,
	// quarantine, repoint and the guard run one at a time.
	promoteMu sync.Mutex
	guard     promoteGuard
	// quarantinePending: a quarantine was written while MySQL was down;
	// the guard reports it once MySQL answers (guardQuarantined).
	quarantinePending bool
	// qUptime is MySQL's Uptime as guardQuarantined last read it, at
	// qUptimeAt.
	qUptime   int64
	qUptimeAt time.Time
	// rebuild is the mark of a rebuild load in progress on a quarantined
	// server (rebuild_load.go); it has its own lock, never promoteMu.
	rebuild rebuildLoad

	// qUp is whether MySQL answered guardQuarantined's last tick;
	// qStartPending: a start of MySQL it has not reported yet.
	qUp           bool
	qStartPending bool
	// pendingStart: start reports the panel has not got yet, oldest
	// first (emitStart); startSendWarned: their failed send was logged.
	// The caller holds promoteMu.
	pendingStart    []proto.Event
	startSendWarned bool
	// qNotice: the quarantine took effect in this guard round
	// (guardQuarantined), sent once the round's work is done
	// (sendRoundEvents). The caller holds promoteMu.
	qNotice *proto.QuarantinedEvent

	// startLockState is the start lock (startlock.go) as last checked,
	// reported while startLockSet; startLockMu guards both.
	startLockMu    sync.Mutex
	startLockState proto.StartLockState
	startLockSet   bool
	// The start lock's guard (startlock_guard.go); the caller holds
	// promoteMu. startLockProved: this agent run PERSISTed read_only and
	// super_read_only ON on a live MySQL since the quarantine; the guard
	// trusts it only while both are still on (readOnlyProvedNow), and a
	// promotion PERSISTs them again whatever it says (releaseForPromote).
	// startLockNextTry: a write of the lock (or its release) failed, the
	// next is not tried before then. startLockCheckedAt: when
	// startLockStatus last ran. bootFailAt: the time of the last
	// "bootstrap file" error log line reported; bootLog and bootLogAt:
	// the error log as --print-defaults named it, and when it was read.
	startLockProved    bool
	startLockNextTry   time.Time
	startLockCheckedAt time.Time
	bootFailAt         time.Time
	bootLog            string
	bootLogAt          time.Time
	// bootLogWarnAt: when an unreadable error log was last logged.
	// bootSince: the guard wrote a missing or changed lock file again
	// while MySQL was down; the log is read from then (the managed
	// config's mtime) until MySQL answers.
	bootLogWarnAt time.Time
	bootSince     time.Time

	salvageMu sync.Mutex // a salvage rename, a delete or the sweep; never held across mysqlbinlog
	// salvageRun is held by the one mysql.salvage that may run; another
	// is refused (TryLock).
	salvageRun sync.Mutex
	// The set the last mysql.leftover found (salvage.go: recordLeftover).
	leftoverMu    sync.Mutex
	leftoverSet   string
	leftoverKnown bool

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
	d.registerPromote(reg)
	d.registerPromoteJob(reg)
	d.registerQuarantine(reg)
	d.registerRepoint(reg)
	d.registerBinlog(reg)
	d.registerConnections(reg)
	d.registerLeftover(reg)
	d.registerSalvage(reg)
	d.registerRecordReplicas(reg)
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
