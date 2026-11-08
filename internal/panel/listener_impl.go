package panel

import (
	"context"
	"errors"
	"time"

	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listenerImpl turns hub lifecycle changes into store updates, browser
// events, probes and monitor updates, and receives agent events from
// Hub.OnEvent. OnOnline and OnOffline run under the hub's lifecycle lock,
// so they only touch the store briefly and hand probes and
// monitor.configure to goroutines.
type listenerImpl struct{ p *Panel }

func (l *listenerImpl) OnOnline(serverID int64)  { l.p.setOnline(serverID, true) }
func (l *listenerImpl) OnOffline(serverID int64) { l.p.setOnline(serverID, false) }
func (l *listenerImpl) OnEvent(serverID int64, ev proto.Event) {
	l.p.onEvent(serverID, ev)
}

func (p *Panel) setOnline(id int64, online bool) {
	// A server deleted from the UI goes offline after its row is gone.
	if err := p.Store.TouchSeen(id, p.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		p.log.Warn("record last seen", "server_id", id, "err", err)
	}
	p.publish("server_status", map[string]any{"id": id, "online": online})
	if p.Monitor != nil {
		if online {
			p.Monitor.OnOnline(id)
		} else {
			p.Monitor.OnOffline(id)
		}
	}
	// After the Monitor's: a pending quarantine or repoint is sent to the
	// agent that just connected (in the background).
	if online && p.Orchestrator != nil {
		p.Orchestrator.OnOnline(id)
	}
	if online {
		p.goProbe(id)
	}
}

// eventLogInterval is the minimum time between two logged events of one
// server, so an agent flooding events cannot flood the journal.
const eventLogInterval = 10 * time.Second

// onEvent receives events from the agents, all of which reach it through Hub.OnEvent and the hub's listener. Health reports go to
// the monitor, job events to the orchestrator (neither blocks); every
// event is logged at debug level, at most one per server per
// eventLogInterval.
func (p *Panel) onEvent(id int64, ev proto.Event) {
	handled := p.Monitor != nil && p.Monitor.HandleEvent(id, ev)
	if !handled && p.Orchestrator != nil {
		p.Orchestrator.HandleEvent(id, ev)
	}
	now := p.now()
	p.evMu.Lock()
	last, seen := p.evLogged[id]
	if seen && now.Sub(last) < eventLogInterval {
		p.evMu.Unlock()
		return
	}
	p.evLogged[id] = now
	p.evMu.Unlock()
	p.log.Debug("agent event", "server_id", id, "type", ev.Type)
}

// probeLoop probes every online server each probeInterval until ctx is done.
func (p *Panel) probeLoop(ctx context.Context) {
	t := time.NewTicker(p.probeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, id := range p.Hub.OnlineIDs() {
				p.goProbe(id)
			}
		}
	}
}

// renewMySQLCerts starts mysql_tls for the MySQL certificates that expire
// within 30 days: tlsRenewFirst after the start, then daily, until ctx is
// done. A server it skips (offline, busy) is tried the next day.
func (p *Panel) renewMySQLCerts(ctx context.Context) {
	t := time.NewTimer(p.tlsRenewFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Orchestrator.RenewMySQLTLS(ctx)
			t.Reset(tlsRenewEvery)
		}
	}
}

// retentionLoop prunes the rows past their retention pruneFirst after the
// start, then daily, until ctx is done.
func (p *Panel) retentionLoop(ctx context.Context) {
	t := time.NewTimer(p.pruneFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pruneOld()
			t.Reset(pruneEvery)
		}
	}
}

// pruneOld deletes the alerts closed more than alerts_retention_days ago,
// the status events older than events_retention_days and the expired
// confirmations. It is the panel's only pruning: audit_log and job_log
// are never pruned (spec 5.12). Failures are logged only.
func (p *Panel) pruneOld() {
	set, err := healthmon.LoadSettings(p.Store)
	if err != nil {
		p.log.Warn("retention: load settings", "err", err)
		return
	}
	now := p.now()
	days := func(n int) time.Time { return now.AddDate(0, 0, -n) }
	for _, step := range []struct {
		what  string
		prune func() (int64, error)
	}{
		{"closed alerts", func() (int64, error) { return p.Store.PruneAlertsClosedBefore(days(set.AlertsRetentionDays)) }},
		{"status events", func() (int64, error) { return p.Store.PruneStatusEvents(days(set.EventsRetentionDays)) }},
		{"expired confirmations", func() (int64, error) { return p.Store.PruneConfirmations(now) }},
	} {
		n, err := step.prune()
		switch {
		case err != nil:
			p.log.Warn("retention: prune "+step.what, "err", err)
		case n > 0:
			p.log.Info("retention: pruned "+step.what, "deleted", n)
		}
	}
}

// goProbe probes id in the background while Run is serving; outside Run it
// does nothing.
func (p *Panel) goProbe(id int64) {
	p.bgMu.Lock()
	defer p.bgMu.Unlock()
	if p.runCtx == nil || p.bgClosed {
		return
	}
	ctx := p.runCtx
	p.bg.Go(func() { p.probeOne(ctx, id) })
}

// stopBackground refuses new probes and waits for running ones, which end
// with Run's context.
func (p *Panel) stopBackground() {
	p.bgMu.Lock()
	p.bgClosed = true
	p.bgMu.Unlock()
	p.bg.Wait()
}

// probeOne asks the agent of id to describe its host, stores the result and
// tells browsers. Failures are logged only.
func (p *Panel) probeOne(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var res proto.ProbeResult
	if err := p.Hub.Call(ctx, id, "probe", nil, &res); err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) { // not shutting down
			p.log.Warn("probe failed", "server_id", id, "err", err)
		}
		return
	}
	if err := p.Store.UpdateProbe(id, res, res.AgentVersion, p.now()); err != nil {
		if errors.Is(err, store.ErrProbeTooLarge) {
			p.log.Warn("probe result too large; keeping the previous one", "server_id", id, "err", err)
			return
		}
		p.log.Warn("store probe result", "server_id", id, "err", err)
		return
	}
	p.publish("server_probe", map[string]any{"id": id})
}
