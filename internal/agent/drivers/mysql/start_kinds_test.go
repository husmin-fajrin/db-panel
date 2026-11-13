package mysql_test

import (
	"context"
	"encoding/json"
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

// wantKind is what the start-result file should hold for one kind: absent,
// or the start whose id is id, over or not.
type wantKind struct {
	id   string // "" : no entry
	over bool
}

func checkKinds(t *testing.T, step string, rec mysql.StartResultRecord, want map[string]wantKind) {
	t.Helper()
	for _, k := range []string{proto.QuarantineStartLate, proto.QuarantineStartFailed, proto.QuarantineStartBypassed} {
		w := want[k]
		got, ok := rec.Kinds[k]
		switch {
		case w.id == "" && ok:
			t.Errorf("%s: kinds[%s] = %+v, want none", step, k, got)
		case w.id == "":
		case !ok:
			t.Errorf("%s: kinds[%s] missing, want start %s (over %v)", step, k, w.id, w.over)
		case got.Decided.UTC().Format(time.RFC3339Nano) != w.id || got.Over != w.over:
			t.Errorf("%s: kinds[%s] = %+v, want start %s (over %v)", step, k, got, w.id, w.over)
		}
	}
}

// newStartID is the result and start id of the one start event in ev
// after the first skip events (other events are left out).
func newStartID(t *testing.T, ev *eventLog, skip int) (string, string) {
	t.Helper()
	var res, id []string
	for _, e := range ev.all()[skip:] {
		switch e.Type {
		case proto.EventQuarantineStart:
			var p proto.QuarantineStartEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			r := p.Result
			if p.Bypassed && r != proto.QuarantineStartBypassed {
				r += "+bypassed"
			}
			res, id = append(res, r), append(id, p.StartID)
		case proto.EventQuarantineStartFailed:
			var p proto.QuarantineStartFailedEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			res, id = append(res, proto.QuarantineStartFailed), append(id, p.StartID)
		}
	}
	if len(res) != 1 {
		t.Fatalf("start events of the round = %v, want one", res)
	}
	return res[0], id[0]
}

// 19 and 20: the starts the guard judges keep the last start of each bad
// kind in the start-result file: failed until MySQL comes up, bypassed
// until a start uses the lock, late for good. Each start is judged by the
// guard itself (a new agent on the same files judges the start it finds);
// the snapshot's Kinds are the file's, and a restarted agent gives the
// same ids.
func TestStartKindsJudged(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db, "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	dir := pinDir(t, "quarantined", guardCandidateKey)
	// MySQL runs without the lock line: a lock file older than mysqld says
	// it was bypassed.
	if err := mysql.WriteStartLock(context.Background(), inst.Host()); err != nil {
		t.Fatal(err)
	}
	lock, _ := mysql.StartLockPath(inst.Layout())
	seen := filepath.Join(t.TempDir(), "replicas-seen")
	path := filepath.Join(filepath.Dir(seen), "start-result")
	agentOnFiles := func() *guardRig {
		g := newGuardRig(t, inst, dir)
		g.d.SeenPath = seen
		return g
	}
	read := func() mysql.StartResultRecord {
		t.Helper()
		rec, ok := readStartResultFile(t, path)
		if !ok {
			t.Fatal("no start-result file")
		}
		return rec
	}
	judge := func(g *guardRig, wantResult string) string {
		t.Helper()
		skip := len(g.ev.all())
		g.tickAt(time.Now())
		res, id := newStartID(t, g.ev, skip)
		if res != wantResult || id == "" {
			t.Fatalf("judged start = %s (id %q), want %s with an id", res, id, wantResult)
		}
		return id
	}
	setLockTime := func(at time.Time) {
		t.Helper()
		if err := os.Chtimes(lock, at, at); err != nil {
			t.Fatal(err)
		}
	}

	setLockTime(time.Now().Add(-time.Hour))
	b1 := judge(agentOnFiles(), proto.QuarantineStartBypassed)
	checkKinds(t, "bypassed", read(), map[string]wantKind{proto.QuarantineStartBypassed: {b1, false}})

	setLockTime(time.Now()) // written after mysqld started: proves nothing, an ok start
	judge(agentOnFiles(), proto.QuarantineStartOK)
	checkKinds(t, "ok after bypassed", read(), map[string]wantKind{proto.QuarantineStartBypassed: {b1, true}})

	setLockTime(time.Now().Add(-time.Hour))
	execAll(t, db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	g := agentOnFiles()
	l1 := judge(g, proto.QuarantineStartLate+"+bypassed")
	checkKinds(t, "late and bypassed", read(), map[string]wantKind{
		proto.QuarantineStartLate: {l1, false}, proto.QuarantineStartBypassed: {l1, false}})
	if rec := read(); rec.Result != proto.QuarantineStartLate || rec.Decided.UTC().Format(time.RFC3339Nano) != l1 {
		t.Errorf("worst result = %s at %v, want late at %s", rec.Result, rec.Decided, l1)
	}

	inst.Stop(t)
	setLockTime(time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC))
	f, err := os.OpenFile(inst.ErrorLog, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) +
		" 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file " + lock + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	f1 := judge(g, proto.QuarantineStartFailed)
	rec := read()
	checkKinds(t, "failed", rec, map[string]wantKind{proto.QuarantineStartLate: {l1, false},
		proto.QuarantineStartFailed: {f1, false}, proto.QuarantineStartBypassed: {l1, false}})
	if rec.Kinds[proto.QuarantineStartFailed].InitFile != lock {
		t.Errorf("failed init_file = %q, want %s", rec.Kinds[proto.QuarantineStartFailed].InitFile, lock)
	}
	if rec.Result != proto.QuarantineStartLate {
		t.Errorf("worst result after a failed start = %s, want late kept", rec.Result)
	}

	inst.Restart(t)
	judge(g, proto.QuarantineStartOK)
	checkKinds(t, "ok after failed", read(), map[string]wantKind{proto.QuarantineStartLate: {l1, false},
		proto.QuarantineStartFailed: {f1, true}, proto.QuarantineStartBypassed: {l1, true}})

	// 20: the snapshot carries the file's state, in the order late,
	// failed, bypassed; a restarted agent gives the same ids.
	want := []proto.StartKindState{
		{Kind: proto.QuarantineStartLate, StartID: l1},
		{Kind: proto.QuarantineStartFailed, StartID: f1, InitFile: lock, Over: true},
		{Kind: proto.QuarantineStartBypassed, StartID: l1, Over: true},
	}
	for name, d := range map[string]*mysql.Driver{"running": g.d, "restarted": agentOnFiles().d} {
		sr := d.Snapshot(context.Background()).StartResult
		if sr == nil || len(sr.Kinds) != len(want) {
			t.Fatalf("%s: snapshot StartResult = %+v, want %d kinds", name, sr, len(want))
		}
		for i, w := range want {
			got := sr.Kinds[i]
			if got.AgeSec < 0 || got.AgeSec > 600 {
				t.Errorf("%s: kinds[%d] age = %d, want in [0,600]", name, i, got.AgeSec)
			}
			got.AgeSec = 0
			if got != w {
				t.Errorf("%s: kinds[%d] = %+v, want %+v", name, i, got, w)
			}
		}
	}
}

// 20 (file side): Kinds read from the file whatever it holds: an unknown
// key or an unreadable kinds is ignored (the worst result still reads), a
// decided time in the future is age 0, a long init_file is cut on a rune
// boundary.
func TestStartKindsSnapshotFromFile(t *testing.T) {
	dir := pinDir(t, "quarantined", guardCandidateKey)
	r := newPinRig(t, nil, dir)
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	long := "aa" + strings.Repeat("€", 2000)
	b, _ := json.Marshal(map[string]any{
		"result": "late", "decided": now.Add(-time.Minute),
		"kinds": map[string]any{
			"bypassed": map[string]any{"decided": now.Add(-30 * time.Second), "over": true},
			"failed":   map[string]any{"decided": now.Add(time.Hour), "init_file": long},
			"late":     map[string]any{"decided": now.Add(-time.Minute)},
			"weird":    map[string]any{"decided": now},
			"other":    "not an object",
		},
	})
	write(string(b))
	sr := r.d.StartResult(now)
	if sr == nil || sr.Result != "late" || len(sr.Kinds) != 3 {
		t.Fatalf("StartResult = %+v, want late with 3 kinds", sr)
	}
	if k := sr.Kinds[0]; k.Kind != "late" || k.AgeSec != 60 || k.StartID != now.Add(-time.Minute).Format(time.RFC3339Nano) || k.Over {
		t.Errorf("kinds[0] = %+v", k)
	}
	if k := sr.Kinds[1]; k.Kind != "failed" || k.AgeSec != 0 || len(k.InitFile) > 4096 || len(k.InitFile) < 4093 || !utf8.ValidString(k.InitFile) {
		t.Errorf("kinds[1] = %s age %d init_file %d bytes (valid %v)", k.Kind, k.AgeSec, len(k.InitFile), utf8.ValidString(k.InitFile))
	}
	if k := sr.Kinds[2]; k.Kind != "bypassed" || k.AgeSec != 30 || !k.Over {
		t.Errorf("kinds[2] = %+v", k)
	}

	for name, kinds := range map[string]string{"not an object": `"x"`, "a list": `[1,2]`, "bad entry": `{"late":{"decided":"bukan waktu"}}`} {
		write(`{"result":"bypassed","decided":"2026-11-12T09:59:00Z","kinds":` + kinds + `}`)
		sr := r.d.StartResult(now)
		if sr == nil || sr.Result != "bypassed" || sr.AgeSec != 60 || len(sr.Kinds) != 0 {
			t.Errorf("%s: StartResult = %+v, want bypassed without kinds", name, sr)
		}
	}
}

// 22: the state goes with the file where the file is cleared: entering a
// new quarantine, a finished rebuild (the guard's clearing outside
// quarantine is TestStartResultClearedOutsideQuarantine).
func TestStartKindsGoWithTheFile(t *testing.T) {
	r := newPinRig(t, nil, pinDir(t, "master", ""))
	path := filepath.Join(filepath.Dir(r.d.SeenPath), "start-result")
	if err := r.d.RecordJudgedStart(mysql.StartResultRecord{Result: "late", Decided: time.Now()}, true); err != nil {
		t.Fatal(err)
	}
	if rec, ok := readStartResultFile(t, path); !ok || len(rec.Kinds) != 2 {
		t.Fatalf("file = %+v (exists %v), want late and bypassed kinds", rec, ok)
	}
	_ = r.d.SetQuarantined(guardCandidateKey, "")
	if _, ok := readStartResultFile(t, path); ok {
		t.Error("start-result (with its kinds) survived entering quarantine")
	}

	dir := pinDir(t, "quarantined", strictSource)
	p := newPinRig(t, nil, dir)
	path = filepath.Join(filepath.Dir(p.d.SeenPath), "start-result")
	if err := p.d.RecordJudgedStart(mysql.StartResultRecord{Result: "failed", Decided: time.Now(), InitFile: "/x"}, false); err != nil {
		t.Fatal(err)
	}
	if s := p.d.StartResult(time.Now()); s == nil || len(s.Kinds) != 1 {
		t.Fatalf("StartResult = %+v, want a failed kind", s)
	}
	if !p.d.MarkRebuildLoaded("10.0.0.1", 3306) {
		t.Fatal("no load mark toward the pinned source")
	}
	if err := p.d.SettleRebuilt("10.0.0.1", 3306); err != nil {
		t.Fatal(err)
	}
	if _, ok := readStartResultFile(t, path); ok {
		t.Error("start-result (with its kinds) still there after the rebuild settled")
	}
}
