package mysql_test

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
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

	// The guard: nothing to do while read-only. Flags lifted while MySQL
	// stays up (a rebuild's load) are left alone; a MySQL that comes back
	// writable is made read-only again and reported.
	r.tick()
	r.wantEvents(proto.EventQuarantined)
	execAll(t, r.db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	r.tick()
	if sro, ro := readOnly(t, r.db); sro || ro {
		t.Errorf("guard put read-only back while MySQL stayed up: super_read_only=%v read_only=%v", sro, ro)
	}
	r.wantEvents(proto.EventQuarantined)
	execAll(t, r.db, "RESET PERSIST super_read_only", "RESET PERSIST read_only")
	r.inst.Restart(t)
	if sro, _ := readOnly(t, r.db); sro {
		t.Fatal("MySQL came back read-only by itself; the test proves nothing")
	}
	r.tick()
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	r.wantEvents(proto.EventQuarantined, proto.EventQuarantined)
	r.tick()
	r.wantEvents(proto.EventQuarantined, proto.EventQuarantined)
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
	r.wantEvents(proto.EventQuarantined)
	r.tick()
	r.wantEvents(proto.EventQuarantined)
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
	d.Register(rig.reg)
	tick := func() {
		if err := d.GuardTick(context.Background(), time.Now()); err != nil {
			t.Errorf("GuardTick: %v", err)
		}
	}
	tick()

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
	if len(ev.all()) != 0 {
		t.Errorf("guard reported %d events during the rebuild", len(ev.all()))
	}
	t.Logf("guard ticked %d times during the load", n)
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
// the replicas-seen file matched, with its name kept for TLS.
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
	wantQuarantined(t, r.db, r.dir, "db2.example:3306")
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
