package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// retryClock is the Runner's clock in the retry tests: the real time plus
// an offset the test moves forward.
type retryClock struct{ off atomic.Int64 }

func (c *retryClock) Now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *retryClock) advance(d time.Duration) { c.off.Add(int64(d)) }

// retryOf is the round's schedule of serverID; false when it has none.
func (r *Runner) retryOf(serverID int64) (quarantineDue, bool) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	d, ok := r.retries[serverID]
	if !ok {
		return quarantineDue{}, false
	}
	return *d, true
}

// silence makes fa receive cmd and never answer it.
func (fa *fakeAgent) silence(cmd string) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.silent == nil {
		fa.silent = map[string]bool{}
	}
	fa.silent[cmd] = true
}

// pendingRig is a promote rig after an emergency promote of c while m
// (the old master) and d were offline: m's quarantine is pending, d waits
// in needs_repoint, both agents still away. clk, when not nil, is the
// Runner's clock from before the promote.
func pendingRig(t *testing.T, clk *retryClock) *promoteRig {
	t.Helper()
	g := newPromoteRig(t)
	e := g.e
	if clk != nil {
		e.r.now = clk.Now
	}
	g.offline(g.m)
	g.offline(g.d)
	for _, fa := range []*fakeAgent{g.c, g.b} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	id, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
		CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if !QuarantinePending(g.promotion()) {
		t.Fatal("quarantine not pending after the promote")
	}
	return g
}

// startRound starts the retry round with a short scan, as Resume does.
func startRound(e *env) {
	e.r.retryScan = 10 * time.Millisecond
	e.r.Resume(context.Background())
}

func (g *promoteRig) pending() bool { return QuarantinePending(g.promotion()) }

// oldMasterRecord is m's entry in the promotion.
func (g *promoteRig) oldMasterRecord() PromotionServer {
	g.e.t.Helper()
	s, ok := OldMasterRecord(g.promotion())
	if !ok {
		g.e.t.Fatal("no old-master entry in the promotion")
	}
	return s
}

// TestPendingQuarantineNotHeldBySilentAgent (spec 5.3, 5.13): replica X
// waits in needs_repoint and its agent holds mysql.repoint without an
// answer; the old master M with a pending quarantine connects meanwhile:
// its mysql.quarantine is sent within a second, X's lock is X's alone.
func TestPendingQuarantineNotHeldBySilentAgent(t *testing.T) {
	g := pendingRig(t, nil)
	e := g.e
	g.d.silence("mysql.repoint")
	g.d.attach()
	e.r.OnOnline(g.d.id)
	e.waitFor("X's repoint sent", func() bool { return g.d.count("mysql.repoint") == 1 })

	g.m.attach()
	e.r.OnOnline(g.m.id)
	deadline := time.Now().Add(time.Second)
	for g.m.count("mysql.quarantine") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("mysql.quarantine not sent within a second while another agent holds mysql.repoint")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.waitFor("quarantine recorded", func() bool { return !g.pending() })
}

// TestOnOnlineTakesNoLockWithoutWork (spec 5.3): an agent with nothing
// owed (a healthy replica, the master, an old master already quarantined)
// connects without lockTarget being taken; one owed a quarantine takes it.
func TestOnOnlineTakesNoLockWithoutWork(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	var locks atomic.Int32
	e.r.lockTargetHook = func(int64) { locks.Add(1) }
	e.r.OnOnline(g.b.id)
	e.r.OnOnline(g.m.id)
	e.r.Close()
	if n := locks.Load(); n != 0 {
		t.Fatalf("lockTarget taken %d times for agents with nothing owed", n)
	}

	g = pendingRig(t, nil)
	e = g.e
	locks.Store(0)
	e.r.lockTargetHook = func(int64) { locks.Add(1) }
	g.m.attach()
	e.r.OnOnline(g.m.id)
	e.waitFor("quarantine recorded", func() bool { return !g.pending() })
	e.r.OnOnline(g.m.id)
	e.r.Close()
	if n := locks.Load(); n != 1 {
		t.Fatalf("lockTarget taken %d times; want once, for the pending quarantine only", n)
	}
}

// TestQuarantineRetriedAfterRefusal (spec 5.3, 5.13, K26): the old master
// refuses its pending quarantine, then accepts: the round sends it again
// without a reconnect, 5 minutes after the refusal and not before. A
// "sibuk" answer is retried the same way.
func TestQuarantineRetriedAfterRefusal(t *testing.T) {
	for _, refusal := range []string{"master baru 10.0.0.2:3306 tidak bisa dibaca", "agent sedang sibuk; coba lagi nanti"} {
		t.Run(refusal, func(t *testing.T) {
			clk := &retryClock{}
			g := pendingRig(t, clk)
			e := g.e
			startRound(e)
			g.m.fail("mysql.quarantine", refusal)
			g.m.attach()
			e.r.OnOnline(g.m.id)
			e.waitFor("the refusal recorded", func() bool {
				d, ok := e.r.retryOf(g.m.id)
				return ok && d.wait == quarantineRetryMax && !d.inflight
			})
			if !g.pending() {
				t.Fatal("quarantine not pending after a refusal")
			}
			rec := g.oldMasterRecord()
			if !rec.Refused || rec.TriedAt == nil || QuarantineRefusal(rec) != refusal ||
				!strings.HasPrefix(rec.Detail, agentLabelOf("db-master")) {
				t.Fatalf("old master record after the refusal = %+v", rec)
			}
			g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
			clk.advance(quarantineRetryMax - 5*time.Second)
			time.Sleep(100 * time.Millisecond)
			if n := g.m.count("mysql.quarantine"); n != 1 {
				t.Fatalf("quarantine sent %d times before 5 minutes passed", n)
			}
			clk.advance(5 * time.Second)
			e.waitFor("quarantine landed on the next round", func() bool { return !g.pending() })
			if n := g.m.count("mysql.quarantine"); n != 2 {
				t.Fatalf("quarantine sent %d times; want 2", n)
			}
			e.waitFor("the round ended", func() bool { _, ok := e.r.retryOf(g.m.id); return !ok })
			if rec := g.oldMasterRecord(); rec.Refused || rec.TriedAt == nil {
				t.Fatalf("old master record after it landed = %+v", rec)
			}
		})
	}
}

// TestQuarantineRetryNeverStops (spec 5.3): ten refusals in a row, and the
// eleventh round is still scheduled.
func TestQuarantineRetryNeverStops(t *testing.T) {
	clk := &retryClock{}
	g := pendingRig(t, clk)
	e := g.e
	startRound(e)
	g.m.fail("mysql.quarantine", "berkas replika belum terbaca")
	g.m.attach()
	e.r.OnOnline(g.m.id)
	for i := 1; i <= 10; i++ {
		e.waitFor("refusal recorded", func() bool {
			d, ok := e.r.retryOf(g.m.id)
			return g.m.count("mysql.quarantine") == i && ok && !d.inflight && d.wait == quarantineRetryMax
		})
		if i < 10 {
			clk.advance(quarantineRetryMax)
		}
	}
	d, ok := e.r.retryOf(g.m.id)
	if !ok || !d.at.After(clk.Now()) || d.at.Sub(clk.Now()) > quarantineRetryMax {
		t.Fatalf("after ten refusals: schedule %+v, %v; want the eleventh within 5 minutes", d, ok)
	}
	clk.advance(quarantineRetryMax)
	e.waitFor("the eleventh round", func() bool { return g.m.count("mysql.quarantine") == 11 })
	if !g.pending() {
		t.Fatal("quarantine no longer pending although every round was refused")
	}
}

// TestQuarantineRetryBacksOffWithoutAnswer (K26): a send without an answer
// (the call's own bound passes) is retried after 60 s, then 120, 240, 300
// and 300 s.
func TestQuarantineRetryBacksOffWithoutAnswer(t *testing.T) {
	clk := &retryClock{}
	g := pendingRig(t, clk)
	e := g.e
	e.r.onlineSend = 30 * time.Millisecond
	startRound(e)
	g.m.silence("mysql.quarantine")
	g.m.attach()
	e.r.OnOnline(g.m.id)
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 300 * time.Second, 300 * time.Second}
	for i, w := range want {
		var d quarantineDue
		e.waitFor("timeout recorded", func() bool {
			var ok bool
			d, ok = e.r.retryOf(g.m.id)
			return g.m.count("mysql.quarantine") == i+1 && ok && !d.inflight && d.wait != 0
		})
		if d.wait != w {
			t.Fatalf("interval after timeout %d = %s, want %s", i+1, d.wait, w)
		}
		if rec := g.oldMasterRecord(); rec.Refused {
			t.Fatalf("a timeout recorded as the agent's refusal: %+v", rec)
		}
		clk.advance(w)
	}
}

// TestQuarantineRetryUsesCurrentMaster (spec 5.3, NR-1): the old master
// refused while c was the master; the topology was promoted to b since.
// The round sends under the topology's current master b, never under c.
func TestQuarantineRetryUsesCurrentMaster(t *testing.T) {
	clk := &retryClock{}
	g := pendingRig(t, clk)
	e := g.e
	startRound(e)
	g.m.fail("mysql.quarantine", "master baru tidak bisa dibaca")
	g.m.attach()
	e.r.OnOnline(g.m.id)
	e.waitFor("the refusal recorded", func() bool {
		d, ok := e.r.retryOf(g.m.id)
		return ok && d.wait == quarantineRetryMax && !d.inflight
	})
	var q proto.QuarantineParams
	g.m.params("mysql.quarantine", &q)
	if q.NewMaster.Host != "10.0.0.2" {
		t.Fatalf("first quarantine named %s", q.NewMaster.Host)
	}
	// The topology is promoted again: b is its master now.
	if err := e.st.SwapMaster(g.topo.ID, g.c.id, g.b.id, "10.0.0.3", 3306, "job:test", time.Now()); err != nil {
		t.Fatal(err)
	}
	g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
	clk.advance(quarantineRetryMax)
	e.waitFor("quarantine landed", func() bool { return !g.pending() })
	all := g.m.allParams("mysql.quarantine")
	if len(all) != 2 {
		t.Fatalf("quarantine sent %d times", len(all))
	}
	if err := json.Unmarshal([]byte(all[1]), &q); err != nil {
		t.Fatal(err)
	}
	if q.NewMaster.Host != "10.0.0.3" {
		t.Fatalf("retried quarantine named %s, want the current master 10.0.0.3", q.NewMaster.Host)
	}
}

// TestQuarantineRetryEndsWhenRoleChangesOrServerDeleted (spec 5.3): the
// round for a server ends when its role is no longer quarantined or it is
// deleted; nothing more is sent to it.
func TestQuarantineRetryEndsWhenRoleChangesOrServerDeleted(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(e *env, id int64) error
	}{
		{"role", func(e *env, id int64) error { return e.st.SetServerRole(id, store.RoleUnassigned) }},
		{"deleted", func(e *env, id int64) error { return e.st.DeleteServer(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &retryClock{}
			g := pendingRig(t, clk)
			e := g.e
			startRound(e)
			g.m.fail("mysql.quarantine", "master baru tidak bisa dibaca")
			g.m.attach()
			e.r.OnOnline(g.m.id)
			e.waitFor("the refusal recorded", func() bool {
				d, ok := e.r.retryOf(g.m.id)
				return ok && d.wait == quarantineRetryMax && !d.inflight
			})
			if err := tc.end(e, g.m.id); err != nil {
				t.Fatal(err)
			}
			e.waitFor("the round ended", func() bool { _, ok := e.r.retryOf(g.m.id); return !ok })
			clk.advance(2 * quarantineRetryMax)
			time.Sleep(100 * time.Millisecond)
			if n := g.m.count("mysql.quarantine"); n != 1 {
				t.Fatalf("quarantine sent %d times after the round ended", n)
			}
		})
	}
}

// TestOnlineSendHasFreshTimeout (spec 5.3): onOnline's send gets its own
// bound once the lock is held, not what is left since the agent
// connected. Scaled down: the bound is 400 ms, the lock is held 300 ms
// (the "4 of 5 minutes"), the agent answers after 200 ms.
func TestOnlineSendHasFreshTimeout(t *testing.T) {
	g := pendingRig(t, nil)
	e := g.e
	e.r.onlineSend = 400 * time.Millisecond
	g.m.on("mysql.quarantine", func(json.RawMessage) (any, error) {
		time.Sleep(200 * time.Millisecond)
		return proto.QuarantineResult{Applied: true}, nil
	})
	g.m.attach()
	unlock := e.r.lockTarget(g.m.id)
	e.r.OnOnline(g.m.id)
	time.Sleep(300 * time.Millisecond)
	if n := g.m.count("mysql.quarantine"); n != 0 {
		unlock()
		t.Fatalf("quarantine sent %d times while its server's lock was held", n)
	}
	unlock()
	e.waitFor("quarantine landed", func() bool { return !g.pending() })
}
