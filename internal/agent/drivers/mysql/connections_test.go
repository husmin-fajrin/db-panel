package mysql_test

import (
	"database/sql"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// openAs opens n connections of user to inst over TCP with db as default
// database, kept open until the test ends.
func openAs(t *testing.T, inst *testmysql.Instance, user, password, db string, n int) {
	t.Helper()
	pool, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(127.0.0.1:%d)/%s?tls=preferred", user, password, inst.Port, db))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	for i := 0; i < n; i++ {
		c, err := pool.Conn(t.Context())
		if err != nil {
			t.Fatalf("connect as %s: %v", user, err)
		}
		t.Cleanup(func() { c.Close() })
	}
}

// TestConnectionsHidesPanelAndReplicationThreads: mysql.connections groups
// the client connections by host, user and database, most first, and
// leaves out the agent's replication and dump accounts, a replica reading
// the binary log under an account of its own name, the event scheduler and
// MySQL's own threads; on the replica, its replication threads. The rows
// are typed and the result is complete.
func TestConnectionsHidesPanelAndReplicationThreads(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	execAll(t, mdb,
		"CREATE DATABASE toko", "CREATE DATABASE gudang",
		"CREATE USER 'app'@'%' IDENTIFIED BY 'App-pw-1'", "GRANT ALL ON *.* TO 'app'@'%'",
		"CREATE USER 'dbpanel_dump_0a1b2c3d'@'%' IDENTIFIED BY 'Dump-pw-1'", "GRANT SELECT ON *.* TO 'dbpanel_dump_0a1b2c3d'@'%'",
		"CREATE USER 'salin'@'%' IDENTIFIED BY 'Salin-pw-1'", "GRANT REPLICATION SLAVE ON *.* TO 'salin'@'%'",
		"SET GLOBAL event_scheduler = ON")
	// A replica under an account that is not the panel's.
	execAll(t, rootDB(t, replica),
		fmt.Sprintf("CHANGE REPLICATION SOURCE TO SOURCE_HOST='127.0.0.1', SOURCE_PORT=%d, SOURCE_USER='salin', "+
			"SOURCE_PASSWORD='Salin-pw-1', SOURCE_SSL=1, SOURCE_AUTO_POSITION=1", master.Port),
		"START REPLICA")
	waitBinlogDump(t, mdb)
	openAs(t, master, "app", "App-pw-1", "toko", 3)
	openAs(t, master, "app", "App-pw-1", "gudang", 1)
	openAs(t, master, "dbpanel_dump_0a1b2c3d", "Dump-pw-1", "toko", 2)

	at := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: master.Host(), Now: func() time.Time { return at }}).Register(reg)
	res := decode[proto.ConnectionsResult](t, reg.Dispatch(t.Context(), proto.Request{ID: "1", Cmd: "mysql.connections"}))
	if !res.TakenAt.Equal(at) {
		t.Errorf("TakenAt = %v, want %v", res.TakenAt, at)
	}
	if !res.Typed || res.Source != "performance_schema" || res.Unreadable != "" || res.Incomplete != "" || res.LocalAdmin != 0 || res.AgentIndistinct {
		t.Errorf("result flags = %+v, want typed, performance_schema, complete, no local admin", res)
	}
	var app []proto.ConnectionRow
	for i, r := range res.Rows {
		switch {
		case strings.HasPrefix(r.User, "dbpanel_"), r.User == "salin", r.User == "event_scheduler", r.User == "system user":
			t.Errorf("row %+v should be hidden", r)
		case strings.Contains(r.Host, ":"):
			t.Errorf("row %+v: host keeps its port", r)
		case r.User == "app":
			app = append(app, r)
		case r.User == "root":
			t.Errorf("row %+v: the agent's own tagged session should be hidden", r)
		}
		if r.Kind != "sql" {
			t.Errorf("row %+v: Kind = %q, want sql", r, r.Kind)
		}
		if i > 0 && (res.Rows[i-1].Count < r.Count || (res.Rows[i-1].Count == r.Count && res.Rows[i-1].Host > r.Host)) {
			t.Errorf("rows not sorted by count desc then host: %+v", res.Rows)
		}
	}
	// On the replica, the receiver, applier and workers are not
	// connections: performance_schema lists them under the account that ran
	// START REPLICA (root, the agent's), so they are excluded by their
	// instrument names. The test's own sessions are the agent's (tagged).
	rres := readConns(t, replica)
	if len(rres.Rows) != 0 || rres.LocalAdmin != 0 {
		t.Errorf("replica result = %+v, want no rows and no local admin", rres)
	}
	// An untagged local session of the agent's account on a running replica
	// is still counted: the exclusion is by thread name only.
	plain, err := sql.Open("mysql", "root@unix("+replica.Socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plain.Close() })
	pc, err := plain.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if rres := readConns(t, replica); len(rres.Rows) != 1 || !rres.Rows[0].LocalAdmin || rres.Rows[0].Kind != "sql" || rres.LocalAdmin != 1 {
		t.Errorf("replica with an untagged root session = %+v, want one local admin sql row", rres)
	}
	if len(app) != 2 || app[0].DB != "toko" || app[0].Count != 3 || app[1].DB != "gudang" || app[1].Count != 1 || app[0].Host != app[1].Host {
		t.Errorf("app rows = %+v, want toko x3 then gudang x1 from one host", app)
	}
}

// TestConnectionsHidesAgentAccount: the agent's own sessions (tagged
// program_name=dbpanel-agent, its monitoring session included) from the
// server itself are not application connections; a session of the same
// account from the server itself without the tag (a person on the server)
// is counted as a local admin session; that account's sessions from
// another host are listed, marked as the panel's admin account; another
// admin account's are listed unmarked.
func TestConnectionsHidesAgentAccount(t *testing.T) {
	ip := nonLoopbackIPv4(t)
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"bind-address=0.0.0.0", "skip-name-resolve=ON"}})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE DATABASE toko",
		"CREATE USER 'ops'@'%' IDENTIFIED BY 'Ops-pw-1'", "GRANT ALL ON *.* TO 'ops'@'%'",
		"CREATE USER 'root'@'%' IDENTIFIED BY 'Root-pw-1'", "GRANT ALL ON *.* TO 'root'@'%'")
	// The agent's account (root, what Host() logs in as): long-lived
	// tagged sessions over the socket like the agent's monitor, plus a
	// person's on it over TCP from the server itself (no tag).
	for i := 0; i < 2; i++ {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if _, err := c.ExecContext(t.Context(), "USE toko"); err != nil {
			t.Fatal(err)
		}
	}
	openAs(t, inst, "root", "Root-pw-1", "toko", 1)
	openAs(t, inst, "ops", "Ops-pw-1", "toko", 2)
	// The same account from another host: an application sharing it.
	pool, err := sql.Open("mysql", fmt.Sprintf("root:Root-pw-1@tcp(%s:%d)/toko?tls=preferred", ip, inst.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	for i := 0; i < 3; i++ {
		c, err := pool.Conn(t.Context())
		if err != nil {
			t.Fatalf("connect as root from %s: %v", ip, err)
		}
		t.Cleanup(func() { c.Close() })
	}

	res := readConns(t, inst)
	want := []proto.ConnectionRow{
		{Host: ip, User: "root", DB: "toko", Count: 3, AdminAccount: true, Kind: "sql"},
		{Host: "127.0.0.1", User: "ops", DB: "toko", Count: 2, Kind: "sql"},
		{Host: "127.0.0.1", User: "root", DB: "toko", Count: 1, Kind: "sql", LocalAdmin: true},
	}
	if !slices.Equal(res.Rows, want) {
		t.Fatalf("rows = %+v, want %+v", res.Rows, want)
	}
	if res.LocalAdmin != 1 || res.AgentIndistinct {
		t.Errorf("LocalAdmin = %d, AgentIndistinct = %v; want 1, false", res.LocalAdmin, res.AgentIndistinct)
	}
}

// readConns runs mysql.connections against inst.
func readConns(t *testing.T, inst *testmysql.Instance) proto.ConnectionsResult {
	t.Helper()
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host()}).Register(reg)
	return decode[proto.ConnectionsResult](t, reg.Dispatch(t.Context(), proto.Request{ID: "1", Cmd: "mysql.connections"}))
}

// TestConnectionsIdleGTIDServerCompleteAndEmpty: a GTID server nobody but
// the agent is connected to (two of its sessions kept open) gives a
// complete, typed and empty list: MySQL's own threads such as
// compress_gtid_table are not connections.
func TestConnectionsIdleGTIDServerCompleteAndEmpty(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	h := inst.Host()
	for i := 0; i < 2; i++ {
		db, err := h.Open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
	}
	res := readConns(t, inst)
	if len(res.Rows) != 0 || !res.Typed || res.Unreadable != "" || res.Incomplete != "" || res.LocalAdmin != 0 || res.Source != "performance_schema" {
		t.Errorf("result = %+v, want complete, typed and empty", res)
	}
	if res.Rows == nil {
		t.Error("Rows is nil: it must encode as []")
	}
}

// TestConnectionsPerformanceSchemaOff: without performance_schema the
// agent reads information_schema.PROCESSLIST: application sessions are
// counted (kind "lain", it has no thread name), and the agent cannot tell
// its own sessions from a person's on the server, so local sessions of its
// account are counted and flagged.
func TestConnectionsPerformanceSchemaOff(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"--performance-schema=OFF"}})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE DATABASE toko",
		"CREATE USER 'app'@'%' IDENTIFIED BY 'App-pw-1'", "GRANT ALL ON *.* TO 'app'@'%'")
	openAs(t, inst, "app", "App-pw-1", "toko", 2)
	// The agent's own session over the socket, held open.
	c, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	res := readConns(t, inst)
	if res.Source != "information_schema" || !res.Typed || res.Unreadable != "" {
		t.Fatalf("result = %+v, want typed information_schema", res)
	}
	var app []proto.ConnectionRow
	local := 0
	for _, r := range res.Rows {
		if r.Kind != "lain" {
			t.Errorf("row %+v: Kind = %q, want lain", r, r.Kind)
		}
		switch {
		case r.User == "app":
			app = append(app, r)
		case r.LocalAdmin:
			local += r.Count
		default:
			t.Errorf("unexpected row %+v", r)
		}
	}
	if len(app) != 1 || app[0].Count != 2 || app[0].DB != "toko" {
		t.Errorf("app rows = %+v, want one group of 2 in toko", app)
	}
	if res.LocalAdmin == 0 || res.LocalAdmin != local || !res.AgentIndistinct {
		t.Errorf("LocalAdmin = %d (rows %d), AgentIndistinct = %v; want > 0, equal, true", res.LocalAdmin, local, res.AgentIndistinct)
	}
}

// TestConnectionsUnreadableWhenNoSourceReads: when neither performance_schema
// nor information_schema can be read, the result says so (Unreadable), with
// no rows, and it is not an error.
func TestConnectionsUnreadableWhenNoSourceReads(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // every query on it now fails
	res := mysql.ReadConnections(t.Context(), db)
	if res.Unreadable == "" || len(res.Rows) != 0 || res.Rows == nil || !res.Typed || res.Source != "" {
		t.Errorf("result = %+v, want unreadable and no rows", res)
	}
}

// TestConnectionsIncompleteWhenThreadsLost: a performance_schema that
// dropped threads (Performance_schema_thread_instances_lost > 0) makes the
// result incomplete, with the reason.
func TestConnectionsIncompleteWhenThreadsLost(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"--performance-schema-max-thread-instances=" + strconv.Itoa(lostThreadsLimit)}})
	db := rootDB(t, inst)
	for i := 0; i < 5; i++ {
		openAs(t, inst, "root", "", "", 1)
	}
	var name string
	var lost int
	if err := db.QueryRow("SHOW GLOBAL STATUS LIKE 'Performance_schema_thread_instances_lost'").Scan(&name, &lost); err != nil {
		t.Fatal(err)
	}
	if lost == 0 {
		t.Skip("the server did not lose threads with this limit")
	}
	res := readConns(t, inst)
	if res.Incomplete != "sebagian thread tidak tercatat di performance_schema" || res.Unreadable != "" {
		t.Errorf("result = %+v, want incomplete", res)
	}
}

// TestConnKindFromThreadName: the kind comes from the thread name; a
// simulated X Protocol worker thread (the test server runs with mysqlx=OFF
// and the repo has no X client) of user app is counted, not filtered out.
func TestConnKindFromThreadName(t *testing.T) {
	for name, want := range map[string]string{
		"thread/sql/one_connection":      "sql",
		"thread/mysqlx/worker":           "x",
		"thread/sql/compress_gtid_table": "lain",
		"":                               "lain",
	} {
		if got := mysql.ConnKind(name); got != want {
			t.Errorf("ConnKind(%q) = %q, want %q", name, got, want)
		}
	}
	rows, local := mysql.GroupConnections([]mysql.ConnThread{
		{Name: "thread/mysqlx/worker", Host: "10.0.0.5", User: "app", DB: "toko"},
		{Name: "thread/sql/one_connection", Host: "10.0.0.5", User: "app", DB: "toko"},
		{Name: "thread/sql/one_connection", Host: "10.0.0.5", User: "app", DB: "toko"},
	}, false)
	want := []proto.ConnectionRow{
		{Host: "10.0.0.5", User: "app", DB: "toko", Count: 2, Kind: "sql"},
		{Host: "10.0.0.5", User: "app", DB: "toko", Count: 1, Kind: "x"},
	}
	if !slices.Equal(rows, want) || local != 0 {
		t.Errorf("rows = %+v (local %d), want %+v", rows, local, want)
	}
}

// TestGroupConnectionsHidesOnlyWhatIsAllowed: the only sessions left out
// are the service accounts (any host) and, with performance_schema, the
// agent account's tagged sessions from the server itself. Everything else
// is counted.
func TestGroupConnectionsHidesOnlyWhatIsAllowed(t *testing.T) {
	const sql = "thread/sql/one_connection"
	rows, local := mysql.GroupConnections([]mysql.ConnThread{
		{Name: sql, Host: "", User: "root", AgentAcct: true, Tagged: true},                 // hidden: agent's own
		{Name: sql, Host: "localhost", User: "root", AgentAcct: true, Tagged: true},        // hidden
		{Name: sql, Host: "::ffff:127.0.0.1", User: "root", AgentAcct: true, Tagged: true}, // hidden
		{Name: sql, Host: "", User: "root", AgentAcct: true},                               // local admin
		{Name: sql, Host: "10.0.0.9", User: "root", AgentAcct: true, Tagged: true},         // tag from a remote host: admin account
		{Name: sql, Host: "127.0.0.1", User: "app", Tagged: true},                          // another account tagged: counted
		{Name: sql, Host: "10.0.0.9", User: "dbpanel_repl"},                                // hidden: service
		{Name: sql, Host: "10.0.0.9", User: "dbpanel_dump_0123abcd"},                       // hidden: service
		{Name: sql, Host: "10.0.0.9", User: "dbpanel_dump_0123abcdef"},                     // not the pattern: counted
		{Name: sql, Host: "10.0.0.9", User: "dbpanel_admin"},                               // admin account
		{Name: "", Host: "10.0.0.9", User: "unauthenticated user"},                         // counted
		{Name: "thread/sql/replica_worker_x", Host: "10.0.0.8", User: "app"},               // not an excluded instrument: counted
	}, false)
	got := map[string]proto.ConnectionRow{}
	for _, r := range rows {
		got[r.Host+"|"+r.User] = r
	}
	if len(rows) != 7 || local != 1 {
		t.Errorf("rows = %+v, local = %d; want 7 rows and 1 local admin session", rows, local)
	}
	if r := got["10.0.0.8|app"]; r.Count != 1 || r.Kind != "lain" {
		t.Errorf("row of an unlisted replica-like instrument = %+v, want counted as lain", r)
	}
	if r := got["|root"]; !r.LocalAdmin || r.Count != 1 {
		t.Errorf("untagged local row = %+v, want local admin", r)
	}
	if r := got["10.0.0.9|root"]; !r.AdminAccount || r.LocalAdmin {
		t.Errorf("remote agent-account row = %+v, want admin account", r)
	}
	if r := got["10.0.0.9|dbpanel_admin"]; !r.AdminAccount {
		t.Errorf("dbpanel_admin row = %+v, want admin account", r)
	}
	// Without performance_schema nothing is tagged: all local sessions of
	// the agent's account are counted.
	rows, local = mysql.GroupConnections([]mysql.ConnThread{
		{Host: "localhost", User: "root", AgentAcct: true, Tagged: true},
		{Host: "localhost", User: "root", AgentAcct: true},
	}, true)
	if local != 2 || len(rows) != 1 || rows[0].Count != 2 || !rows[0].LocalAdmin {
		t.Errorf("indistinct rows = %+v, local = %d; want one local admin row of 2", rows, local)
	}
}

// TestConnectionsShowsDbpanelAdminFromOtherHost: dbpanel_admin is no longer
// hidden: from a non-loopback address it is listed as the panel's admin
// account, while the replication and dump accounts stay hidden and another
// dbpanel_ name is listed.
func TestConnectionsShowsDbpanelAdminFromOtherHost(t *testing.T) {
	ip := nonLoopbackIPv4(t)
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"bind-address=0.0.0.0", "skip-name-resolve=ON"}})
	execAll(t, rootDB(t, inst), "CREATE DATABASE toko")
	for _, u := range []string{"dbpanel_admin", "dbpanel_repl", "dbpanel_dump_0123abcd", "dbpanel_lain"} {
		execAll(t, rootDB(t, inst), "CREATE USER '"+u+"'@'%' IDENTIFIED BY 'Pw-1-"+u+"'", "GRANT ALL ON *.* TO '"+u+"'@'%'")
		pool, err := sql.Open("mysql", fmt.Sprintf("%s:Pw-1-%s@tcp(%s:%d)/toko?tls=preferred", u, u, ip, inst.Port))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Close() })
		c, err := pool.Conn(t.Context())
		if err != nil {
			t.Fatalf("connect as %s: %v", u, err)
		}
		t.Cleanup(func() { c.Close() })
	}
	res := readConns(t, inst)
	want := []proto.ConnectionRow{
		{Host: ip, User: "dbpanel_admin", DB: "toko", Count: 1, AdminAccount: true, Kind: "sql"},
		{Host: ip, User: "dbpanel_lain", DB: "toko", Count: 1, Kind: "sql"},
	}
	if !slices.Equal(res.Rows, want) {
		t.Errorf("rows = %+v, want %+v", res.Rows, want)
	}
}

// TestConnectionsUntaggedLocalAgentAccountCounted: a root session over the
// socket made with a DSN without the attribute is a local admin session;
// the agent's own session (Host.Open) is not listed.
func TestConnectionsUntaggedLocalAgentAccountCounted(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	agent, err := inst.Host().Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Close() })
	plain, err := sql.Open("mysql", "root@unix("+inst.Socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plain.Close() })
	c, err := plain.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	res := readConns(t, inst)
	if len(res.Rows) != 1 || !res.Rows[0].LocalAdmin || res.Rows[0].User != "root" || res.Rows[0].Count != 1 || res.LocalAdmin != 1 {
		t.Errorf("result = %+v, want one untagged local root session", res)
	}
}

// TestAgentConnectionsCarryProgramName: the DSN carries the connection
// attribute, and the sessions Host.Open and ReadPeer make show it in
// session_connect_attrs.
func TestAgentConnectionsCarryProgramName(t *testing.T) {
	if !strings.Contains(mysql.DSN(proto.ConnectParams{User: "root"}, "/tmp/s.sock"), "connectionAttributes=program_name%3Adbpanel-agent") {
		t.Errorf("DSN lacks the program_name attribute: %s", mysql.DSN(proto.ConnectParams{User: "root"}, "/tmp/s.sock"))
	}
	if mysql.AgentProgramName != "dbpanel-agent" {
		t.Errorf("AgentProgramName = %q", mysql.AgentProgramName)
	}
	inst := testmysql.Start(t, testmysql.Options{})
	// An observer without the tag, so any tagged session it sees is another.
	obs, err := sql.Open("mysql", "root@unix("+inst.Socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { obs.Close() })
	const tagged = `SELECT COUNT(*) FROM performance_schema.session_connect_attrs
		WHERE ATTR_NAME = 'program_name' AND ATTR_VALUE = 'dbpanel-agent' AND PROCESSLIST_ID <> CONNECTION_ID()`
	count := func() int {
		var n int
		if err := obs.QueryRowContext(t.Context(), tagged).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("%d tagged sessions before the agent connects", n)
	}
	agent, err := inst.Host().Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Close() })
	if n := count(); n != 1 {
		t.Errorf("tagged sessions after Host.Open = %d, want 1", n)
	}
	agent.Close()

	// ReadPeer's session is short: read the attribute while it lives, by
	// polling from another connection while ReadPeer runs repeatedly.
	execAll(t, rootDB(t, inst), "CREATE USER 'peer'@'%' IDENTIFIED BY "+sqlLiteral(replPassword))
	c := proto.PeerCred{Host: "127.0.0.1", Port: inst.Port, User: "peer", Password: replPassword}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if mysql.ReadPeer(t.Context(), c, 5*time.Second).Err != "" {
					return
				}
			}
		}
	}()
	defer func() { close(stop); <-done }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := obs.QueryRowContext(t.Context(), tagged+` AND PROCESSLIST_ID IN (SELECT PROCESSLIST_ID FROM performance_schema.threads WHERE PROCESSLIST_USER = 'peer')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("never saw a ReadPeer session with program_name=dbpanel-agent")
}

// nonLoopbackIPv4 is an IPv4 address of this host other than loopback;
// the test is skipped without one.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address to connect from")
	return ""
}

// lostThreadsLimit is a performance_schema_max_thread_instances small
// enough that the server's own threads and a few sessions overflow it.
const lostThreadsLimit = 5

// midLogin holds a TCP connection to inst that never answers the server's
// greeting, until the test ends, and waits until the server has a thread for
// it. The server keeps it for connect_timeout.
func midLogin(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inst.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	obs, err := sql.Open("mysql", "root@unix("+inst.Socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { obs.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := obs.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = 'unauthenticated user'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never listed the half-open connection")
}

// TestConnectionsCountsSessionMidLogin: a connection that has not finished
// logging in (the server shows it as 'unauthenticated user') is a
// connection, from either source.
func TestConnectionsCountsSessionMidLogin(t *testing.T) {
	for name, args := range map[string][]string{
		"performance_schema": nil,
		"information_schema": {"--performance-schema=OFF"},
	} {
		t.Run(name, func(t *testing.T) {
			inst := testmysql.Start(t, testmysql.Options{ExtraArgs: args})
			midLogin(t, inst)
			if name == "performance_schema" {
				var tn, user, host, cmd sql.NullString
				db := rootDB(t, inst)
				if err := db.QueryRow(`SELECT NAME, PROCESSLIST_USER, PROCESSLIST_HOST, PROCESSLIST_COMMAND FROM performance_schema.threads
					WHERE PROCESSLIST_USER = 'unauthenticated user' OR (PROCESSLIST_USER IS NULL AND NAME = 'thread/sql/one_connection')`).Scan(&tn, &user, &host, &cmd); err != nil {
					t.Fatal(err)
				}
				t.Logf("performance_schema thread: NAME=%v USER=%v HOST=%v COMMAND=%v", tn, user, host, cmd)
			}
			if name == "information_schema" {
				var user, host, cmd sql.NullString
				if err := rootDB(t, inst).QueryRow(`SELECT USER, HOST, COMMAND FROM information_schema.PROCESSLIST WHERE USER = 'unauthenticated user'`).Scan(&user, &host, &cmd); err != nil {
					t.Fatal(err)
				}
				t.Logf("information_schema thread: USER=%v HOST=%v COMMAND=%v", user, host, cmd)
			}
			res := readConns(t, inst)
			if res.Source != name {
				t.Fatalf("Source = %q, want %q", res.Source, name)
			}
			var found bool
			for _, r := range res.Rows {
				if r.User == "unauthenticated user" {
					found = true
					if r.Count != 1 || (name == "performance_schema" && r.Kind != "sql") {
						t.Errorf("row %+v, want one session (kind sql from performance_schema)", r)
					}
				}
			}
			if !found {
				t.Errorf("rows = %+v, want the session mid-login counted", res.Rows)
			}
		})
	}
}

// TestConnectionsFilterByNameIsExact: the only thread names the
// performance_schema query filters on are the six replication instruments
// (an exact NOT IN list, one bound parameter each) and one_connection for a
// session mid-login. A LIKE, REGEXP or prefix on NAME would hide a session
// the agent cannot classify. (A live check would need a thread with a
// replica-like name that is not in the list, which a client cannot make.)
func TestConnectionsFilterByNameIsExact(t *testing.T) {
	want := []string{
		"thread/sql/replica_io", "thread/sql/replica_sql", "thread/sql/replica_worker",
		"thread/sql/slave_io", "thread/sql/slave_sql", "thread/sql/slave_worker",
	}
	if !slices.Equal(mysql.ReplicationThreadNames, want) {
		t.Errorf("replicationThreadNames = %v, want exactly %v", mysql.ReplicationThreadNames, want)
	}
	q := mysql.ConnectionsPSQuery()
	pattern := regexp.MustCompile(`(?i)\b(LIKE|REGEXP|RLIKE)\b`)
	for _, line := range strings.Split(q, "\n") {
		if strings.Contains(line, "t.NAME") && pattern.MatchString(line) {
			t.Errorf("the query filters NAME with a pattern: %s", strings.TrimSpace(line))
		}
	}
	if n := strings.Count(q, "?"); n != len(mysql.PSArgs()) || n != len(want)+1 {
		t.Errorf("query has %d parameters, args %d, want %d", n, len(mysql.PSArgs()), len(want)+1)
	}
}
