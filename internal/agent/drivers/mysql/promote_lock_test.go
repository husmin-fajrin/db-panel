package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	var uuid string
	if err := mdb.QueryRow("SELECT @@global.server_uuid").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	res := decode[proto.LockForPromoteResult](t, r.dispatch("mysql.lockForPromote", p))
	if res.LockedAt.Before(before) || res.LockedAt.After(time.Now()) || !res.Deadline.Equal(res.LockedAt.Add(600*time.Second)) {
		t.Errorf("locked at %v, deadline %v", res.LockedAt, res.Deadline)
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
	if err != nil || !ok || l.Candidate != key || !l.Deadline.Equal(res.Deadline.Truncate(time.Second)) {
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
