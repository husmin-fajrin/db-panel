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
