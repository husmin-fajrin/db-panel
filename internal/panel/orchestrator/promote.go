package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	// onlineHookTimeout bounds the work OnOnline starts.
	onlineHookTimeout = 5 * time.Minute
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
}

// StartPromote starts the promotion of p.CandidateServerID to master of
// p.MasterServerID's topology and records it in promotions. It refuses
// what the consideration data (PromoteView) does not allow: a candidate
// that is not eligible, branched replicas, a partial candidate without
// AcceptPartial, unreachable replicas not in AcceptUnreachable, a mode the
// master's status does not permit, an outdated agent (SEC-62), and a
// topology with an unfinished job or a cutover still pending.
func (r *Runner) StartPromote(ctx context.Context, p PromoteParams) (string, error) {
	if p.MasterServerID == p.CandidateServerID {
		return "", errors.New("kandidat tidak boleh master itu sendiri")
	}
	// Refusals that need no agent come first; they are checked again
	// under startMu.
	if topo, err := r.d.Store.GetTopologyByMaster(p.MasterServerID); err == nil && topo.CutoverPending {
		return "", errCutoverStillPending
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
	if topo.CutoverPending {
		return "", errCutoverStillPending
	}
	mode := view.Mode
	switch p.Mode {
	case "", mode:
	case proto.PromotePlanned:
		return "", errors.New("promote terencana butuh agent master tersambung dan MySQL master hidup; status master sekarang hanya mengizinkan promote darurat")
	case proto.PromoteEmergency:
		return "", errors.New("master masih sehat; jalankan promote terencana")
	default:
		return "", fmt.Errorf("mode promote tidak dikenal: %q", p.Mode)
	}
	rep, err := r.d.Store.GetReplicaByServer(p.CandidateServerID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rep.TopologyID != topo.ID) {
		return "", errors.New("kandidat bukan replika master ini")
	}
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(view.Candidates, func(c Candidate) bool { return c.ServerID == p.CandidateServerID })
	if i < 0 {
		return "", errors.New("kandidat bukan replika master ini")
	}
	cand := view.Candidates[i]
	if view.Branched && !p.AcceptBranched {
		return "", errors.New("replika bercabang: masing-masing punya transaksi yang tidak dimiliki yang lain, " +
			"sehingga tidak ada replika yang memuat semuanya. Pilih satu dan setujui bahwa replika lain yang bercabang " +
			"dibangun ulang dari master baru")
	}
	// A replica that already takes writes may be a master the panel does
	// not know about (an earlier promotion whose outcome was lost).
	for _, c := range view.Candidates {
		if c.Writable {
			return "", fmt.Errorf("replika %s bisa ditulis (super_read_only mati) dan mungkin sudah menjadi master; "+
				"promote ditolak. Periksa server itu dulu", c.Name)
		}
	}
	if prev, err := r.d.Store.LatestPromotion(topo.ID); err == nil && (prev.Status == promotionRunning || prev.Status == promotionPartial) {
		return "", errors.New("promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi")
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if mode == proto.PromoteEmergency {
		if err := r.refuseWhileLockMayStand(topo.ID, p.MasterServerID, verbEmergency); err != nil {
			return "", err
		}
	}
	if !cand.Eligible {
		return "", fmt.Errorf("%s tidak bisa dipromosikan: %s", cand.Name, cand.Reason)
	}
	// SEC-62: on a planned promotion the master's agent takes part too.
	if mode == proto.PromotePlanned && r.agentOutdated(view.Master) {
		return "", fmt.Errorf("agent master %s: Agent perlu dimutakhirkan", view.Master.Name)
	}
	if cand.Partial && !p.AcceptPartial {
		missing := "sebagian database"
		if len(cand.MissingDatabases) > 0 {
			missing = strings.Join(cand.MissingDatabases, ", ")
		}
		return "", fmt.Errorf("%s hanya mereplikasi sebagian database; %s tidak akan ada di master baru. "+
			"Setujui itu dulu untuk melanjutkan", cand.Name, missing)
	}
	var unreachable []string
	for _, c := range view.Candidates {
		if c.ServerID != cand.ServerID && candidateUnreachable(c) && !slices.Contains(p.AcceptUnreachable, c.ServerID) {
			unreachable = append(unreachable, c.Name)
		}
	}
	if len(unreachable) > 0 {
		return "", fmt.Errorf("replika berikut tidak terjangkau: %s. Setujui bahwa replika itu diarahkan ulang ke master baru "+
			"saat tersambung lagi untuk melanjutkan", strings.Join(unreachable, ", "))
	}
	if _, busy, err := r.d.Store.ActiveJobForTopology(topo.ID); err != nil {
		return "", err
	} else if busy {
		return "", fmt.Errorf("topologi ini %w", ErrBusy)
	}
	if err := r.refuseBusy(p.MasterServerID, "master"); err != nil {
		return "", err
	}
	reps, err := r.d.Store.ListReplicas(topo.ID)
	if err != nil {
		return "", err
	}
	for _, x := range reps {
		if err := r.refuseBusy(x.ServerID, "replika "+r.nameOf(x.ServerID)); err != nil {
			return "", err
		}
	}

	promID, err := r.d.Store.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: mode, OldMasterServerID: p.MasterServerID,
		NewMasterServerID: p.CandidateServerID, StartedAt: r.now(), LastMasterGTID: view.LastMasterGTID, Status: promotionRunning})
	if err != nil {
		return "", err
	}
	st := jobState{MasterServerID: p.MasterServerID, CandidateServerID: p.CandidateServerID, PromotionID: promID,
		PartialCandidate: cand.Partial, OldSourceHost: topo.SourceHost, OldSourcePort: topo.SourcePort,
		LastMasterGTID: view.LastMasterGTID}
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
	return r.refuseWhileLockMayStand(topo.ID, topo.MasterServerID, what)
}

// refuseWhileLockMayStand refuses what (an emergency promotion of
// masterID, or a job that makes one of its replicas writable for a while)
// while the latest promotion of topology topoID, a failed planned one of
// that master, asked for the master's promote lock and its unlock was
// never confirmed, until the lock's deadline plus the guard's grace
// (proto.PromoteLockGrace) has passed (SEC-66). Before then the master
// may be locked for the earlier candidate: an emergency promotion of
// another replica would see it alive (and the agent refuses it then), or
// it would reopen beside the new master once its guard unlocks it. A
// replica the guard has seen that is writable at the deadline (a resync
// or dump load, Buat replika, Tambah database) makes the guard quarantine
// a healthy master instead of unlocking it (4b final review). what starts
// the message's second sentence ("Promote darurat", "Resync ulang", ...).
func (r *Runner) refuseWhileLockMayStand(topoID, masterID int64, what string) error {
	prev, err := r.d.Store.LatestPromotion(topoID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if prev.Status != promotionFailed || prev.OldMasterServerID != masterID || prev.JobID == "" {
		return nil
	}
	j, _, err := r.d.Store.GetJob(prev.JobID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Kind != KindPromotePlanned {
		return nil
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return fmt.Errorf("catatan promote sebelumnya tidak terbaca: %w", err)
	}
	if st.LockUnlocked || (!st.LockTried && st.LockDeadline == nil) {
		return nil
	}
	var deadline time.Time
	switch {
	case st.LockDeadline != nil:
		deadline = *st.LockDeadline
	case st.LockTriedAt != nil:
		deadline = st.LockTriedAt.Add(promoteLockSec*time.Second + lockCallSlack)
	case prev.FinishedAt != nil:
		deadline = prev.FinishedAt.Add(promoteLockSec * time.Second)
	default:
		deadline = prev.StartedAt.Add(promoteLockSec*time.Second + defaultPromoteCatchUp + lockCallSlack)
	}
	until := deadline.Add(proto.PromoteLockGrace)
	if !r.now().Before(until) {
		return nil
	}
	// The command alone does not tell the panel: only the planned
	// promotion's Ulangi records a confirmed unlock (LockUnlocked).
	const howToUnlock = "Sebelum itu, bila agent master tersambung lagi, tekan Ulangi pada promote terencana itu; bila tidak, " +
		"buka kuncinya di server master dengan `sudo dbpanel agent unlock-master`, lalu tekan Ulangi pada promote terencana " +
		"itu (panel memastikan kunci terbuka)."
	at := until.Local().Format("02-01-2006 15:04:05")
	if what == verbEmergency {
		return fmt.Errorf("master %s mungkin masih terkunci oleh promote terencana sebelumnya: kuncinya belum terbukti dibuka. "+
			"%s baru bisa dijalankan mulai %s, saat agent master sudah membuka kunci itu sendiri bila kandidat lama "+
			"masih replika. %s", r.nameOf(masterID), what, at, howToUnlock)
	}
	return fmt.Errorf("master %s mungkin masih terkunci oleh promote terencana sebelumnya: kuncinya belum terbukti dibuka, "+
		"dan memuat data membuat replika bisa ditulis sebentar dan penjaga kunci bisa mengkarantina master. "+
		"%s baru bisa dijalankan mulai %s, saat penjaga kunci sudah memutuskan. %s", r.nameOf(masterID), what, at, howToUnlock)
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

// promoteCall is jr.call for a promotion command (see promoteCmdErr).
func (jr *jobRun) promoteCall(ctx context.Context, serverID int64, cmd string, params, result any) error {
	return jr.r.promoteCmdErr(serverID, jr.call(ctx, serverID, cmd, params, result))
}

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
	return stepError(fmt.Sprintf("Agent di %s versi lama dan belum mengenal promote. "+
		"Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu Ulangi.", r.nameOf(serverID)))
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
func (jr *jobRun) promoteLock(ctx context.Context, _ bool) (outcome, error) {
	cred, err := jr.candidateCred(ctx, jr.master())
	if err != nil {
		return outcome{}, err
	}
	var res proto.LockForPromoteResult
	tried := jr.r.now()
	jr.st.LockTried, jr.st.LockTriedAt, jr.st.LockUnlocked = true, &tried, false
	jr.save()
	if err := jr.promoteCall(ctx, jr.master(), "mysql.lockForPromote",
		proto.LockForPromoteParams{Candidate: cred, TimeoutSec: promoteLockSec}, &res); err != nil {
		return outcome{}, err
	}
	deadline := res.Deadline
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
			return outcome{}, notSynced(w, fmt.Sprintf("replikasi kandidat tidak berjalan (IO %s, SQL %s)", s.IORunning, s.SQLRunning))
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
		return outcome{}, err
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
	jr.st.PromoteSent = true
	jr.save()
	var res proto.PromoteResult
	jr.promoteStartUnknown = false
	if err := jr.runAgentJob(ctx, jr.candidate(), "mysql.promote", params, &res); err != nil {
		// A start call the agent answered with an error (a refusal) is
		// definite; any other failure of it (a timeout, a dropped link)
		// may have started the job unseen. The hub reports a timeout or
		// a lost link as a *proto.Error it makes itself, without an agent
		// reply, so those codes are no answer too.
		jr.promoteStartUnknown = !jr.agentStarted && !promoteStartRefused(err)
		return outcome{}, jr.r.promoteCmdErr(jr.candidate(), err)
	}
	jr.st.PastPoint = true
	jr.save()
	jr.r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) { p.NewMasterGTID = res.GTIDExecuted })
	if res.Resumed {
		jr.logf("info", "Promote dilanjutkan dari langkah yang sudah membuka penulisan.")
	}
	if res.Mode != "" && res.Mode != mode {
		jr.logf("warn", "Agent mencatat promote ini sebagai %s.", res.Mode)
	}
	if res.Unapplied != "" {
		jr.logf("warn", "Transaksi yang tidak lengkap di relay log tidak diterapkan: %s", truncate(res.Unapplied, 512))
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
		jr.r.markQuarantine(jr.st.PromotionID, old, true, "agent offline")
		jr.logf("warn", "Agent master lama %s offline: karantina tertunda dan dikirim saat agent tersambung lagi. "+
			"Pastikan aplikasi dialihkan sebelum ia hidup kembali.", jr.serverName(old))
		return skipped("tertunda: agent master lama offline; dikirim saat tersambung lagi"), nil
	}
	jr.r.repointMu.Lock()
	err = jr.r.sendQuarantine(ctx, jr.st.PromotionID, old, jr.candidate(), host, port, topo.ReplUser, password)
	jr.r.repointMu.Unlock()
	if errors.Is(err, proto.ErrOffline) {
		jr.logf("warn", "Agent master lama %s terputus: karantina tertunda dan dikirim saat agent tersambung lagi.", jr.serverName(old))
		return skipped("tertunda: agent master lama terputus; dikirim saat tersambung lagi"), nil
	}
	if err != nil {
		jr.deferStep()
		msg := truncate("karantina tertunda: "+err.Error(), maxLogLine)
		jr.logf("error", "Karantina master lama gagal: %v. Dikirim lagi saat agent tersambung ulang, atau tekan Ulangi.", err)
		return outcome{store.StepFailed, msg}, nil
	}
	jr.logf("info", "Master lama %s dikarantina di bawah master baru.", jr.serverName(old))
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
	if err := jr.r.d.Store.SwapMaster(jr.job.TopologyID, jr.master(), jr.candidate(), host, port); err != nil {
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
	reps, err := jr.r.d.Store.ListReplicas(topo.ID)
	if err != nil {
		return outcome{}, err
	}
	var ok, waiting, rebuild, failed int
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
		jr.r.repointMu.Lock()
		out := jr.r.repointReplica(ctx, topo, rep, jr.st.PromotionID)
		jr.r.repointMu.Unlock()
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
			jr.logf("warn", "Replika %s punya transaksi yang tidak ada di master baru (%s): perlu dibangun ulang dengan Resync ulang.",
				name, truncate(out.errant, 256))
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
// mysql.promote failed, the candidate is asked whether it opened writes
// anyway; when it cannot answer, the lock is kept (the master's guard
// decides at its deadline) and the job fails (Retry resumes the promote).
func (jr *jobRun) promoteFailed() string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(jr.lj.ctx), cancelTimeout)
	defer cancel()
	if !jr.st.PastPoint && jr.st.PromoteSent {
		var cs proto.MySQLStatus
		err := jr.r.d.Hub.Call(ctx, jr.candidate(), "mysql.status", nil, &cs)
		switch {
		case err != nil:
			// Unknown: the candidate may have opened writes. Nothing is
			// undone and the job is partial, which blocks any other
			// promotion of the topology until a retry settles it.
			jr.logf("warn", "Keadaan kandidat tidak bisa dibaca (%v); kandidat mungkin sudah menerima tulisan, jadi kunci master "+
				"tidak dibuka. Tekan Ulangi untuk melanjutkan promote.", err)
			jr.setPromotionStatus(promotionPartial)
			return store.JobPartial
		case !cs.ReadOnly && !cs.SuperReadOnly:
			jr.st.PastPoint = true
			jr.save()
			jr.logf("warn", "Kandidat sudah menerima tulisan: promote tidak dibatalkan. Tekan Ulangi untuk melanjutkan.")
		default:
			// Still read-only, but the agent job may still be running
			// (the wait ended while it was away, and it came back): it
			// could open writes later. Only a job known to be over lets
			// the job fail.
			if jr.promoteStartUnknown {
				jr.logf("warn", "Perintah promote mungkin sudah diterima kandidat tanpa jawaban; kunci master tidak dibuka. "+
					"Tekan Ulangi untuk melanjutkan promote.")
				jr.setPromotionStatus(promotionPartial)
				return store.JobPartial
			}
			if running, why := jr.promoteJobMayRun(ctx); running {
				jr.logf("warn", "Pekerjaan promote di kandidat %s; kunci master tidak dibuka. Tekan Ulangi untuk melanjutkan promote.", why)
				jr.setPromotionStatus(promotionPartial)
				return store.JobPartial
			}
			// The agent restored it; the next attempt sends
			// mysql.promote anew.
			jr.st.PromoteSent = false
			jr.save()
		}
	}
	if jr.st.PastPoint {
		jr.setPromotionStatus(promotionPartial)
		return store.JobPartial
	}
	jr.promoteUndo(ctx)
	return store.JobFailed
}

// promoteJobMayRun reports whether the candidate's mysql.promote agent job
// the step waited for may still be running, and why: a job still known to
// the agent as running, or whose state cannot be read. A job the agent no
// longer knows (it restarted; its jobs ended with it) or that ended is
// over.
func (jr *jobRun) promoteJobMayRun(ctx context.Context) (bool, string) {
	if jr.st.AgentJobID == "" || jr.st.AgentCmd != "mysql.promote" {
		return false, "" // ended: agentJobEnded forgot it
	}
	var s proto.JobStatus
	err := jr.r.d.Hub.Call(ctx, jr.st.AgentServerID, "job.status", proto.JobIDParams{JobID: jr.st.AgentJobID}, &s)
	var pe *proto.Error
	switch {
	case errors.As(err, &pe) && pe.Code == proto.CodeBadParams:
		return false, ""
	case err != nil:
		return true, "tidak bisa diperiksa (" + err.Error() + ")"
	case s.State == "running":
		return true, "masih berjalan"
	}
	return false, ""
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
	cred, err := func() (proto.PeerCred, error) {
		srv, err := r.d.Store.GetServer(jr.candidate())
		if err != nil {
			return proto.PeerCred{}, err
		}
		topo, password, err := jr.replAccount()
		if err != nil {
			return proto.PeerCred{}, err
		}
		port := jr.st.CandidatePort
		if port == 0 {
			port = 3306
		}
		return r.peerCred(ctx, jr.master(), jr.candidate(), r.sourceHost(srv), port, topo.ReplUser, password)
	}()
	if err == nil {
		err = r.d.Hub.Call(ctx, jr.master(), "mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: cred}, nil)
	}
	if err != nil && strings.Contains(err.Error(), "tidak dikunci untuk promosi") {
		jr.st.LockDeadline, jr.st.LockTried, jr.st.LockUnlocked = nil, false, true
		jr.save()
		jr.logf("info", "Master tidak sedang dikunci untuk promote; tidak ada yang dibuka.")
		return
	}
	if err != nil {
		r.log.Warn("unlock promote", "job", jr.job.ID, "err", logErr(err))
		jr.logf("error", "Kunci master tidak bisa dibuka (%v); agent master membukanya sendiri saat tenggat kunci.", err)
		return
	}
	jr.st.LockDeadline, jr.st.LockTried, jr.st.LockUnlocked = nil, false, true
	jr.save()
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
// outcome on the promotion. A failure keeps the quarantine pending.
// Callers hold repointMu.
func (r *Runner) sendQuarantine(ctx context.Context, promID, oldID, newID int64, host string, port int, user, password string) error {
	cred, err := r.peerCred(ctx, oldID, newID, host, port, user, password)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		var res proto.QuarantineResult
		err = r.promoteCmdErr(oldID, r.d.Hub.Call(cctx, oldID, "mysql.quarantine", proto.QuarantineParams{NewMaster: cred}, &res))
		cancel()
		if err == nil && !res.Applied {
			r.log.Info("quarantine pinned; read-only follows when MySQL answers", "server_id", oldID)
		}
	}
	if err != nil {
		r.markQuarantine(promID, oldID, true, truncate(err.Error(), maxLogLine))
		return err
	}
	r.markQuarantine(promID, oldID, false, "")
	r.log.Info("old master quarantined", "server_id", oldID, "new_master", newID)
	return nil
}

// repointOutcome is how mysql.repoint of one replica ended.
type repointOutcome struct {
	state   string // the replica's state now
	offline bool   // its agent was offline: it waits in needs_repoint
	errant  string // NeedsRebuild: what it holds that the new master lacks
	ioErr   string // repointed, but its receiver has not connected yet
	err     error  // it answered and refused or failed: needs_repoint
}

// repointReplica points rep at topo's (new) master and records its state
// (ready, needs_rebuild, or needs_repoint) and the outcome on the
// promotion promID (0: the topology's latest). Callers hold repointMu.
func (r *Runner) repointReplica(ctx context.Context, topo store.Topology, rep store.Replica, promID int64) repointOutcome {
	set := func(o repointOutcome, lastErr string) repointOutcome {
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
	case errors.Is(err, proto.ErrOffline):
		return set(repointOutcome{state: store.ReplicaNeedsRepoint, offline: true}, waitMsg)
	case err != nil:
		return set(repointOutcome{state: store.ReplicaNeedsRepoint, err: err}, truncate("Arahkan ulang gagal: "+err.Error(), maxLogLine))
	case res.NeedsRebuild:
		return set(repointOutcome{state: store.ReplicaNeedsRebuild, errant: res.Errant},
			truncate("Perlu dibangun ulang: replika ini punya transaksi yang tidak ada di master baru ("+res.Errant+")", maxLogLine))
	}
	// SEC-63c: verified only when the agent saw the receiver connected
	// with SOURCE_SSL_VERIFY_SERVER_CERT=1; a receiver still connecting
	// stays unverified until Perketat.
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
		o.ioErr = truncate(res.IOError, maxLogLine)
		if o.ioErr == "" {
			o.ioErr = "penerima replikasi belum tersambung"
		}
	}
	return set(o, "")
}

// ErrNeedsRebuild answers RepointNow for a replica that holds transactions
// the new master lacks: it is now needs_rebuild.
var ErrNeedsRebuild = errors.New("replika punya transaksi yang tidak ada di master baru; bangun ulang dengan Resync ulang")

// RepointNow points a needs_repoint replica at its topology's new master
// now ("Arahkan sekarang"), instead of waiting for its agent to reconnect.
func (r *Runner) RepointNow(ctx context.Context, replicaServerID int64) error {
	topo, rep, err := func() (store.Topology, store.Replica, error) {
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
	r.repointMu.Lock()
	defer r.repointMu.Unlock()
	// Re-read under repointMu: OnOnline may have repointed it meanwhile.
	if rep, err = r.d.Store.GetReplicaByServer(replicaServerID); err != nil {
		return err
	}
	if rep.State != store.ReplicaNeedsRepoint {
		return nil
	}
	out := r.repointReplica(ctx, topo, rep, 0)
	switch {
	case out.offline:
		return errors.New("agent replika terputus; replika diarahkan otomatis saat agent tersambung lagi")
	case out.err != nil:
		return out.err
	case out.state == store.ReplicaNeedsRebuild:
		return fmt.Errorf("%w (%s)", ErrNeedsRebuild, out.errant)
	}
	return nil
}

// OnOnline runs when an agent connects (the panel calls it after the
// Monitor's): a quarantined old master whose quarantine is pending gets
// mysql.quarantine, a replica in needs_repoint whose topology's master was
// swapped gets mysql.repoint. It runs under the hub's lifecycle lock, so
// the work happens on its own goroutine.
func (r *Runner) OnOnline(serverID int64) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(r.base, onlineHookTimeout)
		defer cancel()
		r.onOnline(ctx, serverID)
	}()
}

func (r *Runner) onOnline(ctx context.Context, serverID int64) {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return
	}
	r.repointMu.Lock()
	defer r.repointMu.Unlock()
	switch srv.Role {
	case store.RoleQuarantined:
		prom, err := r.d.Store.PromotionByOldMaster(serverID)
		if err != nil || !QuarantinePending(prom) {
			return
		}
		topo, err := r.d.Store.GetTopologyByOldMaster(serverID)
		if err != nil || topo.MasterServerID != prom.NewMasterServerID {
			return
		}
		password, err := r.replPassword(topo)
		if err != nil {
			r.log.Warn("pending quarantine", "server_id", serverID, "err", err)
			return
		}
		if err := r.sendQuarantine(ctx, prom.ID, serverID, topo.MasterServerID, topo.SourceHost, topo.SourcePort,
			topo.ReplUser, password); err != nil {
			r.log.Warn("pending quarantine failed", "server_id", serverID, "err", logErr(err))
		}
	case store.RoleReplica:
		rep, err := r.d.Store.GetReplicaByServer(serverID)
		if err != nil || rep.State != store.ReplicaNeedsRepoint {
			return
		}
		topo, err := r.d.Store.GetTopology(rep.TopologyID)
		if err != nil {
			return
		}
		// Only after a promotion (the old master may since have been
		// rebuilt or deleted, which clears OldMasterServerID).
		if _, err := r.d.Store.LatestPromotion(topo.ID); err != nil {
			return
		}
		// A promotion still running repoints its replicas itself.
		if _, busy, err := r.d.Store.ActiveJobForTopology(topo.ID, KindPromotePlanned, KindPromoteEmergency); err != nil || busy {
			return
		}
		out := r.repointReplica(ctx, topo, rep, 0)
		r.log.Info("replica repointed on reconnect", "server_id", serverID, "state", out.state, "err", logErr(out.err))
	}
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
// sends it when the agent connects.
func QuarantinePending(p store.Promotion) bool {
	for _, s := range PromotionServers(p) {
		if s.Role == PromotionOldMaster {
			return s.State == QuarantineStatePending
		}
	}
	return false
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
	r.updatePromotion(promID, func(p *store.Promotion) {
		list := PromotionServers(*p)
		if i := slices.IndexFunc(list, func(x PromotionServer) bool { return x.ServerID == s.ServerID }); i >= 0 {
			list[i] = s
		} else {
			list = append(list, s)
		}
		if b, err := json.Marshal(list); err == nil {
			p.ReplicasJSON = string(b)
		}
	})
}

func (r *Runner) markQuarantine(promID, oldID int64, pending bool, detail string) {
	state := QuarantineStateDone
	if pending {
		state = QuarantineStatePending
	}
	r.setPromotionServer(promID, PromotionServer{ServerID: oldID, Role: PromotionOldMaster, State: state, Detail: detail})
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
	r.setPromotionServer(promID, PromotionServer{ServerID: serverID, Role: PromotionReplica, State: state, Detail: detail})
}
