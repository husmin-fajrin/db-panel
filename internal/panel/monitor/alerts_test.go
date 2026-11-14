package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// alerts are the alerts of kind for server id, open and closed, oldest
// first.
func (r *rig) alerts(id int64, kind string) []store.Alert {
	r.t.Helper()
	all, err := r.st.ListAlerts(time.Time{}, 10000)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []store.Alert
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].ServerID == id && all[i].Kind == kind {
			out = append(out, all[i])
		}
	}
	return out
}

// openAlert returns the open alert of kind for id, failing when there is
// none.
func (r *rig) openAlert(id int64, kind string) store.Alert {
	r.t.Helper()
	a, err := r.st.GetOpenAlert(kind, id)
	if err != nil {
		r.t.Fatalf("no open %s alert for server %d: %v", kind, id, err)
	}
	return a
}

func (r *rig) isOpen(id int64, kind string) bool {
	r.t.Helper()
	_, err := r.st.GetOpenAlert(kind, id)
	if err != nil && err != store.ErrNotFound {
		r.t.Fatal(err)
	}
	return err == nil
}

// alertUpdates are the alert_update payloads published for id and kind.
func (r *rig) alertUpdates(id int64, kind string) []map[string]any {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	var out []map[string]any
	for _, p := range r.pubs {
		if p.Event != "alert_update" {
			continue
		}
		b, _ := json.Marshal(p.Data)
		var m map[string]any
		json.Unmarshal(b, &m)
		if int64(m["server_id"].(float64)) == id && m["kind"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func stoppedBy(errno int, text string) func(s *proto.HealthSnapshot) {
	return func(s *proto.HealthSnapshot) {
		rs := *s.Replica
		rs.SQLRunning, rs.LastSQLErrno, rs.LastSQLError = "No", errno, text
		s.Replica = &rs
	}
}

func running(s *proto.HealthSnapshot) {
	rs := *s.Replica
	rs.SQLRunning, rs.IORunning, rs.LastSQLErrno, rs.LastSQLError = "Yes", "Yes", 0, ""
	rs.SecondsBehind = ptr(0)
	s.Replica = &rs
}

func TestAllKinds(t *testing.T) {
	want := "replica_stopped replica_disconnected replica_lagging replica_unlocked master_locked master_down " +
		"mysql_unreachable agent_offline disk_warn disk_crit connections_high binlog_deadline job_failed tls_expiring web_tls_expiring " +
		"promote_cutover promote_partial replica_needs_repoint replica_needs_rebuild master_quarantined master_lock_expired master_unpinned " +
		"master_start_unguarded master_start_late master_start_bypassed master_start_blocked master_lock_kept"
	if got := strings.Join(AllKinds(), " "); got != want {
		t.Fatalf("AllKinds = %s", got)
	}
}

func TestOpenOnceWhileCondition(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	ra.setSnap(stoppedBy(1062, "Duplicate entry"))
	ra.send()
	for range 10 {
		r.clk.Add(2 * time.Second) // within the unattached master's offline grace
		r.m.tick()
		ra.send()
	}
	as := r.alerts(rID, KindReplicaStopped)
	if len(as) != 1 || as[0].ClosedAt != nil || as[0].TopologyID != topo.ID {
		t.Fatalf("replica_stopped alerts = %+v, want one open in topology %d", as, topo.ID)
	}
	ups := r.alertUpdates(rID, KindReplicaStopped)
	if len(ups) != 1 || ups[0]["open"] != true || int64(ups[0]["id"].(float64)) != as[0].ID || len(ups[0]) != 4 {
		t.Fatalf("alert_update = %v, want one open for alert %d", ups, as[0].ID)
	}
	if n := changed.Load(); n != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", n)
	}
}

func TestCloseOnRecovery(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	ra.setSnap(stoppedBy(1032, "Could not find row"))
	ra.send()
	opened := r.openAlert(rID, KindReplicaStopped)
	r.clk.Add(time.Minute)
	ra.setSnap(running)
	ra.send()
	if r.isOpen(rID, KindReplicaStopped) {
		t.Fatal("replica_stopped still open after Sinkron")
	}
	as := r.alerts(rID, KindReplicaStopped)
	if len(as) != 1 || as[0].ID != opened.ID || as[0].ClosedAt == nil || !as[0].ClosedAt.Equal(r.clk.Now()) {
		t.Fatalf("alerts = %+v, want the one alert closed now", as)
	}
	ups := r.alertUpdates(rID, KindReplicaStopped)
	if len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v, want open then closed", ups)
	}
	// Stopping again is a new alert.
	ra.setSnap(stoppedBy(1032, "Could not find row"))
	ra.send()
	if as := r.alerts(rID, KindReplicaStopped); len(as) != 2 || as[1].ClosedAt != nil {
		t.Fatalf("alerts after a second stop = %+v", as)
	}
}

func TestStoppedOutsidePanelAlertsButPausedDoesNot(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.SQLRunning, s.Replica.IORunning = "No", "No" })
	ra.send()
	if v := r.view(rID); v.Status.Code != StatusStoppedOutside {
		t.Fatalf("status %q", v.Status.Code)
	}
	if a := r.openAlert(rID, KindReplicaStopped); !strings.Contains(a.Message, "tanpa lewat panel") {
		t.Fatalf("message = %q", a.Message)
	}
	// Stopped through the panel: Dihentikan closes it and opens nothing.
	if err := r.st.SetReplicaPaused(rID, true); err != nil {
		t.Fatal(err)
	}
	r.clk.Add(5 * time.Second)
	ra.send()
	if v := r.view(rID); v.Status.Code != StatusPaused {
		t.Fatalf("status %q, want Dihentikan", v.Status.Code)
	}
	for _, k := range []string{KindReplicaStopped, KindReplicaDisconnected} {
		if r.isOpen(rID, k) {
			t.Fatalf("%s open while Dihentikan", k)
		}
	}
}

func TestDisconnectedOpensAfter3Min(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.IORunning = "Connecting" })
	ra.send()
	t0 := r.clk.Now()

	r.clk.Set(t0.Add(3*time.Minute - time.Second))
	r.m.tick()
	if r.isOpen(rID, KindReplicaDisconnected) {
		t.Fatal("replica_disconnected open before 3 min")
	}
	r.clk.Set(t0.Add(3 * time.Minute))
	r.m.tick()
	a := r.openAlert(rID, KindReplicaDisconnected)
	if strings.Contains(a.Message, "detik") || !strings.Contains(a.Message, "3 menit") {
		t.Fatalf("message = %q", a.Message)
	}
	r.clk.Add(time.Minute)
	ra.setSnap(running)
	ra.send()
	if r.isOpen(rID, KindReplicaDisconnected) {
		t.Fatal("replica_disconnected still open with the IO thread running")
	}
}

func TestLaggingOpensAfter5MinClosesAfter1MinBelow(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	lag := func(sec int64) {
		ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = ptr(sec) })
		ra.send()
	}
	at := func(d time.Duration) { r.clk.Set(r.clk.Now().Add(d)); r.m.tick() }
	lag(120)
	at(5*time.Minute - time.Second)
	if r.isOpen(rID, KindReplicaLagging) {
		t.Fatal("replica_lagging open before 5 min")
	}
	at(time.Second)
	r.openAlert(rID, KindReplicaLagging)

	lag(0)
	at(30 * time.Second)
	lag(90) // lagging again: the minute starts over
	at(10 * time.Second)
	lag(0)
	at(59 * time.Second)
	if !r.isOpen(rID, KindReplicaLagging) {
		t.Fatal("replica_lagging closed before 1 min below the threshold")
	}
	at(time.Second)
	if r.isOpen(rID, KindReplicaLagging) {
		t.Fatal("replica_lagging still open after 1 min below the threshold")
	}
	if n := len(r.alerts(rID, KindReplicaLagging)); n != 1 {
		t.Fatalf("replica_lagging alerts = %d, want 1", n)
	}
}

func TestDiskHysteresis(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	usedPct := func(pct uint64) {
		r.clk.Add(15 * time.Second)
		ma.setSnap(func(s *proto.HealthSnapshot) { s.DiskTotal, s.DiskFree = 100<<30, (100-pct)<<30 })
		ma.send()
	}
	steps := []struct {
		pct        uint64
		warn, crit bool
	}{
		{86, true, false}, {84, true, false}, {80, true, false}, {79, false, false},
		// Critical replaces the warning: one condition, one alert.
		{86, true, false}, {96, false, true}, {91, false, true},
		// Critical closes under 90 %; still at or over 85 %: warning again.
		{89, true, false}, {79, false, false},
		{96, false, true}, {50, false, false},
	}
	for _, s := range steps {
		usedPct(s.pct)
		if w, c := r.isOpen(mID, KindDiskWarn), r.isOpen(mID, KindDiskCrit); w != s.warn || c != s.crit {
			t.Fatalf("at %d%%: disk_warn open %v, disk_crit open %v; want %v, %v", s.pct, w, c, s.warn, s.crit)
		}
	}
	if n := len(r.alerts(mID, KindDiskWarn)); n != 3 {
		t.Fatalf("disk_warn alerts = %d, want 3", n)
	}
	if a := r.alerts(mID, KindDiskCrit); len(a) != 2 || !strings.Contains(a[0].Message, "96%") {
		t.Fatalf("disk_crit alerts = %+v", a)
	}
}

func TestConnectionsHigh5Min(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	conns := func(n int) {
		ma.setSnap(func(s *proto.HealthSnapshot) { s.ThreadsConnected = n })
		ma.send()
	}
	at := func(d time.Duration) { r.clk.Add(d); r.m.tick() }

	conns(85)
	at(2 * time.Minute)
	conns(79) // a dip below 80% starts the five minutes over
	at(10 * time.Second)
	conns(85)
	t0 := r.clk.Now()
	at(5*time.Minute - time.Second)
	if r.isOpen(mID, KindConnectionsHigh) {
		t.Fatal("connections_high open before 5 min")
	}
	conns(90)
	at(time.Second)
	if !r.clk.Now().Equal(t0.Add(5 * time.Minute)) {
		t.Fatal("test clock off")
	}
	r.openAlert(mID, KindConnectionsHigh)
	conns(75)
	at(time.Minute)
	if !r.isOpen(mID, KindConnectionsHigh) {
		t.Fatal("connections_high closed at 75%")
	}
	conns(69)
	if r.isOpen(mID, KindConnectionsHigh) {
		t.Fatal("connections_high still open at 69%")
	}
}

// binlogRig is a master whose binlogs expire after expireSec and a
// replica disconnected from it at t0, both agents reporting every
// minute.
func binlogRig(t *testing.T, expireSec int64) (r *rig, rID int64, ma, ra *fakeAgent) {
	r = newRig(t)
	mID, topo := r.master("db-master")
	rID = r.replica("db-replica", topo)
	ms := masterSnap(srcUUID + ":1-100")
	ms.BinlogExpireSec = expireSec
	ma = r.agent(mID, true, ms).attach()
	ra = r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.IORunning = "Connecting" })
	ra.send()
	return r, rID, ma, ra
}

func (r *rig) minutes(n int, agents ...*fakeAgent) {
	for range n {
		r.clk.Add(time.Minute)
		for _, a := range agents {
			a.send()
		}
		r.m.tick()
	}
}

func TestBinlogDeadline80Percent(t *testing.T) {
	r, rID, ma, ra := binlogRig(t, 600) // 80% = 8 min
	t0 := r.clk.Now()
	r.minutes(3, ma, ra)
	// Disconnected, then stopped: still not following the master since t0.
	ra.setSnap(stoppedBy(1236, "Could not read binlog"))
	r.minutes(5, ma, ra)
	if r.isOpen(rID, KindBinlogDeadline) {
		t.Fatalf("binlog_deadline open at exactly 80%% (%v)", r.clk.Now().Sub(t0))
	}
	r.clk.Add(time.Second)
	r.m.tick()
	a := r.openAlert(rID, KindBinlogDeadline)
	if !strings.Contains(a.Message, "binlog") {
		t.Fatalf("message = %q", a.Message)
	}
	ra.setSnap(running)
	r.minutes(1, ma, ra)
	if r.isOpen(rID, KindBinlogDeadline) {
		t.Fatal("binlog_deadline still open with the replica Sinkron")
	}

	// Disconnected again for long, then resynced: closed by the state.
	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.IORunning = "Connecting" })
	r.minutes(10, ma, ra)
	r.openAlert(rID, KindBinlogDeadline)
	if err := r.st.SetReplicaState(rID, store.ReplicaResyncing, ""); err != nil {
		t.Fatal(err)
	}
	r.minutes(1, ma, ra)
	for _, k := range []string{KindBinlogDeadline, KindReplicaDisconnected} {
		if r.isOpen(rID, k) {
			t.Fatalf("%s still open while the replica resyncs", k)
		}
	}
}

func TestBinlogDeadlineNeverWhenExpireZero(t *testing.T) {
	r, rID, ma, ra := binlogRig(t, 0)
	r.minutes(120, ma, ra)
	if n := len(r.alerts(rID, KindBinlogDeadline)); n != 0 {
		t.Fatalf("binlog_deadline alerts = %d without binlog_expire_sec", n)
	}
	if !r.isOpen(rID, KindReplicaDisconnected) {
		t.Fatal("replica_disconnected not open after 2 h")
	}
}

// TestBacklogCollapsesAlerts: 200 backlog reports flipping between
// Berhenti and Sinkron open or close an alert at most once, by the end
// state of the batch.
func TestBacklogCollapsesAlerts(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	stopped := replicaSnap(srcUUID + ":1-100")
	stoppedBy(1062, "Duplicate entry")(&stopped)
	synced := replicaSnap(srcUUID + ":1-100")

	batch := func(final proto.HealthSnapshot) {
		ra.detach()
		r.clk.Add(2 * time.Hour)
		ra.mu.Lock()
		ra.backlog = nil
		for i := range 200 {
			snap := synced
			if i%2 == 0 {
				snap = stopped
			}
			ra.backlog = append(ra.backlog, proto.HealthEvent{Seq: uint64(i + 1), Backlog: true,
				TakenAgoMS: int64(200-i) * 30_000, Snapshot: snap})
		}
		ra.seq = 200
		ra.snap = final
		ra.mu.Unlock()
		ra.attach()
	}

	batch(stopped)
	as := r.alerts(rID, KindReplicaStopped)
	if len(as) != 1 || as[0].ClosedAt != nil {
		t.Fatalf("after a batch ending Berhenti: alerts = %+v, want one open", as)
	}
	if ups := r.alertUpdates(rID, KindReplicaStopped); len(ups) != 1 {
		t.Fatalf("alert_update published %d times, want 1", len(ups))
	}

	batch(synced)
	as = r.alerts(rID, KindReplicaStopped)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("after a batch ending Sinkron: alerts = %+v, want the one closed", as)
	}
	if ups := r.alertUpdates(rID, KindReplicaStopped); len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v, want open then closed", ups)
	}
	all, _ := r.st.ListAlerts(time.Time{}, 10000)
	for _, a := range all {
		if a.ServerID == rID && a.Kind != KindReplicaStopped {
			t.Fatalf("unexpected alert %+v", a)
		}
	}
}

func TestMessageNeverContainsRawError(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	ra.setSnap(stoppedBy(1062, "Duplicate entry 'Budi' for key 'PRIMARY'"))
	ra.send()
	a := r.openAlert(rID, KindReplicaStopped)
	want := "Replikasi berhenti karena error 1062. " + expDuplicate.Text + " Saran: Resync ulang."
	if a.Message != want {
		t.Fatalf("message = %q, want %q", a.Message, want)
	}
	// Another error while still stopped: the message follows it.
	r.clk.Add(15 * time.Second)
	ra.setSnap(stoppedBy(1032, "Can't find record in 'Budi'"))
	ra.send()
	if b := r.openAlert(rID, KindReplicaStopped); b.ID != a.ID || !strings.Contains(b.Message, "error 1032") ||
		!strings.Contains(b.Message, "Baris tidak ditemukan") {
		t.Fatalf("alert after errno change = %+v", b)
	}

	ra.setSnap(func(s *proto.HealthSnapshot) {
		*s = proto.HealthSnapshot{MySQLError: "Error 1045: Access denied for user 'Budi'@'10.0.0.9'"}
	})
	ra.send()
	r.clk.Add(30 * time.Second)
	r.m.tick()
	if a := r.openAlert(rID, KindMySQLUnreachable); a.Message == "" {
		t.Fatal("mysql_unreachable without a message")
	}
	all, _ := r.st.ListAlerts(time.Time{}, 10000)
	for _, a := range all {
		if strings.Contains(a.Message, "Budi") || strings.Contains(a.Message, "Duplicate") || strings.Contains(a.Message, "Access denied") {
			t.Fatalf("alert %s carries raw error text: %q", a.Kind, a.Message)
		}
	}
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	for _, p := range r.pubs {
		if b, _ := json.Marshal(p.Data); strings.Contains(string(b), "Budi") {
			t.Fatalf("%s carries raw error text: %s", p.Event, b)
		}
	}
}

func TestStatusAlertsOpenAndClose(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	step := func(a *fakeAgent, f func(s *proto.HealthSnapshot)) {
		r.clk.Add(10 * time.Second)
		a.setSnap(f)
		a.send()
	}

	step(ma, func(s *proto.HealthSnapshot) { s.SuperReadOnly = true })
	if a := r.openAlert(mID, KindMasterLocked); !strings.Contains(a.Message, "super_read_only") ||
		!strings.Contains(a.Message, "sudo dbpanel agent unlock-master") {
		t.Fatalf("master_locked message = %q", a.Message)
	}
	step(ma, func(s *proto.HealthSnapshot) { s.SuperReadOnly = false })
	if r.isOpen(mID, KindMasterLocked) {
		t.Fatal("master_locked open on a writable master")
	}

	step(ra, func(s *proto.HealthSnapshot) { s.SuperReadOnly = false })
	r.openAlert(rID, KindReplicaUnlocked)
	step(ra, func(s *proto.HealthSnapshot) { s.SuperReadOnly = true })
	if r.isOpen(rID, KindReplicaUnlocked) {
		t.Fatal("replica_unlocked open on a locked replica")
	}

	// MySQL down on the replica: open after 30 s, closed when it answers.
	step(ra, func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	r.clk.Add(29 * time.Second)
	r.m.tick()
	if r.isOpen(rID, KindMySQLUnreachable) {
		t.Fatal("mysql_unreachable open before 30 s")
	}
	r.clk.Add(time.Second)
	r.m.tick()
	r.openAlert(rID, KindMySQLUnreachable)
	step(ra, func(s *proto.HealthSnapshot) { s.MySQLOK = true })
	if r.isOpen(rID, KindMySQLUnreachable) {
		t.Fatal("mysql_unreachable open with MySQL answering")
	}

	// Agents gone: agent_offline for the replica, master_down for the master.
	ma.detach()
	ra.detach()
	r.clk.Add(31 * time.Second)
	r.m.tick()
	r.openAlert(rID, KindAgentOffline)
	if a := r.openAlert(mID, KindMasterDown); !strings.Contains(a.Message, "agent") {
		t.Fatalf("master_down message = %q", a.Message)
	}
	ma.attach()
	ra.attach()
	if r.isOpen(rID, KindAgentOffline) || r.isOpen(mID, KindMasterDown) {
		t.Fatal("alerts still open after the agents reconnected")
	}

	// A replica that is no longer ready keeps no status alert open, but
	// keeps its job_failed.
	step(ra, stoppedBy(1062, "dup"))
	r.openAlert(rID, KindReplicaStopped)
	r.m.JobFailed(rID, "job-7", "resync_replica", "Salin data", "boom")
	if err := r.st.SetReplicaState(rID, store.ReplicaResyncing, ""); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if r.isOpen(rID, KindReplicaStopped) {
		t.Fatal("replica_stopped open while the replica resyncs")
	}
	r.openAlert(rID, KindJobFailed)
	// It closed without recovering, and says why (no "pulih" for it);
	// a recovery has no reason.
	stops := r.alerts(rID, KindReplicaStopped)
	if last := stops[len(stops)-1]; last.CloseReason != "Resync ulang dimulai" {
		t.Fatalf("close reason of replica_stopped at resync = %q", last.CloseReason)
	}
	if un := r.alerts(rID, KindReplicaUnlocked); len(un) != 1 || un[0].CloseReason != "" {
		t.Fatalf("replica_unlocked after relocking = %+v, want closed without a reason", un)
	}

	// A replica that leaves the panel (Lepas) closes its alerts the same way.
	if err := r.st.SetReplicaState(rID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	r.settle()
	r.openAlert(rID, KindReplicaStopped)
	if err := r.st.RemoveReplica(rID); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	stops = r.alerts(rID, KindReplicaStopped)
	if last := stops[len(stops)-1]; last.ClosedAt == nil || last.CloseReason != "replika dilepas dari panel" {
		t.Fatalf("replica_stopped after Lepas = %+v", last)
	}
}

func TestJobFailedAlert(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }

	r.m.JobFailed(rID, "job-1", "create_replica", "Salin data", "Error 1062: Duplicate entry 'Budi'")
	a := r.openAlert(rID, KindJobFailed)
	if a.Message != "Pekerjaan Buat replika gagal pada tahap Salin data." || a.TopologyID != topo.ID {
		t.Fatalf("job_failed = %+v", a)
	}
	if ups := r.alertUpdates(rID, KindJobFailed); len(ups) != 1 || ups[0]["open"] != true {
		t.Fatalf("alert_update = %v", ups)
	}
	if changed.Load() != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", changed.Load())
	}

	// A second failure keeps one alert, now about the newer job.
	r.m.JobFailed(rID, "job-2", "add_databases", "", "boom")
	if as := r.alerts(rID, KindJobFailed); len(as) != 1 || as[0].Message != "Pekerjaan Tambah database gagal." {
		t.Fatalf("after a second failure = %+v", as)
	}
	// Any later job ending closes it, whichever job failed: here a new
	// job (job-3) succeeding after job-2 failed.
	r.m.JobEnded(rID, "job-3")
	if r.isOpen(rID, KindJobFailed) {
		t.Fatal("job_failed open after a later job ended")
	}
	if ups := r.alertUpdates(rID, KindJobFailed); len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v, want open then closed", ups)
	}
	r.m.JobEnded(rID, "job-4") // nothing open: no-op
	if changed.Load() != 2 {
		t.Fatalf("OnAlertsChanged called %d times, want 2", changed.Load())
	}
}

func TestSnoozeKeepsAlertsOpen(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	until := r.clk.Now().Add(2 * time.Hour)
	if err := r.m.Snooze(rID, &until); err != nil {
		t.Fatal(err)
	}
	if v := r.view(rID); v.SnoozeUntil == nil || !v.SnoozeUntil.Equal(until) {
		t.Fatalf("view SnoozeUntil = %v", v.SnoozeUntil)
	}
	if srv, _ := r.st.GetServer(rID); srv.SnoozeUntil == nil || !srv.SnoozeUntil.Equal(until) {
		t.Fatalf("stored snooze = %v", srv.SnoozeUntil)
	}
	ra.setSnap(stoppedBy(1062, "dup"))
	ra.send()
	r.openAlert(rID, KindReplicaStopped)
	if err := r.m.Snooze(rID, nil); err != nil {
		t.Fatal(err)
	}
	if v := r.view(rID); v.SnoozeUntil != nil {
		t.Fatalf("view SnoozeUntil after clear = %v", v.SnoozeUntil)
	}
	if err := r.m.Snooze(9999, &until); err == nil {
		t.Fatal("Snooze of an unknown server succeeded")
	}
}

// TestAgentOfflineAlertStableUntilReport: a reconnected agent whose first
// report has not arrived shows Menunggu laporan pertama (spec 5.12), but
// its agent_offline stays open: other servers' reports and the ticks
// neither close nor reopen it; its own first report closes it once.
func TestAgentOfflineAlertStableUntilReport(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.detach()
	r.clk.Add(31 * time.Second)
	ma.send()
	r.m.tick()
	first := r.openAlert(rID, KindAgentOffline)

	r.m.OnOnline(rID) // connected again, no report yet
	if v := r.view(rID); v.Status.Code != StatusAwaitingReport {
		t.Fatalf("status after reconnecting = %q, want %q", v.Status.Code, StatusAwaitingReport)
	}
	for range 2 {
		r.clk.Add(time.Second)
		ma.send()
	}
	r.m.tick()
	as := r.alerts(rID, KindAgentOffline)
	if len(as) != 1 || as[0].ID != first.ID || as[0].ClosedAt != nil {
		t.Fatalf("agent_offline alerts after other reports = %+v, want the first still open", as)
	}
	if ups := r.alertUpdates(rID, KindAgentOffline); len(ups) != 1 {
		t.Fatalf("alert_update = %v, want only the opening", ups)
	}
	// Its own report closes it, once.
	ra.send()
	as = r.alerts(rID, KindAgentOffline)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("agent_offline alerts after the report = %+v, want the one closed", as)
	}
	ra.send()
	r.m.tick()
	if n := len(r.alerts(rID, KindAgentOffline)); n != 1 {
		t.Fatalf("agent_offline alerts = %d, want 1", n)
	}
	if ups := r.alertUpdates(rID, KindAgentOffline); len(ups) != 2 {
		t.Fatalf("alert_update = %v, want the opening and one closing", ups)
	}
}

// TestAgentOfflineAlertOneThroughReconnectLoop: an agent that keeps
// dropping, staying away past the grace, and connecting again without a
// report has one agent_offline open the whole time, never closed and
// reopened.
func TestAgentOfflineAlertOneThroughReconnectLoop(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.detach()
	r.clk.Add(31 * time.Second)
	r.m.tick()
	first := r.openAlert(rID, KindAgentOffline)
	for range 5 {
		r.m.OnOnline(rID)
		for range 3 {
			r.clk.Add(5 * time.Second)
			r.m.tick()
		}
		r.m.OnOffline(rID)
		r.clk.Add(31 * time.Second)
		r.m.tick()
	}
	as := r.alerts(rID, KindAgentOffline)
	if len(as) != 1 || as[0].ID != first.ID || as[0].ClosedAt != nil {
		t.Fatalf("agent_offline alerts through the loop = %+v, want the first still open", as)
	}
	if ups := r.alertUpdates(rID, KindAgentOffline); len(ups) != 1 {
		t.Fatalf("alert_update = %v, want only the opening", ups)
	}
}

func TestTLSExpiringAlert(t *testing.T) {
	r := newRig(t)
	id := r.server("db-utama")
	other := r.server("db-lain")
	now := r.clk.Now()
	notAfter := now.Add(14*24*time.Hour + time.Hour)
	set := func(serverID int64, na time.Time) {
		t.Helper()
		if err := r.st.SetServerTLS(store.ServerTLS{ServerID: serverID, CertSerial: "01", NotAfter: na, InstalledAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	set(id, notAfter)
	set(other, now.AddDate(1, 0, 0))

	// 14 days and an hour left: nothing yet.
	r.m.CheckTLSExpiry(now)
	if r.isOpen(id, KindTLSExpiring) {
		t.Fatal("tls_expiring open 14 days before the expiry")
	}
	// 13 days left: open, as a warning naming the server and the days.
	r.clk.Set(notAfter.Add(-13*24*time.Hour - time.Minute))
	r.m.CheckTLSExpiry(r.clk.Now())
	a := r.openAlert(id, KindTLSExpiring)
	if a.Message != "Sertifikat MySQL db-utama kedaluwarsa dalam 13 hari." {
		t.Fatalf("message = %q", a.Message)
	}
	if r.isOpen(other, KindTLSExpiring) {
		t.Fatal("tls_expiring open for a certificate valid for a year")
	}
	if ups := r.alertUpdates(id, KindTLSExpiring); len(ups) != 1 || ups[0]["open"] != true {
		t.Fatalf("alert_update = %v", ups)
	}
	// A day later the same alert says 12 days.
	r.clk.Add(24 * time.Hour)
	r.m.CheckTLSExpiry(r.clk.Now())
	if as := r.alerts(id, KindTLSExpiring); len(as) != 1 || as[0].Message != "Sertifikat MySQL db-utama kedaluwarsa dalam 12 hari." {
		t.Fatalf("after a day = %+v", as)
	}
	// Renewed (SetServerTLS with the new certificate): closed.
	set(id, r.clk.Now().AddDate(2, 0, 0))
	r.m.CheckTLSExpiry(r.clk.Now())
	if r.isOpen(id, KindTLSExpiring) {
		t.Fatal("tls_expiring still open after the renewal")
	}
	if ups := r.alertUpdates(id, KindTLSExpiring); len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v, want open then closed", ups)
	}
	// The tick checks too.
	set(other, r.clk.Now().Add(2*24*time.Hour))
	r.m.tick()
	if a := r.openAlert(other, KindTLSExpiring); a.Message != "Sertifikat MySQL db-lain kedaluwarsa dalam 2 hari." {
		t.Fatalf("message = %q", a.Message)
	}
	// Expired.
	r.clk.Add(3 * 24 * time.Hour)
	r.m.CheckTLSExpiry(r.clk.Now())
	if a := r.openAlert(other, KindTLSExpiring); a.Message != "Sertifikat MySQL db-lain sudah kedaluwarsa." {
		t.Fatalf("message = %q", a.Message)
	}
}

// TestRedirectWindowOpensNoStoppedAlert: Perketat marks the replica
// paused around its STOP/START; a report taken in between reads
// Dihentikan and opens nothing, and once unpaused and running again the
// replica is Sinkron.
func TestRedirectWindowOpensNoStoppedAlert(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.send()

	if err := r.st.SetReplicaPaused(rID, true); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	r.settle()
	ra.setSnap(func(s *proto.HealthSnapshot) { s.Replica.SQLRunning, s.Replica.IORunning = "No", "No" })
	r.clk.Add(5 * time.Second)
	ra.send()
	if v := r.view(rID); v.Status.Code != StatusPaused {
		t.Fatalf("status during the redirect %q, want Dihentikan", v.Status.Code)
	}
	if r.isOpen(rID, KindReplicaStopped) || len(r.alerts(rID, KindReplicaStopped)) != 0 {
		t.Fatal("replica_stopped opened during the redirect")
	}

	// START REPLICA first, then the paused mark is cleared.
	r.clk.Add(time.Second)
	ra.setSnap(running)
	if err := r.st.SetReplicaPaused(rID, false); err != nil {
		t.Fatal(err)
	}
	r.m.ReplicaChanged(rID)
	r.settle()
	r.clk.Add(5 * time.Second)
	ra.send()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status after START %q, want Sinkron", v.Status.Code)
	}
	if len(r.alerts(rID, KindReplicaStopped)) != 0 {
		t.Fatalf("replica_stopped opened: %+v", r.alerts(rID, KindReplicaStopped))
	}
}

// TestWebCertExpiringAlert: the panel's HTTPS certificate in use (an
// uploaded one, typically) expiring within 14 days opens web_tls_expiring
// on the panel's host, beside that host's own MySQL certificate alert (I3).
func TestWebCertExpiringAlert(t *testing.T) {
	r := newRig(t)
	local := r.server("panel-host")
	now := r.clk.Now()
	webNotAfter := now.Add(10 * 24 * time.Hour)
	r.m.webCert = func() (int64, time.Time, bool) { return local, webNotAfter, true }
	if err := r.st.SetServerTLS(store.ServerTLS{ServerID: local, CertSerial: "01", NotAfter: now.Add(5 * 24 * time.Hour),
		InstalledAt: now}); err != nil {
		t.Fatal(err)
	}
	r.m.CheckTLSExpiry(now)
	if a := r.openAlert(local, KindWebTLSExpiring); a.Message != "Sertifikat keamanan panel kedaluwarsa dalam 10 hari." {
		t.Fatalf("web message = %q", a.Message)
	}
	if a := r.openAlert(local, KindTLSExpiring); a.Message != "Sertifikat MySQL panel-host kedaluwarsa dalam 5 hari." {
		t.Fatalf("mysql message = %q", a.Message)
	}
	r.clk.Add(24 * time.Hour)
	r.m.CheckTLSExpiry(r.clk.Now())
	if a := r.openAlert(local, KindWebTLSExpiring); a.Message != "Sertifikat keamanan panel kedaluwarsa dalam 9 hari." {
		t.Fatalf("web message a day later = %q", a.Message)
	}
	// A new certificate in use closes the web alert only.
	webNotAfter = r.clk.Now().AddDate(1, 0, 0)
	r.m.CheckTLSExpiry(r.clk.Now())
	if r.isOpen(local, KindWebTLSExpiring) || !r.isOpen(local, KindTLSExpiring) {
		t.Fatal("after the new web certificate: want only the MySQL alert open")
	}
}

// promoted swaps the topology's master to newID at 10.0.0.2:3306, as a
// finished promotion does, and records the promotion with status.
func (r *rig) promoted(topo store.Topology, oldID, newID int64, status, replicasJSON string) int64 {
	r.t.Helper()
	if err := r.st.SwapMaster(topo.ID, oldID, newID, "10.0.0.2", 3306); err != nil {
		r.t.Fatal(err)
	}
	return r.promotion(topo, oldID, newID, status, replicasJSON)
}

func (r *rig) promotion(topo store.Topology, oldID, newID int64, status, replicasJSON string) int64 {
	r.t.Helper()
	id, err := r.st.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: "planned", OldMasterServerID: oldID,
		NewMasterServerID: newID, StartedAt: r.clk.Now(), Status: status, ReplicasJSON: replicasJSON})
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *rig) setPromotionStatus(id int64, status string) {
	r.t.Helper()
	p, err := r.st.GetPromotion(id)
	if err != nil {
		r.t.Fatal(err)
	}
	p.Status = status
	if err := r.st.UpdatePromotion(p); err != nil {
		r.t.Fatal(err)
	}
}

func TestPromoteCutoverAlertLifecycle(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	oID := r.replica("db-lain", topo)
	r.m.tick()
	if r.isOpen(rID, KindPromoteCutover) {
		t.Fatal("promote_cutover open before any promotion")
	}

	if err := r.st.SetReplicaState(oID, store.ReplicaNeedsRepoint, ""); err != nil {
		t.Fatal(err)
	}
	r.promoted(topo, mID, rID, "done", `[{"server_id":`+itoa(mID)+`,"role":"old_master","state":"quarantined"},`+
		`{"server_id":`+itoa(oID)+`,"role":"replica","state":"needs_repoint"}]`)
	r.m.tick()
	a := r.openAlert(rID, KindPromoteCutover)
	if a.TopologyID != topo.ID {
		t.Fatalf("promote_cutover topology = %d, want %d", a.TopologyID, topo.ID)
	}
	for _, want := range []string{"db-baru", "db-lama", "10.0.0.2:3306", "Sudah saya alihkan", "perlu diarahkan ulang: 1"} {
		if !strings.Contains(a.Message, want) {
			t.Errorf("promote_cutover message %q lacks %q", a.Message, want)
		}
	}

	// It stays one open alert for as long as the cutover waits: the
	// notifier repeats it every 6 hours (alert_repeat_hours).
	for range 14 {
		r.clk.Add(30 * time.Minute)
		r.m.tick()
	}
	if as := r.alerts(rID, KindPromoteCutover); len(as) != 1 || as[0].ClosedAt != nil {
		t.Fatalf("promote_cutover after 7 h = %+v, want the same one open", as)
	}

	// "Sudah saya alihkan" (ConfirmCutover) clears cutover_pending: the
	// alert closes as resolved.
	if err := r.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	as := r.alerts(rID, KindPromoteCutover)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("promote_cutover after confirm = %+v, want closed as resolved", as)
	}
	if ups := r.alertUpdates(rID, KindPromoteCutover); len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v", ups)
	}
	r.m.tick()
	if len(r.alerts(rID, KindPromoteCutover)) != 1 {
		t.Fatal("promote_cutover reopened after confirm")
	}
}

func TestPromotePartialAlert(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)

	// Partial before the swap: the alert belongs to the candidate.
	pid := r.promotion(topo, mID, rID, "partial", "")
	r.m.tick()
	a := r.openAlert(rID, KindPromotePartial)
	if a.TopologyID != topo.ID || !strings.Contains(a.Message, "sebagian") || !strings.Contains(a.Message, "Ulangi") {
		t.Fatalf("promote_partial = %+v", a)
	}

	// A retry runs it again: the alert stays open, through the swap.
	r.setPromotionStatus(pid, "running")
	if err := r.st.SwapMaster(topo.ID, mID, rID, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	r.setPromotionStatus(pid, "partial")
	r.m.tick()
	if as := r.alerts(rID, KindPromotePartial); len(as) != 1 || as[0].ClosedAt != nil {
		t.Fatalf("promote_partial during the retry = %+v, want the same one open", as)
	}

	// Every remaining step done: resolved.
	r.setPromotionStatus(pid, "done")
	r.m.tick()
	as := r.alerts(rID, KindPromotePartial)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("promote_partial after done = %+v, want closed as resolved", as)
	}

	// A newer promotion replaces a partial one: closed, not resolved.
	pid = r.promotion(topo, rID, mID, "partial", "")
	r.m.tick()
	r.openAlert(mID, KindPromotePartial)
	r.promotion(topo, rID, mID, "failed", "")
	r.m.tick()
	as = r.alerts(mID, KindPromotePartial)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason == "" {
		t.Fatalf("promote_partial after a newer promotion = %+v, want closed with a reason", as)
	}
}

func lockExpired(t *testing.T, action, reason string) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.LockExpiredEvent{Action: action, Candidate: "10.0.0.2:3306", Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventLockExpired, Payload: b}
}

func TestMasterLockExpiredFromEvent(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }

	if !r.m.HandleEvent(mID, lockExpired(t, "kept", "kandidat tidak terjangkau: Access denied for user 'x'@'h'")) {
		t.Fatal("HandleEvent did not take monitor.lockExpired")
	}
	a := r.openAlert(mID, KindMasterLockExpired)
	if a.TopologyID != topo.ID || !strings.Contains(a.Message, "tetap dikunci") || !strings.Contains(a.Message, "10.0.0.2:3306") ||
		strings.Contains(a.Message, "Access denied") {
		t.Fatalf("kept alert = %+v", a)
	}
	if changed.Load() != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", changed.Load())
	}

	// The agent repeats "kept" hourly: still the one alert.
	r.clk.Add(time.Hour)
	r.m.HandleEvent(mID, lockExpired(t, "kept", "kandidat tidak terjangkau"))
	if as := r.alerts(mID, KindMasterLockExpired); len(as) != 1 {
		t.Fatalf("alerts after a second kept = %+v", as)
	}

	// The guard unlocked the master: the message says so, as a new alert.
	r.m.HandleEvent(mID, lockExpired(t, "unlocked", ""))
	as := r.alerts(mID, KindMasterLockExpired)
	if len(as) != 2 || as[0].ClosedAt == nil || as[0].CloseReason == "" || as[1].ClosedAt != nil ||
		!strings.Contains(as[1].Message, "membuka kunci") {
		t.Fatalf("alerts after unlocked = %+v", as)
	}

	// It closes on its own after 24 hours.
	r.clk.Add(23 * time.Hour)
	r.m.tick()
	if !r.isOpen(mID, KindMasterLockExpired) {
		t.Fatal("master_lock_expired closed before 24 h")
	}
	r.clk.Add(time.Hour)
	r.m.tick()
	as = r.alerts(mID, KindMasterLockExpired)
	if len(as) != 2 || as[1].ClosedAt == nil || as[1].CloseReason == "" {
		t.Fatalf("alerts after 24 h = %+v", as)
	}

	// An unreadable payload is taken, never passed on, and opens the
	// generic kept notice (SP-9: not judging it must not mean silence).
	if !r.m.HandleEvent(mID, proto.Event{Type: proto.EventLockExpired, Payload: []byte("{")}) {
		t.Fatal("unreadable monitor.lockExpired not taken")
	}
	if as := r.alerts(mID, KindMasterLockExpired); len(as) != 3 || as[2].ClosedAt != nil || !strings.HasPrefix(as[2].Message, lockKeptPrefix) {
		t.Fatalf("alerts after an unreadable payload = %+v, want the generic kept notice open", as)
	}
}

func TestMasterQuarantinedFromEvent(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	r.promoted(topo, mID, rID, "done", "")
	r.m.tick()

	ev := proto.Event{Type: proto.EventQuarantined, Payload: []byte(`{"new_source":"10.0.0.2:3306"}`)}
	if !r.m.HandleEvent(mID, ev) {
		t.Fatal("HandleEvent did not take monitor.quarantined")
	}
	a := r.openAlert(mID, KindMasterQuarantined)
	if a.TopologyID != topo.ID || !strings.Contains(a.Message, "dikarantina") || !strings.Contains(a.Message, "10.0.0.2:3306") {
		t.Fatalf("master_quarantined = %+v", a)
	}
	// Once per 24 hours.
	r.clk.Add(time.Hour)
	r.m.HandleEvent(mID, ev)
	if as := r.alerts(mID, KindMasterQuarantined); len(as) != 1 {
		t.Fatalf("alerts after a second event = %+v", as)
	}
	r.clk.Add(23 * time.Hour)
	r.m.tick()
	as := r.alerts(mID, KindMasterQuarantined)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("master_quarantined after 24 h = %+v", as)
	}
}

func TestReplicaNeedsAlerts(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.setSnap(stoppedBy(1062, "Duplicate entry"))
	ra.send()
	if !r.isOpen(rID, KindReplicaStopped) {
		t.Fatal("replica_stopped not open")
	}
	state := func(s string) {
		t.Helper()
		if err := r.st.SetReplicaState(rID, s, ""); err != nil {
			t.Fatal(err)
		}
		r.m.ReplicaChanged(rID)
		r.settle()
	}

	state(store.ReplicaNeedsRepoint)
	if v := r.view(rID); v.Status.Code != StatusNeedsRepoint {
		t.Fatalf("status = %+v", v.Status)
	}
	a := r.openAlert(rID, KindReplicaNeedsRepoint)
	if !strings.Contains(a.Message, "diarahkan ulang") {
		t.Fatalf("replica_needs_repoint = %+v", a)
	}
	// What the replica did under the old master is no longer judged.
	if as := r.alerts(rID, KindReplicaStopped); len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason == "" {
		t.Fatalf("replica_stopped = %+v, want closed with a reason", as)
	}

	// The repoint found transactions the new master lacks.
	state(store.ReplicaNeedsRebuild)
	as := r.alerts(rID, KindReplicaNeedsRepoint)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason == "" {
		t.Fatalf("replica_needs_repoint = %+v, want closed with a reason", as)
	}
	r.openAlert(rID, KindReplicaNeedsRebuild)

	// It stays open through the resync, also a failed one, and closes as
	// resolved when the replica is ready again.
	state(store.ReplicaResyncing)
	r.m.tick()
	state(store.ReplicaFailed)
	r.m.tick()
	state(store.ReplicaResyncing)
	ra.setSnap(running)
	state(store.ReplicaReady)
	ra.send()
	as = r.alerts(rID, KindReplicaNeedsRebuild)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("replica_needs_rebuild = %+v, want one closed as resolved", as)
	}
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status after the resync = %+v", v.Status)
	}
}

func TestPromoteCutoverClosedBySecondPromotion(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-a")
	rID := r.replica("db-b", topo)
	r.promoted(topo, mID, rID, "done", `[{"server_id":`+itoa(mID)+`,"role":"old_master","state":"quarantine_pending"}]`)
	r.m.tick()
	if a := r.openAlert(rID, KindPromoteCutover); !strings.Contains(a.Message, "Master lama belum dikarantina; jangan hidupkan") {
		t.Fatalf("cutover message lacks the pending quarantine warning: %q", a.Message)
	}

	// The old master comes back as a replica and is promoted again before
	// anyone confirmed the first cutover.
	if err := r.st.SetServerRole(mID, store.RoleUnassigned); err != nil {
		t.Fatal(err)
	}
	topo, _ = r.st.GetTopology(topo.ID)
	if _, err := r.st.AddReplica(topo.ID, mID, nil); err != nil {
		t.Fatal(err)
	}
	r.promoted(topo, rID, mID, "done", "")
	r.m.tick()
	as := r.alerts(rID, KindPromoteCutover)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "master berganti oleh promote lain" {
		t.Fatalf("first promote_cutover = %+v, want closed by the second promotion", as)
	}
	if a := r.openAlert(mID, KindPromoteCutover); strings.Contains(a.Message, "belum dikarantina") {
		t.Fatalf("second promote_cutover = %q", a.Message)
	}
}

func TestPromotePartialMessageSaysWhatStopped(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	oID := r.replica("db-lain", topo)

	// Stopped before the swap: the master is still locked.
	pid := r.promotion(topo, mID, rID, "partial", "")
	r.m.tick()
	msg := r.openAlert(rID, KindPromotePartial).Message
	for _, want := range []string{"masih dikunci", "aplikasi tidak bisa menulis", `"Ulangi dari tahap ini"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("before the swap: %q lacks %q", msg, want)
		}
	}

	// An emergency promote stopped before the swap took no lock.
	p, _ := r.st.GetPromotion(pid)
	p.Mode = "emergency"
	if err := r.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	msg = r.openAlert(rID, KindPromotePartial).Message
	if strings.Contains(msg, "dikunci") || !strings.Contains(msg, "aplikasi tidak bisa menulis") ||
		!strings.Contains(msg, `"Ulangi dari tahap ini"`) {
		t.Errorf("emergency before the swap: %q", msg)
	}
	p.Mode = "planned"
	if err := r.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}

	// Swapped, the old master not quarantined yet.
	if err := r.st.SwapMaster(topo.ID, mID, rID, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	p, _ = r.st.GetPromotion(pid)
	p.ReplicasJSON = `[{"server_id":` + itoa(mID) + `,"role":"old_master","state":"quarantine_pending"},` +
		`{"server_id":` + itoa(oID) + `,"role":"replica","state":"needs_repoint"}]`
	if err := r.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	msg = r.openAlert(rID, KindPromotePartial).Message
	if !strings.Contains(msg, "belum dikarantina") || !strings.Contains(msg, "jangan hidupkan") || strings.Contains(msg, "masih dikunci") {
		t.Errorf("pending quarantine: %q", msg)
	}

	// Quarantined; a replica still to repoint.
	p.ReplicasJSON = strings.Replace(p.ReplicasJSON, "quarantine_pending", "quarantined", 1)
	if err := r.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	msg = r.openAlert(rID, KindPromotePartial).Message
	if !strings.Contains(msg, "belum diarahkan ulang: 1") || !strings.Contains(msg, `"Ulangi dari tahap ini"`) {
		t.Errorf("repoint left: %q", msg)
	}
	if as := r.alerts(rID, KindPromotePartial); len(as) != 1 {
		t.Fatalf("promote_partial alerts = %+v, want one", as)
	}
}

func TestLockKeptClosesWhenOver(t *testing.T) {
	quarantinedEv := proto.Event{Type: proto.EventQuarantined, Payload: []byte(`{"new_source":"10.0.0.2:3306"}`)}

	// The guard quarantines the master itself.
	r := newRig(t)
	mID, _ := r.master("db-master")
	r.m.HandleEvent(mID, lockExpired(t, "kept", ""))
	if a := r.openAlert(mID, KindMasterLockExpired); !strings.Contains(a.Message, `"Buka kunci master"`) {
		t.Fatalf("kept advice = %q", a.Message)
	}
	r.m.HandleEvent(mID, quarantinedEv)
	as := r.alerts(mID, KindMasterLockExpired)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "master sudah dikarantina sebagai master lama" {
		t.Fatalf("kept after quarantine = %+v, want closed as quarantined", as)
	}

	// Unlocked from the panel: the next report shows the master writable.
	r = newRig(t)
	mID, _ = r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.SuperReadOnly, s.ReadOnly = true, true })
	ma.send()
	r.clk.Add(time.Second)
	r.m.HandleEvent(mID, lockExpired(t, "kept", ""))
	r.clk.Add(time.Second)
	ma.send()
	r.m.tick()
	if !r.isOpen(mID, KindMasterLockExpired) {
		t.Fatal("kept closed while the master is still locked")
	}
	ma.setSnap(func(s *proto.HealthSnapshot) { s.SuperReadOnly, s.ReadOnly = false, false })
	r.clk.Add(time.Second)
	ma.send()
	r.m.tick()
	as = r.alerts(mID, KindMasterLockExpired)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "master sudah bisa ditulisi lagi" {
		t.Fatalf("kept after unlock = %+v", as)
	}

	// Ulangi finished the promote: the server is now the old master.
	r = newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	r.m.HandleEvent(mID, lockExpired(t, "kept", ""))
	r.promoted(topo, mID, rID, "done", `[{"server_id":`+itoa(mID)+`,"role":"old_master","state":"quarantine_pending"}]`)
	r.m.tick()
	as = r.alerts(mID, KindMasterLockExpired)
	// Its quarantine is still pending: the reason does not claim it.
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "promote selesai; server kini master lama" {
		t.Fatalf("kept after the promote finished = %+v", as)
	}
}

// TestMasterQuarantinedFromStore: a planned promote quarantines through
// the panel's own mysql.quarantine, which sends no event: the store's
// record opens master_quarantined, once.
func TestMasterQuarantinedFromStore(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	r.promoted(topo, mID, rID, "done", `[{"server_id":`+itoa(mID)+`,"role":"old_master","state":"quarantined"}]`)
	r.m.tick()
	a := r.openAlert(mID, KindMasterQuarantined)
	if a.TopologyID != topo.ID || !strings.Contains(a.Message, "10.0.0.2:3306") {
		t.Fatalf("master_quarantined = %+v", a)
	}
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantined, Payload: []byte(`{"new_source":"db-baru:3306"}`)})
	for range 3 {
		r.clk.Add(10 * time.Hour)
		r.m.tick()
	}
	as := r.alerts(mID, KindMasterQuarantined)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("master_quarantined after the event and 30 h = %+v, want the one, closed", as)
	}

	// A pending quarantine opens nothing until it is delivered.
	r2 := newRig(t)
	mID, topo = r2.master("db-lama")
	rID = r2.replica("db-baru", topo)
	pid := r2.promoted(topo, mID, rID, "done", `[{"server_id":`+itoa(mID)+`,"role":"old_master","state":"quarantine_pending"}]`)
	r2.m.tick()
	if r2.isOpen(mID, KindMasterQuarantined) {
		t.Fatal("master_quarantined open while the quarantine is pending")
	}
	p, _ := r2.st.GetPromotion(pid)
	p.ReplicasJSON = strings.Replace(p.ReplicasJSON, "quarantine_pending", "quarantined", 1)
	if err := r2.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	r2.m.tick()
	r2.openAlert(mID, KindMasterQuarantined)
}

func rolePin(role string) func(s *proto.HealthSnapshot) {
	return func(s *proto.HealthSnapshot) { s.RolePin, s.PinError = role, "" }
}

// TestMasterUnpinnedAlertAfterThreeReports: a master whose agent reports
// a role pin other than master for more than two reports in a row gets
// master_unpinned (a warning, advising to prepare the master again); a
// report with the master pin closes it. Replicas, old masters and agents
// that report no pins never get it.
func TestMasterUnpinnedAlertAfterThreeReports(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	unpinned := masterSnap(srcUUID + ":1-100")
	unpinned.RolePin = "unassigned"
	ma := r.agent(mID, true, unpinned).attach() // report 1: monitor.configure's answer
	rsnap := replicaSnap(srcUUID + ":1-100")
	rsnap.RolePin, rsnap.SourcePin = "replica", "10.0.0.1:3306"
	ra := r.agent(rID, true, rsnap).attach()

	ma.send() // report 2
	for range 3 {
		r.clk.Add(time.Minute)
		r.m.tick() // ticks are no reports
	}
	if r.isOpen(mID, KindMasterUnpinned) {
		t.Fatal("master_unpinned open after two reports")
	}
	ma.send() // report 3
	a := r.openAlert(mID, KindMasterUnpinned)
	for _, want := range []string{"tidak dipin", "belum ada", "jalankan ulang Siapkan master"} {
		if !strings.Contains(a.Message, want) {
			t.Errorf("message %q lacks %q", a.Message, want)
		}
	}
	if a.TopologyID != topo.ID {
		t.Errorf("alert topology = %d, want %d", a.TopologyID, topo.ID)
	}

	ma.setSnap(rolePin("master"))
	ma.send()
	if as := r.alerts(mID, KindMasterUnpinned); len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("after the master pin: %+v, want the alert closed as recovered", as)
	}
	// The master pin starts the count again.
	ma.setSnap(rolePin("replica"))
	ma.send()
	ma.send()
	if r.isOpen(mID, KindMasterUnpinned) {
		t.Fatal("master_unpinned open after two reports following the master pin")
	}
	ma.send()
	if a := r.openAlert(mID, KindMasterUnpinned); !strings.Contains(a.Message, "replica") ||
		!strings.Contains(a.Message, "sudo dbpanel agent set-role master") {
		t.Errorf("message %q does not name the pinned role and the local command", a.Message)
	}
	ma.setSnap(rolePin("master"))
	ma.send()

	// A pin file the agent cannot trust counts as no master pin.
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin, s.PinError = "", "role harus milik root" })
	for range 3 {
		ma.send()
	}
	a = r.openAlert(mID, KindMasterUnpinned)
	if !strings.Contains(a.Message, "berkas pin") || strings.Contains(a.Message, "milik root") {
		t.Errorf("message %q: want the unreadable pin, without the agent's text", a.Message)
	}
	ma.setSnap(rolePin("master"))
	ma.send()

	// Backlog reports (kept while the panel was away) do not count; the
	// live ones after them do.
	ma.setSnap(rolePin("unassigned"))
	for range 5 {
		he := ma.report()
		he.Backlog = true
		ma.emit(he)
	}
	ma.send()
	r.m.tick()
	if r.isOpen(mID, KindMasterUnpinned) {
		t.Fatal("master_unpinned opened by backlog reports")
	}
	ma.send()
	ma.send()
	if a := r.openAlert(mID, KindMasterUnpinned); strings.Contains(a.Message, "set-role") {
		t.Errorf("message %q: an unassigned pin needs only Siapkan master", a.Message)
	}
	ma.setSnap(rolePin("master"))
	ma.send()

	// An agent that reports no pins at all (too old) is not judged.
	ma.setSnap(rolePin(""))
	for range 5 {
		ma.send()
	}
	if r.isOpen(mID, KindMasterUnpinned) {
		t.Fatal("master_unpinned open for an agent that reports no pins")
	}

	// A replica is pinned replica: never master_unpinned.
	for range 5 {
		ra.send()
	}
	if len(r.alerts(rID, KindMasterUnpinned)) != 0 {
		t.Fatal("master_unpinned for a replica")
	}

	// An old master after a promotion is pinned quarantined: its open
	// master_unpinned closes and none opens again.
	ma.setSnap(rolePin("unassigned"))
	for range 3 {
		ma.send()
	}
	r.openAlert(mID, KindMasterUnpinned)
	if err := r.st.SwapMaster(topo.ID, mID, rID, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	ma.setSnap(rolePin("quarantined"))
	for range 5 {
		ma.send()
	}
	r.m.tick()
	if r.view(mID).Role != store.RoleQuarantined {
		t.Fatalf("old master role = %q", r.view(mID).Role)
	}
	as := r.alerts(mID, KindMasterUnpinned)
	if last := as[len(as)-1]; last.ClosedAt == nil || r.isOpen(mID, KindMasterUnpinned) {
		t.Fatalf("old master: master_unpinned %+v, want closed and none open", as)
	}
	// The new master was a replica a moment ago: its replica reports do
	// not count towards it.
	ra.send()
	ra.send()
	if r.isOpen(rID, KindMasterUnpinned) {
		t.Fatal("the new master's reports as a replica counted towards master_unpinned")
	}
}

// The Indonesian texts of plan 5a (spec 5.2), letter for letter.
const (
	wantStartUnguardedText = "master lama belum terjamin terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung"
	wantStartLateText      = "master lama sempat bisa ditulis saat menyala; jalankan Periksa data tertinggal"
	wantStartBypassedText  = "kunci start tidak dipakai MySQL; periksa opsi layanan"
	wantStartBlockedText   = "MySQL master lama gagal menyala karena berkas kunci tidak terbaca"
)

// quarSnap is the report of an old master's agent: pinned quarantined,
// with the start lock sl (nil: an agent too old to report one).
func quarSnap(sl *proto.StartLockState) proto.HealthSnapshot {
	s := masterSnap(srcUUID + ":1-100")
	s.SuperReadOnly, s.ReadOnly = true, true
	s.RolePin, s.StartLock = "quarantined", sl
	return s
}

// quarantined makes db-lama the old master of a promotion that is done
// and attaches its agent, reporting snap.
func (r *rig) quarantined(snap proto.HealthSnapshot) (mID int64, pid int64, ma *fakeAgent) {
	r.t.Helper()
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	pid = r.promoted(topo, mID, rID, "done", "")
	r.m.tick()
	if r.view(mID).Role != store.RoleQuarantined {
		r.t.Fatalf("db-lama role = %q, want quarantined", r.view(mID).Role)
	}
	ma = r.agent(mID, true, snap).attach()
	return mID, pid, ma
}

func startEvent(t *testing.T, result string, bypassed bool) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.QuarantineStartEvent{Result: result, Bypassed: bypassed, InitFile: "/etc/mysql/x.sql"})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventQuarantineStart, Payload: b}
}

func startFailedEvent(t *testing.T, file string) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.QuarantineStartFailedEvent{File: file, At: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventQuarantineStartFailed, Payload: b}
}

func keptEvent(t *testing.T, hosts ...string) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.LockExpiredEvent{Action: "kept", Candidate: "10.0.0.2:3306", Hosts: hosts})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventLockExpired, Payload: b}
}

// closedAs reports whether the only alert of kind is closed with a reason.
func closedWithReason(t *testing.T, as []store.Alert) {
	t.Helper()
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason == "" {
		t.Fatalf("alerts = %+v, want one closed with a reason", as)
	}
}

func TestStartUnguardedFromSnapshot(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: false, Reason: "x"}))
	if a := r.openAlert(mID, KindMasterStartUnguarded); a.Message != wantStartUnguardedText+" (x)" {
		t.Fatalf("message = %q", a.Message)
	}

	// A new reason updates the open alert in place.
	ma.setSnap(func(s *proto.HealthSnapshot) {
		s.StartLock = &proto.StartLockState{Reason: "baris init_file agent tidak dibaca mysqld"}
	})
	ma.send()
	as := r.alerts(mID, KindMasterStartUnguarded)
	if len(as) != 1 || as[0].ClosedAt != nil || as[0].Message != wantStartUnguardedText+" (baris init_file agent tidak dibaca mysqld)" {
		t.Fatalf("after a new reason = %+v", as)
	}

	// MySQL down does not matter: the report still says the lock is missing.
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.send()
	if !r.isOpen(mID, KindMasterStartUnguarded) {
		t.Fatal("alert closed when MySQL stopped answering")
	}

	// A lock that holds closes it as recovered.
	ma.setSnap(func(s *proto.HealthSnapshot) {
		s.MySQLOK, s.StartLock = true, &proto.StartLockState{Present: true, Locked: true}
	})
	ma.send()
	as = r.alerts(mID, KindMasterStartUnguarded)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("after Locked = %+v, want closed as recovered", as)
	}

	// Without a reason the message is the bare text.
	ma.setSnap(func(s *proto.HealthSnapshot) { s.StartLock = &proto.StartLockState{} })
	ma.send()
	if a := r.openAlert(mID, KindMasterStartUnguarded); a.Message != wantStartUnguardedText {
		t.Fatalf("message without a reason = %q", a.Message)
	}

	// The server is no longer quarantined: closed with a reason.
	if err := r.st.SetServerRole(mID, store.RoleUnassigned); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	as = r.alerts(mID, KindMasterStartUnguarded)
	if len(as) != 2 || as[1].ClosedAt == nil || as[1].CloseReason == "" {
		t.Fatalf("after leaving quarantine = %+v, want the second closed with a reason", as)
	}
}

func TestStartUnguardedNeverForNormalMaster(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ma.send()
	// A master whose start lock is not released yet is not judged.
	ma.setSnap(func(s *proto.HealthSnapshot) { s.StartLock = &proto.StartLockState{Present: true, Reason: "x"} })
	ma.send()
	r.m.tick()
	if as := r.alerts(mID, KindMasterStartUnguarded); len(as) != 0 {
		t.Fatalf("alerts for a normal master = %+v", as)
	}
}

func TestOldAgentQuarantineUnguarded(t *testing.T) {
	r := newRig(t)
	// No report yet: nothing to judge.
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	r.promoted(topo, mID, rID, "done", "")
	r.m.tick()
	if len(r.alerts(mID, KindMasterStartUnguarded)) != 0 {
		t.Fatal("opened before any report")
	}
	// An agent that sends no start_lock at all.
	ma := r.agent(mID, true, quarSnap(nil)).attach()
	ma.send()
	if a := r.openAlert(mID, KindMasterStartUnguarded); a.Message != wantStartUnguardedText {
		t.Fatalf("message = %q", a.Message)
	}
}

func TestQuarantineStartLateAndBypassed(t *testing.T) {
	r := newRig(t)
	mID, _, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))

	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, false))
	if a := r.openAlert(mID, KindMasterStartLate); a.Message != wantStartLateText {
		t.Fatalf("late message = %q", a.Message)
	}
	if r.isOpen(mID, KindMasterStartBypassed) {
		t.Fatal("late opened bypassed")
	}
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartBypassed, true))
	if a := r.openAlert(mID, KindMasterStartBypassed); a.Message != wantStartBypassedText {
		t.Fatalf("bypassed message = %q", a.Message)
	}
	// A repeated event is the same alert.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartBypassed, true))
	if n := len(r.alerts(mID, KindMasterStartBypassed)); n != 1 {
		t.Fatalf("bypassed alerts = %d, want 1", n)
	}

	// The next start came up with the lock: bypassed closes, late does not.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartOK, false))
	as := r.alerts(mID, KindMasterStartBypassed)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("bypassed after ok = %+v", as)
	}
	if as := r.alerts(mID, KindMasterStartLate); len(as) != 1 || as[0].ClosedAt != nil {
		t.Fatalf("late after ok = %+v, want still open", as)
	}

	// Late with the lock bypassed too opens both.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, true))
	r.openAlert(mID, KindMasterStartBypassed)

	// A later late event closes nothing: bypassed false closes only bypassed.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, false))
	if r.isOpen(mID, KindMasterStartBypassed) || !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("late with bypassed=false must close bypassed and keep late")
	}

	// The server leaves quarantine: both close with a reason.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartBypassed, true))
	if err := r.st.SetServerRole(mID, store.RoleUnassigned); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if r.isOpen(mID, KindMasterStartLate) || r.isOpen(mID, KindMasterStartBypassed) {
		t.Fatal("start alerts still open for a server that is no longer quarantined")
	}
	for _, k := range []string{KindMasterStartLate, KindMasterStartBypassed} {
		as := r.alerts(mID, k)
		if last := as[len(as)-1]; last.CloseReason == "" {
			t.Errorf("%s closed without a reason: %+v", k, last)
		}
	}
}

// An event that cannot be judged opens and closes nothing, and never
// panics.
func TestQuarantineStartUnjudgedEventsChangeNothing(t *testing.T) {
	r := newRig(t)
	mID, _, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, true))
	r.m.HandleEvent(mID, startFailedEvent(t, "/etc/mysql/x.sql"))
	for _, ev := range []proto.Event{
		{Type: proto.EventQuarantineStart, Payload: []byte("{")},
		{Type: proto.EventQuarantineStart, Payload: nil},
		{Type: proto.EventQuarantineStart, Payload: []byte("null")},
		{Type: proto.EventQuarantineStart, Payload: []byte(`{"result":"entah","bypassed":false}`)},
		{Type: proto.EventQuarantineStart, Payload: []byte(`{"result":"","bypassed":false}`)},
		{Type: proto.EventQuarantineStart, Payload: []byte(strings.Repeat("a", 8<<10))},
		{Type: proto.EventQuarantineStartFailed, Payload: []byte("{")},
	} {
		if !r.m.HandleEvent(mID, ev) {
			t.Errorf("HandleEvent did not take %s (%d bytes)", ev.Type, len(ev.Payload))
		}
	}
	for _, k := range []string{KindMasterStartLate, KindMasterStartBypassed, KindMasterStartBlocked} {
		if !r.isOpen(mID, k) {
			t.Errorf("%s was closed by an event that cannot be judged", k)
		}
	}
	// An unknown result with Bypassed true is still a bypass: the safe side.
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantineStart, Payload: []byte(`{"result":"entah","bypassed":true}`)})
	if !r.isOpen(mID, KindMasterStartBypassed) {
		t.Error("an unknown result with bypassed=true did not open master_start_bypassed")
	}
}

func TestStartLateClosesAfterLeftoverCheck(t *testing.T) {
	r := newRig(t)
	mID, pid, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	setCheck := func(at time.Time) {
		t.Helper()
		p, err := r.st.GetPromotion(pid)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"transactions": 0, "checked_at": at})
		p.LeftoverJSON = string(b)
		if err := r.st.UpdatePromotion(p); err != nil {
			t.Fatal(err)
		}
	}
	// A check from before the alert says nothing about this start.
	setCheck(r.clk.Now())
	r.clk.Add(time.Minute)
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, false))
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("a check older than the alert closed it")
	}
	// A check at the very moment of the alert is not after it.
	setCheck(r.clk.Now())
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("a check at the alert's own time closed it")
	}
	// An unreadable check closes nothing.
	p, _ := r.st.GetPromotion(pid)
	p.LeftoverJSON = `{"checked_at":"bukan waktu"}`
	if err := r.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("an unreadable check closed it")
	}
	r.clk.Add(time.Minute)
	setCheck(r.clk.Now())
	r.m.tick()
	closedWithReason(t, r.alerts(mID, KindMasterStartLate))

	// The check does not close bypassed or blocked.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartBypassed, true))
	r.m.HandleEvent(mID, startFailedEvent(t, "/x"))
	r.clk.Add(time.Minute)
	setCheck(r.clk.Now())
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartBypassed) || !r.isOpen(mID, KindMasterStartBlocked) {
		t.Fatal("a leftover check closed bypassed or blocked")
	}
}

// TestStartLateSurvivesRebuildUntilCopied (SP-2): the rebuild of the old
// master makes it a replica in state creating at once; that does not
// close master_start_late (its data is still there), nor does a cancel
// that makes it an old master again. Once the rebuild finished (a replica
// whose copy is made) the alert closes: the data it speaks of is gone.
func TestStartLateSurvivesRebuildUntilCopied(t *testing.T) {
	r := newRig(t)
	mID, _, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartLate, false))
	topo, err := r.st.GetTopologyByOldMaster(mID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddReplica(topo.ID, mID, nil); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("master_start_late closed when a rebuild made the server a replica in state creating")
	}
	// The rebuild is cancelled: the old master is quarantined again.
	if err := r.st.RemoveReplica(mID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetServerRole(mID, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if !r.isOpen(mID, KindMasterStartLate) {
		t.Fatal("master_start_late closed after a cancelled rebuild")
	}
	// A rebuild that finished: the copy replaced the data.
	if _, err := r.st.AddReplica(topo.ID, mID, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetReplicaState(mID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	closedWithReason(t, r.alerts(mID, KindMasterStartLate))
}

// TestLockKeptOversizeStillAlerts (SP-9): a kept-lock event too large or
// unreadable to judge still opens the lock-expired notice (the generic
// kept text) instead of nothing.
func TestLockKeptOversizeStillAlerts(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"action":"kept","hosts":["` + strings.Repeat("h", 5<<10) + `"]}`), []byte("{")} {
		r := newRig(t)
		mID, _ := r.master("db-master")
		if !r.m.HandleEvent(mID, proto.Event{Type: proto.EventLockExpired, Payload: payload}) {
			t.Fatal("HandleEvent did not take monitor.lockExpired")
		}
		if a := r.openAlert(mID, KindMasterLockExpired); !strings.HasPrefix(a.Message, lockKeptPrefix) {
			t.Fatalf("%d-byte event: message = %q", len(payload), a.Message)
		}
	}
}

// TestUnreadableStartEventsStillAlert (SP-9): a start report that cannot
// be read is taken as the worst it could be: a late start (run "Periksa
// data tertinggal"), a failed start; an open alert keeps its message.
func TestUnreadableStartEventsStillAlert(t *testing.T) {
	r := newRig(t)
	mID, _, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantineStart, Payload: []byte("{")})
	if a := r.openAlert(mID, KindMasterStartLate); !strings.Contains(a.Message, "Periksa data tertinggal") {
		t.Fatalf("late message = %q", a.Message)
	}
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantineStartFailed, Payload: []byte(strings.Repeat("a", 8<<10))})
	r.openAlert(mID, KindMasterStartBlocked)

	r2 := newRig(t)
	m2, _, _ := r2.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	r2.m.HandleEvent(m2, startEvent(t, proto.QuarantineStartLate, false))
	r2.m.HandleEvent(m2, proto.Event{Type: proto.EventQuarantineStart, Payload: []byte("{")})
	if a := r2.openAlert(m2, KindMasterStartLate); a.Message != wantStartLateText {
		t.Fatalf("an unreadable event rewrote the open late alert: %q", a.Message)
	}
}

func TestStartBlockedOpensAndStartCloses(t *testing.T) {
	r := newRig(t)
	mID, _, _ := r.quarantined(quarSnap(&proto.StartLockState{Present: true, Locked: true}))
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }

	if !r.m.HandleEvent(mID, startFailedEvent(t, "/etc/mysql/conf.d/dbpanel-start-lock.sql")) {
		t.Fatal("HandleEvent did not take monitor.quarantineStartFailed")
	}
	want := wantStartBlockedText + " (/etc/mysql/conf.d/dbpanel-start-lock.sql). Pastikan berkas itu ada dan boleh dibaca " +
		"pengguna mysql (juga oleh AppArmor/SELinux), lalu pastikan agent berjalan: agent menulis ulang berkas itu dalam " +
		"beberapa detik. Jangan menghapus baris init_file dari konfigurasi MySQL."
	if a := r.openAlert(mID, KindMasterStartBlocked); a.Message != want {
		t.Fatalf("message =\n%q\nwant\n%q", a.Message, want)
	}
	if changed.Load() != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", changed.Load())
	}
	// Repeated: one alert.
	r.m.HandleEvent(mID, startFailedEvent(t, "/etc/mysql/conf.d/dbpanel-start-lock.sql"))
	if n := len(r.alerts(mID, KindMasterStartBlocked)); n != 1 {
		t.Fatalf("blocked alerts = %d", n)
	}
	// Any start event closes it, whatever its result says.
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartOK, false))
	as := r.alerts(mID, KindMasterStartBlocked)
	if len(as) != 1 || as[0].ClosedAt == nil {
		t.Fatalf("blocked after a start = %+v", as)
	}

	// A bypassed start closes it too; so does leaving quarantine.
	r.m.HandleEvent(mID, startFailedEvent(t, "/x"))
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartBypassed, true))
	if r.isOpen(mID, KindMasterStartBlocked) {
		t.Fatal("blocked open after a start event")
	}
	r.m.HandleEvent(mID, startFailedEvent(t, "/x"))
	if err := r.st.SetServerRole(mID, store.RoleUnassigned); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if r.isOpen(mID, KindMasterStartBlocked) {
		t.Fatal("blocked open for a server that is no longer quarantined")
	}

	// No file name: the text still reads.
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantineStartFailed, Payload: []byte(`{}`)})
	if a := r.openAlert(mID, KindMasterStartBlocked); !strings.HasPrefix(a.Message, wantStartBlockedText+". Pastikan") {
		t.Fatalf("message without a file = %q", a.Message)
	}
}

// Text from an agent is data: control characters and length are bounded
// before it reaches an alert message.
func TestStartAndKeptMessagesBoundAgentText(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(quarSnap(&proto.StartLockState{Reason: "baris\nkedua\x00‮" + strings.Repeat("z", 5000)}))
	a := r.openAlert(mID, KindMasterStartUnguarded)
	if strings.ContainsAny(a.Message, "\n\x00‮") || len(a.Message) > 1200 || !strings.Contains(a.Message, "baris kedua") {
		t.Fatalf("unguarded message not bounded: %d bytes %q", len(a.Message), a.Message[:200])
	}
	_ = ma
	r.m.HandleEvent(mID, startFailedEvent(t, "/a\n/b"+strings.Repeat("q", 1500)))
	b := r.openAlert(mID, KindMasterStartBlocked)
	if strings.Contains(b.Message, "\n") || len(b.Message) > 1200 {
		t.Fatalf("blocked message not bounded: %d bytes", len(b.Message))
	}

	m2, _ := r.master("db-master")
	hosts := make([]string, 0, 40)
	for i := range 40 {
		hosts = append(hosts, "r"+itoa(int64(i))+".db:3306")
	}
	hosts = append(hosts, "x\ny:3306")
	r.m.HandleEvent(m2, keptEvent(t, hosts...))
	k := r.openAlert(m2, KindMasterLockKept)
	if strings.Contains(k.Message, "\n") || len(k.Message) > 2000 || !strings.Contains(k.Message, "dan ") ||
		!strings.Contains(k.Message, "sudo dbpanel agent unlock-master") {
		t.Fatalf("kept message not bounded: %d bytes %q", len(k.Message), k.Message)
	}

	// SP-11: the candidate of a lock-expired notice and the new source of
	// a quarantine notice are the agent's words too.
	long := "x\ny" + strings.Repeat("c", 5000)
	if msg := lockExpiredMessage(proto.LockExpiredEvent{Action: lockExpiredKept, Candidate: long}); strings.Contains(msg, "\n") || len(msg) > 1200 {
		t.Fatalf("lock expired message not bounded: %d bytes", len(msg))
	}
	if msg := quarantinedMessage(long); strings.Contains(msg, "\n") || len(msg) > 1200 {
		t.Fatalf("quarantined message not bounded: %d bytes", len(msg))
	}
}

func TestLockKeptWithHostsIsCritical(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }

	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.5:3306", "10.0.0.6:3307"))
	want := "Batas waktu kunci promote di master habis, tetapi master tetap dikunci karena replika berikut tidak terbaca: " +
		"10.0.0.5:3306, 10.0.0.6:3307. Kunci terbuka sendiri setelah semua replika itu terbaca dan masih hanya-baca. " +
		"Bila replika itu sudah tidak dipakai, jalankan di server master: sudo dbpanel agent forget-replica <host>. " +
		"Untuk membuka kunci sekarang (pastikan dulu replika itu tidak sedang menjadi master di sisi jaringan lain: " +
		"unlock-master membuka master tanpa memeriksanya): sudo dbpanel agent unlock-master"
	a := r.openAlert(mID, KindMasterLockKept)
	if a.Message != want || a.TopologyID != topo.ID {
		t.Fatalf("kept alert = %+v\nwant message %q", a, want)
	}
	if r.isOpen(mID, KindMasterLockExpired) {
		t.Fatal("a kept event with hosts opened master_lock_expired")
	}
	if changed.Load() != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", changed.Load())
	}

	// The same hosts, in another order, hourly: nothing changes.
	r.clk.Add(time.Hour)
	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.6:3307", "10.0.0.5:3306", "10.0.0.5:3306"))
	if as := r.alerts(mID, KindMasterLockKept); len(as) != 1 || as[0].Message != want {
		t.Fatalf("after the same hosts = %+v", as)
	}
	// Past the 24 hours of the one-time notices it stays: the lock holds.
	r.clk.Add(30 * time.Hour)
	r.m.tick()
	if !r.isOpen(mID, KindMasterLockKept) {
		t.Fatal("master_lock_kept closed by age while the lock may still hold")
	}

	// A different host set updates the open alert's message in place.
	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.7:3306"))
	as := r.alerts(mID, KindMasterLockKept)
	if len(as) != 1 || as[0].ClosedAt != nil || !strings.Contains(as[0].Message, "tidak terbaca: 10.0.0.7:3306.") ||
		strings.Contains(as[0].Message, "10.0.0.5") {
		t.Fatalf("after another host set = %+v", as)
	}

	// A kept without hosts is master_lock_expired as before and closes nothing.
	r.m.HandleEvent(mID, lockExpired(t, "kept", ""))
	if !r.isOpen(mID, KindMasterLockExpired) || !r.isOpen(mID, KindMasterLockKept) {
		t.Fatal("kept without hosts must open master_lock_expired and leave master_lock_kept")
	}

	// The guard unlocked the master: closed with a reason.
	r.m.HandleEvent(mID, lockExpired(t, "unlocked", ""))
	closedWithReason(t, r.alerts(mID, KindMasterLockKept))
	_ = topo
}

func TestLockKeptClosesWhenOverLikeLockExpired(t *testing.T) {
	// The master quarantines itself.
	r := newRig(t)
	mID, _ := r.master("db-master")
	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.5:3306"))
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantined, Payload: []byte(`{"new_source":"10.0.0.2:3306"}`)})
	as := r.alerts(mID, KindMasterLockKept)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "master sudah dikarantina sebagai master lama" {
		t.Fatalf("kept after quarantine = %+v", as)
	}

	// Unlocked from the panel: the next report shows the master writable.
	r = newRig(t)
	mID, _ = r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.SuperReadOnly, s.ReadOnly = true, true })
	ma.send()
	r.clk.Add(time.Second)
	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.5:3306"))
	r.clk.Add(time.Second)
	ma.send()
	r.m.tick()
	if !r.isOpen(mID, KindMasterLockKept) {
		t.Fatal("kept closed while the master is still locked")
	}
	ma.setSnap(func(s *proto.HealthSnapshot) { s.SuperReadOnly, s.ReadOnly = false, false })
	r.clk.Add(time.Second)
	ma.send()
	r.m.tick()
	as = r.alerts(mID, KindMasterLockKept)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "master sudah bisa ditulisi lagi" {
		t.Fatalf("kept after unlock = %+v", as)
	}

	// The server became the old master through a finished promote.
	r = newRig(t)
	mID, topo := r.master("db-lama")
	rID := r.replica("db-baru", topo)
	r.m.HandleEvent(mID, keptEvent(t, "10.0.0.5:3306"))
	r.promoted(topo, mID, rID, "done", "")
	r.m.tick()
	closedWithReason(t, r.alerts(mID, KindMasterLockKept))
}

// A kept event naming no usable host is a plain kept.
func TestLockKeptWithEmptyHostsIsLockExpired(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-master")
	r.m.HandleEvent(mID, keptEvent(t, "", "  "))
	if r.isOpen(mID, KindMasterLockKept) || !r.isOpen(mID, KindMasterLockExpired) {
		t.Fatal("hosts without a name must be a plain master_lock_expired")
	}
}

// The start alerts of an agent that reports the state of its starts
// (StartResultState.Kinds) follow that state; its start events (they carry
// a start id) open and close nothing. An agent without either is judged
// by its events, as before the panel read the snapshot. Tests 1-18 of the
// requirement; 17 is TestStartMarksReplaceAndCascade in the store.

func (r *rig) setLeftoverCheck(pid int64, at time.Time) {
	r.t.Helper()
	p, err := r.st.GetPromotion(pid)
	if err != nil {
		r.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"transactions": 0, "checked_at": at})
	p.LeftoverJSON = string(b)
	if err := r.st.UpdatePromotion(p); err != nil {
		r.t.Fatal(err)
	}
}

// checkLeftover stores a "Periksa data tertinggal" result taken now and
// lets the tick close what it closes.
func (r *rig) checkLeftover(pid int64) {
	r.t.Helper()
	r.setLeftoverCheck(pid, r.clk.Now())
	r.m.tick()
}

func (r *rig) count(id int64, kind string) int { return len(r.alerts(id, kind)) }

func (r *rig) mark(id int64, kind string) string {
	r.t.Helper()
	m, err := r.st.StartMarks(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m[kind]
}

// wantAlerts: n alerts of kind, the newest open (or closed) as given.
func (r *rig) wantAlerts(id int64, kind string, n int, open bool) {
	r.t.Helper()
	if got, o := r.count(id, kind), r.isOpen(id, kind); got != n || o != open {
		r.t.Fatalf("%s alerts = %d (one open %v), want %d (one open %v)", kind, got, o, n, open)
	}
}

func plainQuar() proto.HealthSnapshot {
	return quarSnap(&proto.StartLockState{Present: true, Locked: true})
}

// sk is one entry of the start state: the last start of kind, with its
// id, age and whether it is over; a failed start names the lock file.
func sk(kind, id string, age int64, over bool) proto.StartKindState {
	e := proto.StartKindState{Kind: kind, StartID: id, AgeSec: age, Over: over}
	if kind == proto.QuarantineStartFailed {
		e.InitFile = "/etc/mysql/x.sql"
	}
	return e
}

// state sets the start state the agent reports (none: no start result);
// the worst result is the first entry (the panel does not read it).
func (a *fakeAgent) state(entries ...proto.StartKindState) {
	a.setSnap(func(s *proto.HealthSnapshot) {
		if len(entries) == 0 {
			s.StartResult = nil
			return
		}
		s.StartResult = &proto.StartResultState{Result: entries[0].Kind, AgeSec: entries[0].AgeSec,
			StartID: entries[0].StartID, Kinds: entries}
	})
}

// idStartEvent is startEvent with the id the agent gave the start.
func idStartEvent(t *testing.T, result string, bypassed bool, startID string) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.QuarantineStartEvent{Result: result, Bypassed: bypassed, InitFile: "/etc/mysql/x.sql", StartID: startID})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventQuarantineStart, Payload: b}
}

func idFailedEvent(t *testing.T, file, startID string) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.QuarantineStartFailedEvent{File: file, StartID: startID})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventQuarantineStartFailed, Payload: b}
}

const (
	late     = proto.QuarantineStartLate
	failed   = proto.QuarantineStartFailed
	bypassed = proto.QuarantineStartBypassed
)

// 1: the snapshot opens the late alert once; the late event, before or
// after the snapshot, changes nothing.
func TestStartState01_LateOpensOnceEventsChangeNothing(t *testing.T) {
	for _, eventFirst := range []bool{true, false} {
		r := newRig(t)
		mID, _, ma := r.quarantined(plainQuar())
		if eventFirst {
			r.m.HandleEvent(mID, idStartEvent(t, late, false, "L1"))
			if n := r.count(mID, KindMasterStartLate); n != 0 {
				t.Fatalf("event first: the late event opened %d alerts, want none (left to the snapshot)", n)
			}
		}
		ma.state(sk(late, "L1", 30, false))
		ma.send()
		ma.send()
		ma.send()
		if !eventFirst {
			r.m.HandleEvent(mID, idStartEvent(t, late, false, "L1"))
		}
		r.wantAlerts(mID, KindMasterStartLate, 1, true)
		if got := r.mark(mID, KindMasterStartLate); got != "L1" {
			t.Fatalf("event first %v: mark = %q, want L1", eventFirst, got)
		}
	}
}

// 2: a second genuine late start after the leftover check closed the
// first opens again from the snapshot, also when its id is an earlier time
// (the agent's clock stepped back).
func TestStartState02_SecondLateOpensAgain(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	ma.state(sk(late, "2026-11-02T09:00:00Z", 0, false))
	ma.send()
	r.clk.Add(time.Minute)
	r.checkLeftover(pid)
	closedWithReason(t, r.alerts(mID, KindMasterStartLate))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, false)

	r.clk.Add(3 * time.Minute)
	ma.state(sk(late, "2026-11-02T09:04:00Z", 0, false))
	ma.send()
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 2, true)

	r.clk.Add(time.Minute)
	r.checkLeftover(pid)
	r.wantAlerts(mID, KindMasterStartLate, 2, false)
	r.clk.Add(3 * time.Minute)
	ma.state(sk(late, "2026-11-01T23:00:00Z", 0, false)) // earlier than both
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 3, true)
}

// 3: bypassed opens from the snapshot, closes when the snapshot says it is
// over; a second bypass opens again.
func TestStartState03_BypassOverClosesSecondOpens(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(bypassed, "B1", 5, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
	ma.state(sk(bypassed, "B1", 65, true))
	ma.send()
	ma.send()
	as := r.alerts(mID, KindMasterStartBypassed)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != "" {
		t.Fatalf("after over = %+v, want the one closed as recovered", as)
	}
	ma.state(sk(bypassed, "B2", 1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	if got := r.mark(mID, KindMasterStartBypassed); got != "B2" {
		t.Fatalf("mark = %q, want B2", got)
	}
}

// 4: the re-review's sequence (a): a bypass B1 alert is open; after a
// reconnect the snapshot shows bypass B2 (not over) before the queued ok
// event of the start between them and the B2 event: the alert stays open
// and stands for B2.
func TestStartState04_SnapshotBeforeQueuedEventsA(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(bypassed, "B1", 5, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
	ma.state(sk(bypassed, "B2", 1, false))
	ma.send()
	r.m.HandleEvent(mID, idStartEvent(t, proto.QuarantineStartOK, false, "O1"))
	r.m.HandleEvent(mID, idStartEvent(t, bypassed, true, "B2"))
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
	if got := r.mark(mID, KindMasterStartBypassed); got != "B2" {
		t.Fatalf("mark = %q, want B2", got)
	}
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
}

// 5: the re-review's sequence (b): B1 handled and closed; the snapshot
// shows B2 (not over) and opens; the queued ok and B2 events that follow
// leave it open.
func TestStartState05_SnapshotBeforeQueuedEventsB(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(bypassed, "B1", 5, false))
	ma.send()
	ma.state(sk(bypassed, "B1", 10, true))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	ma.state(sk(bypassed, "B2", 1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	r.m.HandleEvent(mID, idStartEvent(t, proto.QuarantineStartOK, false, "O1"))
	r.m.HandleEvent(mID, idStartEvent(t, bypassed, true, "B2"))
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
}

// 6: a failed start opens with MySQL down (the message names the lock
// file); with MySQL running it opens nothing and sets no mark; a later
// snapshot saying it is over closes it with the "over" reason.
func TestStartState06_Failed(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(failed, "F1", 10, false)) // MySQLOK is true
	ma.send()
	if n := r.count(mID, KindMasterStartBlocked); n != 0 || r.mark(mID, KindMasterStartBlocked) != "" {
		t.Fatalf("with MySQL running: blocked alerts = %d, mark %q; want none", n, r.mark(mID, KindMasterStartBlocked))
	}
	r.m.HandleEvent(mID, idFailedEvent(t, "/etc/mysql/x.sql", "F1"))
	if n := r.count(mID, KindMasterStartBlocked); n != 0 {
		t.Fatalf("the failed event opened %d alerts, want none", n)
	}
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.send()
	if a := r.openAlert(mID, KindMasterStartBlocked); !strings.Contains(a.Message, "/etc/mysql/x.sql") {
		t.Fatalf("message = %q, want the lock file", a.Message)
	}
	if got := r.mark(mID, KindMasterStartBlocked); got != "F1" {
		t.Fatalf("mark = %q, want F1", got)
	}
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 1, true)
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = true })
	ma.state(sk(failed, "F1", 70, true))
	ma.send()
	as := r.alerts(mID, KindMasterStartBlocked)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != startBlockedOverReason {
		t.Fatalf("after over = %+v, want closed with %q", as, startBlockedOverReason)
	}
}

// 7: a late start on file does not hide a bypass: both open from one
// snapshot.
func TestStartState07_LateAndBypassedBothOpen(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(late, "L1", 30, false), sk(bypassed, "B1", 5, false))
	ma.send()
	if a := r.openAlert(mID, KindMasterStartLate); a.Message != wantStartLateText {
		t.Fatalf("late message = %q", a.Message)
	}
	if a := r.openAlert(mID, KindMasterStartBypassed); a.Message != wantStartBypassedText {
		t.Fatalf("bypassed message = %q", a.Message)
	}
}

// 8: an older agent (no state, no ids) is judged by its events as before:
// a second event of a kind minutes after the first alert closed opens
// again, and nothing is marked. TestQuarantineStartLateAndBypassed and
// TestStartBlockedOpensAndStartCloses cover the rest of the event rules.
func TestStartState08_OldAgentEvents(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	// A start result without a state (an agent of Task 7) opens nothing.
	ma.setSnap(func(s *proto.HealthSnapshot) {
		s.StartResult = &proto.StartResultState{Result: late, AgeSec: 1}
	})
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 0, false)

	r.m.HandleEvent(mID, startEvent(t, late, false))
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	r.clk.Add(time.Minute)
	r.checkLeftover(pid)
	closedWithReason(t, r.alerts(mID, KindMasterStartLate))
	r.clk.Add(3 * time.Minute)
	r.m.HandleEvent(mID, startEvent(t, late, false))
	r.wantAlerts(mID, KindMasterStartLate, 2, true)

	r.m.HandleEvent(mID, startEvent(t, bypassed, true))
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartOK, false))
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	r.clk.Add(3 * time.Minute)
	r.m.HandleEvent(mID, startEvent(t, bypassed, true))
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)

	r.m.HandleEvent(mID, startFailedEvent(t, "/x"))
	r.m.HandleEvent(mID, startEvent(t, proto.QuarantineStartOK, false))
	as := r.alerts(mID, KindMasterStartBlocked)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != startBlockedOverReason {
		t.Fatalf("blocked after an ok event = %+v", as)
	}
	r.clk.Add(3 * time.Minute)
	r.m.HandleEvent(mID, startFailedEvent(t, "/x"))
	r.wantAlerts(mID, KindMasterStartBlocked, 2, true)
	if m, _ := r.st.StartMarks(mID); len(m) != 0 {
		t.Fatalf("marks from events without an id = %v, want none", m)
	}
}

// 9: an agent of this version whose events are all lost: the alerts open
// and close from the snapshots alone.
func TestStartState09_EventsLost(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	r.clk.Add(10 * time.Minute) // the starts below are of this quarantine
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.state(sk(late, "L1", 300, false), sk(failed, "F1", 10, false), sk(bypassed, "B1", 200, false))
	ma.send()
	for _, k := range []string{KindMasterStartLate, KindMasterStartBlocked, KindMasterStartBypassed} {
		r.wantAlerts(mID, k, 1, true)
	}
	// MySQL came up with the lock: the failed start and the bypass are over.
	r.clk.Add(time.Minute)
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = true })
	ma.state(sk(late, "L1", 360, false), sk(failed, "F1", 70, true), sk(bypassed, "B1", 260, true))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	r.wantAlerts(mID, KindMasterStartBlocked, 1, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
}

// 10: a long quarantine: alerts opened and closed, the closed ones pruned;
// the same snapshot keeps arriving for 400 days and nothing reopens.
func TestStartState10_LongQuarantine(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	ma.state(sk(late, "L1", 0, false), sk(bypassed, "B1", 0, false))
	ma.send()
	r.clk.Add(time.Minute)
	r.checkLeftover(pid)
	ma.state(sk(late, "L1", 60, false), sk(bypassed, "B1", 60, true))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	for day := int64(1); day <= 400; day += 7 {
		r.clk.Add(7 * 24 * time.Hour)
		if _, err := r.st.PruneAlertsClosedBefore(r.clk.Now().Add(-24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		age := int64(r.clk.Now().Sub(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) / time.Second)
		ma.state(sk(late, "L1", age, false), sk(bypassed, "B1", age, true))
		ma.send()
		r.m.tick()
	}
	r.wantAlerts(mID, KindMasterStartLate, 0, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 0, false)
}

// 11: a panel restart (a new Monitor on the same store) reopens nothing;
// with the mark replaced by another id the same snapshot opens (control:
// the mark is the only thing that tells).
func TestStartState11_PanelRestart(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(bypassed, "B1", 0, false))
	ma.send()
	// The alert is closed by something other than the state (the bypass is
	// not over): the operator dealt with it.
	a := r.openAlert(mID, KindMasterStartBypassed)
	if err := r.st.CloseAlertWithReason(a.ID, r.clk.Now(), "ditangani"); err != nil {
		t.Fatal(err)
	}
	report := func() {
		m := New(Deps{Store: r.st, Hub: r.hub, Publish: r.publish,
			Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: r.clk.Now, TickEvery: time.Hour})
		t.Cleanup(m.Close)
		snap := plainQuar()
		snap.StartResult = &proto.StartResultState{Result: bypassed, AgeSec: 30, StartID: "B1",
			Kinds: []proto.StartKindState{sk(bypassed, "B1", 30, false)}}
		m.report(mID, proto.HealthEvent{Seq: 1, Snapshot: snap})
	}
	report()
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	if err := r.st.SetStartMark(mID, KindMasterStartBypassed, "other"); err != nil {
		t.Fatal(err)
	}
	report()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
}

// 12: the alert insert fails once: no mark; the next snapshot opens it.
func TestStartState12_FailedOpenLeavesNoMark(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	fail := true
	r.m.openFault = func() error {
		if fail {
			fail = false
			return errors.New("disk full")
		}
		return nil
	}
	ma.state(sk(bypassed, "B1", 5, false))
	ma.send()
	if r.isOpen(mID, KindMasterStartBypassed) || r.mark(mID, KindMasterStartBypassed) != "" {
		t.Fatalf("after a failed open: open %v, mark %q; want neither", r.isOpen(mID, KindMasterStartBypassed), r.mark(mID, KindMasterStartBypassed))
	}
	ma.send()
	if !r.isOpen(mID, KindMasterStartBypassed) || r.mark(mID, KindMasterStartBypassed) != "B1" {
		t.Fatal("the next snapshot did not open the alert and mark it")
	}
}

// 13: an entry decided before the promotion's start (minus the slack)
// belongs to an earlier quarantine: nothing opens, nothing is marked.
func TestStartState13_EarlierQuarantine(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	for _, age := range []int64{3600, int64(startResultSlack/time.Second) + 2, math.MaxInt64} {
		ma.state(sk(late, "L1", age, false), sk(bypassed, "B1", age, false))
		ma.send()
	}
	r.wantAlerts(mID, KindMasterStartLate, 0, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 0, false)
	if m, err := r.st.StartMarks(mID); err != nil || len(m) != 0 {
		t.Fatalf("marks = %v, %v; want none", m, err)
	}
	// Within the slack it is this quarantine's.
	ma.state(sk(late, "L2", int64(startResultSlack/time.Second)-1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
}

// 14: a late start already followed by a "Periksa data tertinggal" result
// opens nothing and is marked.
func TestStartState14_LeftoverGuard(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	r.setLeftoverCheck(pid, r.clk.Now())
	r.clk.Add(time.Minute)
	ma.state(sk(late, "L1", 120, false)) // decided a minute before the check
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 0, false)
	if got := r.mark(mID, KindMasterStartLate); got != "L1" {
		t.Fatalf("mark = %q, want L1", got)
	}
}

// 15: the state is untrusted input: an unknown kind, a second entry of a
// kind (the first wins), an id empty or over 64 bytes, absurd ages: no
// panic, no alert from a bad entry, the age clamped.
func TestStartState15_UntrustedInput(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	long := strings.Repeat("x", 65)
	ma.state(
		sk("weird", "W1", 1, false),
		sk(late, "", 1, false), sk(late, "L1", 1, false), // the first late has no id: no late at all
		sk(failed, long, 1, false),
		sk(bypassed, "B1", 1, false), sk(bypassed, "B1", 1, true), // the first wins: open
	)
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 0, false)
	r.wantAlerts(mID, KindMasterStartBlocked, 0, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
	if m, _ := r.st.StartMarks(mID); len(m) != 1 || m[KindMasterStartBypassed] != "B1" {
		t.Fatalf("marks = %v, want only bypassed B1", m)
	}
	// Ages: negative and MinInt64 are now (this quarantine), MaxInt64 is
	// clamped to ten years (an earlier quarantine); a 64-byte id is an id.
	ma.state(sk(late, strings.Repeat("y", 64), math.MinInt64, false), sk(failed, "F1", -5, false),
		sk(bypassed, "B2", math.MaxInt64, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	r.wantAlerts(mID, KindMasterStartBlocked, 1, true)
	if got := r.mark(mID, KindMasterStartBypassed); got != "B1" {
		t.Fatalf("bypassed mark = %q, want B1 kept (B2 is from an earlier quarantine)", got)
	}

	// An event whose id is over 64 bytes has no usable id: it is judged as
	// an older agent's event (it opens, and marks nothing).
	r2 := newRig(t)
	m2, _, _ := r2.quarantined(plainQuar())
	r2.m.HandleEvent(m2, idStartEvent(t, late, false, long))
	r2.wantAlerts(m2, KindMasterStartLate, 1, true)
	if m, _ := r2.st.StartMarks(m2); len(m) != 0 {
		t.Fatalf("marks from an event with an oversized id = %v, want none", m)
	}
}

func TestStartDecidedAtBoundsTheAge(t *testing.T) {
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		age  int64
		want time.Time
	}{
		{0, at},
		{-5, at},
		{math.MinInt64, at},
		{30, at.Add(-30 * time.Second)},
		{math.MaxInt64, at.Add(-maxStartAge * time.Second)},
		{maxStartAge + 1, at.Add(-maxStartAge * time.Second)},
	} {
		if got := startDecidedAt(at, c.age); !got.Equal(c.want) {
			t.Errorf("startDecidedAt(age %d) = %v, want %v", c.age, got, c.want)
		}
	}
}

// 16: the server left quarantine on its own side (a finished rebuild, a
// local set-role): a report whose role pin reads and is not quarantined
// closes the blocked and bypassed alerts it has marks for; not late, not
// on an unreadable pin, not an alert without a mark.
func TestStartState16_QuarantineEnded(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.state(sk(late, "L1", 30, false), sk(failed, "F1", 10, false), sk(bypassed, "B1", 20, false))
	ma.send()
	for _, k := range []string{KindMasterStartLate, KindMasterStartBlocked, KindMasterStartBypassed} {
		r.wantAlerts(mID, k, 1, true)
	}
	ma.state()
	ma.setSnap(func(s *proto.HealthSnapshot) {
		s.RolePin, s.PinError, s.StartLock = "", "pin tidak bisa dipercaya", nil
	})
	ma.send()
	for _, k := range []string{KindMasterStartLate, KindMasterStartBlocked, KindMasterStartBypassed} {
		r.wantAlerts(mID, k, 1, true)
	}
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin, s.PinError = "master", "" })
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	as := r.alerts(mID, KindMasterStartBlocked)
	if len(as) != 1 || as[0].ClosedAt == nil || as[0].CloseReason != startBlockedOverReason {
		t.Fatalf("blocked = %+v, want closed with %q", as, startBlockedOverReason)
	}
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)

	// An alert without a mark (opened by an event without an id) stays.
	r2 := newRig(t)
	m2, _, ma2 := r2.quarantined(plainQuar())
	r2.m.HandleEvent(m2, startFailedEvent(t, "/x"))
	ma2.setSnap(func(s *proto.HealthSnapshot) { s.RolePin = "master" })
	ma2.send()
	r2.wantAlerts(m2, KindMasterStartBlocked, 1, true)
}

// 18: a server the panel does not see as quarantined: a start state opens
// nothing.
func TestStartState18_NotQuarantined(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-biasa")
	snap := masterSnap(srcUUID + ":1-100")
	snap.MySQLOK = false
	snap.StartResult = &proto.StartResultState{Result: late, AgeSec: 1, StartID: "L1",
		Kinds: []proto.StartKindState{sk(late, "L1", 1, false), sk(failed, "F1", 1, false), sk(bypassed, "B1", 1, false)}}
	r.agent(mID, true, snap).attach()
	for _, k := range []string{KindMasterStartLate, KindMasterStartBlocked, KindMasterStartBypassed} {
		r.wantAlerts(mID, k, 0, false)
	}
	if m, _ := r.st.StartMarks(mID); len(m) != 0 {
		t.Fatalf("marks = %v, want none", m)
	}
}

// Round 4.

// R4-1: a report ingest does not take (older by Seq: a forced-check reply
// that arrived after a newer report) is not reconciled: it closes nothing
// and moves no mark, and its role pin closes nothing.
func TestStartStateR4StaleReportIgnored(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.state(sk(bypassed, "B1", 30, false))
	ma.send()
	ma.state(sk(bypassed, "B1", 40, true))
	ma.send()
	ma.state(sk(bypassed, "B2", 1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	stale := plainQuar()
	stale.StartResult = &proto.StartResultState{Result: bypassed, AgeSec: 40, StartID: "B1",
		Kinds: []proto.StartKindState{sk(bypassed, "B1", 40, true)}}
	ma.emit(proto.HealthEvent{Seq: 1, Snapshot: stale})
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	if got := r.mark(mID, KindMasterStartBypassed); got != "B2" {
		t.Fatalf("mark after a stale report = %q, want B2", got)
	}
	stale = plainQuar()
	stale.RolePin = "master"
	ma.emit(proto.HealthEvent{Seq: 1, Snapshot: stale})
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
}

// A hostile agent alternating a bypassed start with new ids and its end
// opens a bounded number of alerts from its reports; the first alert of
// another kind still opens at once, and a start held back opens once the
// limit refills.
func TestStartAlertOpensRateLimited(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	for i := range 50 {
		id := fmt.Sprintf("B%d", i)
		ma.state(sk(bypassed, id, 1, false))
		ma.send()
		ma.state(sk(bypassed, id, 2, true))
		ma.send()
		r.clk.Add(time.Second)
	}
	if n := r.count(mID, KindMasterStartBypassed); n != startOpenBurst {
		t.Fatalf("bypassed alerts after 100 alternating reports = %d, want %d", n, startOpenBurst)
	}
	// A genuine late start: its first alert opens at once.
	ma.state(sk(late, "L1", 0, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	// A bypass held back by the limit opens when the limit refills, if the
	// state still says so; until then it is not accounted.
	ma.state(sk(bypassed, "B-echt", 1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, startOpenBurst, false)
	if got := r.mark(mID, KindMasterStartBypassed); got == "B-echt" {
		t.Fatal("a start held back by the limit was marked")
	}
	r.clk.Add(startOpenRefill)
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, startOpenBurst+1, true)
	if got := r.mark(mID, KindMasterStartBypassed); got != "B-echt" {
		t.Fatalf("mark = %q, want B-echt", got)
	}
}

// A report with a lower Seq, carrying an older start id1, after the report
// with id2 was taken (a replayed backlog report after a reconnect)
// changes nothing: it does not open an alert for id1 again and does not
// move the mark.
func TestStartStateOlderIDLowerSeqIgnored(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	ma.send()
	ma.state(sk(late, "L2", 0, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartLate, 1, true)
	r.clk.Add(time.Minute)
	r.checkLeftover(pid)
	r.wantAlerts(mID, KindMasterStartLate, 1, false)
	old := plainQuar()
	old.StartResult = &proto.StartResultState{Result: late, StartID: "L1", Kinds: []proto.StartKindState{sk(late, "L1", 0, false)}}
	ma.emit(proto.HealthEvent{Seq: 1, Snapshot: old})
	r.wantAlerts(mID, KindMasterStartLate, 1, false)
	if got := r.mark(mID, KindMasterStartLate); got != "L2" {
		t.Fatalf("mark after an older report = %q, want L2", got)
	}
}

// R4-3: the L1 alert is open; "Periksa data tertinggal" is stored, then a
// newer late start L2 is reported before the tick closes L1. L1 closes as
// checked, L2 opens its own alert, and the tick closes nothing more.
func TestStartStateR4NewerLateAfterCheck(t *testing.T) {
	r := newRig(t)
	mID, pid, ma := r.quarantined(plainQuar())
	ma.state(sk(late, "L1", 0, false))
	ma.send()
	r.clk.Add(time.Minute)
	r.setLeftoverCheck(pid, r.clk.Now()) // no tick yet
	r.clk.Add(time.Minute)
	ma.state(sk(late, "L2", 0, false))
	ma.send()
	as := r.alerts(mID, KindMasterStartLate)
	if len(as) != 2 || as[0].ClosedAt == nil || as[0].CloseReason != leftoverCheckedReason || as[1].ClosedAt != nil {
		t.Fatalf("late alerts = %+v, want L1 closed as checked and L2 open", as)
	}
	if got := r.mark(mID, KindMasterStartLate); got != "L2" {
		t.Fatalf("mark = %q, want L2", got)
	}
	r.m.tick()
	r.wantAlerts(mID, KindMasterStartLate, 2, true)
}

// R4-4: the role-pin close runs once per end of quarantine (on the change
// to a readable pin that is not quarantined, and once after a panel
// restart when marks exist), and deletes the blocked and bypassed marks.
func TestStartStateR4PinCloseOncePerEnd(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.state(sk(failed, "F1", 10, false), sk(bypassed, "B1", 20, false))
	ma.send()
	ma.state()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin, s.StartLock = "master", nil })
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 1, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	if m, _ := r.st.StartMarks(mID); m[KindMasterStartBlocked] != "" || m[KindMasterStartBypassed] != "" {
		t.Fatalf("marks after the end of quarantine = %v, want no blocked or bypassed", m)
	}
	// Not again on the next reports: an alert opened while the pin stays
	// master (an inconsistent agent) is not closed by the pin.
	ma.state(sk(bypassed, "B5", 1, false))
	ma.send()
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	// quarantined, then master again: a new end, it closes.
	ma.state()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin = "quarantined" })
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, true)
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin = "master" })
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 2, false)

	// After a panel restart the previous pin is unknown: with a mark, the
	// first readable non-quarantined pin closes once.
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin = "quarantined" })
	ma.state(sk(bypassed, "B6", 1, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBypassed, 3, true)
	m := New(Deps{Store: r.st, Hub: r.hub, Publish: r.publish,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: r.clk.Now, TickEvery: time.Hour})
	t.Cleanup(m.Close)
	snap := plainQuar()
	snap.RolePin = "master"
	m.report(mID, proto.HealthEvent{Seq: 1, Snapshot: snap})
	r.wantAlerts(mID, KindMasterStartBypassed, 3, false)
	if got := r.mark(mID, KindMasterStartBypassed); got != "" {
		t.Fatalf("bypassed mark after the restart's close = %q, want none", got)
	}
}

// R4-4b: the role-pin close fails on the store (the close of the alert):
// the alert stays open and the end of quarantine stays pending; the next
// taken report closes it and deletes the marks.
func TestStartStateR4PinCloseRetriedAfterStoreFailure(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.state(sk(failed, "F1", 10, false), sk(bypassed, "B1", 20, false))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 1, true)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, true)
	fail := true
	r.m.closeFault = func() error {
		if fail {
			fail = false
			return errors.New("database is locked")
		}
		return nil
	}
	ma.state()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.RolePin, s.StartLock = "master", nil })
	ma.send()
	if !r.isOpen(mID, KindMasterStartBlocked) || r.mark(mID, KindMasterStartBlocked) != "F1" {
		t.Fatalf("after a failed close: blocked open %v, mark %q; want open, F1",
			r.isOpen(mID, KindMasterStartBlocked), r.mark(mID, KindMasterStartBlocked))
	}
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 1, false)
	r.wantAlerts(mID, KindMasterStartBypassed, 1, false)
	if m, _ := r.st.StartMarks(mID); m[KindMasterStartBlocked] != "" || m[KindMasterStartBypassed] != "" {
		t.Fatalf("marks after the retried close = %v, want no blocked or bypassed", m)
	}
}

// R4-5: an over entry already dealt with costs nothing more: an alert of
// its kind opened later by an event without an id (the "worst" form) is
// not closed by the same over entry again.
func TestStartStateR4OverHandledOnce(t *testing.T) {
	r := newRig(t)
	mID, _, ma := r.quarantined(plainQuar())
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = false })
	ma.state(sk(failed, "F1", 10, false))
	ma.send()
	ma.setSnap(func(s *proto.HealthSnapshot) { s.MySQLOK = true })
	ma.state(sk(failed, "F1", 70, true))
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 1, false)
	r.m.HandleEvent(mID, proto.Event{Type: proto.EventQuarantineStartFailed, Payload: []byte("{")})
	r.wantAlerts(mID, KindMasterStartBlocked, 2, true)
	ma.send()
	r.wantAlerts(mID, KindMasterStartBlocked, 2, true)
}

// R4-6: no more than 16 entries of Kinds are looked at.
func TestStartStateR4KindsBounded(t *testing.T) {
	for _, junk := range []int{15, 16} {
		r := newRig(t)
		mID, _, ma := r.quarantined(plainQuar())
		var entries []proto.StartKindState
		for i := 0; i < junk; i++ {
			entries = append(entries, sk("weird", "W", 1, false))
		}
		ma.state(append(entries, sk(bypassed, "B1", 1, false))...)
		ma.send()
		want := 0
		if junk < 16 {
			want = 1
		}
		if n := r.count(mID, KindMasterStartBypassed); n != want {
			t.Fatalf("%d entries before the bypass: alerts = %d, want %d", junk, n, want)
		}
	}
}

// 17 (monitor side; the store cascade is TestStartMarksReplaceAndCascade):
// the in-memory mark cache goes with a server the monitor forgets.
func TestStartStateR4MarkCacheDroppedWithServer(t *testing.T) {
	r := newRig(t)
	id := r.server("db-hapus")
	r.m.tick()
	r.m.mu.Lock()
	r.m.markStart(id, KindMasterStartBypassed, "B1")
	cached := r.m.servers[id] != nil && r.m.servers[id].marks[KindMasterStartBypassed] == "B1"
	r.m.mu.Unlock()
	if !cached {
		t.Fatal("the mark is not cached")
	}
	if err := r.st.DeleteServer(id); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	r.m.mu.Lock()
	_, kept := r.m.servers[id]
	r.m.mu.Unlock()
	if kept {
		t.Fatal("the monitor kept the state (and its mark cache) of a deleted server")
	}
	if m, err := r.st.StartMarks(id); err != nil || len(m) != 0 {
		t.Fatalf("marks after the delete = %v, %v", m, err)
	}
}
