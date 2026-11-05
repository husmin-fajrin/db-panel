// Package monitor is the agent's health monitor: it reads a HealthSnapshot
// every CheckEvery, reports to the panel at once when something that matters
// changed and otherwise sends a summary every SummaryEvery. While the panel
// is unreachable, change reports wait in a ring buffer and are sent, oldest
// first, once it is back; summaries are not kept.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

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

// DumpUserMaxAge is the age past which the sweep drops a dump account: no
// dump job runs that long. sweepTimeout bounds one sweep.
const (
	DumpUserMaxAge = 24 * time.Hour
	sweepTimeout   = time.Minute
)

// Bounds of the intervals monitor.configure accepts, in seconds.
const (
	minIntervalSec = 10
	maxIntervalSec = 3600
)

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
	reconf chan struct{} // wakes the loop to pause again with new options

	mu       sync.Mutex // guards opts and roleHint
	opts     Options
	roleHint string

	checkMu  sync.Mutex // held for a whole check; guards the fields below
	seq      uint64
	prev     proto.HealthSnapshot // last snapshot reported (sent or kept)
	lastSent time.Time            // when the last fresh report reached the panel
	buf      ring

	lastSweep time.Time // loop goroutine only
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
	if o.SweepEvery <= 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	return &Monitor{
		src: src, em: em, pins: o.Pins, logger: logger, now: o.Now, after: o.After,
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
		return rangeError("check_every_sec")
	}
	if !validInterval(p.SummaryEverySec) {
		return rangeError("summary_every_sec")
	}
	if p.LagThresholdSec != 0 && !validInterval(p.LagThresholdSec) {
		return rangeError("lag_threshold_sec")
	}
	switch p.RoleHint {
	case "", "master", "replica":
	default:
		return &commands.ParamError{Msg: "role_hint harus master, replica atau kosong"}
	}
	m.mu.Lock()
	m.opts.CheckEvery = time.Duration(p.CheckEverySec) * time.Second
	m.opts.SummaryEvery = time.Duration(p.SummaryEverySec) * time.Second
	if p.LagThresholdSec != 0 {
		m.opts.LagThreshold = time.Duration(p.LagThresholdSec) * time.Second
	}
	m.roleHint = p.RoleHint
	m.mu.Unlock()
	select {
	case m.reconf <- struct{}{}:
	default:
	}
	return nil
}

func validInterval(sec int) bool { return sec >= minIntervalSec && sec <= maxIntervalSec }

func rangeError(field string) error {
	return &commands.ParamError{Msg: fmt.Sprintf("%s harus %d sampai %d detik", field, minIntervalSec, maxIntervalSec)}
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

// Run checks at once, then every CheckEvery, until ctx is done. After a
// check it sweeps the dump accounts when SweepEvery has passed since the
// last sweep (the first one at once).
func (m *Monitor) Run(ctx context.Context) {
	for {
		m.tick(ctx)
		m.sweep(ctx)
		if !m.pause(ctx) {
			return
		}
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
// snapshot, and report it when it changed or a summary is due. A change
// the panel cannot take now joins the backlog; so does any change while
// older entries still wait, keeping the panel's view in order.
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
