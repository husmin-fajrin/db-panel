package notify

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// startSnooze snoozes the server for d from the rig's clock, in the store
// (where the digest is driven from) and in the rig's snooze flag (the
// monitor's memory).
func (r *rig) startSnooze(serverID int64, d time.Duration) time.Time {
	r.t.Helper()
	until := r.clk.Now().Add(d)
	if err := r.st.SetSnooze(serverID, &until, r.clk.Now()); err != nil {
		r.t.Fatal(err)
	}
	r.snooze(serverID, true)
	return until
}

// digests are the snooze digests the Bot API received.
func (r *rig) digests() []string {
	var out []string
	for _, t := range r.texts() {
		if strings.HasPrefix(t, "Jangan ganggu untuk ") {
			out = append(out, t)
		}
	}
	return out
}

// TestUnsnoozableKindsPinned pins the set (spec 5.7): a later edit cannot
// shrink it silently, and every member is a real alert kind.
func TestUnsnoozableKindsPinned(t *testing.T) {
	want := []string{
		monitor.KindReplicaUnlocked, monitor.KindMasterDown, monitor.KindDiskCrit, monitor.KindPromotePartial,
		monitor.KindMasterLockExpired, monitor.KindMasterQuarantined, monitor.KindMasterUnpinned,
		monitor.KindMasterStartUnguarded, monitor.KindMasterStartLate, monitor.KindMasterStartBypassed,
		monitor.KindMasterStartBlocked, monitor.KindMasterLockKept,
	}
	if len(unsnoozableKinds) != 12 {
		t.Fatalf("unsnoozableKinds has %d members, want 12", len(unsnoozableKinds))
	}
	for _, k := range want {
		if !unsnoozableKinds[k] {
			t.Errorf("%s is no longer unsnoozable", k)
		}
	}
	all := monitor.AllKinds()
	for k := range unsnoozableKinds {
		if !slices.Contains(all, k) {
			t.Errorf("unsnoozable kind %q is not in AllKinds", k)
		}
	}
	got := UnsnoozableKinds()
	var inOrder []string
	for _, k := range all {
		if unsnoozableKinds[k] {
			inOrder = append(inOrder, k)
		}
	}
	if !slices.Equal(got, inOrder) || len(got) != 12 {
		t.Fatalf("UnsnoozableKinds() = %v, want %v (AllKinds order)", got, inOrder)
	}
}

// TestCriticalKindsSentOnceDuringSnooze: master_down opened during a
// snooze goes out once; seven hours later, still snoozed and past the
// repeat, nothing more.
func TestCriticalKindsSentOnceDuringSnooze(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, 24*time.Hour)
	id := r.open(monitor.KindMasterDown, srv, "Master diduga tumbang.")
	r.pass()
	got := r.texts()
	if len(got) != 1 || !strings.Contains(got[0], "Master diduga tumbang.\nServer: DB-1") {
		t.Fatalf("messages = %q, want master_down once", got)
	}
	r.clk.Add(7 * time.Hour) // past the 6 h repeat
	r.pass()
	if got := r.texts(); len(got) != 1 {
		t.Fatalf("repeated during the snooze: %q", got)
	}
	if a := r.alert(id); a.NotifyCount != 1 || a.TelegramState != store.TelegramSent {
		t.Fatalf("alert = %+v", a)
	}
}

// TestCriticalAlreadySentBeforeSnoozeNotRepeated: an unsnoozable alert
// sent before the snooze stays quiet while it lasts.
func TestCriticalAlreadySentBeforeSnoozeNotRepeated(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.open(monitor.KindDiskCrit, srv, "Disk data terpakai 96%.")
	r.pass()
	r.startSnooze(srv, 24*time.Hour)
	r.clk.Add(7 * time.Hour)
	r.pass()
	if got := r.texts(); len(got) != 1 {
		t.Fatalf("messages = %q, want only the one sent before the snooze", got)
	}
}

// TestCriticalFailedSendDuringSnoozeIsRetried: a failed first send is
// neither marked sent nor forgotten; the next pass, still snoozed, sends it.
func TestCriticalFailedSendDuringSnoozeIsRetried(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, 24*time.Hour)
	id := r.open(monitor.KindMasterDown, srv, "Master diduga tumbang.")
	r.api.fail(500, "boom")
	r.pass()
	if a := r.alert(id); a.TelegramState != store.TelegramFailed || a.NotifyCount != 0 {
		t.Fatalf("after a failed send: %+v", a)
	}
	r.api.fail(0, "")
	r.clk.Add(time.Minute)
	r.pass()
	if a := r.alert(id); a.TelegramState != store.TelegramSent || a.NotifyCount != 1 {
		t.Fatalf("after the retry: %+v", a)
	}
}

// TestCriticalRecoverySentDuringSnooze: the recovery of an unsnoozable
// alert goes out while the snooze lasts.
func TestCriticalRecoverySentDuringSnooze(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, 24*time.Hour)
	id := r.open(monitor.KindMasterDown, srv, "Master diduga tumbang.")
	r.pass()
	r.clk.Add(10 * time.Minute)
	r.close(id)
	r.clk.Add(recoveryHold)
	r.pass()
	got := r.texts()
	if len(got) != 2 || !strings.HasPrefix(got[1], "✅ DB-1 pulih: master diduga tumbang selesai setelah 10 menit") {
		t.Fatalf("messages = %q, want the alert and its recovery", got)
	}
	if a := r.alert(id); a.TelegramState != store.TelegramRecovered {
		t.Fatalf("alert = %+v", a)
	}
}

// TestOtherKindsSilencedAsBefore: replica_lagging opened during a snooze
// is not sent, nor is its recovery.
func TestOtherKindsSilencedAsBefore(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, 24*time.Hour)
	id := r.open(monitor.KindReplicaLagging, srv, "Replika tertinggal 120 detik.")
	r.pass()
	r.clk.Add(7 * time.Hour)
	r.pass()
	if a := r.alert(id); a.TelegramState != store.TelegramPending {
		t.Fatalf("open alert = %+v, want pending", a)
	}
	r.close(id)
	r.clk.Add(recoveryHold)
	r.pass()
	if got := r.texts(); len(got) != 0 {
		t.Fatalf("messages = %q, want none", got)
	}
}

// TestSnoozeDigestAfterPanelRestart (spec 5.10): the snooze ends while the
// old Notifier is gone; a new one over the same store sends one digest with
// the alert still open and the one that recovered, and clears snooze_from.
func TestSnoozeDigestAfterPanelRestart(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	until := r.clk.Now().Add(2 * time.Hour)
	r.startSnooze(srv, 2*time.Hour)
	r.clk.Add(10 * time.Minute)
	recovered := r.open(monitor.KindMasterDown, srv, "Master diduga tumbang.")
	r.pass() // sent despite the snooze
	r.clk.Add(30 * time.Minute)
	r.close(recovered)
	r.clk.Add(recoveryHold)
	r.pass()
	r.open(monitor.KindReplicaLagging, srv, "Replika tertinggal 120 detik.") // stays open, held back
	r.clk.Add(10 * time.Minute)
	r.pass()
	if got := r.digests(); len(got) != 0 {
		t.Fatalf("digest before the snooze ended: %q", got)
	}

	// The panel stops; the snooze ends while it is down; a new one starts.
	r.clk.Add(3 * time.Hour)
	r.snooze(srv, false)
	r.restart()
	r.pass()
	got := r.digests()
	if len(got) != 1 {
		t.Fatalf("digests after the restart = %q, want one", got)
	}
	d := got[0]
	for _, want := range []string{
		"Jangan ganggu untuk DB-1 berakhir (09:00–11:00). Selama senyap terbuka 2 peringatan:",
		"• master diduga tumbang · 09:10 · pulih 09:40",
		"• replika ketinggalan · 09:41",
		"http://panel.local/alerts",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("digest lacks %q:\n%s", want, d)
		}
	}
	srvNow, err := r.st.GetServer(srv)
	if err != nil || srvNow.SnoozeFrom != nil {
		t.Fatalf("snooze_from after the digest = %v, %v; want nil", srvNow.SnoozeFrom, err)
	}
	if srvNow.SnoozeUntil == nil || !srvNow.SnoozeUntil.Equal(until) {
		t.Fatalf("snooze_until = %v, want %v", srvNow.SnoozeUntil, until)
	}
	r.restart()
	r.pass()
	if got := r.digests(); len(got) != 1 {
		t.Fatalf("a second digest went out: %q", got)
	}
}

// TestSnoozeDigestSentOnce: two passes after the end send one digest; a
// failed send leaves it owed and the next pass sends it. A snooze during
// which nothing opened says so.
func TestSnoozeDigestSentOnce(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, time.Hour)
	r.clk.Add(2 * time.Hour)
	r.snooze(srv, false)

	r.api.fail(500, "boom")
	r.pass()
	if sp, err := r.st.EndedSnoozes(r.clk.Now()); err != nil || len(sp) != 1 {
		t.Fatalf("after a failed send the digest is not owed any more: %+v, %v", sp, err)
	}
	r.api.fail(0, "")
	r.pass()
	r.pass()
	got := r.digests()
	if len(got) != 1 {
		t.Fatalf("digests = %q, want one", got)
	}
	want := "Jangan ganggu untuk DB-1 berakhir (09:00–10:00). Selama senyap tidak ada peringatan baru.\nhttp://panel.local/alerts"
	if got[0] != want {
		t.Fatalf("digest = %q, want %q", got[0], want)
	}
	if sp, _ := r.st.EndedSnoozes(r.clk.Now()); len(sp) != 0 {
		t.Fatalf("digest still owed: %+v", sp)
	}
}

// TestSnoozeDigestAfterCancelAndExtend: cancelling a snooze ends it now and
// its digest still goes out, from the start; extending keeps the start.
func TestSnoozeDigestAfterCancelAndExtend(t *testing.T) {
	r := newRig(t)
	r.configure()
	srv := r.server("DB-1")
	r.startSnooze(srv, time.Hour)
	r.clk.Add(30 * time.Minute)
	ext := r.clk.Now().Add(2 * time.Hour)
	if err := r.st.SetSnooze(srv, &ext, r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.clk.Add(30 * time.Minute)
	r.pass()
	if got := r.digests(); len(got) != 0 {
		t.Fatalf("digest while the extended snooze runs: %q", got)
	}
	if err := r.st.SetSnooze(srv, nil, r.clk.Now()); err != nil { // cancelled at 10:00
		t.Fatal(err)
	}
	r.snooze(srv, false)
	r.pass()
	got := r.digests()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Jangan ganggu untuk DB-1 berakhir (09:00–10:00).") {
		t.Fatalf("digests = %q, want one from 09:00 to 10:00", got)
	}
}
