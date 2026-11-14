package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
// more health events before going offline; failOnce refuses the next
// health event only. Samples (monitor.sample) are refused while offline
// and otherwise recorded apart, never counted against acceptLeft or
// failOnce.
type fakeEmitter struct {
	mu          sync.Mutex
	offline     bool
	acceptLeft  int
	failOnce    bool
	events      []proto.HealthEvent
	samples     []proto.SampleEvent
	sampleBytes []int
}

func (e *fakeEmitter) Emit(ev proto.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.offline {
		return proto.ErrOffline
	}
	if ev.Type == proto.EventSample {
		var se proto.SampleEvent
		if err := json.Unmarshal(ev.Payload, &se); err != nil {
			panic(err)
		}
		e.samples = append(e.samples, se)
		e.sampleBytes = append(e.sampleBytes, len(ev.Payload))
		return nil
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

func (e *fakeEmitter) gotSamples() ([]proto.SampleEvent, []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]proto.SampleEvent(nil), e.samples...), append([]int(nil), e.sampleBytes...)
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
	// gwaits and gcur are the same for the guard loop.
	gwaits chan wait
	gcur   wait
	logs   *logBuf
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
		t:      t,
		src:    src,
		em:     &fakeEmitter{acceptLeft: -1},
		clock:  &fakeClock{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)},
		reg:    commands.NewRegistry(),
		ctx:    ctx,
		waits:  make(chan wait),
		gwaits: make(chan wait),
		logs:   &logBuf{},
	}
	r.m = New(monSrc, r.em, slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Options{
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
		GuardAfter: func(d time.Duration) <-chan time.Time {
			w := wait{d: d, ch: make(chan time.Time, 1)}
			select {
			case r.gwaits <- w:
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
	r.idle()  // the first tick runs at once
	r.gidle() // so does the first guard round
	return r
}

// logBuf is a log sink the test can read while the monitor writes to it.
type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// count is the number of log lines containing sub.
func (l *logBuf) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// gidle waits until the guard loop pauses again.
func (r *rig) gidle() {
	r.t.Helper()
	select {
	case r.gcur = <-r.gwaits:
	case <-time.After(5 * time.Second):
		r.t.Fatal("guard loop did not pause")
	}
}

// gtick advances the clock by d, ends the guard loop's pause and waits for
// its round to finish.
func (r *rig) gtick(d time.Duration) {
	r.t.Helper()
	r.clock.advance(d)
	r.gcur.ch <- r.clock.Now()
	r.gidle()
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

// TestReportsOnStartJudged: a start judged between two checks is reported
// at the next check with nothing else changed; the same state with only
// its ages advanced is not.
func TestReportsOnStartJudged(t *testing.T) {
	r := newRig(t, base())
	r.src.set(func(s *proto.HealthSnapshot) {
		s.StartResult = &proto.StartResultState{Result: "late", StartID: "2026-11-14T01:00:00Z",
			Kinds: []proto.StartKindState{{Kind: "late", StartID: "2026-11-14T01:00:00Z"}}}
	})
	r.tick(15 * time.Second)
	got := r.wantReports(2)
	if sr := got[1].Snapshot.StartResult; sr == nil || sr.Result != "late" {
		t.Fatalf("change report = %+v", got[1].Snapshot.StartResult)
	}
	r.src.set(func(s *proto.HealthSnapshot) {
		s.StartResult = &proto.StartResultState{Result: "late", StartID: "2026-11-14T01:00:00Z", AgeSec: 15,
			Kinds: []proto.StartKindState{{Kind: "late", StartID: "2026-11-14T01:00:00Z", AgeSec: 15}}}
	})
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

// TestConfigureAcceptsQuarantinedHint: a panel that sends "quarantined"
// for a quarantined server is accepted (its intervals apply) and the hint
// counts as "": it never pins anything.
func TestConfigureAcceptsQuarantinedHint(t *testing.T) {
	r := newRig(t, base())
	if err := r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 30, SummaryEverySec: 600, RoleHint: "quarantined"}); err != nil {
		t.Fatalf("configure with role_hint quarantined: %v", err)
	}
	if o := r.m.options(); o.CheckEvery != 30*time.Second || o.SummaryEvery != 600*time.Second {
		t.Errorf("options = %+v, want the panel's intervals", o)
	}
	if h := r.m.LastRoleHint(); h != "" {
		t.Errorf("role hint = %q, want \"\"", h)
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

// TestMonitorGuardsDumpLocksEveryRound: the guard runs every guard round
// (not hourly like the sweep), with a one-minute bound (SEC-56).
func TestMonitorGuardsDumpLocksEveryRound(t *testing.T) {
	src := &guardSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.guards(); len(got) != 1 || got[0] != time.Minute {
		t.Fatalf("guards after the first check = %v, want [1m]", got)
	}
	for i := 0; i < 3; i++ {
		r.gtick(GuardEvery)
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

// TestMonitorRecordsReplicasSeenEveryRound: the replicas reading the
// binary log are recorded every guard round, at the monitor's time.
func TestMonitorRecordsReplicasSeenEveryRound(t *testing.T) {
	src := &seenSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.records(); len(got) != 1 || !got[0].Equal(r.clock.Now()) {
		t.Fatalf("records after the first check = %v, want [%v]", got, r.clock.Now())
	}
	for i := 0; i < 3; i++ {
		r.gtick(GuardEvery)
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

// TestMonitorRunsPromoteGuardEveryRound: the promote-lock guard runs after
// every check, at the monitor's (injectable) time.
func TestMonitorRunsPromoteGuardEveryRound(t *testing.T) {
	src := &promoteSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if got := src.ticks(); len(got) != 1 || !got[0].Equal(r.clock.Now()) {
		t.Fatalf("guard ticks after the first check = %v, want [%v]", got, r.clock.Now())
	}
	for i := 0; i < 3; i++ {
		r.gtick(GuardEvery)
	}
	got := src.ticks()
	if len(got) != 4 || !got[3].Equal(r.clock.Now()) {
		t.Errorf("guard ticks after four checks = %v", got)
	}
}

// salvageSource is a fakeSource that also sweeps salvage files
// (SalvageSweeper), recording the time of each call.
type salvageSource struct {
	*fakeSource
	mu    sync.Mutex
	calls []time.Time
	busy  int // how many of the next calls report busy
}

func (s *salvageSource) SweepSalvage(now time.Time) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, now)
	if s.busy > 0 {
		s.busy--
		return 0, true, nil
	}
	return 0, false, nil
}

func (s *salvageSource) sweeps() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// TestMonitorSweepsSalvageDaily: a source that keeps salvage files is
// swept at the first check and then once a day, at the monitor's clock.
func TestMonitorSweepsSalvageDaily(t *testing.T) {
	src := &salvageSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	if n := src.sweeps(); n != 1 {
		t.Fatalf("salvage sweeps after the first check = %d, want 1", n)
	}
	if !src.calls[0].Equal(r.clock.Now()) {
		t.Errorf("swept at %v, want the monitor's clock %v", src.calls[0], r.clock.Now())
	}
	r.tick(SalvageSweepEvery - time.Minute)
	if n := src.sweeps(); n != 1 {
		t.Fatalf("salvage sweeps before a day passed = %d, want 1", n)
	}
	r.tick(time.Minute)
	if n := src.sweeps(); n != 2 {
		t.Errorf("salvage sweeps after a day = %d, want 2", n)
	}
	if SalvageSweepEvery != 24*time.Hour {
		t.Errorf("SalvageSweepEvery = %v, want 24h", SalvageSweepEvery)
	}
}

// TestMonitorRetriesBusySalvageSweep: a sweep that reports busy is tried
// again at the next check, not a day later; once it ran, the day counts
// from then.
func TestMonitorRetriesBusySalvageSweep(t *testing.T) {
	src := &salvageSource{fakeSource: &fakeSource{snap: base()}, busy: 2}
	r := startRig(t, src.fakeSource, src, nil)
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	if n := src.sweeps(); n != 3 {
		t.Fatalf("sweeps after two busy rounds and one more check = %d, want 3", n)
	}
	ran := r.clock.Now()
	r.tick(SalvageSweepEvery - 30*time.Second)
	if n := src.sweeps(); n != 3 {
		t.Fatalf("sweeps before a day after the one that ran = %d, want 3", n)
	}
	r.tick(30 * time.Second)
	src.mu.Lock()
	defer src.mu.Unlock()
	if n := len(src.calls); n != 4 || !src.calls[3].Equal(ran.Add(SalvageSweepEvery)) {
		t.Errorf("sweeps a day after the one that ran = %d (%v), want 4", n, src.calls)
	}
}

// TestAgentEmitsSampleEveryCheck (spec 8.1): every scheduled check sends
// one monitor.sample, whether or not a health report goes out; a sample
// carries the lag, the thread states and MySQLOK, stays within 200 bytes
// and is never kept in the backlog while the panel is away.
func TestAgentEmitsSampleEveryRound(t *testing.T) {
	r := newRig(t, base())
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	if reports := r.em.got(); len(reports) > 1 {
		t.Fatalf("3 checks without a change sent %d health reports, want at most 1", len(reports))
	}
	samples, sizes := r.em.gotSamples()
	if len(samples) != 3 {
		t.Fatalf("3 checks sent %d samples, want 3", len(samples))
	}
	for i, s := range samples {
		if s.LagSec == nil || *s.LagSec != 5 || s.IORunning != "Yes" || s.SQLRunning != "Yes" || !s.MySQLOK || s.TakenAgoMS != 0 {
			t.Errorf("sample %d = %+v, want lag 5, Yes/Yes, mysql ok, taken now", i, s)
		}
	}

	// The largest usual sample: the longest lag, Connecting.
	r.src.set(func(s *proto.HealthSnapshot) {
		s.MySQLOK = true
		s.Replica.IORunning, s.Replica.SQLRunning = "Connecting", "No"
		big := int64(1<<62 - 1)
		s.Replica.SecondsBehind = &big
	})
	r.tick(15 * time.Second)
	// MySQL not answering: no replica fields, lag unknown.
	r.src.set(func(s *proto.HealthSnapshot) { *s = proto.HealthSnapshot{MySQLError: "dial tcp: connection refused"} })
	r.tick(15 * time.Second)
	samples, sizes = r.em.gotSamples()
	if len(samples) != 5 {
		t.Fatalf("5 checks sent %d samples, want 5", len(samples))
	}
	if s := samples[4]; s.MySQLOK || s.LagSec != nil || s.IORunning != "" || s.SQLRunning != "" {
		t.Errorf("sample without MySQL = %+v", s)
	}
	for i, n := range sizes {
		if n > 200 {
			t.Errorf("sample %d is %d bytes of JSON, want at most 200", i, n)
		}
	}

	// Offline: samples are dropped, not kept for later.
	r.em.setOnline(false, -1)
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	r.em.setOnline(true, -1)
	r.tick(15 * time.Second)
	samples, _ = r.em.gotSamples()
	if len(samples) != 6 {
		t.Fatalf("after 2 offline checks and 1 online: %d samples, want 6 (none from the backlog)", len(samples))
	}

	// Long, odd thread states: clipped on a rune boundary, and the JSON
	// stays within 200 bytes even when every byte needs escaping.
	big := int64(-1 << 63)
	for i, st := range [][2]string{
		{strings.Repeat("Connecting", 10), "x" + strings.Repeat("é", 40)},
		{strings.Repeat("<", 40), strings.Repeat("\x01", 40)},
		{strings.Repeat("&", 40), "\xff\xfe" + strings.Repeat("\x00", 40)},
	} {
		r.src.set(func(s *proto.HealthSnapshot) {
			*s = base()
			s.Replica.IORunning, s.Replica.SQLRunning, s.Replica.SecondsBehind = st[0], st[1], &big
		})
		r.tick(15 * time.Second)
		samples, sizes = r.em.gotSamples()
		last, n := samples[len(samples)-1], sizes[len(sizes)-1]
		if n > 200 {
			t.Errorf("case %d: sample is %d bytes of JSON, want at most 200", i, n)
		}
		if len(last.IORunning) > 16 || len(last.SQLRunning) > 16 || !utf8.ValidString(last.SQLRunning) || last.LagSec == nil {
			t.Errorf("case %d: sample = %+v", i, last)
		}
	}
	if s := samples[len(samples)-3]; s.IORunning != "ConnectingConnec" || s.SQLRunning != "x"+strings.Repeat("é", 7) {
		t.Errorf("long states = %q, %q; want 16 bytes cut on a rune boundary", s.IORunning, s.SQLRunning)
	}
}

// countingSource is a guard source that counts each guard call and, for
// GuardTick, can hold for a fake-clock duration and return an error.
type countingSource struct {
	*fakeSource
	mu    sync.Mutex
	kills []time.Duration
	ticks int
	recs  int
	gate  chan struct{} // when set, GuardTick waits for it
	err   error
	cur   int
	max   int
}

func (s *countingSource) KillDumpLocks(_ context.Context, maxHold time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, maxHold)
	return 0, nil
}

func (s *countingSource) GuardTick(_ context.Context, _ time.Time) error {
	s.mu.Lock()
	s.ticks++
	s.cur++
	if s.cur > s.max {
		s.max = s.cur
	}
	gate, err := s.gate, s.err
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	s.mu.Lock()
	s.cur--
	s.mu.Unlock()
	return err
}

func (s *countingSource) RecordReplicas(_ context.Context, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs++
	return nil
}

func (s *countingSource) counts() (kills, ticks, recs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.kills), s.ticks, s.recs
}

func newCountingRig(t *testing.T) (*rig, *countingSource) {
	t.Helper()
	src := &countingSource{fakeSource: &fakeSource{snap: base()}}
	r := startRig(t, src.fakeSource, src, nil)
	return r, src
}

// TestGuardLoopRunsEveryFiveSecondsAtCheckEvery300: with the check interval
// at its 300 s maximum, the guards still run every 5 s.
func TestGuardLoopRunsEveryFiveSecondsAtCheckEvery300(t *testing.T) {
	r, src := newCountingRig(t)
	if err := r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 300, SummaryEverySec: 300}); err != nil {
		t.Fatal(err)
	}
	r.tick(15 * time.Second) // the loop pauses again with the new interval
	if r.cur.d != 300*time.Second {
		t.Fatalf("check loop asks for a pause of %v, want 300s", r.cur.d)
	}
	k0, t0, c0 := src.counts()
	for i := 0; i < 10; i++ {
		if r.gcur.d != 5*time.Second {
			t.Fatalf("step %d: guard loop asks for %v, want 5s", i, r.gcur.d)
		}
		r.gtick(5 * time.Second)
		k, tk, c := src.counts()
		if k != k0+i+1 || tk != t0+i+1 || c != c0+i+1 {
			t.Fatalf("step %d: kills/ticks/records = %d/%d/%d, want %d each", i, k, tk, c, k0+i+1)
		}
	}
	if src.kills[0] != time.Minute {
		t.Errorf("KillDumpLocks maxHold = %v, want 1m", src.kills[0])
	}
}

// TestGuardLoopSleepsAfterCompletion: the pause is asked for only after the
// round has finished, and rounds never overlap.
func TestGuardLoopSleepsAfterCompletion(t *testing.T) {
	r, src := newCountingRig(t)
	gate := make(chan struct{})
	src.mu.Lock()
	src.gate = gate
	src.mu.Unlock()
	r.gcur.ch <- r.clock.Now() // starts a round that stops inside GuardTick
	select {
	case w := <-r.gwaits:
		t.Fatalf("guard loop asked for a pause of %v while GuardTick was still running", w.d)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	r.gidle()
	if r.gcur.d != GuardEvery {
		t.Errorf("pause = %v, want %v", r.gcur.d, GuardEvery)
	}
	r.gtick(5 * time.Second)
	_, ticks, _ := src.counts()
	if ticks != 3 {
		t.Fatalf("GuardTick ran %d times, want 3", ticks)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.max != 1 {
		t.Errorf("GuardTick ran %d at once, want 1", src.max)
	}
}

// TestGuardWarningsRateLimited: the same guard warning is logged once a
// minute; a different error text is logged at once.
func TestGuardWarningsRateLimited(t *testing.T) {
	r, src := newCountingRig(t)
	src.mu.Lock()
	src.err = errors.New("kunci gagal")
	src.mu.Unlock()
	for i := 0; i < 20; i++ { // 40 s of rounds: inside one minute
		r.gtick(2 * time.Second)
	}
	if n := r.logs.count("promote lock guard"); n != 1 {
		t.Fatalf("%d warnings in 40+ s, want 1", n)
	}
	r.gtick(61 * time.Second)
	if n := r.logs.count("promote lock guard"); n != 2 {
		t.Fatalf("%d warnings after 61 s more, want 2", n)
	}
	src.mu.Lock()
	src.err = errors.New("kunci lain")
	src.mu.Unlock()
	r.gtick(time.Second)
	if n := r.logs.count("kunci lain"); n != 1 {
		t.Fatalf("a different error was logged %d times, want 1", n)
	}
}

func TestWarnLimiter(t *testing.T) {
	var l warnLimiter
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if !l.allow("a", t0) || l.allow("a", t0.Add(59*time.Second)) || !l.allow("b", t0.Add(time.Second)) {
		t.Fatal("first use must pass, repeat within a minute must not, other key must")
	}
	if !l.allow("a", t0.Add(time.Minute)) {
		t.Fatal("a minute later must pass")
	}
}

// TestConfigureClampsCheckEveryTo300: an older panel's 3600 is applied as
// 300; below 10 is refused; the summary interval keeps its 3600 bound.
func TestConfigureClampsCheckEveryTo300(t *testing.T) {
	r := newRig(t, base())
	if err := r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 3600, SummaryEverySec: 3600}); err != nil {
		t.Fatalf("3600 refused: %v", err)
	}
	if o := r.m.options(); o.CheckEvery != 300*time.Second || o.SummaryEvery != time.Hour {
		t.Fatalf("options = %+v, want check 300s summary 1h", o)
	}
	r.tick(15 * time.Second)
	if r.cur.d != 300*time.Second {
		t.Errorf("check loop pauses %v, want 300s", r.cur.d)
	}
	if n := r.logs.count("applied as the maximum"); n != 1 {
		t.Errorf("clamp logged %d times, want 1", n)
	}
	r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 3600, SummaryEverySec: 120})
	if n := r.logs.count("applied as the maximum"); n != 1 {
		t.Errorf("clamp logged %d times after a second configure, want 1", n)
	}
	err := r.m.Configure(proto.MonitorConfigureParams{CheckEverySec: 9, SummaryEverySec: 120})
	var pe *commands.ParamError
	if !errors.As(err, &pe) || pe.Msg != "check_every_sec harus 10 sampai 300 detik" {
		t.Fatalf("Configure(9) = %v", err)
	}
}

// TestSnapshotCommandRunsNoGuard: monitor.snapshot and monitor.configure
// run none of the guards.
func TestSnapshotCommandRunsNoGuard(t *testing.T) {
	r, src := newCountingRig(t)
	k0, t0, c0 := src.counts()
	r.dispatch("monitor.snapshot", nil)
	r.dispatch("monitor.configure", proto.MonitorConfigureParams{CheckEverySec: 20, SummaryEverySec: 120})
	if k, tk, c := src.counts(); k != k0 || tk != t0 || c != c0 {
		t.Fatalf("guards ran for a command: %d/%d/%d, was %d/%d/%d", k, tk, c, k0, t0, c0)
	}
}
