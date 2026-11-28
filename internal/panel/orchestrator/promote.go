package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/redact"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Promotion of a replica to master (spec 5.3): the jobs promote_planned
// (the master answers; it is locked, the candidate catches up, then opens
// writes) and promote_emergency (the master is gone; the candidate applies
// what it received and opens writes). Both are recorded in promotions from
// the start; one runs per topology at a time.
//
// The agents verify each change themselves (spec 5.1); the panel only
// drives the order and records the outcome. Until the candidate opens
// writes (mysql.promote, the point of no return) a failure undoes the
// master's lock and fails the job. After it nothing is undone: a step that
// cannot finish (the quarantine of the old master, repointing a replica)
// leaves the job "partial", and Retry continues from that step; the
// steps are safe to run again (mysql.promote resumes from the agent's
// marker, the quarantine and the swap of the panel's data are idempotent,
// repointed replicas are not repointed again).

const (
	// promoteLockSec is how long the master's promote lock lasts before
	// its agent's guard checks on it (ruling R2: 30 minutes).
	promoteLockSec = 1800
	// defaultPromoteCatchUp bounds "Replika mengejar" (ruling R2).
	defaultPromoteCatchUp = 10 * time.Minute
	// promoteSyncWindowSec is one mysql.waitSynced window of the catch-up;
	// the job logs the progress after each.
	promoteSyncWindowSec = 5
	// repointTimeout bounds mysql.repoint, which probes the old source
	// three times before it touches anything.
	repointTimeout = 3 * time.Minute
	// promoteClockSkew is how much earlier than the promotion's start (the
	// panel's clock) the candidate's own record of it (its clock) may be
	// and still be taken as this promotion (PromoteParams.NotBefore).
	promoteClockSkew = 5 * time.Minute
)

// Promotion statuses (promotions.status).
const (
	promotionRunning = "running"
	promotionPartial = "partial"
	promotionDone    = "done"
	promotionFailed  = "failed"
)

// Step titles of the promotion jobs.
const (
	stepPromoteAccount    = "Buat akun replikasi di kandidat"
	stepPromoteLock       = "Kunci master"
	stepPromoteCatchUp    = "Replika mengejar"
	stepPromoteNewMaster  = "Jadikan master baru"
	stepPromoteQuarantine = "Karantina master lama"
	stepPromoteSwap       = "Perbarui data panel"
	stepPromoteRepoint    = "Arahkan replika lain"
	stepPromoteFinish     = "Selesai"
)

func promotePlannedStepNames() []string {
	return []string{stepPromoteAccount, stepPromoteLock, stepPromoteCatchUp, stepPromoteNewMaster,
		stepPromoteQuarantine, stepPromoteSwap, stepPromoteRepoint, stepPromoteFinish}
}

func promoteEmergencyStepNames() []string {
	return []string{stepPromoteNewMaster, stepPromoteSwap, stepPromoteQuarantine, stepPromoteRepoint, stepPromoteFinish}
}

// PromoteStepNames are the steps of a promote job in mode
// (proto.PromotePlanned or proto.PromoteEmergency), in order; the
// consideration screen lists them.
func PromoteStepNames(mode string) []string {
	if mode == proto.PromoteEmergency {
		return promoteEmergencyStepNames()
	}
	return promotePlannedStepNames()
}

func (jr *jobRun) promotePlannedSteps() []step {
	return []step{
		{stepPromoteAccount, jr.promoteAccount},
		{stepPromoteLock, jr.promoteLock},
		{stepPromoteCatchUp, jr.promoteCatchUp},
		{stepPromoteNewMaster, jr.promoteNewMaster},
		{stepPromoteQuarantine, jr.promoteQuarantine},
		{stepPromoteSwap, jr.promoteSwap},
		{stepPromoteRepoint, jr.promoteRepoint},
		{stepPromoteFinish, jr.promoteFinish},
	}
}

func (jr *jobRun) promoteEmergencySteps() []step {
	return []step{
		{stepPromoteNewMaster, jr.promoteNewMaster},
		{stepPromoteSwap, jr.promoteSwap},
		{stepPromoteQuarantine, jr.promoteQuarantine},
		{stepPromoteRepoint, jr.promoteRepoint},
		{stepPromoteFinish, jr.promoteFinish},
	}
}

// PromoteParams are what the user chose on the consideration screen.
type PromoteParams struct {
	MasterServerID, CandidateServerID int64
	// Mode is proto.PromotePlanned or proto.PromoteEmergency; "" takes the
	// mode the master's status allows (PromoteView.Mode). A mode the
	// status does not allow is refused.
	Mode string
	// AcceptPartial: the user agreed that the databases the candidate
	// does not replicate will not be on the new master.
	AcceptPartial bool
	// AcceptUnreachable are the replicas the user agreed to repoint later
	// (agent offline or its probe failed): they become needs_repoint.
	AcceptUnreachable []int64
	// AcceptBranched: the replicas are branched (PromoteView.Branched) and
	// the user chose this candidate, accepting that the others holding
	// transactions it lacks are rebuilt (repoint finds them needs_rebuild;
	// spec 5.2).
	AcceptBranched bool
	// BackupAck: the user ticked that the master is backed up (form
	// backup_ack=1); a planned promote needs it when the gate's backup row
	// asks for it (no backup folder set, GateResult.NeedsBackupAck).
	BackupAck bool
	// OldMasterDeadAck: the user ticked that the old master is dead or
	// cut off from the applications (form old_master_dead=1, D5); an
	// emergency promote needs it (ErrOldMasterAck), a planned one does
	// not ask.
	OldMasterDeadAck bool
}

// ErrOldMasterAck: an emergency promote without the tick that the old
// master is dead or cut off from the applications. While the old master
// still takes writes, a second master loses data.
var ErrOldMasterAck = errors.New("centang master lama mati belum diisi")

// StartPromote starts the promotion of p.CandidateServerID to master of
// p.MasterServerID's topology and records it in promotions. It refuses
// what the consideration data (PromoteView) does not allow: a candidate
// that is not eligible, branched replicas, a partial candidate without
// AcceptPartial, unreachable replicas not in AcceptUnreachable, a mode the
// master's status does not permit, an emergency without OldMasterDeadAck
// (ErrOldMasterAck), an outdated agent (SEC-62), a topology with an
// unfinished job or a cutover still pending, an emergency while an earlier
// planned promote's lock is within its Until (SEC-66), a planned one
// while such a lock record stands or the master reads Terkunci (T12c,
// T12d), and another replica read writable or not readable (R-198: T24,
// T24b, T24c); promoteRefusals holds those refusals, the gate follows.
func (r *Runner) StartPromote(ctx context.Context, p PromoteParams) (string, error) {
	if p.MasterServerID == p.CandidateServerID {
		return "", errors.New("kandidat tidak boleh master itu sendiri")
	}
	// Refusals that need no agent come first; they are checked again
	// under startMu.
	if err := r.refuseUnlockInFlightNow(p.MasterServerID); err != nil {
		return "", err
	}
	if topo, err := r.d.Store.GetTopologyByMaster(p.MasterServerID); err == nil && topo.CutoverPending {
		return "", errCutoverStillPending
	} else if err == nil {
		if err := r.refuseQuarantinedCandidate(p.CandidateServerID); err != nil {
			return "", err
		}
		if p.Mode == proto.PromotePlanned {
			if err := r.refuseOldMasterPending(topo); err != nil {
				return "", err
			}
		}
	}
	// The consideration data asks the agents; it is read before startMu.
	view, err := r.PromoteView(ctx, p.MasterServerID)
	if err != nil {
		return "", err
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	topo, err := r.d.Store.GetTopologyByMaster(p.MasterServerID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("server ini bukan master")
	}
	if err != nil {
		return "", err
	}
	cand, mode, err := r.promoteRefusals(p, view, topo, 0)
	if err != nil {
		return "", err
	}
	// The precondition gate (spec 5.7), last before the first write; an
	// emergency is never refused by a row.
	if mode == proto.PromotePlanned {
		res, err := r.refuseRedGate(GateInput{Action: GatePromotePlanned, MasterID: p.MasterServerID,
			TargetID: p.CandidateServerID})
		if err != nil {
			return "", err
		}
		if res.NeedsBackupAck() && !p.BackupAck {
			return "", ErrBackupAck
		}
	}

	promID, err := r.d.Store.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: mode, OldMasterServerID: p.MasterServerID,
		NewMasterServerID: p.CandidateServerID, StartedAt: r.now(), LastMasterGTID: view.LastMasterGTID, Status: promotionRunning})
	if err != nil {
		return "", err
	}
	st := jobState{MasterServerID: p.MasterServerID, CandidateServerID: p.CandidateServerID, PromotionID: promID,
		PartialCandidate: cand.Partial, OldSourceHost: topo.SourceHost, OldSourcePort: topo.SourcePort,
		LastMasterGTID: view.LastMasterGTID, AcceptUnreachable: p.AcceptUnreachable, AcceptBranched: p.AcceptBranched}
	if mode == proto.PromotePlanned {
		// Its first confirmation: its Ulangi before the send is bound to
		// confirmMaxAge from the last one (T12).
		st.ConfirmedAt = r.now()
	}
	kind, names := KindPromotePlanned, promotePlannedStepNames()
	if mode == proto.PromoteEmergency {
		kind, names = KindPromoteEmergency, promoteEmergencyStepNames()
	}
	id, err := r.createJob(kind, p.MasterServerID, topo.ID, st, names)
	if err != nil {
		r.updatePromotion(promID, func(pr *store.Promotion) { pr.Status = promotionFailed })
		return "", err
	}
	r.updatePromotion(promID, func(pr *store.Promotion) { pr.JobID = id })
	r.log.Info("promote started", "job", id, "mode", mode, "master", p.MasterServerID, "candidate", p.CandidateServerID)
	return id, nil
}

// lockCallSlack is how long after LockTriedAt a lock whose answer was lost
// may have been taken (the call's bound), so its deadline is at most
// LockTriedAt + promoteLockSec + lockCallSlack.
const lockCallSlack = 5 * time.Minute

// verbEmergency is refuseWhileLockMayStand's what for an emergency
// promotion; any other what loads data on a replica.
const verbEmergency = "Promote darurat"

// retryLoadVerb is refuseWhileLockMayStand's what for an Ulangi of j, ""
// when j's kind does not make a replica writable (fix round 1).
func retryLoadVerb(j store.Job) string {
	switch j.Kind {
	case KindResyncReplica:
		return "Resync ulang"
	case KindCreateReplica:
		return "Buat replika"
	case KindAddDatabases:
		return "Tambah database"
	case KindRemoveDatabases:
		var st jobState
		if json.Unmarshal([]byte(j.ParamsJSON), &st) == nil && !st.DropData {
			return ""
		}
		return "Lepas database"
	}
	return ""
}

// refuseRetryWhileLockMayStand is refuseWhileLockMayStand for an Ulangi
// of j: a failed load retried inside the window lifts read-only as a new
// one would.
func (r *Runner) refuseRetryWhileLockMayStand(j store.Job) error {
	what := retryLoadVerb(j)
	if what == "" || j.TopologyID == 0 {
		return nil
	}
	topo, err := r.d.Store.GetTopology(j.TopologyID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.refuseWhileLockMayStand(topo.MasterServerID, what)
}

// refuseWhileLockMayStand refuses what (an emergency promotion of
// masterID, or a job that makes one of its replicas writable for a while)
// while a lock record of a planned promotion of that master may stand
// (every one, not only the latest). A replica the guard has seen that is
// writable at the deadline (a resync or dump load, Buat replika, Tambah
// database, Lepas database with its data) makes the guard quarantine a
// healthy master instead of unlocking it (4b final review, T-4): a load
// is refused with a *LockStandsError (T15b) naming the newest record's
// job (K13) for as long as a record stands (settleLockRecords; callers
// hold startMu). Inside the record's Until nothing proves it gone, so the
// clock rule holds whatever the report says; past it only an unlock or a
// live report of the master with both flags off does (N-2): no live
// report means refused. An emergency promotion keeps the clock rule only
// (SEC-66): it is refused while a record is within its Until, the lock's
// clamped deadline plus the guard's grace (proto.PromoteLockGrace), its
// text ending with T15, and never longer: the master may be dead.
func (r *Runner) refuseWhileLockMayStand(masterID int64, what string) error {
	if what != verbEmergency {
		recs, err := r.settleLockRecords(masterID)
		if err != nil || len(recs) == 0 {
			return err
		}
		return &LockStandsError{Code: "load", Master: r.nameOf(masterID), What: what, JobID: recs[0].JobID}
	}
	recs, err := r.LockRecords(masterID)
	if err != nil {
		return err
	}
	now := r.now()
	var until time.Time
	for _, rec := range recs {
		if now.Before(rec.Until) && rec.Until.After(until) {
			until = rec.Until
		}
	}
	if until.IsZero() {
		return nil
	}
	at := until.Local().Format("02-01-2006 15:04:05")
	return fmt.Errorf("master %s mungkin masih terkunci oleh promote terencana sebelumnya: kuncinya belum terbukti dibuka. "+
		"%s baru bisa dijalankan mulai %s, saat agent master sudah membuka kunci itu sendiri bila kandidat lama "+
		"masih replika. %s", r.nameOf(masterID), what, at, textEmergencyLockStands)
}

// refusePlannedWhileLockStands refuses a new planned promotion of
// masterID (S-2): while a lock record of an earlier one stands
// (settleLockRecords; callers hold startMu) with T12c naming the newest
// record's job, also one whose mysql.promote was sent; else while the
// Monitor reads the master Terkunci with T12d. A second planned promote
// on top of a standing lock can strand the first.
func (r *Runner) refusePlannedWhileLockStands(masterID int64) error {
	recs, err := r.settleLockRecords(masterID)
	if err != nil {
		return err
	}
	if len(recs) > 0 {
		return &LockStandsError{Code: "planned", Master: r.nameOf(masterID), JobID: recs[0].JobID}
	}
	if v, ok := r.view(masterID); ok && v.Status.Code == monitor.StatusLocked {
		return &LockStandsError{Code: "master_locked", Master: r.nameOf(masterID)}
	}
	return nil
}

// refuseOldMasterPending is errOldMasterPending while topo's old master
// is pending (pendingOldMaster).
func (r *Runner) refuseOldMasterPending(topo store.Topology) error {
	old, ok, err := r.pendingOldMaster(topo)
	if err != nil {
		return err
	}
	if ok {
		return oldMasterPendingError{old.Name}
	}
	return nil
}

// refuseQuarantinedCandidate refuses a quarantined candidate as the
// consideration screen does (reasonQuarantined), before any agent is asked.
func (r *Runner) refuseQuarantinedCandidate(id int64) error {
	srv, err := r.d.Store.GetServer(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if srv.Role == store.RoleQuarantined {
		return fmt.Errorf("%s tidak bisa dipromosikan: %s", srv.Name, reasonQuarantined)
	}
	return nil
}

// errOldMasterPending (T4) refuses a planned promotion while the
// topology's old master is still quarantined; Error names it.
var errOldMasterPending error = oldMasterPendingError{}

// oldMasterPendingError is errOldMasterPending naming the old master;
// every such error is errOldMasterPending for errors.Is.
type oldMasterPendingError struct{ name string }

func (e oldMasterPendingError) Error() string {
	return fmt.Sprintf("master lama %s dari promote sebelumnya belum dibangun ulang atau dihapus; selesaikan itu dulu", e.name)
}

func (oldMasterPendingError) Is(target error) bool {
	_, ok := target.(oldMasterPendingError)
	return ok
}

var errCutoverStillPending = errors.New("promote sebelumnya masih menunggu pengalihan aplikasi; tekan \"Sudah saya alihkan\" dulu")

// candidateUnreachable: the replica's agent is offline or could not
// answer its probe of the master.
func candidateUnreachable(c Candidate) bool {
	return c.Reason == reasonAgentOffline || c.ProbeErr != ""
}

// agentOutdated reports whether srv's agent is of another version than the
// panel (SEC-62); never when the panel's version is unknown.
func (r *Runner) agentOutdated(srv store.Server) bool {
	return r.d.Version != "" && srv.AgentVersion != r.d.Version
}

func (r *Runner) nameOf(id int64) string {
	srv, err := r.d.Store.GetServer(id)
	if err != nil {
		return fmt.Sprintf("#%d", id)
	}
	return srv.Name
}

// ---- the steps ----

func (jr *jobRun) candidate() int64 { return jr.st.CandidateServerID }

// promoteCmdErr turns the unknown_command of an agent older than the
// promotion commands into a clear message, and marks the agent as needing
// an update.
func (r *Runner) promoteCmdErr(serverID int64, err error) error {
	if !errors.Is(err, &proto.Error{Code: proto.CodeUnknownCommand}) {
		return err
	}
	if r.d.Alerts != nil {
		r.d.Alerts.AgentNeedsUpgrade(serverID)
	}
	return oldPromoteAgent{r.nameOf(serverID)}
}

// oldPromoteAgent is promoteCmdErr's step error: the agent of the server
// named name does not know a promotion command. Its text ends "lalu
// Ulangi."; promoteAgainErr ends it otherwise where Ulangi is refused.
type oldPromoteAgent struct{ name string }

func (e oldPromoteAgent) text(then string) string {
	return fmt.Sprintf("Agent di %s versi lama dan belum mengenal promote. "+
		"Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, %s", e.name, then)
}

func (e oldPromoteAgent) Error() string { return e.text("lalu Ulangi.") }

// Unwrap makes it a stepError: its text is the step's whole message.
func (e oldPromoteAgent) Unwrap() error { return stepError(e.Error()) }

// promoteAgainErr is err for promoteNewMaster: an emergency job that fails
// there before the point of no return has no Ulangi (promoteRetryRefused),
// so an oldPromoteAgent text says to run promote again instead.
func (jr *jobRun) promoteAgainErr(err error) error {
	var old oldPromoteAgent
	if jr.job.Kind == KindPromoteEmergency && errors.As(err, &old) {
		return stepError(old.text("lalu jalankan promote lagi."))
	}
	return err
}

// candidatePort is the candidate's MySQL port, read once (mysql.status).
func (jr *jobRun) candidatePort(ctx context.Context) (int, error) {
	if jr.st.CandidatePort != 0 {
		return jr.st.CandidatePort, nil
	}
	var cs proto.MySQLStatus
	if err := jr.call(ctx, jr.candidate(), "mysql.status", nil, &cs); err != nil {
		return 0, err
	}
	jr.setCandidatePort(cs.Port)
	return jr.st.CandidatePort, nil
}

func (jr *jobRun) setCandidatePort(port int) {
	if port == 0 {
		port = 3306
	}
	jr.st.CandidatePort = port
	jr.save()
}

// newMasterAddr is where the other servers reach the candidate's MySQL.
func (jr *jobRun) newMasterAddr(ctx context.Context) (string, int, error) {
	srv, err := jr.r.d.Store.GetServer(jr.candidate())
	if err != nil {
		return "", 0, err
	}
	port, err := jr.candidatePort(ctx)
	if err != nil {
		return "", 0, err
	}
	return jr.r.sourceHost(srv), port, nil
}

// candidateCred is how the agent of from logs in to the candidate.
func (jr *jobRun) candidateCred(ctx context.Context, from int64) (proto.PeerCred, error) {
	host, port, err := jr.newMasterAddr(ctx)
	if err != nil {
		return proto.PeerCred{}, err
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return proto.PeerCred{}, err
	}
	return jr.r.peerCred(ctx, from, jr.candidate(), host, port, topo.ReplUser, password)
}

// promoteAccount: a candidate that replicates only some databases may lack
// the replication account (it lives in the mysql schema the filter leaves
// out); the master's agent logs in to it with that account, so it is
// made there first. A full replica got it from the master.
func (jr *jobRun) promoteAccount(ctx context.Context, _ bool) (outcome, error) {
	if !jr.st.PartialCandidate {
		return skipped("kandidat mengikuti semua database; akun replikasi sudah tersalin dari master"), nil
	}
	var cs proto.MySQLStatus
	if err := jr.call(ctx, jr.candidate(), "mysql.status", nil, &cs); err != nil {
		return outcome{}, err
	}
	jr.setCandidatePort(cs.Port)
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	err = jr.call(ctx, jr.candidate(), "mysql.createReplUser",
		proto.ReplUserParams{User: topo.ReplUser, Password: password, FromHost: "%", RequireSSL: cs.TLSAvailable}, nil)
	switch {
	case err == nil:
		jr.logf("info", "Akun replikasi %s dibuat di kandidat %s.", topo.ReplUser, jr.serverName(jr.candidate()))
		return done("akun %s dibuat", topo.ReplUser), nil
	case strings.Contains(err.Error(), accountExistsOnPinnedMaster):
		// The agent never alters an existing account (SEC-42).
		jr.logf("warn", "Akun replikasi %s sudah ada di kandidat dan tidak diubah agent; promote memakai akun itu apa adanya.", topo.ReplUser)
		return done("akun %s sudah ada", topo.ReplUser), nil
	}
	return outcome{}, err
}

// promoteLock locks the master for the promotion (ruling R2: 1800 s).
//
// The lock record (promote_lockrecord.go) is never wiped by an error: the
// master's agent can answer one after the lock is set. A refusal by the
// agent (lockRefused: nothing was locked by this call) puts the times
// back to what they were before it, so that a refused relock does not
// move the window of a lock that may still stand (SEC-289). Any other
// error may have set a lock now: the time it was asked for stays, and the
// deadline is forgotten, so the record counts from that time (lockUntil).
// The deadline the agent answers is cut to lockDeadlineCap (SEC-290); the
// cut value is stored and sent to the candidate.
func (jr *jobRun) promoteLock(ctx context.Context, _ bool) (outcome, error) {
	cred, err := jr.candidateCred(ctx, jr.master())
	if err != nil {
		return outcome{}, err
	}
	var res proto.LockForPromoteResult
	oldLockTried, oldUnlocked := jr.st.LockTried, jr.st.LockUnlocked
	oldTried, oldDeadline := jr.st.LockTriedAt, jr.st.LockDeadline
	tried := jr.r.now()
	// The record stands before the lock is asked for (C-1): from tried,
	// with no deadline (lockUntil), until the answer says more. When it
	// cannot be stored, nothing is sent: a lock the panel has no record of
	// would let a load or an emergency start on it.
	jr.st.LockTried, jr.st.LockTriedAt, jr.st.LockUnlocked, jr.st.LockDeadline = true, &tried, false, nil
	if err := jr.saveState(); err != nil {
		jr.st.LockTried, jr.st.LockTriedAt, jr.st.LockUnlocked, jr.st.LockDeadline = oldLockTried, oldTried, oldUnlocked, oldDeadline
		return outcome{}, err
	}
	if err := jr.call(ctx, jr.master(), "mysql.lockForPromote",
		proto.LockForPromoteParams{Candidate: cred, TimeoutSec: promoteLockSec}, &res); err != nil {
		if lockRefused(err) {
			if oldTried != nil {
				jr.st.LockTriedAt, jr.st.LockDeadline = oldTried, oldDeadline
			}
		} else {
			jr.st.LockDeadline = nil
		}
		jr.save()
		return outcome{}, jr.r.promoteCmdErr(jr.master(), err)
	}
	deadline := res.Deadline
	if limit := lockDeadlineCap(tried); deadline.After(limit) {
		jr.r.log.Warn("promote lock deadline from the master's agent cut", "job", jr.job.ID,
			"deadline", res.Deadline, "cut_to", limit)
		deadline = limit
	}
	jr.st.LockDeadline = &deadline
	if res.GTIDExecuted != "" {
		jr.st.LastMasterGTID = res.GTIDExecuted
	}
	jr.save()
	lockedAt := res.LockedAt
	jr.r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) {
		p.LockAt, p.LastMasterGTID = &lockedAt, jr.st.LastMasterGTID
	})
	jr.logf("warn", "Master dikunci pukul %s (sampai %s): aplikasi tidak bisa menulis sampai master baru dibuka.",
		lockedAt.Local().Format("15:04:05"), deadline.Local().Format("15:04:05"))
	return done("dikunci pukul %s", lockedAt.Local().Format("15:04:05")), nil
}

// lockRefused reports whether err from mysql.lockForPromote (as jr.call
// returns it) is the master's agent refusing the lock before taking it:
// an answer from the agent (hub.SentByAgent, or labelled as its words by
// jr.call) with a code it gives only to a refusal: bad_params, which the
// agent's lockForPromote returns only before it writes the lock, or an
// unknown command. Its other errors may come after the lock was set, and
// no answer says nothing.
func lockRefused(err error) bool {
	_, labelled := errors.AsType[agentTextError](err)
	if !labelled && !hub.SentByAgent(err) {
		return false
	}
	var pe *proto.Error
	if !errors.As(err, &pe) {
		return false
	}
	return pe.Code == proto.CodeBadParams || pe.Code == proto.CodeUnknownCommand
}

// locked reports whether the master's promote lock is held as far as the
// job knows: taken and its deadline not passed (past it the master's guard
// may already have lifted it).
func (jr *jobRun) locked() bool {
	return jr.st.LockDeadline != nil && jr.r.now().Before(*jr.st.LockDeadline)
}

// relock takes the master's promote lock again when a failure lifted it
// (a retry of "Replika mengejar" or "Jadikan master baru").
func (jr *jobRun) relock(ctx context.Context) error {
	if jr.job.Kind != KindPromotePlanned || jr.locked() {
		return nil
	}
	jr.logf("info", "Kunci master sudah dibuka oleh percobaan sebelumnya; master dikunci lagi.")
	_, err := jr.promoteLock(ctx, false)
	return err
}

// promoteCatchUp waits until the candidate executed the locked master's
// set: mysql.waitSynced in short windows (the existing catch-up command,
// ruling R2), bounded by promoteCatchUp, logging the progress.
func (jr *jobRun) promoteCatchUp(ctx context.Context, _ bool) (outcome, error) {
	if err := jr.relock(ctx); err != nil {
		return outcome{}, err
	}
	if jr.st.LastMasterGTID == "" {
		return outcome{}, errors.New("gtid_executed master saat dikunci tidak diketahui")
	}
	deadline := jr.r.now().Add(jr.r.promoteCatchUp)
	for {
		var w proto.WaitSyncedResult
		if err := jr.callWithin(ctx, (promoteSyncWindowSec+60)*time.Second, jr.candidate(), "mysql.waitSynced",
			proto.WaitSyncedParams{GTIDSet: jr.st.LastMasterGTID, TimeoutSec: promoteSyncWindowSec}, &w); err != nil {
			return outcome{}, err
		}
		if w.Synced {
			jr.logf("info", "Kandidat sudah menerapkan semua transaksi master.")
			return done("kandidat sinkron dengan master"), nil
		}
		s := w.Status
		if s.LastIOErrno != 0 || s.LastSQLErrno != 0 || s.SQLRunning != "Yes" {
			return outcome{}, jr.notSynced(jr.candidate(), w, fmt.Sprintf("replikasi kandidat tidak berjalan (IO %s, SQL %s)", s.IORunning, s.SQLRunning))
		}
		if !jr.r.now().Before(deadline) {
			return outcome{}, fmt.Errorf("kandidat belum menerapkan semua transaksi master dalam %s", jr.r.promoteCatchUp)
		}
		msg := fmt.Sprintf("kandidat masih mengejar master: tertinggal %d transaksi", gtidMissing(jr.st.LastMasterGTID, w.Executed))
		jr.logf("info", "%s", msg)
		jr.setStep(store.StepRunning, msg)
		select {
		case <-ctx.Done():
			return outcome{}, ctx.Err()
		case <-time.After(jr.r.pollEvery):
		}
	}
}

// promoteNewMaster runs mysql.promote on the candidate as an agent job
// (ruling R2); run again after a failure it resumes from the agent's
// marker.
func (jr *jobRun) promoteNewMaster(ctx context.Context, _ bool) (outcome, error) {
	mode := proto.PromotePlanned
	if jr.job.Kind == KindPromoteEmergency {
		mode = proto.PromoteEmergency
	}
	if _, err := jr.candidatePort(ctx); err != nil {
		return outcome{}, err
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	old, err := jr.r.peerCred(ctx, jr.candidate(), jr.master(), jr.st.OldSourceHost, jr.st.OldSourcePort, topo.ReplUser, password)
	if err != nil {
		return outcome{}, jr.promoteAgainErr(err)
	}
	params := proto.PromoteParams{Mode: mode, OldSource: old, ReplUser: topo.ReplUser, ReplPassword: password}
	if mode == proto.PromotePlanned {
		// A retry after a failure that lifted the lock takes it again and
		// lets the candidate catch up first. Not while an earlier
		// mysql.promote may have opened writes (PromoteSent): the master
		// may be quarantined by its guard by now and refuse a lock, and the
		// candidate's agent verifies the request itself (it answers from
		// its promotion record, resumes, or refuses).
		if !jr.locked() && !jr.st.PromoteSent {
			if _, err := jr.promoteCatchUp(ctx, false); err != nil {
				return outcome{}, err
			}
		}
		// The deadline is the master's clock; the seconds left, counted
		// here now, are what the candidate measures its margin with on
		// its own clock (SEC-73).
		params.LockDeadline = jr.st.LockDeadline
		if d := jr.st.LockDeadline; d != nil {
			left := max(int(d.Sub(jr.r.now())/time.Second), 0)
			params.LockRemainingSec = &left
		}
	}
	if prom, err := jr.r.d.Store.GetPromotion(jr.st.PromotionID); err == nil {
		params.NotBefore = prom.StartedAt.Add(-promoteClockSkew)
	}
	// "Sent" is stored before the command goes out (C-1), or it is not
	// sent: a promote the stored state calls not sent would get the gate
	// and the token on its Ulangi, and could be relocked and sent again.
	// runAgentJob stores it (markPromoteSending), with the send's time
	// and its start unanswered; promoteFailed settles a failure from
	// the candidate's evidence (promoteSentVerdict).
	var res proto.PromoteResult
	if err := jr.runAgentJob(ctx, jr.candidate(), "mysql.promote", params, &res); err != nil {
		return outcome{}, jr.promoteAgainErr(jr.r.promoteCmdErr(jr.candidate(), err))
	}
	jr.st.PastPoint = true
	jr.save()
	jr.r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) { p.NewMasterGTID = res.GTIDExecuted })
	if res.Resumed {
		jr.logf("info", "Promote dilanjutkan dari langkah yang sudah membuka penulisan.")
	}
	if res.Mode != "" && res.Mode != mode {
		jr.logf("warn", "Agent mencatat promote ini sebagai %s.", agentPart(res.Mode, 64))
	}
	if res.Unapplied != "" {
		jr.logf("warn", "Transaksi yang tidak lengkap di relay log tidak diterapkan: %s", agentPart(res.Unapplied, 512))
	}
	name := jr.serverName(jr.candidate())
	jr.logf("info", "%s sekarang menerima tulisan sebagai master baru (%d transaksi relay log diterapkan).", name, res.Applied)
	return done("%s menjadi master baru", name), nil
}

// promoteStartRefused reports whether err from the mysql.promote start call
// is an answer from the agent (a refusal), as opposed to no answer at all.
// The hub synthesizes CodeTimeout (the call's deadline passed) and
// CodeOffline (the link was closed or the write failed) locally without a
// reply, so those say nothing about whether the agent started the job.
// The same codes sent by the agent are deliberately read as no answer as
// well: the start may already have opened writes.
func promoteStartRefused(err error) bool {
	var pe *proto.Error
	if !errors.As(err, &pe) {
		return false
	}
	return pe.Code != proto.CodeTimeout && pe.Code != proto.CodeOffline
}

// promoteQuarantine quarantines the old master under the new one. An
// offline agent defers it to OnOnline (the step is skipped); a failing
// command defers it too, and the job ends partial.
func (jr *jobRun) promoteQuarantine(ctx context.Context, _ bool) (outcome, error) {
	jr.undefer()
	old := jr.master()
	host, port, err := jr.newMasterAddr(ctx)
	if err != nil {
		return outcome{}, err
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	if !jr.r.d.Hub.IsOnline(old) {
		jr.r.markQuarantine(jr.st.PromotionID, old, true, "agent offline", nil, false)
		jr.logf("warn", "Agent master lama %s offline: karantina tertunda dan dikirim saat agent tersambung lagi. "+
			"Pastikan aplikasi dialihkan sebelum ia hidup kembali.", jr.serverName(old))
		return skipped("tertunda: agent master lama offline; dikirim saat tersambung lagi"), nil
	}
	unlock := jr.r.lockTarget(old)
	qres, err := jr.r.sendQuarantine(ctx, jr.st.PromotionID, old, jr.candidate(), host, port, topo.ReplUser, password)
	unlock()
	if errors.Is(err, proto.ErrOffline) && !hub.SentByAgent(err) {
		jr.logf("warn", "Agent master lama %s terputus: karantina tertunda dan dikirim saat agent tersambung lagi.", jr.serverName(old))
		return skipped("tertunda: agent master lama terputus; dikirim saat tersambung lagi"), nil
	}
	if err != nil {
		jr.deferStep()
		msg := truncate("karantina tertunda: "+jr.r.agentErrText(err), maxLogLine)
		jr.logf("error", "Karantina master lama gagal: %v. Dikirim lagi saat agent tersambung ulang, atau tekan Ulangi.", err)
		return outcome{store.StepFailed, msg}, nil
	}
	jr.logf("info", "Master lama %s dikarantina di bawah master baru.", jr.serverName(old))
	if !qres.StartLocked {
		// Pinned all the same, but an older agent or a refused lock leaves
		// MySQL free to start writable: say so in the job log. The reason
		// is the agent's free text, labelled as its words.
		jr.logf("warn", "Master lama dipin dikarantina, tetapi belum terjamin terkunci saat MySQL menyala: %s.%s",
			startLockReasonText(jr.serverName(old), qres.StartLockReason), operatorInitFilesText(qres.OperatorInitFiles))
	}
	return done("dikarantina"), nil
}

// promoteSwap records the new master in the panel's data in one
// transaction (store.SwapMaster) and marks the new master's inventory
// MasterReady: it was prepared as a replica of a prepared master and the
// promote created dbpanel_repl on it, so "Buat replika" and the old
// master's rebuild may start from it without "Siapkan master"; it then
// re-reads the new master's inventory (a failure is only logged). Running
// it again changes nothing.
func (jr *jobRun) promoteSwap(ctx context.Context, _ bool) (outcome, error) {
	host, port, err := jr.newMasterAddr(ctx)
	if err != nil {
		return outcome{}, err
	}
	// The address is recorded as the job's (K25): the master page names
	// it, not a user, as the author of the address replicas follow.
	if err := jr.r.d.Store.SwapMaster(jr.job.TopologyID, jr.master(), jr.candidate(), host, port, "job:"+jr.job.ID, jr.r.now()); err != nil {
		return outcome{}, err
	}
	if err := jr.r.updateInventory(jr.candidate(), func(inv *store.Inventory) { inv.MasterReady = true }); err != nil {
		return outcome{}, err
	}
	jr.r.d.Publish("mysql_inventory", map[string]any{"id": jr.candidate()})
	// The stored inventory is the one taken while the new master was an
	// empty replica target: read it again so "Buat replika" and the old
	// master's rebuild offer its databases. A failure leaves "Segarkan" to
	// the user and does not fail the step.
	if _, err := jr.r.RefreshInventory(ctx, jr.candidate()); err != nil {
		jr.logf("warn", "Inventaris MySQL %s belum diperbarui: %v. Tekan Segarkan di halaman servernya.",
			jr.serverName(jr.candidate()), err)
	}
	jr.r.replicaChanged(jr.candidate())
	jr.r.d.Publish("topology_update", map[string]any{"id": jr.job.TopologyID, "master_server_id": jr.candidate()})
	jr.logf("info", "Data panel diperbarui: %s master baru di %s:%d, %s dikarantina.",
		jr.serverName(jr.candidate()), host, port, jr.serverName(jr.master()))
	return done("master baru %s:%d", host, port), nil
}

// promoteRepoint points every other replica at the new master. An offline
// replica waits in needs_repoint (OnOnline repoints it), one holding
// transactions the new master lacks becomes needs_rebuild; neither fails
// the job. A replica whose agent answers but refuses stays needs_repoint
// and the job ends partial (Retry repoints it again).
func (jr *jobRun) promoteRepoint(ctx context.Context, _ bool) (outcome, error) {
	jr.undefer()
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return outcome{}, err
	}
	// A quarantined old master with a row is no replica: not repointed.
	reps, _, _, err := jr.r.promoteReplicas(topo.ID)
	if err != nil {
		return outcome{}, err
	}
	var ok, waiting, rebuild, failed int
	record := false
	// The new master records the replicas now connected to it once, after
	// the repoints and outside any target lock (FR-12).
	defer func() {
		if record {
			jr.r.recordReplicas(ctx, topo.MasterServerID)
		}
	}()
	for _, rep := range reps {
		if rep.ServerID == jr.candidate() || slices.Contains(jr.st.Repointed, rep.ServerID) {
			continue
		}
		name := jr.serverName(rep.ServerID)
		switch rep.State {
		case store.ReplicaReady, store.ReplicaAttention, store.ReplicaNeedsRepoint:
		default:
			jr.logf("warn", "Replika %s %s; tidak diarahkan ulang (bangun ulang dari master baru bila perlu).", name, replicaStateText(rep.State))
			continue
		}
		jr.setStep(store.StepRunning, "mengarahkan "+name)
		unlock := jr.r.lockTarget(rep.ServerID)
		out := jr.r.repointReplica(ctx, topo, rep, jr.st.PromotionID)
		unlock()
		record = record || out.record
		if ctx.Err() != nil {
			return outcome{}, ctx.Err()
		}
		switch {
		case out.offline:
			waiting++
			jr.logf("warn", "Replika %s tidak terjangkau: ditandai Perlu diarahkan ulang dan diarahkan ke master baru saat tersambung lagi.", name)
		case out.err != nil:
			failed++
			jr.logf("error", "Replika %s tidak bisa diarahkan ulang: %v", name, out.err)
		case out.state == store.ReplicaNeedsRebuild:
			rebuild++
			jr.st.Repointed = append(jr.st.Repointed, rep.ServerID)
			if out.errant != "" {
				jr.logf("warn", "Replika %s punya transaksi yang tidak ada di master baru (%s): perlu dibangun ulang dengan Resync ulang.",
					name, truncate(out.errant, 256))
			} else {
				jr.logf("warn", "Replika %s punya transaksi yang tidak ada di master baru: perlu dibangun ulang dengan Resync ulang.", name)
			}
		default:
			ok++
			jr.st.Repointed = append(jr.st.Repointed, rep.ServerID)
			if out.ioErr != "" {
				jr.logf("warn", "Replika %s diarahkan ke master baru, tetapi belum tersambung: %s", name, out.ioErr)
			} else {
				jr.logf("info", "Replika %s mengikuti master baru.", name)
			}
		}
		jr.save()
	}
	msg := fmt.Sprintf("%d diarahkan, %d menunggu tersambung, %d perlu dibangun ulang", ok, waiting, rebuild)
	if failed > 0 {
		jr.deferStep()
		return outcome{store.StepFailed, fmt.Sprintf("%s, %d gagal", msg, failed)}, nil
	}
	return done("%s", msg), nil
}

// promoteFinish opens the cutover screen (cutover_pending) and records how
// the promotion ended.
func (jr *jobRun) promoteFinish(ctx context.Context, _ bool) (outcome, error) {
	prom, err := jr.r.d.Store.GetPromotion(jr.st.PromotionID)
	if err != nil {
		return outcome{}, err
	}
	// A cutover confirmed while this job was partial stays confirmed.
	if prom.CutoverConfirmedAt == nil {
		if err := jr.r.d.Store.SetCutoverPending(jr.job.TopologyID, true); err != nil {
			return outcome{}, err
		}
	}
	status := promotionDone
	if len(jr.st.Deferred) > 0 {
		status = promotionPartial
	}
	now := jr.r.now()
	jr.r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) { p.Status, p.FinishedAt = status, &now })
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return outcome{}, err
	}
	jr.r.d.Publish("promotion_update", map[string]any{"topology_id": topo.ID, "status": status})
	jr.logf("info", "Arahkan semua aplikasi yang menulis ke %s:%d, lalu tekan \"Sudah saya alihkan\".", topo.SourceHost, topo.SourcePort)
	return done("alamat master baru %s:%d", topo.SourceHost, topo.SourcePort), nil
}

// deferStep records the current step as ended with work left; undefer
// clears it when the step runs again.
func (jr *jobRun) deferStep() {
	if !slices.Contains(jr.st.Deferred, jr.idx) {
		jr.st.Deferred = append(jr.st.Deferred, jr.idx)
	}
	jr.save()
}

func (jr *jobRun) undefer() {
	if i := slices.Index(jr.st.Deferred, jr.idx); i >= 0 {
		jr.st.Deferred = slices.Delete(jr.st.Deferred, i, i+1)
		jr.save()
	}
}

// endPartial ends a promotion whose steps all ran but some deferred work:
// the job is partial at the first such step, which Retry runs again.
func (jr *jobRun) endPartial() {
	jr.idx = slices.Min(jr.st.Deferred)
	msg := "promote selesai sebagian: " + jr.steps[jr.idx].title + " belum tuntas; tekan Ulangi untuk mencobanya lagi"
	jr.logf("warn", "Promote selesai sebagian: %s belum tuntas.", jr.steps[jr.idx].title)
	if jr.setJob(store.JobPartial, msg) {
		jr.r.jobFailed(jr.job.ServerID, jr.job.ID, jr.job.Kind, jr.steps[jr.idx].title, msg)
	}
}

func (jr *jobRun) setPromotionStatus(status string) {
	jr.r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) { p.Status = status })
}

// promoteFailed decides how a failed promotion step ends the job. Before
// the point of no return the master's lock is lifted (planned) and the
// job fails; after it nothing is undone and the job is partial. When
// mysql.promote was sent, the candidate's evidence decides
// (promoteSentVerdict): writable is past the point of no return; a job
// proven over is stored not sent first, and only then is the lock lifted
// (a failed store keeps the lock and ends partial: the next Ulangi reads
// the candidate again); unreadable, or a job that may still run, keeps
// the lock and ends partial, which blocks any other promotion of the
// topology until a retry settles it.
func (jr *jobRun) promoteFailed() string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(jr.lj.ctx), cancelTimeout)
	defer cancel()
	if !jr.st.PastPoint && jr.st.PromoteSent {
		v, _ := jr.r.promoteSentVerdict(ctx, jr.job, jr.st)
		switch v {
		case sentUnreadable:
			jr.logf("warn", "Keadaan kandidat tidak bisa dibaca; kandidat mungkin sudah menerima tulisan, jadi kunci master "+
				"tidak dibuka. Tekan Ulangi untuk melanjutkan promote.")
			jr.setPromotionStatus(promotionPartial)
			return store.JobPartial
		case sentWritable:
			jr.st.PastPoint = true
			jr.save()
			jr.logf("warn", "Kandidat sudah menerima tulisan: promote tidak dibatalkan. Tekan Ulangi untuk melanjutkan.")
		case sentRunning:
			jr.logf("warn", "Pekerjaan promote di kandidat masih berjalan, atau belum terbukti berakhir; kunci master tidak "+
				"dibuka. Tekan Ulangi untuk melanjutkan promote.")
			jr.setPromotionStatus(promotionPartial)
			return store.JobPartial
		default: // sentOver: the agent restored it; the next attempt sends anew.
			if err := jr.storeNotSent(); err != nil {
				jr.r.log.Warn("store promote not sent", "job", jr.job.ID, "err", err)
				jr.logf("warn", "Keadaan pekerjaan tidak bisa disimpan (%v), jadi kunci master tidak dibuka. Tekan Ulangi "+
					"untuk mencobanya lagi.", err)
				jr.setPromotionStatus(promotionPartial)
				return store.JobPartial
			}
		}
	}
	if jr.st.PastPoint {
		jr.setPromotionStatus(promotionPartial)
		return store.JobPartial
	}
	jr.promoteUndo(ctx)
	return store.JobFailed
}

// storeNotSent stores, before anything is unlocked, that the job's
// mysql.promote changed nothing: its params "not sent" and its promotion
// failed, in one transaction (store.MarkPromoteNotSent, through the
// notSentStore seam). When it fails nothing has changed and the job's
// state in memory is left as it was.
func (jr *jobRun) storeNotSent() error {
	cur, _, err := jr.r.d.Store.GetJob(jr.job.ID)
	if err != nil {
		return err
	}
	st := jr.st
	st.PromoteSent, st.PromoteUnanswered, st.PromoteSentAt = false, false, nil
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	mark := jr.r.d.Store.MarkPromoteNotSent
	if jr.r.notSentStore != nil {
		mark = jr.r.notSentStore
	}
	if err := mark(jr.job.ID, cur.ParamsJSON, string(b), st.PromotionID); err != nil {
		return err
	}
	jr.st = st
	return nil
}

// promoteUndo lifts the master's promote lock of a promotion that did not
// reach its point of no return, and marks the promotion failed (best
// effort; the master's guard lifts it at the deadline otherwise). Once the
// lock was asked for, unlockPromote is always tried: a lock taken by a
// call whose answer was lost is lifted too, and "not locked" is harmless.
// It does not use the job's steps: cancelJob calls it too.
func (jr *jobRun) promoteUndo(ctx context.Context) {
	if jr.st.PastPoint || jr.st.PromoteSent {
		return
	}
	jr.setPromotionStatus(promotionFailed)
	if jr.job.Kind != KindPromotePlanned || !jr.st.LockTried {
		return
	}
	r := jr.r
	cred, err := r.promoteUnlockCred(ctx, jr.job.TopologyID, jr.st)
	var ans unlockAnswer
	if err == nil {
		ans, err = r.sendUnlockPromote(ctx, jr.master(), cred)
	}
	if err != nil {
		r.log.Warn("unlock promote", "job", jr.job.ID, "err", logErr(err))
		jr.logf("error", textUnlockFailed, r.agentErrText(err))
		return
	}
	jr.st.LockDeadline, jr.st.LockTried, jr.st.LockUnlocked = nil, false, true
	jr.save()
	if ans == unlockWasNotLocked {
		jr.logf("info", "Master tidak sedang dikunci untuk promote; tidak ada yang dibuka.")
		return
	}
	jr.logf("info", "Kunci master dibuka; master tidak berubah.")
}

// resumePromote is how Resume ends a promotion cut off by a restart: partial
// once mysql.promote was sent, failed before (the master's lock, if any,
// is lifted by its agent's guard at the deadline; at startup the agents
// have rarely reconnected to ask for an unlock).
func (r *Runner) resumePromote(j store.Job) string {
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		r.log.Warn("job params unreadable", "job", j.ID, "err", err)
		return store.JobFailed
	}
	// After mysql.promote was sent its outcome is unknown here: partial,
	// never failed (a retry settles it).
	if st.PastPoint || st.PromoteSent {
		r.updatePromotion(st.PromotionID, func(p *store.Promotion) { p.Status = promotionPartial })
		return store.JobPartial
	}
	r.updatePromotion(st.PromotionID, func(p *store.Promotion) { p.Status = promotionFailed })
	return store.JobFailed
}

// promoteSupersededBy says why a failed or partial promotion must not run
// again: a newer promotion of its topology, or a topology changed since
// (before the point of no return its master must still be the old one and
// the candidate its replica; after it, the master is the old one or the
// candidate).
func (r *Runner) promoteSupersededBy(j store.Job) (string, error) {
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return "", err
	}
	if prom, err := r.d.Store.LatestPromotion(j.TopologyID); err == nil && prom.ID != st.PromotionID {
		return "topologi ini sudah punya promote yang lebih baru", nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	topo, err := r.d.Store.GetTopology(j.TopologyID)
	if errors.Is(err, store.ErrNotFound) {
		return "topologi ini sudah dihapus", nil
	}
	if err != nil {
		return "", err
	}
	if st.PastPoint {
		if topo.MasterServerID != st.MasterServerID && topo.MasterServerID != st.CandidateServerID {
			return "master topologi ini sudah berganti", nil
		}
		return "", nil
	}
	if topo.MasterServerID != st.MasterServerID {
		return "master topologi ini sudah berganti", nil
	}
	rep, err := r.d.Store.GetReplicaByServer(st.CandidateServerID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rep.TopologyID != topo.ID) {
		return "kandidat tidak lagi replika topologi ini", nil
	}
	return "", err
}

// ---- credentials ----

// peerCred is how the agent of from logs in to the MySQL server to at
// host:port with the replication account. When to has the panel's
// certificate (server_tls, 4a) the login verifies it against the panel's
// CA, stored on from (mysql.tlsTrust; PeerCred.SSLCA is a path there).
func (r *Runner) peerCred(ctx context.Context, from, to int64, host string, port int, user, password string) (proto.PeerCred, error) {
	c := proto.PeerCred{Host: host, Port: port, User: user, Password: password}
	has, err := r.masterHasCert(to)
	if err != nil || !has {
		return c, err
	}
	path, err := r.trustCAOn(ctx, from)
	if err != nil {
		return c, err
	}
	c.SSLCA, c.Verify = path, true
	return c, nil
}

// trustCAOn stores the panel's MySQL CA on serverID and returns its path
// there (jobRun.trustCA without a job).
func (r *Runner) trustCAOn(ctx context.Context, serverID int64) (string, error) {
	if r.d.MySQLCA == nil {
		return "", errors.New("penerbit sertifikat MySQL panel tidak tersedia")
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var res proto.TLSTrustResult
	err := r.d.Hub.Call(ctx, serverID, "mysql.tlsTrust", proto.TLSTrustParams{CAPEM: string(r.d.MySQLCA.CAPEM())}, &res)
	if err != nil {
		return "", r.promoteCmdErr(serverID, err)
	}
	if res.CAPath == "" {
		return "", errors.New("agent tidak menyebut letak berkas CA MySQL")
	}
	return res.CAPath, nil
}

func (r *Runner) replPassword(topo store.Topology) (string, error) {
	plain, err := r.d.Secrets.Open(topo.ReplPasswordEnc)
	if err != nil {
		return "", fmt.Errorf("buka password replikasi tersimpan: %w", err)
	}
	return string(plain), nil
}

// ---- quarantine and repoint, in a job or on their own ----

// sendQuarantine sends mysql.quarantine to the old master and records the
// outcome on the promotion (when it was sent, and the agent's words when
// it refused) and in the pending-quarantine round's schedule
// (quarantineTried). A failure keeps the quarantine pending. Callers hold
// lockTarget(oldID).
func (r *Runner) sendQuarantine(ctx context.Context, promID, oldID, newID int64, host string, port int, user, password string) (proto.QuarantineResult, error) {
	var res proto.QuarantineResult
	tried := r.now()
	cred, err := r.peerCred(ctx, oldID, newID, host, port, user, password)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		err = r.promoteCmdErr(oldID, r.d.Hub.Call(cctx, oldID, "mysql.quarantine", proto.QuarantineParams{NewMaster: cred}, &res))
		cancel()
		if err == nil && !res.Applied {
			r.log.Info("quarantine pinned; read-only follows when MySQL answers", "server_id", oldID)
		}
	}
	// The record first: the round drops the schedule of a server no
	// longer pending, and keeps one it sees still pending.
	defer r.quarantineTried(oldID, err)
	if err != nil {
		// Refused: the agent answered; its words are labelled in the
		// detail (QuarantineRefusal finds them for T18).
		_, refused := fromAgent(err)
		r.markQuarantine(promID, oldID, true, truncate(r.agentErrText(err), maxLogLine), &tried, refused)
		return res, err
	}
	r.markQuarantine(promID, oldID, false, "", &tried, false)
	r.log.Info("old master quarantined", "server_id", oldID, "new_master", newID)
	return res, nil
}

// repointOutcome is how mysql.repoint of one replica ended.
type repointOutcome struct {
	state   string // the replica's state now
	offline bool   // its agent was offline: it waits in needs_repoint
	errant  string // NeedsRebuild: what it holds that the new master lacks (shownGTID; "" when unreadable)
	ioErr   string // repointed, but its receiver has not connected yet
	err     error  // it answered and refused or failed: needs_repoint
	// record: its receiver is connected to the new master, which the
	// caller has record its replicas (Runner.recordReplicas) once it no
	// longer holds the replica's target lock (FR-12).
	record bool
}

// repointReplica points rep at topo's (new) master and records its state
// (ready, needs_rebuild, or needs_repoint) and the outcome on the
// promotion promID (0: the topology's latest). Callers hold
// lockTarget(rep.ServerID).
func (r *Runner) repointReplica(ctx context.Context, topo store.Topology, rep store.Replica, promID int64) repointOutcome {
	set := func(o repointOutcome, lastErr string) repointOutcome {
		// lastErr and ioErr may carry the agent's (MySQL's) text: censored
		// before replicas.last_error and the promotion's detail.
		lastErr = redact.Quotes(lastErr)
		if err := r.d.Store.SetReplicaState(rep.ServerID, o.state, lastErr); err != nil {
			r.log.Warn("replica state after repoint", "server_id", rep.ServerID, "err", err)
		}
		r.replicaChanged(rep.ServerID)
		detail := lastErr
		if detail == "" {
			detail = o.ioErr
		}
		r.markReplica(promID, topo.ID, rep.ServerID, o.state, detail)
		return o
	}
	const waitMsg = "Perlu diarahkan ulang ke master baru; dilakukan saat agent tersambung"
	if !r.d.Hub.IsOnline(rep.ServerID) {
		return set(repointOutcome{state: store.ReplicaNeedsRepoint, offline: true}, waitMsg)
	}
	res, verify, err := func() (proto.RepointResult, bool, error) {
		password, err := r.replPassword(topo)
		if err != nil {
			return proto.RepointResult{}, false, err
		}
		cred, err := r.peerCred(ctx, rep.ServerID, topo.MasterServerID, topo.SourceHost, topo.SourcePort, topo.ReplUser, password)
		if err != nil {
			return proto.RepointResult{}, false, err
		}
		p := proto.RepointParams{NewMaster: cred, Verify: cred.Verify, Databases: rep.Databases}
		if cred.Verify {
			p.CAPEM = string(r.d.MySQLCA.CAPEM())
		}
		cctx, cancel := context.WithTimeout(ctx, repointTimeout)
		defer cancel()
		var res proto.RepointResult
		err = r.promoteCmdErr(rep.ServerID, r.d.Hub.Call(cctx, rep.ServerID, "mysql.repoint", p, &res))
		return res, cred.Verify, err
	}()
	switch {
	case errors.Is(err, proto.ErrOffline) && !hub.SentByAgent(err):
		return set(repointOutcome{state: store.ReplicaNeedsRepoint, offline: true}, waitMsg)
	case err != nil:
		return set(repointOutcome{state: store.ReplicaNeedsRepoint, err: err}, truncate("Arahkan ulang gagal: "+r.agentErrText(err), maxLogLine))
	case res.NeedsRebuild:
		// The agent's errant set is shown only when it reads as a GTID set.
		errant := shownGTID(res.Errant)
		msg := "Perlu dibangun ulang: replika ini punya transaksi yang tidak ada di master baru"
		if errant != "" {
			msg += " (" + errant + ")"
		}
		return set(repointOutcome{state: store.ReplicaNeedsRebuild, errant: errant}, msg)
	}
	// SEC-63c: verified only when the agent saw the receiver connected
	// with SOURCE_SSL_VERIFY_SERVER_CERT=1; a receiver still connecting
	// stays unverified until "Wajibkan pemeriksaan sertifikat TLS".
	if err := r.d.Store.SetReplicaTLSVerified(rep.ServerID, verify && res.IORunning && res.SSLVerifyServerCert); err != nil {
		r.log.Warn("tls_verified after repoint", "server_id", rep.ServerID, "err", err)
	}
	// mysql.repoint started replication on the new master.
	if rep.Paused {
		if err := r.d.Store.SetReplicaPaused(rep.ServerID, false); err != nil {
			r.log.Warn("clear paused after repoint", "server_id", rep.ServerID, "err", err)
		}
	}
	o := repointOutcome{state: store.ReplicaReady}
	if !res.IORunning {
		o.ioErr = "penerima replikasi belum tersambung"
		if res.IOError != "" {
			// The receiver's error is the agent's (MySQL's) words.
			o.ioErr = truncate(redact.Quotes(r.agentWords(rep.ServerID, res.IOError, maxLogLine)), maxLogLine)
		}
	}
	out := set(o, "")
	// The receiver is connected to the new master: the caller has it
	// record the connection, outside the target lock.
	out.record = res.IORunning
	return out
}

// ErrNeedsRebuild answers RepointNow for a replica that holds transactions
// the new master lacks: it is now needs_rebuild.
var ErrNeedsRebuild = errors.New("replika punya transaksi yang tidak ada di master baru; bangun ulang dengan Resync ulang")

// RepointNow points a needs_repoint replica at its topology's new master
// now ("Arahkan sekarang"), instead of waiting for its agent to reconnect.
func (r *Runner) RepointNow(ctx context.Context, replicaServerID int64) error {
	topo, _, err := func() (store.Topology, store.Replica, error) {
		r.startMu.Lock()
		defer r.startMu.Unlock()
		rep, err := r.d.Store.GetReplicaByServer(replicaServerID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Topology{}, rep, errors.New("server ini bukan replika")
		}
		if err != nil {
			return store.Topology{}, rep, err
		}
		if rep.State != store.ReplicaNeedsRepoint {
			return store.Topology{}, rep, fmt.Errorf("replika %s; hanya replika yang perlu diarahkan ulang yang bisa diarahkan", replicaStateText(rep.State))
		}
		if err := r.refuseBusy(replicaServerID, "server ini"); err != nil {
			return store.Topology{}, rep, err
		}
		topo, err := r.d.Store.GetTopology(rep.TopologyID)
		return topo, rep, err
	}()
	if err != nil {
		return err
	}
	if !r.d.Hub.IsOnline(replicaServerID) {
		return errors.New("agent replika offline; replika diarahkan otomatis saat agent tersambung lagi")
	}
	out, err := func() (repointOutcome, error) {
		unlock := r.lockTarget(replicaServerID)
		defer unlock()
		// Re-read under the lock: OnOnline may have repointed it meanwhile.
		rep, err := r.d.Store.GetReplicaByServer(replicaServerID)
		if err != nil {
			return repointOutcome{}, err
		}
		if rep.State != store.ReplicaNeedsRepoint {
			return repointOutcome{state: rep.State}, nil
		}
		return r.repointReplica(ctx, topo, rep, 0), nil
	}()
	if err != nil {
		return err
	}
	if out.record {
		r.recordReplicas(ctx, topo.MasterServerID)
	}
	switch {
	case out.offline:
		return errors.New("agent replika terputus; replika diarahkan otomatis saat agent tersambung lagi")
	case out.err != nil:
		return out.err
	case out.state == store.ReplicaNeedsRebuild && out.errant != "":
		return fmt.Errorf("%w (%s)", ErrNeedsRebuild, out.errant)
	case out.state == store.ReplicaNeedsRebuild:
		return ErrNeedsRebuild
	}
	return nil
}

// OnOnline runs when an agent connects (the panel calls it after the
// Monitor's): a quarantined old master whose quarantine is pending gets
// mysql.quarantine, a replica in needs_repoint whose topology's master was
// swapped gets mysql.repoint, and a server whose MySQL account is not
// known yet has its inventory read (refreshUnknownAccount). It runs under
// the hub's lifecycle lock, so the work happens on goroutines of its own.
func (r *Runner) OnOnline(serverID int64) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(2)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		r.onOnline(serverID)
	}()
	go func() {
		defer r.wg.Done()
		r.refreshUnknownAccount(r.base, serverID)
	}()
}

// refreshUnknownAccount reads the inventory of serverID once, as Segarkan
// does (RefreshInventory: read-only on the agent), when the stored one is
// connected without the MySQL account the agent logs in with: an
// inventory stored before the panel read accounts (Ruling R40). The gate's
// Akun panel row then judges the account. It takes no lock but the
// inventory's own; a failure is logged and the row stays as it was.
func (r *Runner) refreshUnknownAccount(ctx context.Context, serverID int64) {
	inv, err := r.d.Store.GetInventory(serverID)
	if err != nil || !inv.Connected || inv.Account != "" {
		return
	}
	if _, err := r.RefreshInventory(ctx, serverID); err != nil {
		r.log.Warn("inventory not read on connect (MySQL account unknown)", "server_id", serverID, "err", logErr(err))
		return
	}
	r.log.Info("inventory read on connect (MySQL account was unknown)", "server_id", serverID)
}

// onOnline decides from the store first (spec 5.3) and returns without
// any lock when serverID is owed nothing. What it owes is decided again
// under the server's target lock, and each send gets its own bound
// (onlineSend) once the lock is held, not what is left since the agent
// connected.
func (r *Runner) onOnline(serverID int64) {
	if _, ok := r.pendingQuarantine(serverID); ok {
		r.sendPendingQuarantine(serverID)
		return
	}
	if _, _, ok := r.pendingRepoint(serverID); !ok {
		return
	}
	// A repointed replica's new master records it once the replica's
	// lock is released (FR-12).
	recordOn := func() int64 {
		unlock := r.lockTarget(serverID)
		defer unlock()
		topo, rep, ok := r.pendingRepoint(serverID)
		if !ok {
			return 0
		}
		ctx, cancel := context.WithTimeout(r.base, r.onlineSend)
		defer cancel()
		out := r.repointReplica(ctx, topo, rep, 0)
		r.log.Info("replica repointed on reconnect", "server_id", serverID, "state", out.state, "err", logErr(out.err))
		if out.record {
			return topo.MasterServerID
		}
		return 0
	}()
	if recordOn != 0 {
		ctx, cancel := context.WithTimeout(r.base, r.onlineSend)
		defer cancel()
		r.recordReplicas(ctx, recordOn)
	}
}

// pendingRepoint is the repoint a connecting replica is owed, decided from
// the store alone: it waits in needs_repoint after a promotion of its
// topology (the old master may since have been rebuilt or deleted, which
// clears OldMasterServerID) that no longer runs (a promotion still
// running repoints its replicas itself).
func (r *Runner) pendingRepoint(serverID int64) (store.Topology, store.Replica, bool) {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil || srv.Role != store.RoleReplica {
		return store.Topology{}, store.Replica{}, false
	}
	rep, err := r.d.Store.GetReplicaByServer(serverID)
	if err != nil || rep.State != store.ReplicaNeedsRepoint {
		return store.Topology{}, store.Replica{}, false
	}
	topo, err := r.d.Store.GetTopology(rep.TopologyID)
	if err != nil {
		return store.Topology{}, store.Replica{}, false
	}
	if _, err := r.d.Store.LatestPromotion(topo.ID); err != nil {
		return store.Topology{}, store.Replica{}, false
	}
	if _, busy, err := r.d.Store.ActiveJobForTopology(topo.ID, KindPromotePlanned, KindPromoteEmergency); err != nil || busy {
		return store.Topology{}, store.Replica{}, false
	}
	return topo, rep, true
}

// ConfirmCutover records that the applications now write to the new
// master ("Sudah saya alihkan"): cutover_pending is cleared and the
// promotion marked confirmed. warning, when not "", is what the user must
// still be told: the old master has not been quarantined yet (its agent
// was unreachable), so it must not be started again before it is.
func (r *Runner) ConfirmCutover(topologyID int64) (warning string, err error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	topo, err := r.d.Store.GetTopology(topologyID)
	if err != nil {
		return "", err
	}
	if !topo.CutoverPending {
		return "", errors.New("tidak ada promote yang menunggu pengalihan aplikasi di topologi ini")
	}
	if _, busy, err := r.d.Store.ActiveJobForTopology(topologyID, KindPromotePlanned, KindPromoteEmergency); err != nil {
		return "", err
	} else if busy {
		return "", errors.New("promote masih berjalan; tunggu sampai selesai")
	}
	if err := r.d.Store.SetCutoverPending(topologyID, false); err != nil {
		return "", err
	}
	prom, err := r.d.Store.LatestPromotion(topologyID)
	switch {
	case err == nil:
		if prom.CutoverConfirmedAt == nil {
			now := r.now()
			r.updatePromotion(prom.ID, func(p *store.Promotion) { p.CutoverConfirmedAt = &now })
		}
		if QuarantinePending(prom) {
			warning = fmt.Sprintf("Master lama %s belum dikarantina karena agent-nya belum terjangkau. Jangan hidupkan "+
				"server itu sebelum agent-nya tersambung dan karantina terkirim; aplikasi yang belum dialihkan bisa menulis ke sana.",
				r.nameOf(prom.OldMasterServerID))
		}
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	r.d.Publish("promotion_update", map[string]any{"topology_id": topologyID, "cutover_pending": false})
	return warning, nil
}

// ---- the promotion record ----

// PromotionServer is one server's outcome in a promotion, as kept in
// promotions.replicas_json: the old master (quarantined or still pending)
// and each other replica (ready, needs_repoint or needs_rebuild).
type PromotionServer struct {
	ServerID int64  `json:"server_id"`
	Role     string `json:"role"` // PromotionOldMaster | PromotionReplica
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	// For the old master: TriedAt is the last mysql.quarantine sent (nil:
	// none yet); Refused, that the agent answered it with a refusal,
	// whose words Detail then holds after their label (QuarantineRefusal).
	TriedAt *time.Time `json:"tried_at,omitempty"` // last send
	Refused bool       `json:"refused,omitempty"`  // the agent answered that send with a refusal (Detail holds its text)
}

// PromotionServer roles and the old master's states.
const (
	PromotionOldMaster = "old_master"
	PromotionReplica   = "replica"

	QuarantineStatePending = "quarantine_pending"
	QuarantineStateDone    = "quarantined"
)

// PromotionServers reads p.ReplicasJSON; nil when it holds none.
func PromotionServers(p store.Promotion) []PromotionServer {
	var out []PromotionServer
	if json.Unmarshal([]byte(p.ReplicasJSON), &out) != nil {
		return nil
	}
	return out
}

// QuarantinePending reports whether the old master of p has not been
// quarantined yet (its agent was offline or the command failed); OnOnline
// sends it when the agent connects, and the round again while it stays
// connected.
func QuarantinePending(p store.Promotion) bool {
	s, ok := OldMasterRecord(p)
	return ok && s.State == QuarantineStatePending
}

// QuarantineRefusal is the agent's own words in s, the old master's
// entry, when its last send was refused: what follows the label of the
// agent's text in Detail (the label is the panel's, so its first
// occurrence ends it); "" when s records no refusal. The words are the
// agent's: a page labels, bounds and escapes them.
func QuarantineRefusal(s PromotionServer) string {
	if !s.Refused {
		return ""
	}
	_, words, ok := strings.Cut(s.Detail, agentLabelTail)
	if !ok {
		return ""
	}
	return words
}

// OldMasterRecord is the old master's entry in p's record; false when it
// has none.
func OldMasterRecord(p store.Promotion) (PromotionServer, bool) {
	for _, s := range PromotionServers(p) {
		if s.Role == PromotionOldMaster {
			return s, true
		}
	}
	return PromotionServer{}, false
}

// updatePromotion applies change to the stored promotion id (read, change,
// write under promoMu). Errors are logged: the promotion is a record, the
// panel's data (topology, replicas) is what the job acts on.
func (r *Runner) updatePromotion(id int64, change func(p *store.Promotion)) {
	if id == 0 {
		return
	}
	r.promoMu.Lock()
	defer r.promoMu.Unlock()
	p, err := r.d.Store.GetPromotion(id)
	if err == nil {
		change(&p)
		err = r.d.Store.UpdatePromotion(p)
	}
	if err != nil {
		r.log.Warn("update promotion", "promotion", id, "err", err)
	}
}

func (r *Runner) setPromotionServer(promID int64, s PromotionServer) {
	r.editPromotionServer(promID, s.ServerID, func(PromotionServer) PromotionServer { return s })
}

// editPromotionServer replaces serverID's entry in promotion promID with
// change(the entry now; zero when it has none).
func (r *Runner) editPromotionServer(promID, serverID int64, change func(old PromotionServer) PromotionServer) {
	r.updatePromotion(promID, func(p *store.Promotion) {
		list := PromotionServers(*p)
		if i := slices.IndexFunc(list, func(x PromotionServer) bool { return x.ServerID == serverID }); i >= 0 {
			list[i] = change(list[i])
		} else {
			list = append(list, change(PromotionServer{}))
		}
		if b, err := json.Marshal(list); err == nil {
			p.ReplicasJSON = string(b)
		}
	})
}

// markQuarantine records the old master's quarantine state on promID.
// tried is when the send it records went out; nil (no send: the agent was
// offline) keeps the last send's time and clears Refused, which then no
// longer describes Detail. refused: detail is the agent's refusal.
func (r *Runner) markQuarantine(promID, oldID int64, pending bool, detail string, tried *time.Time, refused bool) {
	state := QuarantineStateDone
	if pending {
		state = QuarantineStatePending
	}
	r.editPromotionServer(promID, oldID, func(old PromotionServer) PromotionServer {
		s := PromotionServer{ServerID: oldID, Role: PromotionOldMaster, State: state, Detail: redact.Quotes(detail),
			TriedAt: tried, Refused: refused}
		if tried == nil && old.Role == PromotionOldMaster {
			s.TriedAt = old.TriedAt
		}
		return s
	})
}

// markReplica records a replica's outcome on promotion promID, or on the
// topology's latest promotion when promID is 0.
func (r *Runner) markReplica(promID, topologyID, serverID int64, state, detail string) {
	if promID == 0 {
		p, err := r.d.Store.LatestPromotion(topologyID)
		if err != nil {
			return
		}
		promID = p.ID
	}
	r.setPromotionServer(promID, PromotionServer{ServerID: serverID, Role: PromotionReplica, State: state, Detail: redact.Quotes(detail)})
}

// ErrPromoteRetryBeforePoint refuses Ulangi of an emergency promote job
// that failed before mysql.promote was sent (promoteRetryRefused).
var ErrPromoteRetryBeforePoint = errors.New(`Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia ` +
	`untuk keadaan ini: mulai promote baru dari halaman master.`)

// promoteRetryRefused reports whether Ulangi of j is refused (PC-1, rulings
// R43 and R46): an emergency promote job whose mysql.promote was never sent
// (PromoteSent) and that is not past the point of no return (PastPoint)
// changed nothing; a new emergency promote reads the mode again and asks
// for the tick, the names and the password, which Ulangi would skip. A
// planned promote job keeps its Ulangi, refused only while its lock record
// stands (T12b, refusePlannedRetryWhileLockStands): "Buka kunci promote"
// lifts the lock, not Ulangi. A job whose params cannot be read is left to
// the checks that refuse it (promoteSupersededBy, newRun).
func promoteRetryRefused(j store.Job) bool {
	if j.Kind != KindPromoteEmergency {
		return false
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return false
	}
	return !st.PromoteSent && !st.PastPoint
}

// RetryRefused is why Ulangi of the job is refused whatever is acked
// (ErrPromoteRetryBeforePoint's text; T12b while the lock record of a
// planned promote job not sent stands; T12 for such a job last confirmed
// more than confirmMaxAge ago); "" when it is not, or when the job cannot
// be read (Retry decides then). The job page hides Ulangi for it. It
// writes nothing (LockRecords).
func (r *Runner) RetryRefused(jobID string) string {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil {
		return ""
	}
	if promoteRetryRefused(j) {
		return ErrPromoteRetryBeforePoint.Error()
	}
	master, stands, err := r.plannedLockStands(j, r.LockRecords)
	if err != nil {
		r.log.Warn("lock records", "job", jobID, "err", err)
		return ""
	}
	if stands {
		return (&PromoteLockStandsError{Master: r.nameOf(master)}).Error()
	}
	if st, planned := plannedNotSent(j); planned && plannedConfirmStale(j, st, r.now()) {
		return ErrPromoteRetryStale.Error()
	}
	return ""
}

// ErrPromoteRetryStale (T12) refuses Ulangi of a planned promote job not
// sent whose last confirmation is more than confirmMaxAge old.
var ErrPromoteRetryStale = errors.New(`Promote ini terakhir dikonfirmasi lebih dari 24 jam lalu, jadi "Ulangi dari tahap ` +
	`ini" tidak tersedia lagi. Mulai promote baru dari halaman master.`)

// plannedNotSent is j's state when j is a planned promote job whose
// mysql.promote was not sent (plannedNotSentState); false for any other
// job and for one whose params cannot be read (left to the checks that
// refuse it: promoteSupersededBy, newRun).
func plannedNotSent(j store.Job) (jobState, bool) {
	if j.Kind != KindPromotePlanned {
		return jobState{}, false
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return jobState{}, false
	}
	return st, plannedNotSentState(j.Kind, st)
}

// plannedConfirmStale: the last confirmation of j (ConfirmedAt; its
// creation for a job of 0.2.0, which stored none) is more than
// confirmMaxAge before now.
func plannedConfirmStale(j store.Job, st jobState, now time.Time) bool {
	last := st.ConfirmedAt
	if last.IsZero() {
		last = j.CreatedAt
	}
	return now.Sub(last) > confirmMaxAge
}

// plannedView is the consideration data plannedRetryView read for a
// Retry: ok when it was read (err its failure).
type plannedView struct {
	view PromoteView
	ok   bool
	err  error
}

// plannedRetryView reads, before startMu, a fresh PromoteView of the
// master of a failed planned promote job not sent, for its Retry to judge
// with promoteRefusals (spec 5.2 row 1). It reads nothing for any other
// job, nor while T12b or an unlock in flight refuses the Retry anyway
// (both are checked again under startMu): ok is false then.
func (r *Runner) plannedRetryView(jobID string) (PromoteView, bool, error) {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil || (j.Status != store.JobFailed && j.Status != store.JobPartial) {
		return PromoteView{}, false, nil
	}
	st, planned := plannedNotSent(j)
	if !planned {
		return PromoteView{}, false, nil
	}
	if _, stands, err := r.plannedLockStands(j, r.LockRecords); err != nil || stands {
		return PromoteView{}, false, nil
	}
	if err := r.refuseUnlockInFlightNow(st.MasterServerID); err != nil {
		return PromoteView{}, false, nil
	}
	ctx, cancel := context.WithTimeout(r.base, callTimeout)
	defer cancel()
	view, err := r.PromoteView(ctx, st.MasterServerID)
	return view, true, err
}

// confirmPlannedRetry checks a Retry of j, a planned promote job not sent
// (st), as StartPromote checks its start, and stores the new confirmation
// (ConfirmedAt) before the caller launches it; nothing is stored when it
// refuses. In order: the refusals of the start (promoteRefusals, mode
// planned, the params the job was started with) judged on pv, read for
// this Retry before startMu; the gate (GateError); the backup tick when
// the gate asks for it (ErrBackupAck); the token (ErrJobNeedsConfirm);
// the last confirmation at most confirmMaxAge old (T12). A Retry for
// which no view was read (the job changed in between) is refused: it is
// asked again. Callers hold startMu.
func (r *Runner) confirmPlannedRetry(j store.Job, st jobState, pv plannedView, ack JobAck) error {
	if pv.err != nil {
		return pv.err
	}
	if !pv.ok {
		return errBusy
	}
	topo, err := r.d.Store.GetTopologyByMaster(st.MasterServerID)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("server ini bukan master")
	}
	if err != nil {
		return err
	}
	p := PromoteParams{MasterServerID: st.MasterServerID, CandidateServerID: st.CandidateServerID, Mode: proto.PromotePlanned,
		AcceptPartial: st.PartialCandidate, AcceptUnreachable: st.AcceptUnreachable, AcceptBranched: st.AcceptBranched,
		BackupAck: ack.BackupAck}
	if _, _, err := r.promoteRefusals(p, pv.view, topo, st.PromotionID); err != nil {
		return err
	}
	res, err := r.refuseRedGate(GateInput{Action: GatePromotePlanned, MasterID: st.MasterServerID, TargetID: st.CandidateServerID})
	if err != nil {
		return err
	}
	if res.NeedsBackupAck() && !ack.BackupAck {
		return ErrBackupAck
	}
	if !ack.Confirmed {
		return ErrJobNeedsConfirm
	}
	now := r.now()
	if plannedConfirmStale(j, st, now) {
		return ErrPromoteRetryStale
	}
	if err := r.launchRefused(j.ID); err != nil {
		return err
	}
	st.ConfirmedAt = now
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return r.d.Store.SetJobParams(j.ID, string(b))
}

// plannedRetryFrom is the step a Retry of a planned promote job not sent
// runs from (K15): the earlier of its failed step and "Kunci master" (a
// failure lifted the lock; the candidate catches up under a new one). A
// job failed in "Buat akun replikasi di kandidat" starts there.
func plannedRetryFrom(failed int, steps []store.JobStep) int {
	lock := slices.IndexFunc(steps, func(s store.JobStep) bool { return s.Name == stepPromoteLock })
	if lock < 0 {
		return failed // an unknown step list: newRun fails it
	}
	return min(failed, lock)
}
