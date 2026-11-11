package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// recordReplicasTimeout bounds mysql.recordReplicas: it is best effort and
// must not hold a job back.
const recordReplicasTimeout = 10 * time.Second

// recordReplicas asks the master's agent to look at its replica
// connections now and record them (spec 5.3), right after a replica was
// verified connected. It sends no parameters: the agent observes its own
// connections and never receives a replica's host or port from the panel.
// Best effort: an error (an older agent answers unknown_command) is only
// logged and never fails or delays the job beyond its bound.
func (r *Runner) recordReplicas(ctx context.Context, masterServerID int64) {
	ctx, cancel := context.WithTimeout(ctx, recordReplicasTimeout)
	defer cancel()
	var res proto.RecordReplicasResult
	if err := r.d.Hub.Call(ctx, masterServerID, "mysql.recordReplicas", nil, &res); err != nil {
		r.log.Debug("record replicas", "server_id", masterServerID, "err", logErr(err))
	}
}

// operatorInitFilesText is the sentence, with a leading space, that names
// the operator's own init_file options the start lock keeps MySQL from
// running; empty when there are none.
func operatorInitFilesText(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return " init_file milik operator berikut tidak dijalankan selama karantina: " + strings.Join(files, ", ") + "."
}

// startLockReasonText is the agent's reason as given, truncated; an agent
// that predates the start lock gives none.
func startLockReasonText(reason string) string {
	if reason == "" {
		return "agent tidak melaporkan kunci start (versi lama?)"
	}
	return truncate(reason, maxLogLine)
}
