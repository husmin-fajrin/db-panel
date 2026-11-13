// Package monitor is the agent's health monitor: it reads a HealthSnapshot
// every CheckEvery (a separate guard loop runs the safety guards every
// GuardEvery), reports to the panel at once when something that matters
// changed and otherwise sends a summary every SummaryEvery. While the panel
// is unreachable, change reports wait in a ring buffer and are sent, oldest
// first, once it is back; summaries are not kept.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Source takes one health reading; it bounds its own time (5 s for MySQL).
// Each reading is a fresh value the monitor keeps; the source must not
// change it afterwards.
type Source interface {
	Snapshot(ctx context.Context) proto.HealthSnapshot
}

// DumpSweeper is a Source that can also drop the per-job dump accounts a
// dead job left behind (mysql.Driver.SweepDumpUsers): those older than
// olderThan. The monitor sweeps every SweepEvery when its Source is one;
// the panel never schedules it.
type DumpSweeper interface {
	SweepDumpUsers(ctx context.Context, olderThan time.Duration) (dropped int, err error)
}

// SalvageSweeper is a Source that can also remove the salvage files
// older than their 7 days (mysql.Driver.SweepSalvage), judged at now. The
// monitor sweeps every SalvageSweepEvery when its Source is one; busy
// means the sweep could not run yet (a salvage is being written) and is
// tried again at the next check.
type SalvageSweeper interface {
	SweepSalvage(now time.Time) (removed int, busy bool, err error)
}

// DumpLockGuard is a Source that can also end the session of a per-job
// dump account that has held the global read lock (FLUSH TABLES WITH READ
// LOCK, which stops every write) for maxHold or longer
// (mysql.Driver.KillDumpLocks). The guard loop runs it every GuardEvery.
type DumpLockGuard interface {
	KillDumpLocks(ctx context.Context, maxHold time.Duration) (killed int, err error)
}

// ReplicaRecorder is a Source that can also record the hosts of the
// replicas reading its binary log, seen at now, in the replicas-seen file
// (mysql.Driver.RecordReplicas). The guard loop runs it every GuardEvery.
type ReplicaRecorder interface {
	RecordReplicas(ctx context.Context, now time.Time) error
}

// PromoteGuard is a Source that also guards a promote lock
// (mysql.Driver.GuardTick): it keeps a locked master read-only and ends
// the lock at its deadline, at now. The guard loop runs it every GuardEvery.
type PromoteGuard interface {
	GuardTick(ctx context.Context, now time.Time) error
}

// EventSender is a Source that sends its guard's events itself
// (mysql.Driver.RunEvents); Run runs it beside its loops and waits for it.
type EventSender interface{ RunEvents(ctx context.Context) }

// Emitter sends an event to the panel; an error (proto.ErrOffline while
// there is no connection) means the event was not delivered.
type Emitter interface {
	Emit(ev proto.Event) error
}

// Default intervals, used until the panel sends monitor.configure.
const (
	DefaultCheckEvery   = 15 * time.Second
	DefaultSummaryEvery = 120 * time.Second
	DefaultLagThreshold = 60 * time.Second
	DefaultSweepEvery   = time.Hour
)

// DumpUserMaxAge is the age past which the sweep drops a dump account that
// has no session left: one that old is taken as left behind by a dead job
// (SEC-56); one still in use is a long dump and waits for a later sweep.
// sweepTimeout bounds one sweep.
const (
	DumpUserMaxAge = 6 * time.Hour
	sweepTimeout   = time.Minute
	// DumpLockMaxHold is how long a dump account may hold the global read
	// lock: mysqldump --single-transaction needs it for a moment only.
	DumpLockMaxHold  = time.Minute
	lockGuardTimeout = 10 * time.Second
	// recordTimeout bounds one RecordReplicas.
	recordTimeout = 10 * time.Second
	// SalvageSweepEvery paces the salvage sweep.
	SalvageSweepEvery = 24 * time.Hour
	// promoteGuardTimeout bounds one GuardTick (a 5 s read of the
	// candidate plus statements on MySQL).
	promoteGuardTimeout = 30 * time.Second
)

// Bounds of the intervals monitor.configure accepts, in seconds.
// check_every_sec up to maxIntervalSec is accepted (an older panel sends up
// to 3600) and applied as at most maxCheckEverySec.
const (
	minIntervalSec   = 10
	maxIntervalSec   = 3600
	maxCheckEverySec = 300
)

// GuardEvery is the pause of the guard loop after one round has finished.
// The panel cannot change it.
const GuardEvery = 5 * time.Second

// warnEvery is how often the same guard warning may be logged.
const warnEvery = time.Minute

// Pinner pins the role of a server configured before role pins existed,
// from a reading MySQL answered and the panel's role hint
// (mysql.Pins.Bootstrap). It writes at most once and decides by itself
// whether there is anything to do.
type Pinner interface {
	Bootstrap(hint string, snap proto.HealthSnapshot) error
}

// Options configure a Monitor; zero fields take their defaults.
type Options struct {
	CheckEvery, SummaryEvery, LagThreshold time.Duration
	Now                                    func() time.Time // default time.Now
	// After returns a channel that receives once d has passed; it paces
	// the loop. Default time.After; tests replace it to step the loop.
	After func(d time.Duration) <-chan time.Time
	// GuardAfter paces the guard loop the same way; default time.After.
	GuardAfter func(d time.Duration) <-chan time.Time
	// Pins, when set, gets every reading MySQL answered (Pinner).
	Pins Pinner
	// SweepEvery paces the dump-account sweep when the Source is a
	// DumpSweeper; default DefaultSweepEvery.
	SweepEvery time.Duration
}

// Monitor runs the check loop and answers the monitor.* commands. The loop
// is one goroutine; command handlers run their checks under the same lock,
// so checks, the backlog and the sequence never interleave.
type Monitor struct {
	src    Source
	em     Emitter
	pins   Pinner
	logger *slog.Logger
	now    func() time.Time
	after  func(d time.Duration) <-chan time.Time
	gafter func(d time.Duration) <-chan time.Time
	warns  warnLimiter
	reconf chan struct{} // wakes the loop to pause again with new options

	mu       sync.Mutex // guards opts and roleHint
	opts     Options
	roleHint string
	clamped  bool // the clamp of check_every_sec was logged already

	checkMu  sync.Mutex // held for a whole check; guards the fields below
	seq      uint64
	prev     proto.HealthSnapshot // last snapshot reported (sent or kept)
	lastSent time.Time            // when the last fresh report reached the panel
	buf      ring

	lastSweep        time.Time // loop goroutine only
	lastSalvageSweep time.Time // loop goroutine only
}

// New returns a Monitor reading src and reporting through em.
func New(src Source, em Emitter, logger *slog.Logger, o Options) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	if o.CheckEvery <= 0 {
		o.CheckEvery = DefaultCheckEvery
	}
	if o.SummaryEvery <= 0 {
		o.SummaryEvery = DefaultSummaryEvery
	}
	if o.LagThreshold <= 0 {
		o.LagThreshold = DefaultLagThreshold
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.GuardAfter == nil {
		o.GuardAfter = time.After
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	return &Monitor{
		src: src, em: em, pins: o.Pins, logger: logger, now: o.Now, after: o.After, gafter: o.GuardAfter,
		reconf: make(chan struct{}, 1), opts: o,
	}
}

// Register adds monitor.configure and monitor.snapshot. Both answer with a
// fresh HealthEvent, which the panel takes as a report.
func (m *Monitor) Register(reg *commands.Registry) {
	reg.Register("monitor.configure", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.MonitorConfigureParams
		if len(params) == 0 {
			return nil, &commands.ParamError{Msg: "parameter tidak valid"}
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := m.Configure(p); err != nil {
			return nil, err
		}
		m.checkMu.Lock()
		defer m.checkMu.Unlock()
		m.flush()
		return m.forcedCheck(ctx)
	})
	reg.Register("monitor.snapshot", func(ctx context.Context, _ json.RawMessage) (any, error) {
		m.checkMu.Lock()
		defer m.checkMu.Unlock()
		return m.forcedCheck(ctx)
	})
}

// Configure validates and applies the panel's settings; the loop's next
// pause uses the new check interval.
func (m *Monitor) Configure(p proto.MonitorConfigureParams) error {
	if !validInterval(p.CheckEverySec) {
		return rangeError("check_every_sec", maxCheckEverySec)
	}
	if !validInterval(p.SummaryEverySec) {
		return rangeError("summary_every_sec", maxIntervalSec)
	}
	if p.LagThresholdSec != 0 && !validInterval(p.LagThresholdSec) {
		return rangeError("lag_threshold_sec", maxIntervalSec)
	}
	switch p.RoleHint {
	case "", "master", "replica":
	default:
		return &commands.ParamError{Msg: "role_hint harus master, replica atau kosong"}
	}
	checkSec := p.CheckEverySec
	m.mu.Lock()
	logClamp := false
	if checkSec > maxCheckEverySec {
		checkSec = maxCheckEverySec
		logClamp, m.clamped = !m.clamped, true
	}
	m.opts.CheckEvery = time.Duration(checkSec) * time.Second
	m.opts.SummaryEvery = time.Duration(p.SummaryEverySec) * time.Second
	if p.LagThresholdSec != 0 {
		m.opts.LagThreshold = time.Duration(p.LagThresholdSec) * time.Second
	}
	m.roleHint = p.RoleHint
	m.mu.Unlock()
	if logClamp {
		m.logger.Info("check_every_sec above the maximum applied as the maximum",
			"asked", p.CheckEverySec, "applied", maxCheckEverySec)
	}
	select {
	case m.reconf <- struct{}{}:
	default:
	}
	return nil
}

func validInterval(sec int) bool { return sec >= minIntervalSec && sec <= maxIntervalSec }

func rangeError(field string, max int) error {
	return &commands.ParamError{Msg: fmt.Sprintf("%s harus %d sampai %d detik", field, minIntervalSec, max)}
}

// LastRoleHint is the RoleHint of the last accepted monitor.configure.
func (m *Monitor) LastRoleHint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.roleHint
}

func (m *Monitor) options() Options {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opts
}

// Run runs two loops until ctx is done and returns when both have. The
// check loop checks at once, then every CheckEvery, sweeping the dump
// accounts when SweepEvery has passed since the last sweep and the salvage
// files when SalvageSweepEvery has (the first sweeps at once). The guard
// loop (guardLoop) runs the safety guards every GuardEvery, whatever
// CheckEvery is. When the Source is an EventSender its sender runs beside
// them, and Run waits for it too.
func (m *Monitor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.guardLoop(ctx)
	}()
	if s, ok := m.src.(EventSender); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RunEvents(ctx)
		}()
	}
	for {
		m.tick(ctx)
		m.sweep(ctx)
		m.sweepSalvage(ctx)
		if !m.pause(ctx) {
			break
		}
	}
	wg.Wait()
}

// guardLoop runs the guards, then pauses GuardEvery after the round has
// finished (never a ticker, so rounds cannot overlap). It touches none of
// the check loop's state.
func (m *Monitor) guardLoop(ctx context.Context) {
	for {
		m.guardDumpLocks(ctx)
		m.guardPromote(ctx)
		m.recordReplicas(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.gafter(GuardEvery):
		}
	}
}

// warnLimiter lets the same warning through at most once a minute.
type warnLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow reports whether the warning with this key may be logged at now, and
// counts it as logged when it is.
func (l *warnLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[key]; ok && now.Sub(t) < warnEvery {
		return false
	}
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	for k, t := range l.last {
		if now.Sub(t) >= warnEvery {
			delete(l.last, k)
		}
	}
	l.last[key] = now
	return true
}

// warn logs msg with err at Warn, at most once a minute per msg and error text.
func (m *Monitor) warn(msg string, err error) {
	if m.warns.allow(msg+"\x00"+err.Error(), m.now()) {
		m.logger.Warn(msg, "err", err)
	}
}

// sweep drops the stale dump accounts when the Source is a DumpSweeper
// and a sweep is due. A failed sweep is tried again a SweepEvery later.
func (m *Monitor) sweep(ctx context.Context) {
	sw, ok := m.src.(DumpSweeper)
	if !ok || ctx.Err() != nil {
		return
	}
	now := m.now()
	if !m.lastSweep.IsZero() && now.Sub(m.lastSweep) < m.options().SweepEvery {
		return
	}
	m.lastSweep = now
	sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	n, err := sw.SweepDumpUsers(sctx, DumpUserMaxAge)
	switch {
	case err != nil:
		m.logger.Warn("stale dump users not swept", "err", err)
	case n > 0:
		m.logger.Info("stale dump users swept", "dropped", n)
	}
}

// sweepSalvage removes the expired salvage files when the Source is a
// SalvageSweeper and a sweep is due; a failed one is tried a day later, a
// busy one at the next check.
func (m *Monitor) sweepSalvage(ctx context.Context) {
	sw, ok := m.src.(SalvageSweeper)
	if !ok || ctx.Err() != nil {
		return
	}
	now := m.now()
	if !m.lastSalvageSweep.IsZero() && now.Sub(m.lastSalvageSweep) < SalvageSweepEvery {
		return
	}
	n, busy, err := sw.SweepSalvage(now)
	if busy {
		m.logger.Info("sapuan salvage ditunda: sedang ada ekstraksi")
		return
	}
	m.lastSalvageSweep = now
	switch {
	case err != nil:
		m.logger.Warn("salvage files not swept", "err", err)
	case n > 0:
		m.logger.Info("expired salvage files removed", "removed", n)
	}
}

// guardDumpLocks runs the Source's DumpLockGuard, when it is one.
func (m *Monitor) guardDumpLocks(ctx context.Context) {
	g, ok := m.src.(DumpLockGuard)
	if !ok || ctx.Err() != nil {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, lockGuardTimeout)
	defer cancel()
	if n, err := g.KillDumpLocks(gctx, DumpLockMaxHold); err != nil {
		m.logger.Debug("dump lock guard", "err", err)
	} else if n > 0 {
		m.logger.Warn("dump sessions holding the global read lock killed", "killed", n)
	}
}

// guardPromote runs the Source's PromoteGuard, when it is one.
func (m *Monitor) guardPromote(ctx context.Context) {
	g, ok := m.src.(PromoteGuard)
	if !ok || ctx.Err() != nil {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, promoteGuardTimeout)
	defer cancel()
	if err := g.GuardTick(gctx, m.now()); err != nil {
		m.warn("promote lock guard", err)
	}
}

// recordReplicas runs the Source's ReplicaRecorder, when it is one.
func (m *Monitor) recordReplicas(ctx context.Context) {
	r, ok := m.src.(ReplicaRecorder)
	if !ok || ctx.Err() != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()
	if err := r.RecordReplicas(rctx, m.now()); err != nil {
		m.warn("replicas seen not recorded", err)
	}
}

// pause waits CheckEvery, starting over when Configure changes it. It
// reports false once ctx is done.
func (m *Monitor) pause(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-m.after(m.options().CheckEvery):
			return true
		case <-m.reconf:
		}
	}
}

// tick is one scheduled check: send what the backlog holds, read a
// snapshot, send its lag sample, and report it when it changed or a
// summary is due. A change the panel cannot take now joins the backlog;
// so does any change while older entries still wait, keeping the panel's
// view in order.
func (m *Monitor) tick(ctx context.Context) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	m.flush()
	o := m.options()
	snap := m.src.Snapshot(ctx)
	if ctx.Err() != nil {
		return // stopping: the reading may have been cut short
	}
	m.bootstrap(snap)
	takenAt := m.now()
	m.emitSample(snap, takenAt)
	change := changed(m.prev, snap, o.LagThreshold, diskWarnPct, diskCritPct, connWarnPct)
	if !change && takenAt.Sub(m.lastSent) < o.SummaryEvery {
		return
	}
	e := entry{ev: proto.HealthEvent{Seq: m.seq + 1, Snapshot: snap}, takenAt: takenAt}
	if m.buf.Len() == 0 {
		err := m.emit(e, false)
		if err == nil {
			m.seq, m.prev, m.lastSent = e.ev.Seq, snap, m.now()
			return
		}
		m.logger.Debug("health report not sent", "seq", e.ev.Seq, "change", change, "err", err)
	}
	if change {
		m.seq, m.prev = e.ev.Seq, snap
		m.buf.Push(e)
	}
}

// forcedCheck reads a snapshot now and returns it as the newest report,
// for a command to answer with. A command cancelled during the reading
// (the panel gave up or went away) gets ctx's error and the reading,
// possibly cut short, is not kept. The caller holds checkMu.
func (m *Monitor) forcedCheck(ctx context.Context) (proto.HealthEvent, error) {
	snap := m.src.Snapshot(ctx)
	if err := ctx.Err(); err != nil {
		return proto.HealthEvent{}, err
	}
	m.bootstrap(snap)
	takenAt := m.now()
	m.seq++
	m.prev, m.lastSent = snap, takenAt
	return proto.HealthEvent{Seq: m.seq, TakenAgoMS: m.now().Sub(takenAt).Milliseconds(), Snapshot: snap}, nil
}

// bootstrap hands a reading MySQL answered to the pins with the panel's
// latest role hint. The caller holds checkMu.
func (m *Monitor) bootstrap(snap proto.HealthSnapshot) {
	if m.pins == nil || !snap.MySQLOK {
		return
	}
	if err := m.pins.Bootstrap(m.LastRoleHint(), snap); err != nil {
		m.logger.Warn("role pin not bootstrapped", "err", err)
	}
}

// flush sends the backlog oldest first, stopping at the first failure; the
// entries not sent stay for the next attempt. The caller holds checkMu.
func (m *Monitor) flush() {
	items := m.buf.Drain()
	for i, e := range items {
		if err := m.emit(e, true); err != nil {
			for _, rest := range items[i:] {
				m.buf.Push(rest)
			}
			m.logger.Debug("health backlog not sent", "kept", len(items)-i, "err", err)
			return
		}
	}
	if len(items) > 0 {
		m.logger.Info("health backlog sent", "reports", len(items))
	}
}

// emit sends e with its age as of now.
func (m *Monitor) emit(e entry, backlog bool) error {
	ev := e.ev
	ev.Backlog = backlog
	ev.TakenAgoMS = m.now().Sub(e.takenAt).Milliseconds()
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return m.em.Emit(proto.Event{Type: proto.EventHealth, Payload: b})
}

// maxSampleState bounds the thread states a sample carries (Yes, No and
// Connecting fit); maxSampleBytes is the most JSON a sample may be.
const (
	maxSampleState = 16
	maxSampleBytes = 200
)

// emitSample sends snap's lag sample (proto.EventSample), taken at
// takenAt. It is sent on every scheduled check and never kept: when the
// panel cannot take it the chart loses one point only.
func (m *Monitor) emitSample(snap proto.HealthSnapshot, takenAt time.Time) {
	se := proto.SampleEvent{TakenAgoMS: m.now().Sub(takenAt).Milliseconds(), MySQLOK: snap.MySQLOK}
	if rs := snap.Replica; rs != nil {
		se.IORunning, se.SQLRunning = clip(rs.IORunning, maxSampleState), clip(rs.SQLRunning, maxSampleState)
		if rs.SecondsBehind != nil {
			lag := *rs.SecondsBehind
			se.LagSec = &lag
		}
	}
	b, err := json.Marshal(se)
	if err == nil && len(b) > maxSampleBytes {
		// States JSON escapes past the limit (control characters,
		// <, > or &): send the sample without them.
		se.IORunning, se.SQLRunning = "", ""
		b, err = json.Marshal(se)
	}
	if err != nil {
		return
	}
	if err := m.em.Emit(proto.Event{Type: proto.EventSample, Payload: b}); err != nil {
		m.logger.Debug("lag sample not sent", "err", err)
	}
}

// clip is s as valid UTF-8, cut to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
