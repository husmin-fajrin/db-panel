package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// The fake agent is a commands.Registry whose handlers are scripted per
// command, served to a real hub through hub.NewLocalConn, with a real
// jobs.Runner behind the job.* commands and the scripted agent jobs. Its
// events reach the Runner through Hub.OnEvent, as a real agent's do.

const (
	masterUUID = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	targetUUID = "4f22ab58-82db-22f2-af44-d91bb0530673"
	masterGTID = masterUUID + ":1-120"
	replPW     = "Q7w8E9r0T1y2U3i4O5p6A7s8"
)

// agentCommands are the commands the fake registers; an unscripted one
// fails the test when called.
var agentCommands = []string{
	"probe", "mysql.status", "mysql.connect", "mysql.inventory", "mysql.preflight", "mysql.configure",
	"mysql.restart", "mysql.createReplUser", "mysql.setReadOnly", "mysql.cloneFrom", "mysql.dumpFrom",
	"mysql.install", "mysql.configureReplication", "mysql.setReplicationFilter", "mysql.startReplica",
	"mysql.stopReplica", "mysql.resetReplica", "mysql.waitSynced", "mysql.startUntil", "mysql.dropDatabases",
}

type call struct {
	Cmd    string
	Params json.RawMessage
}

type handler func(params json.RawMessage) (any, error)

type fakeAgent struct {
	t    *testing.T
	e    *env
	id   int64
	name string
	reg  *commands.Registry
	jobs *jobs.Runner
	conn hub.Conn

	mu       sync.Mutex
	calls    []call
	script   map[string]handler
	attached bool
	started  []string // agent job ids, oldest first
}

func newFakeAgent(t *testing.T, e *env, id int64, name string) *fakeAgent {
	fa := &fakeAgent{t: t, e: e, id: id, name: name, reg: commands.NewRegistry(), script: map[string]handler{}}
	fa.jobs = jobs.NewRunner(fa, slog.New(slog.DiscardHandler))
	for _, cmd := range agentCommands {
		fa.reg.Register(cmd, func(ctx context.Context, params json.RawMessage) (any, error) {
			fa.record(cmd, params)
			fa.mu.Lock()
			h := fa.script[cmd]
			fa.mu.Unlock()
			if h == nil {
				t.Errorf("%s: unscripted command %s %s", name, cmd, params)
				return nil, fmt.Errorf("tidak diskrip: %s", cmd)
			}
			return h(params)
		})
	}
	fa.reg.Register("job.status", func(ctx context.Context, params json.RawMessage) (any, error) {
		fa.record("job.status", params)
		var p proto.JobIDParams
		json.Unmarshal(params, &p)
		st, ok := fa.jobs.Status(p.JobID)
		if !ok {
			return nil, &commands.ParamError{Msg: "job tidak dikenal"}
		}
		return st, nil
	})
	fa.reg.Register("job.cancel", func(ctx context.Context, params json.RawMessage) (any, error) {
		fa.record("job.cancel", params)
		var p proto.JobIDParams
		json.Unmarshal(params, &p)
		return map[string]bool{"canceled": fa.jobs.Cancel(p.JobID)}, nil
	})
	fa.conn = hub.NewLocalConn(fa.reg, func(ev proto.Event) { e.hub.OnEvent(id, ev) })
	return fa
}

func (fa *fakeAgent) record(cmd string, params json.RawMessage) {
	fa.mu.Lock()
	fa.calls = append(fa.calls, call{cmd, append(json.RawMessage(nil), params...)})
	fa.mu.Unlock()
}

// Emit is the agent's emitter: events reach the panel only while attached.
func (fa *fakeAgent) Emit(ev proto.Event) error {
	fa.mu.Lock()
	attached := fa.attached
	fa.mu.Unlock()
	if !attached {
		return proto.ErrOffline
	}
	fa.e.hub.OnEvent(fa.id, ev)
	return nil
}

func (fa *fakeAgent) attach() {
	fa.mu.Lock()
	fa.attached = true
	fa.mu.Unlock()
	fa.e.hub.Attach(fa.id, fa.conn)
}

func (fa *fakeAgent) detach() {
	fa.mu.Lock()
	fa.attached = false
	fa.mu.Unlock()
	fa.e.hub.Detach(fa.id, fa.conn)
}

// on scripts cmd.
func (fa *fakeAgent) on(cmd string, h handler) {
	fa.mu.Lock()
	fa.script[cmd] = h
	fa.mu.Unlock()
}

// reply scripts cmd to answer result.
func (fa *fakeAgent) reply(cmd string, result any) {
	fa.on(cmd, func(json.RawMessage) (any, error) { return result, nil })
}

// fail scripts cmd to fail with msg as the agent's error text.
func (fa *fakeAgent) fail(cmd, msg string) {
	fa.on(cmd, func(json.RawMessage) (any, error) { return nil, &commands.ParamError{Msg: msg} })
}

// job scripts cmd as an agent job running fn.
func (fa *fakeAgent) job(cmd string, fn func(j *jobs.Job, params json.RawMessage) (any, error)) {
	fa.on(cmd, func(params json.RawMessage) (any, error) {
		id, err := fa.jobs.Start(cmd, func(j *jobs.Job) (any, error) { return fn(j, params) })
		if err != nil {
			return nil, err
		}
		fa.mu.Lock()
		fa.started = append(fa.started, id)
		fa.mu.Unlock()
		return proto.StartedJob{JobID: id}, nil
	})
}

// blockingJob scripts cmd as an agent job that runs until it is canceled.
func (fa *fakeAgent) blockingJob(cmd string) {
	fa.job(cmd, func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Progress(10, "menyalin")
		<-j.Context().Done()
		return nil, j.Context().Err()
	})
}

// cmds is the command sequence the agent received, without job.status
// polls, from index from on.
func (fa *fakeAgent) cmds(from int) []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var out []string
	for _, c := range fa.calls[from:] {
		if c.Cmd != "job.status" {
			out = append(out, c.Cmd)
		}
	}
	return out
}

func (fa *fakeAgent) mark() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return len(fa.calls)
}

func (fa *fakeAgent) count(cmd string) int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	n := 0
	for _, c := range fa.calls {
		if c.Cmd == cmd {
			n++
		}
	}
	return n
}

// params unmarshals the params of the last cmd call into v.
func (fa *fakeAgent) params(cmd string, v any) {
	fa.t.Helper()
	fa.mu.Lock()
	defer fa.mu.Unlock()
	for i := len(fa.calls) - 1; i >= 0; i-- {
		if fa.calls[i].Cmd == cmd {
			if err := json.Unmarshal(fa.calls[i].Params, v); err != nil {
				fa.t.Fatalf("%s params: %v", cmd, err)
			}
			return
		}
	}
	fa.t.Fatalf("%s: %s never called; calls %v", fa.name, cmd, fa.cmds(0))
}

// allParams is the raw params of every cmd call.
func (fa *fakeAgent) allParams(cmd string) []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var out []string
	for _, c := range fa.calls {
		if c.Cmd == cmd {
			out = append(out, string(c.Params))
		}
	}
	return out
}

type published struct {
	Event string
	Data  any
}

type env struct {
	t     *testing.T
	st    *store.Store
	hub   *hub.Hub
	box   *secrets.Box
	r     *Runner
	pubMu sync.Mutex
	pubs  []published

	agents []*fakeAgent
}

// testListener hands agent events to the Runner, as the panel's listener does.
type testListener struct{ r atomic.Pointer[Runner] }

func (l *testListener) OnOnline(int64)  {}
func (l *testListener) OnOffline(int64) {}
func (l *testListener) OnEvent(id int64, ev proto.Event) {
	if r := l.r.Load(); r != nil {
		r.HandleEvent(id, ev)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "panel.key"))
	if err != nil {
		t.Fatal(err)
	}
	l := &testListener{}
	e := &env{t: t, st: st, hub: hub.New(l), box: box}
	e.r = New(Deps{Store: st, Hub: e.hub, Secrets: box, Publish: e.publish,
		Logger: slog.New(slog.DiscardHandler), Now: time.Now, PublicHost: "203.0.113.7"})
	e.r.offlineGrace = 300 * time.Millisecond
	e.r.pollEvery = 10 * time.Millisecond
	l.r.Store(e.r)
	t.Cleanup(func() {
		e.r.Close()
		for _, fa := range e.agents {
			fa.stopJobs()
		}
		st.Close()
	})
	return e
}

// startedJobs is how many agent jobs the agent started.
func (fa *fakeAgent) startedJobs() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return len(fa.started)
}

// lastJob is the id of the agent job started last.
func (fa *fakeAgent) lastJob() string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.started) == 0 {
		fa.t.Fatalf("%s started no agent job", fa.name)
	}
	return fa.started[len(fa.started)-1]
}

// stopJobs cancels the agent's jobs, so none outlives the test.
func (fa *fakeAgent) stopJobs() {
	fa.mu.Lock()
	ids := slices.Clone(fa.started)
	fa.mu.Unlock()
	for _, id := range ids {
		fa.jobs.Cancel(id)
	}
}

func (e *env) publish(event string, data any) {
	e.pubMu.Lock()
	e.pubs = append(e.pubs, published{event, data})
	e.pubMu.Unlock()
}

func (e *env) published(event string) int {
	e.pubMu.Lock()
	defer e.pubMu.Unlock()
	n := 0
	for _, p := range e.pubs {
		if p.Event == event {
			n++
		}
	}
	return n
}

// agent adds a paired server reachable at addr with a fake agent attached.
func (e *env) agent(name, addr string) *fakeAgent {
	e.t.Helper()
	srv, err := e.st.CreateServer(name)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.MarkPaired(srv.ID, "serial-"+name, addr); err != nil {
		e.t.Fatal(err)
	}
	fa := newFakeAgent(e.t, e, srv.ID, name)
	e.agents = append(e.agents, fa)
	fa.attach()
	return fa
}

func masterStatus() proto.MySQLStatus {
	return proto.MySQLStatus{Version: "8.4.3", Series: "8.4", ServerID: 1, ServerUUID: masterUUID, LogBin: true,
		GTIDMode: "ON", EnforceGTID: true, BinlogFormat: "ROW", GTIDExecuted: masterGTID, Port: 3306,
		BindAddress: "0.0.0.0"}
}

var masterDBs = []proto.DatabaseInfo{{Name: "a", Bytes: 100}, {Name: "b", Bytes: 200}}

// connectedMaster is a server whose MySQL is connected with every grant.
func (e *env) connectedMaster(name string) *fakeAgent {
	fa := e.agent(name, "10.0.0.1")
	if err := e.st.SetInventory(store.Inventory{ServerID: fa.id, Connected: true, Version: "8.4.3", Series: "8.4",
		Databases: masterDBs, UpdatedAt: time.Now()}); err != nil {
		e.t.Fatal(err)
	}
	fa.reply("mysql.status", masterStatus())
	return fa
}

// readyMaster is a prepared master: inventory MasterReady and a topology
// with a sealed replication password.
func (e *env) readyMaster(name string) (*fakeAgent, store.Topology) {
	e.t.Helper()
	fa := e.connectedMaster(name)
	inv, _ := e.st.GetInventory(fa.id)
	inv.MasterReady = true
	if err := e.st.SetInventory(inv); err != nil {
		e.t.Fatal(err)
	}
	enc, err := e.box.Seal([]byte(replPW))
	if err != nil {
		e.t.Fatal(err)
	}
	topo, err := e.st.CreateTopology(fa.id, "dbpanel_repl", enc, "10.0.0.1", 3306)
	if err != nil {
		e.t.Fatal(err)
	}
	return fa, topo
}

// replicaAgent is an empty target with MySQL installed whose preflight
// answers method, and whose every later step succeeds.
func (e *env) replicaAgent(name, method string) *fakeAgent {
	fa := e.agent(name, "10.0.0.2")
	fa.reply("probe", proto.ProbeResult{Hostname: name, MySQL: proto.MySQLInfo{Installed: true, Version: "8.4.3"}})
	fa.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckOK, Method: method,
		Checks: []proto.Check{{ID: "dedicated", Status: proto.CheckOK, Title: "Server replika khusus"}}})
	fa.on("mysql.configure", func(raw json.RawMessage) (any, error) {
		var p proto.ConfigureParams
		json.Unmarshal(raw, &p)
		return proto.ConfigureResult{NeedsRestart: true, ServerID: p.ServerID,
			RestartReasons: []string{"pengaturan replika berlaku setelah restart"}}, nil
	})
	fa.reply("mysql.restart", proto.RestartResult{TookMs: 5})
	fa.reply("mysql.setReadOnly", nil)
	snapshot := func(m string) func(j *jobs.Job, _ json.RawMessage) (any, error) {
		return func(j *jobs.Job, _ json.RawMessage) (any, error) {
			j.Logf("info", "%s dari master dimulai", m)
			j.Progress(50, "menyalin")
			return proto.SnapshotResult{Method: m, Bytes: 300, GTIDExecuted: masterGTID}, nil
		}
	}
	fa.job("mysql.cloneFrom", snapshot("clone"))
	fa.job("mysql.dumpFrom", snapshot("dump"))
	fa.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Series: "8.4", ServerID: 77, ServerUUID: targetUUID})
	fa.reply("mysql.configureReplication", proto.ReplicaStatus{IORunning: "No", SQLRunning: "No"})
	fa.reply("mysql.startReplica", proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"})
	fa.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: true, Executed: masterGTID,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	fa.reply("mysql.stopReplica", nil)
	fa.on("mysql.dropDatabases", func(raw json.RawMessage) (any, error) {
		var p proto.DropDatabasesParams
		json.Unmarshal(raw, &p)
		return proto.DropDatabasesResult{Dropped: p.Databases}, nil
	})
	return fa
}

// waitJob waits until the job reaches status and returns it with its steps.
func (e *env) waitJob(id, status string) (store.Job, []store.JobStep) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, steps, err := e.st.GetJob(id)
		if err != nil {
			e.t.Fatal(err)
		}
		if j.Status == status {
			return j, steps
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %s: status %s (step %d, error %q), want %s; steps %+v", id, j.Status, j.CurrentStep, j.Error, status, steps)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitFor polls cond for up to 10 s.
func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) jobLog(id string) string {
	e.t.Helper()
	lines, err := e.st.JobLog(id, 0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Message + "\n")
	}
	return b.String()
}

func stepStatuses(steps []store.JobStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Status
	}
	return out
}

func wantCmds(t *testing.T, who string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s commands:\n got %v\nwant %v", who, got, want)
	}
}
