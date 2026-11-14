package orchestrator

import (
	"context"
	"encoding/json"
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
// paused by the operator. Paused is written first, so a health report
// taken between the stop and the write is never judged "Berhenti (di luar
// panel)"; it is cleared again when the stop fails.
func (r *Runner) StopReplica(ctx context.Context, serverID int64) error {
	if err := r.controllableReplica(serverID); err != nil {
		return err
	}
	if err := r.d.Store.SetReplicaPaused(serverID, true); err != nil {
		return err
	}
	defer r.replicaChanged(serverID)
	if err := r.directCall(ctx, serverID, "mysql.stopReplica", nil); err != nil {
		if perr := r.d.Store.SetReplicaPaused(serverID, false); perr != nil {
			r.log.Warn("clear paused after a failed stop", "server_id", serverID, "err", perr)
		}
		return err
	}
	return nil
}

// StartReplica starts replication on a ready replica and clears paused.
func (r *Runner) StartReplica(ctx context.Context, serverID int64) error {
	if err := r.controllableReplica(serverID); err != nil {
		return err
	}
	if err := r.directCall(ctx, serverID, "mysql.startReplica", nil); err != nil {
		return err
	}
	if err := r.d.Store.SetReplicaPaused(serverID, false); err != nil {
		return err
	}
	r.replicaChanged(serverID)
	return nil
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
// replicas). super_read_only stays on. An old master left by a failed
// rebuild is refused (ErrAwaitingRebuild) before anything is sent.
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
			r.log.Warn("release role on detach", "server_id", serverID, "err", logErr(err))
		}
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if err := r.refuseAwaitingRebuild(serverID); err != nil {
		return err
	}
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

// errNotFailedRebuild refuses "Batalkan percobaan bangun ulang" for a
// server that is not an old master left by a failed rebuild.
var errNotFailedRebuild = errors.New("server ini bukan master lama dengan percobaan bangun ulang yang gagal")

// CancelRebuildAttempt is "Batalkan percobaan bangun ulang" (G-2): an old
// master left by a failed rebuild (role replica, failed row, still the
// topology's old master) becomes a plain quarantined old master again, in
// the panel only: its failed replica row goes and its role is
// quarantined, in one store transaction, with no agent call (its agent
// never left the quarantine). Its failed rebuild job is then superseded.
func (r *Runner) CancelRebuildAttempt(ctx context.Context, serverID int64) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return err
	}
	// Its agent ended the quarantine in the latest rebuild (the marker
	// may be left when its clear failed, H-2): not an old master any more.
	if ended, err := r.latestRebuildEnded(serverID); err != nil {
		return err
	} else if ended {
		return errNotFailedRebuild
	}
	if err := r.d.Store.ReturnToQuarantine(serverID); errors.Is(err, store.ErrNotFailedRebuild) {
		return errNotFailedRebuild
	} else if err != nil {
		return err
	}
	r.log.Info("failed rebuild attempt cancelled: quarantined again", "server_id", serverID)
	r.replicaChanged(serverID)
	// The failed rebuild job leaves no open job_failed behind.
	r.jobEnded(serverID, "")
	return nil
}

// latestRebuildEnded reports whether the newest create_replica job of
// serverID rebuilt an old master whose agent ended the quarantine
// (QuarantineEnded). A job whose params cannot be read counts as not ended.
func (r *Runner) latestRebuildEnded(serverID int64) (bool, error) {
	jobs, err := r.d.Store.ListJobsForServer(serverID, 0)
	if err != nil {
		return false, err
	}
	for _, j := range jobs {
		if j.Kind != KindCreateReplica {
			continue
		}
		var st jobState
		if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
			return false, nil
		}
		return st.RebuildOldMaster && st.QuarantineEnded, nil
	}
	return false, nil
}

func (r *Runner) detachable(serverID int64) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if _, err := r.d.Store.GetReplicaByServer(serverID); errors.Is(err, store.ErrNotFound) {
		return errors.New("server ini bukan replika")
	} else if err != nil {
		return err
	}
	// An old master left by a failed rebuild stays quarantined at its
	// agent: detaching it would make the panel treat it as unassigned
	// (D-10).
	if err := r.refuseAwaitingRebuild(serverID); err != nil {
		return err
	}
	return r.refuseBusy(serverID, "server ini")
}

// DeleteServer removes a server that is not the panel's own, neither the
// master of a topology with replicas nor a registered replica, nor a
// quarantined old master whose cutover is pending, and has no unfinished
// job: its agent releases its pinned role first (best effort; the agent may
// be offline; not asked for a quarantined server, which stays quarantined),
// then the server is deleted and its connection dropped. It answers
// store.ErrNotFound for an unknown server.
func (r *Runner) DeleteServer(ctx context.Context, serverID int64) error {
	return r.deleteServer(ctx, serverID, false)
}

// DeleteUnquarantined deletes a quarantined old master whose quarantine is
// still pending (a dead server that will not come back), as DeleteServer
// does otherwise; the web layer guards it with a typed confirmation. Any
// other server is refused with ErrNotQuarantinePending: its delete is the
// plain DeleteServer.
func (r *Runner) DeleteUnquarantined(ctx context.Context, serverID int64) error {
	return r.deleteServer(ctx, serverID, true)
}

func (r *Runner) deleteServer(ctx context.Context, serverID int64, unquarantined bool) error {
	if err := r.deletable(serverID, unquarantined); err != nil {
		return err
	}
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return err
	}
	// A quarantined server stays quarantined: its agent is not asked to
	// release the pin (it refuses anyway), whatever agent version it runs.
	if srv.Role != store.RoleQuarantined {
		rctx, cancel := context.WithTimeout(ctx, cancelTimeout)
		if err := r.d.Hub.Call(rctx, serverID, "mysql.releaseRole", nil, nil); err != nil {
			r.log.Warn("release role before delete", "server_id", serverID, "err", logErr(err))
		}
		cancel()
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if err := r.deletableLocked(serverID, unquarantined); err != nil {
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

// DeleteServer's refusals; their texts are shown to the user as they are.
// A busy server is refused with an error that wraps ErrBusy.
var (
	ErrLocalServer     = errors.New("server panel sendiri tidak bisa dihapus")
	ErrServerIsMaster  = errors.New("server ini masih menjadi master; lepas atau hapus semua replikanya dulu")
	ErrServerIsReplica = errors.New("server ini masih terdaftar sebagai replika; lepas replika dulu")
	// ErrCutoverPending: the quarantined old master of a promotion whose
	// applications are not confirmed on the new master yet. Releasing its
	// pin would drop its read-only guard at start (spec 5.4).
	// ErrQuarantinePending: the quarantined old master never received its
	// quarantine (its agent was unreachable). Releasing its pin, which a
	// delete tries, would let it come back as a writable master;
	// DeleteUnquarantined deletes a dead one anyway.
	ErrQuarantinePending = errors.New("master lama ini belum dikarantina karena agent-nya belum terjangkau; " +
		"hapus hanya bila server itu sudah mati dan tidak akan dihidupkan lagi (Hapus tanpa karantina)")
	// ErrNotQuarantinePending: DeleteUnquarantined of a server whose
	// quarantine is not pending (not a quarantined old master, or one
	// whose quarantine reached it).
	ErrNotQuarantinePending = errors.New("karantina tidak tertunda; gunakan Hapus biasa")
	ErrCutoverPending       = errors.New("server ini master lama yang dikarantina dan aplikasi belum dinyatakan dialihkan; " +
		"tekan \"Sudah saya alihkan\" di layar pengalihan dulu")
)

func (r *Runner) deletable(serverID int64, unquarantined bool) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return r.deletableLocked(serverID, unquarantined)
}

// deletableLocked is DeleteServer's check; unquarantined lets a pending
// quarantine pass (DeleteUnquarantined). Callers hold startMu.
func (r *Runner) deletableLocked(serverID int64, unquarantined bool) error {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return err
	}
	// A quarantined old master is deleted like a detached server (its
	// agent is not asked to release the quarantined pin), but not
	// while applications may still write to it.
	if unquarantined && srv.Role != store.RoleQuarantined {
		return ErrNotQuarantinePending
	}
	if srv.Role == store.RoleQuarantined {
		if topo, err := r.d.Store.GetTopologyByOldMaster(serverID); err == nil && topo.CutoverPending {
			return ErrCutoverPending
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		prom, err := r.d.Store.PromotionByOldMaster(serverID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		pending := err == nil && QuarantinePending(prom)
		switch {
		case pending && !unquarantined:
			return ErrQuarantinePending
		case !pending && unquarantined:
			return ErrNotQuarantinePending
		}
	}
	if r.d.LocalServerID != 0 && serverID == r.d.LocalServerID {
		return ErrLocalServer
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
			return ErrServerIsMaster
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := r.d.Store.GetReplicaByServer(serverID); err == nil {
		return ErrServerIsReplica
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return r.refuseBusy(serverID, "server ini")
}
