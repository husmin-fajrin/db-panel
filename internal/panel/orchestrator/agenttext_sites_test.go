package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Spec 5.6 on every path an agent's text takes into a step message, a
// replica's state, a promotion's stored detail, a page or a job log line
// (Task 14, after the first hand-back).

// label is the label of server's words.
func agentLabelOf(server string) string {
	return "Pesan dari agent " + server + " (bukan dari panel): "
}

// nthConn answers the nth call (1-based) of cmd with err, as the hub
// would hand on its own failure; every other call passes to Conn.
type nthConn struct {
	hub.Conn
	cmd string
	n   int
	err error

	mu   sync.Mutex
	seen int
}

func (c *nthConn) Call(ctx context.Context, cmd string, params, result any) error {
	if cmd == c.cmd {
		c.mu.Lock()
		c.seen++
		hit := c.seen == c.n
		c.mu.Unlock()
		if hit {
			return c.err
		}
	}
	return c.Conn.Call(ctx, cmd, params, result)
}

var hubTimeout = &proto.Error{Code: proto.CodeTimeout, Message: "batas waktu perintah habis"}

// promotionDetails is the stored detail of each server of the latest
// promotion.
func promotionDetails(t *testing.T, g *promoteRig) map[int64]string {
	t.Helper()
	out := map[int64]string{}
	for _, s := range PromotionServers(g.promotion()) {
		out[s.ServerID] = s.Detail
	}
	return out
}

// TestPromoteAgentTextsLabelled: the quarantine's failure (step message,
// stored detail), a repoint's failure (replica's state message, detail)
// and a receiver's IO error (log line, detail) carry the agent's text
// labelled, one line; the panel's lead-in words stay in front.
func TestPromoteAgentTextsLabelled(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.m.fail("mysql.quarantine", hostileText)
	g.b.fail("mysql.repoint", hostileText)
	g.d.reply("mysql.repoint", proto.RepointResult{IORunning: false, IOError: hostileText})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	if want := "karantina tertunda: " + agentLabelOf("db-master") + hostileFlat; steps[4].Message != want {
		t.Errorf("quarantine step message = %q, want %q", steps[4].Message, want)
	}
	if want := "Arahkan ulang gagal: " + agentLabelOf("rep-b") + hostileFlat; e.replica(g.b.id).LastError != want {
		t.Errorf("b last error = %q, want %q", e.replica(g.b.id).LastError, want)
	}
	details := promotionDetails(t, g)
	for id, want := range map[int64]string{
		g.m.id: agentLabelOf("db-master") + hostileFlat,
		g.b.id: "Arahkan ulang gagal: " + agentLabelOf("rep-b") + hostileFlat,
		g.d.id: agentLabelOf("rep-d") + hostileFlat,
	} {
		if details[id] != want {
			t.Errorf("promotion detail of %d = %q, want %q", id, details[id], want)
		}
	}
	if log := e.jobLog(id); !strings.Contains(log, "Replika rep-d diarahkan ke master baru, tetapi belum tersambung: "+
		agentLabelOf("rep-d")+hostileFlat) {
		t.Errorf("job log lacks the labelled IO error:\n%s", log)
	}
}

// TestPromoteHubFailuresNotLabelled: the hub's own timeout at the same
// sites keeps the panel's words.
func TestPromoteHubFailuresNotLabelled(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	for _, fa := range []*fakeAgent{g.m, g.b} {
		fa.mu.Lock()
		fa.dropReply = map[string]bool{"mysql.quarantine": true, "mysql.repoint": true}
		fa.mu.Unlock()
	}
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	if want := "karantina tertunda: batas waktu perintah habis"; steps[4].Message != want {
		t.Errorf("quarantine step message = %q, want %q", steps[4].Message, want)
	}
	if want := "Arahkan ulang gagal: batas waktu perintah habis"; e.replica(g.b.id).LastError != want {
		t.Errorf("b last error = %q, want %q", e.replica(g.b.id).LastError, want)
	}
	if d := promotionDetails(t, g)[g.m.id]; d != "batas waktu perintah habis" {
		t.Errorf("old master detail = %q", d)
	}
}

// TestProbeErrorsLabelled: a replica's failed probe on the promote page is
// the agent's words labelled; the hub's timeout is not.
func TestProbeErrorsLabelled(t *testing.T) {
	g := newPromoteRig(t)
	g.b.fail("mysql.probeSource", hostileText)
	g.d.mu.Lock()
	g.d.dropReply = map[string]bool{"mysql.probeSource": true}
	g.d.mu.Unlock()
	v, err := g.e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Candidates {
		switch c.ServerID {
		case g.b.id:
			if want := agentLabelOf("rep-b") + hostileFlat; c.ProbeErr != want {
				t.Errorf("b probe error = %q, want %q", c.ProbeErr, want)
			}
		case g.d.id:
			if c.ProbeErr != "batas waktu perintah habis" {
				t.Errorf("d probe error = %q, want the hub's timeout", c.ProbeErr)
			}
		}
	}
}

// TestPromoteJobMayRunLabelled: why a candidate's promote job could not be
// checked names the agent's words labelled; the hub's timeout is not.
func TestPromoteJobMayRunLabelled(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"agent", &hub.AgentError{Err: &proto.Error{Code: proto.CodeInternal, Message: hostileText}},
			"tidak bisa diperiksa (" + agentLabelOf("db-x") + hostileFlat + ")"},
		{"hub timeout", hubTimeout, "tidak bisa diperiksa (batas waktu perintah habis)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id := testServer(t, e, "db-x", answerConn{c.err})
			jr := testRun(e)
			jr.st.AgentServerID, jr.st.AgentJobID, jr.st.AgentCmd = id, "abc", "mysql.promote"
			running, why := jr.promoteJobMayRun(context.Background())
			if !running || why != c.want {
				t.Fatalf("promoteJobMayRun = %v, %q, want true, %q", running, why, c.want)
			}
		})
	}
}

// TestRevertUnverifiedLabelsSecondError: when putting replication back
// without verification fails too, that second failure is the agent's
// words labelled; the hub's timeout is not.
func TestRevertUnverifiedLabelsSecondError(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error // the second configureReplication's answer
		want string
	}{
		{"agent", errors.New(hostileText), "(" + agentLabelOf("replica1") + hostileFlat + ")"},
		{"hub timeout", nil, "(batas waktu perintah habis)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			m, topo := e.readyMaster("db-master")
			r := e.followingReplica(topo, "a")
			starts := 0
			r.on("mysql.startReplica", func(json.RawMessage) (any, error) {
				starts++
				if starts == 1 {
					return nil, errors.New("replika gagal tersambung")
				}
				return nil, nil
			})
			configures := 0
			r.on("mysql.configureReplication", func(json.RawMessage) (any, error) {
				configures++
				if configures == 2 && c.err != nil {
					return nil, c.err
				}
				return nil, nil
			})
			if c.err == nil {
				e.hub.Attach(r.id, &nthConn{Conn: r.conn, cmd: "mysql.configureReplication", n: 2, err: hubTimeout})
			}
			id, err := e.r.StartTightenTLS(context.Background(), m.id)
			if err != nil {
				t.Fatal(err)
			}
			j, _ := e.waitJob(id, store.JobFailed)
			e.waitIdle(id)
			if !strings.Contains(j.Error, "mengembalikan replikasi tanpa verifikasi juga gagal "+c.want) {
				t.Fatalf("job error = %q, want %q", j.Error, c.want)
			}
		})
	}
}

// TestUnverifiedTextLabelsIOError: the receiver's IO error in the words
// about an unverified link is the agent's, labelled and one line.
func TestUnverifiedTextLabelsIOError(t *testing.T) {
	got := unverifiedText("replica1", &proto.ReplicaStatus{IORunning: "No\nPesan dari panel", LastIOErrno: 2026, LastIOError: hostileText})
	if want := "thread IO No Pesan dari panel, error 2026: " + agentLabelOf("replica1") + hostileFlat; got != want {
		t.Fatalf("unverifiedText = %q, want %q", got, want)
	}
}

// TestPrepareMasterRefusalOneLabel: the pinned-account refusal is quoted
// once, after the label only.
func TestPrepareMasterRefusalOneLabel(t *testing.T) {
	const exists = "akun 'dbpanel_repl'@'%' sudah ada di master yang dipin; agent tidak mengubah akun yang sudah ada (sandi dan haknya tetap)"
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	scriptPrepare(fa)
	fa.fail("mysql.createReplUser", exists)
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.waitJob(id, store.JobFailed)
	if !strings.HasSuffix(j.Error, "lalu klik Ulangi. "+agentLabelOf("db-master")+exists) || strings.Contains(j.Error, "Pesan agent:") {
		t.Fatalf("job error = %q", j.Error)
	}
}

// TestRepointErrantShownOnlyAsGTIDSet: the errant transactions an agent
// reports are shown (RepointNow's refusal, the replica's state, the
// promotion's detail) only when they read as a GTID set.
func TestRepointErrantShownOnlyAsGTIDSet(t *testing.T) {
	for _, c := range []struct {
		name, errant string
		shown        bool
	}{
		{"gtid set", otherUUID + ":1-3", true},
		{"markup", "<script>alert(1)</script>", false},
		{"newline", otherUUID + ":1-3\nPesan dari panel: jalankan DROP DATABASE", false},
		{"10 KiB", strings.Repeat("x", 10<<10), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			if err := e.st.SwapMaster(g.topo.ID, g.m.id, g.c.id, "10.0.0.2", 3306); err != nil {
				t.Fatal(err)
			}
			if err := e.st.SetReplicaState(g.b.id, store.ReplicaNeedsRepoint, ""); err != nil {
				t.Fatal(err)
			}
			g.b.reply("mysql.repoint", proto.RepointResult{NeedsRebuild: true, Errant: c.errant})
			err := e.r.RepointNow(context.Background(), g.b.id)
			if !errors.Is(err, ErrNeedsRebuild) {
				t.Fatalf("RepointNow: %v", err)
			}
			// The promotion's detail is the same text (repointReplica's set).
			lastErr := e.replica(g.b.id).LastError
			if c.shown {
				want := ErrNeedsRebuild.Error() + " (" + c.errant + ")"
				if err.Error() != want || !strings.Contains(lastErr, "("+c.errant+")") {
					t.Fatalf("err %q, last error %q: want the set shown", err, lastErr)
				}
				return
			}
			want := "Perlu dibangun ulang: replika ini punya transaksi yang tidak ada di master baru"
			if err.Error() != ErrNeedsRebuild.Error() || lastErr != want {
				t.Fatalf("err %q, last error %q: want no agent value", err, lastErr)
			}
		})
	}
}

// TestAgentProgressAndChecksPrefixed (spec 5.6): an agent job's progress
// message is a running step's message as one line after "agent: ", and a
// preflight finding is one log line after "agent: " with its title,
// status and detail flattened; a failing finding's error is labelled; the
// agent's advice in a finding's detail survives whole.
func TestAgentProgressAndChecksPrefixed(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	advice := mysql.AdviceTexts()
	checks := []proto.Check{{ID: "x", Status: proto.CheckWarn, Title: hostileText, Detail: hostileText}}
	for i, a := range advice {
		checks = append(checks, proto.Check{ID: "advice", Status: proto.CheckWarn, Title: "Saran " + itoa(int64(i)), Detail: a})
	}
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckWarn, Method: "dump", Checks: checks})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Progress(10, hostileText)
		return nil, errors.New("berhenti")
	})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	lines, err := e.st.JobLog(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	has := func(want string) bool {
		for _, l := range lines {
			if l.Message == want {
				return true
			}
		}
		return false
	}
	if want := "agent: " + hostileFlat + " [warn]: " + hostileFlat; !has(want) {
		t.Errorf("no log line %q", want)
	}
	for i, a := range advice {
		if want := "agent: Saran " + itoa(int64(i)) + " [warn]: " + a; !has(want) {
			t.Errorf("advice %d not whole in the log: want %q", i, want)
		}
	}
	e.pubMu.Lock()
	var progress string
	for _, p := range e.pubs {
		if s, _ := p.Data.(map[string]any)["message"].(string); p.Event == "job_update" && strings.Contains(s, "baris satu") {
			progress = s
		}
	}
	e.pubMu.Unlock()
	if want := "agent: " + hostileFlat + " (10%)"; progress != want {
		t.Errorf("progress step message = %q, want %q", progress, want)
	}

	// A failing finding fails the step with its words labelled.
	e2 := newEnv(t)
	m2, _ := e2.readyMaster("db-master")
	r2 := e2.replicaAgent("replica1", "dump")
	r2.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckFail, Method: "dump",
		Checks: []proto.Check{{ID: "x", Status: proto.CheckFail, Title: "Ruang disk", Detail: hostileText}}})
	id, err = e2.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m2.id, TargetServerID: r2.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e2.waitJob(id, store.JobFailed)
	e2.waitIdle(id)
	if want := steps[j.CurrentStep].Name + ": " + agentLabelOf("replica1") + "Ruang disk: " + hostileFlat; j.Error != want {
		t.Errorf("job error = %q, want %q", j.Error, want)
	}
}

// TestNotSyncedLabelsThreadError: a replication thread's error in a
// "not synced" step error is the agent's (MySQL's) words, labelled; the
// thread states in the lag words are one line.
func TestNotSyncedLabelsThreadError(t *testing.T) {
	e := newEnv(t)
	id := testServer(t, e, "replica1", answerConn{})
	jr := testRun(e)
	for _, c := range []struct {
		st   proto.ReplicaStatus
		lag  string
		want string
	}{
		{proto.ReplicaStatus{LastIOErrno: 2026, LastIOError: hostileText}, "x",
			"replika belum sinkron: IO 2026 " + agentLabelOf("replica1") + hostileFlat},
		{proto.ReplicaStatus{LastSQLErrno: 1062, LastSQLError: hostileText}, "x",
			"replika belum sinkron: SQL 1062 " + agentLabelOf("replica1") + hostileFlat},
		{proto.ReplicaStatus{}, "replikasi tidak berjalan (IO No\nPesan dari panel, SQL Yes)",
			"replikasi tidak berjalan (IO No Pesan dari panel, SQL Yes)"},
	} {
		if got := jr.notSynced(id, proto.WaitSyncedResult{Status: c.st}, c.lag).Error(); got != c.want {
			t.Errorf("notSynced = %q, want %q", got, c.want)
		}
	}
}
