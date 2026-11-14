package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The rebuild of a quarantined old master needs root's approval on that
// server (spec 5.2): its agent refuses the commands that wipe it until
// root runs "dbpanel agent approve <kode>" there, so the panel alone
// cannot. The panel asks the agent before it starts, retries or approves
// such a job, and words a refusal in the middle of one.

// ErrRebuildNeedsApproval matches the refusal of a rebuild root on the
// server has not approved; its text names the command and the code.
var ErrRebuildNeedsApproval = errors.New("bangun ulang butuh persetujuan di server")

// rebuildApprovalError is the refusal of a rebuild without root's
// approval on its server.
type rebuildApprovalError struct{ msg string }

func (e rebuildApprovalError) Error() string        { return e.msg }
func (e rebuildApprovalError) Is(target error) bool { return target == ErrRebuildNeedsApproval }

// RebuildApproval asks serverID's agent (mysql.rebuildApproval) whether
// its rebuild needs root's approval and whether it has one; while it has
// none the agent keeps a request whose code root approves. An agent
// without the command: AgentNeedsUpgrade and an outdatedAgentError
// (errors.Is ErrAgentOutdated) saying the rebuild is refused. An answer
// that is not RebuildApprovalUsable is errApprovalUnreadable, so no
// caller sees its values.
func (r *Runner) RebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error) {
	ctx, cancel := context.WithTimeout(ctx, directReadTimeout)
	defer cancel()
	var res proto.RebuildApprovalResult
	err := r.d.Hub.Call(ctx, serverID, "mysql.rebuildApproval", nil, &res)
	if errors.Is(err, &proto.Error{Code: proto.CodeUnknownCommand}) {
		if r.d.Alerts != nil {
			r.d.Alerts.AgentNeedsUpgrade(serverID)
		}
		return proto.RebuildApprovalResult{}, outdatedAgentError{stepError(fmt.Sprintf("Agent perlu dimutakhirkan: agent di %s "+
			"belum mengenal persetujuan di server, jadi bangun ulang ditolak. Klik Pasang ulang agent di halaman server itu, "+
			"jalankan perintahnya, lalu coba lagi.", r.nameOf(serverID)))}
	}
	if err != nil {
		return proto.RebuildApprovalResult{}, err
	}
	if !RebuildApprovalUsable(res) {
		r.log.Warn("rebuild approval answer not usable", "server_id", serverID, "required", res.Required, "approved", res.Approved,
			"answer", rejectedFields(res))
		return proto.RebuildApprovalResult{}, errApprovalUnreadable
	}
	return res, nil
}

// ApprovalSeen is the agent's answer about root's approval that a
// rebuild's start, retry or approval decided on (for the audit): Read is
// false when no usable answer was read.
type ApprovalSeen struct {
	Read   bool
	Result proto.RebuildApprovalResult
}

// requireRebuildApproval is nil only when serverID's agent needs no
// approval or holds one; without one it is a rebuildApprovalError naming
// the command to run there with the agent's code. The answer it decided
// on goes to seen, when set.
func (r *Runner) requireRebuildApproval(ctx context.Context, serverID int64, seen *ApprovalSeen) error {
	res, err := r.RebuildApproval(ctx, serverID)
	if seen != nil {
		*seen = ApprovalSeen{Read: err == nil, Result: res}
	}
	if errors.Is(err, errApprovalUnreadable) {
		return rebuildApprovalError{fmt.Sprintf("Bangun ulang %s butuh persetujuan di server itu, tetapi kodenya tidak "+
			"bisa dibaca dari agent. Buka form Bangun ulang sebagai replika dan klik Periksa lagi.", r.nameOf(serverID))}
	}
	if err != nil {
		return err
	}
	if !res.Required || res.Approved {
		return nil
	}
	r.log.Info("rebuild refused: no local approval on the server", "server_id", serverID)
	return rebuildApprovalError{fmt.Sprintf("Bangun ulang %s butuh persetujuan di server itu. Jalankan di server itu "+
		"sebagai root: sudo dbpanel agent approve %s, lalu coba lagi.", r.nameOf(serverID), res.Code)}
}

// rebuildApprovalForJob is requireRebuildApproval on the server of a
// create_replica job that rebuilds an old master and is failed, partial
// or waiting (what Retry and Approve run again); nil for any other job.
// It asks the agent, so callers run it before startMu; the answer it
// decided on goes to seen, when set.
func (r *Runner) rebuildApprovalForJob(jobID string, seen *ApprovalSeen) error {
	j, _, err := r.d.Store.GetJob(jobID)
	if err != nil || j.Kind != KindCreateReplica {
		return nil
	}
	switch j.Status {
	case store.JobFailed, store.JobPartial, store.JobWaiting:
	default:
		return nil
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil || !st.RebuildOldMaster {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.base, callTimeout)
	defer cancel()
	return r.requireRebuildApproval(ctx, j.ServerID, seen)
}

// localApprovalErr words a needs_local_approval answer from serverID in
// a job: the command to run there with the code its agent gives now, or,
// when the code cannot be read, the way to it through the rebuild form.
// Any other err is returned as it is.
func (r *Runner) localApprovalErr(ctx context.Context, serverID int64, err error) error {
	if !errors.Is(err, &proto.Error{Code: proto.CodeNeedsLocalApproval}) {
		return err
	}
	name := r.nameOf(serverID)
	res, rerr := r.RebuildApproval(ctx, serverID)
	// A code only from a request (RebuildApproval let through only a
	// usable answer: its code is six ASCII digits).
	if rerr != nil || !res.Required || res.Approved || !validApprovalCode(res.Code) {
		r.log.Warn("needs local approval; code not readable", "server_id", serverID, "err", logErr(rerr))
		return stepError(fmt.Sprintf("Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. "+
			"Buka form Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, "+
			"lalu klik Ulangi.", name))
	}
	return stepError(fmt.Sprintf("Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. "+
		"Jalankan di server itu sebagai root: sudo dbpanel agent approve %s, lalu klik Ulangi.", name, res.Code))
}

// rejectedFields is an unusable answer's text fields for the panel's log
// (an admin diagnoses a broken agent from it): each quoted and cut.
func rejectedFields(res proto.RebuildApprovalResult) string {
	const n = 100
	return fmt.Sprintf("code=%q source=%q addr=%q remaining_sec=%d invalid_code=%q invalid=%q",
		truncate(res.Code, n), truncate(res.Source, n), truncate(res.Addr, n), res.RemainingSec,
		truncate(res.InvalidCode, n), truncate(res.Invalid, n))
}

// errApprovalUnreadable is an answer to mysql.rebuildApproval the panel
// cannot make sense of (see RebuildApprovalUsable): treated as one that
// could not be read, never shown or acted on.
var errApprovalUnreadable = errors.New("rebuild approval answer not usable")

// RebuildApprovalUsable reports whether an agent's answer can be shown and
// acted on: an approval names a source host:port and an IP address; a
// request names a source host:port and a code of exactly six ASCII
// digits (it goes into a command root runs) and time left. An answer that needs no
// approval, or holds one, carries no code: one that does is not usable
// (P-1), whatever the code.
func RebuildApprovalUsable(res proto.RebuildApprovalResult) bool {
	if !res.Required || res.Approved {
		if res.Code != "" {
			return false
		}
	}
	if !res.Required {
		return true
	}
	if !validApprovalSource(res.Source) {
		return false
	}
	if res.Approved {
		return net.ParseIP(res.Addr) != nil
	}
	// A request with no time left is not one root can approve: the agent
	// makes a new one on the next read (P-8).
	return validApprovalCode(res.Code) && res.RemainingSec > 0
}

func validApprovalCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// validApprovalSource reports whether source is host:port with a host
// that is an IP address or an RFC 1123 host name (validHostName) and a
// port 1-65535 (P-3): nothing else of the agent's reaches a page or a
// job through it.
func validApprovalSource(source string) bool {
	host, port, err := net.SplitHostPort(source)
	if err != nil || (net.ParseIP(host) == nil && !validHostName(host)) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// validHostName reports whether host is an RFC 1123 host name: ASCII
// letters, digits and hyphens in dot-separated labels of 1-63 characters,
// at most 253 characters in all.
func validHostName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// AgentQuote is an agent's own sentence as the panel shows it (P-2): after
// a label saying whose words they are, made one line and cut by
// monitor.AgentText, so it never reads as the panel's instruction.
func AgentQuote(server, text string) string {
	return fmt.Sprintf("Pesan dari agent %s (bukan dari panel): %s", server, monitor.AgentText(text, maxAgentQuote))
}

// maxAgentQuote bounds the agent's sentence in an AgentQuote.
const maxAgentQuote = 300

// invalidReasons word why an approval on file no longer holds, from the
// agent's InvalidCode (P-2); the agent's own sentence is never shown.
var invalidReasons = map[string]string{
	"expired":  "masa berlakunya sudah habis",
	"source":   "sumber yang dipin di server itu sudah berubah",
	"addr":     "nama sumbernya sekarang mengarah ke alamat lain",
	"dns":      "alamat untuk nama sumbernya tidak ditemukan (DNS)",
	"future":   "waktu persetujuannya di masa depan; periksa jam server itu",
	"lifetime": "masa berlakunya tidak wajar",
	"role":     "server itu tidak lagi dikarantina",
	"file":     "berkas persetujuannya tidak bisa dipakai",
}

// InvalidReason words why res's approval on file no longer holds: the
// panel's sentence for its InvalidCode, a fallback for a code this panel
// does not know, "" when the agent names no reason.
func InvalidReason(res proto.RebuildApprovalResult) string {
	if res.Invalid == "" && res.InvalidCode == "" {
		return ""
	}
	if reason, ok := invalidReasons[res.InvalidCode]; ok {
		return reason
	}
	return "alasannya tidak dikenali panel ini"
}
