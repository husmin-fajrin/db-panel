package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// auditSettings is the audit target kind of the settings page's actions;
// their target id is 0.
const auditSettings = "settings"

// auditTelegramBot names a newly saved bot token among the changed keys of
// settings_update, so the entry says that it changed without the value
// (the new chat id and panel base URL are recorded with their values).
const auditTelegramBot = "telegram_bot"

const (
	msgTelegramUnreadable = "Pengaturan Telegram tersimpan tidak bisa dibaca; simpan ulang token bot dan chat id. " +
		"Lihat log panel untuk detailnya."
	maxBaseURL = 200
)

type weekDay struct{ Value, Label string }

// weekDays are the days the weekly summary can go out, as the setting
// stores them ("Mon 07:00") with their names.
var weekDays = []weekDay{
	{"Mon", "Senin"}, {"Tue", "Selasa"}, {"Wed", "Rabu"}, {"Thu", "Kamis"},
	{"Fri", "Jumat"}, {"Sat", "Sabtu"}, {"Sun", "Minggu"},
}

var (
	weeklyTimeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	// chatIDRe: a numeric chat id (groups and channels are negative) or a
	// public channel's @name.
	chatIDRe = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// settingsForm is the settings page's form as posted (or as filled from
// the stored settings); validate turns it into settings. The bot token is
// never part of it: its field is always shown empty.
type settingsForm struct {
	ChatID string
	Kinds  map[string]bool // alert kind -> sent to Telegram

	// Seconds, except AlertRepeat (hours) and the percentages.
	CheckEvery, SummaryEvery, Lag, DisconnectAlert, DownAfter, LaggingAlert string
	DiskWarn, DiskCrit, ConnPct, AlertRepeat                                string

	Weekly                bool
	WeeklyDay, WeeklyTime string
	BaseURL               string

	// Days.
	AlertsRetention, EventsRetention string
}

// readSettingsForm reads the posted form.
func readSettingsForm(r *http.Request) settingsForm {
	v := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	f := settingsForm{
		ChatID: v("chat_id"), Kinds: make(map[string]bool),
		CheckEvery: v("check_every"), SummaryEvery: v("summary_every"), Lag: v("lag"),
		DisconnectAlert: v("disconnect_alert"), DownAfter: v("down_after"), LaggingAlert: v("lagging_alert"),
		DiskWarn: v("disk_warn"), DiskCrit: v("disk_crit"), ConnPct: v("conn_pct"), AlertRepeat: v("alert_repeat"),
		Weekly: r.PostFormValue("weekly_summary") == "on", WeeklyDay: v("weekly_day"), WeeklyTime: v("weekly_time"),
		BaseURL:         v("base_url"),
		AlertsRetention: v("alerts_retention"), EventsRetention: v("events_retention"),
	}
	posted := r.PostForm["kinds"]
	for _, k := range monitor.AllKinds() {
		f.Kinds[k] = slices.Contains(posted, k)
	}
	return f
}

// formFromSettings fills the form from the stored settings.
func formFromSettings(set monitor.Settings, chatID string, kinds map[string]bool) settingsForm {
	sec := func(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
	day, at, _ := strings.Cut(set.WeeklySummaryAt, " ")
	f := settingsForm{
		ChatID: chatID, Kinds: make(map[string]bool),
		CheckEvery: sec(set.CheckEvery), SummaryEvery: sec(set.SummaryEvery), Lag: sec(set.Lag),
		DisconnectAlert: sec(set.DisconnectAlert), DownAfter: sec(set.DownAfter), LaggingAlert: sec(set.LaggingAlert),
		DiskWarn: strconv.Itoa(set.DiskWarn), DiskCrit: strconv.Itoa(set.DiskCrit), ConnPct: strconv.Itoa(set.ConnPct),
		AlertRepeat: strconv.FormatInt(int64(set.AlertRepeat/time.Hour), 10),
		Weekly:      set.WeeklySummary, WeeklyDay: day, WeeklyTime: at,
		BaseURL:         set.PanelBaseURL,
		AlertsRetention: strconv.Itoa(set.AlertsRetentionDays), EventsRetention: strconv.Itoa(set.EventsRetentionDays),
	}
	for _, k := range monitor.AllKinds() {
		on, ok := kinds[k]
		f.Kinds[k] = !ok || on // a kind not listed is on
	}
	return f
}

// validate checks every field's range and returns base with the form's
// values, or the first problem in words for the user ("" when none).
func (f settingsForm) validate(base monitor.Settings) (monitor.Settings, string) {
	set := base
	msg := ""
	num := func(raw string, lo, hi int, name, unit string) int {
		v, err := strconv.Atoi(raw)
		if msg == "" && (err != nil || v < lo || v > hi) {
			msg = fmt.Sprintf("%s harus %d sampai %d %s.", name, lo, hi, unit)
		}
		return v
	}
	secs := func(raw string, lo, hi int, name string) time.Duration {
		return time.Duration(num(raw, lo, hi, name, "detik")) * time.Second
	}
	set.CheckEvery = secs(f.CheckEvery, 10, 3600, "Interval pemeriksaan")
	set.SummaryEvery = secs(f.SummaryEvery, 10, 3600, "Interval ringkasan")
	if msg == "" && set.SummaryEvery < set.CheckEvery {
		msg = "Interval ringkasan tidak boleh lebih kecil dari interval pemeriksaan."
	}
	// The agent accepts a lag threshold of 10 to 3600 s only.
	set.Lag = secs(f.Lag, 10, 3600, "Ambang ketinggalan")
	set.DisconnectAlert = secs(f.DisconnectAlert, 60, 86400, "Batas terputus")
	set.DownAfter = secs(f.DownAfter, 10, 600, "Batas diduga tumbang")
	set.DiskWarn = num(f.DiskWarn, 50, 98, "Ambang disk peringatan", "persen")
	set.DiskCrit = num(f.DiskCrit, 51, 99, "Ambang disk kritis", "persen")
	if msg == "" && set.DiskWarn >= set.DiskCrit {
		msg = "Ambang disk peringatan harus lebih kecil dari ambang disk kritis."
	}
	set.ConnPct = num(f.ConnPct, 50, 99, "Ambang koneksi", "persen dari max_connections")
	set.AlertRepeat = time.Duration(num(f.AlertRepeat, 1, 168, "Pengulangan peringatan", "jam")) * time.Hour
	set.LaggingAlert = secs(f.LaggingAlert, 60, 86400, "Batas peringatan ketinggalan")
	set.AlertsRetentionDays = num(f.AlertsRetention, monitor.MinRetentionDays, monitor.MaxRetentionDays,
		"Retensi peringatan tertutup", "hari")
	set.EventsRetentionDays = num(f.EventsRetention, monitor.MinRetentionDays, monitor.MaxRetentionDays,
		"Retensi riwayat status", "hari")
	switch {
	case msg != "":
	case !slices.ContainsFunc(weekDays, func(d weekDay) bool { return d.Value == f.WeeklyDay }):
		msg = "Hari ringkasan mingguan tidak dikenal."
	case !weeklyTimeRe.MatchString(f.WeeklyTime):
		msg = "Jam ringkasan mingguan harus berbentuk JJ:MM, misalnya 07:00."
	case !validBaseURL(f.BaseURL):
		msg = "Alamat dasar panel harus alamat lengkap yang diawali http:// atau https:// " +
			"(tanpa nama pengguna dan sandi), atau dikosongkan."
	case f.ChatID != "" && !chatIDRe.MatchString(f.ChatID):
		msg = "Chat id harus angka (grup dan kanal diawali tanda minus) atau @nama kanal."
	}
	if msg != "" {
		return base, msg
	}
	set.WeeklySummary = f.Weekly
	set.WeeklySummaryAt = f.WeeklyDay + " " + f.WeeklyTime
	set.PanelBaseURL = f.BaseURL
	set.StaleAfter = 3 * set.SummaryEvery // as monitor.LoadSettings derives it
	return set, ""
}

// validBaseURL: empty, or an absolute http(s) URL without credentials,
// query or fragment.
func validBaseURL(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxBaseURL {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// kindsJSON is the telegram_kinds_json setting: every kind with its switch,
// as a compact object.
func (f settingsForm) kindsJSON() string {
	raw, _ := json.Marshal(f.Kinds) // map keys come out sorted
	return string(raw)
}

type kindOption struct {
	Kind, Label string
	On          bool
}

type settingsPage struct {
	Page
	Form       settingsForm
	TokenSaved bool
	PublicURL  string
	Kinds      []kindOption
	Days       []weekDay
	// PlainBaseURL: the address for links starts with http://; the page
	// offers to change it to https://.
	PlainBaseURL bool
	Cert         CertInfo
	ExtraNames   string // https_extra_names, as stored
}

func (a *app) renderSettings(w http.ResponseWriter, r *http.Request, status int, f settingsForm, tokenSaved bool, flash string) {
	p := settingsPage{Page: a.page(r), Form: f, TokenSaved: tokenSaved, PublicURL: a.PublicURL(), Days: weekDays,
		PlainBaseURL: plainBaseURL(f.BaseURL), Cert: a.CertInfo()}
	extra, err := a.Store.GetSetting(store.SettingHTTPSExtraNames)
	if err != nil {
		a.Logger.Warn("read extra certificate names", "err", err)
	}
	p.ExtraNames = strings.ReplaceAll(extra, ",", ", ")
	p.Flash = flash
	p.Notice = notices[r.URL.Query().Get("pesan")]
	for _, k := range monitor.AllKinds() {
		p.Kinds = append(p.Kinds, kindOption{Kind: k, Label: alertKindLabel(k), On: f.Kinds[k]})
	}
	a.render.renderStatus(w, status, "settings", p)
}

// storedTelegram reads the chat id and the kind switches as stored.
func (a *app) storedTelegram() (chatID string, kinds map[string]bool, err error) {
	kv, err := a.Store.GetSettings(store.SettingTelegramChatID, store.SettingTelegramKinds)
	if err != nil {
		return "", nil, err
	}
	if raw := kv[store.SettingTelegramKinds]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &kinds); err != nil {
			// The form shows every kind on; saving it rewrites the setting.
			a.Logger.Warn("stored telegram kinds unreadable", "err", err)
			kinds = nil
		}
	}
	return kv[store.SettingTelegramChatID], kinds, nil
}

// tokenSaved reports whether a bot token is stored; a stored token that
// cannot be opened is an error worded for the user.
func (a *app) tokenSaved() (bool, string) {
	cfg, err := a.Telegram.LoadConfig()
	if err != nil {
		a.Logger.Warn("load telegram settings", "err", err)
		return false, msgTelegramUnreadable
	}
	return cfg.Token != "", ""
}

// renderStoredSettings shows the settings page filled from the stored
// settings; flash, when set, wins over a problem reading them.
func (a *app) renderStoredSettings(w http.ResponseWriter, r *http.Request, status int, flash string) {
	set, err := a.Settings.Load()
	if err != nil {
		a.internalError(w, "load settings", err)
		return
	}
	chatID, kinds, err := a.storedTelegram()
	if err != nil {
		a.internalError(w, "load telegram settings", err)
		return
	}
	saved, problem := a.tokenSaved()
	if flash == "" {
		flash = problem
	}
	// An empty base URL stays empty (messages then carry no links); the
	// public URL is only the field's placeholder, so clearing it sticks.
	f := formFromSettings(set, chatID, kinds)
	a.renderSettings(w, r, status, f, saved, flash)
}

func (a *app) showSettings(w http.ResponseWriter, r *http.Request) {
	a.renderStoredSettings(w, r, http.StatusOK, "")
}

func (a *app) saveSettings(w http.ResponseWriter, r *http.Request) {
	f := readSettingsForm(r)
	prev, err := a.Settings.Load()
	if err != nil {
		a.internalError(w, "load settings", err)
		return
	}
	refuse := func(msg string) {
		a.audit(r, "settings_update", auditSettings, 0, nil, msg)
		saved, _ := a.tokenSaved()
		a.renderSettings(w, r, http.StatusBadRequest, f, saved, msg)
	}
	set, msg := f.validate(prev)
	if msg != "" {
		refuse(msg)
		return
	}
	prevChat, prevKinds, err := a.storedTelegram()
	if err != nil {
		a.internalError(w, "load telegram settings", err)
		return
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	if token != "" {
		// First, so a refused token leaves every setting as it was. The
		// error never holds the token.
		if err := a.Telegram.SaveToken(token); err != nil {
			a.Logger.Warn("save telegram token refused", "err", err)
			refuse(err.Error())
			return
		}
	}
	err = a.Settings.Save(set)
	if err == nil {
		err = errors.Join(
			a.Store.SetSetting(store.SettingTelegramChatID, f.ChatID),
			a.Store.SetSetting(store.SettingTelegramKinds, f.kindsJSON()),
		)
	}
	if err != nil {
		a.audit(r, "settings_update", auditSettings, 0, nil, err.Error())
		a.internalError(w, "save settings", err)
		return
	}
	a.Settings.Reconfigure()

	changed := changedSettings(prev, set)
	if f.ChatID != prevChat {
		changed = append(changed, store.SettingTelegramChatID)
	}
	if !sameKinds(prevKinds, f.Kinds) {
		changed = append(changed, store.SettingTelegramKinds)
	}
	if token != "" {
		changed = append(changed, auditTelegramBot)
	}
	// The new values of the keys that redirect messages or their links are
	// recorded (SEC-47); the token never is.
	detail := map[string]any{"changed": changed}
	if f.ChatID != prevChat {
		detail[store.SettingTelegramChatID] = f.ChatID
	}
	if prev.PanelBaseURL != set.PanelBaseURL {
		detail[store.SettingPanelBaseURL] = set.PanelBaseURL
	}
	a.audit(r, "settings_update", auditSettings, 0, detail, auditOK)
	a.Logger.Info("settings saved", "changed", changed, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, "/settings?pesan=pengaturan", http.StatusSeeOther)
}

// sameKinds reports whether the stored kind switches prev (a kind not
// listed is on) equal cur.
func sameKinds(prev, cur map[string]bool) bool {
	for _, k := range monitor.AllKinds() {
		on, ok := prev[k]
		if (!ok || on) != cur[k] {
			return false
		}
	}
	return true
}

// changedSettings lists the setting keys whose value differs between prev
// and cur; never nil.
func changedSettings(prev, cur monitor.Settings) []string {
	out := []string{}
	for _, c := range []struct {
		key  string
		same bool
	}{
		{store.SettingMonitorCheckEvery, prev.CheckEvery == cur.CheckEvery},
		{store.SettingMonitorSummaryEvery, prev.SummaryEvery == cur.SummaryEvery},
		{store.SettingLagThreshold, prev.Lag == cur.Lag},
		{store.SettingDisconnectAlert, prev.DisconnectAlert == cur.DisconnectAlert},
		{store.SettingDownAfter, prev.DownAfter == cur.DownAfter},
		{store.SettingDiskWarn, prev.DiskWarn == cur.DiskWarn},
		{store.SettingDiskCrit, prev.DiskCrit == cur.DiskCrit},
		{store.SettingConnPct, prev.ConnPct == cur.ConnPct},
		{store.SettingAlertRepeatHours, prev.AlertRepeat == cur.AlertRepeat},
		{store.SettingLaggingAlert, prev.LaggingAlert == cur.LaggingAlert},
		{store.SettingWeeklySummary, prev.WeeklySummary == cur.WeeklySummary},
		{store.SettingWeeklySummaryAt, prev.WeeklySummaryAt == cur.WeeklySummaryAt},
		{store.SettingPanelBaseURL, prev.PanelBaseURL == cur.PanelBaseURL},
		{store.SettingAlertsRetentionDays, prev.AlertsRetentionDays == cur.AlertsRetentionDays},
		{store.SettingEventsRetentionDays, prev.EventsRetentionDays == cur.EventsRetentionDays},
	} {
		if !c.same {
			out = append(out, c.key)
		}
	}
	return out
}

// testTelegram sends a test message with the saved settings (at most one
// per 10 s, enforced by the notifier).
func (a *app) testTelegram(w http.ResponseWriter, r *http.Request) {
	err := a.Telegram.SendTest(r.Context())
	a.audit(r, "telegram_test", auditSettings, 0, nil, outcomeOf(err))
	if err != nil {
		// The notifier's errors never hold the bot token.
		a.Logger.Warn("telegram test failed", "err", err)
		status := http.StatusBadRequest
		if errors.Is(err, notify.ErrTestTooSoon) {
			status = http.StatusTooManyRequests
		}
		a.renderStoredSettings(w, r, status, "Pesan tes tidak terkirim: "+err.Error())
		return
	}
	http.Redirect(w, r, "/settings?pesan=tes-terkirim", http.StatusSeeOther)
}
