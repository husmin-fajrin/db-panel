package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// callPreflight runs mysql.preflight through the registry on h.
func callPreflight(t *testing.T, h mysql.Host, p proto.PreflightParams) proto.PreflightResult {
	t.Helper()
	r := dispatchPreflight(t, h, p)
	if !r.OK {
		t.Fatalf("mysql.preflight: %+v", r.Error)
	}
	var res proto.PreflightResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func dispatchPreflight(t *testing.T, h mysql.Host, p any) proto.Response {
	t.Helper()
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: h}).Register(reg)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.preflight", Params: raw})
}

func checkIDs(res proto.PreflightResult) []string {
	var ids []string
	for _, c := range res.Checks {
		ids = append(ids, c.ID)
	}
	return ids
}

// check returns the check with id, failing the test when there is none.
func check(t *testing.T, res proto.PreflightResult, id string) proto.Check {
	t.Helper()
	for _, c := range res.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %v", id, checkIDs(res))
	return proto.Check{}
}

func wantCheck(t *testing.T, res proto.PreflightResult, id, status string) proto.Check {
	t.Helper()
	c := check(t, res, id)
	if c.Status != status {
		t.Errorf("check %s = %s (%s), want %s", id, c.Status, c.Detail, status)
	}
	return c
}

func execAll(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// closedPort is a 127.0.0.1 port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// memCreds is a CredentialStore holding one account in memory.
type memCreds struct{ p proto.ConnectParams }

func (m *memCreds) Load() (proto.ConnectParams, bool, error) { return m.p, true, nil }
func (m *memCreds) Save(p proto.ConnectParams) error         { m.p = p; return nil }
func (m *memCreds) Clear() error                             { return errors.New("not supported") }

// smallDisk is a Host whose data directory has free bytes left.
type smallDisk struct {
	mysql.Host
	free uint64
}

func (h smallDisk) DiskFree(string) (uint64, uint64, error) { return h.free, 4 * h.free, nil }

const gib = 1 << 30

func TestPreflightMasterHealthy(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"bind-address=0.0.0.0", "plugin-load-add=mysql_clone.so"}})
	root := rootDB(t, inst)
	execAll(t, root,
		"CREATE DATABASE demo", "CREATE TABLE demo.t (id INT PRIMARY KEY, v VARCHAR(100)) ENGINE=InnoDB",
		"INSERT INTO demo.t VALUES (1, 'x'), (2, 'y')",
		"CREATE USER 'dbpanel_repl'@'%' IDENTIFIED BY 'pw-repl-1'")

	res := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master", ReplUser: "dbpanel_repl"})
	want := []string{"version", "grants", "server_id", "log_bin", "binlog_format", "gtid_mode",
		"enforce_gtid_consistency", "clone_plugin", "bind_address", "repl_user", "non_innodb",
		"existing_replication", "data_size"}
	if got := checkIDs(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	for _, c := range res.Checks {
		if c.Status != proto.CheckOK {
			t.Errorf("check %s = %s (%s), want ok", c.ID, c.Status, c.Detail)
		}
		if c.Title == "" || c.Detail == "" {
			t.Errorf("check %s has no title or detail: %+v", c.ID, c)
		}
	}
	if res.Worst != proto.CheckOK || res.Method != "" {
		t.Errorf("Worst = %q, Method = %q", res.Worst, res.Method)
	}
	if res.Status.Version == "" || res.Status.ServerID != inst.ServerID || res.Status.DataDir == "" {
		t.Errorf("Status = %+v", res.Status)
	}
	if res.DataBytes == 0 {
		t.Error("DataBytes = 0 with a demo table")
	}
	if d := check(t, res, "data_size").Detail; !strings.Contains(d, " GB") {
		t.Errorf("data_size detail = %q, want GB", d)
	}

	// A replication user that does not exist yet is created later: a warning.
	res = callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master", ReplUser: "belum_ada"})
	if c := wantCheck(t, res, "repl_user", proto.CheckWarn); !strings.Contains(c.Detail, "akan dibuat") {
		t.Errorf("repl_user detail = %q", c.Detail)
	}
	if res.Worst != proto.CheckWarn {
		t.Errorf("Worst = %q, want warn", res.Worst)
	}
}

// TestPreflightMasterReportsFixes: settings that can change online are
// reported with the statement that fixes them.
func TestPreflightMasterReportsFixes(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst),
		"SET GLOBAL server_id = 0", "SET GLOBAL binlog_format = 'STATEMENT'", "SET GLOBAL gtid_mode = ON_PERMISSIVE")

	res := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master"})
	c := wantCheck(t, res, "server_id", proto.CheckFail)
	if len(c.FixSQL) != 1 {
		t.Fatalf("server_id FixSQL = %q", c.FixSQL)
	}
	m := regexp.MustCompile(`^SET PERSIST server_id=(\d+)$`).FindStringSubmatch(c.FixSQL[0])
	if m == nil {
		t.Fatalf("server_id FixSQL = %q", c.FixSQL)
	}
	if n, err := strconv.ParseUint(m[1], 10, 32); err != nil || n < 2 {
		t.Errorf("new server_id %s outside [2, 4294967295]: %v", m[1], err)
	}
	c = wantCheck(t, res, "binlog_format", proto.CheckFail)
	if !reflect.DeepEqual(c.FixSQL, []string{"SET PERSIST binlog_format='ROW'"}) {
		t.Errorf("binlog_format FixSQL = %q", c.FixSQL)
	}
	if c := wantCheck(t, res, "gtid_mode", proto.CheckWarn); !strings.Contains(c.Detail, "akan dinaikkan bertahap tanpa restart") {
		t.Errorf("gtid_mode detail = %q", c.Detail)
	}
	wantCheck(t, res, "enforce_gtid_consistency", proto.CheckOK)
	wantCheck(t, res, "existing_replication", proto.CheckOK)
	if res.Worst != proto.CheckFail {
		t.Errorf("Worst = %q, want fail", res.Worst)
	}
}

func TestPreflightMasterNonInnoDBOnlyInSelectedDBs(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	root := rootDB(t, inst)
	execAll(t, root,
		"CREATE DATABASE app", "CREATE TABLE app.orders (id INT PRIMARY KEY) ENGINE=InnoDB",
		"INSERT INTO app.orders VALUES (1), (2), (3)",
		"CREATE VIEW app.v AS SELECT id FROM app.orders",
		"CREATE DATABASE arsip")
	for i := 0; i < 21; i++ {
		execAll(t, root, fmt.Sprintf("CREATE TABLE arsip.t%02d (id INT) ENGINE=MyISAM", i))
	}

	selected := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master", Databases: []string{"app"}})
	wantCheck(t, selected, "non_innodb", proto.CheckOK)
	all := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master"})
	if all.DataBytes <= selected.DataBytes || selected.DataBytes == 0 {
		t.Errorf("DataBytes: app only %d, all %d", selected.DataBytes, all.DataBytes)
	}

	for _, dbs := range [][]string{nil, {"arsip"}} {
		res := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master", Databases: dbs})
		c := wantCheck(t, res, "non_innodb", proto.CheckFail)
		if !strings.Contains(c.Detail, "arsip.t00 (MyISAM)") || !strings.Contains(c.Detail, "arsip.t19 (MyISAM)") {
			t.Errorf("databases %v: detail = %q", dbs, c.Detail)
		}
		if strings.Contains(c.Detail, "arsip.t20") || !strings.Contains(c.Detail, "dan 1 lainnya") {
			t.Errorf("databases %v: detail lists more than 20 tables: %q", dbs, c.Detail)
		}
		if strings.Contains(c.Detail, "app.") {
			t.Errorf("databases %v: InnoDB table or view listed: %q", dbs, c.Detail)
		}
		if res.Worst != proto.CheckFail {
			t.Errorf("Worst = %q", res.Worst)
		}
	}

	// A selected database the server does not have is an error, not 0 bytes.
	res := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master", Databases: []string{"app", "hilang"}})
	if c := wantCheck(t, res, "data_size", proto.CheckFail); !strings.Contains(c.Detail, "hilang") {
		t.Errorf("data_size detail = %q", c.Detail)
	}
}

func TestPreflightMasterBindLocalhost(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{}) // the harness binds 127.0.0.1
	res := callPreflight(t, inst.Host(), proto.PreflightParams{Role: "master"})
	c := wantCheck(t, res, "bind_address", proto.CheckNeedsRestart)
	if c.FixConfig != "bind-address=0.0.0.0" || !strings.Contains(c.Detail, "127.0.0.1") {
		t.Errorf("bind_address = %+v", c)
	}
	// Without the clone plugin loaded that is only a warning.
	wantCheck(t, res, "clone_plugin", proto.CheckWarn)
	if res.Worst != proto.CheckNeedsRestart {
		t.Errorf("Worst = %q, want needs_restart", res.Worst)
	}
}

func TestPreflightMasterMissingGrantsFails(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst),
		"CREATE USER 'limited'@'%' IDENTIFIED BY 'pw-1'", "GRANT SELECT ON *.* TO 'limited'@'%'")
	h := inst.HostWith(&memCreds{proto.ConnectParams{User: "limited", Password: "pw-1", Host: "127.0.0.1", Port: inst.Port}})

	for _, role := range []string{"master", "replica"} {
		p := proto.PreflightParams{Role: role}
		if role == "replica" {
			p.PeerHost, p.PeerPort, p.PeerVersion = "127.0.0.1", inst.Port, "8.0.46"
		}
		res := callPreflight(t, h, p)
		// The other checks would read a partial picture with this account.
		want := []string{"version", "grants"}
		if role == "replica" {
			want = []string{"version", "same_series", "grants"}
		}
		if got := checkIDs(res); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: checks = %v, want %v", role, got, want)
		}
		c := wantCheck(t, res, "grants", proto.CheckFail)
		if len(c.FixSQL) == 0 || !strings.HasPrefix(c.FixSQL[0], "GRANT ") || !strings.Contains(c.FixSQL[0], "'limited'@'%'") {
			t.Errorf("%s: FixSQL = %q", role, c.FixSQL)
		}
		if !strings.Contains(c.Detail, "limited@%") {
			t.Errorf("%s: detail = %q, want the account", role, c.Detail)
		}
		if res.Worst != proto.CheckFail || res.Method != "" {
			t.Errorf("%s: Worst = %q, Method = %q", role, res.Worst, res.Method)
		}
	}
}

// replicaParams preflights inst as a replica of a master at inst's own port
// (reachable) with the same version.
func replicaParams(t *testing.T, inst *testmysql.Instance) proto.PreflightParams {
	t.Helper()
	var version string
	if err := rootDB(t, inst).QueryRow("SELECT @@version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return proto.PreflightParams{Role: "replica", PeerHost: "127.0.0.1", PeerPort: inst.Port, PeerVersion: version, PeerDataBytes: 1 << 20}
}

func TestPreflightReplicaUnreachable(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	p := replicaParams(t, inst)

	res := callPreflight(t, inst.Host(), p)
	want := []string{"version", "same_series", "grants", "reachable", "disk", "dedicated", "leftover", "not_master", "clone_patch"}
	if got := checkIDs(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	for _, c := range res.Checks {
		if c.Status != proto.CheckOK {
			t.Errorf("check %s = %s (%s), want ok", c.ID, c.Status, c.Detail)
		}
	}
	if res.Worst != proto.CheckOK || res.Method != "clone" {
		t.Errorf("Worst = %q, Method = %q; want ok, clone", res.Worst, res.Method)
	}

	port := closedPort(t)
	p.PeerPort = port
	res = callPreflight(t, inst.Host(), p)
	c := wantCheck(t, res, "reachable", proto.CheckFail)
	if want := fmt.Sprintf("tidak bisa menjangkau 127.0.0.1:%d dari server ini; periksa firewall dan bind-address master", port); c.Detail != want {
		t.Errorf("reachable detail = %q, want %q", c.Detail, want)
	}
	if res.Worst != proto.CheckFail {
		t.Errorf("Worst = %q", res.Worst)
	}

	// Free space below 1.2 × the master's data fails with both sizes in GB.
	p.PeerPort = inst.Port
	p.PeerDataBytes = 10 * gib
	res = callPreflight(t, smallDisk{inst.Host(), 11 * gib}, p) // 11 < 12
	if c := wantCheck(t, res, "disk", proto.CheckFail); !strings.Contains(c.Detail, "11.00 GB") || !strings.Contains(c.Detail, "12.00 GB") {
		t.Errorf("disk detail = %q", c.Detail)
	}
	res = callPreflight(t, smallDisk{inst.Host(), 12 * gib}, p)
	wantCheck(t, res, "disk", proto.CheckOK)
}

func TestPreflightReplicaNotDedicated(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "CREATE DATABASE lain", "CREATE DATABASE shop")
	p := replicaParams(t, inst)
	p.PeerDatabases = []string{"shop", "crm"}

	res := callPreflight(t, inst.Host(), p)
	c := wantCheck(t, res, "dedicated", proto.CheckFail)
	if want := "server ini masih berisi database lain (lain); versi ini hanya mendukung server replika khusus"; c.Detail != want {
		t.Errorf("dedicated detail = %q, want %q", c.Detail, want)
	}
	wantCheck(t, res, "leftover", proto.CheckNeedsConfirm)
	if res.Worst != proto.CheckFail || res.Method != "dump" {
		t.Errorf("Worst = %q, Method = %q", res.Worst, res.Method)
	}
	// Confirming the wipe covers leftovers only, never foreign databases.
	p.ConfirmWipe = true
	res = callPreflight(t, inst.Host(), p)
	wantCheck(t, res, "dedicated", proto.CheckFail)
}

func TestPreflightReplicaLeftoverNeedsConfirm(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "CREATE DATABASE shop", "CREATE DATABASE crm")
	p := replicaParams(t, inst)
	p.PeerDatabases = []string{"crm", "shop", "web"}

	res := callPreflight(t, inst.Host(), p)
	c := wantCheck(t, res, "leftover", proto.CheckNeedsConfirm)
	if want := "database dari percobaan sebelumnya akan dibuang: crm, shop"; c.Detail != want {
		t.Errorf("leftover detail = %q, want %q", c.Detail, want)
	}
	wantCheck(t, res, "dedicated", proto.CheckOK)
	if res.Worst != proto.CheckNeedsConfirm {
		t.Errorf("Worst = %q, want needs_confirm", res.Worst)
	}

	p.ConfirmWipe = true
	res = callPreflight(t, inst.Host(), p)
	wantCheck(t, res, "leftover", proto.CheckOK)
	if res.Worst != proto.CheckOK {
		t.Errorf("Worst with ConfirmWipe = %q, want ok", res.Worst)
	}
	// The server still holds databases, so CLONE is not chosen.
	if res.Method != "dump" {
		t.Errorf("Method = %q, want dump", res.Method)
	}
}

func TestPreflightReplicaMethodDumpWhenSubset(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	p := replicaParams(t, inst)
	p.PeerDatabases = []string{"shop", "crm"}

	if res := callPreflight(t, inst.Host(), p); res.Method != "clone" {
		t.Errorf("all databases, empty server: Method = %q, want clone", res.Method)
	}
	sub := p
	sub.Databases = []string{"shop"}
	if res := callPreflight(t, inst.Host(), sub); res.Method != "dump" {
		t.Errorf("subset: Method = %q, want dump", res.Method)
	}

	// Before 8.0.37 CLONE needs the same patch release on both sides.
	old := p
	old.PeerVersion = "8.0.36"
	res := callPreflight(t, inst.Host(), old)
	wantCheck(t, res, "clone_patch", proto.CheckWarn)
	if res.Method != "dump" {
		t.Errorf("patch mismatch: Method = %q, want dump", res.Method)
	}

	other := p
	other.PeerVersion = "8.4.3"
	res = callPreflight(t, inst.Host(), other)
	if c := wantCheck(t, res, "same_series", proto.CheckFail); !strings.Contains(c.Detail, "8.4") {
		t.Errorf("same_series detail = %q", c.Detail)
	}
}

func TestPreflightRejectsMariaDBVersion(t *testing.T) {
	for _, v := range []string{"10.11.6-MariaDB-0ubuntu0.24.04.1", "5.7.44-log", "8.0.25"} {
		for _, role := range []string{"master", "replica"} {
			// Facts that would fail most other checks: none of them may run.
			f := mysql.PreflightFacts{
				Status:        proto.MySQLStatus{Version: v, BinlogFormat: "STATEMENT", BindAddress: "127.0.0.1", BinlogDumpThreads: 2},
				MissingGrants: []string{"GRANT RELOAD ON *.* TO 'x'@'%'"},
				NonInnoDB:     []string{"a.b (MyISAM)"}, NonInnoDBCount: 1,
				Unreachable: errors.New("refused"),
				Databases:   []proto.DatabaseInfo{{Name: "lain"}},
			}
			p := proto.PreflightParams{Role: role, PeerHost: "10.0.0.1", PeerPort: 3306, PeerVersion: "8.0.46"}
			res := mysql.EvaluatePreflight(p, f)
			if got := checkIDs(res); !reflect.DeepEqual(got, []string{"version"}) {
				t.Fatalf("%s %s: checks = %v, want [version]", role, v, got)
			}
			c := res.Checks[0]
			if c.Status != proto.CheckFail || !strings.Contains(c.Detail, mysql.ErrUnsupported.Error()) || !strings.Contains(c.Detail, v) {
				t.Errorf("%s %s: version = %+v", role, v, c)
			}
			if res.Worst != proto.CheckFail || res.Method != "" {
				t.Errorf("%s %s: Worst = %q, Method = %q", role, v, res.Worst, res.Method)
			}
		}
	}
}

// TestPreflightRules covers rules that need states a single test server
// cannot easily reach (replicas attached, a non-GTID master with replicas).
func TestPreflightRules(t *testing.T) {
	healthy := func() mysql.PreflightFacts {
		return mysql.PreflightFacts{
			Status: proto.MySQLStatus{Version: "8.0.46", Series: "8.0", ServerID: 7, LogBin: true, GTIDMode: "ON",
				EnforceGTID: true, BinlogFormat: "ROW", BindAddress: "*", DataDir: "/var/lib/mysql"},
			ClonePlugin: "ACTIVE", DiskFree: 100 * gib,
		}
	}
	master := proto.PreflightParams{Role: "master"}

	f := healthy()
	f.Status.BinlogDumpThreads = 2
	res := mysql.EvaluatePreflight(master, f)
	if c := wantCheck(t, res, "existing_replication", proto.CheckWarn); !strings.Contains(c.Detail, "2") {
		t.Errorf("existing_replication detail = %q", c.Detail)
	}

	f.Status.GTIDMode, f.Status.EnforceGTID = "OFF", false
	res = mysql.EvaluatePreflight(master, f)
	if c := wantCheck(t, res, "existing_replication", proto.CheckFail); !strings.Contains(c.Detail, "menaikkan gtid_mode akan memutus replika lama yang belum GTID") {
		t.Errorf("existing_replication detail = %q", c.Detail)
	}
	wantCheck(t, res, "gtid_mode", proto.CheckWarn)
	wantCheck(t, res, "enforce_gtid_consistency", proto.CheckWarn)

	// The server itself replicating from elsewhere is a warning even without GTID.
	f = healthy()
	f.Status.GTIDMode = "OFF"
	f.Status.Replica = &proto.ReplicaStatus{SourceHost: "10.0.0.9", SourcePort: 3306}
	res = mysql.EvaluatePreflight(master, f)
	if c := wantCheck(t, res, "existing_replication", proto.CheckWarn); !strings.Contains(c.Detail, "10.0.0.9:3306") {
		t.Errorf("existing_replication detail = %q", c.Detail)
	}

	f = healthy()
	f.Status.LogBin = false
	f.SkipNetworking = true
	f.ClonePlugin = ""
	res = mysql.EvaluatePreflight(master, f)
	wantCheck(t, res, "log_bin", proto.CheckNeedsRestart)
	if c := wantCheck(t, res, "bind_address", proto.CheckNeedsRestart); c.FixConfig != "skip-networking=OFF" {
		t.Errorf("bind_address FixConfig = %q", c.FixConfig)
	}
	if c := wantCheck(t, res, "clone_plugin", proto.CheckWarn); !strings.Contains(c.Detail, "akan diaktifkan") {
		t.Errorf("clone_plugin detail = %q", c.Detail)
	}
	if res.Worst != proto.CheckNeedsRestart {
		t.Errorf("Worst = %q", res.Worst)
	}

	for _, bind := range []string{"127.0.0.1", "::1", "localhost", "127.0.0.1,::1"} {
		f = healthy()
		f.Status.BindAddress = bind
		wantCheck(t, mysql.EvaluatePreflight(master, f), "bind_address", proto.CheckNeedsRestart)
	}
	for _, bind := range []string{"*", "0.0.0.0", "::", "10.0.0.5", "127.0.0.1,10.0.0.5"} {
		f = healthy()
		f.Status.BindAddress = bind
		wantCheck(t, mysql.EvaluatePreflight(master, f), "bind_address", proto.CheckOK)
	}

	// A replica that is itself a source refuses; a patch mismatch before
	// 8.0.37 on either side means dump.
	replica := proto.PreflightParams{Role: "replica", PeerHost: "10.0.0.1", PeerPort: 3306, PeerVersion: "8.0.46"}
	f = healthy()
	f.Status.BinlogDumpThreads = 1
	res = mysql.EvaluatePreflight(replica, f)
	wantCheck(t, res, "not_master", proto.CheckFail)
	if res.Method != "clone" {
		t.Errorf("Method = %q", res.Method)
	}
	f = healthy()
	f.Status.Version = "8.0.30"
	res = mysql.EvaluatePreflight(replica, f)
	wantCheck(t, res, "clone_patch", proto.CheckWarn)
	if res.Method != "dump" {
		t.Errorf("Method = %q, want dump", res.Method)
	}
	f.Status.Version = "8.0.46"
	if res := mysql.EvaluatePreflight(replica, f); res.Method != "clone" {
		t.Errorf("same patch: Method = %q, want clone", res.Method)
	}
	f.DiskErr = errors.New("statfs: no such file")
	if c := wantCheck(t, mysql.EvaluatePreflight(replica, f), "disk", proto.CheckFail); !strings.Contains(c.Detail, "statfs") {
		t.Errorf("disk detail = %q", c.Detail)
	}
}

func TestPreflightBadParams(t *testing.T) {
	for _, p := range []any{
		"not an object",
		proto.PreflightParams{Role: "primary"},
		proto.PreflightParams{Role: "replica", PeerPort: 3306, PeerVersion: "8.0.46"},
		proto.PreflightParams{Role: "replica", PeerHost: "10.0.0.1", PeerPort: 0, PeerVersion: "8.0.46"},
		proto.PreflightParams{Role: "replica", PeerHost: "10.0.0.1", PeerPort: 3306},
		proto.PreflightParams{Role: "master", Databases: []string{"shop`; DROP"}},
		proto.PreflightParams{Role: "master", ReplUser: "repl'@'%"},
	} {
		r := dispatchPreflight(t, nil, p)
		if r.OK || r.Error == nil || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%+v: response = %+v / %+v, want bad_params", p, r, r.Error)
		}
	}
}
