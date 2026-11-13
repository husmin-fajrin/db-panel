package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func readStartResultFile(t *testing.T, path string) (mysql.StartResultRecord, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return mysql.StartResultRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var r mysql.StartResultRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, true
}

func TestStartResultWorstKept(t *testing.T) {
	dir := t.TempDir()
	d := &mysql.Driver{SeenPath: filepath.Join(dir, "replicas-seen")}
	path := filepath.Join(dir, "start-result")
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	seq := []string{"ok", "bypassed", "ok", "failed", "bypassed", "late", "failed"}
	want := []string{"ok", "bypassed", "bypassed", "failed", "failed", "late", "late"}
	for i, res := range seq {
		if err := d.RecordStartResult(mysql.StartResultRecord{Result: res, Decided: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		got, ok := readStartResultFile(t, path)
		if !ok || got.Result != want[i] {
			t.Errorf("after %v: file holds %+v (exists %v), want %s", seq[:i+1], got, ok, want[i])
		}
	}
	newer := base.Add(time.Hour)
	if err := d.RecordStartResult(mysql.StartResultRecord{Result: "late", Decided: newer, InitFile: "/x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readStartResultFile(t, path); !got.Decided.Equal(newer) || got.InitFile != "/x" {
		t.Errorf("a newer late did not replace the stored one: %+v", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("start-result mode = %v, %v; want 0644", fi, err)
	}
}

func TestStartResultSurvivesRestart(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newGuardRig(t, inst, dir)
	r.ev.setOffline(true)
	// MySQL is writable: the first round judges a late start.
	r.tickAt(time.Now())
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if got, ok := readStartResultFile(t, path); !ok || got.Result != "late" {
		t.Fatalf("start-result after the first round = %+v (exists %v), want late", got, ok)
	}
	var initFile sql.NullString
	if err := rootDB(t, inst).QueryRow("SELECT @@global.init_file").Scan(&initFile); err != nil {
		t.Fatal(err)
	}

	g := newGuardRig(t, inst, dir) // "restart": a new driver, the same files
	g.d.SeenPath = r.d.SeenPath
	want := proto.StartResultState{Result: "late", InitFile: initFile.String}
	check := func(when string) {
		t.Helper()
		got := g.d.Snapshot(context.Background()).StartResult
		if got == nil || got.Result != want.Result || got.InitFile != want.InitFile || got.AgeSec < 0 || got.AgeSec > 5 {
			t.Errorf("%s: StartResult = %+v, want %+v with age in [0,5]", when, got, want)
		}
	}
	check("before the new agent's first round")
	g.tickAt(time.Now())
	if got := startEvents(t, g.ev); len(got) != 1 || got[0].Result != "ok" {
		t.Errorf("start events of the new agent = %+v, want one ok", got)
	}
	check("after the new agent's ok")
}

// readAtEmit reads the start-result file whenever an event is emitted.
type readAtEmit struct {
	*eventLog
	path string
	seen []mysql.StartResultRecord
}

func (e *readAtEmit) Emit(ev proto.Event) error {
	b, err := os.ReadFile(e.path)
	var rec mysql.StartResultRecord
	if err == nil {
		_ = json.Unmarshal(b, &rec)
	}
	e.seen = append(e.seen, rec)
	return e.eventLog.Emit(ev)
}

func TestStartResultWrittenBeforeSend(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	em := &readAtEmit{eventLog: r.ev, path: filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")}
	r.d.Events = em
	r.tickAt(time.Now())
	if len(em.seen) == 0 {
		t.Fatal("no event emitted")
	}
	if got := startEvents(t, r.ev); len(got) != 1 || got[0].Result != "late" {
		t.Fatalf("start events = %+v, want one late", got)
	}
	if em.seen[0].Result != "late" {
		t.Errorf("start-result at the first Emit = %+v, want late already written", em.seen[0])
	}
}

func TestStartResultClearedOutsideQuarantine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newGuardRig(t, inst, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "bypassed", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if s := r.d.Snapshot(context.Background()).StartResult; s == nil || s.Result != "bypassed" {
		t.Fatalf("Snapshot.StartResult while quarantined = %+v, want bypassed", s)
	}
	if err := mustPins(t, dir).SetRole("replica"); err != nil {
		t.Fatal(err)
	}
	r.tickAt(time.Now())
	if _, ok := readStartResultFile(t, path); ok {
		t.Error("start-result still there after the role left quarantine")
	}
	if s := r.d.Snapshot(context.Background()).StartResult; s != nil {
		t.Errorf("Snapshot.StartResult after the role left quarantine = %+v, want nil", s)
	}
}

func TestStartFailedRecorded(t *testing.T) {
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
	r.tickAt(time.Now())
	got, ok := readStartResultFile(t, filepath.Join(filepath.Dir(r.d.SeenPath), "start-result"))
	if !ok || got.Result != "failed" || got.InitFile != r.lock {
		t.Errorf("start-result = %+v (exists %v), want failed with init_file %s", got, ok, r.lock)
	}
}

func logDriver(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

// A new quarantine, entered from another role by the Driver, starts with no
// start result; a repeated quarantine keeps it.
func TestStartResultClearedWhenQuarantineBegins(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	// The server is a master (a replica's mysql.quarantine is refused by
	// its role pin): any role but quarantined is a new quarantine.
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "late", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	wantQuarantined(t, r.db, r.dir, guardCandidateKey)
	if _, ok := readStartResultFile(t, path); ok {
		t.Error("start-result of the earlier quarantine survived entering quarantine")
	}
	if s := r.d.Snapshot(context.Background()).StartResult; s != nil {
		t.Errorf("Snapshot.StartResult right after entering quarantine = %+v, want nil", s)
	}
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "ok", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if s := r.d.Snapshot(context.Background()).StartResult; s == nil || s.Result != "ok" {
		t.Errorf("Snapshot.StartResult after an ok start = %+v, want ok", s)
	}

	// Already quarantined: the result belongs to this quarantine.
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "late", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if got, ok := readStartResultFile(t, path); !ok || got.Result != "late" {
		t.Errorf("start-result after a repeated quarantine = %+v (exists %v), want late kept", got, ok)
	}
}

func TestStartResultUnreadableStoredIsReplacedWithWarning(t *testing.T) {
	for name, content := range map[string]string{
		"not json":       "garbage",
		"unknown result": `{"result":"weird","decided":"2026-11-12T10:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var buf bytes.Buffer
			d := &mysql.Driver{SeenPath: filepath.Join(dir, "replicas-seen"), Logger: logDriver(&buf)}
			path := filepath.Join(dir, "start-result")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := d.RecordStartResult(mysql.StartResultRecord{Result: "ok", Decided: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if got, ok := readStartResultFile(t, path); !ok || got.Result != "ok" {
				t.Errorf("file = %+v (exists %v), want ok", got, ok)
			}
			if log := buf.String(); !strings.Contains(log, "level=WARN") || !strings.Contains(log, path) {
				t.Errorf("log lacks a warning naming %s:\n%s", path, log)
			}
		})
	}
}

// The start result is on disk by the time the report is queued (before
// emitStart queues it, hence before any send).
func TestStartResultWrittenBeforeReportQueued(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	var atQueue []mysql.StartResultRecord
	t.Cleanup(mysql.SetEmitStartHook(func(*mysql.Driver) {
		rec, _ := readStartResultFile(t, path)
		atQueue = append(atQueue, rec)
	}))
	r.tickAt(time.Now())
	if len(atQueue) != 1 || atQueue[0].Result != "late" {
		t.Errorf("start-result when the report was queued = %+v, want one late", atQueue)
	}
}

func TestStartResultWriteFailureDoesNotStopTheEvent(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	r := newGuardRig(t, inst, pinDir(t, "quarantined", guardCandidateKey))
	var buf bytes.Buffer
	r.d.Logger = logDriver(&buf)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.d.StartResultPath = filepath.Join(blocker, "start-result") // its parent is a regular file
	r.tickAt(time.Now())
	if got := startEvents(t, r.ev); len(got) != 1 || got[0].Result != "late" {
		t.Errorf("start events = %+v, want one late despite the failed write", got)
	}
	if !strings.Contains(buf.String(), "start result not kept on disk") {
		t.Errorf("no warning for the failed write:\n%s", buf.String())
	}
}

func TestSettleRebuiltClearsStartResult(t *testing.T) {
	dir := pinDir(t, "quarantined", strictSource)
	r := newPinRig(t, nil, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "late", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !r.d.MarkRebuildLoaded("10.0.0.1", 3306) {
		t.Fatal("no load mark toward the pinned source")
	}
	if err := r.d.SettleRebuilt("10.0.0.1", 3306); err != nil {
		t.Fatal(err)
	}
	if _, ok := readStartResultFile(t, path); ok {
		t.Error("start-result still there after the rebuild settled")
	}
}

func TestStartFailedThenOkKeepsFailed(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
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
	r.tickAt(time.Now())
	if s := r.d.Snapshot(context.Background()).StartResult; s == nil || s.Result != "failed" {
		t.Fatalf("StartResult after the failed start = %+v, want failed", s)
	}
	inst.Restart(t)
	r.tickAt(time.Now())
	if got := startEvents(t, r.ev); len(got) != 1 || got[0].Result != "ok" {
		t.Fatalf("start events after the restart = %+v, want one ok", got)
	}
	got, ok := readStartResultFile(t, filepath.Join(filepath.Dir(r.d.SeenPath), "start-result"))
	if !ok || got.Result != "failed" {
		t.Errorf("file = %+v (exists %v), want failed kept", got, ok)
	}
	if s := r.d.Snapshot(context.Background()).StartResult; s == nil || s.Result != "failed" {
		t.Errorf("StartResult after the ok start = %+v, want failed", s)
	}
}

func TestStartResultSnapshotValidatesFile(t *testing.T) {
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newPinRig(t, nil, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	write := func(rec mysql.StartResultRecord) {
		t.Helper()
		b, _ := json.Marshal(rec)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(mysql.StartResultRecord{Result: "weird", Decided: time.Now()})
	if s := r.d.StartResult(time.Now()); s != nil {
		t.Errorf("StartResult with an unknown result = %+v, want nil", s)
	}
	write(mysql.StartResultRecord{Result: "bypassed", Decided: time.Now(), InitFile: strings.Repeat("a", 10000)})
	if s := r.d.StartResult(time.Now()); s == nil || len(s.InitFile) != 4096 {
		t.Errorf("StartResult init_file length = %v, want 4096", s)
	}
}

// The dispatch path refuses an untrusted pin before it gets here, so the
// Driver's step is called directly: it must not read "unreadable" as
// "another role".
func TestStartResultKeptWhenRolePinUnreadable(t *testing.T) {
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newPinRig(t, nil, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if err := r.d.RecordStartResult(mysql.StartResultRecord{Result: "late", Decided: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, mysql.RoleFile), 0o666); err != nil {
		t.Fatal(err)
	}
	_ = r.d.SetQuarantined(guardCandidateKey, "") // whatever it returns with the pin untrusted
	if got, ok := readStartResultFile(t, path); !ok || got.Result != "late" {
		t.Errorf("start-result with an unreadable role pin = %+v (exists %v), want late kept", got, ok)
	}
}

func TestStartResultInitFileCutOnRuneBoundary(t *testing.T) {
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newPinRig(t, nil, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	// "aa" + 3-byte characters: byte 4096 falls inside one ((4096-2)%3 == 2).
	b, _ := json.Marshal(mysql.StartResultRecord{Result: "bypassed", Decided: time.Now(), InitFile: "aa" + strings.Repeat("\u20ac", 2000)})
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s := r.d.StartResult(time.Now())
	if s == nil || len(s.InitFile) > 4096 || len(s.InitFile) < 4093 || !utf8.ValidString(s.InitFile) {
		t.Errorf("InitFile = %d bytes, valid UTF-8 %v; want valid and 4093..4096 bytes", len(s.InitFile), s != nil && utf8.ValidString(s.InitFile))
	}
}
