package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
		wantRetryOffered(t, e, id)
	})
}
