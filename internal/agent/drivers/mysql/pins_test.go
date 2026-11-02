package mysql_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	{"mysql.configure", proto.ConfigureParams{Role: "replica"}},
	{"mysql.setReadOnly", proto.ReadOnlyParams{On: true}},
	{"mysql.releaseRole", nil},
}

// replicaRefused are the commands spec 5.5 refuses on a replica.
var replicaRefused = []pinCase{
	{"mysql.setReadOnly", proto.ReadOnlyParams{On: false}},
	{"mysql.configure", proto.ConfigureParams{Role: "master"}},
	{"mysql.createReplUser", proto.ReplUserParams{User: "dbpanel_repl", Password: "rahasia"}},
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
		pinCase{"mysql.stopReplica", nil}) {
		wantPastPins(t, c.cmd, r.dispatch(c.cmd, c.params))
	}
	if got := pinFile(t, dir, mysql.RoleFile); got != "master" {
		t.Errorf("role pin = %q, want master", got)
	}
}

// TestReplicaRefusesUnlockAndCreateReplUser: a replica refuses to be made
// writable, made a master or given a replication account; the commands a
// replica's flows use get past the pins.
func TestReplicaRefusesUnlockAndCreateReplUser(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "replica", ""))
	for _, c := range replicaRefused {
		wantRefused(t, c.cmd, r.dispatch(c.cmd, c.params),
			"server ini dipin sebagai replica", "perintah "+c.cmd+" ditolak", "sudo dbpanel agent set-role <peran>")
	}
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
// binlog, and once replication is reset it removes both pins.
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
	for _, name := range []string{mysql.RoleFile, mysql.SourceFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after release: %v, want removed", name, err)
		}
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
