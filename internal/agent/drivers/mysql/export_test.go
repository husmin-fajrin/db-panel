package mysql

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// Internals the external tests (package mysql_test) exercise directly.

// PreflightFacts is what mysql.preflight reads from the server and the host.
type PreflightFacts = preflightFacts

// EvaluatePreflight turns facts into checks without a server.
var EvaluatePreflight = evaluatePreflight

// Config rendering and the restart decision, testable without a server.
var (
	RenderConfig   = renderConfig
	RestartReasons = restartReasons
	ParseOptions   = parseOptions
	NotInEffect    = notInEffect
)

// Snapshot helpers, testable without a server.
var (
	DumpArgs       = dumpArgs
	ProbeArgs      = probeArgs
	LoadArgs       = loadArgs
	DumpGTIDMode   = dumpGTIDMode
	ClientDefaults = clientDefaults
	FindClientTool = findClientTool
)

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
