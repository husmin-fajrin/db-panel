package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The direct controls are single agent commands, not jobs: each is refused
// while the server has an unfinished job and when its role does not fit.
// The checks run under startMu; the agent commands do not (a job started
// in between is caught by its own checks of the replica's state).

// StopReplica stops replication on a ready replica and records it as
// paused by the operator.
func (r *Runner) StopReplica(ctx context.Context, serverID int64) error {
	if err := r.controllableReplica(serverID); err != nil {
		return err
	}
	if err := r.directCall(ctx, serverID, "mysql.stopReplica", nil); err != nil {
		return err
	}
	return r.d.Store.SetReplicaPaused(serverID, true)
}

// StartReplica starts replication on a ready replica and clears paused.
func (r *Runner) StartReplica(ctx context.Context, serverID int64) error {
	if err := r.controllableReplica(serverID); err != nil {
		return err
	}
	if err := r.directCall(ctx, serverID, "mysql.startReplica", nil); err != nil {
		return err
	}
	return r.d.Store.SetReplicaPaused(serverID, false)
}

// Relock makes a ready replica read-only (super_read_only) again.
func (r *Runner) Relock(ctx context.Context, serverID int64) error {
	if err := r.controllableReplica(serverID); err != nil {
		return err
	}
	return r.directCall(ctx, serverID, "mysql.setReadOnly", proto.ReadOnlyParams{On: true})
}

// UnlockMaster lifts read-only on a topology's master.
func (r *Runner) UnlockMaster(ctx context.Context, serverID int64) error {
	r.startMu.Lock()
	err := func() error {
		if _, err := r.d.Store.GetTopologyByMaster(serverID); errors.Is(err, store.ErrNotFound) {
			return errors.New("server ini bukan master")
		} else if err != nil {
			return err
		}
		return r.refuseBusy(serverID, "server ini")
	}()
	r.startMu.Unlock()
	if err != nil {
		return err
	}
	return r.directCall(ctx, serverID, "mysql.setReadOnly", proto.ReadOnlyParams{On: false})
}

// controllableReplica refuses serverID unless it is a replica in state
// ready or attention without an unfinished job.
func (r *Runner) controllableReplica(serverID int64) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	rep, err := r.d.Store.GetReplicaByServer(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("server ini bukan replika")
	}
	if err != nil {
		return err
	}
	if rep.State != store.ReplicaReady && rep.State != store.ReplicaAttention {
		return fmt.Errorf("replika %s; tombol ini hanya bisa dipakai saat replika siap atau perlu perhatian",
			replicaStateText(rep.State))
	}
	return r.refuseBusy(serverID, "server ini")
}

func (r *Runner) directCall(ctx context.Context, serverID int64, cmd string, params any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return r.d.Hub.Call(ctx, serverID, cmd, params, nil)
}

// DetachReplica stops managing the replica on serverID: its replica row
// goes and its role becomes unassigned. With stop, replication on it is
// stopped and its configuration removed first (an error there keeps the
// replica registered), then its agent releases the pinned replica role
// (best effort). Nothing is sent to the master, which may be offline; the
// replication account there stays (it is shared by the topology's
// replicas). super_read_only stays on.
func (r *Runner) DetachReplica(ctx context.Context, serverID int64, stop bool) error {
	if err := r.detachable(serverID); err != nil {
		return err
	}
	if stop {
		if err := r.directCall(ctx, serverID, "mysql.stopReplica", nil); err != nil {
			return fmt.Errorf("hentikan replikasi: %w", err)
		}
		if err := r.directCall(ctx, serverID, "mysql.resetReplica", nil); err != nil {
			return fmt.Errorf("hapus pengaturan replikasi: %w", err)
		}
		if err := r.directCall(ctx, serverID, "mysql.releaseRole", nil); err != nil {
			r.log.Warn("release role on detach", "server_id", serverID, "err", err)
		}
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return err
	}
	if err := r.d.Store.RemoveReplica(serverID); errors.Is(err, store.ErrNotFound) {
		return errors.New("server ini bukan replika")
	} else if err != nil {
		return err
	}
	r.log.Info("replica detached", "server_id", serverID, "stopped", stop)
	// A failed job of the replica leaves no open job_failed behind.
	r.jobEnded(serverID, "")
	return nil
}

func (r *Runner) detachable(serverID int64) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if _, err := r.d.Store.GetReplicaByServer(serverID); errors.Is(err, store.ErrNotFound) {
		return errors.New("server ini bukan replika")
	} else if err != nil {
		return err
	}
	return r.refuseBusy(serverID, "server ini")
}

// DeleteServer removes a server that is not the panel's own, neither the
// master of a topology with replicas nor a registered replica, and has no
// unfinished job: its agent releases its
// pinned role first (best effort; the agent may be offline), then the
// server is deleted and its connection dropped. It answers
// store.ErrNotFound for an unknown server.
func (r *Runner) DeleteServer(ctx context.Context, serverID int64) error {
	if err := r.deletable(serverID); err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, cancelTimeout)
	if err := r.d.Hub.Call(rctx, serverID, "mysql.releaseRole", nil, nil); err != nil {
		r.log.Info("release role before delete", "server_id", serverID, "err", err)
	}
	cancel()
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if err := r.deletableLocked(serverID); err != nil {
		return err
	}
	if err := r.d.Store.DeleteServer(serverID); err != nil {
		return err
	}
	r.d.Hub.Disconnect(serverID)
	// Its alerts went with it (ON DELETE CASCADE); the Monitor hears it too.
	r.jobEnded(serverID, "")
	return nil
}

func (r *Runner) deletable(serverID int64) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return r.deletableLocked(serverID)
}

// deletableLocked is DeleteServer's check. Callers hold startMu.
func (r *Runner) deletableLocked(serverID int64) error {
	if _, err := r.d.Store.GetServer(serverID); err != nil {
		return err
	}
	if r.d.LocalServerID != 0 && serverID == r.d.LocalServerID {
		return errors.New("server panel sendiri tidak bisa dihapus")
	}
	// A master without replicas goes with its topology (ON DELETE CASCADE);
	// one with replicas would leave them following a master the panel no
	// longer knows.
	if topo, err := r.d.Store.GetTopologyByMaster(serverID); err == nil {
		reps, err := r.d.Store.ListReplicas(topo.ID)
		if err != nil {
			return err
		}
		if len(reps) > 0 {
			return errors.New("server ini masih menjadi master; lepas atau hapus semua replikanya dulu")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := r.d.Store.GetReplicaByServer(serverID); err == nil {
		return errors.New("server ini masih terdaftar sebagai replika; lepas replika dulu")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return r.refuseBusy(serverID, "server ini")
}
