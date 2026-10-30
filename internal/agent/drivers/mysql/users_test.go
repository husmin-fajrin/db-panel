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

	root := rootDB(t, inst)
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
	if !strings.Contains(all, "GRANT REPLICATION SLAVE ON *.*") || !strings.Contains(all, "GRANT BACKUP_ADMIN ON *.*") {
		t.Errorf("grants:\n%s", all)
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
		{User: "repl'@'%' IDENTIFIED BY 'x'; --", Password: "x"},
		{User: "a-b", Password: "x"},
		{User: "repl`", Password: "x"},
		{User: strings.Repeat("u", 33), Password: "x"},
		{User: "repl", Password: ""},
		{User: "repl", Password: "line\nbreak"},
		{User: "repl", Password: "x", FromHost: "x' OR '1"},
		{User: "repl", Password: "x", FromHost: "a b"},
		{User: "repl", Password: "x", FromHost: `h\`},
		{User: "repl", Password: "x", FromHost: strings.Repeat("h", 256)},
	} {
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.createReplUser", Params: mustJSON(t, p)})
		if r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%+v: response = %+v, want bad_params", p, r)
		}
	}
}
