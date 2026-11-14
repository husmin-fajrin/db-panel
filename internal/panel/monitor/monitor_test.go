package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The rig serves fake agents (a commands.Registry with scripted monitor.*
// handlers) to a real hub through hub.NewLocalConn; the hub's listener
// hands lifecycle changes and events to the Monitor, as the panel's does.
// The Monitor's clock is fake; its loop never runs, tests call tick.

const srcUUID = "3e11fa47-71ca-11e1-9e33-c80aa9429562"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type forwarder struct{ m atomic.Pointer[Monitor] }

func (f *forwarder) OnOnline(id int64)  { f.m.Load().OnOnline(id) }
func (f *forwarder) OnOffline(id int64) { f.m.Load().OnOffline(id) }
func (f *forwarder) OnEvent(id int64, ev proto.Event) {
	f.m.Load().HandleEvent(id, ev)
}

type published struct {
	Event string
	Data  any
}

type rig struct {
	t    *testing.T
	st   *store.Store
	hub  *hub.Hub
	clk  *fakeClock
	m    *Monitor
	logs *syncBuffer

	pubMu sync.Mutex
	pubs  []published
}

// newRig starts a Monitor over a fresh store; setup runs on the store
// before the Monitor is created (settings, health rows).
func newRig(t *testing.T, setup ...func(st *store.Store)) *rig {
	t.Helper()
	r := &rig{t: t, st: openStore(t), clk: &fakeClock{t: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}, logs: &syncBuffer{}}
	for _, f := range setup {
		f(r.st)
	}
	l := &forwarder{}
	// The tests send reports faster than an agent does; the hub's clock
	// moves 2 s on each reading so its report rate limit never drops one.
	// This hides the limiter from every test on this rig: Dropped (read
	// by View) is always 0 here, and a report is never refused. The limit
	// is tested in the hub package (TestHubRateLimitsSnapshotsPerServer,
	// TestHubBacklogAllowance) and its display in the web package.
	var hubTicks atomic.Int64
	r.hub = hub.New(l, hub.WithNow(func() time.Time {
		return r.clk.Now().Add(time.Duration(hubTicks.Add(1)) * 2 * time.Second)
	}))
	r.m = New(Deps{Store: r.st, Hub: r.hub, Publish: r.publish,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: r.clk.Now, TickEvery: time.Hour})
	l.m.Store(r.m)
	t.Cleanup(r.m.Close)
	return r
}

func (r *rig) publish(event string, data any) {
	r.pubMu.Lock()
	r.pubs = append(r.pubs, published{event, data})
	r.pubMu.Unlock()
}

// healthUpdates are the health_update payloads published for id.
func (r *rig) healthUpdates(id int64) []map[string]any {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	var out []map[string]any
	for _, p := range r.pubs {
		if p.Event != "health_update" {
			continue
		}
		b, _ := json.Marshal(p.Data)
		var m map[string]any
		json.Unmarshal(b, &m)
		if int64(m["server_id"].(float64)) == id {
			out = append(out, m)
		}
	}
	return out
}

// settle waits for the Monitor's background calls (monitor.configure).
func (r *rig) settle() { r.m.bg.Wait() }

func (r *rig) server(name string) int64 {
	r.t.Helper()
	srv, err := r.st.CreateServer(name)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.MarkPaired(srv.ID, "serial-"+name, "10.0.0.9"); err != nil {
		r.t.Fatal(err)
	}
	return srv.ID
}

func (r *rig) master(name string) (int64, store.Topology) {
	r.t.Helper()
	id := r.server(name)
	topo, err := r.st.CreateTopology(id, "dbpanel_repl", nil, "10.0.0.1", 3306)
	if err != nil {
		r.t.Fatal(err)
	}
	return id, topo
}

func (r *rig) replica(name string, topo store.Topology) int64 {
	r.t.Helper()
	id := r.server(name)
	if _, err := r.st.AddReplica(topo.ID, id, nil); err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.SetReplicaState(id, store.ReplicaReady, ""); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *rig) events(id int64, kind string) []store.StatusEvent {
	r.t.Helper()
	evs, err := r.st.ListStatusEvents(id, time.Time{}, 1000)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []store.StatusEvent
	for i := len(evs) - 1; i >= 0; i-- { // oldest first
		if kind == "" || evs[i].Kind == kind {
			out = append(out, evs[i])
		}
	}
	return out
}

func (r *rig) view(id int64) View {
	r.t.Helper()
	v, ok := r.m.View(id)
	if !ok {
		r.t.Fatalf("no view for server %d", id)
	}
	return v
}

// fakeAgent answers monitor.configure and monitor.snapshot with its
// current snapshot (modern) or knows no monitor.* command (old).
type fakeAgent struct {
	r    *rig
	id   int64
	conn hub.Conn

	mu       sync.Mutex
	seq      uint64
	snap     proto.HealthSnapshot
	backlog  []proto.HealthEvent // emitted before answering monitor.configure
	calls    []string
	configs  []proto.MonitorConfigureParams
	attached bool
	// gate, when set, holds monitor.snapshot answers until it is closed.
	gate chan struct{}
}

func (r *rig) agent(id int64, modern bool, snap proto.HealthSnapshot) *fakeAgent {
	a := &fakeAgent{r: r, id: id, snap: snap}
	reg := commands.NewRegistry()
	reg.Register("probe", func(context.Context, json.RawMessage) (any, error) { return proto.ProbeResult{}, nil })
	if modern {
		reg.Register("monitor.configure", func(_ context.Context, raw json.RawMessage) (any, error) {
			var p proto.MonitorConfigureParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			a.mu.Lock()
			a.calls = append(a.calls, "monitor.configure")
			a.configs = append(a.configs, p)
			backlog := a.backlog
			a.backlog = nil
			a.mu.Unlock()
			for _, he := range backlog {
				a.emit(he)
			}
			return a.report(), nil
		})
		reg.Register("monitor.snapshot", func(context.Context, json.RawMessage) (any, error) {
			a.mu.Lock()
			a.calls = append(a.calls, "monitor.snapshot")
			gate := a.gate
			a.mu.Unlock()
			if gate != nil {
				<-gate
			}
			return a.report(), nil
		})
	}
	a.conn = hub.NewLocalConn(reg, func(ev proto.Event) { r.hub.OnEvent(id, ev) })
	return a
}

func (a *fakeAgent) report() proto.HealthEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	return proto.HealthEvent{Seq: a.seq, Snapshot: a.snap}
}

func (a *fakeAgent) attach() *fakeAgent {
	a.r.hub.Attach(a.id, a.conn)
	a.r.settle()
	return a
}

func (a *fakeAgent) detach() { a.r.hub.Detach(a.id, a.conn) }

// holdSnapshots makes monitor.snapshot wait until the returned function
// is called (also at the end of the test, before the Monitor closes).
func (a *fakeAgent) holdSnapshots() (release func()) {
	gate := make(chan struct{})
	a.mu.Lock()
	a.gate = gate
	a.mu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			a.mu.Lock()
			a.gate = nil
			a.mu.Unlock()
			close(gate)
		})
	}
	a.r.t.Cleanup(release)
	return release
}

func (a *fakeAgent) setSnap(f func(s *proto.HealthSnapshot)) {
	a.mu.Lock()
	f(&a.snap)
	a.mu.Unlock()
}

// send reports the agent's current snapshot as a live event.
func (a *fakeAgent) send() {
	a.emit(a.report())
}

func (a *fakeAgent) emit(he proto.HealthEvent) {
	b, err := json.Marshal(he)
	if err != nil {
		a.r.t.Fatal(err)
	}
	a.r.hub.OnEvent(a.id, proto.Event{Type: proto.EventHealth, Payload: b})
}

func (a *fakeAgent) count(cmd string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.calls {
		if c == cmd {
			n++
		}
	}
	return n
}

func (a *fakeAgent) lastConfig() proto.MonitorConfigureParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.configs) == 0 {
		a.r.t.Fatalf("server %d never got monitor.configure", a.id)
	}
	return a.configs[len(a.configs)-1]
}

func ptr(v int64) *int64 { return &v }

func masterSnap(gtid string) proto.HealthSnapshot {
	return proto.HealthSnapshot{MySQLOK: true, Version: "8.4.3", UptimeSec: 1000, LogBin: true,
		GTIDExecuted: gtid, ThreadsConnected: 10, MaxConnections: 100,
		DiskTotal: 100 << 30, DiskFree: 50 << 30, AgentVersion: "3.0.0"}
}

func replicaSnap(received string) proto.HealthSnapshot {
	s := masterSnap(received)
	s.SuperReadOnly, s.ReadOnly = true, true
	s.Replica = &proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", SecondsBehind: ptr(0),
		RetrievedGTID: received, ExecutedGTID: received}
	return s
}

// TestConfigureQuarantinedSendsNoHint: a quarantined server gets the
// panel's intervals with an empty role hint, which every agent version
// accepts (an older one refuses "quarantined"); "" never pins a role.
func TestConfigureQuarantinedSendsNoHint(t *testing.T) {
	r := newRig(t)
	id := r.server("db-old")
	if err := r.st.SetServerRole(id, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	a := r.agent(id, true, masterSnap(srcUUID+":1-100")).attach()
	if p := a.lastConfig(); p != (proto.MonitorConfigureParams{CheckEverySec: 15, SummaryEverySec: 120, LagThresholdSec: 60}) {
		t.Fatalf("quarantined configure params = %+v, want no role hint", p)
	}
}

func TestSnapshotStoredAndStatusDerived(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	uID := r.server("db-free")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ua := r.agent(uID, true, masterSnap("")).attach()

	if p := ma.lastConfig(); p != (proto.MonitorConfigureParams{CheckEverySec: 15, SummaryEverySec: 120,
		LagThresholdSec: 60, RoleHint: "master"}) {
		t.Fatalf("master configure params = %+v", p)
	}
	if p := ra.lastConfig(); p.RoleHint != "replica" {
		t.Fatalf("replica RoleHint = %q, want replica", p.RoleHint)
	}
	if p := ua.lastConfig(); p.RoleHint != "" {
		t.Fatalf("unassigned RoleHint = %q, want empty", p.RoleHint)
	}

	now := r.clk.Now()
	mv := r.view(mID)
	if mv.Role != "master" || mv.Status.Code != StatusHealthy || mv.Status.Severity != SevOK || !mv.ReceivedAt.Equal(now) {
		t.Fatalf("master view = %+v", mv)
	}
	rv := r.view(rID)
	if rv.Role != "replica" || rv.Status.Code != StatusSynced || rv.Snap == nil || rv.Snap.Replica == nil {
		t.Fatalf("replica view = %+v", rv)
	}
	if !rv.Since.Equal(now) {
		t.Fatalf("replica Since = %v, want %v", rv.Since, now)
	}
	uv := r.view(uID)
	if uv.Role != "" || uv.Status.Code != "" || uv.Snap == nil {
		t.Fatalf("unassigned view = %+v, want not monitored but with its snapshot", uv)
	}

	h, err := r.st.GetHealth(rID)
	if err != nil {
		t.Fatal(err)
	}
	var snap proto.HealthSnapshot
	if err := json.Unmarshal([]byte(h.SnapshotJSON), &snap); err != nil {
		t.Fatal(err)
	}
	if h.Status != StatusSynced || !h.LockOK || !h.ReceivedAt.Equal(now) || h.AgentVersion != "3.0.0" ||
		snap.Replica == nil || snap.Replica.ExecutedGTID != srcUUID+":1-100" {
		t.Fatalf("stored health = %+v", h)
	}
	if len(r.Views()) != 3 {
		t.Fatalf("Views = %d entries, want 3", len(r.Views()))
	}
	if _, ok := r.m.View(9999); ok {
		t.Fatal("View of an unknown server reported ok")
	}
}

func (r *rig) Views() map[int64]View { return r.m.Views() }

func TestStatusChangeWritesEventAndPublishes(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	before := len(r.healthUpdates(rID))

	r.clk.Add(20 * time.Second)
	ra.setSnap(func(s *proto.HealthSnapshot) {
		rs := *s.Replica
		rs.SQLRunning, rs.LastSQLErrno, rs.LastSQLError = "No", 1062, "Duplicate entry 'Budi' for key 'PRIMARY'"
		s.Replica = &rs
	})
	ra.send()

	evs := r.events(rID, "status")
	last := evs[len(evs)-1]
	if last.FromStatus != StatusSynced || last.ToStatus != StatusStopped || !last.TS.Equal(r.clk.Now()) {
		t.Fatalf("last status event = %+v, want Sinkron → Berhenti now", last)
	}
	if v := r.view(rID); v.Status.Code != StatusStopped || !v.Since.Equal(r.clk.Now()) {
		t.Fatalf("view = %+v", v)
	}
	ups := r.healthUpdates(rID)
	if len(ups) != before+1 {
		t.Fatalf("health_update published %d times for the report, want 1", len(ups)-before)
	}
	got := ups[len(ups)-1]
	if got["status"] != StatusStopped || got["severity"] != "crit" || len(got) != 3 {
		t.Fatalf("health_update = %v, want server_id, status Berhenti, severity crit", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "Budi") || strings.Contains(string(b), "Duplicate") {
		t.Fatalf("health_update carries error text: %s", b)
	}

	// The same report again: no new event.
	n := len(r.events(rID, ""))
	ra.send()
	if len(r.events(rID, "")) != n {
		t.Fatal("an unchanged report wrote an event")
	}

	// MySQL's own error text stays out of the timeline.
	ra.setSnap(func(s *proto.HealthSnapshot) {
		*s = proto.HealthSnapshot{MySQLError: "Error 1045: Access denied for user 'Budi'@'10.0.0.9'"}
	})
	ra.send()
	evs = r.events(rID, "status")
	if last := evs[len(evs)-1]; last.ToStatus != StatusUnreachable || strings.Contains(last.Detail, "Budi") {
		t.Fatalf("unreachable event = %+v, want Tidak terjangkau without the MySQL error", last)
	}
}

func TestTimelineEventKinds(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	steps := []struct {
		kind   string
		mutate func(s *proto.HealthSnapshot)
		detail string
	}{
		{"lock", func(s *proto.HealthSnapshot) { s.SuperReadOnly = false }, "Kunci terlepas"},
		{"lock", func(s *proto.HealthSnapshot) { s.SuperReadOnly = true }, "Kunci kembali"},
		{"restart", func(s *proto.HealthSnapshot) { s.UptimeSec = 5 }, "MySQL dimulai ulang"},
		{"disk", func(s *proto.HealthSnapshot) { s.DiskFree = 10 << 30 }, "90%"},
		{"disk", func(s *proto.HealthSnapshot) { s.DiskFree = 4 << 30 }, "96%"},
		{"disk", func(s *proto.HealthSnapshot) { s.DiskFree = 50 << 30 }, "50%"},
		{"connections", func(s *proto.HealthSnapshot) { s.ThreadsConnected = 85 }, "85%"},
		{"connections", func(s *proto.HealthSnapshot) { s.ThreadsConnected = 20 }, "20%"},
	}
	for _, s := range steps {
		r.clk.Add(15 * time.Second)
		before := len(r.events(rID, s.kind))
		ra.setSnap(s.mutate)
		ra.send()
		evs := r.events(rID, s.kind)
		if len(evs) != before+1 {
			t.Fatalf("%s (%s): %d new events, want 1", s.kind, s.detail, len(evs)-before)
		}
		if e := evs[len(evs)-1]; !strings.Contains(e.Detail, s.detail) || e.TopologyID != topo.ID {
			t.Fatalf("%s event = %+v, want detail with %q in topology %d", s.kind, e, s.detail, topo.ID)
		}
	}
	if n := len(r.events(rID, "status")); n != 1 {
		t.Fatalf("status events = %d, want only the first Sinkron", n)
	}
	ra.detach()
	if evs := r.events(rID, "agent"); len(evs) != 2 || evs[0].Detail != "Agent terhubung" || evs[1].Detail != "Agent terputus" {
		t.Fatalf("agent events = %+v", evs)
	}
}

func TestBacklogUsesPanelClock(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.detach()

	// Three hours later the agent reconnects with two changes it kept.
	r.clk.Add(3 * time.Hour)
	stopped := replicaSnap(srcUUID + ":1-100")
	stopped.Replica.SQLRunning, stopped.Replica.LastSQLErrno = "No", 1032
	synced := replicaSnap(srcUUID + ":1-100")
	ra.mu.Lock()
	ra.backlog = []proto.HealthEvent{
		{Seq: 2, TakenAgoMS: 7_200_000, Backlog: true, Snapshot: stopped},
		{Seq: 3, TakenAgoMS: 60_000, Backlog: true, Snapshot: synced},
	}
	ra.seq = 3
	ra.mu.Unlock()
	before := len(r.healthUpdates(rID))
	ra.attach()

	now := r.clk.Now()
	evs := r.events(rID, "status")
	if len(evs) != 3 {
		t.Fatalf("status events = %+v, want Sinkron, Berhenti, Sinkron", evs)
	}
	down, up := evs[1], evs[2]
	if down.ToStatus != StatusStopped || !down.TS.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("stop event = %+v, want Berhenti at panel now − 2 h", down)
	}
	if up.ToStatus != StatusSynced || !up.TS.Equal(now.Add(-time.Minute)) {
		t.Fatalf("recovery event = %+v, want Sinkron at panel now − 1 min", up)
	}
	if d := up.TS.Sub(down.TS); d != 119*time.Minute {
		t.Fatalf("events %v apart, want 119 min", d)
	}
	if n := len(r.healthUpdates(rID)) - before; n != 1 {
		t.Fatalf("health_update published %d times for the batch, want 1", n)
	}
	if v := r.view(rID); v.Status.Code != StatusSynced || !v.ReceivedAt.Equal(now) {
		t.Fatalf("view after the batch = %+v", v)
	}
}

func TestBacklogWithoutLiveReportPublishedByTick(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	before := len(r.healthUpdates(rID))
	stopped := replicaSnap(srcUUID + ":1-100")
	stopped.Replica.SQLRunning, stopped.Replica.LastSQLErrno = "No", 1032
	ra.emit(proto.HealthEvent{Seq: 5, TakenAgoMS: 30_000, Backlog: true, Snapshot: stopped})
	if n := len(r.healthUpdates(rID)) - before; n != 0 {
		t.Fatalf("backlog entry published %d times, want 0 until the batch ends", n)
	}
	r.m.tick()
	if n := len(r.healthUpdates(rID)) - before; n != 1 {
		t.Fatalf("after a tick: %d publishes, want 1", n)
	}
}

func TestLowerSeqIgnoredUntilReconnect(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ra.send() // seq 2
	stopped := replicaSnap(srcUUID + ":1-100")
	stopped.Replica.SQLRunning, stopped.Replica.LastSQLErrno = "No", 1032
	ra.emit(proto.HealthEvent{Seq: 1, Snapshot: stopped})
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("an older report changed the status to %q", v.Status.Code)
	}
	// A restarted agent counts from 1 again.
	ra.detach()
	ra.mu.Lock()
	ra.seq = 0
	ra.mu.Unlock()
	ra.attach()
	ra.emit(proto.HealthEvent{Seq: 2, Snapshot: stopped})
	if v := r.view(rID); v.Status.Code != StatusStopped {
		t.Fatalf("after reconnect the report was ignored: %q", v.Status.Code)
	}
}

func TestOldAgentNeedsUpgrade(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(rID, false, proto.HealthSnapshot{}).attach()

	v := r.view(rID)
	if !v.NeedsUpgrade || v.Status.Code != StatusNeedsUpgrade || v.Status.Code == StatusSynced {
		t.Fatalf("old agent view = %+v, want NeedsUpgrade and %q", v, StatusNeedsUpgrade)
	}
	events := len(r.events(rID, ""))
	pubs := len(r.healthUpdates(rID))
	for range 30 {
		r.clk.Add(5 * time.Second)
		r.m.tick()
	}
	if v := r.view(rID); v.Status.Code != StatusNeedsUpgrade {
		t.Fatalf("after ticks status = %q", v.Status.Code)
	}
	if n := len(r.events(rID, "")); n != events {
		t.Fatalf("ticks wrote %d events, want none", n-events)
	}
	if n := len(r.healthUpdates(rID)); n != pubs {
		t.Fatalf("ticks published %d health updates, want none", n-pubs)
	}
}

func TestOfflineThenDownAfterThreshold(t *testing.T) {
	r := newRig(t, func(st *store.Store) {
		if err := st.SetSetting(store.SettingDownAfter, "60"); err != nil {
			t.Fatal(err)
		}
	})
	mID, _ := r.master("db-master")
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ma.detach()
	t0 := r.clk.Now()

	step := func(at time.Duration, want string) {
		t.Helper()
		r.clk.Set(t0.Add(at))
		r.m.tick()
		if v := r.view(mID); v.Status.Code != want {
			t.Fatalf("at +%v: status %q (%s), want %q", at, v.Status.Code, v.Status.Detail, want)
		}
	}
	step(10*time.Second, StatusHealthy)
	step(31*time.Second, StatusAgentOffline)
	step(45*time.Second, StatusAgentOffline)
	step(61*time.Second, StatusDown)

	evs := r.events(mID, "status")
	got := []string{}
	for _, e := range evs {
		got = append(got, e.FromStatus+"→"+e.ToStatus)
	}
	want := "→Sehat,Sehat→Agent offline,Agent offline→Diduga tumbang"
	if strings.Join(got, ",") != want {
		t.Fatalf("status events %v, want %s", got, want)
	}
	if !evs[2].TS.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("down event at %v", evs[2].TS)
	}
	ups := r.healthUpdates(mID)
	if last := ups[len(ups)-1]; last["status"] != StatusDown {
		t.Fatalf("last health_update = %v", last)
	}
	if h, _ := r.st.GetHealth(mID); h.Status != StatusDown {
		t.Fatalf("stored status = %q, want %q", h.Status, StatusDown)
	}

	// Reconnecting brings a fresh report and the status back.
	ma.attach()
	if v := r.view(mID); v.Status.Code != StatusHealthy {
		t.Fatalf("after reconnect: %q", v.Status.Code)
	}
}

func TestReplicaUsesMasterGTIDFromMasterView(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	other, otherTopo := r.master("db-other")
	r.agent(mID, true, masterSnap(srcUUID+":1-200")).attach()
	r.agent(other, true, masterSnap(srcUUID+":1-900")).attach()
	_ = otherTopo
	r.clk.Add(time.Second) // the replica's report is the newer one
	r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	v := r.view(rID)
	if v.Status.GTIDGap != 100 || v.Status.Code != StatusSynced {
		t.Fatalf("replica view = %+v, want a gap of 100 to its own master, not yet lagging", v.Status)
	}
	r.clk.Add(61 * time.Second)
	r.m.tick()
	v = r.view(rID)
	if v.Status.Code != StatusLagging || !strings.Contains(v.Status.Detail, "100 transaksi") {
		t.Fatalf("after 61 s: %+v, want Ketinggalan by 100 transactions", v.Status)
	}
}

// gapSince is the replica's gap bookkeeping, read under the lock.
func (r *rig) gapSince(id int64) time.Time {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return r.m.servers[id].gapSince
}

// TestGapNotTrackedAgainstNewerMaster: agents do not report GTID movement
// as a change, so a master summary newer than the replica's report says
// nothing about the replica lagging.
func TestGapNotTrackedAgainstNewerMaster(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()

	r.clk.Add(70 * time.Second)
	ma.setSnap(func(s *proto.HealthSnapshot) { s.GTIDExecuted = srcUUID + ":1-200" })
	ma.send()
	for range 3 {
		r.m.tick()
		if g := r.gapSince(rID); !g.IsZero() {
			t.Fatalf("gapSince = %v against a newer master report, want unchanged (zero)", g)
		}
		r.clk.Add(61 * time.Second)
	}
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("replica status %q (%s), want Sinkron", v.Status.Code, v.Status.Detail)
	}
	if n := len(r.events(rID, "status")); n != 1 {
		t.Fatalf("status events = %d, want only the first Sinkron", n)
	}
}

// TestBacklogEntryDoesNotTrackGap: a backlog report is compared with no
// master set; neither its ingestion nor a tick before the next live
// report starts the gap clock.
func TestBacklogEntryDoesNotTrackGap(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(mID, true, masterSnap(srcUUID+":1-200")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-200")).attach()

	r.clk.Add(time.Second)
	ra.emit(proto.HealthEvent{Seq: 10, Backlog: true, Snapshot: replicaSnap(srcUUID + ":1-100")})
	if g := r.gapSince(rID); !g.IsZero() {
		t.Fatalf("gapSince = %v after a backlog entry, want zero", g)
	}
	r.m.tick()
	if g := r.gapSince(rID); !g.IsZero() {
		t.Fatalf("gapSince = %v after a tick on a backlog snapshot, want zero", g)
	}
}

// TestReplicasDisconnectedCountsOnlyLiveReplicas: a master presumed down
// counts the replicas that lost it, but not one whose agent is gone or
// whose report is stale.
func TestReplicasDisconnectedCountsOnlyLiveReplicas(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	live := r.replica("db-live", topo)
	gone := r.replica("db-gone", topo)
	stale := r.replica("db-stale", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	connecting := replicaSnap(srcUUID + ":1-100")
	connecting.Replica.IORunning = "Connecting"
	r.agent(live, true, connecting).attach()
	ga := r.agent(gone, true, connecting).attach()
	ga.detach()
	sa := r.agent(stale, true, replicaSnap(srcUUID+":1-100")).attach()
	sa.emit(proto.HealthEvent{Seq: 100, TakenAgoMS: 400_000, Snapshot: connecting}) // older than StaleAfter (360 s)

	ma.detach()
	r.clk.Add(31 * time.Second)
	r.m.tick()
	v := r.view(mID)
	if v.Status.Code != StatusDown || !strings.Contains(v.Status.Detail, "dan 1 replika kehilangan koneksi") {
		t.Fatalf("master = %q (%s), want Diduga tumbang with 1 replica", v.Status.Code, v.Status.Detail)
	}
}

// TestStartupGraceForStoredReports: after a panel downtime longer than
// StaleAfter, the stored reports are not stale until one StaleAfter has
// passed since startup.
func TestStartupGraceForStoredReports(t *testing.T) {
	var rID int64
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // the rig's clock
	r := newRig(t, func(st *store.Store) {
		m, _ := st.CreateServer("db-master")
		topo, err := st.CreateTopology(m.ID, "u", nil, "h", 3306)
		if err != nil {
			t.Fatal(err)
		}
		rep, _ := st.CreateServer("db-replica")
		rID = rep.ID
		st.AddReplica(topo.ID, rID, nil)
		st.SetReplicaState(rID, store.ReplicaReady, "")
		b, _ := json.Marshal(replicaSnap(srcUUID + ":1-100"))
		if err := st.SetHealth(store.Health{ServerID: rID, ReceivedAt: start.Add(-2 * time.Hour), SnapshotJSON: string(b),
			Status: StatusSynced, StatusSince: start.Add(-3 * time.Hour), LockOK: true}); err != nil {
			t.Fatal(err)
		}
	})
	// The agent is connected (the hub decides AgentOnline) but has not
	// reported yet.
	r.m.mu.Lock()
	r.m.servers[rID].online = true
	r.m.mu.Unlock()

	r.m.tick()
	r.clk.Add(5 * time.Minute)
	r.m.tick()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("within the startup grace: %q (%s), want Sinkron", v.Status.Code, v.Status.Detail)
	}
	if n := len(r.events(rID, "")); n != 0 {
		t.Fatalf("events within the startup grace = %d, want 0", n)
	}
	r.clk.Add(61 * time.Second) // 6 min 1 s after startup
	r.m.tick()
	if v := r.view(rID); v.Status.Code != StatusAgentOffline || !strings.Contains(v.Status.Detail, "Laporan terakhir") {
		t.Fatalf("after StaleAfter: %q (%s), want Agent offline for a stale report", v.Status.Code, v.Status.Detail)
	}
}

func TestOversizedPayloadIgnored(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)

	if r.m.HandleEvent(rID, proto.Event{Type: proto.EventJobLog}) {
		t.Fatal("HandleEvent took a job event")
	}
	snap := replicaSnap(srcUUID + ":1-100")
	snap.MySQLError = strings.Repeat("x", 70<<10)
	b, _ := json.Marshal(proto.HealthEvent{Seq: 1, Snapshot: snap})
	if !r.m.HandleEvent(rID, proto.Event{Type: proto.EventHealth, Payload: b}) {
		t.Fatal("HandleEvent did not claim the health event")
	}
	if v, ok := r.m.View(rID); ok && v.Snap != nil {
		t.Fatal("oversized report was stored")
	}
	if _, err := r.st.GetHealth(rID); err != store.ErrNotFound {
		t.Fatalf("GetHealth err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(r.logs.String(), "level=WARN") || strings.Contains(r.logs.String(), "xxxx") {
		t.Fatalf("log = %q, want one warning without the payload", r.logs.String())
	}
	if len(r.healthUpdates(rID)) != 0 {
		t.Fatal("oversized report published")
	}
}

func TestReconfigureSendsToOnlineAgents(t *testing.T) {
	r := newRig(t)
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	offID := r.replica("db-off", topo)
	ma := r.agent(mID, true, masterSnap(srcUUID+":1-100")).attach()
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	off := r.agent(offID, true, replicaSnap(srcUUID+":1-100")).attach()
	off.detach()

	s, err := LoadSettings(r.st)
	if err != nil {
		t.Fatal(err)
	}
	s.CheckEvery, s.SummaryEvery, s.Lag = 30*time.Second, 600*time.Second, 120*time.Second
	if err := SaveSettings(r.st, s); err != nil {
		t.Fatal(err)
	}
	r.m.Reconfigure()
	r.settle()

	for _, a := range []*fakeAgent{ma, ra} {
		if n := a.count("monitor.configure"); n != 2 {
			t.Fatalf("server %d got monitor.configure %d times, want 2", a.id, n)
		}
		p := a.lastConfig()
		if p.CheckEverySec != 30 || p.SummaryEverySec != 600 || p.LagThresholdSec != 120 {
			t.Fatalf("server %d configure = %+v", a.id, p)
		}
	}
	if ra.lastConfig().RoleHint != "replica" {
		t.Fatalf("replica RoleHint = %q", ra.lastConfig().RoleHint)
	}
	if n := off.count("monitor.configure"); n != 1 {
		t.Fatalf("offline agent got monitor.configure %d times, want only the first", n)
	}
	// The new summary interval moves the staleness limit to 30 min.
	r.clk.Add(20 * time.Minute)
	r.m.tick()
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("after 20 min without reports: %q, want still Sinkron", v.Status.Code)
	}
}

func TestCheckNowRateLimited(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	ctx := context.Background()

	r.clk.Add(time.Minute)
	ra.setSnap(func(s *proto.HealthSnapshot) { s.UptimeSec = 2000 })
	if err := r.m.CheckNow(ctx, rID); err != nil {
		t.Fatal(err)
	}
	if v := r.view(rID); !v.ReceivedAt.Equal(r.clk.Now()) || v.Snap.UptimeSec != 2000 {
		t.Fatalf("view after CheckNow = %+v", v)
	}
	r.clk.Add(5 * time.Second)
	err := r.m.CheckNow(ctx, rID)
	if err == nil || err.Error() != "tunggu 10 detik sebelum memeriksa lagi" {
		t.Fatalf("second CheckNow err = %v", err)
	}
	if n := ra.count("monitor.snapshot"); n != 1 {
		t.Fatalf("monitor.snapshot sent %d times, want 1", n)
	}
	r.clk.Add(5 * time.Second)
	if err := r.m.CheckNow(ctx, rID); err != nil {
		t.Fatalf("CheckNow after 10 s: %v", err)
	}
	if n := ra.count("monitor.snapshot"); n != 2 {
		t.Fatalf("monitor.snapshot sent %d times, want 2", n)
	}
}

// TestLoadsHealthOnStartup: the view is loaded from the store; the tick
// no longer prunes status events (the panel's daily retention does, spec
// 5.12).
func TestLoadsHealthOnStartup(t *testing.T) {
	var rID int64
	since := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	r := newRig(t, func(st *store.Store) {
		m, _ := st.CreateServer("db-master")
		topo, err := st.CreateTopology(m.ID, "u", nil, "h", 3306)
		if err != nil {
			t.Fatal(err)
		}
		rep, _ := st.CreateServer("db-replica")
		rID = rep.ID
		st.AddReplica(topo.ID, rID, nil)
		st.SetReplicaState(rID, store.ReplicaReady, "")
		b, _ := json.Marshal(replicaSnap(srcUUID + ":1-100"))
		if err := st.SetHealth(store.Health{ServerID: rID, ReceivedAt: since.Add(59 * time.Minute), SnapshotJSON: string(b),
			Status: StatusSynced, StatusSince: since, LockOK: true, AgentVersion: "3.0.0"}); err != nil {
			t.Fatal(err)
		}
		st.AddStatusEvent(store.StatusEvent{ServerID: rID, TS: since.Add(-91 * 24 * time.Hour), Kind: "status", ToStatus: StatusSynced})
		st.AddStatusEvent(store.StatusEvent{ServerID: rID, TS: since.Add(-89 * 24 * time.Hour), Kind: "status", ToStatus: StatusSynced})
	})
	v := r.view(rID)
	if v.Role != "replica" || v.Status.Code != StatusSynced || !v.Since.Equal(since) || v.Snap == nil || v.Snap.Replica == nil {
		t.Fatalf("loaded view = %+v", v)
	}
	r.m.tick()
	if n := len(r.events(rID, "")); n != 2 {
		t.Fatalf("events after the first tick = %d, want both kept", n)
	}
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("within the offline grace the status is %q", v.Status.Code)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes (log output).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestHealthReportKeepsTLSVerified (SEC-63c, Task 6 fix round 1): a ready
// replica's tls_verified follows its live reports when its agent is of the
// panel's version: a replica whose receiver connects to the topology's
// source with SOURCE_SSL_VERIFY_SERVER_CERT=1 (a paused one started later)
// becomes verified, as long as the master has the panel's certificate; a
// report showing the verification off (reconfigured outside the panel)
// clears it; an agent of another version, a receiver still connecting or
// another source leave it as it is.
func TestHealthReportKeepsTLSVerified(t *testing.T) {
	r := newRig(t)
	r.m.version = "v-test"
	mID, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	if err := r.st.UpdateProbe(rID, proto.ProbeResult{}, "v-test", r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	snap := replicaSnap(srcUUID + ":1-100")
	snap.Replica.SourceHost, snap.Replica.SourcePort = "10.0.0.1", 3306
	a := r.agent(rID, true, snap).attach()
	verified := func() bool {
		t.Helper()
		rep, err := r.st.GetReplicaByServer(rID)
		if err != nil {
			t.Fatal(err)
		}
		return rep.TLSVerified
	}
	set := func(io string, verify bool, host string) {
		a.setSnap(func(s *proto.HealthSnapshot) {
			s.Replica.IORunning, s.Replica.SSLVerifyServerCert, s.Replica.SourceHost = io, verify, host
		})
		a.send()
	}

	// Verified link, but the master has no panel certificate: unchanged.
	set("Yes", true, "10.0.0.1")
	if verified() {
		t.Fatal("verified without the master's certificate")
	}
	if err := r.st.SetServerTLS(store.ServerTLS{ServerID: mID, CertSerial: "01", NotAfter: r.clk.Now().AddDate(2, 0, 0),
		InstalledAt: r.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	// Still connecting, or another source: unchanged.
	set("Connecting", true, "10.0.0.1")
	set("Yes", true, "10.9.9.9")
	if verified() {
		t.Fatal("verified while connecting or on another source")
	}
	// Connected with verification to the topology's source: verified.
	set("Yes", true, "10.0.0.1")
	if !verified() {
		t.Fatal("not verified once the receiver connected with verification")
	}
	// Reconfigured outside the panel without verification: cleared.
	set("Yes", false, "10.0.0.1")
	if verified() {
		t.Fatal("still verified with SOURCE_SSL_VERIFY_SERVER_CERT=0")
	}
	// An agent of another version (no such field): unchanged either way.
	set("Yes", true, "10.0.0.1")
	if err := r.st.UpdateProbe(rID, proto.ProbeResult{}, "v-lama", r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	set("Yes", false, "10.0.0.1")
	if !verified() {
		t.Fatal("an older agent's report cleared tls_verified")
	}
	if err := r.st.SetReplicaTLSVerified(rID, false); err != nil {
		t.Fatal(err)
	}
	set("Yes", true, "10.0.0.1")
	if verified() {
		t.Fatal("an older agent's report set tls_verified")
	}
}

// sendSample emits a monitor.sample of the agent through the hub.
func (a *fakeAgent) sendSample(se proto.SampleEvent) {
	b, err := json.Marshal(se)
	if err != nil {
		a.r.t.Fatal(err)
	}
	a.r.hub.OnEvent(a.id, proto.Event{Type: proto.EventSample, Payload: b})
}

// samplesAt is serverID's stored samples between from and to, one point
// per second.
func (r *rig) samplesAt(serverID int64, from, to time.Time) []store.LagPoint {
	r.t.Helper()
	got, err := r.st.LagSeries([]int64{serverID}, from, to, time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	return got[serverID]
}

// TestPanelStoresSampleAndSnapshotLag (spec 5.7, 8.1): a monitor.sample
// becomes a health_samples row at the panel's now minus its age; a health
// report (also from the backlog, also from an agent without samples)
// writes one at the report's time; a sample and a report at the same
// second are one row. Samples never change the status; one arriving
// within 5 s of the server's previous sample is dropped and counted in
// ReportsDropped.
func TestPanelStoresSampleAndSnapshotLag(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	start := r.clk.Now()
	if pts := r.samplesAt(rID, start.Add(-time.Minute), start.Add(time.Minute)); len(pts) != 1 ||
		!pts[0].T.Equal(start) || pts[0].Lag == nil || *pts[0].Lag != 0 {
		t.Fatalf("samples after the first report = %+v, want one at %v with lag 0", pts, start)
	}
	statusEvents := len(r.events(rID, "status"))

	// A sample taken 1.5 s ago, the threads stopped: stored, status kept.
	r.clk.Add(30 * time.Second)
	ra.sendSample(proto.SampleEvent{TakenAgoMS: 1500, LagSec: ptr(42), IORunning: "No", SQLRunning: "No", MySQLOK: true})
	at := r.clk.Now().Add(-1500 * time.Millisecond).Truncate(time.Second)
	pts := r.samplesAt(rID, start.Add(time.Second), r.clk.Now())
	if len(pts) != 1 || !pts[0].T.Equal(at) || pts[0].Lag == nil || *pts[0].Lag != 42 {
		t.Fatalf("samples after monitor.sample = %+v, want lag 42 at %v", pts, at)
	}
	if v := r.view(rID); v.Status.Code != StatusSynced {
		t.Fatalf("status after a sample = %q, want %q (samples never change it)", v.Status.Code, StatusSynced)
	}
	if n := len(r.events(rID, "status")); n != statusEvents {
		t.Fatalf("a sample wrote %d status events", n-statusEvents)
	}

	// Within 5 s of the last sample: dropped and counted.
	r.clk.Add(2 * time.Second)
	ra.sendSample(proto.SampleEvent{LagSec: ptr(77), IORunning: "Yes", SQLRunning: "Yes", MySQLOK: true})
	if pts := r.samplesAt(rID, start.Add(time.Second), r.clk.Now()); len(pts) != 1 {
		t.Fatalf("a sample 2 s after the last one was stored: %+v", pts)
	}
	if v := r.view(rID); v.ReportsDropped != 1 {
		t.Fatalf("ReportsDropped = %d, want 1 (the early sample)", v.ReportsDropped)
	}
	r.clk.Add(5 * time.Second)
	ra.sendSample(proto.SampleEvent{LagSec: nil, IORunning: "Connecting", SQLRunning: "Yes", MySQLOK: true})
	if pts := r.samplesAt(rID, start.Add(time.Second), r.clk.Now()); len(pts) != 2 || pts[1].Lag != nil {
		t.Fatalf("samples after one 5 s later = %+v, want 2, the last without lag", pts)
	}

	// An unreadable, oversized or too old sample is dropped and counted,
	// however long after the last one it comes. An age that would
	// overflow (MaxInt64 ms) never places a row anywhere.
	end := r.clk.Now().Add(2 * time.Minute) // past every sample below
	rows := len(r.samplesAt(rID, start.Add(-time.Second), end))
	dropped := r.view(rID).ReportsDropped
	for i, ev := range []proto.Event{
		{Type: proto.EventSample, Payload: json.RawMessage(`{"lag_sec":"x"`)},
		{Type: proto.EventSample, Payload: json.RawMessage(`"` + strings.Repeat("a", 2048) + `"`)},
		{Type: proto.EventSample, Payload: json.RawMessage(`{"taken_ago_ms":9223372036854775807,"lag_sec":1,"mysql_ok":true}`)},
		{Type: proto.EventSample, Payload: json.RawMessage(`{"taken_ago_ms":61000,"lag_sec":1,"mysql_ok":true}`)},
	} {
		r.clk.Add(10 * time.Second)
		r.hub.OnEvent(rID, ev)
		if v := r.view(rID); v.ReportsDropped != dropped+i+1 {
			t.Fatalf("bad sample %d: ReportsDropped = %d, want %d", i, v.ReportsDropped, dropped+i+1)
		}
	}
	all := r.samplesAt(rID, start.Add(-time.Second), end)
	if len(all) != rows {
		t.Fatalf("bad samples stored rows: %d, want %d", len(all), rows)
	}
	if ok, err := r.st.HasSamples(rID, r.clk.Now().Add(time.Second)); err != nil || ok {
		t.Fatalf("a sample was placed in the future: %v, %v", ok, err)
	}
	// 60 s old is still taken.
	ra.sendSample(proto.SampleEvent{TakenAgoMS: 60_000, LagSec: ptr(3), MySQLOK: true})
	if n := len(r.samplesAt(rID, start.Add(-time.Second), end)); n != rows+1 {
		t.Fatalf("a 60 s old sample: %d rows, want %d", n, rows+1)
	}

	// A health report (an agent without samples gives these only): a row
	// at the report's time; a sample at the same second adds nothing.
	r.clk.Add(time.Minute)
	ra.setSnap(func(s *proto.HealthSnapshot) {
		rs := *s.Replica
		rs.SecondsBehind = ptr(7)
		s.Replica = &rs
	})
	b, err := json.Marshal(proto.HealthEvent{Seq: 50, TakenAgoMS: 3000, Snapshot: func() proto.HealthSnapshot {
		ra.mu.Lock()
		defer ra.mu.Unlock()
		return ra.snap
	}()})
	if err != nil {
		t.Fatal(err)
	}
	r.hub.OnEvent(rID, proto.Event{Type: proto.EventHealth, Payload: b})
	repAt := r.clk.Now().Add(-3 * time.Second)
	ra.sendSample(proto.SampleEvent{TakenAgoMS: 3000, LagSec: ptr(8), IORunning: "Yes", SQLRunning: "Yes", MySQLOK: true})
	pts = r.samplesAt(rID, repAt.Add(-time.Second), r.clk.Now())
	if len(pts) != 1 || !pts[0].T.Equal(repAt) || pts[0].Lag == nil || *pts[0].Lag != 7 {
		t.Fatalf("samples around the report = %+v, want one at %v with the report's lag 7", pts, repAt)
	}

	// A backlog report fills the gap at its own time.
	r.clk.Add(time.Minute)
	b, err = json.Marshal(proto.HealthEvent{Seq: 51, TakenAgoMS: 40_000, Backlog: true, Snapshot: replicaSnap(srcUUID + ":1-100")})
	if err != nil {
		t.Fatal(err)
	}
	r.hub.OnEvent(rID, proto.Event{Type: proto.EventHealth, Payload: b})
	blAt := r.clk.Now().Add(-40 * time.Second)
	if pts := r.samplesAt(rID, blAt, blAt); len(pts) != 1 {
		t.Fatalf("no sample from the backlog report at %v: %+v", blAt, pts)
	}
	if ok, err := r.st.HasSamples(rID, start); err != nil || !ok {
		t.Fatalf("HasSamples = %v, %v", ok, err)
	}
}

// TestReportSampleBounds (fix round 1): a health report gives no sample
// when its age would place it in the future (an overflowing TakenAgoMS)
// or before the samples' retention (7 days); one with long thread states
// is still stored (clipped, clipState).
func TestReportSampleBounds(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	day := 24 * time.Hour
	r.clk.Add(10 * day)
	now := r.clk.Now()
	inPast := func() int {
		t.Helper()
		got, err := r.st.LagSeries([]int64{rID}, now.Add(-9*day), now.Add(-7*day), day)
		if err != nil {
			t.Fatal(err)
		}
		return len(got[rID])
	}
	send := func(seq uint64, ago int64, snap proto.HealthSnapshot) {
		t.Helper()
		b, err := json.Marshal(proto.HealthEvent{Seq: seq, TakenAgoMS: ago, Backlog: true, Snapshot: snap})
		if err != nil {
			t.Fatal(err)
		}
		r.hub.OnEvent(rID, proto.Event{Type: proto.EventHealth, Payload: b})
	}
	send(10, math.MaxInt64, replicaSnap(srcUUID+":1-100"))
	send(11, (8 * day).Milliseconds(), replicaSnap(srcUUID+":1-100"))
	if ok, err := r.st.HasSamples(rID, now.Add(time.Second)); err != nil || ok {
		t.Fatalf("a report sample was placed in the future: %v, %v", ok, err)
	}
	if n := inPast(); n != 0 {
		t.Fatalf("a report 8 days old stored %d samples, want 0", n)
	}
	if ok, err := r.st.HasSamples(rID, now.Add(-7*day)); err != nil || ok {
		t.Fatalf("out-of-window reports stored a sample: %v, %v", ok, err)
	}

	long := replicaSnap(srcUUID + ":1-100")
	long.Replica.IORunning = strings.Repeat("é", 40)
	long.Replica.SQLRunning = strings.Repeat("x", 4000)
	send(12, 1000, long)
	if ok, err := r.st.HasSamples(rID, now.Add(-2*time.Second)); err != nil || !ok {
		t.Fatalf("a report with long thread states stored no sample: %v, %v", ok, err)
	}
}

func TestClipState(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Yes", "Yes"},
		{"Connecting", "Connecting"},
		{strings.Repeat("x", 40), strings.Repeat("x", 16)},
		{strings.Repeat("é", 40), strings.Repeat("é", 8)},             // 2-byte runes: 16 bytes exactly
		{"x" + strings.Repeat("é", 40), "x" + strings.Repeat("é", 7)}, // never half a rune
	} {
		if got := clipState(c.in); got != c.want || len(got) > maxSampleState {
			t.Errorf("clipState(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestReportAgeBounded (SEC-81): an agent's taken_ago_ms near MaxInt64
// would overflow the Duration product and place the report in the future,
// keeping the server fresh after its reports stop. The age is clamped to
// the events' retention before it is multiplied.
func TestReportAgeBounded(t *testing.T) {
	r := newRig(t)
	_, topo := r.master("db-master")
	rID := r.replica("db-replica", topo)
	ra := r.agent(rID, true, replicaSnap(srcUUID+":1-100")).attach()
	r.clk.Add(time.Minute)
	stopped := replicaSnap(srcUUID + ":1-100")
	stopped.Replica.SQLRunning, stopped.Replica.LastSQLErrno = "No", 1032
	ra.emit(proto.HealthEvent{Seq: 1000, TakenAgoMS: math.MaxInt64, Snapshot: stopped})
	now := r.clk.Now()
	if v := r.view(rID); v.ReceivedAt.After(now) {
		t.Fatalf("report placed in the future: ReceivedAt %v after now %v", v.ReceivedAt, now)
	}
	for _, ev := range r.events(rID, "status") {
		if ev.TS.After(now) {
			t.Fatalf("status event in the future: %+v (now %v)", ev, now)
		}
	}
	r.m.mu.Lock()
	limit := time.Duration(r.m.settings.EventsRetentionDays) * 24 * time.Hour
	got := r.m.reportAge(rID, math.MaxInt64)
	clamped := r.m.ageClamped[rID]
	r.m.mu.Unlock()
	if got != limit {
		t.Fatalf("reportAge(MaxInt64) = %v, want the events' retention %v", got, limit)
	}
	if !clamped {
		t.Fatal("clamped age not recorded for the once-per-server log")
	}
	if at := now.Add(-got); at.After(now) {
		t.Fatalf("at = %v is in the future (now %v)", at, now)
	}
}
