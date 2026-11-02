package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	maxPayload       = 64 << 10 // largest health report accepted
	configureTimeout = 30 * time.Second
	checkNowEvery    = 10 * time.Second // per server
	eventRetention   = 90 * 24 * time.Hour
	pruneEvery       = 24 * time.Hour
	defaultTickEvery = 5 * time.Second
)

// ErrCheckTooSoon answers a CheckNow within 10 s of the previous one.
var ErrCheckTooSoon = errors.New("tunggu 10 detik sebelum memeriksa lagi")

// errNeedsUpgrade answers a CheckNow to an agent without monitor.*.
var errNeedsUpgrade = errors.New("versi agent perlu dimutakhirkan untuk pemeriksaan ini")

// Deps are what a Monitor works with.
type Deps struct {
	Store   *store.Store
	Hub     *hub.Hub
	Publish func(event string, data any) // SSE; nil publishes nothing
	Logger  *slog.Logger                 // nil means slog.Default()
	Now     func() time.Time             // nil means time.Now
	// TickEvery paces the re-evaluation of time-based statuses; 0 means 5 s.
	TickEvery time.Duration
	// OnAlertsChanged is called, without the Monitor's lock, after alerts
	// were opened or closed (the notifier's wake-up); nil means none.
	OnAlertsChanged func()
}

// View is what the panel knows of one server's health.
type View struct {
	ServerID int64
	// Role is store.RoleMaster or store.RoleReplica; "" when the server is
	// in no topology and so not monitored (Status is then zero).
	Role         string
	Status       Status
	Since        time.Time             // when Status.Code took its current value
	ReceivedAt   time.Time             // when the last report was taken, by the panel's clock
	Snap         *proto.HealthSnapshot // shared with the Monitor: read only
	NeedsUpgrade bool                  // the agent does not know monitor.*
	SnoozeUntil  *time.Time
}

// Monitor ingests the agents' health reports, derives each server's status,
// records the timeline (status_events) and tells browsers (health_update).
// Report handlers, the tick and the background monitor.configure calls
// share the per-server state under one mutex, which is never held across
// a Hub.Call.
type Monitor struct {
	st        *store.Store
	hub       *hub.Hub
	publish   func(event string, data any)
	log       *slog.Logger
	now       func() time.Time
	tickEvery time.Duration
	startedAt time.Time

	ctx    context.Context // background calls; canceled by Close
	cancel context.CancelFunc
	bg     sync.WaitGroup

	mu        sync.Mutex
	closed    bool
	settings  Settings
	servers   map[int64]*serverState
	lastCheck map[int64]time.Time // last CheckNow per server
	lastPrune time.Time
	failedJob map[int64]string // per server, the job its open job_failed is about

	onAlertsChanged func() // set once by New
}

// New returns a Monitor holding the health rows the store kept from the
// last run, so views have data before the first report arrives.
func New(d Deps) *Monitor {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.TickEvery <= 0 {
		d.TickEvery = defaultTickEvery
	}
	if d.Publish == nil {
		d.Publish = func(string, any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Monitor{
		st: d.Store, hub: d.Hub, publish: d.Publish, log: d.Logger, now: d.Now,
		tickEvery: d.TickEvery, startedAt: d.Now(), ctx: ctx, cancel: cancel,
		servers: make(map[int64]*serverState), lastCheck: make(map[int64]time.Time),
		failedJob: make(map[int64]string), onAlertsChanged: d.OnAlertsChanged,
	}
	set, err := LoadSettings(d.Store)
	if err != nil {
		m.log.Warn("load monitor settings; using the defaults", "err", err)
		set = DefaultSettings()
	}
	m.settings = set
	m.load()
	return m
}

// Close cancels the background monitor.configure calls and waits for them.
// It is safe to call more than once.
func (m *Monitor) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.bg.Wait()
}

// goBg runs fn in the background until Close; after Close it does nothing.
func (m *Monitor) goBg(fn func(ctx context.Context)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.bg.Go(func() { fn(m.ctx) })
}

// Run re-evaluates the time-based statuses every TickEvery until ctx is
// done; once a day it prunes status events older than 90 days.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick()
		}
	}
}

// HandleEvent takes an agent's health report. It reports whether ev was a
// health event (handled, possibly by ignoring it).
func (m *Monitor) HandleEvent(serverID int64, ev proto.Event) bool {
	if ev.Type != proto.EventHealth {
		return false
	}
	if len(ev.Payload) > maxPayload {
		m.log.Warn("health report too large; ignored", "server_id", serverID, "bytes", len(ev.Payload))
		return true
	}
	var he proto.HealthEvent
	if err := json.Unmarshal(ev.Payload, &he); err != nil {
		m.log.Warn("health report unreadable; ignored", "server_id", serverID, "err", err)
		return true
	}
	m.report(serverID, he)
	return true
}

// OnOnline records the agent as connected and, in the background, sends it
// the settings with monitor.configure; the answer is its first report. It
// runs under the hub's lifecycle lock, so it never calls the hub itself.
func (m *Monitor) OnOnline(serverID int64) {
	now := m.now()
	m.mu.Lock()
	s := m.state(serverID)
	s.online, s.offlineSince = true, time.Time{}
	s.lastSeq = 0 // a restarted agent counts from 1 again
	m.addEvent(serverID, s, now, kindAgent, "", "", "Agent terhubung")
	m.mu.Unlock()
	m.goBg(func(ctx context.Context) { m.configure(ctx, serverID) })
}

// OnOffline records the agent as disconnected; the tick turns that into
// Agent offline after the grace period.
func (m *Monitor) OnOffline(serverID int64) {
	if _, err := m.st.GetServer(serverID); errors.Is(err, store.ErrNotFound) {
		m.mu.Lock()
		delete(m.servers, serverID) // deleted from the UI
		m.mu.Unlock()
		return
	}
	now := m.now()
	m.mu.Lock()
	s := m.state(serverID)
	s.online, s.offlineSince = false, now
	m.addEvent(serverID, s, now, kindAgent, "", "", "Agent terputus")
	m.mu.Unlock()
}

// Reconfigure re-reads the settings and sends them to every online agent;
// call it after SaveSettings.
func (m *Monitor) Reconfigure() {
	set, err := LoadSettings(m.st)
	if err != nil {
		m.log.Warn("reload monitor settings", "err", err)
		return
	}
	m.mu.Lock()
	m.settings = set
	m.mu.Unlock()
	for _, id := range m.hub.OnlineIDs() {
		m.goBg(func(ctx context.Context) { m.configure(ctx, id) })
	}
}

// CheckNow asks the agent for a fresh report and takes it; at most once
// per 10 s per server.
func (m *Monitor) CheckNow(ctx context.Context, serverID int64) error {
	now := m.now()
	m.mu.Lock()
	if last, ok := m.lastCheck[serverID]; ok && now.Sub(last) < checkNowEvery {
		m.mu.Unlock()
		return ErrCheckTooSoon
	}
	m.lastCheck[serverID] = now
	m.mu.Unlock()

	var he proto.HealthEvent
	if err := m.hub.Call(ctx, serverID, "monitor.snapshot", nil, &he); err != nil {
		if unknownCommand(err) {
			m.markNeedsUpgrade(serverID)
			return errNeedsUpgrade
		}
		return err
	}
	m.report(serverID, he)
	return nil
}

// configure sends the settings to the agent of id and takes its answer as
// a report. An agent that does not know the command needs an upgrade.
func (m *Monitor) configure(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(ctx, configureTimeout)
	defer cancel()
	srv, err := m.st.GetServer(id)
	if err != nil {
		m.log.Warn("monitor.configure: read server", "server_id", id, "err", err)
		return
	}
	hint := srv.Role
	if hint == store.RoleUnassigned {
		hint = ""
	}
	m.mu.Lock()
	set := m.settings
	m.mu.Unlock()
	params := proto.MonitorConfigureParams{
		CheckEverySec:   int(set.CheckEvery / time.Second),
		SummaryEverySec: int(set.SummaryEvery / time.Second),
		LagThresholdSec: int(set.Lag / time.Second),
		RoleHint:        hint,
	}
	var he proto.HealthEvent
	err = m.hub.Call(ctx, id, "monitor.configure", params, &he)
	switch {
	case unknownCommand(err):
		m.log.Info("agent does not know monitor.configure; it needs an upgrade", "server_id", id)
		m.markNeedsUpgrade(id)
	case err != nil:
		if m.ctx.Err() == nil { // not shutting down
			m.log.Warn("monitor.configure failed", "server_id", id, "err", err)
		}
	default:
		m.report(id, he)
	}
}

func unknownCommand(err error) bool {
	return errors.Is(err, &proto.Error{Code: proto.CodeUnknownCommand})
}

// View returns the health view of serverID; false when the Monitor does
// not know the server.
func (m *Monitor) View(serverID int64) (View, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[serverID]
	if !ok {
		return View{}, false
	}
	return s.view(serverID), true
}

// Views returns the health view of every server the Monitor knows.
func (m *Monitor) Views() map[int64]View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int64]View, len(m.servers))
	for id, s := range m.servers {
		out[id] = s.view(id)
	}
	return out
}

// String names a severity for browsers: ok, warn, crit or neutral.
func (s Severity) String() string {
	switch s {
	case SevOK:
		return "ok"
	case SevWarn:
		return "warn"
	case SevCrit:
		return "crit"
	}
	return "neutral"
}

// healthUpdate is one health_update to publish once the lock is released.
type healthUpdate struct {
	id       int64
	status   string
	severity Severity
}

// emit publishes health_update for each update: ids, status and severity
// only, never error text. A server without a status is neutral.
func (m *Monitor) emit(ups []healthUpdate) {
	for _, u := range ups {
		sev := u.severity
		if u.status == "" {
			sev = SevNeutral
		}
		m.publish("health_update", map[string]any{
			"server_id": u.id, "status": u.status, "severity": sev.String(),
		})
	}
}

// report takes one health report of serverID, placed at the panel's now
// minus its age, and evaluates the alerts. A backlog report is published
// and evaluated with the end of its batch: the next live report or tick.
func (m *Monitor) report(serverID int64, he proto.HealthEvent) {
	now := m.now()
	at := now.Add(-time.Duration(max(he.TakenAgoMS, 0)) * time.Millisecond)
	m.mu.Lock()
	ups := m.ingest(serverID, he, at)
	var alerts []alertUpdate
	if !he.Backlog {
		alerts = m.evaluate(now)
	}
	m.mu.Unlock()
	m.emit(ups)
	m.emitAlerts(alerts)
}

// markNeedsUpgrade flags the agent of id as too old for monitoring until a
// report from it arrives.
func (m *Monitor) markNeedsUpgrade(id int64) {
	now := m.now()
	m.mu.Lock()
	s := m.state(id)
	s.needsUpgrade = true
	var ups []healthUpdate
	if m.resolve(id, s) == nil {
		m.derive(id, s, now, false)
		ups = m.commit(id, s, nil)
	}
	m.mu.Unlock()
	m.emit(ups)
}

// tick re-derives every server's status at now, so statuses that change
// with time alone (agent offline, presumed down, stale reports,
// disconnected for long, lagging by a GTID gap) do; it publishes what
// changed and the end of backlog batches, evaluates the alerts, and prunes
// old events daily.
func (m *Monitor) tick() {
	now := m.now()
	servers, err := m.st.ListServers()
	if err != nil {
		m.log.Warn("monitor tick: list servers", "err", err)
		return
	}
	m.mu.Lock()
	seen := make(map[int64]bool, len(servers))
	for _, srv := range servers {
		seen[srv.ID] = true
		m.state(srv.ID).snooze = srv.SnoozeUntil
	}
	for id := range m.servers {
		if !seen[id] {
			delete(m.servers, id)
			delete(m.lastCheck, id)
		}
	}
	var ups []healthUpdate
	for _, srv := range servers {
		s := m.servers[srv.ID]
		if err := m.resolve(srv.ID, s); err != nil {
			m.log.Warn("monitor tick: resolve role", "server_id", srv.ID, "err", err)
			continue
		}
		m.derive(srv.ID, s, now, false)
		ups = m.commit(srv.ID, s, ups)
	}
	alerts := m.evaluate(now)
	prune := now.Sub(m.lastPrune) >= pruneEvery
	if prune {
		m.lastPrune = now
	}
	m.mu.Unlock()
	m.emit(ups)
	m.emitAlerts(alerts)
	if prune {
		if n, err := m.st.PruneStatusEvents(now.Add(-eventRetention)); err != nil {
			m.log.Warn("prune status events", "err", err)
		} else if n > 0 {
			m.log.Info("pruned old status events", "deleted", n)
		}
	}
}
