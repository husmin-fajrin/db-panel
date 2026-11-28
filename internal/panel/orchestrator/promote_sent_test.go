package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The texts of a sent promote's Ulangi, letter for letter (spec 5.11; P6
// is the task's).
const (
	wantT13  = `Promote darurat ini belum membuka master baru: kandidat %s masih baca-saja. Pekerjaan ini digagalkan; tidak ada yang berubah. Mulai promote darurat baru dari halaman master.`
	wantT13b = `Promote ini belum mengganti master: kandidat %s masih baca-saja. Pekerjaan ini digagalkan. Bila master masih terkunci, tekan "Buka kunci promote". Sesudah itu: "Ulangi dari tahap ini" bila konfirmasi terakhirnya belum lewat 24 jam, atau promote baru.`
	wantT14  = `Kandidat %s belum bisa dibaca, jadi panel belum tahu apakah ia sudah menerima tulisan. Tidak ada yang diubah. Tekan "Ulangi dari tahap ini" lagi sesudah agent kandidat tersambung.`
	wantT14b = `Pekerjaan promote di kandidat %s masih berjalan, atau belum terbukti berakhir. Tidak ada yang diubah. Tunggu sebentar, lalu tekan "Ulangi dari tahap ini" lagi. Bila agent kandidat tidak melaporkannya, panel menganggapnya berakhir pukul %s.`
	wantT14t = `Master %s tetap baca-saja selama itu. Bila kandidat tidak akan kembali, buka master di servernya: sudo dbpanel agent unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat bukan master dan tidak akan menjadi master: MySQL-nya terbaca baca-saja di server kandidat itu sendiri, atau host-nya mati.`
	wantT14u = `Master lama %s sudah kembali dan masih baca-saja oleh kunci promote terencana sebelumnya. Bila kandidat promote darurat ini tidak akan kembali, buka master lama itu di servernya: sudo dbpanel agent unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat promote darurat ini dan kandidat promote terencana sebelumnya bukan master dan tidak akan menjadi master: MySQL-nya terbaca baca-saja di servernya sendiri, atau host-nya mati.`
	wantP6   = `Keadaan pekerjaan ini berubah selagi panel memeriksa kandidat; muat ulang halaman ini.`
)

// pukul is a time as T14b shows it.
func pukul(t time.Time) string { return t.Local().Format("02-01-2006 15:04:05") }

// candStatus scripts fa's mysql.status: read-only or writable, and the
// field of Task 7 (nil: an agent of 0.2.0, which does not send it).
func candStatus(fa *fakeAgent, readOnly bool, request *bool) {
	fa.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID, ReadOnly: readOnly,
		SuperReadOnly: readOnly, TLSAvailable: true, PromoteRequest: request})
}

// sentUnanswered starts a promotion of g whose mysql.promote start call
// nobody answers: the candidate's agent runs its job (until release is
// closed, or it is canceled) and the answer is lost. The job ends
// partial; it returns the job, when mysql.promote was sent, and release.
// emergency: the master and d are away first (emergencyRig's state).
func (g *promoteRig) sentUnanswered(t *testing.T, emergency bool) (string, time.Time, chan struct{}) {
	t.Helper()
	e := g.e
	release := make(chan struct{})
	g.c.job("mysql.promote", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		select {
		case <-release:
		case <-j.Context().Done():
		}
		return nil, errors.New("promote dihentikan")
	})
	g.c.mu.Lock()
	g.c.dropReply = map[string]bool{"mysql.promote": true}
	g.c.mu.Unlock()
	p := PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}
	if emergency {
		g.offline(g.m)
		g.offline(g.d)
		for _, fa := range []*fakeAgent{g.c, g.b} {
			fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
		}
		p.OldMasterDeadAck, p.AcceptUnreachable = true, []int64{g.d.id}
	}
	id, err := e.r.StartPromote(context.Background(), p)
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	st := e.storedState(id)
	if !st.PromoteSent || st.PastPoint || !st.PromoteUnanswered || st.PromoteSentAt == nil || st.AgentJobID != "" {
		t.Fatalf("stored state = %+v, want sent, unanswered, no job id", st)
	}
	g.c.mu.Lock()
	g.c.dropReply = nil
	g.c.mu.Unlock()
	return id, *st.PromoteSentAt, release
}

// sentRefusal fails unless err is a *PromoteSentError with code and text
// want.
func sentRefusal(t *testing.T, err error, code, want string) *PromoteSentError {
	t.Helper()
	pe, ok := errors.AsType[*PromoteSentError](err)
	if !ok {
		t.Fatalf("Retry = %v (%T), want a *PromoteSentError %s", err, err, code)
	}
	if pe.Code != code || err.Error() != want {
		t.Fatalf("refusal %s %q\nwant %s %q", pe.Code, err.Error(), code, want)
	}
	return pe
}

// wantUnchanged fails unless the job is still partial with params, and
// its promotion partial.
func (g *promoteRig) wantUnchanged(t *testing.T, id, params string) {
	t.Helper()
	j, _, err := g.e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != store.JobPartial || j.ParamsJSON != params {
		t.Fatalf("job = %s %s, want partial %s", j.Status, j.ParamsJSON, params)
	}
	if p := g.promotion(); p.Status != promotionPartial {
		t.Fatalf("promotion = %s, want partial", p.Status)
	}
}

func (e *env) paramsOf(id string) string {
	e.t.Helper()
	j, _, err := e.st.GetJob(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return j.ParamsJSON
}

// TestPromoteSendClearsOldAgentJobAndMarksUnanswered: before mysql.promote
// goes out, the agent job id of an earlier attempt is gone from the
// stored state, the start is marked unanswered and its time stored; the
// answer with a job id clears the mark.
func TestPromoteSendClearsOldAgentJobAndMarksUnanswered(t *testing.T) {
	const oldJob = "0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld00"
	g := newPromoteRig(t)
	e := g.e
	// Past the point, cut off by a restart while waiting for an agent job
	// the candidate's agent no longer knows.
	id, _ := g.cutOffPromote(jobState{PromoteSent: true, PastPoint: true, LockTried: true, CandidatePort: 3306,
		OldSourceHost: "10.0.0.1", OldSourcePort: 3306,
		AgentServerID: g.c.id, AgentJobID: oldJob, AgentCmd: "mysql.promote"}, 3)
	e.r.Resume(context.Background())
	var atSend jobState
	release := make(chan struct{})
	g.c.on("mysql.promote", func(raw json.RawMessage) (any, error) {
		atSend = e.storedState(id)
		jid, err := g.c.jobs.Start("mysql.promote", func(*jobs.Job) (any, error) {
			<-release
			return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Resumed: true, Mode: proto.PromotePlanned}, nil
		})
		if err != nil {
			return nil, err
		}
		return proto.StartedJob{JobID: jid}, nil
	})
	before := time.Now()
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitFor("the answer's job id stored", func() bool {
		jid := e.storedState(id).AgentJobID
		return jid != "" && jid != oldJob
	})
	if atSend.AgentJobID != "" || atSend.AgentCmd != "" || atSend.AgentServerID != 0 {
		t.Fatalf("stored when mysql.promote arrived = %+v, want no agent job", atSend)
	}
	if !atSend.PromoteSent || !atSend.PromoteUnanswered || atSend.PromoteSentAt == nil || atSend.PromoteSentAt.Before(before) {
		t.Fatalf("stored when mysql.promote arrived = %+v, want sent, unanswered, sent at", atSend)
	}
	if st := e.storedState(id); st.PromoteUnanswered || st.AgentCmd != "mysql.promote" {
		t.Fatalf("stored after the answer = %+v, want answered", st)
	}
	close(release)
	e.waitJob(id, store.JobSucceeded)
}

// TestRetrySentUnansweredReadOnlyWithinBoundRefused (spec 5.13's guard): an
// unanswered start, a read-only candidate whose agent does not send the
// field (0.2.0), Ulangi at sent + B − 1 minute: T14b naming sent + B,
// nothing changed, only mysql.status sent to the candidate, and an
// emergency promotion to another replica still refused.
func TestRetrySentUnansweredReadOnlyWithinBoundRefused(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, sent, _ := g.sentUnanswered(t, false)
	e.r.now = func() time.Time { return sent.Add(promoteUnansweredBound - time.Minute) }
	params := e.paramsOf(id)
	marks := g.marks()
	over := sent.Add(promoteUnansweredBound)
	err := e.r.Retry(id, JobAck{})
	pe := sentRefusal(t, err, PromoteSentRunning, fmt.Sprintf(wantT14b, "cand", pukul(over))+" "+fmt.Sprintf(wantT14t, "db-master"))
	if !pe.Over.Equal(over) {
		t.Fatalf("Over = %v, want %v", pe.Over, over)
	}
	if got := g.c.cmds(marks[g.c]); !slices.Equal(got, []string{"mysql.status"}) {
		t.Fatalf("candidate received %v, want only mysql.status", got)
	}
	for _, fa := range []*fakeAgent{g.m, g.b, g.d} {
		if got := fa.cmds(marks[fa]); len(got) > 0 {
			t.Errorf("%s received %v", fa.name, got)
		}
	}
	g.wantUnchanged(t, id, params)
	emergencyRigFrom(g)
	if _, err := e.r.StartPromote(context.Background(), PromoteParams{OldMasterDeadAck: true, MasterServerID: g.m.id,
		CandidateServerID: g.b.id, Mode: proto.PromoteEmergency}); err == nil || !strings.Contains(err.Error(), "belum tuntas") {
		t.Fatalf("emergency promote to another replica = %v, want refused (unfinished)", err)
	}
}

// TestRetrySentUnansweredAfterBoundFails: at sent + B the job is failed:
// T13 for an emergency promotion; T13b for a planned one, whose lock is
// lifted (mysql.unlockPromote sent).
func TestRetrySentUnansweredAfterBoundFails(t *testing.T) {
	t.Run("emergency", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, sent, _ := g.sentUnanswered(t, true)
		e.r.now = func() time.Time { return sent.Add(promoteUnansweredBound) }
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverEmergency, fmt.Sprintf(wantT13, "cand"))
		g.wantSentFailed(t, id, fmt.Sprintf(wantT13, "cand"))
		if g.m.count("mysql.unlockPromote") != 0 {
			t.Fatal("an emergency promotion sent mysql.unlockPromote")
		}
		if got := e.r.RetryRefused(id); got != ErrPromoteRetryBeforePoint.Error() {
			t.Fatalf("RetryRefused = %q, want a new emergency promote", got)
		}
	})
	t.Run("planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, sent, _ := g.sentUnanswered(t, false)
		e.r.now = func() time.Time { return sent.Add(promoteUnansweredBound) }
		unlocks := g.m.count("mysql.unlockPromote")
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
		g.wantSentFailed(t, id, fmt.Sprintf(wantT13b, "cand"))
		if n := g.m.count("mysql.unlockPromote") - unlocks; n != 1 {
			t.Fatalf("mysql.unlockPromote sent %d times, want 1", n)
		}
		if st := e.storedState(id); !st.LockUnlocked {
			t.Fatalf("job state = %+v, want the lock stored open", st)
		}
		if e.r.unlockInFlightNow(g.m.id) {
			t.Fatal("unlock mark left")
		}
	})
}

// wantSentFailed fails unless the job is failed with msg, stored not sent,
// and its promotion failed.
func (g *promoteRig) wantSentFailed(t *testing.T, id, msg string) {
	t.Helper()
	j, _, err := g.e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != store.JobFailed || j.Error != msg {
		t.Fatalf("job = %s %q, want failed %q", j.Status, j.Error, msg)
	}
	if st := g.e.storedState(id); st.PromoteSent || st.PastPoint || st.PromoteUnanswered {
		t.Fatalf("job state = %+v, want not sent", st)
	}
	if p := g.promotion(); p.Status != promotionFailed {
		t.Fatalf("promotion = %s, want failed", p.Status)
	}
	if !strings.Contains(g.e.jobLog(id), msg) {
		t.Fatalf("job log lacks %q:\n%s", msg, g.e.jobLog(id))
	}
}

// TestRetrySentFieldNoneFails: inside B, the candidate's agent says no
// promote request is pending: the job fails (T13b).
func TestRetrySentFieldNoneFails(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _, _ := g.sentUnanswered(t, false)
	candStatus(g.c, true, new(false))
	sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
	g.wantSentFailed(t, id, fmt.Sprintf(wantT13b, "cand"))
}

// endedPromoteJob is a mysql.promote job of fa's agent that ended.
func endedPromoteJob(t *testing.T, e *env, fa *fakeAgent) string {
	t.Helper()
	jid, err := fa.jobs.Start("mysql.promote", func(*jobs.Job) (any, error) { return nil, errors.New("master lama tidak read-only") })
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("agent job ended", func() bool {
		s, ok := fa.jobs.Status(jid)
		return ok && s.State != "running"
	})
	return jid
}

// heldSent is a planned promote job cut off after mysql.promote was
// answered with agent job jid (a restart): partial.
func (g *promoteRig) heldSent(t *testing.T, jid string) string {
	t.Helper()
	sent := time.Now().Add(-time.Minute)
	id, _ := g.cutOffPromote(jobState{PromoteSent: true, PromoteSentAt: &sent, LockTried: true,
		AgentServerID: g.c.id, AgentJobID: jid, AgentCmd: "mysql.promote"}, 3)
	g.e.r.Resume(context.Background())
	if j, _, _ := g.e.st.GetJob(id); j.Status != store.JobPartial {
		t.Fatalf("job = %s, want partial", j.Status)
	}
	return id
}

// TestRetrySentFieldPendingBeatsJobStatus: the panel holds the job id and
// job.status says it ended, but the candidate's agent says a promote
// request is pending: T14b, nothing changed.
func TestRetrySentFieldPendingBeatsJobStatus(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.heldSent(t, endedPromoteJob(t, e, g.c))
	candStatus(g.c, true, new(true))
	params := e.paramsOf(id)
	marks := g.marks()
	err := e.r.Retry(id, JobAck{})
	if pe, ok := errors.AsType[*PromoteSentError](err); !ok || pe.Code != PromoteSentRunning {
		t.Fatalf("Retry = %v, want T14b", err)
	}
	g.wantUnchanged(t, id, params)
	if g.m.count("mysql.unlockPromote") != 0 {
		t.Fatal("master unlocked")
	}
	for _, fa := range []*fakeAgent{g.m, g.b, g.d} {
		if got := fa.cmds(marks[fa]); len(got) > 0 {
			t.Errorf("%s received %v", fa.name, got)
		}
	}
}

// TestRetrySentJobStatusEndedFails: the panel holds the job id, the agent
// does not send the field, and job.status says the job ended: failed.
func TestRetrySentJobStatusEndedFails(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.heldSent(t, endedPromoteJob(t, e, g.c))
	sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
	g.wantSentFailed(t, id, fmt.Sprintf(wantT13b, "cand"))
	// One the agent no longer knows (it restarted) has ended as well.
	g2 := newPromoteRig(t)
	id2 := g2.heldSent(t, "0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld00")
	sentRefusal(t, g2.e.r.Retry(id2, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
}

// TestRetrySentCandidateWritableContinues: the candidate opened writes:
// without a token and with every gate row red, the job is stored past the
// point of no return and goes on; the old master's quarantine is sent.
func TestRetrySentCandidateWritableContinues(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _, release := g.sentUnanswered(t, false)
	close(release)
	e.waitFor("agent job ended", func() bool {
		s, ok := g.c.jobs.Status(g.c.lastJob())
		return ok && s.State != "running"
	})
	candStatus(g.c, false, new(false))
	g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Resumed: true, Mode: proto.PromotePlanned}, nil
	})
	redEverything(t, g)
	if need, err := e.r.NeedsConfirm(id); err != nil || need {
		t.Fatalf("NeedsConfirm = %v, %v; want false", need, err)
	}
	quarantines := g.m.count("mysql.quarantine")
	if err := e.r.Retry(id, JobAck{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if st := e.storedState(id); !st.PastPoint {
		t.Fatalf("job state = %+v, want past the point", st)
	}
	e.waitFor("quarantine sent", func() bool { return g.m.count("mysql.quarantine") > quarantines })
}

// TestRetrySentCandidateUnreadable: T14, nothing changed; a planned job
// whose lock record stands has the tail T14t. The compound state (spec
// 5.2 row 7): a planned promotion failed with its lock kept, then an
// emergency one sent whose candidate went silent, the old master back,
// connected and Terkunci: the emergency job's Ulangi is T14 with the tail
// T14u, and "Buka kunci promote" of the old planned job T11t.
func TestRetrySentCandidateUnreadable(t *testing.T) {
	t.Run("planned", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		id, _, _ := g.sentUnanswered(t, false)
		g.c.fail("mysql.status", "MySQL tidak menjawab")
		params := e.paramsOf(id)
		sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentUnreadable,
			fmt.Sprintf(wantT14, "cand")+" "+fmt.Sprintf(wantT14t, "db-master"))
		g.wantUnchanged(t, id, params)
	})
	t.Run("compound", func(t *testing.T) {
		g := newPromoteRig(t)
		e := g.e
		planned, _ := g.plantStanding(time.Now().Add(-time.Hour))
		emergency, _, _ := g.sentUnanswered(t, true)
		if !g.hasRecord(planned) {
			t.Fatal("the planned job's record does not stand")
		}
		g.c.fail("mysql.status", "MySQL tidak menjawab")
		g.m.attach()
		g.h.set(g.m.id, lockedMaster())
		params := e.paramsOf(emergency)
		sentRefusal(t, e.r.Retry(emergency, JobAck{}), PromoteSentUnreadable,
			fmt.Sprintf(wantT14, "cand")+" "+fmt.Sprintf(wantT14u, "db-master"))
		g.wantUnchanged(t, emergency, params)
		marks := g.marks()
		_, err := e.r.UnlockPromoteLock(context.Background(), planned)
		wantUnlockRefused(t, err, UnlockNewerOpen, fmt.Sprintf(wantT11t, emergency))
		g.wantNoCommands(t, marks)
	})
}

// TestOldJobSentWithoutAgentJobCountsUnanswered: params of 0.2.0
// (promote_sent, no agent job id, no send time) count as unanswered from
// the job's FinishedAt t0 (K17): inside t0 + B running, at it over.
func TestOldJobSentWithoutAgentJobCountsUnanswered(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	id := g.plantFailedPlanned(jobState{PromoteSent: true, LockTried: true}, 3, t0.Add(-time.Hour))
	if err := e.st.ReopenJob(id, t0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobPartial, 3, "promote selesai sebagian", t0); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.st.GetJob(id)
	st := e.storedState(id)
	if !promoteUnanswered(st) || !promoteSentAt(j, st).Equal(t0) {
		t.Fatalf("unanswered %v, sent at %v; want true, %v", promoteUnanswered(st), promoteSentAt(j, st), t0)
	}
	e.r.now = func() time.Time { return t0.Add(promoteUnansweredBound - time.Second) }
	if v, over := e.r.promoteSentVerdict(context.Background(), j, st); v != sentRunning || !over.Equal(t0.Add(promoteUnansweredBound)) {
		t.Fatalf("verdict inside B = %v, %v", v, over)
	}
	e.r.now = func() time.Time { return t0.Add(promoteUnansweredBound) }
	if v, _ := e.r.promoteSentVerdict(context.Background(), j, st); v != sentOver {
		t.Fatalf("verdict at t0 + B = %v, want over", v)
	}
	// Holding a mysql.promote job id, it is not unanswered.
	st.AgentServerID, st.AgentJobID, st.AgentCmd = g.c.id, "abc", "mysql.promote"
	if promoteUnanswered(st) {
		t.Fatal("a 0.2.0 job holding its agent job id counts as unanswered")
	}
}

// TestRetryAfterPanelRestartKeepsUnanswered: an unanswered start ends
// partial; a new Runner over the same store (Resume) still refuses Ulangi
// inside B with T14b.
func TestRetryAfterPanelRestartKeepsUnanswered(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, sent, _ := g.sentUnanswered(t, false)
	e.r.Close()
	e.r = New(e.r.d)
	e.r.now = func() time.Time { return sent.Add(time.Minute) }
	e.r.Resume(context.Background())
	params := e.paramsOf(id)
	err := e.r.Retry(id, JobAck{})
	if pe, ok := errors.AsType[*PromoteSentError](err); !ok || pe.Code != PromoteSentRunning ||
		!pe.Over.Equal(sent.Add(promoteUnansweredBound)) {
		t.Fatalf("Retry after the restart = %v, want T14b", err)
	}
	g.wantUnchanged(t, id, params)
}

// TestUnansweredBoundClockSetBack: a send time in the future (the clock
// set back since) is running, never over.
func TestUnansweredBoundClockSetBack(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, sent, _ := g.sentUnanswered(t, false)
	e.r.now = func() time.Time { return sent.Add(-promoteUnansweredBound - time.Hour) }
	j, _, _ := e.st.GetJob(id)
	if v, _ := e.r.promoteSentVerdict(context.Background(), j, e.storedState(id)); v != sentRunning {
		t.Fatalf("verdict with the send in the future = %v, want running", v)
	}
}

// TestPromoteUnansweredBoundCoversAgentJob: B is at least 30 minutes and
// twice the agent job's bound (K18); the agent side checks the parts
// (TestPromoteJobBoundParts).
func TestPromoteUnansweredBoundCoversAgentJob(t *testing.T) {
	if promoteUnansweredBound < 30*time.Minute || promoteUnansweredBound < 2*agentPromoteJobBound {
		t.Fatalf("B = %v, agent job bound %v", promoteUnansweredBound, agentPromoteJobBound)
	}
	if want := 443*time.Minute + 40*time.Second; agentPromoteJobBound != want {
		t.Fatalf("agentPromoteJobBound = %v, want %v as its comment adds it up", agentPromoteJobBound, want)
	}
}

// TestRetrySentChangedMeanwhile: the job changed between the verdict and
// startMu: P6, nothing changed.
func TestRetrySentChangedMeanwhile(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _, _ := g.sentUnanswered(t, false)
	candStatus(g.c, true, new(false))
	sr := e.r.sentRetryVerdict(id)
	if !sr.ok || sr.verdict != sentOver {
		t.Fatalf("verdict = %+v", sr)
	}
	e.setState(id, func(st *jobState) { st.LockUnlocked = true })
	j, _, _ := e.st.GetJob(id)
	st, _ := sentNotPast(j)
	e.r.startMu.Lock()
	_, undo, err := e.r.settleSentRetry(j, st, sr)
	e.r.startMu.Unlock()
	if undo != nil {
		t.Fatal("an undo for a changed job")
	}
	sentRefusal(t, err, PromoteSentChanged, wantP6)
	g.wantUnchanged(t, id, e.paramsOf(id))
}

// TestPromoteFailedSaveFailureKeepsLock (O-1): the candidate's job is
// proven over but "not sent" cannot be stored: the lock is kept, the job
// ends partial and the stored state still reads sent, so the next Ulangi
// reads the candidate again.
func TestPromoteFailedSaveFailureKeepsLock(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	// "Not sent" cannot be stored (the in-run transaction fails).
	e.r.notSentStore = func(string, string, string, int64) error { return errors.New("database is locked") }
	g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
		return nil, errors.New("master lama tidak read-only")
	})
	candStatus(g.c, true, nil)
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobPartial)
	e.waitIdle(id)
	if n := g.m.count("mysql.unlockPromote"); n != 0 {
		t.Fatalf("mysql.unlockPromote sent %d times, want 0", n)
	}
	if st := e.storedState(id); !st.PromoteSent {
		t.Fatalf("stored state = %+v, want still sent", st)
	}
	if p := g.promotion(); p.Status != promotionPartial {
		t.Fatalf("promotion = %s, want partial", p.Status)
	}
	e.r.notSentStore = nil
	sentRefusal(t, e.r.Retry(id, JobAck{}), PromoteSentOverPlanned, fmt.Sprintf(wantT13b, "cand"))
	if n := g.m.count("mysql.unlockPromote"); n != 1 {
		t.Fatalf("mysql.unlockPromote sent %d times after the Ulangi, want 1", n)
	}
}

// TestSentVerdictFlagCombinations: read_only OFF on the candidate, with
// either super_read_only, is writable whatever the evidence says;
// read_only ON with super_read_only OFF is unreadable; only both ON reach
// the evidence (the field, the bound). The mix on the candidate, the field
// saying no request or the panel inside or outside the bound, is never
// over, and the old master's lock is not lifted.
func TestSentVerdictFlagCombinations(t *testing.T) {
	evidence := []struct {
		name    string
		request *bool         // the field: nil, an agent of 0.2.0
		at      time.Duration // the panel's clock after the send
		bothOn  sentVerdict   // the verdict when both flags are ON
	}{
		{"field none", new(false), time.Minute, sentOver},
		{"inside bound", nil, promoteUnansweredBound - time.Minute, sentRunning},
		{"outside bound", nil, promoteUnansweredBound, sentOver},
	}
	for _, c := range flagCombos {
		for _, ev := range evidence {
			want := ev.bothOn
			switch {
			case !c.readOnly:
				want = sentWritable
			case !c.superReadOnly:
				want = sentUnreadable
			}
			t.Run(c.name+"/"+ev.name, func(t *testing.T) {
				g := newPromoteRig(t)
				e := g.e
				id, sent, release := g.sentUnanswered(t, false)
				g.c.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID,
					ReadOnly: c.readOnly, SuperReadOnly: c.superReadOnly, TLSAvailable: true, PromoteRequest: ev.request})
				e.r.now = func() time.Time { return sent.Add(ev.at) }
				j, _, _ := e.st.GetJob(id)
				if v, _ := e.r.promoteSentVerdict(context.Background(), j, e.storedState(id)); v != want {
					t.Fatalf("verdict = %v, want %v", v, want)
				}
				if c.name != "mix" {
					return
				}
				// Ulangi goes on with the promote: nothing failed, the
				// old master not unlocked.
				close(release)
				e.waitFor("agent job ended", func() bool {
					s, ok := g.c.jobs.Status(g.c.lastJob())
					return ok && s.State != "running"
				})
				g.c.job("mysql.promote", func(*jobs.Job, json.RawMessage) (any, error) {
					return proto.PromoteResult{GTIDExecuted: masterGTID, ServerUUID: targetUUID, Resumed: true, Mode: proto.PromotePlanned}, nil
				})
				unlocks := g.m.count("mysql.unlockPromote")
				if err := e.r.Retry(id, JobAck{}); err != nil {
					t.Fatalf("Retry: %v", err)
				}
				if st := e.storedState(id); !st.PastPoint {
					t.Fatalf("job state = %+v, want past the point", st)
				}
				e.waitIdle(id)
				if j, _, _ := e.st.GetJob(id); j.Status == store.JobFailed {
					t.Fatalf("job failed: %s", j.Error)
				}
				if n := g.m.count("mysql.unlockPromote") - unlocks; n != 0 {
					t.Fatalf("mysql.unlockPromote sent %d times", n)
				}
			})
		}
	}
}
