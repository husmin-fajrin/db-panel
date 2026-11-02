package notify

import (
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// The panel's local time zone: Format prints the time people read on the
// panel host's clock.
func at(day, hour, min int) time.Time { return time.Date(2026, 11, day, hour, min, 0, 0, time.Local) }

// TestFormatMatchesSpec: the replica_stopped message for error 1062 is the
// example of spec 5.3, letter for letter.
func TestFormatMatchesSpec(t *testing.T) {
	a := store.Alert{
		Kind: monitor.KindReplicaStopped, ServerID: 3, OpenedAt: at(2, 2, 14),
		// The alert engine's message for a replica stopped by error 1062.
		Message: "Replikasi berhenti karena error 1062. " + monitor.Explain(1062, 0).Text + " Saran: Resync ulang.",
	}
	got := Format(a, "RS-2", "RS-1", "http://panel.local", at(2, 2, 15))
	want := "⛔ Replika RS-2 berhenti\n" +
		"Data ganda (error 1062): ada baris yang sudah ada di replika.\n" +
		"Saran: Resync ulang.\n" +
		"Master: RS-1 · Sejak: 02:14 · http://panel.local/replicas/3"
	if got != want {
		t.Fatalf("Format =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatStoppedVariants(t *testing.T) {
	now := at(2, 9, 0)
	cases := []struct {
		name, msg, master, base, want string
	}{
		{"no advice, no colon", "Replikasi berhenti karena error 4242. Penyebab belum dikenali panel.", "RS-1", "",
			"⛔ Replika RS-2 berhenti\nPenyebab belum dikenali panel (error 4242).\nMaster: RS-1 · Sejak: 08:30"},
		{"advice with comma", "Replikasi berhenti karena error 1594. Relay log di replika rusak. Saran: x.", "", "http://p/",
			"⛔ Replika RS-2 berhenti\nRelay log di replika rusak (error 1594).\n" +
				"Saran: Start replika; bila gagal lagi, Resync ulang.\nSejak: 08:30 · http://p/replicas/3"},
		{"no errno", "Replikasi berhenti tanpa lewat panel.", "RS-1", "http://p",
			"⛔ Replika RS-2 berhenti\nReplikasi berhenti tanpa lewat panel.\nMaster: RS-1 · Sejak: 08:30 · http://p/replicas/3"},
	}
	for _, c := range cases {
		a := store.Alert{Kind: monitor.KindReplicaStopped, ServerID: 3, OpenedAt: at(2, 8, 30), Message: c.msg}
		if got := Format(a, "RS-2", c.master, c.base, now); got != c.want {
			t.Errorf("%s: Format =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

func TestFormatOtherKinds(t *testing.T) {
	now := at(3, 9, 0)
	cases := []struct {
		kind, msg string
		opened    time.Time
		base      string
		want      string
	}{
		{monitor.KindDiskWarn, "Disk data terpakai 87%.", at(3, 8, 5), "http://p",
			"⚠️ Disk data terpakai 87%.\nServer: DB-1 · Sejak: 08:05 · http://p/servers/7"},
		{monitor.KindDiskCrit, "Disk data terpakai 96%.", at(3, 8, 5), "http://p",
			"⛔ Disk data terpakai 96%.\nServer: DB-1 · Sejak: 08:05 · http://p/servers/7"},
		{monitor.KindMasterLocked, "Master terkunci.", at(1, 23, 50), "http://p",
			"⛔ Master terkunci.\nServer: DB-1 · Sejak: 01-11 23:50 · http://p/masters/7"},
		{monitor.KindReplicaLagging, "Replika tertinggal.", at(3, 8, 5), "",
			"⚠️ Replika tertinggal.\nServer: DB-1 · Sejak: 08:05"},
		{monitor.KindReplicaUnlocked, "Kunci terlepas.", at(3, 8, 5), "http://p",
			"⛔ Kunci terlepas.\nServer: DB-1 · Sejak: 08:05 · http://p/replicas/7"},
	}
	for _, c := range cases {
		a := store.Alert{Kind: c.kind, ServerID: 7, OpenedAt: c.opened, Message: c.msg}
		if got := Format(a, "DB-1", "", c.base, now); got != c.want {
			t.Errorf("%s: Format =\n%s\nwant\n%s", c.kind, got, c.want)
		}
	}
}

// TestEveryKindHasIconAndName: no kind falls through to an empty icon or
// a raw kind name.
func TestEveryKindHasIconAndName(t *testing.T) {
	for _, k := range monitor.AllKinds() {
		if _, ok := kindNames[k]; !ok {
			t.Errorf("kind %s has no plain name", k)
		}
		if icon(k) == "" {
			t.Errorf("kind %s has no icon", k)
		}
	}
}

func TestFormatRecovered(t *testing.T) {
	opened := at(2, 2, 14)
	a := store.Alert{Kind: monitor.KindReplicaStopped, ServerID: 3, OpenedAt: opened}
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45 detik"},
		{12 * time.Minute, "12 menit"},
		{2 * time.Hour, "2 jam"},
		{2*time.Hour + 5*time.Minute + 30*time.Second, "2 jam 5 menit"},
		{50 * time.Hour, "2 hari 2 jam"},
	}
	for _, c := range cases {
		got := FormatRecovered(a, "RS-2", opened.Add(c.d))
		if want := "✅ RS-2 pulih: replika berhenti selesai setelah " + c.want; got != want {
			t.Errorf("FormatRecovered(%v) = %q, want %q", c.d, got, want)
		}
	}
}
