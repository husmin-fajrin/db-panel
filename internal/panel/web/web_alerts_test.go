package web_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// openAlert opens an alert of kind for serverID, opened ago before now.
func (e *env) openAlert(t *testing.T, kind string, serverID int64, ago time.Duration, msg string) int64 {
	t.Helper()
	id, err := e.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, OpenedAt: time.Now().Add(-ago), Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAlertsPageShowsOpenWithTelegramState(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	replica := e.pairedOnline(t, "db-replika")

	e.openAlert(t, monitor.KindReplicaStopped, replica.ID, 10*time.Minute, "Replika berhenti karena data ganda.")
	sent := e.openAlert(t, monitor.KindMasterLocked, master.ID, 2*time.Hour, "Master terkunci hanya-baca.")
	failed := e.openAlert(t, monitor.KindDiskWarn, master.ID, time.Hour, "Disk terpakai 87%.")
	if err := e.st.SetAlertNotified(sent, time.Now(), store.TelegramSent, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAlertNotified(failed, time.Now(), store.TelegramFailed, "chat not found"); err != nil {
		t.Fatal(err)
	}
	// History: closed 2 hours after it opened 3 days ago.
	closed := e.openAlert(t, monitor.KindAgentOffline, replica.ID, 72*time.Hour, "Agent tidak terhubung.")
	if err := e.st.CloseAlert(closed, time.Now().Add(-70*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAlertTelegramState(closed, store.TelegramRecovered, ""); err != nil {
		t.Fatal(err)
	}
	// Closed because the replica is being resynced: no recovery.
	reason := e.openAlert(t, monitor.KindReplicaUnlocked, replica.ID, 48*time.Hour, "Kunci replika terlepas.")
	if err := e.st.CloseAlertWithReason(reason, time.Now().Add(-47*time.Hour), "Resync ulang dimulai"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAlertTelegramState(reason, store.TelegramRecovered, ""); err != nil {
		t.Fatal(err)
	}
	// Older than 90 days: not shown.
	old := e.openAlert(t, monitor.KindJobFailed, replica.ID, 100*24*time.Hour, "Pekerjaan lama gagal.")
	if err := e.st.CloseAlert(old, time.Now().Add(-99*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSnooze(replica.ID, ptr(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", "/alerts", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	active := between(body, `id="aktif"`, `id="riwayat"`)
	history := between(body, `id="riwayat"`, "</section>")
	for _, s := range []string{
		"Replika berhenti", "db-replika", "Replika berhenti karena data ganda.", "Menunggu kirim",
		"Master terkunci", "db-master", "Terkirim",
		"Disk hampir penuh", "Gagal: chat not found",
		`action="/servers/` + itoa(master.ID) + `/snooze"`, `action="/servers/` + itoa(replica.ID) + `/snooze"`,
		"Jangan ganggu sampai", "10 menit lalu",
	} {
		if !strings.Contains(active, s) {
			t.Errorf("active alerts do not show %q:\n%s", s, active)
		}
	}
	if strings.Contains(active, "Agent offline") {
		t.Errorf("a closed alert is listed as active:\n%s", active)
	}
	for _, s := range []string{"Agent offline", "Agent tidak terhubung.", "Pulih terkirim", "2 jam 0 menit",
		"Ditutup: Resync ulang dimulai"} {
		if !strings.Contains(history, s) {
			t.Errorf("history does not show %q:\n%s", s, history)
		}
	}
	if strings.Contains(history, "Pekerjaan lama gagal.") {
		t.Errorf("history shows an alert older than 90 days:\n%s", history)
	}
	// The navbar badge counts the open alerts.
	wantBody(t, rec, `data-alert-count`, `href="/alerts"`, `href="/settings"`, `href="/audit"`)
	if got := between(body, "data-alert-count", "</span>"); !strings.Contains(got, ">3") {
		t.Errorf("navbar badge = %q, want 3", got)
	}

	rec = e.do("GET", "/alerts/count", nil, c)
	wantCode(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var got struct{ Count int }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Count != 3 {
		t.Errorf("GET /alerts/count = %s (%v), want count 3", rec.Body.String(), err)
	}
}

func TestAlertPagesNeedLogin(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/alerts", "/alerts/count", "/settings", "/audit"} {
		rec := e.do("GET", path, nil, nil)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("GET %s without login: %d %q, want a redirect to /login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
