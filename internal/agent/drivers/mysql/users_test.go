package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
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
	grants := showGrants(t, root, user, "127.0.0.1")
	for _, priv := range []string{"SELECT", "RELOAD", "LOCK TABLES", "SHOW VIEW", "TRIGGER", "EVENT"} {
		if !grantsInclude(grants, priv) {
			t.Errorf("dump grants lack %s: %q", priv, grants)
		}
	}
	for _, priv := range []string{"REPLICATION SLAVE", "BACKUP_ADMIN", "INSERT", "DROP", "SUPER", "CREATE USER"} {
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
