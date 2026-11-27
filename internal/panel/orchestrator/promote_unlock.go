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
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// "Buka kunci promote" (spec 5.2): the operator's own action that asks the
// master's agent to lift the promote lock a failed or canceled planned
// promotion left behind, with the candidate's credential (the agent checks
// that the candidate is still a read-only replica, and every replica the
// lock lists). No password and no gate: it only gives writes back to a
// healthy master, the agent checks, and a password here would lengthen a
// write stop the panel made itself. It is never sent for a job whose
// mysql.promote was sent (the candidate may be opening writes), nor for a
// job a newer promotion of its topology superseded (it could open the old
// master beside the new one).

// Texts of the action (spec 5.11; P1 and P2 are not in the spec).
const (
	// textUnlockSent is P1.
	textUnlockSent = `Perintah promote pekerjaan ini sudah dikirim ke kandidat, jadi kunci promote tidak dibuka dari panel. ` +
		`Tekan "Ulangi dari tahap ini" pada pekerjaan ini; panel memeriksa kandidat dulu.`
	textUnlockRunning = "promote masih berjalan; tunggu sampai selesai"
	// textUnlockSuperseded is T11s.
	textUnlockSuperseded = "Kunci promote ini tidak dibuka dari panel: topologi ini sudah promote lagi sejak pekerjaan ini, " +
		"atau server itu bukan lagi masternya. Server itu ditangani sebagai master lama di halamannya."
	// textUnlockNewerOpen is T11t; %s is the newer promotion's job.
	textUnlockNewerOpen = "Kunci promote ini tidak dibuka dari panel selagi promote yang lebih baru di topologi ini belum " +
		"tuntas. Selesaikan dulu pekerjaan promote %s."
	// textUnlockOpened is T10.
	textUnlockOpened = "Kunci promote di master %s dibuka; aplikasi bisa menulis lagi."
	// textUnlockNotLocked is T10b.
	textUnlockNotLocked = `Master %s tidak sedang dikunci untuk promote; tidak ada yang dibuka. Bila master masih berstatus ` +
		`"Terkunci", sebabnya lain: tekan "Buka kunci master".`
	// textUnlockNoResync is the end of T11 and T11b, from "Jangan Resync
	// kandidat" on.
	textUnlockNoResync = "Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan " +
		"dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master"
	// textUnlockOffline is T11b without its end.
	textUnlockOffline = "Agent master %s tidak tersambung, jadi panel tidak bisa membuka kuncinya. Sambungkan agent itu lalu " +
		"tekan lagi. "
	// textUnlockRefused starts T11, T11c and T11d; the agent's words follow.
	textUnlockRefused = "Agent master %s tidak membuka kunci. %s. "
	// textUnlockCandidateWritable is the end of T11c.
	textUnlockCandidateWritable = "Kandidat %s terbaca bisa ditulis dan mungkin sudah menjadi master: jangan buka master ini. " +
		"Periksa kandidat itu dulu. Bila ia memang bukan master, kunci lagi di halaman replikanya, lalu tekan tombol ini lagi."
	// textUnlockQuarantined is the end of T11d.
	textUnlockQuarantined = "Server ini sudah dikarantina; tindakannya ada di kotak Master lama di halaman server."
	// textUnlockNoCredential is P2; T11's end follows.
	textUnlockNoCredential = "Panel tidak bisa menyusun kredensial kandidat %s untuk membuka kunci (%s). "
	// textPromoteLockStands is T12b.
	textPromoteLockStands = `Master %s mungkin masih terkunci oleh promote ini. Tekan "Buka kunci promote" dulu; sesudah ` +
		`kuncinya terbuka, "Ulangi dari tahap ini" tersedia lagi.`
	// textLoadLockStands is T15b: the master, the action, the promote job.
	textLoadLockStands = `Master %s mungkin masih terkunci oleh promote terencana sebelumnya. %s tidak dijalankan selama ` +
		`itu: memuat data membuat replika bisa ditulis sebentar, dan penjaga kunci master bisa mengkarantina master. ` +
		`Tekan "Buka kunci promote" pada pekerjaan promote %s dulu.`
	// textEmergencyLockStands is T15, the end of the emergency promotion's
	// refusal while a lock may stand.
	textEmergencyLockStands = `Sebelum itu, bila agent master tersambung, tekan "Buka kunci promote" pada pekerjaan promote ` +
		`terencana itu: begitu agent menjawab, panel mencatat kunci terbuka dan promote darurat bisa langsung dijalankan.`
	// textUnlockFailed is T17, the job log line when the panel could not
	// lift the lock (also for a canceled promotion); %v is why.
	textUnlockFailed = `Kunci master tidak bisa dibuka (%v). Pada tenggat kunci agent master membukanya sendiri bila ` +
		`kandidat masih replika dan semua replika terbaca; bila tidak, kunci dipertahankan. Tekan "Buka kunci promote" untuk ` +
		`mencoba lagi. Jangan Resync kandidat selama master masih terkunci.`
)

// notLockedForPromote is the agent's answer to mysql.unlockPromote when no
// promote lock is set: the one agent sentence the panel reads (spec 5.2).
const notLockedForPromote = "tidak dikunci untuk promosi"

// unlockCandidateRead bounds the candidate's mysql.status after the
// master's agent refused the unlock.
const unlockCandidateRead = 10 * time.Second

// UnlockOutcome is how "Buka kunci promote" ended when it was not refused.
type UnlockOutcome int

const (
	// UnlockOpened: the master's agent lifted the lock (T10).
	UnlockOpened UnlockOutcome = iota + 1
	// UnlockNotLocked: the master was not locked for a promotion (T10b).
	UnlockNotLocked
)

// Codes of UnlockLockError.
const (
	UnlockSent              = "sent"
	UnlockRunning           = "running"
	UnlockSuperseded        = "superseded"
	UnlockNewerOpen         = "newer_open"
	UnlockAgentOffline      = "agent_offline"
	UnlockCandidateWritable = "candidate_writable"
	UnlockQuarantined       = "quarantined"
	UnlockRefused           = "refused"
	UnlockNoCredential      = "no_credential"
)

// UnlockLockError is a refusal of "Buka kunci promote"; Code picks the text.
type UnlockLockError struct {
	Code       string // sent | running | superseded | newer_open | agent_offline | candidate_writable | quarantined | refused | no_credential
	Master     string // server names
	Candidate  string
	AgentText  string // labelled and bounded; "" when no agent answered
	NewerJobID string // newer_open: the promote job to finish first
	Cause      string // no_credential: why
}

func (e *UnlockLockError) Error() string {
	refused := func() string {
		words := strings.TrimRight(e.AgentText, ". ")
		return fmt.Sprintf(textUnlockRefused, e.Master, words)
	}
	switch e.Code {
	case UnlockSent:
		return textUnlockSent
	case UnlockRunning:
		return textUnlockRunning
	case UnlockSuperseded:
		return textUnlockSuperseded
	case UnlockNewerOpen:
		return fmt.Sprintf(textUnlockNewerOpen, e.NewerJobID)
	case UnlockAgentOffline:
		return fmt.Sprintf(textUnlockOffline, e.Master) + textUnlockNoResync
	case UnlockCandidateWritable:
		return refused() + fmt.Sprintf(textUnlockCandidateWritable, e.Candidate)
	case UnlockQuarantined:
		return refused() + textUnlockQuarantined
	case UnlockNoCredential:
		return fmt.Sprintf(textUnlockNoCredential, e.Candidate, e.Cause) + textUnlockNoResync
	}
	return refused() + textUnlockNoResync
}

// ErrPromoteLockStands refuses Ulangi of a planned promote job whose
// mysql.promote was not sent while its lock record stands (T12b); every
// *PromoteLockStandsError is it for errors.Is.
var ErrPromoteLockStands error = &PromoteLockStandsError{}

// PromoteLockStandsError is ErrPromoteLockStands naming the master.
type PromoteLockStandsError struct{ Master string }

func (e *PromoteLockStandsError) Error() string { return fmt.Sprintf(textPromoteLockStands, e.Master) }

func (*PromoteLockStandsError) Is(target error) bool {
	_, ok := target.(*PromoteLockStandsError)
	return ok
}

// LockStandsError refuses a load while a planned promote's lock record
// may stand (T15b); Task 4 adds the codes planned and master_locked.
type LockStandsError struct {
	Code   string // load
	Master string
	What   string // "Resync ulang", "Buat replika", "Tambah database", "Lepas database"
	JobID  string // the promote job whose lock record stands
}

func (e *LockStandsError) Error() string {
	return fmt.Sprintf(textLoadLockStands, e.Master, e.What, e.JobID)
}

// UnlockOutcomeText is the page's notice of out for master: T10 or T10b.
func UnlockOutcomeText(out UnlockOutcome, master string) string {
	if out == UnlockNotLocked {
		return fmt.Sprintf(textUnlockNotLocked, master)
	}
	return fmt.Sprintf(textUnlockOpened, master)
}

// unlockAnswer is how the master's agent answered mysql.unlockPromote.
type unlockAnswer int

const (
	unlockLifted unlockAnswer = iota + 1
	unlockWasNotLocked
)

// promoteUnlockCred is how the master's agent of st (a planned promotion
// of topology topoID) logs in to the candidate to check it before it lifts
// the promote lock: the candidate's stored address, the port the job read
// (3306 when it read none) and the replication account.
func (r *Runner) promoteUnlockCred(ctx context.Context, topoID int64, st jobState) (proto.PeerCred, error) {
	srv, err := r.d.Store.GetServer(st.CandidateServerID)
	if errors.Is(err, store.ErrNotFound) {
		return proto.PeerCred{}, errors.New("server kandidat sudah dihapus dari panel")
	}
	if err != nil {
		return proto.PeerCred{}, err
	}
	topo, err := r.d.Store.GetTopology(topoID)
	if err != nil {
		return proto.PeerCred{}, err
	}
	password, err := r.replPassword(topo)
	if err != nil {
		return proto.PeerCred{}, err
	}
	port := st.CandidatePort
	if port == 0 {
		port = 3306
	}
	return r.peerCred(ctx, st.MasterServerID, st.CandidateServerID, r.sourceHost(srv), port, topo.ReplUser, password)
}

// sendUnlockPromote sends mysql.unlockPromote with cred to the master's
// agent, bounded by unlockTimeout. Its answer "tidak dikunci untuk
// promosi" (notLockedForPromote) is unlockWasNotLocked; any other error is
// returned as it is (hub.SentByAgent tells an agent's refusal from no
// answer).
func (r *Runner) sendUnlockPromote(ctx context.Context, masterID int64, cred proto.PeerCred) (unlockAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, r.unlockTimeout)
	defer cancel()
	err := r.d.Hub.Call(ctx, masterID, "mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: cred}, nil)
	switch {
	case err == nil:
		return unlockLifted, nil
	case hub.SentByAgent(err) && strings.Contains(err.Error(), notLockedForPromote):
		return unlockWasNotLocked, nil
	}
	return 0, err
}

// unlockTarget is the job "Buka kunci promote" acts on, as checked.
type unlockTarget struct {
	job               store.Job
	st                jobState
	master, candidate int64
}

// unlockChecks are rules 1 to 4 of UnlockPromoteLock. Callers hold startMu.
func (r *Runner) unlockChecks(jobID string) (unlockTarget, error) {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil {
		return unlockTarget{}, err
	}
	if j.Kind != KindPromotePlanned {
		return unlockTarget{}, errors.New("pekerjaan ini bukan promote terencana; kunci promote tidak dibuka dari sini")
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return unlockTarget{}, fmt.Errorf("catatan promote ini tidak terbaca: %w", err)
	}
	t := unlockTarget{job: j, st: st, master: st.MasterServerID, candidate: st.CandidateServerID}
	if t.master == 0 {
		t.master = j.ServerID
	}
	t.st.MasterServerID = t.master
	name := r.nameOf(t.master)
	if st.PromoteSent || st.PastPoint {
		return t, &UnlockLockError{Code: UnlockSent, Master: name}
	}
	switch j.Status {
	case store.JobQueued, store.JobRunning, store.JobWaiting:
		return t, &UnlockLockError{Code: UnlockRunning, Master: name}
	}
	if err := r.unlockSuperseded(t); err != nil {
		return t, err
	}
	return t, nil
}

// unlockSuperseded is rule 4: a promotion of the topology done after the
// job's, or the job's master no longer its topology's master, supersedes
// it (T11s); a newer one still running or partial keeps the button off
// until it is settled (T11t).
func (r *Runner) unlockSuperseded(t unlockTarget) error {
	name := r.nameOf(t.master)
	superseded := &UnlockLockError{Code: UnlockSuperseded, Master: name}
	topo, err := r.d.Store.GetTopology(t.job.TopologyID)
	if errors.Is(err, store.ErrNotFound) {
		return superseded
	}
	if err != nil {
		return err
	}
	if topo.MasterServerID != t.master {
		return superseded
	}
	proms, err := r.d.Store.ListPromotions(topo.ID)
	if err != nil {
		return err
	}
	var newer *store.Promotion
	for i, p := range proms { // newest first
		if p.ID <= t.st.PromotionID || p.JobID == t.job.ID {
			break
		}
		switch p.Status {
		case promotionDone:
			return superseded
		case promotionRunning, promotionPartial:
			if newer == nil {
				newer = &proms[i]
			}
		}
	}
	if newer != nil {
		return &UnlockLockError{Code: UnlockNewerOpen, Master: name, NewerJobID: newer.JobID}
	}
	return nil
}

// UnlockPromoteLock is "Buka kunci promote" of job jobID (spec 5.2): rules
// 1 to 4 (unlockChecks) under startMu; the agent of the job's master is
// asked without it; the record (LockUnlocked) is stored only after rules 1
// to 4 passed again under startMu. A refusal is an *UnlockLockError except
// for rule 1 and a store failure.
func (r *Runner) UnlockPromoteLock(ctx context.Context, jobID string) (UnlockOutcome, error) {
	r.startMu.Lock()
	t, err := r.unlockChecks(jobID)
	r.startMu.Unlock()
	if err != nil {
		return 0, err
	}
	masterName, candName := r.nameOf(t.master), r.nameOf(t.candidate)
	if !r.d.Hub.IsOnline(t.master) {
		return 0, &UnlockLockError{Code: UnlockAgentOffline, Master: masterName}
	}
	cred, err := r.promoteUnlockCred(ctx, t.job.TopologyID, t.st)
	if err != nil {
		r.log.Warn("unlock promote: candidate credential", "job", jobID, "err", logErr(err))
		return 0, &UnlockLockError{Code: UnlockNoCredential, Master: masterName, Candidate: candName,
			Cause: r.agentErrText(err)}
	}
	ans, err := r.sendUnlockPromote(ctx, t.master, cred)
	if err != nil {
		r.log.Warn("unlock promote refused", "job", jobID, "master", t.master, "err", logErr(err))
		if !hub.SentByAgent(err) {
			// No answer (K12): the panel cannot tell whether the lock is
			// gone; the record stands.
			return 0, &UnlockLockError{Code: UnlockAgentOffline, Master: masterName}
		}
		return 0, r.unlockRefusal(ctx, t, err)
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if _, err := r.unlockChecks(jobID); err != nil {
		r.log.Warn("unlock promote: job changed while the agent answered; record not stored", "job", jobID, "err", logErr(err))
		return 0, err
	}
	out := UnlockOpened
	if ans == unlockWasNotLocked {
		out = UnlockNotLocked
	}
	if err := r.storeLockOpened(jobID, UnlockOutcomeText(out, masterName)); err != nil {
		return 0, err
	}
	r.log.Info("promote lock lifted by the operator", "job", jobID, "master", t.master, "not_locked", ans == unlockWasNotLocked)
	return out, nil
}

// unlockRefusal words the master's agent's refusal err of the unlock of
// t: its words labelled and bounded, followed by a tail picked from the
// state the panel reads, never from those words: the master quarantined
// (its role, or the pin its live report shows) T11d; the candidate read
// writable T11c; otherwise (read-only, or not readable) T11.
func (r *Runner) unlockRefusal(ctx context.Context, t unlockTarget, err error) error {
	words, _ := fromAgent(err)
	e := &UnlockLockError{Code: UnlockRefused, Master: r.nameOf(t.master), Candidate: r.nameOf(t.candidate),
		AgentText: AgentQuote(r.nameOf(t.master), words)}
	if r.masterQuarantined(t.master) {
		e.Code = UnlockQuarantined
		return e
	}
	cctx, cancel := context.WithTimeout(ctx, unlockCandidateRead)
	defer cancel()
	var cs proto.MySQLStatus
	if err := r.d.Hub.Call(cctx, t.candidate, "mysql.status", nil, &cs); err == nil && !cs.SuperReadOnly {
		e.Code = UnlockCandidateWritable
	}
	return e
}

// masterQuarantined reports whether the panel knows id as quarantined: its
// role, or the role pin of its live report.
func (r *Runner) masterQuarantined(id int64) bool {
	if srv, err := r.d.Store.GetServer(id); err == nil && srv.Role == store.RoleQuarantined {
		return true
	}
	v, live := r.liveView(id)
	return live && v.Snap != nil && v.Snap.RolePin == store.RoleQuarantined
}

// storeLockOpened stores the lock of job id as confirmed open (no
// deadline, not tried, LockUnlocked) and logs line. Callers hold startMu
// and checked that the job is not active.
func (r *Runner) storeLockOpened(id, line string) error {
	j, _, err := r.d.Store.GetJob(id)
	if err != nil {
		return err
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return fmt.Errorf("catatan promote ini tidak terbaca: %w", err)
	}
	st.LockDeadline, st.LockTried, st.LockUnlocked = nil, false, true
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := r.d.Store.SetJobParams(id, string(b)); err != nil {
		return err
	}
	now := r.now()
	lid, err := r.d.Store.AppendJobLog(id, "info", line, now)
	if err != nil {
		r.log.Warn("job log", "job", id, "err", err)
		return nil
	}
	r.d.Publish("job_log", map[string]any{"job_id": id, "id": lid, "ts": now, "level": "info", "message": line})
	return nil
}

// UnlockLockOffered reports whether the job page offers "Buka kunci
// promote" for jobID: a planned promote job that failed or was canceled,
// whose mysql.promote was not sent, whose lock record stands (LockRecords)
// and that no newer promotion supersedes (rule 4).
func (r *Runner) UnlockLockOffered(jobID string) bool {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil || j.Kind != KindPromotePlanned || (j.Status != store.JobFailed && j.Status != store.JobCanceled) {
		return false
	}
	t, err := r.unlockChecks(jobID)
	if err != nil {
		return false
	}
	recs, err := r.LockRecords(t.master)
	if err != nil {
		r.log.Warn("lock records", "job", jobID, "err", err)
		return false
	}
	return slices.ContainsFunc(recs, func(rec LockRecord) bool { return rec.JobID == jobID })
}

// UnlockLockJob is the planned promote job the master page's button acts
// on, while the master reads "Terkunci": the newest job with a standing
// lock record that is not active; when there is none, the latest planned
// promotion of that master that failed or was canceled, was not sent and
// is not superseded (the panel's record may be wrong; the agent knows).
// sent: that record's job sent mysql.promote, so the page offers no
// button. "" when there is no such job.
func (r *Runner) UnlockLockJob(masterID int64) (jobID string, sent bool, err error) {
	recs, err := r.LockRecords(masterID)
	if err != nil {
		return "", false, err
	}
	for _, rec := range recs {
		j, _, err := r.d.Store.GetJob(rec.JobID)
		if err != nil {
			return "", false, err
		}
		switch j.Status {
		case store.JobQueued, store.JobRunning, store.JobWaiting:
			continue // its own run decides about its lock
		}
		return rec.JobID, rec.Sent, nil
	}
	topo, err := r.d.Store.GetTopologyByMaster(masterID)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	proms, err := r.d.Store.ListPromotions(topo.ID)
	if err != nil {
		return "", false, err
	}
	for _, p := range proms {
		if p.Status == promotionDone {
			break
		}
		if p.Mode != proto.PromotePlanned || p.OldMasterServerID != masterID || p.JobID == "" {
			continue
		}
		j, _, err := r.d.Store.GetJob(p.JobID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if j.Status != store.JobFailed && j.Status != store.JobCanceled {
			continue
		}
		if _, err := r.unlockChecks(j.ID); err != nil {
			continue
		}
		return j.ID, false, nil
	}
	return "", false, nil
}

// refusePlannedRetryWhileLockStands refuses Ulangi of j, a planned promote
// job whose mysql.promote was not sent, while its lock record stands
// (T12b), before anything is sent: "Buka kunci promote" lifts the lock
// first. A job that was sent or passed the point of no return is never
// refused for its record. Callers hold startMu (settleLockRecords).
func (r *Runner) refusePlannedRetryWhileLockStands(j store.Job) error {
	master, stands, err := r.plannedLockStands(j, r.settleLockRecords)
	if err != nil || !stands {
		return err
	}
	return &PromoteLockStandsError{Master: r.nameOf(master)}
}

// plannedLockStands reports whether j is a planned promote job not sent
// whose lock record stands by records (LockRecords or settleLockRecords),
// with its master.
func (r *Runner) plannedLockStands(j store.Job, records func(int64) ([]LockRecord, error)) (int64, bool, error) {
	if j.Kind != KindPromotePlanned {
		return 0, false, nil
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return 0, false, nil // left to the checks that refuse it
	}
	if st.PromoteSent || st.PastPoint {
		return 0, false, nil
	}
	master := st.MasterServerID
	if master == 0 {
		master = j.ServerID
	}
	recs, err := records(master)
	if err != nil {
		return 0, false, err
	}
	return master, slices.ContainsFunc(recs, func(rec LockRecord) bool { return rec.JobID == j.ID }), nil
}
