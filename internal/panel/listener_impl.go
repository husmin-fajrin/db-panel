package panel

import (
	"context"
	"errors"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listenerImpl turns hub lifecycle changes into store updates, browser
// events and probes, and receives agent events from Hub.OnEvent. OnOnline
// and OnOffline run under the hub's lifecycle lock, so they only touch the
// store briefly and hand probes to goroutines.
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
	if online {
		p.goProbe(id)
	}
}

// eventLogInterval is the minimum time between two logged events of one
// server, so an agent flooding events cannot flood the journal.
const eventLogInterval = 10 * time.Second

// onEvent receives events from remote and embedded agents, all of which
// reach it through Hub.OnEvent and the hub's listener. Job events go to the
// orchestrator (which never blocks); every event is logged at debug level,
// at most one per server per eventLogInterval.
func (p *Panel) onEvent(id int64, ev proto.Event) {
	if p.Orchestrator != nil {
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
