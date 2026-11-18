package mysql_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	return newSettleRigHooked(t, nil, oldDatabases...)
}

// newSettleRigHooked is newSettleRig with the driver's host going through
// hook (nil: none), which wraps the target's host.
func newSettleRigHooked(t *testing.T, hook *hookHost, oldDatabases ...string) *settleRig {
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
	var h mysql.Host = target.HostWith(creds)
	if hook != nil {
		hook.Host = h
		h = hook
	}
	d := &mysql.Driver{Host: h, Creds: creds, Jobs: runner, PinDir: dir}
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
	want := "server ini masih punya database yang tidak disalin dari sumber " + r.src + "; karantina tidak diakhiri. " +
		"Bila database itu tidak ada di master baru: jalankan di server ini sudo dbpanel agent drop-database <nama> " +
		"untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel. Bila ada di master baru: mulai " +
		"lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang. Database itu: kecil, rs_lama."
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

// TestSettleRemovesLeftoverSet (SEC-201): the leftover set file stays
// while the quarantine does (a refused settle keeps it) and is removed when
// the settle ends the quarantine.
func TestSettleRemovesLeftoverSet(t *testing.T) {
	r := newSettleRig(t)
	r.d.SalvageDir = filepath.Join(t.TempDir(), "salvage")
	if err := os.MkdirAll(r.d.SalvageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.d.SalvageDir, mysql.LeftoverSetFile)
	if err := os.WriteFile(path, []byte("source "+r.src+"\n3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No load yet: the settle is refused and the file stays.
	if resp := r.dispatch("mysql.configureReplication", sourceParams(r.master)); resp.OK {
		t.Fatalf("configureReplication before any load = %+v, want refused", resp)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("leftover set file after a refused settle: %v", err)
	}
	r.dump()
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master)))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Fatalf("role pin = %q, want replica", role)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("leftover set file after the settle: %v, want removed", err)
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
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "Database itu: toko.") {
		t.Fatalf("configureReplication = %+v, want A6 naming toko", resp)
	}
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Errorf("role pin = %q, want quarantined", role)
	}
}

// hookHost is a host whose program runs can be watched and failed: every
// listing of the source's databases is counted, onList runs before
// listing n (from 1) and fail fails it; onLoad runs as the dump's load
// (mysql reading the dump) starts. The hooks run on the job's goroutine.
type hookHost struct {
	mysql.Host
	onList func(n int)
	fail   func(n int) bool
	onLoad func()

	mu    sync.Mutex
	lists int
}

func (h *hookHost) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	switch {
	case slices.Contains(args, mysql.SourceListQuery):
		h.mu.Lock()
		h.lists++
		n := h.lists
		h.mu.Unlock()
		if h.onList != nil {
			h.onList(n)
		}
		if h.fail != nil && h.fail(n) {
			return fmt.Errorf("%s gagal: listing %d failed by the test", name, n)
		}
	case stdin != nil && filepath.Base(name) == "mysql" && h.onLoad != nil:
		h.onLoad()
	}
	return h.Host.Exec(ctx, stdin, stdout, name, args...)
}

// listings is how many source listings ran.
func (h *hookHost) listings() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lists
}

// TestRebuildFullDumpRefusesDatabaseCreatedDuringLoad: while a full dump
// loads (read-only lifted), an account with SUPER creates a database. It
// is on neither listing of the source, so it was not copied from it: the
// settle refuses naming it and the quarantine stays.
func TestRebuildFullDumpRefusesDatabaseCreatedDuringLoad(t *testing.T) {
	hook := &hookHost{}
	r := newSettleRigHooked(t, hook)
	tdb := rootDB(t, r.target)
	hook.onLoad = func() {
		if _, err := tdb.Exec("CREATE DATABASE liar"); err != nil {
			t.Errorf("CREATE DATABASE liar during the load: %v", err)
		}
	}
	r.dump()
	if n := hook.listings(); n != 2 {
		t.Errorf("source listings = %d, want 2 (before and after the load)", n)
	}
	r.wantSettleRefused("a database created during the load", "server ini masih punya database yang tidak disalin dari sumber "+
		r.src+"; karantina tidak diakhiri. Yang tidak ada di master baru (liar): jalankan di server ini sudo dbpanel "+
		"agent drop-database <nama> untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel.")
}

// TestSecondSourceListRetriedThreeTimes: the listing after the load fails
// twice, then works: the load counts, and the record holds both listings
// together. Between them the source dropped kecil (first listing only) and
// gained sesudah (second only); with both on this server the settle ends
// the quarantine.
func TestSecondSourceListRetriedThreeTimes(t *testing.T) {
	hook := &hookHost{fail: func(n int) bool { return n == 2 || n == 3 }}
	r := newSettleRigHooked(t, hook)
	mdb := rootDB(t, r.master)
	hook.onList = func(n int) {
		if n != 2 {
			return
		}
		for _, q := range []string{"DROP DATABASE kecil", "CREATE DATABASE sesudah"} {
			if _, err := mdb.Exec(q); err != nil {
				t.Errorf("%s on the source: %v", q, err)
			}
		}
	}
	r.dump()
	if n := hook.listings(); n != 4 {
		t.Errorf("source listings = %d, want 4 (one before, three after the load)", n)
	}
	if !r.d.LoadedFrom("127.0.0.1", r.master.Port) {
		t.Fatal("no load record after the listing worked on its third try")
	}
	execAll(t, rootDB(t, r.target), "SET GLOBAL super_read_only = OFF", "CREATE DATABASE IF NOT EXISTS kecil",
		"CREATE DATABASE sesudah", "SET GLOBAL super_read_only = ON")
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master)))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin = %q, want replica", role)
	}
}

// TestSecondSourceListFailureFailsCopyStep: the listing after the load
// fails three times: the copy step fails with T11, no load record is kept,
// and both read-only flags are on.
func TestSecondSourceListFailureFailsCopyStep(t *testing.T) {
	hook := &hookHost{fail: func(n int) bool { return n > 1 }}
	r := newSettleRigHooked(t, hook)
	d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master))
	if d.OK || d.Error != mysql.ErrSecondListFailed.Error() {
		t.Fatalf("dumpFrom{all} = %+v\nwant error %q", d, mysql.ErrSecondListFailed.Error())
	}
	if d.Error != `salinan selesai dimuat, tetapi daftar database master baru tidak bisa dibaca sesudahnya, jadi salinan ini `+
		`tidak dihitung; tekan "Ulangi dari tahap ini"` {
		t.Errorf("T11 = %q", d.Error)
	}
	if n := hook.listings(); n != 4 {
		t.Errorf("source listings = %d, want 4 (one before, three after the load)", n)
	}
	if r.d.LoadedFrom("127.0.0.1", r.master.Port) {
		t.Error("a load whose second listing failed was recorded")
	}
	var ro, sro int
	if err := rootDB(t, r.target).QueryRow("SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only").Scan(&ro, &sro); err != nil {
		t.Fatal(err)
	}
	if ro != 1 || sro != 1 {
		t.Errorf("flags = %d %d, want 1 1", ro, sro)
	}
	r.wantSettleRefused("after the failed listing", mysql.ErrNoLoadRecord.Error())
}

// tick runs one guard round of the rig's driver.
func (r *settleRig) tick() {
	r.t.Helper()
	if err := r.d.GuardTick(context.Background(), time.Now()); err != nil {
		r.t.Fatalf("GuardTick: %v", err)
	}
}

// wantSettleRefused dispatches configureReplication toward the master and
// checks it is refused with want, the role pin and the approval kept.
func (r *settleRig) wantSettleRefused(what, want string) {
	r.t.Helper()
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master))
	if resp.OK || resp.Error == nil || resp.Error.Message != want {
		r.t.Errorf("%s: configureReplication = %+v\nwant error %q", what, resp, want)
	}
	if role := pinFile(r.t, r.dir, mysql.RoleFile); role != "quarantined" {
		r.t.Errorf("%s: role pin = %q, want quarantined", what, role)
	}
	if got := pinFile(r.t, r.dir, mysql.RebuildApprovalFile); got == "" {
		r.t.Errorf("%s: the approval was cleared by a refused settle", what)
	}
}

// dump runs a dump of dbs (none: every database) from the master and
// requires it to succeed.
func (r *settleRig) dump(dbs ...string) {
	r.t.Helper()
	if d := r.snap.run(r.t, "mysql.dumpFrom", dumpParams(r.master, dbs...)); !d.OK {
		r.t.Fatalf("dumpFrom%v = %+v", dbs, d)
	}
}

// TestSettleRefusedAfterWritableSinceLoad: the guard found a flag off
// after the load: the settle answers T7 and keeps the quarantine and the
// approval. The next load drops that record.
func TestSettleRefusedAfterWritableSinceLoad(t *testing.T) {
	r := newSettleRig(t)
	r.dump("toko")
	r.tick()
	execAll(t, rootDB(t, r.target), "SET GLOBAL super_read_only = OFF")
	r.tick()
	if !r.d.WritableAfterLoad() {
		t.Fatal("writable since the load not recorded")
	}
	r.wantSettleRefused("after the guard relocked", mysql.ErrWritableSinceLoad.Error())
	execAll(t, rootDB(t, r.target), "STOP REPLICA")
	r.dump("toko")
	if r.d.WritableAfterLoad() {
		t.Error("the next load kept the writable-since-load record")
	}
}

// TestSettleRefusedWhenFlagsOffNow: a flag off when the settle reads them
// itself, before any guard round saw it: T7.
func TestSettleRefusedWhenFlagsOffNow(t *testing.T) {
	r := newSettleRig(t)
	r.dump("toko")
	execAll(t, rootDB(t, r.target), "SET GLOBAL super_read_only = OFF")
	r.wantSettleRefused("flags off at the settle", mysql.ErrWritableSinceLoad.Error())
}

// TestSettleWaitsForCLILift: while a dbpanel agent command holds the lift
// lock the settle answers T12 (it can be retried) and records nothing;
// once the command let go the settle ends the quarantine.
func TestSettleWaitsForCLILift(t *testing.T) {
	r := newSettleRig(t)
	r.dump("toko")
	release, err := mysql.AcquireLiftLock(r.d.LiftLockPath())
	if err != nil {
		t.Fatal(err)
	}
	r.wantSettleRefused("while a command holds the lift lock", mysql.ErrLiftHeldByCommand.Error())
	release()
	if r.d.WritableAfterLoad() {
		t.Error("a command's lift recorded as writable since the load")
	}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master)))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after the command let go = %q, want replica", role)
	}
}

// TestFailedLoadDropsEarlierLoadRecord: a load that began changing local
// data and failed leaves no load record: the earlier one no longer
// describes the server. The second load fails in its load stream: the old
// master's toko.t has been made a view, so the dump's CREATE TABLE fails.
func TestFailedLoadDropsEarlierLoadRecord(t *testing.T) {
	r := newSettleRig(t)
	r.dump()
	host, port := "127.0.0.1", r.master.Port
	if !r.d.LoadedFrom(host, port) {
		t.Fatal("no load record after the full load")
	}
	execAll(t, rootDB(t, r.target), "SET GLOBAL super_read_only = OFF", "DROP TABLE toko.t",
		"CREATE VIEW toko.t AS SELECT 1 AS id", "SET GLOBAL super_read_only = ON")
	d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "toko"))
	if d.OK {
		t.Fatalf("second load = %+v, want it to fail in its stream", d)
	}
	t.Logf("second load failed: %s", d.Error)
	if r.d.LoadedFrom(host, port) {
		t.Error("the earlier load record survived a load that began")
	}
	r.wantSettleRefused("after the failed load", mysql.ErrNoLoadRecord.Error())
}

// TestEarlyFailedLoadKeepsRecordAndWritableFlag: a second load refused by
// its client check (wrong password of the dump account) changed nothing:
// the earlier record stays, and so does the guard's writable-since-load
// record, so the settle answers T7.
func TestEarlyFailedLoadKeepsRecordAndWritableFlag(t *testing.T) {
	r := newSettleRig(t)
	r.dump()
	r.tick()
	execAll(t, rootDB(t, r.target), "SET GLOBAL super_read_only = OFF")
	r.tick()
	if !r.d.WritableAfterLoad() {
		t.Fatal("writable since the load not recorded")
	}
	p := dumpParams(r.master, "toko")
	p.Password = "salah"
	if d := r.snap.run(t, "mysql.dumpFrom", p); d.OK || !strings.Contains(d.Error, "uji dump dari master gagal") {
		t.Fatalf("dump with a wrong password = %+v, want the client check's refusal", d)
	}
	if !r.d.LoadedFrom("127.0.0.1", r.master.Port) {
		t.Error("a load refused before any local change dropped the earlier record")
	}
	if !r.d.WritableAfterLoad() {
		t.Error("a load refused before any local change cleared writable since the load")
	}
	r.wantSettleRefused("after the refused load", mysql.ErrWritableSinceLoad.Error())
}
