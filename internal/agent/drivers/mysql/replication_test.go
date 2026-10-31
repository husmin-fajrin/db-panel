package mysql_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// sourceParams points a replica at master through the replication user
// replUser creates, replicating dbs (none = every database).
func sourceParams(master *testmysql.Instance, dbs ...string) proto.ReplicationSourceParams {
	return proto.ReplicationSourceParams{Host: "127.0.0.1", Port: master.Port, User: "dbpanel_repl",
		Password: replPassword, Databases: dbs}
}

// follow configures replica to replicate dbs from master and starts it.
func follow(t *testing.T, master, replica *testmysql.Instance, dbs ...string) {
	t.Helper()
	decode[proto.ReplicaStatus](t, dispatch(t, replica.Host(), "mysql.configureReplication", sourceParams(master, dbs...)))
	rs := decode[proto.ReplicaStatus](t, dispatch(t, replica.Host(), "mysql.startReplica", nil))
	if rs.SQLRunning != "Yes" || (rs.IORunning != "Yes" && rs.IORunning != "Connecting") {
		t.Fatalf("after startReplica: %+v", rs)
	}
}

// synced waits up to a minute for replica to execute master's GTID set.
func synced(t *testing.T, master, replica *testmysql.Instance) proto.WaitSyncedResult {
	t.Helper()
	set := gtidExecuted(t, rootDB(t, master))
	res := decode[proto.WaitSyncedResult](t, dispatch(t, replica.Host(), "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: set, TimeoutSec: 60}))
	if !res.Synced {
		t.Fatalf("replica not synced to %q: %+v", set, res)
	}
	return res
}

// globalFilters is the server's global REPLICATE_WILD_DO_TABLE rule.
func globalFilters(t *testing.T, inst *testmysql.Instance) string {
	t.Helper()
	var rule string
	err := rootDB(t, inst).QueryRow(`SELECT COALESCE(MAX(FILTER_RULE), '') FROM performance_schema.replication_applier_global_filters
		WHERE FILTER_NAME = 'REPLICATE_WILD_DO_TABLE'`).Scan(&rule)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestWildDoTablePatterns(t *testing.T) {
	if got, want := mysql.WildDoTablePatterns([]string{"a", "x_y", "$z_", "toko-online"}),
		[]string{`a.%`, `x\_y.%`, `$z\_.%`, `toko-online.%`}; !slices.Equal(got, want) {
		t.Errorf("patterns = %q, want %q", got, want)
	}
	quote := func(s string) string { return mysql.QuoteLiteral(s, false) }
	if got, want := mysql.FilterStatement([]string{"a", "x_y"}, quote),
		`CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = ('a.%', 'x\\_y.%')`; got != want {
		t.Errorf("filter statement = %s, want %s", got, want)
	}
	noBackslash := func(s string) string { return mysql.QuoteLiteral(s, true) }
	if got, want := mysql.FilterStatement([]string{"x_y"}, noBackslash),
		`CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = ('x\_y.%')`; got != want {
		t.Errorf("filter statement (NO_BACKSLASH_ESCAPES) = %s, want %s", got, want)
	}
	if got, want := mysql.FilterStatement(nil, quote), `CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = ()`; got != want {
		t.Errorf("clearing filter statement = %s, want %s", got, want)
	}
}

// TestDBNameRe: letters and digits of any script, _, $ and - (never
// first); nothing that could leave a backtick-quoted identifier, a path or
// a LIKE pattern, or read as a command-line flag.
func TestDBNameRe(t *testing.T) {
	for _, name := range []string{"a", "toko-online", "rs_jiwa", "データ", "$x", "db2", "x-", strings.Repeat("a", 64)} {
		if !mysql.DBNameRe.MatchString(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", "-x", "a b", "a.b", "a/b", "a`b", "a%", "a'b", `a\b`, "a\nb", strings.Repeat("a", 65)} {
		if mysql.DBNameRe.MatchString(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestValidGTIDSet(t *testing.T) {
	const u1, u2 = "3E11FA47-71CA-11E1-9E33-C80AA9429562", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for _, s := range []string{
		"",
		u1 + ":1",
		u1 + ":1-5",
		u1 + ":1-5:7:9-12",
		u1 + ":1-5,\n" + u2 + ":1-3",
		"  " + u1 + ":1-5 , " + u2 + ":4\n",
		u2 + ":tag_1:1-4:7",
	} {
		if !mysql.ValidGTIDSet(s) {
			t.Errorf("ValidGTIDSet(%q) = false", s)
		}
	}
	for _, s := range []string{
		u1,
		u1 + ":",
		u1 + ":1-",
		u1 + ":a-5",
		u1 + ":1,",
		"zzzzzzzz-bbbb-cccc-dddd-eeeeeeeeeeee:1",
		u1 + ":1'; DROP DATABASE a; --",
		u1 + ":1\\",
		u1 + ":1;" + u2 + ":1",
		u1 + ":1-5" + strings.Repeat(","+u2+":1", 2000),
	} {
		if mysql.ValidGTIDSet(s) {
			t.Errorf("ValidGTIDSet(%.80q) = true", s)
		}
	}
}

func TestSourceStatement(t *testing.T) {
	p := proto.ReplicationSourceParams{Host: "10.0.0.5", Port: 3307, User: "dbpanel_repl", Password: `p'w\x"`}
	quote := func(s string) string { return mysql.QuoteLiteral(s, false) }
	want := `CHANGE REPLICATION SOURCE TO SOURCE_HOST = '10.0.0.5', SOURCE_PORT = 3307, SOURCE_USER = 'dbpanel_repl', ` +
		`SOURCE_PASSWORD = 'p''w\\x"', SOURCE_AUTO_POSITION = 1, GET_SOURCE_PUBLIC_KEY = 1, ` +
		`SOURCE_CONNECT_RETRY = 10, SOURCE_RETRY_COUNT = 86400, SOURCE_SSL = 0`
	if got := mysql.SourceStatement(p, quote); got != want {
		t.Errorf("statement =\n%s\nwant\n%s", got, want)
	}
	p.SSL = true
	// Over TLS the master's RSA key is not requested.
	if got := mysql.SourceStatement(p, quote); !strings.HasSuffix(got, ", SOURCE_SSL = 1") ||
		!strings.Contains(got, "GET_SOURCE_PUBLIC_KEY = 0") {
		t.Errorf("SSL statement = %s", got)
	}
	noBackslash := func(s string) string { return mysql.QuoteLiteral(s, true) }
	if got := mysql.SourceStatement(p, noBackslash); !strings.Contains(got, `SOURCE_PASSWORD = 'p''w\x"'`) {
		t.Errorf("NO_BACKSLASH_ESCAPES statement = %s", got)
	}
}

func TestReplicationRejectsBadParams(t *testing.T) {
	good := proto.ReplicationSourceParams{Host: "db1.example", Port: 3306, User: "repl", Password: "Rahasia-123"}
	for name, mod := range map[string]func(p *proto.ReplicationSourceParams){
		"host quote":    func(p *proto.ReplicationSourceParams) { p.Host = "db'1" },
		"host space":    func(p *proto.ReplicationSourceParams) { p.Host = "db 1" },
		"host colon":    func(p *proto.ReplicationSourceParams) { p.Host = "db1:3306" },
		"port zero":     func(p *proto.ReplicationSourceParams) { p.Port = 0 },
		"port high":     func(p *proto.ReplicationSourceParams) { p.Port = 65536 },
		"user":          func(p *proto.ReplicationSourceParams) { p.User = "re'pl" },
		"no password":   func(p *proto.ReplicationSourceParams) { p.Password = "" },
		"control char":  func(p *proto.ReplicationSourceParams) { p.Password = "a\nb" },
		"long password": func(p *proto.ReplicationSourceParams) { p.Password = strings.Repeat("é", 33) },
		"db name":       func(p *proto.ReplicationSourceParams) { p.Databases = []string{"a%"} },
		"db quote":      func(p *proto.ReplicationSourceParams) { p.Databases = []string{"a'"} },
		"system db":     func(p *proto.ReplicationSourceParams) { p.Databases = []string{"mysql"} },
		"duplicate db":  func(p *proto.ReplicationSourceParams) { p.Databases = []string{"a", "a"} },
		"empty db name": func(p *proto.ReplicationSourceParams) { p.Databases = []string{""} },
	} {
		p := good
		mod(&p)
		if r := dispatch(t, nil, "mysql.configureReplication", p); r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%s: response = %+v", name, r)
		} else if strings.Contains(r.Error.Message, p.Password) && p.Password != "" {
			t.Errorf("%s: error shows the password: %s", name, r.Error.Message)
		}
	}
	// 32 characters is MySQL's SOURCE_PASSWORD limit, counted in characters:
	// this one passes validation and fails only for want of a server.
	p := good
	p.Password = strings.Repeat("é", 32)
	if r := dispatch(t, nil, "mysql.configureReplication", p); r.OK || r.Error.Code == proto.CodeBadParams {
		t.Errorf("32-character password: %+v", r)
	}
	if r := dispatch(t, nil, "mysql.setReplicationFilter", proto.ReplicationFilterParams{Databases: []string{"a.b"}}); r.OK || r.Error.Code != proto.CodeBadParams {
		t.Errorf("setReplicationFilter a.b: %+v", r)
	}
	const set = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-3"
	for _, cmd := range []string{"mysql.waitSynced", "mysql.startUntil"} {
		for _, p := range []proto.WaitSyncedParams{
			{GTIDSet: set, TimeoutSec: 0},
			{GTIDSet: set, TimeoutSec: 86401},
			{GTIDSet: set + "'", TimeoutSec: 5},
		} {
			if r := dispatch(t, nil, cmd, p); r.OK || r.Error.Code != proto.CodeBadParams {
				t.Errorf("%s %+v: %+v", cmd, p, r)
			}
		}
	}
	if r := dispatch(t, nil, "mysql.startUntil", proto.StartUntilParams{GTIDSet: " ", TimeoutSec: 5}); r.OK || r.Error.Code != proto.CodeBadParams {
		t.Errorf("startUntil with an empty set: %+v", r)
	}
}

// TestFilteredReplicationAppliesOnlySelected replicates only database a:
// a's writes arrive, b never exists on the replica, and the replica still
// records every master transaction as executed.
func TestFilteredReplicationAppliesOnlySelected(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb, rdb := rootDB(t, master), rootDB(t, replica)
	replUser(t, master)
	execAll(t, mdb,
		"CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "INSERT INTO a.t VALUES (1), (2)",
		"CREATE DATABASE b", "CREATE TABLE b.t (id INT PRIMARY KEY)", "INSERT INTO b.t VALUES (1)",
	)

	follow(t, master, replica, "a")
	synced(t, master, replica)
	execAll(t, mdb, "INSERT INTO a.t VALUES (3)", "INSERT INTO b.t VALUES (2)", "CREATE TABLE b.u (id INT)")
	res := synced(t, master, replica)

	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows on the replica = %d, want 3", n)
	}
	if got := databases(t, rdb); !slices.Equal(got, []string{"a"}) {
		t.Errorf("replica databases = %q, want only a", got)
	}
	full := gtidExecuted(t, mdb)
	if n := count(t, rdb, "SELECT GTID_SUBSET("+sqlLiteral(full)+", @@GLOBAL.gtid_executed)"); n != 1 {
		t.Errorf("replica gtid_executed %q lacks the master's %q", gtidExecuted(t, rdb), full)
	}
	if n := count(t, rdb, "SELECT GTID_SUBSET("+sqlLiteral(full)+", "+sqlLiteral(res.Executed)+")"); n != 1 {
		t.Errorf("Executed = %q, want it to hold %q", res.Executed, full)
	}
	if !slices.Equal(res.Status.Filters, []string{"a.%"}) {
		t.Errorf("filters = %q", res.Status.Filters)
	}
	if res.Status.SQLRunning != "Yes" || res.Status.IORunning != "Yes" {
		t.Errorf("status = %+v", res.Status)
	}
}

// TestConfigureStartWaitSynced re-points a replica twice (a closed port, a
// wrong password) before the right source, then follows every database.
func TestConfigureStartWaitSynced(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb, rdb := rootDB(t, master), rootDB(t, replica)
	replUser(t, master)
	h := replica.Host()

	p := sourceParams(master)
	p.Port = closedPort(t)
	decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.configureReplication", p))
	if r := dispatch(t, h, "mysql.startReplica", nil); r.OK || !strings.Contains(r.Error.Message, "master tidak bisa dihubungi dari server ini") ||
		!strings.Contains(r.Error.Message, "2003") {
		t.Errorf("start against a closed port = %+v", r)
	}

	p = sourceParams(master)
	p.Password = "salah"
	rs := decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.configureReplication", p))
	if rs.IORunning != "No" || rs.SQLRunning != "No" || rs.SourcePort != master.Port {
		t.Errorf("configure did not stop and re-point: %+v", rs)
	}
	if r := dispatch(t, h, "mysql.startReplica", nil); r.OK || !strings.Contains(r.Error.Message, "user replikasi ditolak master; periksa password/hak") ||
		!strings.Contains(r.Error.Message, "1045") {
		t.Errorf("start with a wrong password = %+v", r)
	}

	execAll(t, mdb, "CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "INSERT INTO a.t VALUES (1)")
	rs = decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.configureReplication", sourceParams(master)))
	if len(rs.Filters) != 0 {
		t.Errorf("filters = %q, want none", rs.Filters)
	}
	for range 2 { // starting a running replica is harmless
		rs = decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.startReplica", nil))
		if rs.SQLRunning != "Yes" || rs.LastIOErrno != 0 {
			t.Errorf("after startReplica: %+v", rs)
		}
	}
	execAll(t, mdb, "CREATE DATABASE b", "CREATE TABLE b.t (id INT)", "INSERT INTO a.t VALUES (2)")
	res := synced(t, master, replica)
	if res.Status.IORunning != "Yes" || res.Status.SourceHost != "127.0.0.1" {
		t.Errorf("status = %+v", res.Status)
	}
	if got := databases(t, rdb); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("replica databases = %q", got)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 2 {
		t.Errorf("a.t rows = %d", n)
	}

	for range 2 { // stopping a stopped replica is harmless
		rs = decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.stopReplica", nil))
		if rs.IORunning != "No" || rs.SQLRunning != "No" {
			t.Errorf("after stopReplica: %+v", rs)
		}
	}
}

// TestSetReplicationFilterExpands adds a database to a running filtered
// replica without touching its source; _ in a name matches only itself.
func TestSetReplicationFilterExpands(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb, rdb := rootDB(t, master), rootDB(t, replica)
	replUser(t, master)
	h := replica.Host()
	execAll(t, mdb, "CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "CREATE DATABASE b", "CREATE TABLE b.t (id INT)")
	follow(t, master, replica, "a")
	synced(t, master, replica)

	rs := decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.setReplicationFilter",
		proto.ReplicationFilterParams{Databases: []string{"a", "x_y"}}))
	if want := []string{"a.%", `x\_y.%`}; !slices.Equal(rs.Filters, want) {
		t.Errorf("filters = %q, want %q", rs.Filters, want)
	}
	if rs.SQLRunning != "Yes" || rs.SourcePort != master.Port || rs.SourceHost != "127.0.0.1" {
		t.Errorf("after setReplicationFilter: %+v", rs)
	}
	execAll(t, mdb,
		"INSERT INTO a.t VALUES (1)",
		"CREATE DATABASE x_y", "CREATE TABLE x_y.t (id INT)", "INSERT INTO x_y.t VALUES (1)",
		"CREATE DATABASE xzy", "CREATE TABLE xzy.t (id INT)", "INSERT INTO b.t VALUES (1)",
	)
	synced(t, master, replica)
	if got := databases(t, rdb); !slices.Equal(got, []string{"a", "x_y"}) {
		t.Errorf("replica databases = %q, want a and x_y", got)
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM x_y.t"); n != 1 {
		t.Errorf("x_y.t rows = %d", n)
	}
	if g := globalFilters(t, replica); g != `a.%,x\_y.%` {
		t.Errorf("global filter = %q", g)
	}

	// Clearing the filter replicates everything from then on.
	rs = decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.setReplicationFilter", proto.ReplicationFilterParams{}))
	if len(rs.Filters) != 0 || globalFilters(t, replica) != "" {
		t.Errorf("filters after clearing = %q / %q", rs.Filters, globalFilters(t, replica))
	}
	execAll(t, mdb, "CREATE DATABASE c")
	synced(t, master, replica)
	if got := databases(t, rdb); !slices.Equal(got, []string{"a", "c", "x_y"}) {
		t.Errorf("replica databases = %q", got)
	}
}

func TestStartUntilStopsAtGTID(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb, rdb := rootDB(t, master), rootDB(t, replica)
	replUser(t, master)
	h := replica.Host()
	execAll(t, mdb, "CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "INSERT INTO a.t VALUES (1)")
	until := gtidExecuted(t, mdb)
	execAll(t, mdb, "INSERT INTO a.t VALUES (2)", "INSERT INTO a.t VALUES (3)")

	decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.configureReplication", sourceParams(master)))
	for range 2 { // the second run finds the point already reached
		rs := decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.startUntil",
			proto.StartUntilParams{GTIDSet: until, TimeoutSec: 60}))
		if rs.SQLRunning != "No" || rs.LastSQLErrno != 0 {
			t.Errorf("after startUntil: %+v", rs)
		}
		if rs.IORunning != "Yes" && rs.IORunning != "Connecting" {
			t.Errorf("receiver after startUntil = %q, want it still running", rs.IORunning)
		}
		if got := gtidExecuted(t, rdb); got != until {
			t.Errorf("replica gtid_executed = %q, want %q", got, until)
		}
		if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 1 {
			t.Errorf("a.t rows = %d, want 1", n)
		}
	}

	follow(t, master, replica)
	synced(t, master, replica)
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 3 {
		t.Errorf("a.t rows after startReplica = %d, want 3", n)
	}

	// A point the master never reaches times out with what was executed.
	server := strings.SplitN(until, ":", 2)[0]
	r := dispatch(t, h, "mysql.startUntil", proto.StartUntilParams{GTIDSet: server + ":1-100000", TimeoutSec: 1})
	if r.OK || !strings.Contains(r.Error.Message, gtidExecuted(t, rdb)) {
		t.Errorf("startUntil past the master = %+v", r)
	}
}

func TestResetReplicaClears(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb, rdb := rootDB(t, master), rootDB(t, replica)
	replUser(t, master)
	h := replica.Host()
	execAll(t, mdb, "CREATE DATABASE a", "CREATE TABLE a.t (id INT)", "INSERT INTO a.t VALUES (1)")
	follow(t, master, replica, "a")
	synced(t, master, replica)

	for range 2 { // resetting a server that is not a replica is harmless
		if r := dispatch(t, h, "mysql.resetReplica", nil); !r.OK {
			t.Fatalf("resetReplica = %+v", r)
		}
		if st := status(t, h); st.Replica != nil {
			t.Errorf("replica status after reset = %+v", st.Replica)
		}
		if g := globalFilters(t, replica); g != "" {
			t.Errorf("global filter after reset = %q", g)
		}
	}
	if n := count(t, rdb, "SELECT COUNT(*) FROM a.t"); n != 1 {
		t.Errorf("a.t rows after reset = %d, want the data kept", n)
	}
	for _, cmd := range []string{"mysql.startReplica", "mysql.setReplicationFilter", "mysql.startUntil"} {
		var params any
		if cmd == "mysql.startUntil" {
			params = proto.StartUntilParams{GTIDSet: gtidExecuted(t, mdb), TimeoutSec: 5}
		}
		if r := dispatch(t, h, cmd, params); r.OK || r.Error.Code != proto.CodeBadParams ||
			!strings.Contains(r.Error.Message, "belum dikonfigurasi") {
			t.Errorf("%s after reset = %+v", cmd, r)
		}
	}
	if rs := decode[*proto.ReplicaStatus](t, dispatch(t, h, "mysql.stopReplica", nil)); rs != nil {
		t.Errorf("stopReplica without a source = %+v", rs)
	}
}

func TestWaitSyncedTimesOut(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	replUser(t, master)
	h := replica.Host()
	follow(t, master, replica)
	synced(t, master, replica)

	// A set the master has not written yet: the applier runs, so the wait
	// lasts the whole timeout.
	server := strings.SplitN(gtidExecuted(t, mdb), ":", 2)[0]
	start := time.Now()
	res := decode[proto.WaitSyncedResult](t, dispatch(t, h, "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: server + ":1-100000", TimeoutSec: 2}))
	if took := time.Since(start); res.Synced || took < 2*time.Second || took > 10*time.Second {
		t.Errorf("waitSynced past the master = %+v after %v", res, took)
	}
	if res.Status.SQLRunning != "Yes" || res.Executed == "" {
		t.Errorf("result = %+v", res)
	}

	// A stopped applier cannot catch up: the answer comes at once.
	decode[proto.ReplicaStatus](t, dispatch(t, h, "mysql.stopReplica", nil))
	execAll(t, mdb, "CREATE DATABASE late")
	start = time.Now()
	res = decode[proto.WaitSyncedResult](t, dispatch(t, h, "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: gtidExecuted(t, mdb), TimeoutSec: 60}))
	if took := time.Since(start); res.Synced || took > 5*time.Second {
		t.Errorf("waitSynced with the applier stopped = %+v after %v", res, took)
	}
	if res.Status.SQLRunning != "No" {
		t.Errorf("status = %+v", res.Status)
	}

	// A set already executed is synced even with the applier stopped.
	res = decode[proto.WaitSyncedResult](t, dispatch(t, h, "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: res.Executed, TimeoutSec: 5}))
	if !res.Synced {
		t.Errorf("waitSynced on its own set = %+v", res)
	}

	// A receiver stopped on a fatal error (here: the master's server_id)
	// fetches nothing more: the answer comes at once too.
	rdb := rootDB(t, replica)
	execAll(t, rdb, fmt.Sprintf("SET GLOBAL server_id = %d", master.ServerID))
	dispatch(t, h, "mysql.startReplica", nil) // may answer before the receiver fails
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if rs := status(t, h).Replica; rs != nil && rs.IORunning == "No" && rs.LastIOErrno != 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the receiver did not fail on the master's server_id: %+v", rs)
		}
	}
	start = time.Now()
	res = decode[proto.WaitSyncedResult](t, dispatch(t, h, "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: gtidExecuted(t, mdb), TimeoutSec: 60}))
	if took := time.Since(start); res.Synced || took > 5*time.Second {
		t.Errorf("waitSynced with the receiver failed = %+v after %v", res, took)
	}
	if res.Status.IORunning != "No" || res.Status.LastIOErrno == 0 {
		t.Errorf("status = %+v", res.Status)
	}
}

// TestConfigureRefusedKeepsReplica: MySQL refuses SOURCE_AUTO_POSITION
// while gtid_mode is OFF (error 1777), and the replica keeps its source
// and filter.
func TestConfigureRefusedKeepsReplica(t *testing.T) {
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: gtidOff})
	h := replica.Host()
	oldPort := closedPort(t)
	execAll(t, rootDB(t, replica),
		fmt.Sprintf("CHANGE REPLICATION SOURCE TO SOURCE_HOST = '127.0.0.1', SOURCE_PORT = %d, SOURCE_USER = 'old', SOURCE_PASSWORD = 'x'", oldPort),
		"CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = ('a.%')",
	)

	p := proto.ReplicationSourceParams{Host: "127.0.0.2", Port: oldPort + 1, User: "dbpanel_repl", Password: replPassword,
		Databases: []string{"b"}}
	r := dispatch(t, h, "mysql.configureReplication", p)
	if r.OK || !strings.Contains(r.Error.Message, "1777") {
		t.Fatalf("configure with gtid_mode OFF = %+v", r)
	}
	if strings.Contains(r.Error.Message, replPassword) {
		t.Errorf("error shows the password: %s", r.Error.Message)
	}
	rs := status(t, h).Replica
	if rs == nil || rs.SourceHost != "127.0.0.1" || rs.SourcePort != oldPort {
		t.Errorf("source after the refused configure = %+v", rs)
	} else if !slices.Equal(rs.Filters, []string{"a.%"}) {
		t.Errorf("filters after the refused configure = %q, want the old a.%%", rs.Filters)
	}
	if g := globalFilters(t, replica); g != "a.%" {
		t.Errorf("global filter = %q, want the old a.%%", g)
	}
}

// TestDestructiveCommandsRefusedOnSource: on a server replicas read from,
// the agent itself refuses every command that would drop its data, replace
// it, point it at another source, reset its replication or make it
// writable, whatever the panel asks.
func TestDestructiveCommandsRefusedOnSource(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	// The replica follows over TLS with an account that requires it.
	if err := mysql.CreateReplUser(context.Background(), master.Host(),
		proto.ReplUserParams{User: "dbpanel_repl", Password: replPassword, RequireSSL: true}); err != nil {
		t.Fatal(err)
	}
	execAll(t, mdb, "CREATE DATABASE keep", "CREATE TABLE keep.t (id INT PRIMARY KEY)", "INSERT INTO keep.t VALUES (1)")
	src := sourceParams(master)
	src.SSL = true
	decode[proto.ReplicaStatus](t, dispatch(t, replica.Host(), "mysql.configureReplication", src))
	decode[proto.ReplicaStatus](t, dispatch(t, replica.Host(), "mysql.startReplica", nil))
	synced(t, master, replica)
	var cipher string
	if err := rootDB(t, replica).QueryRow(`SELECT COALESCE(MAX(SSL_ALLOWED), '') FROM performance_schema.replication_connection_configuration`).
		Scan(&cipher); err != nil || !strings.EqualFold(cipher, "yes") {
		t.Errorf("replication connection SSL_ALLOWED = %q, %v; want Yes", cipher, err)
	}
	const refused = "replika sedang membaca binlog server ini"

	h := master.Host()
	for _, c := range []struct {
		cmd    string
		params any
	}{
		{"mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"keep"}}},
		{"mysql.resetReplica", nil},
		{"mysql.configureReplication", sourceParams(replica)},
		{"mysql.setReadOnly", proto.ReadOnlyParams{On: false}},
	} {
		if r := dispatch(t, h, c.cmd, c.params); r.OK || !strings.Contains(r.Error.Message, refused) {
			t.Errorf("%s on a source = %+v, want refused", c.cmd, r)
		}
	}
	rig := rootRig(master)
	other := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replUser(t, other)
	if d := rig.run(t, "mysql.cloneFrom", cloneParams(other)); d.OK || !strings.Contains(d.Error, refused) {
		t.Errorf("cloneFrom on a source = %+v, want refused", d)
	}
	dp := dumpParams(other, "keep")
	dp.DropExisting = true
	if d := rig.run(t, "mysql.dumpFrom", dp); d.OK || !strings.Contains(d.Error, refused) {
		t.Errorf("dumpFrom DropExisting on a source = %+v, want refused", d)
	}
	if n := count(t, mdb, "SELECT COUNT(*) FROM keep.t"); n != 1 {
		t.Errorf("keep.t rows on the master = %d, want 1", n)
	}
	// Making a source read-only is not destructive and still allowed.
	if r := dispatch(t, h, "mysql.setReadOnly", proto.ReadOnlyParams{On: true}); !r.OK {
		t.Errorf("setReadOnly on = %+v", r)
	}
}
