package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// "Buka kunci promote" (spec 5.2, 5.13): the texts, copied from spec 5.11
// (P1 and P2 are not in the spec).
const (
	wantP1 = `Perintah promote pekerjaan ini sudah dikirim ke kandidat, jadi kunci promote tidak dibuka dari panel. ` +
		`Tekan "Ulangi dari tahap ini" pada pekerjaan ini; panel memeriksa kandidat dulu.`
	wantT10     = `Kunci promote di master db-master dibuka; aplikasi bisa menulis lagi.`
	wantT10b    = `Master db-master tidak sedang dikunci untuk promote; tidak ada yang dibuka. Bila master masih berstatus "Terkunci", sebabnya lain: tekan "Buka kunci master".`
	wantT11b    = `Agent master db-master tidak tersambung, jadi panel tidak bisa membuka kuncinya. Sambungkan agent itu lalu tekan lagi. Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
	wantT11s    = `Kunci promote ini tidak dibuka dari panel: topologi ini sudah promote lagi sejak pekerjaan ini, atau server itu bukan lagi masternya. Server itu ditangani sebagai master lama di halamannya.`
	wantT11t    = `Kunci promote ini tidak dibuka dari panel selagi promote yang lebih baru di topologi ini belum tuntas. Selesaikan dulu pekerjaan promote %s.`
	wantT12b    = `Master db-master mungkin masih terkunci oleh promote ini. Tekan "Buka kunci promote" dulu; sesudah kuncinya terbuka, "Ulangi dari tahap ini" tersedia lagi.`
	wantT11     = `Agent master db-master tidak membuka kunci. %s. Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
	wantT11c    = `Agent master db-master tidak membuka kunci. %s. Kandidat cand terbaca bisa ditulis dan mungkin sudah menjadi master: jangan buka master ini. Periksa kandidat itu dulu. Bila ia memang bukan master, kunci lagi di halaman replikanya, lalu tekan tombol ini lagi.`
	wantT11d    = `Agent master db-master tidak membuka kunci. %s. Server ini sudah dikarantina; tindakannya ada di kotak Master lama di halaman server.`
	wantP2      = `Panel tidak bisa menyusun kredensial kandidat %s untuk membuka kunci (%s). Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
	wantT15     = `Sebelum itu, bila agent master tersambung, tekan "Buka kunci promote" pada pekerjaan promote terencana itu: begitu agent menjawab, panel mencatat kunci terbuka dan promote darurat bisa langsung dijalankan.`
	wantT17     = `Kunci master tidak bisa dibuka (%s). Pada tenggat kunci agent master membukanya sendiri bila kandidat masih replika dan semua replika terbaca; bila tidak, kunci dipertahankan. Tekan "Buka kunci promote" untuk mencoba lagi. Jangan Resync kandidat selama master masih terkunci.`
	keptRefusal = "kandidat cand tidak bisa dibaca (i/o timeout); kunci tidak dibuka"
)

// keptLock runs a planned promotion that fails in "Replika mengejar"
// while the master's agent refuses the unlock of its failure path: the
// lock record stands. It returns the job and its stored state.
func (g *promoteRig) keptLock(t *testing.T) (string, jobState) {
	t.Helper()
	e := g.e
	e.r.promoteCatchUp = 100 * time.Millisecond
	g.c.reply("mysql.waitSynced", proto.WaitSyncedResult{Synced: false, Executed: lagUUIDs,
		Status: proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}})
	g.m.fail("mysql.unlockPromote", keptRefusal)
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	e.waitJob(id, store.JobFailed)
	e.waitIdle(id)
	st := e.storedState(id)
	if !st.LockTried || st.LockUnlocked || st.LockDeadline == nil {
		t.Fatalf("job state = %+v, want a lock not confirmed open", st)
	}
	return id, st
}

// wantUnlockRefused fails unless err is an *UnlockLockError with code and
// text want.
func wantUnlockRefused(t *testing.T, err error, code, want string) *UnlockLockError {
	t.Helper()
	ue, ok := errors.AsType[*UnlockLockError](err)
	if !ok {
		t.Fatalf("UnlockPromoteLock = %v (%T), want an *UnlockLockError %s", err, err, code)
	}
	if ue.Code != code || err.Error() != want {
		t.Fatalf("refusal %s %q\nwant %s %q", ue.Code, err.Error(), code, want)
	}
	return ue
}

// plantSent is plantStanding of a job whose mysql.promote was sent.
func (g *promoteRig) plantSent() string {
	tried := time.Now().Add(-time.Minute)
	id, _ := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried":true,`+
		`"promote_sent":true,"lock_tried_at":"`+tried.UTC().Format(time.RFC3339Nano)+`"}`, nil)
	return id
}

func (g *promoteRig) hasRecord(id string) bool {
	for _, rec := range g.lockRecords() {
		if rec.JobID == id {
			return true
		}
	}
	return false
}

// TestUnlockPromoteLockAfterKeptLock (spec 5.13; it replaces
// TestPromotePlannedRetryLiftsKeptLock): the guard kept the lock, and an
// hour past its deadline the operator presses "Buka kunci promote": one
// mysql.unlockPromote to the master with the candidate's credential, the
// record stored open, nothing sent to the candidate, and an emergency
// promotion is no longer refused by the lock's window.
func TestUnlockPromoteLockAfterKeptLock(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, st := g.keptLock(t)
	deadline := *st.LockDeadline
	e.r.now = func() time.Time { return deadline.Add(time.Hour) }
	if !e.r.UnlockLockOffered(id) {
		t.Fatal("Buka kunci promote not offered for a kept lock")
	}
	if got := e.r.RetryRefused(id); got != wantT12b {
		t.Fatalf("RetryRefused = %q, want T12b", got)
	}
	g.m.reply("mysql.unlockPromote", nil)
	marks := g.marks()
	unlocks := g.m.count("mysql.unlockPromote")
	out, err := e.r.UnlockPromoteLock(context.Background(), id)
	if err != nil || out != UnlockOpened {
		t.Fatalf("UnlockPromoteLock = %v, %v; want UnlockOpened", out, err)
	}
	if n := g.m.count("mysql.unlockPromote") - unlocks; n != 1 {
		t.Fatalf("unlockPromote sent %d times, want 1", n)
	}
	var u proto.UnlockPromoteParams
	g.m.params("mysql.unlockPromote", &u)
	if u.Candidate.Host != "10.0.0.2" || u.Candidate.User != "dbpanel_repl" || u.Candidate.Port != 3306 || u.Candidate.Password != replPW {
		t.Fatalf("unlock params = %+v, want the candidate's credential", u.Candidate)
	}
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		if got := fa.cmds(marks[fa]); len(got) > 0 {
			t.Errorf("%s received %v", fa.name, got)
		}
	}
	if g.c.count("mysql.promote") != 0 {
		t.Fatal("mysql.promote sent")
	}
	if st := e.storedState(id); !st.LockUnlocked || st.LockTried || st.LockDeadline != nil {
		t.Fatalf("job state = %+v, want the lock stored open", st)
	}
	if !strings.Contains(e.jobLog(id), wantT10) {
		t.Fatalf("job log lacks T10:\n%s", e.jobLog(id))
	}
	if g.hasRecord(id) || e.r.UnlockLockOffered(id) || e.r.RetryRefused(id) != "" {
		t.Fatal("the record still stands after the unlock")
	}
	// Right away, even inside the old lock's window: an emergency
	// promotion is not refused by it.
	e.r.now = func() time.Time { return deadline.Add(-time.Minute) }
	if err := e.r.refuseWhileLockMayStand(g.m.id, verbEmergency); err != nil {
		t.Fatalf("emergency right after the unlock: %v", err)
	}
}

func TestUnlockPromoteLockNotLocked(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	g.m.fail("mysql.unlockPromote", "server ini tidak dikunci untuk promosi")
	out, err := e.r.UnlockPromoteLock(context.Background(), id)
	if err != nil || out != UnlockNotLocked {
		t.Fatalf("UnlockPromoteLock = %v, %v; want UnlockNotLocked", out, err)
	}
	if st := e.storedState(id); !st.LockUnlocked {
		t.Fatalf("job state = %+v, want the lock stored open", st)
	}
	if g.hasRecord(id) {
		t.Fatal("record stands after not locked")
	}
	if !strings.Contains(e.jobLog(id), wantT10b) {
		t.Fatalf("job log lacks T10b:\n%s", e.jobLog(id))
	}
}

// TestUnlockPromoteLockRefusedAfterSend (spec 5.13): once mysql.promote
// was sent (or the job passed the point), the button is refused with P1
// and no agent gets anything.
func TestUnlockPromoteLockRefusedAfterSend(t *testing.T) {
	for _, flag := range []string{"promote_sent", "past_point"} {
		t.Run(flag, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _ := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_tried":true,"`+
				flag+`":true}`, nil)
			marks := g.marks()
			if e.r.UnlockLockOffered(id) {
				t.Fatal("offered for a sent job")
			}
			_, err := e.r.UnlockPromoteLock(context.Background(), id)
			wantUnlockRefused(t, err, UnlockSent, wantP1)
			g.wantNoCommands(t, marks)
		})
	}
}

// TestUnlockPromoteLockSuperseded (spec 5.13): a planned promotion failed,
// then an emergency promotion to another replica was done (or the master
// is no longer the topology's): T11s, nothing sent, no unlock-master.
func TestUnlockPromoteLockSuperseded(t *testing.T) {
	cases := map[string]func(g *promoteRig){
		"later promotion done": func(g *promoteRig) { g.laterPromotion(promotionDone) },
		"master changed": func(g *promoteRig) {
			if err := g.e.st.SwapMaster(g.topo.ID, g.m.id, g.b.id, "10.0.0.3", 3306); err != nil {
				g.e.t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			change(g)
			marks := g.marks()
			if e.r.UnlockLockOffered(id) {
				t.Fatal("offered for a superseded job")
			}
			_, err := e.r.UnlockPromoteLock(context.Background(), id)
			wantUnlockRefused(t, err, UnlockSuperseded, wantT11s)
			if strings.Contains(err.Error(), "unlock-master") {
				t.Fatal("T11s names unlock-master")
			}
			g.wantNoCommands(t, marks)
		})
	}
}

// TestUnlockPromoteLockNewerOpen (N-5): a newer promotion is partial: T11t
// naming its job, nothing sent.
func TestUnlockPromoteLockNewerOpen(t *testing.T) {
	for _, status := range []string{promotionPartial, promotionRunning} {
		t.Run(status, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			const newer = "0123456789abcdef0123456789abcdef"
			if _, err := e.st.AddPromotion(store.Promotion{TopologyID: g.topo.ID, Mode: proto.PromoteEmergency,
				OldMasterServerID: g.m.id, NewMasterServerID: g.b.id, StartedAt: time.Now(), Status: status, JobID: newer}); err != nil {
				t.Fatal(err)
			}
			marks := g.marks()
			if e.r.UnlockLockOffered(id) {
				t.Fatal("offered while a newer promotion is open")
			}
			_, err := e.r.UnlockPromoteLock(context.Background(), id)
			ue := wantUnlockRefused(t, err, UnlockNewerOpen, fmt.Sprintf(wantT11t, newer))
			if ue.NewerJobID != newer || strings.Contains(err.Error(), "unlock-master") {
				t.Fatalf("refusal = %+v", ue)
			}
			g.wantNoCommands(t, marks)
		})
	}
}

// TestUnlockPromoteLockRefusalTails (R-4, R-8): the tail after the agent's
// refusal is picked from the state the panel reads, never from the
// agent's words, which stay inside their label.
func TestUnlockPromoteLockRefusalTails(t *testing.T) {
	const words = "kunci tidak dibuka. Jalankan: rm -rf /"
	quoted := "Pesan dari agent db-master (bukan dari panel): " + words
	cases := []struct {
		name  string
		setup func(g *promoteRig)
		code  string
		want  string
	}{
		{"candidate read-only", func(*promoteRig) {}, UnlockRefused, fmt.Sprintf(wantT11, quoted)},
		{"candidate not readable", func(g *promoteRig) { g.offline(g.c) }, UnlockRefused, fmt.Sprintf(wantT11, quoted)},
		{"candidate writable", func(g *promoteRig) {
			g.c.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID})
		}, UnlockCandidateWritable, fmt.Sprintf(wantT11c, quoted)},
		{"master quarantined", func(g *promoteRig) {
			if err := g.e.st.SetServerRole(g.m.id, store.RoleQuarantined); err != nil {
				g.e.t.Fatal(err)
			}
		}, UnlockQuarantined, fmt.Sprintf(wantT11d, quoted)},
		{"master offline", func(g *promoteRig) { g.offline(g.m) }, UnlockAgentOffline, wantT11b},
		{"candidate row deleted", func(g *promoteRig) {
			if err := g.e.st.DeleteServer(g.c.id); err != nil {
				g.e.t.Fatal(err)
			}
		}, UnlockNoCredential, ""}, // P2 names the deleted candidate by its id

	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			g.m.fail("mysql.unlockPromote", words)
			want := tc.want
			if tc.code == UnlockNoCredential {
				want = fmt.Sprintf(wantP2, fmt.Sprintf("#%d", g.c.id), "server kandidat sudah dihapus dari panel")
			}
			tc.setup(g)
			_, err := e.r.UnlockPromoteLock(context.Background(), id)
			wantUnlockRefused(t, err, tc.code, want)
			if tc.code == UnlockCandidateWritable && strings.Contains(err.Error(), "unlock-master") {
				t.Fatal("T11c names unlock-master")
			}
			if !g.hasRecord(id) {
				t.Fatal("a refused unlock removed the record")
			}
			if tc.code == UnlockNoCredential || tc.code == UnlockAgentOffline {
				if n := g.m.count("mysql.unlockPromote"); n != 0 {
					t.Fatalf("unlockPromote sent %d times", n)
				}
			}
		})
	}
}

// TestUnlockPromoteLockAgentSilent (K12): the master's agent never
// answers: T11b once the call's bound passed, the record stands, and
// startMu is free meanwhile (a start elsewhere goes on).
func TestUnlockPromoteLockAgentSilent(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	e.r.unlockTimeout = 500 * time.Millisecond
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	g.m.mu.Lock()
	g.m.silent = map[string]bool{"mysql.unlockPromote": true}
	g.m.mu.Unlock()
	other := e.agent("lain", "10.0.0.9")
	type result struct {
		out UnlockOutcome
		err error
	}
	done := make(chan result, 1)
	began := time.Now()
	go func() {
		out, err := e.r.UnlockPromoteLock(context.Background(), id)
		done <- result{out, err}
	}()
	e.waitFor("unlockPromote sent", func() bool { return g.m.count("mysql.unlockPromote") == 1 })
	if _, err := e.r.StartPrepareMaster(context.Background(), other.id); err == nil || !strings.Contains(err.Error(), "belum dihubungkan") {
		t.Fatalf("start elsewhere during the wait = %v", err)
	}
	select {
	case <-done:
		t.Fatal("UnlockPromoteLock answered before its bound")
	default:
	}
	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("UnlockPromoteLock did not end at its bound")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("UnlockPromoteLock took %v", took)
	}
	wantUnlockRefused(t, res.err, UnlockAgentOffline, wantT11b)
	if !g.hasRecord(id) {
		t.Fatal("record gone after no answer")
	}
}

// TestUnlockPromoteLockAfterPanelRestart: a planned promotion cut off by a
// restart after its lock was asked for (Resume fails it) offers the
// button, and it works.
func TestUnlockPromoteLockAfterPanelRestart(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	tried := time.Now().Add(-time.Minute)
	deadline := tried.Add(30 * time.Minute)
	id, promID := g.cutOffPromote(jobState{LockTried: true, LockTriedAt: &tried, LockDeadline: &deadline}, 2)
	p, err := e.st.GetPromotion(promID) // a real start records its job
	if err != nil {
		t.Fatal(err)
	}
	p.JobID = id
	if err := e.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	e.r.Resume(context.Background())
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after the restart = %s, want failed", j.Status)
	}
	if !e.r.UnlockLockOffered(id) {
		t.Fatal("Buka kunci promote not offered after the restart")
	}
	out, err := e.r.UnlockPromoteLock(context.Background(), id)
	if err != nil || out != UnlockOpened {
		t.Fatalf("UnlockPromoteLock = %v, %v", out, err)
	}
	if st := e.storedState(id); !st.LockUnlocked {
		t.Fatalf("job state = %+v", st)
	}
}

// TestRetryPlannedRefusedWhileLockStands (P2 of the builder check): T12b,
// no agent command, the lock's times unchanged.
func TestRetryPlannedRefusedWhileLockStands(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, st := g.keptLock(t)
	marks := g.marks()
	err := e.r.Retry(id, JobAck{})
	if !errors.Is(err, ErrPromoteLockStands) || err.Error() != wantT12b {
		t.Fatalf("Retry = %v, want T12b", err)
	}
	g.wantNoCommands(t, marks)
	after := e.storedState(id)
	if after.LockTriedAt == nil || !after.LockTriedAt.Equal(*st.LockTriedAt) || !after.LockDeadline.Equal(*st.LockDeadline) {
		t.Fatalf("lock times moved: %+v -> %+v", st, after)
	}
	if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job = %s, want failed", j.Status)
	}
	if got := e.r.RetryRefused(id); got != wantT12b {
		t.Fatalf("RetryRefused = %q", got)
	}
}

// TestRetrySentJobIgnoresLockRecord: a job whose mysql.promote was sent is
// never refused for its lock record.
func TestRetrySentJobIgnoresLockRecord(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id := g.plantSent()
	if !g.hasRecord(id) {
		t.Fatal("planted record does not stand")
	}
	if got := e.r.RetryRefused(id); got != "" {
		t.Fatalf("RetryRefused = %q, want none", got)
	}
	j, _, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	e.r.startMu.Lock()
	err = e.r.refusePlannedRetryWhileLockStands(j)
	e.r.startMu.Unlock()
	if err != nil {
		t.Fatalf("sent job refused for its record: %v", err)
	}
}

// TestUnlockLockJobForMasterPage: the master page's button acts on the
// newest standing record's job; without one, on the latest failed planned
// promotion not sent and not superseded; a record's job that was sent is
// reported sent.
func TestUnlockLockJobForMasterPage(t *testing.T) {
	t.Run("record", func(t *testing.T) {
		g := newPromoteRig(t)
		g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_unlocked":true}`, nil)
		id, _ := g.plantStanding(time.Now().Add(-time.Minute))
		if got, sent, err := g.e.r.UnlockLockJob(g.m.id); err != nil || got != id || sent {
			t.Fatalf("UnlockLockJob = %q, %v, %v; want %s", got, sent, err, id)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		g := newPromoteRig(t)
		older, _ := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_unlocked":true}`, nil)
		latest, _ := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_unlocked":true}`, nil)
		if got, sent, err := g.e.r.UnlockLockJob(g.m.id); err != nil || got != latest || sent {
			t.Fatalf("UnlockLockJob = %q, %v, %v; want %s (not %s)", got, sent, err, latest, older)
		}
		g.laterPromotion(promotionDone)
		if got, _, err := g.e.r.UnlockLockJob(g.m.id); err != nil || got != "" {
			t.Fatalf("UnlockLockJob after a done promotion = %q, %v; want none", got, err)
		}
	})
	t.Run("sent", func(t *testing.T) {
		g := newPromoteRig(t)
		id := g.plantSent()
		if got, sent, err := g.e.r.UnlockLockJob(g.m.id); err != nil || got != id || !sent {
			t.Fatalf("UnlockLockJob = %q, %v, %v; want %s sent", got, sent, err, id)
		}
	})
}

// TestUndoLogLineT17: the failure path that cannot lift the lock logs T17.
func TestUndoLogLineT17(t *testing.T) {
	g := newPromoteRig(t)
	id, _ := g.keptLock(t)
	want := fmt.Sprintf(wantT17, "Pesan dari agent db-master (bukan dari panel): "+keptRefusal)
	if log := g.e.jobLog(id); !strings.Contains(log, want) {
		t.Fatalf("job log lacks T17 %q:\n%s", want, log)
	}
}

// TestEmergencyLockWindowTextT15: the emergency refusal inside a lock's
// window ends with T15.
func TestEmergencyLockWindowTextT15(t *testing.T) {
	g := newPromoteRig(t)
	g.plantStanding(time.Now().Add(-time.Minute))
	err := g.e.r.refuseWhileLockMayStand(g.m.id, verbEmergency)
	if err == nil || !strings.HasSuffix(err.Error(), ". "+wantT15) || strings.Contains(err.Error(), "Ulangi") {
		t.Fatalf("emergency refusal = %v, want it to end with T15", err)
	}
}

// Fix round 1 (C-1, I-1): an unlock in flight, and an unlock the panel
// could not record.
const (
	wantP10 = `Agent master db-master sudah membuka kunci promote, tetapi panel tidak mencatatnya karena pekerjaan atau topologi berubah saat perintah berjalan. Master itu sekarang tidak lagi dikunci oleh promote ini. Periksa pekerjaan promote yang lebih baru di halaman Pekerjaan sebelum melakukan apa pun pada master ini.`
	wantP11 = `Kunci promote master db-master sedang dibuka dari panel; tunggu sampai selesai (paling lama dua menit), lalu coba lagi.`
)

// unlockInFlightNow reports whether an unlock of master is marked in flight.
func (r *Runner) unlockInFlightNow(master int64) bool {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return r.unlocking[master]
}

// heldUnlock starts UnlockPromoteLock of id with the master's agent
// holding its answer to mysql.unlockPromote until release is called; it
// returns once the command reached the agent.
func (g *promoteRig) heldUnlock(t *testing.T, id string) (release func(), result func() (UnlockOutcome, error)) {
	t.Helper()
	gate := make(chan struct{})
	g.m.on("mysql.unlockPromote", func(json.RawMessage) (any, error) {
		<-gate
		return nil, nil
	})
	type res struct {
		out UnlockOutcome
		err error
	}
	done := make(chan res, 1)
	before := g.m.count("mysql.unlockPromote")
	go func() {
		out, err := g.e.r.UnlockPromoteLock(context.Background(), id)
		done <- res{out, err}
	}()
	g.e.waitFor("unlockPromote reached the agent", func() bool { return g.m.count("mysql.unlockPromote") == before+1 })
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release, func() (UnlockOutcome, error) {
		select {
		case r := <-done:
			return r.out, r.err
		case <-time.After(10 * time.Second):
			t.Fatal("UnlockPromoteLock did not end")
			return 0, nil
		}
	}
}

// TestUnlockInFlightRefusesRetry (C-1): while mysql.unlockPromote is on its
// way, Ulangi of a promote job of that master is refused with P11 and sends
// nothing; once the unlock returned, the mark is gone. Also for a job whose
// record was settled open (the master page's fallback offers it).
func TestUnlockInFlightRefusesRetry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(g *promoteRig) string
	}{
		{"record stands", func(g *promoteRig) string {
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			return id
		}},
		{"record settled open", func(g *promoteRig) string {
			id, _ := g.plantPlanned(`{"master_server_id":%d,"candidate_server_id":%d,"promotion_id":%d,"lock_unlocked":true}`, nil)
			if got, _, err := g.e.r.UnlockLockJob(g.m.id); err != nil || got != id {
				g.e.t.Fatalf("UnlockLockJob = %q, %v; want the fallback %s", got, err, id)
			}
			return id
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			e := g.e
			id := tc.plant(g)
			release, result := g.heldUnlock(t, id)
			if !e.r.unlockInFlightNow(g.m.id) {
				t.Fatal("no in-flight mark during the unlock")
			}
			marks := g.marks()
			if err := e.r.Retry(id, JobAck{}); err == nil || err.Error() != wantP11 {
				t.Fatalf("Retry during the unlock = %v, want P11", err)
			}
			g.wantNoCommands(t, marks)
			if j, _, _ := e.st.GetJob(id); j.Status != store.JobFailed {
				t.Fatalf("job = %s, want failed", j.Status)
			}
			release()
			if out, err := result(); err != nil || out != UnlockOpened {
				t.Fatalf("UnlockPromoteLock = %v, %v", out, err)
			}
			if e.r.unlockInFlightNow(g.m.id) {
				t.Fatal("mark left after the unlock")
			}
		})
	}
}

// TestUnlockInFlightRefusesPromoteStart (C-1): planned and emergency starts
// in the master's topology are refused with P11 during the unlock, before
// any agent is asked; a start in another topology is not.
func TestUnlockInFlightRefusesPromoteStart(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	m2, _ := e.readyMaster("db-master-2")
	release, result := g.heldUnlock(t, id)
	ctx := context.Background()
	marks := g.marks()
	for _, p := range []PromoteParams{
		{MasterServerID: g.m.id, CandidateServerID: g.c.id, Mode: proto.PromotePlanned},
		{MasterServerID: g.m.id, CandidateServerID: g.b.id, Mode: proto.PromoteEmergency, OldMasterDeadAck: true},
		{MasterServerID: g.m.id, CandidateServerID: g.b.id},
	} {
		if _, err := e.r.StartPromote(ctx, p); err == nil || err.Error() != wantP11 {
			t.Fatalf("StartPromote %+v during the unlock = %v, want P11", p, err)
		}
	}
	g.wantNoCommands(t, marks)
	if _, err := e.r.StartPromote(ctx, PromoteParams{MasterServerID: m2.id, CandidateServerID: g.c.id}); err != nil &&
		err.Error() == wantP11 {
		t.Fatal("a start in another topology refused with P11")
	}
	// A second Buka kunci promote for that master.
	if _, err := e.r.UnlockPromoteLock(ctx, id); err == nil || err.Error() != wantP11 {
		t.Fatalf("second unlock = %v, want P11", err)
	}
	release()
	if _, err := result(); err != nil {
		t.Fatalf("UnlockPromoteLock: %v", err)
	}
}

// TestUnlockInFlightMarkClearedOnEveryPath (C-1): the mark goes on an
// agent refusal, a timeout and a refusal of the repeated checks.
func TestUnlockInFlightMarkClearedOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(g *promoteRig)
		code  string
	}{
		{"agent refusal", func(g *promoteRig) { g.m.fail("mysql.unlockPromote", "kunci tidak dibuka") }, UnlockRefused},
		{"timeout", func(g *promoteRig) {
			g.e.r.unlockTimeout = 200 * time.Millisecond
			g.m.mu.Lock()
			g.m.silent = map[string]bool{"mysql.unlockPromote": true}
			g.m.mu.Unlock()
		}, UnlockAgentOffline},
		{"repeated checks refuse", func(g *promoteRig) {
			g.m.reply("mysql.unlockPromote", nil)
			g.e.r.afterUnlockAnswer = func(string) { g.laterPromotion(promotionPartial) }
		}, UnlockOpenedUnrecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			id, _ := g.plantStanding(time.Now().Add(-time.Minute))
			tc.setup(g)
			_, err := g.e.r.UnlockPromoteLock(context.Background(), id)
			if ue, ok := errors.AsType[*UnlockLockError](err); !ok || ue.Code != tc.code {
				t.Fatalf("UnlockPromoteLock = %v, want code %s", err, tc.code)
			}
			if g.e.r.unlockInFlightNow(g.m.id) {
				t.Fatal("mark left")
			}
		})
	}
}

// TestUnlockOpenedUnrecorded (I-1): the agent lifted the lock, but a newer
// promotion appeared before the repeated checks: the record stands, the job
// log and the refusal say what happened (P10).
func TestUnlockOpenedUnrecorded(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	id, _ := g.plantStanding(time.Now().Add(-time.Minute))
	g.m.reply("mysql.unlockPromote", nil)
	e.r.afterUnlockAnswer = func(string) { g.laterPromotion(promotionPartial) }
	_, err := e.r.UnlockPromoteLock(context.Background(), id)
	wantUnlockRefused(t, err, UnlockOpenedUnrecorded, wantP10)
	if st := e.storedState(id); st.LockUnlocked {
		t.Fatal("record stored open")
	}
	if !strings.Contains(e.jobLog(id), wantP10) {
		t.Fatalf("job log lacks P10:\n%s", e.jobLog(id))
	}
}
