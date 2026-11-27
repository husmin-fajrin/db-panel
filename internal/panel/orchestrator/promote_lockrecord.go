package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The lock record of a planned promotion (spec 5.2, "Catatan kunci"): once
// mysql.lockForPromote was sent, the master may be locked for that
// promotion's candidate, even when the agent answered with an error (it
// can answer one after the lock is set). The record is the job's own
// fields (K9): it stands while (LockTried || LockDeadline != nil) &&
// !LockUnlocked, the job's master is still its topology's master, no
// promotion of that topology finished (done) after it, and it is not
// proven gone. It is proven gone only by the master's agent (unlockPromote
// answered unlocked, or "tidak dikunci untuk promosi"; promoteUndo stores
// that), or, past its Until, by a live report of the master (monitor.Live)
// with both read-only flags off: a standing promote lock keeps
// super_read_only on, its guard sets it again every tick.

// textLockGoneLive is the job log line of a record settleLockRecords
// stores as gone (P9, not in the spec).
const textLockGoneLive = "Kunci promote terbukti sudah tidak ada: master melapor bisa ditulis sesudah tenggat kunci."

// LockRecord is a standing lock record of a planned promotion.
type LockRecord struct {
	JobID                 string
	PromotionID           int64
	MasterID, CandidateID int64
	Until                 time.Time // clamped deadline + proto.PromoteLockGrace
	Sent                  bool      // PromoteSent || PastPoint
}

// lockDeadlineCap is the latest deadline a lock asked for at triedAt can
// have: the lock's length, the call's bound and the clock skew between
// the panel and the master allowed for.
func lockDeadlineCap(triedAt time.Time) time.Time {
	return triedAt.Add(promoteLockSec*time.Second + lockCallSlack + promoteClockSkew)
}

// lockUntil is when the master's guard has decided about the lock of st
// (a planned promotion recorded as prom) at the latest: its deadline, cut
// to lockDeadlineCap of when it was asked for (also for a job stored by
// 0.2.0), plus the guard's grace. Without a stored deadline or without
// the time the lock was asked for, the earlier fallbacks apply.
func lockUntil(st jobState, prom store.Promotion) time.Time {
	var deadline time.Time
	switch {
	case st.LockDeadline != nil:
		deadline = *st.LockDeadline
		if st.LockTriedAt != nil {
			if limit := lockDeadlineCap(*st.LockTriedAt); deadline.After(limit) {
				deadline = limit
			}
		}
	case st.LockTriedAt != nil:
		deadline = st.LockTriedAt.Add(promoteLockSec*time.Second + lockCallSlack)
	case prom.FinishedAt != nil:
		deadline = prom.FinishedAt.Add(promoteLockSec * time.Second)
	default:
		deadline = prom.StartedAt.Add(promoteLockSec*time.Second + defaultPromoteCatchUp + lockCallSlack)
	}
	return deadline.Add(proto.PromoteLockGrace)
}

// lockCandidate is a lock record that stands by the job's fields, with
// whether the master's live report proves it gone.
type lockCandidate struct {
	rec  LockRecord
	gone bool
}

// lockCandidates reads every planned promotion of masterID's topology
// whose record stands by the job's fields, newest first, and marks those
// the master's live report proves gone. It writes nothing.
func (r *Runner) lockCandidates(masterID int64) ([]lockCandidate, error) {
	topo, err := r.d.Store.GetTopologyByMaster(masterID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // no longer a master: no record of it counts
	}
	if err != nil {
		return nil, err
	}
	proms, err := r.d.Store.ListPromotions(topo.ID)
	if err != nil {
		return nil, err
	}
	now := r.now()
	var (
		out           []lockCandidate
		known, opened bool
	)
	for _, prom := range proms {
		if prom.Mode == proto.PromotePlanned && prom.OldMasterServerID == masterID && prom.JobID != "" {
			c, ok, err := r.lockCandidate(prom, masterID)
			if err != nil {
				return nil, err
			}
			if ok {
				if !now.Before(c.rec.Until) {
					if !known {
						opened, known = r.masterReportsOpen(masterID), true
					}
					c.gone = opened
				}
				out = append(out, c)
			}
		}
		// A promotion that finished replaced the master; the records
		// before it no longer count.
		if prom.Status == promotionDone {
			break
		}
	}
	return out, nil
}

// lockCandidate is the record of prom's job when it stands by the job's
// fields.
func (r *Runner) lockCandidate(prom store.Promotion, masterID int64) (lockCandidate, bool, error) {
	j, _, err := r.d.Store.GetJob(prom.JobID)
	if errors.Is(err, store.ErrNotFound) {
		return lockCandidate{}, false, nil
	}
	if err != nil {
		return lockCandidate{}, false, err
	}
	if j.Kind != KindPromotePlanned {
		return lockCandidate{}, false, nil
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return lockCandidate{}, false, fmt.Errorf("catatan promote sebelumnya tidak terbaca: %w", err)
	}
	if st.MasterServerID != 0 && st.MasterServerID != masterID {
		return lockCandidate{}, false, nil
	}
	if st.LockUnlocked || (!st.LockTried && st.LockDeadline == nil) {
		return lockCandidate{}, false, nil
	}
	cand := st.CandidateServerID
	if cand == 0 {
		cand = prom.NewMasterServerID
	}
	return lockCandidate{rec: LockRecord{JobID: j.ID, PromotionID: prom.ID, MasterID: masterID, CandidateID: cand,
		Until: lockUntil(st, prom), Sent: st.PromoteSent || st.PastPoint}}, true, nil
}

// masterReportsOpen reports whether the master's live report shows MySQL
// up with read_only and super_read_only both off.
func (r *Runner) masterReportsOpen(masterID int64) bool {
	v, live := r.liveView(masterID)
	return live && v.Snap.MySQLOK && !v.Snap.ReadOnly && !v.Snap.SuperReadOnly
}

// LockRecords returns the standing lock records of masterID's planned
// promotions, newest first. It writes nothing (K10): a record the master's
// live report proves gone is left out, and stored only by
// settleLockRecords.
func (r *Runner) LockRecords(masterID int64) ([]LockRecord, error) {
	cs, err := r.lockCandidates(masterID)
	if err != nil {
		return nil, err
	}
	var out []LockRecord
	for _, c := range cs {
		if !c.gone {
			out = append(out, c.rec)
		}
	}
	return out, nil
}

// settleLockRecords is LockRecords, and stores LockUnlocked, with the job
// log line P9, on each record the master's live report proves gone.
// Callers hold startMu, so no Ulangi reopens such a job meanwhile; a job
// that is still active is left to its own run.
func (r *Runner) settleLockRecords(masterID int64) ([]LockRecord, error) {
	cs, err := r.lockCandidates(masterID)
	if err != nil {
		return nil, err
	}
	var out []LockRecord
	for _, c := range cs {
		if !c.gone {
			out = append(out, c.rec)
			continue
		}
		if err := r.storeLockGone(c.rec.JobID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// storeLockGone stores LockUnlocked on job id, unless it is active, and
// logs P9.
func (r *Runner) storeLockGone(id string) error {
	j, _, err := r.d.Store.GetJob(id)
	if err != nil {
		return err
	}
	switch j.Status {
	case store.JobQueued, store.JobRunning, store.JobWaiting:
		return nil
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		return fmt.Errorf("catatan promote sebelumnya tidak terbaca: %w", err)
	}
	st.LockUnlocked = true
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := r.d.Store.SetJobParams(id, string(b)); err != nil {
		return err
	}
	r.log.Info("promote lock record cleared by a live report", "job", id, "master", j.ServerID)
	now := r.now()
	lid, err := r.d.Store.AppendJobLog(id, "info", textLockGoneLive, now)
	if err != nil {
		r.log.Warn("job log", "job", id, "err", err)
		return nil
	}
	r.d.Publish("job_log", map[string]any{"job_id": id, "id": lid, "ts": now, "level": "info", "message": textLockGoneLive})
	return nil
}
