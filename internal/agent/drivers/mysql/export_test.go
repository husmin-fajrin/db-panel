package mysql

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Internals the external tests (package mysql_test) exercise directly.

// PreflightFacts is what mysql.preflight reads from the server and the host.
type PreflightFacts = preflightFacts

// EvaluatePreflight turns facts into checks without a server.
var EvaluatePreflight = evaluatePreflight

// BinlogBlock is what the managed block says about the binary log.
type BinlogBlock = binlogBlock

// Config rendering and the restart decision, testable without a server.
var (
	RenderConfig   = renderConfig
	RestartReasons = restartReasons
	ParseOptions   = parseOptions
	NotInEffect    = notInEffect
	TLSAvailable   = tlsAvailable
)

// FlushTablesMinVersion is the first MySQL release with FLUSH_TABLES.
const FlushTablesMinVersion = flushTablesMinVersion

// Snapshot helpers, testable without a server.
var (
	DumpGrantsFor  = dumpGrantsFor
	LoadArgs       = loadArgs
	DumpGTIDMode   = dumpGTIDMode
	ClientDefaults = clientDefaults
	FindClientTool = findClientTool
)

// DumpArgs and ProbeArgs are the dump's arguments without TLS options;
// SourceDumpArgs and SourceProbeArgs add p's.
func DumpArgs(defaultsFile string, databases []string, gtidPurged string) []string {
	return dumpArgs(defaultsFile, databases, gtidPurged, clientTLS{})
}

func ProbeArgs(defaultsFile string, databases []string) []string {
	return probeArgs(defaultsFile, databases, clientTLS{})
}

func SourceDumpArgs(defaultsFile string, p proto.SnapshotParams, gtidPurged string) []string {
	return dumpArgs(defaultsFile, p.Databases, gtidPurged, sourceTLS(p))
}

func SourceProbeArgs(defaultsFile string, p proto.SnapshotParams) []string {
	return probeArgs(defaultsFile, p.Databases, sourceTLS(p))
}

// SourceTLSOptions are the TLS lines of the master's option file for p.
func SourceTLSOptions(p proto.SnapshotParams) string { return sourceTLS(p).options() }

// CaptureDumpGTID writes chunks through the dump's counting writer and
// parses the GTID header from what it kept.
func CaptureDumpGTID(chunks []string) (string, bool) {
	c := &countingWriter{w: io.Discard}
	for _, ch := range chunks {
		c.Write([]byte(ch))
	}
	return parseDumpGTID(c.head)
}

// Replication statement builders and validators, testable without a server.
var (
	WildDoTablePatterns = wildDoTablePatterns
	FilterStatement     = filterStatement
	SourceStatement     = sourceStatement
	QuoteLiteral        = quotePassword
	ValidGTIDSet        = validGTIDSet
	DBNameRe            = dbNameRe
)

// Installer is what mysql.install runs commands, reads files and downloads
// through before MySQL is installed.
type Installer = installer

// SetInstallHooks makes mysql.install use inst, and newHost to find the
// MySQL it installed.
func SetInstallHooks(d *Driver, inst Installer, newHost func(config.Config, CredentialStore, probe.System, *slog.Logger) (Host, error)) {
	d.inst, d.newHost = inst, newHost
}

// NewRealInstaller is the production installer; rt replaces the HTTP
// transport when not nil.
func NewRealInstaller(sys probe.System, rt http.RoundTripper) Installer {
	return realInstaller{sys: sys, transport: rt}
}

// SetInstallCancelWait changes how long a cancelled installer command gets
// after SIGTERM; it returns the function that restores it.
func SetInstallCancelWait(d time.Duration) (restore func()) {
	old := installCancelWait
	installCancelWait = d
	return func() { installCancelWait = old }
}

// Install helpers, testable without a server.
var (
	TemporaryPassword = temporaryPassword
	RandomPassword    = randomPassword
)

// MissingReplicaGrants is what the replica preflight finds missing in
// grants (SHOW GRANTS rows) of account.
func MissingReplicaGrants(grants []string, account string) []string {
	return missingGrants(grants, account, replicaGrants)
}

// Health helpers, testable without a server.
var (
	SystemStats     = systemStats
	BinlogOldestAge = binlogOldestAge
)

// ClaimRole and ClaimSource are the claims the handlers make after a
// successful configure, configureReplication, dumpFrom or cloneFrom.
func (p *Pins) ClaimRole(role string) error             { return p.claimRole(role) }
func (p *Pins) ClaimSource(host string, port int) error { return p.claimSource(host, port) }

// SetCommandHook calls seen with every child process command the agent
// builds; it returns the function that restores the default.
func SetCommandHook(seen func(*exec.Cmd)) (restore func()) {
	old := newCommand
	newCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := old(ctx, name, args...)
		seen(cmd)
		return cmd
	}
	return func() { newCommand = old }
}

// TLS directory operations, testable without a server.
var (
	TrustTLS   = trustTLS
	RequestTLS = requestTLS
)
