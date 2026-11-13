package mysql

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// StartResultFile is the worst start of MySQL since the quarantine (spec
// 5.5): kept on disk so an agent restart or a lost event does not lose it,
// and carried in every health snapshot while the role pin is quarantined.
const StartResultFile = "/var/lib/dbpanel/start-result"

// maxInitFile caps the init_file a snapshot carries.
const maxInitFile = 4096

// startResult is the content of the start-result file: the worst start
// (Result, Decided, InitFile) and the last start of each bad kind (Kinds,
// keyed late, failed, bypassed), from which the panel's start alerts
// follow.
type startResult struct {
	Result   string                     `json:"result"`
	Decided  time.Time                  `json:"decided"` // wall clock: the only time that survives a restart
	InitFile string                     `json:"init_file,omitempty"`
	Kinds    map[string]startKindRecord `json:"kinds,omitempty"`
}

// startKindRecord is the last start of one bad kind.
type startKindRecord struct {
	Decided  time.Time `json:"decided"`             // when the agent judged that start; its id is derived from it
	InitFile string    `json:"init_file,omitempty"` // failed: the lock file
	Over     bool      `json:"over,omitempty"`      // a later start ended it (never set for late)
}

// startKindOrder is the order of the kinds in a snapshot.
var startKindOrder = []string{proto.QuarantineStartLate, proto.QuarantineStartFailed, proto.QuarantineStartBypassed}

// UnmarshalJSON reads the file as written, except that kinds is read
// entry by entry: an unknown key, an entry that does not read or has no
// decided time, or a kinds that is not an object is left out, and the
// worst result still reads.
func (r *startResult) UnmarshalJSON(b []byte) error {
	var raw struct {
		Result   string          `json:"result"`
		Decided  time.Time       `json:"decided"`
		InitFile string          `json:"init_file"`
		Kinds    json.RawMessage `json:"kinds"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*r = startResult{Result: raw.Result, Decided: raw.Decided, InitFile: raw.InitFile}
	var kinds map[string]json.RawMessage
	if len(raw.Kinds) == 0 || json.Unmarshal(raw.Kinds, &kinds) != nil {
		return nil
	}
	for _, k := range startKindOrder {
		var rec startKindRecord
		if v, ok := kinds[k]; ok && json.Unmarshal(v, &rec) == nil && !rec.Decided.IsZero() {
			if r.Kinds == nil {
				r.Kinds = map[string]startKindRecord{}
			}
			r.Kinds[k] = rec
		}
	}
	return nil
}

// judgeKinds updates kinds for the start judged at t with result (failed,
// or ok, late, bypassed: MySQL came up) and bypassed (the start did not
// use the lock: the event's Bypassed flag or the result bypassed); initFile
// is the lock file of a failed start.
func judgeKinds(kinds map[string]startKindRecord, result string, bypassed bool, t time.Time, initFile string) map[string]startKindRecord {
	if kinds == nil {
		kinds = map[string]startKindRecord{}
	}
	over := func(k string) {
		if rec, ok := kinds[k]; ok {
			rec.Over = true
			kinds[k] = rec
		}
	}
	if result == proto.QuarantineStartFailed {
		kinds[proto.QuarantineStartFailed] = startKindRecord{Decided: t, InitFile: initFile}
		return kinds
	}
	over(proto.QuarantineStartFailed)
	if bypassed || result == proto.QuarantineStartBypassed {
		kinds[proto.QuarantineStartBypassed] = startKindRecord{Decided: t}
	} else {
		over(proto.QuarantineStartBypassed)
	}
	if result == proto.QuarantineStartLate {
		kinds[proto.QuarantineStartLate] = startKindRecord{Decided: t}
	}
	return kinds
}

// startID is the identity of the start decided at t: t in UTC, as
// time.RFC3339Nano. The event of a start, the file and the snapshot all
// derive it from the one decided time.
func startID(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// startResultRank orders results from worst to best: late 4, failed 3,
// bypassed 2, ok 1, anything else 0.
func startResultRank(result string) int {
	switch result {
	case proto.QuarantineStartLate:
		return 4
	case proto.QuarantineStartFailed:
		return 3
	case proto.QuarantineStartBypassed:
		return 2
	case proto.QuarantineStartOK:
		return 1
	}
	return 0
}

// startResultPath is StartResultPath; else start-result beside SeenPath (so
// a test that moves SeenPath never touches the system's file); else
// StartResultFile.
func (d *Driver) startResultPath() string {
	switch {
	case d.StartResultPath != "":
		return d.StartResultPath
	case d.SeenPath != "":
		return filepath.Join(filepath.Dir(d.SeenPath), filepath.Base(StartResultFile))
	}
	return StartResultFile
}

// startState is the start-result state of the running agent: what the
// snapshot reports and the file persists. A value is never modified once
// published (Driver.startState); a writer, under promoteMu, publishes a new
// one. unsaved: the file does not hold rec yet (a write or a removal
// failed); every guard round retries it (retryStartResult). warned: a
// failed retry of this unsaved state was logged.
type startState struct {
	rec     startResult // Result "": nothing judged in this quarantine (no file)
	unsaved bool
	warned  bool
}

// currentStartState is the published state, loaded from the file on first
// use (an agent start). The snapshot calls it without promoteMu: the load
// is published only if nothing was published before (CompareAndSwap from
// nil), so it never overwrites a writer's state.
func (d *Driver) currentStartState() *startState {
	if st := d.startState.Load(); st != nil {
		return st
	}
	st := &startState{}
	path := d.startResultPath()
	var r startResult
	switch ok, err := readJSON(path, &r); {
	case err != nil:
		d.warn("stored start result unreadable; ignored", "file", path, "err", err)
	case ok:
		st.rec = r
	}
	if d.startState.CompareAndSwap(nil, st) {
		return st
	}
	return d.startState.Load()
}

// saveStartResult makes the file hold rec: written, or removed for an
// empty rec.
func (d *Driver) saveStartResult(rec startResult) error {
	path := d.startResultPath()
	if rec.Result == "" && len(rec.Kinds) == 0 {
		return removeFile(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeJSON(path, rec)
}

// setStartResult publishes rec, then persists it; a failed write leaves
// the state unsaved (retried each guard round) and is returned. The caller
// holds promoteMu.
func (d *Driver) setStartResult(rec startResult) error {
	d.startState.Store(&startState{rec: rec, unsaved: true})
	if err := d.saveStartResult(rec); err != nil {
		return err
	}
	d.startState.Store(&startState{rec: rec})
	return nil
}

// recordStartResult keeps the start judged at r.Decided (bypassed: it did
// not use the lock): r replaces the worst start when it is as bad as or
// worse than the kept one (an equal rank replaces it: the newer decision
// wins) or when nothing readable is kept, and the kinds take the start
// (judgeKinds). The state is published first, then written. The caller
// holds promoteMu.
func (d *Driver) recordStartResult(r startResult, bypassed bool) error {
	stored := d.currentStartState().rec
	worst := r
	switch {
	case stored.Result == "":
	case startResultRank(stored.Result) == 0:
		d.warn("stored start result has an unknown result; replaced", "file", d.startResultPath(), "result", stored.Result)
	case startResultRank(r.Result) < startResultRank(stored.Result):
		worst = stored
	}
	worst.Kinds = judgeKinds(maps.Clone(stored.Kinds), r.Result, bypassed, r.Decided, r.InitFile)
	return d.setStartResult(worst)
}

// retryStartResult writes a state whose write failed; a failure is warned
// once per state, a success logged. The caller holds promoteMu.
func (d *Driver) retryStartResult() {
	st := d.startState.Load()
	if st == nil || !st.unsaved {
		return
	}
	if err := d.saveStartResult(st.rec); err != nil {
		if !st.warned {
			d.warn("start result still not kept on disk; retried every guard round", "err", err)
			d.startState.Store(&startState{rec: st.rec, unsaved: true, warned: true})
		}
		return
	}
	d.startState.Store(&startState{rec: st.rec})
	d.log("start result kept on disk after an earlier failure")
}

// StartResult is the worst start and the state of the starts for the
// health snapshot at now: nil unless the role pin is quarantined and a
// start with a known result is kept. It reads the published state (no lock,
// no file work once loaded).
func (d *Driver) StartResult(now time.Time) *proto.StartResultState {
	if !d.pins.RoleIs(roleQuarantined) {
		return nil
	}
	r := d.currentStartState().rec
	if startResultRank(r.Result) == 0 {
		return nil
	}
	sr := &proto.StartResultState{Result: r.Result, AgeSec: startAge(now, r.Decided), InitFile: cutInitFile(r.InitFile),
		StartID: startID(r.Decided)}
	for _, k := range startKindOrder {
		rec, ok := r.Kinds[k]
		if !ok {
			continue
		}
		sr.Kinds = append(sr.Kinds, proto.StartKindState{Kind: k, StartID: startID(rec.Decided),
			AgeSec: startAge(now, rec.Decided), InitFile: cutInitFile(rec.InitFile), Over: rec.Over && k != proto.QuarantineStartLate})
	}
	return sr
}

// startAge is the whole seconds from decided to now, 0 when decided is
// later.
func startAge(now, decided time.Time) int64 {
	return max(int64(now.Sub(decided)/time.Second), 0)
}

// cutInitFile is s cut to at most maxInitFile bytes on a rune boundary.
func cutInitFile(s string) string {
	if len(s) <= maxInitFile {
		return s
	}
	n := maxInitFile
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// resetStartResult empties the state and removes the file (nothing to do
// when the state is empty and saved); a failed removal leaves the state
// unsaved, retried each guard round. The caller holds promoteMu.
func (d *Driver) resetStartResult() error {
	if st := d.startState.Load(); st != nil && !st.unsaved && st.rec.Result == "" && len(st.rec.Kinds) == 0 {
		return nil
	}
	return d.setStartResult(startResult{})
}

// clearStartResult is resetStartResult with a failure warned.
func (d *Driver) clearStartResult() {
	if err := d.resetStartResult(); err != nil {
		d.warn("start result file not removed", "err", err)
	}
}

// setQuarantined pins the role quarantined (Pins.SetQuarantinedAt). Coming
// from another role, the start result of an earlier quarantine goes first,
// before the pins are written; a failure is logged and does not stop the
// quarantine. A server already quarantined keeps its result: it belongs to
// this quarantine. While the role pin cannot be read nothing is known
// about the role, so the result is kept (as guardStartLock changes nothing
// then).
func (d *Driver) setQuarantined(newSource, addr string) error {
	if role, rerr := d.pins.roleErr(); rerr == nil && role != roleQuarantined {
		if err := d.resetStartResult(); err != nil && d.Logger != nil {
			d.Logger.Error("start result of an earlier quarantine not removed", "err", err)
		}
	}
	return d.pins.SetQuarantinedAt(newSource, addr)
}
