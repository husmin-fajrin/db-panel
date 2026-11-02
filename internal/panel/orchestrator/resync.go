package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// "Resync ulang" (resync_replica) copies the master's data to a registered
// replica again and reconnects it, with the steps of create_replica minus
// "Pasang MySQL" and the same databases as recorded. The user confirmed,
// when starting it, the exact list of the replica's databases that are
// dropped (WipeList), so the job waits only for databases outside that
// list; CLONE is allowed although the replica is not empty. The replica is
// in state resyncing while the job runs, ready (and no longer paused) when
// it succeeds, failed when it fails or is canceled; it stays a replica of
// its master either way.

// ResyncParams are what the user chose in "Resync ulang".
type ResyncParams struct {
	ServerID  int64
	ForceDump bool // dump even when CLONE is possible
	// WipeList is the replica's databases the modal listed as dropped.
	WipeList []string
}

// installStep is the index of "Pasang MySQL" in createReplicaStepNames,
// which a resync does not have.
const installStep = 2

func resyncStepNames() []string {
	return slices.Delete(createReplicaStepNames(), installStep, installStep+1)
}

func (jr *jobRun) resyncReplicaSteps() []step {
	return slices.Delete(jr.createReplicaSteps(), installStep, installStep+1)
}

// StartResync starts the resync_replica job for the replica on p.ServerID.
// It refuses a server that is not a replica, a replica that is not ready,
// in attention or failed, and a server with an unfinished job.
func (r *Runner) StartResync(ctx context.Context, p ResyncParams) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	rep, err := r.d.Store.GetReplicaByServer(p.ServerID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("server ini bukan replika")
	}
	if err != nil {
		return "", err
	}
	switch rep.State {
	case store.ReplicaReady, store.ReplicaAttention, store.ReplicaFailed:
	default:
		return "", fmt.Errorf("replika %s; Resync ulang hanya bisa saat replika siap, perlu perhatian, atau gagal",
			replicaStateText(rep.State))
	}
	if err := r.refuseBusy(p.ServerID, "server ini"); err != nil {
		return "", err
	}
	topo, err := r.d.Store.GetTopology(rep.TopologyID)
	if err != nil {
		return "", fmt.Errorf("topologi replika: %w", err)
	}
	wipe := slices.Clone(p.WipeList)
	slices.Sort(wipe)
	wipe = slices.Compact(wipe)
	st := jobState{MasterServerID: topo.MasterServerID, Databases: rep.Databases, ForceDump: p.ForceDump,
		ConfirmWipe: true, WipeList: wipe, Resync: true}
	if err := r.d.Store.SetReplicaState(p.ServerID, store.ReplicaResyncing, ""); err != nil {
		return "", err
	}
	defer r.replicaChanged(p.ServerID)
	id, err := r.createJob(KindResyncReplica, p.ServerID, topo.ID, st, resyncStepNames())
	if err != nil {
		if serr := r.d.Store.SetReplicaState(p.ServerID, rep.State, rep.LastError); serr != nil {
			r.log.Warn("restore replica state", "server_id", p.ServerID, "err", serr)
		}
		return "", err
	}
	return id, nil
}
