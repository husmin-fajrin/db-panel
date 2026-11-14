package mysql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// strictSource is the source pin of the strict mode tests' quarantined
// server.
const strictSource = "10.0.0.1:3306"

// protectedCases are the commands strict mode protects, with valid
// parameters.
var protectedCases = []pinCase{
	{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}},
	{"mysql.dumpFrom", validSnapshot},
	{"mysql.cloneFrom", validSnapshot},
	{"mysql.configureReplication", validSource},
	{"mysql.setReplicationFilter", proto.ReplicationFilterParams{Databases: []string{"toko"}}},
	{"mysql.startReplica", nil},
	{"mysql.startUntil", proto.StartUntilParams{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5", TimeoutSec: 10}},
	{"mysql.resetReplica", nil},
	{"mysql.configure", proto.ConfigureParams{Role: "replica"}},
	{"mysql.restart", nil},
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", Password: "rahasia"}},
	{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "10.0.0.2", Password: "rahasia"}},
	{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_dump_0123abcd", FromHost: "10.0.0.2"}},
	{"mysql.sweepDumpUsers", proto.SweepDumpUsersParams{OlderThanSec: 3600}},
	{"mysql.tlsRequest", nil},
	{"mysql.tlsInstall", proto.TLSInstallParams{CertPEM: "x", CAPEM: "y", VerifyHost: "db1"}},
}

// TestQuarantinedStrictModeRefusesProtected: on a quarantined server
// without an approval every protected command is refused with
// needs_local_approval, naming the command, the CLI command and how long
// the code is valid; all refusals carry the one request's code.
func TestQuarantinedStrictModeRefusesProtected(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	// Every command classed protected has a case here.
	cased := map[string]bool{}
	for _, c := range protectedCases {
		cased[c.cmd] = true
	}
	for cmd, class := range mysql.StrictClassNames() {
		if class == "protected" && !cased[cmd] {
			t.Errorf("%s is protected but has no case in protectedCases", cmd)
		}
	}
	codes := map[string]bool{}
	codeRe := regexp.MustCompile(`approve ([0-9]{6}) `)
	for _, c := range protectedCases {
		resp := r.dispatch(c.cmd, c.params)
		wantLocalRefusal(t, c.cmd, resp, c.cmd+" butuh persetujuan root di server ini",
			"sudo dbpanel agent approve ", "kode berlaku ", "mengikuti sumber "+strictSource)
		if resp.Error != nil {
			if m := codeRe.FindStringSubmatch(resp.Error.Message); m != nil {
				codes[m[1]] = true
			} else {
				t.Errorf("%s: no code in %q", c.cmd, resp.Error.Message)
			}
		}
	}
	req, ok, err := mustPins(t, dir).ReadRebuildRequest()
	if err != nil || !ok {
		t.Fatalf("request file after the refusals: %+v, %v, %v", req, ok, err)
	}
	if len(codes) != 1 || !codes[req.Code] {
		t.Errorf("codes in the refusals = %v, want only the request's %q", codes, req.Code)
	}
	if req.Source != strictSource {
		t.Errorf("request source = %q, want %s", req.Source, strictSource)
	}
	if _, ok, _ := mustPins(t, dir).ReadRebuildApproval(); ok {
		t.Error("a refusal wrote an approval")
	}
}

// TestStrictRefusalCarriesFullCommand: the refusal ends with the whole
// command root runs, so an old panel that only shows the text still
// tells the operator what to do.
func TestStrictRefusalCarriesFullCommand(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "quarantined", strictSource))
	resp := r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}})
	if resp.OK || resp.Error == nil {
		t.Fatalf("dropDatabases = %+v, want refused", resp)
	}
	re := regexp.MustCompile(`sudo dbpanel agent approve [0-9]{6} \(kode berlaku [0-9]+ menit lagi\)$`)
	if !re.MatchString(resp.Error.Message) {
		t.Errorf("message %q does not end with the full command", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "(kode berlaku 30 menit lagi)") {
		t.Errorf("fresh code: message %q, want 30 minutes left", resp.Error.Message)
	}
}

// allowedParams are valid parameters for the allowed commands that take
// some; the others get none.
var allowedParams = map[string]any{
	"mysql.connect":       proto.ConnectParams{User: "root", Password: "rahasia"},
	"mysql.preflight":     proto.PreflightParams{Role: "master"},
	"mysql.waitSynced":    proto.WaitSyncedParams{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5", TimeoutSec: 1},
	"mysql.leftover":      proto.LeftoverParams{NewMaster: proto.PeerCred{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "rahasia"}},
	"mysql.salvage":       proto.SalvageParams{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5"},
	"mysql.salvageRead":   proto.SalvageReadParams{Name: "20261112T080000Z-1.sql"},
	"mysql.salvageDelete": proto.SalvageDeleteParams{Name: "20261112T080000Z-1.sql"},
	"mysql.quarantine":    proto.QuarantineParams{NewMaster: proto.PeerCred{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "rahasia"}},
	"mysql.setReadOnly":   proto.ReadOnlyParams{On: true},
}

// TestQuarantinedStrictModeAllowsListed: the allowed mysql.* commands get
// past strict mode without an approval (to the handler, which has no
// MySQL here; mysql.tlsTrust among them, which the rescue path needs),
// and make no request (mysql.rebuildApproval, which makes
// one, has its own test); setReadOnly{off} stays the role table's refusal.
func TestQuarantinedStrictModeAllowsListed(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	for cmd, class := range mysql.StrictClassNames() {
		if class != "allowed" || !strings.HasPrefix(cmd, "mysql.") || cmd == "mysql.rebuildApproval" {
			continue
		}
		params := allowedParams[cmd]
		if cmd == "mysql.tlsTrust" {
			params = proto.TLSTrustParams{CAPEM: string(testMySQLCA(t).CAPEM())}
		}
		resp := r.dispatch(cmd, params)
		if resp.OK {
			continue
		}
		if resp.Error != nil && resp.Error.Code == proto.CodeNeedsLocalApproval {
			t.Errorf("%s = %+v, want it past strict mode", cmd, resp)
			continue
		}
		// The salvage file commands need no MySQL: their handler answers
		// that the file is not there.
		if (cmd == "mysql.salvageRead" || cmd == "mysql.salvageDelete") && resp.Error != nil &&
			resp.Error.Message == "berkas salvage 20261112T080000Z-1.sql tidak ada" {
			continue
		}
		wantPastPins(t, cmd, resp)
	}
	wantRefused(t, "setReadOnly off", r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}),
		"dipin sebagai quarantined")
	if _, ok, err := mustPins(t, dir).ReadRebuildRequest(); ok || err != nil {
		t.Errorf("allowed commands made a request (%v, %v)", ok, err)
	}
}

// TestStrictApprovalOpensRebuildPath: with root's approval the rebuild path
// toward the pinned source gets past strict mode and the pins; another
// source is still refused by the source pin.
func TestStrictApprovalOpensRebuildPath(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	approveRebuild(t, dir, strictSource)
	for _, c := range []pinCase{
		{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}},
		{"mysql.dumpFrom", validSnapshot},
		{"mysql.cloneFrom", validSnapshot},
		{"mysql.configure", proto.ConfigureParams{Role: "replica"}},
		{"mysql.configureReplication", validSource},
		{"mysql.setReplicationFilter", proto.ReplicationFilterParams{Databases: []string{"toko"}}},
		{"mysql.resetReplica", nil},
		{"mysql.startReplica", nil},
		{"mysql.setReadOnly", proto.ReadOnlyParams{On: true}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", Password: "rahasia"}},
	} {
		wantPastPins(t, c.cmd, r.dispatch(c.cmd, c.params))
	}
	other := validSnapshot
	other.Host = "10.0.0.2"
	wantRefused(t, "dumpFrom another host", r.dispatch("mysql.dumpFrom", other),
		"server ini dipin mengikuti sumber "+strictSource)
}

// TestQuarantinedApprovalLimits: an approval never opens mysql.install on
// a quarantined server (no rebuild installs MySQL on an old master), and
// under it mysql.dropUser drops only the per-job dump accounts, as on a
// master.
func TestQuarantinedApprovalLimits(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	install := proto.InstallParams{Series: "8.4"}
	wantRefused(t, "install without an approval", r.dispatch("mysql.install", install),
		"server ini dipin sebagai quarantined", "perintah mysql.install ditolak")
	approveRebuild(t, dir, strictSource)
	wantRefused(t, "install with an approval", r.dispatch("mysql.install", install),
		"server ini dipin sebagai quarantined", "perintah mysql.install ditolak")
	for _, u := range []proto.DropUserParams{{User: "dbpanel_repl", FromHost: "%"}, {User: "dbpanel_admin", FromHost: "localhost"}} {
		wantRefused(t, "dropUser "+u.User, r.dispatch("mysql.dropUser", u),
			"server ini dipin sebagai quarantined", "perintah mysql.dropUser ditolak")
	}
	wantPastPins(t, "dropUser of a dump account", r.dispatch("mysql.dropUser",
		proto.DropUserParams{User: "dbpanel_dump_0123abcd", FromHost: "10.0.0.2"}))
}

// TestStrictApprovalVoidedBySourceDNSExpiryAndSetRole: an approval holds
// only for the pinned source, while its name resolves to the approved
// address, before it expires, and until root sets the role again.
func TestStrictApprovalVoidedBySourceDNSExpiryAndSetRole(t *testing.T) {
	drop := pinCase{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}}

	// (a) Another source pin.
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	approveRebuild(t, dir, strictSource)
	wantPastPins(t, "approved", r.dispatch(drop.cmd, drop.params))
	if err := mustPins(t, dir).SetSource("10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	wantLocalRefusal(t, "after set-source", r.dispatch(drop.cmd, drop.params), "butuh persetujuan root", "mengikuti sumber 10.0.0.2:3306")

	// (b) A source name that now resolves elsewhere.
	ip := "10.0.0.5"
	t.Cleanup(mysql.SetResolver(func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP(ip)}, nil }, nil))
	ndir := pinDir(t, "quarantined", "db-b:3306")
	nr := newPinRig(t, nil, ndir)
	if err := mustPins(t, ndir).WriteRebuildApproval(mysql.NewRebuildApproval("db-b:3306", "10.0.0.5", time.Now())); err != nil {
		t.Fatal(err)
	}
	wantPastPins(t, "name at the approved address", nr.dispatch(drop.cmd, drop.params))
	ip = "10.0.0.6"
	wantLocalRefusal(t, "name at another address", nr.dispatch(drop.cmd, drop.params), "butuh persetujuan root")

	// (c) Expired.
	edir := pinDir(t, "quarantined", strictSource)
	er := newPinRig(t, nil, edir)
	approveRebuild(t, edir, strictSource)
	a, ok, err := mustPins(t, edir).ReadRebuildApproval()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	wantPastPins(t, "before expiry", er.dispatch(drop.cmd, drop.params))
	er.d.Now = func() time.Time { return a.Expires }
	wantLocalRefusal(t, "at expiry", er.dispatch(drop.cmd, drop.params), "butuh persetujuan root")

	// (d) Root sets the role again: both files go.
	sdir := pinDir(t, "quarantined", strictSource)
	sr := newPinRig(t, nil, sdir)
	approveRebuild(t, sdir, strictSource)
	if _, err := mustPins(t, sdir).EnsureRebuildRequest(time.Now()); err != nil {
		t.Fatal(err)
	}
	wantPastPins(t, "before set-role", sr.dispatch(drop.cmd, drop.params))
	if err := mustPins(t, sdir).SetRole("quarantined"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if _, err := os.Stat(filepath.Join(sdir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after set-role: %v, want gone", name, err)
		}
	}
	wantLocalRefusal(t, "after set-role", sr.dispatch(drop.cmd, drop.params), "butuh persetujuan root")
}

// TestSetRoleKeepsRoleWhenApprovalStays: when the approval cannot be
// removed, set-role fails and the role is not written.
func TestSetRoleKeepsRoleWhenApprovalStays(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	// A directory in the approval's place cannot be removed.
	if err := os.MkdirAll(filepath.Join(dir, mysql.RebuildApprovalFile, "blok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mustPins(t, dir).SetRole("master"); err == nil {
		t.Fatal("SetRole succeeded with an approval it could not remove")
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "quarantined" {
		t.Errorf("role pin = %q, want quarantined", got)
	}
}

// TestUnsourcedQuarantinedStrictRefusal: a quarantined server without a
// source pin refuses protected commands with needs_local_approval and the
// set-source command, and makes no request.
func TestUnsourcedQuarantinedStrictRefusal(t *testing.T) {
	dir := pinDir(t, "quarantined", "")
	r := newPinRig(t, nil, dir)
	wantLocalRefusal(t, "dropDatabases", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}),
		"quarantined tanpa sumber yang dipin", "perintah mysql.dropDatabases ditolak", "sudo dbpanel agent set-source")
	if _, err := os.Stat(filepath.Join(dir, mysql.RebuildRequestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("request file: %v, want none", err)
	}
	wantRefused(t, "rebuildApproval", r.dispatch("mysql.rebuildApproval", nil),
		"quarantined tanpa sumber yang dipin", "perintah mysql.rebuildApproval ditolak", "sudo dbpanel agent set-source")
}

// TestApprovalFirstUseLogged: the first command an approval lets through
// is logged once, with the source and the address.
func TestApprovalFirstUseLogged(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	var buf bytes.Buffer
	r.d.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	approveRebuild(t, dir, strictSource)
	wantPastPins(t, "dropDatabases", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}))
	wantPastPins(t, "dumpFrom", r.dispatch("mysql.dumpFrom", validSnapshot))
	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `msg="local rebuild approval used"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("approval use logged %d times, want once:\n%s", len(lines), buf.String())
	}
	for _, want := range []string{"cmd=mysql.dropDatabases", "source=" + strictSource, "addr=10.0.0.1", "approved=", "expires="} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line %q does not contain %q", lines[0], want)
		}
	}
}

// TestApprovedCommandsLogged: every protected command that passes the
// gate under an approval is logged at Info with its name, the source and
// the approval time.
func TestApprovedCommandsLogged(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	var buf bytes.Buffer
	r.d.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	approveRebuild(t, dir, strictSource)
	wantPastPins(t, "dropDatabases", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}))
	wantPastPins(t, "dumpFrom", r.dispatch("mysql.dumpFrom", validSnapshot))
	wantPastPins(t, "status", r.dispatch("mysql.status", nil)) // allowed: not logged
	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `msg="protected command allowed by the local rebuild approval"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("approved commands logged %d times, want twice:\n%s", len(lines), buf.String())
	}
	for i, cmd := range []string{"mysql.dropDatabases", "mysql.dumpFrom"} {
		for _, want := range []string{"level=INFO", "cmd=" + cmd, "source=" + strictSource, "approved="} {
			if !strings.Contains(lines[i], want) {
				t.Errorf("log line %q does not contain %q", lines[i], want)
			}
		}
	}
}

// TestRebuildApprovalCommand: mysql.rebuildApproval tells the panel
// whether an approval is needed, the code to show while none holds, and
// the approval while one does.
func TestRebuildApprovalCommand(t *testing.T) {
	rep := newPinRig(t, nil, pinDir(t, "replica", strictSource))
	if res := decode[proto.RebuildApprovalResult](t, rep.dispatch("mysql.rebuildApproval", nil)); res != (proto.RebuildApprovalResult{}) {
		t.Errorf("on a replica = %+v, want not required", res)
	}

	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	res := decode[proto.RebuildApprovalResult](t, r.dispatch("mysql.rebuildApproval", nil))
	if !res.Required || res.Approved || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(res.Code) || res.Source != strictSource ||
		res.RemainingSec <= 1790 || res.RemainingSec > 1800 || res.Invalid != "" || res.Addr != "" {
		t.Errorf("without an approval = %+v", res)
	}
	again := decode[proto.RebuildApprovalResult](t, r.dispatch("mysql.rebuildApproval", nil))
	if again.Code != res.Code {
		t.Errorf("second call code %q, want the same %q", again.Code, res.Code)
	}

	approveRebuild(t, dir, strictSource)
	res = decode[proto.RebuildApprovalResult](t, r.dispatch("mysql.rebuildApproval", nil))
	if !res.Required || !res.Approved || res.Addr != "10.0.0.1" || res.AgeSec < 0 || res.AgeSec >= 5 ||
		res.RemainingSec <= 86000 || res.Code != "" || res.Source != strictSource || res.Invalid != "" {
		t.Errorf("approved = %+v", res)
	}

	if err := mustPins(t, dir).WriteRebuildApproval(mysql.NewRebuildApproval("10.0.0.9:3306", "10.0.0.9", time.Now())); err != nil {
		t.Fatal(err)
	}
	res = decode[proto.RebuildApprovalResult](t, r.dispatch("mysql.rebuildApproval", nil))
	if !res.Required || res.Approved || !strings.Contains(res.Invalid, "10.0.0.9:3306") || res.Code == "" || res.Addr != "" {
		t.Errorf("approval for another source = %+v", res)
	}
}

// TestSettleRebuiltClearsApproval: the end of a quarantine by a finished
// rebuild removes the request and the approval.
func TestSettleRebuiltClearsApproval(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	p := mustPins(t, dir)
	if _, err := p.EnsureRebuildRequest(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteRebuildApproval(mysql.NewRebuildApproval(strictSource, "10.0.0.1", time.Now())); err != nil {
		t.Fatal(err)
	}
	if !r.d.MarkRebuildLoaded("10.0.0.1", 3306) {
		t.Fatal("no load mark toward the pinned source")
	}
	if err := r.d.SettleRebuilt("10.0.0.1", 3306); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "replica" {
		t.Errorf("role pin = %q, want replica", got)
	}
	for _, name := range []string{mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after the rebuild: %v, want gone", name, err)
		}
	}
}

// TestStrictModeWithMySQL (spec 5.13): on a real server quarantined under
// its new master, a drop is refused without root's approval and the data
// stays; with it the drop runs; another source pin, an expired approval
// and root setting the role again each take the approval away.
func TestStrictModeWithMySQL(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	drop := func() proto.Response {
		return r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}})
	}
	hasToko := func() bool {
		return count(t, r.db, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = 'toko'") == 1
	}

	wantLocalRefusal(t, "drop without an approval", drop(), "mysql.dropDatabases butuh persetujuan root")
	if !hasToko() {
		t.Fatal("toko gone after a refused drop")
	}
	approveRebuild(t, r.dir, guardCandidateKey)
	decode[proto.DropDatabasesResult](t, drop())
	if hasToko() {
		t.Fatal("toko still there after an approved drop")
	}
	recreate := func() {
		t.Helper()
		execAll(t, r.db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF", "CREATE DATABASE toko",
			"SET GLOBAL super_read_only = ON")
	}
	recreate()

	// Another source pin.
	if err := mustPins(t, r.dir).SetSource("10.9.9.8", 3306); err != nil {
		t.Fatal(err)
	}
	wantLocalRefusal(t, "drop after set-source", drop(), "butuh persetujuan root")
	if !hasToko() {
		t.Fatal("toko gone after a drop refused for another source")
	}
	if err := mustPins(t, r.dir).SetSource("10.9.9.9", 3306); err != nil {
		t.Fatal(err)
	}

	// Expired.
	approveRebuild(t, r.dir, guardCandidateKey)
	a, _, err := mustPins(t, r.dir).ReadRebuildApproval()
	if err != nil {
		t.Fatal(err)
	}
	r.d.Now = func() time.Time { return a.Expires.Add(time.Second) }
	wantLocalRefusal(t, "drop after expiry", drop(), "butuh persetujuan root")
	r.d.Now = nil
	if !hasToko() {
		t.Fatal("toko gone after a drop refused for an expired approval")
	}

	// Root sets the role again.
	approveRebuild(t, r.dir, guardCandidateKey)
	if err := mustPins(t, r.dir).SetRole("quarantined"); err != nil {
		t.Fatal(err)
	}
	wantLocalRefusal(t, "drop after set-role", drop(), "butuh persetujuan root")
	if !hasToko() {
		t.Fatal("toko gone after a drop refused after set-role")
	}
}

// TestStrictModeFailsClosed: an approval file the agent cannot trust lets
// nothing through, and when no request can be written the refusal says so
// (text A3) and still needs root.
func TestStrictModeFailsClosed(t *testing.T) {
	drop := proto.DropDatabasesParams{Databases: []string{"toko"}}
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	approveRebuild(t, dir, strictSource)
	if err := os.Chmod(filepath.Join(dir, mysql.RebuildApprovalFile), 0o666); err != nil {
		t.Fatal(err)
	}
	wantLocalRefusal(t, "untrusted approval", r.dispatch("mysql.dropDatabases", drop), "butuh persetujuan root", "sudo dbpanel agent approve ")

	// A directory in the request's place cannot be read or replaced.
	ndir := pinDir(t, "quarantined", strictSource)
	nr := newPinRig(t, nil, ndir)
	if err := os.MkdirAll(filepath.Join(ndir, mysql.RebuildRequestFile, "blok"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp := nr.dispatch("mysql.dropDatabases", drop)
	wantLocalRefusal(t, "no request", resp, "server ini dikarantina (quarantined); perintah mysql.dropDatabases butuh persetujuan root di server ini, "+
		"tetapi permintaan persetujuan tidak bisa dibuat: ")
	if resp.Error != nil && strings.Contains(resp.Error.Message, "approve ") {
		t.Errorf("refusal without a request names a code: %q", resp.Error.Message)
	}
}

// TestStrictGateProtectsUnclassified: a command strict mode does not know
// is protected.
func TestStrictGateProtectsUnclassified(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "quarantined", strictSource))
	ran := false
	r.reg.Register("mysql.notClassified", func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil })
	wantLocalRefusal(t, "unclassified", r.dispatch("mysql.notClassified", nil), "perintah mysql.notClassified butuh persetujuan root")
	if ran {
		t.Error("the unclassified command's handler ran")
	}
}

// TestStrictGateRefusesWithUnreadablePins: while the pin files cannot be
// trusted only the allowed commands get past the gate, also those whose
// handlers never ask the pins; a server without pin files is unassigned,
// not unreadable.
func TestStrictGateRefusesWithUnreadablePins(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	if err := os.Chmod(filepath.Join(dir, mysql.RoleFile), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, c := range []pinCase{
		{"mysql.install", proto.InstallParams{Series: "8.4"}},
		{"mysql.sweepDumpUsers", proto.SweepDumpUsersParams{OlderThanSec: 3600}},
		{"mysql.restart", nil},
		{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}},
	} {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params), "pin peran/sumber agent tidak bisa dipakai, perintah "+c.cmd+" ditolak: ")
	}
	wantPastPins(t, "mysql.status", r.dispatch("mysql.status", nil))

	fresh := newPinRig(t, nil, pinDir(t, "", ""))
	resp := fresh.dispatch("mysql.install", proto.InstallParams{Series: "8.4"})
	if resp.OK || resp.Error == nil || resp.Error.Message != "agent tidak bisa memasang MySQL di server ini" {
		t.Errorf("install without pin files = %+v, want it past the gate to the handler", resp)
	}
}

// TestRebuildApprovalWithUnreadablePins: with pins that cannot be read
// mysql.rebuildApproval does not say "not required"; it answers a
// parameter error carrying the pin error, as the gate does.
func TestRebuildApprovalWithUnreadablePins(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	if err := os.Chmod(filepath.Join(dir, mysql.RoleFile), 0o666); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "rebuildApproval", r.dispatch("mysql.rebuildApproval", nil),
		"pin peran/sumber agent tidak bisa dipakai, perintah mysql.rebuildApproval ditolak: ", mysql.RoleFile)
}

// TestEnteringQuarantineClearsStaleApproval: an approval left on a
// rebuilt server never holds when it is quarantined again; a quarantine
// written again on a quarantined server keeps root's approval.
func TestEnteringQuarantineClearsStaleApproval(t *testing.T) {
	dir := pinDir(t, "replica", strictSource)
	p := mustPins(t, dir)
	if err := p.WriteRebuildApproval(mysql.NewRebuildApproval(strictSource, "10.0.0.1", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mysql.RebuildRequestFile), []byte(`{"code":"123456","source":"10.0.0.1:3306","created":"2026-11-13T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newPinRig(t, nil, dir)
	if err := p.SetQuarantined(strictSource); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after entering quarantine: %v, want gone", name, err)
		}
	}
	wantLocalRefusal(t, "drop after entering quarantine", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}),
		"butuh persetujuan root")

	// Already quarantined: root's fresh approval stays valid.
	approveRebuild(t, dir, strictSource)
	if err := p.SetQuarantined(strictSource); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckRebuildApproval(context.Background(), time.Now()); err != nil {
		t.Errorf("approval after a repeated quarantine: %v, want valid", err)
	}
	wantPastPins(t, "drop after a repeated quarantine", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}))

	// Rebuild files that cannot be removed: the role is not written.
	bdir := pinDir(t, "master", "")
	if err := os.MkdirAll(filepath.Join(bdir, mysql.RebuildApprovalFile, "blok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mustPins(t, bdir).SetQuarantined(strictSource); err == nil {
		t.Fatal("SetQuarantined succeeded with an approval it could not remove")
	}
	if role, src := pinFile(t, bdir, mysql.RoleFile), pinFile(t, bdir, mysql.SourceFile); role != "master" || src != "" {
		t.Errorf("pins after a refused quarantine = %q, %q; want master and no source", role, src)
	}
}
