package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// textP9 is the job log line of a lock record proven gone by the master's
// live report (P9, not in the spec).
const textP9 = "Kunci promote terbukti sudah tidak ada: master melapor bisa ditulis sesudah tenggat kunci."

// forget makes the Monitor know nothing of id (no report at all).
func (h *fakeHealth) forget(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.views, id)
	delete(h.green, id)
}

// plantPlanned stores a failed planned promotion of the rig's master to
// its candidate, with its job's params as paramsJSON (fmt verbs %d for the
// master, the candidate and the promotion id, in that order), and returns
// the job and promotion ids. finished is the promotion's FinishedAt (nil:
// none).
func (g *promoteRig) plantPlanned(paramsJSON string, finished *time.Time) (string, int64) {
	e := g.e
	e.t.Helper()
	now := time.Now()
	promID, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromotePlanned, OldMasterServerID: g.m.id,
		NewMasterServerID: g.c.id, StartedAt: now.Add(-time.Hour), FinishedAt: finished, Status: promotionFailed})
	if err != nil {
		e.t.Fatal(err)
	}
	id := fmt.Sprintf("planted%025d", promID)
	params := fmt.Sprintf(paramsJSON, g.m.id, g.c.id, promID)
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindPromotePlanned, ServerID: g.m.id, TopologyID: g.topo.ID,
		ParamsJSON: params, CreatedAt: now.Add(-time.Hour)}, promotePlannedStepNames()); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, 2, "kandidat belum menerapkan semua transaksi master", now); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.st.GetPromotion(promID)
	if err != nil {
		e.t.Fatal(err)
	}
	p.JobID = id
	if err := e.st.UpdatePromotion(p); err != nil {
		e.t.Fatal(err)
	}
	return id, promID
}

// plantStanding is plantPlanned of a record that stands: the lock tried at
// tried, its deadline 30 minutes later, never confirmed open.
func (g *promoteRig) plantStanding(tried time.Time) (string, int64) {
	return g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried":true,`+
		`"lock_tried_at":"`+tried.UTC().Format(time.RFC3339Nano)+`","lock_deadline":"`+
		tried.Add(30*time.Minute).UTC().Format(time.RFC3339Nano)+`"}`, nil)
}

func (g *promoteRig) lockRecords() []LockRecord {
	g.e.t.Helper()
	recs, err := g.e.r.LockRecords(g.m.id)
	if err != nil {
		g.e.t.Fatalf("LockRecords: %v", err)
	}
	return recs
}

func recordJobs(recs []LockRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.JobID
	}
	return out
}

// TestLockDeadlineClamped (SEC-290): a deadline the master's agent answers
// far ahead is cut to the lock's request time + promoteLockSec +
// lockCallSlack + promoteClockSkew; the cut value is what the candidate
// is sent.
func TestLockDeadlineClamped(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	now := time.Now()
	g.m.reply("mysql.lockForPromote", proto.LockForPromoteResult{LockedAt: now, Deadline: now.Add(10 * time.Hour),
		ServerUUID: masterUUID, GTIDExecuted: masterGTID})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	st := e.storedState(id)
	if st.LockTriedAt == nil || st.LockDeadline == nil {
		t.Fatalf("job state = %+v", st)
	}
	want := st.LockTriedAt.Add(40 * time.Minute)
	if !st.LockDeadline.Equal(want) {
		t.Fatalf("stored deadline %v, want %v (tried + 40 min)", st.LockDeadline, want)
	}
	if got := lockDeadlineCap(*st.LockTriedAt); !got.Equal(want) {
		t.Fatalf("lockDeadlineCap = %v, want %v", got, want)
	}
	var pp proto.PromoteParams
	g.c.params("mysql.promote", &pp)
	if pp.LockDeadline == nil || !pp.LockDeadline.Equal(want) {
		t.Fatalf("mysql.promote deadline %v, want %v", pp.LockDeadline, want)
	}
	if pp.LockRemainingSec == nil || *pp.LockRemainingSec > 2400 {
		t.Fatalf("mysql.promote seconds left %v, want at most 2400", pp.LockRemainingSec)
	}
}

// TestStoredLockDeadlineClampedAtRead: a job stored by 0.2.0 with a
// deadline a year ahead is cut the same way when read.
func TestStoredLockDeadlineClampedAtRead(t *testing.T) {
	g := newPromoteRig(t)
	tried := time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	id, promID := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"candidate_port":3306,`+
		`"lock_deadline":"`+tried.AddDate(1, 0, 0).Format(time.RFC3339)+`","last_master_gtid":"`+masterGTID+`",`+
		`"lock_tried":true,"lock_tried_at":"`+tried.Format(time.RFC3339)+`"}`, nil)
	recs := g.lockRecords()
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one", recs)
	}
	r := recs[0]
	want := tried.Add(40*time.Minute + 60*time.Second)
	if r.JobID != id || r.PromotionID != promID || r.MasterID != g.m.id || r.CandidateID != g.c.id || r.Sent || !r.Until.Equal(want) {
		t.Fatalf("record = %+v, want job %s, promotion %d, until %v", r, id, promID, want)
	}
}

// TestLockRecordFromOldJobWithoutTimes: a lock tried without its times
// (an older job) counts from the promotion's end, as before.
func TestLockRecordFromOldJobWithoutTimes(t *testing.T) {
	g := newPromoteRig(t)
	finished := time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried":true}`, &finished)
	recs := g.lockRecords()
	want := finished.Add(1800*time.Second + 60*time.Second)
	if len(recs) != 1 || !recs[0].Until.Equal(want) {
		t.Fatalf("records = %+v, want one until %v", recs, want)
	}
}

// TestRelockRefusalKeepsRecordAndTimes (SEC-289, S-1): the guard kept the
// lock; Ulangi is refused while the record stands (T12b); once it was
// settled open, Ulangi long after its deadline relocks, the master's agent
// refuses and the unlock cannot be delivered. The record stands with the times it
// had before Ulangi, so the clock window does not move: an emergency
// promotion after it is not refused by the window.
func TestRelockRefusalKeepsRecordAndTimes(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	g.m.on("mysql.unlockPromote", func(json.RawMessage) (any, error) {
		return nil, &proto.Error{Code: proto.CodeOffline, Message: "agent offline"}
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	before := e.storedState(id)
	if !before.LockTried || before.LockUnlocked || before.LockTriedAt == nil || before.LockDeadline == nil {
		t.Fatalf("job state = %+v, want a lock not confirmed open", before)
	}

	later := before.LockDeadline.Add(time.Hour)
	e.r.now = func() time.Time { return later }
	// While the record stands Ulangi is refused (T12b); once it was
	// settled open (a live report of the master past its Until) Ulangi
	// relocks.
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrPromoteLockStands) {
		t.Fatalf("Retry while the record stands = %v, want T12b", err)
	}
	e.setState(id, func(st *jobState) { st.LockUnlocked = true })
	g.reportsAt(later) // the reports its start would judge, fresh
	g.m.fail("mysql.lockForPromote", "server ini sudah dikunci untuk promosi (kandidat 10.0.0.2:3306); kunci baru ditolak")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if n := g.m.count("mysql.lockForPromote"); n != 2 {
		t.Fatalf("lockForPromote sent %d times, want 2", n)
	}
	after := e.storedState(id)
	if !after.LockTried || after.LockUnlocked {
		t.Fatalf("job state after the refused relock = %+v, want the record kept", after)
	}
	if after.LockTriedAt == nil || !after.LockTriedAt.Equal(*before.LockTriedAt) ||
		after.LockDeadline == nil || !after.LockDeadline.Equal(*before.LockDeadline) {
		t.Fatalf("times after the refused relock: tried %v, deadline %v; want %v, %v",
			after.LockTriedAt, after.LockDeadline, before.LockTriedAt, before.LockDeadline)
	}

	// The master goes away; an emergency promotion of b is not held back by
	// the window, which ended at the first deadline plus the grace.
	g.offline(g.m)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
		v := replicaView(masterGTID, masterGTID)
		v.ReceivedAt = later
		g.h.set(fa.id, v)
	}
	g.b.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: proto.PromoteEmergency}, nil
	})
	g.b.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: masterDBs})
	eid, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.b.id})
	if err != nil {
		t.Fatalf("emergency promote after the refused relock: %v", err)
	}
	e.waitJob(eid, store.JobSucceeded)
}

// TestLockAnsweredErrorDoesNotClearRecord (S-1): the master's agent
// answers the lock with an error after the lock was taken, and the unlock
// fails: the record stands.
func TestLockAnsweredErrorDoesNotClearRecord(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.m.on("mysql.lockForPromote", func(json.RawMessage) (any, error) {
		return nil, errors.New("master sudah dikunci (kunci promosi tetap), tetapi server_uuid/gtid_executed gagal dibaca: i/o timeout")
	})
	g.m.on("mysql.unlockPromote", func(json.RawMessage) (any, error) {
		return nil, errors.New("kunci promosi tidak bisa dibuka: MySQL tidak menjawab")
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	st := e.storedState(id)
	if !st.LockTried || st.LockUnlocked || st.LockTriedAt == nil {
		t.Fatalf("job state = %+v, want the record standing", st)
	}
	recs := g.lockRecords()
	if len(recs) != 1 || recs[0].JobID != id {
		t.Fatalf("records = %+v, want the job's", recs)
	}
	if want := st.LockTriedAt.Add(promoteLockSec*time.Second + lockCallSlack + proto.PromoteLockGrace); !recs[0].Until.Equal(want) {
		t.Fatalf("until %v, want %v", recs[0].Until, want)
	}
}

// TestLockRecordsReadEveryPlannedPromotion: of two failed planned
// promotions of the master, the older stands and the newer was proven
// open; the older is read, and an emergency promotion inside its window
// is refused.
func TestLockRecordsReadEveryPlannedPromotion(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	old, _ := g.plantStanding(time.Now().Add(-time.Minute))
	tried := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried_at":"`+tried+`",`+
		`"lock_unlocked":true}`, nil)
	if got := recordJobs(g.lockRecords()); len(got) != 1 || got[0] != old {
		t.Fatalf("records %v, want only %s", got, old)
	}
	g.offline(g.m)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	_, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.b.id})
	if err == nil || !strings.Contains(err.Error(), "mungkin masih terkunci") {
		t.Fatalf("emergency promote inside the older record's window = %v, want refused", err)
	}
	if g.b.count("mysql.promote") != 0 {
		t.Fatal("candidate asked to promote")
	}
}

func TestLockRecordNotCountedAfterMasterChanged(t *testing.T) {
	g := newPromoteRig(t)
	g.plantStanding(time.Now().Add(-time.Minute))
	if n := len(g.lockRecords()); n != 1 {
		t.Fatalf("records before the change = %d, want 1", n)
	}
	if err := g.e.st.SwapMaster(g.topo.ID, g.m.id, g.b.id, "10.0.0.3", 3306); err != nil {
		t.Fatal(err)
	}
	if recs := g.lockRecords(); len(recs) != 0 {
		t.Fatalf("records after the master changed = %+v, want none", recs)
	}
}

// laterPromotion adds a promotion of the topology after the planted ones,
// with status.
func (g *promoteRig) laterPromotion(status string) {
	g.e.t.Helper()
	if _, err := g.e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromoteEmergency, OldMasterServerID: g.m.id,
		NewMasterServerID: g.b.id, StartedAt: time.Now(), Status: status}); err != nil {
		g.e.t.Fatal(err)
	}
}

func TestLockRecordNotCountedAfterLaterPromotionDone(t *testing.T) {
	g := newPromoteRig(t)
	g.plantStanding(time.Now().Add(-time.Minute))
	g.laterPromotion(promotionDone)
	if recs := g.lockRecords(); len(recs) != 0 {
		t.Fatalf("records after a later promotion was done = %+v, want none", recs)
	}
}

func TestLockRecordCountedWhileLaterPromotionPartial(t *testing.T) {
	for _, status := range []string{promotionPartial, promotionRunning, promotionFailed} {
		t.Run(status, func(t *testing.T) {
			g := newPromoteRig(t)
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			g.laterPromotion(status)
			if got := recordJobs(g.lockRecords()); len(got) != 1 || got[0] != id {
				t.Fatalf("records with a later %s promotion = %v, want %s", status, got, id)
			}
		})
	}
}

// TestLockRecordClearedByLiveReport (N-3): a live report of the master
// with both read-only flags off proves the lock gone, but only past the
// record's Until; settleLockRecords stores it and logs P9. A restored
// report, no report, an agent away, or read_only on keep the record.
func TestLockRecordClearedByLiveReport(t *testing.T) {
	open := func(at time.Time) monitor.View {
		return monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: at,
			Snap: &proto.HealthSnapshot{MySQLOK: true, GTIDExecuted: masterGTID}}
	}
	type rig struct {
		g     *promoteRig
		id    string
		until time.Time
	}
	setup := func(t *testing.T) rig {
		g := newPromoteRig(t)
		id, _ := g.plantStanding(time.Now().Add(-time.Minute))
		recs := g.lockRecords()
		if len(recs) != 1 {
			t.Fatalf("records = %+v", recs)
		}
		return rig{g, id, recs[0].Until}
	}
	stands := func(t *testing.T, x rig, what string) {
		t.Helper()
		if got := recordJobs(x.g.lockRecords()); len(got) != 1 || got[0] != x.id {
			t.Fatalf("%s: records %v, want %s standing", what, got, x.id)
		}
		x.g.e.r.startMu.Lock()
		recs, err := x.g.e.r.settleLockRecords(x.g.m.id)
		x.g.e.r.startMu.Unlock()
		if err != nil || len(recs) != 1 {
			t.Fatalf("%s: settleLockRecords = %+v, %v; want the record", what, recs, err)
		}
		if st := x.g.e.storedState(x.id); st.LockUnlocked {
			t.Fatalf("%s: LockUnlocked stored", what)
		}
		if strings.Contains(x.g.e.jobLog(x.id), textP9) {
			t.Fatalf("%s: P9 logged", what)
		}
	}

	t.Run("before Until", func(t *testing.T) {
		x := setup(t)
		at := x.until.Add(-time.Second)
		x.g.e.r.now = func() time.Time { return at }
		x.g.h.set(x.g.m.id, open(at))
		stands(t, x, "live report, both flags off, before Until")
	})
	t.Run("after Until", func(t *testing.T) {
		x := setup(t)
		e := x.g.e
		at := x.until
		e.r.now = func() time.Time { return at }
		x.g.h.set(x.g.m.id, open(at))
		if recs := x.g.lockRecords(); len(recs) != 0 {
			t.Fatalf("records past Until with a live open report = %+v, want none", recs)
		}
		e.r.startMu.Lock()
		recs, err := e.r.settleLockRecords(x.g.m.id)
		e.r.startMu.Unlock()
		if err != nil || len(recs) != 0 {
			t.Fatalf("settleLockRecords = %+v, %v; want none", recs, err)
		}
		if st := e.storedState(x.id); !st.LockUnlocked {
			t.Fatalf("job state = %+v, want LockUnlocked stored", st)
		}
		if log := e.jobLog(x.id); strings.Count(log, textP9) != 1 {
			t.Fatalf("job log lacks P9 once:\n%s", log)
		}
	})
	for _, tc := range []struct {
		name string
		set  func(x rig, at time.Time)
	}{
		{"restored report", func(x rig, at time.Time) {
			v := open(at)
			v.Restored = true
			x.g.h.set(x.g.m.id, v)
		}},
		{"no report", func(x rig, at time.Time) { x.g.h.forget(x.g.m.id) }},
		{"agent offline", func(x rig, at time.Time) {
			x.g.h.set(x.g.m.id, open(at))
			x.g.m.detach()
		}},
		{"read_only on", func(x rig, at time.Time) {
			v := open(at)
			v.Snap.ReadOnly = true
			x.g.h.set(x.g.m.id, v)
		}},
		{"super_read_only on", func(x rig, at time.Time) {
			v := open(at)
			v.Snap.SuperReadOnly = true
			x.g.h.set(x.g.m.id, v)
		}},
		{"stale report", func(x rig, at time.Time) { x.g.h.set(x.g.m.id, open(at.Add(-time.Hour))) }},
		{"MySQL down", func(x rig, at time.Time) {
			v := open(at)
			v.Snap.MySQLOK = false
			x.g.h.set(x.g.m.id, v)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := setup(t)
			at := x.until.Add(time.Minute)
			x.g.e.r.now = func() time.Time { return at }
			tc.set(x, at)
			stands(t, x, tc.name)
		})
	}
}

// TestLockRecordsWritesNothing (K10): LockRecords proves a record gone
// without storing it; the page reads it (GET) and never writes.
func TestLockRecordsWritesNothing(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	j, _, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour)
	e.r.now = func() time.Time { return at }
	g.h.set(g.m.id, monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: at,
		Snap: &proto.HealthSnapshot{MySQLOK: true}})
	if recs := g.lockRecords(); len(recs) != 0 {
		t.Fatalf("records = %+v, want none", recs)
	}
	after, _, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.ParamsJSON != j.ParamsJSON {
		t.Fatalf("params changed by LockRecords:\n%s\n%s", j.ParamsJSON, after.ParamsJSON)
	}
	if log := e.jobLog(id); log != "" {
		t.Fatalf("LockRecords wrote to the job log:\n%s", log)
	}
}

// TestRelockErrorMayHaveLockedCountsFromRetry: a relock the master's agent
// answers with an error that is not a refusal (it can come after the lock
// was set), or that no answer comes back for, may have locked the master
// now: the record counts from the relock's time, not from the earlier
// deadline, and an emergency promotion inside that window is refused.
func TestRelockErrorMayHaveLockedCountsFromRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(g *promoteRig)
	}{
		{"agent error after the lock", func(g *promoteRig) {
			g.m.on("mysql.lockForPromote", func(json.RawMessage) (any, error) {
				return nil, errors.New("master sudah dikunci (kunci promosi tetap), tetapi server_uuid/gtid_executed gagal dibaca: i/o timeout")
			})
		}},
		{"no answer", func(g *promoteRig) {
			g.m.mu.Lock()
			g.m.dropReply = map[string]bool{"mysql.lockForPromote": true}
			g.m.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			e.r.promoteCatchUp = 100 * time.Millisecond
			g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
				Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
			g.m.on("mysql.unlockPromote", func(json.RawMessage) (any, error) {
				return nil, &proto.Error{Code: proto.CodeOffline, Message: "agent offline"}
			})
			id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
			if err != nil {
				t.Fatalf("StartPromote: %v", err)
			}
			e.waitJob(id, store.JobFailed)
			e.waitIdle(id)
			before := e.storedState(id)
			later := before.LockDeadline.Add(time.Hour)
			e.r.now = func() time.Time { return later }
			g.reportsAt(later)
			tc.script(g)
			// Settled open (a live report past its Until), so Ulangi
			// relocks (T12b refuses it while the record stands).
			e.setState(id, func(st *jobState) { st.LockUnlocked = true })
			if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
				t.Fatalf("Retry: %v", err)
			}
			e.waitJob(id, store.JobFailed)
			e.waitIdle(id)
			after := e.storedState(id)
			if !after.LockTried || after.LockUnlocked || after.LockTriedAt == nil || !after.LockTriedAt.Equal(later) ||
				after.LockDeadline != nil {
				t.Fatalf("job state after the relock = %+v, want tried at %v, no deadline", after, later)
			}
			recs := g.lockRecords()
			want := later.Add(promoteLockSec*time.Second + lockCallSlack + proto.PromoteLockGrace)
			if len(recs) != 1 || !recs[0].Until.Equal(want) {
				t.Fatalf("records = %+v, want one until %v", recs, want)
			}
			if err := e.r.refuseWhileLockMayStand(g.m.id, verbEmergency); err == nil ||
				!strings.Contains(err.Error(), "mungkin masih terkunci") {
				t.Fatalf("emergency inside the relock's window = %v, want refused", err)
			}
		})
	}
}

func TestLockRefused(t *testing.T) {
	agent := func(code string) error {
		return &hub.AgentError{Server: 1, Err: &proto.Error{Code: code, Message: "x"}}
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"bad_params", agent(proto.CodeBadParams), true},
		{"bad_params labelled", agentError(agent(proto.CodeBadParams), func(int64) string { return "m" }), true},
		{"unknown command", agent(proto.CodeUnknownCommand), true},
		{"internal", agent(proto.CodeInternal), false},
		{"internal labelled", agentError(agent(proto.CodeInternal), func(int64) string { return "m" }), false},
		{"hub timeout", &proto.Error{Code: proto.CodeTimeout, Message: "batas waktu"}, false},
		{"hub bad_params without an agent", &proto.Error{Code: proto.CodeBadParams, Message: "x"}, false},
		{"plain", errors.New("x"), false},
	} {
		if got := lockRefused(tc.err); got != tc.want {
			t.Errorf("%s: lockRefused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ---- refusals while a lock record stands (spec 5.2, Task 4) ----

// wantT15b is T15b for what, naming the promote job.
func wantT15b(what, job string) string {
	return "Master db-master mungkin masih terkunci oleh promote terencana sebelumnya. " + what +
		" tidak dijalankan selama itu: memuat data membuat replika bisa ditulis sebentar, dan penjaga kunci " +
		`master bisa mengkarantina master. Tekan "Buka kunci promote" pada pekerjaan promote ` + job + " dulu."
}

// wantT12c is T12c naming the promote job.
func wantT12c(job string) string {
	return `Master db-master masih terkunci, atau kunci promote terencana sebelumnya belum terbukti dibuka. ` +
		`Tekan "Buka kunci promote" pada pekerjaan promote ` + job + ` dulu, lalu mulai promote ini lagi.`
}

// wantT12d is T12d.
const wantT12d = `Master db-master masih terkunci. Buka kuncinya dulu dengan "Buka kunci master" di halaman master, ` +
	`lalu mulai promote ini lagi.`

// plantSentAt is plantStanding of a job whose mysql.promote was sent.
func (g *promoteRig) plantSentAt(tried time.Time) (string, int64) {
	return g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried":true,`+
		`"promote_sent":true,"lock_tried_at":"`+tried.UTC().Format(time.RFC3339Nano)+`","lock_deadline":"`+
		tried.Add(30*time.Minute).UTC().Format(time.RFC3339Nano)+`"}`, nil)
}

// lockedMaster is a live report of the master reading Terkunci.
func lockedMaster() monitor.View {
	return monitor.View{Status: monitor.Status{Code: monitor.StatusLocked}, ReceivedAt: time.Now(),
		Snap: &proto.HealthSnapshot{MySQLOK: true, GTIDExecuted: masterGTID, ReadOnly: true, SuperReadOnly: true}}
}

// loadStart is a load of the rig's topology, started or retried.
type loadStart struct {
	what, how string
	run       func() error
}

// loadStarts are the loads of the rig's topology (spec 5.2: Resync ulang,
// Buat replika, Tambah database, Lepas database with its data), each
// started and retried ("Ulangi" of a failed job of its kind).
func (g *promoteRig) loadStarts() []loadStart {
	e := g.e
	t := e.t
	sub := e.agent("rep-sub", "10.0.0.5")
	if _, err := e.st.AddReplica(g.topo.ID, sub.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(sub.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	target := e.agent("baru", "10.0.0.6")
	retry := func(id, kind string, serverID int64, params string) func() error {
		if err := e.st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: serverID, TopologyID: g.topo.ID, ParamsJSON: params,
			CreatedAt: time.Now()}, []string{"x"}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(id, store.JobFailed, 0, "gagal", time.Now()); err != nil {
			t.Fatal(err)
		}
		return func() error { return e.r.Retry(id, JobAck{}) }
	}
	ctx := context.Background()
	return []loadStart{
		{"Resync ulang", "mulai", func() error {
			_, err := e.r.StartResync(ctx, ResyncParams{ServerID: g.b.id})
			return err
		}},
		{"Resync ulang", "Ulangi", retry("failed-resync", KindResyncReplica, g.b.id, "{}")},
		{"Buat replika", "mulai", func() error {
			_, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.m.id, TargetServerID: target.id,
				Databases: []string{"a"}})
			return err
		}},
		{"Buat replika", "Ulangi", retry("failed-create", KindCreateReplica, target.id, "{}")},
		{"Tambah database", "mulai", func() error {
			_, err := e.r.StartAddDatabases(ctx, sub.id, []string{"b"})
			return err
		}},
		{"Tambah database", "Ulangi", retry("failed-add", KindAddDatabases, sub.id, "{}")},
		{"Lepas database", "mulai", func() error {
			_, err := e.r.StartRemoveDatabases(ctx, sub.id, []string{"a"}, true)
			return err
		}},
		{"Lepas database", "Ulangi", retry("failed-drop", KindRemoveDatabases, sub.id, `{"drop_data":true}`)},
	}
}

// wantLoadRefused checks err is T15b for s naming job.
func wantLoadRefused(t *testing.T, s loadStart, err error, job, when string) {
	t.Helper()
	want := wantT15b(s.what, job)
	ls, ok := errors.AsType[*LockStandsError](err)
	if !ok || err.Error() != want || ls.Code != "load" || ls.What != s.what || ls.JobID != job {
		t.Errorf("%s %s %s: %v (%T), want %q", s.what, s.how, when, err, err, want)
	}
}

// wantPastLockCheck checks err is not a lock refusal (the start went on to
// its next check, or started).
func wantPastLockCheck(t *testing.T, what string, err error) {
	t.Helper()
	if _, ok := errors.AsType[*LockStandsError](err); ok || (err != nil && strings.Contains(err.Error(), "terkunci")) {
		t.Errorf("%s: %v, want past the lock check", what, err)
	}
}

// TestLoadRefusedWhileLockRecordStands (5.13 guard): a lock kept by a
// failed planned promote, two hours past its deadline, the master reading
// Terkunci: every load, started or retried, is refused with T15b naming
// that job; a job whose mysql.promote was sent holds them the same way.
func TestLoadRefusedWhileLockRecordStands(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(g *promoteRig, tried time.Time) (string, int64)
	}{
		{"not sent", (*promoteRig).plantStanding},
		{"sent", (*promoteRig).plantSentAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			id, _ := tc.plant(g, time.Now().Add(-30*time.Minute-2*time.Hour))
			g.h.set(g.m.id, lockedMaster())
			for _, s := range g.loadStarts() {
				wantLoadRefused(t, s, s.run(), id, "two hours past the deadline")
			}
			if st := g.e.storedState(id); st.LockUnlocked {
				t.Fatal("record stored as unlocked")
			}
		})
	}
}

// TestLoadRefusedWithoutLiveMasterReport (5.13 guard, N-2): past the
// window, with no live report of the master (restored at startup, none at
// all, its agent away), Resync ulang and Ulangi of a failed resync are
// refused: the record is not proven gone.
func TestLoadRefusedWithoutLiveMasterReport(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(g *promoteRig)
	}{
		{"restored report", func(g *promoteRig) {
			v := healthyView(monitor.StatusHealthy, masterGTID, nil)
			v.Restored = true
			g.h.set(g.m.id, v)
		}},
		{"no report", func(g *promoteRig) { g.h.forget(g.m.id) }},
		{"agent disconnected", func(g *promoteRig) { g.m.detach() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			id, _ := g.plantStanding(time.Now().Add(-2 * time.Hour))
			tc.set(g)
			for _, s := range g.loadStarts() {
				if s.what == "Resync ulang" {
					wantLoadRefused(t, s, s.run(), id, tc.name)
				}
			}
		})
	}
}

// TestLoadRefusedInsideWindowDespiteOpenReport (N-2): inside the window a
// load is refused whatever the report says, also a live report with both
// read-only flags off.
func TestLoadRefusedInsideWindowDespiteOpenReport(t *testing.T) {
	g := newPromoteRig(t)
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	g.h.set(g.m.id, healthyView(monitor.StatusHealthy, masterGTID, nil))
	for _, s := range g.loadStarts() {
		wantLoadRefused(t, s, s.run(), id, "inside the window")
	}
}

// TestLoadAllowedAfterLiveOpenReport: past the window a live report of the
// master with both flags off proves the record gone (stored, P9); a
// resync and a new planned promote are let through.
func TestLoadAllowedAfterLiveOpenReport(t *testing.T) {
	t.Run("resync", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, _ := g.plantStanding(time.Now().Add(-2 * time.Hour))
		starts := g.loadStarts()
		// A busy b shows the resync got past the lock check without
		// starting a job.
		if err := e.st.CreateJob(store.Job{ID: "busy-b", Kind: KindMySQLTLS, ServerID: g.b.id, ParamsJSON: "{}",
			CreatedAt: time.Now()}, []string{"x"}); err != nil {
			t.Fatal(err)
		}
		for _, s := range starts[:2] {
			err := s.run()
			wantPastLockCheck(t, s.what+" "+s.how, err)
			if err == nil || !strings.Contains(err.Error(), "pekerjaan") {
				t.Errorf("%s %s: %v, want refused by b's busy job", s.what, s.how, err)
			}
		}
		if st := e.storedState(id); !st.LockUnlocked {
			t.Fatal("record not stored as gone")
		}
		if log := e.jobLog(id); strings.Count(log, textP9) != 1 {
			t.Fatalf("job log lacks P9 once:\n%s", log)
		}
	})
	t.Run("planned promote", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		g.plantStanding(time.Now().Add(-2 * time.Hour))
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
	})
}

// TestLoadIgnoresRecordOfFormerMaster: a record of a job whose master was
// replaced since holds nothing back.
func TestLoadIgnoresRecordOfFormerMaster(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.plantStanding(time.Now().Add(-time.Minute))
	if err := e.st.SwapMaster(g.topo.ID, g.m.id, g.b.id, "10.0.0.3", 3306); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: "busy-d", Kind: KindMySQLTLS, ServerID: g.d.id, ParamsJSON: "{}",
		CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: "failed-resync-d", Kind: KindResyncReplica, ServerID: g.d.id, TopologyID: g.topo.ID,
		ParamsJSON: "{}", CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob("failed-resync-d", store.JobFailed, 0, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: g.d.id})
	wantPastLockCheck(t, "Resync ulang of d", err)
	if err == nil || !strings.Contains(err.Error(), "pekerjaan") {
		t.Errorf("Resync ulang of d: %v, want refused by d's busy job", err)
	}
	err = e.r.Retry("failed-resync-d", JobAck{})
	wantPastLockCheck(t, "Ulangi of d's resync", err)
	if err == nil || !strings.Contains(err.Error(), "pekerjaan lain") {
		t.Errorf("Ulangi of d's resync: %v, want refused by d's busy job", err)
	}
	e.r.startMu.Lock()
	defer e.r.startMu.Unlock()
	for _, id := range []int64{g.m.id, g.b.id} {
		if err := e.r.refusePlannedWhileLockStands(id); err != nil {
			t.Errorf("planned refusal for %d: %v", id, err)
		}
	}
}

// TestPlannedStartRefusedWhileLockStands (S-2): a new planned promote of a
// master whose record stands is refused at its start with T12c naming
// the newest such job, inside the window, past it with the master
// Terkunci, and for a job whose mysql.promote was sent; nothing is sent.
func TestPlannedStartRefusedWhileLockStands(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tried  time.Duration
		plant  func(g *promoteRig, tried time.Time) (string, int64)
		locked bool
	}{
		{"inside the window", -time.Minute, (*promoteRig).plantStanding, false},
		{"past the window, master Terkunci", -2 * time.Hour, (*promoteRig).plantStanding, true},
		{"sent", -time.Minute, (*promoteRig).plantSentAt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			g.plantStanding(time.Now().Add(-3 * time.Hour)) // older: not the one named
			id, promID := tc.plant(g, time.Now().Add(tc.tried))
			if tc.locked {
				g.h.set(g.m.id, lockedMaster())
			}
			_, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
			ls, ok := errors.AsType[*LockStandsError](err)
			if !ok || ls.Code != "planned" || ls.JobID != id || err.Error() != wantT12c(id) {
				t.Fatalf("StartPromote = %v (%T), want %q", err, err, wantT12c(id))
			}
			if n := g.m.count("mysql.lockForPromote"); n != 0 {
				t.Fatalf("lockForPromote sent %d times", n)
			}
			if p := g.promotion(); p.ID != promID {
				t.Fatalf("latest promotion = %+v, want the planted one", p)
			}
		})
	}
}

// TestPlannedStartRefusedWhileMasterLocked (S-2): no record, but the
// master reads Terkunci: T12d, without a job.
func TestPlannedStartRefusedWhileMasterLocked(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.h.set(g.m.id, lockedMaster())
	_, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	ls, ok := errors.AsType[*LockStandsError](err)
	if !ok || ls.Code != "master_locked" || ls.JobID != "" || err.Error() != wantT12d {
		t.Fatalf("StartPromote = %v (%T), want %q", err, err, wantT12d)
	}
	if n := g.m.count("mysql.lockForPromote"); n != 0 {
		t.Fatalf("lockForPromote sent %d times", n)
	}
}

// TestEmergencyKeepsClockRuleOnly (T-4): an emergency promote is refused
// only until the record's Until; past it, with the master not reporting at
// all, it starts although the record stands.
func TestEmergencyKeepsClockRuleOnly(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _ := g.plantStanding(time.Now().Add(-2 * time.Hour))
	g.offline(g.m)
	g.h.forget(g.m.id)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	if got := recordJobs(g.lockRecords()); len(got) != 1 || got[0] != id {
		t.Fatalf("records = %v, want %s standing", got, id)
	}
	job, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
		CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("emergency promote past the record's Until: %v", err)
	}
	if j, _ := e.waitJob(job, store.JobSucceeded); j.Kind != KindPromoteEmergency {
		t.Fatalf("kind = %s", j.Kind)
	}
}

// plantRecordOf stores a failed planned promotion of masterID (topology
// topo) to candidateID whose lock record stands, its lock tried a minute
// ago, and returns its job id.
func (e *env) plantRecordOf(topo store.Topology, masterID, candidateID int64) string {
	e.t.Helper()
	now := time.Now()
	tried := now.Add(-time.Minute).UTC()
	promID, err := e.st.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: proto.PromotePlanned, OldMasterServerID: masterID,
		NewMasterServerID: candidateID, StartedAt: now.Add(-time.Hour), Status: promotionFailed})
	if err != nil {
		e.t.Fatal(err)
	}
	id := fmt.Sprintf("planted%025d", promID)
	deadline := tried.Add(30 * time.Minute)
	b, err := json.Marshal(jobState{MasterServerID: masterID, CandidateServerID: candidateID, PromotionID: promID,
		LockTried: true, LockTriedAt: &tried, LockDeadline: &deadline})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindPromotePlanned, ServerID: masterID, TopologyID: topo.ID,
		ParamsJSON: string(b), CreatedAt: now.Add(-time.Hour)}, promotePlannedStepNames()); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, 2, "gagal", now); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.st.GetPromotion(promID)
	if err != nil {
		e.t.Fatal(err)
	}
	p.JobID = id
	if err := e.st.UpdatePromotion(p); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// TestApproveLoadRefusedWhileLockRecordStands (review C1): approving a
// waiting load (a resync waiting before its wipe, an add_databases
// waiting for the databases the replica already has) drops and loads on
// the replica, so while a lock record stands it is refused with T15b
// before anything is sent, and the job keeps waiting, unchanged. Once a
// live report of the master past the record's Until proves it gone, the
// same approval goes through.
func TestApproveLoadRefusedWhileLockRecordStands(t *testing.T) {
	for _, tc := range []struct {
		what  string
		setup func(t *testing.T, e *env) (m, r *fakeAgent, topo store.Topology, id string)
		ack   JobAck
	}{
		{"Resync ulang", func(t *testing.T, e *env) (*fakeAgent, *fakeAgent, store.Topology, string) {
			m, topo := e.readyMaster("db-master")
			r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}, {Name: "b", Bytes: 200}}, "a")
			id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
			if err != nil {
				t.Fatal(err)
			}
			j, _ := e.waitJob(id, store.JobWaiting)
			if !strings.Contains(j.ParamsJSON, `"wait_reason":"wipe"`) {
				t.Fatalf("job = %+v, want waiting for its wipe", j)
			}
			return m, r, topo, id
		}, JobAck{Confirmed: true}},
		{"Tambah database", func(t *testing.T, e *env) (*fakeAgent, *fakeAgent, store.Topology, string) {
			m, r, id := waitingAddExisting(t, e)
			topo, err := e.st.GetTopologyByMaster(m.id)
			if err != nil {
				t.Fatal(err)
			}
			return m, r, topo, id
		}, JobAck{Confirmed: true, TypedDatabases: []string{"rekap"}}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			e := newEnv(t)
			m, r, topo, id := tc.setup(t, e)
			e.waitIdle(id)
			cand := e.agent("cand", "10.0.0.9")
			planted := e.plantRecordOf(topo, m.id, cand.id)
			before, steps, err := e.st.GetJob(id)
			if err != nil {
				t.Fatal(err)
			}
			mark, mmark := r.mark(), m.mark()
			err = e.r.Approve(id, tc.ack)
			ls, ok := errors.AsType[*LockStandsError](err)
			if want := wantT15b(tc.what, planted); !ok || ls.Code != "load" || ls.JobID != planted || err.Error() != want {
				t.Fatalf("Approve = %v (%T), want %q", err, err, want)
			}
			wantCmds(t, "replica", r.cmds(mark), nil)
			wantCmds(t, "master", m.cmds(mmark), nil)
			after, afterSteps, err := e.st.GetJob(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != store.JobWaiting || after.ParamsJSON != before.ParamsJSON || after.CurrentStep != before.CurrentStep ||
				afterSteps[after.CurrentStep].Status != steps[before.CurrentStep].Status {
				t.Fatalf("job after the refusal = %+v, want unchanged %+v", after, before)
			}

			// Past the record's Until, a live report of the master with both
			// flags off proves it gone: the same approval goes through.
			e.setState(planted, func(st *jobState) {
				tried := time.Now().Add(-2 * time.Hour)
				deadline := tried.Add(30 * time.Minute)
				st.LockTriedAt, st.LockDeadline = &tried, &deadline
			})
			e.health.set(m.id, healthyView(monitor.StatusHealthy, masterGTID, nil))
			if err := e.r.Approve(id, tc.ack); err != nil {
				t.Fatalf("Approve once the record is gone: %v", err)
			}
			e.waitJob(id, store.JobSucceeded)
			if st := e.storedState(planted); !st.LockUnlocked {
				t.Fatal("record not stored as gone")
			}
		})
	}
}
