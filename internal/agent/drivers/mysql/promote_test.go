package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

// newReplPassword is the password promote gives a replication account it
// has to create.
const newReplPassword = "Baru-repl-pw-1"

// promoteRig is a master and a read-only replica following it, pinned
// replica under the master; the replica's driver runs jobs.
type promoteRig struct {
	t               *testing.T
	master, replica *testmysql.Instance
	mdb, rdb        *sql.DB
	dir, source     string
	d               *mysql.Driver
	reg             *commands.Registry
	ev              *jobEvents
	replicaServerID string
}

func newPromoteRig(t *testing.T) *promoteRig {
	t.Helper()
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	// dbpanel_repl@'%', as preparing a master makes it.
	if err := mysql.CreateReplUser(context.Background(), master.Host(),
		proto.ReplUserParams{User: "dbpanel_repl", FromHost: "%", Password: replPassword}); err != nil {
		t.Fatal(err)
	}
	r := &promoteRig{t: t, master: master, replica: replica, mdb: rootDB(t, master), rdb: rootDB(t, replica),
		source: "127.0.0.1:" + strconv.Itoa(master.Port)}
	execAll(t, r.mdb, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY)")
	follow(t, master, replica)
	synced(t, master, replica)
	execAll(t, r.rdb, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r.dir = pinDir(t, "replica", r.source)
	r.ev = &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(r.ev, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.d = &mysql.Driver{Host: replica.Host(), PinDir: r.dir, Jobs: runner}
	r.reg = commands.NewRegistry()
	r.d.Register(r.reg)
	if err := r.rdb.QueryRow("SELECT @@global.server_uuid").Scan(&r.replicaServerID); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *promoteRig) params(mode string) proto.PromoteParams {
	return proto.PromoteParams{Mode: mode, OldSource: proto.PeerCred{Host: "127.0.0.1", Port: r.master.Port,
		User: "dbpanel_repl", Password: replPassword}, ReplPassword: newReplPassword}
}

func (r *promoteRig) dispatch(cmd string, params any) proto.Response {
	r.t.Helper()
	return r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: mustJSON(r.t, params)})
}

// run starts mysql.promote and waits for its job.
func (r *promoteRig) run(p proto.PromoteParams) proto.JobDone {
	r.t.Helper()
	started := decode[proto.StartedJob](r.t, r.dispatch("mysql.promote", p))
	if started.JobID == "" {
		r.t.Fatal("mysql.promote: no job id")
	}
	select {
	case d := <-r.ev.done:
		return d
	case <-time.After(10 * time.Minute):
		r.t.Fatal("promote did not finish")
	}
	return proto.JobDone{}
}

func promoteResult(t *testing.T, d proto.JobDone) proto.PromoteResult {
	t.Helper()
	if !d.OK {
		t.Fatalf("promote failed: %s", d.Error)
	}
	var res proto.PromoteResult
	if err := json.Unmarshal(d.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// wantFailed fails unless the job failed with an error holding part, and
// the replica is untouched: read-only, replicating, pinned replica.
func (r *promoteRig) wantFailed(d proto.JobDone, part string) {
	r.t.Helper()
	if d.OK || !strings.Contains(d.Error, part) {
		r.t.Fatalf("promote = ok %v, error %q; want an error with %q", d.OK, d.Error, part)
	}
	if strings.Contains(d.Error, replPassword) || strings.Contains(d.Error, newReplPassword) {
		r.t.Errorf("error shows a password: %s", d.Error)
	}
	if sro, ro := readOnly(r.t, r.rdb); !sro || !ro {
		r.t.Errorf("after a refused promote: super_read_only=%v read_only=%v", sro, ro)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var io, sqlState string
		err := r.rdb.QueryRow(`SELECT c.SERVICE_STATE, a.SERVICE_STATE FROM performance_schema.replication_connection_status c
			JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE CHANNEL_NAME = ''`).Scan(&io, &sqlState)
		if err != nil {
			r.t.Fatal(err)
		}
		if io != "OFF" && sqlState == "ON" {
			break
		}
		if time.Now().After(deadline) {
			r.t.Errorf("after a refused promote: receiver %s, applier %s", io, sqlState)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := pinFile(r.t, r.dir, mysql.RoleFile); got != "replica" {
		r.t.Errorf("role pin = %q, want replica", got)
	}
	if got := pinFile(r.t, r.dir, mysql.SourceFile); got != r.source {
		r.t.Errorf("source pin = %q, want %s", got, r.source)
	}
}

// wantPromoted checks the replica became the master: writable for good,
// no replication configured, pinned master without a source, the
// promotion recorded, the replication account present, and no
// transaction of its own.
func (r *promoteRig) wantPromoted(res proto.PromoteResult, mode string) {
	r.t.Helper()
	if sro, ro := readOnly(r.t, r.rdb); sro || ro {
		r.t.Errorf("after promote: super_read_only=%v read_only=%v", sro, ro)
	}
	for _, v := range []string{"read_only", "super_read_only"} {
		if got := persisted(r.t, r.rdb, v); got != "OFF" {
			r.t.Errorf("persisted %s = %q, want OFF", v, got)
		}
	}
	if n := count(r.t, r.rdb, "SELECT COUNT(*) FROM performance_schema.replication_connection_configuration"); n != 0 {
		r.t.Errorf("%d replication channels left", n)
	}
	if got := pinFile(r.t, r.dir, mysql.RoleFile); got != "master" {
		r.t.Errorf("role pin = %q, want master", got)
	}
	if got := pinFile(r.t, r.dir, mysql.SourceFile); got != "" {
		r.t.Errorf("source pin = %q, want none", got)
	}
	pr, ok, err := r.d.Pins().ReadPromoted()
	if err != nil || !ok || pr.Mode != mode || pr.OldSource != r.source || pr.GTIDExecuted != res.GTIDExecuted ||
		time.Since(pr.At) > 10*time.Minute {
		r.t.Errorf("promotion record = %+v, %v, %v", pr, ok, err)
	}
	if res.GTIDExecuted != gtidExecuted(r.t, r.rdb) || res.ServerUUID != r.replicaServerID {
		r.t.Errorf("result = %+v; want executed %q, uuid %s", res, gtidExecuted(r.t, r.rdb), r.replicaServerID)
	}
	if mysql.GTIDHasUUID(res.GTIDExecuted, r.replicaServerID) {
		r.t.Errorf("the promoted replica has transactions of its own: %s", res.GTIDExecuted)
	}
	if n := count(r.t, r.rdb, "SELECT COUNT(*) FROM mysql.user WHERE user = 'dbpanel_repl' AND host = '%'"); n != 1 {
		r.t.Errorf("dbpanel_repl@%% rows = %d", n)
	}
	execAll(r.t, r.rdb, "INSERT INTO toko.t VALUES (1000)")
}

// dropLocalReplUser removes dbpanel_repl from the replica only.
func dropLocalReplUser(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, q := range []string{"SET SESSION sql_log_bin = 0", "SET GLOBAL super_read_only = OFF",
		"DROP USER 'dbpanel_repl'@'%'", "SET GLOBAL super_read_only = ON"} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// waitBinlogDump waits until a replica reads master's binary log.
func waitBinlogDump(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for count(t, db, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replica never connected")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestPromotePinRules: only a replica pinned to the old master is
// promoted.
func TestPromotePinRules(t *testing.T) {
	p := proto.PromoteParams{Mode: proto.PromotePlanned, OldSource: proto.PeerCred{Host: "10.0.0.1", Port: 3306}}
	for _, c := range []struct {
		role, source string
		want         error
	}{
		{"master", "", mysql.ErrRolePinned},
		{"", "", mysql.ErrRolePinned},
		{"quarantined", "10.0.0.1:3306", mysql.ErrRolePinned},
		{"replica", "", mysql.ErrSourcePinned},
		{"replica", "10.0.0.2:3306", mysql.ErrSourcePinned},
		{"replica", "10.0.0.1:3307", mysql.ErrSourcePinned},
		{"replica", "10.0.0.1:3306", nil},
	} {
		pins, err := mysql.LoadPins(pinDir(t, c.role, c.source))
		if err != nil {
			t.Fatal(err)
		}
		if err := pins.Allow("mysql.promote", p); !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
			t.Errorf("Allow(promote) on %q/%q = %v, want %v", c.role, c.source, err, c.want)
		}
	}
}

// TestPromotePlannedHappyPath: with the master locked through
// lockForPromote, the replica promotes itself: writable, no replication,
// pinned master, the promotion recorded and the replication account
// (missing on the replica) created locally.
func TestPromotePlannedHappyPath(t *testing.T) {
	r := newPromoteRig(t)
	waitBinlogDump(t, r.mdb)
	mr := newPinRig(t, r.master.Host(), pinDir(t, "master", ""))
	lock := decode[proto.LockForPromoteResult](t, mr.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{
		Candidate: proto.PeerCred{Host: "127.0.0.1", Port: r.replica.Port, User: "dbpanel_repl", Password: replPassword}, TimeoutSec: 600}))
	dropLocalReplUser(t, r.rdb)

	p := r.params(proto.PromotePlanned)
	p.LockDeadline = &lock.Deadline
	res := promoteResult(t, r.run(p))
	if res.Applied != 0 || res.Resumed {
		t.Errorf("result = %+v; want nothing applied, not resumed", res)
	}
	if res.GTIDExecuted != lock.GTIDExecuted {
		t.Errorf("executed %q, the locked master's %q", res.GTIDExecuted, lock.GTIDExecuted)
	}
	r.wantPromoted(res, proto.PromotePlanned)
	// The account was made with the given password.
	if pr := mysql.ReadPeer(context.Background(), proto.PeerCred{Host: "127.0.0.1", Port: r.replica.Port, User: "dbpanel_repl",
		Password: newReplPassword}, 5*time.Second); !pr.Reachable {
		t.Errorf("dbpanel_repl with the new password: %s", pr.Err)
	}
	// Done: a second promote is refused by the pins.
	wantRefused(t, "promote of the new master", r.dispatch("mysql.promote", p), "server ini dipin sebagai master")
}

// TestPromotePlannedRefusedWhenMasterWritable: a planned promotion of a
// replica whose master is not locked is refused and leaves it as it was;
// an old source other than the pinned one is refused by the pins.
func TestPromotePlannedRefusedWhenMasterWritable(t *testing.T) {
	r := newPromoteRig(t)
	other := r.params(proto.PromotePlanned)
	other.OldSource.Port++
	wantRefused(t, "promote from another source", r.dispatch("mysql.promote", other), "server ini dipin mengikuti sumber "+r.source)

	r.wantFailed(r.run(r.params(proto.PromotePlanned)), "masih bisa ditulis")
}

// TestPromoteEmergencyAppliesRelayLog: with its master down, the replica
// applies what it had received but not applied, then becomes the master.
func TestPromoteEmergencyAppliesRelayLog(t *testing.T) {
	r := newPromoteRig(t)
	t.Cleanup(mysql.SetProbeSpacing(50 * time.Millisecond))
	if res := decode[proto.ProbeSourceResult](t, r.dispatch("mysql.probeSource", nil)); !res.Reachable ||
		res.Host != "127.0.0.1" || res.Port != r.master.Port {
		t.Errorf("probeSource with the master up = %+v", res)
	}

	execAll(t, r.rdb, "STOP REPLICA SQL_THREAD")
	execAll(t, r.mdb, "INSERT INTO toko.t VALUES (1)", "INSERT INTO toko.t VALUES (2)", "INSERT INTO toko.t VALUES (3)")
	want := gtidExecuted(t, r.mdb)
	deadline := time.Now().Add(time.Minute)
	for {
		var ok bool
		if err := r.rdb.QueryRow(`SELECT GTID_SUBSET(?, (SELECT RECEIVED_TRANSACTION_SET FROM performance_schema.replication_connection_status
			WHERE CHANNEL_NAME = ''))`, want).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replica never received the rows")
		}
		time.Sleep(200 * time.Millisecond)
	}
	r.master.Stop(t)
	deadline = time.Now().Add(time.Minute)
	for count(t, r.rdb, "SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE SERVICE_STATE = 'ON'") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("receiver still connected after the master stopped")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if res := decode[proto.ProbeSourceResult](t, r.dispatch("mysql.probeSource", nil)); res.Reachable || res.Err == "" {
		t.Errorf("probeSource with the master down = %+v", res)
	}

	res := promoteResult(t, r.run(r.params(proto.PromoteEmergency)))
	if res.Applied != 3 || res.Resumed {
		t.Errorf("result = %+v; want 3 applied", res)
	}
	if n := count(t, r.rdb, "SELECT COUNT(*) FROM toko.t"); n != 3 {
		t.Errorf("rows = %d, want 3", n)
	}
	if ok, err := subset(r.rdb, want, res.GTIDExecuted); err != nil || !ok {
		t.Errorf("executed %q lacks the master's %q (%v)", res.GTIDExecuted, want, err)
	}
	r.wantPromoted(res, proto.PromoteEmergency)
	probes := 0
	for _, l := range r.ev.logs {
		if strings.Contains(l, "tidak menjawab (percobaan") {
			probes++
		}
	}
	if probes != 3 {
		t.Errorf("%d failed probes logged, want 3: %q", probes, r.ev.logs)
	}
}

func subset(db *sql.DB, a, b string) (bool, error) {
	var ok bool
	err := db.QueryRow("SELECT GTID_SUBSET(?, ?)", a, b).Scan(&ok)
	return ok, err
}

// TestPromoteEmergencyRefusedWhenMasterAnswers: an emergency promotion
// while the master answers writable is refused, pointing at lock-master.
func TestPromoteEmergencyRefusedWhenMasterAnswers(t *testing.T) {
	r := newPromoteRig(t)
	r.wantFailed(r.run(r.params(proto.PromoteEmergency)), "sudo dbpanel agent lock-master")
}

// TestPromoteResumesAfterOpenWrites: a promotion that opened writes but
// did not finish is resumed without reading the old master again.
func TestPromoteResumesAfterOpenWrites(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.rdb, "SET PERSIST super_read_only = OFF", "SET PERSIST read_only = OFF")
	t.Cleanup(mysql.SetReadPeer(func(context.Context, proto.PeerCred, time.Duration) mysql.PeerState {
		t.Error("resume read the old master")
		return mysql.PeerState{}
	}))
	t.Cleanup(mysql.SetProbePeer(func(context.Context, string, int, time.Duration) error {
		t.Error("resume probed the old master")
		return nil
	}))
	res := promoteResult(t, r.run(r.params(proto.PromotePlanned)))
	if !res.Resumed || res.Applied != 0 {
		t.Errorf("result = %+v; want resumed", res)
	}
	r.wantPromoted(res, proto.PromotePlanned)
}

// TestPromoteRefusesNearLockDeadline: less than two minutes left on the
// master's lock: replication is started again and the promotion refused.
func TestPromoteRefusesNearLockDeadline(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.mdb, "SET GLOBAL super_read_only = ON")
	p := r.params(proto.PromotePlanned)
	deadline := time.Now().Add(time.Minute)
	p.LockDeadline = &deadline
	r.wantFailed(r.run(p), "tenggat kunci master hampir habis; ulangi promote")
	if !strings.Contains(strings.Join(r.ev.logs, "\n"), "replikasi dijalankan kembali") {
		t.Errorf("logs = %q; want replication restarted", r.ev.logs)
	}
}
