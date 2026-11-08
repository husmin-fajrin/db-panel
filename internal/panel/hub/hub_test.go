package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// fakeConn records calls made through the Conn interface. Close sleeps for
// closeDelay, then signals on closed.
type fakeConn struct {
	closeDelay time.Duration
	closed     chan struct{}

	mu     sync.Mutex
	calls  []string
	closes int
}

func newFakeConn(closeDelay time.Duration) *fakeConn {
	return &fakeConn{closeDelay: closeDelay, closed: make(chan struct{}, 8)}
}

func (f *fakeConn) Call(ctx context.Context, cmd string, params any, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cmd)
	return nil
}

func (f *fakeConn) Close() error {
	time.Sleep(f.closeDelay)
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	f.closed <- struct{}{}
	return nil
}

// waitClosed waits for one Close call to finish.
func (f *fakeConn) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-f.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("conn was not closed")
	}
}

func (f *fakeConn) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *fakeConn) callCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeListener counts hub notifications.
type fakeListener struct {
	mu       sync.Mutex
	online   []int64
	offline  []int64
	events   []proto.Event
	eventIDs []int64
}

func (l *fakeListener) OnOnline(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.online = append(l.online, id)
}

func (l *fakeListener) OnOffline(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.offline = append(l.offline, id)
}

func (l *fakeListener) OnEvent(id int64, ev proto.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	l.eventIDs = append(l.eventIDs, id)
}

func (l *fakeListener) counts() (online, offline int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.online), len(l.offline)
}

func TestAttachReplacesExistingAndClosesOld(t *testing.T) {
	l := &fakeListener{}
	h := New(l)
	oldConn, newConn := newFakeConn(0), newFakeConn(0)

	if h.Attach(7, oldConn) {
		t.Error("first Attach reported a replacement")
	}
	if !h.Attach(7, newConn) {
		t.Error("second Attach did not report a replacement")
	}
	oldConn.waitClosed(t)

	on, off := l.counts()
	if on != 2 || off != 0 {
		t.Errorf("OnOnline = %d, OnOffline = %d; want 2, 0", on, off)
	}
	if n := oldConn.closeCount(); n != 1 {
		t.Errorf("old conn closed %d times, want 1", n)
	}
	if n := newConn.closeCount(); n != 0 {
		t.Errorf("new conn closed %d times, want 0", n)
	}
	if err := h.Call(context.Background(), 7, "ping", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := newConn.callCmds(); len(got) != 1 || got[0] != "ping" {
		t.Errorf("new conn calls = %v", got)
	}
	if got := oldConn.callCmds(); len(got) != 0 {
		t.Errorf("old conn calls = %v", got)
	}
}

func TestDisconnectClosesAndReportsOffline(t *testing.T) {
	l := &fakeListener{}
	h := New(l)
	c := newFakeConn(0)
	h.Attach(4, c)

	h.Disconnect(4)
	c.waitClosed(t)
	if h.IsOnline(4) {
		t.Fatal("server 4 still online after Disconnect")
	}
	if _, off := l.counts(); off != 1 {
		t.Fatalf("OnOffline = %d, want 1", off)
	}
	h.Detach(4, c) // the connection's own handler detaching later is a no-op
	h.Disconnect(4)
	if _, off := l.counts(); off != 1 {
		t.Fatalf("OnOffline after repeated Disconnect/Detach = %d, want 1", off)
	}
}

func TestAttachDoesNotBlockOnSlowClose(t *testing.T) {
	h := New(&fakeListener{})
	slow := newFakeConn(200 * time.Millisecond)
	h.Attach(1, slow)

	start := time.Now()
	h.Attach(1, newFakeConn(0))
	if d := time.Since(start); d >= 50*time.Millisecond {
		t.Errorf("Attach took %v, want < 50ms", d)
	}
	slow.waitClosed(t)
	if n := slow.closeCount(); n != 1 {
		t.Errorf("old conn closed %d times, want 1", n)
	}
}

// orderListener records the last lifecycle callback per server, yielding
// before recording to widen any window in which callbacks could reorder.
type orderListener struct {
	mu   sync.Mutex
	last map[int64]bool // true = OnOnline, false = OnOffline
}

func (l *orderListener) record(id int64, online bool) {
	runtime.Gosched()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last[id] = online
}

func (l *orderListener) OnOnline(id int64)          { l.record(id, true) }
func (l *orderListener) OnOffline(id int64)         { l.record(id, false) }
func (l *orderListener) OnEvent(int64, proto.Event) {}

func TestAttachDetachOrderConsistent(t *testing.T) {
	for i := range 200 {
		l := &orderListener{last: make(map[int64]bool)}
		h := New(l)
		oldConn, newConn := newFakeConn(0), newFakeConn(0)
		h.Attach(7, oldConn)

		var wg sync.WaitGroup
		wg.Go(func() { h.Detach(7, oldConn) })
		wg.Go(func() { h.Attach(7, newConn) })
		wg.Wait()

		l.mu.Lock()
		last := l.last[7]
		l.mu.Unlock()
		if online := h.IsOnline(7); last != online {
			t.Fatalf("iteration %d: last callback online=%v, IsOnline=%v", i, last, online)
		}
	}
}

func TestDetachOnlyActive(t *testing.T) {
	l := &fakeListener{}
	h := New(l)
	oldConn, newConn := newFakeConn(0), newFakeConn(0)
	h.Attach(3, oldConn)
	h.Attach(3, newConn)

	h.Detach(3, oldConn)
	if _, off := l.counts(); off != 0 {
		t.Fatalf("OnOffline after stale Detach = %d, want 0", off)
	}
	if !h.IsOnline(3) {
		t.Fatal("server 3 should still be online")
	}

	h.Detach(3, newConn)
	if _, off := l.counts(); off != 1 {
		t.Fatalf("OnOffline after active Detach = %d, want 1", off)
	}
	if h.IsOnline(3) {
		t.Fatal("server 3 should be offline")
	}

	h.Detach(3, newConn)
	if _, off := l.counts(); off != 1 {
		t.Fatalf("OnOffline after repeated Detach = %d, want 1", off)
	}
}

func TestOnlineIDs(t *testing.T) {
	h := New(&fakeListener{})
	h.Attach(9, newFakeConn(0))
	h.Attach(2, newFakeConn(0))
	h.Attach(5, newFakeConn(0))
	got := h.OnlineIDs()
	want := []int64{2, 5, 9}
	if len(got) != len(want) {
		t.Fatalf("OnlineIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("OnlineIDs = %v, want %v", got, want)
		}
	}
}

func TestCallOfflineReturnsErrOffline(t *testing.T) {
	h := New(&fakeListener{})
	err := h.Call(context.Background(), 42, "probe", nil, nil)
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
	if err.Error() != "koneksi agent terputus" {
		t.Errorf("message = %q", err.Error())
	}
}

func TestLocalConnDispatch(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register("echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		return params, nil
	})
	c := NewLocalConn(reg, func(proto.Event) {})

	in := map[string]any{"name": "db1", "port": float64(3306)}
	var out map[string]any
	if err := c.Call(context.Background(), "echo", in, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["name"] != "db1" || out["port"] != float64(3306) || len(out) != 2 {
		t.Errorf("result = %v, want %v", out, in)
	}

	err := c.Call(context.Background(), "nope", nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *proto.Error", err, err)
	}
	if pe.Code != proto.CodeUnknownCommand {
		t.Errorf("code = %q, want %q", pe.Code, proto.CodeUnknownCommand)
	}

	if err := c.Call(context.Background(), "echo", func() {}, nil); err == nil {
		t.Error("Call with unmarshalable params: want error")
	} else if errors.As(err, &pe) {
		t.Errorf("marshal error should not be *proto.Error: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestHubOnEventForwards: Hub.OnEvent is the single entry for agent events
// (remote and embedded); it hands each one to the listener with its server.
func TestHubOnEventForwards(t *testing.T) {
	l := &fakeListener{}
	h := New(l)
	h.OnEvent(7, proto.Event{Type: "job.log", JobID: "j1"})
	h.OnEvent(9, proto.Event{Type: "job.done"})

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) != 2 || l.events[0].Type != "job.log" || l.events[0].JobID != "j1" || l.events[1].Type != "job.done" {
		t.Fatalf("events = %+v", l.events)
	}
	if len(l.eventIDs) != 2 || l.eventIDs[0] != 7 || l.eventIDs[1] != 9 {
		t.Fatalf("event server IDs = %v, want [7 9]", l.eventIDs)
	}
}

// TestErrorsIsOffline: "no connection" before a call and "connection lost"
// during one are the same error to errors.Is, with or without wrapping, and
// other agent errors are not.
func TestErrorsIsOffline(t *testing.T) {
	if ErrOffline != proto.ErrOffline {
		t.Fatal("hub.ErrOffline is not proto.ErrOffline")
	}
	h := New(&fakeListener{})
	before := h.Call(context.Background(), 42, "probe", nil, nil)
	during := offlineError()
	for name, err := range map[string]error{
		"Call without connection": before,
		"connection lost":         during,
		"wrapped":                 fmt.Errorf("probe: %w", during),
	} {
		if !errors.Is(err, proto.ErrOffline) {
			t.Errorf("%s: errors.Is(%v, proto.ErrOffline) = false", name, err)
		}
		if !errors.Is(err, ErrOffline) {
			t.Errorf("%s: errors.Is(%v, hub.ErrOffline) = false", name, err)
		}
	}
	for _, err := range []error{
		timeoutError(),
		&proto.Error{Code: proto.CodeInternal, Message: "koneksi agent terputus"},
		errors.New("koneksi agent terputus"),
	} {
		if errors.Is(err, proto.ErrOffline) {
			t.Errorf("errors.Is(%#v, proto.ErrOffline) = true", err)
		}
	}
	if during.Error() != "koneksi agent terputus" {
		t.Errorf("message = %q", during.Error())
	}
}

// fakeClock is a settable clock for the report rate limit.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// eventCount counts the events the listener got for id, of type typ.
func (l *fakeListener) eventCount(id int64, typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for i, ev := range l.events {
		if l.eventIDs[i] == id && ev.Type == typ {
			n++
		}
	}
	return n
}

// TestHubRateLimitsSnapshotsPerServer (spec 5.12): a bucket of 10 health
// reports per server, refilled with one every 2 s; reports over it are
// dropped and counted, other events and other servers are not affected,
// and the connection stays.
func TestHubRateLimitsSnapshotsPerServer(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
	l := &fakeListener{}
	h := New(l, WithNow(clk.Now))
	c1, c2 := newFakeConn(0), newFakeConn(0)
	h.Attach(1, c1)
	h.Attach(2, c2)
	snap := proto.Event{Type: proto.EventHealth}

	for range 11 {
		h.OnEvent(1, snap)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 10 {
		t.Fatalf("server 1 reports passed = %d, want 10", n)
	}
	if d := h.Dropped(1); d != 1 {
		t.Fatalf("Dropped(1) = %d, want 1", d)
	}
	// Other events of the flooding server still pass.
	h.OnEvent(1, proto.Event{Type: proto.EventJobLog})
	if n := l.eventCount(1, proto.EventJobLog); n != 1 {
		t.Fatalf("job.log events passed = %d, want 1", n)
	}
	// Another server has its own bucket.
	for range 10 {
		h.OnEvent(2, snap)
	}
	if n := l.eventCount(2, proto.EventHealth); n != 10 || h.Dropped(2) != 0 {
		t.Fatalf("server 2 reports passed = %d, dropped %d; want 10 and 0", n, h.Dropped(2))
	}

	// 2 s later one token is back: one passes, the next is dropped.
	clk.Add(2 * time.Second)
	h.OnEvent(1, snap)
	h.OnEvent(1, snap)
	if n := l.eventCount(1, proto.EventHealth); n != 11 {
		t.Fatalf("server 1 reports after 2 s = %d, want 11", n)
	}
	if d := h.Dropped(1); d != 2 {
		t.Fatalf("Dropped(1) = %d, want 2", d)
	}

	// Flooding never drops the connection.
	if !h.IsOnline(1) || c1.closeCount() != 0 {
		t.Fatalf("flooding server: online %v, closes %d; want online and never closed", h.IsOnline(1), c1.closeCount())
	}

	// The count covers the last hour; a full bucket after a long pause.
	clk.Add(time.Hour)
	if d := h.Dropped(1); d != 0 {
		t.Fatalf("Dropped(1) an hour later = %d, want 0", d)
	}
	for range 10 {
		h.OnEvent(1, snap)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 21 || h.Dropped(1) != 0 {
		t.Fatalf("after the pause passed = %d, dropped %d; want 21 and 0", n, h.Dropped(1))
	}
}

// healthEvent is a health report event, marked as backlog or not.
func healthEvent(t *testing.T, backlog bool) proto.Event {
	t.Helper()
	b, err := json.Marshal(proto.HealthEvent{Seq: 1, Backlog: backlog})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Event{Type: proto.EventHealth, Payload: b}
}

// TestHubBacklogAllowance: the backlog a sound agent flushes after a
// reconnect (up to 200 reports) passes whole; past the bucket's 10 tokens
// and the 200 of the allowance a report is dropped, a re-Attach within 10 min grants nothing new, and live
// reports still obey the bucket. After 10 min a new connection gets a
// fresh allowance.
func TestHubBacklogAllowance(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
	l := &fakeListener{}
	h := New(l, WithNow(clk.Now))
	h.Attach(1, newFakeConn(0))
	backlog, live := healthEvent(t, true), healthEvent(t, false)

	// A full ring (200 reports) passes whole.
	for range 200 {
		h.OnEvent(1, backlog)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 200 || h.Dropped(1) != 0 {
		t.Fatalf("full backlog: passed %d, dropped %d; want 200 and 0", n, h.Dropped(1))
	}
	// The bucket's 10 tokens went first, so 10 more backlog reports fit;
	// the 211th is dropped.
	for range 11 {
		h.OnEvent(1, backlog)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 210 || h.Dropped(1) != 1 {
		t.Fatalf("past the allowance: passed %d, dropped %d; want 210 and 1", n, h.Dropped(1))
	}
	// Live reports still obey the empty bucket.
	h.OnEvent(1, live)
	if n := l.eventCount(1, proto.EventHealth); n != 210 || h.Dropped(1) != 2 {
		t.Fatalf("live report with an empty bucket: passed %d, dropped %d; want 210 and 2", n, h.Dropped(1))
	}

	// Reconnecting within 10 min mints no new allowance.
	clk.Add(5 * time.Minute) // refills the bucket: 10 tokens
	h.Attach(1, newFakeConn(0))
	for range 11 {
		h.OnEvent(1, backlog)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 220 || h.Dropped(1) != 3 {
		t.Fatalf("re-Attach within 10 min: passed %d, dropped %d; want 220 and 3", n, h.Dropped(1))
	}

	// 10 min after the grant a new connection gets a fresh allowance.
	clk.Add(5 * time.Minute)
	h.Attach(1, newFakeConn(0))
	for range 211 {
		h.OnEvent(1, backlog)
	}
	if n := l.eventCount(1, proto.EventHealth); n != 430 {
		t.Fatalf("after 10 min: passed %d, want 430 (10 tokens + 200 allowance)", n)
	}
}

// TestHubRateLimitConcurrent: reports racing in from many goroutines on
// two servers with a frozen clock let exactly 10 through per server.
func TestHubRateLimitConcurrent(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
	l := &fakeListener{}
	h := New(l, WithNow(clk.Now))
	snap := proto.Event{Type: proto.EventHealth}
	var wg sync.WaitGroup
	for i := range 40 {
		id := int64(1 + i%2)
		wg.Go(func() {
			for range 25 {
				h.OnEvent(id, snap)
				h.Dropped(id)
			}
		})
	}
	wg.Wait()
	for _, id := range []int64{1, 2} {
		if n := l.eventCount(id, proto.EventHealth); n != 10 {
			t.Errorf("server %d: %d reports passed, want 10", id, n)
		}
		if d := h.Dropped(id); d != 490 {
			t.Errorf("server %d: Dropped = %d, want 490", id, d)
		}
	}
}

// TestDisconnectForgetsOfflineBucket: deleting a server that is offline
// also forgets its rate limit state.
func TestDisconnectForgetsOfflineBucket(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
	h := New(&fakeListener{}, WithNow(clk.Now))
	for range 11 {
		h.OnEvent(5, proto.Event{Type: proto.EventHealth})
	}
	if h.Dropped(5) != 1 {
		t.Fatalf("Dropped = %d, want 1", h.Dropped(5))
	}
	h.Disconnect(5)
	h.rateMu.Lock()
	_, kept := h.buckets[5]
	h.rateMu.Unlock()
	if kept || h.Dropped(5) != 0 {
		t.Fatal("bucket of a server deleted while offline kept")
	}
}
