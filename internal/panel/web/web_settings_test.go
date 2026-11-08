package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

const (
	botToken = "123456789:AAH-rahasiaBotTokenXyz_09"
	chatID   = "-1001234567890"
)

// fakeSettings keeps the settings in the store like the panel does and
// records the saves and reconfigures.
type fakeSettings struct {
	st *store.Store

	mu           sync.Mutex
	saved        []monitor.Settings
	reconfigured int
}

func (f *fakeSettings) Load() (monitor.Settings, error) { return monitor.LoadSettings(f.st) }

func (f *fakeSettings) Save(s monitor.Settings) error {
	f.mu.Lock()
	f.saved = append(f.saved, s)
	f.mu.Unlock()
	return monitor.SaveSettings(f.st, s)
}

func (f *fakeSettings) Reconfigure() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconfigured++
}

func (f *fakeSettings) counts() (saves, reconfigures int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved), f.reconfigured
}

// fakeSender records the Telegram messages instead of sending them.
type fakeSender struct {
	mu   sync.Mutex
	sent []string // "token|chat|text"
}

func (s *fakeSender) Send(_ context.Context, token, chat, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, token+"|"+chat+"|"+text)
	return nil
}

func (s *fakeSender) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// testTelegram is the panel's Telegram adapter over a real Notifier whose
// sender is fake.
type testTelegram struct {
	st  *store.Store
	box *secrets.Box
	n   *notify.Notifier
}

func (t testTelegram) LoadConfig() (notify.Config, error) { return notify.LoadConfig(t.st, t.box) }
func (t testTelegram) SaveToken(token string) error       { return notify.SaveToken(t.st, t.box, token) }
func (t testTelegram) SendTest(ctx context.Context) error { return t.n.SendTest(ctx) }

type settingsRig struct {
	*env
	set    *fakeSettings
	tg     testTelegram
	sender *fakeSender
}

func settingsEnv(t *testing.T, mods ...func(*web.Deps)) *settingsRig {
	t.Helper()
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "panel.key"))
	if err != nil {
		t.Fatal(err)
	}
	rig := &settingsRig{sender: &fakeSender{}}
	rig.env = newEnv(t, func(d *web.Deps) {
		rig.set = &fakeSettings{st: d.Store}
		rig.tg = testTelegram{st: d.Store, box: box,
			n: notify.New(notify.Deps{Store: d.Store, Box: box, Sender: rig.sender})}
		d.Settings = rig.set
		d.Telegram = rig.tg
		for _, m := range mods {
			m(d)
		}
	})
	return rig
}

// validSettings is a form that passes validation, with every field set.
func validSettings() url.Values {
	return url.Values{
		"chat_id":          {chatID},
		"kinds":            {monitor.KindReplicaStopped, monitor.KindMasterDown, monitor.KindJobFailed},
		"check_every":      {"30"},
		"summary_every":    {"300"},
		"lag":              {"90"},
		"disconnect_alert": {"600"},
		"down_after":       {"45"},
		"disk_warn":        {"80"},
		"disk_crit":        {"92"},
		"conn_pct":         {"75"},
		"alert_repeat":     {"12"},
		"lagging_alert":    {"900"},
		"weekly_summary":   {"on"},
		"weekly_day":       {"Fri"},
		"weekly_time":      {"17:30"},
		"base_url":         {"https://panel.example.com"},
		"alerts_retention": {"60"},
		"events_retention": {"120"},
	}
}

func with(v url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

func TestSettingsPageShowsDefaults(t *testing.T) {
	e := settingsEnv(t)
	rec := e.do("GET", "/settings", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec,
		`name="token"`, `name="chat_id"`, `action="/settings/telegram-test"`,
		`name="check_every" value="15"`, `name="summary_every" value="120"`, `name="lag" value="60"`,
		`name="disconnect_alert" value="180"`, `name="down_after" value="30"`,
		`name="disk_warn" value="85"`, `name="disk_crit" value="95"`, `name="conn_pct" value="80"`,
		`name="alert_repeat" value="6"`, `name="lagging_alert" value="300"`,
		`name="weekly_time" value="07:00"`, `<option value="Mon" selected>Senin</option>`,
		// No stored base URL: the public URL is only the placeholder, so
		// an empty field saves as empty (no links).
		`name="base_url" value="" placeholder="`+testPublicURL+`"`,
		"Bila dikosongkan, pesan tidak memuat tautan ke panel",
		"Alamat abu-abu di kolom hanya saran (alamat publik panel), bukan nilai yang dipakai",
		"Replika berhenti", "Master diduga tumbang", "Pekerjaan gagal")
	// The page is HTTPS only: no plain-HTTP warning any more.
	wantNoBody(t, rec, "melintas jaringan tanpa enkripsi")
	for _, k := range monitor.AllKinds() {
		wantBody(t, rec, `name="kinds" value="`+k+`" checked`)
	}
	wantNoBody(t, rec, "tersimpan")
}

func TestSettingsSaveSealsTokenAndNeverEchoes(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	rec := e.do("POST", "/settings", with(validSettings(), "token", botToken), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/settings?pesan=pengaturan")
	wantNoBody(t, rec, botToken)

	stored, err := e.st.GetSetting(store.SettingTelegramTokenEnc)
	if err != nil || stored == "" || strings.Contains(stored, botToken) || strings.Contains(stored, "rahasia") {
		t.Fatalf("stored token = %q (%v), want it sealed", stored, err)
	}
	cfg, err := e.tg.LoadConfig()
	if err != nil || cfg.Token != botToken || cfg.ChatID != chatID {
		t.Fatalf("LoadConfig = %+v, %v", cfg, err)
	}

	rec = e.do("GET", "/settings?pesan=pengaturan", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Pengaturan disimpan", "tersimpan", `value="`+chatID+`"`)
	wantNoBody(t, rec, botToken, "rahasiaBot")
	if strings.Contains(between(rec.Body.String(), `name="token"`, ">"), "value=") {
		t.Errorf("token field has a value: %s", between(rec.Body.String(), `name="token"`, ">"))
	}

	// An empty token field keeps the saved token.
	wantCode(t, e.do("POST", "/settings", validSettings(), c), http.StatusSeeOther)
	if cfg, _ := e.tg.LoadConfig(); cfg.Token != botToken {
		t.Errorf("token after a save without one = %q, want it kept", cfg.Token)
	}

	// A refused form does not echo the posted token either.
	rec = e.do("POST", "/settings", with(validSettings(), "token", botToken, "check_every", "5"), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantNoBody(t, rec, botToken, "rahasiaBot")

	// A malformed token is refused without saving anything.
	saves, _ := e.set.counts()
	rec = e.do("POST", "/settings", with(validSettings(), "token", "bukan token", "check_every", "40"), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "token bot tidak valid")
	wantNoBody(t, rec, "bukan token")
	if n, _ := e.set.counts(); n != saves {
		t.Errorf("settings saved despite the refused token")
	}
	if cfg, _ := e.tg.LoadConfig(); cfg.Token != botToken {
		t.Errorf("token after a refused one = %q, want the old one", cfg.Token)
	}
}

func TestSettingsValidatesRanges(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	cases := []struct {
		field, value, msg string
	}{
		{"check_every", "9", "Interval pemeriksaan"},
		{"check_every", "3601", "Interval pemeriksaan"},
		{"check_every", "abc", "Interval pemeriksaan"},
		{"summary_every", "3601", "Interval ringkasan"},
		{"summary_every", "20", "tidak boleh lebih kecil dari interval pemeriksaan"}, // check is 30
		{"lag", "9", "Ambang ketinggalan"},
		{"lag", "3601", "Ambang ketinggalan"},
		{"disconnect_alert", "59", "Batas terputus"},
		{"disconnect_alert", "86401", "Batas terputus"},
		{"down_after", "9", "Batas diduga tumbang"},
		{"down_after", "601", "Batas diduga tumbang"},
		{"disk_warn", "49", "Ambang disk peringatan"},
		{"disk_warn", "99", "Ambang disk peringatan"},
		{"disk_crit", "50", "Ambang disk kritis"},
		{"disk_crit", "100", "Ambang disk kritis"},
		{"disk_warn", "92", "harus lebih kecil dari ambang disk kritis"}, // crit is 92
		{"conn_pct", "49", "Ambang koneksi"},
		{"conn_pct", "100", "Ambang koneksi"},
		{"alert_repeat", "0", "Pengulangan peringatan"},
		{"alert_repeat", "169", "Pengulangan peringatan"},
		{"lagging_alert", "59", "Batas peringatan ketinggalan"},
		{"lagging_alert", "86401", "Batas peringatan ketinggalan"},
		{"weekly_day", "Senin", "Hari ringkasan mingguan"},
		{"weekly_time", "24:00", "Jam ringkasan mingguan"},
		{"weekly_time", "7:00", "Jam ringkasan mingguan"},
		{"base_url", "ftp://panel.example.com", "Alamat dasar panel"},
		{"base_url", "panel.example.com", "Alamat dasar panel"},
		{"base_url", "http://admin:rahasia@panel.example.com", "Alamat dasar panel"},
		{"chat_id", "chat saya", "Chat id"},
	}
	for _, tc := range cases {
		rec := e.do("POST", "/settings", with(validSettings(), tc.field, tc.value), c)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s=%q: status %d, want 400", tc.field, tc.value, rec.Code)
			continue
		}
		body := rec.Body.String()
		flash := between(body, `role="alert"`, "</div>")
		if !strings.Contains(flash, tc.msg) {
			t.Errorf("%s=%q: flash %q does not mention %q", tc.field, tc.value, flash, tc.msg)
		}
		// The form comes back with the posted values.
		if tc.field != "weekly_day" && tc.field != "kinds" &&
			!strings.Contains(body, `name="`+tc.field+`" value="`+attrEscape(tc.value)+`"`) {
			t.Errorf("%s=%q: the form does not keep the posted value", tc.field, tc.value)
		}
		if !strings.Contains(body, `name="kinds" value="`+monitor.KindJobFailed+`" checked`) ||
			strings.Contains(body, `name="kinds" value="`+monitor.KindDiskWarn+`" checked`) {
			t.Errorf("%s=%q: the form does not keep the posted kinds", tc.field, tc.value)
		}
	}
	if saves, reconf := e.set.counts(); saves != 0 || reconf != 0 {
		t.Errorf("refused forms saved %d times, reconfigured %d times", saves, reconf)
	}
	if v, _ := e.st.GetSetting(store.SettingTelegramChatID); v != "" {
		t.Errorf("chat id stored from a refused form: %q", v)
	}
	// An empty base URL is allowed (messages then carry no links), and
	// stays empty: clearing a saved address persists.
	wantCode(t, e.do("POST", "/settings", validSettings(), c), http.StatusSeeOther)
	wantCode(t, e.do("POST", "/settings", with(validSettings(), "base_url", ""), c), http.StatusSeeOther)
	if got, _ := monitor.LoadSettings(e.st); got.PanelBaseURL != "" {
		t.Errorf("base URL after clearing = %q", got.PanelBaseURL)
	}
	wantBody(t, e.do("GET", "/settings", nil, c), `name="base_url" value=""`)
}

// attrEscape escapes s the way html/template writes it in an attribute.
func attrEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;", "+", "&#43;").Replace(s)
}

func TestSettingsSaveReconfiguresMonitor(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	rec := e.do("POST", "/settings", validSettings(), c)
	wantCode(t, rec, http.StatusSeeOther)
	if saves, reconf := e.set.counts(); saves != 1 || reconf != 1 {
		t.Fatalf("saved %d times, reconfigured %d times; want 1 and 1", saves, reconf)
	}
	got, err := monitor.LoadSettings(e.st)
	if err != nil {
		t.Fatal(err)
	}
	want := monitor.DefaultSettings()
	want.CheckEvery, want.SummaryEvery = 30*time.Second, 300*time.Second
	want.Lag, want.DisconnectAlert, want.DownAfter = 90*time.Second, 10*time.Minute, 45*time.Second
	want.DiskWarn, want.DiskCrit, want.ConnPct = 80, 92, 75
	want.AlertRepeat, want.LaggingAlert = 12*time.Hour, 15*time.Minute
	want.WeeklySummary, want.WeeklySummaryAt = true, "Fri 17:30"
	want.PanelBaseURL = "https://panel.example.com"
	want.AlertsRetentionDays, want.EventsRetentionDays = 60, 120
	want.StaleAfter = 3 * want.SummaryEvery
	if got != want {
		t.Fatalf("settings:\n got %+v\nwant %+v", got, want)
	}
	if v, _ := e.st.GetSetting(store.SettingTelegramChatID); v != chatID {
		t.Errorf("chat id = %q", v)
	}
	kinds, _ := e.st.GetSetting(store.SettingTelegramKinds)
	if !strings.HasPrefix(kinds, `{"agent_offline":false,`) || strings.ContainsAny(kinds, " \n") ||
		!strings.Contains(kinds, `"job_failed":true`) || !strings.Contains(kinds, `"master_down":true`) ||
		!strings.Contains(kinds, `"disk_warn":false`) || strings.Count(kinds, ":") != len(monitor.AllKinds()) {
		t.Errorf("kinds = %s, want a compact object of every kind", kinds)
	}
	cfg, err := e.tg.LoadConfig()
	if err != nil || !cfg.Kinds[monitor.KindReplicaStopped] || cfg.Kinds[monitor.KindAgentOffline] {
		t.Errorf("LoadConfig kinds = %v, %v", cfg.Kinds, err)
	}

	// Weekly summary off; the page shows the saved values back.
	form := validSettings()
	form.Del("weekly_summary")
	wantCode(t, e.do("POST", "/settings", form, c), http.StatusSeeOther)
	if got, _ := monitor.LoadSettings(e.st); got.WeeklySummary {
		t.Error("weekly summary still on")
	}
	rec = e.do("GET", "/settings", nil, c)
	wantBody(t, rec, `name="check_every" value="30"`, `name="lagging_alert" value="900"`,
		`<option value="Fri" selected>Jumat</option>`, `name="weekly_time" value="17:30"`,
		`name="base_url" value="https://panel.example.com"`, `name="kinds" value="job_failed" checked`)
	wantNoBody(t, rec, `name="kinds" value="disk_warn" checked`, `name="weekly_summary" value="on" checked`)

	// Cross-site posts are refused.
	r := httptest.NewRequest("POST", "/settings", strings.NewReader(validSettings().Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://evil.example")
	r.AddCookie(c)
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, r)
	wantCode(t, rr, http.StatusForbidden)
	if saves, _ := e.set.counts(); saves != 2 {
		t.Errorf("a cross-site post saved the settings")
	}
}

func TestTelegramTestButton(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()

	// Not set up yet.
	rec := e.do("POST", "/settings/telegram-test", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Telegram belum diatur")

	wantCode(t, e.do("POST", "/settings", with(validSettings(), "token", botToken), c), http.StatusSeeOther)
	rec = e.do("POST", "/settings/telegram-test", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/settings?pesan=tes-terkirim")
	wantBody(t, e.do("GET", "/settings?pesan=tes-terkirim", nil, c), "Pesan tes terkirim")
	msgs := e.sender.messages()
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], botToken+"|"+chatID+"|") {
		t.Fatalf("sent = %q, want one test message with the saved token and chat id", msgs)
	}

	// A second test within 10 s is refused.
	rec = e.do("POST", "/settings/telegram-test", noForm, c)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, "tunggu 10 detik")
	wantNoBody(t, rec, botToken)
	if n := len(e.sender.messages()); n != 1 {
		t.Errorf("sent %d messages, want 1", n)
	}

	var outcomes []string
	for _, a := range auditEntries(t, e.st) {
		if a.Action == "telegram_test" {
			outcomes = append(outcomes, a.Outcome)
			wantNoSecretInDetail(t, a)
		}
	}
	if len(outcomes) != 3 || outcomes[0] != "Telegram belum diatur" || outcomes[1] != "ok" ||
		!strings.Contains(outcomes[2], "tunggu 10 detik") {
		t.Errorf("telegram_test audit outcomes = %q", outcomes)
	}
}

func TestSettingsUpdateAuditHasNoToken(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	// Only the check interval, the chat id and the token differ from the
	// defaults below; the kinds stay all on.
	form := url.Values{
		"token": {botToken}, "chat_id": {chatID}, "kinds": monitor.AllKinds(),
		"check_every": {"20"}, "summary_every": {"120"}, "lag": {"60"}, "disconnect_alert": {"180"},
		"down_after": {"30"}, "disk_warn": {"85"}, "disk_crit": {"95"}, "conn_pct": {"80"},
		"alert_repeat": {"6"}, "lagging_alert": {"300"}, "weekly_summary": {"on"},
		"weekly_day": {"Mon"}, "weekly_time": {"07:00"}, "base_url": {""},
		"alerts_retention": {"90"}, "events_retention": {"180"},
	}
	wantCode(t, e.do("POST", "/settings", form, c), http.StatusSeeOther)

	var entries []store.AuditEntry
	for _, a := range auditEntries(t, e.st) {
		if a.Action == "settings_update" {
			entries = append(entries, a)
		}
	}
	if len(entries) != 1 {
		t.Fatalf("settings_update entries = %d, want 1", len(entries))
	}
	a := entries[0]
	wantNoSecretInDetail(t, a)
	for _, s := range []string{botToken, "rahasiaBot"} {
		if strings.Contains(a.DetailJSON, s) || strings.Contains(a.Outcome, s) {
			t.Errorf("audit row carries %q: %+v", s, a)
		}
	}
	// The new chat id is recorded with its value (SEC-47): a redirection
	// of the alerts can be reconstructed.
	if !strings.Contains(a.DetailJSON, `"telegram_chat_id":"`+chatID+`"`) {
		t.Errorf("detail %s does not record the new chat id", a.DetailJSON)
	}
	if a.Outcome != "ok" || a.Username != "admin" || a.IP != testRemoteIP {
		t.Errorf("audit row = %+v", a)
	}
	for _, k := range []string{store.SettingMonitorCheckEvery, store.SettingTelegramChatID, "telegram_bot"} {
		if !strings.Contains(a.DetailJSON, `"`+k+`"`) {
			t.Errorf("detail %s does not list changed key %s", a.DetailJSON, k)
		}
	}
	for _, k := range []string{store.SettingMonitorSummaryEvery, store.SettingDiskWarn, store.SettingTelegramKinds,
		store.SettingWeeklySummaryAt, store.SettingPanelBaseURL} {
		if strings.Contains(a.DetailJSON, `"`+k+`"`) {
			t.Errorf("detail %s lists unchanged key %s", a.DetailJSON, k)
		}
	}

	// A new panel base URL is recorded with its value too.
	form.Set("base_url", "https://phish.example.net")
	form.Del("token")
	wantCode(t, e.do("POST", "/settings", form, c), http.StatusSeeOther)
	all := auditEntries(t, e.st)
	last := all[len(all)-1]
	for _, x := range all {
		if x.ID > last.ID {
			last = x
		}
	}
	if last.Action != "settings_update" || !strings.Contains(last.DetailJSON, `"panel_base_url":"https://phish.example.net"`) ||
		strings.Contains(last.DetailJSON, "telegram_chat_id\":") {
		t.Errorf("settings_update after a base URL change = %s", last.DetailJSON)
	}

	// The longest base URL accepted is recorded whole (M3): the tail of
	// a long host is where the links really go.
	const maxBaseURL = 200 // the longest base URL the settings accept
	label := strings.Repeat("a", 40)
	long := "https://panel-rs1.internal.lan." + strings.Repeat(label+".", 3) + strings.Repeat("b", 32) + ".evil.example/"
	if len(long) != maxBaseURL {
		t.Fatalf("test URL is %d bytes, want %d", len(long), maxBaseURL)
	}
	form.Set("base_url", long)
	wantCode(t, e.do("POST", "/settings", form, c), http.StatusSeeOther)
	all = auditEntries(t, e.st)
	last = all[0]
	for _, x := range all {
		if x.ID > last.ID {
			last = x
		}
	}
	if !strings.Contains(last.DetailJSON, `"panel_base_url":"`+long+`"`) || strings.Contains(last.DetailJSON, "dipotong") {
		t.Errorf("settings_update after a %d-byte base URL = %s", len(long), last.DetailJSON)
	}
}

// TestRetentionSettingsValidation (spec 5.12): the Retensi section shows
// 90 and 180 days by default, takes 1 to 3650 days and stores
// alerts_retention_days and events_retention_days.
func TestRetentionSettingsValidation(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/settings", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Retensi", `name="alerts_retention" value="90"`, `name="events_retention" value="180"`,
		"Log audit dan log pekerjaan tidak pernah dipangkas")

	for _, tc := range []struct{ field, value, msg string }{
		{"alerts_retention", "0", "Retensi peringatan tertutup harus 1 sampai 3650 hari"},
		{"alerts_retention", "3651", "Retensi peringatan tertutup harus 1 sampai 3650 hari"},
		{"alerts_retention", "abc", "Retensi peringatan tertutup harus 1 sampai 3650 hari"},
		{"events_retention", "0", "Retensi riwayat status harus 1 sampai 3650 hari"},
		{"events_retention", "3651", "Retensi riwayat status harus 1 sampai 3650 hari"},
		{"events_retention", "", "Retensi riwayat status harus 1 sampai 3650 hari"},
	} {
		rec := e.do("POST", "/settings", with(validSettings(), tc.field, tc.value), c)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s=%q: status %d, want 400", tc.field, tc.value, rec.Code)
			continue
		}
		if flash := between(rec.Body.String(), `role="alert"`, "</div>"); !strings.Contains(flash, tc.msg) {
			t.Errorf("%s=%q: flash %q does not mention %q", tc.field, tc.value, flash, tc.msg)
		}
	}
	if saves, _ := e.set.counts(); saves != 0 {
		t.Fatalf("refused forms saved %d times", saves)
	}

	for _, ok := range [][2]string{{"1", "3650"}, {"3650", "1"}} {
		rec := e.do("POST", "/settings", with(validSettings(), "alerts_retention", ok[0], "events_retention", ok[1]), c)
		wantCode(t, rec, http.StatusSeeOther)
		kv, err := e.st.GetSettings(store.SettingAlertsRetentionDays, store.SettingEventsRetentionDays)
		if err != nil || kv[store.SettingAlertsRetentionDays] != ok[0] || kv[store.SettingEventsRetentionDays] != ok[1] {
			t.Fatalf("stored retention = %v, %v; want %v", kv, err, ok)
		}
	}
	rec = e.do("GET", "/settings", nil, c)
	wantBody(t, rec, `name="alerts_retention" value="3650"`, `name="events_retention" value="1"`)
}
