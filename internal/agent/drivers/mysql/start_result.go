package mysql

import (
	"encoding/json"
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

// recordStartResult keeps the start judged at r.Decided (bypassed: it did
// not use the lock) in the file: r replaces the worst start when it is as
// bad as or worse than the stored one (an equal rank replaces it: the
// newer decision wins) or when the file holds nothing readable, and the
// kinds of the file take the start (judgeKinds).
func (d *Driver) recordStartResult(r startResult, bypassed bool) error {
	path := d.startResultPath()
	var stored startResult
	ok, err := readJSON(path, &stored)
	worst := r
	switch {
	case err != nil:
		d.warn("stored start result unreadable; replaced", "file", path, "err", err)
		stored = startResult{}
	case ok && startResultRank(stored.Result) == 0:
		d.warn("stored start result has an unknown result; replaced", "file", path, "result", stored.Result)
	case ok && startResultRank(r.Result) < startResultRank(stored.Result):
		worst = stored
	}
	worst.Kinds = judgeKinds(stored.Kinds, r.Result, bypassed, r.Decided, r.InitFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeJSON(path, worst)
}

// StartResult is the stored worst start for the health snapshot at now: nil
// unless the role pin is quarantined and the file reads.
func (d *Driver) StartResult(now time.Time) *proto.StartResultState {
	if !d.pins.RoleIs(roleQuarantined) {
		return nil
	}
	var r startResult
	if ok, err := readJSON(d.startResultPath(), &r); err != nil || !ok {
		return nil
	}
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

// clearStartResult removes the file; a failure is warned.
func (d *Driver) clearStartResult() {
	if err := removeFile(d.startResultPath()); err != nil {
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
		if err := removeFile(d.startResultPath()); err != nil && d.Logger != nil {
			d.Logger.Error("start result of an earlier quarantine not removed", "err", err)
		}
	}
	return d.pins.SetQuarantinedAt(newSource, addr)
}
