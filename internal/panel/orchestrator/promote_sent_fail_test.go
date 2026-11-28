package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Fix round 1 of Task 8 (review I2, I3): a store failure anywhere on the
// path that settles a sent promote as over leaves a job with a working
// next step, never the old master unlocked while the job reads sent; and
// a job.status that fails says "may still run".

// trigger installs sqlite trigger sql on e's store file and returns its
// removal.
func (e *env) trigger(name, sqlText string) func() {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec(sqlText); err != nil {
		db.Close()
		e.t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec("DROP TRIGGER " + name); err != nil {
			e.t.Error(err)
		}
		db.Close()
	}
}

// wantStillSent fails unless the job is partial with params, its promotion
// partial, and no unlock was sent since unlocks.
func (g *promoteRig) wantStillSent(t *testing.T, id, params string, unlocks int) {
	t.Helper()
	g.wantUnchanged(t, id, params)
	if !g.e.storedState(id).PromoteSent {
		t.Fatal("stored not sent")
	}
	if n := g.m.count("mysql.unlockPromote"); n != unlocks {
		t.Fatalf("mysql.unlockPromote sent %d times, want %d", n, unlocks)
	}
}

// TestRetrySentOverStoreFailureChangesNothing: the transaction that fails
// a sent promote proven over (params "not sent", job failed, promotion
// failed) fails: Retry answers the failure, nothing changed, no unlock;
// the next Ulangi settles it. Both kinds, an injected failure and a real
// one inside the transaction (a trigger refusing the promotion's update:
// the job's write before it is rolled back too).
func TestRetrySentOverStoreFailureChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name      string
		emergency bool
		want      string
		code      string
	}{
		{"emergency", true, wantT13, PromoteSentOverEmergency},
		{"planned", false, wantT13b, PromoteSentOverPlanned},
	} {
		t.Run(c.name+" injected", func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _, _ := g.sentUnanswered(t, c.emergency)
			candStatus(g.c, true, new(false))
			params, unlocks := e.paramsOf(id), g.m.count("mysql.unlockPromote")
			e.r.failSentStore = func(string, string, string, int, string, int64, time.Time) error {
				return errors.New("database is locked")
			}
			if err := e.r.Retry(id, JobAck{}); err == nil || err.Error() != "database is locked" {
				t.Fatalf("Retry = %v, want the store's failure", err)
			}
			g.wantStillSent(t, id, params, unlocks)
			e.r.failSentStore = nil
			sentRefusal(t, e.r.Retry(id, JobAck{}), c.code, fmt.Sprintf(c.want, "cand"))
			g.wantSentFailed(t, id, fmt.Sprintf(c.want, "cand"))
		})
		t.Run(c.name+" inside the transaction", func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _, _ := g.sentUnanswered(t, c.emergency)
			candStatus(g.c, true, new(false))
			params, unlocks := e.paramsOf(id), g.m.count("mysql.unlockPromote")
			drop := e.trigger("no_failed_promotion", `CREATE TRIGGER no_failed_promotion BEFORE UPDATE OF status ON promotions
				WHEN NEW.status = 'failed' BEGIN SELECT RAISE(ABORT, 'promotion not writable'); END`)
			if err := e.r.Retry(id, JobAck{}); err == nil {
				t.Fatal("Retry with the promotion's update refused succeeded")
			}
			g.wantStillSent(t, id, params, unlocks)
			drop()
			sentRefusal(t, e.r.Retry(id, JobAck{}), c.code, fmt.Sprintf(c.want, "cand"))
			g.wantSentFailed(t, id, fmt.Sprintf(c.want, "cand"))
		})
	}
	t.Run("job changed", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, _, _ := g.sentUnanswered(t, false)
		e.r.failSentStore = func(string, string, string, int, string, int64, time.Time) error { return store.ErrJobChanged }
		candStatus(g.c, true, new(false))
		params := e.paramsOf(id)
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentChanged, wantP6)
		g.wantStillSent(t, id, params, g.m.count("mysql.unlockPromote"))
	})
}

// TestRetrySentOverUnlockFailureOffersButton: after the job is failed not
// sent, the lift of the planned promotion's lock fails (the master's agent
// refuses, or the record cannot be stored): the record stands, so "Buka
// kunci promote" is offered and works; T13b says to press it.
func TestRetrySentOverUnlockFailureOffersButton(t *testing.T) {
	t.Run("agent refuses", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, _, _ := g.sentUnanswered(t, false)
		candStatus(g.c, true, new(false))
		g.m.fail("mysql.unlockPromote", keptRefusal)
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
		g.wantSentFailed(t, id, fmt.Sprintf(wantT13b, "cand"))
		if !g.hasRecord(id) || !e.r.UnlockLockOffered(id) {
			t.Fatal("Buka kunci promote not offered after a failed unlock")
		}
		g.m.reply("mysql.unlockPromote", nil)
		if out, err := e.r.UnlockPromoteLock(context.Background(), id); err != nil || out != UnlockOpened {
			t.Fatalf("UnlockPromoteLock = %v, %v", out, err)
		}
	})
	t.Run("record not stored", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, _, _ := g.sentUnanswered(t, false)
		candStatus(g.c, true, new(false))
		failParamsWith(e, `"lock_unlocked":true`, false)
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
		if st := e.storedState(id); st.PromoteSent || st.LockUnlocked {
			t.Fatalf("job state = %+v, want not sent, the lock not stored open", st)
		}
		e.r.paramsStore = nil
		if !e.r.UnlockLockOffered(id) {
			t.Fatal("Buka kunci promote not offered")
		}
		if _, err := e.r.UnlockPromoteLock(context.Background(), id); err != nil {
			t.Fatalf("UnlockPromoteLock: %v", err)
		}
		if !e.storedState(id).LockUnlocked {
			t.Fatal("the lock not stored open")
		}
	})
}

// TestPromoteFailedJobStatusWriteFailsCancelable: in a run, "not sent" and
// the promotion's failure are stored and the lock lifted, but the job's own
// failed status cannot be written: the job reads running with no run, not
// sent, so "Batalkan" ends it.
func TestPromoteFailedJobStatusWriteFailsCancelable(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return nil, errors.New("master lama tidak read-only")
	})
	candStatus(g.c, true, new(false))
	drop := e.trigger("no_failed_job", `CREATE TRIGGER no_failed_job BEFORE UPDATE OF status ON jobs
		WHEN NEW.status = 'failed' AND NEW.kind = 'promote_planned' BEGIN SELECT RAISE(ABORT, 'job not writable'); END`)
	defer drop()
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitFor("unlock", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	e.waitIdle(id)
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobRunning {
		t.Fatalf("job = %s, want running (its failed status refused)", j.Status)
	}
	if st := e.storedState(id); st.PromoteSent {
		t.Fatalf("job state = %+v, want not sent", st)
	}
	if p := g.promotion(); p.Status != promotionFailed {
		t.Fatalf("promotion = %s, want failed", p.Status)
	}
	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	e.waitJob(id, store.JobCanceled)
}

// TestSentVerdictJobStatusErrorMayRun (review I3): the candidate is
// read-only, sends no promote_request, and the panel holds the job id: a
// job.status that times out or answers an internal error says the job may
// still run (T14b through Retry, nothing changed); "not known" says ended.
func TestSentVerdictJobStatusErrorMayRun(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(fa *fakeAgent)
		want  sentVerdict
	}{
		{"hub timeout", func(fa *fakeAgent) {
			fa.mu.Lock()
			fa.dropReply = map[string]bool{"job.status": true}
			fa.mu.Unlock()
		}, sentRunning},
		{"agent internal error", func(fa *fakeAgent) {
			fa.mu.Lock()
			fa.jobStatusFail = "panic: runtime error"
			fa.mu.Unlock()
		}, sentRunning},
		{"not known to the agent", func(fa *fakeAgent) { fa.forgetJobs.Store(1) }, sentOver},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id := g.heldSent(t, endedPromoteJob(t, e, g.c))
			c.setup(g.c)
			j, _, _ := e.st.GetJob(id)
			if v, _ := e.r.promoteSentVerdict(context.Background(), j, e.storedState(id)); v != c.want {
				t.Fatalf("verdict = %v, want %v", v, c.want)
			}
			if c.want != sentRunning {
				return
			}
			params := e.paramsOf(id)
			err := e.r.Retry(id, JobAck{})
			if pe, ok := errors.AsType[*PromoteSentError](err); !ok || pe.Code != PromoteSentRunning {
				t.Fatalf("Retry = %v, want T14b", err)
			}
			g.wantUnchanged(t, id, params)
			if g.m.count("mysql.unlockPromote") != 0 {
				t.Fatal("master unlocked")
			}
		})
	}
}
