package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Ulangi of a promote job that failed before mysql.promote was sent (PC-1,
// ruling R43): refused, so that the operator starts a new promote, which
// reads the mode again and asks for the tick, the names, the password and
// (planned) the gate. A job past that point keeps its tokenless Ulangi.

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

// A planned promote that failed in "Replika mengejar" and whose lock was
// lifted: Ulangi is refused and sends nothing; a new planned start is
// judged by the gate (refused on a red row) and runs once it is green.
func TestPromotePlannedRetryBeforePointRefused(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitFor("unlockPromote", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	wantRetryRefused(t, e, id)
	if n := g.m.count("mysql.lockForPromote"); n != 1 {
		t.Fatalf("lockForPromote sent %d times, want 1", n)
	}

	// A red row refuses the new start before anything is written.
	if err := e.st.SetReplicaTLSVerified(g.b.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}); !errors.Is(err, ErrGateRed) {
		t.Fatalf("new planned start with a red row = %v, want ErrGateRed", err)
	}
	if err := e.st.SetReplicaTLSVerified(g.b.id, true); err != nil {
		t.Fatal(err)
	}
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id2, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("new planned start after the refused retry: %v", err)
	}
	e.waitJob(id2, store.JobSucceeded)
}

// A planned promote whose unlock failed (the master's agent did not
// answer it): while its lock may still stand, Ulangi stays as it was (it
// is how the panel confirms the lock open, and a new start would meet the
// lock); once the lock's deadline and the guard's grace have passed, it is
// refused like any other.
func TestPromotePlannedRetryWhileLockMayStand(t *testing.T) {
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
	st := e.storedState(id)
	if !st.LockTried || st.LockUnlocked || st.LockDeadline == nil {
		t.Fatalf("job state = %+v, want a lock not confirmed open", st)
	}
	if got := e.r.RetryRefused(id); got != "" {
		t.Fatalf("RetryRefused while the lock may stand = %q", got)
	}
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry while the lock may stand: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)

	deadline := *e.storedState(id).LockDeadline
	e.r.now = func() time.Time { return deadline.Add(proto.PromoteLockGrace - time.Second) }
	if got := e.r.RetryRefused(id); got != "" {
		t.Fatalf("RetryRefused within the grace = %q", got)
	}
	e.r.now = func() time.Time { return deadline.Add(proto.PromoteLockGrace + time.Second) }
	wantRetryRefused(t, e, id)
}

// A promote past the point of no return keeps its Ulangi, without a
// token and without the gate (K4).
func TestPromoteRetryPastPointStillRuns(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.m.fail("mysql.quarantine", "master lama tidak menjawab")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	if got := e.r.RetryRefused(id); got != "" {
		t.Fatalf("RetryRefused past the point = %q", got)
	}
	if drops, err := e.r.DropsData(id); err != nil || drops {
		t.Fatalf("DropsData = %v, %v; want false", drops, err)
	}
	if j, _, err := e.st.GetJob(id); err != nil {
		t.Fatal(err)
	} else if in, gated := JobGateInput(j); gated {
		t.Fatalf("a promote past the point is judged again: %+v", in)
	}
	e.fullDisk(g.c) // a red row on the new master changes nothing
	g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry past the point: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}
