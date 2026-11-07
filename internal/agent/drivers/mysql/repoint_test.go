package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// repointRig is an old master A with two replicas: B, the promotion
// candidate, and C, pinned replica under A, whose driver runs repoint.
type repointRig struct {
	*pinRig
	a, b, c       *testmysql.Instance
	adb, bdb, cdb *sql.DB
	dir, source   string
	cUUID         string
}

func newRepointRig(t *testing.T, beforeFollow func(b *testmysql.Instance)) *repointRig {
	t.Helper()
	a := testmysql.Start(t, testmysql.Options{})
	b := testmysql.Start(t, testmysql.Options{})
	c := testmysql.Start(t, testmysql.Options{})
	if beforeFollow != nil {
		beforeFollow(b)
	}
	if err := mysql.CreateReplUser(context.Background(), a.Host(),
		proto.ReplUserParams{User: "dbpanel_repl", FromHost: "%", Password: replPassword}); err != nil {
		t.Fatal(err)
	}
	r := &repointRig{a: a, b: b, c: c, adb: rootDB(t, a), bdb: rootDB(t, b), cdb: rootDB(t, c),
		source: "127.0.0.1:" + strconv.Itoa(a.Port)}
	execAll(t, r.adb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)", "INSERT INTO toko.t VALUES (1)")
	follow(t, a, b)
	follow(t, a, c)
	synced(t, a, b)
	synced(t, a, c)
	execAll(t, r.cdb, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r.dir = pinDir(t, "replica", r.source)
	r.pinRig = newPinRig(t, c.Host(), r.dir)
	if err := r.cdb.QueryRow("SELECT @@global.server_uuid").Scan(&r.cUUID); err != nil {
		t.Fatal(err)
	}
	return r
}

// lockA makes the old master read-only, as lockForPromote does.
func (r *repointRig) lockA() { execAll(r.t, r.adb, "SET GLOBAL super_read_only = ON") }

// promoteB turns the candidate into a writable master with a
// transaction of its own.
func (r *repointRig) promoteB() {
	execAll(r.t, r.bdb, "STOP REPLICA", "RESET REPLICA ALL", "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF",
		"INSERT INTO toko.t VALUES (2)")
}

func (r *repointRig) params() proto.RepointParams {
	return proto.RepointParams{NewMaster: proto.PeerCred{Host: "127.0.0.1", Port: r.b.Port, User: "dbpanel_repl", Password: replPassword}}
}

func (r *repointRig) bKey() string { return "127.0.0.1:" + strconv.Itoa(r.b.Port) }

// channelSource is the host:port C's replication channel points at.
func (r *repointRig) channelSource() string {
	r.t.Helper()
	var host string
	var port int
	if err := r.cdb.QueryRow(`SELECT HOST, PORT FROM performance_schema.replication_connection_configuration
		WHERE CHANNEL_NAME = ''`).Scan(&host, &port); err != nil {
		r.t.Fatal(err)
	}
	return host + ":" + strconv.Itoa(port)
}

// wantUntouched checks a refused repoint left C on A, pinned so.
func (r *repointRig) wantUntouched(what string) {
	r.t.Helper()
	if got := r.channelSource(); got != r.source {
		r.t.Errorf("%s: channel points at %s, want %s", what, got, r.source)
	}
	if role, src := pinFile(r.t, r.dir, mysql.RoleFile), pinFile(r.t, r.dir, mysql.SourceFile); role != "replica" || src != r.source {
		r.t.Errorf("%s: pins = %q, %q; want replica, %s", what, role, src, r.source)
	}
}

// TestRepointHappyPath: with the old master locked read-only and the
// candidate promoted, the third server is pointed at the new master over
// verified TLS (the CA it did not have is stored), its receiver runs, it
// catches up with the new master, and its source pin moves.
func TestRepointHappyPath(t *testing.T) {
	m := testMySQLCA(t)
	r := newRepointRig(t, func(b *testmysql.Instance) { installCert(t, b, m) })
	r.lockA()
	r.promoteB()
	caPath := filepath.Join(mysql.TLSDir(r.c.Host().Layout()), "mysql-ca.crt")
	if _, err := os.Stat(caPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CA file already there: %v", err)
	}

	p := r.params()
	p.Verify, p.CAPEM, p.Databases = true, string(m.CAPEM()), []string{"toko"}
	resp := r.dispatch("mysql.repoint", p)
	res := decode[proto.RepointResult](t, resp)
	if !res.IORunning || res.NeedsRebuild || res.Errant != "" {
		t.Fatalf("repoint = %+v", res)
	}
	if strings.Contains(string(resp.Result), replPassword) {
		t.Errorf("result shows the password: %s", resp.Result)
	}
	if got := r.channelSource(); got != r.bKey() {
		t.Errorf("channel points at %s, want %s", got, r.bKey())
	}
	var ca, verify string
	if err := r.cdb.QueryRow(`SELECT SSL_CA_FILE, SSL_VERIFY_SERVER_CERTIFICATE
		FROM performance_schema.replication_connection_configuration WHERE CHANNEL_NAME = ''`).Scan(&ca, &verify); err != nil {
		t.Fatal(err)
	}
	if ca != caPath || verify != "YES" {
		t.Errorf("channel CA %q, verify %s; want %s, YES", ca, verify, caPath)
	}
	if got := readFile(t, caPath); string(got) != string(m.CAPEM()) {
		t.Errorf("stored CA differs from the one sent")
	}
	if role, src := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "replica" || src != r.bKey() {
		t.Errorf("pins = %q, %q; want replica, %s", role, src, r.bKey())
	}
	execAll(t, r.bdb, "INSERT INTO toko.t VALUES (3)")
	synced(t, r.b, r.c)
	if n := count(t, r.cdb, "SELECT COUNT(*) FROM toko.t"); n != 3 {
		t.Errorf("rows on the repointed replica = %d, want 3", n)
	}
	if sro, ro := readOnly(t, r.cdb); !sro || !ro {
		t.Errorf("repointed replica: super_read_only=%v read_only=%v", sro, ro)
	}

	// The new master is the pinned source now: nothing to repoint.
	wantRefused(t, "repoint to the pinned source", r.dispatch("mysql.repoint", r.params()), "sudah dipin")
}

// TestRepointErrantReturnsNeedsRebuildAndMovesPin: with the old master
// down (three failed probes; its server_uuid from the replica's
// Source_UUID) a replica holding a transaction the new master lacks is
// not repointed: it answers NeedsRebuild with the errant set, its
// replication stays as it was, and its source pin moves to the new master
// so that the rebuild from it is accepted.
func TestRepointErrantReturnsNeedsRebuildAndMovesPin(t *testing.T) {
	r := newRepointRig(t, nil)
	t.Cleanup(mysql.SetProbeSpacing(10 * time.Millisecond))
	conn, err := r.cdb.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SET GLOBAL super_read_only = OFF", "INSERT INTO toko.t VALUES (99)", "SET GLOBAL super_read_only = ON"} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	conn.Close()
	r.promoteB()
	r.a.Stop(t)

	res := decode[proto.RepointResult](t, r.dispatch("mysql.repoint", r.params()))
	if !res.NeedsRebuild || res.Errant != r.cUUID+":1" || res.IORunning {
		t.Fatalf("repoint = %+v; want needs rebuild with errant %s:1", res, r.cUUID)
	}
	if got := r.channelSource(); got != r.source {
		t.Errorf("channel points at %s, want it left on %s", got, r.source)
	}
	if role, src := pinFile(t, r.dir, mysql.RoleFile), pinFile(t, r.dir, mysql.SourceFile); role != "replica" || src != r.bKey() {
		t.Errorf("pins = %q, %q; want replica, %s", role, src, r.bKey())
	}
	if err := r.d.Pins().Allow("mysql.dumpFrom", proto.SnapshotParams{Host: "127.0.0.1", Port: r.b.Port}); err != nil {
		t.Errorf("rebuild from the new master refused: %v", err)
	}
	if err := r.d.Pins().Allow("mysql.dumpFrom", proto.SnapshotParams{Host: "127.0.0.1", Port: r.a.Port}); err == nil {
		t.Error("rebuild from the old master still allowed")
	}
}

// TestRepointRefusedWhileOldSourceWritable: nothing changes while the
// pinned source answers writable, while the new master is still
// read-only, or when the new master does not hold the old source's
// transactions.
func TestRepointRefusedWhileOldSourceWritable(t *testing.T) {
	r := newRepointRig(t, nil)
	r.promoteB()
	resp := r.dispatch("mysql.repoint", r.params())
	wantRefused(t, "old source writable", resp, r.source, "masih bisa ditulis")
	if strings.Contains(resp.Error.Message, replPassword) {
		t.Errorf("error shows the password: %s", resp.Error.Message)
	}
	r.wantUntouched("old source writable")

	r.lockA()
	execAll(t, r.bdb, "SET GLOBAL super_read_only = ON")
	wantRefused(t, "new master read-only", r.dispatch("mysql.repoint", r.params()), r.bKey(), "read-only")
	r.wantUntouched("new master read-only")
	execAll(t, r.bdb, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")

	// A writable server that never replicated from the old source.
	var aUUID string
	if err := r.adb.QueryRow("SELECT @@global.server_uuid").Scan(&aUUID); err != nil {
		t.Fatal(err)
	}
	stranger := mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-50", ServerUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	t.Cleanup(mysql.SetReadPeer(func(ctx context.Context, c proto.PeerCred, to time.Duration) mysql.PeerState {
		if c.Host == "10.9.9.9" {
			return stranger
		}
		return mysql.ReadPeer(ctx, c, to)
	}))
	p := r.params()
	p.NewMaster.Host = "10.9.9.9"
	wantRefused(t, "new master without the old source's transactions", r.dispatch("mysql.repoint", p),
		"tidak memuat transaksi sumber lama", aUUID)
	r.wantUntouched("stranger new master")

	// With all checks met it goes through.
	res := decode[proto.RepointResult](t, r.dispatch("mysql.repoint", r.params()))
	if !res.IORunning {
		t.Errorf("repoint = %+v", res)
	}
}

// TestRepointPinRules: only a replica with a source pin is repointed.
func TestRepointPinRules(t *testing.T) {
	p := proto.RepointParams{NewMaster: guardCandidate}
	for _, c := range []struct{ role, source, want string }{
		{"master", "", "dipin sebagai master"},
		{"quarantined", "10.0.0.1:3306", "dipin sebagai quarantined"},
		{"", "10.0.0.1:3306", "dipin sebagai unassigned"},
		{"replica", "", "tanpa sumber yang dipin"},
	} {
		r := newPinRig(t, nil, pinDir(t, c.role, c.source))
		wantRefused(t, "repoint on "+c.role, r.dispatch("mysql.repoint", p), c.want)
	}
	r := newPinRig(t, nil, pinDir(t, "replica", "10.0.0.1:3306"))
	wantPastPins(t, "repoint on a pinned replica", r.dispatch("mysql.repoint", p))
	bad := p
	bad.Verify, bad.CAPEM = true, "bukan sertifikat"
	if resp := r.dispatch("mysql.repoint", bad); resp.OK || resp.Error.Code != proto.CodeBadParams {
		t.Errorf("repoint with a bad CA = %+v", resp)
	}
}
