package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// guardRig is a driver over inst with its pins in dir (role, source), the
// events it emits recorded.
type guardRig struct {
	*pinRig
	inst *testmysql.Instance
	dir  string
	ev   *eventLog
	lock string
}

func newGuardRig(t *testing.T, inst *testmysql.Instance, dir string) *guardRig {
	t.Helper()
	r := &guardRig{pinRig: newPinRig(t, inst.Host(), dir), inst: inst, dir: dir, ev: &eventLog{}}
	r.d.Events = r.ev
	lock, err := mysql.StartLockPath(inst.Layout())
	if err != nil {
		t.Fatal(err)
	}
	r.lock = lock
	return r
}

func (r *guardRig) tickAt(now time.Time) {
	r.t.Helper()
	if err := r.d.GuardTick(context.Background(), now); err != nil {
		r.t.Fatalf("GuardTick: %v", err)
	}
}

// lineAndFile is lineAndFile for the rig's MySQL.
func (r *guardRig) lineAndFile() (line, file bool) {
	r.t.Helper()
	return lineAndFile(r.t, r.inst.Host(), r.lock)
}

// dropLine removes the agent's init_file line from my.cnf by hand.
func dropLine(t *testing.T, inst *testmysql.Instance, lock string) {
	t.Helper()
	b, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "init_file="+lock+"\n", "", 1)
	if s == string(b) {
		t.Fatalf("no init_file line in my.cnf:\n%s", b)
	}
	if err := os.WriteFile(inst.ConfigFile, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startEvents are the payloads of the EventQuarantineStart events in ev.
func startEvents(t *testing.T, ev *eventLog) []proto.QuarantineStartEvent {
	t.Helper()
	var out []proto.QuarantineStartEvent
	for _, e := range ev.all() {
		if e.Type != proto.EventQuarantineStart {
			continue
		}
		var p proto.QuarantineStartEvent
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// countingHook fails every config write while fail is set, counting the
// writes it sees.
type countingHook struct {
	fail  atomic.Bool
	calls atomic.Int32
}

func (h *countingHook) hook(string) error {
	h.calls.Add(1)
	if h.fail.Load() {
		return errors.New("tulis konfigurasi gagal (test)")
	}
	return nil
}

// TestGuardWritesStartLockForOldAgentQuarantine: a server quarantined by
// an older agent (pins only, no line, no file) gets its start lock on the
// guard's first tick.
func TestGuardWritesStartLockForOldAgentQuarantine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	if line, file := r.lineAndFile(); line || file {
		t.Fatalf("before the tick: line=%v file=%v", line, file)
	}
	r.tickAt(time.Now())
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after the tick: line=%v file=%v", line, file)
	}
	if st := r.d.StartLock(); st == nil || !st.Locked || !st.Present {
		t.Errorf("StartLock() = %+v, want locked", st)
	}
}

// TestGuardRewritesMissingFileAndLine: while quarantined, a lock file or
// line that went missing is written again on the next tick; a write that
// fails is tried again no sooner than a minute later.
func TestGuardRewritesMissingFileAndLine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	now := time.Now()
	r.tickAt(now)
	if line, file := r.lineAndFile(); !line || !file {
		t.Fatalf("after the first tick: line=%v file=%v", line, file)
	}

	if err := os.Remove(r.lock); err != nil {
		t.Fatal(err)
	}
	r.tickAt(now.Add(5 * time.Second))
	if _, file := r.lineAndFile(); !file {
		t.Error("lock file not written again")
	}
	// A file whose content changed is written again too.
	if err := os.WriteFile(r.lock, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := mysql.StartLockStatus(context.Background(), inst.Host()); st.Locked || st.Reason == "" {
		t.Errorf("status with changed content = %+v, want not locked with a reason", st)
	}
	r.tickAt(now.Add(10 * time.Second))
	if b, err := os.ReadFile(r.lock); err != nil || string(b) != mysql.StartLockContent {
		t.Errorf("lock file after the tick = %q, %v", b, err)
	}

	dropLine(t, inst, r.lock)
	r.tickAt(now.Add(15 * time.Second))
	if line, _ := r.lineAndFile(); !line {
		t.Error("init_file line not written again")
	}

	// mysqld refuses the config: no new attempt before a minute.
	hook := &countingHook{}
	hook.fail.Store(true)
	t.Cleanup(mysql.SetWriteConfigHook(hook.hook))
	dropLine(t, inst, r.lock)
	failAt := now.Add(20 * time.Second)
	r.tickAt(failAt)
	if n := hook.calls.Load(); n != 1 {
		t.Fatalf("config writes after a missing line = %d, want 1", n)
	}
	if st := r.d.StartLock(); st == nil || st.Locked {
		t.Errorf("StartLock() after a failed write = %+v, want not locked", st)
	}
	r.tickAt(failAt.Add(30 * time.Second))
	r.tickAt(failAt.Add(59 * time.Second))
	if n := hook.calls.Load(); n != 1 {
		t.Errorf("config writes within the minute = %d, want 1", n)
	}
	r.tickAt(failAt.Add(61 * time.Second))
	if n := hook.calls.Load(); n != 2 {
		t.Errorf("config writes after the minute = %d, want 2", n)
	}
	hook.fail.Store(false)
	r.tickAt(failAt.Add(2*time.Minute + time.Second))
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after the retry: line=%v file=%v", line, file)
	}
	if st := r.d.StartLock(); st == nil || !st.Locked {
		t.Errorf("StartLock() after the retry = %+v, want locked", st)
	}
}

// TestStartLockReleaseInterruptedNeverLeavesLineWithoutFile: a release
// cut short leaves the line with its file, or the file without its line,
// never the line without its file; the guard removes a file left alone.
func TestStartLockReleaseInterruptedNeverLeavesLineWithoutFile(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	h := inst.Host()
	ctx := context.Background()
	if err := mysql.WriteStartLock(ctx, h); err != nil {
		t.Fatal(err)
	}
	r := newGuardRig(t, inst, pinDir(t, "master", ""))

	// The block without the line cannot be written.
	hook := &countingHook{}
	hook.fail.Store(true)
	restore := mysql.SetWriteConfigHook(hook.hook)
	if err := mysql.ReleaseStartLock(ctx, h); err == nil {
		t.Fatal("release with the config write failing succeeded")
	}
	restore()
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after a failed config write: line=%v file=%v; want both", line, file)
	}

	// The file cannot be removed.
	restore = mysql.SetRemoveStartLockFile(func(string) error { return errors.New("hapus gagal (test)") })
	if err := mysql.ReleaseStartLock(ctx, h); err == nil {
		t.Fatal("release with the file removal failing succeeded")
	}
	restore()
	if line, file := r.lineAndFile(); line || !file {
		t.Errorf("after a failed removal: line=%v file=%v; want the file only", line, file)
	}

	// The guard removes a file without its line.
	r.tickAt(time.Now())
	if line, file := r.lineAndFile(); line || file {
		t.Errorf("after the tick: line=%v file=%v; want neither", line, file)
	}
	if st := r.d.StartLock(); st != nil {
		t.Errorf("StartLock() on a master without a line = %+v, want nil", st)
	}
}

// TestReleaseNeedsPersistedOn: on a replica the guard releases the start
// lock only once MySQL answers with read_only and super_read_only
// PERSISTed ON (or this agent run PERSISTed them); it never sets them.
func TestReleaseNeedsPersistedOn(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, "RESET PERSIST IF EXISTS read_only", "RESET PERSIST IF EXISTS super_read_only")
	r := newGuardRig(t, inst, pinDir(t, "replica", guardCandidateKey))
	now := time.Now()
	r.tickAt(now)
	r.tickAt(now.Add(5 * time.Second))
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("without the flags persisted: line=%v file=%v; want the lock kept", line, file)
	}
	if s, ro := persisted(t, db, "super_read_only"), persisted(t, db, "read_only"); s != "" || ro != "" {
		t.Errorf("the guard persisted super_read_only=%q read_only=%q", s, ro)
	}
	if st := r.d.StartLock(); st == nil || !st.Present {
		t.Errorf("StartLock() with the line still there = %+v, want present", st)
	}

	execAll(t, db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r.tickAt(now.Add(10 * time.Second))
	if line, file := r.lineAndFile(); line || file {
		t.Errorf("with the flags persisted: line=%v file=%v; want released", line, file)
	}
	if st := r.d.StartLock(); st != nil {
		t.Errorf("StartLock() after the release = %+v, want nil", st)
	}
}

// TestQuarantineStartEvents: the guard reports how a quarantined server
// came up: ok, late (a flag was off) or bypassed (@@init_file is not the
// agent's file although the file is older than mysqld).
func TestQuarantineStartEvents(t *testing.T) {
	t.Run("ok after a quarantine while down", func(t *testing.T) {
		r := newQuarantineRig(t)
		r.seenHosts("10.9.9.9")
		r.promoted()
		r.inst.Stop(t)
		decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
		r.inst.Restart(t)
		r.tick()
		lock, _ := mysql.StartLockPath(r.inst.Layout())
		if got := startEvents(t, r.ev); len(got) != 1 || got[0] != (proto.QuarantineStartEvent{Result: "ok", InitFile: lock}) {
			t.Errorf("start events = %+v, want one ok with init_file %s", got, lock)
		}
		r.tick()
		if got := startEvents(t, r.ev); len(got) != 1 {
			t.Errorf("start events after a second tick = %+v, want still one", got)
		}

		// The agent stops, someone opens writes, a new agent starts.
		execAll(t, r.db, "SET GLOBAL super_read_only = OFF")
		g := newGuardRig(t, r.inst, r.dir)
		g.tickAt(time.Now())
		if got := startEvents(t, g.ev); len(got) != 1 || got[0].Result != "late" || got[0].Bypassed {
			t.Errorf("start events of the new agent = %+v, want one late", got)
		}
		wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	})

	t.Run("bypassed and ok on a server started without the line", func(t *testing.T) {
		inst := testmysql.Start(t, testmysql.Options{})
		db := rootDB(t, inst)
		execAll(t, db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
		dir := pinDir(t, "quarantined", guardCandidateKey)
		if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
			t.Fatal(err)
		}
		lock, _ := mysql.StartLockPath(inst.Layout())
		hourAgo := time.Now().Add(-time.Hour)
		if err := os.Chtimes(lock, hourAgo, hourAgo); err != nil {
			t.Fatal(err)
		}
		r := newGuardRig(t, inst, dir)
		r.tickAt(time.Now())
		if got := startEvents(t, r.ev); len(got) != 1 || got[0] != (proto.QuarantineStartEvent{Result: "bypassed", Bypassed: true}) {
			t.Errorf("start events = %+v, want one bypassed", got)
		}
		if fi, err := os.Stat(lock); err != nil || fi.ModTime().After(hourAgo.Add(time.Second)) {
			t.Errorf("the guard rewrote an intact lock file: %v, %v", fi, err)
		}

		// A file written after mysqld started proves nothing either way.
		now := time.Now()
		if err := os.Chtimes(lock, now, now); err != nil {
			t.Fatal(err)
		}
		r = newGuardRig(t, inst, dir)
		r.tickAt(time.Now())
		if got := startEvents(t, r.ev); len(got) != 1 || got[0] != (proto.QuarantineStartEvent{Result: "ok"}) {
			t.Errorf("start events = %+v, want one ok", got)
		}
	})
}

// TestBootstrapFailureEventFromErrorLog: with MySQL down, a line of the
// error log newer than the lock file saying it could not open the lock
// file is reported once, at the line's time.
func TestBootstrapFailureEventFromErrorLog(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
		t.Fatal(err)
	}
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	inst.Stop(t)
	written := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(r.lock, written, written); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(inst.ErrorLog, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("2026-11-11T02:10:00.000000Z 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + r.lock + "\n" +
		"2026-11-11T03:18:19.026539Z 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + r.lock + "\n" +
		"2026-11-11T03:18:19.026600Z 0 [ERROR] [MY-010119] [Server] Aborting\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.tickAt(now)
	var got []proto.QuarantineStartFailedEvent
	for _, e := range r.ev.all() {
		if e.Type != proto.EventQuarantineStartFailed {
			t.Errorf("event %s", e.Type)
			continue
		}
		var p proto.QuarantineStartFailedEvent
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	at := time.Date(2026, 11, 11, 3, 18, 19, 26539000, time.UTC)
	if len(got) != 1 || got[0].File != r.lock || !got[0].At.Equal(at) {
		t.Fatalf("start failed events = %+v, want one for %s at %s", got, r.lock, at)
	}
	r.tickAt(now.Add(5 * time.Second))
	if n := len(r.ev.all()); n != 1 {
		t.Errorf("events after a tick without a new line = %d, want 1", n)
	}
}

// TestQuarantineStartReportRetriedUntilDelivered: a start report (late)
// and a failed start computed while the panel is not connected are kept
// and sent again each guard round until one is delivered: exactly one of
// each reaches the panel, none lost, none twice (FR-1, SA-5).
func TestQuarantineStartReportRetriedUntilDelivered(t *testing.T) {
	t.Run("late", func(t *testing.T) {
		inst := testmysql.Start(t, testmysql.Options{})
		// A writable MySQL on a server pinned quarantined: the first
		// round finds the flags off, a late start.
		r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
		r.ev.setOffline(true)
		now := time.Now()
		r.tickAt(now)
		r.tickAt(now.Add(5 * time.Second))
		if got := startEvents(t, r.ev); len(got) != 0 {
			t.Fatalf("delivered while offline: %+v", got)
		}
		r.ev.setOffline(false)
		r.tickAt(now.Add(10 * time.Second))
		if got := startEvents(t, r.ev); len(got) != 1 || got[0].Result != proto.QuarantineStartLate {
			t.Fatalf("start events once connected = %+v, want one late", got)
		}
		r.tickAt(now.Add(15 * time.Second))
		if got := startEvents(t, r.ev); len(got) != 1 {
			t.Errorf("start events after another round = %+v, want still one", got)
		}
	})

	t.Run("start failed", func(t *testing.T) {
		inst := testmysql.Start(t, testmysql.Options{})
		if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
			t.Fatal(err)
		}
		r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
		inst.Stop(t)
		written := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
		if err := os.Chtimes(r.lock, written, written); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(inst.ErrorLog, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("2026-11-11T03:18:19.026539Z 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + r.lock + "\n")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		r.ev.setOffline(true)
		now := time.Now()
		r.tickAt(now)
		if n := len(r.ev.all()); n != 0 {
			t.Fatalf("delivered while offline: %d events", n)
		}
		r.ev.setOffline(false)
		r.tickAt(now.Add(5 * time.Second))
		r.tickAt(now.Add(10 * time.Second))
		evs := r.ev.all()
		if len(evs) != 1 || evs[0].Type != proto.EventQuarantineStartFailed {
			t.Fatalf("events once connected = %+v, want one %s", evs, proto.EventQuarantineStartFailed)
		}
	})
}

// TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock: a health
// snapshot taken before the guard's first round on a quarantined server
// (every agent start) checks the start lock itself instead of saying "not
// checked yet", which the panel reads as unguarded (FR-2).
func TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
		t.Fatal(err)
	}
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	s := r.d.Snapshot(context.Background())
	if s.StartLock == nil || !s.StartLock.Locked || !s.StartLock.Present || s.StartLock.File != r.lock {
		t.Fatalf("StartLock of the first snapshot = %+v, want locked at %s", s.StartLock, r.lock)
	}
}

// TestReadTailOnlyRegularFiles (SA-6): the error log is read only when it
// is a regular file, opened without following a symlink and without
// blocking: a FIFO there (planted by the mysql user) fails at once
// instead of hanging the guard loop; a symlink is not followed.
func TestReadTailOnlyRegularFiles(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "error.log")
	if err := os.WriteFile(regular, []byte("baris satu\nbaris dua\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := mysql.ReadTail(regular, 9); err != nil || string(b) != "aris dua\n" {
		t.Fatalf("ReadTail(regular) = %q, %v", b, err)
	}
	fifo := filepath.Join(dir, "fifo.log")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := mysql.ReadTail(fifo, 1024)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("ReadTail(fifo) read a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadTail(fifo) blocked")
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if b, err := mysql.ReadTail(link, 1024); err == nil {
		t.Errorf("ReadTail(symlink) followed the link: %q", b)
	}
}

// TestBootstrapFailureReportedWhenGuardRecreatesFile (FR-4): the lock file
// was deleted while the agent was down and MySQL then refused to start;
// the guard writes the file again (a new mtime) and still reports the
// failed start from the log, which predates the new file.
func TestBootstrapFailureReportedWhenGuardRecreatesFile(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
		t.Fatal(err)
	}
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	inst.Stop(t)
	written := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	cfg, err := mysql.ManagedConfigPath(inst.Layout())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, written, written); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.lock); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(inst.ErrorLog, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("2026-11-11T03:18:19.026539Z 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + r.lock + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r.tickAt(time.Now())
	if _, file := r.lineAndFile(); !file {
		t.Fatal("the guard did not write the lock file again")
	}
	evs := r.ev.all()
	if len(evs) != 1 || evs[0].Type != proto.EventQuarantineStartFailed {
		t.Fatalf("events = %+v, want one %s", evs, proto.EventQuarantineStartFailed)
	}
}

// TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping (FR-13 b): on a
// quarantined server the guard puts read-only back before it runs the start
// lock's housekeeping, which may run --print-defaults.
func TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	db := rootDB(t, inst)
	var roAtFirstWrite atomic.Int32
	t.Cleanup(mysql.SetWriteConfigHook(func(string) error {
		if roAtFirstWrite.Load() == 0 {
			var sro bool
			if err := db.QueryRow("SELECT @@global.super_read_only").Scan(&sro); err == nil && sro {
				roAtFirstWrite.Store(1)
			} else {
				roAtFirstWrite.Store(2)
			}
		}
		return nil
	}))
	r.tickAt(time.Now())
	if got := roAtFirstWrite.Load(); got != 1 {
		t.Fatalf("super_read_only when the start lock was first written: state %d, want on (1)", got)
	}
}

// TestQuarantineGuardMakesNoAbortedHandshake (FR-13 a): the guard of a
// quarantined server learns whether MySQL is up from its own login, not
// from a greeting probe each round that MySQL counts as an aborted
// connection.
func TestQuarantineGuardMakesNoAbortedHandshake(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	db := rootDB(t, inst)
	aborted := func() int {
		t.Helper()
		var name string
		var n int
		if err := db.QueryRow("SHOW GLOBAL STATUS LIKE 'Aborted_connects'").Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	now := time.Now()
	r.tickAt(now)
	before := aborted()
	for i := 1; i <= 3; i++ {
		r.tickAt(now.Add(time.Duration(i) * 5 * time.Second))
	}
	if after := aborted(); after != before {
		t.Errorf("Aborted_connects grew by %d over three guard rounds, want 0", after-before)
	}
}

func TestScanBootstrapFailure(t *testing.T) {
	const file = "/etc/mysql/dbpanel-start-lock.sql"
	since := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	line := func(ts, f string) string {
		return ts + " 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + f + "\n"
	}
	for _, c := range []struct {
		name, tail string
		want       time.Time
		ok         bool
	}{
		{"none", "2026-11-11T03:10:00Z 0 [System] ready\n", time.Time{}, false},
		{"older", line("2026-11-11T02:59:59Z", file), time.Time{}, false},
		{"other file", line("2026-11-11T03:10:00Z", "/tmp/x.sql"), time.Time{}, false},
		{"longer name", line("2026-11-11T03:10:00Z", file+".bak"), time.Time{}, false},
		{"newer", line("2026-11-11T02:00:00Z", file) + line("2026-11-11T03:10:00.5Z", file) + "partial line 2026",
			time.Date(2026, 11, 11, 3, 10, 0, 5e8, time.UTC), true},
		{"system time zone", line("2026-11-11T10:10:00.000000+07:00", file), time.Date(2026, 11, 11, 3, 10, 0, 0, time.UTC), true},
	} {
		got, ok := mysql.ScanBootstrapFailure([]byte(c.tail), file, since)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("%s: = %v, %v; want %v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestReleasedServerRestartsWithoutInitFile: a quarantined server rebuilt
// as a replica has its start lock released by the guard; opened later by
// hand, it restarts without init_file and writable.
func TestReleasedServerRestartsWithoutInitFile(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Applied || !res.StartLocked {
		t.Fatalf("quarantine = %+v", res)
	}
	if ok, err := r.d.Pins().SettleRebuilt("10.9.9.9", 3306); err != nil || !ok {
		t.Fatalf("settle rebuilt = %v, %v", ok, err)
	}
	r.tick()
	lock, _ := mysql.StartLockPath(r.inst.Layout())
	if line, file := lineAndFile(t, r.inst.Host(), lock); line || file {
		t.Fatalf("after the release tick: line=%v file=%v", line, file)
	}
	execAll(t, r.db, "SET PERSIST super_read_only = OFF", "SET PERSIST read_only = OFF")
	r.inst.Restart(t)
	var initFile sql.NullString
	if err := r.db.QueryRow("SELECT @@global.init_file").Scan(&initFile); err != nil {
		t.Fatal(err)
	}
	if initFile.String != "" {
		t.Errorf("@@init_file after the restart = %q", initFile.String)
	}
	execAll(t, r.db, "CREATE DATABASE ditulis")
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file after the restart: %v", err)
	}
}

// TestQuarantineFailureAfterPinStillLocksStart: a quarantine that fails
// after the pin is written (the promote lock cannot be removed) still
// writes the start lock.
func TestQuarantineFailureAfterPinStillLocksStart(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	t.Cleanup(mysql.SetClearLock(func(*mysql.Pins) error { return errors.New("hapus kunci gagal (test)") }))
	if resp := r.quarantine(quarantineParams); resp.OK {
		t.Fatalf("quarantine with ClearLock failing = %+v, want an error", resp)
	}
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Fatalf("role = %q", role)
	}
	lock, _ := mysql.StartLockPath(r.inst.Layout())
	if line, file := lineAndFile(t, r.inst.Host(), lock); !line || !file {
		t.Errorf("after the failed quarantine: line=%v file=%v; want the start lock", line, file)
	}
}

// TestConfigureKeepsStaleStartLockLine: an agent init_file line naming a
// lock file elsewhere (the layout changed) is kept byte for byte by
// configure, with its file; release removes it and its file.
func TestConfigureKeepsStaleStartLockLine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	h := inst.Host()
	old := openDir(t) + "/" + mysql.StartLockFile
	if err := os.WriteFile(old, []byte(mysql.StartLockContent), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := "init-file = " + old
	b, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.ConfigFile, append(b, "# BEGIN dbpanel\n[mysqld]\n"+stale+"\n# END dbpanel\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even with its file gone, configure keeps the line with its file.
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	configure(t, h, proto.ConfigureParams{Role: "replica"})
	content, _, err := mysql.ReadManagedContent(h.Layout())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, "\n"+stale+"\n") || !strings.Contains(content, "read_only=ON") {
		t.Errorf("block after configure:\n%s", content)
	}
	if b, err := os.ReadFile(old); err != nil || string(b) != mysql.StartLockContent {
		t.Errorf("stale lock file = %q, %v", b, err)
	}

	if err := mysql.ReleaseStartLock(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	content, _, _ = mysql.ReadManagedContent(h.Layout())
	if strings.Contains(content, "init") {
		t.Errorf("block after release:\n%s", content)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale lock file after release: %v", err)
	}
}

// TestReleaseProofLostOnceWritesOpen: this agent run's proof (it PERSISTed
// read-only ON on the quarantined server) no longer releases the start
// lock once read-only is opened afterwards; persisted ON again does.
func TestReleaseProofLostOnceWritesOpen(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	now := time.Now()
	r.tickAt(now)
	if line, file := r.lineAndFile(); !line || !file {
		t.Fatalf("after the quarantined tick: line=%v file=%v", line, file)
	}
	if err := r.d.Pins().SetRole("unassigned"); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, "SET PERSIST super_read_only = OFF", "SET PERSIST read_only = OFF")
	r.tickAt(now.Add(5 * time.Second))
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("with writes opened after the proof: line=%v file=%v; want the lock kept", line, file)
	}
	execAll(t, db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	r.tickAt(now.Add(10 * time.Second))
	if line, file := r.lineAndFile(); line || file {
		t.Errorf("with the flags persisted again: line=%v file=%v; want released", line, file)
	}
}

// releaseHelperEnv carries the layout (JSON) to the helper process that
// releases the start lock as "dbpanel agent set-role master" would.
const releaseHelperEnv = "DBPANEL_TEST_RELEASE_LAYOUT"

// TestReleaseHelperProcess is the other process of
// TestStartLockReleaseRaceAcrossProcesses; it does nothing otherwise. It
// releases the start lock, pausing before it removes the file so the
// guard of the first process has time to act, and prints the outcome.
func TestReleaseHelperProcess(t *testing.T) {
	raw := os.Getenv(releaseHelperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var l mysql.Layout
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	mysql.SetRemoveStartLockFile(func(path string) error {
		time.Sleep(150 * time.Millisecond)
		return os.Remove(path)
	})
	h := mysql.NewHost(l, nil, nil, probe.RealSystem(), nil)
	if err := mysql.ReleaseStartLock(context.Background(), h); err != nil {
		fmt.Println("release-error:", err)
		return
	}
	fmt.Println("released")
}

// TestStartLockReleaseRaceAcrossProcesses: the guard of a quarantined
// server keeps its start lock while another process (as the CLI would)
// releases it again and again; at no point does the managed config name
// the lock file while that file is missing.
func TestStartLockReleaseRaceAcrossProcesses(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	base := time.Now()
	r.tickAt(base)
	if line, file := r.lineAndFile(); !line || !file {
		t.Fatalf("after the first tick: line=%v file=%v", line, file)
	}
	layout, err := json.Marshal(inst.Layout())
	if err != nil {
		t.Fatal(err)
	}
	l := inst.Layout()
	exists := func() bool { _, err := os.Stat(r.lock); return err == nil }

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var violations, ticks, rewrites atomic.Int32
	wg.Add(2)
	go func() { // the agent's guard; each round a minute later, so no retry waits
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if err := r.d.GuardTick(context.Background(), base.Add(time.Duration(i)*61*time.Second)); err != nil {
				t.Errorf("GuardTick: %v", err)
			}
			ticks.Add(1)
		}
	}()
	go func() { // the observer: a line while the file is missing before and after reading it
		defer wg.Done()
		was := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			before := exists()
			line, err := mysql.StartLockLinePresent(l)
			if err != nil {
				continue
			}
			if line && !before && !exists() {
				violations.Add(1)
			}
			if line && !was {
				rewrites.Add(1)
			}
			was = line
			time.Sleep(time.Millisecond)
		}
	}()

	outcomes := map[string]int{}
	for range 20 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReleaseHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), releaseHelperEnv+"="+string(layout))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper process: %v\n%s", err, out)
		}
		switch {
		case strings.Contains(string(out), "\nreleased\n"), strings.HasPrefix(string(out), "released\n"):
			outcomes["released"]++
		default:
			outcomes["refused"]++
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("helper outcomes %v, guard ticks %d, line written again %d times", outcomes, ticks.Load(), rewrites.Load())
	if n := violations.Load(); n != 0 {
		t.Errorf("observed the init_file line without its file %d times", n)
	}
	if outcomes["released"] == 0 || rewrites.Load() == 0 {
		t.Error("no release, or no rewrite by the guard: the race was not exercised")
	}
}
