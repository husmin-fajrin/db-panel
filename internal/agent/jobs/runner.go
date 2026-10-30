// Package jobs runs the agent's long operations (clone, dump, install) as
// background jobs. A job reports progress, log lines and completion to the
// panel as proto.Event values and can be polled or cancelled through the
// job.* commands.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	logRingSize     = 2000
	historySize     = 20
	progressEvery   = time.Second
	progressMinStep = 5

	stateRunning   = "running"
	stateSucceeded = "succeeded"
	stateFailed    = "failed"
	stateCanceled  = "canceled"
)

// ErrBusy is returned by Start while another job is still running.
var ErrBusy = errors.New("agent sedang menjalankan pekerjaan lain")

// Emitter delivers events to the panel. *agent.Agent implements it; it
// returns proto.ErrOffline while disconnected.
type Emitter interface {
	Emit(ev proto.Event) error
}

// Func is the body of a job. Its result is marshalled to JSON and sent in the
// job.done event; it must honour j.Context() so the job can be cancelled.
type Func func(j *Job) (result any, err error)

// Job is the handle a Func uses to report progress and logs.
type Job struct {
	id   string
	kind string
	r    *Runner
	ctx  context.Context

	cancel context.CancelFunc

	mu          sync.Mutex
	state       string
	percent     int
	message     string
	errMsg      string
	result      json.RawMessage
	startedAt   time.Time
	finishedAt  *time.Time
	canceled    bool
	lastEmitAt  time.Time
	lastEmitPct int
	lastEmitMsg string
	emittedOnce bool
	logs        []proto.JobLog // ring, at most logRingSize lines
	logTotal    int            // lines ever written; line n has index n (1-based)
}

// ID returns the job id.
func (j *Job) ID() string { return j.id }

// Context is cancelled by Runner.Cancel.
func (j *Job) Context() context.Context { return j.ctx }

// Progress records percent (clamped to 0..100) and message and emits a
// job.progress event, at most once per second unless the percent moved by at
// least 5 points or the message changed since the last emitted event.
func (j *Job) Progress(percent int, message string) {
	percent = min(max(percent, 0), 100)
	now := j.r.now()

	j.mu.Lock()
	j.percent, j.message = percent, message
	diff := percent - j.lastEmitPct
	if diff < 0 {
		diff = -diff
	}
	emit := !j.emittedOnce ||
		message != j.lastEmitMsg ||
		diff >= progressMinStep ||
		now.Sub(j.lastEmitAt) >= progressEvery
	if emit {
		j.emittedOnce = true
		j.lastEmitAt, j.lastEmitPct, j.lastEmitMsg = now, percent, message
	}
	j.mu.Unlock()

	if emit {
		j.r.emit(proto.EventJobProgress, j.id, proto.JobProgress{JobID: j.id, Percent: percent, Message: message})
	}
}

// Logf appends a line to the job's log (level info, warn or error; anything
// else is treated as info) and emits it as a job.log event.
func (j *Job) Logf(level, format string, args ...any) {
	switch level {
	case "info", "warn", "error":
	default:
		level = "info"
	}
	line := proto.JobLog{JobID: j.id, TS: j.r.now(), Level: level, Message: fmt.Sprintf(format, args...)}

	j.mu.Lock()
	j.logTotal++
	j.logs = append(j.logs, line)
	if len(j.logs) >= 2*logRingSize { // compact in bulk: amortised O(1) per line
		j.logs = append(j.logs[:0], j.logs[len(j.logs)-logRingSize:]...)
	}
	j.mu.Unlock()

	j.r.emit(proto.EventJobLog, j.id, line)
}

func (j *Job) status() proto.JobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := proto.JobStatus{
		ID: j.id, Kind: j.kind, State: j.state,
		Percent: j.percent, Message: j.message, Error: j.errMsg,
		Result: j.result, StartedAt: j.startedAt,
	}
	if j.finishedAt != nil {
		t := *j.finishedAt
		st.FinishedAt = &t
	}
	return st
}

// Runner runs at most one job at a time and remembers the last 20 finished.
type Runner struct {
	em     Emitter
	logger *slog.Logger
	now    func() time.Time // replaceable in tests

	mu      sync.Mutex
	active  *Job
	history []*Job // finished jobs, oldest first
}

// NewRunner returns a Runner that reports through em.
func NewRunner(em Emitter, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{em: em, logger: logger, now: time.Now}
}

func newJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Start runs fn in the background and returns the new job id. It returns
// ErrBusy if a job is already running. When fn returns (or panics) the
// job is finalised and a job.done event is emitted.
func (r *Runner) Start(kind string, fn Func) (string, error) {
	id, err := newJobID()
	if err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{id: id, kind: kind, r: r, ctx: ctx, cancel: cancel, state: stateRunning, startedAt: r.now()}

	r.mu.Lock()
	if r.active != nil {
		r.mu.Unlock()
		cancel()
		return "", ErrBusy
	}
	r.active = j
	r.mu.Unlock()

	r.logger.Info("job started", "job", id, "kind", kind)
	go r.run(j, fn)
	return id, nil
}

func (r *Runner) run(j *Job, fn Func) {
	result, err := r.call(j, fn)

	var raw json.RawMessage
	if err == nil && result != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			err = fmt.Errorf("encode job result: %w", merr)
		} else {
			raw = b
		}
	}

	j.mu.Lock()
	switch {
	case err == nil:
		j.state, j.result = stateSucceeded, raw
	case j.canceled:
		j.state, j.errMsg = stateCanceled, err.Error()
	default:
		j.state, j.errMsg = stateFailed, err.Error()
	}
	t := r.now()
	j.finishedAt = &t
	done := proto.JobDone{JobID: j.id, OK: err == nil, Result: j.result, Error: j.errMsg}
	state := j.state
	j.mu.Unlock()
	j.cancel() // release the context

	// Move to history before announcing completion, so a panel that reacts to
	// job.done by starting the next job never sees ErrBusy.
	r.mu.Lock()
	r.active = nil
	r.history = append(r.history, j)
	if len(r.history) > historySize {
		r.history = append(r.history[:0], r.history[len(r.history)-historySize:]...)
	}
	r.mu.Unlock()

	r.logger.Info("job finished", "job", j.id, "kind", j.kind, "state", state)
	r.emit(proto.EventJobDone, j.id, done)
}

// call runs fn, turning a panic into an error.
func (r *Runner) call(j *Job, fn Func) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.Error("job panicked", "job", j.id, "kind", j.kind, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			result, err = nil, fmt.Errorf("panic: %v", p)
		}
	}()
	return fn(j)
}

// emit sends one job event; failures (typically proto.ErrOffline) are only
// logged because the job must keep running while the panel is away.
func (r *Runner) emit(typ, jobID string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		r.logger.Error("encode job event", "job", jobID, "type", typ, "err", err)
		return
	}
	if err := r.em.Emit(proto.Event{Type: typ, JobID: jobID, Payload: b}); err != nil {
		r.logger.Debug("emit job event failed", "job", jobID, "type", typ, "err", err)
	}
}

func (r *Runner) find(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil && r.active.id == id {
		return r.active, true
	}
	for _, j := range r.history {
		if j.id == id {
			return j, true
		}
	}
	return nil, false
}

// Status returns a snapshot of the running job or one of the last 20 finished.
func (r *Runner) Status(id string) (proto.JobStatus, bool) {
	j, ok := r.find(id)
	if !ok {
		return proto.JobStatus{}, false
	}
	return j.status(), true
}

// Cancel cancels the context of the job id and reports whether it was running.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	j := r.active
	r.mu.Unlock()
	if j == nil || j.id != id {
		return false
	}
	j.mu.Lock()
	running := j.state == stateRunning
	if running {
		j.canceled = true
	}
	j.mu.Unlock()
	if running {
		j.cancel()
	}
	return running
}

// Logs returns the retained log lines with index > after (lines are numbered
// from 1 in the order written; only the last 2000 are kept). Next is the index
// of the last line returned, or after if there were none.
func (r *Runner) Logs(id string, after int) (proto.JobLogResult, bool) {
	j, ok := r.find(id)
	if !ok {
		return proto.JobLogResult{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	after = max(after, 0)
	kept := j.logs[max(len(j.logs)-logRingSize, 0):] // the last 2000 lines
	first := j.logTotal - len(kept)                  // index of the oldest kept line, minus 1
	skip := max(after-first, 0)
	res := proto.JobLogResult{Lines: []proto.JobLog{}, Next: after}
	if skip < len(kept) {
		res.Lines = append(res.Lines, kept[skip:]...)
		res.Next = j.logTotal
	}
	return res, true
}

// Register adds job.status, job.cancel and job.log to reg.
func (r *Runner) Register(reg *commands.Registry) {
	reg.Register("job.status", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.JobIDParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		st, ok := r.Status(p.JobID)
		if !ok {
			return nil, errUnknownJob
		}
		return st, nil
	})
	reg.Register("job.cancel", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.JobIDParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if _, ok := r.find(p.JobID); !ok {
			return nil, errUnknownJob
		}
		return map[string]bool{"canceled": r.Cancel(p.JobID)}, nil
	})
	reg.Register("job.log", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.JobLogParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		res, ok := r.Logs(p.JobID, p.After)
		if !ok {
			return nil, errUnknownJob
		}
		return res, nil
	})
}

var errUnknownJob = &commands.ParamError{Msg: "job tidak dikenal"}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return &commands.ParamError{Msg: "parameter tidak valid"}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
	}
	return nil
}
