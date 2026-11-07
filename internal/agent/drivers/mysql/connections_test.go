package mysql_test

import (
	"database/sql"
	"fmt"
	"net"
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
// leaves out the panel's accounts, a replica reading the binary log under
// an account of its own name, the event scheduler and MySQL's own threads;
// on the replica, its replication threads.
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
	var app []proto.ConnectionRow
	for i, r := range res.Rows {
		switch {
		case strings.HasPrefix(r.User, "dbpanel_"), r.User == "salin", r.User == "event_scheduler", r.User == "":
			t.Errorf("row %+v should be hidden", r)
		case strings.Contains(r.Host, ":"):
			t.Errorf("row %+v: host keeps its port", r)
		case r.User == "app":
			app = append(app, r)
		}
		if i > 0 && (res.Rows[i-1].Count < r.Count || (res.Rows[i-1].Count == r.Count && res.Rows[i-1].Host > r.Host)) {
			t.Errorf("rows not sorted by count desc then host: %+v", res.Rows)
		}
	}
	// On the replica, its receiver, applier and workers are not
	// connections, and the test's root session is the agent's account.
	rreg := commands.NewRegistry()
	(&mysql.Driver{Host: replica.Host()}).Register(rreg)
	rres := decode[proto.ConnectionsResult](t, rreg.Dispatch(t.Context(), proto.Request{ID: "1", Cmd: "mysql.connections"}))
	if len(rres.Rows) != 0 {
		t.Errorf("replica rows = %+v, want none", rres.Rows)
	}
	if len(app) != 2 || app[0].DB != "toko" || app[0].Count != 3 || app[1].DB != "gudang" || app[1].Count != 1 || app[0].Host != app[1].Host {
		t.Errorf("app rows = %+v, want toko x3 then gudang x1 from one host", app)
	}
}

// TestConnectionsHidesAgentAccount: the agent's own sessions (its
// monitoring session included) and every other session of the account it
// logs in as that comes from the server itself (socket or loopback) are
// not application connections; that account's sessions from another host
// are listed, marked as the panel's admin account; another admin
// account's are listed unmarked.
func TestConnectionsHidesAgentAccount(t *testing.T) {
	ip := nonLoopbackIPv4(t)
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"bind-address=0.0.0.0", "skip-name-resolve=ON"}})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE DATABASE toko",
		"CREATE USER 'ops'@'%' IDENTIFIED BY 'Ops-pw-1'", "GRANT ALL ON *.* TO 'ops'@'%'",
		"CREATE USER 'root'@'%' IDENTIFIED BY 'Root-pw-1'", "GRANT ALL ON *.* TO 'root'@'%'")
	// The agent's account (root, what Host() logs in as): a long-lived
	// session over the socket like the agent's monitor, plus a person's on
	// it over TCP from the server itself.
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

	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host()}).Register(reg)
	res := decode[proto.ConnectionsResult](t, reg.Dispatch(t.Context(), proto.Request{ID: "1", Cmd: "mysql.connections"}))
	want := []proto.ConnectionRow{
		{Host: ip, User: "root", DB: "toko", Count: 3, AdminAccount: true},
		{Host: "127.0.0.1", User: "ops", DB: "toko", Count: 2},
	}
	if len(res.Rows) != len(want) || res.Rows[0] != want[0] || res.Rows[1] != want[1] {
		t.Fatalf("rows = %+v, want %+v", res.Rows, want)
	}
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
