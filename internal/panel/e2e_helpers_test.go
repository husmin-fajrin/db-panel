package panel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
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
	base := "http://" + uiAddr

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
