package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	methodClone = "clone"
	methodDump  = "dump"

	// errCloneRestart is ER_CLONE_RESTART_FAILED (3707): the data was
	// cloned, but no supervisor restarts mysqld, which shuts down.
	errCloneRestart = 3707
	// cloneProgressInterval is how often a running clone's progress is read.
	cloneProgressInterval = time.Second
	// cloneKillWait bounds the wait for a killed CLONE statement to return.
	cloneKillWait = 30 * time.Second
)

// CloneFrom replaces this server's data with the donor's through CLONE
// INSTANCE, then waits for MySQL to come back and checks that the clone
// completed. The donor account needs BACKUP_ADMIN; this server's admin
// account CLONE_ADMIN.
//
// CLONE ends by restarting mysqld. Under systemd the statement's connection
// is simply lost; without a supervisor it fails with 3707 and the server
// shuts down. Both mean the data phase is over: the server is started (a
// no-op when systemd already did) and clone_status must read Completed.
func CloneFrom(j *jobs.Job, h Host, p proto.SnapshotParams) (proto.SnapshotResult, error) {
	ctx := j.Context()
	start := time.Now()
	res := proto.SnapshotResult{Method: methodClone}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()

	if _, err := refuseRunningReplica(ctx, db); err != nil {
		return res, err
	}
	if err := refuseOnSource(ctx, db); err != nil {
		return res, err
	}
	if _, err := installClonePlugin(ctx, db, j.Logf); err != nil {
		return res, err
	}
	// The host is validated (no quote, no colon) and the port an integer.
	donor := p.Host + ":" + strconv.Itoa(p.Port)
	if _, err := db.ExecContext(ctx, "SET GLOBAL clone_valid_donor_list = "+quoteString(donor)); err != nil {
		return res, fmt.Errorf("atur clone_valid_donor_list: %w", err)
	}

	// One session runs CLONE: the sql_mode read is the one the password
	// literal is escaped for, and its id is what KILL QUERY stops.
	conn, err := db.Conn(ctx)
	if err != nil {
		return res, err
	}
	// began is the server's clock before CLONE, to tell this attempt's
	// clone_status row from an older one (BEGIN_TIME has milliseconds).
	var connID int64
	var mode, began string
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID(), @@SESSION.sql_mode, CAST(NOW(3) AS CHAR)").Scan(&connID, &mode, &began); err != nil {
		conn.Close()
		return res, fmt.Errorf("baca sesi MySQL: %w", err)
	}
	// MySQL takes no placeholder in IDENTIFIED BY; the literal never reaches
	// a log or an error.
	password := quotePassword(p.Password, strings.Contains(mode, "NO_BACKSLASH_ESCAPES"))
	stmt := "CLONE INSTANCE FROM " + quoteString(p.User) + "@" + quoteString(p.Host) + ":" + strconv.Itoa(p.Port) +
		" IDENTIFIED BY " + password
	if p.SSL {
		// The donor offers TLS: never fall back to a plaintext copy.
		stmt += " REQUIRE SSL"
	}

	j.Logf("info", "cloning from %s as %s", donor, p.User)
	j.Progress(0, "CLONE dimulai")
	done := make(chan error, 1)
	go func() {
		// Cancellation goes through KILL QUERY, not by dropping the session.
		_, err := conn.ExecContext(context.WithoutCancel(ctx), stmt)
		conn.Close()
		done <- err
	}()
	cloneErr, err := waitClone(j, db, connID, done, p.EstimateBytes)
	if err != nil {
		return res, err
	}
	if cloneErr != nil && !isCloneRestart(cloneErr) {
		return res, cloneFailure(ctx, db, cloneErr, began, p.Password, password)
	}

	// The data is replaced: MySQL must come back even if the job is
	// cancelled now, or a server nothing supervises stays down.
	j.Logf("info", "clone data phase finished (%v); waiting for MySQL to restart, cancellation is ignored until it is up", cloneErr)
	j.Progress(99, "menunggu MySQL hidup kembali")
	db.Close() // its sessions died with the old server
	if err := restartAfterClone(context.WithoutCancel(ctx), h, cloneErr); err != nil {
		return res, err
	}
	// The checks below report on data that is already replaced: a late
	// cancel does not stop them, and the job keeps its result.
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartReadyTimeout)
	defer cancel()
	after, err := h.Open(vctx)
	if LoginRefused(err) {
		// The donor's accounts replaced this server's: the stored one is
		// gone. The data is in place; the panel checks clone_status and
		// the GTID set once the user reconnects with the master's account.
		res.AdminReplaced = true
		res.Bytes = p.EstimateBytes
		res.TookMs = time.Since(start).Milliseconds()
		j.Logf("warn", "clone finished; the stored admin account is refused now (the master's accounts came with the copy): %v", err)
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("CLONE selesai, tetapi login ke MySQL gagal: %w", err)
	}
	defer after.Close()
	st, err := readCloneStatus(vctx, after)
	if err != nil {
		return res, err
	}
	if st.State != "Completed" || st.ErrNo != 0 {
		return res, fmt.Errorf("CLONE tidak selesai: status %s, %d %s", st.State, st.ErrNo, st.ErrMsg)
	}
	if res.GTIDExecuted, err = localGTIDExecuted(vctx, after); err != nil {
		return res, err
	}
	if prog, err := readCloneProgress(vctx, after); err == nil {
		res.Bytes = prog.data
	}
	if res.Bytes == 0 {
		res.Bytes = p.EstimateBytes
	}
	res.TookMs = time.Since(start).Milliseconds()
	if ctx.Err() != nil {
		j.Logf("warn", "cancellation arrived after the clone data phase; the clone result stands")
	}
	j.Logf("info", "clone completed: %d bytes, gtid_executed %s", res.Bytes, res.GTIDExecuted)
	return res, nil
}

// waitClone reports the clone's progress until the statement returns, and
// returns its error. When the job is cancelled it kills the statement and
// returns the context's error.
func waitClone(j *jobs.Job, db *sql.DB, connID int64, done <-chan error, estimate uint64) (cloneErr, err error) {
	ctx := j.Context()
	tick := time.NewTicker(cloneProgressInterval)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err, nil
		case <-ctx.Done():
			kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, kerr := db.ExecContext(kctx, "KILL QUERY "+strconv.FormatInt(connID, 10))
			cancel()
			if kerr != nil {
				j.Logf("warn", "kill clone statement: %v", kerr)
			}
			select {
			case err := <-done:
				if err != nil && isCloneRestart(err) {
					// Too late to stop: the data is replaced and MySQL
					// must be brought back.
					j.Logf("warn", "cancel came after the clone data phase: %v", err)
					return err, nil
				}
				j.Logf("info", "clone statement stopped: %v", err)
			case <-time.After(cloneKillWait):
				j.Logf("warn", "clone statement still running %s after KILL QUERY", cloneKillWait)
			}
			return nil, ctx.Err()
		case <-tick.C:
			// The server may be dropping data or shutting down: skip a tick.
			prog, err := readCloneProgress(ctx, db)
			if err != nil {
				continue
			}
			total := prog.estimate
			if total == 0 {
				total = estimate
			}
			msg := "CLONE: " + megabytes(prog.data)
			if prog.stage != "" {
				msg = fmt.Sprintf("CLONE tahap %s: %s", prog.stage, megabytes(prog.data))
			}
			if total > 0 {
				msg += " dari " + megabytes(total)
			}
			j.Progress(percentOf(prog.data, total), msg)
		}
	}
}

// isCloneRestart reports whether err ends a CLONE whose data phase is over:
// 3707 (no supervisor to restart mysqld) or the session lost to the restart.
func isCloneRestart(err error) bool {
	var me *drv.MySQLError
	if errors.As(err, &me) {
		return me.Number == errCloneRestart
	}
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, drv.ErrInvalidConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		strings.Contains(err.Error(), "invalid connection")
}

// restartAfterClone brings MySQL back after the clone's data phase. After
// 3707 the server is still shutting down: it waits for it to stop first, so
// the readiness check does not see the old server. Every step is bounded by
// restartReadyTimeout; callers pass a context that is not cancelled.
func restartAfterClone(ctx context.Context, h Host, cloneErr error) error {
	svc := h.Service()
	var me *drv.MySQLError
	if errors.As(cloneErr, &me) && me.Number == errCloneRestart {
		waitStopped(ctx, svc, restartReadyTimeout)
	}
	sctx, cancel := context.WithTimeout(ctx, restartReadyTimeout)
	defer cancel()
	if err := svc.Start(sctx); err != nil {
		return fmt.Errorf("CLONE selesai, tetapi MySQL gagal dinyalakan: %w", err)
	}
	if err := svc.WaitReady(ctx, restartReadyTimeout); err != nil {
		return fmt.Errorf("CLONE selesai, tetapi MySQL belum siap: %w", err)
	}
	return nil
}

// waitStopped polls until svc reports the server stopped, or timeout passes.
func waitStopped(ctx context.Context, svc ServiceController, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if active, err := svc.IsActive(ctx); err != nil || !active {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ReadyPollInterval):
		}
	}
}

// cloneState is the row of performance_schema.clone_status.
type cloneState struct {
	State  string
	ErrNo  int
	ErrMsg string
}

func readCloneStatus(ctx context.Context, db *sql.DB) (cloneState, error) {
	var st cloneState
	err := db.QueryRowContext(ctx, "SELECT STATE, ERROR_NO, ERROR_MESSAGE FROM performance_schema.clone_status").
		Scan(&st.State, &st.ErrNo, &st.ErrMsg)
	if err != nil {
		return st, fmt.Errorf("baca clone_status: %w", err)
	}
	return st, nil
}

// cloneProgress sums performance_schema.clone_progress: bytes copied and
// estimated over every stage, and the stage in progress.
type cloneProgress struct {
	stage          string
	data, estimate uint64
}

func readCloneProgress(ctx context.Context, db *sql.DB) (cloneProgress, error) {
	var p cloneProgress
	rows, err := db.QueryContext(ctx, "SELECT STAGE, STATE, DATA, ESTIMATE FROM performance_schema.clone_progress ORDER BY ID")
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var stage, state string
		var data, estimate sql.NullInt64
		if err := rows.Scan(&stage, &state, &data, &estimate); err != nil {
			return p, err
		}
		p.data += uint64(max(data.Int64, 0))
		p.estimate += uint64(max(estimate.Int64, 0))
		if state == "In Progress" {
			p.stage = stage
		}
	}
	return p, rows.Err()
}

// cloneFailure words a CLONE that failed before its data phase ended,
// preferring clone_status's message when that row is this attempt's (it
// began at or after began, in the server's clock). Neither password form
// survives.
func cloneFailure(ctx context.Context, db *sql.DB, err error, began, password, literal string) error {
	msg := err.Error()
	var errNo int
	var errMsg string
	serr := db.QueryRowContext(ctx, "SELECT ERROR_NO, ERROR_MESSAGE FROM performance_schema.clone_status WHERE BEGIN_TIME >= ?", began).
		Scan(&errNo, &errMsg)
	if serr == nil && errNo != 0 {
		msg = fmt.Sprintf("%d %s", errNo, errMsg)
	}
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, literal, "'***'"), password, "***")
	return fmt.Errorf("CLONE gagal: %s", msg)
}
