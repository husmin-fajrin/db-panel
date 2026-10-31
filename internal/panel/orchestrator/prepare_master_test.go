package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// scriptPrepare scripts a master whose preflight passes and whose configure
// needs a restart when restartReasons is not empty.
func scriptPrepare(fa *fakeAgent, restartReasons ...string) {
	fa.reply("mysql.preflight", proto.PreflightResult{Status: masterStatus(), Worst: proto.CheckWarn, Checks: []proto.Check{
		{ID: "repl_user", Status: proto.CheckWarn, Title: "User replikasi", Detail: "user dbpanel_repl belum ada; akan dibuat"},
	}})
	fa.reply("mysql.configure", proto.ConfigureResult{Applied: []string{"gtid_mode=ON"}, ServerID: 1,
		NeedsRestart: len(restartReasons) > 0, RestartReasons: restartReasons})
	fa.reply("mysql.createReplUser", nil)
	fa.reply("mysql.restart", proto.RestartResult{TookMs: 3000})
}

func TestPrepareMasterHappyPath(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	scriptPrepare(fa)

	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatalf("StartPrepareMaster: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindPrepareMaster || j.ServerID != fa.id {
		t.Fatalf("job = %+v", j)
	}
	if got, want := stepStatuses(steps), []string{store.StepDone, store.StepDone, store.StepSkipped, store.StepDone}; !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	wantCmds(t, "master", fa.cmds(0), []string{"mysql.preflight", "mysql.configure", "mysql.createReplUser", "mysql.status"})

	var pre proto.PreflightParams
	fa.params("mysql.preflight", &pre)
	if pre.Role != "master" || pre.ReplUser != "dbpanel_repl" {
		t.Fatalf("preflight params = %+v", pre)
	}
	var conf proto.ConfigureParams
	fa.params("mysql.configure", &conf)
	if conf.Role != "master" || conf.ServerID != 0 || !conf.EnableClone || !conf.GTID {
		t.Fatalf("configure params = %+v", conf)
	}
	var ru proto.ReplUserParams
	fa.params("mysql.createReplUser", &ru)
	if ru.User != "dbpanel_repl" || ru.FromHost != "%" || !regexp.MustCompile(`^[A-Za-z0-9]{24}$`).MatchString(ru.Password) {
		t.Fatalf("createReplUser params = %+v", ru)
	}

	topo, err := e.st.GetTopologyByMaster(fa.id)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if strings.Contains(string(topo.ReplPasswordEnc), ru.Password) {
		t.Fatal("replication password stored in clear")
	}
	plain, err := e.box.Open(topo.ReplPasswordEnc)
	if err != nil || string(plain) != ru.Password {
		t.Fatalf("sealed password opens to %q, %v; want the one sent to the agent", plain, err)
	}
	if topo.ReplUser != "dbpanel_repl" || topo.SourceHost != "10.0.0.1" || topo.SourcePort != 3306 {
		t.Fatalf("topology = %+v", topo)
	}
	inv, _ := e.st.GetInventory(fa.id)
	if !inv.MasterReady || inv.PreflightJSON == "" {
		t.Fatalf("inventory = %+v", inv)
	}
	if srv, _ := e.st.GetServer(fa.id); srv.Role != store.RoleMaster {
		t.Fatalf("role = %q", srv.Role)
	}
	if log := e.jobLog(id); strings.Contains(log, ru.Password) {
		t.Fatalf("job log holds the password:\n%s", log)
	}
	if e.published("job_update") == 0 || e.published("job_log") == 0 {
		t.Fatal("no job_update/job_log published")
	}

	// Running it again keeps the topology and its password.
	id2, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatalf("second StartPrepareMaster: %v", err)
	}
	e.waitJob(id2, store.JobSucceeded)
	fa.params("mysql.createReplUser", &ru)
	if again, _ := e.st.GetTopologyByMaster(fa.id); again.ID != topo.ID || ru.Password != string(plain) {
		t.Fatalf("second run: topology %d→%d, password changed %v", topo.ID, again.ID, ru.Password != string(plain))
	}
}

// scriptNeedsRestart makes the master's status show a loopback bind and
// the binary log off until restarted is set; mysql.restart sets it.
func scriptNeedsRestart(fa *fakeAgent, restarted *atomic.Bool) {
	scriptPrepare(fa, "log_bin masih OFF; butuh restart",
		"bind-address hanya localhost (127.0.0.1); replika tidak bisa terhubung sebelum restart")
	fa.on("mysql.status", func(json.RawMessage) (any, error) {
		st := masterStatus()
		if !restarted.Load() {
			st.LogBin, st.BindAddress = false, "127.0.0.1"
		}
		return st, nil
	})
	fa.on("mysql.restart", func(json.RawMessage) (any, error) {
		restarted.Store(true)
		return proto.RestartResult{TookMs: 3000}, nil
	})
}

func TestPrepareMasterWaitsForRestartApproval(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	var restarted atomic.Bool
	scriptNeedsRestart(fa, &restarted)

	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 2 || steps[2].Status != store.StepWaiting {
		t.Fatalf("job %+v steps %+v", j, steps)
	}
	msg := steps[2].Message
	for _, want := range []string{"log_bin masih OFF", "bind-address hanya localhost",
		"Aplikasi yang memakai database ini akan terputus beberapa detik; lakukan di jam sepi.",
		"MySQL akan menerima koneksi dari semua alamat jaringan; pastikan firewall membatasi port 3306."} {
		if !strings.Contains(msg, want) {
			t.Errorf("waiting message lacks %q:\n%s", want, msg)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := fa.count("mysql.restart"); n != 0 {
		t.Fatalf("mysql.restart called %d times before Approve", n)
	}
	if inv, _ := e.st.GetInventory(fa.id); inv.MasterReady {
		t.Fatal("MasterReady before the restart")
	}

	if err := e.r.Approve(id); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	_, steps = e.waitJob(id, store.JobSucceeded)
	if steps[2].Status != store.StepDone {
		t.Fatalf("restart step = %+v", steps[2])
	}
	wantCmds(t, "master", fa.cmds(0), []string{"mysql.preflight", "mysql.configure", "mysql.createReplUser", "mysql.status",
		"mysql.restart", "mysql.status"})
	if j, _, _ := e.st.GetJob(id); !strings.Contains(j.ParamsJSON, `"wait_reason":"restart"`) {
		t.Errorf("params do not name the restart wait: %s", j.ParamsJSON)
	}
	if inv, _ := e.st.GetInventory(fa.id); !inv.MasterReady {
		t.Fatal("MasterReady not set")
	}
	if err := e.r.Approve(id); err == nil {
		t.Fatal("Approve of a finished job succeeded")
	}
}

// TestPrepareMasterRecheckAfterOwnRestart: "Sudah saya restart — periksa
// lagi" (Retry on the waiting restart step) reads the values in effect: not
// restarted yet, it keeps waiting; restarted by the user, it goes on
// without restarting the master again.
func TestPrepareMasterRecheckAfterOwnRestart(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	var restarted atomic.Bool
	scriptNeedsRestart(fa, &restarted)
	fa.on("mysql.restart", func(json.RawMessage) (any, error) {
		t.Error("the panel restarted the master although the user did")
		return proto.RestartResult{}, nil
	})
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobWaiting)
	if _, steps, _ := e.st.GetJob(id); !strings.Contains(steps[2].Message, "Sudah saya restart") {
		t.Errorf("waiting message does not offer the re-check:\n%s", steps[2].Message)
	}

	// Not restarted yet: it waits again and says what is still pending.
	mark := fa.mark()
	if err := e.r.Retry(id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("second status read", func() bool { return fa.count("mysql.status") >= 2 })
	j, _ := e.waitJob(id, store.JobWaiting)
	if j.CurrentStep != 2 {
		t.Fatalf("job = %+v", j)
	}
	wantCmds(t, "master after the first re-check", fa.cmds(mark), []string{"mysql.status"})
	if log := e.jobLog(id); !strings.Contains(log, "MySQL belum direstart: binary log masih OFF") {
		t.Errorf("job log does not say what is pending:\n%s", log)
	}

	restarted.Store(true) // the user restarts MySQL at 2 am
	if err := e.r.Retry(id); err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if steps[2].Status != store.StepDone || !strings.Contains(steps[2].Message, "di luar panel") {
		t.Fatalf("restart step = %+v", steps[2])
	}
	if n := fa.count("mysql.restart"); n != 0 {
		t.Fatalf("mysql.restart called %d times", n)
	}
}

// TestPrepareMasterMixedBinlogFormat: a master with binlog_format MIXED and
// server_id 0 only warns at the checks; configure sets both, and the log
// says open connections keep the old format until they reconnect.
func TestPrepareMasterMixedBinlogFormat(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	scriptPrepare(fa)
	st := masterStatus()
	st.BinlogFormat, st.ServerID = "MIXED", 0
	fa.reply("mysql.preflight", proto.PreflightResult{Status: st, Worst: proto.CheckWarn, Checks: []proto.Check{
		{ID: "server_id", Status: proto.CheckWarn, Title: "server_id", Detail: "server_id masih 0"},
		{ID: "binlog_format", Status: proto.CheckWarn, Title: "Format binlog", Detail: "binlog_format = MIXED; replikasi butuh ROW"},
	}})
	fa.reply("mysql.configure", proto.ConfigureResult{Applied: []string{"server_id=4242", "binlog_format=ROW"}, ServerID: 4242})
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	log := e.jobLog(id)
	for _, want := range []string{"Format binlog [warn]", "Diterapkan: binlog_format=ROW",
		"Koneksi aplikasi yang sudah terbuka tetap memakai format lama sampai tersambung ulang"} {
		if !strings.Contains(log, want) {
			t.Errorf("job log lacks %q:\n%s", want, log)
		}
	}
	var ru proto.ReplUserParams
	fa.params("mysql.createReplUser", &ru)
	if !ru.RequireSSL {
		t.Error("the replication account does not require TLS on a master that offers it")
	}
}

func TestPrepareMasterRefusesMissingGrants(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	inv, _ := e.st.GetInventory(fa.id)
	inv.MissingGrants = []string{"GRANT RELOAD ON *.* TO `adm`@`%`"}
	if err := e.st.SetInventory(inv); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartPrepareMaster(context.Background(), fa.id); err == nil || !strings.Contains(err.Error(), "GRANT") {
		t.Fatalf("err = %v, want a refusal naming GRANT", err)
	}

	other := e.agent("db-unconnected", "10.0.0.3")
	if _, err := e.r.StartPrepareMaster(context.Background(), other.id); err == nil {
		t.Fatal("unconnected MySQL accepted")
	}
	if jobs, _ := e.st.ListJobs(0); len(jobs) != 0 {
		t.Fatalf("jobs created: %+v", jobs)
	}
	if len(fa.cmds(0)) != 0 {
		t.Fatalf("agent called: %v", fa.cmds(0))
	}
}

// TestPrepareMasterFailsOnPreflight: a failing check stops the job at step
// 0 with the check's words; the failure is the step's and the job's error.
func TestPrepareMasterFailsOnPreflight(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	fa.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckFail, Checks: []proto.Check{
		{ID: "non_innodb", Status: proto.CheckFail, Title: "Tabel non-InnoDB", Detail: "a.t (MyISAM)"},
	}})
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 0 || steps[0].Status != store.StepFailed || !strings.Contains(j.Error, "a.t (MyISAM)") ||
		!strings.Contains(j.Error, "Periksa master") {
		t.Fatalf("job %+v steps %+v", j, steps)
	}
	if err := e.r.Approve(id); err == nil {
		t.Fatal("Approve of a failed job succeeded")
	}
	// Retry runs the step again with the same params.
	scriptPrepare(fa)
	if err := e.r.Retry(id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if err := e.r.Retry(id); !errors.Is(err, store.ErrJobFinished) {
		t.Fatalf("Retry of a succeeded job: err = %v", err)
	}
}

// TestConnectStoresInventory: Connect and RefreshInventory store what the
// agent answers and keep what the panel learned itself (MasterReady).
func TestConnectStoresInventory(t *testing.T) {
	e := newEnv(t)
	fa, _ := e.readyMaster("db-master")
	res := proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.4", Series: "8.4"},
		Databases: []proto.DatabaseInfo{{Name: "c", Bytes: 5}}, MissingGrants: []string{"GRANT RELOAD ON *.* TO `u`@`%`"}}
	fa.reply("mysql.connect", res)
	got, err := e.r.Connect(context.Background(), fa.id, proto.ConnectParams{User: "adm", Password: "pw"})
	if err != nil || got.Status.Version != "8.4.4" {
		t.Fatalf("Connect = %+v, %v", got, err)
	}
	var cp proto.ConnectParams
	fa.params("mysql.connect", &cp)
	if cp.User != "adm" || cp.Password != "pw" {
		t.Fatalf("connect params = %+v", cp)
	}
	inv, _ := e.st.GetInventory(fa.id)
	if !inv.Connected || inv.Version != "8.4.4" || len(inv.Databases) != 1 || len(inv.MissingGrants) != 1 || !inv.MasterReady {
		t.Fatalf("inventory = %+v", inv)
	}

	fa.fail("mysql.inventory", "login MySQL gagal: 1045 Access denied")
	if _, err := e.r.RefreshInventory(context.Background(), fa.id); err == nil || !strings.Contains(err.Error(), "1045") {
		t.Fatalf("RefreshInventory err = %v", err)
	}
	if again, _ := e.st.GetInventory(fa.id); again.Version != "8.4.4" {
		t.Fatalf("failed refresh changed the inventory: %+v", again)
	}
	if e.published("mysql_inventory") != 1 {
		t.Fatalf("mysql_inventory published %d times", e.published("mysql_inventory"))
	}
}
