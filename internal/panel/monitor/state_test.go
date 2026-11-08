package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// brokenAgent is an agent whose monitor.configure fails; it reports only
// through emit, as its own monitor would.
type brokenAgent struct {
	r    *rig
	id   int64
	conn hub.Conn
	seq  uint64
}

func (r *rig) brokenAgent(id int64) *brokenAgent {
	reg := commands.NewRegistry()
	reg.Register("probe", func(context.Context, json.RawMessage) (any, error) { return proto.ProbeResult{}, nil })
	reg.Register("monitor.configure", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("monitor belum siap")
	})
	a := &brokenAgent{r: r, id: id}
	a.conn = hub.NewLocalConn(reg, func(ev proto.Event) { r.hub.OnEvent(id, ev) })
	return a
}

func (a *brokenAgent) attach() *brokenAgent {
	a.r.hub.Attach(a.id, a.conn)
	a.r.settle()
	return a
}

func (a *brokenAgent) detach() { a.r.hub.Detach(a.id, a.conn) }

// send reports snap, live or from the backlog, taken ago before now.
func (a *brokenAgent) send(snap proto.HealthSnapshot, backlog bool, ago time.Duration) {
	a.seq++
	b, err := json.Marshal(proto.HealthEvent{Seq: a.seq, Backlog: backlog, TakenAgoMS: ago.Milliseconds(), Snapshot: snap})
	if err != nil {
		a.r.t.Fatal(err)
	}
	a.r.hub.OnEvent(a.id, proto.Event{Type: proto.EventHealth, Payload: b})
}

func (r *rig) wantStatus(id int64, code string, sev Severity) View {
	r.t.Helper()
	v := r.view(id)
	if v.Status.Code != code || v.Status.Severity != sev {
		r.t.Fatalf("server %d: status %q (%v) %q; want %q (%v)", id, v.Status.Code, v.Status.Severity, v.Status.Detail, code, sev)
	}
	return v
}

// wantOffline fails unless id's status comes from its agent's absence:
// Agent offline, or a master presumed down because of it.
func (r *rig) wantOffline(id int64) View {
	r.t.Helper()
	v := r.view(id)
	if !offlineStatus(v.Status) {
		r.t.Fatalf("server %d: status %q %q; want Agent offline", id, v.Status.Code, v.Status.Detail)
	}
	return v
}

// TestReconnectShowsWaitingFirstReport: an agent that connects again
// after its server went Agent offline shows "Menunggu laporan pertama" at
// once, even when monitor.configure fails, until a live report arrives;
// a backlog report is no fresh one. Silent for longer than a report may
// be late, it is Agent offline again. A reconnect within the grace keeps
// the status it had.
func TestReconnectShowsWaitingFirstReport(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	r.wantStatus(mID, StatusHealthy, SevOK)
	r.wantStatus(rID, StatusSynced, SevOK)

	ma.detach()
	ra.detach()
	r.clk.Add(time.Minute)
	r.m.tick()
	r.wantOffline(mID)
	r.wantStatus(rID, StatusAgentOffline, SevCrit)
	if !r.isOpen(rID, KindAgentOffline) {
		t.Fatal("agent_offline not open")
	}

	// Back, but monitor.configure fails: waiting at once, no tick needed.
	r.clk.Add(time.Second)
	mb := r.brokenAgent(mID).attach()
	rb := r.brokenAgent(rID).attach()
	v := r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	if v.Status.Detail == "" {
		t.Error("waiting status has no detail")
	}
	r.wantStatus(rID, StatusAwaitingReport, SevNeutral)
	if ups := r.healthUpdates(mID); ups[len(ups)-1]["status"] != StatusAwaitingReport || ups[len(ups)-1]["severity"] != "neutral" {
		t.Errorf("last health_update = %v, want the waiting status", ups[len(ups)-1])
	}
	evs := r.events(rID, kindStatus)
	if last := evs[len(evs)-1]; last.FromStatus != StatusAgentOffline || last.ToStatus != StatusAwaitingReport {
		t.Errorf("last status event %s → %s", last.FromStatus, last.ToStatus)
	}
	r.clk.Add(5 * time.Second)
	r.m.tick()
	r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	r.wantStatus(rID, StatusAwaitingReport, SevNeutral)
	if !r.isOpen(rID, KindAgentOffline) {
		t.Error("agent_offline closed before the agent reported")
	}

	// A backlog report is not fresh; the next live one is.
	old := masterSnap(srcUUID + ":1-100")
	mb.send(old, true, 2*time.Minute)
	r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	mb.send(masterSnap(srcUUID+":1-120"), false, 0)
	r.wantStatus(mID, StatusHealthy, SevOK)
	rb.send(replicaSnap(srcUUID+":1-120"), false, 0)
	r.wantStatus(rID, StatusSynced, SevOK)
	if r.isOpen(rID, KindAgentOffline) {
		t.Error("agent_offline still open after a live report")
	}

	// Connected again but silent: waiting for as long as a report may be
	// late, then Agent offline.
	mb.detach()
	r.clk.Add(time.Minute)
	r.m.tick()
	r.wantOffline(mID)
	mb = r.brokenAgent(mID).attach()
	r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	r.clk.Add(r.m.settings.StaleAfter)
	r.m.tick()
	r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	r.clk.Add(time.Second)
	r.m.tick()
	r.wantOffline(mID)
	mb.send(masterSnap(srcUUID+":1-130"), false, 0)
	r.wantStatus(mID, StatusHealthy, SevOK)

	// A reconnect within the grace: the status stays, no waiting.
	n := len(r.events(mID, kindStatus))
	mb.detach()
	r.clk.Add(10 * time.Second)
	r.m.tick()
	r.brokenAgent(mID).attach()
	r.m.tick()
	r.wantStatus(mID, StatusHealthy, SevOK)
	if evs := r.events(mID, kindStatus); len(evs) != n {
		t.Errorf("status events after a quick reconnect: %+v", evs[n:])
	}
}

// TestReconnectWithoutAnyReport: a server never reported on goes Agent
// offline after the grace; once its agent connects it waits for the first
// report, and after as long as a report may be late without one it is
// Agent offline again.
func TestReconnectWithoutAnyReport(t *testing.T) {
	r := newRig(t)
	mID, _ := r.master("db-master")
	r.clk.Add(time.Minute)
	r.m.tick()
	r.wantOffline(mID)
	a := r.brokenAgent(mID).attach()
	r.wantStatus(mID, StatusAwaitingReport, SevNeutral)
	r.clk.Add(r.m.settings.StaleAfter + time.Second)
	r.m.tick()
	if v := r.wantStatus(mID, StatusAgentOffline, SevCrit); !strings.Contains(v.Status.Detail, "belum mengirim laporan") {
		t.Errorf("detail %q", v.Status.Detail)
	}
	a.send(masterSnap(srcUUID+":1-5"), false, 0)
	r.wantStatus(mID, StatusHealthy, SevOK)
}
