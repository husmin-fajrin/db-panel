package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
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
	r.ev.flush = r.d.FlushEvents
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
// A restart comes back read-only through the start lock.
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

	// The guard: nothing to do while read-only. Flags lifted while MySQL
	// stays up (a rebuild's load, through the lift gate) are left alone; a
	// MySQL that comes back writable is made read-only again and reported.
	r.tick()
	r.wantEvents(proto.EventQuarantined)
	hold, err := r.d.LiftGate().Begin(context.Background(), r.db, func(string, string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	r.tick()
	if sro, _ := readOnly(t, r.db); sro {
		t.Errorf("guard put super_read_only back during a lift while MySQL stayed up")
	}
	r.wantEvents(proto.EventQuarantined)
	if err := hold.End(); err != nil {
		t.Fatal(err)
	}
	execAll(t, r.db, "RESET PERSIST super_read_only", "RESET PERSIST read_only")
	r.inst.Restart(t)
	// The start lock written by the quarantine locks it at start; opened
	// by hand before the guard's tick, the guard locks it again.
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Fatalf("MySQL came back with super_read_only=%v read_only=%v; the start lock should have locked it", sro, ro)
	}
	execAll(t, r.db, "RESET PERSIST super_read_only", "RESET PERSIST read_only",
		"SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	r.tick()
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	// The restart is reported as a start: late, the flags were off.
	r.wantEvents(proto.EventQuarantined, proto.EventQuarantineStart, proto.EventQuarantined)
	if st := startEvents(t, r.ev); len(st) != 1 || st[0].Result != proto.QuarantineStartLate {
		t.Errorf("start events = %+v, want one late", st)
	}
	r.tick()
	r.wantEvents(proto.EventQuarantined, proto.EventQuarantineStart, proto.EventQuarantined)
}

// TestQuarantineDownReportedOnceWhenAlreadyReadOnly: a quarantine written
// while MySQL was down is reported once MySQL answers, also when it came
// back read-only already (nothing to put back).
func TestQuarantineDownReportedOnceWhenAlreadyReadOnly(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	execAll(t, r.db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r.inst.Stop(t)
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if res.Applied {
		t.Errorf("quarantine while down = %+v", res)
	}
	r.inst.Restart(t)
	r.tick()
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantineStart, proto.EventQuarantined)
	if st := startEvents(t, r.ev); len(st) != 1 || st[0].Result != proto.QuarantineStartOK {
		t.Errorf("start events = %+v, want one ok", st)
	}
	r.tick()
	r.wantEvents(proto.EventQuarantineStart, proto.EventQuarantined)
}

// TestQuarantineGuardLeavesRebuildAlone: the rebuild of a quarantined
// server from its pinned source (dumpFrom, which lifts read-only for its
// load) succeeds while the guard ticks, and the guard reports nothing.
func TestQuarantineGuardLeavesRebuildAlone(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	target := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	dumpUser(t, mdb)
	execAll(t, mdb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)", "INSERT INTO toko.t VALUES (1), (2), (3)")
	tdb := rootDB(t, target)
	execAll(t, tdb, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")

	dir := pinDir(t, "quarantined", "127.0.0.1:"+strconv.Itoa(master.Port))
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	jev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(jev, slog.New(slog.DiscardHandler))
	rig := &snapshotRig{reg: commands.NewRegistry(), runner: runner, events: jev}
	ev := &eventLog{}
	d := &mysql.Driver{Host: target.HostWith(creds), Creds: creds, Jobs: runner, PinDir: dir, Events: ev}
	ev.flush = d.FlushEvents
	d.Register(rig.reg)
	approveRebuild(t, dir, "127.0.0.1:"+strconv.Itoa(master.Port))
	tick := func() {
		if err := d.GuardTick(context.Background(), time.Now()); err != nil {
			t.Errorf("GuardTick: %v", err)
		}
	}
	tick()
	// The first tick reports how MySQL started (this agent's start).
	if evs := ev.all(); len(evs) != 1 || evs[0].Type != proto.EventQuarantineStart {
		t.Fatalf("events of the first tick = %+v, want one %s", evs, proto.EventQuarantineStart)
	}

	stop := make(chan struct{})
	ticked := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				ticked <- n
				return
			case <-time.After(20 * time.Millisecond):
				tick()
				n++
			}
		}
	}()
	done := rig.run(t, "mysql.dumpFrom", dumpParams(master, "toko"))
	close(stop)
	n := <-ticked
	if !done.OK {
		t.Fatalf("dumpFrom on a quarantined server with the guard ticking: %s", done.Error)
	}
	if got := count(t, tdb, "SELECT COUNT(*) FROM toko.t"); got != 3 {
		t.Errorf("rows loaded = %d, want 3", got)
	}
	if n := len(ev.all()) - 1; n != 0 {
		t.Errorf("guard reported %d events during the rebuild", n)
	}
	if d.RebuildLoadMarked() {
		t.Error("the rebuild-load mark outlived the dump job")
	}
	// The load from the pinned source lets replication toward it end the
	// quarantine.
	decode[proto.ReplicaStatus](t, (&pinRig{t: t, reg: rig.reg}).dispatch("mysql.configureReplication", sourceParams(master)))
	if role := pinFile(t, dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after the dump and configureReplication = %q, want replica", role)
	}
	t.Logf("guard ticked %d times during the load", n)
}

// TestQuarantineWhenMySQLDownWritesPinThenLocksOnStart: with MySQL down
// the GTID check is impossible, so the replicas-seen file is required
// even under a lock for the new master; once it holds the new master the
// pins and the start lock are written (Applied false, StartLocked): MySQL
// starts read-only by itself. Opened by hand before the guard's first
// tick, it is made read-only again by the guard, which reports
// monitor.quarantined then.
func TestQuarantineWhenMySQLDownWritesPinThenLocksOnStart(t *testing.T) {
	r := newQuarantineRig(t)
	if err := r.d.Pins().WriteLock(mysql.PromoteLock{Candidate: guardCandidateKey, Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.promoted()
	// The promote lock never reached MySQL (no guard tick).
	r.inst.Stop(t)

	wantRefused(t, "quarantine with MySQL down, new master never seen", r.quarantine(quarantineParams),
		"10.9.9.9 tidak pernah tercatat", "MySQL server ini mati")
	wantMaster(t, r.dir, "refused while down")
	r.seenHosts("10.9.9.9")
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if res.Applied || res.Already || !res.StartLocked {
		t.Errorf("quarantine with MySQL down = %+v, want not applied, start locked", res)
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
	if !res.Already || res.Applied || !res.StartLocked {
		t.Errorf("quarantine again while down = %+v, want already, not applied, start locked", res)
	}

	r.inst.Restart(t)
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Fatalf("MySQL came back with super_read_only=%v read_only=%v; the start lock should have locked it", sro, ro)
	}
	// Someone opens it before the guard's first tick.
	execAll(t, r.db, "SET PERSIST super_read_only = OFF", "SET PERSIST read_only = OFF")
	r.tick()
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantineStart, proto.EventQuarantined)
	if st := startEvents(t, r.ev); len(st) != 1 || st[0].Result != proto.QuarantineStartLate {
		t.Errorf("start events = %+v, want one late", st)
	}
	if src := r.ev.payload(t, 1)["new_source"]; src != guardCandidateKey {
		t.Errorf("quarantined event new_source = %q", src)
	}
	r.tick()
	r.wantEvents(proto.EventQuarantineStart, proto.EventQuarantined)
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

// fakeDNS is a resolver: forward records by name, reverse by address.
type fakeDNS struct {
	fwd map[string][]string
	rev map[string][]string
}

func (f fakeDNS) ip(_ context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	for _, a := range f.fwd[strings.ToLower(host)] {
		ips = append(ips, net.ParseIP(a))
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return ips, nil
}

func (f fakeDNS) addr(_ context.Context, a string) ([]string, error) { return f.rev[a], nil }

// TestQuarantineSeenHostForms: the new master's host is resolved once and
// matches the replicas-seen file by one of its addresses, or by a reverse
// name of one that resolves back to it; the address that matched is the
// one returned (and read). A name with the attacker's address beside a
// seen replica's yields the replica's address; an unconfirmed reverse
// name never matches.
func TestQuarantineSeenHostForms(t *testing.T) {
	ctx := context.Background()
	dns := fakeDNS{
		fwd: map[string][]string{
			"evil.example":      {"6.6.6.6", "10.0.0.5"},
			"spoof.example":     {"6.6.6.6"},
			"replica1.internal": {"10.0.0.5"},
			"replica2.internal": {"10.0.0.6"},
			"localhost":         {"127.0.0.1"},
		},
		rev: map[string][]string{
			"6.6.6.6":   {"replica1.internal."}, // a PTR the attacker controls
			"10.0.0.6":  {"replica2.internal."},
			"127.0.0.1": {"localhost"},
		},
	}
	t.Cleanup(mysql.SetResolver(dns.ip, dns.addr))
	path := filepath.Join(t.TempDir(), "replicas-seen")
	seen := func(hosts ...string) {
		t.Helper()
		if err := mysql.RecordReplicasSeen(path, hosts, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	want := func(host, addr string, ok bool) {
		t.Helper()
		got, gotOK, err := mysql.SeenAddr(ctx, path, host)
		if err != nil || gotOK != ok || got != addr {
			t.Errorf("SeenAddr(%q) = %q, %v, %v; want %q, %v", host, got, gotOK, err, addr, ok)
		}
	}
	want("10.9.9.9", "", false) // no file
	seen("10.9.9.9", "10.0.0.5", "Replica1.Internal", "replica2.internal")
	want("10.9.9.9", "10.9.9.9", true)
	want("10.9.9.90", "", false)
	// Several A records: the seen replica's address, never the attacker's.
	want("evil.example", "10.0.0.5", true)
	// The attacker's PTR claims a seen name but does not resolve back.
	want("spoof.example", "", false)
	want("6.6.6.6", "", false)
	// A forward-confirmed reverse name matches.
	want("10.0.0.6", "10.0.0.6", true)
	// The host string alone is not enough: it must resolve to the address.
	want("unknown.example", "", false)
	// Resolution of a name to a seen address.
	seen("127.0.0.1")
	want("localhost", "127.0.0.1", true)
}

// TestQuarantineReadsNewMasterAtSeenAddress: without a lock the new
// master named by a host with several addresses is read at the address
// the replicas-seen file matched, with its name kept for TLS; the pin
// keeps the name and source-addr the address, which later reads dial.
func TestQuarantineReadsNewMasterAtSeenAddress(t *testing.T) {
	r := newQuarantineRig(t)
	dns := fakeDNS{fwd: map[string][]string{"db2.example": {"6.6.6.6", "10.9.9.9"}}}
	t.Cleanup(mysql.SetResolver(dns.ip, dns.addr))
	r.seenHosts("10.9.9.9")
	r.promoted()
	p := quarantineParams
	p.NewMaster.Host = "db2.example"
	decode[proto.QuarantineResult](t, r.quarantine(p))
	c := r.peer.creds[len(r.peer.creds)-1]
	if c.Host != "10.9.9.9" || c.ServerName != "db2.example" {
		t.Errorf("new master read at %q (name %q), want 10.9.9.9 (db2.example)", c.Host, c.ServerName)
	}
	// The pin keeps the name; the verified address is recorded beside it.
	wantQuarantined(t, r.db, r.dir, "db2.example:3306")
	if got := pinFile(t, r.dir, mysql.SourceAddrFile); got != "10.9.9.9" {
		t.Errorf("source-addr = %q, want 10.9.9.9", got)
	}
	if src := r.ev.payload(t, 0)["new_source"]; src != "db2.example:3306" {
		t.Errorf("quarantined event new_source = %q", src)
	}
	if res := decode[proto.QuarantineResult](t, r.quarantine(p)); !res.Already {
		t.Errorf("quarantine again under the name = %+v, want already", res)
	}
	// The agent's own later reads of the pinned source dial that address.
	var probed []string
	t.Cleanup(mysql.SetProbePeer(func(_ context.Context, host string, _ int, _ time.Duration) error {
		probed = append(probed, host)
		return nil
	}))
	ps := decode[proto.ProbeSourceResult](t, r.dispatch("mysql.probeSource", nil))
	if ps.Host != "db2.example" || len(probed) != 1 || probed[0] != "10.9.9.9" {
		t.Errorf("probeSource = %+v, probed %v; want db2.example probed at 10.9.9.9", ps, probed)
	}
	// A rebuild from the new master is matched by its name.
	if err := r.d.Pins().Allow("mysql.dumpFrom", proto.SnapshotParams{Host: "db2.example", Port: 3306}); err != nil {
		t.Errorf("dumpFrom the pinned name refused: %v", err)
	}
	// set-source (root) moves the pin and drops the recorded address.
	if err := r.d.Pins().SetSource("10.0.0.7", 3306); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, r.dir, mysql.SourceAddrFile); got != "" {
		t.Errorf("source-addr after set-source = %q", got)
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

// TestQuarantineGuardDefersToRebuildLoad: while a rebuild load from the
// pinned source is marked, the guard does not put read-only back after a
// restart (it logs its deferral once); once the mark is cleared, the next
// tick and the next restart put it back. A mark whose source is no longer
// the source pin is not honoured, and none is set toward another source.
func TestQuarantineGuardDefersToRebuildLoad(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	const src = "10.0.0.1:3306"
	dir := pinDir(t, "quarantined", src)
	var logs bytes.Buffer
	ev := &eventLog{}
	d := &mysql.Driver{Host: inst.Host(), PinDir: dir, Events: ev, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	ev.flush = d.FlushEvents
	d.Register(commands.NewRegistry())
	// The relock the guard reports while running (a late entry that is no
	// start) is not what this test counts: only real starts.
	starts := func() []proto.QuarantineStartEvent {
		var out []proto.QuarantineStartEvent
		for _, p := range startEvents(t, ev) {
			if p.While == "" {
				out = append(out, p)
			}
		}
		return out
	}
	tick := func() {
		t.Helper()
		if err := d.GuardTick(context.Background(), time.Now()); err != nil {
			t.Fatalf("GuardTick: %v", err)
		}
	}
	// What the load does after its restart: super_read_only off by SET
	// GLOBAL (a lift never turns read_only off, spec 5.2).
	restartAndLift := func() {
		t.Helper()
		inst.Restart(t)
		execAll(t, db, "SET GLOBAL super_read_only = OFF")
	}
	tick()
	// The first tick reports how MySQL started (this agent's start).
	if evs := ev.all(); len(evs) != 1 || evs[0].Type != proto.EventQuarantineStart {
		t.Fatalf("events of the first tick = %+v, want one %s", evs, proto.EventQuarantineStart)
	}

	if _, ok := d.MarkRebuildLoad("10.0.0.2", 3306); ok {
		t.Fatal("a rebuild mark toward another source was set")
	}
	if d.RebuildLoadMarked() {
		t.Fatal("marked after a refused mark")
	}
	clear, ok := d.MarkRebuildLoad("10.0.0.1", 3306)
	if !ok {
		t.Fatal("no rebuild mark toward the pinned source")
	}
	restartAndLift()
	tick()
	tick()
	if sro, _ := readOnly(t, db); sro {
		t.Fatalf("guard put super_read_only back during the load")
	}
	if n := strings.Count(logs.String(), "quarantine guard deferring: a rebuild load from the pinned source is running"); n != 1 {
		t.Errorf("deferral logged %d times, want once:\n%s", n, logs.String())
	}
	if n := len(ev.all()) - 1; n != 0 {
		t.Errorf("guard reported %d events during the load", n)
	}

	// The load is over (its restore failed to put read-only back here):
	// the next tick does.
	clear()
	clear()
	tick()
	if sro, ro := readOnly(t, db); !sro || !ro {
		t.Fatalf("guard after the load: super_read_only=%v read_only=%v, want both on", sro, ro)
	}
	// The load's restart is not a start of the quarantined server.
	if got := starts(); len(got) != 1 {
		t.Errorf("start events after the load = %+v, want only the agent's start", got)
	}
	// And the next restart is a transition again, reported.
	restartAndLift()
	tick()
	if sro, ro := readOnly(t, db); !sro || !ro {
		t.Fatalf("guard after a restart without a load: super_read_only=%v read_only=%v", sro, ro)
	}
	if got := starts(); len(got) != 2 || got[1].Result != proto.QuarantineStartLate {
		t.Errorf("start events after a restart without a load = %+v, want a second, late", got)
	}

	// A load the guard never saw (no tick while marked) whose restore
	// failed: the tick after it reads the flags again.
	clear, ok = d.MarkRebuildLoad("10.0.0.1", 3306)
	if !ok {
		t.Fatal("no rebuild mark for the unseen load")
	}
	execAll(t, db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	clear()
	tick()
	if sro, ro := readOnly(t, db); !sro || !ro {
		t.Fatalf("guard after an unseen load: super_read_only=%v read_only=%v, want both on", sro, ro)
	}
	if got := starts(); len(got) != 2 {
		t.Errorf("start events after an unseen load = %+v, want still two", got)
	}

	// A mark is honoured only while its source is the source pin.
	clear, ok = d.MarkRebuildLoad("10.0.0.1", 3306)
	if !ok {
		t.Fatal("no second rebuild mark")
	}
	defer clear()
	if err := d.Pins().SetSource("10.0.0.7", 3306); err != nil {
		t.Fatal(err)
	}
	restartAndLift()
	tick()
	if sro, ro := readOnly(t, db); !sro || !ro {
		t.Fatalf("guard honoured a mark for a source no longer pinned: super_read_only=%v read_only=%v", sro, ro)
	}
}

// TestRebuildConfigureAndCloneKeepReadOnly: the rebuild of a quarantined
// server keeps it read-only across its configure{replica} restart, and a
// clone from the pinned source PERSISTs read-only again after its own
// restart (here with the persisted values gone, as if CLONE had dropped
// mysqld-auto.cnf). The clone then lets replication toward the source end
// the quarantine.
func TestRebuildConfigureAndCloneKeepReadOnly(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	target := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	mdb := rootDB(t, master)
	fillMaster(t, mdb)
	replUser(t, master)
	execAll(t, rootDB(t, target), "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")

	dir := pinDir(t, "quarantined", "127.0.0.1:"+strconv.Itoa(master.Port))
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	jev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(jev, slog.New(slog.DiscardHandler))
	rig := &snapshotRig{reg: commands.NewRegistry(), runner: runner, events: jev}
	d := &mysql.Driver{Host: target.HostWith(creds), Creds: creds, Jobs: runner, PinDir: dir}
	d.Register(rig.reg)
	r := &pinRig{t: t, reg: rig.reg, d: d}
	approveRebuild(t, dir, "127.0.0.1:"+strconv.Itoa(master.Port))
	locked := func(when string) {
		t.Helper()
		if sro, ro := readOnly(t, rootDB(t, target)); !sro || !ro {
			t.Fatalf("%s: super_read_only=%v read_only=%v, want both ON", when, sro, ro)
		}
	}

	decode[proto.ConfigureResult](t, r.dispatch("mysql.configure", proto.ConfigureParams{Role: "replica"}))
	decode[proto.RestartResult](t, r.dispatch("mysql.restart", nil))
	locked("after the rebuild's configure{replica} restart")

	execAll(t, rootDB(t, target), "RESET PERSIST read_only", "RESET PERSIST super_read_only")
	res := snapshotResult(t, rig.run(t, "mysql.cloneFrom", cloneParams(master)))
	if res.Method != "clone" {
		t.Fatalf("clone result = %+v", res)
	}
	tdb := rootDB(t, target)
	locked("after the clone")
	if s, ro := persisted(t, tdb, "super_read_only"), persisted(t, tdb, "read_only"); s != "ON" || ro != "ON" {
		t.Errorf("after the clone: persisted super_read_only=%q read_only=%q, want ON", s, ro)
	}
	if d.RebuildLoadMarked() {
		t.Error("the rebuild-load mark outlived the clone job")
	}
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(master)))
	if role := pinFile(t, dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after the clone and configureReplication = %q, want replica", role)
	}
}

// relockLog records the driver's relock reports (Driver.relocked).
type relockLog struct {
	mu     sync.Mutex
	whiles []string
}

func watchRelocks(d *mysql.Driver) *relockLog {
	l := &relockLog{}
	d.SetRelocked(func(_ mysql.Host, _ time.Time, while string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.whiles = append(l.whiles, while)
	})
	return l
}

// got is the reports so far, comma-joined.
func (l *relockLog) got() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.whiles, ",")
}

// flagRig is a quarantined server, read_only and super_read_only PERSISTed
// ON, and a driver over it with its own lift lock file, its events and
// relock reports recorded, and a clock of its own that each round moves on
// by monitor.GuardEvery.
type flagRig struct {
	t    *testing.T
	inst *testmysql.Instance
	root *sql.DB
	dir  string
	reg  *commands.Registry
	d    *mysql.Driver
	ev   *eventLog
	rl   *relockLog
	now  time.Time
	// lockAccount: refuseLogin locks the account instead of changing its
	// password.
	lockAccount bool
}

// newFlagRig builds the rig; creds, when not nil, is the stored account
// (else root). source is the source pin.
func newFlagRig(t *testing.T, creds *memCreds, source string) *flagRig {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	root := rootDB(t, inst)
	execAll(t, root, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r := &flagRig{t: t, inst: inst, root: root, dir: pinDir(t, "quarantined", source), ev: &eventLog{}, now: time.Now()}
	r.setDriver(creds)
	return r
}

// setDriver makes a new driver (a new agent run) over the rig's server.
func (r *flagRig) setDriver(creds *memCreds) {
	h := r.inst.Host()
	r.d = &mysql.Driver{PinDir: r.dir, Events: r.ev, LiftLock: lockPath(r.t),
		SeenPath: filepath.Join(r.t.TempDir(), "replicas-seen")}
	if creds != nil {
		h = r.inst.HostWith(creds)
		r.d.Creds = creds
	}
	r.d.Host = h
	r.ev.flush = r.d.FlushEvents
	r.reg = commands.NewRegistry()
	r.d.Register(r.reg)
	r.rl = watchRelocks(r.d)
}

// tick runs one guard round, GuardEvery after the last one.
func (r *flagRig) tick() error {
	r.now = r.now.Add(5 * time.Second)
	return r.d.GuardTick(context.Background(), r.now)
}

// mustTick is tick that fails the test on an error.
func (r *flagRig) mustTick() {
	r.t.Helper()
	if err := r.tick(); err != nil {
		r.t.Fatalf("GuardTick: %v", err)
	}
}

// wantFlags checks read_only and super_read_only ("1 0": read_only on).
func (r *flagRig) wantFlags(when, want string) {
	r.t.Helper()
	if got := flags(r.t, r.root); got != want {
		r.t.Errorf("%s: flags = %s, want %s", when, got, want)
	}
}

// wantRelocks checks the relock reports so far (comma-joined).
func (r *flagRig) wantRelocks(when, want string) {
	r.t.Helper()
	if got := r.rl.got(); got != want {
		r.t.Errorf("%s: relock reports = %q, want %q", when, got, want)
	}
}

// lift opens read-only through the driver's gate, as an agent job does.
func (r *flagRig) lift() *mysql.LiftHold {
	r.t.Helper()
	hold, err := r.d.LiftGate().Begin(context.Background(), r.root, func(string, string, ...any) {})
	if err != nil {
		r.t.Fatalf("lift: %v", err)
	}
	return hold
}

// TestGuardRelocksWhileUp: a flag switched off from outside while MySQL
// keeps running is put back on the next round, PERSISTed, and reported
// once as relocked while running.
func TestGuardRelocksWhileUp(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	r.wantRelocks("first round", "")
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	r.mustTick()
	r.wantFlags("after the round", "1 1")
	if s, ro := persisted(t, r.root, "super_read_only"), persisted(t, r.root, "read_only"); s != "ON" || ro != "ON" {
		t.Errorf("persisted super_read_only=%q read_only=%q, want ON", s, ro)
	}
	r.wantRelocks("after the round", proto.QuarantineWhileRunning)
	r.mustTick()
	r.wantRelocks("a quiet round after", proto.QuarantineWhileRunning)
}

// TestGuardNormalRoundDoesNotTouchLockFile: a transition round takes the
// lift lock; a round with both flags on and no transition does not even
// open the lock file.
func TestGuardNormalRoundDoesNotTouchLockFile(t *testing.T) {
	var opens atomic.Int32
	defer mysql.SetGuardLockOpener(func(path string, replace bool) (*os.File, error) {
		opens.Add(1)
		return mysql.OpenLiftLock(path, replace)
	})()
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	if opens.Load() == 0 {
		t.Fatal("the transition round did not take the lift lock")
	}
	opens.Store(0)
	for range 3 {
		r.mustTick()
	}
	if n := opens.Load(); n != 0 {
		t.Errorf("normal rounds opened the lift lock file %d times", n)
	}
}

// TestGuardLeavesRegisteredLiftAlone: while a lift of the driver's gate is
// open, the guard, on its transition round too, leaves super_read_only
// off and reports nothing.
func TestGuardLeavesRegisteredLiftAlone(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	hold := r.lift()
	r.mustTick()
	r.mustTick()
	r.wantFlags("during the lift", "1 0")
	r.wantRelocks("during the lift", "")
	if evs := r.ev.all(); len(evs) != 0 {
		t.Errorf("events during the lift = %+v", evs)
	}
	if err := hold.End(); err != nil {
		t.Fatal(err)
	}
	r.wantFlags("after the lift", "1 1")
	r.mustTick()
	r.wantRelocks("after the lift", "")
}

// TestGuardKeepsReadOnlyDuringLift: read_only switched off from outside
// during a lift is put back (super_read_only stays off for the lift),
// reported as relocked while running and recorded as writable since the
// load.
func TestGuardKeepsReadOnlyDuringLift(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	hold := r.lift()
	defer hold.End()
	execAll(t, r.root, "SET GLOBAL read_only = OFF")
	r.wantFlags("before the round", "0 0")
	r.mustTick()
	r.wantFlags("after the round", "1 0")
	r.wantRelocks("after the round", proto.QuarantineWhileRunning)
	if !r.d.WritableAfterLoad() {
		t.Error("writable since the load not recorded")
	}
}

// TestGuardLeavesLongLiftAlone: a lift open for twelve rounds is left
// alone and nothing is reported.
func TestGuardLeavesLongLiftAlone(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	start := len(r.ev.all())
	hold := r.lift()
	for range 12 {
		r.mustTick()
	}
	r.wantFlags("after twelve rounds", "1 0")
	r.wantRelocks("after twelve rounds", "")
	if n := len(r.ev.all()) - start; n != 0 {
		t.Errorf("%d events during the lift", n)
	}
	if r.d.WritableAfterLoad() {
		t.Error("a legitimate lift recorded as writable since the load")
	}
	if err := hold.End(); err != nil {
		t.Fatal(err)
	}
}

// TestGuardSkipsWhenCLIHoldsLock: while another descriptor (a dbpanel
// agent command) holds the lift lock, super_read_only off with read_only
// on is left alone and nothing is reported.
func TestGuardSkipsWhenCLIHoldsLock(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	release, err := mysql.AcquireLiftLock(r.d.LiftLockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	for range 3 {
		r.mustTick()
	}
	r.wantFlags("while the command holds the lock", "1 0")
	r.wantRelocks("while the command holds the lock", "")
}

// failingGuardOpener makes tryGuard's open of the lift lock file fail with
// cause, for the rest of the test.
func failingGuardOpener(t *testing.T, cause error) {
	t.Cleanup(mysql.SetGuardLockOpener(func(string, bool) (*os.File, error) { return nil, cause }))
}

// lockedBuffer is a bytes.Buffer safe for a logger and a test to share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestGuardRelocksWhenLockFileCannotBeOpened: a lift lock file the guard
// cannot open, with no lift of this agent open, is no legitimate lift: the
// guard puts super_read_only back as with the lock free, reports the
// relock, and logs the open error at most once per ten minutes. With a lift
// of this agent open the guard leaves the server alone as before.
func TestGuardRelocksWhenLockFileCannotBeOpened(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	var logs lockedBuffer
	r.d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	r.mustTick()
	failingGuardOpener(t, errors.New("no space left on device"))
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	r.mustTick()
	r.wantFlags("after the round", "1 1")
	r.wantRelocks("after the round", proto.QuarantineWhileRunning)
	for range 3 {
		execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
		r.mustTick()
	}
	r.wantFlags("three more rounds", "1 1")
	const logged = "quarantine guard cannot open the lift lock file"
	if n := strings.Count(logs.String(), logged); n != 1 {
		t.Errorf("open error logged %d times in four rounds, want 1; log:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "no space left on device") {
		t.Errorf("the log does not carry the cause:\n%s", logs.String())
	}
	r.now = r.now.Add(10 * time.Minute)
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	r.mustTick()
	if n := strings.Count(logs.String(), logged); n != 2 {
		t.Errorf("open error logged %d times after ten minutes, want 2", n)
	}

	// A lift of this agent is open: left alone.
	hold := r.lift()
	r.mustTick()
	r.mustTick()
	r.wantFlags("during this agent's lift", "1 0")
	if err := hold.End(); err != nil {
		t.Fatal(err)
	}
}

// TestGuardOpenFailureKeepsUnverifiedStreak: a round that relocked without
// being able to open the lift lock file does not end a streak of rounds
// that could not check; the third such round is reported as unverified.
func TestGuardOpenFailureKeepsUnverifiedStreak(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	creds := r.guardAdmin()
	r.mustTick()
	failingGuardOpener(t, errors.New("too many open files"))
	r.refuseLogin(true, creds)
	r.mustTick()
	r.mustTick()
	r.refuseLogin(false, creds)
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	r.mustTick()
	r.wantFlags("the round that logged in", "1 1")
	r.wantRelocks("two refused rounds and one relock", "")
	r.refuseLogin(true, creds)
	r.mustTick()
	r.wantRelocks("third round that could not check", proto.QuarantineWhileUnverified)
}

// TestGuardTransitionDuringUnmarkedLift: a lift without a load mark
// (mysql.dropDatabases, held behind another session's metadata lock) is
// running at the guard's transition round: the guard PERSISTs nothing and
// reports nothing.
func TestGuardTransitionDuringUnmarkedLift(t *testing.T) {
	const src = "10.0.0.1:3306"
	r := newFlagRig(t, nil, src)
	approveRebuild(t, r.dir, src)
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF", "CREATE DATABASE lama", "CREATE TABLE lama.t (id INT)",
		"SET GLOBAL super_read_only = ON", "RESET PERSIST read_only", "RESET PERSIST super_read_only")
	// Another session holds a metadata lock on lama.t: DROP DATABASE waits.
	ctx := context.Background()
	blocker, err := r.root.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	for _, q := range []string{"BEGIN", "SELECT COUNT(*) FROM lama.t"} {
		if _, err := blocker.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan proto.Response, 1)
	params := mustJSON(t, proto.DropDatabasesParams{Databases: []string{"lama"}})
	go func() {
		done <- r.reg.Dispatch(ctx, proto.Request{ID: "1", Cmd: "mysql.dropDatabases", Params: params})
	}()
	deadline := time.Now().Add(30 * time.Second)
	for count(t, r.root, "SELECT COUNT(*) FROM information_schema.processlist WHERE info LIKE 'DROP DATABASE%'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("DROP DATABASE never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.wantFlags("during the drop", "1 0")
	r.mustTick()
	r.wantFlags("after the transition round", "1 0")
	if s, ro := persisted(t, r.root, "super_read_only"), persisted(t, r.root, "read_only"); s != "" || ro != "" {
		t.Errorf("persisted during the lift: super_read_only=%q read_only=%q, want none", s, ro)
	}
	r.wantRelocks("after the transition round", "")
	if evs := r.ev.all(); len(evs) != 0 {
		t.Errorf("events during the lift = %+v", evs)
	}
	if _, err := blocker.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if resp := <-done; !resp.OK {
		t.Fatalf("dropDatabases = %+v", resp)
	}
	r.wantFlags("after the drop", "1 1")
}

// TestGuardBoundedBehindTableLock (X4): with super_read_only off and a
// session that may write holding LOCK TABLES … WRITE, the guard's statement
// ends with error 1205 after its lock_wait_timeout, changes nothing and
// leaves nothing waiting on the server; the round returns. After UNLOCK
// TABLES the next round locks the server and reports it; three rounds in
// a row behind the lock are reported as unverified.
func TestGuardBoundedBehindTableLock(t *testing.T) {
	defer mysql.SetGuardLockWaitTimeout(1)()
	r := newFlagRig(t, nil, guardCandidateKey)
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF", "CREATE DATABASE app", "CREATE TABLE app.t (id INT)",
		"SET GLOBAL super_read_only = ON")
	r.mustTick()
	ctx := context.Background()
	holder, err := r.root.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	lock := func() {
		t.Helper()
		for _, q := range []string{"SET GLOBAL super_read_only = OFF", "LOCK TABLES app.t WRITE", "INSERT INTO app.t VALUES (1)"} {
			if _, err := holder.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	unlock := func() {
		t.Helper()
		if _, err := holder.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			t.Fatal(err)
		}
	}
	waiting := func() int {
		return count(t, r.root, "SELECT COUNT(*) FROM information_schema.processlist WHERE info LIKE 'SET PERSIST%'")
	}

	lock()
	began := time.Now()
	err = r.tick()
	took := time.Since(began)
	if err == nil || !strings.Contains(err.Error(), "1205") {
		t.Errorf("round behind the table lock = %v, want error 1205", err)
	}
	if took < 900*time.Millisecond || took > 8*time.Second {
		t.Errorf("round behind the table lock took %v, want about the 1 s bound", took)
	}
	r.wantFlags("behind the table lock", "1 0")
	if n := waiting(); n != 0 {
		t.Errorf("%d guard statements still waiting on the server", n)
	}
	r.wantRelocks("behind the table lock", "")
	unlock()
	r.mustTick()
	r.wantFlags("after UNLOCK TABLES", "1 1")
	r.wantRelocks("after UNLOCK TABLES", proto.QuarantineWhileRunning)

	lock()
	defer unlock()
	for range 3 {
		r.tick()
	}
	r.wantFlags("three rounds behind the table lock", "1 0")
	r.wantRelocks("three rounds behind the table lock", proto.QuarantineWhileRunning+","+proto.QuarantineWhileUnverified)
	if n := waiting(); n != 0 {
		t.Errorf("%d guard statements still waiting on the server", n)
	}
}

// guardAdmin is a stored admin account adm@localhost (ALL, minus without)
// on r's server, the driver using it.
func (r *flagRig) guardAdmin(without ...string) *memCreds {
	r.t.Helper()
	execAll(r.t, r.root, "SET GLOBAL super_read_only = OFF")
	creds := noSuperAdmin(r.t, r.inst, without...)
	execAll(r.t, r.root, "SET GLOBAL super_read_only = ON")
	r.setDriver(creds)
	return creds
}

// refuseLogin makes the server refuse adm's login (on) or accept it again:
// by a changed password (1045), or, with r.lockAccount, by ACCOUNT LOCK
// (3118).
func (r *flagRig) refuseLogin(on bool, creds *memCreds) {
	r.t.Helper()
	q := "ALTER USER adm@localhost IDENTIFIED BY " + sqlLiteral(creds.p.Password) + " ACCOUNT UNLOCK"
	switch {
	case on && r.lockAccount:
		q = "ALTER USER adm@localhost ACCOUNT LOCK"
	case on:
		q = "ALTER USER adm@localhost IDENTIFIED BY " + sqlLiteral("lain")
	}
	execAll(r.t, r.root, "SET GLOBAL super_read_only = OFF", q, "SET GLOBAL super_read_only = ON")
}

// TestGuardReportsWhenItCannotLogIn: three logins in a row that the server
// refuses (the stored account locked, or its password changed), with no
// load mark and the lift lock free, are reported once as unverified and
// recorded as writable since the load; again no sooner than ten minutes
// later. Two refused rounds and then a login report nothing.
func TestGuardReportsWhenItCannotLogIn(t *testing.T) {
	for _, c := range []struct {
		name string
		lock bool
	}{{"account locked", true}, {"password changed", false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newFlagRig(t, nil, guardCandidateKey)
			r.lockAccount = c.lock
			creds := r.guardAdmin()
			r.mustTick()
			r.refuseLogin(true, creds)
			r.mustTick()
			r.mustTick()
			r.wantRelocks("two refused rounds", "")
			r.mustTick()
			r.wantRelocks("three refused rounds", proto.QuarantineWhileUnverified)
			if !r.d.WritableAfterLoad() {
				t.Error("writable since the load not recorded")
			}
			for range 5 {
				r.mustTick()
			}
			r.wantRelocks("refused rounds within ten minutes", proto.QuarantineWhileUnverified)
			r.now = r.now.Add(10 * time.Minute)
			r.mustTick()
			r.wantRelocks("ten minutes later", proto.QuarantineWhileUnverified+","+proto.QuarantineWhileUnverified)

			// Two refused rounds, then a login: nothing.
			r.setDriver(creds)
			r.refuseLogin(false, creds)
			r.mustTick()
			r.refuseLogin(true, creds)
			r.mustTick()
			r.mustTick()
			r.refuseLogin(false, creds)
			r.mustTick()
			r.mustTick()
			r.wantRelocks("two refused rounds then a login", "")
			if r.d.WritableAfterLoad() {
				t.Error("two refused rounds recorded as writable since the load")
			}
		})
	}
}

// TestGuardQuietWhileMySQLDown: MySQL not answering at all is no round
// the guard could not check: no unverified report, nothing recorded.
func TestGuardQuietWhileMySQLDown(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.mustTick()
	r.inst.Stop(t)
	for range 5 {
		r.mustTick()
	}
	r.wantRelocks("rounds with MySQL down", "")
	if r.d.WritableAfterLoad() {
		t.Error("rounds with MySQL down recorded as writable since the load")
	}
	r.inst.Restart(t)
	r.mustTick()
	r.wantRelocks("MySQL back", "")
}

// TestGuardReportsWhenRelockFails: a flag off and an agent account that
// cannot set it (no SYSTEM_VARIABLES_ADMIN): after three rounds the guard
// reports unverified.
func TestGuardReportsWhenRelockFails(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.guardAdmin("SUPER", "SYSTEM_VARIABLES_ADMIN")
	r.tick()
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF")
	r.tick()
	r.tick()
	r.wantRelocks("two rounds", "")
	r.tick()
	r.wantRelocks("three rounds", proto.QuarantineWhileUnverified)
	r.wantFlags("three rounds", "1 0")
	if !r.d.WritableAfterLoad() {
		t.Error("writable since the load not recorded")
	}
}

// TestGuardUnverifiedNotDuringLift: refused logins while another
// descriptor holds the lift lock are not reported.
func TestGuardUnverifiedNotDuringLift(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	creds := r.guardAdmin()
	r.mustTick()
	release, err := mysql.AcquireLiftLock(r.d.LiftLockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r.refuseLogin(true, creds)
	for range 4 {
		r.mustTick()
	}
	r.wantRelocks("refused rounds during a lift", "")
	if r.d.WritableAfterLoad() {
		t.Error("refused rounds during a lift recorded as writable since the load")
	}
}

// TestFailedPersistAfterFlagsOnCostsNothing: on a transition round both
// flags read on and SET PERSIST fails: nothing is recorded or reported,
// and the next round tries again.
func TestFailedPersistAfterFlagsOnCostsNothing(t *testing.T) {
	r := newFlagRig(t, nil, guardCandidateKey)
	r.guardAdmin("SUPER", "SYSTEM_VARIABLES_ADMIN")
	execAll(t, r.root, "RESET PERSIST read_only", "RESET PERSIST super_read_only")
	r.wantFlags("before", "1 1")
	if err := r.tick(); err == nil {
		t.Error("round with a failing SET PERSIST returned no error")
	}
	if r.d.WritableAfterLoad() {
		t.Error("a failed PERSIST with both flags on recorded as writable since the load")
	}
	r.wantRelocks("failed PERSIST", "")
	if evs := r.ev.all(); len(evs) != 0 {
		t.Errorf("events after a failed PERSIST = %+v", evs)
	}
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF", "GRANT SYSTEM_VARIABLES_ADMIN ON *.* TO adm@localhost",
		"SET GLOBAL super_read_only = ON")
	r.mustTick()
	if s, ro := persisted(t, r.root, "super_read_only"), persisted(t, r.root, "read_only"); s != "ON" || ro != "ON" {
		t.Errorf("next round: persisted super_read_only=%q read_only=%q, want ON", s, ro)
	}
	r.wantRelocks("next round", "")
	if st := startEvents(t, r.ev); len(st) != 1 || st[0].Result != proto.QuarantineStartOK {
		t.Errorf("start events = %+v, want one ok", st)
	}
}

// adminReplacedRig is a quarantined server rebuilt with a full dump from
// its pinned source that replaced the stored admin account (AdminReplaced),
// the driver's guard watched.
func adminReplacedRig(t *testing.T) (*flagRig, *memCreds) {
	t.Helper()
	r, creds, d := fullDumpRebuildRig(t, nil)
	if res := snapshotResult(t, d); !res.AdminReplaced {
		t.Fatalf("result = %+v, want AdminReplaced", res)
	}
	return r, creds
}

// fullDumpRebuildRig is adminReplacedRig with the driver's host going
// through hook (nil: none); it returns the full dump's outcome unchecked.
func fullDumpRebuildRig(t *testing.T, hook *hookHost) (*flagRig, *memCreds, proto.JobDone) {
	t.Helper()
	master := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	dumpUser(t, mdb)
	execAll(t, mdb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)", "INSERT INTO toko.t VALUES (1)")
	src := "127.0.0.1:" + strconv.Itoa(master.Port)
	r := newFlagRig(t, nil, src)
	creds := r.guardAdmin("SUPER")
	jev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(jev, slog.New(slog.DiscardHandler))
	var h mysql.Host = r.inst.HostWith(creds)
	if hook != nil {
		hook.Host = h
		h = hook
	}
	r.d = &mysql.Driver{Host: h, Creds: creds, Jobs: runner, PinDir: r.dir, Events: r.ev,
		LiftLock: lockPath(t), SeenPath: filepath.Join(t.TempDir(), "replicas-seen")}
	r.ev.flush = r.d.FlushEvents
	r.reg = commands.NewRegistry()
	r.d.Register(r.reg)
	r.rl = watchRelocks(r.d)
	approveRebuild(t, r.dir, src)
	r.mustTick()
	snap := &snapshotRig{reg: r.reg, runner: runner, events: jev}
	return r, creds, snap.run(t, "mysql.dumpFrom", dumpParams(master))
}

// TestSecondListFailureAfterAccountsReplaced: a full dump that replaced
// the stored admin account, whose listing of the source fails three times
// after the load, fails with T11b and keeps no load record; the guard's
// refused logins are expected as after a successful load (no unverified
// report) until it has logged in once.
func TestSecondListFailureAfterAccountsReplaced(t *testing.T) {
	hook := &hookHost{fail: func(n int) bool { return n > 1 }}
	r, creds, d := fullDumpRebuildRig(t, hook)
	const t11b = `salinan selesai dimuat, tetapi daftar database master baru tidak bisa dibaca sesudahnya, jadi salinan ` +
		`ini tidak dihitung; salinan itu juga mengganti akun MySQL server ini: jalankan "Hubungkan MySQL" untuk server ` +
		`ini dengan akun admin MySQL master, lalu tekan "Ulangi dari tahap ini"`
	if d.OK || d.Error != t11b {
		t.Errorf("dumpFrom{all} = %+v\nwant error %q", d, t11b)
	}
	if n := hook.listings(); n != 4 {
		t.Errorf("source listings = %d, want 4", n)
	}
	host, port, err := mysql.ParseSource(pinFile(t, r.dir, mysql.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	if r.d.LoadedFrom(host, port) {
		t.Error("a load whose second listing failed was recorded")
	}
	r.wantFlags("after the failed listing", "1 1")
	for range 4 {
		r.mustTick()
	}
	r.wantRelocks("refused rounds after the replaced accounts", "")
	// "Hubungkan MySQL": the account works again; refused logins count
	// once it has logged in.
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF", "CREATE USER adm@localhost IDENTIFIED BY "+sqlLiteral(creds.p.Password),
		"GRANT ALL PRIVILEGES ON *.* TO adm@localhost", "SET GLOBAL super_read_only = ON")
	r.mustTick()
	r.refuseLogin(true, creds)
	for range 3 {
		r.mustTick()
	}
	r.wantRelocks("three refused rounds after a login", proto.QuarantineWhileUnverified)
}

// TestFullDumpRebuildLeavesNoRelock: after a full-dump rebuild that
// replaced the admin account, twelve guard rounds (refused logins) leave
// the flags 1 1 and report nothing.
func TestFullDumpRebuildLeavesNoRelock(t *testing.T) {
	r, _ := adminReplacedRig(t)
	for range 12 {
		r.mustTick()
	}
	r.wantFlags("after twelve rounds", "1 1")
	r.wantRelocks("after twelve rounds", "")
	if r.d.WritableAfterLoad() {
		t.Error("the rebuild recorded as writable since the load")
	}
}

// TestGuardQuietWhileAdminReplaced: refused logins after a load that
// replaced the accounts are expected and not reported; the first login
// that works ends the exception.
func TestGuardQuietWhileAdminReplaced(t *testing.T) {
	r, creds := adminReplacedRig(t)
	for range 12 {
		r.mustTick()
	}
	r.wantRelocks("twelve refused rounds", "")
	if r.d.WritableAfterLoad() {
		t.Error("refused rounds after the load recorded as writable since the load")
	}
	// "Hubungkan MySQL": the account works again.
	execAll(t, r.root, "SET GLOBAL super_read_only = OFF", "CREATE USER adm@localhost IDENTIFIED BY "+sqlLiteral(creds.p.Password),
		"GRANT ALL PRIVILEGES ON *.* TO adm@localhost", "SET GLOBAL super_read_only = ON")
	r.mustTick()
	r.refuseLogin(true, creds)
	for range 3 {
		r.mustTick()
	}
	r.wantRelocks("three refused rounds after a login", proto.QuarantineWhileUnverified)
}
