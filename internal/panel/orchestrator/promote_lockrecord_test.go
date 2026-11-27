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
// lock; Ulangi long after its deadline relocks, the master's agent refuses
// and the unlock cannot be delivered. The record stands with the times it
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
	g.m.fail("mysql.lockForPromote", "server ini sudah dikunci untuk promosi (kandidat 10.0.0.2:3306); kunci baru ditolak")
	if err := e.r.Retry(id, JobAck{}); err != nil {
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
			tc.script(g)
			if err := e.r.Retry(id, JobAck{}); err != nil {
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
