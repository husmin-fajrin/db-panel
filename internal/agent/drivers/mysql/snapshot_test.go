package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	// replPassword and dumpPassword hold every character the SQL literal
	// and the option-file value must escape.
	replPassword = `Repl'pw\"#; x`
	dumpPassword = `Dump"pw\'#;  x `
)

// cloneArgs loads the clone plugin at startup.
var cloneArgs = []string{"plugin-load-add=mysql_clone.so"}

// jobEvents collects the events of the jobs a test runs.
type jobEvents struct {
	mu       sync.Mutex
	progress []proto.JobProgress
	logs     []string
	done     chan proto.JobDone
}

func (e *jobEvents) Emit(ev proto.Event) error {
	switch ev.Type {
	case proto.EventJobProgress:
		var p proto.JobProgress
		_ = json.Unmarshal(ev.Payload, &p)
		e.mu.Lock()
		e.progress = append(e.progress, p)
		e.mu.Unlock()
	case proto.EventJobLog:
		var l proto.JobLog
		_ = json.Unmarshal(ev.Payload, &l)
		e.mu.Lock()
		e.logs = append(e.logs, l.Message)
		e.mu.Unlock()
	case proto.EventJobDone:
		var d proto.JobDone
		_ = json.Unmarshal(ev.Payload, &d)
		e.done <- d
	}
	return nil
}

func (e *jobEvents) progressCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.progress)
}

// snapshotRig runs snapshot jobs through a registry with a job runner.
type snapshotRig struct {
	reg    *commands.Registry
	runner *jobs.Runner
	events *jobEvents
}

func newSnapshotRig(h mysql.Host, creds mysql.CredentialStore) *snapshotRig {
	ev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(ev, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: h, Creds: creds, Jobs: runner}).Register(reg)
	return &snapshotRig{reg: reg, runner: runner, events: ev}
}

// start dispatches cmd and returns the started job's id.
func (r *snapshotRig) start(t *testing.T, cmd string, p proto.SnapshotParams) string {
	t.Helper()
	resp := r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(t, p)})
	started := decode[proto.StartedJob](t, resp)
	if started.JobID == "" {
		t.Fatalf("%s: no job id", cmd)
	}
	return started.JobID
}

func (r *snapshotRig) wait(t *testing.T) proto.JobDone {
	t.Helper()
	select {
	case d := <-r.events.done:
		return d
	case <-time.After(5 * time.Minute):
		t.Fatal("job did not finish")
	}
	return proto.JobDone{}
}

// run starts cmd and waits for it.
func (r *snapshotRig) run(t *testing.T, cmd string, p proto.SnapshotParams) proto.JobDone {
	t.Helper()
	r.start(t, cmd, p)
	return r.wait(t)
}

func snapshotResult(t *testing.T, d proto.JobDone) proto.SnapshotResult {
	t.Helper()
	if !d.OK {
		t.Fatalf("job failed: %s", d.Error)
	}
	var res proto.SnapshotResult
	if err := json.Unmarshal(d.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func gtidExecuted(t *testing.T, db *sql.DB) string {
	t.Helper()
	var g string
	if err := db.QueryRow("SELECT @@GLOBAL.gtid_executed").Scan(&g); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(g, "\n", "")
}

func databases(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT schema_name FROM information_schema.schemata
		WHERE schema_name NOT IN ('mysql', 'sys', 'information_schema', 'performance_schema') ORDER BY schema_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// fillMaster creates databases a and b on the master: a with a table, a
// view, a routine, a trigger and an event; b with one table.
func fillMaster(t *testing.T, db *sql.DB) {
	t.Helper()
	execAll(t, db,
		"CREATE DATABASE a",
		"CREATE TABLE a.t (id INT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO a.t VALUES (1, 'satu'), (2, 'dua'), (3, 'tiga')",
		"CREATE TABLE a.audit (n INT)",
		"CREATE VIEW a.v AS SELECT id FROM a.t",
		"CREATE FUNCTION a.f() RETURNS INT DETERMINISTIC RETURN 42",
		"CREATE TRIGGER a.tr AFTER INSERT ON a.t FOR EACH ROW INSERT INTO a.audit VALUES (NEW.id)",
		"CREATE EVENT a.e ON SCHEDULE EVERY 1 DAY DISABLE DO DELETE FROM a.audit",
		"CREATE DATABASE b",
		"CREATE TABLE b.t (id INT PRIMARY KEY)",
		"INSERT INTO b.t VALUES (7)",
	)
}

// dumpUser creates the account the dump reads the master with.
func dumpUser(t *testing.T, db *sql.DB) {
	t.Helper()
	execAll(t, db,
		"CREATE USER dumper@'%' IDENTIFIED BY "+sqlLiteral(dumpPassword),
		"GRANT SELECT, RELOAD, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, PROCESS, REPLICATION CLIENT ON *.* TO dumper@'%'",
	)
}

// replUser creates the replication (and clone donor) account on the master.
func replUser(t *testing.T, master *testmysql.Instance) {
	t.Helper()
	if err := mysql.CreateReplUser(context.Background(), master.Host(),
		proto.ReplUserParams{User: "dbpanel_repl", Password: replPassword}); err != nil {
		t.Fatal(err)
	}
}

func sqlLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}

func dumpParams(master *testmysql.Instance, dbs ...string) proto.SnapshotParams {
	return proto.SnapshotParams{Host: "127.0.0.1", Port: master.Port, User: "dumper", Password: dumpPassword,
		Databases: dbs, EstimateBytes: 1 << 20}
}

func cloneParams(master *testmysql.Instance) proto.SnapshotParams {
	return proto.SnapshotParams{Host: "127.0.0.1", Port: master.Port, User: "dbpanel_repl", Password: replPassword,
		EstimateBytes: 100 << 20}
}

func TestCloneFromReplicatesData(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	replUser(t, master)
	want := gtidExecuted(t, mdb)

	rig := newSnapshotRig(replica.Host(), nil)
	p := cloneParams(master)
	p.EstimateBytes = 0 // Bytes must come from clone_progress
	res := snapshotResult(t, rig.run(t, "mysql.cloneFrom", p))
	if res.Method != "clone" || res.Bytes == 0 || res.TookMs <= 0 {
		t.Errorf("result = %+v", res)
	}
	if res.GTIDExecuted != want {
		t.Errorf("GTIDExecuted = %q, want the master's %q", res.GTIDExecuted, want)
	}
	if rig.events.progressCount() == 0 {
		t.Error("no progress reported")
	}

	rdb := rootDB(t, replica)
	if got := gtidExecuted(t, rdb); got != want {
		t.Errorf("replica gtid_executed = %q, want %q", got, want)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows = %d, want 3", n)
	}
	if got := databases(t, rdb); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("replica databases = %q", got)
	}
	var state string
	var errNo int
	if err := rdb.QueryRow("SELECT STATE, ERROR_NO FROM performance_schema.clone_status").Scan(&state, &errNo); err != nil || state != "Completed" || errNo != 0 {
		t.Errorf("clone_status = %s, %d, %v", state, errNo, err)
	}
	if active, _ := replica.Service().IsActive(context.Background()); !active {
		t.Error("replica is not running after the clone")
	}
}

func TestCloneFailsWithWrongPassword(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replUser(t, master)
	rdb := rootDB(t, replica)
	execAll(t, rdb, "CREATE DATABASE keepme")

	rig := newSnapshotRig(replica.Host(), nil)
	p := cloneParams(master)
	p.Password = "wrong'pass\\word"
	d := rig.run(t, "mysql.cloneFrom", p)
	if d.OK || !strings.Contains(d.Error, "CLONE gagal") || !strings.Contains(d.Error, "1045") {
		t.Errorf("clone with a wrong password = %+v", d)
	}
	if strings.Contains(d.Error, "wrong") || strings.Contains(d.Error, replPassword) {
		t.Errorf("error leaks a password: %s", d.Error)
	}

	// Nothing listens on the donor port.
	p = cloneParams(master)
	p.Port = closedPort(t)
	d = rig.run(t, "mysql.cloneFrom", p)
	if d.OK || !strings.Contains(d.Error, "CLONE gagal") {
		t.Errorf("clone from an unreachable donor = %+v", d)
	}

	// The failed attempts left the replica as it was.
	if got := databases(t, rdb); !reflect.DeepEqual(got, []string{"keepme"}) {
		t.Errorf("replica databases after failed clones = %q", got)
	}
}

// TestCloneCancel cancels a clone, slowed down, while it copies files.
func TestCloneCancel(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replUser(t, master)
	// 64 MB that does not compress, so the copy outlasts the cancel.
	conn, err := rootDB(t, master).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SET SESSION cte_max_recursion_depth = 100000",
		"CREATE DATABASE big",
		"CREATE TABLE big.t (b BLOB)",
		"INSERT INTO big.t WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 65536) SELECT RANDOM_BYTES(1024) FROM n",
	} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	conn.Close()
	rdb := rootDB(t, replica)
	execAll(t, rdb, "SET GLOBAL clone_max_data_bandwidth = 1")

	rig := newSnapshotRig(replica.Host(), nil)
	id := rig.start(t, "mysql.cloneFrom", cloneParams(master))
	deadline := time.Now().Add(time.Minute)
	for {
		var n int
		_ = rdb.QueryRow("SELECT COUNT(*) FROM performance_schema.clone_progress WHERE STAGE = 'FILE COPY' AND STATE = 'In Progress'").Scan(&n)
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the clone never reached FILE COPY")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !rig.runner.Cancel(id) {
		t.Fatal("Cancel found no running job")
	}
	d := rig.wait(t)
	if d.OK {
		t.Fatalf("canceled clone succeeded: %+v", d)
	}
	st, _ := rig.runner.Status(id)
	if st.State != "canceled" {
		t.Errorf("job state = %s (%s), want canceled", st.State, st.Error)
	}
	// The clone statement was killed, not left running.
	if n := count(t, rdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE INFO LIKE 'CLONE INSTANCE%'"); n != 0 {
		t.Errorf("%d CLONE statements still running", n)
	}
	var state string
	if err := rdb.QueryRow("SELECT STATE FROM performance_schema.clone_status").Scan(&state); err != nil || state == "Completed" {
		t.Errorf("clone_status = %s, %v", state, err)
	}
}

func TestDumpSelectedDatabasesOnly(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	dumpUser(t, mdb)

	// A replica set up by hand already has transactions of its own, and a
	// configured replica is read-only.
	rdb := rootDB(t, replica)
	execAll(t, rdb, "CREATE USER someone@localhost", "SET GLOBAL super_read_only = ON")

	rig := newSnapshotRig(replica.Host(), nil)
	res := snapshotResult(t, rig.run(t, "mysql.dumpFrom", dumpParams(master, "a")))
	// mysqldump's FLUSH TABLES is a transaction on the master: its
	// gtid_executed at the dump is the one after it.
	want := gtidExecuted(t, mdb)
	if res.Method != "dump" || res.Bytes == 0 || res.TookMs <= 0 {
		t.Errorf("result = %+v", res)
	}
	if res.GTIDExecuted != want {
		t.Errorf("GTIDExecuted = %q, want the master's %q", res.GTIDExecuted, want)
	}
	if got := gtidExecuted(t, rdb); got != want {
		t.Errorf("replica gtid_executed = %q, want the master's %q", got, want)
	}
	if got := databases(t, rdb); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("replica databases = %q, want only a", got)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows = %d", n)
	}
	for q, want := range map[string]int{
		"SELECT COUNT(*) FROM information_schema.views WHERE table_schema = 'a'":      1,
		"SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = 'a'": 1,
		"SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = 'a'": 1,
		"SELECT COUNT(*) FROM information_schema.events WHERE event_schema = 'a'":     1,
	} {
		if n := count(t, rdb, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	if v := variable(t, rdb, "super_read_only"); v != "ON" {
		t.Errorf("super_read_only after the dump = %s, want ON again", v)
	}
	if rig.events.progressCount() == 0 {
		t.Error("no progress reported")
	}
}

func TestDumpAllDatabases(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	dumpUser(t, mdb)

	rig := newSnapshotRig(replica.Host(), nil)
	res := snapshotResult(t, rig.run(t, "mysql.dumpFrom", dumpParams(master)))
	want := gtidExecuted(t, mdb) // the master is idle: its set at the dump
	if res.GTIDExecuted != want {
		t.Errorf("GTIDExecuted = %q, want %q", res.GTIDExecuted, want)
	}
	rdb := rootDB(t, replica)
	if got := databases(t, rdb); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("replica databases = %q", got)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM b.t"); n != 1 {
		t.Errorf("b.t rows = %d", n)
	}
	if got := gtidExecuted(t, rdb); got != want {
		t.Errorf("replica gtid_executed = %q, want %q", got, want)
	}
}

func TestDumpDropExistingReplaces(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	dumpUser(t, mdb)
	rdb := rootDB(t, replica)
	execAll(t, rdb,
		"CREATE DATABASE a",
		"CREATE TABLE a.stale (x INT)",
		"CREATE TABLE a.t (id INT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO a.t VALUES (99, 'lama')",
	)

	rig := newSnapshotRig(replica.Host(), nil)
	p := dumpParams(master, "a")
	p.DropExisting = true
	snapshotResult(t, rig.run(t, "mysql.dumpFrom", p))
	if n := count(t, rdb, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'a' AND table_name = 'stale'"); n != 0 {
		t.Error("a.stale survived DropExisting")
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t WHERE id = 99"); n != 0 {
		t.Error("the replica's own row survived DropExisting")
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows = %d, want the master's 3", n)
	}
}

func TestSnapshotRefusesWhileReplicaRunning(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	dumpUser(t, mdb)
	replUser(t, master)
	rdb := rootDB(t, replica)
	execAll(t, rdb,
		fmt.Sprintf("CHANGE REPLICATION SOURCE TO SOURCE_HOST = '127.0.0.1', SOURCE_PORT = %d, SOURCE_USER = 'dbpanel_repl', "+
			"SOURCE_PASSWORD = %s, SOURCE_AUTO_POSITION = 1, GET_SOURCE_PUBLIC_KEY = 1", master.Port, sqlLiteral(replPassword)),
		"START REPLICA",
	)

	rig := newSnapshotRig(replica.Host(), nil)
	for cmd, p := range map[string]proto.SnapshotParams{
		"mysql.cloneFrom": cloneParams(master),
		"mysql.dumpFrom":  dumpParams(master, "a"),
	} {
		if d := rig.run(t, cmd, p); d.OK || !strings.Contains(d.Error, "replikasi masih berjalan; hentikan dulu") {
			t.Errorf("%s while replicating = %+v", cmd, d)
		}
	}

	// A configured but stopped replica that already holds data takes a
	// further database without losing its GTID history.
	deadline := time.Now().Add(time.Minute)
	for gtidExecuted(t, rdb) != gtidExecuted(t, mdb) {
		if time.Now().After(deadline) {
			t.Fatal("the replica did not catch up")
		}
		time.Sleep(200 * time.Millisecond)
	}
	execAll(t, rdb, "STOP REPLICA")
	execAll(t, mdb, "CREATE DATABASE c", "CREATE TABLE c.t (id INT PRIMARY KEY)", "INSERT INTO c.t VALUES (1), (2)")
	before := gtidExecuted(t, rdb)
	res := snapshotResult(t, rig.run(t, "mysql.dumpFrom", dumpParams(master, "c")))
	if res.GTIDExecuted != before {
		t.Errorf("GTIDExecuted = %q, want the replica's own %q", res.GTIDExecuted, before)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM c.t"); n != 2 {
		t.Errorf("c.t rows = %d", n)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows = %d after adding c", n)
	}
}

// TestDumpDefinerErrorExplained loads, as an admin account without
// SET_USER_ID, a view whose DEFINER does not exist on the replica.
func TestDumpDefinerErrorExplained(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	dumpUser(t, mdb)
	execAll(t, mdb,
		"CREATE DATABASE a",
		"CREATE TABLE a.t (id INT)",
		"CREATE DEFINER = ghost@'%' VIEW a.v AS SELECT id FROM a.t",
	)

	rdb := rootDB(t, replica)
	var privs []string
	for _, g := range mysql.RequiredReplicaGrants() {
		if g != "GRANT OPTION" {
			privs = append(privs, g)
		}
	}
	const adminPassword = `adm"in\pw`
	execAll(t, rdb,
		"CREATE USER admin@localhost IDENTIFIED BY "+sqlLiteral(adminPassword),
		"GRANT "+strings.Join(privs, ", ")+" ON *.* TO admin@localhost WITH GRANT OPTION",
	)
	creds := &memCreds{p: proto.ConnectParams{User: "admin", Password: adminPassword}}
	rig := newSnapshotRig(replica.HostWith(creds), creds)
	d := rig.run(t, "mysql.dumpFrom", dumpParams(master, "a"))
	if d.OK || !strings.Contains(d.Error, "DEFINER") || !strings.Contains(d.Error, "SET_ANY_DEFINER") {
		t.Errorf("dump with a foreign DEFINER = %+v", d)
	}
	if strings.Contains(d.Error, adminPassword) || strings.Contains(d.Error, dumpPassword) {
		t.Errorf("error leaks a password: %s", d.Error)
	}
}

func TestSnapshotRejectsBadParams(t *testing.T) {
	reg := commands.NewRegistry()
	(&mysql.Driver{}).Register(reg) // no host: only validation can answer
	ok := proto.SnapshotParams{Host: "10.0.0.5", Port: 3306, User: "repl", Password: "x"}
	with := func(f func(p *proto.SnapshotParams)) proto.SnapshotParams { p := ok; f(&p); return p }
	cases := map[string][]proto.SnapshotParams{
		"mysql.cloneFrom": {
			with(func(p *proto.SnapshotParams) { p.Host = "" }),
			with(func(p *proto.SnapshotParams) { p.Host = "h' OR '1" }),
			with(func(p *proto.SnapshotParams) { p.Host = "::1" }), // CLONE takes no IPv6 address
			with(func(p *proto.SnapshotParams) { p.Port = 0 }),
			with(func(p *proto.SnapshotParams) { p.Port = 65536 }),
			with(func(p *proto.SnapshotParams) { p.User = "repl'@'%" }),
			with(func(p *proto.SnapshotParams) { p.Password = "" }),
			with(func(p *proto.SnapshotParams) { p.Password = "a\nb" }),
			with(func(p *proto.SnapshotParams) { p.Databases = []string{"a"} }),
		},
		"mysql.dumpFrom": {
			with(func(p *proto.SnapshotParams) { p.Host = "a b" }),
			with(func(p *proto.SnapshotParams) { p.Port = -1 }),
			with(func(p *proto.SnapshotParams) { p.Password = "tab\there" }),
			with(func(p *proto.SnapshotParams) { p.Databases = []string{"a`; DROP"} }),
			with(func(p *proto.SnapshotParams) { p.Databases = []string{"--all-databases"} }),
			with(func(p *proto.SnapshotParams) { p.Databases = []string{"mysql"} }),
			with(func(p *proto.SnapshotParams) { p.Databases = []string{"a", "a"} }),
		},
	}
	for cmd, ps := range cases {
		for _, p := range ps {
			r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(t, p)})
			if r.OK || r.Error.Code != proto.CodeBadParams {
				t.Errorf("%s %+v: response = %+v, want bad_params", cmd, p, r)
			}
		}
	}
	// Valid parameters get past validation to the missing host.
	for _, cmd := range []string{"mysql.cloneFrom", "mysql.dumpFrom"} {
		p := ok
		if cmd == "mysql.dumpFrom" {
			p.Host, p.Databases = "fe80::1", []string{"a", "b_2"}
		}
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(t, p)})
		if r.OK || r.Error.Code == proto.CodeBadParams {
			t.Errorf("%s with valid params = %+v, want the missing-host error", cmd, r)
		}
	}
}

func TestDumpArgs(t *testing.T) {
	args := func(gtid string, selection ...string) []string {
		return append([]string{"--defaults-extra-file=/tmp/src.cnf", "--single-transaction", "--set-gtid-purged=" + gtid,
			"--triggers", "--routines", "--events", "--skip-lock-tables", "--column-statistics=0", "--no-tablespaces"}, selection...)
	}
	got := mysql.DumpArgs("/tmp/src.cnf", nil, "ON")
	want := args("ON", "--all-databases")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("all databases:\n got %q\nwant %q", got, want)
	}
	got = mysql.DumpArgs("/tmp/src.cnf", []string{"a", "b"}, "COMMENTED")
	want = args("COMMENTED", "--databases", "a", "b")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selected databases:\n got %q\nwant %q", got, want)
	}
	if got := mysql.LoadArgs("/tmp/local.cnf"); !reflect.DeepEqual(got, []string{"--defaults-extra-file=/tmp/local.cnf", "--init-command=SET sql_log_bin=0"}) {
		t.Errorf("LoadArgs = %q", got)
	}
}

func TestDumpGTIDMode(t *testing.T) {
	sel := proto.SnapshotParams{Databases: []string{"a"}}
	all := proto.SnapshotParams{}
	drop := proto.SnapshotParams{Databases: []string{"a"}, DropExisting: true}
	for _, c := range []struct {
		name       string
		executed   string
		configured bool
		p          proto.SnapshotParams
		want       string
	}{
		{"fresh replica", "", false, sel, "ON"},
		{"replica with its own transactions", "uuid:1-3", false, sel, "ON"},
		{"stopped replica adds a database", "uuid:1-30", true, sel, "COMMENTED"},
		{"stopped replica, every database", "uuid:1-30", true, all, "ON"},
		{"stopped replica, database replaced", "uuid:1-30", true, drop, "ON"},
		{"configured replica without history", "", true, sel, "ON"},
	} {
		if got := mysql.DumpGTIDMode(c.executed, c.configured, c.p); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestClientDefaults(t *testing.T) {
	got, err := mysql.ClientDefaults(proto.ConnectParams{User: "dumper", Password: `a"b\c#d 'e `, Host: "10.0.0.5", Port: 3307}, "/run/mysqld.sock")
	want := "[client]\nuser=\"dumper\"\npassword=\"a\\\"b\\\\c#d 'e \"\nhost=\"10.0.0.5\"\nport=3307\nget-server-public-key\n"
	if err != nil || got != want {
		t.Errorf("remote:\n got %q, %v\nwant %q", got, err, want)
	}
	got, err = mysql.ClientDefaults(proto.ConnectParams{User: "root"}, "/run/my sql.sock")
	want = "[client]\nuser=\"root\"\nsocket=\"/run/my sql.sock\"\n"
	if err != nil || got != want {
		t.Errorf("local socket:\n got %q, %v\nwant %q", got, err, want)
	}
	got, err = mysql.ClientDefaults(proto.ConnectParams{User: "admin", Password: "p", Host: "localhost", Port: 3306}, "/s")
	want = "[client]\nuser=\"admin\"\npassword=\"p\"\nhost=\"127.0.0.1\"\nport=3306\nget-server-public-key\n"
	if err != nil || got != want {
		t.Errorf("local over TCP:\n got %q, %v\nwant %q", got, err, want)
	}
	if _, err := mysql.ClientDefaults(proto.ConnectParams{User: "u", Password: "new\nline"}, "/s"); err == nil {
		t.Error("a control character in the password was accepted")
	}
}

// TestClientDefaultsAcceptedByMysql checks that the mysql client reads the
// escaped password back exactly.
func TestClientDefaultsAcceptedByMysql(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "CREATE USER dumper@'%' IDENTIFIED BY "+sqlLiteral(dumpPassword))
	content, err := mysql.ClientDefaults(proto.ConnectParams{User: "dumper", Password: dumpPassword, Host: "127.0.0.1", Port: inst.Port}, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.cnf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := mysql.FindClientTool(inst.Layout().MysqldPath, "mysql")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := inst.Host().Exec(context.Background(), nil, &out, client, "--defaults-extra-file="+path, "-N", "-e", "SELECT CURRENT_USER()"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "dumper@%" {
		t.Errorf("CURRENT_USER() = %q", out.String())
	}
}

func TestFindClientTool(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "mysqldump")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := mysql.FindClientTool(filepath.Join(dir, "mysqld"), "mysqldump"); err != nil || got != tool {
		t.Errorf("next to mysqld: %q, %v", got, err)
	}
	t.Setenv("PATH", t.TempDir())
	_, err := mysql.FindClientTool("/nonexistent/sbin/mysqld", "mysqldump")
	if err == nil || err.Error() != "mysqldump tidak ditemukan; pasang paket mysql-client" {
		t.Errorf("missing mysqldump: %v", err)
	}
}
