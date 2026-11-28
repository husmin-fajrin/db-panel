package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
)

// The write-opening statements of mysql.promote (step 5) and what happens
// when their outcome is unknown.
//
// A client that stops waiting does not stop a statement on mysqld: a SET
// PERSIST read_only = OFF stuck on a stalled disk is applied when the
// stall ends, after the admin connection's read timeout. So the two
// statements run on one connection whose id, and the mysqld instance it
// belongs to, the promoting marker records before the first one leaves.
// An answer (OK or an error packet) is a known outcome. Anything else
// (timeout, lost connection, a driver error) is unknown, and the job does
// not go on, roll back or end until it is settled: the connection is gone
// from mysqld (killed and absent from the process list, or mysqld is
// another instance since), and only then are the flags read. While it is
// not settled the job runs, so mysql.status says a promote request is
// pending (promote_pending.go). An agent that starts over a marker that
// names a connection runs the same settling before it says none is.

var (
	// openSettleTry bounds one settling try; openSettlePause is the pause
	// between two; openSettleLogEvery is how often a try that keeps
	// failing is logged again. Tests shorten them.
	openSettleTry      = 15 * time.Second
	openSettlePause    = 5 * time.Second
	openSettleLogEvery = time.Minute
)

// openKillPoll is how often a try reads the process list after the KILL.
const openKillPoll = 50 * time.Millisecond

// bootTolerance: two instance starts this close are the same instance.
// UNIX_TIMESTAMP() and Uptime are whole seconds, so the start read twice
// may differ by one; a false "another instance" would skip the KILL.
const bootTolerance = 2

// restartedSince reports whether mysqld started at now is another
// instance than the one started at rec: both known and more than
// bootTolerance apart.
func restartedSince(rec, now int64) bool {
	if rec == 0 || now == 0 {
		return false
	}
	d := now - rec
	return d > bootTolerance || d < -bootTolerance
}

// errUnknownThread is ER_NO_SUCH_THREAD: KILL of an id mysqld does not
// have.
const errUnknownThread = 1094

// instanceBootQuery reads the second the running mysqld started
// (server_start_time). Uptime is this statement's start time less
// server_start_time, and UNIX_TIMESTAMP() is this statement's start time,
// so the difference is exact and does not move when the clock is set; it
// changes on every restart (server_uuid does not).
const instanceBootQuery = "SELECT UNIX_TIMESTAMP() - CAST(VARIABLE_VALUE AS SIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME = 'Uptime'"

// openExec sends one write-opening statement on c; tests replace it.
var openExec = func(ctx context.Context, c *sql.Conn, q string) error {
	_, err := c.ExecContext(ctx, q)
	return err
}

// openSettleHook, when set, is called at the start of every settling try;
// an error fails the try (tests).
var openSettleHook func() error

// knownOutcome reports whether err is an answer of the server: none, or
// an error packet. An answer ends the statement; an error packet still
// does not say what it changed (sendWriteOpening reads the flags).
func knownOutcome(err error) bool {
	var me *drv.MySQLError
	return err == nil || errors.As(err, &me)
}

// instanceBoot reads instanceBootQuery on c; 0 (unknown) when it cannot
// be read. Unknown only means the settling kills without asking whether
// mysqld was restarted.
func instanceBoot(ctx context.Context, c *sql.Conn) int64 {
	var b int64
	if err := c.QueryRowContext(ctx, instanceBootQuery).Scan(&b); err != nil {
		return 0
	}
	return b
}

// writeOpeningConn takes one connection of db for the write-opening
// statements and writes the promoting marker m with its id and instance
// (synced) before anything is sent on it.
func (d *Driver) writeOpeningConn(ctx context.Context, db *sql.DB, m Promoting) (*sql.Conn, Promoting, error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, m, fmt.Errorf("sambungan untuk membuka tulis: %w", err)
	}
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&m.ConnID); err != nil {
		c.Close()
		return nil, m, fmt.Errorf("baca CONNECTION_ID(): %w", err)
	}
	m.Boot = instanceBoot(ctx, c)
	if err := d.pins.WritePromoting(m); err != nil {
		c.Close()
		return nil, m, fmt.Errorf("catatan promote gagal ditulis: %w", err)
	}
	return c, m, nil
}

const (
	openReadOnlyStmt  = "SET PERSIST read_only = OFF"
	openSuperOnlyStmt = "SET PERSIST super_read_only = OFF"
)

// sendWriteOpening runs the two write-opening statements on c (named in
// the marker m) in their order. An error packet is not proof that a
// statement did nothing (MySQL changes the global value before it writes
// mysqld-auto.cnf, and answers an error when that write fails), so after
// one the flags are read again (answeredWritable). For the first: still
// read-only rolls back (restore) as before, writable goes on as after an
// OK. An outcome that is unknown, or flags that cannot be read after an
// error packet, is settled first (settleOpen): still read-only rolls
// back, writable goes on with the second on a new connection the marker
// names. Writes are open when it returns nil.
func (d *Driver) sendWriteOpening(j *jobs.Job, db *sql.DB, c *sql.Conn, m Promoting, restore func(error) error) error {
	// Past the point of no return a cancel does not stop anything.
	ctx := context.WithoutCancel(j.Context())
	defer func() { c.Close() }()
	jobDB := func(context.Context) (*sql.DB, func(), error) { return db, func() {}, nil }
	err := openExec(ctx, c, openReadOnlyStmt)
	unknown := !knownOutcome(err)
	if err != nil && !unknown {
		writable, rerr := answeredWritable(ctx, db)
		switch {
		case rerr != nil:
			j.Logf("warn", "MySQL menjawab %s dengan galat (%v), lalu read_only tidak bisa dibaca (%v)", openReadOnlyStmt, err, rerr)
			unknown = true
		case writable:
			j.Logf("warn", "MySQL menjawab %s dengan galat (%v), tetapi server ini sudah bisa ditulis: tulis terbuka, promote dilanjutkan",
				openReadOnlyStmt, err)
			err = nil
		default:
			return restore(fmt.Errorf("%s: %w", openReadOnlyStmt, err))
		}
	}
	if unknown {
		c.Close()
		j.Logf("warn", "MySQL tidak menjawab %s dengan pasti (%v); promote ditahan sampai pasti perintah itu sudah berhenti di MySQL", openReadOnlyStmt, err)
		if settleOpen(ctx, m, jobDB, j.Logf) {
			j.Logf("info", "perintah pembuka tulis sudah berhenti di MySQL dan server ini tetap baca-saja (read-only)")
			return restore(fmt.Errorf("%s: %w; perintah itu sudah dipastikan berhenti di MySQL dan server ini tetap baca-saja (read-only)",
				openReadOnlyStmt, err))
		}
		j.Logf("info", "perintah pembuka tulis sudah berhenti di MySQL dan sudah dijalankan: tulis terbuka, promote dilanjutkan")
		// Writes opened now: a resume after a later failure counts the
		// marker's age from here (checkResume).
		m.At = d.now()
		next, nm, err := d.writeOpeningConn(ctx, db, m)
		if err != nil {
			return fmt.Errorf("%s: %w; tulis sudah terbuka, jalankan promote lagi untuk melanjutkan", openSuperOnlyStmt, err)
		}
		c, m = next, nm
	}
	return openSuper(ctx, j, db, c, m)
}

// openSuper sends the second write-opening statement on c (named in the
// marker m), read_only being off already. Writes are open either way; only
// an unknown outcome, or flags that cannot be read after an error packet,
// is settled before the error is returned (run promote again to resume).
func openSuper(ctx context.Context, j *jobs.Job, db *sql.DB, c *sql.Conn, m Promoting) error {
	defer c.Close()
	err := openExec(ctx, c, openSuperOnlyStmt)
	if err == nil {
		return nil
	}
	unknown := !knownOutcome(err)
	if !unknown {
		if _, rerr := answeredWritable(ctx, db); rerr != nil {
			j.Logf("warn", "MySQL menjawab %s dengan galat (%v), lalu read_only tidak bisa dibaca (%v)", openSuperOnlyStmt, err, rerr)
			unknown = true
		}
	}
	if unknown {
		c.Close()
		j.Logf("warn", "MySQL tidak menjawab %s dengan pasti (%v); promote ditahan sampai pasti perintah itu sudah berhenti di MySQL", openSuperOnlyStmt, err)
		jobDB := func(context.Context) (*sql.DB, func(), error) { return db, func() {}, nil }
		settleOpen(ctx, m, jobDB, j.Logf)
		j.Logf("info", "perintah pembuka tulis sudah berhenti di MySQL")
	}
	return fmt.Errorf("%s: %w; tulis sudah terbuka, jalankan promote lagi untuk melanjutkan", openSuperOnlyStmt, err)
}

// finishWriteOpening is a resumed promotion on a server whose read_only is
// off and super_read_only still on (resumeOpen): the first statement was
// applied by an earlier run, whose marker m checkResume accepted. The
// marker is written again naming a new connection, and the second
// statement goes on it (openSuper), as in the earlier run.
func (d *Driver) finishWriteOpening(j *jobs.Job, db *sql.DB, m Promoting) error {
	// Past the point of no return a cancel does not stop anything.
	ctx := context.WithoutCancel(j.Context())
	c, nm, err := d.writeOpeningConn(ctx, db, m)
	if err != nil {
		return fmt.Errorf("%s: %w; tulis sudah terbuka, jalankan promote lagi untuk melanjutkan", openSuperOnlyStmt, err)
	}
	return openSuper(ctx, j, db, c, nm)
}

// openReadFlags reads read_only and super_read_only on db; tests replace
// it.
var openReadFlags = func(ctx context.Context, db *sql.DB) (ro, sro bool, err error) {
	err = db.QueryRowContext(ctx, "SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro)
	return ro, sro, err
}

// answeredWritable reads, within openSettleTry, whether writes are open
// after a write-opening statement was answered with an error packet (the
// statement has ended: it was answered): read_only OFF.
func answeredWritable(ctx context.Context, db *sql.DB) (bool, error) {
	rctx, cancel := context.WithTimeout(ctx, openSettleTry)
	defer cancel()
	ro, _, err := openReadFlags(rctx, db)
	if err != nil {
		return false, err
	}
	return AppsWrite(ro), nil
}

// settleOpen returns once the connection m names is gone from mysqld, with
// whether read_only was on when read after that. It tries until then,
// openSettlePause apart, each try bounded by openSettleTry; ctx should not
// end (the job's is WithoutCancel). open gives the pool a try uses and
// the function that releases it.
func settleOpen(ctx context.Context, m Promoting, open func(context.Context) (*sql.DB, func(), error),
	logf func(level, format string, args ...any)) (readOnly bool) {
	var logged time.Time
	for try := 1; ; try++ {
		tctx, cancel := context.WithTimeout(ctx, openSettleTry)
		ro, err := settleOpenOnce(tctx, m, open)
		cancel()
		if err == nil {
			return ro
		}
		if try == 1 || time.Since(logged) >= openSettleLogEvery {
			logf("warn", "belum bisa dipastikan perintah pembuka tulis sudah berhenti di MySQL (percobaan ke-%d): %v; dicoba lagi tiap %s. "+
				"Bila MySQL tidak pulih, mulai ulang MySQL di server ini: perintah yang tertahan ikut berhenti, lalu agent memastikannya sendiri",
				try, err, openSettlePause)
			logged = time.Now()
		}
		t := time.NewTimer(openSettlePause)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
		}
	}
}

// settleOpenOnce is one try of settleOpen on a fresh connection: the
// connection of m is gone when mysqld is another instance than m's
// (restartedSince), when this try's own connection has its id (ids are
// not reused by one instance), or once it is absent from the process list
// (after a KILL when the row is of this try's own account, or KILL found
// the id unknown). Then the flags are read.
func settleOpenOnce(ctx context.Context, m Promoting, open func(context.Context) (*sql.DB, func(), error)) (bool, error) {
	if openSettleHook != nil {
		if err := openSettleHook(); err != nil {
			return false, err
		}
	}
	db, release, err := open(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	c, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer c.Close()
	var self uint64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&self); err != nil {
		return false, fmt.Errorf("baca CONNECTION_ID(): %w", err)
	}
	boot := instanceBoot(ctx, c)
	if !restartedSince(m.Boot, boot) && self != m.ConnID {
		// A restart within bootTolerance reads as the same instance, and
		// its connection with the recorded id is not ours: only a row of
		// this try's own account is killed. Any other row is waited for.
		var own string
		if err := c.QueryRowContext(ctx, "SELECT USER FROM information_schema.PROCESSLIST WHERE ID = CONNECTION_ID()").Scan(&own); err != nil {
			return false, fmt.Errorf("baca daftar proses MySQL: %w", err)
		}
		id := strconv.FormatUint(m.ConnID, 10)
		killed := false
		for {
			var user sql.NullString
			err := c.QueryRowContext(ctx, "SELECT USER FROM information_schema.PROCESSLIST WHERE ID = ?", m.ConnID).Scan(&user)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return false, fmt.Errorf("baca daftar proses MySQL: %w", err)
			}
			if !killed && user.Valid && user.String == own {
				var me *drv.MySQLError
				if _, err := c.ExecContext(ctx, "KILL "+id); err != nil {
					if errors.As(err, &me) && me.Number == errUnknownThread {
						break
					}
					return false, fmt.Errorf("KILL %s: %w", id, err)
				}
				killed = true
			}
			select {
			case <-ctx.Done():
				if !killed {
					return false, fmt.Errorf("sambungan %s milik akun lain (%s) masih ada di daftar proses MySQL", id, user.String)
				}
				return false, fmt.Errorf("sambungan %s masih ada di daftar proses MySQL setelah KILL", id)
			case <-time.After(openKillPoll):
			}
		}
	}
	var ro, sro bool
	if err := c.QueryRowContext(ctx, "SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro); err != nil {
		return false, fmt.Errorf("baca read_only: %w", err)
	}
	return ro, nil
}

// settleLeftOpenAtStart: a promoting marker that names a connection was
// left by an agent that may have ended while a write-opening statement
// was in flight. Until the same settling has run once to its end,
// mysql.status says a promote request is pending, and a promote job waits
// for it (waitLeftOpen). The marker itself is left as it is: checkResume
// handles it as a marker left by a crash. Called by Register, before any
// request is read.
func (d *Driver) settleLeftOpenAtStart() {
	if d.pins == nil {
		return
	}
	m, ok, err := d.pins.ReadPromoting()
	if err != nil {
		d.warn("promoting marker unreadable; a write-opening statement it may name is not settled", "err", err)
		return
	}
	if !ok || m.ConnID == 0 {
		return
	}
	d.promoteReqs.Add(1)
	done := make(chan struct{})
	d.leftOpen = done
	d.warn("promoting marker names a write-opening connection; settling it before mysql.status reports no promote request",
		"conn_id", m.ConnID, "mysqld_boot", m.Boot)
	open := func(ctx context.Context) (*sql.DB, func(), error) {
		h, err := d.host()
		if err != nil {
			return nil, nil, err
		}
		db, err := h.Open(ctx)
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}
	logf := func(_, format string, args ...any) {
		d.warn("settling a write-opening connection", "detail", fmt.Sprintf(format, args...))
	}
	go func() {
		defer close(done)
		defer d.promoteDone()
		ro := settleOpen(context.Background(), m, open, logf)
		d.log("write-opening connection of the promoting marker settled", "conn_id", m.ConnID, "read_only", ro)
	}()
}

// waitLeftOpen holds a promote job until settleLeftOpenAtStart is done.
func (d *Driver) waitLeftOpen(j *jobs.Job) error {
	if d.leftOpen == nil {
		return nil
	}
	select {
	case <-d.leftOpen:
		return nil
	default:
	}
	j.Logf("info", "menunggu agent memastikan perintah pembuka tulis dari promote sebelumnya sudah berhenti di MySQL")
	select {
	case <-d.leftOpen:
		return nil
	case <-j.Context().Done():
		return j.Context().Err()
	}
}
