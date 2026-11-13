package mysql

import (
	"os"
	"path/filepath"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// StartResultFile is the worst start of MySQL since the quarantine (spec
// 5.5): kept on disk so an agent restart or a lost event does not lose it,
// and carried in every health snapshot while the role pin is quarantined.
const StartResultFile = "/var/lib/dbpanel/start-result"

// maxInitFile caps the init_file a snapshot carries.
const maxInitFile = 4096

// startResult is the content of the start-result file.
type startResult struct {
	Result   string    `json:"result"`
	Decided  time.Time `json:"decided"` // wall clock: the only time that survives a restart
	InitFile string    `json:"init_file,omitempty"`
}

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

// recordStartResult stores r when it is as bad as or worse than what the
// file holds (an equal rank replaces it: the newer decision wins), or when
// the file holds nothing readable.
func (d *Driver) recordStartResult(r startResult) error {
	path := d.startResultPath()
	var stored startResult
	ok, err := readJSON(path, &stored)
	switch {
	case err != nil:
		d.warn("stored start result unreadable; replaced", "file", path, "err", err)
	case ok && startResultRank(stored.Result) == 0:
		d.warn("stored start result has an unknown result; replaced", "file", path, "result", stored.Result)
	case ok && startResultRank(r.Result) < startResultRank(stored.Result):
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeJSON(path, r)
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
	if len(r.InitFile) > maxInitFile {
		r.InitFile = r.InitFile[:maxInitFile]
	}
	age := int64(now.Sub(r.Decided) / time.Second)
	if age < 0 {
		age = 0
	}
	return &proto.StartResultState{Result: r.Result, AgeSec: age, InitFile: r.InitFile}
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
// this quarantine.
func (d *Driver) setQuarantined(newSource, addr string) error {
	if !d.pins.RoleIs(roleQuarantined) {
		if err := removeFile(d.startResultPath()); err != nil && d.Logger != nil {
			d.Logger.Error("start result of an earlier quarantine not removed", "err", err)
		}
	}
	return d.pins.SetQuarantinedAt(newSource, addr)
}
