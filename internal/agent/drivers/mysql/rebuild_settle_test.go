package mysql_test

import (
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// settleRig is a quarantined old master (target) pinned to master as its
// source, with root's approval for the rebuild and the old master's own
// databases still on it.
type settleRig struct {
	*pinRig
	snap   *snapshotRig
	creds  *memCreds
	master *testmysql.Instance
	target *testmysql.Instance
	dir    string
	src    string
}

func newSettleRig(t *testing.T, oldDatabases ...string) *settleRig {
	t.Helper()
	master := testmysql.Start(t, testmysql.Options{})
	target := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	dumpUser(t, mdb)
	replUser(t, master)
	execAll(t, mdb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)", "INSERT INTO toko.t VALUES (1)",
		"CREATE DATABASE kecil", "CREATE TABLE kecil.t (id INT PRIMARY KEY)", "INSERT INTO kecil.t VALUES (1)")
	tdb := rootDB(t, target)
	for _, name := range oldDatabases {
		execAll(t, tdb, "CREATE DATABASE "+name, "CREATE TABLE "+name+".lama (id INT PRIMARY KEY)",
			"INSERT INTO "+name+".lama VALUES (7)")
	}
	execAll(t, tdb, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")

	src := "127.0.0.1:" + strconv.Itoa(master.Port)
	dir := pinDir(t, "quarantined", src)
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	jev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(jev, slog.New(slog.DiscardHandler))
	snap := &snapshotRig{reg: commands.NewRegistry(), runner: runner, events: jev}
	d := &mysql.Driver{Host: target.HostWith(creds), Creds: creds, Jobs: runner, PinDir: dir}
	d.Register(snap.reg)
	approveRebuild(t, dir, src)
	return &settleRig{pinRig: &pinRig{t: t, reg: snap.reg, d: d}, snap: snap, creds: creds, master: master, target: target, dir: dir, src: src}
}

// TestRebuildPartialDumpKeepsQuarantine: the auditor's sequence. Under one
// approval the panel loads only one small database from the pinned source
// and configures replication toward it; the old master's other databases
// are still there, so the quarantine is not ended: the role stays
// quarantined, the approval stays, and the command answers A6 naming the
// databases that were not copied.
func TestRebuildPartialDumpKeepsQuarantine(t *testing.T) {
	r := newSettleRig(t, "rs_lama", "kecil")
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil")); !d.OK {
		t.Fatalf("dumpFrom{kecil} = %+v", d)
	}
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master))
	if resp.OK || resp.Error == nil {
		t.Fatalf("configureReplication with old databases left = %+v, want an error", resp)
	}
	want := "server ini masih punya database yang tidak disalin dari sumber " + r.src + " (kecil, rs_lama); " +
		"karantina tidak diakhiri. Selamatkan atau hapus database itu lewat panel, lalu ulangi Bangun ulang sebagai replika."
	if resp.Error.Message != want {
		t.Errorf("message = %q\nwant      %q", resp.Error.Message, want)
	}
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Errorf("role pin = %q, want quarantined", role)
	}
	if got := pinFile(t, r.dir, mysql.RebuildApprovalFile); got == "" {
		t.Error("the approval was cleared by a refused settle")
	}
	// The refusal holds: a second try answers the same.
	if resp := r.dispatch("mysql.configureReplication", sourceParams(r.master)); resp.OK {
		t.Error("second configureReplication settled")
	}
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Errorf("role pin after the second try = %q, want quarantined", role)
	}
}

// TestRebuildFullDumpSettles: the legitimate flow without a filter. The
// panel drops the old master's databases (mysql.dropDatabases, after the
// wipe approval) and loads every database: the quarantine ends.
func TestRebuildFullDumpSettles(t *testing.T) {
	r := newSettleRig(t, "toko", "kecil")
	decode[proto.DropDatabasesResult](t, r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"kecil", "toko"}}))
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master)); !d.OK {
		t.Fatalf("dumpFrom{all} = %+v", d)
	}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master)))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after a full rebuild = %q, want replica", role)
	}
	if got := pinFile(t, r.dir, mysql.RebuildApprovalFile); got != "" {
		t.Errorf("approval after the settle = %q, want removed", got)
	}
}

// TestRebuildFilteredDumpSettles: the legitimate flow with a replication
// filter. Every leftover is dropped (also those not selected now), only
// the selected database is loaded, and replication follows that database:
// the quarantine ends.
func TestRebuildFilteredDumpSettles(t *testing.T) {
	r := newSettleRig(t, "toko", "kecil")
	decode[proto.DropDatabasesResult](t, r.dispatch("mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"kecil", "toko"}}))
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "toko")); !d.OK {
		t.Fatalf("dumpFrom{toko} = %+v", d)
	}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master, "toko")))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after a filtered rebuild = %q, want replica", role)
	}
}

// TestRebuildDumpOverOldDatabaseKeepsQuarantine: a dump of a database the
// old master still had (not dropped first) leaves that database's own
// tables in place, so it does not count as copied.
func TestRebuildDumpOverOldDatabaseKeepsQuarantine(t *testing.T) {
	r := newSettleRig(t, "toko")
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "toko")); !d.OK {
		t.Fatalf("dumpFrom{toko} = %+v", d)
	}
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master, "toko"))
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "("+"toko"+"); karantina tidak diakhiri") {
		t.Fatalf("configureReplication = %+v, want A6 naming toko", resp)
	}
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Errorf("role pin = %q, want quarantined", role)
	}
}
