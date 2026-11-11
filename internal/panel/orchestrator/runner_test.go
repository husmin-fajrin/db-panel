package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestJobLogRedacts: an agent error quoting row data reaches the job log,
// the step, jobs.error, the replica's last error and the published
// updates only censored; the panel's own quotes in a log line's format
// stay.
func TestJobLogRedacts(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")
	r.fail("mysql.dropDatabases", `Error 1062 (23000): Duplicate entry 'O\'Budi' for key 'nama' at end_log_pos 123456789`)

	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, true)
	if err != nil {
		t.Fatalf("StartRemoveDatabases: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	jr := &jobRun{r: e.r, job: j}
	jr.logf("warn", "Agent menjawab: %s; tekan \"Ulangi\".", "Duplicate entry 'a''Budi' for key")
	jr.logf("warn", "galat: %v", errors.New("Duplicate entry \"Budi\" for key"))

	log := e.jobLog(id)
	for _, leak := range []string{"Budi", `O\'`} {
		if strings.Contains(log, leak) {
			t.Errorf("job log holds %q:\n%s", leak, log)
		}
	}
	for _, kept := range []string{"Duplicate entry '…' for key '…' at end_log_pos 123456789", `tekan "Ulangi".`} {
		if !strings.Contains(log, kept) {
			t.Errorf("job log lost %q:\n%s", kept, log)
		}
	}
	if strings.Contains(j.Error, "Budi") || !strings.Contains(j.Error, "Duplicate entry '…'") {
		t.Errorf("jobs.error = %q", j.Error)
	}
	for _, s := range steps {
		if strings.Contains(s.Message, "Budi") {
			t.Errorf("step %d message = %q", s.Idx, s.Message)
		}
	}
	if rep := e.replica(r.id); strings.Contains(rep.LastError, "Budi") || !strings.Contains(rep.LastError, "'…'") {
		t.Errorf("replica last error = %q", rep.LastError)
	}
	e.pubMu.Lock()
	b, _ := json.Marshal(e.pubs)
	e.pubMu.Unlock()
	if strings.Contains(string(b), "Budi") {
		t.Errorf("published updates hold Budi: %s", b)
	}
}

// storedState is the job's jobState as stored.
func (e *env) storedState(id string) jobState {
	e.t.Helper()
	j, _, err := e.st.GetJob(id)
	if err != nil {
		e.t.Fatal(err)
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

// setState rewrites the stored jobState of a job no goroutine runs.
func (e *env) setState(id string, change func(*jobState)) {
	e.t.Helper()
	st := e.storedState(id)
	change(&st)
	b, err := json.Marshal(st)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.SetJobParams(id, string(b)); err != nil {
		e.t.Fatal(err)
	}
}

// waitIdle waits until no goroutine runs the job.
func (e *env) waitIdle(id string) {
	e.t.Helper()
	e.waitFor("job goroutine gone", func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return e.r.live[id] == nil
	})
}

func TestDropsData(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		st   jobState
		want bool
	}{
		{"resync", KindResyncReplica, jobState{Resync: true}, true},
		{"resync, nothing listed", KindResyncReplica, jobState{}, true},
		{"create, wipe list", KindCreateReplica, jobState{WipeList: []string{"a"}}, true},
		{"create, rebuild", KindCreateReplica, jobState{RebuildOldMaster: true}, true},
		{"create, leftover wait", KindCreateReplica, jobState{WaitReason: WaitWipe}, true},
		{"create, clone wait", KindCreateReplica, jobState{WaitReason: WaitClone}, true},
		{"create, foreign source wait", KindCreateReplica, jobState{WaitReason: WaitForeignSource}, false},
		{"create, reconnect wait", KindCreateReplica, jobState{WaitReason: WaitReconnect}, false},
		{"create, plain", KindCreateReplica, jobState{}, false},
		{"remove, drop", KindRemoveDatabases, jobState{DropData: true}, true},
		{"remove, keep", KindRemoveDatabases, jobState{}, false},
		{"add", KindAddDatabases, jobState{WipeList: []string{"a"}}, false},
		{"prepare master", KindPrepareMaster, jobState{WaitReason: WaitRestart}, false},
		{"certificate", KindMySQLTLS, jobState{}, false},
		{"tighten TLS", KindTightenTLS, jobState{}, false},
		{"promote planned", KindPromotePlanned, jobState{}, false},
		{"promote emergency", KindPromoteEmergency, jobState{}, false},
		{"leftover check", KindLeftover, jobState{}, false},
		{"salvage", KindSalvage, jobState{}, false},
	} {
		if got := dropsData(tc.kind, tc.st); got != tc.want {
			t.Errorf("%s: dropsData = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestApproveLeftoverNeedsToken: a create_replica waiting to drop the
// leftovers of an earlier attempt is not approved without a fresh
// confirmation; nothing reaches the target and the job keeps waiting.
func TestApproveLeftoverNeedsToken(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	scriptLeftovers(r, "dump", "a", "b")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobWaiting)
	e.waitIdle(id)
	if drops, err := e.r.DropsData(id); err != nil || !drops {
		t.Fatalf("DropsData = %v, %v; want true", drops, err)
	}
	mark := r.mark()
	if err := e.r.Approve(id, JobAck{}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Approve without confirmation = %v, want ErrJobNeedsConfirm", err)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobWaiting || j.CurrentStep != 1 {
		t.Fatalf("job after a refused approve = %+v", j)
	}
	if st := e.storedState(id); !st.ConfirmedAt.IsZero() {
		t.Fatalf("refused approve stored ConfirmedAt %v", st.ConfirmedAt)
	}
	wantCmds(t, "replica", r.cmds(mark), nil)

	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	var drop proto.DropDatabasesParams
	r.params("mysql.dropDatabases", &drop)
	if !slices.Equal(drop.Databases, []string{"a", "b"}) {
		t.Fatalf("dropDatabases = %v", drop.Databases)
	}
}

// TestApproveRefreshesConfirmedAt: a confirmed Approve stores when, before
// the job runs on; an old confirmation does not stop an Approve (it is a
// fresh one).
func TestApproveRefreshesConfirmedAt(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	scriptLeftovers(r, "dump", "a")
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobWaiting)
	e.waitIdle(id)
	old := time.Now().Add(-30 * time.Hour)
	e.setState(id, func(st *jobState) { st.ConfirmedAt = old })
	// The first step to run after the approval sees the new time stored.
	var seen time.Time
	r.on("probe", func(json.RawMessage) (any, error) {
		if seen.IsZero() {
			seen = e.storedState(id).ConfirmedAt
		}
		return proto.ProbeResult{Hostname: "replica1", MySQL: proto.MySQLInfo{Installed: true, Version: "8.4.3"}}, nil
	})
	before := time.Now()
	if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	e.waitJob(id, store.JobSucceeded)
	if seen.Before(before) || seen.After(after) {
		t.Fatalf("ConfirmedAt when the step ran = %v, want between %v and %v", seen, before, after)
	}
	if got := e.storedState(id).ConfirmedAt; !got.Equal(seen) {
		t.Fatalf("ConfirmedAt after the job = %v, want %v", got, seen)
	}
}

// TestPromoteRetryStillTokenless: a promote drops nothing; its Retry needs
// no confirmation (spec 5.6).
func TestPromoteRetryStillTokenless(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitFor("unlockPromote", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	if drops, err := e.r.DropsData(id); err != nil || drops {
		t.Fatalf("DropsData = %v, %v; want false", drops, err)
	}
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry of a promote without confirmation: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	if n := g.m.count("mysql.lockForPromote"); n != 2 {
		t.Fatalf("lockForPromote sent %d times, want 2 (the retry ran)", n)
	}
}

// TestTightenTLSRetryTokenless: tightening TLS drops nothing; its Retry
// needs no confirmation and no 24-hour limit applies.
func TestTightenTLSRetryTokenless(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	const id = "dddddddddddddddddddddddddddddddd"
	b, _ := json.Marshal(jobState{})
	if err := e.st.CreateJob(store.Job{ID: id, Kind: KindTightenTLS, ServerID: m.id, TopologyID: topo.ID,
		ParamsJSON: string(b), CreatedAt: time.Now().Add(-72 * time.Hour)}, tightenStepNames(nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, 0, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.fail("mysql.tlsRequest", "gagal lagi")
	if drops, err := e.r.DropsData(id); err != nil || drops {
		t.Fatalf("DropsData = %v, %v; want false", drops, err)
	}
	mark := m.mark()
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry of tighten TLS without confirmation: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	if len(m.cmds(mark)) == 0 {
		t.Fatal("the retry sent nothing to the master")
	}
	if st := e.storedState(id); !st.ConfirmedAt.IsZero() {
		t.Fatalf("tokenless retry stored ConfirmedAt %v", st.ConfirmedAt)
	}
}

// TestRetryRebuildRechecksConnections (decision 13): a Retry of the old
// master's rebuild reads its connections again; the acknowledgement given
// when the job started does not count, only the Retry's own.
func TestRetryRebuildRechecksConnections(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	pid, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(pid, store.JobSucceeded)
	if err := e.st.SetCutoverPending(g.topo.ID, false); err != nil {
		t.Fatal(err)
	}
	row := proto.ConnectionRow{Host: "10.0.0.9", User: "app", DB: "simrs", Count: 2, Kind: "sql"}
	g.m.reply("mysql.connections", proto.ConnectionsResult{Rows: []proto.ConnectionRow{row}, Typed: true, TakenAt: time.Now()})
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	g.c.reply("mysql.status", masterStatus())
	g.c.reply("mysql.createDumpUser", nil)
	g.c.reply("mysql.dropUser", nil)
	g.c.reply("mysql.sweepDumpUsers", proto.SweepDumpUsersResult{})
	id, err := e.r.StartCreateReplica(ctx, CreateReplicaParams{MasterServerID: g.c.id, TargetServerID: g.m.id,
		RebuildOldMaster: true, ConfirmWipe: true, AcceptConnections: true})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	if st := e.storedState(id); !st.AcceptConnections {
		t.Fatal("the start's acknowledgement was not stored")
	}

	reads := g.m.count("mysql.connections")
	mark := g.m.mark()
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrConnectionsRemain) {
		t.Fatalf("Retry without acknowledging the connections = %v, want ErrConnectionsRemain", err)
	}
	if n := g.m.count("mysql.connections"); n != reads+1 {
		t.Fatalf("connections read %d times by the retry, want 1", n-reads)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %+v", j)
	}
	for _, c := range g.m.cmds(mark) {
		if c != "mysql.connections" {
			t.Fatalf("refused retry sent %s to the old master", c)
		}
	}
	// An unreadable list is not clear either.
	g.m.fail("mysql.connections", "login admin gagal")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); !errors.Is(err, ErrConnectionsRemain) {
		t.Fatalf("Retry with unreadable connections = %v, want ErrConnectionsRemain", err)
	}
	// Without the token the connections are not even read.
	reads = g.m.count("mysql.connections")
	if err := e.r.Retry(id, JobAck{AcceptConnections: true}); !errors.Is(err, ErrJobNeedsConfirm) {
		t.Fatalf("Retry without confirmation = %v, want ErrJobNeedsConfirm", err)
	}
	if n := g.m.count("mysql.connections"); n != reads {
		t.Fatalf("an unconfirmed retry read the connections")
	}

	scriptReplicaTarget(g.m, "db-master", "clone")
	if err := e.r.Retry(id, JobAck{Confirmed: true, AcceptConnections: true}); err != nil {
		t.Fatalf("acknowledged retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
}

// A remove_databases that drops the data is confirmed when it is created
// (its Retry counts 24 hours from then); one that keeps the data is not.
func TestRemoveDatabasesDropConfirmedAtStart(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")
	r.reply("mysql.dropDatabases", proto.DropDatabasesResult{Dropped: []string{"b"}})
	before := time.Now()
	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.storedState(id).ConfirmedAt; got.Before(before) || got.After(time.Now()) {
		t.Fatalf("ConfirmedAt = %v, want the start time", got)
	}
	e.waitJob(id, store.JobSucceeded)
	keep, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"a"}, false)
	if err == nil {
		if got := e.storedState(keep).ConfirmedAt; !got.IsZero() {
			t.Fatalf("ConfirmedAt of a remove keeping the data = %v", got)
		}
		e.waitJob(keep, store.JobSucceeded)
	}
}
