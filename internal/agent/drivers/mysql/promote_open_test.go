package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const setReadOnlyOff = "SET PERSIST read_only = OFF"

// shortSettle makes settling tries short and close together.
func shortSettle(t *testing.T) {
	t.Cleanup(mysql.SetOpenSettle(3*time.Second, 50*time.Millisecond))
}

// standIn makes the first write-opening statement a stand-in that keeps
// running on the server after the client stopped waiting (DO SLEEP(600)
// under a one-second deadline): its outcome is unknown. The id of its
// connection goes to ids.
func standIn(t *testing.T, ids chan<- uint64) {
	t.Cleanup(mysql.SetOpenExec(func(ctx context.Context, c *sql.Conn, q string) error {
		if q != setReadOnlyOff {
			_, err := c.ExecContext(ctx, q)
			return err
		}
		var id uint64
		if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			return err
		}
		ids <- id
		qctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := c.ExecContext(qctx, "DO SLEEP(600)")
		return err
	}))
}

// holdSettle makes every settling try fail until the returned func is
// called; tries gets a value at each try.
func holdSettle(t *testing.T) (tries <-chan struct{}, release func()) {
	c := make(chan struct{}, 64)
	var released atomic.Bool
	t.Cleanup(mysql.SetOpenSettleHook(func() error {
		select {
		case c <- struct{}{}:
		default:
		}
		if !released.Load() {
			return errors.New("ditahan (test)")
		}
		return nil
	}))
	return c, func() { released.Store(true) }
}

// startJob starts mysql.promote and returns its job id without waiting.
func (r *promoteRig) startJob(p proto.PromoteParams) string {
	r.t.Helper()
	started := decode[proto.StartedJob](r.t, r.dispatch("mysql.promote", p))
	if started.JobID == "" {
		r.t.Fatal("mysql.promote: no job id")
	}
	return started.JobID
}

func (r *promoteRig) waitJob() proto.JobDone {
	r.t.Helper()
	select {
	case d := <-r.ev.done:
		return d
	case <-time.After(5 * time.Minute):
		r.t.Fatal("promote did not finish")
	}
	return proto.JobDone{}
}

// promoteRequest is mysql.status's promote_request.
func promoteRequest(t *testing.T, resp proto.Response) bool {
	t.Helper()
	st := decode[proto.MySQLStatus](t, resp)
	if st.PromoteRequest == nil {
		t.Fatal("mysql.status without promote_request")
	}
	return *st.PromoteRequest
}

// inProcessList reports whether connection id is in db's process list.
func inProcessList(t *testing.T, db *sql.DB, id uint64) bool {
	t.Helper()
	return count(t, db, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID = "+strconv.FormatUint(id, 10)) > 0
}

// replicationOff reports whether the receiver and the applier are both
// stopped.
func replicationOff(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var io, sqlState string
	err := db.QueryRow(`SELECT c.SERVICE_STATE, a.SERVICE_STATE FROM performance_schema.replication_connection_status c
		JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE CHANNEL_NAME = ''`).Scan(&io, &sqlState)
	if err != nil {
		t.Fatal(err)
	}
	return io == "OFF" && sqlState == "OFF"
}

func recv[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(time.Minute):
		t.Fatalf("%s did not happen", what)
	}
	var zero T
	return zero
}

// mysqldPID is the pid of inst's mysqld.
func mysqldPID(t *testing.T, inst *testmysql.Instance) int {
	t.Helper()
	b, err := os.ReadFile(inst.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// TestOpenWritesUnknownOutcomeKeepsRequestPending: the first SET gives no
// answer while the statement still runs on the server; until that is
// settled the job runs, mysql.status says a promote request is pending,
// the marker naming the connection is on disk and replication is not
// started again.
func TestOpenWritesUnknownOutcomeKeepsRequestPending(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.mdb, "SET GLOBAL super_read_only = ON")
	shortSettle(t)
	ids := make(chan uint64, 1)
	standIn(t, ids)
	tries, release := holdSettle(t)

	r.startJob(r.params(proto.PromotePlanned))
	id := recv(t, ids, "the first write-opening statement")
	recv(t, tries, "a settling try")
	recv(t, tries, "a second settling try")

	select {
	case d := <-r.ev.done:
		t.Fatalf("the job ended while the statement was unsettled: %+v", d)
	default:
	}
	if !promoteRequest(t, r.dispatch("mysql.status", nil)) {
		t.Error("mysql.status: promote_request false while the write-opening statement is unsettled")
	}
	m, ok, err := r.d.Pins().ReadPromoting()
	if err != nil || !ok || m.ConnID != id || m.Boot == 0 {
		t.Errorf("marker = %+v, %v, %v; want one naming connection %d and the instance", m, ok, err, id)
	}
	if !inProcessList(t, r.rdb, id) {
		t.Error("the stand-in statement is not running on the server")
	}
	if !replicationOff(t, r.rdb) {
		t.Error("replication was started again while the statement was unsettled")
	}
	if sro, ro := readOnly(t, r.rdb); !sro || !ro {
		t.Errorf("super_read_only=%v read_only=%v", sro, ro)
	}

	release()
	r.wantFailed(r.waitJob(), setReadOnlyOff)
	if promoteRequest(t, r.dispatch("mysql.status", nil)) {
		t.Error("mysql.status: promote_request true after the job ended")
	}
}

// TestOpenWritesUnknownOutcomeSettledReadOnlyRollsBack: the connection of
// the unanswered statement is killed and gone, both flags are on: the
// rollback of before, the job fails, no request is pending.
func TestOpenWritesUnknownOutcomeSettledReadOnlyRollsBack(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.mdb, "SET GLOBAL super_read_only = ON")
	shortSettle(t)
	ids := make(chan uint64, 1)
	standIn(t, ids)

	r.startJob(r.params(proto.PromotePlanned))
	id := recv(t, ids, "the first write-opening statement")
	d := r.waitJob()
	r.wantFailed(d, "tetap baca-saja")
	if inProcessList(t, r.rdb, id) {
		t.Errorf("connection %d still in the process list after the job ended", id)
	}
	if got := pinFile(t, r.dir, mysql.PromotingFile); got != "" {
		t.Errorf("promoting marker left after the rollback: %s", got)
	}
	if !r.ev.logged("replikasi dijalankan kembali") {
		t.Error("the rollback did not start replication again")
	}
	if promoteRequest(t, r.dispatch("mysql.status", nil)) {
		t.Error("mysql.status: promote_request true after the job failed")
	}
}

// TestOpenWritesUnknownOutcomeSettledWritableContinues: the first SET is
// sent to a stopped mysqld (SIGSTOP), the client gives up, and mysqld
// applies it once it runs again: settled as writable, the job goes on to
// its normal end without starting replication again.
func TestOpenWritesUnknownOutcomeSettledWritableContinues(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.mdb, "SET GLOBAL super_read_only = ON")
	shortSettle(t)
	pid := mysqldPID(t, r.replica)
	var stopped atomic.Bool
	t.Cleanup(func() {
		if stopped.Load() {
			_ = syscall.Kill(pid, syscall.SIGCONT)
		}
	})
	sent := make(chan error, 1)
	t.Cleanup(mysql.SetOpenExec(func(ctx context.Context, c *sql.Conn, q string) error {
		if q != setReadOnlyOff {
			_, err := c.ExecContext(ctx, q)
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			return err
		}
		stopped.Store(true)
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := c.ExecContext(qctx, q)
		sent <- err
		return err
	}))
	tries, release := holdSettle(t)

	r.startJob(r.params(proto.PromotePlanned))
	if err := recv(t, sent, "the first write-opening statement"); err == nil {
		t.Fatal("the statement was answered by a stopped mysqld")
	}
	recv(t, tries, "a settling try")
	recv(t, tries, "a second settling try")
	select {
	case d := <-r.ev.done:
		t.Fatalf("the job ended while mysqld was stopped: %+v", d)
	default:
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	stopped.Store(false)
	// mysqld applies the statement its client gave up on.
	deadline := time.Now().Add(time.Minute)
	for {
		if _, ro := readOnly(t, r.rdb); !ro {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mysqld never applied the statement sent while it was stopped")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !promoteRequest(t, r.dispatch("mysql.status", nil)) {
		t.Error("mysql.status: promote_request false while the job is not settled")
	}
	release()

	res := promoteResult(t, r.waitJob())
	r.wantPromoted(res, proto.PromotePlanned)
	if r.ev.logged("replikasi dijalankan kembali") {
		t.Error("replication was started again")
	}
	if !r.ev.logged("sudah dijalankan: tulis terbuka") {
		t.Error("the job log does not say the statement was found applied")
	}
}

// TestOpenWritesKnownErrorRollsBackAsBefore: an error packet answering
// the first SET is the rollback of before, with no settling.
func TestOpenWritesKnownErrorRollsBackAsBefore(t *testing.T) {
	r := newPromoteRig(t)
	execAll(t, r.mdb, "SET GLOBAL super_read_only = ON")
	t.Cleanup(mysql.SetOpenExec(func(ctx context.Context, c *sql.Conn, q string) error {
		if q == setReadOnlyOff {
			q = "SET PERSIST read_only = 'tidak'" // an error packet (1231)
		}
		_, err := c.ExecContext(ctx, q)
		return err
	}))
	t.Cleanup(mysql.SetOpenSettleHook(func() error {
		t.Error("a known error was settled")
		return nil
	}))
	d := r.run(r.params(proto.PromotePlanned))
	r.wantFailed(d, setReadOnlyOff+": Error 1231")
	if strings.Contains(d.Error, "baca-saja") {
		t.Errorf("error = %q; want the error of before", d.Error)
	}
	if got := pinFile(t, r.dir, mysql.PromotingFile); got != "" {
		t.Errorf("promoting marker left: %s", got)
	}
	if promoteRequest(t, r.dispatch("mysql.status", nil)) {
		t.Error("mysql.status: promote_request true after the job failed")
	}
}

// waitSettled waits until r's driver says no promote request is pending,
// then checks mysql.status says so too.
func waitSettled(t *testing.T, r *pendingRig) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for r.d.PromoteRequestPending() {
		if time.Now().After(deadline) {
			t.Fatal("still a promote request pending after a minute")
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.want("settled", false)
}

// TestPendingAfterRestartWithOpenMarker: an agent that starts over a
// marker naming a connection says a promote request is pending until it
// settled that connection itself: killed while mysqld is the same
// instance, left alone once mysqld was restarted. The marker stays.
func TestPendingAfterRestartWithOpenMarker(t *testing.T) {
	inst := newPendingInstance(t)
	db := rootDB(t, inst)
	shortSettle(t)
	ctx := context.Background()
	marker := func(id uint64, boot int64) string {
		dir := pinDir(t, "replica", pendingSource)
		pins, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		m := mysql.Promoting{Mode: proto.PromoteEmergency, OldSource: pendingSource, At: time.Now(), ConnID: id, Boot: boot}
		if err := pins.WritePromoting(m); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	held := func(db *sql.DB) (*sql.Conn, uint64) {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		var id uint64
		if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			t.Fatal(err)
		}
		return c, id
	}

	t.Run("same mysqld", func(t *testing.T) {
		c, id := held(db)
		boot, err := mysql.InstanceBoot(ctx, db)
		if err != nil || boot == 0 {
			t.Fatalf("instance boot = %d, %v", boot, err)
		}
		dir := marker(id, boot)
		tries, release := holdSettle(t)
		r := newPendingRig(t, inst, dir)
		if !r.d.PromoteRequestPending() {
			t.Fatal("PromoteRequestPending false right after the driver started over the marker")
		}
		recv(t, tries, "a settling try")
		r.want("connection alive, not settled", true)
		release()
		waitSettled(t, r)
		if inProcessList(t, db, id) {
			t.Errorf("connection %d not killed", id)
		}
		if err := c.PingContext(ctx); err == nil {
			t.Error("the marker's connection still answers")
		}
		if got := pinFile(t, dir, mysql.PromotingFile); got == "" {
			t.Error("the marker was removed by the settling")
		}
	})

	t.Run("mysqld restarted", func(t *testing.T) {
		before, err := mysql.InstanceBoot(ctx, db)
		if err != nil || before == 0 {
			t.Fatalf("instance boot = %d, %v", before, err)
		}
		inst.Restart(t)
		db := rootDB(t, inst)
		after, err := mysql.InstanceBoot(ctx, db)
		if err != nil || after == before {
			t.Fatalf("instance boot after restart = %d (before %d), %v", after, before, err)
		}
		// The connection id the marker names now belongs to another
		// connection of the new instance: it must not be killed.
		c, id := held(db)
		dir := marker(id, before)
		r := newPendingRig(t, inst, dir)
		waitSettled(t, r)
		if err := c.PingContext(ctx); err != nil {
			t.Errorf("the new instance's connection %d was killed: %v", id, err)
		}
	})
}

// TestOldMarkerWithoutConnIDReadsAsBefore: a marker without a connection
// (an older agent's, or written as before) reads as before and starts no
// settling.
func TestOldMarkerWithoutConnIDReadsAsBefore(t *testing.T) {
	dir := pinDir(t, "replica", pendingSource)
	at := time.Now().UTC().Truncate(time.Second)
	old, err := json.Marshal(map[string]string{"mode": proto.PromoteEmergency, "old_source": pendingSource, "at": at.Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mysql.PromotingFile), old, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mysql.SetOpenSettleHook(func() error {
		t.Error("a marker without a connection was settled")
		return nil
	}))
	d := &mysql.Driver{PinDir: dir}
	d.Register(commands.NewRegistry())
	if d.PromoteRequestPending() {
		t.Error("PromoteRequestPending true over a marker without a connection")
	}
	m, ok, err := d.Pins().ReadPromoting()
	if err != nil || !ok || m != (mysql.Promoting{Mode: proto.PromoteEmergency, OldSource: pendingSource, At: at}) {
		t.Fatalf("marker = %+v, %v, %v", m, ok, err)
	}
	// Written without a connection, it is the old format.
	if err := d.Pins().WritePromoting(m); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, dir, mysql.PromotingFile); strings.Contains(got, "conn_id") || strings.Contains(got, "mysqld_boot") {
		t.Errorf("marker without a connection written as %s", got)
	}
}

// TestKillAndConfirmGone: a statement still running on a connection the
// client gave up is killed and leaves the process list; a second try finds
// the id unknown, which counts as gone.
func TestKillAndConfirmGone(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id uint64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatal(err)
	}
	qctx, cancel := context.WithTimeout(ctx, time.Second)
	_, err = c.ExecContext(qctx, "DO SLEEP(600)")
	cancel()
	c.Close()
	if err == nil {
		t.Fatal("the stand-in statement returned")
	}
	time.Sleep(time.Second)
	if !inProcessList(t, db, id) {
		t.Fatal("the stand-in statement left the process list when its client gave up")
	}
	boot, err := mysql.InstanceBoot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ro, err := mysql.SettleOpenOnce(tctx, db, id, boot)
	cancel()
	if err != nil || ro {
		t.Fatalf("settling = read_only %v, %v; want settled, writable", ro, err)
	}
	if inProcessList(t, db, id) {
		t.Fatal("still in the process list after the settling")
	}
	var me *drv.MySQLError
	if _, err := db.Exec("KILL " + strconv.FormatUint(id, 10)); !errors.As(err, &me) || me.Number != 1094 {
		t.Fatalf("second KILL = %v; want error 1094 (unknown thread)", err)
	}
	tctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := mysql.SettleOpenOnce(tctx, db, id, boot); err != nil {
		t.Fatalf("settling an unknown id: %v", err)
	}
}
