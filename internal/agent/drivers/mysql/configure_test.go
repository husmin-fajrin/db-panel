package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// gtidOff starts an instance the way a fresh distro MySQL runs: no GTID.
var gtidOff = []string{"--gtid-mode=OFF", "--enforce-gtid-consistency=OFF"}

// dispatch runs cmd through a registry holding a driver over h.
func dispatch(t *testing.T, h mysql.Host, cmd string, params any) proto.Response {
	t.Helper()
	reg := commands.NewRegistry()
	(&mysql.Driver{Host: h}).Register(reg)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return reg.Dispatch(ctx, proto.Request{ID: "1", Cmd: cmd, Params: raw})
}

// decode fails the test unless r is OK and unmarshals its result into T.
func decode[T any](t *testing.T, r proto.Response) T {
	t.Helper()
	var v T
	if !r.OK {
		t.Fatalf("response = %+v", r.Error)
	}
	if err := json.Unmarshal(r.Result, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func configure(t *testing.T, h mysql.Host, p proto.ConfigureParams) proto.ConfigureResult {
	t.Helper()
	return decode[proto.ConfigureResult](t, dispatch(t, h, "mysql.configure", p))
}

func status(t *testing.T, h mysql.Host) proto.MySQLStatus {
	t.Helper()
	db, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := mysql.Status(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func variable(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var n, v string
	if err := db.QueryRow("SHOW GLOBAL VARIABLES LIKE '"+name+"'").Scan(&n, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func TestRenderConfig(t *testing.T) {
	master := mysql.RenderConfig("master", 1234, false)
	want := "[mysqld]\nlog_bin=binlog\nbinlog_expire_logs_seconds=604800\ngtid_mode=ON\nenforce_gtid_consistency=ON\n" +
		"server_id=1234\nbinlog_format=ROW\n"
	if master != want {
		t.Errorf("master block =\n%s\nwant\n%s", master, want)
	}
	if got := mysql.RenderConfig("master", 1234, true); got != want+"bind_address=0.0.0.0\n" {
		t.Errorf("master block with bindAll =\n%s", got)
	}
	replica := mysql.RenderConfig("replica", 99, false)
	want = "[mysqld]\nlog_bin=binlog\nbinlog_expire_logs_seconds=604800\ngtid_mode=ON\nenforce_gtid_consistency=ON\n" +
		"server_id=99\nbinlog_format=ROW\nread_only=ON\nsuper_read_only=ON\nrelay_log=relay\nlog_replica_updates=ON\n"
	if replica != want {
		t.Errorf("replica block =\n%s\nwant\n%s", replica, want)
	}
}

func TestRestartReasons(t *testing.T) {
	healthy := proto.MySQLStatus{LogBin: true, BindAddress: "*"}
	if got := mysql.RestartReasons("master", healthy); len(got) != 0 {
		t.Errorf("healthy master: %v", got)
	}
	got := mysql.RestartReasons("master", proto.MySQLStatus{LogBin: false, BindAddress: "127.0.0.1"})
	if len(got) != 2 || !strings.Contains(got[0], "log_bin masih OFF") || !strings.Contains(got[1], "bind-address hanya localhost") {
		t.Errorf("master reasons = %q", got)
	}
	if got := mysql.RestartReasons("master", proto.MySQLStatus{LogBin: true, BindAddress: "127.0.0.1,::1"}); len(got) != 1 {
		t.Errorf("loopback-only bind: %q", got)
	}
	if got := mysql.RestartReasons("replica", healthy); len(got) != 1 {
		t.Errorf("replica reasons = %q, want one", got)
	}
}

func TestParseOptionsAndNotInEffect(t *testing.T) {
	want := mysql.ParseOptions("[mysqld]\n# comment\nlog-bin=binlog\nserver_id = 7\nsuper_read_only=ON\nrelay_log=relay\nskip_name_resolve\n")
	if !reflect.DeepEqual(want, map[string]string{
		"log_bin": "binlog", "server_id": "7", "super_read_only": "ON", "relay_log": "relay", "skip_name_resolve": "ON",
	}) {
		t.Fatalf("ParseOptions = %v", want)
	}
	have := map[string]string{"log_bin": "ON", "server_id": "7", "super_read_only": "1", "relay_log": "relay", "skip_name_resolve": "ON"}
	if got := mysql.NotInEffect(want, have); len(got) != 0 {
		t.Errorf("all in effect, got %q", got)
	}
	have["super_read_only"] = "OFF"
	have["log_bin"] = "OFF"
	delete(have, "relay_log")
	got := mysql.NotInEffect(want, have)
	// relay_log is not a variable of this server: not checked.
	if strings.Join(got, "; ") != "log_bin=OFF (diminta ON); super_read_only=OFF (diminta ON)" {
		t.Errorf("NotInEffect = %q", got)
	}
}

func TestConfigureMasterSetsGTIDAndWritesBlock(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: append([]string{"binlog-format=MIXED"}, gtidOff...)})
	h := inst.Host()
	root, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}

	res := configure(t, h, proto.ConfigureParams{Role: "master", GTID: true, EnableClone: true})
	st := status(t, h)
	if st.GTIDMode != "ON" || !st.EnforceGTID || st.BinlogFormat != "ROW" {
		t.Errorf("after configure: gtid_mode=%s enforce=%v binlog_format=%s", st.GTIDMode, st.EnforceGTID, st.BinlogFormat)
	}
	// A server_id other than the default is kept.
	if res.ServerID != inst.ServerID || st.ServerID != inst.ServerID {
		t.Errorf("ServerID = %d (server %d), want %d", res.ServerID, st.ServerID, inst.ServerID)
	}
	wantApplied := []string{"binlog_format=ROW", "enforce_gtid_consistency=ON", "gtid_mode=OFF_PERMISSIVE",
		"gtid_mode=ON_PERMISSIVE", "gtid_mode=ON", "plugin clone"}
	if !reflect.DeepEqual(res.Applied, wantApplied) {
		t.Errorf("Applied = %q, want %q", res.Applied, wantApplied)
	}
	// The harness binds 127.0.0.1: a master must listen beyond loopback.
	if !res.NeedsRestart || len(res.RestartReasons) != 1 || !strings.Contains(res.RestartReasons[0], "bind-address") {
		t.Errorf("NeedsRestart = %v %q", res.NeedsRestart, res.RestartReasons)
	}
	var plugin string
	if err := root.QueryRow("SELECT PLUGIN_STATUS FROM information_schema.PLUGINS WHERE PLUGIN_NAME = 'clone'").Scan(&plugin); err != nil || plugin != "ACTIVE" {
		t.Errorf("clone plugin = %q, %v", plugin, err)
	}
	// Online changes survive a restart through mysqld-auto.cnf.
	var persisted int
	if err := root.QueryRow(`SELECT COUNT(*) FROM performance_schema.persisted_variables
		WHERE VARIABLE_NAME IN ('gtid_mode', 'enforce_gtid_consistency', 'binlog_format')`).Scan(&persisted); err != nil || persisted != 3 {
		t.Errorf("persisted variables = %d, %v", persisted, err)
	}

	if res.ConfigPath != inst.ConfigFile {
		t.Errorf("ConfigPath = %q, want %q", res.ConfigPath, inst.ConfigFile)
	}
	cnf, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	block := "# BEGIN dbpanel\n" + mysql.RenderConfig("master", inst.ServerID, true) + "# END dbpanel\n"
	if !strings.HasSuffix(string(cnf), block) {
		t.Errorf("my.cnf does not end with the block:\n%s", cnf)
	}
	if err := h.ValidateConfig(context.Background()); err != nil {
		t.Errorf("ValidateConfig: %v", err)
	}

	// A second run changes nothing online; an explicit server_id is applied.
	res = configure(t, h, proto.ConfigureParams{Role: "master", ServerID: 4242, GTID: true, EnableClone: true})
	if !reflect.DeepEqual(res.Applied, []string{"server_id=4242"}) || res.ServerID != 4242 {
		t.Errorf("second run: Applied = %q, ServerID = %d", res.Applied, res.ServerID)
	}
	if got := variable(t, root, "server_id"); got != "4242" {
		t.Errorf("server_id = %s", got)
	}
	root.Close() // before its connections die in the restart below

	// A config mysqld refuses is rolled back.
	before, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	r := dispatch(t, invalidConfig{h}, "mysql.configure", proto.ConfigureParams{Role: "replica"})
	if r.OK || !strings.Contains(r.Error.Message, "tidak valid") {
		t.Fatalf("configure with invalid config = %+v", r)
	}
	after, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("config not restored:\n%s", after)
	}

	// After the restart the master listens beyond loopback, and configuring
	// again keeps the line that makes it do so.
	decode[proto.RestartResult](t, dispatch(t, h, "mysql.restart", struct{}{}))
	if st := status(t, h); st.BindAddress != "0.0.0.0" || st.ServerID != 4242 {
		t.Errorf("after restart: bind_address=%s server_id=%d", st.BindAddress, st.ServerID)
	}
	res = configure(t, h, proto.ConfigureParams{Role: "master", GTID: true, EnableClone: true})
	if res.NeedsRestart || len(res.Applied) != 0 || res.ServerID != 4242 {
		t.Errorf("after restart: NeedsRestart=%v %q Applied=%q ServerID=%d", res.NeedsRestart, res.RestartReasons, res.Applied, res.ServerID)
	}
	if cnf, err := os.ReadFile(inst.ConfigFile); err != nil || !strings.Contains(string(cnf), "bind_address=0.0.0.0\n") {
		t.Errorf("bind_address line dropped (%v):\n%s", err, cnf)
	}
}

// invalidConfig is a Host whose mysqld refuses every config.
type invalidConfig struct{ mysql.Host }

func (invalidConfig) ValidateConfig(context.Context) error {
	return errors.New("konfigurasi MySQL tidak valid: unknown variable")
}

func TestConfigureReportsRestartWhenLogBinOff(t *testing.T) {
	// server_id 1 is the default: configure picks a new one.
	inst := testmysql.Start(t, testmysql.Options{ServerID: 1, ExtraArgs: append([]string{"skip-log-bin", "bind-address=0.0.0.0"}, gtidOff...)})
	h := inst.Host()

	res := configure(t, h, proto.ConfigureParams{Role: "master"})
	if !res.NeedsRestart || len(res.RestartReasons) != 1 || !strings.Contains(res.RestartReasons[0], "log_bin masih OFF") {
		t.Errorf("NeedsRestart = %v %q", res.NeedsRestart, res.RestartReasons)
	}
	if res.ServerID < 2 || res.ServerID == 1 {
		t.Errorf("ServerID = %d, want a new one", res.ServerID)
	}
	st := status(t, h)
	if st.ServerID != res.ServerID {
		t.Errorf("server_id = %d, want %d", st.ServerID, res.ServerID)
	}
	// GTID not requested: gtid_mode is left for the restart.
	if st.GTIDMode != "OFF" {
		t.Errorf("gtid_mode = %s, want OFF", st.GTIDMode)
	}
	if !reflect.DeepEqual(res.Applied, []string{"server_id=" + strconv.FormatUint(uint64(res.ServerID), 10)}) {
		t.Errorf("Applied = %q", res.Applied)
	}
}

func TestRestartAppliesReplicaConfig(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: gtidOff})
	h := inst.Host()

	res := configure(t, h, proto.ConfigureParams{Role: "replica", GTID: true})
	if !res.NeedsRestart || len(res.RestartReasons) == 0 {
		t.Errorf("replica must restart: %v %q", res.NeedsRestart, res.RestartReasons)
	}
	if st := status(t, h); st.SuperReadOnly {
		t.Fatal("super_read_only already ON before the restart")
	}

	rr := decode[proto.RestartResult](t, dispatch(t, h, "mysql.restart", struct{}{}))
	if rr.TookMs <= 0 {
		t.Errorf("TookMs = %d", rr.TookMs)
	}
	st := status(t, h)
	if !st.ReadOnly || !st.SuperReadOnly || !st.LogBin || st.GTIDMode != "ON" || !st.EnforceGTID || st.ServerID != res.ServerID {
		t.Errorf("after restart: %+v", st)
	}
	root, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if v := variable(t, root, "log_replica_updates"); v != "ON" {
		t.Errorf("log_replica_updates = %s", v)
	}
	if v := variable(t, root, "binlog_expire_logs_seconds"); v != "604800" {
		t.Errorf("binlog_expire_logs_seconds = %s", v)
	}
	root.Close() // before its connection dies in the next restart

	// read_only off is persisted and overrides the block on the next
	// restart: mysql.restart names what is not in effect.
	if r := dispatch(t, h, "mysql.setReadOnly", proto.ReadOnlyParams{On: false}); !r.OK {
		t.Fatalf("setReadOnly off: %+v", r.Error)
	}
	if st := status(t, h); st.ReadOnly || st.SuperReadOnly {
		t.Errorf("after setReadOnly off: read_only=%v super_read_only=%v", st.ReadOnly, st.SuperReadOnly)
	}
	r := dispatch(t, h, "mysql.restart", struct{}{})
	if r.OK || !strings.Contains(r.Error.Message, "read_only=OFF (diminta ON); super_read_only=OFF (diminta ON)") {
		t.Errorf("restart with persisted read_only off = %+v", r)
	}

	if r := dispatch(t, h, "mysql.setReadOnly", proto.ReadOnlyParams{On: true}); !r.OK {
		t.Fatalf("setReadOnly on: %+v", r.Error)
	}
	if st := status(t, h); !st.ReadOnly || !st.SuperReadOnly {
		t.Errorf("after setReadOnly on: read_only=%v super_read_only=%v", st.ReadOnly, st.SuperReadOnly)
	}
}
