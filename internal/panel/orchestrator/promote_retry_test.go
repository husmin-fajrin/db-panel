package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Ulangi of an emergency promote job that failed before mysql.promote was
// sent (PC-1, rulings R43 and R46): refused, so that the operator starts a
// new emergency promote, which reads the mode again and asks for the tick,
// the names and the password. A planned promote job keeps its Ulangi,
// refused only while its lock record stands (T12b; "Buka kunci promote"
// lifts the lock). A job past the point keeps its tokenless Ulangi,
// whatever its kind.

// wantRetryRefused fails unless Retry of id is refused with
// ErrPromoteRetryBeforePoint, the job stays failed, no agent got a
// command, and the job page's RetryRefused says the same.
func wantRetryRefused(t *testing.T, e *env, id string) {
	t.Helper()
	e.waitIdle(id)
	marks := markAll(e)
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrPromoteRetryBeforePoint) {
		t.Fatalf("Retry = %v, want ErrPromoteRetryBeforePoint", err)
	}
	onlyCmds(t, marks)
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after the refused retry = %s, want failed", j.Status)
	}
	if got := e.r.RetryRefused(id); got != ErrPromoteRetryBeforePoint.Error() {
		t.Fatalf("RetryRefused = %q", got)
	}
}

func TestPromoteRetryRefusedText(t *testing.T) {
	const want = `Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: ` +
		`mulai promote baru dari halaman master.`
	if ErrPromoteRetryBeforePoint.Error() != want {
		t.Fatalf("text = %q", ErrPromoteRetryBeforePoint.Error())
	}
}

// An emergency promote whose candidate refused mysql.promote: Ulangi is
// refused and sends nothing; a new emergency start is refused while the
// master is healthy again, refused without the tick, and runs with it.
func TestPromoteEmergencyRetryBeforePointRefused(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	unreachable := func() {
		g.offline(g.m)
		for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
			fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
		}
	}
	unreachable()
	g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return nil, errors.New("master lama masih menjawab; promote darurat ditolak")
	})
	id, err := e.r.StartPromote(ctx, PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	if j, _ := e.waitJob(id, store.JobFailed); j.Kind != KindPromoteEmergency {
		t.Fatalf("kind = %s", j.Kind)
	}
	if p := g.promotion(); p.Status != promotionFailed {
		t.Fatalf("promotion = %s, want failed", p.Status)
	}
	wantRetryRefused(t, e, id)
	if n := g.c.count("mysql.promote"); n != 1 {
		t.Fatalf("mysql.promote sent %d times, want 1", n)
	}

	// The master is back and healthy: the mode check refuses an emergency.
	g.m.attach()
	g.h.set(g.m.id, healthyView(monitor.StatusHealthy, masterGTID, nil))
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: true, Host: "10.0.0.1", Port: 3306})
	}
	if _, err := e.r.StartPromote(ctx, PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.c.id,
		Mode: proto.PromoteEmergency}); err == nil || !strings.Contains(err.Error(), "master masih sehat") {
		t.Fatalf("emergency start with a healthy master = %v, want the mode refusal", err)
	}

	// Gone again: without the tick refused; with it, the new promote runs.
	unreachable()
	g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: proto.PromoteEmergency}, nil
	})
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}); !errors.Is(err, ErrOldMasterAck) {
		t.Fatalf("emergency start without the tick = %v, want ErrOldMasterAck", err)
	}
	id2, err := e.r.StartPromote(ctx, PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id, CandidateServerID: g.c.id,
		AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("new emergency start after the refused retry: %v", err)
	}
	e.waitJob(id2, store.JobSucceeded)
	// The old job is superseded by the new promotion now.
	if reason := e.r.Superseded(id); reason == "" {
		t.Fatal("old job not superseded by the new promotion")
	}
}

// A planned promote that failed in "Replika mengejar" with its lock kept is
// lifted by "Buka kunci promote" now, not by Ulangi:
// TestUnlockPromoteLockAfterKeptLock (promote_unlock_test.go).

// A promote past the point of no return keeps its Ulangi, without a
// token and without the gate (K4), whatever its kind.
func TestPromoteRetryPastPointStillRuns(t *testing.T) {
	t.Run("planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		g.m.fail("mysql.quarantine", "master lama tidak menjawab")
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		e.waitJob(id, store.JobPartial)
		e.waitIdle(id)
		wantRetryOffered(t, e, id)
		e.fullDisk(g.c) // a red row on the new master changes nothing
		g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry past the point: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
	})
	t.Run("emergency", func(t *testing.T) {
		// mysql.promote was sent and its answer lost: the job is partial
		// (PromoteSent, not PastPoint) and Ulangi sends it again.
		g := emergencyRig(t)
		e := g.e
		g.c.mu.Lock()
		g.c.dropReply = map[string]bool{"mysql.promote": true}
		g.c.mu.Unlock()
		id, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
			CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		if j, _ := e.waitJob(id, store.JobPartial); j.Kind != KindPromoteEmergency {
			t.Fatalf("kind = %s", j.Kind)
		}
		e.waitIdle(id)
		if st := e.storedState(id); !st.PromoteSent || st.PastPoint {
			t.Fatalf("job state = %+v, want mysql.promote sent, outcome unknown", st)
		}
		wantRetryOffered(t, e, id)
		g.c.mu.Lock()
		g.c.dropReply = nil
		g.c.mu.Unlock()
		// The lost answer's job opened writes: Ulangi reads the candidate.
		g.c.reply("mysql.status", proto.MySQLStatus{Port: 3306})
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry past the point: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if n := g.c.count("mysql.promote"); n != 2 {
			t.Fatalf("mysql.promote sent %d times, want 2 (the retry ran)", n)
		}
	})
}

// wantRetryOffered fails unless Ulangi of id is offered, needs no token and
// is not judged by the gate.
func wantRetryOffered(t *testing.T, e *env, id string) {
	t.Helper()
	if got := e.r.RetryRefused(id); got != "" {
		t.Fatalf("RetryRefused = %q, want none", got)
	}
	if drops, err := e.r.DropsData(id); err != nil || drops {
		t.Fatalf("DropsData = %v, %v; want false", drops, err)
	}
	if j, _, err := e.st.GetJob(id); err != nil {
		t.Fatal(err)
	} else if in, gated := JobGateInput(j); gated {
		t.Fatalf("a promote's Ulangi is judged again: %+v", in)
	}
}

// An agent that does not know a promotion command (N2): on an emergency
// job, whose Ulangi is refused, the step error says to run promote again;
// on a planned job it still says Ulangi.
func TestPromoteOldAgentTextFollowsRetry(t *testing.T) {
	const old = "Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, "
	t.Run("emergency", func(t *testing.T) {
		g := emergencyRig(t)
		e := g.e
		g.c.oldAgent("mysql.promote")
		id, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
			CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		j, _ := e.waitJob(id, store.JobFailed)
		if !strings.HasSuffix(j.Error, old+"lalu jalankan promote lagi.") {
			t.Fatalf("error = %q", j.Error)
		}
		wantRetryRefused(t, e, id)
	})
	t.Run("planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		g.c.oldAgent("mysql.promote")
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		j, _ := e.waitJob(id, store.JobFailed)
		if !strings.HasSuffix(j.Error, old+"lalu Ulangi.") {
			t.Fatalf("error = %q", j.Error)
		}
		e.waitIdle(id)
		// Offered; not sent, so it needs what its start needs.
		if got := e.r.RetryRefused(id); got != "" {
			t.Fatalf("RetryRefused = %q, want none", got)
		}
		if need, err := e.r.NeedsConfirm(id); err != nil || !need {
			t.Fatalf("NeedsConfirm = %v, %v; want true", need, err)
		}
	})
}

// ---- "Ulangi" of a planned promote before mysql.promote was sent (spec
// 5.2 row 1, 5.1; S-3, S-5): what its start needs. ----

// specT12 is T12 of spec 5.11, copied letter for letter.
const specT12 = `Promote ini terakhir dikonfirmasi lebih dari 24 jam lalu, jadi "Ulangi dari tahap ini" tidak tersedia ` +
	`lagi. Mulai promote baru dari halaman master.`

// viewReads are the reads a planned retry's fresh PromoteView sends
// before startMu; a refused retry sends nothing else.
var viewReads = []string{"mysql.binlogTail", "mysql.probeSource", "mysql.status"}

// failedAtCatchUp runs a planned promote of c that fails in "Replika
// mengejar" (its lock lifted by the failure path) and makes the next
// catch-up succeed. It returns the job.
func (g *promoteRig) failedAtCatchUp(t *testing.T) string {
	t.Helper()
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if st := e.storedState(id); st.PromoteSent || st.PastPoint || !st.LockUnlocked {
		t.Fatalf("job state = %+v, want not sent, lock confirmed open", st)
	}
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	return id
}

// wantRetryStays fails unless the job is still failed, ConfirmedAt is
// unchanged and no agent got more than the view's reads since marks.
func wantRetryStays(t *testing.T, e *env, id string, marks map[*fakeAgent]int, confirmed time.Time) {
	t.Helper()
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %s, want failed", j.Status)
	}
	if got := e.storedState(id).ConfirmedAt; !got.Equal(confirmed) {
		t.Fatalf("refused retry stored a confirmation: %v, was %v", got, confirmed)
	}
	onlyCmds(t, marks, viewReads...)
}

// reportsAt gives every rig server a fresh report received at at (the
// panel's clock moved there).
func (g *promoteRig) reportsAt(at time.Time) {
	v := healthyView(monitor.StatusHealthy, masterGTID, nil)
	v.ReceivedAt = at
	g.h.set(g.m.id, v)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		v := replicaView(masterGTID, masterGTID)
		v.ReceivedAt = at
		g.h.set(fa.id, v)
	}
}

// skewClock gives fa's report (a replica's) a clock d off the panel's.
func (g *promoteRig) skewClock(fa *fakeAgent, d time.Duration) {
	v := replicaView(masterGTID, masterGTID)
	v.Snap.ClockUnixMS = v.ReceivedAt.Add(d).UnixMilli()
	g.h.set(fa.id, v)
}

// TestStartPromoteStoresConfirmedAt: a planned start is its first
// confirmation (the 24-hour bound of its Ulangi counts from it).
func TestStartPromoteStoresConfirmedAt(t *testing.T) {
	g := newPromoteRig(t)
	before := time.Now()
	id := g.failedAtCatchUp(t)
	if at := g.e.storedState(id).ConfirmedAt; at.Before(before) || at.After(time.Now()) {
		t.Fatalf("ConfirmedAt = %v, want the start's time", at)
	}
}

func TestPlannedRetryBeforeSendNeedsToken(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.failedAtCatchUp(t)
	if need, err := e.r.NeedsConfirm(id); err != nil || !need {
		t.Fatalf("NeedsConfirm = %v, %v; want true", need, err)
	}
	if drops, err := e.r.DropsData(id); err != nil || drops {
		t.Fatalf("DropsData = %v, %v; want false (no database list)", drops, err)
	}
	confirmed := e.storedState(id).ConfirmedAt
	marks := markAll(e)
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without a token = %v, want ErrJobNeedsConfirm", err)
	}
	wantRetryStays(t, e, id, marks, confirmed)
	if g.m.count("mysql.lockForPromote") != 1 {
		t.Fatal("a refused retry locked the master")
	}
	// Confirmed: it runs.
	time.Sleep(5 * time.Millisecond)
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("confirmed Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if got := e.storedState(id).ConfirmedAt; !got.After(confirmed) {
		t.Fatalf("ConfirmedAt = %v, want the retry's time (after %v)", got, confirmed)
	}
}

func TestPlannedRetryBeforeSendGated(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.failedAtCatchUp(t)
	g.skewClock(g.c, 31*time.Second)
	confirmed := e.storedState(id).ConfirmedAt
	marks := markAll(e)
	err := e.r.Retry(id, JobAck{Confirmed: true})
	wantGateRed(t, err, "Jam (clock)")
	wantRetryStays(t, e, id, marks, confirmed)
	// The gate itself failing refuses too.
	g.skewClock(g.c, 0)
	gateCannotJudge(e)
	wantGateFault(t, e.r.Retry(id, JobAck{Confirmed: true}))
	wantRetryStays(t, e, id, marks, confirmed)
	e.r.gateFault = nil
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry once the gate is green: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

func TestPlannedRetryBeforeSendNeedsBackupAck(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.failedAtCatchUp(t)
	if err := e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	confirmed := e.storedState(id).ConfirmedAt
	marks := markAll(e)
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrBackupAck) {
		t.Fatalf("Retry without the backup tick = %v, want ErrBackupAck", err)
	}
	wantRetryStays(t, e, id, marks, confirmed)
	if err := e.r.Retry(id, JobAck{Confirmed: true, BackupAck: true}); err != nil {
		t.Fatalf("Retry with the tick: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// TestPlannedRetryRunsStartRefusals (S-3): the refusals of the start, from
// a view read for the retry.
func TestPlannedRetryRunsStartRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *promoteRig)
		want  string
	}{
		{"other replica writable", func(g *promoteRig) {
			v := replicaView(masterGTID, masterGTID)
			v.Snap.SuperReadOnly = false
			g.h.set(g.b.id, v)
			g.b.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID})
		}, named(specT24, "rep-b")},
		{"other replica unreadable", func(g *promoteRig) { g.b.detach() }, named(specT24c, "rep-b")},
		{"candidate stopped", func(g *promoteRig) {
			v := replicaView(masterGTID, masterGTID)
			v.Status.Code = monitor.StatusPaused
			g.h.set(g.c.id, v)
		}, "cand tidak bisa dipromosikan: Dihentikan"},
		{"master agent of another version", func(g *promoteRig) {
			g.e.r.d.Version = "1.2.3"
			for _, fa := range []*fakeAgent{g.m, g.c, g.b, g.d} {
				v := "1.2.3"
				if fa == g.m {
					v = "1.2.2"
				}
				if err := g.e.st.UpdateProbe(fa.id, proto.ProbeResult{Hostname: fa.name}, v, time.Now()); err != nil {
					g.e.t.Fatal(err)
				}
			}
		}, "agent master db-master: Agent perlu dimutakhirkan"},
		{"master allows only an emergency", func(g *promoteRig) { g.offline(g.m) },
			"promote terencana butuh agent master tersambung dan MySQL master hidup; status master sekarang hanya mengizinkan promote darurat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id := g.failedAtCatchUp(t)
			tc.setup(g)
			confirmed := e.storedState(id).ConfirmedAt
			marks := markAll(e)
			err := e.r.Retry(id, JobAck{Confirmed: true, BackupAck: true})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Retry = %v, want %q", err, tc.want)
			}
			wantRetryStays(t, e, id, marks, confirmed)
		})
	}
	t.Run("old master not settled", func(t *testing.T) {
		g := secondPromoteRig(t) // c the master, m its quarantined old master
		e := g.e
		promID, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromotePlanned,
			OldMasterServerID: g.c.id, NewMasterServerID: g.b.id, StartedAt: time.Now(), Status: promotionFailed})
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%032x", promID+300)
		e.storeOldJob(id, KindPromotePlanned, g.c.id, g.topo.ID, plannedSteps(), jobState{MasterServerID: g.c.id,
			CandidateServerID: g.b.id, PromotionID: promID, ConfirmedAt: time.Now()}, 2, store.JobRunning)
		if err := e.st.UpdateJob(id, store.JobFailed, 2, "gagal", time.Now()); err != nil {
			t.Fatal(err)
		}
		marks := markAll(e)
		if err := e.r.Retry(id, JobAck{Confirmed: true, BackupAck: true}); err == nil || err.Error() != oldMasterPendingText {
			t.Fatalf("Retry = %v, want %q", err, oldMasterPendingText)
		}
		onlyCmds(t, marks, viewReads...)
	})
}

// TestPlannedRetryRefusedWhileTopologyBusy: Ulangi does not relock the
// master while another job of the topology runs or waits (a resync of
// another replica waiting for its approval).
func TestPlannedRetryRefusedWhileTopologyBusy(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.failedAtCatchUp(t)
	const waiting = "7777777777777777777777777777777b"
	b, _ := json.Marshal(jobState{MasterServerID: g.m.id, Resync: true, WaitReason: WaitWipe})
	if err := e.st.CreateJob(store.Job{ID: waiting, Kind: KindResyncReplica, ServerID: g.b.id, TopologyID: g.topo.ID,
		ParamsJSON: string(b), CreatedAt: time.Now()}, resyncStepNames()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(waiting, store.JobWaiting, 0, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	confirmed := e.storedState(id).ConfirmedAt
	marks := markAll(e)
	err := e.r.Retry(id, JobAck{Confirmed: true, BackupAck: true})
	if !errors.Is(err, ErrBusy) || err.Error() != "topologi ini masih punya pekerjaan yang belum selesai" {
		t.Fatalf("Retry = %v, want the topology busy refusal", err)
	}
	wantRetryStays(t, e, id, marks, confirmed)
	if g.m.count("mysql.lockForPromote") != 1 {
		t.Fatal("the master was locked again")
	}
}

// plantFailedPlanned stores a planned promote of m to c failed at step idx
// before the send, created at created, with st's other fields.
func (g *promoteRig) plantFailedPlanned(st jobState, idx int, created time.Time) string {
	e := g.e
	e.t.Helper()
	promID, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromotePlanned, OldMasterServerID: g.m.id,
		NewMasterServerID: g.c.id, StartedAt: created, Status: promotionFailed})
	if err != nil {
		e.t.Fatal(err)
	}
	st.PromotionID, st.MasterServerID, st.CandidateServerID = promID, g.m.id, g.c.id
	b, _ := json.Marshal(st)
	id := fmt.Sprintf("%032x", promID+500)
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindPromotePlanned, ServerID: g.m.id, TopologyID: g.topo.ID,
		ParamsJSON: string(b), CreatedAt: created}, plannedSteps()); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, idx, "gagal", time.Now()); err != nil {
		e.t.Fatal(err)
	}
	if p, err := e.st.GetPromotion(promID); err == nil {
		p.JobID = id
		if err := e.st.UpdatePromotion(p); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

func TestPlannedRetryStaleAfter24h(t *testing.T) {
	t.Run("confirmed 25 hours ago", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id := g.failedAtCatchUp(t)
		old := time.Now().Add(-25 * time.Hour)
		e.setState(id, func(st *jobState) { st.ConfirmedAt = old })
		if got := e.r.RetryRefused(id); got != specT12 {
			t.Fatalf("RetryRefused = %q, want T12", got)
		}
		marks := markAll(e)
		err := e.r.Retry(id, JobAck{Confirmed: true, BackupAck: true})
		if !errors.Is(err, ErrPromoteRetryStale) || err.Error() != specT12 {
			t.Fatalf("Retry = %v, want T12", err)
		}
		wantRetryStays(t, e, id, marks, old)
	})
	t.Run("0.2.0 job created 25 hours ago", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id := g.plantFailedPlanned(jobState{}, 2, time.Now().Add(-25*time.Hour))
		if got := e.r.RetryRefused(id); got != specT12 {
			t.Fatalf("RetryRefused = %q, want T12", got)
		}
		if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrPromoteRetryStale) {
			t.Fatalf("Retry = %v, want T12", err)
		}
	})
	t.Run("0.2.0 job created an hour ago", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id := g.plantFailedPlanned(jobState{}, 2, time.Now().Add(-time.Hour))
		if got := e.r.RetryRefused(id); got != "" {
			t.Fatalf("RetryRefused = %q, want none", got)
		}
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if st := e.storedState(id); st.ConfirmedAt.IsZero() {
			t.Fatal("the retry stored no confirmation")
		}
	})
}

// TestPlannedRetryStartsAtLockStep (K15): from the earlier of the failed
// step and "Kunci master".
func TestPlannedRetryStartsAtLockStep(t *testing.T) {
	t.Run("failed catching up", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id := g.failedAtCatchUp(t)
		syncedBeforeLock := -1
		now := time.Now()
		g.m.on("mysql.lockForPromote", func(json.RawMessage) (any, error) {
			syncedBeforeLock = g.c.count("mysql.waitSynced")
			return proto.LockForPromoteResult{LockedAt: now, Deadline: now.Add(30 * time.Minute), ServerUUID: masterUUID,
				GTIDExecuted: masterGTID}, nil
		})
		waits := g.c.count("mysql.waitSynced")
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if syncedBeforeLock != waits {
			t.Fatalf("waitSynced sent %d times before the lock, want %d (the lock first)", syncedBeforeLock, waits)
		}
		if n := g.m.count("mysql.lockForPromote"); n != 2 {
			t.Fatalf("lockForPromote sent %d times, want 2", n)
		}
	})
	t.Run("failed making the account", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		if err := e.st.SetReplicaDatabases(g.c.id, []string{"a"}); err != nil {
			t.Fatal(err)
		}
		g.c.fail("mysql.createReplUser", "akun tidak bisa dibuat")
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
			AcceptPartial: true})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		if j, _ := e.waitJob(id, store.JobFailed); j.CurrentStep != 0 {
			t.Fatalf("failed at step %d, want 0", j.CurrentStep)
		}
		e.waitIdle(id)
		if g.m.count("mysql.lockForPromote") != 0 {
			t.Fatal("the master was locked")
		}
		g.c.reply("mysql.createReplUser", nil)
		if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if n := g.c.count("mysql.createReplUser"); n != 2 {
			t.Fatalf("createReplUser sent %d times, want 2 (the retry starts at the account)", n)
		}
	})
}

// TestPlannedRetryAfterPanelRestart: a planned promote failed by Resume
// before the send follows the same rules: T12b while its lock record
// stands; once the lock is proved open, the gate and the token.
func TestPlannedRetryAfterPanelRestart(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	tried := time.Now().Add(-time.Minute)
	deadline := tried.Add(30 * time.Minute)
	id, promID := g.cutOffPromote(jobState{LockTried: true, LockTriedAt: &tried, LockDeadline: &deadline}, 2)
	p, err := e.st.GetPromotion(promID)
	if err != nil {
		t.Fatal(err)
	}
	p.JobID = id // the record is the promotion's job's
	if err := e.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	e.r.Resume(context.Background())
	e.waitJob(id, store.JobFailed)
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrPromoteLockStands) {
		t.Fatalf("Retry while the lock record stands = %v, want T12b", err)
	}
	if out, err := e.r.UnlockPromoteLock(context.Background(), id); err != nil || out != UnlockOpened {
		t.Fatalf("UnlockPromoteLock = %v, %v", out, err)
	}
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without a token = %v, want ErrJobNeedsConfirm", err)
	}
	g.skewClock(g.c, -31*time.Second)
	wantGateRed(t, e.r.Retry(id, JobAck{Confirmed: true}), "Jam (clock)")
	g.skewClock(g.c, 0)
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// redEverything makes every precondition row of a promote of g it can
// red (the clocks, the master's agent version, a replica's TLS, the
// backup folder) and the gate itself fail.
func redEverything(t *testing.T, g *promoteRig) {
	t.Helper()
	e := g.e
	g.skewClock(g.c, time.Hour)
	g.skewClock(g.b, -time.Hour)
	if err := e.st.SetReplicaTLSVerified(g.b.id, false); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateProbe(g.m.id, proto.ProbeResult{Hostname: "db-master"}, "0.0.1", time.Now()); err != nil {
		t.Fatal(err)
	}
	gateCannotJudge(e)
}

// TestPromoteRetryPastPointTokenlessAndUngated (spec 5.13's guard, F13):
// past the point of no return, or once mysql.promote was sent, Ulangi is
// one click whatever its kind: every row red, the gate failing, no token,
// no tick; it runs and the old master's quarantine is sent.
func TestPromoteRetryPastPointTokenlessAndUngated(t *testing.T) {
	t.Run("planned past the point", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		g.m.fail("mysql.quarantine", "master lama tidak menjawab")
		id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		e.waitJob(id, store.JobPartial)
		e.waitIdle(id)
		if st := e.storedState(id); !st.PastPoint {
			t.Fatalf("job state = %+v, want past the point", st)
		}
		redEverything(t, g)
		e.setState(id, func(st *jobState) { st.ConfirmedAt = time.Now().Add(-72 * time.Hour) })
		wantRetryOffered(t, e, id)
		if need, err := e.r.NeedsConfirm(id); err != nil || need {
			t.Fatalf("NeedsConfirm = %v, %v; want false", need, err)
		}
		quarantines := g.m.count("mysql.quarantine")
		g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry past the point: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if n := g.m.count("mysql.quarantine"); n != quarantines+1 {
			t.Fatalf("mysql.quarantine sent %d times, want %d", n, quarantines+1)
		}
	})
	t.Run("planned sent, outcome unknown", func(t *testing.T) {
		// mysql.promote sent and its outcome lost (a restart): partial,
		// PromoteSent, not PastPoint, confirmed three days ago.
		g := newPromoteRig(t)
		e := g.e
		id := g.plantFailedPlanned(jobState{PromoteSent: true, CandidatePort: 3306}, 3, time.Now().Add(-72*time.Hour))
		if err := e.st.ReopenJob(id, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateJob(id, store.JobPartial, 3, "promote selesai sebagian", time.Now()); err != nil {
			t.Fatal(err)
		}
		redEverything(t, g)
		wantRetryOffered(t, e, id)
		// The candidate opened writes: Ulangi reads it first.
		g.c.reply("mysql.status", proto.MySQLStatus{Port: 3306})
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry of a sent promote: %v", err)
		}
		e.waitJob(id, store.JobSucceeded)
		if n := g.c.count("mysql.promote"); n != 1 {
			t.Fatalf("mysql.promote sent %d times, want 1", n)
		}
		if n := g.m.count("mysql.quarantine"); n != 1 {
			t.Fatalf("mysql.quarantine sent %d times, want 1", n)
		}
	})
	t.Run("emergency past the point", func(t *testing.T) {
		g := emergencyRig(t)
		e := g.e
		// The old master's agent comes back while the candidate opens
		// writes, and refuses the quarantine: partial past the point.
		g.m.fail("mysql.quarantine", "master lama tidak menjawab")
		g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
			g.m.attach()
			return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: proto.PromoteEmergency}, nil
		})
		id, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
			CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
		if err != nil {
			t.Fatalf("StartPromote: %v", err)
		}
		e.waitJob(id, store.JobPartial)
		e.waitIdle(id)
		if st := e.storedState(id); !st.PastPoint {
			t.Fatalf("job state = %+v, want past the point", st)
		}
		redEverything(t, g)
		wantRetryOffered(t, e, id)
		quarantines := g.m.count("mysql.quarantine")
		g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
		if err := e.r.Retry(id, JobAck{}); err != nil {
			t.Fatalf("Retry past the point: %v", err)
		}
		e.waitFor("quarantine resent", func() bool { return g.m.count("mysql.quarantine") > quarantines })
	})
}

// ---- Fix round 1: "sent" and the lock record are stored before the
// command goes out, or it is not sent (C-1); a retry does not count its
// own promotion as an earlier one unfinished (I-1). ----

// failParamsWith makes the job params store fail every write whose JSON
// holds marker (first only: only the first such write fails).
func failParamsWith(e *env, marker string, first bool) {
	var mu sync.Mutex
	failed := false
	e.r.paramsStore = func(jobID, paramsJSON string) error {
		mu.Lock()
		hit := strings.Contains(paramsJSON, marker) && !(first && failed)
		if hit {
			failed = true
		}
		mu.Unlock()
		if hit {
			return errors.New("database is locked")
		}
		return e.st.SetJobParams(jobID, paramsJSON)
	}
}

// TestPromoteSentSaveFailureSendsNothing: when "sent" cannot be stored,
// mysql.promote is not sent and the job fails before the switch; its
// stored state reads not sent and its Ulangi follows the before-send
// rules.
func TestPromoteSentSaveFailureSendsNothing(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	failParamsWith(e, `"promote_sent":true`, false)
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if steps[j.CurrentStep].Name != stepPromoteNewMaster || !strings.Contains(j.Error, "database is locked") {
		t.Fatalf("job failed at %q with %q, want the new-master step failing on the save", steps[j.CurrentStep].Name, j.Error)
	}
	if n := g.c.count("mysql.promote"); n != 0 {
		t.Fatalf("mysql.promote sent %d times", n)
	}
	st := e.storedState(id)
	if st.PromoteSent || st.PastPoint {
		t.Fatalf("stored state = %+v, want not sent", st)
	}
	if p := g.promotion(); p.Status != promotionFailed {
		t.Fatalf("promotion = %s, want failed", p.Status)
	}
	e.r.paramsStore = nil
	if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without a token = %v, want ErrJobNeedsConfirm", err)
	}
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("confirmed Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// TestLockRecordSaveFailureSendsNoLock: when the lock record cannot be
// stored, mysql.lockForPromote is not sent and the job fails.
func TestLockRecordSaveFailureSendsNoLock(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	failParamsWith(e, `"lock_tried":true`, false)
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if steps[j.CurrentStep].Name != stepPromoteLock || !strings.Contains(j.Error, "database is locked") {
		t.Fatalf("job failed at %q with %q, want the lock step failing on the save", steps[j.CurrentStep].Name, j.Error)
	}
	if n := g.m.count("mysql.lockForPromote"); n != 0 {
		t.Fatalf("mysql.lockForPromote sent %d times", n)
	}
	if st := e.storedState(id); st.LockTried || st.LockTriedAt != nil {
		t.Fatalf("stored state = %+v, want no lock asked for", st)
	}
	if recs := g.lockRecords(); len(recs) != 0 {
		t.Fatalf("lock records = %+v, want none", recs)
	}
}

// TestLockDeadlineSaveFailureKeepsRecord: the master answered the lock but
// its deadline could not be stored: the record stands from the time the
// lock was asked for (as for a lost answer), never from an older deadline.
func TestLockDeadlineSaveFailureKeepsRecord(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	failParamsWith(e, `"lock_deadline"`, true)
	var seen jobState
	var recs []LockRecord
	var once sync.Once
	g.c.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		once.Do(func() {
			j, _, _ := e.st.GetJob(g.promotion().JobID)
			json.Unmarshal([]byte(j.ParamsJSON), &seen)
			recs, _ = e.r.LockRecords(g.m.id)
		})
		return proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
			Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}}, nil
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if !seen.LockTried || seen.LockTriedAt == nil || seen.LockDeadline != nil {
		t.Fatalf("stored while catching up = %+v, want the lock asked for, no deadline", seen)
	}
	want := seen.LockTriedAt.Add(promoteLockSec*time.Second + lockCallSlack + proto.PromoteLockGrace)
	if len(recs) != 1 || recs[0].JobID != id || !recs[0].Until.Equal(want) {
		t.Fatalf("records while catching up = %+v, want one until %v", recs, want)
	}
}

// TestPastPointSaveFailureReadsSent: the candidate opened writes but
// PastPoint could not be stored: the stored state still reads sent, so
// Ulangi stays one click.
func TestPastPointSaveFailureReadsSent(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	failParamsWith(e, `"past_point":true`, true)
	var seen jobState
	g.m.on("mysql.quarantine", func(json.RawMessage) (any, error) {
		j, _, _ := e.st.GetJob(g.promotion().JobID)
		json.Unmarshal([]byte(j.ParamsJSON), &seen)
		return nil, &commands.ParamError{Msg: "master lama tidak menjawab"}
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	if !seen.PromoteSent {
		t.Fatalf("stored after the switch = %+v, want sent", seen)
	}
	if need, err := e.r.NeedsConfirm(id); err != nil || need {
		t.Fatalf("NeedsConfirm = %v, %v; want false", need, err)
	}
}

// TestPlannedRetryOwnPromotionUnfinished (I-1): the job's own promotion
// left running or partial does not refuse its Ulangi as "an earlier
// promote unfinished": it goes on to the gate and the token.
func TestPlannedRetryOwnPromotionUnfinished(t *testing.T) {
	for _, status := range []string{promotionRunning, promotionPartial} {
		t.Run(status, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id := g.failedAtCatchUp(t)
			p := g.promotion()
			p.Status = status
			if err := e.st.UpdatePromotion(p); err != nil {
				t.Fatal(err)
			}
			if err := e.r.Retry(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
				t.Fatalf("Retry without a token = %v, want ErrJobNeedsConfirm (past the refusals)", err)
			}
			g.skewClock(g.c, 31*time.Second)
			wantGateRed(t, e.r.Retry(id, JobAck{Confirmed: true}), "Jam (clock)")
			g.skewClock(g.c, 0)
			if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
				t.Fatalf("Retry: %v", err)
			}
			e.waitJob(id, store.JobSucceeded)
		})
	}
}

// TestPromoteRefusalsOtherUnfinishedPromotion (I-1): another promotion of
// the topology left unfinished still refuses, with the existing text,
// whichever promotion is the caller's own.
func TestPromoteRefusalsOtherUnfinishedPromotion(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.failedAtCatchUp(t)
	own := e.storedState(id).PromotionID
	if _, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromotePlanned, OldMasterServerID: g.m.id,
		NewMasterServerID: g.b.id, StartedAt: time.Now(), Status: promotionRunning}); err != nil {
		t.Fatal(err)
	}
	view, err := e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	const want = "promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi"
	p := PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, Mode: proto.PromotePlanned}
	e.r.startMu.Lock()
	defer e.r.startMu.Unlock()
	for _, prom := range []int64{0, own} {
		if _, _, err := e.r.promoteRefusals(p, view, g.topo, prom); err == nil || err.Error() != want {
			t.Fatalf("promoteRefusals (own %d) = %v, want %q", prom, err, want)
		}
	}
}
