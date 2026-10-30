package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

type fakeEmitter struct {
	mu     sync.Mutex
	events []proto.Event
	err    error
	done   chan struct{}
}

func newFakeEmitter() *fakeEmitter { return &fakeEmitter{done: make(chan struct{}, 16)} }

func (f *fakeEmitter) Emit(ev proto.Event) error {
	f.mu.Lock()
	f.events = append(f.events, ev)
	err := f.err
	f.mu.Unlock()
	if ev.Type == proto.EventJobDone {
		f.done <- struct{}{}
	}
	return err
}

func (f *fakeEmitter) ofType(typ string) []proto.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []proto.Event
	for _, ev := range f.events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (f *fakeEmitter) waitDone(t *testing.T) proto.JobDone {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for job.done")
	}
	evs := f.ofType(proto.EventJobDone)
	var d proto.JobDone
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func newTestRunner(em Emitter) *Runner {
	return NewRunner(em, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestStartRunsAndEmitsDone(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	id, err := r.Start("demo", func(j *Job) (any, error) {
		j.Progress(50, "setengah")
		j.Logf("info", "halo %d", 1)
		return map[string]int{"n": 7}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("id = %q, want 32 hex chars", id)
	}
	d := em.waitDone(t)
	if d.JobID != id || !d.OK || d.Error != "" || string(d.Result) != `{"n":7}` {
		t.Errorf("done = %+v (%s)", d, d.Result)
	}
	evs := em.ofType(proto.EventJobDone)
	if evs[0].JobID != id {
		t.Errorf("Event.JobID = %q, want %q", evs[0].JobID, id)
	}
	st, ok := r.Status(id)
	if !ok || st.State != "succeeded" || st.Kind != "demo" || st.FinishedAt == nil || st.Percent != 50 || string(st.Result) != `{"n":7}` {
		t.Errorf("status = %+v ok=%v", st, ok)
	}
	if logs := em.ofType(proto.EventJobLog); len(logs) != 1 || logs[0].JobID != id {
		t.Errorf("log events = %+v", logs)
	}
	// A new job may start once the previous one is done.
	if _, err := r.Start("again", func(j *Job) (any, error) { return nil, errors.New("gagal") }); err != nil {
		t.Fatal(err)
	}
	d = em.waitDone(t)
	if d.OK || d.Error != "gagal" {
		t.Errorf("done = %+v", d)
	}
}

func TestStartRejectsSecondJob(t *testing.T) {
	r := newTestRunner(newFakeEmitter())
	release := make(chan struct{})
	started := make(chan struct{})
	if _, err := r.Start("a", func(j *Job) (any, error) {
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := r.Start("b", func(j *Job) (any, error) { return nil, nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(release)
}

func TestCancelStopsJob(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	started := make(chan struct{})
	id, err := r.Start("slow", func(j *Job) (any, error) {
		close(started)
		<-j.Context().Done()
		return nil, j.Context().Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !r.Cancel(id) {
		t.Fatal("Cancel returned false for running job")
	}
	d := em.waitDone(t)
	if d.OK {
		t.Errorf("done = %+v", d)
	}
	if st, _ := r.Status(id); st.State != "canceled" {
		t.Errorf("state = %q, want canceled", st.State)
	}
	if r.Cancel(id) {
		t.Error("Cancel of finished job returned true")
	}
	if r.Cancel("nope") {
		t.Error("Cancel of unknown job returned true")
	}
}

func TestPanicBecomesFailure(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	id, err := r.Start("boom", func(j *Job) (any, error) { panic("kaboom") })
	if err != nil {
		t.Fatal(err)
	}
	d := em.waitDone(t)
	if d.OK || d.Error != "panic: kaboom" {
		t.Errorf("done = %+v", d)
	}
	if st, _ := r.Status(id); st.State != "failed" || st.Error != "panic: kaboom" {
		t.Errorf("status = %+v", st)
	}
}

func TestProgressThrottled(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	id, err := r.Start("p", func(j *Job) (any, error) {
		j.Progress(10, "a") // first: emitted
		j.Progress(11, "a") // same second, <5 points, same message: dropped
		j.Progress(12, "a") // dropped
		j.Progress(16, "a") // +6 from last emitted (10): emitted
		j.Progress(16, "b") // message changed: emitted
		now = now.Add(1100 * time.Millisecond)
		j.Progress(17, "b") // a second later: emitted
		j.Progress(18, "b") // dropped
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	em.waitDone(t)
	var got []string
	for _, ev := range em.ofType(proto.EventJobProgress) {
		if ev.JobID != id {
			t.Errorf("JobID = %q", ev.JobID)
		}
		var p proto.JobProgress
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d%s", p.Percent, p.Message))
	}
	want := "10a,16a,16b,17b"
	if strings.Join(got, ",") != want {
		t.Errorf("progress events = %v, want %s", got, want)
	}
	// Status still reflects the latest value even when the event was dropped.
	if st, _ := r.Status(id); st.Percent != 18 || st.Message != "b" {
		t.Errorf("status = %+v", st)
	}
}

func TestLogsPagination(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	id, err := r.Start("l", func(j *Job) (any, error) {
		for i := 1; i <= 2005; i++ {
			j.Logf("info", "line %d", i)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	em.waitDone(t)

	res, ok := r.Logs(id, 0)
	if !ok || len(res.Lines) != 2000 || res.Next != 2005 {
		t.Fatalf("len=%d next=%d ok=%v", len(res.Lines), res.Next, ok)
	}
	if res.Lines[0].Message != "line 6" || res.Lines[1999].Message != "line 2005" {
		t.Errorf("first=%q last=%q", res.Lines[0].Message, res.Lines[1999].Message)
	}
	res, _ = r.Logs(id, 2003)
	if len(res.Lines) != 2 || res.Lines[0].Message != "line 2004" || res.Next != 2005 {
		t.Errorf("tail = %+v", res)
	}
	res, _ = r.Logs(id, 2005)
	if len(res.Lines) != 0 || res.Next != 2005 {
		t.Errorf("empty = %+v", res)
	}
	if res.Lines == nil {
		t.Error("Lines should be non-nil so it encodes as []")
	}
	if _, ok := r.Logs("nope", 0); ok {
		t.Error("unknown id reported ok")
	}
}

func TestRegisterCommands(t *testing.T) {
	em := newFakeEmitter()
	r := newTestRunner(em)
	reg := commands.NewRegistry()
	r.Register(reg)

	release := make(chan struct{})
	started := make(chan struct{})
	id, err := r.Start("k", func(j *Job) (any, error) {
		j.Logf("warn", "hati-hati")
		close(started)
		<-j.Context().Done()
		<-release
		return nil, j.Context().Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	call := func(cmd string, params any) proto.Response {
		raw, _ := json.Marshal(params)
		return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: raw})
	}

	resp := call("job.status", proto.JobIDParams{JobID: id})
	var st proto.JobStatus
	if !resp.OK || json.Unmarshal(resp.Result, &st) != nil || st.State != "running" {
		t.Fatalf("job.status = %+v / %+v", resp, st)
	}

	resp = call("job.log", proto.JobLogParams{JobID: id})
	var lr proto.JobLogResult
	if !resp.OK || json.Unmarshal(resp.Result, &lr) != nil || len(lr.Lines) != 1 || lr.Lines[0].Level != "warn" || lr.Next != 1 {
		t.Fatalf("job.log = %+v / %+v", resp, lr)
	}

	for _, cmd := range []string{"job.status", "job.cancel", "job.log"} {
		resp = call(cmd, proto.JobIDParams{JobID: "nope"})
		if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeBadParams || resp.Error.Message != "job tidak dikenal" {
			t.Errorf("%s unknown id: %+v", cmd, resp)
		}
	}
	resp = reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "job.status", Params: json.RawMessage(`{`)})
	if resp.OK || resp.Error.Code != proto.CodeBadParams {
		t.Errorf("bad json: %+v", resp)
	}

	resp = call("job.cancel", proto.JobIDParams{JobID: id})
	if !resp.OK || string(resp.Result) != `{"canceled":true}` {
		t.Fatalf("job.cancel = %+v (%s)", resp, resp.Result)
	}
	close(release)
	em.waitDone(t)
	resp = call("job.cancel", proto.JobIDParams{JobID: id})
	if !resp.OK || string(resp.Result) != `{"canceled":false}` {
		t.Errorf("cancel finished = %+v (%s)", resp, resp.Result)
	}
}

func TestEmitOfflineDoesNotStopJob(t *testing.T) {
	em := newFakeEmitter()
	em.err = proto.ErrOffline
	r := newTestRunner(em)
	ran := false
	id, err := r.Start("o", func(j *Job) (any, error) {
		j.Progress(20, "x")
		j.Logf("info", "y")
		j.Progress(90, "z")
		ran = true
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	em.waitDone(t)
	st, _ := r.Status(id)
	if !ran || st.State != "succeeded" || string(st.Result) != `"ok"` {
		t.Errorf("ran=%v status=%+v", ran, st)
	}
}
