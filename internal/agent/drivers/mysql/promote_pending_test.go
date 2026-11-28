package mysql_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// pendingSource is the old master the requests name and the instance's
// replication source; nothing listens there, replication is never
// started, and the emergency probes of it are replaced by the tests.
const pendingSource = "127.0.0.1:3399"

// heldEvents is a job emitter whose job.log events wait while hold is
// set, so a test can keep a job running at its first log line.
type heldEvents struct {
	mu   sync.Mutex
	hold chan struct{} // closed to let the waiting events go
	held chan struct{} // one value each time an event starts waiting
	done chan proto.JobDone
}

func newHeldEvents() *heldEvents {
	return &heldEvents{held: make(chan struct{}, 16), done: make(chan proto.JobDone, 4)}
}

func (e *heldEvents) Emit(ev proto.Event) error {
	switch ev.Type {
	case proto.EventJobLog:
		e.mu.Lock()
		h := e.hold
		e.mu.Unlock()
		if h != nil {
			e.held <- struct{}{}
			<-h
		}
	case proto.EventJobDone:
		var d proto.JobDone
		_ = json.Unmarshal(ev.Payload, &d)
		e.done <- d
	}
	return nil
}

// holdLogs makes job.log events wait until the returned func is called.
func (e *heldEvents) holdLogs() (release func()) {
	h := make(chan struct{})
	e.mu.Lock()
	e.hold = h
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		e.hold = nil
		e.mu.Unlock()
		close(h)
	}
}

// pendingRig is one read-only MySQL and a driver pinned by role and
// source, with a job runner, answering requests as the agent's session
// does: NoteRead when it reads one, then Dispatch, then NoteAnswered.
type pendingRig struct {
	t      *testing.T
	inst   *testmysql.Instance
	d      *mysql.Driver
	reg    *commands.Registry
	runner *jobs.Runner
	ev     *heldEvents
}

func newPendingInstance(t *testing.T) *testmysql.Instance {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "CHANGE REPLICATION SOURCE TO SOURCE_HOST = '127.0.0.1', SOURCE_PORT = 3399, "+
		"SOURCE_USER = 'dbpanel_repl', SOURCE_AUTO_POSITION = 1",
		"SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	return inst
}

func newPendingRig(t *testing.T, inst *testmysql.Instance, dir string) *pendingRig {
	t.Helper()
	r := &pendingRig{t: t, inst: inst, ev: newHeldEvents()}
	r.runner = jobs.NewRunner(r.ev, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.d = &mysql.Driver{Host: inst.Host(), PinDir: dir, Jobs: r.runner}
	r.reg = commands.NewRegistry()
	r.runner.Register(r.reg)
	r.d.Register(r.reg)
	return r
}

// params is an emergency promote from pendingSource.
func (r *pendingRig) params() proto.PromoteParams {
	return proto.PromoteParams{Mode: proto.PromoteEmergency, OldSource: proto.PeerCred{Host: "127.0.0.1", Port: 3399,
		User: "dbpanel_repl", Password: replPassword}, ReplPassword: newReplPassword}
}

// send handles one request as the session does, answering in the
// background: the read is noted before send returns.
func (r *pendingRig) send(cmd string, params json.RawMessage) <-chan proto.Response {
	r.reg.NoteRead(cmd)
	out := make(chan proto.Response, 1)
	go func() {
		resp := r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: params})
		r.reg.NoteAnswered(cmd, resp.OK)
		out <- resp
	}()
	return out
}

func (r *pendingRig) request(cmd string, params json.RawMessage) proto.Response {
	r.t.Helper()
	select {
	case resp := <-r.send(cmd, params):
		return resp
	case <-time.After(time.Minute):
		r.t.Fatalf("%s not answered", cmd)
	}
	return proto.Response{}
}

// pending is mysql.status's promote_request.
func (r *pendingRig) pending() bool {
	r.t.Helper()
	st := decode[proto.MySQLStatus](r.t, r.request("mysql.status", nil))
	if st.PromoteRequest == nil {
		r.t.Fatal("mysql.status without promote_request")
	}
	if *st.PromoteRequest != r.d.PromoteRequestPending() {
		r.t.Fatalf("promote_request %v, PromoteRequestPending %v", *st.PromoteRequest, r.d.PromoteRequestPending())
	}
	return *st.PromoteRequest
}

func (r *pendingRig) want(what string, want bool) {
	r.t.Helper()
	if got := r.pending(); got != want {
		r.t.Errorf("%s: promote_request = %v, want %v", what, got, want)
	}
}

func (r *pendingRig) jobDone() proto.JobDone {
	r.t.Helper()
	select {
	case d := <-r.ev.done:
		return d
	case <-time.After(time.Minute):
		r.t.Fatal("job did not end")
	}
	return proto.JobDone{}
}

// holdProbes replaces the emergency probes of the old master: each one
// waits for a value on the returned channel (nil: the master answers) or
// for the job's cancel; probing gets a value when one starts waiting.
func holdProbes(t *testing.T) (probing <-chan struct{}, answer chan<- error) {
	p, a := make(chan struct{}, 8), make(chan error)
	t.Cleanup(mysql.SetProbePeer(func(ctx context.Context, _ string, _ int, _ time.Duration) error {
		p <- struct{}{}
		select {
		case err := <-a:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	return p, a
}

func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(time.Minute):
		t.Fatalf("%s did not happen", what)
	}
}

// TestPromoteRequestMarkedBeforeHandlerRegistersJob: a mysql.promote whose
// handler is still releasing the start lock, before it registers its job,
// is reported by mysql.status; once its job ended it is not.
func TestPromoteRequestMarkedBeforeHandlerRegistersJob(t *testing.T) {
	r := newPendingRig(t, newPendingInstance(t), pinDir(t, "replica", pendingSource))
	r.want("before any request", false)
	if err := mysql.WriteStartLock(context.Background(), r.inst.Host()); err != nil {
		t.Fatal(err)
	}
	releasing, release := make(chan struct{}, 1), make(chan struct{})
	t.Cleanup(mysql.SetWriteConfigHook(func(string) error {
		releasing <- struct{}{}
		<-release
		return nil
	}))
	probing, answer := holdProbes(t)

	answered := r.send("mysql.promote", mustJSON(t, r.params()))
	waitFor(t, releasing, "the start lock release")
	r.want("handler held before Jobs.Start", true)
	close(release)
	waitFor(t, probing, "the first probe")
	r.want("job running", true)
	if resp := <-answered; !resp.OK {
		t.Fatalf("mysql.promote = %+v", resp.Error)
	}
	answer <- nil // the old master answers: no emergency
	if d := r.jobDone(); d.OK {
		t.Fatal("promote with an answering old master succeeded")
	}
	r.want("job ended", false)
}

// TestPromoteRequestClearedOnRefusal: a mysql.promote refused before or
// by its handler is not reported once it is answered.
func TestPromoteRequestClearedOnRefusal(t *testing.T) {
	inst := newPendingInstance(t)
	r := newPendingRig(t, inst, pinDir(t, "replica", pendingSource))
	other := r.params()
	other.OldSource.Port = 3398
	for _, c := range []struct {
		what   string
		params json.RawMessage
	}{
		{"pin mismatch", mustJSON(t, other)},
		{"broken params", json.RawMessage(`{"mode":`)},
		{"invalid params", mustJSON(t, proto.PromoteParams{Mode: "sometimes"})},
	} {
		if resp := r.request("mysql.promote", c.params); resp.OK {
			t.Fatalf("%s: mysql.promote = ok", c.what)
		}
		r.want(c.what, false)
	}

	// Strict mode: pins that cannot be read refuse the command at the gate.
	bad := pinDir(t, "replica", pendingSource)
	if err := os.WriteFile(filepath.Join(bad, mysql.RoleFile), []byte("bukan-peran\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newPendingRig(t, inst, bad)
	if resp := g.request("mysql.promote", mustJSON(t, g.params())); resp.OK {
		t.Fatal("mysql.promote with unusable pins = ok")
	}
	g.want("refused by the strict gate", false)

	// The host's local policy.
	p := newPendingRig(t, inst, pinDir(t, "replica", pendingSource))
	p.reg.Deny("mysql.promote")
	if resp := p.request("mysql.promote", mustJSON(t, p.params())); resp.OK {
		t.Fatal("denied mysql.promote = ok")
	}
	p.want("denied by local policy", false)
}

// TestPromoteRequestClearedWhenJobEnds: a mysql.promote whose job runs is
// reported until that job ends, whether it fails, is cancelled or
// succeeds (here answered from the promotion record).
func TestPromoteRequestClearedWhenJobEnds(t *testing.T) {
	inst := newPendingInstance(t)
	probing, answer := holdProbes(t)

	t.Run("failed", func(t *testing.T) {
		r := newPendingRig(t, inst, pinDir(t, "replica", pendingSource))
		started := decode[proto.StartedJob](t, r.request("mysql.promote", mustJSON(t, r.params())))
		waitFor(t, probing, "the first probe")
		r.want("job running, answered", true)
		answer <- nil
		if d := r.jobDone(); d.OK || d.JobID != started.JobID {
			t.Fatalf("job = %+v; want %s failed", d, started.JobID)
		}
		r.want("job failed", false)
	})

	t.Run("cancelled", func(t *testing.T) {
		r := newPendingRig(t, inst, pinDir(t, "replica", pendingSource))
		started := decode[proto.StartedJob](t, r.request("mysql.promote", mustJSON(t, r.params())))
		waitFor(t, probing, "the first probe")
		r.want("job running", true)
		if resp := r.request("job.cancel", mustJSON(t, map[string]string{"job_id": started.JobID})); !resp.OK {
			t.Fatalf("job.cancel = %+v", resp.Error)
		}
		if d := r.jobDone(); d.OK {
			t.Fatal("cancelled job succeeded")
		}
		if st, _ := r.runner.Status(started.JobID); st.State != "canceled" {
			t.Fatalf("job state %q, want canceled", st.State)
		}
		r.want("job cancelled", false)
	})

	t.Run("panicked", func(t *testing.T) {
		t.Cleanup(mysql.SetProbeSpacing(10 * time.Millisecond))
		t.Cleanup(mysql.SetPromoteCrash(func(stage string) {
			if stage == "before-open" {
				panic("crash (test)")
			}
		}))
		r := newPendingRig(t, inst, pinDir(t, "replica", pendingSource))
		decode[proto.StartedJob](t, r.request("mysql.promote", mustJSON(t, r.params())))
		down := errors.New("tidak menjawab (test)")
		for i := range 4 { // three probes, then one more before writes open
			waitFor(t, probing, "a probe")
			if i == 0 {
				r.want("job running", true)
			}
			answer <- down
		}
		if d := r.jobDone(); d.OK || !strings.Contains(d.Error, "crash (test)") {
			t.Fatalf("job = %+v; want the panic", d)
		}
		r.want("job panicked", false)
	})

	t.Run("succeeded", func(t *testing.T) {
		dir := pinDir(t, "master", "")
		pins, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := pins.WritePromoted(mysql.Promoted{At: time.Now(), Mode: proto.PromoteEmergency, OldSource: pendingSource}); err != nil {
			t.Fatal(err)
		}
		r := newPendingRig(t, inst, dir)
		p := r.params()
		p.NotBefore = time.Now().Add(-time.Hour)
		release := r.ev.holdLogs()
		answered := r.send("mysql.promote", mustJSON(t, p))
		waitFor(t, r.ev.held, "the job's log line")
		r.want("job running", true)
		decode[proto.StartedJob](t, <-answered)
		release()
		if d := r.jobDone(); !d.OK {
			t.Fatalf("promote of the promoted server failed: %s", d.Error)
		}
		r.want("job succeeded", false)
	})
}

// TestPromoteRequestCountsEveryRequest: of two requests read, one refused
// and one running, the refusal does not clear the running one.
func TestPromoteRequestCountsEveryRequest(t *testing.T) {
	r := newPendingRig(t, newPendingInstance(t), pinDir(t, "replica", pendingSource))
	probing, answer := holdProbes(t)
	t.Cleanup(mysql.SetProbeSpacing(10 * time.Millisecond))
	other := r.params()
	other.OldSource.Port = 3398

	running := r.send("mysql.promote", mustJSON(t, r.params()))
	waitFor(t, probing, "the first probe")
	refused := r.send("mysql.promote", mustJSON(t, other))
	if resp := <-refused; resp.OK {
		t.Fatal("promote from another source = ok")
	}
	r.want("one refused, one running", true)
	decode[proto.StartedJob](t, <-running)
	r.want("one refused, one running and answered", true)
	answer <- errors.New("tidak menjawab (test)")
	waitFor(t, probing, "the second probe")
	r.want("still running", true)
	answer <- nil
	r.jobDone()
	r.want("both ended", false)
}
