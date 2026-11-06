package monitor

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// fakeSource returns whatever snapshot the test set last.
type fakeSource struct {
	mu    sync.Mutex
	snap  proto.HealthSnapshot
	calls int
	gate  chan struct{} // when set, Snapshot waits for it (or ctx)
}

func (f *fakeSource) Snapshot(ctx context.Context) proto.HealthSnapshot {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	// Like the real source, every reading is a fresh value the monitor
	// owns: the test's later edits must not reach it.
	s := f.snap
	if s.Replica != nil {
		r := *s.Replica
		s.Replica = &r
	}
	return s
}

func (f *fakeSource) set(fn func(s *proto.HealthSnapshot)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.snap)
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeEmitter records the health events it accepts. While offline it
// refuses with proto.ErrOffline; acceptLeft >= 0 accepts only that many
// more events before going offline; failOnce refuses the next event only.
type fakeEmitter struct {
	mu         sync.Mutex
	offline    bool
	acceptLeft int
	failOnce   bool
	events     []proto.HealthEvent
}

func (e *fakeEmitter) Emit(ev proto.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.offline {
		return proto.ErrOffline
	}
	if e.failOnce {
		e.failOnce = false
		return proto.ErrOffline
	}
	if e.acceptLeft == 0 {
		e.offline = true
		return proto.ErrOffline
	}
	if e.acceptLeft > 0 {
		e.acceptLeft--
	}
	if ev.Type != proto.EventHealth {
		panic("unexpected event type " + ev.Type)
	}
	var h proto.HealthEvent
	if err := json.Unmarshal(ev.Payload, &h); err != nil {
		panic(err)
	}
	e.events = append(e.events, h)
	return nil
}

func (e *fakeEmitter) setOnline(on bool, acceptLeft int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.offline, e.acceptLeft = !on, acceptLeft
}

func (e *fakeEmitter) got() []proto.HealthEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]proto.HealthEvent(nil), e.events...)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// wait is one pause of the loop: the interval it asked for and the channel
// that ends it.
type wait struct {
	d  time.Duration
	ch chan time.Time
}

// rig runs a Monitor whose loop the test steps: every pause of the loop
// arrives on waits, so receiving one means the previous tick has finished.
type rig struct {
	t     *testing.T
	src   *fakeSource
	em    *fakeEmitter
	clock *fakeClock
	reg   *commands.Registry
	m     *Monitor
	ctx   context.Context
	waits chan wait
	cur   wait
}

func newRig(t *testing.T, snap proto.HealthSnapshot) *rig {
	t.Helper()
	return newRigWith(t, snap, nil)
}

// newRigWith is newRig whose monitor bootstraps pins.
func newRigWith(t *testing.T, snap proto.HealthSnapshot, pins Pinner) *rig {
	t.Helper()
	src := &fakeSource{snap: snap}
	return startRig(t, src, src, pins)
}

// startRig runs a Monitor reading monSrc, which wraps src (the fake the
// test edits).
func startRig(t *testing.T, src *fakeSource, monSrc Source, pins Pinner) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{
		t:     t,
		src:   src,
		em:    &fakeEmitter{acceptLeft: -1},
		clock: &fakeClock{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)},
		reg:   commands.NewRegistry(),
		ctx:   ctx,
		waits: make(chan wait),
	}
	r.m = New(monSrc, r.em, slog.New(slog.DiscardHandler), Options{
		Pins: pins,
		Now:  r.clock.Now,
		After: func(d time.Duration) <-chan time.Time {
			w := wait{d: d, ch: make(chan time.Time, 1)}
			select {
			case r.waits <- w:
			case <-ctx.Done():
			}
			return w.ch
		},
	})
	r.m.Register(r.reg)
	done := make(chan struct{})
	go func() {
		r.m.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	r.idle() // the first tick runs at once
	return r
}

// idle waits until the loop pauses again.
func (r *rig) idle() {
	r.t.Helper()
	select {
	case r.cur = <-r.waits:
	case <-time.After(5 * time.Second):
		r.t.Fatal("monitor loop did not pause")
	}
}

// tick advances the clock by d, ends the current pause and waits for the
// tick to finish.
func (r *rig) tick(d time.Duration) {
	r.t.Helper()
	r.clock.advance(d)
	r.cur.ch <- r.clock.Now()
	r.idle()
}

func (r *rig) dispatch(cmd string, params any) proto.Response {
	r.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			r.t.Fatal(err)
		}
		raw = b
	}
	return r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: raw})
}

func (r *rig) wantReports(n int) []proto.HealthEvent {
	r.t.Helper()
	got := r.em.got()
	if len(got) != n {
		r.t.Fatalf("got %d reports, want %d: %+v", len(got), n, got)
	}
	return got
}

func resultEvent(t *testing.T, resp proto.Response) proto.HealthEvent {
	t.Helper()
	if !resp.OK {
		t.Fatalf("command failed: %+v", resp.Error)
	}
	var ev proto.HealthEvent
	if err := json.Unmarshal(resp.Result, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestFirstTickReportsImmediately(t *testing.T) {
	r := newRig(t, base())
	got := r.wantReports(1)
	ev := got[0]
	if ev.Seq != 1 || ev.Backlog || ev.TakenAgoMS != 0 {
		t.Fatalf("first report = seq %d backlog %v taken_ago %d, want 1 false 0", ev.Seq, ev.Backlog, ev.TakenAgoMS)
	}
	if ev.Snapshot.UptimeSec != 1000 || ev.Snapshot.Replica == nil {
		t.Fatalf("snapshot not carried: %+v", ev.Snapshot)
	}
	if r.cur.d != 15*time.Second {
		t.Fatalf("loop waits %v, want the default 15s", r.cur.d)
	}
}

func TestNoReportWhenUnchanged(t *testing.T) {
	r := newRig(t, base())
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	r.wantReports(1)
	// Up to 105 s after the last report: still quiet.
	for range 5 {
		r.tick(15 * time.Second)
	}
	r.wantReports(1)
	// 120 s: the summary.
	r.tick(15 * time.Second)
	got := r.wantReports(2)
	if got[1].Seq != 2 || got[1].Backlog {
		t.Fatalf("summary = %+v, want seq 2, not backlog", got[1])
	}
	r.tick(15 * time.Second)
	r.wantReports(2)
}

func TestReportsOnIOStateChange(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) { s.Replica.IORunning = "Connecting" })
	r.tick(15 * time.Second)
	got := r.wantReports(2)
	if got[1].Snapshot.Replica.IORunning != "Connecting" || got[1].Seq != 2 {
		t.Fatalf("change report = %+v", got[1])
	}
}

func TestReportsOnLagCrossingThreshold(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(59) })
	r.tick(15 * time.Second)
	r.wantReports(1)
	r.src.set(func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(61) })
	r.tick(15 * time.Second)
	r.wantReports(2)
	r.src.set(func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(70) })
	r.tick(15 * time.Second)
	r.wantReports(2)
}

func TestReportsOnDiskCrossing85(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) { s.DiskFree = 16 }) // 84%
	r.tick(15 * time.Second)
	r.wantReports(1)
	r.src.set(func(s *proto.HealthSnapshot) { s.DiskFree = 14 }) // 86%
	r.tick(15 * time.Second)
	r.wantReports(2)
}

func TestReportsOnUptimeDecrease(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) { s.UptimeSec = 1015 })
	r.tick(15 * time.Second)
	r.wantReports(1)
	r.src.set(func(s *proto.HealthSnapshot) { s.UptimeSec = 4 })
	r.tick(15 * time.Second)
	got := r.wantReports(2)
	if got[1].Snapshot.UptimeSec != 4 {
		t.Fatalf("restart report = %+v", got[1].Snapshot)
	}
}

func TestReportsOnDumpThreadsChange(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) { s.BinlogDumpThreads = 1 })
	r.tick(15 * time.Second)
	r.wantReports(2)
	r.src.set(func(s *proto.HealthSnapshot) { s.BinlogDumpThreads = 0 })
	r.tick(15 * time.Second)
	r.wantReports(3)
}

// toggle flips read_only, which is always a change.
func (r *rig) toggle() {
	r.src.set(func(s *proto.HealthSnapshot) { s.ReadOnly = !s.ReadOnly })
}

func (r *rig) buffered() int {
	r.m.checkMu.Lock()
	defer r.m.checkMu.Unlock()
	return r.m.buf.Len()
}

func TestOfflineBuffersChangesOnly(t *testing.T) {
	src := base()
	r := newRig(t, src)
	r.wantReports(1)
	r.em.setOnline(false, -1)

	r.toggle()
	r.tick(15 * time.Second)
	if n := r.buffered(); n != 1 {
		t.Fatalf("buffer holds %d after a change, want 1", n)
	}
	// Ten unchanged ticks span more than SummaryEvery: summaries are not kept.
	for range 10 {
		r.tick(15 * time.Second)
	}
	if n := r.buffered(); n != 1 {
		t.Fatalf("buffer holds %d after quiet ticks, want 1 (summaries are not buffered)", n)
	}
	for range ringCapacity + 10 {
		r.toggle()
		r.tick(15 * time.Second)
	}
	if n := r.buffered(); n != ringCapacity {
		t.Fatalf("buffer holds %d, want the cap %d", n, ringCapacity)
	}
	r.wantReports(1)
}

func TestConfigureFlushesBacklogInOrder(t *testing.T) {
	r := newRig(t, base()) // seq 1, sent
	r.em.setOnline(false, -1)
	r.toggle()
	r.tick(15 * time.Second) // seq 2 at +15s, buffered
	r.toggle()
	r.tick(15 * time.Second) // seq 3 at +30s, buffered
	r.clock.advance(10 * time.Second)
	r.em.setOnline(true, -1)

	resp := r.dispatch("monitor.configure", proto.MonitorConfigureParams{
		CheckEverySec: 30, SummaryEverySec: 300, LagThresholdSec: 0, RoleHint: "replica",
	})
	cur := resultEvent(t, resp)
	got := r.wantReports(3)
	b1, b2 := got[1], got[2]
	if !b1.Backlog || !b2.Backlog || b1.Seq != 2 || b2.Seq != 3 {
		t.Fatalf("backlog = %+v, %+v; want seq 2, 3 with Backlog", b1, b2)
	}
	if b1.TakenAgoMS != 25000 || b2.TakenAgoMS != 10000 {
		t.Fatalf("taken_ago = %d, %d; want 25000, 10000", b1.TakenAgoMS, b2.TakenAgoMS)
	}
	if cur.Seq != 4 || cur.Backlog || cur.TakenAgoMS != 0 {
		t.Fatalf("configure result = seq %d backlog %v taken_ago %d; want 4 false 0", cur.Seq, cur.Backlog, cur.TakenAgoMS)
	}
	if r.buffered() != 0 {
		t.Fatal("backlog not emptied")
	}
	if r.m.LastRoleHint() != "replica" {
		t.Fatalf("role hint = %q", r.m.LastRoleHint())
	}
	// The loop restarts its pause with the new interval; the lag
	// threshold kept its default.
	r.idle()
	if r.cur.d != 30*time.Second {
		t.Fatalf("loop waits %v after configure, want 30s", r.cur.d)
	}
	if o := r.m.options(); o.SummaryEvery != 300*time.Second || o.LagThreshold != time.Minute {
		t.Fatalf("options after configure = %+v", o)
	}
}

func TestFlushKeepsEntriesItCouldNotSend(t *testing.T) {
	r := newRig(t, base())
	r.em.setOnline(false, -1)
	for range 3 {
		r.toggle()
		r.tick(15 * time.Second) // seq 2, 3, 4 buffered
	}
	r.em.setOnline(true, 1) // accepts one event, then drops again
	resp := r.dispatch("monitor.configure", proto.MonitorConfigureParams{CheckEverySec: 15, SummaryEverySec: 120})
	resultEvent(t, resp)
	got := r.wantReports(2)
	if got[1].Seq != 2 || !got[1].Backlog {
		t.Fatalf("flushed = %+v, want seq 2", got[1])
	}
	if n := r.buffered(); n != 2 {
		t.Fatalf("buffer holds %d, want the 2 unsent entries", n)
	}

	// Back online: the next tick sends the rest in order before anything new.
	r.idle() // the pause restarted by configure
	r.em.setOnline(true, -1)
	r.toggle()
	r.tick(15 * time.Second)
	got = r.wantReports(5)
	if got[2].Seq != 3 || got[3].Seq != 4 || !got[2].Backlog || !got[3].Backlog {
		t.Fatalf("rest of backlog = %+v, %+v", got[2], got[3])
	}
	if got[4].Seq != 6 || got[4].Backlog {
		t.Fatalf("fresh report after backlog = %+v, want seq 6 (5 was the configure result)", got[4])
	}
}

// TestChangeWaitsBehindBacklog: a change found while older entries could
// not be sent joins the backlog even if the panel would take it, so the
// panel never sees a newer report before older ones.
func TestChangeWaitsBehindBacklog(t *testing.T) {
	r := newRig(t, base())
	r.em.setOnline(false, -1)
	r.toggle()
	r.tick(15 * time.Second) // seq 2 buffered
	r.em.mu.Lock()
	r.em.offline, r.em.failOnce = false, true // the flush fails, then the panel is back
	r.em.mu.Unlock()
	r.toggle()
	r.tick(15 * time.Second) // seq 3 must queue behind seq 2
	r.wantReports(1)
	if n := r.buffered(); n != 2 {
		t.Fatalf("buffer holds %d, want 2", n)
	}
	r.tick(15 * time.Second)
	got := r.wantReports(3)
	if got[1].Seq != 2 || got[2].Seq != 3 || !got[1].Backlog || !got[2].Backlog {
		t.Fatalf("backlog sent as %+v, %+v", got[1], got[2])
	}
}

func TestConfigureRejectsOutOfRange(t *testing.T) {
	r := newRig(t, base())
	bad := []proto.MonitorConfigureParams{
		{CheckEverySec: 9, SummaryEverySec: 120},
		{CheckEverySec: 3601, SummaryEverySec: 120},
		{CheckEverySec: 15, SummaryEverySec: 9},
		{CheckEverySec: 15, SummaryEverySec: 3601},
		{CheckEverySec: 15, SummaryEverySec: 120, LagThresholdSec: 5},
		{CheckEverySec: 15, SummaryEverySec: 120, LagThresholdSec: 3601},
		{CheckEverySec: 15, SummaryEverySec: 120, LagThresholdSec: -1},
		{CheckEverySec: 15, SummaryEverySec: 120, RoleHint: "boss"},
	}
	for _, p := range bad {
		if err := r.m.Configure(p); err == nil {
			t.Errorf("Configure(%+v) accepted", p)
		}
		resp := r.dispatch("monitor.configure", p)
		if resp.OK || resp.Error.Code != proto.CodeBadParams {
			t.Errorf("monitor.configure %+v = %+v, want bad_params", p, resp)
		}
	}
	if resp := r.dispatch("monitor.configure", nil); resp.OK || resp.Error.Code != proto.CodeBadParams {
		t.Errorf("monitor.configure without params = %+v, want bad_params", resp)
	}
	if o := r.m.options(); o.CheckEvery != 15*time.Second || o.SummaryEvery != 120*time.Second || o.LagThreshold != time.Minute {
		t.Fatalf("rejected params changed the options: %+v", o)
	}
	r.wantReports(1) // a rejected configure runs no check
	if err := r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 10, SummaryEverySec: 3600, LagThresholdSec: 3600, RoleHint: "master"}); err != nil {
		t.Fatalf("bounds rejected: %v", err)
	}
}

func TestSnapshotCommandForcesCheck(t *testing.T) {
	r := newRig(t, base())
	if n := r.src.callCount(); n != 1 {
		t.Fatalf("source read %d times, want 1", n)
	}
	r.src.set(func(s *proto.HealthSnapshot) { s.UptimeSec = 1234 })
	r.clock.advance(3 * time.Second)
	ev := resultEvent(t, r.dispatch("monitor.snapshot", nil))
	if n := r.src.callCount(); n != 2 {
		t.Fatalf("source read %d times, want 2", n)
	}
	if ev.Seq != 2 || ev.Backlog || ev.TakenAgoMS != 0 || ev.Snapshot.UptimeSec != 1234 {
		t.Fatalf("snapshot result = %+v", ev)
	}
	r.wantReports(1) // returned, not emitted
}

// TestCancelledSnapshotCommandKeepsNothing: a monitor.snapshot whose
// request is cancelled during the reading fails and leaves the sequence
// and the last reported snapshot alone.
func TestCancelledSnapshotCommandKeepsNothing(t *testing.T) {
	r := newRig(t, base())
	r.src.mu.Lock()
	r.src.gate = make(chan struct{}) // never opened: the reading ends with ctx
	r.src.mu.Unlock()
	r.src.set(func(s *proto.HealthSnapshot) { s.UptimeSec = 3 })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := r.reg.Dispatch(ctx, proto.Request{ID: "1", Cmd: "monitor.snapshot"})
	if resp.OK {
		t.Fatalf("cancelled monitor.snapshot answered %s", resp.Result)
	}
	r.src.mu.Lock()
	r.src.gate = nil
	r.src.mu.Unlock()
	r.tick(15 * time.Second) // the restart is still news to the panel
	got := r.wantReports(2)
	if got[1].Seq != 2 || got[1].Snapshot.UptimeSec != 3 {
		t.Fatalf("report after the cancelled command = seq %d uptime %d, want 2 and 3", got[1].Seq, got[1].Snapshot.UptimeSec)
	}
}

// TestConfigureDoesNotWaitForSlowCheck: a check stuck in the source does
// not hold up Configure itself.
func TestConfigureDoesNotWaitForSlowCheck(t *testing.T) {
	r := newRig(t, base())
	gate := make(chan struct{})
	r.src.mu.Lock()
	r.src.gate = gate
	r.src.mu.Unlock()
	r.clock.advance(15 * time.Second)
	r.cur.ch <- r.clock.Now() // the loop is now blocked in the source

	done := make(chan error, 1)
	go func() {
		done <- r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 20, SummaryEverySec: 120})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Configure waited for the running check")
	}
	close(gate)
	r.idle()
}

// fakePins records the Bootstrap calls the monitor makes.
type fakePins struct {
	mu    sync.Mutex
	hints []string
}

func (f *fakePins) Bootstrap(hint string, snap proto.HealthSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !snap.MySQLOK {
		panic("Bootstrap called with a failed reading")
	}
	f.hints = append(f.hints, hint)
	return nil
}

func (f *fakePins) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hints...)
}

// TestBootstrapsPinsAfterEachReading: every reading MySQL answered is
// handed to the pins with the panel's latest role hint; failed readings
// are not.
func TestBootstrapsPinsAfterEachReading(t *testing.T) {
	pins := &fakePins{}
	r := newRigWith(t, base(), pins)
	if got := pins.calls(); len(got) != 1 || got[0] != "" {
		t.Fatalf("after the first tick: %q, want one call without a hint", got)
	}
	resultEvent(t, r.dispatch("monitor.configure", proto.MonitorConfigureParams{
		CheckEverySec: 15, SummaryEverySec: 120, RoleHint: "replica"}))
	r.idle() // the loop pauses again with the new interval
	r.tick(15 * time.Second)
	if got := pins.calls(); len(got) != 3 || got[1] != "replica" || got[2] != "replica" {
		t.Fatalf("after configure and a tick: %q", got)
	}
	r.src.set(func(s *proto.HealthSnapshot) { s.MySQLOK, s.MySQLError = false, "mati" })
	r.tick(15 * time.Second)
	resultEvent(t, r.dispatch("monitor.snapshot", nil))
	if got := pins.calls(); len(got) != 3 {
		t.Fatalf("failed readings were bootstrapped: %q", got)
	}
}

// sweepSource is a fakeSource that also sweeps dump accounts
// (DumpSweeper), recording each call.
type sweepSource struct {
	*fakeSource
	mu    sync.Mutex
	calls []time.Duration // olderThan of each call
}

func (s *sweepSource) SweepDumpUsers(_ context.Context, olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, olderThan)
	return 1, nil
}

func (s *sweepSource) sweeps() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.calls...)
}

// TestMonitorSweepsDumpUsersHourly: a source that sweeps dump accounts is
// swept at the first check and then once per SweepEvery (an hour), for
// accounts older than 6 hours; the checks keep their own interval.
func TestMonitorSweepsDumpUsersHourly(t *testing.T) {
	src := &sweepSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.sweeps(); len(got) != 1 || got[0] != 6*time.Hour {
		t.Fatalf("sweeps after the first check = %v, want [6h]", got)
	}
	if r.cur.d != DefaultCheckEvery {
		t.Errorf("loop waits %v, want %v", r.cur.d, DefaultCheckEvery)
	}
	// 59 min 45 s of checks: no second sweep.
	for i := 0; i < 239; i++ {
		r.tick(15 * time.Second)
	}
	if n := len(src.sweeps()); n != 1 {
		t.Fatalf("sweeps before an hour passed = %d, want 1", n)
	}
	r.tick(15 * time.Second)
	if n := len(src.sweeps()); n != 2 {
		t.Fatalf("sweeps after an hour = %d, want 2", n)
	}
	for i := 0; i < 240; i++ {
		r.tick(15 * time.Second)
	}
	if n := len(src.sweeps()); n != 3 {
		t.Errorf("sweeps after two hours = %d, want 3", n)
	}
	if got := DefaultSweepEvery; got != time.Hour {
		t.Errorf("DefaultSweepEvery = %v, want 1h", got)
	}
}

// guardSource is a fakeSource that also guards against dump sessions
// holding the global read lock (DumpLockGuard), recording each call.
type guardSource struct {
	*fakeSource
	mu    sync.Mutex
	calls []time.Duration // maxHold of each call
}

func (s *guardSource) KillDumpLocks(_ context.Context, maxHold time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, maxHold)
	return 0, nil
}

func (s *guardSource) guards() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.calls...)
}

// TestMonitorGuardsDumpLocksEveryCheck: the guard runs after every check
// (not hourly like the sweep), with a one-minute bound (SEC-56).
func TestMonitorGuardsDumpLocksEveryCheck(t *testing.T) {
	src := &guardSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.guards(); len(got) != 1 || got[0] != time.Minute {
		t.Fatalf("guards after the first check = %v, want [1m]", got)
	}
	for i := 0; i < 3; i++ {
		r.tick(15 * time.Second)
	}
	if n := len(src.guards()); n != 4 {
		t.Fatalf("guards after four checks = %d, want 4", n)
	}
}

// seenSource is a fakeSource that also records the replicas reading its
// binary log (ReplicaRecorder), recording each call's time.
type seenSource struct {
	*fakeSource
	mu    sync.Mutex
	calls []time.Time
}

func (s *seenSource) RecordReplicas(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, now)
	return nil
}

func (s *seenSource) records() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

// TestMonitorRecordsReplicasSeenEveryCheck: the replicas reading the
// binary log are recorded after every check, at the monitor's time.
func TestMonitorRecordsReplicasSeenEveryCheck(t *testing.T) {
	src := &seenSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.records(); len(got) != 1 || !got[0].Equal(r.clock.Now()) {
		t.Fatalf("records after the first check = %v, want [%v]", got, r.clock.Now())
	}
	for i := 0; i < 3; i++ {
		r.tick(15 * time.Second)
	}
	got := src.records()
	if len(got) != 4 || !got[3].Equal(r.clock.Now()) {
		t.Errorf("records after four checks = %v", got)
	}
}

// promoteSource is a fakeSource that also guards the promote lock
// (PromoteGuard), recording each call's time.
type promoteSource struct {
	*fakeSource
	mu    sync.Mutex
	calls []time.Time
}

func (s *promoteSource) GuardTick(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, now)
	return nil
}

func (s *promoteSource) ticks() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

// TestMonitorRunsPromoteGuardEveryCheck: the promote-lock guard runs after
// every check, at the monitor's (injectable) time.
func TestMonitorRunsPromoteGuardEveryCheck(t *testing.T) {
	src := &promoteSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.ticks(); len(got) != 1 || !got[0].Equal(r.clock.Now()) {
		t.Fatalf("guard ticks after the first check = %v, want [%v]", got, r.clock.Now())
	}
	for i := 0; i < 3; i++ {
		r.tick(15 * time.Second)
	}
	got := src.ticks()
	if len(got) != 4 || !got[3].Equal(r.clock.Now()) {
		t.Errorf("guard ticks after four checks = %v", got)
	}
}
