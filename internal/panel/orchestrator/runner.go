// Package orchestrator runs the panel's multi-step jobs: "Siapkan master"
// (prepare_master), "Buat replika" (create_replica), and adding or removing
// databases on a running replica (add_databases, remove_databases). A job
// is a list of steps, each of which calls agent commands through the hub;
// long agent operations (clone, dump, install) are agent jobs whose events
// reach the job through HandleEvent.
//
// Each job runs on one goroutine at a time, and that goroutine makes every
// store write about the job once it exists. A job stops on its goroutine
// when it succeeds, fails, is canceled or waits for the user; Approve, Retry
// and Cancel start a new goroutine that picks it up from the store, so they
// work the same after a panel restart. What a job learns along the way (the
// copy method, the confirmed databases, the agent job it waits for) is kept
// in its ParamsJSON.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// Job kinds.
const (
	KindPrepareMaster   = "prepare_master"
	KindCreateReplica   = "create_replica"
	KindAddDatabases    = "add_databases"
	KindRemoveDatabases = "remove_databases"
)

// changesDatabases reports whether kind is a job that changes the databases
// a ready replica follows.
func changesDatabases(kind string) bool {
	return kind == KindAddDatabases || kind == KindRemoveDatabases
}

const (
	// replUser is the replication account the panel creates on a master.
	// It is also the account dumps read the master with (see
	// mysql.CreateReplUser).
	replUser = "dbpanel_repl"

	defaultOfflineGrace = 2 * time.Minute // agent offline while its job runs
	defaultPollEvery    = 5 * time.Second // agent job status / online check
	callTimeout         = 2 * time.Minute // one ordinary agent command
	restartTimeout      = 10 * time.Minute
	cancelTimeout       = 15 * time.Second
	syncTimeoutSec      = 900 // mysql.waitSynced

	resumeError = "panel dimulai ulang saat job berjalan"
)

// Deps are the Runner's dependencies.
type Deps struct {
	Store   *store.Store
	Hub     *hub.Hub
	Secrets *secrets.Box // seals the replication password
	Publish func(event string, data any)
	Logger  *slog.Logger
	Now     func() time.Time
	// LocalServerID is the panel host's own server, paired with address
	// 127.0.0.1; replicas reach a master there at PublicHost.
	LocalServerID int64
	PublicHost    string
}

// Runner starts, runs and steers orchestrated jobs.
type Runner struct {
	d   Deps
	log *slog.Logger
	now func() time.Time
	// offlineGrace is how long an agent may stay away while the panel waits
	// for its job; pollEvery how often the wait checks the agent. Tests
	// shorten both.
	offlineGrace time.Duration
	pollEvery    time.Duration

	base context.Context // parent of every job context; ended by Close
	stop context.CancelFunc
	wg   sync.WaitGroup

	startMu sync.Mutex // serialises the checks and creation of new jobs
	invMu   sync.Mutex // serialises read-modify-write of inventories

	mu     sync.Mutex
	live   map[string]*liveJob // jobs with a goroutine
	closed bool
}

// New returns a Runner. Call Resume once at startup and Close at shutdown.
func New(d Deps) *Runner {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Publish == nil {
		d.Publish = func(string, any) {}
	}
	base, stop := context.WithCancel(context.Background())
	return &Runner{
		d: d, log: d.Logger, now: d.Now,
		offlineGrace: defaultOfflineGrace, pollEvery: defaultPollEvery,
		base: base, stop: stop, live: make(map[string]*liveJob),
	}
}

// Close stops every job goroutine and waits for them. Jobs interrupted this
// way stay running in the store; Resume marks them failed at the next start.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
}

// Connect hands the admin account to the server's agent (mysql.connect),
// which keeps it, and stores the inventory it answers.
func (r *Runner) Connect(ctx context.Context, serverID int64, p proto.ConnectParams) (proto.ConnectResult, error) {
	return r.inventoryCall(ctx, serverID, "mysql.connect", p)
}

// RefreshInventory re-reads the server's MySQL inventory with the account
// its agent keeps and stores it.
func (r *Runner) RefreshInventory(ctx context.Context, serverID int64) (proto.ConnectResult, error) {
	return r.inventoryCall(ctx, serverID, "mysql.inventory", nil)
}

func (r *Runner) inventoryCall(ctx context.Context, serverID int64, cmd string, params any) (proto.ConnectResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var res proto.ConnectResult
	if err := r.d.Hub.Call(ctx, serverID, cmd, params, &res); err != nil {
		return res, err
	}
	err := r.updateInventory(serverID, func(inv *store.Inventory) {
		inv.Connected = true
		inv.Version, inv.Series = res.Status.Version, res.Status.Series
		inv.Databases, inv.MissingGrants = res.Databases, res.MissingGrants
	})
	if err != nil {
		return res, err
	}
	r.d.Publish("mysql_inventory", map[string]any{"id": serverID})
	return res, nil
}

// updateInventory applies change to the server's inventory (a new one when
// none is stored) and saves it.
func (r *Runner) updateInventory(serverID int64, change func(inv *store.Inventory)) error {
	r.invMu.Lock()
	defer r.invMu.Unlock()
	inv, err := r.d.Store.GetInventory(serverID)
	if errors.Is(err, store.ErrNotFound) {
		inv, err = store.Inventory{ServerID: serverID}, nil
	}
	if err != nil {
		return err
	}
	change(&inv)
	inv.UpdatedAt = r.now()
	return r.d.Store.SetInventory(inv)
}

// createJob stores a new queued job and starts its goroutine. Callers hold
// startMu.
func (r *Runner) createJob(kind string, serverID, topologyID int64, st jobState, steps []string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	params, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	j := store.Job{ID: id, Kind: kind, ServerID: serverID, TopologyID: topologyID, ParamsJSON: string(params), CreatedAt: r.now()}
	if err := r.d.Store.CreateJob(j, steps); err != nil {
		return "", err
	}
	r.d.Publish("job_update", map[string]any{"id": id, "kind": kind, "server_id": serverID, "status": store.JobQueued})
	if err := r.launch(id, action{kind: actRun}); err != nil {
		// No goroutine will run it: do not leave it queued, blocking the server.
		msg := "pekerjaan tidak bisa dimulai: " + err.Error()
		now := r.now()
		if serr := r.d.Store.SetStep(id, 0, store.StepFailed, msg, now); serr != nil {
			r.log.Warn("fail step of unstarted job", "job", id, "err", serr)
		}
		if uerr := r.d.Store.UpdateJob(id, store.JobFailed, 0, msg, now); uerr != nil {
			r.log.Warn("fail unstarted job", "job", id, "err", uerr)
		}
		return "", err
	}
	return id, nil
}

// refuseBusy refuses a server with a queued, running or waiting job.
func (r *Runner) refuseBusy(serverID int64, who string) error {
	if _, busy, err := r.d.Store.ActiveJobForServer(serverID); err != nil {
		return err
	} else if busy {
		return fmt.Errorf("%s masih punya pekerjaan yang belum selesai", who)
	}
	return nil
}

type actionKind int

const (
	actRun actionKind = iota // a new job
	actApprove
	actRetry
	actCancel
)

// action is what a job goroutine is asked to do. step binds an approval or
// retry to the step that was current when Approve or Retry was called, so
// a late double click cannot approve a later step.
type action struct {
	kind actionKind
	step int
}

// liveJob is a job that has a goroutine.
type liveJob struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	// userCanceled tells a context ended by Cancel from one ended by Close.
	userCanceled atomic.Bool

	// Guarded by Runner.mu.
	pending     *action // asked for while the goroutine was finishing
	watching    bool    // waiting for an agent job's events
	watchServer int64
	watchJob    string // "" until the agent answered with the job id

	evMu   sync.Mutex
	events []proto.Event
	notify chan struct{} // cap 1: events were queued
}

// push queues ev for the job goroutine; it never blocks.
func (lj *liveJob) push(ev proto.Event) {
	lj.evMu.Lock()
	lj.events = append(lj.events, ev)
	lj.evMu.Unlock()
	select {
	case lj.notify <- struct{}{}:
	default:
	}
}

func (lj *liveJob) drain() []proto.Event {
	lj.evMu.Lock()
	defer lj.evMu.Unlock()
	evs := lj.events
	lj.events = nil
	return evs
}

var (
	errClosed     = errors.New("panel sedang berhenti")
	errNotWaiting = errors.New("pekerjaan tidak sedang menunggu persetujuan")
	errBusy       = errors.New("pekerjaan sedang diproses; coba lagi sebentar")
)

// launch hands act to the job's goroutine, starting one when it has none.
func (r *Runner) launch(id string, act action) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	if lj := r.live[id]; lj != nil {
		if act.kind == actCancel {
			lj.userCanceled.Store(true)
			lj.cancel()
			lj.pending = &act
			return nil
		}
		if lj.pending != nil {
			return errBusy
		}
		// The goroutine is finishing (the store already shows the state
		// act was checked against); it takes act before it exits.
		lj.pending = &act
		return nil
	}
	ctx, cancel := context.WithCancel(r.base)
	lj := &liveJob{id: id, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	r.live[id] = lj
	r.wg.Add(1)
	go r.loop(lj, act)
	return nil
}

func (r *Runner) loop(lj *liveJob, act action) {
	defer r.wg.Done()
	for {
		r.execute(lj, act)
		r.mu.Lock()
		if lj.pending != nil {
			act, lj.pending = *lj.pending, nil
			r.mu.Unlock()
			continue
		}
		delete(r.live, lj.id)
		r.mu.Unlock()
		lj.cancel()
		return
	}
}

// execute performs act on the job as the store has it now.
func (r *Runner) execute(lj *liveJob, act action) {
	j, steps, err := r.d.Store.GetJob(lj.id)
	if err != nil {
		r.log.Warn("load job", "job", lj.id, "err", err)
		return
	}
	if act.kind == actCancel || lj.userCanceled.Load() {
		r.cancelJob(lj, j)
		return
	}
	if lj.ctx.Err() != nil { // shutting down
		return
	}
	switch act.kind {
	case actRun:
		if j.Status != store.JobQueued {
			return
		}
	case actApprove:
		if j.Status != store.JobWaiting || j.CurrentStep != act.step {
			return
		}
	case actRetry:
		if j.CurrentStep != act.step {
			return
		}
		switch j.Status {
		case store.JobWaiting:
		case store.JobFailed:
			if err := r.d.Store.ReopenJob(j.ID, r.now()); err != nil {
				r.log.Warn("reopen job", "job", j.ID, "err", err)
				return
			}
		default:
			return
		}
	}
	jr, err := r.newRun(lj, j, steps)
	if err != nil {
		r.log.Error("job params unreadable", "job", j.ID, "err", err)
		return
	}
	jr.run(j.CurrentStep, act.kind == actApprove)
}

// Approve lets a job waiting for the user continue: the waiting step runs
// again with the approval.
func (r *Runner) Approve(jobID string) error {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil {
		return err
	}
	if j.Status != store.JobWaiting {
		return errNotWaiting
	}
	return r.launch(jobID, action{kind: actApprove, step: j.CurrentStep})
}

// Retry runs a failed job again from the step that failed, with the same
// params; a waiting job re-checks its waiting step.
func (r *Runner) Retry(jobID string) error {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil {
		return err
	}
	switch j.Status {
	case store.JobFailed, store.JobWaiting:
		if other, busy, err := r.d.Store.ActiveJobForServer(j.ServerID); err != nil {
			return err
		} else if busy && other.ID != jobID {
			return errors.New("server ini sudah menjalankan pekerjaan lain")
		}
		return r.launch(jobID, action{kind: actRetry, step: j.CurrentStep})
	case store.JobSucceeded, store.JobCanceled:
		return store.ErrJobFinished
	}
	return errors.New("pekerjaan masih berjalan")
}

// Cancel stops a job that has not finished: the agent job it waits for, if
// any, is canceled too.
func (r *Runner) Cancel(jobID string) error {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil {
		return err
	}
	switch j.Status {
	case store.JobSucceeded, store.JobFailed, store.JobCanceled:
		return store.ErrJobFinished
	}
	return r.launch(jobID, action{kind: actCancel})
}

// Resume runs once at startup, before any job goroutine: a job still
// running (or queued) in the store was cut off by the previous stop and is
// failed at its step (Retry continues it); a waiting job keeps waiting.
func (r *Runner) Resume(ctx context.Context) {
	jobs, err := r.d.Store.RunningJobs()
	if err != nil {
		r.log.Error("list running jobs", "err", err)
		return
	}
	all, err := r.d.Store.ListJobs(0)
	if err != nil {
		r.log.Error("list jobs", "err", err)
		return
	}
	for _, j := range all {
		if j.Status == store.JobQueued {
			jobs = append(jobs, j)
		}
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		_, live := r.live[j.ID]
		r.mu.Unlock()
		if live || (j.Status != store.JobRunning && j.Status != store.JobQueued) {
			continue
		}
		now := r.now()
		if err := r.d.Store.SetStep(j.ID, j.CurrentStep, store.StepFailed, resumeError, now); err != nil {
			r.log.Warn("fail step on resume", "job", j.ID, "err", err)
		}
		if err := r.d.Store.UpdateJob(j.ID, store.JobFailed, j.CurrentStep, resumeError, now); err != nil &&
			!errors.Is(err, store.ErrJobFinished) {
			r.log.Warn("fail job on resume", "job", j.ID, "err", err)
		}
		if _, err := r.d.Store.AppendJobLog(j.ID, "error", resumeError, now); err != nil {
			r.log.Warn("job log", "job", j.ID, "err", err)
		}
		state, lastErr := store.ReplicaFailed, resumeError
		if changesDatabases(j.Kind) {
			state, lastErr = store.ReplicaAttention, failedAt(j.Kind, j.CurrentStep, resumeError)
		}
		if j.Kind == KindCreateReplica || changesDatabases(j.Kind) {
			if err := r.d.Store.SetReplicaState(j.ServerID, state, lastErr); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				r.log.Warn("replica state on resume", "job", j.ID, "err", err)
			}
		}
		r.log.Info("job failed by restart", "job", j.ID, "kind", j.Kind, "step", j.CurrentStep)
	}
}

// HandleEvent receives agent job events (job.progress, job.log, job.done)
// from Hub.OnEvent and queues them for the job waiting on that agent job.
// It runs on the agent connection's goroutine and never blocks.
func (r *Runner) HandleEvent(serverID int64, ev proto.Event) {
	switch ev.Type {
	case proto.EventJobProgress, proto.EventJobLog, proto.EventJobDone:
	default:
		r.log.Debug("agent event ignored", "server_id", serverID, "type", ev.Type)
		return
	}
	r.mu.Lock()
	var target *liveJob
	for _, lj := range r.live {
		if lj.watching && lj.watchServer == serverID && (lj.watchJob == "" || lj.watchJob == ev.JobID) {
			target = lj
			break
		}
	}
	r.mu.Unlock()
	if target == nil {
		r.log.Debug("event for no running job", "server_id", serverID, "type", ev.Type, "agent_job", ev.JobID)
		return
	}
	target.push(ev)
}

// cancelJob ends a job the user canceled.
func (r *Runner) cancelJob(lj *liveJob, j store.Job) {
	switch j.Status {
	case store.JobSucceeded, store.JobFailed, store.JobCanceled:
		return
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		r.log.Warn("job params unreadable", "job", j.ID, "err", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	if st.AgentJobID != "" {
		if err := r.d.Hub.Call(ctx, st.AgentServerID, "job.cancel", proto.JobIDParams{JobID: st.AgentJobID}, nil); err != nil {
			r.log.Warn("cancel agent job", "job", j.ID, "agent_job", st.AgentJobID, "err", err)
		}
	}
	jr := &jobRun{r: r, lj: lj, job: j, st: st, idx: j.CurrentStep}
	const msg = "dibatalkan oleh pengguna"
	switch {
	case j.Kind == KindCreateReplica:
		jr.stopReplicaAfter(ctx)
		if err := r.d.Store.RemoveReplica(j.ServerID); err != nil && !errors.Is(err, store.ErrNotFound) {
			r.log.Warn("remove replica on cancel", "job", j.ID, "err", err)
		}
	case changesDatabases(j.Kind):
		// Replication is left as the canceled step left it; the next
		// add job restores the filter and starts it.
		jr.setReplicaState(store.ReplicaAttention, "dibatalkan saat "+stepTitle(j.Kind, j.CurrentStep))
	}
	jr.setStep(store.StepFailed, msg)
	jr.setJob(store.JobCanceled, msg)
	jr.logf("warn", "Pekerjaan dibatalkan.")
}

// jobState is a job's params and what it learned, kept in ParamsJSON.
type jobState struct {
	// create_replica input.
	MasterServerID int64    `json:"master_server_id,omitempty"`
	Databases      []string `json:"databases,omitempty"` // empty = all
	ForceDump      bool     `json:"force_dump,omitempty"`
	ConfirmWipe    bool     `json:"confirm_wipe,omitempty"` // the form's; pre-approves the CLONE wait only

	// create_replica, learned.
	MasterUUID      string   `json:"master_uuid,omitempty"`
	MasterMySQLID   uint32   `json:"master_mysql_server_id,omitempty"`
	MasterVersion   string   `json:"master_version,omitempty"`
	MasterSeries    string   `json:"master_series,omitempty"`
	PeerDatabases   []string `json:"peer_databases,omitempty"`
	DataBytes       uint64   `json:"data_bytes,omitempty"`
	NeedsInstall    bool     `json:"needs_install,omitempty"`
	WipeHash        string   `json:"wipe_hash,omitempty"` // sha256 of the leftover list last shown
	WipeList        []string `json:"wipe_list,omitempty"` // leftovers the user approved; dropped before a dump
	Method          string   `json:"method,omitempty"`    // clone | dump
	CloneApproved   bool     `json:"clone_approved,omitempty"`
	ReplicaServerID uint32   `json:"replica_server_id,omitempty"`
	SnapshotGTID    string   `json:"snapshot_gtid,omitempty"`

	// add_databases and remove_databases input; Databases above is what the
	// replica followed when the job started.
	Change   []string `json:"change_databases,omitempty"` // added or removed
	DropData bool     `json:"drop_data,omitempty"`        // remove: drop them on the replica too

	// prepare_master, learned.
	MasterPort     int      `json:"master_port,omitempty"`
	NeedsRestart   bool     `json:"needs_restart,omitempty"`
	RestartReasons []string `json:"restart_reasons,omitempty"`

	// The agent job the current step waits for.
	AgentServerID int64  `json:"agent_server_id,omitempty"`
	AgentJobID    string `json:"agent_job_id,omitempty"`
	AgentCmd      string `json:"agent_cmd,omitempty"`
}

// outcome is how a step ended when it did not fail.
type outcome struct {
	status  string // StepDone | StepSkipped | StepWaiting
	message string
}

func done(format string, args ...any) outcome {
	return outcome{store.StepDone, fmt.Sprintf(format, args...)}
}
func skipped(msg string) outcome { return outcome{store.StepSkipped, msg} }
func wait(msg string) outcome    { return outcome{store.StepWaiting, msg} }

// step is one step of a job; approved is true when the user approved the
// wait this step returned last time.
type step struct {
	title string
	fn    func(ctx context.Context, approved bool) (outcome, error)
}

// stepError is a failure whose text already says everything; it is not
// prefixed with the step title.
type stepError string

func (e stepError) Error() string { return string(e) }

// jobRun is one pass of a job's goroutine over its steps.
type jobRun struct {
	r     *Runner
	lj    *liveJob
	job   store.Job
	st    jobState
	steps []step
	idx   int // current step
}

func (r *Runner) newRun(lj *liveJob, j store.Job, rows []store.JobStep) (*jobRun, error) {
	jr := &jobRun{r: r, lj: lj, job: j}
	if err := json.Unmarshal([]byte(j.ParamsJSON), &jr.st); err != nil {
		return nil, err
	}
	switch j.Kind {
	case KindPrepareMaster:
		jr.steps = jr.prepareMasterSteps()
	case KindCreateReplica:
		jr.steps = jr.createReplicaSteps()
	case KindAddDatabases:
		jr.steps = jr.addDatabasesSteps()
	case KindRemoveDatabases:
		jr.steps = jr.removeDatabasesSteps()
	default:
		return nil, fmt.Errorf("jenis pekerjaan tidak dikenal: %q", j.Kind)
	}
	if len(jr.steps) != len(rows) {
		return nil, fmt.Errorf("pekerjaan punya %d tahap, diharapkan %d", len(rows), len(jr.steps))
	}
	return jr, nil
}

// run executes the steps from from on. approved applies to the first one.
func (jr *jobRun) run(from int, approved bool) {
	ctx := jr.lj.ctx
	switch {
	case jr.job.Kind == KindCreateReplica:
		jr.setReplicaState(store.ReplicaCreating, "")
	case changesDatabases(jr.job.Kind):
		jr.setReplicaState(store.ReplicaChanging, "")
	}
	for i := from; i < len(jr.steps); i++ {
		jr.idx = i
		if !jr.setJob(store.JobRunning, "") {
			return
		}
		jr.setStep(store.StepRunning, "")
		out, err := jr.steps[i].fn(ctx, approved && i == from)
		if ctx.Err() != nil {
			// Canceled by the user (the goroutine's next action cancels
			// the job) or by Close (Resume fails it at the next start).
			return
		}
		if err != nil {
			jr.fail(err)
			return
		}
		jr.setStep(out.status, out.message)
		if out.status == store.StepWaiting {
			jr.logf("warn", "Menunggu persetujuan: %s", out.message)
			jr.setJob(store.JobWaiting, "")
			return
		}
	}
	jr.setJob(store.JobSucceeded, "")
	jr.logf("info", "Pekerjaan selesai.")
}

// fail ends the job at the current step.
func (jr *jobRun) fail(err error) {
	msg := jr.steps[jr.idx].title + ": " + err.Error()
	var se stepError
	if errors.As(err, &se) {
		msg = se.Error()
	}
	jr.r.log.Warn("job step failed", "job", jr.job.ID, "kind", jr.job.Kind, "step", jr.idx, "err", err)
	jr.setStep(store.StepFailed, msg)
	jr.logf("error", "%s", msg)
	switch {
	case jr.job.Kind == KindCreateReplica:
		ctx, cancel := context.WithTimeout(context.WithoutCancel(jr.lj.ctx), cancelTimeout)
		jr.stopReplicaAfter(ctx)
		cancel()
		jr.setReplicaState(store.ReplicaFailed, msg)
	case changesDatabases(jr.job.Kind):
		jr.logChangeFailure()
		jr.setReplicaState(store.ReplicaAttention, failedAt(jr.job.Kind, jr.idx, err.Error()))
	}
	jr.setJob(store.JobFailed, msg)
}

// setJob records the job's status at the current step and reports whether
// the job is still open (false once it already finished).
func (jr *jobRun) setJob(status, errMsg string) bool {
	err := jr.r.d.Store.UpdateJob(jr.job.ID, status, jr.idx, errMsg, jr.r.now())
	if errors.Is(err, store.ErrJobFinished) {
		return false
	}
	if err != nil {
		jr.r.log.Warn("update job", "job", jr.job.ID, "err", err)
	}
	jr.r.d.Publish("job_update", map[string]any{"id": jr.job.ID, "kind": jr.job.Kind, "server_id": jr.job.ServerID,
		"status": status, "step": jr.idx, "error": errMsg})
	return true
}

func (jr *jobRun) setStep(status, message string) {
	if err := jr.r.d.Store.SetStep(jr.job.ID, jr.idx, status, message, jr.r.now()); err != nil {
		jr.r.log.Warn("update job step", "job", jr.job.ID, "step", jr.idx, "err", err)
	}
	jr.r.d.Publish("job_update", map[string]any{"id": jr.job.ID, "step": jr.idx, "step_status": status, "message": message})
}

// logf appends a line to the job log and publishes it. Never log a password.
func (jr *jobRun) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := jr.r.now()
	id, err := jr.r.d.Store.AppendJobLog(jr.job.ID, level, msg, now)
	if err != nil {
		jr.r.log.Warn("job log", "job", jr.job.ID, "err", err)
		return
	}
	jr.r.d.Publish("job_log", map[string]any{"job_id": jr.job.ID, "id": id, "ts": now, "level": level, "message": msg})
}

// save stores the job's state in its ParamsJSON.
func (jr *jobRun) save() {
	b, err := json.Marshal(jr.st)
	if err == nil {
		err = jr.r.d.Store.SetJobParams(jr.job.ID, string(b))
	}
	if err != nil {
		jr.r.log.Warn("save job params", "job", jr.job.ID, "err", err)
	}
}

// call runs an ordinary agent command for the current step.
func (jr *jobRun) call(ctx context.Context, serverID int64, cmd string, params, result any) error {
	return jr.callWithin(ctx, callTimeout, serverID, cmd, params, result)
}

func (jr *jobRun) callWithin(ctx context.Context, d time.Duration, serverID int64, cmd string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err := jr.r.d.Hub.Call(ctx, serverID, cmd, params, result)
	if errors.Is(err, proto.ErrOffline) {
		return stepError(fmt.Sprintf("agent %s terputus saat %s", jr.serverName(serverID), jr.steps[jr.idx].title))
	}
	return err
}

func (jr *jobRun) serverName(id int64) string {
	srv, err := jr.r.d.Store.GetServer(id)
	if err != nil {
		return fmt.Sprintf("#%d", id)
	}
	return srv.Name
}

// sourceHost is the address replicas use to reach srv's MySQL.
func (r *Runner) sourceHost(srv store.Server) string {
	if srv.ID == r.d.LocalServerID && r.d.PublicHost != "" {
		return r.d.PublicHost
	}
	return srv.Address
}

// logChecks writes the preflight findings that are not ok to the job log.
func (jr *jobRun) logChecks(checks []proto.Check) {
	for _, c := range checks {
		if c.Status == proto.CheckOK {
			continue
		}
		level := "warn"
		if c.Status == proto.CheckFail {
			level = "error"
		}
		jr.logf(level, "%s [%s]: %s", c.Title, c.Status, c.Detail)
	}
}

// failedChecks describes the checks with status, or "" when there are none.
func failedChecks(checks []proto.Check, status string) string {
	var out []string
	for _, c := range checks {
		if c.Status == status {
			out = append(out, c.Title+": "+c.Detail)
		}
	}
	return strings.Join(out, "; ")
}
