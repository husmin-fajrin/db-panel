package mysql

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"os/user"
	"strings"
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

// SettleRebuilt is what mysql.configureReplication does to the role pin
// after it succeeded toward host:port.
func (p *Pins) SettleRebuilt(host string, port int) (bool, error) { return p.settleRebuilt(host, port) }

// SettleRebuilt is the driver's end of a quarantine after a successful
// mysql.configureReplication toward host:port.
func (d *Driver) SettleRebuilt(host string, port int) error { return d.settleRebuilt(host, port) }

// HostWithoutPort is a processlist HOST without its port.
var HostWithoutPort = hostWithoutPort

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
	TrustTLS        = trustTLS
	RequestTLS      = requestTLS
	MatchServedCert = matchServedCert
)

// SetReadPeer replaces how the driver reads another server's globals.
func SetReadPeer(f func(context.Context, proto.PeerCred, time.Duration) PeerState) (restore func()) {
	old := readPeer
	readPeer = f
	return func() { readPeer = old }
}

// ArmGuard gives the guard the candidate's account, as lockForPromote
// does, without a connected replica.
func (d *Driver) ArmGuard(c proto.PeerCred) { d.armGuard(c) }

// GTIDHasUUID reports whether a GTID set holds a transaction of uuid.
var GTIDHasUUID = gtidHasUUID

// SetWriteLock replaces how mysql.lockForPromote writes the lock file.
func SetWriteLock(f func(*Pins, PromoteLock) error) (restore func()) {
	old := writeLock
	writeLock = f
	return func() { writeLock = old }
}

// SetProbePeer replaces how the driver probes another server.
func SetProbePeer(f func(context.Context, string, int, time.Duration) error) (restore func()) {
	old := probePeer
	probePeer = f
	return func() { probePeer = old }
}

// SetProbeSpacing changes the pause between emergency probes.
func SetProbeSpacing(d time.Duration) (restore func()) {
	old := probeSpacing
	probeSpacing = d
	return func() { probeSpacing = old }
}

// GTID set helpers and the mysqlbinlog parser, testable without a server.
func NormalizeGTIDSet(s string) (string, error) {
	g, err := parseGTIDSet(s)
	if err != nil {
		return "", err
	}
	return g.String(), nil
}

var GTIDCount = gtidCount

// ParseBinlogText is the GTID set of mysqlbinlog's output.
func ParseBinlogText(text string) (string, error) {
	g, err := parseBinlogText(strings.NewReader(text))
	if err != nil {
		return "", err
	}
	return g.String(), nil
}

// BinlogIndexPath finds the binary log index of l without a server.
var BinlogIndexPath = binlogIndexPath

// SetPromoteCrash makes mysql.promote call f at its crash points.
func SetPromoteCrash(f func(stage string)) (restore func()) {
	old := promoteCrash
	promoteCrash = f
	return func() { promoteCrash = old }
}

// SetRelayWait changes step 3's bound and its stall time.
func SetRelayWait(sec int, stall time.Duration) (restore func()) {
	oldSec, oldStall := relayWaitSec, relayStall
	relayWaitSec, relayStall = sec, stall
	return func() { relayWaitSec, relayStall = oldSec, oldStall }
}

// SetRelayLogEvery changes how often step 3 checks the relay log.
func SetRelayLogEvery(d time.Duration) (restore func()) {
	old := relayLogEvery
	relayLogEvery = d
	return func() { relayLogEvery = old }
}

// SeenAddr is the address of a host the replicas-seen file holds.
var SeenAddr = seenAddr

// SetResolver replaces the seen check's forward and reverse lookups.
func SetResolver(ip func(context.Context, string) ([]net.IP, error), addr func(context.Context, string) ([]string, error)) (restore func()) {
	oldIP, oldAddr := lookupIP, lookupAddr
	lookupIP, lookupAddr = ip, addr
	return func() { lookupIP, lookupAddr = oldIP, oldAddr }
}

// SetRepointHook makes mysql.repoint call f at its test points.
func SetRepointHook(f func(stage string)) (restore func()) {
	old := repointHook
	repointHook = f
	return func() { repointHook = old }
}

// SetRepointIOWait changes how long mysql.repoint waits for the receiver.
func SetRepointIOWait(d time.Duration) (restore func()) {
	old := repointIOWait
	repointIOWait = d
	return func() { repointIOWait = old }
}

// ScanLeftover runs mysql.leftover's parser over mysqlbinlog output text
// for the transactions of set.
func ScanLeftover(set, text string) (proto.LeftoverResult, error) {
	var res proto.LeftoverResult
	g, err := parseGTIDSet(set)
	if err != nil {
		return res, err
	}
	sc := newLeftoverScan(g)
	for _, l := range strings.Split(text, "\n") {
		if err := sc.line(l); err != nil {
			return res, err
		}
	}
	err = sc.result(&res)
	return res, err
}

// MarkRebuildLoad sets the rebuild-load mark as a snapshot from host:port
// does once read-only is lifted; ok is false when the pins do not allow
// one (not quarantined under host:port). clear ends it.
func (d *Driver) MarkRebuildLoad(host string, port int) (clear func(), ok bool) {
	m := d.rebuildMark(host, port)
	m.set()
	return m.clear, m != nil
}

// RebuildLoadMarked reports whether a rebuild-load mark is set.
func (d *Driver) RebuildLoadMarked() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	return d.rebuild.active
}

// MarkRebuildLoaded records a successful snapshot job from host:port, as
// dumpFrom and cloneFrom do on a quarantined server under that source.
func (d *Driver) MarkRebuildLoaded(host string, port int) bool {
	m := d.rebuildMark(host, port)
	m.done()
	return m != nil
}

// HoldPromoteMu takes the lock promote commands and the guard share, as
// a running repoint does; release gives it back.
func (d *Driver) HoldPromoteMu() (release func()) {
	d.promoteMu.Lock()
	return d.promoteMu.Unlock
}

// LeftoverSetFile is the file under the salvage directory that keeps the
// last mysql.leftover set.
const LeftoverSetFile = leftoverSetFile

// The start lock (startlock.go).
var (
	WithStartLockLine    = withStartLockLine
	WithoutStartLockLine = withoutStartLockLine
	PrintDefaultsArgs    = printDefaultsArgs
	StartLockStatus      = startLockStatus
	WriteStartLock       = writeStartLock
	StartLockLinePresent = startLockLinePresent
	ReadManagedContent   = readManagedContent
	MACAllows            = macAllows
	OptionName           = optionName
	ReplaceEnv           = replaceEnv
)

// StartLockContent is what the start lock file holds.
const StartLockContent = startLockContent

// SetMACConfinement replaces the AppArmor/SELinux detection; it returns
// the function that restores it.
func SetMACConfinement(f func(Layout) string) (restore func()) {
	old := macConfinement
	macConfinement = f
	return func() { macConfinement = old }
}

// SetLookupUser replaces the account lookup of the start lock check; it
// returns the function that restores it.
func SetLookupUser(f func(name string) (*user.User, error)) (restore func()) {
	old := lookupUser
	lookupUser = f
	return func() { lookupUser = old }
}

// SetListHome replaces the listing of the service account's home; it
// returns the function that restores it.
func SetListHome(f func(path string) ([]string, error)) (restore func()) {
	old := listHome
	listHome = f
	return func() { listHome = old }
}

// SetReadXattr replaces the extended attribute reader; it returns the
// function that restores it.
func SetReadXattr(f func(path, name string) (string, error)) (restore func()) {
	old := readXattr
	readXattr = f
	return func() { readXattr = old }
}

// The start lock's guard and release (startlock_guard.go).
var ScanBootstrapFailure = scanBootstrapFailure

// SetWriteConfigHook makes every write of the managed config first call f,
// failing with its error; it returns the function that restores it.
func SetWriteConfigHook(f func(content string) error) (restore func()) {
	old := writeConfigHook
	writeConfigHook = f
	return func() { writeConfigHook = old }
}

// SetRemoveStartLockFile replaces the removal of a start lock file; it
// returns the function that restores it.
func SetRemoveStartLockFile(f func(path string) error) (restore func()) {
	old := removeStartLockFile
	removeStartLockFile = f
	return func() { removeStartLockFile = old }
}

// SetClearLock replaces Pins.ClearLock where a quarantine calls it; it
// returns the function that restores it.
func SetClearLock(f func(*Pins) error) (restore func()) {
	old := clearLock
	clearLock = f
	return func() { clearLock = old }
}

// The managed config's cross-process lock (startlock.go).
var (
	ConfigLockPath = configLockPath
	FlockConfig    = flockConfig
)

// SetStartLockProved sets the proof that this agent run PERSISTed
// read-only ON (startLockProved), as a quarantine would.
func (d *Driver) SetStartLockProved(v bool) {
	d.promoteMu.Lock()
	d.startLockProved = v
	d.promoteMu.Unlock()
}

// Replica port record (seen_ports).
var (
	ReadSeenPorts = readSeenPorts
	ReplicaPorts  = replicaPorts
)

// The promote lock's replica reads (R-033).
var (
	ReadableAtLock = readableAtLock
	ReadListed     = readListed
)

// MaxPeerReads bounds the replica reads made at once.
const MaxPeerReads = maxPeerReads

// SetBeforeLockRecord calls f in mysql.lockForPromote after the candidate's
// Binlog Dump thread was found and before the replicas are recorded.
func SetBeforeLockRecord(f func()) (restore func()) {
	old := beforeLockRecord
	beforeLockRecord = f
	return func() { beforeLockRecord = old }
}

// ObserveReplicas is d.observeReplicas.
func ObserveReplicas(d *Driver, ctx context.Context, db *sql.DB, now time.Time, also ...string) ([]string, []int, error) {
	return d.observeReplicas(ctx, db, now, also...)
}

// SetAfterForgetRead calls f in ForgetLockHost between its read of the
// lock and its write.
func SetAfterForgetRead(f func()) (restore func()) {
	old := afterForgetRead
	afterForgetRead = f
	return func() { afterForgetRead = old }
}

// HoldLockFlock takes p's LockFlockFile, as a writer of the promote lock
// file does; release gives it back.
func HoldLockFlock(p *Pins) (release func(), err error) { return p.flockLock() }

// SetLockFlockWait changes how long a promote lock writer waits for the
// flock; it returns the function that restores it.
func SetLockFlockWait(d time.Duration) (restore func()) {
	old := lockFlockWait
	lockFlockWait = d
	return func() { lockFlockWait = old }
}

// Connection list internals.
var (
	ConnKind       = connKind
	ServiceAccount = serviceAccount
)

// ConnThread is one client thread as the connection list reads it.
type ConnThread = connThread

// GroupConnections groups threads into rows (no server).
var GroupConnections = groupConnections

// AgentProgramName is the connection attribute value of the agent's sessions.
const AgentProgramName = agentProgramName

// ReadConnections reads the connection list from db.
var ReadConnections = readConnections

// Connection list filter, for the guard tests.
var (
	ReplicationThreadNames = replicationThreadNames
	PSArgs                 = psArgs
)

// ConnectionsPSQuery is the performance_schema thread query.
func ConnectionsPSQuery() string { return connectionsPSQuery }

// CheckApproval is the validity rule of a rebuild approval.
func CheckApproval(a RebuildApproval, role, source string, ips []net.IP, now time.Time) error {
	return checkApproval(a, role, source, ips, now)
}
