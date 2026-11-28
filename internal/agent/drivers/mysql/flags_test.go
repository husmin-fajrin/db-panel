package mysql_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
)

// flagCombo is one read_only/super_read_only combination.
type flagCombo struct {
	name    string
	ro, sro bool
}

// flagCombos are the four combinations; "mix" (read_only OFF,
// super_read_only ON) lets every account write, "read_only_only" refuses
// ordinary accounts only (readonly_mix_test.go).
var flagCombos = []flagCombo{
	{"off_off", false, false},
	{"mix", false, true},
	{"read_only_only", true, false},
	{"on_on", true, true},
}

// peerIn is a reachable peer in combination c whose gtid_executed holds
// uuid's transactions.
func peerIn(c flagCombo, uuid string) mysql.PeerState {
	return mysql.PeerState{Reachable: true, ReadOnly: c.ro, SuperReadOnly: c.sro, GTIDExecuted: uuid + ":1-3"}
}

// wantParamError fails unless err is a *commands.ParamError holding every
// one of parts.
func wantParamError(t *testing.T, what string, err error, parts ...string) {
	t.Helper()
	var pe *commands.ParamError
	if !errors.As(err, &pe) {
		t.Errorf("%s = %v, want a refusal", what, err)
		return
	}
	for _, p := range parts {
		if !strings.Contains(pe.Msg, p) {
			t.Errorf("%s = %q, want it to hold %q", what, pe.Msg, p)
		}
	}
}

const (
	mixFlagsText      = "read_only=OFF, super_read_only=ON"
	appsOnlyFlagsText = "terkunci untuk aplikasi saja (read_only=ON, super_read_only=OFF)"
)

func TestFlagHelpersAgent(t *testing.T) {
	for _, c := range flagCombos {
		wantNo := c.name == "on_on"
		wantApps := c.name == "off_off" || c.name == "mix"
		if got := mysql.NoWrites(c.ro, c.sro); got != wantNo {
			t.Errorf("%s: NoWrites = %v, want %v", c.name, got, wantNo)
		}
		if got := mysql.AppsWrite(c.ro); got != wantApps {
			t.Errorf("%s: AppsWrite = %v, want %v", c.name, got, wantApps)
		}
	}
}

// TestResumeFlagCombinations: a promote run on a server whose
// applications can write resumes (never verifies again or rolls back);
// with super_read_only still on it finishes the opening first. Only both
// ON takes the verified path; read_only ON with super_read_only OFF is
// refused with nothing changed.
func TestResumeFlagCombinations(t *testing.T) {
	want := map[string]mysql.OpenStep{
		"off_off":        mysql.OpenResume,
		"mix":            mysql.OpenFinish,
		"read_only_only": mysql.OpenRefuse,
		"on_on":          mysql.OpenVerify,
	}
	for _, c := range flagCombos {
		if got := mysql.ResumeOpen(c.ro, c.sro); got != want[c.name] {
			t.Errorf("%s: step %d, want %d", c.name, got, want[c.name])
		}
	}
}

// TestGuardCandidateFlagCombinations: at the deadline the guard takes a
// candidate whose applications can write for promoted, never for a
// replica; read_only ON with super_read_only OFF keeps the lock; only a
// candidate read-only for every account starts, and after the grace
// ends, the way to opening the master.
func TestGuardCandidateFlagCombinations(t *testing.T) {
	want := map[string][2]mysql.GuardStep{
		"off_off":        {mysql.GuardPromoted, mysql.GuardPromoted},
		"mix":            {mysql.GuardPromoted, mysql.GuardPromoted},
		"read_only_only": {mysql.GuardAppsLocked, mysql.GuardAppsLocked},
		"on_on":          {mysql.GuardGraceStart, mysql.GuardGraceDone},
	}
	for _, c := range flagCombos {
		for i, inGrace := range []bool{false, true} {
			if got := mysql.GuardCandidateStep(peerIn(c, "u"), inGrace); got != want[c.name][i] {
				t.Errorf("%s, in grace %v: step %d, want %d", c.name, inGrace, got, want[c.name][i])
			}
		}
	}
	for _, inGrace := range []bool{false, true} {
		if got := mysql.GuardCandidateStep(mysql.PeerState{Err: "unreachable"}, inGrace); got != mysql.GuardUnreachable {
			t.Errorf("unreachable, in grace %v: step %d", inGrace, got)
		}
	}
}

// TestReadListedFlagCombinations: a listed replica holding this server's
// transactions is its successor when its applications can write, keeps
// the lock when it is locked for applications only, and passes only when
// read-only for every account; without this server's transactions it is
// never a successor.
func TestReadListedFlagCombinations(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	const host = "10.9.9.7:3306"
	for _, c := range flagCombos {
		for _, holds := range []bool{true, false} {
			peer := &fakePeer{}
			st := peerIn(c, uuid)
			if !holds {
				st.GTIDExecuted = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-9"
			}
			peer.setAt(host, st)
			restore := mysql.SetReadPeer(peer.read)
			unreadable, writable, appsLocked := mysql.ReadListed(context.Background(), guardCandidate, []string{host}, uuid)
			restore()
			wantW, wantA := "", ""
			if holds {
				switch c.name {
				case "off_off", "mix":
					wantW = host
				case "read_only_only":
					wantA = host
				}
			}
			if len(unreadable) != 0 || writable != wantW || strings.Join(appsLocked, ",") != wantA {
				t.Errorf("%s, holds this server's transactions %v: unreadable %v, writable %q, apps-locked %v; want writable %q, apps-locked %q",
					c.name, holds, unreadable, writable, appsLocked, wantW, wantA)
			}
		}
	}
}

// TestUnlockCandidateFlagCombinations: mysql.unlockPromote opens the
// master only on a candidate read-only for every account. The refusal
// keeps its old text for a writable candidate, names both flags for the
// mix, and for read_only ON with super_read_only OFF says what was read,
// where, and the way out.
func TestUnlockCandidateFlagCombinations(t *testing.T) {
	const readAt = "10.9.9.10:3306"
	const oldText = "kandidat " + guardCandidateKey + " bisa ditulis (%s) dan mungkin sudah menjadi master; kunci tidak dibuka"
	for _, c := range flagCombos {
		err := mysql.UnlockCandidateRefusal(guardCandidateKey, readAt, peerIn(c, "u"))
		var pe *commands.ParamError
		errors.As(err, &pe)
		switch c.name {
		case "on_on":
			if err != nil {
				t.Errorf("%s: %v, want the unlock to go on", c.name, err)
			}
		case "off_off":
			if pe == nil || pe.Msg != fmt.Sprintf(oldText, "super_read_only=OFF") {
				t.Errorf("%s: %v, want the old text", c.name, err)
			}
		case "mix":
			if pe == nil || pe.Msg != fmt.Sprintf(oldText, mixFlagsText) {
				t.Errorf("%s: %v, want the old text naming both flags", c.name, err)
			}
		default:
			wantParamError(t, c.name, err, guardCandidateKey, readAt, appsOnlyFlagsText, "aplikasi biasa tidak bisa menulis",
				"SUPER atau CONNECTION_ADMIN bisa", "Kunci kembali", "kunci tidak dibuka", "ulangi promote")
			if pe != nil && strings.Contains(pe.Msg, "mungkin sudah menjadi master") {
				t.Errorf("%s: %q calls the candidate a possible master", c.name, pe.Msg)
			}
		}
	}
	wantParamError(t, "unreachable", mysql.UnlockCandidateRefusal(guardCandidateKey, readAt, mysql.PeerState{Err: "x"}), "tidak bisa dibaca")
}

// lockedPeerRefusals are the places that need a peer locked for every
// account before they go on: the planned promote's two reads of the old
// master and the repoint's read of an old source that answers.
var lockedPeerRefusals = []struct {
	name    string
	refusal func(string, mysql.PeerState) error
	mixText string // what the refusal of the mix holds
}{
	{"verifyOldSource", mysql.VerifyOldRefusal, mixFlagsText},
	{"recheckOldSource", mysql.RecheckOldRefusal, "bisa ditulis lagi"},
	{"repointOldSource", mysql.OldSourceRefusal, mixFlagsText},
}

// TestOldSourceFlagCombinations: the planned promote (steps 2 and 4) and
// the repoint go on only past an old master or source read-only for
// every account.
func TestOldSourceFlagCombinations(t *testing.T) {
	const key = "10.9.9.1:3306"
	for _, p := range lockedPeerRefusals {
		for _, c := range flagCombos {
			err := p.refusal(key, peerIn(c, "u"))
			switch c.name {
			case "on_on":
				if err != nil {
					t.Errorf("%s %s: %v, want it to go on", p.name, c.name, err)
				}
			case "mix":
				wantParamError(t, p.name+" "+c.name, err, key, p.mixText)
			default:
				wantParamError(t, p.name+" "+c.name, err, key)
			}
		}
		wantParamError(t, p.name+" unreachable", p.refusal(key, mysql.PeerState{Err: "x"}), key, "tidak bisa dibaca")
	}
}

// TestNewMasterFlagCombinations: mysql.quarantine and mysql.repoint take
// a new master for promoted only when its applications can write.
func TestNewMasterFlagCombinations(t *testing.T) {
	const key = "10.9.9.2:3306"
	for _, refused := range []string{"karantina ditolak", "repoint ditolak"} {
		for _, c := range flagCombos {
			err := mysql.NewMasterRefusal(key, peerIn(c, "u"), refused)
			switch c.name {
			case "off_off", "mix":
				if err != nil {
					t.Errorf("%s %s: %v, want it to go on", refused, c.name, err)
				}
			case "read_only_only":
				wantParamError(t, refused+" "+c.name, err, key, appsOnlyFlagsText, "belum dipromosikan", refused)
			default:
				wantParamError(t, refused+" "+c.name, err, key, "masih read-only (super_read_only=ON)", refused)
			}
		}
	}
}
