package monitor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	st := openStore(t)
	got, err := LoadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		CheckEvery:   15 * time.Second,
		SummaryEvery: 120 * time.Second,
		Thresholds: Thresholds{
			Lag:             60 * time.Second,
			DisconnectAlert: 3 * time.Minute,
			DownAfter:       30 * time.Second,
			StaleAfter:      360 * time.Second,
			DiskWarn:        85,
			DiskCrit:        95,
			ConnPct:         80,
		},
		AlertRepeat:     6 * time.Hour,
		LaggingAlert:    5 * time.Minute,
		WeeklySummary:   true,
		WeeklySummaryAt: "Mon 07:00",
		PanelBaseURL:    "",

		AlertsRetentionDays: 90,
		EventsRetentionDays: 180,

		SamplesRetentionDays: 7,
	}
	if got != want {
		t.Fatalf("defaults:\n got %+v\nwant %+v", got, want)
	}

	mod := want
	mod.CheckEvery = 30 * time.Second
	mod.SummaryEvery = 300 * time.Second
	mod.Lag = 90 * time.Second
	mod.DisconnectAlert = 5 * time.Minute
	mod.DownAfter = 45 * time.Second
	mod.DiskWarn, mod.DiskCrit = 80, 90
	mod.AlertRepeat = 12 * time.Hour
	mod.ConnPct = 90
	mod.LaggingAlert = 10 * time.Minute
	mod.WeeklySummary = false
	mod.WeeklySummaryAt = "Fri 17:30"
	mod.PanelBaseURL = "https://panel.example.com"
	mod.AlertsRetentionDays, mod.EventsRetentionDays = 30, 3650
	mod.SamplesRetentionDays = 90
	if err := SaveSettings(st, mod); err != nil {
		t.Fatal(err)
	}
	got, err = LoadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	mod.StaleAfter = 900 * time.Second // 3 × the new summary interval
	if got != mod {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, mod)
	}
	if v, _ := st.GetSetting(store.SettingWeeklySummary); v != "off" {
		t.Fatalf("weekly_summary stored as %q, want off", v)
	}
	if v, _ := st.GetSetting(store.SettingMonitorCheckEvery); v != "30" {
		t.Fatalf("monitor_check_every stored as %q, want 30", v)
	}

	// A value the agent would refuse falls back to its default.
	if err := st.SetSetting(store.SettingMonitorCheckEvery, "5"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(store.SettingDiskWarn, "abc"); err != nil {
		t.Fatal(err)
	}
	got, err = LoadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckEvery != 15*time.Second || got.DiskWarn != 85 {
		t.Fatalf("invalid stored values: CheckEvery %v DiskWarn %d, want the defaults", got.CheckEvery, got.DiskWarn)
	}

	for _, c := range []struct {
		name   string
		mutate func(s *Settings)
		want   string
	}{
		{"check too short", func(s *Settings) { s.CheckEvery = 5 * time.Second }, "10 sampai 3600"},
		{"summary too long", func(s *Settings) { s.SummaryEvery = 2 * time.Hour }, "10 sampai 3600"},
		{"disk order", func(s *Settings) { s.DiskWarn, s.DiskCrit = 95, 90 }, "disk"},
		{"weekly time", func(s *Settings) { s.WeeklySummaryAt = "Senin pagi" }, "Mon 07:00"},
		{"connections over 100", func(s *Settings) { s.ConnPct = 101 }, "koneksi"},
		{"lagging alert zero", func(s *Settings) { s.LaggingAlert = 0 }, "ketinggalan"},
		{"samples retention zero", func(s *Settings) { s.SamplesRetentionDays = 0 }, "sampel lag harus 1 sampai 90"},
		{"samples retention over 90", func(s *Settings) { s.SamplesRetentionDays = 91 }, "sampel lag harus 1 sampai 90"},
		{"alerts retention zero", func(s *Settings) { s.AlertsRetentionDays = 0 }, "retensi peringatan tertutup harus 1 sampai 3650 hari"},
		{"events retention over", func(s *Settings) { s.EventsRetentionDays = 3651 }, "retensi riwayat status harus 1 sampai 3650 hari"},
	} {
		s := want
		c.mutate(&s)
		err := SaveSettings(st, s)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: SaveSettings err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}
}
