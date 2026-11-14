package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// fakeHealth is the Monitor as the Runner sees it: scripted views, and
// CheckNow recorded.
type fakeHealth struct {
	mu     sync.Mutex
	views  map[int64]monitor.View
	checks []int64
}

func (h *fakeHealth) CheckNow(_ context.Context, id int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, id)
	return nil
}

func (h *fakeHealth) View(id int64) (monitor.View, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.views[id]
	return v, ok
}

func (h *fakeHealth) set(id int64, v monitor.View) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v.ServerID = id
	h.views[id] = v
}

func (h *fakeHealth) checked() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.checks)
}

const (
	otherUUID = "5a33bc69-93ec-33a3-b02c-e01cc1641784"
	lagUUIDs  = masterUUID + ":1-118"
)

// promoteRig is a master with three ready replicas: c (the candidate,
// 10.0.0.2), b (10.0.0.3) and d (10.0.0.4), every agent online and every
// report fresh, the master Sehat.
type promoteRig struct {
	e       *env
	h       *fakeHealth
	m, c, b *fakeAgent
	d       *fakeAgent
	topo    store.Topology
}

func healthyView(code, gtid string, replica *proto.ReplicaStatus) monitor.View {
	return monitor.View{Status: monitor.Status{Code: code}, ReceivedAt: time.Now(),
		Snap: &proto.HealthSnapshot{MySQLOK: true, GTIDExecuted: gtid, Replica: replica}}
}

func replicaView(executed, retrieved string) monitor.View {
	lag := int64(0)
	return monitor.View{Role: store.RoleReplica, Status: monitor.Status{Code: monitor.StatusSynced, LagSec: &lag}, ReceivedAt: time.Now(),
		Snap: &proto.HealthSnapshot{MySQLOK: true, GTIDExecuted: executed, SuperReadOnly: true,
			Replica: &proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", ExecutedGTID: executed, RetrievedGTID: retrieved}}}
}

func newPromoteRig(t *testing.T) *promoteRig {
	e := newEnv(t)
	h := &fakeHealth{views: map[int64]monitor.View{}}
	e.r.d.Health = h
	e.r.promoteCatchUp = 2 * time.Second
	m, topo := e.readyMaster("db-master")
	g := &promoteRig{e: e, h: h, m: m, topo: topo}
	h.set(m.id, healthyView(monitor.StatusHealthy, masterGTID, nil))
	now := time.Now()
	m.reply("mysql.lockForPromote", proto.LockForPromoteResult{LockedAt: now, Deadline: now.Add(30 * time.Minute),
		ServerUUID: masterUUID, GTIDExecuted: masterGTID})
	m.reply("mysql.unlockPromote", nil)
	m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
	m.reply("mysql.binlogTail", proto.BinlogTailResult{GTIDExecuted: masterGTID, File: "binlog.000007", MySQLRunning: true})
	m.reply("mysql.releaseRole", nil)

	add := func(name, addr string, dbs ...string) *fakeAgent {
		fa := e.agent(name, addr)
		if _, err := e.st.AddReplica(topo.ID, fa.id, dbs); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetReplicaState(fa.id, store.ReplicaReady, ""); err != nil {
			t.Fatal(err)
		}
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: true, Host: "10.0.0.1", Port: 3306})
		fa.reply("mysql.repoint", proto.RepointResult{IORunning: true})
		fa.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID,
			SuperReadOnly: true, ReadOnly: true, TLSAvailable: true})
		h.set(fa.id, replicaView(masterGTID, masterGTID))
		return fa
	}
	g.c = add("cand", "10.0.0.2")
	g.b = add("rep-b", "10.0.0.3")
	g.d = add("rep-d", "10.0.0.4")
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	g.c.reply("mysql.createReplUser", nil)
	// The swap step re-reads the new master's inventory.
	g.c.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: masterDBs})
	g.c.job("mysql.promote", func(j *jobs.Job, raw json.RawMessage) (any, error) {
		var p proto.PromoteParams
		json.Unmarshal(raw, &p)
		j.Logf("info", "replika dipromosikan (%s)", p.Mode)
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: p.Mode}, nil
	})
	return g
}

// offline takes fa's agent away and its report with it.
func (g *promoteRig) offline(fa *fakeAgent) {
	fa.detach()
	g.h.set(fa.id, monitor.View{Status: monitor.Status{Code: monitor.StatusAgentOffline}})
}

func (g *promoteRig) promotion() store.Promotion {
	g.e.t.Helper()
	p, err := g.e.st.LatestPromotion(g.topo.ID)
	if err != nil {
		g.e.t.Fatal(err)
	}
	return p
}

func plannedSteps() []string {
	return []string{"Buat akun replikasi di kandidat", "Kunci master", "Replika mengejar", "Jadikan master baru",
		"Karantina master lama", "Perbarui data panel", "Arahkan replika lain", "Selesai"}
}

func TestPromotePlannedStepsAndCommands(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.offline(g.d)
	ctx := context.Background()

	// d is unreachable: refused until the user accepts it.
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}); err == nil ||
		!strings.Contains(err.Error(), "rep-d") {
		t.Fatalf("StartPromote with an unreachable replica not accepted: %v", err)
	}
	e.seqMu.Lock()
	e.seq = nil
	e.seqMu.Unlock()
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindPromotePlanned || j.ServerID != g.m.id || j.TopologyID != g.topo.ID {
		t.Fatalf("job = %+v", j)
	}
	if got := stepNames(steps); !slices.Equal(got, plannedSteps()) {
		t.Fatalf("steps = %v", got)
	}
	if got := stepStatuses(steps); got[0] != store.StepSkipped || slices.ContainsFunc(got[1:], func(s string) bool { return s != store.StepDone }) {
		t.Fatalf("step statuses = %v", got)
	}

	// The commands, across the agents, in order (the probes and snapshots
	// of the consideration data come first).
	e.seqMu.Lock()
	var seq []string
	for _, s := range e.seq {
		if !strings.HasSuffix(s, "mysql.probeSource") && !strings.HasSuffix(s, "mysql.binlogTail") {
			seq = append(seq, s)
		}
	}
	e.seqMu.Unlock()
	wantCmds(t, "all agents", seq, []string{"cand mysql.status", "db-master mysql.lockForPromote", "cand mysql.waitSynced",
		"cand mysql.promote", "db-master mysql.quarantine", "cand mysql.inventory", "rep-b mysql.repoint", "cand mysql.recordReplicas"})

	var lock proto.LockForPromoteParams
	g.m.params("mysql.lockForPromote", &lock)
	if lock.TimeoutSec != 1800 || lock.Candidate.Host != "10.0.0.2" || lock.Candidate.Port != 3306 ||
		lock.Candidate.User != replUser || lock.Candidate.Password != replPW {
		t.Fatalf("lockForPromote params = %+v", lock)
	}
	var pp proto.PromoteParams
	g.c.params("mysql.promote", &pp)
	if pp.Mode != proto.PromotePlanned || pp.OldSource.Host != "10.0.0.1" || pp.OldSource.Port != 3306 ||
		pp.LockDeadline == nil || pp.ReplUser != replUser || pp.ReplPassword != replPW {
		t.Fatalf("promote params = %+v", pp)
	}
	// NotBefore lets a retry find this promotion in the candidate's record.
	if started := g.promotion().StartedAt; pp.NotBefore.IsZero() || pp.NotBefore.After(started) || started.Sub(pp.NotBefore) > 10*time.Minute {
		t.Fatalf("promote NotBefore = %v, promotion started %v", pp.NotBefore, started)
	}
	var w proto.WaitSyncedParams
	g.c.params("mysql.waitSynced", &w)
	if w.GTIDSet != masterGTID {
		t.Fatalf("waitSynced params = %+v", w)
	}
	var q proto.QuarantineParams
	g.m.params("mysql.quarantine", &q)
	if q.NewMaster.Host != "10.0.0.2" || q.NewMaster.Port != 3306 || q.NewMaster.Password != replPW {
		t.Fatalf("quarantine params = %+v", q)
	}
	var rp proto.RepointParams
	g.b.params("mysql.repoint", &rp)
	if rp.NewMaster.Host != "10.0.0.2" || rp.NewMaster.Port != 3306 || rp.Verify {
		t.Fatalf("repoint params = %+v", rp)
	}

	// The panel's data: swapped, d waits for its agent, b follows c.
	topo, err := e.st.GetTopology(g.topo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if topo.MasterServerID != g.c.id || topo.SourceHost != "10.0.0.2" || topo.SourcePort != 3306 ||
		!topo.CutoverPending || topo.OldMasterServerID != g.m.id {
		t.Fatalf("topology after promote = %+v", topo)
	}
	if srv, _ := e.st.GetServer(g.m.id); srv.Role != store.RoleQuarantined {
		t.Fatalf("old master role = %s", srv.Role)
	}
	if srv, _ := e.st.GetServer(g.c.id); srv.Role != store.RoleMaster {
		t.Fatalf("new master role = %s", srv.Role)
	}
	if _, err := e.st.GetReplicaByServer(g.c.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("candidate's replica row: %v", err)
	}
	if st := e.replica(g.d.id).State; st != store.ReplicaNeedsRepoint {
		t.Fatalf("offline replica state = %s, want needs_repoint", st)
	}
	if st := e.replica(g.b.id).State; st != store.ReplicaReady {
		t.Fatalf("repointed replica state = %s, want ready", st)
	}
	p := g.promotion()
	if p.Status != "done" || p.Mode != proto.PromotePlanned || p.OldMasterServerID != g.m.id || p.NewMasterServerID != g.c.id ||
		p.JobID != id || p.LockAt == nil || p.FinishedAt == nil || p.LastMasterGTID != masterGTID || QuarantinePending(p) {
		t.Fatalf("promotion = %+v", p)
	}
	// Only one promotion per topology at a time; the master is gone anyway.
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.c.id, CandidateServerID: g.b.id}); err == nil {
		t.Fatal("a second promote while cutover is pending was accepted")
	}
}

func TestPromotePlannedCatchUpTimeoutUnlocks(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 2 || !strings.Contains(j.Error, "Replika mengejar") {
		t.Fatalf("failed at %d: %q (%v)", j.CurrentStep, j.Error, stepStatuses(steps))
	}
	e.waitFor("unlockPromote", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	if g.c.count("mysql.promote") != 0 || g.m.count("mysql.quarantine") != 0 {
		t.Fatalf("promote went on: candidate %v, master %v", g.c.cmds(0), g.m.cmds(0))
	}
	var u proto.UnlockPromoteParams
	g.m.params("mysql.unlockPromote", &u)
	if u.Candidate.Host != "10.0.0.2" {
		t.Fatalf("unlock params = %+v", u)
	}
	topo, _ := e.st.GetTopology(g.topo.ID)
	if topo.MasterServerID != g.m.id || topo.CutoverPending {
		t.Fatalf("topology changed: %+v", topo)
	}
	if p := g.promotion(); p.Status != "failed" {
		t.Fatalf("promotion status = %s", p.Status)
	}
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry of a failed promote before the point of no return: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	// The retry took the lock again before waiting, and lifted it again.
	if g.m.count("mysql.lockForPromote") != 2 || g.m.count("mysql.unlockPromote") != 2 {
		t.Fatalf("master commands after retry: %v", g.m.cmds(0))
	}
}

func TestPromotePartialThenRetryResumes(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.m.fail("mysql.quarantine", "master lama tidak menjawab")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobPartial)
	if j.CurrentStep != 4 || steps[4].Status != store.StepFailed {
		t.Fatalf("partial at %d, steps %v", j.CurrentStep, stepStatuses(steps))
	}
	// Everything after the quarantine ran.
	if steps[5].Status != store.StepDone || steps[6].Status != store.StepDone {
		t.Fatalf("steps after the quarantine = %v", stepStatuses(steps))
	}
	if topo, _ := e.st.GetTopology(g.topo.ID); topo.MasterServerID != g.c.id {
		t.Fatalf("topology not swapped: %+v", topo)
	}
	if p := g.promotion(); p.Status != "partial" || !QuarantinePending(p) {
		t.Fatalf("promotion = %+v", p)
	}
	// A partial job cannot be canceled, only retried.
	if err := e.r.Cancel(id); !errors.Is(err, store.ErrJobFinished) {
		t.Fatalf("Cancel of a partial job: %v", err)
	}

	g.m.reply("mysql.quarantine", proto.QuarantineResult{Applied: true})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if n := g.m.count("mysql.quarantine"); n != 2 {
		t.Fatalf("quarantine sent %d times, want 2", n)
	}
	if n := g.c.count("mysql.promote"); n != 1 {
		t.Fatalf("promote sent %d times, want 1", n)
	}
	if n := g.b.count("mysql.repoint"); n != 1 {
		t.Fatalf("b repointed %d times, want 1", n)
	}
	if p := g.promotion(); p.Status != "done" || QuarantinePending(p) {
		t.Fatalf("promotion after retry = %+v", p)
	}
}

func TestPromoteEmergencyQuarantineDeferredUntilOnline(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.offline(g.m)
	g.offline(g.d)
	for _, fa := range []*fakeAgent{g.c, g.b} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	ctx := context.Background()
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, Mode: proto.PromotePlanned,
		AcceptUnreachable: []int64{g.d.id}}); err == nil {
		t.Fatal("planned promote of an offline master accepted")
	}
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindPromoteEmergency {
		t.Fatalf("kind = %s", j.Kind)
	}
	if got := stepNames(steps); !slices.Equal(got, []string{"Jadikan master baru", "Perbarui data panel", "Karantina master lama",
		"Arahkan replika lain", "Selesai"}) {
		t.Fatalf("steps = %v", got)
	}
	if steps[2].Status != store.StepSkipped {
		t.Fatalf("quarantine step = %s, want skipped (deferred)", steps[2].Status)
	}
	var pp proto.PromoteParams
	g.c.params("mysql.promote", &pp)
	if pp.Mode != proto.PromoteEmergency || pp.LockDeadline != nil {
		t.Fatalf("promote params = %+v", pp)
	}
	p := g.promotion()
	if !QuarantinePending(p) || p.Status != "done" {
		t.Fatalf("promotion = %+v", p)
	}
	if st := e.replica(g.d.id).State; st != store.ReplicaNeedsRepoint {
		t.Fatalf("d = %s", st)
	}

	// The old master comes back: quarantined at once.
	g.m.attach()
	e.r.OnOnline(g.m.id)
	e.waitFor("quarantine", func() bool { return g.m.count("mysql.quarantine") == 1 })
	e.waitFor("quarantine recorded", func() bool { return !QuarantinePending(g.promotion()) })
	var q proto.QuarantineParams
	g.m.params("mysql.quarantine", &q)
	if q.NewMaster.Host != "10.0.0.2" {
		t.Fatalf("quarantine params = %+v", q)
	}
	// So does d: pointed at the new master.
	g.d.attach()
	e.r.OnOnline(g.d.id)
	e.waitFor("repoint", func() bool { return e.replica(g.d.id).State == store.ReplicaReady })
	if n := g.d.count("mysql.repoint"); n != 1 {
		t.Fatalf("d repointed %d times", n)
	}
	// Nothing more on a second reconnect.
	e.r.OnOnline(g.m.id)
	e.r.OnOnline(g.d.id)
	time.Sleep(50 * time.Millisecond)
	if g.m.count("mysql.quarantine") != 1 || g.d.count("mysql.repoint") != 1 {
		t.Fatalf("resent: quarantine %d, repoint %d", g.m.count("mysql.quarantine"), g.d.count("mysql.repoint"))
	}
}

// TestPromoteViewWaitingMasterIsEmergency: a master whose agent connected
// again but has not reported since (Menunggu laporan pertama) is judged
// like one whose agent is away: its last report is old, so the mode is
// emergency, as before the waiting status existed.
func TestPromoteViewWaitingMasterIsEmergency(t *testing.T) {
	g := newPromoteRig(t)
	v, err := g.e.r.PromoteView(context.Background(), g.m.id)
	if err != nil || v.Mode != proto.PromotePlanned {
		t.Fatalf("healthy master: mode %q, err %v", v.Mode, err)
	}
	g.h.set(g.m.id, healthyView(monitor.StatusAwaitingReport, masterGTID, nil))
	v, err = g.e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatalf("PromoteView: %v", err)
	}
	if v.Mode != proto.PromoteEmergency {
		t.Fatalf("waiting master: mode %q, want emergency", v.Mode)
	}
}

func TestPromoteViewMarksPartialAndMostCurrent(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	// b follows only database a, and is 2 transactions behind with 1 of
	// them received but not applied.
	if err := e.st.SetReplicaDatabases(g.b.id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	g.h.set(g.b.id, replicaView(lagUUIDs, masterUUID+":1-119"))
	g.offline(g.d)

	v, err := e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatalf("PromoteView: %v", err)
	}
	if v.Mode != proto.PromotePlanned || v.Branched || v.LastMasterGTID != masterGTID || v.BinlogTail == nil || v.WritePause == "" {
		t.Fatalf("view = %+v", v)
	}
	if got := g.h.checked(); !slices.Contains(got, g.m.id) || !slices.Contains(got, g.c.id) || !slices.Contains(got, g.b.id) {
		t.Fatalf("CheckNow for %v", got)
	}
	by := map[int64]Candidate{}
	for _, c := range v.Candidates {
		by[c.ServerID] = c
	}
	c, b, d := by[g.c.id], by[g.b.id], by[g.d.id]
	if !c.Eligible || !c.MostCurrent || c.Partial || c.GapToMaster != 0 || c.ProbeOK == nil || !*c.ProbeOK {
		t.Fatalf("candidate c = %+v", c)
	}
	if !b.Eligible || b.MostCurrent || !b.Partial || !slices.Equal(b.MissingDatabases, []string{"b"}) ||
		b.GapToMaster != 1 || b.ApplyBacklog != 1 {
		t.Fatalf("candidate b = %+v", b)
	}
	if d.Eligible || d.Reason != "Agent offline" || d.ProbeOK != nil {
		t.Fatalf("candidate d = %+v", d)
	}

	// A partial candidate needs AcceptPartial; an outdated agent is refused.
	if _, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id,
		AcceptUnreachable: []int64{g.d.id}}); err == nil || !strings.Contains(err.Error(), "sebagian") {
		t.Fatalf("partial candidate without AcceptPartial: %v", err)
	}
	e.r.d.Version = "1.2.3"
	if err := e.st.UpdateProbe(g.c.id, proto.ProbeResult{}, "1.2.2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateProbe(g.m.id, proto.ProbeResult{}, "1.2.3", time.Now()); err != nil {
		t.Fatal(err)
	}
	v, err = e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Candidates {
		if c.ServerID == g.c.id && (c.Eligible || c.Reason != "Agent perlu dimutakhirkan") {
			t.Fatalf("outdated candidate = %+v", c)
		}
	}
	if _, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		AcceptUnreachable: []int64{g.d.id}}); err == nil || !strings.Contains(err.Error(), "Agent perlu dimutakhirkan") {
		t.Fatalf("outdated candidate: %v", err)
	}
}

func TestPromoteViewBranchedNeedsAcceptance(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	// The master is gone with no report kept; c and b each hold
	// transactions the other lacks.
	g.offline(g.m)
	g.offline(g.d)
	g.h.set(g.c.id, replicaView(masterUUID+":1-120,"+otherUUID+":1-2", masterUUID+":1-120"))
	g.h.set(g.b.id, replicaView(masterUUID+":1-121", masterUUID+":1-121"))
	for _, fa := range []*fakeAgent{g.c, g.b} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "timeout"})
	}
	v, err := e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatalf("PromoteView: %v", err)
	}
	if v.Mode != proto.PromoteEmergency || !v.Branched || v.BinlogTail != nil {
		t.Fatalf("view = %+v", v)
	}
	for _, c := range v.Candidates {
		if c.MostCurrent {
			t.Fatalf("most current marked although branched: %+v", c)
		}
		if want := c.ServerID != g.d.id; c.Branched != want {
			t.Fatalf("candidate %s branched = %v, want %v", c.Name, c.Branched, want)
		}
	}
	accept := PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, AcceptUnreachable: []int64{g.d.id}}
	if _, err := e.r.StartPromote(context.Background(), accept); err == nil || !strings.Contains(err.Error(), "bercabang") {
		t.Fatalf("StartPromote on branched replicas: %v", err)
	}
	if g.c.count("mysql.promote") != 0 {
		t.Fatal("promote sent")
	}

	// Chosen with the acceptance: b, holding what c lacks, is rebuilt.
	g.b.reply("mysql.repoint", proto.RepointResult{NeedsRebuild: true, Errant: masterUUID + ":121"})
	accept.AcceptBranched = true
	id, err := e.r.StartPromote(context.Background(), accept)
	if err != nil {
		t.Fatalf("StartPromote with AcceptBranched: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if st := e.replica(g.b.id).State; st != store.ReplicaNeedsRebuild {
		t.Fatalf("b = %s, want needs_rebuild", st)
	}
}

func TestPromoteRefusesWritableReplica(t *testing.T) {
	g := newPromoteRig(t)
	v := replicaView(masterGTID, masterGTID)
	v.Snap.SuperReadOnly = false
	g.h.set(g.b.id, v)
	if _, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}); err == nil ||
		!strings.Contains(err.Error(), "rep-b bisa ditulis") {
		t.Fatalf("StartPromote with a writable replica: %v", err)
	}
}

// failedPromoteJob stores a running promote job cut off at step idx with
// st (a restart), and its promotion.
func (g *promoteRig) cutOffPromote(st jobState, idx int) (string, int64) {
	e := g.e
	e.t.Helper()
	promID, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromotePlanned, OldMasterServerID: g.m.id,
		NewMasterServerID: g.c.id, StartedAt: time.Now(), Status: "running"})
	if err != nil {
		e.t.Fatal(err)
	}
	st.PromotionID, st.MasterServerID, st.CandidateServerID = promID, g.m.id, g.c.id
	id := fmt.Sprintf("%032x", promID+100)
	e.storeOldJob(id, KindPromotePlanned, g.m.id, g.topo.ID, plannedSteps(), st, idx, store.JobRunning)
	return id, promID
}

func TestPromoteResumeAfterRestart(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	// Cut off while mysql.promote ran: the outcome is unknown, so partial.
	sent, sentProm := g.cutOffPromote(jobState{PromoteSent: true, LockTried: true}, 3)
	e.r.Resume(context.Background())
	if j, _, _ := e.st.GetJob(sent); j.Status != store.JobPartial {
		t.Fatalf("job cut off during the promote = %s, want partial", j.Status)
	}
	if p, _ := e.st.GetPromotion(sentProm); p.Status != "partial" {
		t.Fatalf("its promotion = %s", p.Status)
	}
	// A partial promotion blocks a new one.
	if _, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id}); err == nil ||
		!strings.Contains(err.Error(), "belum tuntas") {
		t.Fatalf("StartPromote after an unfinished promote: %v", err)
	}
}

func TestPromoteResumeBeforePromoteFails(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, prom := g.cutOffPromote(jobState{LockTried: true}, 2)
	e.r.Resume(context.Background())
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job cut off while catching up = %s, want failed", j.Status)
	}
	if p, _ := e.st.GetPromotion(prom); p.Status != "failed" {
		t.Fatalf("its promotion = %s", p.Status)
	}
}

func TestPromoteCancelRefusedOncePromoteSent(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.blockingJob("mysql.promote")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitFor("promote agent job", func() bool { return g.c.startedJobs() == 1 })
	if err := e.r.Cancel(id); err == nil || !strings.Contains(err.Error(), "tidak bisa dibatalkan") {
		t.Fatalf("Cancel during the promote: %v", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobRunning {
		t.Fatalf("job = %s", j.Status)
	}
	if g.m.count("mysql.unlockPromote") != 0 {
		t.Fatal("master unlocked during the promote")
	}
}

func TestPromoteLockFailureStillUnlocks(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.m.fail("mysql.lockForPromote", "master sudah dikunci, tetapi server_uuid/gtid_executed gagal dibaca")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 1 || g.m.count("mysql.unlockPromote") != 1 {
		t.Fatalf("failed at %d; master %v", j.CurrentStep, g.m.cmds(0))
	}
}

func TestPromoteExpiredLockIsTakenAgain(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	// The first lock's deadline is already past (its guard may have lifted
	// it); the job does not trust it and locks again before the promote.
	var n int
	future := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	g.m.on("mysql.lockForPromote", func(json.RawMessage) (any, error) {
		n++
		deadline := time.Now().Add(-time.Minute)
		if n > 1 {
			deadline = future
		}
		return proto.LockForPromoteResult{LockedAt: time.Now(), Deadline: deadline, GTIDExecuted: masterGTID}, nil
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	var pp proto.PromoteParams
	g.c.params("mysql.promote", &pp)
	if g.m.count("mysql.lockForPromote") != 2 || pp.LockDeadline == nil || !pp.LockDeadline.Equal(future) {
		t.Fatalf("locks %d, promote deadline %v; want 2 and %v", g.m.count("mysql.lockForPromote"), pp.LockDeadline, future)
	}
}

func TestRepointNowNeedsRebuildState(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	if err := e.st.SwapMaster(g.topo.ID, g.m.id, g.c.id, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(g.b.id, store.ReplicaNeedsRepoint, ""); err != nil {
		t.Fatal(err)
	}
	g.b.reply("mysql.repoint", proto.RepointResult{NeedsRebuild: true, Errant: otherUUID + ":1-3"})
	err := e.r.RepointNow(context.Background(), g.b.id)
	if !errors.Is(err, ErrNeedsRebuild) {
		t.Fatalf("RepointNow: %v", err)
	}
	rep := e.replica(g.b.id)
	if rep.State != store.ReplicaNeedsRebuild || !strings.Contains(rep.LastError, otherUUID) {
		t.Fatalf("replica = %+v", rep)
	}
	var rp proto.RepointParams
	g.b.params("mysql.repoint", &rp)
	if rp.NewMaster.Host != "10.0.0.2" || rp.NewMaster.User != replUser {
		t.Fatalf("repoint params = %+v", rp)
	}
	// Only a replica waiting for its repoint is repointed.
	if err := e.r.RepointNow(context.Background(), g.b.id); err == nil {
		t.Fatal("RepointNow of a needs_rebuild replica accepted")
	}
}

func TestPromoteFailureAtNewMasterUnlocksOrGoesPartial(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	failPromote := func() {
		g.c.job("mysql.promote", func(j *jobs.Job, _ json.RawMessage) (any, error) {
			return nil, errors.New("master lama tidak read-only")
		})
	}
	failPromote()
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	// The candidate is still read-only: the lock is lifted, the job fails.
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 3 || g.m.count("mysql.unlockPromote") != 1 {
		t.Fatalf("failed at %d; master %v", j.CurrentStep, g.m.cmds(0))
	}
	if p := g.promotion(); p.Status != "failed" {
		t.Fatalf("promotion = %s", p.Status)
	}

	// It fails again, but the candidate answers writable this time: past
	// the point of no return, nothing is undone and the job is partial.
	g.c.reply("mysql.status", proto.MySQLStatus{Port: 3306})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	j, _ = e.waitJob(id, store.JobPartial)
	if j.CurrentStep != 3 || g.m.count("mysql.lockForPromote") != 2 || g.m.count("mysql.unlockPromote") != 1 {
		t.Fatalf("partial at %d; master %v", j.CurrentStep, g.m.cmds(0))
	}
	if p := g.promotion(); p.Status != "partial" {
		t.Fatalf("promotion = %s", p.Status)
	}
	if err := e.r.Cancel(id); !errors.Is(err, store.ErrJobFinished) {
		t.Fatalf("Cancel of a partial promote: %v", err)
	}

	// The agent resumes its promote; the job finishes.
	g.c.job("mysql.promote", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, Resumed: true, Mode: proto.PromotePlanned}, nil
	})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if g.m.count("mysql.lockForPromote") != 2 {
		t.Fatalf("the master was locked again after the point of no return: %v", g.m.cmds(0))
	}
	if p := g.promotion(); p.Status != "done" {
		t.Fatalf("promotion = %s", p.Status)
	}
}

func TestPromoteRetrySupersededByTopologyChange(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	e.r.promoteCatchUp = 50 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Executed: lagUUIDs, Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	// Another promotion made b the master since.
	if err := e.st.SwapMaster(g.topo.ID, g.m.id, g.b.id, "10.0.0.3", 3306); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Retry(id, JobAck{}); err == nil || !strings.Contains(err.Error(), "sudah berganti") {
		t.Fatalf("Retry after the master changed: %v", err)
	}
	if reason := e.r.Superseded(id); reason == "" {
		t.Fatal("Superseded is empty")
	}
}

// A planned promotion whose promote was sent, with an unknown outcome,
// retried after the lock's deadline: mysql.promote goes again directly
// (the candidate's agent verifies), not after a relock the quarantined
// old master would refuse; the swap follows.
func TestPromoteRetryAfterDeadlineSkipsRelock(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	var statusFails atomic.Bool
	g.c.on("mysql.status", func(json.RawMessage) (any, error) {
		if statusFails.Load() {
			return nil, errors.New("MySQL tidak menjawab")
		}
		return proto.MySQLStatus{Port: 3306, ReadOnly: true, SuperReadOnly: true}, nil
	})
	g.c.job("mysql.promote", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		statusFails.Store(true)
		return nil, errors.New("koneksi ke MySQL terputus")
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, _ := e.waitJob(id, store.JobPartial)
	if j.CurrentStep != 3 {
		t.Fatalf("partial at %d", j.CurrentStep)
	}

	// Past the lock's deadline; the guard quarantined the master, which
	// refuses a lock now. The candidate answers from its promotion record.
	e.r.now = func() time.Time { return time.Now().Add(time.Hour) }
	g.m.fail("mysql.lockForPromote", "perintah mysql.lockForPromote hanya untuk server yang dipin sebagai master; server ini dipin sebagai quarantined")
	g.c.job("mysql.promote", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Resumed: true, Mode: proto.PromotePlanned}, nil
	})
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if n := g.m.count("mysql.lockForPromote"); n != 1 {
		t.Fatalf("lockForPromote sent %d times, want 1 (no relock)", n)
	}
	if g.c.count("mysql.promote") != 2 {
		t.Fatalf("promote sent %d times", g.c.count("mysql.promote"))
	}
	if topo, _ := e.st.GetTopology(g.topo.ID); topo.MasterServerID != g.c.id {
		t.Fatalf("not swapped: %+v", topo)
	}
}

// The wait for the promote ends (the agent seemed to have lost the job)
// while the job is in fact still running: the candidate still reads
// read-only, but the job ends partial, not failed, and the lock stays.
func TestPromoteWaitEndsWhileAgentJobRuns(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.blockingJob("mysql.promote")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitFor("promote agent job", func() bool { return g.c.startedJobs() == 1 })
	g.c.forgetJobs.Store(1) // the next poll: "job tidak dikenal"; then running again
	j, _ := e.waitJob(id, store.JobPartial)
	if j.CurrentStep != 3 {
		t.Fatalf("partial at %d", j.CurrentStep)
	}
	if g.m.count("mysql.unlockPromote") != 0 {
		t.Fatal("master unlocked while the promote may still run")
	}
	if p := g.promotion(); p.Status != "partial" {
		t.Fatalf("promotion = %s", p.Status)
	}
	if !strings.Contains(e.jobLog(id), "masih berjalan") {
		t.Fatalf("log:\n%s", e.jobLog(id))
	}
}

// The start call of mysql.promote fails without an answer from the agent
// (it may have started the job): partial and the lock kept. Refused by
// the agent (a definite answer): failed and unlocked, as before.
func TestPromoteStartCallLostEndsPartial(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.blockingJob("mysql.promote")
	g.c.mu.Lock()
	g.c.dropReply = map[string]bool{"mysql.promote": true}
	g.c.mu.Unlock()
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, _ := e.waitJob(id, store.JobPartial)
	if j.CurrentStep != 3 || g.m.count("mysql.unlockPromote") != 0 {
		t.Fatalf("partial at %d; master %v", j.CurrentStep, g.m.cmds(0))
	}
	if p := g.promotion(); p.Status != "partial" {
		t.Fatalf("promotion = %s", p.Status)
	}
}

func TestPromoteStartRefusedFails(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.fail("mysql.promote", "master lama: password wajib diisi")
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 3 || g.m.count("mysql.unlockPromote") != 1 {
		t.Fatalf("failed at %d; master %v", j.CurrentStep, g.m.cmds(0))
	}
}

// TestPromoteNewMasterReadyForReplicas: the swap marks the new master's
// inventory MasterReady and re-reads it (its databases, copied after its
// inventory was taken), so "Buat replika" and the old master's rebuild
// start from it without "Siapkan master" or "Segarkan".
func TestPromoteNewMasterReadyForReplicas(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	// The candidate's inventory as connecting it as an empty replica
	// target left it: connected, never prepared, no database yet.
	if err := e.st.SetInventory(store.Inventory{ServerID: g.c.id, Connected: true, Version: "8.4.3", Series: "8.4",
		UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fresh := e.replicaAgent("replica-new", "clone")
	if _, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: fresh.id}); err == nil {
		t.Fatal("Buat replika from a replica accepted")
	}

	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if inv, err := e.st.GetInventory(g.c.id); err != nil || !inv.MasterReady || !inv.Connected || len(inv.Databases) != 2 {
		t.Fatalf("new master's inventory = %+v, %v", inv, err)
	}

	finished := func(id string) {
		e.waitFor("job "+id+" to end", func() bool {
			j, _, err := e.st.GetJob(id)
			return err == nil && (j.Status == store.JobSucceeded || j.Status == store.JobFailed)
		})
	}
	newID, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: fresh.id})
	if err != nil {
		t.Fatalf("Buat replika from the new master: %v", err)
	}
	finished(newID)

	if err := e.st.SetCutoverPending(g.topo.ID, false); err != nil {
		t.Fatal(err)
	}
	g.m.reply("mysql.connections", proto.ConnectionsResult{Typed: true})
	// Root on the old master approved its rebuild.
	g.m.reply("mysql.rebuildApproval", proto.RebuildApprovalResult{Required: true, Approved: true,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"})
	rebuildID, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
		RebuildOldMaster: true})
	if err != nil {
		t.Fatalf("rebuild of the old master from the new master: %v", err)
	}
	finished(rebuildID)
}

// TestPromoteSwapInventoryRefreshFailureOnlyLogged: when the new master's
// inventory cannot be re-read, the swap step still succeeds (MasterReady
// set) and the job log tells the user to press Segarkan.
func TestPromoteSwapInventoryRefreshFailureOnlyLogged(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.on("mysql.inventory", func(json.RawMessage) (any, error) { return nil, errors.New("inventaris gagal dibaca") })
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if inv, err := e.st.GetInventory(g.c.id); err != nil || !inv.MasterReady {
		t.Fatalf("new master's inventory = %+v, %v; want MasterReady", inv, err)
	}
	if log := e.jobLog(id); !strings.Contains(log, "Tekan Segarkan") {
		t.Fatalf("job log lacks the Segarkan advice:\n%s", log)
	}
}

// emergencyAfterFailedPlanned runs a planned promotion to the candidate
// that fails in "Replika mengejar" with unlockPromote answering unlock,
// then takes the master's agent away (its replicas cannot reach it
// either), so that the master's status allows only an emergency.
func (g *promoteRig) emergencyAfterFailedPlanned(unlock func(json.RawMessage) (any, error)) {
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	g.m.on("mysql.unlockPromote", unlock)
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		e.t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitFor("unlockPromote", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	if p := g.promotion(); p.Status != promotionFailed {
		e.t.Fatalf("promotion status = %s, want failed", p.Status)
	}
	g.offline(g.m)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
}

// TestPromoteEmergencyRefusedWhileLockMayStand (SEC-66): a planned
// promotion to X fails before the point of no return and its unlock fails
// (the master's agent dropped off); the master now only allows an
// emergency. An emergency promotion of Y is refused, saying from when it
// is possible and naming unlock-master, until the lock's deadline plus
// the guard's grace has passed.
func TestPromoteEmergencyRefusedWhileLockMayStand(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.emergencyAfterFailedPlanned(func(json.RawMessage) (any, error) {
		return nil, &proto.Error{Code: proto.CodeOffline, Message: "agent offline"}
	})
	deadline := *g.promotion().LockAt // the rig's lock: LockedAt + 30 min
	deadline = deadline.Add(30 * time.Minute)
	ctx := context.Background()
	_, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id})
	if err == nil {
		t.Fatal("emergency promote while the earlier lock may stand was accepted")
	}
	until := deadline.Add(proto.PromoteLockGrace).Local().Format("02-01-2006 15:04:05")
	for _, part := range []string{"mungkin masih terkunci", "Promote darurat baru bisa dijalankan mulai " + until,
		"`sudo dbpanel agent unlock-master`, lalu tekan Ulangi pada promote terencana itu (panel memastikan kunci terbuka)"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("refusal %q lacks %q", err, part)
		}
	}
	if strings.Contains(err.Error(), "memuat data") {
		t.Errorf("emergency refusal %q carries the data-load reason", err)
	}
	if g.b.count("mysql.promote") != 0 {
		t.Fatal("candidate Y was asked to promote")
	}
	// Past the deadline but within the grace: still refused.
	e.r.now = func() time.Time { return deadline.Add(30 * time.Second) }
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id}); err == nil ||
		!strings.Contains(err.Error(), "mungkin masih terkunci") {
		t.Fatalf("emergency promote within the grace = %v, want refused", err)
	}
	// Past the deadline and the grace: the guard has decided; allowed.
	later := deadline.Add(proto.PromoteLockGrace + time.Second)
	e.r.now = func() time.Time { return later }
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		v := replicaView(masterGTID, masterGTID)
		v.ReceivedAt = later
		g.h.set(fa.id, v)
	}
	g.b.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: proto.PromoteEmergency}, nil
	})
	g.b.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: masterDBs})
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id})
	if err != nil {
		t.Fatalf("emergency promote after the deadline and grace: %v", err)
	}
	if j, _ := e.waitJob(id, store.JobSucceeded); j.Kind != KindPromoteEmergency {
		t.Fatalf("kind = %s", j.Kind)
	}
}

// TestPromoteEmergencyAllowedAfterConfirmedUnlock: the same failed planned
// promotion whose unlock the master's agent confirmed records it, and an
// emergency promotion is not held back by the old lock.
func TestPromoteEmergencyAllowedAfterConfirmedUnlock(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.emergencyAfterFailedPlanned(func(json.RawMessage) (any, error) { return nil, nil })
	j, _, err := e.st.GetJob(g.promotion().JobID)
	if err != nil {
		t.Fatal(err)
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		t.Fatal(err)
	}
	if !st.LockUnlocked || st.LockTried || st.LockTriedAt == nil {
		t.Fatalf("job state after a confirmed unlock = %+v", st)
	}
	g.b.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Mode: proto.PromoteEmergency}, nil
	})
	g.b.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: masterDBs})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.b.id})
	if err != nil {
		t.Fatalf("emergency promote after a confirmed unlock: %v", err)
	}
	if j, _ := e.waitJob(id, store.JobSucceeded); j.Kind != KindPromoteEmergency {
		t.Fatalf("kind = %s", j.Kind)
	}
}

// TestPromoteSendsLockRemaining (SEC-73): a planned mysql.promote carries
// the seconds left on the lock, counted on the panel when it is sent, with
// the deadline.
func TestPromoteSendsLockRemaining(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	var pp proto.PromoteParams
	g.c.params("mysql.promote", &pp)
	if pp.LockDeadline == nil || pp.LockRemainingSec == nil {
		t.Fatalf("promote params = %+v; want the deadline and the seconds left", pp)
	}
	if left := *pp.LockRemainingSec; left < 29*60 || left > 30*60 {
		t.Fatalf("lock_remaining_sec = %d, want about 1800", left)
	}
}

// TestRebuiltOldMasterIsNoLongerOldMaster (C-2): a completed rebuild of
// the old master clears it as the topology's old master, so a later
// failed attempt on that server is not taken for a failed rebuild and the
// server is not quarantined again; deleting an old master clears it too.
func TestRebuiltOldMasterIsNoLongerOldMaster(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	id, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if err := e.st.SetCutoverPending(g.topo.ID, false); err != nil {
		t.Fatal(err)
	}
	if topo, err := e.st.GetTopologyByOldMaster(g.m.id); err != nil || topo.ID != g.topo.ID {
		t.Fatalf("old master before the rebuild: %+v, %v", topo, err)
	}
	// The old master's agent answers a rebuild like an empty target.
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.c.reply("mysql.status", masterStatus())
	g.c.reply("mysql.createDumpUser", nil)
	g.c.reply("mysql.dropUser", nil)
	g.c.reply("mysql.sweepDumpUsers", proto.SweepDumpUsersResult{})
	g.m.reply("mysql.connections", proto.ConnectionsResult{Typed: true})
	rebuildID, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
		RebuildOldMaster: true, ConfirmWipe: true})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	e.waitJob(rebuildID, store.JobSucceeded)
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old master after the rebuild: %v, want none", err)
	}
	if srv, _ := e.st.GetServer(g.m.id); srv.Role != store.RoleReplica {
		t.Fatalf("rebuilt server role = %s, want replica", srv.Role)
	}
	// A later attempt on it that failed, then another: not quarantined.
	if err := e.st.SetReplicaState(g.m.id, store.ReplicaFailed, "uji"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id}); err != nil {
		t.Fatalf("new attempt on the rebuilt server: %v", err)
	}
	if srv, _ := e.st.GetServer(g.m.id); srv.Role == store.RoleQuarantined {
		t.Fatal("rebuilt server quarantined again by a later attempt")
	}
}

// TestDeleteServerClearsOldMaster (C-2): deleting the old master leaves
// no topology naming it.
func TestDeleteServerClearsOldMaster(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if err := e.st.DeleteServer(g.m.id); err != nil {
		t.Fatal(err)
	}
	topo, err := e.st.GetTopology(g.topo.ID)
	if err != nil || topo.OldMasterServerID != 0 {
		t.Fatalf("topology after deleting the old master = %+v, %v", topo, err)
	}
}

// SEC-75: an agent error quoting row data on the promote paths (the
// quarantine, a repoint refusal, a receiver's IO error) reaches no step
// message, replicas.last_error, promotion detail, job error or job log
// uncensored.
func TestPromoteAgentErrorsCensored(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	const quoted = "Error 'Duplicate entry 'Budi' for key 'PRIMARY'' on query. Default database: 'rs'. Query: 'INSERT INTO p VALUES ('Budi')'"
	g.m.fail("mysql.quarantine", "quarantine: "+quoted)
	g.b.fail("mysql.repoint", "repoint: "+quoted)
	g.d.reply("mysql.repoint", proto.RepointResult{IORunning: false, IOError: "receiver: Duplicate entry 'Budi' for key 'k'"})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, steps := e.waitJob(id, store.JobPartial)
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, "Budi") {
			t.Errorf("%s holds Budi: %q", what, s)
		}
	}
	check("jobs.error", j.Error)
	sawQuarantine := false
	for _, s := range steps {
		check("step "+s.Name, s.Message)
		if s.Idx == 4 && strings.Contains(s.Message, "'…'") {
			sawQuarantine = true
		}
	}
	if !sawQuarantine {
		t.Errorf("quarantine step message = %q, want the censored agent error", steps[4].Message)
	}
	for _, fa := range []*fakeAgent{g.b, g.d} {
		rep, err := e.st.GetReplicaByServer(fa.id)
		if err != nil {
			t.Fatal(err)
		}
		check("replicas.last_error", rep.LastError)
	}
	if rep, _ := e.st.GetReplicaByServer(g.b.id); !strings.Contains(rep.LastError, "Arahkan ulang gagal") {
		t.Errorf("b last_error = %q, want the repoint failure", rep.LastError)
	}
	p := g.promotion()
	check("promotion replicas", p.ReplicasJSON)
	if !strings.Contains(p.ReplicasJSON, "receiver") || !strings.Contains(p.ReplicasJSON, "quarantine") {
		t.Errorf("promotion replicas = %s, want the censored details", p.ReplicasJSON)
	}
	lines, err := e.st.JobLog(id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		check("job log", l.Message)
	}
}

// TestRecordReplicasOutsideRepointMu (FR-12): the new master records its
// replicas after the repoints, without repointMu held, so a hung agent's
// recordReplicas never holds back a quarantine or another repoint.
func TestRecordReplicasOutsideRepointMu(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	var held, calls atomic.Int32
	g.c.on("mysql.recordReplicas", func(json.RawMessage) (any, error) {
		calls.Add(1)
		if e.r.repointMu.TryLock() {
			e.r.repointMu.Unlock()
		} else {
			held.Add(1)
		}
		return proto.RecordReplicasResult{}, nil
	})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		AcceptUnreachable: []int64{g.d.id}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	if calls.Load() == 0 {
		t.Fatal("the new master never recorded its replicas")
	}
	if n := held.Load(); n != 0 {
		t.Fatalf("mysql.recordReplicas ran %d times with repointMu held", n)
	}
}
