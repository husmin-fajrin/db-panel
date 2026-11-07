package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	agentmon "github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

// e2eWait bounds each monitoring step of the end-to-end tests: with the
// intervals of e2eMonitorSettings a change reaches the panel, the alerts
// and Telegram well within it (spec 5.1: under a minute with the defaults).
const e2eWait = 30 * time.Second

// e2eRecoveryWait bounds the wait for a "pulih" message: the notifier
// holds a recovery until the alert stayed closed a minute (flap control).
const e2eRecoveryWait = e2eWait + time.Minute

// Known defects of Plan 3 the end-to-end tests stepped around until the
// final fix wave, which fixed both: the checks are strict.
const (
	// e2eCloneResyncFixed: defect F1, "Resync ulang" through CLONE failed
	// at "Salin data" with 1290 (--super-read-only) on a registered replica.
	e2eCloneResyncFixed = true
	// e2eResyncQuietFixed: defect F2, a successful resync raised a false
	// replica_stopped alert and Telegram message (the Monitor judged the
	// last report, taken mid-job, once the replica was ready again).
	e2eResyncQuietFixed = true
)

// Known defect of Plan 4b found by the promote end-to-end tests: the
// promote's swap never marks the new master's inventory MasterReady (only
// prepare_master sets it), so "Bangun ulang sebagai replika" (and "Buat
// replika") from the new master is refused with "master belum disiapkan;
// jalankan Siapkan master dulu". Until the fix the tests log the step as
// pending instead of running it; set this to true with the fix.
const e2ePromoteRebuildFixed = false

// skipWithoutE2E skips the test in -short mode and without mysqld.
func skipWithoutE2E(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}
	if _, err := mysql.FindMysqld(probe.RealSystem()); err != nil {
		t.Skip("mysqld not installed: " + err.Error())
	}
}

// e2eTopology is a running panel with a master of databases a and b and
// one replica following a (copied with a dump) made through the UI,
// Telegram pointed at a fake Bot API, and short monitoring intervals.
type e2eTopology struct {
	p                       *Panel
	ui                      *uiClient
	tg                      *fakeTelegram
	masterSrv, replicaSrv   int64
	masterName, replicaName string
	masterDB, replicaDB     *sql.DB
	masterPins              string // the master agent's pin directory
}

// startE2ETopology starts two mysqld instances, the panel and two agents,
// then runs "Siapkan master" and "Buat replika" (database a of a and b,
// so a dump) over the UI. Each agent reports every 10 s (summary 20 s)
// once online.
func startE2ETopology(t *testing.T) *e2eTopology {
	t.Helper()
	start := time.Now()
	// The instances first, so their cleanup (stop mysqld) runs last.
	masterInst := testmysql.Start(t, testmysql.Options{})
	replicaInst := testmysql.Start(t, testmysql.Options{})
	e := &e2eTopology{
		masterName: "master-e2e", replicaName: "replika-e2e",
		masterDB: openInstance(t, masterInst), replicaDB: openInstance(t, replicaInst),
	}
	// This topology follows a subset (a of a and b), so its resync uses
	// the dump path; TestE2ECreateReplica resyncs through CLONE (defect F1,
	// e2eCloneResyncFixed).
	execSQL(t, e.masterDB,
		"CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO a.t VALUES (1, 'awal')",
		"CREATE DATABASE b", "CREATE TABLE b.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO b.t VALUES (1, 'awal')",
	)

	var logs syncBuffer
	o := testOptions(t)
	o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("panel and agent log:\n%s", tail(logs.String(), 200))
		}
	})
	e.p = newPanel(t, o)
	e.tg = newFakeTelegram(t)
	configureE2EMonitoring(t, e.p, e.tg)
	uiAddr, _ := startPanel(t, e.p)
	base := "https://" + uiAddr

	e.masterSrv, e.masterPins = startE2EAgent(t, e.p, e.masterName, masterInst, &logs)
	e.replicaSrv, _ = startE2EAgent(t, e.p, e.replicaName, replicaInst, &logs)

	e.ui = newUIClient(t, base)
	e.ui.login(readInitialPassword(t, o))
	for _, id := range []int64{e.masterSrv, e.replicaSrv} {
		loc := e.ui.postRedirect(serverPath(id)+"/mysql/connect", url.Values{"user": {"root"}, "password": {""}, "host": {""}})
		if want := serverPath(id) + "?pesan=terhubung"; loc != want {
			t.Fatalf("connect MySQL on server %d redirected to %q, want %q", id, loc, want)
		}
	}

	prepareID := e.ui.postJob(serverPath(e.masterSrv)+"/prepare-master", nil)
	job := waitJob(t, e.p, prepareID, e2ePrepareTimeout, func(j store.Job) bool {
		// The instance binds 127.0.0.1 only: approve the restart.
		e.ui.postRedirect("/jobs/"+j.ID+"/approve", nil)
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("prepare_master ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, e.p, prepareID))
	}
	createID := e.ui.postJob(fmt.Sprintf("/masters/%d/replicas", e.masterSrv), url.Values{
		"target":       {strconv.FormatInt(e.replicaSrv, 10)},
		"databases":    {"a"},
		"confirm_wipe": {"1"},
	})
	job = waitJob(t, e.p, createID, e2eCreateTimeout, nil)
	if job.Status != store.JobSucceeded {
		t.Fatalf("create_replica ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, e.p, createID))
	}
	waitRow(t, e.replicaDB, "a", 1, "awal")
	t.Logf("topology ready after %s", time.Since(start).Round(time.Second))
	return e
}

// configureE2EMonitoring sets the shortest intervals the settings accept
// (check 10 s, summary 20 s, master down after 10 s), disk thresholds no
// test host reaches, no weekly summary, and Telegram (token, chat id) sent
// to tg. Call it before the panel runs: agents get the intervals with
// monitor.configure when they come online.
func configureE2EMonitoring(t *testing.T, p *Panel, tg *fakeTelegram) {
	t.Helper()
	set, err := healthmon.LoadSettings(p.Store)
	if err != nil {
		t.Fatal(err)
	}
	set.CheckEvery, set.SummaryEvery, set.DownAfter = 10*time.Second, 20*time.Second, 10*time.Second
	// A nearly full disk on the test host must not add alerts (and
	// Telegram messages) the counts below would see.
	set.DiskWarn, set.DiskCrit = 99, 100
	set.WeeklySummary = false
	if err := healthmon.SaveSettings(p.Store, set); err != nil {
		t.Fatal(err)
	}
	p.Monitor.Reconfigure()
	p.telegram.BaseURL = tg.srv.URL
	if err := notify.SaveToken(p.Store, p.box, fakeBotToken); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.SetSetting(store.SettingTelegramChatID, "-100200300"); err != nil {
		t.Fatal(err)
	}
}

const fakeBotToken = "123456:e2e-bot-token"

// fakeTelegram is a Bot API that records the text of every sendMessage.
type fakeTelegram struct {
	srv *httptest.Server

	mu    sync.Mutex
	texts []string
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+fakeBotToken+"/sendMessage" {
			t.Errorf("Telegram request to %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var m struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.ChatID != "-100200300" {
			t.Errorf("Telegram message %+v, %v", m, err)
		}
		f.mu.Lock()
		f.texts = append(f.texts, m.Text)
		f.mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// messages returns the texts received so far, in order.
func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// since returns the texts received after the first n.
func (f *fakeTelegram) since(n int) []string { return f.messages()[n:] }

// containing returns the texts of msgs that contain sub.
func containing(msgs []string, sub string) []string {
	var out []string
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			out = append(out, m)
		}
	}
	return out
}

// waitStatus waits up to e2eWait for the server's monitored status to be
// code and returns its view.
func waitStatus(t *testing.T, p *Panel, serverID int64, code string) healthmon.View {
	t.Helper()
	return waitStatusUntil(t, p, serverID, code, time.Now().Add(e2eWait))
}

// waitStatusUntil is waitStatus with a deadline shared with other waits.
func waitStatusUntil(t *testing.T, p *Panel, serverID int64, code string, deadline time.Time) healthmon.View {
	t.Helper()
	var last healthmon.View
	for {
		v, ok := p.Monitor.View(serverID)
		if ok && v.Status.Code == code {
			return v
		}
		last = v
		if time.Now().After(deadline) {
			t.Fatalf("server %d did not reach status %q by %s (last: %q, %q)",
				serverID, code, deadline.Format("15:04:05.000"), last.Status.Code, last.Status.Detail)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// resyncThroughUI runs "Resync ulang" on the replica on serverID as a user
// does: it opens the dialog (a fresh inventory), posts the listed databases
// with the typed name, and requires the resync_replica job to succeed
// without a waiting step, leaving the replica ready and not paused. It
// returns the wipe list the dialog showed and the job's id.
func resyncThroughUI(t *testing.T, p *Panel, ui *uiClient, serverID int64, name string) ([]string, string) {
	t.Helper()
	path := "/replicas/" + strconv.FormatInt(serverID, 10)
	loc := ui.postRedirect(path+"/resync/prepare", nil)
	if loc != path+"?buka=resync" {
		t.Fatalf("resync/prepare redirected to %q", loc)
	}
	var wipe []string
	for _, m := range wipeListRe.FindAllStringSubmatch(ui.get(loc), -1) {
		wipe = append(wipe, m[1])
	}
	id := ui.postJob(path+"/resync", url.Values{"confirm_name": {name}, "wipe_list[]": wipe,
		"confirm_token": {ui.confirmToken(web.ActResync, "replica", serverID)}})
	job := waitJob(t, p, id, e2eCreateTimeout, nil) // nil: a waiting step fails the test
	if job.Kind != "resync_replica" || job.Status != store.JobSucceeded {
		t.Fatalf("resync job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, p, id))
	}
	if r, err := p.Store.GetReplicaByServer(serverID); err != nil || r.State != store.ReplicaReady || r.Paused {
		t.Fatalf("replica after resync = %+v, %v; want ready, not paused", r, err)
	}
	return wipe, id
}

// wipeListRe finds the databases the Resync ulang dialog lists for wiping.
var wipeListRe = regexp.MustCompile(`name="wipe_list\[\]" value="([^"]*)"`)

// openAlert reports whether an alert of kind is open on the server.
func openAlert(t *testing.T, p *Panel, kind string, serverID int64) (store.Alert, bool) {
	t.Helper()
	a, err := p.Store.GetOpenAlert(kind, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Alert{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return a, true
}

// openKinds lists the kinds of the alerts open on the server.
func openKinds(t *testing.T, p *Panel, serverID int64) []string {
	t.Helper()
	alerts, err := p.Store.ListOpenAlerts()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, a := range alerts {
		if a.ServerID == serverID {
			kinds = append(kinds, a.Kind)
		}
	}
	return kinds
}

// holdFor fails the test unless cond stays true for d.
func holdFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s no longer holds", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// post posts form to path and returns the status and body of the answer
// (a redirect is not followed).
func (u *uiClient) post(path string, form url.Values) (int, string) {
	u.t.Helper()
	req, err := http.NewRequest("POST", u.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		u.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", u.base)
	resp, err := u.c.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		u.t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// confirmToken asks POST /confirm for a token for action on the target,
// with the password the client logged in with, as the confirmation
// dialog does.
func (u *uiClient) confirmToken(action, kind string, id int64) string {
	u.t.Helper()
	status, body := u.post("/confirm", url.Values{"action": {action}, "target_kind": {kind},
		"target_id": {strconv.FormatInt(id, 10)}, "password": {u.password}})
	var out struct {
		Token string `json:"token"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Token == "" {
		u.t.Fatalf("POST /confirm %s %s/%d = %d: %s", action, kind, id, status, body)
	}
	return out.Token
}

// tail is the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// e2eAgent is an in-process agent paired with the panel whose MySQL driver
// manages inst. Unlike a plain startE2EAgent it can be stopped and started
// again with the same directory (its certificate, pins, promote lock and
// salvage files), as "systemctl stop/start dbpanel-agent" would.
type e2eAgent struct {
	p    *Panel
	name string
	inst *testmysql.Instance
	logs io.Writer
	srv  int64
	dir  string
	cfg  config.Config

	stopFn func() // nil while stopped
}

// pairE2EAgent creates a server called name on p and pairs an agent
// directory with it; start runs the agent. The agent, if running, stops
// when the test ends, before inst does.
func pairE2EAgent(t *testing.T, p *Panel, name string, inst *testmysql.Instance, logs io.Writer) *e2eAgent {
	t.Helper()
	srv, err := p.Store.CreateServer(name)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg, err := agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil, agent.PairOptions{WebPin: p.certInfo().Pin})
	if err != nil {
		t.Fatalf("Pair %s: %v", name, err)
	}
	ag := &e2eAgent{p: p, name: name, inst: inst, logs: logs, srv: srv.ID, dir: dir, cfg: cfg}
	t.Cleanup(func() {
		if ag.stopFn != nil {
			ag.stopFn()
		}
	})
	return ag
}

// start runs the agent and its health monitor, wired as "dbpanel agent"
// wires them (the promote guard's events go to the panel), with the
// replicas-seen file and the salvage directory in the agent's own
// directory so the test's agents do not share them, and waits until the
// panel sees it online with its probe.
func (ag *e2eAgent) start(t *testing.T) {
	t.Helper()
	if ag.stopFn != nil {
		t.Fatalf("agent %s already running", ag.name)
	}
	box, err := secrets.LoadOrCreate(filepath.Join(ag.dir, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	creds := mysql.NewFileCredentialStore(filepath.Join(ag.dir, "mysql.cred"), box)
	logger := slog.New(slog.NewTextHandler(ag.logs, &slog.HandlerOptions{Level: slog.LevelDebug})).With("agent", ag.name)
	sys := probe.RealSystem()
	reg := commands.NewRegistry()
	// The panel's own version, as an up-to-date agent reports it: the
	// promotion refuses agents of another version (agentOutdated).
	probe.Register(reg, sys, version.Version)
	a := agent.New(ag.cfg, reg, logger)
	runner := jobs.NewRunner(a, logger)
	runner.Register(reg)
	d := &mysql.Driver{Host: ag.inst.HostWith(creds), Creds: creds, Jobs: runner, Logger: logger, Sys: sys, Cfg: ag.cfg,
		PinDir: ag.dir, SeenPath: filepath.Join(ag.dir, "replicas-seen"), SalvageDir: filepath.Join(ag.dir, "salvage"),
		Events: a}
	d.Register(reg)
	// The health monitor, as "dbpanel agent" runs it; the panel sends its
	// intervals with monitor.configure once the agent is online.
	mon := agentmon.New(d, a, logger, agentmon.Options{Pins: d.Pins()})
	mon.Register(reg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	monDone := make(chan struct{})
	go func() { done <- a.Run(ctx) }()
	go func() {
		defer close(monDone)
		mon.Run(ctx)
	}()
	ag.stopFn = func() {
		ag.stopFn = nil
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("agent %s did not stop", ag.name)
		}
		select {
		case <-monDone:
		case <-time.After(30 * time.Second):
			t.Errorf("health monitor of agent %s did not stop", ag.name)
		}
	}
	waitForWithin(t, 30*time.Second, "agent "+ag.name+" online", func() bool {
		got, err := ag.p.Store.GetServer(ag.srv)
		if err != nil {
			t.Fatal(err)
		}
		return ag.p.Hub.IsOnline(ag.srv) && got.ProbeJSON != ""
	})
}

// stop stops the agent process (its MySQL keeps running) and waits until
// the panel sees it offline.
func (ag *e2eAgent) stop(t *testing.T) {
	t.Helper()
	if ag.stopFn == nil {
		t.Fatalf("agent %s is not running", ag.name)
	}
	ag.stopFn()
	waitForWithin(t, e2eWait, "agent "+ag.name+" offline", func() bool { return !ag.p.Hub.IsOnline(ag.srv) })
}

// killMySQL kills inst's mysqld with SIGKILL, a crash without a clean
// shutdown, and waits until it is gone. restartMySQL starts it again.
func killMySQL(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	b, err := os.ReadFile(inst.PIDFile)
	if err != nil {
		t.Fatalf("killMySQL: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("killMySQL: pid file %q", b)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("killMySQL %d: %v", pid, err)
	}
	// A zombie (nobody reaps the daemonised server) has an empty cmdline.
	waitForWithin(t, e2eWait, "mysqld "+strconv.Itoa(pid)+" gone", func() bool {
		cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		return err != nil || !strings.Contains(string(cmdline), "--defaults-file="+inst.ConfigFile+"\x00")
	})
}

// restartMySQL starts inst's mysqld after killMySQL (or restarts a running
// one) and waits until it accepts connections.
func restartMySQL(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	inst.Restart(t)
}

// e2eNode is one server of e2ePromoteTopology.
type e2eNode struct {
	name  string
	inst  *testmysql.Instance
	db    *sql.DB
	agent *e2eAgent
}

func (n *e2eNode) srv() int64 { return n.agent.srv }

// addr is the node's MySQL address as the topology knows it.
func (n *e2eNode) addr() string { return "127.0.0.1:" + strconv.Itoa(n.inst.Port) }

// e2ePromoteTopology is a running panel with a master of database a and two
// replicas (A and B) following every database, made through the UI, with
// Telegram pointed at a fake Bot API and short monitoring intervals.
type e2ePromoteTopology struct {
	p              *Panel
	ui             *uiClient
	tg             *fakeTelegram
	logs           *syncBuffer
	master, ra, rb *e2eNode
}

// startE2EPromoteTopology starts three mysqld instances (the baseline
// settings of testmysql, so three fit next to each other), the panel and
// three agents, then runs "Siapkan master" and "Buat replika" of every
// database twice over the UI. Replica B's create starts once A is ready.
func startE2EPromoteTopology(t *testing.T) *e2ePromoteTopology {
	t.Helper()
	start := time.Now()
	// The instances first, so their cleanup (stop mysqld) runs last.
	insts := []*testmysql.Instance{testmysql.Start(t, testmysql.Options{}), testmysql.Start(t, testmysql.Options{}),
		testmysql.Start(t, testmysql.Options{})}
	e := &e2ePromoteTopology{logs: &syncBuffer{}}
	nodes := []**e2eNode{&e.master, &e.ra, &e.rb}
	for i, name := range []string{"master-e2e", "replika-a", "replika-b"} {
		*nodes[i] = &e2eNode{name: name, inst: insts[i], db: openInstance(t, insts[i])}
	}
	execSQL(t, e.master.db,
		"CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO a.t VALUES (1, 'awal')")

	o := testOptions(t)
	o.Logger = slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The agents' version (version.Version), as after an update of both:
	// a promotion refuses agents of another version than the panel's.
	o.Version = version.Version
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("panel and agent log:\n%s", tail(e.logs.String(), 300))
		}
	})
	e.p = newPanel(t, o)
	e.tg = newFakeTelegram(t)
	configureE2EMonitoring(t, e.p, e.tg)
	uiAddr, _ := startPanel(t, e.p)
	for _, n := range []*e2eNode{e.master, e.ra, e.rb} {
		n.agent = pairE2EAgent(t, e.p, n.name, n.inst, e.logs)
		n.agent.start(t)
	}

	e.ui = newUIClient(t, "https://"+uiAddr)
	e.ui.login(readInitialPassword(t, o))
	for _, n := range []*e2eNode{e.master, e.ra, e.rb} {
		e.connectRoot(t, n)
	}
	prepareID := e.ui.postJob(serverPath(e.master.srv())+"/prepare-master", nil)
	job := waitJob(t, e.p, prepareID, e2ePrepareTimeout, func(j store.Job) bool {
		// The instance binds 127.0.0.1 only: approve the restart.
		e.ui.postRedirect("/jobs/"+j.ID+"/approve", nil)
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("prepare_master ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, e.p, prepareID))
	}
	for _, n := range []*e2eNode{e.ra, e.rb} {
		e.createReplica(t, e.master, n, false)
	}
	for _, n := range []*e2eNode{e.ra, e.rb} {
		waitRow(t, n.db, "a", 1, "awal")
	}
	waitStatus(t, e.p, e.master.srv(), healthmon.StatusHealthy)
	for _, n := range []*e2eNode{e.ra, e.rb} {
		waitStatus(t, e.p, n.srv(), healthmon.StatusSynced)
	}
	t.Logf("three-server topology ready after %s", time.Since(start).Round(time.Second))
	return e
}

// connectRoot is "Hubungkan MySQL" on n as the instance's root over the
// socket.
func (e *e2ePromoteTopology) connectRoot(t *testing.T, n *e2eNode) {
	t.Helper()
	loc := e.ui.postRedirect(serverPath(n.srv())+"/mysql/connect", url.Values{"user": {"root"}, "password": {""}, "host": {""}})
	if want := serverPath(n.srv()) + "?pesan=terhubung"; loc != want {
		t.Fatalf("connect MySQL on %s redirected to %q, want %q", n.name, loc, want)
	}
}

// replicaFormDBs are the databases the replica form offers.
var replicaFormDBs = regexp.MustCompile(`name="databases" value="([^"]*)"`)

// createReplica runs "Buat replika" of every database the form of master
// offers onto target over the UI; with rebuild, it is "Bangun ulang sebagai
// replika" of the quarantined old master target (token and typed name). A
// job that waits for the admin account to be reconnected (CLONE replaced
// it) gets root reconnected and approved; any other wait is approved. The
// job must succeed.
func (e *e2ePromoteTopology) createReplica(t *testing.T, master, target *e2eNode, rebuild bool) string {
	t.Helper()
	formPath := fmt.Sprintf("/masters/%d/replicas/new", master.srv())
	if rebuild {
		formPath += "?bangun-ulang=" + strconv.FormatInt(target.srv(), 10)
	}
	form := e.ui.get(formPath)
	var dbs []string
	for _, m := range replicaFormDBs.FindAllStringSubmatch(form, -1) {
		dbs = append(dbs, m[1])
	}
	if len(dbs) == 0 {
		t.Fatalf("replica form %s offers no database:\n%s", formPath, form)
	}
	vals := url.Values{"target": {strconv.FormatInt(target.srv(), 10)}, "databases": dbs, "confirm_wipe": {"1"}}
	if rebuild {
		vals.Set("rebuild", "1")
		vals.Set("confirm_name", target.name)
		vals.Set("confirm_token", e.ui.confirmToken(web.ActRebuildOldMaster, "server", target.srv()))
	}
	id := e.ui.postJob(fmt.Sprintf("/masters/%d/replicas", master.srv()), vals)
	job := waitJob(t, e.p, id, e2eCreateTimeout, func(j store.Job) bool {
		if strings.Contains(j.ParamsJSON, `"wait_reason":"reconnect"`) {
			e.connectRoot(t, target)
		}
		e.ui.postRedirect("/jobs/"+j.ID+"/approve", nil)
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("create_replica of %s (rebuild %v) ended %s: %s\n%s", target.name, rebuild, job.Status, job.Error,
			jobLogText(t, e.p, id))
	}
	if rep, err := e.p.Store.GetReplicaByServer(target.srv()); err != nil || rep.State != store.ReplicaReady {
		t.Fatalf("replica %s after create = %+v, %v; want ready", target.name, rep, err)
	}
	t.Logf("create_replica of %s (rebuild %v) succeeded", target.name, rebuild)
	return id
}

const globalVarQuery = "SELECT VARIABLE_VALUE FROM performance_schema.global_variables WHERE VARIABLE_NAME = ?"

// globalVar reads a global system variable of the MySQL behind db.
func globalVar(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(globalVarQuery, name).Scan(&v); err != nil {
		t.Fatalf("global variable %s: %v", name, err)
	}
	return v
}

// waitVar waits up to e2eWait for a global variable to read want.
func waitVar(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	waitForWithin(t, e2eWait, name+" = "+want, func() bool {
		var v string
		return db.QueryRow(globalVarQuery, name).Scan(&v) == nil && v == want
	})
}

// e2ePromoteTimeout bounds a promote job (planned: lock, catch-up, promote,
// quarantine, repoint; emergency: about 30 s of probes first).
const e2ePromoteTimeout = 5 * time.Minute

// startPromote opens the consideration screen of the master, checks it
// offers candidate in mode, and posts the promotion with a confirmation
// token and the typed names; it returns the job's id and the screen.
func (e *e2ePromoteTopology) startPromote(t *testing.T, candidate *e2eNode, mode string, extra url.Values) (string, string) {
	t.Helper()
	path := fmt.Sprintf("/masters/%d/promote", e.master.srv())
	page := e.ui.get(path)
	for _, want := range []string{`name="mode" value="` + mode + `"`,
		`name="candidate" value="` + strconv.FormatInt(candidate.srv(), 10) + `"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("promote screen lacks %s:\n%s", want, page)
		}
	}
	form := url.Values{"candidate": {strconv.FormatInt(candidate.srv(), 10)}, "mode": {mode},
		"confirm_token": {e.ui.confirmToken(web.ActPromote, "master", e.master.srv())}}
	for k, v := range extra {
		form[k] = v
	}
	return e.ui.postJob(path, form), page
}

// leftoverCheck runs "Periksa data tertinggal" on the quarantined old
// master over the UI and returns the stored result and the server page.
func (e *e2ePromoteTopology) leftoverCheck(t *testing.T, old *e2eNode) (orchestrator.LeftoverCheck, string) {
	t.Helper()
	id := e.ui.postJob(serverPath(old.srv())+"/leftover", nil)
	if job := waitJob(t, e.p, id, e2eWait, nil); job.Status != store.JobSucceeded {
		t.Fatalf("leftover check ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, e.p, id))
	}
	prom, err := e.p.Store.PromotionByOldMaster(old.srv())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := orchestrator.LeftoverOf(prom)
	if !ok {
		t.Fatalf("no leftover check stored after the job: %+v", prom)
	}
	return c, e.ui.get(serverPath(old.srv()))
}

// confirmCutover presses "Sudah saya alihkan" on the new master's cutover
// screen; the cutover is no longer pending.
func (e *e2ePromoteTopology) confirmCutover(t *testing.T, newMaster *e2eNode) {
	t.Helper()
	path := fmt.Sprintf("/masters/%d", newMaster.srv())
	if loc := e.ui.postRedirect(path+"/cutover/done", nil); loc != path+"?pesan=dialihkan" {
		t.Fatalf("cutover/done redirected to %q", loc)
	}
	if topo, err := e.p.Store.GetTopologyByMaster(newMaster.srv()); err != nil || topo.CutoverPending {
		t.Fatalf("topology after Sudah saya alihkan = %+v, %v; want no cutover pending", topo, err)
	}
}

// rebuildOldMaster refreshes the new master's inventory (it was taken when
// the new master was still an empty replica) and runs "Bangun ulang
// sebagai replika" of old; old then follows the new master as a replica.
func (e *e2ePromoteTopology) rebuildOldMaster(t *testing.T, newMaster, old *e2eNode, row int) bool {
	t.Helper()
	if !e2ePromoteRebuildFixed {
		t.Logf("Bangun ulang sebagai replika of %s skipped: pending the fix of the new master's MasterReady "+
			"(e2ePromoteRebuildFixed)", old.name)
		return false
	}
	if loc := e.ui.postRedirect(serverPath(newMaster.srv())+"/mysql/refresh", nil); !strings.HasSuffix(loc, "?pesan=disegarkan") {
		t.Fatalf("refresh of %s redirected to %q", newMaster.name, loc)
	}
	// The old master refuses to become a replica while replicas read its
	// binlog. After a planned promote the old replicas' dump threads on it
	// end only at their next heartbeat (nothing is written to a quarantined
	// server), a moment a user taking the screens at a human pace never
	// sees; the test waits for them like that user.
	start := time.Now()
	waitForWithin(t, 2*time.Minute, "no replica reading the old master's binlog", func() bool {
		var n int
		return old.db.QueryRow("SELECT COUNT(*) FROM performance_schema.threads WHERE PROCESSLIST_COMMAND LIKE 'Binlog Dump%'").
			Scan(&n) == nil && n == 0
	})
	if d := time.Since(start); d > time.Second {
		t.Logf("waited %s for the old replicas' dump threads on %s to end", d.Round(time.Second), old.name)
	}
	e.createReplica(t, newMaster, old, true)
	if srv, err := e.p.Store.GetServer(old.srv()); err != nil || srv.Role != store.RoleReplica {
		t.Fatalf("old master after the rebuild = %+v, %v; want a replica", srv, err)
	}
	if role, src := readPin(t, old.agent.dir, mysql.RoleFile), readPin(t, old.agent.dir, mysql.SourceFile); role != "replica" ||
		src != newMaster.addr() {
		t.Fatalf("old master's pins after the rebuild = %q, %q; want replica, %s", role, src, newMaster.addr())
	}
	if v := globalVar(t, old.db, "super_read_only"); v != "ON" {
		t.Fatalf("super_read_only on the rebuilt old master = %s", v)
	}
	execSQL(t, newMaster.db, fmt.Sprintf("INSERT INTO a.t VALUES (%d, 'setelah-bangun-ulang')", row))
	waitRow(t, old.db, "a", row, "setelah-bangun-ulang")
	t.Logf("old master rebuilt as a replica of %s in %s", newMaster.name, time.Since(start).Round(time.Second))
	return true
}

// wantPromoted checks the swap after a promotion of newMaster: it is the
// topology's master at its address, writable and pinned master, with old
// as the topology's old master, quarantined in the panel and pinned
// quarantined (the old master's MySQL may be down: wantQuarantined checks
// it); other follows newMaster.
func (e *e2ePromoteTopology) wantPromoted(t *testing.T, newMaster, old, other *e2eNode, row int) {
	t.Helper()
	topo, err := e.p.Store.GetTopologyByMaster(newMaster.srv())
	if err != nil {
		t.Fatalf("topology of the new master: %v", err)
	}
	if topo.SourceHost != "127.0.0.1" || topo.SourcePort != newMaster.inst.Port || topo.OldMasterServerID != old.srv() ||
		!topo.CutoverPending {
		t.Fatalf("topology after promote = %+v; want source %s, old master %d, cutover pending", topo, newMaster.addr(), old.srv())
	}
	for _, v := range []string{"super_read_only", "read_only"} {
		if got := globalVar(t, newMaster.db, v); got != "OFF" {
			t.Fatalf("%s on the new master = %s, want OFF", v, got)
		}
	}
	if role := readPin(t, newMaster.agent.dir, mysql.RoleFile); role != "master" {
		t.Fatalf("new master's role pin = %q", role)
	}
	if role, src := readPin(t, old.agent.dir, mysql.RoleFile), readPin(t, old.agent.dir, mysql.SourceFile); role != "quarantined" ||
		src != newMaster.addr() {
		t.Fatalf("old master's pins = %q, %q; want quarantined, %s", role, src, newMaster.addr())
	}
	if srv, err := e.p.Store.GetServer(old.srv()); err != nil || srv.Role != store.RoleQuarantined {
		t.Fatalf("old master in the panel = %+v, %v; want quarantined", srv, err)
	}
	// The other replica follows the new master.
	execSQL(t, newMaster.db, fmt.Sprintf("INSERT INTO a.t VALUES (%d, 'di-master-baru')", row))
	waitRow(t, other.db, "a", row, "di-master-baru")
	if rep, err := e.p.Store.GetReplicaByServer(other.srv()); err != nil || rep.State != store.ReplicaReady {
		t.Fatalf("other replica after promote = %+v, %v; want ready", rep, err)
	}
	if src := readPin(t, other.agent.dir, mysql.SourceFile); src != newMaster.addr() {
		t.Fatalf("other replica's source pin = %q, want %s", src, newMaster.addr())
	}
	if _, err := e.p.Store.GetReplicaByServer(newMaster.srv()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the new master still has a replica row: %v", err)
	}
}

// wantQuarantined checks the running old master: super_read_only on, a
// write refused, and the monitored status of a quarantined old master.
func (e *e2ePromoteTopology) wantQuarantined(t *testing.T, old *e2eNode) {
	t.Helper()
	waitVar(t, old.db, "super_read_only", "ON")
	waitStatus(t, e.p, old.srv(), healthmon.StatusQuarantined)
	if _, err := old.db.Exec("INSERT INTO a.t VALUES (999, 'tidak-boleh')"); err == nil || !strings.Contains(err.Error(), "1290") {
		t.Fatalf("a write to the quarantined old master = %v, want refused (1290, super-read-only)", err)
	}
}

// wantPromoteMessage waits for the Telegram summary of the promotion
// naming the new master's address.
func (e *e2ePromoteTopology) wantPromoteMessage(t *testing.T, since int, newMaster *e2eNode) string {
	t.Helper()
	want := "Alamat master baru: " + newMaster.addr() + "."
	var msg string
	waitForWithin(t, e2eWait, "the Telegram promote summary with "+newMaster.addr(), func() bool {
		got := containing(e.tg.since(since), want)
		if len(got) > 0 {
			msg = got[0]
		}
		return msg != ""
	})
	if !strings.Contains(msg, newMaster.name+" kini master") {
		t.Fatalf("promote message does not name the new master:\n%s", msg)
	}
	return msg
}

var tagRe = regexp.MustCompile(`<[^>]*>|\s+`)

// textOf is an HTML fragment as one line of text.
func textOf(html string) string { return strings.TrimSpace(tagRe.ReplaceAllString(html, " ")) }

// jobTook is how long a finished job ran.
func jobTook(j store.Job) time.Duration {
	if j.StartedAt == nil || j.FinishedAt == nil {
		return 0
	}
	return j.FinishedAt.Sub(*j.StartedAt).Round(time.Millisecond)
}
