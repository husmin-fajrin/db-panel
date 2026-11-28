package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// A promote whose mysql.promote was sent, outcome unknown (spec 5.2 row 5):
// the panel settles it only from evidence read at the candidate, never
// from a read-only mysql.status alone, and never concludes "nothing
// happened" while the candidate's agent may still open writes.

// agentPromoteJobBound is the sum of the bounds of every step of the
// agent's mysql.promote job, from the request being read to its end; its
// comment lists each part with file and line (internal/agent/...). The
// job's own context has no deadline (jobs/runner.go:178, WithCancel), so
// this sum is its bound. A statement is bounded by the admin connection's
// read timeout (K18); a new connection by its dial timeout plus one
// statement for its handshake.
//
// The parts, as the agent defines them (TestPromoteJobBoundParts in
// internal/agent/drivers/mysql reads the lines between the markers and
// compares each value with the agent's):
//
// bound-parts:
//
//	adminReadTimeout = 10m (drivers/mysql/conn.go:53, DSN ReadTimeout)
//	adminDialTimeout = 5s (drivers/mysql/conn.go:52, DSN Timeout)
//	requestTimeout = 10m (agent/session.go:21, the request's context)
//	emergencyProbes = 3 (drivers/mysql/promote.go:51)
//	probeSpacing = 10s (drivers/mysql/promote.go:58)
//	peerTimeout = 5s (drivers/mysql/peer.go:26)
//	relayWaitSec = 600 (drivers/mysql/promote.go:63)
//	relayLogEvery = 5s (drivers/mysql/promote.go:69)
//	restoreTimeout = 30s (drivers/mysql/dump.go:31)
//	liftLockWait = 10s (drivers/mysql/lift.go:31)
//
// :bound-parts
//
// The steps and what bounds them:
//   - the handler, request read to job start (promote.go:78-148): every
//     ctx-aware part under requestTimeout; the promoteMu wait of
//     releaseForPromote (startlock_guard.go:506) is not ctx-aware, its
//     holders are other requests read before it whose context ends by
//     then, plus their ctx-free tails: 2 × requestTimeout;
//   - openAdmin and the read_only read (promote.go:225,232): 1 dial, 2
//     statements; currentStatus (promote.go:328): 1;
//   - the old master's check (promote.go:385-436): emergency 3 probes 10 s
//     apart, 5 s each (35 s) then 1 statement; planned one 5 s read then 1
//     statement: 35 s + 1 statement;
//   - the relay log (promote.go:493-615): 4 statements, 1 dial and its
//     handshake, and the loop: it ends at the first check past
//     relayWaitSec; the last iteration that passes the check can run 2
//     more statements, the next one 2 before it ends, with two ticks of
//     relayLogEvery: 600 s + 10 s + 1 dial + 9 statements;
//   - the re-check (promote.go:357-363,463-480): 5 s + 1 statement;
//   - STOP REPLICA and the two SET PERSIST (promote.go:368-380): 3
//     statements, a failure's START REPLICA within restoreTimeout;
//   - finishPromote (promote.go:633-698, users.go:281-360, lift.go): 21
//     statements and 4 dials; the replUserMu wait (a createReplUser
//     request: requestTimeout + restoreTimeout); the lift gate's mutex
//     (another opener's begin or end: 2 statements + liftLockWait +
//     restoreTimeout), its file lock (liftLockWait), its end
//     (restoreTimeout).
//
// In all: 40 statements × adminReadTimeout (400 min), 6 dials ×
// adminDialTimeout (30 s), 2 × requestTimeout (20 min) for the handler
// and 1 × requestTimeout (10 min) for replUserMu, peers 35 s + 5 s,
// relay 600 s + 2 × 5 s, 4 × restoreTimeout, 2 × liftLockWait:
// 443 min 40 s. The marker file (promote.go:365) and the pin files are
// local writes without a timer.
const agentPromoteJobBound time.Duration = 40*10*time.Minute + 6*5*time.Second +
	2*10*time.Minute + 10*time.Minute +
	(3*5*time.Second + 2*10*time.Second) + 5*time.Second +
	(600*time.Second + 2*5*time.Second) +
	4*30*time.Second + 2*10*time.Second

// promoteUnansweredBound is bound B: max(30 minutes, 2 ×
// agentPromoteJobBound). A start call of mysql.promote nobody answered,
// to an agent that does not say whether it holds a promote request
// (0.2.0), counts as ended only once B passed since it was sent.
const promoteUnansweredBound time.Duration = max(30*time.Minute, 2*agentPromoteJobBound)

// sentStatusRead bounds the candidate's mysql.status (and job.status) a
// verdict reads.
const sentStatusRead = 10 * time.Second

// sentVerdict is what the candidate says about a sent mysql.promote.
type sentVerdict int

const (
	// sentWritable: the candidate opened writes (read_only OFF), past the
	// point of no return.
	sentWritable sentVerdict = iota + 1
	// sentOver: both flags ON, and its agent job is proven ended.
	sentOver
	// sentUnreadable: the candidate could not be read, or read read_only
	// ON with super_read_only OFF.
	sentUnreadable
	// sentRunning: both flags ON, and its agent job may still run.
	sentRunning
)

// promoteSentAt is when mysql.promote of j (state st) was last sent:
// PromoteSentAt, or for a job of 0.2.0, which stored none, its last update
// as K17 reads it: FinishedAt, else StartedAt, else CreatedAt.
func promoteSentAt(j store.Job, st jobState) time.Time {
	switch {
	case st.PromoteSentAt != nil:
		return *st.PromoteSentAt
	case j.FinishedAt != nil:
		return *j.FinishedAt
	case j.StartedAt != nil:
		return *j.StartedAt
	}
	return j.CreatedAt
}

// holdsPromoteJob reports whether st holds the id of a mysql.promote
// agent job.
func holdsPromoteJob(st jobState) bool {
	return st.AgentJobID != "" && st.AgentCmd == "mysql.promote"
}

// promoteUnanswered reports whether the last start call of mysql.promote
// of st was not answered with a job id: PromoteUnanswered, or, for a job
// of 0.2.0 (sent without PromoteSentAt), holding no mysql.promote job id.
func promoteUnanswered(st jobState) bool {
	if st.PromoteSentAt == nil {
		return !holdsPromoteJob(st)
	}
	return st.PromoteUnanswered
}

// promoteSentVerdict reads the candidate of j (state st, mysql.promote
// sent, not past the point of no return) and says what its evidence is
// (spec 5.2 row 5); the time is when B passes since the send (T14b).
// mysql.status within sentStatusRead: unreadable; writable when read_only
// is OFF (appsWrite), whatever super_read_only says; unreadable when
// read_only is ON and super_read_only OFF; only both ON (noWrites) reach
// the evidence: PromoteRequest true running (it beats every other evidence),
// false over; without the field (an agent of 0.2.0): a held job id is
// over when job.status says it ended or the agent no longer knows it
// (CodeBadParams), running otherwise; an unanswered start is over only
// once B passed since it was sent (a send time in the future, a clock
// set back, is not); and a start answered whose job id was forgotten
// (agentJobEnded: the panel saw it end) is over.
func (r *Runner) promoteSentVerdict(ctx context.Context, j store.Job, st jobState) (sentVerdict, time.Time) {
	sent := promoteSentAt(j, st)
	over := sent.Add(promoteUnansweredBound)
	sctx, cancel := context.WithTimeout(ctx, sentStatusRead)
	var cs proto.MySQLStatus
	err := r.d.Hub.Call(sctx, st.CandidateServerID, "mysql.status", nil, &cs)
	cancel()
	switch {
	case err != nil:
		return sentUnreadable, over
	case appsWrite(cs.ReadOnly):
		return sentWritable, over
	case !noWrites(cs.ReadOnly, cs.SuperReadOnly):
		// read_only ON, super_read_only OFF: not the state the evidence
		// below was written for; never over.
		return sentUnreadable, over
	case cs.PromoteRequest != nil:
		if *cs.PromoteRequest {
			return sentRunning, over
		}
		return sentOver, over
	}
	if promoteUnanswered(st) {
		if d := r.now().Sub(sent); d >= 0 && d >= promoteUnansweredBound {
			return sentOver, over
		}
		return sentRunning, over
	}
	if !holdsPromoteJob(st) {
		return sentOver, over
	}
	jctx, cancel := context.WithTimeout(ctx, sentStatusRead)
	defer cancel()
	var s proto.JobStatus
	err = r.d.Hub.Call(jctx, st.AgentServerID, "job.status", proto.JobIDParams{JobID: st.AgentJobID}, &s)
	var pe *proto.Error
	switch {
	case errors.As(err, &pe) && pe.Code == proto.CodeBadParams:
		return sentOver, over
	case err != nil, s.State == "running":
		return sentRunning, over
	}
	return sentOver, over
}

// Codes and tails of PromoteSentError.
const (
	PromoteSentOverEmergency = "over_emergency"
	PromoteSentOverPlanned   = "over_planned"
	PromoteSentUnreadable    = "unreadable"
	PromoteSentRunning       = "running"
	PromoteSentChanged       = "changed"

	PromoteSentTailPlannedLocked   = "planned_locked"
	PromoteSentTailOldMasterLocked = "old_master_locked"
)

// Texts of a sent promote's Ulangi (spec 5.11; P6 is not in the spec).
const (
	// textSentOverEmergency is T13.
	textSentOverEmergency = "Promote darurat ini belum membuka master baru: kandidat %s masih baca-saja. Pekerjaan ini " +
		"digagalkan; tidak ada yang berubah. Mulai promote darurat baru dari halaman master."
	// textSentOverPlanned is T13b.
	textSentOverPlanned = `Promote ini belum mengganti master: kandidat %s masih baca-saja. Pekerjaan ini digagalkan. Bila ` +
		`master masih terkunci, tekan "Buka kunci promote". Sesudah itu: "Ulangi dari tahap ini" bila konfirmasi ` +
		`terakhirnya belum lewat 24 jam, atau promote baru.`
	// textSentUnreadable is T14.
	textSentUnreadable = `Kandidat %s belum bisa dibaca, jadi panel belum tahu apakah ia sudah menerima tulisan. Tidak ada ` +
		`yang diubah. Tekan "Ulangi dari tahap ini" lagi sesudah agent kandidat tersambung.`
	// textSentRunning is T14b: the candidate, when the panel takes the
	// agent job as ended.
	textSentRunning = `Pekerjaan promote di kandidat %s masih berjalan, atau belum terbukti berakhir. Tidak ada yang ` +
		`diubah. Tunggu sebentar, lalu tekan "Ulangi dari tahap ini" lagi. Bila agent kandidat tidak melaporkannya, panel ` +
		`menganggapnya berakhir pukul %s.`
	// textSentTailPlanned is T14t.
	textSentTailPlanned = "Master %s tetap baca-saja selama itu. Bila kandidat tidak akan kembali, buka master di " +
		"servernya: sudo dbpanel agent unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat bukan master dan " +
		"tidak akan menjadi master: MySQL-nya terbaca baca-saja di server kandidat itu sendiri, atau host-nya mati."
	// textSentTailOldMaster is T14u.
	textSentTailOldMaster = "Master lama %s sudah kembali dan masih baca-saja oleh kunci promote terencana sebelumnya. " +
		"Bila kandidat promote darurat ini tidak akan kembali, buka master lama itu di servernya: sudo dbpanel agent " +
		"unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat promote darurat ini dan kandidat promote " +
		"terencana sebelumnya bukan master dan tidak akan menjadi master: MySQL-nya terbaca baca-saja di servernya " +
		"sendiri, atau host-nya mati."
	// textSentChanged is P6.
	textSentChanged = "Keadaan pekerjaan ini berubah selagi panel memeriksa kandidat; muat ulang halaman ini."
)

// PromoteSentError is "Ulangi" of a sent promote that could not continue.
type PromoteSentError struct {
	Code      string    // over_emergency (T13) | over_planned (T13b) | unreadable (T14) | running (T14b) | changed (P6)
	Candidate string    // server names
	Over      time.Time // running: when the panel takes the agent job as ended
	Tail      string    // "" | planned_locked (T14t, Master) | old_master_locked (T14u, Master)
	Master    string
}

func (e *PromoteSentError) Error() string {
	var s string
	switch e.Code {
	case PromoteSentOverEmergency:
		return fmt.Sprintf(textSentOverEmergency, e.Candidate)
	case PromoteSentOverPlanned:
		return fmt.Sprintf(textSentOverPlanned, e.Candidate)
	case PromoteSentChanged:
		return textSentChanged
	case PromoteSentRunning:
		s = fmt.Sprintf(textSentRunning, e.Candidate, e.Over.Local().Format("02-01-2006 15:04:05"))
	default:
		s = fmt.Sprintf(textSentUnreadable, e.Candidate)
	}
	switch e.Tail {
	case PromoteSentTailPlannedLocked:
		s += " " + fmt.Sprintf(textSentTailPlanned, e.Master)
	case PromoteSentTailOldMasterLocked:
		s += " " + fmt.Sprintf(textSentTailOldMaster, e.Master)
	}
	return s
}

// sentTail is the tail of T14 and T14b for j (K19), computed without an
// agent call: a planned job whose lock record stands T14t; an emergency
// job whose old master is connected and reads Terkunci T14u.
func (r *Runner) sentTail(j store.Job, st jobState) (string, error) {
	master := st.MasterServerID
	if master == 0 {
		master = j.ServerID
	}
	switch j.Kind {
	case KindPromotePlanned:
		recs, err := r.LockRecords(master)
		if err != nil {
			return "", err
		}
		for _, rec := range recs {
			if rec.JobID == j.ID {
				return PromoteSentTailPlannedLocked, nil
			}
		}
	case KindPromoteEmergency:
		if v, ok := r.view(master); ok && r.d.Hub.IsOnline(master) && v.Status.Code == monitor.StatusLocked {
			return PromoteSentTailOldMasterLocked, nil
		}
	}
	return "", nil
}

// sentRetry is the verdict a Retry of a sent promote job read before
// startMu: the job as it was read, and ok when one was read.
type sentRetry struct {
	job     store.Job
	verdict sentVerdict
	over    time.Time
	ok      bool
}

// sentRetryVerdict reads, before startMu, the verdict of a failed or
// partial promote job whose mysql.promote was sent and that is not past
// the point of no return; ok is false for any other job.
func (r *Runner) sentRetryVerdict(jobID string) sentRetry {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil || !isPromote(j.Kind) || (j.Status != store.JobFailed && j.Status != store.JobPartial) {
		return sentRetry{}
	}
	st, ok := sentNotPast(j)
	if !ok {
		return sentRetry{}
	}
	v, over := r.promoteSentVerdict(r.base, j, st)
	return sentRetry{job: j, verdict: v, over: over, ok: true}
}

// sentNotPast is j's state when j's mysql.promote was sent and it is not
// past the point of no return.
func sentNotPast(j store.Job) (jobState, bool) {
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return st, false
	}
	return st, st.PromoteSent && !st.PastPoint
}

// settleSentRetry applies sr, the verdict read for j before startMu, to
// the Retry of j, a promote job whose mysql.promote was sent and that is
// not past the point of no return; callers hold startMu. The job read
// again must be the one the verdict was read for (P6). Writable: the
// point of no return is stored and the Retry goes on (no token, no gate);
// the returned job is the one stored. Over: the job is stored not sent
// first (or nothing changes), then failed with its promotion, and T13 or
// T13b is returned; undo, when set, lifts a planned promotion's lock
// after startMu is released (the master is marked in flight until then).
// Unreadable and running refuse with T14 and T14b, changing nothing.
func (r *Runner) settleSentRetry(j store.Job, st jobState, sr sentRetry) (store.Job, func(), error) {
	if !sr.ok || sr.job.ParamsJSON != j.ParamsJSON || sr.job.Status != j.Status || sr.job.CurrentStep != j.CurrentStep {
		return j, nil, &PromoteSentError{Code: PromoteSentChanged}
	}
	cand := r.nameOf(st.CandidateServerID)
	master := st.MasterServerID
	if master == 0 {
		master = j.ServerID
	}
	switch sr.verdict {
	case sentWritable:
		st.PastPoint = true
		b, err := json.Marshal(st)
		if err != nil {
			return j, nil, err
		}
		if err := r.d.Store.SetJobParams(j.ID, string(b)); err != nil {
			return j, nil, err
		}
		j.ParamsJSON = string(b)
		r.appendJobLog(j.ID, "warn", "Kandidat sudah menerima tulisan: promote dilanjutkan tanpa membuka kunci master.")
		return j, nil, nil
	case sentOver:
		return r.failSentOver(j, st, cand, master)
	}
	tail, err := r.sentTail(j, st)
	if err != nil {
		return j, nil, err
	}
	e := &PromoteSentError{Code: PromoteSentUnreadable, Candidate: cand, Tail: tail, Master: r.nameOf(master)}
	if sr.verdict == sentRunning {
		e.Code, e.Over = PromoteSentRunning, sr.over
	}
	return j, nil, e
}

// failSentOver fails j, whose mysql.promote is proven to have changed
// nothing at the candidate (sentOver). The job's params ("not sent"), its
// status (failed, with T13 or T13b) and its promotion's status (failed)
// are written in one store transaction (store.FailSentPromote, through
// the failSentStore seam): when it fails nothing has changed, the job is
// still partial or failed and stored sent, the master stays locked, and
// the next Ulangi reads the candidate again (P6 when the job changed
// meanwhile). Only after it is the lock of a planned promotion lifted, by
// promoteUndo after startMu, with the master marked in flight meanwhile
// (P11 for its other actions); an unlock that fails leaves the lock
// record standing, so "Buka kunci promote" is offered (row 2), as T13b
// says.
func (r *Runner) failSentOver(j store.Job, st jobState, cand string, master int64) (store.Job, func(), error) {
	st.PromoteSent, st.PromoteUnanswered, st.PromoteSentAt = false, false, nil
	b, err := json.Marshal(st)
	if err != nil {
		return j, nil, err
	}
	e := &PromoteSentError{Code: PromoteSentOverEmergency, Candidate: cand}
	if j.Kind == KindPromotePlanned {
		e.Code = PromoteSentOverPlanned
	}
	fail := r.d.Store.FailSentPromote
	if r.failSentStore != nil {
		fail = r.failSentStore
	}
	if err := fail(j.ID, j.ParamsJSON, string(b), j.CurrentStep, e.Error(), st.PromotionID, r.now()); err != nil {
		if errors.Is(err, store.ErrJobChanged) {
			return j, nil, &PromoteSentError{Code: PromoteSentChanged}
		}
		return j, nil, err
	}
	j.ParamsJSON = string(b)
	r.appendJobLog(j.ID, "warn", e.Error())
	r.d.Publish("job_update", map[string]any{"id": j.ID, "kind": j.Kind, "server_id": j.ServerID,
		"status": store.JobFailed, "step": j.CurrentStep, "error": e.Error()})
	if j.Kind != KindPromotePlanned || !st.LockTried {
		return j, nil, e
	}
	r.unlocking[master] = true
	jr := &jobRun{r: r, job: j, st: st, idx: j.CurrentStep}
	undo := func() {
		defer func() {
			r.startMu.Lock()
			delete(r.unlocking, master)
			r.startMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.base, cancelTimeout+r.unlockTimeout)
		defer cancel()
		jr.promoteUndo(ctx)
	}
	return j, undo, e
}
