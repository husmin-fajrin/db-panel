package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// loginTCP reports whether user can log in with password over TCP.
func loginTCP(t *testing.T, inst *testmysql.Instance, user, password string) error {
	t.Helper()
	db, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: user, Password: password, Host: "127.0.0.1", Port: inst.Port}, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	return db.PingContext(context.Background())
}

func TestCreateReplUserIdempotent(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	var logs bytes.Buffer
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.NewTextHandler(&logs, nil))}).Register(reg)
	create := func(p proto.ReplUserParams) proto.Response {
		t.Helper()
		raw := mustJSON(t, p)
		return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createReplUser", Params: raw})
	}

	const first, second = "Pw'1\\ with quotes", "second-Secret-2"
	// An older dbpanel granted the account read and RELOAD for dumps.
	root := rootDB(t, inst)
	execAll(t, root, "CREATE USER dbpanel_repl@'%' IDENTIFIED BY 'old'",
		"GRANT REPLICATION SLAVE, BACKUP_ADMIN, SELECT, RELOAD, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT ON *.* TO dbpanel_repl@'%'")
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: first}); !r.OK {
		t.Fatalf("first create: %+v", r.Error)
	}
	if err := loginTCP(t, inst, "dbpanel_repl", first); err != nil {
		t.Errorf("login with the first password: %v", err)
	}
	// Again: no error, and the password is rotated.
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: second, FromHost: "%"}); !r.OK {
		t.Fatalf("second create: %+v", r.Error)
	}
	if err := loginTCP(t, inst, "dbpanel_repl", second); err != nil {
		t.Errorf("login with the rotated password: %v", err)
	}
	if err := loginTCP(t, inst, "dbpanel_repl", first); err == nil {
		t.Error("the old password still works")
	}

	var hosts []string
	rows, err := root.Query("SELECT host FROM mysql.user WHERE user = 'dbpanel_repl'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, h)
	}
	rows.Close()
	if len(hosts) != 1 || hosts[0] != "%" {
		t.Errorf("accounts = %q, want one at %%", hosts)
	}
	var grants []string
	rows, err = root.Query("SHOW GRANTS FOR 'dbpanel_repl'@'%'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	rows.Close()
	all := strings.Join(grants, "\n")
	// Replication and CLONE only: its password is on every replica, so it
	// must not read tables, reset the binary log or lock the master.
	for _, priv := range []string{"REPLICATION SLAVE", "BACKUP_ADMIN"} {
		if !grantsInclude(grants, priv) {
			t.Errorf("grants lack %s:\n%s", priv, all)
		}
	}
	for _, priv := range []string{"SELECT", "RELOAD", "LOCK TABLES", "SHOW VIEW", "TRIGGER", "EVENT",
		"INSERT", "UPDATE", "DELETE", "DROP", "SUPER", "CREATE USER"} {
		if grantsInclude(grants, priv) {
			t.Errorf("grants include %s:\n%s", priv, all)
		}
	}

	// RequireSSL makes the account TLS-only, and off again.
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: second, RequireSSL: true}); !r.OK {
		t.Fatalf("create with RequireSSL: %+v", r.Error)
	}
	if got := sslType(t, root, "dbpanel_repl", "%"); got != "ANY" {
		t.Errorf("ssl_type with RequireSSL = %q, want ANY", got)
	}
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: second}); !r.OK {
		t.Fatalf("create without RequireSSL: %+v", r.Error)
	}
	if got := sslType(t, root, "dbpanel_repl", "%"); got != "" {
		t.Errorf("ssl_type without RequireSSL = %q, want empty", got)
	}

	// A specific host is a separate account.
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: second, FromHost: "10.0.0.%"}); !r.OK {
		t.Fatalf("create for 10.0.0.%%: %+v", r.Error)
	}
	var n int
	if err := root.QueryRow("SELECT COUNT(*) FROM mysql.user WHERE user = 'dbpanel_repl' AND host = '10.0.0.%'").Scan(&n); err != nil || n != 1 {
		t.Errorf("account at 10.0.0.%%: %d, %v", n, err)
	}

	// The password literal follows the session's sql_mode.
	execAll(t, root, "SET GLOBAL sql_mode = CONCAT(@@GLOBAL.sql_mode, ',NO_BACKSLASH_ESCAPES')")
	const third = `back\\slash'n\'quote`
	if r := create(proto.ReplUserParams{User: "dbpanel_repl", Password: third}); !r.OK {
		t.Fatalf("create under NO_BACKSLASH_ESCAPES: %+v", r.Error)
	}
	if err := loginTCP(t, inst, "dbpanel_repl", third); err != nil {
		t.Errorf("login with a password set under NO_BACKSLASH_ESCAPES: %v", err)
	}

	if strings.Contains(logs.String(), first) || strings.Contains(logs.String(), second) || strings.Contains(logs.String(), third) || !strings.Contains(logs.String(), "dbpanel_repl") {
		t.Errorf("logs leak the password or miss the user:\n%s", logs.String())
	}
}

func TestReplUserRejectsBadIdentifier(t *testing.T) {
	reg := commands.NewRegistry()
	(&mysql.Driver{}).Register(reg) // no host: only validation can answer
	for _, p := range []proto.ReplUserParams{
		{User: "", Password: "x"},
		{User: "dbpanel_repl'@'%' IDENTIFIED BY 'x'; --", Password: "x"},
		{User: "dbpanel_a-b", Password: "x"},
		{User: "dbpanel_repl`", Password: "x"},
		{User: "dbpanel_" + strings.Repeat("u", 25), Password: "x"},
		{User: "dbpanel_repl", Password: ""},
		{User: "dbpanel_repl", Password: "line\nbreak"},
		{User: "dbpanel_repl", Password: "x", FromHost: "x' OR '1"},
		{User: "dbpanel_repl", Password: "x", FromHost: "a b"},
		{User: "dbpanel_repl", Password: "x", FromHost: `h\`},
		{User: "dbpanel_repl", Password: "x", FromHost: strings.Repeat("h", 256)},
		// Only accounts named dbpanel_*: never root or an application's.
		{User: "root", Password: "x", FromHost: "localhost"},
		{User: "simrs_app", Password: "x"},
		{User: "repl", Password: "x"},
		{User: "dbpanel_", Password: "x"},
	} {
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createReplUser", Params: mustJSON(t, p)})
		if r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%+v: response = %+v, want bad_params", p, r)
		}
	}
}

// sslType is mysql.user.ssl_type of user@host.
func sslType(t *testing.T, db *sql.DB, user, host string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT ssl_type FROM mysql.user WHERE user = ? AND host = ?", user, host).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// showGrants is SHOW GRANTS for user@host.
func showGrants(t *testing.T, db *sql.DB, user, host string) []string {
	t.Helper()
	rows, err := db.Query("SHOW GRANTS FOR '" + user + "'@'" + host + "'") // test values, no quotes
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

// TestDumpUserLifecycle: mysql.createDumpUser makes a TLS-optional account
// with only what mysqldump needs, mysql.dropUser removes it (twice is
// harmless), and neither touches an account not named dbpanel_*.
func TestDumpUserLifecycle(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	var logs bytes.Buffer
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.NewTextHandler(&logs, nil))}).Register(reg)
	call := func(cmd string, p any) proto.Response {
		t.Helper()
		return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(t, p)})
	}
	root := rootDB(t, inst)
	const user, pw = "dbpanel_dump_0a1b2c3d", `Dump'pw\"x`
	if r := call("mysql.createDumpUser", proto.DumpUserParams{User: user, Password: pw, FromHost: "127.0.0.1"}); !r.OK {
		t.Fatalf("createDumpUser: %+v", r.Error)
	}
	if err := loginTCP(t, inst, user, pw); err != nil {
		t.Fatalf("login as the dump user: %v", err)
	}
	// SEC-56: the password expires a day after it is set.
	var create string
	if err := root.QueryRow("SHOW CREATE USER '" + user + "'@'127.0.0.1'").Scan(&create); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PASSWORD EXPIRE INTERVAL 1 DAY", "MAX_USER_CONNECTIONS 2"} {
		if !strings.Contains(create, want) {
			t.Errorf("SHOW CREATE USER lacks %q: %s", want, create)
		}
	}
	grants := showGrants(t, root, user, "127.0.0.1")
	for _, priv := range []string{"SELECT", "FLUSH_TABLES", "LOCK TABLES", "SHOW VIEW", "TRIGGER", "EVENT"} {
		if !grantsInclude(grants, priv) {
			t.Errorf("dump grants lack %s: %q", priv, grants)
		}
	}
	for _, priv := range []string{"RELOAD", "REPLICATION SLAVE", "BACKUP_ADMIN", "INSERT", "DROP", "SUPER", "CREATE USER"} {
		if grantsInclude(grants, priv) {
			t.Errorf("dump grants include %s: %q", priv, grants)
		}
	}
	if r := call("mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_ssl", Password: pw, FromHost: "10.0.0.5",
		RequireSSL: true}); !r.OK {
		t.Fatalf("createDumpUser with RequireSSL: %+v", r.Error)
	}
	if got := sslType(t, root, "dbpanel_dump_ssl", "10.0.0.5"); got != "ANY" {
		t.Errorf("ssl_type = %q, want ANY", got)
	}

	for i := 0; i < 2; i++ {
		if r := call("mysql.dropUser", proto.DropUserParams{User: user, FromHost: "127.0.0.1"}); !r.OK {
			t.Fatalf("dropUser #%d: %+v", i+1, r.Error)
		}
	}
	if n := count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"'"); n != 0 {
		t.Errorf("dump user still exists (%d rows)", n)
	}

	// Never an account outside dbpanel_*: not even an existing one.
	execAll(t, root, "CREATE USER simrs_app@'%' IDENTIFIED BY 'app'")
	for _, c := range []struct {
		cmd string
		p   any
	}{
		{"mysql.dropUser", proto.DropUserParams{User: "root", FromHost: "localhost"}},
		{"mysql.dropUser", proto.DropUserParams{User: "simrs_app", FromHost: "%"}},
		{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_", FromHost: "%"}},
		{"mysql.dropUser", proto.DropUserParams{User: "dbpanel_x", FromHost: "a'b"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "simrs_app", Password: "x", FromHost: "%"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "root", Password: "x", FromHost: "localhost"}},
		{"mysql.createDumpUser", proto.DumpUserParams{User: "dbpanel_dump_x", Password: "x"}}, // no host
		{"mysql.createReplUser", proto.ReplUserParams{User: "simrs_app", Password: "x", FromHost: "%"}},
	} {
		if r := call(c.cmd, c.p); r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%s %+v = %+v, want bad_params", c.cmd, c.p, r)
		}
	}
	if err := loginTCP(t, inst, "simrs_app", "app"); err != nil {
		t.Errorf("the application account changed: %v", err)
	}
	if strings.Contains(logs.String(), pw) {
		t.Errorf("logs leak the dump password:\n%s", logs.String())
	}
}

// TestDumpGrantsByVersion: FLUSH_TABLES replaces RELOAD from the first
// release that has it; an older (or unparsable) version keeps RELOAD.
func TestDumpGrantsByVersion(t *testing.T) {
	const withReload = "SELECT, RELOAD, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT"
	const withFlush = "SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT"
	for _, c := range []struct{ version, want string }{
		{"8.0.26", withReload},
		{"8.0.30-log", withReload},
		{"8.0.31", withFlush},
		{"8.0.46-0ubuntu0.24.04.4", withFlush},
		{"8.0.36-28", withFlush},
		{"8.4.3", withFlush},
		{"9.1.0", withFlush},
		{"", withReload},
		{"10.11.6-MariaDB", withReload},
	} {
		if got := mysql.DumpGrantsFor(c.version); got != c.want {
			t.Errorf("DumpGrantsFor(%q) = %q, want %q", c.version, got, c.want)
		}
	}
	if got := mysql.DumpGrantsFor(mysql.FlushTablesMinVersion); got != withFlush {
		t.Errorf("DumpGrantsFor(FlushTablesMinVersion %q) = %q, want %q", mysql.FlushTablesMinVersion, got, withFlush)
	}
}

// TestCreateDumpUserNoReloadOnSupportedSeries: on a server with
// FLUSH_TABLES the dump account holds no RELOAD, the dump as DumpFrom runs
// it (FLUSH TABLES WITH READ LOCK included, --no-data here) still works,
// and the account cannot reset the binary logs.
func TestCreateDumpUserNoReloadOnSupportedSeries(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.DiscardHandler)}).Register(reg)
	root := rootDB(t, inst)
	version := serverVersionOf(t, root)
	d, err := mysql.ParseVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	if mysql.DumpGrantsFor(version) == mysql.DumpGrantsFor("8.0.26") {
		t.Skipf("MySQL %s predates FLUSH_TABLES", version)
	}
	const user, pw = "dbpanel_dump_0badc0de", "dump-Secret-1"
	r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createDumpUser",
		Params: mustJSON(t, proto.DumpUserParams{User: user, Password: pw, FromHost: "127.0.0.1"})})
	if !r.OK {
		t.Fatalf("createDumpUser: %+v", r.Error)
	}
	if string(r.Result) != "{}" {
		t.Errorf("createDumpUser result = %s, want {} (no warning)", r.Result)
	}
	grants := showGrants(t, root, user, "127.0.0.1")
	if grantsInclude(grants, "RELOAD") || !grantsInclude(grants, "FLUSH_TABLES") {
		t.Fatalf("MySQL %s dump grants = %q, want FLUSH_TABLES and no RELOAD", version, grants)
	}

	execAll(t, root, "CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "INSERT INTO a.t VALUES (1)")
	content, err := mysql.ClientDefaults(proto.ConnectParams{User: user, Password: pw, Host: "127.0.0.1", Port: inst.Port}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dump.cnf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dump, err := mysql.FindClientTool(inst.Layout().MysqldPath, "mysqldump")
	if err != nil {
		t.Fatal(err)
	}
	// The real run's arguments (--set-gtid-purged=ON takes FLUSH TABLES
	// and FLUSH TABLES WITH READ LOCK), schema only.
	args := mysql.DumpArgs(path, []string{"a"}, "ON")
	args = append([]string{args[0], "--no-data"}, args[1:]...)
	if err := inst.Host().Exec(context.Background(), nil, io.Discard, dump, args...); err != nil {
		t.Fatalf("mysqldump as the dump account on MySQL %s: %v", version, err)
	}

	db, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: user, Password: pw, Host: "127.0.0.1", Port: inst.Port}, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(d.ResetBinaryLogs())
	var me *drv.MySQLError
	if !errors.As(err, &me) || me.Number != 1227 {
		t.Errorf("%s as the dump account = %v, want error 1227", d.ResetBinaryLogs(), err)
	}
}

// TestCreateDumpUserAdminWithoutFlushTables: while the admin account cannot
// grant FLUSH_TABLES (connected before mysql.connect asked for it), the
// dump account keeps RELOAD and the log names the remedy, after which the
// account gets FLUSH_TABLES.
func TestCreateDumpUserAdminWithoutFlushTables(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	root := rootDB(t, inst)
	if mysql.DumpGrantsFor(serverVersionOf(t, root)) == mysql.DumpGrantsFor("8.0.26") {
		t.Skip("MySQL predates FLUSH_TABLES")
	}
	execAll(t, root, "CREATE USER adm@'%' IDENTIFIED BY 'pw-1'",
		"GRANT CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE ON *.* TO adm@'%' WITH GRANT OPTION",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO adm@'%' WITH GRANT OPTION")
	creds := &memCreds{p: proto.ConnectParams{User: "adm", Password: "pw-1", Host: "127.0.0.1", Port: inst.Port}}
	var logs bytes.Buffer
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.HostWith(creds), Creds: creds, Logger: slog.New(slog.NewTextHandler(&logs, nil))}).Register(reg)
	create := func(user string) ([]string, proto.DumpUserResult) {
		t.Helper()
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createDumpUser",
			Params: mustJSON(t, proto.DumpUserParams{User: user, Password: "pw-2", FromHost: "127.0.0.1"})})
		if !r.OK {
			t.Fatalf("createDumpUser %s as adm: %+v", user, r.Error)
		}
		var res proto.DumpUserResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			t.Fatalf("createDumpUser result %s: %v", r.Result, err)
		}
		return showGrants(t, root, user, "127.0.0.1"), res
	}
	const remedy = "GRANT FLUSH_TABLES ON *.* TO 'adm'@'%' WITH GRANT OPTION"
	g, res := create("dbpanel_dump_00000001")
	if !grantsInclude(g, "RELOAD") || grantsInclude(g, "FLUSH_TABLES") {
		t.Errorf("grants by an admin without FLUSH_TABLES = %q, want RELOAD", g)
	}
	if !strings.Contains(res.Warning, remedy) || !strings.Contains(res.Warning, "RELOAD") {
		t.Errorf("createDumpUser warning = %q, want RELOAD and %q", res.Warning, remedy)
	}
	if !strings.Contains(logs.String(), remedy) {
		t.Errorf("log does not name the remedy:\n%s", logs.String())
	}
	execAll(t, root, "GRANT FLUSH_TABLES ON *.* TO adm@'%' WITH GRANT OPTION")
	g, res = create("dbpanel_dump_00000002")
	if grantsInclude(g, "RELOAD") || !grantsInclude(g, "FLUSH_TABLES") {
		t.Errorf("grants after the remedy = %q, want FLUSH_TABLES", g)
	}
	if res.Warning != "" {
		t.Errorf("createDumpUser warning after the remedy = %q, want none", res.Warning)
	}
}

func serverVersionOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT @@version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSweepDumpUsersDropsOldOnly: mysql.sweepDumpUsers drops the per-job
// dump accounts whose password is older than older_than_sec and nothing
// else: not a fresh dump account, not the replication or admin account,
// not a name that only matches dbpanel_dump_% with _ as a wildcard.
func TestSweepDumpUsersDropsOldOnly(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	var logs bytes.Buffer
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.NewTextHandler(&logs, nil))}).Register(reg)
	sweep := func(p any) proto.Response {
		t.Helper()
		return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.sweepDumpUsers", Params: mustJSON(t, p)})
	}
	root := rootDB(t, inst)
	old := []string{"dbpanel_dump_old1@10.0.0.2", "dbpanel_dump_old2@%", "dbpanel_repl@%", "dbpanel_repl_2@%",
		"dbpanel_admin@localhost", "dbpanelxdumpxold@%", "dbpanel_dumpxold@%", "simrs_app@%"}
	for _, a := range append([]string{"dbpanel_dump_new@10.0.0.2"}, old...) {
		user, host, _ := strings.Cut(a, "@")
		execAll(t, root, "CREATE USER '"+user+"'@'"+host+"' IDENTIFIED BY 'pw-1'")
	}
	for _, a := range old {
		user, host, _ := strings.Cut(a, "@")
		execAll(t, root, "UPDATE mysql.user SET password_last_changed = NOW() - INTERVAL 2 DAY WHERE user = '"+user+"' AND host = '"+host+"'")
	}
	execAll(t, root, "FLUSH PRIVILEGES")

	r := sweep(proto.SweepDumpUsersParams{OlderThanSec: 86400})
	if !r.OK {
		t.Fatalf("sweepDumpUsers: %+v", r.Error)
	}
	var res proto.SweepDumpUsersResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	want := []string{"'dbpanel_dump_old1'@'10.0.0.2'", "'dbpanel_dump_old2'@'%'"}
	if !slices.Equal(res.Dropped, want) {
		t.Errorf("Dropped = %q, want %q", res.Dropped, want)
	}
	for _, a := range []string{"dbpanel_dump_new@10.0.0.2", "dbpanel_repl@%", "dbpanel_repl_2@%", "dbpanel_admin@localhost",
		"dbpanelxdumpxold@%", "dbpanel_dumpxold@%", "simrs_app@%"} {
		user, host, _ := strings.Cut(a, "@")
		if n := count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"' AND host = '"+host+"'"); n != 1 {
			t.Errorf("account %s was dropped", a)
		}
	}
	for _, user := range []string{"dbpanel_dump_old1", "dbpanel_dump_old2"} {
		if n := count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"'"); n != 0 {
			t.Errorf("account %s still exists", user)
		}
		if strings.Count(logs.String(), user) != 1 {
			t.Errorf("want one log line naming %s:\n%s", user, logs.String())
		}
	}
	for _, user := range []string{"dbpanel_repl", "dbpanel_admin", "dbpanel_dump_new", "dbpanelxdumpxold"} {
		if strings.Contains(logs.String(), user) {
			t.Errorf("logs name %s:\n%s", user, logs.String())
		}
	}

	// Nothing left to sweep: an empty list, not null.
	r = sweep(proto.SweepDumpUsersParams{OlderThanSec: 86400})
	if !r.OK || string(r.Result) != `{"dropped":[]}` {
		t.Errorf("second sweep = %+v / %s", r.Error, r.Result)
	}

	for _, p := range []any{"not an object", proto.SweepDumpUsersParams{}, proto.SweepDumpUsersParams{OlderThanSec: 59},
		proto.SweepDumpUsersParams{OlderThanSec: -1}} {
		if r := sweep(p); r.OK || r.Error == nil || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%+v: response = %+v / %+v, want bad_params", p, r, r.Error)
		}
	}
}

// The health monitor sweeps through the driver.
var _ monitor.DumpSweeper = (*mysql.Driver)(nil)

// TestDriverSweepDumpUsers: the monitor's hourly sweep drops the stale
// per-job dump accounts on a server without a replication source, skips a
// server with one (the master's drop replicates; a local drop would be an
// errant transaction) and does nothing without MySQL.
func TestDriverSweepDumpUsers(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	root := rootDB(t, inst)
	aged := func(user string) {
		t.Helper()
		execAll(t, root, "CREATE USER '"+user+"'@'10.0.0.2' IDENTIFIED BY 'pw-1'",
			"UPDATE mysql.user SET password_last_changed = NOW() - INTERVAL 2 DAY WHERE user = '"+user+"'",
			"FLUSH PRIVILEGES")
	}
	exists := func(user string) bool {
		t.Helper()
		return count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"'") == 1
	}
	aged("dbpanel_dump_old1")
	execAll(t, root, "CREATE USER 'dbpanel_dump_new1'@'10.0.0.2' IDENTIFIED BY 'pw-1'")
	d := &mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	if n, err := d.SweepDumpUsers(ctx, 24*time.Hour); n != 1 || err != nil {
		t.Fatalf("SweepDumpUsers = %d, %v; want 1, nil", n, err)
	}
	if exists("dbpanel_dump_old1") || !exists("dbpanel_dump_new1") {
		t.Errorf("after the sweep: old1 %v, new1 %v; want only new1", exists("dbpanel_dump_old1"), exists("dbpanel_dump_new1"))
	}

	aged("dbpanel_dump_old2")
	execAll(t, root, "CHANGE REPLICATION SOURCE TO SOURCE_HOST = '10.0.0.9', SOURCE_PORT = 3306")
	if n, err := d.SweepDumpUsers(ctx, 24*time.Hour); n != 0 || err != nil {
		t.Errorf("SweepDumpUsers with a replication source = %d, %v; want 0, nil", n, err)
	}
	if !exists("dbpanel_dump_old2") {
		t.Error("a server with a replication source dropped a dump account")
	}

	if n, err := (&mysql.Driver{}).SweepDumpUsers(ctx, 24*time.Hour); n != 0 || err != nil {
		t.Errorf("SweepDumpUsers without MySQL = %d, %v; want 0, nil", n, err)
	}
}

// grantsInclude reports whether one of the SHOW GRANTS lines grants priv
// ON *.*: the privilege list sits between "GRANT " and " ON ".
func grantsInclude(grants []string, priv string) bool {
	for _, g := range grants {
		list, ok := strings.CutPrefix(g, "GRANT ")
		if !ok {
			continue
		}
		list, rest, ok := strings.Cut(list, " ON ")
		if !ok || !strings.HasPrefix(rest, "*.*") {
			continue
		}
		for _, p := range strings.Split(list, ",") {
			if strings.TrimSpace(p) == priv {
				return true
			}
		}
	}
	return false
}

// The health monitor guards against dump sessions holding the global read
// lock through the driver.
var _ monitor.DumpLockGuard = (*mysql.Driver)(nil)

// TestDumpAccountCannotHoldGlobalLock (SEC-56, the audit's PoC): a dump
// account is limited to two sessions; one holding FLUSH TABLES WITH READ
// LOCK is killed by the lock guard once seen for maxHold. The hourly sweep
// kills nothing: a stale account with a session open is left for later,
// one without is dropped. Sessions of any other account are never killed.
func TestDumpAccountCannotHoldGlobalLock(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	root := rootDB(t, inst)
	d := &mysql.Driver{Host: inst.Host(), Logger: slog.New(slog.DiscardHandler)}
	reg := commands.NewRegistry()
	d.Register(reg)
	const user, pw = "dbpanel_dump_deadbeef", "Dump-pw-123"
	if r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createDumpUser",
		Params: mustJSON(t, proto.DumpUserParams{User: user, Password: pw, FromHost: "127.0.0.1"})}); !r.OK {
		t.Fatalf("createDumpUser: %+v", r.Error)
	}
	if n := count(t, root, "SELECT max_user_connections FROM mysql.user WHERE user = '"+user+"'"); n != 2 {
		t.Fatalf("max_user_connections = %d, want 2", n)
	}
	execAll(t, root, "CREATE USER 'simrs_app'@'127.0.0.1' IDENTIFIED BY 'App-pw-123'", "GRANT RELOAD ON *.* TO 'simrs_app'@'127.0.0.1'")
	ctx := context.Background()
	hold := func(u, p string) *sql.Conn {
		t.Helper()
		db, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: u, Password: p, Host: "127.0.0.1", Port: inst.Port}, ""))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ExecContext(ctx, "FLUSH TABLES WITH READ LOCK"); err != nil {
			t.Fatalf("FTWRL as %s: %v", u, err)
		}
		return c
	}
	alive := func(c *sql.Conn) bool { return c.PingContext(ctx) == nil }
	canWrite := func() bool {
		t.Helper()
		c, err := root.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.ExecContext(ctx, "SET SESSION lock_wait_timeout = 2"); err != nil {
			t.Fatal(err)
		}
		_, err = c.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS schreibtest")
		return err == nil
	}

	// An application holding the lock is never touched.
	app := hold("simrs_app", "App-pw-123")
	if n, err := d.KillDumpLocks(ctx, 0); n != 0 || err != nil {
		t.Fatalf("KillDumpLocks with an application's lock = %d, %v", n, err)
	}
	if !alive(app) {
		t.Fatal("the application's session was killed")
	}
	app.ExecContext(ctx, "UNLOCK TABLES")

	// The guard: first seen, then killed once held for maxHold.
	c := hold(user, pw)
	if canWrite() {
		t.Fatal("writes go on while the dump account holds the lock")
	}
	if n, err := d.KillDumpLocks(ctx, time.Hour); n != 0 || err != nil {
		t.Fatalf("KillDumpLocks before maxHold = %d, %v", n, err)
	}
	if n, err := d.KillDumpLocks(ctx, 0); n != 1 || err != nil {
		t.Fatalf("KillDumpLocks = %d, %v; want 1", n, err)
	}
	if alive(c) || !canWrite() {
		t.Fatalf("after the guard: session alive %v, writes blocked %v", alive(c), !canWrite())
	}

	// The sweep: a stale account (past the 6 h age) still in use is a
	// long dump and is left alone; once its sessions end it is dropped.
	sess, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: user, Password: pw, Host: "127.0.0.1", Port: inst.Port}, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	dump, err := sess.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	execAll(t, root, "UPDATE mysql.user SET password_last_changed = NOW() - INTERVAL 7 HOUR WHERE user = '"+user+"'")
	if n, err := d.SweepDumpUsers(ctx, 6*time.Hour); n != 0 || err != nil {
		t.Fatalf("SweepDumpUsers with a session open = %d, %v; want 0", n, err)
	}
	if !alive(dump) {
		t.Fatal("the sweep killed a running dump's session")
	}
	if n := count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"'"); n != 1 {
		t.Fatal("the sweep dropped a dump account still in use")
	}
	dump.Close()
	sess.Close()
	deadline := time.Now().Add(10 * time.Second)
	for count(t, root, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = '"+user+"'") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the dump session did not end")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n, err := d.SweepDumpUsers(ctx, 6*time.Hour); n != 1 || err != nil {
		t.Fatalf("SweepDumpUsers = %d, %v; want 1", n, err)
	}
	if n := count(t, root, "SELECT COUNT(*) FROM mysql.user WHERE user = '"+user+"'"); n != 0 {
		t.Fatal("stale dump account not dropped")
	}
}

// TestReplicaCreatesReplUserOnceStrict: a pinned replica or quarantined
// server (a promotion candidate preparing its own replicas) makes the
// replication account under the master's rule: only dbpanel_repl@'%',
// and only while no dbpanel_repl row exists at any host.
func TestReplicaCreatesReplUserOnceStrict(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	for _, role := range []string{"replica", "quarantined"} {
		execAll(t, db, "DROP USER IF EXISTS 'dbpanel_repl'@'%'", "DROP USER IF EXISTS 'dbpanel_repl'@'10.9.9.9'")
		r := newPinRig(t, inst.Host(), pinDir(t, role, "10.0.0.1:3306"))
		for _, c := range replUserRefused {
			wantRefused(t, role+" "+c.cmd, r.dispatch(c.cmd, c.params), "dipin sebagai "+role, "perintah mysql.createReplUser ditolak")
		}
		repl := proto.ReplUserParams{User: "dbpanel_repl", FromHost: "%", Password: "rahasia-satu"}
		if resp := r.dispatch("mysql.createReplUser", repl); !resp.OK {
			t.Fatalf("%s: fresh createReplUser = %+v", role, resp.Error)
		}
		repl.Password = "rahasia-dua"
		wantRefused(t, role+": createReplUser for an existing account", r.dispatch("mysql.createReplUser", repl),
			"sudah ada di master yang dipin")
		if err := loginTCP(t, inst, "dbpanel_repl", "rahasia-satu"); err != nil {
			t.Errorf("%s: replication account's password changed: %v", role, err)
		}
		// A row at any other host blocks the account at '%' too.
		execAll(t, db, "DROP USER 'dbpanel_repl'@'%'", "CREATE USER 'dbpanel_repl'@'10.9.9.9' IDENTIFIED BY 'lain'")
		wantRefused(t, role+": createReplUser while dbpanel_repl exists at another host", r.dispatch("mysql.createReplUser", repl),
			"sudah ada di master yang dipin", "10.9.9.9")
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM mysql.user WHERE user = 'dbpanel_repl' AND host = '%'").Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: dbpanel_repl@'%%' after the refusal: count %d, err %v; want none", role, n, err)
		}
	}
}
