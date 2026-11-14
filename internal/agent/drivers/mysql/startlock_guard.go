package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The start lock's guard (spec 5.2): GuardTick runs guardStartLock first,
// for every role. While the role is quarantined the lock is kept (a file
// or line gone missing, or a file whose content changed, is written
// again) and checked, and a MySQL that does not start because it cannot
// open the lock file is reported from its error log. Once the role is
// master the lock is released at once (root decided); on a replica or an
// unassigned server only once MySQL answers with read_only and
// super_read_only PERSISTed ON (or this agent run PERSISTed them and
// they are still on), so the server still starts read-only without it.
// The guard never runs a SET in that branch.

const (
	// startLockRetry is how long after a failed write of the start lock
	// (or of its release) the next is tried, and how often its full check
	// (--print-defaults) runs while nothing changes.
	startLockRetry = time.Minute
	// bootLogTail is how much of the end of the error log is read for a
	// failed start.
	bootLogTail = 64 << 10
)

// Hooks of the start lock's release and of a quarantine; tests replace
// them.
var (
	removeStartLockFile = os.Remove
	// emitStartHook runs as a start report is queued (enqueue); tests see
	// what is on disk at that moment.
	emitStartHook func(d *Driver)
	clearLock     = (*Pins).ClearLock
)

// releaseStartLock removes the start lock: the agent's init_file lines
// first (the config validated by mysqld, put back otherwise), then the
// files they named and the file at StartLockPath. With no line it only
// removes the files. A file is removed only once the managed config, read
// again, no longer names it. All of it is one critical section under
// lockManagedConfig (the agent's guard and the CLI exclude each other).
// found is false when neither an agent init_file line nor a lock file
// existed (nothing to release). Idempotent.
func releaseStartLock(ctx context.Context, h Host) (found bool, err error) {
	l := h.Layout()
	unlock, err := lockManagedConfig(ctx, l)
	if err != nil {
		return false, err
	}
	defer unlock()
	content, _, err := readManagedContent(l)
	if err != nil {
		return false, err
	}
	paths := agentStartLockPaths(content)
	found = len(paths) > 0
	if len(paths) > 0 {
		rest := content
		for _, p := range paths {
			rest = withoutStartLockLine(rest, p)
		}
		if _, err := writeValidConfigLocked(ctx, h, rest); err != nil {
			return found, fmt.Errorf("baris init_file kunci start tidak bisa dihapus: %w", err)
		}
	}
	if p, err := StartLockPath(l); err == nil && !slices.Contains(paths, p) {
		paths = append(paths, p)
	}
	// Read again under the same lock: a writer that does not take it (an
	// older agent while "dbpanel agent set-role" runs) may have written
	// the line again meanwhile.
	if content, _, err = readManagedContent(l); err != nil {
		return found, err
	}
	if again := agentStartLockPaths(content); len(again) > 0 {
		return found, fmt.Errorf("baris init_file kunci start ditulis lagi (%s); berkas kunci tidak dihapus", strings.Join(again, ", "))
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			continue
		}
		err := removeStartLockFile(p)
		if err == nil {
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return found, fmt.Errorf("berkas kunci start %s tidak bisa dihapus: %w", p, err)
		}
	}
	return found, nil
}

// ReleaseStartLock is releaseStartLock, for "dbpanel agent set-role
// master".
func ReleaseStartLock(ctx context.Context, h Host) (found bool, err error) {
	return releaseStartLock(ctx, h)
}

// startLockIntact reports whether the managed config holds the agent's
// line for path and the file at path holds startLockContent.
func startLockIntact(l Layout, path string) bool {
	content, _, err := readManagedContent(l)
	if err != nil || !hasStartLockLine(content, path) {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && string(b) == startLockContent
}

// guardStartLock is the start lock's part of GuardTick at now, for any
// role (see above). The caller holds promoteMu.
func (d *Driver) guardStartLock(ctx context.Context, h Host, now time.Time) {
	role := d.pins.Role()
	if role == "" {
		return // the pins cannot be trusted: nothing changes
	}
	// Outside quarantine the state is cleared (a failed removal is retried
	// by the clear itself); only a quarantine's state is ever written again.
	if role != roleQuarantined {
		d.clearStartResult()
	} else {
		d.retryStartResult()
	}
	l := h.Layout()
	if role == roleQuarantined {
		d.keepStartLock(ctx, h, now)
		// Whether MySQL answers is what guardQuarantined, run just
		// before, learned from its own login: no greeting probe of our
		// own each round (FR-13 a).
		if !d.qUp {
			d.watchBootFailure(ctx, h, now)
		} else {
			d.bootSince = time.Time{}
		}
		return
	}
	content, _, err := readManagedContent(l)
	if err != nil {
		d.warnStartLock(now, "start lock: managed config unreadable", err)
		return
	}
	if len(agentStartLockPaths(content)) == 0 {
		d.clearStartLockState()
		// A file without its line: a release cut short.
		path, err := StartLockPath(l)
		if err != nil {
			return
		}
		if _, err := os.Lstat(path); err != nil || now.Before(d.startLockNextTry) {
			return
		}
		if _, err := releaseStartLock(ctx, h); err != nil {
			d.startLockNextTry = now.Add(startLockRetry)
			d.warn("start lock file left without its line not removed; tried again in a minute", "file", path, "err", err)
			return
		}
		d.log("start lock file left without its line removed", "file", path)
		return
	}
	if role != roleMaster && !d.readOnlyProvedNow(ctx, h) {
		// The lock goes only while MySQL answers and starts read-only
		// without it.
		d.checkStartLock(ctx, h, now, false)
		return
	}
	if now.Before(d.startLockNextTry) {
		d.checkStartLock(ctx, h, now, false)
		return
	}
	if _, err := releaseStartLock(ctx, h); err != nil {
		d.startLockNextTry = now.Add(startLockRetry)
		d.warn("start lock not released; tried again in a minute", "role", role, "err", err)
		d.checkStartLock(ctx, h, now, true)
		return
	}
	d.startLockNextTry = time.Time{}
	d.log("start lock released", "role", role)
	d.clearStartLockState()
}

// readOnlyProvedNow reports whether MySQL answers and either this agent
// run PERSISTed read_only and super_read_only ON since the quarantine
// (startLockProved) and both are still on (writes opened since, by
// mysql.setReadOnly or a promotion, PERSISTed them off: the proof is
// gone), or performance_schema shows both PERSISTed ON. It only reads.
func (d *Driver) readOnlyProvedNow(ctx context.Context, h Host) bool {
	db, err := h.Open(ctx)
	if err != nil {
		return false
	}
	defer db.Close()
	if d.startLockProved {
		var sro, ro bool
		if err := db.QueryRowContext(ctx, "SELECT @@global.super_read_only, @@global.read_only").Scan(&sro, &ro); err == nil && sro && ro {
			return true
		}
	}
	on, err := persistedReadOnlyOn(ctx, db)
	return err == nil && on
}

// persistedReadOnlyOn reports whether performance_schema.persisted_variables
// holds read_only and super_read_only ON: the next start of MySQL is
// read-only whatever init_file it reads.
func persistedReadOnlyOn(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT VARIABLE_NAME, VARIABLE_VALUE FROM performance_schema.persisted_variables "+
		"WHERE VARIABLE_NAME IN ('read_only', 'super_read_only')")
	if err != nil {
		return false, fmt.Errorf("baca persisted_variables: %w", err)
	}
	defer rows.Close()
	on := map[string]bool{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return false, err
		}
		on[strings.ToLower(name)] = strings.EqualFold(value, "ON") || value == "1"
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return on["read_only"] && on["super_read_only"], nil
}

// keepStartLock writes the start lock again when its line or file is
// missing, or the file's content changed, no sooner than startLockNextTry
// after a failure, and keeps its state for the health reports. The caller
// holds promoteMu.
func (d *Driver) keepStartLock(ctx context.Context, h Host, now time.Time) {
	l := h.Layout()
	path, err := StartLockPath(l)
	if err != nil {
		d.setStartLockState(proto.StartLockState{Reason: err.Error()})
		d.warnStartLock(now, "start lock cannot be placed", err)
		return
	}
	if startLockIntact(l, path) {
		d.checkStartLock(ctx, h, now, false)
		return
	}
	if now.Before(d.startLockNextTry) {
		d.checkStartLock(ctx, h, now, false)
		return
	}
	// A MySQL that failed to start on the missing (or changed) file did
	// so before the file written now: its error log is read from when the
	// managed config was last written instead of from the new file's
	// mtime (FR-4).
	if p, _, err := managedConfigPath(l); err == nil {
		if fi, err := os.Stat(p); err == nil && (d.bootSince.IsZero() || fi.ModTime().Before(d.bootSince)) {
			d.bootSince = fi.ModTime()
		}
	}
	d.lockStart(ctx, h)
	d.startLockCheckedAt = now
	if startLockIntact(l, path) {
		d.startLockNextTry = time.Time{}
		d.log("start lock written again", "file", path)
		return
	}
	d.startLockNextTry = now.Add(startLockRetry)
}

// checkStartLock runs startLockStatus into the state when force, when the
// state is not set, or when the last check is startLockRetry old.
func (d *Driver) checkStartLock(ctx context.Context, h Host, now time.Time, force bool) {
	if !force && d.StartLock() != nil && now.Sub(d.startLockCheckedAt) < startLockRetry && !now.Before(d.startLockCheckedAt) {
		return
	}
	d.setStartLockState(startLockStatus(ctx, h))
	d.startLockCheckedAt = now
}

// warnStartLock logs a start lock problem at most once per startLockRetry.
func (d *Driver) warnStartLock(now time.Time, msg string, err error) {
	if !now.Before(d.startLockNextTry) {
		d.startLockNextTry = now.Add(startLockRetry)
		d.warn(msg, "err", err)
	}
}

// watchBootFailure reads the end of MySQL's error log while a quarantined
// server's MySQL does not answer, and reports a line newer than the lock
// file, or than bootSince when the guard had to write the file again (and
// newer than the last one reported) saying mysqld could not open it
// (EventQuarantineStartFailed). Without a log_error option the log is not
// watched. The caller holds promoteMu.
func (d *Driver) watchBootFailure(ctx context.Context, h Host, now time.Time) {
	l := h.Layout()
	path, err := StartLockPath(l)
	if err != nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if d.bootLogAt.IsZero() || now.Sub(d.bootLogAt) >= startLockRetry || now.Before(d.bootLogAt) {
		opts, err := readEffectiveOptions(ctx, h)
		if err != nil {
			return
		}
		d.bootLog, d.bootLogAt = opts.LogError, now
		if d.bootLog != "" && !filepath.IsAbs(d.bootLog) {
			if l.DataDir == "" {
				d.bootLog = ""
			} else {
				d.bootLog = filepath.Join(l.DataDir, d.bootLog)
			}
		}
	}
	if d.bootLog == "" {
		return
	}
	tail, err := readTail(d.bootLog, bootLogTail)
	if err != nil {
		if now.Sub(d.bootLogWarnAt) >= startLockRetry || now.Before(d.bootLogWarnAt) {
			d.bootLogWarnAt = now
			d.warn("MySQL error log not read; a failed start is not reported", "file", d.bootLog, "err", err)
		}
		return
	}
	since := fi.ModTime()
	if !d.bootSince.IsZero() && d.bootSince.Before(since) {
		since = d.bootSince
	}
	at, ok := scanBootstrapFailure(tail, path, since)
	if !ok || !at.After(d.bootFailAt) {
		return
	}
	d.bootFailAt = at
	d.warn("MySQL of the quarantined server failed to start: it could not open the start lock file", "file", path, "at", at)
	d.recordStart(startResult{Result: proto.QuarantineStartFailed, Decided: now, InitFile: path}, false)
	d.enqueue(proto.EventQuarantineStartFailed, proto.QuarantineStartFailedEvent{File: path, At: at, StartID: startID(now)})
}

// sendRoundEvents queues what a guard round has for the panel once the
// round's work is done: the quarantine that took effect in the round
// (qNotice). It sends nothing (RunEvents does). The caller holds
// promoteMu.
func (d *Driver) sendRoundEvents() {
	if n := d.qNotice; n != nil {
		d.qNotice = nil
		d.enqueue(proto.EventQuarantined, *n)
	}
}

// readTail is the last n bytes of the regular file at path. The error log
// lives in a directory the mysql user owns and the guard runs as root: it
// is opened without following a symlink and without blocking (a FIFO
// planted there would otherwise hang the guard loop for good, with
// promoteMu held), and read only when what was opened is a regular file
// (SA-6).
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s bukan berkas biasa", path)
	}
	if off := fi.Size() - n; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	var b bytes.Buffer
	_, err = io.Copy(&b, io.LimitReader(f, n))
	return b.Bytes(), err
}

// bootstrapFailed is what mysqld logs when it cannot open its init_file.
const bootstrapFailed = "Failed to open the bootstrap file "

// scanBootstrapFailure returns the time of the newest line of tail (the
// end of an error log) that says mysqld failed to open the bootstrap file
// file and starts with an RFC 3339 time after since.
func scanBootstrapFailure(tail []byte, file string, since time.Time) (time.Time, bool) {
	var last time.Time
	found := false
	for _, ln := range strings.Split(string(tail), "\n") {
		ln = strings.TrimRight(ln, "\r \t")
		i := strings.Index(ln, bootstrapFailed)
		if i < 0 || ln[i+len(bootstrapFailed):] != file {
			continue
		}
		ts, _, _ := strings.Cut(ln, " ")
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !at.After(since) {
			continue
		}
		if !found || at.After(last) {
			last, found = at.UTC(), true
		}
	}
	return last, found
}

// reportQuarantineStart queues EventQuarantineStart for a start of MySQL
// on a quarantined server seen at now: late when a flag was off
// (flagsOff), bypassed when @@init_file (initFile) is not the agent's file
// although that file is older than the running mysqld (uptime seconds,
// give or take mysqlUptimeSlack), else ok.
func (d *Driver) reportQuarantineStart(h Host, now time.Time, flagsOff bool, initFile string, uptime int64) {
	ev := proto.QuarantineStartEvent{Result: proto.QuarantineStartOK, InitFile: initFile, StartID: startID(now)}
	path, err := StartLockPath(h.Layout())
	if err != nil || initFile != path {
		started := now.Add(-time.Duration(uptime+mysqlUptimeSlack) * time.Second)
		if err == nil {
			if fi, serr := os.Stat(path); serr == nil && fi.ModTime().Before(started) {
				ev.Bypassed = true
			}
		}
	}
	switch {
	case flagsOff:
		ev.Result = proto.QuarantineStartLate
	case ev.Bypassed:
		ev.Result = proto.QuarantineStartBypassed
	}
	if ev.Result != proto.QuarantineStartOK {
		d.warn("quarantined server started without the start lock holding", "result", ev.Result, "init_file", initFile)
	}
	d.recordStart(startResult{Result: ev.Result, Decided: now, InitFile: initFile}, ev.Bypassed)
	d.enqueue(proto.EventQuarantineStart, ev)
}

// recordStart keeps the judged start on disk (recordStartResult) before it
// is reported; a failed write is warned and the report is still queued.
func (d *Driver) recordStart(r startResult, bypassed bool) {
	if err := d.recordStartResult(r, bypassed); err != nil {
		d.warn("start result not kept on disk; the report is still sent", "result", r.Result, "err", err)
	}
}

// releaseForPromote releases the start lock a replica still carries
// before it is promoted: read-only is PERSISTed first, always (an earlier
// proof may be stale), so a promote job that fails after the release
// leaves the replica starting read-only without the lock.
func (d *Driver) releaseForPromote(ctx context.Context, h Host) error {
	d.promoteMu.Lock()
	defer d.promoteMu.Unlock()
	content, _, err := readManagedContent(h.Layout())
	if err != nil {
		return err
	}
	if len(agentStartLockPaths(content)) == 0 {
		return nil
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	err = persistReadOnly(ctx, db)
	db.Close()
	if err != nil {
		return err
	}
	d.startLockProved = true
	if _, err := releaseStartLock(ctx, h); err != nil {
		return err
	}
	d.log("start lock released for a promotion")
	d.clearStartLockState()
	return nil
}
