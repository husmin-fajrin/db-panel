package mysql_test

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// quarantineParams names the guard tests' candidate as the new master.
var quarantineParams = proto.QuarantineParams{NewMaster: guardCandidate}

// quarantineRig is a pinned master (writable, no lock) whose driver reads
// the new master through a fake peer and keeps its replicas-seen file in
// a temporary directory.
type quarantineRig struct {
	*pinRig
	inst *testmysql.Instance
	db   *sql.DB
	dir  string
	seen string
	ev   *eventLog
	peer *fakePeer
	uuid string
}

func newQuarantineRig(t *testing.T) *quarantineRig {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "master", "")
	r := &quarantineRig{pinRig: newPinRig(t, inst.Host(), dir), inst: inst, dir: dir,
		seen: filepath.Join(t.TempDir(), "replicas-seen"), ev: &eventLog{}, peer: &fakePeer{}}
	r.db = rootDB(t, inst)
	r.d.Events = r.ev
	r.d.SeenPath = r.seen
	t.Cleanup(mysql.SetReadPeer(r.peer.read))
	if err := r.db.QueryRow("SELECT @@global.server_uuid").Scan(&r.uuid); err != nil {
		t.Fatal(err)
	}
	// A transaction of this server's own, which its promoted replica holds.
	execAll(t, r.db, "CREATE DATABASE toko")
	return r
}

// promoted makes the fake new master a writable server holding this
// server's transactions.
func (r *quarantineRig) promoted() {
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-4,\n" + r.uuid + ":1-9",
		ServerUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
}

func (r *quarantineRig) quarantine(p proto.QuarantineParams) proto.Response {
	r.t.Helper()
	return r.dispatch("mysql.quarantine", p)
}

func (r *quarantineRig) seenHosts(hosts ...string) {
	r.t.Helper()
	if err := mysql.RecordReplicasSeen(r.seen, hosts, time.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *quarantineRig) tick() {
	r.t.Helper()
	if err := r.d.GuardTick(context.Background(), time.Now()); err != nil {
		r.t.Fatalf("GuardTick: %v", err)
	}
}

// wantQuarantined checks both flags are on and PERSISTed, and the pins.
func wantQuarantined(t *testing.T, db *sql.DB, dir, src string) {
	t.Helper()
	if sro, ro := readOnly(t, db); !sro || !ro {
		t.Errorf("quarantined: super_read_only=%v read_only=%v", sro, ro)
	}
	if s, ro := persisted(t, db, "super_read_only"), persisted(t, db, "read_only"); s != "ON" || ro != "ON" {
		t.Errorf("quarantined: persisted super_read_only=%q read_only=%q", s, ro)
	}
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "quarantined" || s != src {
		t.Errorf("pins = %q, %q; want quarantined, %s", role, s, src)
	}
	if got := pinFile(t, dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after quarantine = %q", got)
	}
}

// wantMaster checks a refused quarantine left the pins alone.
func wantMaster(t *testing.T, dir, what string) {
	t.Helper()
	if role, s := pinFile(t, dir, mysql.RoleFile), pinFile(t, dir, mysql.SourceFile); role != "master" || s != "" {
		t.Errorf("%s: pins = %q, %q; want master and no source", what, role, s)
	}
}

// TestQuarantineRequiresPromotedPeer: under a promote lock for the new
// master, quarantine is refused while the new master cannot be read, is
// read-only (not promoted), or does not hold this server's transactions;
// a promoted replica holding them quarantines this server for good.
func TestQuarantineRequiresPromotedPeer(t *testing.T) {
	r := newLockRig(t, guardCandidateKey)
	r.d.SeenPath = filepath.Join(t.TempDir(), "replicas-seen") // none: the lock is enough
	quarantine := func() proto.Response { return r.dispatch("mysql.quarantine", quarantineParams) }

	r.peer.set(mysql.PeerState{Err: "dial tcp 10.9.9.9:3306: connection refused"})
	resp := quarantine()
	wantRefused(t, "unreachable new master", resp, guardCandidateKey, "tidak bisa dibaca")
	if strings.Contains(resp.Error.Message, replPassword) {
		t.Errorf("error shows the password: %s", resp.Error.Message)
	}
	r.peer.set(mysql.PeerState{Reachable: true, SuperReadOnly: true, ReadOnly: true, GTIDExecuted: r.uuid + ":1-3"})
	wantRefused(t, "read-only new master", quarantine(), "read-only")
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9"})
	wantRefused(t, "new master without this server's transactions", quarantine(), "tidak memuat transaksi server ini", r.uuid)
	r.wantLocked("after refused quarantines")
	r.wantEvents()

	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9,\n" + r.uuid + ":1-3"})
	res := decode[proto.QuarantineResult](t, quarantine())
	if !res.Applied || res.Already {
		t.Errorf("result = %+v, want applied", res)
	}
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantined)
	if src := r.ev.payload(t, 0)["new_source"]; src != guardCandidateKey {
		t.Errorf("quarantined event new_source = %q", src)
	}
	if c := r.peer.creds[len(r.peer.creds)-1]; c != guardCandidate {
		t.Errorf("new master read with %+v", c)
	}
	for _, ev := range r.ev.all() {
		if strings.Contains(string(ev.Payload), replPassword) {
			t.Errorf("event shows the password: %s", ev.Payload)
		}
	}
	// What a quarantined server refuses now.
	wantRefused(t, "setReadOnly off", r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false}), "quarantined")
}

// TestQuarantineIdempotent: on a quarantined server mysql.quarantine for
// the pinned new master only makes sure read-only is on (Already), also
// after the flags were lost; another new master is refused and the pin
// stays. The guard keeps a quarantined server read-only and reports it.
func TestQuarantineIdempotent(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	reads := r.peer.count()

	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Already || !res.Applied {
		t.Errorf("second quarantine = %+v, want already and applied", res)
	}
	execAll(t, r.db, "RESET PERSIST super_read_only", "RESET PERSIST read_only",
		"SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	res = decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Already || !res.Applied {
		t.Errorf("quarantine after the flags were lost = %+v", res)
	}
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	if n := r.peer.count(); n != reads {
		t.Errorf("new master read %d more times on a quarantined server", n-reads)
	}

	other := quarantineParams
	other.NewMaster.Host = "10.9.9.8"
	wantRefused(t, "quarantine under another new master", r.quarantine(other), "sudah dikarantina", guardCandidateKey, "set-source")
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantined)

	// The guard: nothing to do while read-only; flags lost, put back and
	// reported.
	r.tick()
	r.wantEvents(proto.EventQuarantined)
	execAll(t, r.db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	r.tick()
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Errorf("after the guard: super_read_only=%v read_only=%v", sro, ro)
	}
	r.wantEvents(proto.EventQuarantined, proto.EventQuarantined)
}

// TestQuarantineWhenMySQLDownWritesPinThenLocksOnStart: with MySQL down
// the GTID check is impossible, so the replicas-seen file is required
// even under a lock for the new master; once it holds the new master the
// pins are written (Applied false), and the guard makes MySQL read-only
// as soon as it answers again, reporting monitor.quarantined then.
func TestQuarantineWhenMySQLDownWritesPinThenLocksOnStart(t *testing.T) {
	r := newQuarantineRig(t)
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: guardCandidateKey, Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.promoted()
	// The lock file never reached MySQL (no guard tick): it comes back
	// writable.
	r.inst.Stop(t)

	wantRefused(t, "quarantine with MySQL down, new master never seen", r.quarantine(quarantineParams),
		"10.9.9.9 tidak pernah tercatat", "MySQL server ini mati")
	wantMaster(t, r.dir, "refused while down")
	r.seenHosts("10.9.9.9")
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if res.Applied || res.Already {
		t.Errorf("quarantine with MySQL down = %+v, want not applied", res)
	}
	if role, s := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "quarantined" || s != guardCandidateKey {
		t.Errorf("pins = %q, %q", role, s)
	}
	if got := pinFile(t, r.dir, mysql.LockFile); got != "" {
		t.Errorf("lock file = %q", got)
	}
	r.tick() // still down: nothing to do
	r.wantEvents()
	res = decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Already || res.Applied {
		t.Errorf("quarantine again while down = %+v, want already, not applied", res)
	}

	r.inst.Restart(t)
	if sro, _ := readOnly(t, r.db); sro {
		t.Fatal("MySQL came back read-only by itself; the test proves nothing")
	}
	r.tick()
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantined)
	if src := r.ev.payload(t, 0)["new_source"]; src != guardCandidateKey {
		t.Errorf("quarantined event new_source = %q", src)
	}
	r.tick()
	r.wantEvents(proto.EventQuarantined)
}

// TestQuarantineUnlockedMasterNeedsReplicaSeen: a master not locked for
// the new master (no lock, or a lock for another candidate) is
// quarantined only when the new master was seen as its replica; the
// GTID check applies too.
func TestQuarantineUnlockedMasterNeedsReplicaSeen(t *testing.T) {
	r := newQuarantineRig(t)
	r.promoted()
	wantRefused(t, "no replicas-seen file", r.quarantine(quarantineParams), "10.9.9.9 tidak pernah tercatat", "tidak dikunci")
	r.seenHosts("10.9.9.8", "db2.example")
	wantRefused(t, "new master not among the replicas seen", r.quarantine(quarantineParams), "tidak pernah tercatat")
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: "10.9.9.8:3306", Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "locked for another candidate", r.quarantine(quarantineParams), "tidak pernah tercatat")
	if err := r.d.Pins().ClearLock(); err != nil {
		t.Fatal(err)
	}
	if sro, _ := readOnly(t, r.db); sro {
		t.Error("refused quarantine made the master read-only")
	}
	wantMaster(t, r.dir, "refused quarantines")
	r.wantEvents()

	r.seenHosts("10.9.9.9")
	r.peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9"})
	wantRefused(t, "seen but without this server's transactions", r.quarantine(quarantineParams), "tidak memuat transaksi server ini")
	r.promoted()
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Applied {
		t.Errorf("result = %+v", res)
	}
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantined)

	// Only a master or a quarantined server takes the command.
	rep := newPinRig(t, nil, pinDir(t, "replica", "10.0.0.1:3306"))
	wantRefused(t, "quarantine on a replica", rep.dispatch("mysql.quarantine", quarantineParams), "dipin sebagai replica")
	un := newPinRig(t, nil, pinDir(t, "", ""))
	wantRefused(t, "quarantine on an unassigned server", un.dispatch("mysql.quarantine", quarantineParams), "dipin sebagai unassigned")
}

// TestQuarantineSeenHostForms: the new master's host matches the
// replicas-seen file as the processlist shows it: as given, as an
// address it resolves to, or as a name its addresses resolve back to.
func TestQuarantineSeenHostForms(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replicas-seen")
	seen := func(hosts ...string) {
		t.Helper()
		if err := mysql.RecordReplicasSeen(path, hosts, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	want := func(host string, ok bool) {
		t.Helper()
		got, err := mysql.ReplicaSeenAs(ctx, path, host)
		if err != nil || got != ok {
			t.Errorf("ReplicaSeenAs(%q) = %v, %v; want %v", host, got, err, ok)
		}
	}
	want("10.9.9.9", false) // no file
	seen("10.9.9.9", "DB2.Example")
	want("10.9.9.9", true)
	want("db2.example", true)
	want("10.9.9.90", false)
	want("10.9.9", false)

	// By resolution: localhost resolves to 127.0.0.1.
	seen("127.0.0.1")
	want("localhost", true)
	// By reverse resolution, when this host's resolver maps 127.0.0.1
	// back to a name (MySQL's own lookup without skip_name_resolve).
	path = filepath.Join(t.TempDir(), "replicas-seen")
	if names, _ := net.DefaultResolver.LookupAddr(ctx, "127.0.0.1"); len(names) > 0 {
		seen(strings.TrimSuffix(names[0], "."))
		want("127.0.0.1", true)
	} else {
		t.Log("127.0.0.1 has no reverse name here; reverse form not checked")
	}
}

func (r *quarantineRig) wantEvents(types ...string) {
	r.t.Helper()
	var got []string
	for _, ev := range r.ev.all() {
		got = append(got, ev.Type)
	}
	if strings.Join(got, ",") != strings.Join(types, ",") {
		r.t.Errorf("events = %v, want %v", got, types)
	}
}
