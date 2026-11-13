package mysql_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// numbered is an event of type typ whose payload carries n.
func numbered(typ string, n int) proto.Event {
	return proto.Event{Type: typ, Payload: json.RawMessage(`{"n":` + strconv.Itoa(n) + `}`)}
}

// eventNumber is the n numbered put in ev's payload.
func eventNumber(t *testing.T, ev proto.Event) int {
	t.Helper()
	var p struct{ N int }
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.N
}

// numbers are the numbers of evs, in order.
func numbers(t *testing.T, evs []proto.Event) []int {
	t.Helper()
	out := make([]int, 0, len(evs))
	for _, ev := range evs {
		out = append(out, eventNumber(t, ev))
	}
	return out
}

func wantNumbers(t *testing.T, got []int, want ...int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("queue = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("queue = %v, want %v", got, want)
		}
	}
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// TestEventQueueOrderAndDrop: the queue keeps the order events were put
// in; past its cap it drops the oldest start report first (its value is
// in the health snapshot), else the oldest event.
func TestEventQueueOrderAndDrop(t *testing.T) {
	if mysql.EventQueueCap != 64 {
		t.Fatalf("eventQueueCap = %d, want 64", mysql.EventQueueCap)
	}
	t.Run("oldest start report dropped first", func(t *testing.T) {
		var q mysql.EventQueue
		q.Push(numbered(proto.EventLockExpired, 1))
		q.Push(numbered(proto.EventQuarantineStart, 2))
		for i := 3; i <= 63; i++ {
			q.Push(numbered(proto.EventLockExpired, i))
		}
		q.Push(numbered(proto.EventQuarantineStartFailed, 64))
		dropped := q.Push(numbered(proto.EventLockExpired, 65))
		if dropped == nil || dropped.Type != proto.EventQuarantineStart || eventNumber(t, *dropped) != 2 {
			t.Fatalf("dropped = %+v, want the start report 2", dropped)
		}
		// The next oldest start report is the failed start.
		dropped = q.Push(numbered(proto.EventLockExpired, 66))
		if dropped == nil || dropped.Type != proto.EventQuarantineStartFailed || eventNumber(t, *dropped) != 64 {
			t.Fatalf("second dropped = %+v, want the failed start 64", dropped)
		}
		want := append([]int{1}, seq(3, 63)...)
		wantNumbers(t, numbers(t, q.Drain()), append(want, 65, 66)...)
	})
	t.Run("oldest dropped without start reports", func(t *testing.T) {
		var q mysql.EventQueue
		for i := 1; i <= 64; i++ {
			if d := q.Push(numbered(proto.EventQuarantined, i)); d != nil {
				t.Fatalf("push %d dropped %+v below the cap", i, d)
			}
		}
		dropped := q.Push(numbered(proto.EventLockExpired, 65))
		if dropped == nil || eventNumber(t, *dropped) != 1 {
			t.Fatalf("dropped = %+v, want the oldest (1)", dropped)
		}
		wantNumbers(t, numbers(t, q.Drain()), seq(2, 65)...)
	})
	t.Run("event being sent dropped meanwhile", func(t *testing.T) {
		// The sender holds the front event while it sends; a push that
		// drops that event meanwhile must not make the sender's popFront
		// remove the next one.
		var q mysql.EventQueue
		for i := 1; i <= 64; i++ {
			q.Push(numbered(proto.EventLockExpired, i))
		}
		ev, ok := q.Front()
		if !ok || eventNumber(t, ev) != 1 {
			t.Fatalf("front = %+v, %v", ev, ok)
		}
		if d := q.Push(numbered(proto.EventLockExpired, 65)); d == nil || eventNumber(t, *d) != 1 {
			t.Fatalf("dropped = %+v, want 1", d)
		}
		if d := q.Push(numbered(proto.EventLockExpired, 66)); d == nil || eventNumber(t, *d) != 2 {
			t.Fatalf("dropped = %+v, want 2", d)
		}
		q.PopFront() // 1 was sent: nothing left to remove
		wantNumbers(t, numbers(t, q.Drain()), seq(3, 66)...)
	})
}

// stuckEmitter blocks its first Emit until release is closed. Inside that
// Emit it checks that the lock the guard holds (promoteMu) is free: the
// sender never sends under it.
type stuckEmitter struct {
	eventLog
	d       *mysql.Driver
	release chan struct{}
	calls   atomic.Int32
	// lockFree: whether promoteMu could be taken within 1 s from inside
	// the first Emit; sent once.
	lockFree chan bool
}

func (e *stuckEmitter) Emit(ev proto.Event) error {
	if e.calls.Add(1) == 1 {
		got := make(chan struct{})
		go func() {
			e.d.HoldPromoteMu()()
			close(got)
		}()
		select {
		case <-got:
			e.lockFree <- true
		case <-time.After(time.Second):
			e.lockFree <- false
		}
		<-e.release
	}
	return e.eventLog.Emit(ev)
}

// TestGuardNeverSendsUnderPromoteMu (R-117): a panel that does not take
// the guard's events (a send that blocks) does not delay the guard's
// rounds, and the sender does not hold promoteMu while it sends; once the
// panel takes them again, every queued event arrives in order.
func TestGuardNeverSendsUnderPromoteMu(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	// Quarantined with a writable MySQL: the first round finds a late
	// start and puts read-only back.
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	ev := &stuckEmitter{d: r.d, release: make(chan struct{}), lockFree: make(chan bool, 1)}
	r.d.Events = ev
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	// Runs first: a send still stuck is let go before the sender is
	// waited for.
	released := false
	defer func() {
		if !released {
			close(ev.release)
		}
	}()
	now := time.Now()
	tick := func(i int) {
		t.Helper()
		start := time.Now()
		r.tickAt(now.Add(time.Duration(i) * 5 * time.Second))
		if d := time.Since(start); d >= 2*time.Second {
			t.Errorf("guard round %d took %v with the panel not taking events, want under 2 s", i, d)
		}
	}
	// The sender starts after the first round, so that the check from
	// inside its stuck send is not raced by a round still running.
	tick(0)
	wg.Go(func() { r.d.RunEvents(ctx) })
	select {
	case free := <-ev.lockFree:
		if !free {
			t.Error("promoteMu not free within 1 s while the sender is sending")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sender never sent")
	}
	for i := 1; i < 5; i++ {
		tick(i)
	}
	if n := ev.calls.Load(); n != 1 {
		t.Errorf("Emit called %d times while the first send was stuck, want 1", n)
	}
	close(ev.release)
	released = true
	deadline := time.Now().Add(10 * time.Second)
	var evs []proto.Event
	for {
		evs = ev.delivered()
		if len(evs) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(evs) != 2 || evs[0].Type != proto.EventQuarantineStart || evs[1].Type != proto.EventQuarantined {
		t.Fatalf("delivered = %+v, want the late start report then the quarantine notice", evs)
	}
	var st proto.QuarantineStartEvent
	if err := json.Unmarshal(evs[0].Payload, &st); err != nil || st.Result != proto.QuarantineStartLate {
		t.Errorf("first event = %s (%v), want a late start", evs[0].Payload, err)
	}
}

// TestQueuedEventsDeliveredInOrderAfterOffline (FR-1, SA-5): what the
// guard decided while the panel was not connected stays queued and is
// delivered once it is, in the order decided: exactly one late start
// report, none lost, none twice.
func TestQueuedEventsDeliveredInOrderAfterOffline(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	// A writable MySQL on a server pinned quarantined: the first round
	// finds the flags off, a late start.
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	r.ev.setOffline(true)
	now := time.Now()
	r.tickAt(now)
	r.tickAt(now.Add(5 * time.Second))
	if r.d.FlushEvents() {
		t.Fatal("FlushEvents = true while the panel is not connected")
	}
	if n := len(r.ev.delivered()); n != 0 {
		t.Fatalf("delivered while offline: %d events", n)
	}
	r.ev.setOffline(false)
	if !r.d.FlushEvents() {
		t.Fatal("FlushEvents = false once connected")
	}
	evs := r.ev.delivered()
	if len(evs) != 2 || evs[0].Type != proto.EventQuarantineStart || evs[1].Type != proto.EventQuarantined {
		t.Fatalf("delivered = %+v, want the start report then the quarantine notice", evs)
	}
	if got := startEvents(t, r.ev); len(got) != 1 || got[0].Result != proto.QuarantineStartLate {
		t.Fatalf("start events once connected = %+v, want one late", got)
	}
	r.tickAt(now.Add(10 * time.Second))
	if got := startEvents(t, r.ev); len(got) != 1 {
		t.Errorf("start events after another round = %+v, want still one", got)
	}
}
