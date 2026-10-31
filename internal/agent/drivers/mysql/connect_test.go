package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// connectEnv is a driver over a test instance with a file credential store.
type connectEnv struct {
	inst     *testmysql.Instance
	reg      *commands.Registry
	credPath string
	store    mysql.CredentialStore
}

func newConnectEnv(t *testing.T, inst *testmysql.Instance) *connectEnv {
	t.Helper()
	dir := t.TempDir()
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	e := &connectEnv{inst: inst, credPath: filepath.Join(dir, "mysql.cred"), reg: commands.NewRegistry()}
	e.store = mysql.NewFileCredentialStore(e.credPath, box)
	(&mysql.Driver{Host: inst.HostWith(e.store), Creds: e.store}).Register(e.reg)
	return e
}

func (e *connectEnv) call(t *testing.T, cmd string, params any) proto.Response {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	return e.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: raw})
}

func result(t *testing.T, r proto.Response) proto.ConnectResult {
	t.Helper()
	if !r.OK {
		t.Fatalf("response = %+v", r.Error)
	}
	var res proto.ConnectResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func rootDB(t *testing.T, inst *testmysql.Instance) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConnectSavesAndLists(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	e := newConnectEnv(t, inst)
	root := rootDB(t, inst)
	for _, q := range []string{
		"CREATE DATABASE demo", "CREATE TABLE demo.t (id INT PRIMARY KEY, v VARCHAR(100))",
		"INSERT INTO demo.t VALUES (1, 'x')", "CREATE DATABASE empty_db",
	} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	res := result(t, e.call(t, "mysql.connect", proto.ConnectParams{User: "root"}))
	if res.Account != "root@localhost" {
		t.Errorf("Account = %q", res.Account)
	}
	if !strings.HasPrefix(res.Status.Version, "8.") || res.Status.Series == "" {
		t.Errorf("Status = %+v", res.Status)
	}
	if len(res.MissingGrants) != 0 {
		t.Errorf("root is missing grants: %v", res.MissingGrants)
	}
	byName := map[string]proto.DatabaseInfo{}
	var names []string
	for _, d := range res.Databases {
		byName[d.Name] = d
		names = append(names, d.Name)
	}
	if !reflect.DeepEqual(names, []string{"demo", "empty_db"}) {
		t.Errorf("databases = %v, want [demo empty_db] (system schemas hidden, sorted)", names)
	}
	if d := byName["demo"]; d.Bytes == 0 || d.Tables != 1 || !reflect.DeepEqual(d.Engines, []string{"InnoDB"}) {
		t.Errorf("demo = %+v", d)
	}
	if d := byName["empty_db"]; d.Bytes != 0 || d.Tables != 0 || len(d.Engines) != 0 {
		t.Errorf("empty_db = %+v", d)
	}

	fi, err := os.Stat(e.credPath)
	if err != nil {
		t.Fatalf("credential file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("credential file mode = %v, want 0600", fi.Mode().Perm())
	}
	if raw, _ := os.ReadFile(e.credPath); strings.Contains(string(raw), `"user"`) || strings.Contains(string(raw), "root") {
		t.Error("credential file is plain text, not sealed")
	}
	if p, ok, err := e.store.Load(); err != nil || !ok || p.User != "root" {
		t.Errorf("stored = %+v, %v, %v", p, ok, err)
	}

	// Inventory uses the stored account and answers the same.
	inv := result(t, e.call(t, "mysql.inventory", nil))
	if inv.Account != res.Account || len(inv.Databases) != 2 || inv.Status.Version != res.Status.Version {
		t.Errorf("inventory = %+v", inv)
	}
}

// leakPW is a distinctive wrong password, so a leak into an error message
// cannot hide behind words the server's own message contains.
const leakPW = "pw-Zq81-secret"

func TestConnectWrongPasswordDoesNotSave(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	e := newConnectEnv(t, inst)

	r := e.call(t, "mysql.connect", proto.ConnectParams{User: "root", Password: leakPW, Host: "127.0.0.1", Port: inst.Port})
	if r.OK || r.Error == nil {
		t.Fatalf("response = %+v, want an error", r)
	}
	if r.Error.Code != proto.CodeBadParams || !strings.HasPrefix(r.Error.Message, "login MySQL gagal: 1045 ") {
		t.Errorf("error = %+v", r.Error)
	}
	if strings.Contains(r.Error.Message, leakPW) {
		t.Errorf("message leaks the password: %q", r.Error.Message)
	}
	if _, err := os.Stat(e.credPath); !os.IsNotExist(err) {
		t.Errorf("credential file exists after a failed login: %v", err)
	}

	// A failed attempt does not disturb credentials stored earlier.
	good := proto.ConnectParams{User: "root"}
	want := proto.ConnectParams{User: "root", Host: "localhost"} // the default host is filled in
	result(t, e.call(t, "mysql.connect", good))
	if r := e.call(t, "mysql.connect", proto.ConnectParams{User: "root", Password: leakPW, Host: "127.0.0.1", Port: inst.Port}); r.OK {
		t.Fatal("wrong password accepted")
	}
	if p, ok, err := e.store.Load(); err != nil || !ok || p != want {
		t.Errorf("stored = %+v, %v, %v; want %+v", p, ok, err, want)
	}
}

// TestInventoryStaleCredentials: stored credentials the server no longer
// accepts give the same bad_params answer as mysql.connect, without the password.
func TestInventoryStaleCredentials(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	e := newConnectEnv(t, inst)
	if err := e.store.Save(proto.ConnectParams{User: "root", Password: leakPW, Host: "127.0.0.1", Port: inst.Port}); err != nil {
		t.Fatal(err)
	}
	r := e.call(t, "mysql.inventory", nil)
	if r.OK || r.Error == nil || r.Error.Code != proto.CodeBadParams || !strings.HasPrefix(r.Error.Message, "login MySQL gagal: 1045 ") {
		t.Fatalf("response = %+v / %+v", r, r.Error)
	}
	if strings.Contains(r.Error.Message, leakPW) {
		t.Errorf("message leaks the password: %q", r.Error.Message)
	}
}

func TestConnectReportsMissingGrants(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	e := newConnectEnv(t, inst)
	root := rootDB(t, inst)
	for _, q := range []string{
		"CREATE USER 'limited'@'%' IDENTIFIED BY 'pw-1'", "GRANT SELECT ON *.* TO 'limited'@'%'",
	} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	res := result(t, e.call(t, "mysql.connect", proto.ConnectParams{User: "limited", Password: "pw-1", Host: "127.0.0.1", Port: inst.Port}))
	if res.Account != "limited@%" {
		t.Errorf("Account = %q", res.Account)
	}
	if !strings.HasPrefix(res.Status.Version, "8.") {
		t.Errorf("Status.Version = %q: a limited account should still report the version", res.Status.Version)
	}
	want := []string{
		"GRANT CREATE USER, RELOAD, PROCESS, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE ON *.* TO 'limited'@'%'",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO 'limited'@'%' WITH GRANT OPTION",
		"GRANT USAGE ON *.* TO 'limited'@'%' WITH GRANT OPTION",
	}
	if !reflect.DeepEqual(res.MissingGrants, want) {
		t.Errorf("MissingGrants =\n%q\nwant\n%q", res.MissingGrants, want)
	}
	// The account still works, so it was saved: the panel shows what to fix.
	if _, ok, _ := e.store.Load(); !ok {
		t.Error("account with missing grants was not saved")
	}

	// The suggested statements are the remedy: after applying them the check
	// is clean and the account can really create the replication user.
	for _, g := range res.MissingGrants {
		if _, err := root.Exec(g); err != nil {
			t.Fatalf("remedy %q: %v", g, err)
		}
	}
	res = result(t, e.call(t, "mysql.inventory", nil))
	if len(res.MissingGrants) != 0 {
		t.Fatalf("MissingGrants after the remedy = %q", res.MissingGrants)
	}
	p, _, _ := e.store.Load()
	as, err := sql.Open("mysql", mysql.DSN(p, inst.Socket))
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	for _, q := range []string{
		"CREATE USER 'x'@'%' IDENTIFIED BY 'pw-2'",
		"GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO 'x'@'%'",
	} {
		if _, err := as.Exec(q); err != nil {
			t.Errorf("as the connected account, %q: %v", q, err)
		}
	}
}

func TestInventoryWithoutCredentialsFails(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	e := newConnectEnv(t, inst)

	r := e.call(t, "mysql.inventory", nil)
	if r.OK || r.Error == nil || r.Error.Code != proto.CodeBadParams || r.Error.Message != "MySQL belum dihubungkan" {
		t.Fatalf("response = %+v / %+v", r, r.Error)
	}
}

// TestConnectRejectsBadHost: the host is an address or a name, never text
// that could carry DSN options or reach odd places.
func TestConnectRejectsBadHost(t *testing.T) {
	reg := commands.NewRegistry()
	(&mysql.Driver{}).Register(reg) // no host: only validation can answer
	for _, host := range []string{"a b", "db/x?allowAllFiles=true", "user@db", "db:3306", `x'"><svg/onload=alert(1)>`,
		strings.Repeat("h", 256)} {
		raw, _ := json.Marshal(proto.ConnectParams{User: "root", Password: "x", Host: host})
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.connect", Params: raw})
		if r.OK || r.Error.Code != proto.CodeBadParams || !strings.Contains(r.Error.Message, "host MySQL tidak valid") {
			t.Errorf("host %q: response = %+v / %+v, want bad_params", host, r, r.Error)
		}
	}
	for _, host := range []string{"", "localhost", "10.0.0.5", "db-1.lan", "::1"} {
		raw, _ := json.Marshal(proto.ConnectParams{User: "root", Password: "x", Host: host})
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.connect", Params: raw})
		if r.OK || r.Error.Message != mysql.ErrNoHost.Error() {
			t.Errorf("host %q: response = %+v / %+v, want it past validation", host, r, r.Error)
		}
	}
}
