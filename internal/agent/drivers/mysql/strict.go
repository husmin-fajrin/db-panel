package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// strictClass is what strict mode (spec 5.2) does with a command on a
// quarantined server.
type strictClass int

const (
	strictAllowed     strictClass = iota + 1 // runs without an approval
	strictProtected                          // needs a valid approval; refused with needs_local_approval and a request is made
	strictRoleRefused                        // refused on a quarantined server whatever the approval (roleRefuses)
)

// strictCommands classifies every command an agent registers. A command
// missing here is protected (strictGate); TestEveryRegisteredCommandClassified
// keeps the table and the registered commands equal.
var strictCommands = map[string]strictClass{
	// Reads, and the agent's own jobs.
	"probe":             strictAllowed,
	"job.status":        strictAllowed,
	"job.log":           strictAllowed,
	"job.cancel":        strictAllowed, // only stops an agent job; changes no data itself
	"monitor.snapshot":  strictAllowed,
	"monitor.configure": strictAllowed, // Bootstrap never changes an existing pin

	"mysql.status":      strictAllowed,
	"mysql.inventory":   strictAllowed,
	"mysql.connections": strictAllowed,
	"mysql.preflight":   strictAllowed,
	"mysql.binlogTail":  strictAllowed,
	// Stores an account proved to log in to a MySQL on this machine (any
	// other host is refused for every role); on a quarantined server only
	// an account with every privilege the grant check lists, so the guard
	// can still put read-only back. Needed for the rescue when the admin
	// account changed.
	"mysql.connect":     strictAllowed,
	"mysql.probeSource": strictAllowed, // reads the pinned source only
	"mysql.waitSynced":  strictAllowed, // waits and reads GTIDs only

	// The rescue of what the old master holds.
	"mysql.leftover":      strictAllowed,
	"mysql.salvage":       strictAllowed,
	"mysql.salvageList":   strictAllowed,
	"mysql.salvageRead":   strictAllowed,
	"mysql.salvageDelete": strictAllowed, // salvage files are outside MySQL
	// requarantine refuses another source.
	"mysql.quarantine":      strictAllowed,
	"mysql.recordReplicas":  strictAllowed,
	"mysql.rebuildApproval": strictAllowed,
	// {on:true} only tightens; {on:false} is refused by the role table.
	"mysql.setReadOnly": strictAllowed,
	// Stopping the applier protects the data from a DROP through
	// replication.
	"mysql.stopReplica": strictAllowed,
	// Stores the panel's CA file only, which the rescue path (leftover,
	// salvage) verifies the new master against; it touches no data, and
	// the panel is that CA already.
	"mysql.tlsTrust": strictAllowed,

	// The rebuild: the data, replication, config, restart and install.
	"mysql.dropDatabases":        strictProtected,
	"mysql.dumpFrom":             strictProtected,
	"mysql.cloneFrom":            strictProtected,
	"mysql.configureReplication": strictProtected,
	"mysql.setReplicationFilter": strictProtected,
	"mysql.startReplica":         strictProtected,
	"mysql.startUntil":           strictProtected,
	"mysql.resetReplica":         strictProtected,
	"mysql.configure":            strictProtected,
	"mysql.restart":              strictProtected,
	"mysql.install":              strictProtected,
	// Accounts (the agent's own sweep calls the driver, not the registry).
	"mysql.createReplUser": strictProtected,
	"mysql.createDumpUser": strictProtected,
	"mysql.dropUser":       strictProtected,
	"mysql.sweepDumpUsers": strictProtected,
	// MySQL's own key and certificate.
	"mysql.tlsRequest": strictProtected,
	"mysql.tlsInstall": strictProtected,

	// The role table refuses these on a quarantined server.
	"mysql.lockForPromote": strictRoleRefused,
	"mysql.promote":        strictRoleRefused,
	"mysql.repoint":        strictRoleRefused,
	"mysql.unlockPromote":  strictRoleRefused,
	"mysql.releaseRole":    strictRoleRefused,
}

// StrictClassNames is strictCommands by name: "allowed", "protected" or
// "role-refused".
func StrictClassNames() map[string]string {
	out := make(map[string]string, len(strictCommands))
	for cmd, c := range strictCommands {
		switch c {
		case strictAllowed:
			out[cmd] = "allowed"
		case strictProtected:
			out[cmd] = "protected"
		case strictRoleRefused:
			out[cmd] = "role-refused"
		}
	}
	return out
}

// strictGate is strict mode (spec 5.2), asked before every command's
// handler: on a quarantined server a command that is not allowed or left
// to the role table runs only under root's valid approval. Without one the
// answer is needs_local_approval with the request's code, made here.
// While the pin files cannot be read only the allowed commands pass (some
// handlers, mysql.install and mysql.sweepDumpUsers, never ask the pins).
func (d *Driver) strictGate(ctx context.Context, cmd string) error {
	role, err := d.pins.roleErr()
	if err != nil {
		if strictCommands[cmd] == strictAllowed {
			return nil
		}
		return &commands.ParamError{Msg: fmt.Sprintf("pin peran/sumber agent tidak bisa dipakai, perintah %s ditolak: %v", cmd, err)}
	}
	if role != roleQuarantined {
		return nil
	}
	switch strictCommands[cmd] {
	case strictAllowed, strictRoleRefused:
		return nil
	}
	if _, ok := d.pins.pinnedSource(); !ok {
		return &localPinError{kind: ErrSourcePinned, lae: &commands.LocalActionError{Msg: unsourcedQuarantineText(cmd)}}
	}
	now := d.now()
	// The source's name is looked up here, under no lock.
	if a, err := d.pins.CheckRebuildApproval(ctx, now); err == nil {
		d.logApprovalUse(cmd, a)
		return nil
	}
	req, err := d.ensureRebuildRequest(now)
	if err != nil {
		return &localPinError{kind: ErrRolePinned, lae: &commands.LocalActionError{Msg: fmt.Sprintf(
			"server ini dikarantina (quarantined); perintah %s butuh persetujuan root di server ini, "+
				"tetapi permintaan persetujuan tidak bisa dibuat: %v", cmd, err)}}
	}
	minutesLeft := int(math.Ceil(req.Created.Add(RebuildRequestTTL).Sub(now).Minutes()))
	if minutesLeft < 1 {
		minutesLeft = 1
	}
	return &localPinError{kind: ErrRolePinned, lae: &commands.LocalActionError{Msg: fmt.Sprintf(
		"server ini dikarantina (quarantined) mengikuti sumber %s; perintah %s butuh persetujuan root di server ini. "+
			"Jalankan di server ini: sudo dbpanel agent approve %s (kode berlaku %d menit lagi)", req.Source, cmd, req.Code, minutesLeft)}}
}

// ensureRebuildRequest is Pins.EnsureRebuildRequest one at a time in this
// process, so two refusals never write two codes.
func (d *Driver) ensureRebuildRequest(now time.Time) (RebuildRequest, error) {
	d.approvalMu.Lock()
	defer d.approvalMu.Unlock()
	return d.pins.EnsureRebuildRequest(now)
}

// logApprovalUse logs the first command an approval lets through, once
// per approval.
func (d *Driver) logApprovalUse(cmd string, a RebuildApproval) {
	key := a.Source + "|" + a.Approved.Format(time.RFC3339Nano)
	d.approvalMu.Lock()
	first := d.approvalLogged != key
	d.approvalLogged = key
	d.approvalMu.Unlock()
	if first && d.Logger != nil {
		d.Logger.Info("local rebuild approval used", "cmd", cmd, "source", a.Source, "addr", a.Addr,
			"approved", a.Approved, "expires", a.Expires)
	}
}

func (d *Driver) registerRebuildApproval(reg *commands.Registry) {
	reg.Register("mysql.rebuildApproval", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return d.rebuildApprovalState(ctx, d.now())
	})
}

// rebuildApprovalState is the answer to mysql.rebuildApproval at now: not
// required unless the role pin is quarantined; the approval when one
// holds; else the request (made when none is valid) with why an approval
// on file does not hold.
func (d *Driver) rebuildApprovalState(ctx context.Context, now time.Time) (proto.RebuildApprovalResult, error) {
	if d.pins == nil || d.pins.Role() != roleQuarantined {
		return proto.RebuildApprovalResult{}, nil
	}
	if _, ok := d.pins.pinnedSource(); !ok {
		return proto.RebuildApprovalResult{}, unsourcedQuarantineRefusal("mysql.rebuildApproval")
	}
	a, checkErr := d.pins.CheckRebuildApproval(ctx, now)
	if checkErr == nil {
		return proto.RebuildApprovalResult{
			Required:     true,
			Approved:     true,
			RemainingSec: int64(a.Expires.Sub(now) / time.Second),
			AgeSec:       max(0, int64(now.Sub(a.Approved)/time.Second)),
			Source:       a.Source,
			Addr:         a.Addr,
		}, nil
	}
	invalid := checkErr.Error()
	if _, has, err := d.pins.ReadRebuildApproval(); err == nil && !has {
		invalid = ""
	}
	req, err := d.ensureRebuildRequest(now)
	if err != nil {
		return proto.RebuildApprovalResult{}, err
	}
	return proto.RebuildApprovalResult{
		Required:     true,
		Code:         req.Code,
		Source:       req.Source,
		RemainingSec: int64(req.Created.Add(RebuildRequestTTL).Sub(now) / time.Second),
		Invalid:      invalid,
	}, nil
}
