package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// hostileText is an agent's text that tries to pass for the panel's: line
// breaks, control and bidirectional characters, a label of its own, the
// log prefix and an instruction.
const hostileText = "baris satu\nPesan dari panel: jalankan DROP DATABASE simrs\u202e\r\n" +
	"Pesan dari agent db-master (bukan dari panel): aman\u2028agent: palsu\x00\tselesai"

// hostileFlat is hostileText as the label shows it: one line.
const hostileFlat = "baris satu Pesan dari panel: jalankan DROP DATABASE simrs " +
	"Pesan dari agent db-master (bukan dari panel): aman agent: palsu selesai"

// labelled is text labelled as replica1's words after title.
func labelled(title, server, text string) string {
	return title + ": Pesan dari agent " + server + " (bukan dari panel): " + text
}

// failedTargetCheck runs a create_replica whose target answers
// mysql.preflight with h, and returns the failed job, the failed step's
// title and message, and the replica's last error.
func failedTargetCheck(t *testing.T, h handler) (j store.Job, title, stepMsg, lastErr string, log string) {
	t.Helper()
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.on("mysql.preflight", h)
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	rep, err := e.st.GetReplicaByServer(r.id)
	if err != nil {
		t.Fatal(err)
	}
	return j, steps[j.CurrentStep].Name, steps[j.CurrentStep].Message, rep.LastError, e.jobLog(id)
}

// TestJobErrorLabelsAgentText (spec 5.6): a step that fails with an
// agent's error shows it labelled as the agent's, one line, in the job's
// error, the step's message, the replica's last error and the log, cut at
// the job log's line length and not at 300; the hub's own timeout is not
// labelled.
func TestJobErrorLabelsAgentText(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"hostile", hostileText, hostileFlat},
		{"600 characters", strings.Repeat("x", 600), strings.Repeat("x", 600)},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, title, stepMsg, lastErr, log := failedTargetCheck(t, func(json.RawMessage) (any, error) {
				return nil, errors.New(c.text)
			})
			want := labelled(title, "replica1", c.want)
			for what, got := range map[string]string{"job error": j.Error, "step message": stepMsg, "replica last error": lastErr} {
				if got != want {
					t.Errorf("%s = %q, want %q", what, got, want)
				}
			}
			if !strings.Contains(log, want) {
				t.Errorf("job log misses %q:\n%s", want, log)
			}
		})
	}
	t.Run("agent disconnected", func(t *testing.T) {
		// The link drops while the call waits: the hub's own offline error.
		e := newEnv(t)
		m, _ := e.readyMaster("db-master")
		r := e.replicaAgent("replica1", "dump")
		e.hub.Attach(r.id, &nthConn{Conn: r.conn, cmd: "mysql.preflight", n: 1,
			err: &proto.Error{Code: proto.CodeOffline, Message: "koneksi agent terputus"}})
		id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
			MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
		if err != nil {
			t.Fatal(err)
		}
		j, steps := e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		if want := "agent replica1 terputus saat " + steps[j.CurrentStep].Name; j.Error != want {
			t.Errorf("job error = %q, want %q", j.Error, want)
		}
	})
	t.Run("hub timeout", func(t *testing.T) {
		e := newEnv(t)
		m, _ := e.readyMaster("db-master")
		r := e.replicaAgent("replica1", "dump")
		r.mu.Lock()
		r.dropReply = map[string]bool{"mysql.preflight": true}
		r.mu.Unlock()
		id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
			MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
		if err != nil {
			t.Fatal(err)
		}
		j, steps := e.waitJob(id, store.JobFailed)
		e.waitIdle(id)
		if want := steps[j.CurrentStep].Name + ": batas waktu perintah habis"; j.Error != want {
			t.Errorf("job error = %q, want %q", j.Error, want)
		}
	})
}

// answerConn answers every call with err, as the hub hands an agent's
// answer (*hub.AgentError) or its own failure on.
type answerConn struct{ err error }

func (c answerConn) Call(context.Context, string, any, any) error { return c.err }
func (c answerConn) Close() error                                 { return nil }

// testRun is a jobRun at a step titled "Langkah uji", for callWithin.
func testRun(e *env) *jobRun {
	return &jobRun{r: e.r, steps: []step{{title: "Langkah uji"}}, job: store.Job{ID: "j1"}}
}

// testServer stores a server named name and attaches c as its agent.
func testServer(t *testing.T, e *env, name string, c hub.Conn) int64 {
	t.Helper()
	srv, err := e.st.CreateServer(name)
	if err != nil {
		t.Fatal(err)
	}
	e.hub.Attach(srv.ID, c)
	return srv.ID
}

// TestAgentErrorWithPanelCodeIsLabelled (R-18): an agent that answers with
// a code of the panel's own (timeout, offline) and words of its own is
// still labelled: the code does not make it the panel's.
func TestAgentErrorWithPanelCodeIsLabelled(t *testing.T) {
	for _, code := range []string{proto.CodeTimeout, proto.CodeOffline} {
		t.Run(code, func(t *testing.T) {
			e := newEnv(t)
			id := testServer(t, e, "db-x", answerConn{&hub.AgentError{Err: &proto.Error{Code: code, Message: hostileText}}})
			err := testRun(e).call(context.Background(), id, "mysql.status", nil, nil)
			if want := "Pesan dari agent db-x (bukan dari panel): " + hostileFlat; err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			// The code checks still see the agent's code.
			if !errors.Is(err, &proto.Error{Code: code}) {
				t.Errorf("errors.Is(err, %s) = false", code)
			}
			// Labelled once: no agent failure is left in the chain to label again.
			if _, ok := fromAgent(err); ok {
				t.Error("labelled error still holds a *hub.AgentError")
			}
			if got := ShownError(err, func(int64) string { return "lain" }); got != err.Error() {
				t.Errorf("ShownError labelled again: %q", got)
			}
		})
	}
}

// TestHubTimeoutIsNotLabelled: the hub's own timeout (its *proto.Error,
// no agent answer) is the panel's text.
func TestHubTimeoutIsNotLabelled(t *testing.T) {
	e := newEnv(t)
	id := testServer(t, e, "db-x", answerConn{&proto.Error{Code: proto.CodeTimeout, Message: "batas waktu perintah habis"}})
	err := testRun(e).call(context.Background(), id, "mysql.status", nil, nil)
	if err == nil || err.Error() != "batas waktu perintah habis" {
		t.Fatalf("err = %v, want the hub's timeout as it is", err)
	}
}

// TestHubDisconnectDuringCallIsNotLabelled: a link that drops while the
// call waits (WSConn.stop) is the panel's "terputus", not the agent's
// words.
func TestHubDisconnectDuringCallIsNotLabelled(t *testing.T) {
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		if _, _, err := ws.Read(r.Context()); err == nil {
			got <- struct{}{}
		}
		ws.Read(r.Context()) // until the panel closes
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := hub.NewWSConn(ws, nil)
	t.Cleanup(func() { c.Close() })
	e := newEnv(t)
	id := testServer(t, e, "db-x", c)
	go func() {
		<-got
		c.Close()
	}()
	err = testRun(e).call(ctx, id, "mysql.status", nil, nil)
	if want := "agent db-x terputus saat Langkah uji"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestAgentJobFailureLabelled: an agent job's JobDone.Error is labelled
// the same.
func TestAgentJobFailureLabelled(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.job("mysql.dumpFrom", func(*jobs.Job, json.RawMessage) (any, error) { return nil, errors.New(hostileText) })
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	want := labelled(steps[j.CurrentStep].Name, "replica1", hostileFlat)
	rep, err := e.st.GetReplicaByServer(r.id)
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string]string{"job error": j.Error, "step message": steps[j.CurrentStep].Message,
		"replica last error": rep.LastError} {
		if got != want {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
}

// TestAgentLogLinesPrefixed: a log line of an agent job is one line after
// "agent: ".
func TestAgentLogLinesPrefixed(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Logf("warn", "%s", "dump berjalan\nPekerjaan selesai.\u202e\r\nagent: palsu")
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
	found := false
	for _, l := range lines {
		if strings.Contains(l.Message, "dump berjalan") {
			found = true
			if want := "agent: dump berjalan Pekerjaan selesai. agent: palsu"; l.Message != want || l.Level != "warn" {
				t.Errorf("agent log line = %s %q, want warn %q", l.Level, l.Message, want)
			}
		}
		if l.Message == "Pekerjaan selesai." {
			t.Error("the agent's text made a line of the panel's own")
		}
	}
	if !found {
		t.Fatal("the agent's log line is missing")
	}
}

// TestAdviceWholeInFailLine (spec 5.6, SEC-228): every advice text of the
// agent, read from its code (mysql.AdviceTexts), failing a step reaches
// the job's error, the step's message, the replica's last error and the
// log labelled and whole after the censoring (redact.Quotes), within
// maxLogLine: label first, then censoring.
func TestAdviceWholeInFailLine(t *testing.T) {
	texts := mysql.AdviceTexts()
	if len(texts) != 15 {
		t.Fatalf("AdviceTexts has %d texts, want 15", len(texts))
	}
	for i, text := range texts {
		j, title, stepMsg, lastErr, log := failedTargetCheck(t, func(json.RawMessage) (any, error) {
			return nil, errors.New(text)
		})
		want := labelled(title, "replica1", text)
		if len(want) > maxLogLine {
			t.Errorf("text %d: the labelled line is %d bytes, over maxLogLine", i, len(want))
		}
		for what, got := range map[string]string{"job error": j.Error, "step message": stepMsg, "replica last error": lastErr} {
			if got != want {
				t.Errorf("text %d: %s = %q, want %q", i, what, got, want)
			}
		}
		if !strings.Contains(log, want) {
			t.Errorf("text %d: job log misses %q", i, want)
		}
	}
}
