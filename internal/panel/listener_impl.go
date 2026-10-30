package panel

import (
	"context"
	"errors"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listenerImpl turns hub lifecycle changes into store updates, browser
// events and probes. Its callbacks run under the hub's lifecycle lock, so
// they only touch the store briefly and hand probes to goroutines.
type listenerImpl struct{ p *Panel }

func (l *listenerImpl) OnOnline(serverID int64)  { l.p.setOnline(serverID, true) }
func (l *listenerImpl) OnOffline(serverID int64) { l.p.setOnline(serverID, false) }
func (l *listenerImpl) OnEvent(serverID int64, ev proto.Event) {
	l.p.onEvent(serverID, ev)
}

func (p *Panel) setOnline(id int64, online bool) {
	if err := p.Store.TouchSeen(id, p.now()); err != nil {
		p.log.Warn("record last seen", "server_id", id, "err", err)
	}
	p.Broker.Publish("server_status", map[string]any{"id": id, "online": online})
	if online {
		p.goProbe(id)
	}
}

// onEvent receives events from remote and embedded agents. Nothing consumes
// them yet.
func (p *Panel) onEvent(id int64, ev proto.Event) {
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
		p.log.Warn("store probe result", "server_id", id, "err", err)
		return
	}
	p.Broker.Publish("server_probe", map[string]any{"id": id})
}
