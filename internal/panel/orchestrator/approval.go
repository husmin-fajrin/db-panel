package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
// (errors.Is ErrAgentOutdated) saying the rebuild is refused.
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
	return res, nil
}

// requireRebuildApproval is nil only when serverID's agent needs no
// approval or holds one; without one it is a rebuildApprovalError naming
// the command to run there with the agent's code.
func (r *Runner) requireRebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error) {
	res, err := r.RebuildApproval(ctx, serverID)
	if err != nil {
		return res, err
	}
	if !res.Required || res.Approved {
		return res, nil
	}
	r.log.Info("rebuild refused: no local approval on the server", "server_id", serverID)
	return res, rebuildApprovalError{fmt.Sprintf("Bangun ulang %s butuh persetujuan di server itu. Jalankan di server itu "+
		"sebagai root: sudo dbpanel agent approve %s, lalu coba lagi.", r.nameOf(serverID), res.Code)}
}

// rebuildApprovalForJob is requireRebuildApproval on the server of a
// create_replica job that rebuilds an old master and is failed, partial
// or waiting (what Retry and Approve run again); nil for any other job.
// It asks the agent, so callers run it before startMu.
func (r *Runner) rebuildApprovalForJob(jobID string) error {
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
	_, err = r.requireRebuildApproval(ctx, j.ServerID)
	return err
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
	if rerr != nil || res.Code == "" {
		r.log.Warn("needs local approval; code not readable", "server_id", serverID, "err", logErr(rerr))
		return stepError(fmt.Sprintf("Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. "+
			"Buka form Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, "+
			"lalu klik Ulangi.", name))
	}
	return stepError(fmt.Sprintf("Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. "+
		"Jalankan di server itu sebagai root: sudo dbpanel agent approve %s, lalu klik Ulangi.", name, res.Code))
}
