package mysql_test

import (
	"context"
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
