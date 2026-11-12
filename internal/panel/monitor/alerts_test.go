package monitor

import (
	"encoding/json"
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

	// An unreadable payload is taken (ignored), never passed on.
	if !r.m.HandleEvent(mID, proto.Event{Type: proto.EventLockExpired, Payload: []byte("{")}) {
		t.Fatal("unreadable monitor.lockExpired not taken")
	}
	if len(r.alerts(mID, KindMasterLockExpired)) != 2 {
		t.Fatal("unreadable payload opened an alert")
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
		"Untuk membuka kunci sekarang: sudo dbpanel agent unlock-master"
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
