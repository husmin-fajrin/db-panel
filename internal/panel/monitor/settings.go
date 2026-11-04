package monitor

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Settings are the monitoring and notification settings an operator can
// change. They live in the store's settings table; LoadSettings fills the
// defaults for keys that are not set.
type Settings struct {
	CheckEvery, SummaryEvery time.Duration // sent to agents with monitor.configure
	// Thresholds.StaleAfter is always 3 × SummaryEvery.
	Thresholds
	AlertRepeat time.Duration // re-notify an alert still open after this
	// LaggingAlert is how long a replica lags before it alerts.
	LaggingAlert    time.Duration
	WeeklySummary   bool
	WeeklySummaryAt string // "Mon 07:00", panel time
	// PanelBaseURL starts the links in notifications; "" means messages
	// carry no link (the settings page only suggests the panel's public
	// URL as the field's placeholder).
	PanelBaseURL string
}

// Defaults (plan 3, Global Constraints).
const (
	defaultCheckEvery      = 15 * time.Second
	defaultSummaryEvery    = 120 * time.Second
	defaultLag             = 60 * time.Second
	defaultDisconnectAlert = 3 * time.Minute
	defaultDownAfter       = 30 * time.Second
	defaultDiskWarn        = 85
	defaultDiskCrit        = 95
	defaultConnPct         = 80
	defaultAlertRepeat     = 6 * time.Hour
	defaultLaggingAlert    = 5 * time.Minute
	defaultWeeklyAt        = "Mon 07:00"

	staleFactor = 3 // a report older than staleFactor × SummaryEvery is stale

	// Bounds monitor.configure accepts for its intervals, in seconds.
	minIntervalSec = 10
	maxIntervalSec = 3600

	weeklyLayout = "Mon 15:04"
)

// DefaultSettings are the settings of a panel nobody has configured.
func DefaultSettings() Settings {
	s := Settings{
		CheckEvery:   defaultCheckEvery,
		SummaryEvery: defaultSummaryEvery,
		Thresholds: Thresholds{
			Lag:             defaultLag,
			DisconnectAlert: defaultDisconnectAlert,
			DownAfter:       defaultDownAfter,
			DiskWarn:        defaultDiskWarn,
			DiskCrit:        defaultDiskCrit,
			ConnPct:         defaultConnPct,
		},
		AlertRepeat:     defaultAlertRepeat,
		LaggingAlert:    defaultLaggingAlert,
		WeeklySummary:   true,
		WeeklySummaryAt: defaultWeeklyAt,
	}
	s.StaleAfter = staleFactor * s.SummaryEvery
	return s
}

var settingKeys = []string{
	store.SettingMonitorCheckEvery, store.SettingMonitorSummaryEvery, store.SettingLagThreshold,
	store.SettingDisconnectAlert, store.SettingDownAfter, store.SettingDiskWarn, store.SettingDiskCrit,
	store.SettingAlertRepeatHours, store.SettingWeeklySummary, store.SettingWeeklySummaryAt,
	store.SettingPanelBaseURL, store.SettingConnPct, store.SettingLaggingAlert,
}

// LoadSettings reads the settings from s. A key that is not set, or holds a
// value SaveSettings would refuse, takes its default.
func LoadSettings(s *store.Store) (Settings, error) {
	kv, err := s.GetSettings(settingKeys...)
	if err != nil {
		return Settings{}, err
	}
	st := DefaultSettings()
	num := func(key string, lo, hi int) (int, bool) {
		v, err := strconv.Atoi(kv[key])
		return v, err == nil && v >= lo && v <= hi
	}
	secs := func(key string, lo, hi int, dst *time.Duration) {
		if v, ok := num(key, lo, hi); ok {
			*dst = time.Duration(v) * time.Second
		}
	}
	secs(store.SettingMonitorCheckEvery, minIntervalSec, maxIntervalSec, &st.CheckEvery)
	secs(store.SettingMonitorSummaryEvery, minIntervalSec, maxIntervalSec, &st.SummaryEvery)
	secs(store.SettingLagThreshold, minIntervalSec, maxIntervalSec, &st.Lag)
	secs(store.SettingDisconnectAlert, 1, maxDurationSec, &st.DisconnectAlert)
	secs(store.SettingDownAfter, 1, maxDurationSec, &st.DownAfter)
	if v, ok := num(store.SettingDiskWarn, 1, 100); ok {
		st.DiskWarn = v
	}
	if v, ok := num(store.SettingDiskCrit, 1, 100); ok {
		st.DiskCrit = v
	}
	if st.DiskWarn >= st.DiskCrit {
		st.DiskWarn, st.DiskCrit = defaultDiskWarn, defaultDiskCrit
	}
	if v, ok := num(store.SettingConnPct, 1, 100); ok {
		st.ConnPct = v
	}
	secs(store.SettingLaggingAlert, 1, maxDurationSec, &st.LaggingAlert)
	if h, ok := num(store.SettingAlertRepeatHours, 1, maxRepeatHours); ok {
		st.AlertRepeat = time.Duration(h) * time.Hour
	}
	switch kv[store.SettingWeeklySummary] {
	case "on":
		st.WeeklySummary = true
	case "off":
		st.WeeklySummary = false
	}
	if at := kv[store.SettingWeeklySummaryAt]; validWeekly(at) {
		st.WeeklySummaryAt = at
	}
	st.PanelBaseURL = kv[store.SettingPanelBaseURL]
	st.StaleAfter = staleFactor * st.SummaryEvery
	return st, nil
}

const (
	maxDurationSec = 7 * 24 * 3600 // a week
	maxRepeatHours = 7 * 24
)

func validWeekly(at string) bool {
	_, err := time.Parse(weeklyLayout, at)
	return err == nil
}

// Validate reports the first setting out of range, in words for the user.
func (st Settings) Validate() error {
	interval := func(name string, d time.Duration) error {
		if d%time.Second != 0 || d < minIntervalSec*time.Second || d > maxIntervalSec*time.Second {
			return fmt.Errorf("%s harus %d sampai %d detik", name, minIntervalSec, maxIntervalSec)
		}
		return nil
	}
	for _, c := range []struct {
		name string
		d    time.Duration
	}{{"interval pemeriksaan", st.CheckEvery}, {"interval ringkasan", st.SummaryEvery}, {"ambang ketinggalan", st.Lag}} {
		if err := interval(c.name, c.d); err != nil {
			return err
		}
	}
	switch {
	case st.DisconnectAlert < time.Second || st.DisconnectAlert > maxDurationSec*time.Second:
		return errors.New("batas peringatan terputus harus 1 detik sampai 7 hari")
	case st.DownAfter < time.Second || st.DownAfter > maxDurationSec*time.Second:
		return errors.New("batas master diduga tumbang harus 1 detik sampai 7 hari")
	case st.DiskWarn < 1 || st.DiskCrit > 100 || st.DiskWarn >= st.DiskCrit:
		return errors.New("ambang disk: peringatan harus lebih kecil dari kritis, keduanya 1 sampai 100 persen")
	case st.ConnPct < 1 || st.ConnPct > 100:
		return errors.New("ambang koneksi harus 1 sampai 100 persen")
	case st.LaggingAlert < time.Second || st.LaggingAlert > maxDurationSec*time.Second:
		return errors.New("batas peringatan ketinggalan harus 1 detik sampai 7 hari")
	case st.AlertRepeat < time.Hour || st.AlertRepeat > maxRepeatHours*time.Hour || st.AlertRepeat%time.Hour != 0:
		return errors.New("pengulangan peringatan harus 1 sampai 168 jam")
	case !validWeekly(st.WeeklySummaryAt):
		return errors.New(`waktu ringkasan mingguan harus berbentuk hari dan jam, misalnya "Mon 07:00"`)
	}
	return nil
}

// SaveSettings validates st and writes every setting it holds; StaleAfter
// is derived from SummaryEvery and not stored.
func SaveSettings(s *store.Store, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	weekly := "off"
	if st.WeeklySummary {
		weekly = "on"
	}
	sec := func(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
	kv := [][2]string{
		{store.SettingMonitorCheckEvery, sec(st.CheckEvery)},
		{store.SettingMonitorSummaryEvery, sec(st.SummaryEvery)},
		{store.SettingLagThreshold, sec(st.Lag)},
		{store.SettingDisconnectAlert, sec(st.DisconnectAlert)},
		{store.SettingDownAfter, sec(st.DownAfter)},
		{store.SettingDiskWarn, strconv.Itoa(st.DiskWarn)},
		{store.SettingDiskCrit, strconv.Itoa(st.DiskCrit)},
		{store.SettingAlertRepeatHours, strconv.FormatInt(int64(st.AlertRepeat/time.Hour), 10)},
		{store.SettingWeeklySummary, weekly},
		{store.SettingWeeklySummaryAt, st.WeeklySummaryAt},
		{store.SettingPanelBaseURL, st.PanelBaseURL},
		{store.SettingConnPct, strconv.Itoa(st.ConnPct)},
		{store.SettingLaggingAlert, sec(st.LaggingAlert)},
	}
	for _, p := range kv {
		if err := s.SetSetting(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}
