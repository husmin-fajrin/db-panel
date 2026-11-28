package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
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
	// The one read the refusal needs: what this server holds that the old
	// master lacks (nothing here, so relocking is safe).
	if n := peer.count(); n != 1 {
		t.Errorf("the refused run read the old master %d times, want 1", n)
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

// foreignUUID is a server_uuid no rig server has.
const foreignUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// TestGuardKeepsLockWhileCandidateHoldsOwnTransactions: a candidate that
// reads read-only for every account but holds a transaction this master
// lacks has taken writes of its own: the guard keeps the lock past the
// grace, says why and names the way out, and mysql.unlockPromote refuses
// with that text. With the candidate's set the same as the master's, the
// master opens after the grace as before.
func TestGuardKeepsLockWhileCandidateHoldsOwnTransactions(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	set := gtidExecuted(t, r.db)
	r.peer.set(mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true, GTIDExecuted: set + "," + foreignUUID + ":1"})
	r.tick(0)
	r.tick(lockGrace)
	r.tick(2 * lockGrace)
	r.wantLocked("candidate with a transaction of its own")
	ev := r.ev.lockEvent(t, 0)
	if ev.Action != "kept" || !strings.Contains(ev.Reason, foreignUUID+":1") || !strings.Contains(ev.Reason, "Ulangi") ||
		!strings.Contains(ev.Reason, "Jangan buka kunci master ini") {
		t.Errorf("kept event = %+v", ev)
	}
	resp := r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: guardCandidate})
	wantRefused(t, "unlock while the candidate holds its own transactions", resp, guardCandidateKey, foreignUUID+":1",
		"Ulangi", "kunci tidak dibuka")
	r.wantLocked("after the refused unlock")

	r.peer.set(mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true, GTIDExecuted: set})
	r.tick(2*lockGrace + mysql.LockRecheck)
	r.wantLocked("same set: the grace starts")
	r.tick(2*lockGrace + mysql.LockRecheck + lockGrace)
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("same set, after the grace: super_read_only=%v read_only=%v, want the master opened", sro, ro)
	}
}

// ownTxRig is a promote rig whose replica took a transaction of its own
// (applications wrote while it was open) and is now read_only ON with
// super_read_only OFF, replication running again, under the marker of an
// earlier planned run. old is how the old master reads.
func ownTxRig(t *testing.T, old mysql.PeerState) (*promoteRig, *fakePeer) {
	t.Helper()
	r := newPromoteRig(t)
	if err := r.d.Pins().WritePromoting(mysql.Promoting{Mode: proto.PromotePlanned, OldSource: r.source,
		At: time.Now().UTC().Truncate(time.Second)}); err != nil {
		t.Fatal(err)
	}
	execAll(t, r.rdb, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF", "INSERT INTO toko.t VALUES (500)",
		"SET PERSIST read_only = ON", "SET PERSIST super_read_only = OFF")
	if !mysql.GTIDHasUUID(gtidExecuted(t, r.rdb), r.replicaServerID) {
		t.Fatal("the replica has no transaction of its own")
	}
	if s := readFlags(t, r.replica); s.running() != (runFlags{RO: true}) {
		t.Fatalf("replica flags %s, want running ON/OFF", s)
	}
	old.GTIDExecuted = gtidExecuted(t, r.mdb)
	peer := &fakePeer{}
	peer.set(old)
	t.Cleanup(mysql.SetReadPeer(peer.read))
	return r, peer
}

// runExpired runs a planned promote whose old master's lock has run out.
func (r *promoteRig) runExpired() proto.JobDone {
	r.t.Helper()
	p := r.params(proto.PromotePlanned)
	none := 0
	p.LockRemainingSec = &none
	return r.run(p)
}

// TestPromoteOnAppsLockedCandidateWithOwnTransactionsFinishesOpening: a
// server found read_only ON, super_read_only OFF, holding transactions the
// old master lacks while the old master refuses every write, is the master
// in fact: the run opens writes (both statements, through promote_open.go)
// and ends as a resumed promote, even with the lock run out.
func TestPromoteOnAppsLockedCandidateWithOwnTransactionsFinishesOpening(t *testing.T) {
	r, peer := ownTxRig(t, mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true})
	d := r.runExpired()
	if !d.OK {
		t.Fatalf("promote = %q; want the opening finished", d.Error)
	}
	res := promoteResult(t, d)
	if !res.Resumed || res.Mode != proto.PromotePlanned {
		t.Errorf("result = %+v; want resumed as planned", res)
	}
	if peer.count() != 1 {
		t.Errorf("old master read %d times, want 1", peer.count())
	}
	wantFlags(t, r.replica, "after the promote", runFlags{})
	if n := count(t, r.rdb, "SELECT COUNT(*) FROM performance_schema.replication_connection_configuration"); n != 0 {
		t.Errorf("%d replication channels left", n)
	}
	if got := pinFile(t, r.dir, mysql.RoleFile); got != "master" {
		t.Errorf("role pin = %q, want master", got)
	}
	if got := pinFile(t, r.dir, mysql.PromotingFile); got != "" {
		t.Errorf("marker left: %s", got)
	}
	if n := count(t, r.rdb, "SELECT COUNT(*) FROM toko.t WHERE id = 500"); n != 1 {
		t.Errorf("own row count = %d, want 1", n)
	}
}

// TestPromoteOnAppsLockedCandidateWithOwnTransactionsRefusesWhenOldMasterTakesWrites:
// the same server, with an old master that may take writes or cannot be
// read, is refused with nothing changed and a text that says the two
// servers may hold different writes and nothing is to be done by hand.
func TestPromoteOnAppsLockedCandidateWithOwnTransactionsRefusesWhenOldMasterTakesWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  mysql.PeerState
	}{
		{"old master writable", mysql.PeerState{Reachable: true}},
		{"old master apps-locked", mysql.PeerState{Reachable: true, ReadOnly: true}},
		{"old master unreadable", mysql.PeerState{Err: "dial tcp: i/o timeout"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := ownTxRig(t, tc.old)
			flags, marker, repl := readFlags(t, r.replica), pinFile(t, r.dir, mysql.PromotingFile), replState(t, r.rdb)
			d := r.runExpired()
			if d.OK {
				t.Fatal("promote succeeded; want refused")
			}
			for _, part := range []string{"tulisan yang berbeda", "tidak ada yang diubah", "Jangan kunci, buka kunci", "administrator MySQL"} {
				if !strings.Contains(d.Error, part) {
					t.Errorf("refusal %q does not hold %q", d.Error, part)
				}
			}
			if got := readFlags(t, r.replica); got != flags {
				t.Errorf("flags %s, before %s", got, flags)
			}
			if got := pinFile(t, r.dir, mysql.PromotingFile); got != marker {
				t.Errorf("marker %q, before %q", got, marker)
			}
			time.Sleep(time.Second)
			if got := replState(t, r.rdb); got != repl {
				t.Errorf("replication %s, before %s", got, repl)
			}
		})
	}
}

// TestPromoteRerunDoesNotStartReplicationOverOwnTransactions: a server
// locked fully again (both flags ON) that holds a transaction the old
// master lacks takes the verified path; when that path fails after the
// relay-log step, replication is not started again and the marker is
// kept: the refusal says the two servers may hold different writes.
func TestPromoteRerunDoesNotStartReplicationOverOwnTransactions(t *testing.T) {
	r, _ := ownTxRig(t, mysql.PeerState{Reachable: true, ReadOnly: true, SuperReadOnly: true})
	execAll(t, r.rdb, "SET PERSIST super_read_only = ON")
	marker := pinFile(t, r.dir, mysql.PromotingFile)
	d := r.runExpired()
	if d.OK {
		t.Fatal("promote succeeded; want refused on the lock margin")
	}
	for _, part := range []string{"tulisan yang berbeda", "replikasi tidak dijalankan", "administrator MySQL"} {
		if !strings.Contains(d.Error, part) {
			t.Errorf("refusal %q does not hold %q", d.Error, part)
		}
	}
	time.Sleep(time.Second)
	if got := replState(t, r.rdb); !strings.HasPrefix(got, "receiver OFF") {
		t.Errorf("replication after the refusal: %s; want the receiver not started", got)
	}
	if got := pinFile(t, r.dir, mysql.PromotingFile); got != marker {
		t.Errorf("marker %q, before %q", got, marker)
	}
	wantFlags(t, r.replica, "after the refusal", runFlags{RO: true, SRO: true})
}

// candReplUser makes dbpanel_repl@'%' on a candidate without writing its
// binary log, as the agent makes accounts on a pinned replica
// (accountMode.local): a candidate holds no transaction of its own, which
// the old master's guard would take for writes the candidate took.
func candReplUser(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	withDB(t, inst, func(db *sql.DB) {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		for _, q := range []string{"SET SESSION sql_log_bin = 0",
			"CREATE USER IF NOT EXISTS 'dbpanel_repl'@'%' IDENTIFIED BY " + sqlLiteral(replPassword),
			"GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO 'dbpanel_repl'@'%'"} {
			if _, err := conn.ExecContext(context.Background(), q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	})
}
