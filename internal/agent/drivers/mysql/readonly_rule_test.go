package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestPromoteRerunOnMixCandidateFinishesOpening: a promote whose first
// write-opening statement (SET PERSIST read_only = OFF) was applied and
// whose second was not, followed by a MySQL restart over an option file
// with both flags ON, leaves read_only OFF with super_read_only ON, which
// every account writes to (readonly_mix_test.go). Run again after the old
// master's lock has run out, the promote resumes: it does not verify again,
// roll back or start replication; it sends the second statement and ends
// as a resumed promote, both flags OFF running and stored.
func TestPromoteRerunOnMixCandidateFinishesOpening(t *testing.T) {
	r := newPromoteRig(t)
	logVersion(t, r.replica)
	writeOF(t, r.replica)
	at := time.Now().UTC().Truncate(time.Second)
	if err := r.d.Pins().WritePromoting(mysql.Promoting{Mode: proto.PromotePlanned, OldSource: r.source, At: at}); err != nil {
		t.Fatal(err)
	}
	// The earlier run: STOP REPLICA, then the first statement only, with
	// nothing else of the flags stored (as readonly_mix_test.go has it).
	execAll(t, r.rdb, "STOP REPLICA", "RESET PERSIST IF EXISTS super_read_only", "RESET PERSIST IF EXISTS read_only",
		"SET PERSIST read_only = OFF")
	r.rdb.Close()
	if s := restarted(t, r.replica); s.running() != (runFlags{SRO: true}) {
		t.Fatalf("after the restart: %s, want running OFF/ON", s)
	}
	r.rdb = rootDB(t, r.replica)

	// The old master reads locked, but its lock has run out: a verified
	// run would refuse on the lock margin and roll back.
	peer := &fakePeer{}
	peer.set(mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true, GTIDExecuted: gtidExecuted(t, r.mdb)})
	t.Cleanup(mysql.SetReadPeer(peer.read))
	t.Cleanup(mysql.SetProbePeer(func(_ context.Context, _ string, _ int, _ time.Duration) error {
		t.Error("the rerun probed the old master")
		return nil
	}))
	p := r.params(proto.PromotePlanned)
	none := 0
	p.LockRemainingSec = &none
	d := r.run(p)
	if !d.OK {
		t.Fatalf("rerun on the mix = %q; want a resumed promote", d.Error)
	}
	res := promoteResult(t, d)
	if !res.Resumed || res.Mode != proto.PromotePlanned {
		t.Errorf("result = %+v; want resumed as planned", res)
	}
	if n := peer.count(); n != 0 {
		t.Errorf("the rerun read the old master %d times; a resume does not verify again", n)
	}
	wantFlags(t, r.replica, "after the rerun", runFlags{})
	r.wantPromoted(res, proto.PromotePlanned)
	if n := count(t, r.rdb, "SELECT COUNT(*) FROM performance_schema.replication_applier_status WHERE SERVICE_STATE = 'ON'"); n != 0 {
		t.Errorf("replication applier running after the rerun (%d)", n)
	}
	r.ev.mu.Lock()
	logs := strings.Join(r.ev.logs, "\n")
	r.ev.mu.Unlock()
	if strings.Contains(logs, "replikasi dijalankan kembali") {
		t.Errorf("the rerun rolled back:\n%s", logs)
	}
	if !strings.Contains(logs, "pembukaan tulis diselesaikan") {
		t.Errorf("job log does not say the opening was finished:\n%s", logs)
	}
}

// TestGuardDoesNotOpenMasterForMixCandidate: at the deadline, and past the
// grace, a candidate with read_only OFF and super_read_only ON (every
// account writes there) is not taken for a replica: the old master is not
// opened; holding this server's transactions, the candidate is taken for
// promoted, as one with both flags off is, and this server quarantines
// itself under it, still read-only.
func TestGuardDoesNotOpenMasterForMixCandidate(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.peer.set(mysql.PeerState{Reachable: true, ReadOnly: false, SuperReadOnly: true, GTIDExecuted: r.uuid + ":1-3"})
	for _, after := range []time.Duration{0, lockGrace, 2 * lockGrace, time.Hour} {
		r.tick(after)
		if sro, ro := readOnly(t, r.db); !sro || !ro {
			t.Fatalf("+%v: the master was opened (super_read_only=%v read_only=%v) beside a candidate every account writes to", after, sro, ro)
		}
	}
	if persisted(t, r.db, "super_read_only") != "ON" || persisted(t, r.db, "read_only") != "ON" {
		t.Errorf("persisted super_read_only=%q read_only=%q, want ON, ON",
			persisted(t, r.db, "super_read_only"), persisted(t, r.db, "read_only"))
	}
	if role, src := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "quarantined" || src != guardCandidateKey {
		t.Errorf("pins = %q, %q; want quarantined under %s, as for a promoted candidate", role, src, guardCandidateKey)
	}
	for _, ev := range r.ev.all() {
		if ev.Type == proto.EventLockExpired && strings.Contains(string(ev.Payload), "unlocked") {
			t.Errorf("guard reported the master unlocked: %s", ev.Payload)
		}
	}
	r.wantEvents(proto.EventQuarantined)
}

// replState is the replication channel's receiver and applier state.
func replState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var io, applier string
	if err := db.QueryRow(`SELECT c.SERVICE_STATE, a.SERVICE_STATE FROM performance_schema.replication_connection_status c
		JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE CHANNEL_NAME = ''`).Scan(&io, &applier); err != nil {
		t.Fatal(err)
	}
	return "receiver " + io + ", applier " + applier
}

// TestPromoteRerunOnAppsLockedCandidateRefusesUntouched: a promote run
// that finds this server with read_only ON and super_read_only OFF (an
// earlier run may have opened writes and a restart brought read_only
// back, or a load lifted super_read_only) refuses before anything: the
// flags, the promoting marker and replication stay as they were, and the
// refusal says what was read and to lock the server fully first.
func TestPromoteRerunOnAppsLockedCandidateRefusesUntouched(t *testing.T) {
	r := newPromoteRig(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := r.d.Pins().WritePromoting(mysql.Promoting{Mode: proto.PromotePlanned, OldSource: r.source, At: at}); err != nil {
		t.Fatal(err)
	}
	execAll(t, r.rdb, "SET PERSIST super_read_only = OFF")
	flagsBefore := readFlags(t, r.replica)
	if flagsBefore.running() != (runFlags{RO: true}) {
		t.Fatalf("before the rerun: %s, want running ON/OFF", flagsBefore)
	}
	markerBefore := pinFile(t, r.dir, mysql.PromotingFile)
	replBefore := replState(t, r.rdb)

	// The old master reads locked, its lock has run out: the verified path
	// would refuse on the lock margin and roll back.
	peer := &fakePeer{}
	peer.set(mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true, GTIDExecuted: gtidExecuted(t, r.mdb)})
	t.Cleanup(mysql.SetReadPeer(peer.read))
	p := r.params(proto.PromotePlanned)
	none := 0
	p.LockRemainingSec = &none
	resp := r.dispatch("mysql.promote", p)
	if !resp.OK {
		t.Fatalf("mysql.promote = %+v; want a job started (a refusal inside the job, not an unknown command)", resp.Error)
	}
	var started proto.StartedJob
	if err := json.Unmarshal(resp.Result, &started); err != nil || started.JobID == "" {
		t.Fatalf("mysql.promote result %s: %v", resp.Result, err)
	}
	var d proto.JobDone
	select {
	case d = <-r.ev.done:
	case <-time.After(5 * time.Minute):
		t.Fatal("promote did not finish")
	}
	if d.OK {
		t.Fatal("promote on read_only ON, super_read_only OFF succeeded; want refused")
	}
	for _, part := range []string{"read_only=ON, super_read_only=OFF", "tidak ada yang diubah", "Kunci kembali", "ulangi promote"} {
		if !strings.Contains(d.Error, part) {
			t.Errorf("refusal %q does not hold %q", d.Error, part)
		}
	}
	if n := peer.count(); n != 0 {
		t.Errorf("the refused run read the old master %d times", n)
	}
	if got := readFlags(t, r.replica); got != flagsBefore {
		t.Errorf("flags after the refusal: %s, before %s", got, flagsBefore)
	}
	if got := pinFile(t, r.dir, mysql.PromotingFile); got != markerBefore {
		t.Errorf("marker after the refusal = %q, before %q", got, markerBefore)
	}
	time.Sleep(time.Second)
	if got := replState(t, r.rdb); got != replBefore {
		t.Errorf("replication after the refusal: %s, before %s", got, replBefore)
	}
	if got := pinFile(t, r.dir, mysql.RoleFile); got != "replica" {
		t.Errorf("role pin = %q, want replica", got)
	}
}

// TestGuardOpensAfterAppsLockedCandidateIsLockedAgain: a candidate, or a
// listed replica, read with read_only ON and super_read_only OFF keeps the
// old master locked, and the unlock refusal says so, naming that server
// and the way out. Once it reads both ON, the guard opens the master by
// itself: for the candidate after the grace (two read-only reads, as
// always), for a listed replica at its next read.
func TestGuardOpensAfterAppsLockedCandidateIsLockedAgain(t *testing.T) {
	locked := mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true}
	appsLocked := mysql.PeerState{Reachable: true, ReadOnly: true}
	unlock := func(r *lockRig) proto.Response {
		return r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: guardCandidate})
	}
	wantOpen := func(r *lockRig, when string) {
		t.Helper()
		if sro, ro := readOnly(t, r.db); sro || ro {
			t.Errorf("%s: super_read_only=%v read_only=%v, want the master opened", when, sro, ro)
		}
		if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
			t.Errorf("%s: lock file = %q", when, got)
		}
	}

	t.Run("candidate", func(t *testing.T) {
		r := newLockRig(t, guardCandidateKey)
		st := appsLocked
		st.GTIDExecuted = r.uuid + ":1-3"
		r.peer.set(st)
		r.tick(0)
		r.tick(lockGrace)
		r.wantLocked("candidate read_only ON, super_read_only OFF")
		ev := r.ev.lockEvent(t, 0)
		if ev.Action != "kept" || !strings.Contains(ev.Reason, "read_only=ON, super_read_only=OFF") ||
			!strings.Contains(ev.Reason, guardCandidateKey) || !strings.Contains(ev.Reason, "Kunci kembali") {
			t.Errorf("kept event = %+v", ev)
		}
		resp := unlock(r)
		wantRefused(t, "unlock on an apps-locked candidate", resp, guardCandidateKey, "read_only=ON, super_read_only=OFF",
			"aplikasi biasa tidak bisa menulis", "Kunci kembali")
		if resp.Error != nil && strings.Contains(resp.Error.Message, "mungkin sudah menjadi master") {
			t.Errorf("unlock refusal calls the apps-locked candidate a possible master: %s", resp.Error.Message)
		}
		r.wantLocked("after the refused unlock")

		st.SuperReadOnly = true
		r.peer.set(st)
		r.tick(lockGrace + mysql.LockRecheck)
		r.wantLocked("candidate locked again: the grace starts")
		r.tick(lockGrace + mysql.LockRecheck + lockGrace)
		wantOpen(r, "a grace after the candidate read both ON")
	})

	t.Run("listed replica", func(t *testing.T) {
		r := newLockRig(t, guardCandidateKey)
		const a = "10.9.9.7:3306"
		if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: guardCandidateKey, Deadline: r.deadline, Readable: []string{a}}); err != nil {
			t.Fatal(err)
		}
		r.d.ArmGuard(guardCandidate)
		r.peer.set(locked)
		st := appsLocked
		st.GTIDExecuted = r.uuid + ":1-3"
		r.peer.setAt(a, st)
		r.tick(0)
		r.tick(lockGrace)
		r.wantLocked("listed replica read_only ON, super_read_only OFF")
		ev := r.ev.lockEvent(t, 0)
		if ev.Action != "kept" || !strings.Contains(ev.Reason, a) || !strings.Contains(ev.Reason, "read_only=ON, super_read_only=OFF") ||
			strings.Contains(ev.Reason, "kandidat") || len(ev.Hosts) != 0 {
			t.Errorf("kept event = %+v; want the replica named in the reason, not the candidate, and no unreadable hosts", ev)
		}
		resp := unlock(r)
		wantRefused(t, "unlock with an apps-locked listed replica", resp, a, "read_only=ON, super_read_only=OFF",
			"aplikasi biasa tidak bisa menulis", "Kunci kembali")
		r.wantLocked("after the refused unlock")

		st.SuperReadOnly = true
		r.peer.setAt(a, st)
		r.tick(lockGrace + mysql.LockRecheck)
		wantOpen(r, "the next read after the listed replica read both ON")
	})
}
