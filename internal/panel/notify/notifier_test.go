package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is a Notifier over a fresh store and key, sending to a fake Bot API,
// on a fake clock.
type rig struct {
	t    *testing.T
	st   *store.Store
	box  *secrets.Box
	api  *botAPI
	clk  *fakeClock
	logs *syncBuffer
	n    *Notifier

	snoozeMu sync.Mutex
	snoozed  map[int64]bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "panel.key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, box: box, api: newBotAPI(t), logs: &syncBuffer{},
		clk: &fakeClock{t: time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)}, snoozed: map[int64]bool{}}
	r.n = New(r.deps())
	return r
}

// deps are the Notifier's dependencies over the rig's store, Bot API and
// clock.
func (r *rig) deps() Deps {
	return Deps{
		Store:  r.st,
		Box:    r.box,
		Sender: &Telegram{BaseURL: r.api.srv.URL},
		Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    r.clk.Now,
		Snoozed: func(id int64) bool {
			r.snoozeMu.Lock()
			defer r.snoozeMu.Unlock()
			return r.snoozed[id]
		},
		ServerName: func(id int64) string {
			srv, err := r.st.GetServer(id)
			if err != nil {
				return "?"
			}
			return srv.Name
		},
	}
}

// restart replaces the Notifier by a new one over the same store: a panel
// restart, with nothing kept in memory.
func (r *rig) restart() { r.n = New(r.deps()) }

// configure stores the bot token, chat id and panel address.
func (r *rig) configure() {
	r.t.Helper()
	if err := SaveToken(r.st, r.box, testToken); err != nil {
		r.t.Fatal(err)
	}
	for k, v := range map[string]string{store.SettingTelegramChatID: "-100777", store.SettingPanelBaseURL: "http://panel.local"} {
		if err := r.st.SetSetting(k, v); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) server(name string) int64 {
	r.t.Helper()
	srv, err := r.st.CreateServer(name)
	if err != nil {
		r.t.Fatal(err)
	}
	return srv.ID
}

func (r *rig) open(kind string, serverID int64, msg string) int64 {
	r.t.Helper()
	id, err := r.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, OpenedAt: r.clk.Now(), Message: msg})
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *rig) close(id int64) {
	r.t.Helper()
	if err := r.st.CloseAlert(id, r.clk.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) alert(id int64) store.Alert {
	r.t.Helper()
	all, err := r.st.ListAlerts(time.Time{}, 1000)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, a := range all {
		if a.ID == id {
			return a
		}
	}
	r.t.Fatalf("alert %d not found", id)
	return store.Alert{}
}

func (r *rig) snooze(id int64, on bool) {
	r.snoozeMu.Lock()
	r.snoozed[id] = on
	r.snoozeMu.Unlock()
}

func (r *rig) pass() { r.n.Pass(context.Background()) }

// texts are the texts the Bot API received.
func (r *rig) texts() []string {
	var out []string
	for _, m := range r.api.messages() {
		out = append(out, m.Text)
	}
	return out
}

func TestPassSendsPendingThenMarksSent(t *testing.T) {
	r := newRig(t)
	srv := r.server("DB-1")
	id := r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 87%.")

	r.pass() // Telegram not set up yet: nothing is sent, nothing changes
	if got := r.texts(); len(got) != 0 {
		t.Fatalf("sent before configuration: %q", got)
	}
	if a := r.alert(id); a.TelegramState != store.TelegramPending {
		t.Fatalf("state before configuration = %q", a.TelegramState)
	}

	r.configure()
	r.pass()
	msgs := r.api.messages()
	want := "⚠️ Disk data terpakai 87%.\nServer: DB-1 · Sejak: 09:00 · http://panel.local/servers/" + itoa(srv)
	if len(msgs) != 1 || msgs[0].Text != want || msgs[0].ChatID != "-100777" {
		t.Fatalf("messages = %+v, want one %q", msgs, want)
	}
	a := r.alert(id)
	if a.TelegramState != store.TelegramSent || a.NotifyCount != 1 || a.TelegramError != "" ||
		a.LastNotifiedAt == nil || !a.LastNotifiedAt.Equal(r.clk.Now()) {
		t.Fatalf("alert after send = %+v", a)
	}

	// Sent once: neither the next pass nor a changed message sends again.
	if err := r.st.SetAlertMessage(id, "Disk data terpakai 88%."); err != nil {
		t.Fatal(err)
	}
	r.clk.Add(time.Hour)
	r.pass()
	if got := r.texts(); len(got) != 1 {
		t.Fatalf("messages after second pass = %q", got)
	}
}

func TestPassNamesTheMasterOfAStoppedReplica(t *testing.T) {
	r := newRig(t)
	r.configure()
	master, replica := r.server("RS-1"), r.server("RS-2")
	topo, err := r.st.CreateTopology(master, "repl", nil, "10.0.0.1", 3306)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.OpenAlert(store.Alert{Kind: monitor.KindReplicaStopped, ServerID: replica, TopologyID: topo.ID,
		OpenedAt: r.clk.Now(), Message: "Replikasi berhenti karena error 1062. " + monitor.Explain(1062, 0).Text}); err != nil {
		t.Fatal(err)
	}
	r.pass()
	got := r.texts()
	if len(got) != 1 || !strings.HasPrefix(got[0], "⛔ Replika RS-2 berhenti\n") ||
		!strings.HasSuffix(got[0], "\nMaster: RS-1 · Sejak: 09:00 · http://panel.local/replicas/"+itoa(replica)) {
		t.Fatalf("messages = %q", got)
	}
}

func TestSendFailureIsVisibleAndRetried(t *testing.T) {
	r := newRig(t)
	r.configure()
	id := r.open(monitor.KindAgentOffline, r.server("DB-1"), "Agent tidak terhubung.")

	r.api.fail(http.StatusInternalServerError, "Internal Server Error")
	r.pass()
	a := r.alert(id)
	if a.TelegramState != store.TelegramFailed || !strings.HasPrefix(a.TelegramError, "Telegram menolak: HTTP 500") ||
		!strings.Contains(a.TelegramError, "Internal Server Error") || a.NotifyCount != 0 {
		t.Fatalf("alert after a failed send = %+v", a)
	}
	if !strings.Contains(r.logs.String(), "Internal Server Error") {
		t.Fatalf("the failure is not logged:\n%s", r.logs)
	}

	r.api.fail(0, "")
	r.clk.Add(time.Minute)
	r.pass()
	a = r.alert(id)
	if a.TelegramState != store.TelegramSent || a.TelegramError != "" || a.NotifyCount != 1 || len(r.texts()) != 1 {
		t.Fatalf("after the retry: alert %+v, messages %q", a, r.texts())
	}
}

func TestRepeatEvery6Hours(t *testing.T) {
	r := newRig(t)
	r.configure()
	id := r.open(monitor.KindMasterLocked, r.server("DB-1"), "Master terkunci.")
	r.pass()

	r.clk.Add(6*time.Hour - time.Second)
	r.pass()
	if got := r.texts(); len(got) != 1 {
		t.Fatalf("repeated before 6 h: %q", got)
	}
	r.clk.Add(time.Second)
	r.pass()
	got := r.texts()
	if len(got) != 2 || got[1] != got[0] {
		t.Fatalf("after 6 h = %q, want the message again", got)
	}
	if a := r.alert(id); a.NotifyCount != 2 || !a.LastNotifiedAt.Equal(r.clk.Now()) {
		t.Fatalf("alert after the repeat = %+v", a)
	}

	// The alert_repeat_hours setting changes the pace on the next pass.
	if err := r.st.SetSetting(store.SettingAlertRepeatHours, "1"); err != nil {
		t.Fatal(err)
	}
	r.clk.Add(time.Hour)
	r.pass()
	if got := r.texts(); len(got) != 3 {
		t.Fatalf("with a 1 h repeat = %q", got)
	}
}

func TestRecoveredMessageOnce(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("RS-2")
	id := r.open(monitor.KindReplicaStopped, srv, "Replikasi berhenti tanpa lewat panel.")
	r.pass()

	r.clk.Add(2*time.Hour + 5*time.Minute)
	r.close(id)
	r.pass()
	// The recovery waits until the alert stayed closed a minute.
	if got := r.texts(); len(got) != 1 {
		t.Fatalf("messages right after the close = %q, want the recovery held", got)
	}
	if d, ok := r.n.untilDue(); !ok || d != time.Minute {
		t.Fatalf("next pass due in %v, %v; want 1 min", d, ok)
	}
	r.clk.Add(time.Minute)
	r.pass()
	r.pass()
	got := r.texts()
	if len(got) != 2 || got[1] != "✅ RS-2 pulih: replika berhenti selesai setelah 2 jam 5 menit" {
		t.Fatalf("messages = %q, want the alert then one recovery", got)
	}
	a := r.alert(id)
	if a.TelegramState != store.TelegramRecovered || a.NotifyCount != 1 || !a.LastNotifiedAt.Equal(r.clk.Now()) {
		t.Fatalf("alert after recovery = %+v", a)
	}

	// A recovery that fails is retried.
	id2 := r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 86%.")
	r.pass()
	r.close(id2)
	r.clk.Add(time.Minute)
	r.api.fail(http.StatusBadGateway, "Bad Gateway")
	r.pass()
	if a := r.alert(id2); a.TelegramState != store.TelegramFailed || !strings.Contains(a.TelegramError, "502") {
		t.Fatalf("after a failed recovery = %+v", a)
	}
	r.api.fail(0, "")
	r.pass()
	if a := r.alert(id2); a.TelegramState != store.TelegramRecovered || len(r.texts()) != 4 {
		t.Fatalf("after the retry: alert %+v, messages %q", a, r.texts())
	}

	// An alert closed before it was ever sent has nothing to recover from.
	r.api.fail(http.StatusInternalServerError, "down")
	id3 := r.open(monitor.KindAgentOffline, srv, "Agent tidak terhubung.")
	r.pass() // fails: never delivered
	r.close(id3)
	r.api.fail(0, "")
	r.pass()
	if a := r.alert(id3); a.TelegramState != store.TelegramRecovered || len(r.texts()) != 4 {
		t.Fatalf("closed before sending: alert %+v, messages %q", a, r.texts())
	}
}

func TestSnoozedSkipsUntilEnd(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv, other := r.server("DB-1"), r.server("DB-2")
	sentBefore := r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 86%.")
	r.pass()
	r.snooze(srv, true)

	newDuring := r.open(monitor.KindAgentOffline, srv, "Agent tidak terhubung.")
	r.open(monitor.KindAgentOffline, other, "Agent tidak terhubung.")
	r.close(sentBefore)
	r.clk.Add(7 * time.Hour)
	r.pass()
	got := r.texts()
	if len(got) != 2 || !strings.Contains(got[1], "DB-2") {
		t.Fatalf("messages during the snooze = %q; want only DB-2's alert", got)
	}
	if a := r.alert(newDuring); a.TelegramState != store.TelegramPending {
		t.Fatalf("snoozed alert state = %q", a.TelegramState)
	}

	r.snooze(srv, false)
	r.pass()
	got = r.texts()
	if len(got) != 4 || !strings.HasPrefix(got[2], "✅ DB-1 pulih: disk hampir penuh") ||
		!strings.Contains(got[3], "Agent tidak terhubung.\nServer: DB-1") {
		t.Fatalf("messages after the snooze = %q; want the recovery and the new alert", got)
	}
	r.pass()
	if len(r.texts()) != 4 {
		t.Fatalf("sent again: %q", r.texts())
	}
}

// TestSnoozeEndSendsOpenAlertsOnce: when a snooze ends, each alert of the
// server still open goes out once, as a reminder, even with its repeat not
// yet due; the repeat clock restarts from that send.
func TestSnoozeEndSendsOpenAlertsOnce(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv, other := r.server("DB-1"), r.server("DB-2")
	id := r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 86%.")
	otherID := r.open(monitor.KindDiskWarn, other, "Disk data terpakai 86%.")
	r.pass() // both sent at t0
	r.snooze(srv, true)
	r.clk.Add(time.Hour)
	r.pass()
	if got := r.texts(); len(got) != 2 {
		t.Fatalf("messages during the snooze = %q", got)
	}

	r.clk.Add(time.Hour) // t0+2h: the 6 h repeat is far off
	r.snooze(srv, false)
	r.pass()
	got := r.texts()
	if len(got) != 3 || !strings.Contains(got[2], "Server: DB-1") {
		t.Fatalf("messages at the snooze end = %q; want DB-1's alert once more", got)
	}
	if a := r.alert(id); a.NotifyCount != 2 || !a.LastNotifiedAt.Equal(r.clk.Now()) {
		t.Fatalf("alert after the reminder = %+v", a)
	}
	if a := r.alert(otherID); a.NotifyCount != 1 {
		t.Fatalf("an unsnoozed server's alert was sent again: %+v", a)
	}
	r.pass()
	if got := r.texts(); len(got) != 3 {
		t.Fatalf("the reminder went out twice: %q", got)
	}

	// The repeat counts from the reminder: DB-2's comes due at t0+6h,
	// DB-1's only at t0+8h.
	r.clk.Add(4 * time.Hour)
	r.pass()
	if got := r.texts(); len(got) != 4 || !strings.Contains(got[3], "Server: DB-2") {
		t.Fatalf("messages at t0+6h = %q; want only DB-2's repeat", got)
	}
	r.clk.Add(2*time.Hour - time.Second)
	r.pass()
	if got := r.texts(); len(got) != 4 {
		t.Fatalf("DB-1 repeated before 6 h after the reminder: %q", got)
	}
	r.clk.Add(time.Second)
	r.pass()
	if got := r.texts(); len(got) != 5 || !strings.Contains(got[4], "Server: DB-1") {
		t.Fatalf("messages at t0+8h = %q; want DB-1's repeat", got)
	}
}

func TestKindsFilter(t *testing.T) {
	r := newRig(t)
	r.configure()
	if err := r.st.SetSetting(store.SettingTelegramKinds, `{"disk_warn":false,"agent_offline":true}`); err != nil {
		t.Fatal(err)
	}
	srv := r.server("DB-1")
	off := r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 86%.")
	on := r.open(monitor.KindAgentOffline, srv, "Agent tidak terhubung.")
	unlisted := r.open(monitor.KindMasterLocked, srv, "Master terkunci.")
	r.pass()
	got := r.texts()
	if len(got) != 2 || strings.Contains(strings.Join(got, "\n"), "Disk") {
		t.Fatalf("messages = %q; want agent_offline and master_locked only", got)
	}
	a := r.alert(off)
	if a.TelegramState != store.TelegramSent || a.NotifyCount != 0 {
		t.Fatalf("disabled kind = %+v; want sent without a delivery", a)
	}
	for _, id := range []int64{on, unlisted} {
		if a := r.alert(id); a.TelegramState != store.TelegramSent || a.NotifyCount != 1 {
			t.Fatalf("enabled kind = %+v", a)
		}
	}

	r.close(off)
	r.pass()
	if a := r.alert(off); a.TelegramState != store.TelegramRecovered || len(r.texts()) != 2 {
		t.Fatalf("disabled kind closed: alert %+v, messages %q", a, r.texts())
	}
}

func TestTokenNeverLogged(t *testing.T) {
	r := newRig(t)
	r.configure()
	raw, err := r.st.GetSetting(store.SettingTelegramTokenEnc)
	if err != nil || raw == "" || strings.Contains(raw, "AAHsecret") {
		t.Fatalf("stored token = %q, %v; want it sealed", raw, err)
	}
	cfg, err := LoadConfig(r.st, r.box)
	if err != nil || cfg.Token != testToken || cfg.ChatID != "-100777" || cfg.BaseURL != "http://panel.local" {
		t.Fatalf("LoadConfig = %+v, %v", cfg, err)
	}

	id := r.open(monitor.KindAgentOffline, r.server("DB-1"), "Agent tidak terhubung.")
	r.api.fail(http.StatusUnauthorized, "Unauthorized")
	r.pass()
	r.api.srv.Close() // unreachable: Go's error carries the request URL
	r.clk.Add(time.Minute)
	r.pass()
	r.clk.Add(time.Minute)
	testErr := r.n.SendTest(context.Background())

	a := r.alert(id)
	if a.TelegramState != store.TelegramFailed || testErr == nil {
		t.Fatalf("alert %+v, SendTest %v; want both to fail", a, testErr)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "bot***") {
		t.Fatalf("the transport failure is not logged with its redacted URL:\n%s", logs)
	}
	for what, s := range map[string]string{"log": logs, "telegram_error": a.TelegramError, "SendTest": testErr.Error()} {
		if strings.Contains(s, testToken) || strings.Contains(s, "AAHsecret") {
			t.Errorf("%s holds the token: %s", what, s)
		}
	}
}

func TestSendTestRateLimited(t *testing.T) {
	r := newRig(t)
	if err := r.n.SendTest(context.Background()); err == nil || err.Error() != "Telegram belum diatur" {
		t.Fatalf("SendTest unconfigured = %v", err)
	}
	r.configure()
	if err := r.n.SendTest(context.Background()); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if got := r.texts(); len(got) != 1 || got[0] != "Tes dari dbpanel: notifikasi berfungsi." {
		t.Fatalf("messages = %q", got)
	}
	r.clk.Add(9 * time.Second)
	if err := r.n.SendTest(context.Background()); err == nil || err.Error() != "tunggu 10 detik sebelum mengirim tes lagi" {
		t.Fatalf("second SendTest within 10 s = %v", err)
	}
	r.clk.Add(time.Second)
	if err := r.n.SendTest(context.Background()); err != nil || len(r.texts()) != 2 {
		t.Fatalf("SendTest after 10 s = %v, messages %q", err, r.texts())
	}
	// A failed test says in Indonesian what went wrong, without the token.
	r.clk.Add(10 * time.Second)
	r.api.srv.Close()
	err := r.n.SendTest(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "Telegram tidak terjangkau: ") || strings.Contains(err.Error(), "AAHsecret") {
		t.Fatalf("SendTest with Telegram unreachable = %v", err)
	}
}

func TestSaveTokenRefusesMalformedAndClears(t *testing.T) {
	r := newRig(t)
	if err := SaveToken(r.st, r.box, "not a token"); err == nil || strings.Contains(err.Error(), "not a token") {
		t.Fatalf("SaveToken malformed = %v", err)
	}
	r.configure()
	if err := SaveToken(r.st, r.box, ""); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadConfig(r.st, r.box); err != nil || cfg.Token != "" {
		t.Fatalf("after clearing: %+v, %v", cfg, err)
	}
}

// TestWakeSendsWithoutWaiting: Run passes once at start, then right after
// Wake, long before RetryEvery, and returns when its context ends.
func TestWakeSendsWithoutWaiting(t *testing.T) {
	r := newRig(t)
	r.n.retryEvery = time.Hour
	r.configure()
	srv := r.server("DB-1")
	r.open(monitor.KindAgentOffline, srv, "Agent tidak terhubung.")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.n.Run(ctx); close(done) }()
	waitMessages := func(n int, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(r.texts()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("no message %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitMessages(1, "from the first pass")

	// The first pass listed its alerts before sending; this one can only go
	// out on a pass that Wake starts.
	r.open(monitor.KindDiskWarn, srv, "Disk data terpakai 86%.")
	r.n.Wake()
	r.n.Wake() // never blocks, even with a wake pending
	waitMessages(2, "after Wake")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSendWeekly(t *testing.T) {
	r := newRig(t)
	if err := r.n.SendWeekly(context.Background(), "ringkasan"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SendWeekly unconfigured = %v, want ErrNotConfigured", err)
	}
	r.configure()
	// Every alert kind switched off, the server snoozed: the summary is no
	// alert, so it still goes out.
	if err := r.st.SetSetting(store.SettingTelegramKinds, `{"disk_warn":false,"agent_offline":false}`); err != nil {
		t.Fatal(err)
	}
	r.snoozeMu.Lock()
	r.snoozed[1] = true
	r.snoozeMu.Unlock()

	if err := r.n.SendWeekly(context.Background(), "📊 Ringkasan mingguan"); err != nil {
		t.Fatalf("SendWeekly: %v", err)
	}
	msgs := r.api.messages()
	if len(msgs) != 1 || msgs[0].Text != "📊 Ringkasan mingguan" || msgs[0].ChatID != "-100777" {
		t.Fatalf("messages = %+v", msgs)
	}

	// A failure comes back without the token.
	r.api.srv.Close()
	err := r.n.SendWeekly(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(r.logs.String(), testToken) {
		t.Fatalf("SendWeekly to a dead API = %v; want an error without the token", err)
	}
}

// TestFlappingSendsAtMostTwoMessages (SEC-46): an alert that opens and
// closes 100 times in 2 minutes, with a pass after every change, sends its
// first open message and one "berkedip" message; once it stayed closed a
// minute, one recovery.
func TestFlappingSendsAtMostTwoMessages(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("RS-2")
	for range 100 {
		id := r.open(monitor.KindReplicaStopped, srv, "Replikasi berhenti karena error 1062.")
		r.pass()
		r.clk.Add(600 * time.Millisecond)
		r.close(id)
		r.pass()
		r.clk.Add(600 * time.Millisecond)
	}
	got := r.texts()
	if len(got) != 2 || !strings.HasPrefix(got[0], "⛔ Replika RS-2 berhenti") ||
		!strings.HasPrefix(got[1], "⚠️ RS-2: replika berhenti berkedip ") || !strings.Contains(got[1], " kali dalam 5 menit terakhir") ||
		!strings.Contains(got[1], "http://panel.local/replicas/") {
		t.Fatalf("messages while flapping = %q, want the first alert and one flapping notice", got)
	}
	r.clk.Add(time.Minute)
	r.pass()
	got = r.texts()
	if len(got) != 3 || !strings.HasPrefix(got[2], "✅ RS-2 pulih: replika berhenti") {
		t.Fatalf("messages once it stayed closed = %q, want one recovery", got)
	}
	r.pass()
	if len(r.texts()) != 3 {
		t.Fatalf("sent again: %q", r.texts())
	}
	all, _ := r.st.ListAlertsToNotify()
	if len(all) != 0 {
		t.Fatalf("alerts still waiting for the notifier: %d", len(all))
	}

	// Still flapping after 5 minutes: one more notice, counting the last 5 minutes.
	r.clk.Add(10 * time.Minute)
	id := r.open(monitor.KindReplicaStopped, srv, "Replikasi berhenti karena error 1062.")
	r.pass()
	if got := r.texts(); len(got) != 4 || !strings.HasPrefix(got[3], "⛔ Replika RS-2 berhenti") {
		t.Fatalf("messages after a quiet 10 min = %q, want an ordinary alert", got)
	}
	r.clk.Add(time.Second)
	r.close(id)
	r.pass()
	r.clk.Add(time.Second)
	id = r.open(monitor.KindReplicaStopped, srv, "Replikasi berhenti karena error 1062.")
	r.pass()
	if got := r.texts(); len(got) != 5 || got[4] != "⚠️ RS-2: replika berhenti berkedip 2 kali dalam 5 menit terakhir\nhttp://panel.local/replicas/"+itoa(srv) {
		t.Fatalf("messages after a reopen within 5 min = %q, want a flapping notice", got)
	}
	// The alert before it reopened within a minute: no recovery for it.
	r.clk.Add(time.Minute)
	r.pass()
	if got := r.texts(); len(got) != 5 {
		t.Fatalf("messages a minute later = %q, want no recovery of the reopened alert", got)
	}
	if a := r.alert(id); a.ClosedAt != nil || a.TelegramState != store.TelegramSent {
		t.Fatalf("the reopened alert = %+v", a)
	}
}

// TestClosedWithReasonIsNoRecovery: an alert closed because its replica is
// being resynced (or left the panel) says so at once, without "pulih".
func TestClosedWithReasonIsNoRecovery(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("RS-2")
	id := r.open(monitor.KindReplicaStopped, srv, "Replikasi berhenti karena error 1062.")
	r.pass()
	r.clk.Add(time.Minute)
	if err := r.st.CloseAlertWithReason(id, r.clk.Now(), "Resync ulang dimulai"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	got := r.texts()
	if len(got) != 2 || got[1] != "ℹ️ RS-2: peringatan replika berhenti ditutup: Resync ulang dimulai" {
		t.Fatalf("messages = %q, want the alert then the closing note", got)
	}
	if a := r.alert(id); a.TelegramState != store.TelegramRecovered {
		t.Fatalf("alert after the closing note = %+v", a)
	}
}

// TestPromoteCutoverRemindsEvery6Hours: the promote message goes out
// again with the usual repeat while the cutover waits.
func TestPromoteCutoverRemindsEvery6Hours(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-2")
	r.open(monitor.KindPromoteCutover, srv, "Alamat master baru: 10.0.0.2:3306.")
	r.pass()
	r.clk.Add(6 * time.Hour)
	r.pass()
	got := r.texts()
	if len(got) != 2 || got[1] != got[0] || !strings.Contains(got[0], "http://panel.local/masters/"+itoa(srv)+"/cutover") {
		t.Fatalf("messages = %q, want the promote message twice", got)
	}
}

// TestNoticeKindsSendOnceAndCloseQuietly: master_quarantined is sent once
// and master_lock_expired at most every 24 h; their closing after 24 h
// sends nothing.
func TestNoticeKindsSendOnceAndCloseQuietly(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	q := r.open(monitor.KindMasterQuarantined, srv, "Master lama dikarantina.")
	l := r.open(monitor.KindMasterLockExpired, srv, "Kunci tetap dikunci.")
	r.pass()
	if got := r.texts(); len(got) != 2 {
		t.Fatalf("first pass = %q", got)
	}
	for range 3 {
		r.clk.Add(6 * time.Hour)
		r.pass()
	}
	r.snooze(srv, true)
	r.pass()
	r.snooze(srv, false)
	r.pass() // a snooze ending re-sends open alerts, but not a one-time notice
	if got := r.texts(); len(got) != 3 || !strings.Contains(got[2], "Kunci tetap dikunci.") {
		t.Fatalf("after 18 h = %q, want only the lock alert again after its snooze", got)
	}
	r.clk.Add(6 * time.Hour)
	for _, id := range []int64{q, l} {
		if err := r.st.CloseAlertWithReason(id, r.clk.Now(), "24 jam berlalu"); err != nil {
			t.Fatal(err)
		}
	}
	r.clk.Add(2 * time.Minute)
	r.pass()
	if got := r.texts(); len(got) != 3 {
		t.Fatalf("closing sent %q", got[3:])
	}
	for _, id := range []int64{q, l} {
		if a := r.alert(id); a.TelegramState != store.TelegramRecovered {
			t.Fatalf("alert %d state = %q", id, a.TelegramState)
		}
	}
}
