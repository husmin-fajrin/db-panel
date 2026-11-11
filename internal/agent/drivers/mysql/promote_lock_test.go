package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

var (
	_ monitor.PromoteGuard = (*mysql.Driver)(nil)
	_ mysql.Emitter        = (*eventLog)(nil)
)

// eventLog records the events a driver emits.
type eventLog struct {
	mu  sync.Mutex
	evs []proto.Event
}

func (e *eventLog) Emit(ev proto.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
	return nil
}

func (e *eventLog) all() []proto.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]proto.Event(nil), e.evs...)
}

// payload is event i's payload as a map.
func (e *eventLog) payload(t *testing.T, i int) map[string]string {
	t.Helper()
	evs := e.all()
	if i >= len(evs) {
		t.Fatalf("event %d of %d", i, len(evs))
	}
	var m map[string]string
	if err := json.Unmarshal(evs[i].Payload, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// fakePeer answers ReadPeer with state and counts the reads.
type fakePeer struct {
	mu    sync.Mutex
	state mysql.PeerState
	reads int
	creds []proto.PeerCred
}

func (f *fakePeer) set(st mysql.PeerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = st
}

func (f *fakePeer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *fakePeer) read(_ context.Context, c proto.PeerCred, _ time.Duration) mysql.PeerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.creds = append(f.creds, c)
	return f.state
}

// guardCandidate is the candidate of the guard tests: an address nothing
// answers on; the fake peer stands in for it.
var guardCandidate = proto.PeerCred{Host: "10.9.9.9", Port: 3306, User: "dbpanel_repl", Password: replPassword}

const guardCandidateKey = "10.9.9.9:3306"

// lockRig is a pinned master under a promote lock for candidate, whose
// guard reads the candidate through a fake peer.
type lockRig struct {
	*pinRig
	inst     *testmysql.Instance
	db       *sql.DB
	dir      string
	ev       *eventLog
	peer     *fakePeer
	deadline time.Time
	uuid     string
}

func newLockRig(t *testing.T, candidate string) *lockRig {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "master", "")
	r := &lockRig{pinRig: newPinRig(t, inst.Host(), dir), inst: inst, db: rootDB(t, inst), dir: dir,
		ev: &eventLog{}, peer: &fakePeer{}, deadline: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	r.d.Events = r.ev
	t.Cleanup(mysql.SetReadPeer(r.peer.read))
	execAll(t, r.db, "SET PERSIST super_read_only = ON")
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: candidate, Deadline: r.deadline}); err != nil {
		t.Fatal(err)
	}
	if candidate == guardCandidateKey {
		r.d.ArmGuard(guardCandidate)
	}
	if err := r.db.QueryRow("SELECT @@global.server_uuid").Scan(&r.uuid); err != nil {
		t.Fatal(err)
	}
	return r
}

// tick runs the guard at deadline+after.
func (r *lockRig) tick(after time.Duration) {
	r.t.Helper()
	if err := r.d.GuardTick(context.Background(), r.deadline.Add(after)); err != nil {
		r.t.Fatalf("GuardTick(+%v): %v", after, err)
	}
}

// readOnly is the server's super_read_only and read_only.
func readOnly(t *testing.T, db *sql.DB) (sro, ro bool) {
	t.Helper()
	if err := db.QueryRow("SELECT @@global.super_read_only, @@global.read_only").Scan(&sro, &ro); err != nil {
		t.Fatal(err)
	}
	return sro, ro
}

// persisted is the PERSISTed value of name, "" when none.
func persisted(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	err := db.QueryRow("SELECT VARIABLE_VALUE FROM performance_schema.persisted_variables WHERE VARIABLE_NAME = ?", name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (r *lockRig) wantLocked(when string) {
	r.t.Helper()
	if sro, ro := readOnly(r.t, r.db); !sro || !ro {
		r.t.Errorf("%s: super_read_only=%v read_only=%v, want both ON", when, sro, ro)
	}
	if got := pinFile(r.t, r.dir, mysql.LockFile); got == "" {
		r.t.Errorf("%s: lock file gone", when)
	}
	if got := pinFile(r.t, r.dir, mysql.RoleFile); got != "master" {
		r.t.Errorf("%s: role pin = %q, want master", when, got)
	}
}

func (r *lockRig) wantEvents(types ...string) {
	r.t.Helper()
	var got []string
	for _, ev := range r.ev.all() {
		got = append(got, ev.Type)
	}
	if strings.Join(got, ",") != strings.Join(types, ",") {
		r.t.Errorf("events = %v, want %v", got, types)
	}
}

// TestLockRequiresConnectedCandidate: the lock is refused while the
// candidate has no Binlog Dump thread on the master, and for an address
// that is not the replica's; with the replica connected it is taken,
// PERSISTed, and unlockPromote (the candidate is still a replica, read
// through the real ReadPeer) opens it again.
func TestLockRequiresConnectedCandidate(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	// The candidate takes the replication account too (the lock logs in
	// to it before locking).
	replUser(t, replica)
	mdb := rootDB(t, master)
	dir := pinDir(t, "master", "")
	r := newPinRig(t, master.Host(), dir)
	cand := proto.PeerCred{Host: "127.0.0.1", Port: replica.Port, User: "dbpanel_repl", Password: replPassword}
	key := "127.0.0.1:" + strconv.Itoa(replica.Port)
	p := proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600}

	resp := r.dispatch("mysql.lockForPromote", p)
	if resp.OK || !strings.Contains(resp.Error.Message, key) {
		t.Fatalf("lock without a connected candidate = %+v", resp)
	}
	if strings.Contains(resp.Error.Message, replPassword) {
		t.Errorf("error shows the password: %s", resp.Error.Message)
	}
	for _, sec := range []int{59, 3601} {
		bad := p
		bad.TimeoutSec = sec
		if resp := r.dispatch("mysql.lockForPromote", bad); resp.OK || resp.Error.Code != proto.CodeBadParams {
			t.Errorf("lock with timeout %d = %+v", sec, resp)
		}
	}
	if sro, _ := readOnly(t, mdb); sro || pinFile(t, dir, mysql.LockFile) != "" {
		t.Fatalf("refused lock left super_read_only=%v, lock file %q", sro, pinFile(t, dir, mysql.LockFile))
	}

	follow(t, master, replica)
	deadline := time.Now().Add(time.Minute)
	for count(t, mdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replica never connected")
		}
		time.Sleep(200 * time.Millisecond)
	}
	other := p
	other.Candidate.Host = "127.0.0.2"
	if resp := r.dispatch("mysql.lockForPromote", other); resp.OK || !strings.Contains(resp.Error.Message, "127.0.0.2") {
		t.Fatalf("lock for an address no replica connects from = %+v", resp)
	}

	// The lock file is written first: when it cannot be, MySQL is not
	// locked.
	restore := mysql.SetWriteLock(func(*mysql.Pins, mysql.PromoteLock) error { return errors.New("disk penuh") })
	if resp := r.dispatch("mysql.lockForPromote", p); resp.OK || !strings.Contains(resp.Error.Message, "disk penuh") {
		t.Errorf("lock with a failing lock file = %+v", resp)
	}
	restore()
	if sro, _ := readOnly(t, mdb); sro || pinFile(t, dir, mysql.LockFile) != "" {
		t.Errorf("failed lock file write left super_read_only=%v, lock file %q", sro, pinFile(t, dir, mysql.LockFile))
	}
	// When MySQL refuses the PERSIST (an account without
	// SYSTEM_VARIABLES_ADMIN), the lock file is removed again.
	execAll(t, mdb, "CREATE USER 'adm'@'localhost' IDENTIFIED BY 'Adm-pw-1'", "GRANT PROCESS ON *.* TO 'adm'@'localhost'")
	weak := newPinRig(t, master.HostWith(&memCreds{p: proto.ConnectParams{User: "adm", Password: "Adm-pw-1"}}), dir)
	if resp := weak.dispatch("mysql.lockForPromote", p); resp.OK || !strings.Contains(resp.Error.Message, "SET PERSIST") {
		t.Errorf("lock with a refused PERSIST = %+v", resp)
	}
	if sro, _ := readOnly(t, mdb); sro || pinFile(t, dir, mysql.LockFile) != "" {
		t.Errorf("refused PERSIST left super_read_only=%v, lock file %q", sro, pinFile(t, dir, mysql.LockFile))
	}

	var uuid string
	if err := mdb.QueryRow("SELECT @@global.server_uuid").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	lockedAt := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	r.d.Now = func() time.Time { return lockedAt.Add(400 * time.Millisecond) }
	res := decode[proto.LockForPromoteResult](t, r.dispatch("mysql.lockForPromote", p))
	if !res.LockedAt.Equal(lockedAt) || !res.Deadline.Equal(lockedAt.Add(600*time.Second)) {
		t.Errorf("locked at %v, deadline %v; want %v and 10 minutes later", res.LockedAt, res.Deadline, lockedAt)
	}
	if res.ServerUUID != uuid || res.GTIDExecuted != gtidExecuted(t, mdb) || res.GTIDExecuted == "" {
		t.Errorf("result = %+v; want uuid %s, executed %q", res, uuid, gtidExecuted(t, mdb))
	}
	if sro, ro := readOnly(t, mdb); !sro || !ro {
		t.Errorf("after lock: super_read_only=%v read_only=%v", sro, ro)
	}
	if got := persisted(t, mdb, "super_read_only"); got != "ON" {
		t.Errorf("persisted super_read_only = %q, want ON", got)
	}
	l, ok, err := r.d.Pins().ReadLock()
	if err != nil || !ok || l.Candidate != key || !l.Deadline.Equal(res.Deadline) {
		t.Errorf("lock file = %+v, %v, %v", l, ok, err)
	}
	if resp := r.dispatch("mysql.lockForPromote", p); resp.OK || !strings.Contains(resp.Error.Message, "dikunci") {
		t.Errorf("second lock = %+v", resp)
	}
	if resp := r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}); resp.OK {
		t.Error("setReadOnly{off} accepted under the lock")
	}

	// The candidate is still a replica: unlockPromote opens the master.
	execAll(t, rootDB(t, replica), "SET GLOBAL super_read_only = ON")
	if resp := r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: cand}); !resp.OK {
		t.Fatalf("unlockPromote = %+v", resp.Error)
	}
	if sro, ro := readOnly(t, mdb); sro || ro {
		t.Errorf("after unlock: super_read_only=%v read_only=%v", sro, ro)
	}
	if got := persisted(t, mdb, "super_read_only"); got != "" {
		t.Errorf("persisted super_read_only after unlock = %q", got)
	}
	if got := pinFile(t, dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after unlock = %q", got)
	}
}

// TestGuardQuarantinesWhenCandidateIsMaster: nothing happens before the
// deadline; a writable candidate without this server's transactions is
// not taken for its successor (the lock stays); once the candidate is
// writable and holds them, the server quarantines itself under it.
func TestGuardQuarantinesWhenCandidateIsMaster(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: r.uuid + ":1-3", ServerUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
	r.tick(-time.Second)
	if n := r.peer.count(); n != 0 {
		t.Fatalf("peer read %d times before the deadline", n)
	}
	r.wantLocked("before the deadline")

	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9"})
	r.tick(0)
	r.wantLocked("candidate writable without this server's transactions")
	r.wantEvents(proto.EventLockExpired)
	if a := r.ev.payload(t, 0)["action"]; a != "kept" {
		t.Errorf("lockExpired action = %q, want kept", a)
	}

	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9,\n" + r.uuid + ":1-3"})
	r.tick(30 * time.Second)
	r.wantLocked("half a minute later (no read yet)")
	r.tick(time.Minute)
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Errorf("quarantined: super_read_only=%v read_only=%v", sro, ro)
	}
	if persisted(t, r.db, "super_read_only") != "ON" || persisted(t, r.db, "read_only") != "ON" {
		t.Errorf("quarantined: persisted super_read_only=%q read_only=%q",
			persisted(t, r.db, "super_read_only"), persisted(t, r.db, "read_only"))
	}
	if role, src := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "quarantined" || src != guardCandidateKey {
		t.Errorf("pins = %q, %q; want quarantined, %s", role, src, guardCandidateKey)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after quarantine = %q", got)
	}
	r.wantEvents(proto.EventLockExpired, proto.EventQuarantined)
	if src := r.ev.payload(t, 1)["new_source"]; src != guardCandidateKey {
		t.Errorf("quarantined event new_source = %q", src)
	}
	if p := r.peer.creds[len(r.peer.creds)-1]; p != guardCandidate {
		t.Errorf("peer read with %+v", p)
	}
	n := r.peer.count()
	r.tick(time.Hour)
	if r.peer.count() != n || len(r.ev.all()) != 2 {
		t.Error("guard still acts after the quarantine")
	}
}

// TestGuardUnlocksAfterGraceWhenStillReplica: a read-only candidate at
// the deadline starts a 60 s grace; still read-only after it, the
// master is opened (read_only too) and the lock removed.
func TestGuardUnlocksAfterGraceWhenStillReplica(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true, GTIDExecuted: r.uuid + ":1-3"})
	r.tick(0)
	r.wantLocked("at the deadline")
	r.tick(45 * time.Second)
	r.wantLocked("during the grace")
	if n := r.peer.count(); n != 1 {
		t.Errorf("peer read %d times during the grace, want 1", n)
	}
	r.wantEvents()
	r.tick(60 * time.Second)
	if n := r.peer.count(); n != 2 {
		t.Errorf("peer read %d times, want 2", n)
	}
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("after the grace: super_read_only=%v read_only=%v, want both OFF", sro, ro)
	}
	if got := persisted(t, r.db, "super_read_only"); got != "" {
		t.Errorf("persisted super_read_only after unlock = %q", got)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after unlock = %q", got)
	}
	if got := pinFile(t, r.dir, mysql.RoleFile); got != "master" {
		t.Errorf("role pin after unlock = %q", got)
	}
	r.wantEvents(proto.EventLockExpired)
	if m := r.ev.payload(t, 0); m["action"] != "unlocked" || m["candidate"] != guardCandidateKey {
		t.Errorf("lockExpired payload = %v", m)
	}
}

// TestGuardKeepsLockWhenUnreachable: an unreachable candidate keeps the
// lock; it is read again every minute, and "kept" is reported once, then
// at most once an hour. A candidate that turns read-only during the grace
// is not opened on until it was read-only at both reads.
func TestGuardKeepsLockWhenUnreachable(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.peer.set(mysql.PeerState{Err: "dial tcp 10.9.9.9:3306: i/o timeout"})
	r.tick(0)
	r.wantLocked("unreachable at the deadline")
	r.wantEvents(proto.EventLockExpired)
	if a := r.ev.payload(t, 0)["action"]; a != "kept" {
		t.Errorf("action = %q, want kept", a)
	}
	for s := 15; s < 60; s += 15 {
		r.tick(time.Duration(s) * time.Second)
	}
	if n := r.peer.count(); n != 1 {
		t.Errorf("peer read %d times in the first minute, want 1", n)
	}
	for m := 1; m < 60; m++ {
		r.tick(time.Duration(m) * time.Minute)
	}
	if n := r.peer.count(); n != 60 {
		t.Errorf("peer read %d times in an hour, want 60", n)
	}
	r.wantEvents(proto.EventLockExpired)
	r.tick(time.Hour)
	r.wantEvents(proto.EventLockExpired, proto.EventLockExpired)
	r.wantLocked("an hour later")

	// Read-only once, then unreachable again: the grace starts over.
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true})
	r.tick(time.Hour + time.Minute)
	r.peer.set(mysql.PeerState{Err: "unreachable"})
	r.tick(time.Hour + 2*time.Minute)
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true})
	r.tick(time.Hour + 3*time.Minute)
	r.wantLocked("read-only once after unreachable")
	r.tick(time.Hour + 4*time.Minute)
	if sro, _ := readOnly(t, r.db); sro {
		t.Error("not opened after two read-only reads a minute apart")
	}
}

// TestGuardKeepsLockWithoutCandidateAccount: after an agent restart the
// candidate's account (never stored) is gone: the guard cannot check the
// candidate and keeps the lock.
func TestGuardKeepsLockWithoutCandidateAccount(t *testing.T) {
	r := newLockRig(t, "10.9.9.8:3306")
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true})
	r.tick(0)
	r.tick(2 * time.Minute)
	if n := r.peer.count(); n != 0 {
		t.Errorf("peer read %d times without an account", n)
	}
	r.wantLocked("without the candidate's account")
	r.wantEvents(proto.EventLockExpired)
	if a := r.ev.payload(t, 0)["action"]; a != "kept" {
		t.Errorf("action = %q, want kept", a)
	}
}

// TestGuardIgnoresCLILock: a lock-master lock has no candidate; the guard
// never reads a peer, opens or quarantines under it.
func TestGuardIgnoresCLILock(t *testing.T) {
	r := newLockRig(t, mysql.CLILockCandidate)
	r.d.ArmGuard(guardCandidate)
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true})
	for _, after := range []time.Duration{0, time.Minute, 2 * time.Minute, 48 * time.Hour} {
		r.tick(after)
	}
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: r.uuid + ":1"})
	r.tick(49 * time.Hour)
	if n := r.peer.count(); n != 0 {
		t.Errorf("peer read %d times under a cli lock", n)
	}
	r.wantLocked("cli lock")
	r.wantEvents()
	resp := r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: guardCandidate})
	if resp.OK || !strings.Contains(resp.Error.Message, "unlock-master") {
		t.Errorf("unlockPromote under a cli lock = %+v", resp)
	}
	r.wantLocked("unlockPromote under a cli lock")
}

// TestUnlockPromoteRefusedWhenCandidateWritable: unlockPromote opens the
// master only while the lock's own candidate is reachable and read-only.
func TestUnlockPromoteRefusedWhenCandidateWritable(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	unlock := func(c proto.PeerCred) proto.Response {
		return r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: c})
	}
	other := guardCandidate
	other.Host = "10.9.9.7"
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true})
	if resp := unlock(other); resp.OK || !strings.Contains(resp.Error.Message, guardCandidateKey) {
		t.Errorf("unlock naming another candidate = %+v", resp)
	}
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: r.uuid + ":1"})
	if resp := unlock(guardCandidate); resp.OK || !strings.Contains(resp.Error.Message, guardCandidateKey) {
		t.Errorf("unlock while the candidate is writable = %+v", resp)
	}
	r.wantLocked("candidate writable")
	r.peer.set(mysql.PeerState{Err: "unreachable"})
	if resp := unlock(guardCandidate); resp.OK {
		t.Errorf("unlock while the candidate is unreachable = %+v", resp)
	}
	r.wantLocked("candidate unreachable")
	if resp := unlock(guardCandidate); resp.OK || strings.Contains(resp.Error.Message, replPassword) {
		t.Errorf("unlock error = %+v", resp.Error)
	}

	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true})
	if resp := unlock(guardCandidate); !resp.OK {
		t.Fatalf("unlock with a read-only candidate = %+v", resp.Error)
	}
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("after unlock: super_read_only=%v read_only=%v", sro, ro)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after unlock = %q", got)
	}
	if resp := unlock(guardCandidate); resp.OK {
		t.Error("unlock without a lock accepted")
	}
}

// TestLockReappliedAfterRestart: the lock survives a MySQL restart
// (PERSIST), and while the lock file exists on a master the guard puts
// super_read_only back on before anything else, even before the deadline.
func TestLockReappliedAfterRestart(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.tick(-10 * time.Minute)
	r.inst.Restart(t)
	r.wantLocked("after a restart")
	// A persisted value lost behind the agent's back.
	execAll(t, r.db, "RESET PERSIST super_read_only", "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	r.tick(-9 * time.Minute)
	r.wantLocked("after the guard's check")
	if got := persisted(t, r.db, "super_read_only"); got != "ON" {
		t.Errorf("persisted super_read_only = %q, want ON", got)
	}
	if n := r.peer.count(); n != 0 {
		t.Errorf("peer read %d times before the deadline", n)
	}
	r.wantEvents()
}

// TestLockRecordsMatchedAddress: a candidate named by a host with several
// addresses is locked for the one a Binlog Dump thread comes from; the
// lock records it, and the guard, unlockPromote and quarantine read the
// candidate at that address only (the name kept for TLS), never at the
// other one. A quarantine pins the name and records the address.
func TestLockRecordsMatchedAddress(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	mdb := rootDB(t, master)
	execAll(t, mdb, "CREATE DATABASE toko")
	follow(t, master, replica)
	deadline := time.Now().Add(time.Minute)
	for count(t, mdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replica never connected")
		}
		time.Sleep(200 * time.Millisecond)
	}
	dns := fakeDNS{
		fwd: map[string][]string{
			"cand.example":  {"6.6.6.6", "127.0.0.1"},
			"other.example": {"6.6.6.6"},
			"localhost":     {"127.0.0.1"},
		},
		rev: map[string][]string{"127.0.0.1": {"localhost"}, "6.6.6.6": {"localhost"}},
	}
	t.Cleanup(mysql.SetResolver(dns.ip, dns.addr))
	peer := &fakePeer{}
	t.Cleanup(mysql.SetReadPeer(peer.read))
	dir := pinDir(t, "master", "")
	r := newPinRig(t, master.Host(), dir)
	ev := &eventLog{}
	r.d.Events = ev
	r.d.SeenPath = filepath.Join(t.TempDir(), "replicas-seen") // none: the lock is what counts
	lockedAt := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	r.d.Now = func() time.Time { return lockedAt }
	cand := proto.PeerCred{Host: "cand.example", Port: replica.Port, User: "dbpanel_repl", Password: replPassword}
	key := "cand.example:" + strconv.Itoa(replica.Port)

	// A name whose only address is not a replica's (its PTR is a lie).
	other := cand
	other.Host = "other.example"
	wantRefused(t, "lock for a name with no replica address", r.dispatch("mysql.lockForPromote",
		proto.LockForPromoteParams{Candidate: other, TimeoutSec: 600}), "tidak sedang tersambung")

	wantAt := func(what string) {
		t.Helper()
		for _, c := range peer.creds {
			if c.Host != "127.0.0.1" || c.ServerName != "cand.example" {
				t.Errorf("%s: candidate read at %q (name %q), want 127.0.0.1 (cand.example)", what, c.Host, c.ServerName)
			}
		}
	}
	// The candidate does not take the credential (another password):
	// refused before anything is locked.
	peer.set(mysql.PeerState{Err: "Error 1045: Access denied for user 'dbpanel_repl'"})
	wantRefused(t, "lock with a credential the candidate refuses", r.dispatch("mysql.lockForPromote",
		proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600}), "tidak bisa dibaca dengan akun replikasi")
	if sro, _ := readOnly(t, mdb); sro || pinFile(t, dir, mysql.LockFile) != "" {
		t.Fatalf("refused lock left super_read_only=%v, lock file %q", sro, pinFile(t, dir, mysql.LockFile))
	}
	peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true})
	decode[proto.LockForPromoteResult](t, r.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600}))
	// The replica's processlist name is a recorded host too: the lock
	// reads it as another replica (no name to verify beyond itself).
	peer.creds = slices.DeleteFunc(peer.creds, func(c proto.PeerCred) bool { return c.Host == "localhost" && c.ServerName == "" })
	wantAt("lockForPromote")
	peer.creds, peer.reads = nil, 0
	l, ok, err := r.d.Pins().ReadLock()
	if err != nil || !ok || l.Candidate != key || l.Addr != "127.0.0.1" {
		t.Fatalf("lock = %+v, %v, %v; want candidate %s at 127.0.0.1", l, ok, err, key)
	}

	// unlockPromote: writable candidate, refused; read at the address.
	var uuid string
	if err := mdb.QueryRow("SELECT @@global.server_uuid").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1"})
	if resp := r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: cand}); resp.OK {
		t.Error("unlockPromote with a writable candidate accepted")
	}
	wantAt("unlockPromote")

	// The guard past the deadline: writable without this server's
	// transactions, kept; read at the address.
	if err := r.d.GuardTick(context.Background(), lockedAt.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if peer.count() != 2 {
		t.Errorf("candidate read %d times, want 2", peer.count())
	}
	wantAt("guard")

	// Quarantine on the lock's path: read at the address, pinned at it.
	peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: uuid + ":1-5"})
	res := decode[proto.QuarantineResult](t, r.dispatch("mysql.quarantine", proto.QuarantineParams{NewMaster: cand}))
	if !res.Applied {
		t.Errorf("quarantine = %+v", res)
	}
	wantAt("quarantine")
	if role, src, addr := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile), pinFile(t, dir, mysql.SourceAddrFile); role != "quarantined" || src != key || addr != "127.0.0.1" {
		t.Errorf("pins = %q, %q, addr %q; want quarantined, %s at 127.0.0.1", role, src, addr, key)
	}
	evs := ev.all()
	if len(evs) == 0 || evs[len(evs)-1].Type != proto.EventQuarantined || ev.payload(t, len(evs)-1)["new_source"] != key {
		t.Errorf("events = %+v; want monitor.quarantined naming %s", evs, key)
	}
	// Asked again under the name: the same server (the name resolves to
	// the pinned address).
	if res := decode[proto.QuarantineResult](t, r.dispatch("mysql.quarantine", proto.QuarantineParams{NewMaster: cand})); !res.Already {
		t.Errorf("quarantine again = %+v, want already", res)
	}
}

// TestGuardChecksListedReplicasBeforeUnlock (SEC-66, R-033): with the
// candidate read-only at both reads, the guard reads every replica the lock
// lists as readable when it was set, with the lock's account, before it
// opens the master. A writable replica without this server's transactions
// does not stop the unlock; one that is writable and holds them (a replica
// promoted in an emergency while the lock stood) quarantines this server
// under it.
func TestGuardChecksListedReplicasBeforeUnlock(t *testing.T) {
	// The other replica, a real MySQL, listed by the address and port it
	// was read at when the lock was set.
	other := testmysql.Start(t, testmysql.Options{})
	replUser(t, other)
	odb := rootDB(t, other)
	key := "10.9.9.9:" + strconv.Itoa(other.Port)
	cand := guardCandidate
	cand.Port = other.Port
	want := "127.0.0.1:" + strconv.Itoa(other.Port)
	r := newLockRig(t, key)
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: key, Deadline: r.deadline, Readable: []string{want}}); err != nil {
		t.Fatal(err)
	}
	r.d.ArmGuard(cand)
	// The candidate (fake) stays a read-only replica; anything else is
	// read for real.
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true, GTIDExecuted: r.uuid + ":1-3"})
	var mu sync.Mutex
	var otherReads []mysql.PeerState
	t.Cleanup(mysql.SetReadPeer(func(ctx context.Context, c proto.PeerCred, timeout time.Duration) mysql.PeerState {
		if c.Host == "10.9.9.9" {
			return r.peer.read(ctx, c, timeout)
		}
		st := mysql.ReadPeer(ctx, c, timeout)
		mu.Lock()
		otherReads = append(otherReads, st)
		mu.Unlock()
		return st
	}))
	reads := func() []mysql.PeerState {
		mu.Lock()
		defer mu.Unlock()
		return append([]mysql.PeerState(nil), otherReads...)
	}

	// Writable, but none of this server's transactions: unlocked.
	r.tick(0)
	if n := len(reads()); n != 0 {
		t.Fatalf("other replica read %d times at the first read of the candidate, want 0", n)
	}
	r.tick(time.Minute)
	if got := reads(); len(got) != 1 || !got[0].Reachable || got[0].SuperReadOnly {
		t.Fatalf("other replica reads = %+v, want one writable", got)
	}
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("other replica writable without this server's transactions: super_read_only=%v read_only=%v, want unlocked", sro, ro)
	}
	r.wantEvents(proto.EventLockExpired)
	if a := r.ev.payload(t, 0)["action"]; a != "unlocked" {
		t.Errorf("lockExpired action = %q, want unlocked", a)
	}

	// Lock again; the other replica now holds this server's transactions
	// and takes writes: quarantined under it, never opened.
	execAll(t, r.db, "SET PERSIST super_read_only = ON")
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: key, Deadline: r.deadline, Readable: []string{want}}); err != nil {
		t.Fatal(err)
	}
	r.d.ArmGuard(cand)
	execAll(t, odb, "RESET MASTER", "SET GLOBAL gtid_purged = '"+r.uuid+":1-3'")
	r.tick(0)
	r.tick(time.Minute)
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Errorf("quarantined: super_read_only=%v read_only=%v", sro, ro)
	}
	if persisted(t, r.db, "super_read_only") != "ON" || persisted(t, r.db, "read_only") != "ON" {
		t.Errorf("quarantined: persisted super_read_only=%q read_only=%q",
			persisted(t, r.db, "super_read_only"), persisted(t, r.db, "read_only"))
	}
	if role, src := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "quarantined" || src != want {
		t.Errorf("pins = %q, %q; want quarantined, %s", role, src, want)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after quarantine = %q", got)
	}
	r.wantEvents(proto.EventLockExpired, proto.EventQuarantined)
	if src := r.ev.payload(t, 1)["new_source"]; src != want {
		t.Errorf("quarantined event new_source = %q, want %s", src, want)
	}
}

// TestGuardTickSkipsWhileBusy (SEC-69): while a promote command holds the
// lock (a repoint probing its old source), the guard's tick returns at
// once without reading anything; the next tick does its work.
func TestGuardTickSkipsWhileBusy(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true, GTIDExecuted: r.uuid + ":1-3"})
	release := r.d.HoldPromoteMu()
	done := make(chan error, 1)
	go func() { done <- r.d.GuardTick(context.Background(), r.deadline) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GuardTick while busy: %v", err)
		}
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("GuardTick waited for the promote lock")
	}
	release()
	if n := r.peer.count(); n != 0 {
		t.Errorf("busy tick read the candidate %d times", n)
	}
	r.tick(0)
	if n := r.peer.count(); n != 1 {
		t.Errorf("tick after the command read the candidate %d times, want 1", n)
	}
}

// TestLockForPromoteRecordsCandidate: with nothing recorded yet and no
// monitor tick, a successful lockForPromote has put the candidate's host
// and port in the seen files; a refused lock records nothing, and a failing
// record (an unusable seen directory) never fails the lock. The address the
// candidate was matched at is recorded even when its Binlog Dump thread is
// gone by the time the replicas are recorded.
func TestLockForPromoteRecordsCandidate(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	replUser(t, replica)
	mdb := rootDB(t, master)
	follow(t, master, replica)
	deadline := time.Now().Add(time.Minute)
	for count(t, mdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replica never connected")
		}
		time.Sleep(200 * time.Millisecond)
	}
	var host string
	if err := mdb.QueryRow(`SELECT HOST FROM information_schema.PROCESSLIST WHERE COMMAND LIKE 'Binlog Dump%'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	host = mysql.HostWithoutPort(host)
	cand := proto.PeerCred{Host: "127.0.0.1", Port: replica.Port, User: "dbpanel_repl", Password: replPassword}
	p := proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600}

	// A record that cannot be written does not fail the lock.
	dir := pinDir(t, "master", "")
	r := newPinRig(t, master.Host(), dir)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.d.SeenPath = filepath.Join(blocker, "sub", "replicas-seen")
	decode[proto.LockForPromoteResult](t, r.dispatch("mysql.lockForPromote", p))
	if _, ok, _ := r.d.Pins().ReadLock(); !ok {
		t.Fatal("no lock file after a lock whose record failed")
	}

	seenDir := t.TempDir()
	dir2 := pinDir(t, "master", "")
	r2 := newPinRig(t, master.Host(), dir2)
	r2.d.SeenPath = filepath.Join(seenDir, "replicas-seen")
	if ok, err := mysql.ReplicaSeen(r2.d.SeenPath, host); ok || err != nil {
		t.Fatalf("host seen before the lock: %v, %v", ok, err)
	}
	execAll(t, mdb, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	decode[proto.LockForPromoteResult](t, r2.dispatch("mysql.lockForPromote", p))
	if ok, err := mysql.ReplicaSeen(r2.d.SeenPath, host); !ok || err != nil {
		t.Errorf("ReplicaSeen(%q) after the lock = %v, %v; want true", host, ok, err)
	}
	ports, err := mysql.ReadSeenPorts(filepath.Join(seenDir, "replica-ports-seen"))
	if err != nil || ports[replica.Port].IsZero() {
		t.Errorf("ports after the lock = %v, %v; want %d", ports, err, replica.Port)
	}

	// The candidate's Binlog Dump thread drops after it was matched and
	// before the replicas are recorded: the address it was matched at is
	// recorded all the same.
	dir3 := pinDir(t, "master", "")
	r3 := newPinRig(t, master.Host(), dir3)
	r3.d.SeenPath = filepath.Join(t.TempDir(), "replicas-seen")
	rdb := rootDB(t, replica)
	t.Cleanup(mysql.SetBeforeLockRecord(func() {
		execAll(t, rdb, "STOP REPLICA")
		deadline := time.Now().Add(time.Minute)
		for count(t, mdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") > 0 {
			if time.Now().After(deadline) {
				t.Fatal("dump thread never left")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}))
	decode[proto.LockForPromoteResult](t, r3.dispatch("mysql.lockForPromote", p))
	if ok, err := mysql.ReplicaSeen(r3.d.SeenPath, "127.0.0.1"); !ok || err != nil {
		t.Errorf("candidate address seen after its dump thread dropped = %v, %v; want true", ok, err)
	}
}

// lockEvent is event i as a lockExpired payload.
func (e *eventLog) lockEvent(t *testing.T, i int) proto.LockExpiredEvent {
	t.Helper()
	evs := e.all()
	if i >= len(evs) || evs[i].Type != proto.EventLockExpired {
		t.Fatalf("event %d of %v is not %s", i, evs, proto.EventLockExpired)
	}
	var ev proto.LockExpiredEvent
	if err := json.Unmarshal(evs[i].Payload, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// countingReadPeer makes the driver read peers for real and counts the
// reads; it returns the count.
func countingReadPeer(t *testing.T) func() int {
	var mu sync.Mutex
	n := 0
	t.Cleanup(mysql.SetReadPeer(func(ctx context.Context, c proto.PeerCred, timeout time.Duration) mysql.PeerState {
		mu.Lock()
		n++
		mu.Unlock()
		return mysql.ReadPeer(ctx, c, timeout)
	}))
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestLockKeptWhileListedReplicaDownThenOpens (spec 5.10): a replica that
// was readable when the lock was set and is down at the deadline keeps the
// lock, reported once with its host:port; the guard does not read again
// before the recheck, and opens the master once the replica answers
// read-only again (two candidate reads a grace apart, as always).
func TestLockKeptWhileListedReplicaDownThenOpens(t *testing.T) {
	b := testmysql.Start(t, testmysql.Options{})
	c := testmysql.Start(t, testmysql.Options{})
	replUser(t, b)
	replUser(t, c)
	execAll(t, rootDB(t, b), "SET PERSIST super_read_only = ON")
	execAll(t, rootDB(t, c), "SET PERSIST super_read_only = ON")
	keyB := "127.0.0.1:" + strconv.Itoa(b.Port)
	keyC := "127.0.0.1:" + strconv.Itoa(c.Port)
	r := newLockRig(t, keyB)
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: keyB, Deadline: r.deadline, Readable: []string{keyC}}); err != nil {
		t.Fatal(err)
	}
	r.d.ArmGuard(proto.PeerCred{Host: "127.0.0.1", Port: b.Port, User: "dbpanel_repl", Password: replPassword})
	reads := countingReadPeer(t)

	r.tick(0)
	c.Stop(t)
	r.tick(lockGrace)
	r.wantLocked("listed replica down")
	r.wantEvents(proto.EventLockExpired)
	if ev := r.ev.lockEvent(t, 0); ev.Action != "kept" || strings.Join(ev.Hosts, ",") != keyC {
		t.Errorf("lockExpired = %+v, want kept with hosts [%s]", ev, keyC)
	}
	n := reads()
	r.tick(lockGrace + 30*time.Second)
	if got := reads(); got != n {
		t.Errorf("%d reads 30 s after the lock was kept, want none", got-n)
	}
	r.wantEvents(proto.EventLockExpired)

	c.Restart(t)
	r.tick(lockGrace + time.Minute)
	r.wantLocked("first read after the recheck")
	r.tick(lockGrace + time.Minute + lockGrace)
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("listed replica back read-only: super_read_only=%v read_only=%v, want unlocked", sro, ro)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after the unlock = %q", got)
	}
	r.wantEvents(proto.EventLockExpired, proto.EventLockExpired)
	if ev := r.ev.lockEvent(t, 1); ev.Action != "unlocked" {
		t.Errorf("second lockExpired = %+v, want unlocked", ev)
	}
}

// lockGrace is the guard's grace between two read-only reads.
const lockGrace = proto.PromoteLockGrace

// TestPartialReplicaUnreadableAtLockDoesNotHoldLock (spec 5.10): a replica
// the candidate's account cannot log in to (a partial replica) is not
// readable when the lock is set, so it is not listed and the lock opens at
// its deadline as before.
func TestPartialReplicaUnreadableAtLockDoesNotHoldLock(t *testing.T) {
	b := testmysql.Start(t, testmysql.Options{})
	c := testmysql.Start(t, testmysql.Options{}) // no dbpanel_repl
	replUser(t, b)
	execAll(t, rootDB(t, b), "SET GLOBAL super_read_only = ON")
	keyB := "127.0.0.1:" + strconv.Itoa(b.Port)
	cand := proto.PeerCred{Host: "127.0.0.1", Port: b.Port, User: "dbpanel_repl", Password: replPassword}
	r := newLockRig(t, keyB)
	countingReadPeer(t)
	list := mysql.ReadableAtLock(context.Background(), cand, "127.0.0.1", b.Port, []string{"127.0.0.1"}, []int{c.Port})
	if len(list) != 0 {
		t.Fatalf("readable at lock = %v, want none (the partial replica cannot be read)", list)
	}
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: keyB, Deadline: r.deadline, Readable: list}); err != nil {
		t.Fatal(err)
	}
	r.d.ArmGuard(cand)
	r.tick(0)
	r.tick(lockGrace)
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("super_read_only=%v read_only=%v, want unlocked", sro, ro)
	}
	r.wantEvents(proto.EventLockExpired)
	if ev := r.ev.lockEvent(t, 0); ev.Action != "unlocked" {
		t.Errorf("lockExpired = %+v, want unlocked", ev)
	}
}

// TestReadableAtLockParallel: every recorded host is read at the
// candidate's port and every recorded port (the candidate itself only at
// its own port is skipped), at most MaxPeerReads at once: 20 reads of 5 s
// each take two rounds, not 100 s. Only the readable ones are listed,
// sorted.
func TestReadableAtLockParallel(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	tried := map[string]bool{}
	t.Cleanup(mysql.SetReadPeer(func(ctx context.Context, c proto.PeerCred, timeout time.Duration) mysql.PeerState {
		key := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
		mu.Lock()
		tried[key] = true
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return mysql.PeerState{Err: ctx.Err().Error()}
		}
		if c.Host == "10.0.0.1" || c.ServerName != "" {
			return mysql.PeerState{Err: "refused"}
		}
		return mysql.PeerState{Reachable: true, SuperReadOnly: true}
	}))
	cand := proto.PeerCred{Host: "db-b.example", Port: 3306, User: "dbpanel_repl", Password: replPassword}
	hosts := []string{"10.0.0.7", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6", "10.0.0.1"}
	start := time.Now()
	got := mysql.ReadableAtLock(context.Background(), cand, "10.0.0.7", 3306, hosts, []int{3308, 3307, 3306})
	if el := time.Since(start); el >= 15*time.Second {
		t.Errorf("ReadableAtLock took %v, want < 15 s (parallel)", el)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tried) != 20 {
		t.Errorf("tried %d peers, want 20: %v", len(tried), tried)
	}
	if tried["10.0.0.7:3306"] {
		t.Error("the candidate was read at its own port")
	}
	for _, k := range []string{"10.0.0.2:3306", "10.0.0.2:3307", "10.0.0.2:3308", "10.0.0.7:3307"} {
		if !tried[k] {
			t.Errorf("%s not tried", k)
		}
	}
	if maxInFlight > mysql.MaxPeerReads {
		t.Errorf("%d reads at once, want at most %d", maxInFlight, mysql.MaxPeerReads)
	}
	var want []string
	for _, h := range []string{"10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6", "10.0.0.7"} {
		for _, p := range []string{"3306", "3307", "3308"} {
			if h == "10.0.0.7" && p == "3306" {
				continue
			}
			want = append(want, h+":"+p)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("readable = %v, want %v", got, want)
	}
}

// TestUnlockUsesOwnContext: the tick's context ends as the last replica
// read returns; the master is opened all the same with the unlock's own
// context, never left half open.
func TestUnlockUsesOwnContext(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	const other = "10.9.9.8:3306"
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: guardCandidateKey, Deadline: r.deadline, Readable: []string{other}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(mysql.SetReadPeer(func(_ context.Context, c proto.PeerCred, _ time.Duration) mysql.PeerState {
		if c.Host == "10.9.9.8" {
			defer cancel()
		}
		return mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true}
	}))
	r.tick(0)
	if err := r.d.GuardTick(ctx, r.deadline.Add(lockGrace)); err != nil {
		t.Fatalf("GuardTick: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("the listed replica was not read")
	}
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("super_read_only=%v read_only=%v, want unlocked", sro, ro)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file = %q, want removed", got)
	}
	r.wantEvents(proto.EventLockExpired)
}

// TestLockRefusedWhenCandidateUnreadable (spec 5.4): a connected candidate
// the given credential cannot log in to refuses the lock at once:
// nothing is locked and no lock file is left.
func TestLockRefusedWhenCandidateUnreadable(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master) // replicated to the candidate too
	mdb := rootDB(t, master)
	follow(t, master, replica)
	waitDumpThread(t, mdb)
	dir := pinDir(t, "master", "")
	r := newPinRig(t, master.Host(), dir)
	// A password the candidate refuses: it cannot be read with the account.
	const wrong = "sandi-yang-salah"
	cand := proto.PeerCred{Host: "127.0.0.1", Port: replica.Port, User: "dbpanel_repl", Password: wrong}
	resp := r.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600})
	wantRefused(t, "lock with an unreadable candidate", resp, "127.0.0.1:"+strconv.Itoa(replica.Port), "tidak bisa dibaca", "Master tidak dikunci")
	if resp.Error != nil && strings.Contains(resp.Error.Message, wrong) {
		t.Errorf("error shows the password: %s", resp.Error.Message)
	}
	if sro, ro := readOnly(t, mdb); sro || ro {
		t.Errorf("refused lock: super_read_only=%v read_only=%v", sro, ro)
	}
	if got := pinFile(t, dir, mysql.LockFile); got != "" {
		t.Errorf("refused lock left lock file %q", got)
	}
}

// waitDumpThread waits until a replica reads db's binary log.
func waitDumpThread(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for count(t, db, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replica never connected")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
