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
		"mysql_unreachable agent_offline disk_warn disk_crit connections_high binlog_deadline job_failed"
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
		{96, true, true}, {91, true, true}, {89, true, false}, {50, false, false},
	}
	for _, s := range steps {
		usedPct(s.pct)
		if w, c := r.isOpen(mID, KindDiskWarn), r.isOpen(mID, KindDiskCrit); w != s.warn || c != s.crit {
			t.Fatalf("at %d%%: disk_warn open %v, disk_crit open %v; want %v, %v", s.pct, w, c, s.warn, s.crit)
		}
	}
	if n := len(r.alerts(mID, KindDiskWarn)); n != 2 {
		t.Fatalf("disk_warn alerts = %d, want 2", n)
	}
	if a := r.alerts(mID, KindDiskCrit); len(a) != 1 || !strings.Contains(a[0].Message, "96%") {
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
	if a := r.openAlert(mID, KindMasterLocked); !strings.Contains(a.Message, "super_read_only") {
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
	r.m.JobFailed(rID, "job-7", "resync_replica", "boom")
	if err := r.st.SetReplicaState(rID, store.ReplicaResyncing, ""); err != nil {
		t.Fatal(err)
	}
	r.m.tick()
	if r.isOpen(rID, KindReplicaStopped) {
		t.Fatal("replica_stopped open while the replica resyncs")
	}
	r.openAlert(rID, KindJobFailed)
}

func TestJobFailedAlert(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	var changed atomic.Int32
	r.m.onAlertsChanged = func() { changed.Add(1) }

	r.m.JobFailed(rID, "job-1", "create_replica", "step import: Error 1062: Duplicate entry 'Budi'")
	a := r.openAlert(rID, KindJobFailed)
	if !strings.Contains(a.Message, "Buat replika") || !strings.Contains(a.Message, "gagal") ||
		strings.Contains(a.Message, "Budi") || strings.Contains(a.Message, "1062") || a.TopologyID != topo.ID {
		t.Fatalf("job_failed = %+v", a)
	}
	if ups := r.alertUpdates(rID, KindJobFailed); len(ups) != 1 || ups[0]["open"] != true {
		t.Fatalf("alert_update = %v", ups)
	}
	if changed.Load() != 1 {
		t.Fatalf("OnAlertsChanged called %d times, want 1", changed.Load())
	}

	// A second failure keeps one alert, now about the newer job.
	r.m.JobFailed(rID, "job-2", "add_databases", "boom")
	if as := r.alerts(rID, KindJobFailed); len(as) != 1 || !strings.Contains(as[0].Message, "Tambah database") {
		t.Fatalf("after a second failure = %+v", as)
	}
	// The older job ending leaves the newer failure open.
	r.m.JobEnded(rID, "job-1")
	r.openAlert(rID, KindJobFailed)
	r.m.JobEnded(rID, "job-2")
	if r.isOpen(rID, KindJobFailed) {
		t.Fatal("job_failed open after its job ended")
	}
	if ups := r.alertUpdates(rID, KindJobFailed); len(ups) != 2 || ups[1]["open"] != false {
		t.Fatalf("alert_update = %v, want open then closed", ups)
	}
	r.m.JobEnded(rID, "job-2") // nothing open: no-op
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
