package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// pinDir is a new agent directory holding the given role and source pins
// ("" leaves that file out).
func pinDir(t *testing.T, role, source string) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range map[string]string{mysql.RoleFile: role, mysql.SourceFile: source} {
		if v == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// pinFile is the content of dir/name without the newline; "" when absent.
func pinFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// pinRig is a registry holding one driver over h with its pins in dir, so
// that every dispatch goes through the same Pins (as in a running agent).
type pinRig struct {
	t   *testing.T
	reg *commands.Registry
	d   *mysql.Driver
}

func newPinRig(t *testing.T, h mysql.Host, dir string) *pinRig {
	t.Helper()
	reg := commands.NewRegistry()
	d := &mysql.Driver{PinDir: dir}
	if h != nil {
		d.Host = h
	}
	d.Register(reg)
	return &pinRig{t: t, reg: reg, d: d}
}

func (r *pinRig) dispatch(cmd string, params any) proto.Response {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return r.reg.Dispatch(ctx, proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(r.t, params)})
}

// wantRefused fails unless r is a parameter error whose message holds
// every one of parts.
func wantRefused(t *testing.T, what string, r proto.Response, parts ...string) {
	t.Helper()
	if r.OK || r.Error == nil || r.Error.Code != proto.CodeBadParams {
		t.Errorf("%s = %+v, want refused", what, r)
		return
	}
	for _, p := range parts {
		if !strings.Contains(r.Error.Message, p) {
			t.Errorf("%s message %q does not contain %q", what, r.Error.Message, p)
		}
	}
}

// wantPastPins fails unless r is the answer of a driver without MySQL: the
// command got past the pins.
func wantPastPins(t *testing.T, what string, r proto.Response) {
	t.Helper()
	if r.OK || r.Error == nil || r.Error.Message != mysql.ErrNoHost.Error() {
		t.Errorf("%s = %+v, want it past the pins (%q)", what, r, mysql.ErrNoHost)
	}
}

type pinCase struct {
	cmd    string
	params any
}

var (
	validSnapshot = proto.SnapshotParams{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "rahasia"}
	validSource   = proto.ReplicationSourceParams{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "rahasia"}
)

// masterRefused are the commands spec 5.5 refuses on a master.
var masterRefused = []pinCase{
	{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}},
	{"mysql.dumpFrom", validSnapshot},
	{"mysql.cloneFrom", validSnapshot},
	{"mysql.configureReplication", validSource},
	{"mysql.setReplicationFilter", proto.ReplicationFilterParams{Databases: []string{"toko"}}},
	{"mysql.resetReplica", nil},
	{"mysql.startReplica", nil},
	{"mysql.startUntil", proto.StartUntilParams{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5", TimeoutSec: 10}},
	{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_repl", FromHost: "%"}},
	{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_admin", FromHost: "localhost"}},
	{"mysql.configure", proto.ConfigureParams{Role: "replica"}},
	{"mysql.setReadOnly", proto.ReadOnlyParams{On: true}},
	{"mysql.releaseRole", nil},
}

// replicaRefused are the commands spec 5.5 refuses on a replica.
var replicaRefused = []pinCase{
	{"mysql.setReadOnly", proto.ReadOnlyParams{On: false}},
	{"mysql.configure", proto.ConfigureParams{Role: "master"}},
}

// replUserRefused are the replication accounts a pinned master, replica or
// quarantined server never creates: only dbpanel_repl@'%' gets past the
// pins.
var replUserRefused = []pinCase{
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_x", Password: "rahasia"}},
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "10.0.0.%", Password: "rahasia"}},
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "10.0.0.5", Password: "rahasia"}},
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "localhost", Password: "rahasia"}},
}

// TestMasterRefusesDestructiveCommands: on a server pinned as master every
// command of the spec's master row is refused before MySQL is touched (the
// driver has none), naming the role, the command and the CLI; the others
// get past the pins.
func TestMasterRefusesDestructiveCommands(t *testing.T) {
	dir := pinDir(t, "master", "")
	r := newPinRig(t, nil, dir)
	for _, c := range masterRefused {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params),
			"server ini dipin sebagai master", "perintah "+c.cmd+" ditolak", "sudo dbpanel agent set-role <peran>")
	}
	for _, c := range append(replicaRefused, pinCase{"mysql.configure", proto.ConfigureParams{Role: "master"}},
		pinCase{"mysql.stopReplica", nil},
		// The per-job dump accounts stay droppable on a master.
		pinCase{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_dump_7", FromHost: "10.0.0.2"}}) {
		wantPastPins(t, c.cmd, r.dispatch(c.cmd, c.params))
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "master" {
		t.Errorf("role pin = %q, want master", got)
	}
}

// TestMasterRefusesAccountTakeover (SEC-42): a pinned master makes only a
// per-job dump account (dbpanel_dump_ and 8 hex digits) from one IP
// address and only the replication account, and is not restarted without
// a configure that asked for it; these refusals need no MySQL.
func TestMasterRefusesAccountTakeover(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "master", ""))
	for _, c := range []pinCase{
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_repl", FromHost: "10.0.0.2", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "%", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "10.0.0.%", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "replika.lan", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_x", FromHost: "10.0.0.2", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123ABCD", FromHost: "10.0.0.2", Password: "rahasia"}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_x", Password: "rahasia"}},
		// A more specific host row would shadow dbpanel_repl@'%' for the
		// replicas it matches (1045 on reconnect) and hand out BACKUP_ADMIN.
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "10.0.0.%", Password: "rahasia"}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "10.0.0.5", Password: "rahasia"}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "localhost", Password: "rahasia"}},
	} {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params), "server ini dipin sebagai master", "perintah "+c.cmd+" ditolak")
	}
	wantRefused(t, "restart without a pending configure", r.dispatch("mysql.restart", nil),
		"server ini dipin sebagai master", "perintah mysql.restart ditolak", "tidak ada pengaturan yang menunggu restart")
	for _, c := range []pinCase{
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "10.0.0.2", Password: "rahasia"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "fd00::2", Password: "rahasia"}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", Password: "rahasia"}},
		{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", FromHost: "%", Password: "rahasia"}},
	} {
		wantPastPins(t, c.cmd, r.dispatch(c.cmd, c.params))
	}
	// Elsewhere the restart is not the pins' business.
	for _, role := range []string{"replica", ""} {
		wantPastPins(t, "restart on "+role, newPinRig(t, nil, pinDir(t, role, "")).dispatch("mysql.restart", nil))
	}
}

// TestPinnedMasterKeepsAccountsAndRestartsOnce (SEC-42, SEC-51a): on a
// real master pinned by its first configure, a fresh replication account
// and a per-job dump account are made, but an existing one is never
// altered; the restart that configure asked for is allowed once; a later
// configure keeps the server_id.
func TestPinnedMasterKeepsAccountsAndRestartsOnce(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "", "")
	r := newPinRig(t, inst.Host(), dir)
	res := decode[proto.ConfigureResult](t, r.dispatch("mysql.configure", proto.ConfigureParams{Role: "master", GTID: true}))
	if !res.NeedsRestart || pinFile(t, dir, mysql.RoleFile) != "master" {
		t.Fatalf("configure{master}: NeedsRestart=%v, role pin %q", res.NeedsRestart, pinFile(t, dir, mysql.RoleFile))
	}
	db := rootDB(t, inst)
	grants := func(user, host string) string {
		t.Helper()
		var g string
		if err := db.QueryRow("SHOW GRANTS FOR " + sqlLiteral(user) + "@" + sqlLiteral(host)).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}

	repl := proto.ReplUserParams{User: "dbpanel_repl", FromHost: "%", Password: "rahasia-satu"}
	if resp := r.dispatch("mysql.createReplUser", repl); !resp.OK {
		t.Fatalf("fresh createReplUser on a pinned master = %+v", resp.Error)
	}
	before := grants("dbpanel_repl", "%")
	repl.Password = "rahasia-dua"
	wantRefused(t, "createReplUser for an existing account", r.dispatch("mysql.createReplUser", repl),
		"sudah ada di master yang dipin")
	wantRefused(t, "createDumpUser{dbpanel_repl}", r.dispatch("mysql.createDumpUser",
		proto.DumpUserParams{User: "dbpanel_repl", FromHost: "%", Password: "x"}), "perintah mysql.createDumpUser ditolak")
	if after := grants("dbpanel_repl", "%"); after != before || !strings.Contains(after, "REPLICATION SLAVE") {
		t.Errorf("replication account grants changed: %q -> %q", before, after)
	}
	// The stored password is still the first one.
	if err := sqlLogin(t, inst, "dbpanel_repl", "rahasia-satu"); err != nil {
		t.Errorf("replication account's password changed: %v", err)
	}

	// A replication account at any other host also blocks a new one at
	// '%': the account is created once, as dbpanel_repl@'%', and never
	// again from the panel.
	for _, q := range []string{"DROP USER 'dbpanel_repl'@'%'", "CREATE USER 'dbpanel_repl'@'10.9.9.9' IDENTIFIED BY 'lain'"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	repl.Password = "rahasia-tiga"
	wantRefused(t, "createReplUser while dbpanel_repl exists at another host", r.dispatch("mysql.createReplUser", repl),
		"sudah ada di master yang dipin", "10.9.9.9")
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM mysql.user WHERE user = 'dbpanel_repl' AND host = '%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("dbpanel_repl@'%%' after the refusal: count %d, err %v; want none", n, err)
	}

	dump := proto.DumpUserParams{User: "dbpanel_dump_0123abcd", FromHost: "127.0.0.1", Password: "dump-satu"}
	if resp := r.dispatch("mysql.createDumpUser", dump); !resp.OK {
		t.Fatalf("per-job createDumpUser on a pinned master = %+v", resp.Error)
	}
	dump.Password = "dump-dua"
	wantRefused(t, "createDumpUser for an existing account", r.dispatch("mysql.createDumpUser", dump),
		"sudah ada di master yang dipin")
	if err := sqlLogin(t, inst, "dbpanel_dump_0123abcd", "dump-satu"); err != nil {
		t.Errorf("dump account's password changed: %v", err)
	}
	db.Close() // before its connections die in the restart below

	decode[proto.RestartResult](t, r.dispatch("mysql.restart", nil))
	wantRefused(t, "second restart", r.dispatch("mysql.restart", nil), "tidak ada pengaturan yang menunggu restart")

	// Settings in effect now: no restart needed, so none is allowed; the
	// server_id asked for is ignored.
	res = decode[proto.ConfigureResult](t, r.dispatch("mysql.configure", proto.ConfigureParams{Role: "master", ServerID: 4242, GTID: true}))
	if res.NeedsRestart || res.ServerID != inst.ServerID {
		t.Errorf("configure after restart: NeedsRestart=%v ServerID=%d, want false and %d", res.NeedsRestart, res.ServerID, inst.ServerID)
	}
	if got := variable(t, rootDB(t, inst), "server_id"); got != strconv.Itoa(int(inst.ServerID)) {
		t.Errorf("server_id = %s, want %d", got, inst.ServerID)
	}
	wantRefused(t, "restart after a configure that needs none", r.dispatch("mysql.restart", nil),
		"tidak ada pengaturan yang menunggu restart")
}

// sqlLogin logs in to inst over TCP as user with password.
func sqlLogin(t *testing.T, inst *testmysql.Instance, user, password string) error {
	t.Helper()
	cfg := drv.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = user, password, "tcp", "127.0.0.1:"+strconv.Itoa(inst.Port)
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

// TestReplicaRefusesUnlockAndCreateReplUser: a replica refuses to be made
// writable, made a master or given a replication account other than
// dbpanel_repl@'%'; the commands a replica's flows use get past the pins.
func TestReplicaRefusesUnlockAndCreateReplUser(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "replica", ""))
	for _, c := range append(replicaRefused, replUserRefused...) {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params),
			"server ini dipin sebagai replica", "perintah "+c.cmd+" ditolak", "sudo dbpanel agent set-role <peran>")
	}
	wantPastPins(t, "createReplUser{dbpanel_repl@%}", r.dispatch("mysql.createReplUser",
		proto.ReplUserParams{User: "dbpanel_repl", Password: "rahasia"}))
	for _, c := range masterRefused {
		if c.cmd == "mysql.releaseRole" {
			continue // allowed on a replica; it needs MySQL to check replication is gone
		}
		wantPastPins(t, c.cmd, r.dispatch(c.cmd, c.params))
	}
}

// sourceWithReplica starts a master and a replica that follows it, so the
// master has a binlog dump thread.
func sourceWithReplica(t *testing.T) (master, replica *testmysql.Instance) {
	t.Helper()
	master = testmysql.Start(t, testmysql.Options{})
	replica = testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	follow(t, master, replica)
	synced(t, master, replica)
	return master, replica
}

// TestUnassignedKeepsRefuseOnSource: without a role pin, a server replicas
// read from still refuses unlocking and dropping (refuseOnSource), and the
// refusals write no pin.
func TestUnassignedKeepsRefuseOnSource(t *testing.T) {
	master, _ := sourceWithReplica(t)
	dir := pinDir(t, "", "")
	r := newPinRig(t, master.Host(), dir)
	const onSource = "replika sedang membaca binlog server ini"
	wantRefused(t, "setReadOnly off", r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}), onSource)
	wantRefused(t, "dropDatabases", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}), onSource)
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "" || src != "" {
		t.Errorf("pins after refusals = %q, %q; want none", role, src)
	}
}

// TestMasterUnlockAllowedDespiteDumpThreads: "Buka kunci master" works on
// a pinned master although replicas read its binlog.
func TestMasterUnlockAllowedDespiteDumpThreads(t *testing.T) {
	master, _ := sourceWithReplica(t)
	db := rootDB(t, master)
	execAll(t, db, "SET GLOBAL read_only = ON", "SET GLOBAL super_read_only = ON")
	r := newPinRig(t, master.Host(), pinDir(t, "master", ""))
	if resp := r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}); !resp.OK {
		t.Fatalf("setReadOnly off on a pinned master = %+v", resp.Error)
	}
	if ro, sro := variable(t, db, "read_only"), variable(t, db, "super_read_only"); ro != "OFF" || sro != "OFF" {
		t.Errorf("read_only = %s, super_read_only = %s; want OFF", ro, sro)
	}
}

// TestConfigureWritesRoleOnce: the first successful mysql.configure pins
// its role (0644); a later configure for the other role is refused and the
// pin stays.
func TestConfigureWritesRoleOnce(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "", "")
	r := newPinRig(t, inst.Host(), dir)
	decode[proto.ConfigureResult](t, r.dispatch("mysql.configure", proto.ConfigureParams{Role: "replica"}))
	if got := pinFile(t, dir, mysql.RoleFile); got != "replica" {
		t.Fatalf("role pin after configure{replica} = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, mysql.RoleFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("role pin file: %v, %v; want mode 0644", fi, err)
	}
	wantRefused(t, "configure{master}", r.dispatch("mysql.configure", proto.ConfigureParams{Role: "master"}),
		"server ini dipin sebagai replica", "sudo dbpanel agent set-role")
	decode[proto.ConfigureResult](t, r.dispatch("mysql.configure", proto.ConfigureParams{Role: "replica"}))
	if got := pinFile(t, dir, mysql.RoleFile); got != "replica" {
		t.Errorf("role pin = %q, want replica", got)
	}
}

// TestSourcePinWrittenThenEnforced: as in create_replica, a dump before
// any pin is allowed and pins its source; configureReplication to the same
// source is accepted, and every command naming another source is refused
// with the set-source command.
func TestSourcePinWrittenThenEnforced(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	target := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	dumpUser(t, mdb)
	replUser(t, master)
	execAll(t, mdb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)", "INSERT INTO toko.t VALUES (1)")

	dir := pinDir(t, "replica", "")
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	ev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(ev, slog.New(slog.DiscardHandler))
	rig := &snapshotRig{reg: commands.NewRegistry(), runner: runner, events: ev}
	(&mysql.Driver{Host: target.HostWith(creds), Creds: creds, Jobs: runner, PinDir: dir}).Register(rig.reg)
	if d := rig.run(t, "mysql.dumpFrom", dumpParams(master, "toko")); !d.OK {
		t.Fatalf("dumpFrom before any source pin = %+v", d)
	}
	want := "127.0.0.1:" + strconv.Itoa(master.Port)
	if got := pinFile(t, dir, mysql.SourceFile); got != want {
		t.Fatalf("source pin after dumpFrom = %q, want %q", got, want)
	}

	r := &pinRig{t: t, reg: rig.reg}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(master)))
	other := sourceParams(master)
	other.Host = "10.0.0.2"
	wantRefused(t, "configureReplication to another host", r.dispatch("mysql.configureReplication", other),
		want, "sudo dbpanel agent set-source")
	otherPort := sourceParams(master)
	otherPort.Port++
	wantRefused(t, "configureReplication to another port", r.dispatch("mysql.configureReplication", otherPort),
		"sudo dbpanel agent set-source")
	dp := dumpParams(master, "toko")
	dp.Host = "10.0.0.2"
	wantRefused(t, "dumpFrom another host", r.dispatch("mysql.dumpFrom", dp), "sudo dbpanel agent set-source")
	cp := cloneParams(master)
	cp.Host = "10.0.0.2"
	wantRefused(t, "cloneFrom another host", r.dispatch("mysql.cloneFrom", cp), "sudo dbpanel agent set-source")
	if got := pinFile(t, dir, mysql.SourceFile); got != want {
		t.Errorf("source pin = %q, want %q unchanged", got, want)
	}
}

// TestReleaseRoleRequiresNoReplication: mysql.releaseRole is refused while
// a replication source is configured or replicas read this server's
// binlog, and once replication is reset it unpins the role and keeps the
// source pin (spec 5.10).
func TestReleaseRoleRequiresNoReplication(t *testing.T) {
	master, replica := sourceWithReplica(t)
	src := "127.0.0.1:" + strconv.Itoa(master.Port)
	dir := pinDir(t, "replica", src)
	r := newPinRig(t, replica.Host(), dir)
	wantRefused(t, "releaseRole while replicating", r.dispatch("mysql.releaseRole", nil), "replikasi masih dikonfigurasi")
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "replica" || s != src {
		t.Fatalf("pins after a refused release = %q, %q", role, s)
	}

	// A server replicas read from keeps its pins too, even unassigned.
	mdir := pinDir(t, "", "10.0.0.9:3306")
	wantRefused(t, "releaseRole on a source", newPinRig(t, master.Host(), mdir).dispatch("mysql.releaseRole", nil),
		"replika sedang membaca binlog server ini")
	if s := pinFile(t, mdir, mysql.SourceFile); s != "10.0.0.9:3306" {
		t.Errorf("source pin after a refused release = %q", s)
	}

	decode[*proto.ReplicaStatus](t, r.dispatch("mysql.stopReplica", nil))
	if resp := r.dispatch("mysql.resetReplica", nil); !resp.OK {
		t.Fatalf("resetReplica = %+v", resp.Error)
	}
	if resp := r.dispatch("mysql.releaseRole", nil); !resp.OK {
		t.Fatalf("releaseRole after reset = %+v", resp.Error)
	}
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "unassigned" || s != src {
		t.Errorf("pins after release = %q, %q; want unassigned, %q", role, s, src)
	}
	// Released, the server takes the master role from the panel again.
	if c := r.d.Pins(); c.Role() != "unassigned" {
		t.Errorf("role after release = %q", c.Role())
	}
	// Nothing pinned: releaseRole is a no-op that needs no MySQL.
	if resp := newPinRig(t, nil, pinDir(t, "", "")).dispatch("mysql.releaseRole", nil); !resp.OK {
		t.Errorf("releaseRole with nothing pinned = %+v", resp.Error)
	}
}

// TestReleaseRoleKeepsSourcePin: a released replica is unassigned but
// still follows only its pinned source: replication to another host is
// refused with the pinned source and the set-source command.
func TestReleaseRoleKeepsSourcePin(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	const src = "10.0.0.1:3306"
	dir := pinDir(t, "replica", src)
	r := newPinRig(t, inst.Host(), dir)
	if resp := r.dispatch("mysql.releaseRole", nil); !resp.OK {
		t.Fatalf("releaseRole = %+v", resp.Error)
	}
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "unassigned" || s != src {
		t.Fatalf("pins after release = %q, %q; want unassigned, %q", role, s, src)
	}
	p := r.d.Pins()
	if p.Role() != "unassigned" {
		t.Errorf("Role() = %q, want unassigned", p.Role())
	}
	if host, port, ok := p.Source(); !ok || host != "10.0.0.1" || port != 3306 {
		t.Errorf("Source() = %q, %d, %v; want 10.0.0.1, 3306, true", host, port, ok)
	}
	other := sourceParams(inst)
	other.Host, other.Port = "10.0.0.2", 3306
	wantRefused(t, "configureReplication to another host after release", r.dispatch("mysql.configureReplication", other),
		"server ini dipin mengikuti sumber "+src, "sudo dbpanel agent set-source <host:port>")
	// Released again: still a no-op on the source pin.
	if resp := r.dispatch("mysql.releaseRole", nil); !resp.OK {
		t.Fatalf("second releaseRole = %+v", resp.Error)
	}
	if s := pinFile(t, dir, mysql.SourceFile); s != src {
		t.Errorf("source pin after a second release = %q, want %q", s, src)
	}
}

// TestReleaseRoleRefusedOnMaster: a master pin is never released by a
// panel command, nor by Pins.Release.
func TestReleaseRoleRefusedOnMaster(t *testing.T) {
	dir := pinDir(t, "master", "10.0.0.1:3306")
	wantRefused(t, "releaseRole", newPinRig(t, nil, dir).dispatch("mysql.releaseRole", nil),
		"server ini dipin sebagai master", "sudo dbpanel agent set-role")
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(); !errors.Is(err, mysql.ErrRolePinned) {
		t.Errorf("Release on a master = %v, want ErrRolePinned", err)
	}
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "master" || src != "10.0.0.1:3306" {
		t.Errorf("pins after refused releases = %q, %q", role, src)
	}
}

// TestBootstrapFromHintAndState: an agent upgraded on a configured server
// pins its role once, from the panel's hint confirmed by MySQL's state.
func TestBootstrapFromHintAndState(t *testing.T) {
	replicaSnap := proto.HealthSnapshot{MySQLOK: true, LogBin: true, Replica: &proto.ReplicaStatus{SourceHost: "10.0.0.1"}}
	masterSnap := proto.HealthSnapshot{MySQLOK: true, LogBin: true, BinlogDumpThreads: 1}
	for _, c := range []struct {
		name, hint string
		snap       proto.HealthSnapshot
		want       string
	}{
		{"no hint", "", replicaSnap, ""},
		{"replica hint, no replication", "replica", masterSnap, ""},
		{"replica hint, MySQL down", "replica", proto.HealthSnapshot{Replica: replicaSnap.Replica}, ""},
		{"master hint, no binlog", "master", proto.HealthSnapshot{MySQLOK: true}, ""},
		// A compromised panel cannot get a logging replica pinned master.
		{"master hint on a replica", "master", replicaSnap, ""},
		{"replica", "replica", replicaSnap, "replica"},
		{"master", "master", masterSnap, "master"},
	} {
		dir := pinDir(t, "", "")
		p, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Bootstrap(c.hint, c.snap); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := pinFile(t, dir, mysql.RoleFile); got != c.want {
			t.Errorf("%s: role pin = %q, want %q", c.name, got, c.want)
		}
		if c.want == "" {
			continue
		}
		// At most once: a later, different reading changes nothing.
		other, otherSnap := "master", masterSnap
		if c.want == "master" {
			other, otherSnap = "replica", replicaSnap
		}
		if err := p.Bootstrap(other, otherSnap); err != nil {
			t.Fatal(err)
		}
		if got := pinFile(t, dir, mysql.RoleFile); got != c.want {
			t.Errorf("%s: role pin after a second bootstrap = %q", c.name, got)
		}
	}

	// A role file the agent has seen (here: written by the CLI, then
	// removed) is never recreated by Bootstrap.
	dir := pinDir(t, "replica", "")
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, mysql.RoleFile)); err != nil {
		t.Fatal(err)
	}
	if err := p.Bootstrap("master", masterSnap); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "" {
		t.Errorf("role pin recreated by Bootstrap: %q", got)
	}

	// Real readings: the replica's snapshot pins replica, the master's
	// pins master.
	master, replica := sourceWithReplica(t)
	for _, c := range []struct {
		inst *testmysql.Instance
		hint string
	}{{replica, "replica"}, {master, "master"}} {
		creds := &memCreds{p: proto.ConnectParams{User: "root"}}
		d := &mysql.Driver{Host: c.inst.HostWith(creds), Creds: creds}
		snap := d.Snapshot(context.Background())
		if !snap.MySQLOK {
			t.Fatalf("%s snapshot: %s", c.hint, snap.MySQLError)
		}
		dir := pinDir(t, "", "")
		p, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Bootstrap(c.hint, snap); err != nil {
			t.Fatal(err)
		}
		if got := pinFile(t, dir, mysql.RoleFile); got != c.hint {
			t.Errorf("role pin from the %s's snapshot = %q", c.hint, got)
		}
	}
}

// TestPinsReloadAfterCLIWrite: a running driver sees a pin the CLI wrote on
// its next command, and refuses guarded commands while a pin file is not
// trustworthy.
func TestPinsReloadAfterCLIWrite(t *testing.T) {
	dir := pinDir(t, "master", "")
	r := newPinRig(t, nil, dir)
	drop := proto.DropDatabasesParams{Databases: []string{"toko"}}
	wantRefused(t, "dropDatabases on master", r.dispatch("mysql.dropDatabases", drop), "dipin sebagai master")

	cli, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.SetRole("replica"); err != nil {
		t.Fatal(err)
	}
	wantPastPins(t, "dropDatabases after set-role replica", r.dispatch("mysql.dropDatabases", drop))
	wantRefused(t, "setReadOnly off after set-role replica",
		r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}), "dipin sebagai replica")

	if err := cli.SetSource("10.0.0.1", 3306); err != nil {
		t.Fatal(err)
	}
	if host, port, ok := r.d.Pins().Source(); !ok || host != "10.0.0.1" || port != 3306 {
		t.Errorf("Source() = %q, %d, %v", host, port, ok)
	}
	wantRefused(t, "dumpFrom another source", r.dispatch("mysql.dumpFrom",
		proto.SnapshotParams{Host: "10.0.0.2", Port: 3306, User: "dbpanel_repl", Password: "x"}), "set-source")

	// Writable by others: nothing guarded runs until it is fixed.
	path := filepath.Join(dir, mysql.RoleFile)
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "dropDatabases with a group-writable pin", r.dispatch("mysql.dropDatabases", drop), "chmod 0644")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	wantPastPins(t, "dropDatabases after chmod", r.dispatch("mysql.dropDatabases", drop))

	if err := os.WriteFile(path, []byte("raja\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "dropDatabases with an unknown role", r.dispatch("mysql.dropDatabases", drop), "tidak valid")

	if err := cli.SetRole("unassigned"); err != nil {
		t.Fatal(err)
	}
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "" || src != "" {
		t.Errorf("pins after set-role unassigned = %q, %q; want none", role, src)
	}
	if got := r.d.Pins().Role(); got != "unassigned" {
		t.Errorf("Role() = %q, want unassigned", got)
	}
	if err := cli.SetRole("raja"); err == nil {
		t.Error("SetRole accepted an unknown role")
	}
	if err := cli.SetSource("a b", 3306); err == nil {
		t.Error("SetSource accepted a host with a space")
	}
}

// TestClaimsFailOnMismatch: a claim never changes a pin and reports a
// different one as an error (two first-time commands racing with different
// values); claiming the pinned value again is a no-op.
func TestClaimsFailOnMismatch(t *testing.T) {
	dir := pinDir(t, "replica", "10.0.0.1:3306")
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ClaimSource("10.0.0.2", 3306); !errors.Is(err, mysql.ErrSourcePinned) {
		t.Errorf("claimSource of another host = %v, want ErrSourcePinned", err)
	}
	if err := p.ClaimSource("10.0.0.1", 3307); !errors.Is(err, mysql.ErrSourcePinned) {
		t.Errorf("claimSource of another port = %v, want ErrSourcePinned", err)
	}
	if err := p.ClaimSource("10.0.0.1", 3306); err != nil {
		t.Errorf("claimSource of the pinned source = %v", err)
	}
	if err := p.ClaimRole("master"); !errors.Is(err, mysql.ErrRolePinned) {
		t.Errorf("claimRole master on a replica = %v, want ErrRolePinned", err)
	}
	if err := p.ClaimRole("replica"); err != nil {
		t.Errorf("claimRole of the pinned role = %v", err)
	}
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "replica" || src != "10.0.0.1:3306" {
		t.Errorf("pins after claims = %q, %q", role, src)
	}
}

// TestAllowFailsClosedOnUnexpectedParams: a parameter type Allow does not
// expect for a command it checks is an error, never an allowance, with or
// without a source pin.
func TestAllowFailsClosedOnUnexpectedParams(t *testing.T) {
	for _, dir := range []string{pinDir(t, "replica", "10.0.0.1:3306"), pinDir(t, "", "")} {
		p, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		snap, src := validSnapshot, validSource
		for _, c := range []pinCase{
			{"mysql.dumpFrom", &snap},
			{"mysql.cloneFrom", validSource},
			{"mysql.configureReplication", &src},
			{"mysql.configureReplication", validSnapshot},
			{"mysql.dropUser", &proto.DropUserParams{User: "dbpanel_dump_1"}},
			{"mysql.setReadOnly", &proto.ReadOnlyParams{On: true}},
			{"mysql.configure", nil},
		} {
			if err := p.Allow(c.cmd, c.params); err == nil {
				t.Errorf("%s: Allow(%s, %T) = nil, want an error", dir, c.cmd, c.params)
			}
		}
	}
}

// promoteCommands are the commands of a promotion (Plan 4b) a quarantined
// server refuses whatever their parameters.
var promoteCommands = []string{"mysql.lockForPromote", "mysql.promote", "mysql.repoint"}

// TestQuarantinedRefusalTable: a quarantined server (the old master after
// a promotion) refuses what a replica refuses plus the promotion commands;
// its rebuild path is allowed only toward the pinned source, and it can be
// released like a replica.
func TestQuarantinedRefusalTable(t *testing.T) {
	const src = "10.0.0.1:3306"
	dir := pinDir(t, "quarantined", src)
	r := newPinRig(t, nil, dir)
	for _, c := range append(replicaRefused, replUserRefused...) {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params),
			"server ini dipin sebagai quarantined", "perintah "+c.cmd+" ditolak", "sudo dbpanel agent set-role <peran>")
	}
	p := r.d.Pins()
	if got := p.Role(); got != "quarantined" {
		t.Fatalf("Role() = %q, want quarantined", got)
	}
	for _, cmd := range promoteCommands {
		if err := p.Allow(cmd, nil); !errors.Is(err, mysql.ErrRolePinned) {
			t.Errorf("Allow(%s) on quarantined = %v, want ErrRolePinned", cmd, err)
		}
	}
	// Elsewhere these commands are not the role table's business, but
	// mysql.promote (TestPromotePinRules) and mysql.repoint
	// (TestRepointPinRules), which only a replica takes.
	for _, role := range []string{"master", "replica", ""} {
		q, err := mysql.LoadPins(pinDir(t, role, ""))
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range promoteCommands {
			if cmd == "mysql.promote" || cmd == "mysql.repoint" {
				continue
			}
			if err := q.Allow(cmd, nil); err != nil {
				t.Errorf("Allow(%s) on %q = %v", cmd, role, err)
			}
		}
	}

	// The rebuild path toward the pinned source.
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
	// ... and nowhere else.
	other := validSnapshot
	other.Host = "10.0.0.2"
	wantRefused(t, "dumpFrom another host", r.dispatch("mysql.dumpFrom", other), "server ini dipin mengikuti sumber "+src)
	wantRefused(t, "cloneFrom another host", r.dispatch("mysql.cloneFrom", other), "sudo dbpanel agent set-source")
	otherSrc := validSource
	otherSrc.Port = 3307
	wantRefused(t, "configureReplication to another port", r.dispatch("mysql.configureReplication", otherSrc), "set-source")

	// configure{replica} on a quarantined server keeps the pin.
	if err := p.ClaimRole("replica"); err != nil {
		t.Errorf("claimRole replica on quarantined = %v", err)
	}
	if err := p.ClaimRole("master"); !errors.Is(err, mysql.ErrRolePinned) {
		t.Errorf("claimRole master on quarantined = %v, want ErrRolePinned", err)
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "quarantined" {
		t.Errorf("role pin after claims = %q, want quarantined", got)
	}

	// Released like a replica: unassigned, source kept.
	if err := p.Release(); err != nil {
		t.Fatalf("Release on quarantined = %v", err)
	}
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "unassigned" || s != src {
		t.Errorf("pins after release = %q, %q; want unassigned, %q", role, s, src)
	}
}

// TestMasterRefusesUnlockWhileLocked: while the promote lock exists a
// pinned master is not made writable; locking it again stays allowed, and
// without the lock "Buka kunci master" gets past the pins again.
func TestMasterRefusesUnlockWhileLocked(t *testing.T) {
	dir := pinDir(t, "master", "")
	r := newPinRig(t, nil, dir)
	p := r.d.Pins()
	off, on := proto.ReadOnlyParams{On: false}, proto.ReadOnlyParams{On: true}
	wantPastPins(t, "setReadOnly off without a lock", r.dispatch("mysql.setReadOnly", off))

	if err := p.WriteLock(mysql.PromoteLock{Candidate: "srv-2", Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "setReadOnly off while locked", r.dispatch("mysql.setReadOnly", off),
		"dikunci untuk promosi", "sudo dbpanel agent unlock-master")
	if err := p.Allow("mysql.setReadOnly", off); !errors.Is(err, mysql.ErrRolePinned) {
		t.Errorf("Allow(setReadOnly off) while locked = %v, want ErrRolePinned", err)
	}
	// Locking stays the master table's: setReadOnly on is refused as before.
	wantRefused(t, "setReadOnly on", r.dispatch("mysql.setReadOnly", on), "server ini dipin sebagai master")

	// A lock file the agent cannot trust also refuses the unlock.
	lock := filepath.Join(dir, mysql.LockFile)
	if err := os.Chmod(lock, 0o666); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "setReadOnly off with an untrusted lock", r.dispatch("mysql.setReadOnly", off), "chmod 0644")
	if err := os.Chmod(lock, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.ClearLock(); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearLock(); err != nil {
		t.Errorf("second ClearLock = %v, want nil", err)
	}
	wantPastPins(t, "setReadOnly off after ClearLock", r.dispatch("mysql.setReadOnly", off))
}

// mustPins loads the pins in dir.
func mustPins(t *testing.T, dir string) *mysql.Pins {
	t.Helper()
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBootstrapKeepsQuarantined: a quarantined server whose MySQL is
// writable again (and logs, with no replication source: what Bootstrap
// would pin master) stays quarantined, also in a fresh process and with a
// promotion record present.
func TestBootstrapKeepsQuarantined(t *testing.T) {
	rw := proto.HealthSnapshot{MySQLOK: true, LogBin: true, BinlogDumpThreads: 1}
	dir := pinDir(t, "quarantined", "10.0.0.2:3306")
	p := mustPins(t, dir)
	if err := p.WritePromoted(mysql.Promoted{At: time.Now(), Mode: "planned", OldSource: "10.0.0.1:3306",
		GTIDExecuted: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5"}); err != nil {
		t.Fatal(err)
	}
	for _, hint := range []string{"master", "replica", ""} {
		if err := mustPins(t, dir).Bootstrap(hint, rw); err != nil {
			t.Fatalf("Bootstrap(%q) = %v", hint, err)
		}
		if err := p.Bootstrap(hint, rw); err != nil {
			t.Fatalf("Bootstrap(%q) = %v", hint, err)
		}
		if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "quarantined" || src != "10.0.0.2:3306" {
			t.Errorf("hint %q: pins = %q, %q; want quarantined, 10.0.0.2:3306", hint, role, src)
		}
	}
}

// TestSetQuarantinedAndPromoteToMaster: the two transitions of a promotion
// write both pins, and a running driver sees them on its next command.
func TestSetQuarantinedAndPromoteToMaster(t *testing.T) {
	dir := pinDir(t, "master", "")
	r := newPinRig(t, nil, dir)
	p := r.d.Pins()
	cli := mustPins(t, dir)
	if err := cli.SetQuarantined("10.0.0.2:3306"); err != nil {
		t.Fatal(err)
	}
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "quarantined" || src != "10.0.0.2:3306" {
		t.Errorf("pins after SetQuarantined = %q, %q", role, src)
	}
	if got := p.Role(); got != "quarantined" {
		t.Errorf("driver's Role() after SetQuarantined = %q", got)
	}
	wantRefused(t, "setReadOnly off on quarantined", r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}),
		"dipin sebagai quarantined")
	if err := cli.SetQuarantined("bukan sumber"); err == nil {
		t.Error("SetQuarantined accepted a bad source")
	}
	for _, fi := range []string{mysql.RoleFile, mysql.SourceFile} {
		if st, err := os.Stat(filepath.Join(dir, fi)); err != nil || st.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, %v; want mode 0644", fi, st, err)
		}
	}

	if err := p.PromoteToMaster(); err != nil {
		t.Fatal(err)
	}
	if role, src := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "master" || src != "" {
		t.Errorf("pins after PromoteToMaster = %q, %q; want master and no source", role, src)
	}
	if _, _, ok := p.Source(); ok || p.Role() != "master" {
		t.Errorf("after PromoteToMaster: Role %q, source pinned %v", p.Role(), ok)
	}
	// From a replica too, with its source pin.
	rdir := pinDir(t, "replica", "10.0.0.1:3306")
	if err := mustPins(t, rdir).SetQuarantined("10.0.0.3:3306"); err != nil {
		t.Fatal(err)
	}
	if role, src := pinFile(t, rdir, mysql.RoleFile), pinFile(t, rdir, mysql.SourceFile); role != "quarantined" || src != "10.0.0.3:3306" {
		t.Errorf("replica pins after SetQuarantined = %q, %q", role, src)
	}
}

// TestQuarantinedWithoutSourceFailsClosed: a quarantined server with no
// source pin refuses dropDatabases and every command that names a source,
// whatever host it names; SetQuarantined writes the source before the
// role, so an interrupted quarantine never leaves this state.
func TestQuarantinedWithoutSourceFailsClosed(t *testing.T) {
	dir := pinDir(t, "quarantined", "")
	r := newPinRig(t, nil, dir)
	for _, c := range []pinCase{
		{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}},
		{"mysql.dumpFrom", validSnapshot},
		{"mysql.cloneFrom", validSnapshot},
		{"mysql.configureReplication", validSource},
	} {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params), "quarantined tanpa sumber yang dipin",
			"perintah "+c.cmd+" ditolak", "sudo dbpanel agent set-source")
	}
	if err := r.d.Pins().Allow("mysql.dumpFrom", validSnapshot); !errors.Is(err, mysql.ErrSourcePinned) {
		t.Errorf("Allow(dumpFrom) = %v, want ErrSourcePinned", err)
	}
	// Commands that touch no data and name no source are the table's.
	wantPastPins(t, "stopReplica", r.dispatch("mysql.stopReplica", nil))
	// Once root pins the new master, the rebuild toward it is allowed.
	if err := mustPins(t, dir).SetSource("10.0.0.1", 3306); err != nil {
		t.Fatal(err)
	}
	wantPastPins(t, "dumpFrom the pinned source", r.dispatch("mysql.dumpFrom", validSnapshot))
	wantPastPins(t, "dropDatabases with a source pin", r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"toko"}}))

	// The source is written before the role: when the role cannot be
	// written (here a directory blocks the rename), the source pin is
	// already there.
	bdir := pinDir(t, "", "")
	if err := os.MkdirAll(filepath.Join(bdir, mysql.RoleFile, "blok"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := mysql.LoadPins(bdir)
	if err := p.SetQuarantined("10.0.0.2:3306"); err == nil {
		t.Fatal("SetQuarantined succeeded over a directory")
	}
	if src := pinFile(t, bdir, mysql.SourceFile); src != "10.0.0.2:3306" {
		t.Errorf("source pin after a failed role write = %q, want 10.0.0.2:3306", src)
	}
}

// TestQuarantinedRebuiltPinsReplica: replication configured toward the
// pinned source turns a quarantined pin into replica (spec 5.1); toward
// any other source, without a source pin, or on any other role, nothing
// changes.
func TestQuarantinedRebuiltPinsReplica(t *testing.T) {
	const src = "db-baru.example:3306"
	for _, c := range []struct {
		role, source, host string
		port               int
		want               string
	}{
		{"quarantined", src, "db-baru.example", 3306, "replica"},
		{"quarantined", src, "DB-BARU.example", 3306, "replica"}, // matched like Allow: by name, any case
		{"quarantined", src, "10.0.0.9", 3306, "quarantined"},
		{"quarantined", src, "db-baru.example", 3307, "quarantined"},
		{"quarantined", "", "db-baru.example", 3306, "quarantined"},
		{"master", src, "db-baru.example", 3306, "master"},
		{"replica", src, "db-baru.example", 3306, "replica"},
		{"unassigned", src, "db-baru.example", 3306, "unassigned"},
	} {
		dir := pinDir(t, c.role, c.source)
		if c.source != "" {
			addr := filepath.Join(dir, mysql.SourceAddrFile)
			if err := os.WriteFile(addr, []byte("10.0.0.2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p := mustPins(t, dir)
		settled, err := p.SettleRebuilt(c.host, c.port)
		if err != nil {
			t.Fatalf("%s/%s toward %s:%d: %v", c.role, c.source, c.host, c.port, err)
		}
		what := c.role + " toward " + c.host + ":" + strconv.Itoa(c.port)
		if got := pinFile(t, dir, mysql.RoleFile); got != c.want || p.Role() != c.want || settled != (c.role != c.want) {
			t.Errorf("%s: role pin %q, Role() %q, settled %v; want %q", what, got, p.Role(), settled, c.want)
		}
		if got := pinFile(t, dir, mysql.SourceFile); got != c.source {
			t.Errorf("%s: source pin %q, want %q unchanged", what, got, c.source)
		}
		if c.want == "replica" && c.role == "quarantined" {
			// The verified address of the same source stays for the
			// agent's own reads; the promotion commands are no longer
			// refused by the role, the old master's commands are.
			if _, _, dial, ok, err := p.SourceDial(); err != nil || !ok || dial != "10.0.0.2" {
				t.Errorf("%s: SourceDial = %q, %v, %v", what, dial, ok, err)
			}
			if err := p.Allow("mysql.lockForPromote", nil); err != nil {
				t.Errorf("%s: lockForPromote after the rebuild = %v", what, err)
			}
			for _, cmd := range []string{"mysql.leftover", "mysql.salvage"} {
				if err := p.Allow(cmd, nil); !errors.Is(err, mysql.ErrRolePinned) {
					t.Errorf("%s: %s after the rebuild = %v, want ErrRolePinned", what, cmd, err)
				}
			}
			// The files already salvaged stay reachable.
			for _, cmd := range salvageFileCommands {
				if err := p.Allow(cmd, nil); err != nil {
					t.Errorf("%s: %s after the rebuild = %v", what, cmd, err)
				}
			}
		}
	}
}

// salvageFileCommands reach the salvage files already written.
var salvageFileCommands = []string{"mysql.salvageList", "mysql.salvageRead", "mysql.salvageDelete"}

// TestSalvageFileCommandsByRole: the salvage files are listed, read and
// deleted on a quarantined server and on a replica (a rebuilt old master
// keeps its files), never on a master or an unassigned server; leftover
// and salvage (which read the binlog) stay quarantined-only.
func TestSalvageFileCommandsByRole(t *testing.T) {
	for role, allowed := range map[string]bool{"quarantined": true, "replica": true, "master": false, "": false} {
		p, err := mysql.LoadPins(pinDir(t, role, "10.0.0.1:3306"))
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range salvageFileCommands {
			if err := p.Allow(cmd, nil); (err == nil) != allowed {
				t.Errorf("Allow(%s) on %q = %v, want allowed %v", cmd, role, err, allowed)
			}
		}
		for _, cmd := range []string{"mysql.leftover", "mysql.salvage"} {
			if err := p.Allow(cmd, nil); (err == nil) != (role == "quarantined") {
				t.Errorf("Allow(%s) on %q = %v", cmd, role, err)
			}
		}
	}
}

// TestConfigureReplicationEndsQuarantine: on a quarantined old master,
// mysql.configureReplication toward another host is refused as before; a
// successful one toward the pinned source (the new master) pins replica
// and logs it, once a load from that source succeeded in this agent run. A master refuses configureReplication and keeps its pin.
func TestConfigureReplicationEndsQuarantine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	const src = "10.0.0.1:3306" // validSource's
	dir := pinDir(t, "quarantined", src)
	var logBuf bytes.Buffer
	reg := commands.NewRegistry()
	d := &mysql.Driver{Host: inst.Host(), PinDir: dir, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}
	d.Register(reg)
	r := &pinRig{t: t, reg: reg, d: d}

	other := validSource
	other.Host = "10.0.0.2"
	wantRefused(t, "configureReplication to another host", r.dispatch("mysql.configureReplication", other),
		"server ini dipin mengikuti sumber "+src)
	if got := pinFile(t, dir, mysql.RoleFile); got != "quarantined" {
		t.Fatalf("role pin after a refused configureReplication = %q", got)
	}

	// Without a load from the pinned source in this agent run the pin
	// stays quarantined, and the agent says why.
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", validSource))
	if role := pinFile(t, dir, mysql.RoleFile); role != "quarantined" {
		t.Fatalf("role pin after configureReplication without a load = %q, want quarantined", role)
	}
	if out := logBuf.String(); !strings.Contains(out, "quarantine kept") || strings.Contains(out, `msg="role pinned"`) {
		t.Errorf("agent log after configureReplication without a load:\n%s", out)
	}

	if !d.MarkRebuildLoaded("10.0.0.1", 3306) {
		t.Fatal("no load mark toward the pinned source")
	}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", validSource))
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "replica" || s != src {
		t.Fatalf("pins after configureReplication toward the pinned source = %q, %q; want replica, %s", role, s, src)
	}
	if out := logBuf.String(); !strings.Contains(out, `msg="role pinned" role=replica`) {
		t.Errorf("agent log does not show the replica pin:\n%s", out)
	}
	// The replica's rules now: the quarantined server's commands are
	// refused, and another source still is.
	wantRefused(t, "leftover on the rebuilt server", r.dispatch("mysql.leftover", proto.LeftoverParams{NewMaster: proto.PeerCred{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "rahasia"}}),
		"server ini dipin sebagai replica")
	wantRefused(t, "configureReplication to another host after the rebuild", r.dispatch("mysql.configureReplication", other),
		"server ini dipin mengikuti sumber "+src)

	// A master never takes this path.
	mdir := pinDir(t, "master", "")
	m := newPinRig(t, inst.Host(), mdir)
	wantRefused(t, "configureReplication on a master", m.dispatch("mysql.configureReplication", validSource),
		"server ini dipin sebagai master")
	if got := pinFile(t, mdir, mysql.RoleFile); got != "master" {
		t.Errorf("master's role pin = %q", got)
	}
}
