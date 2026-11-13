package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
