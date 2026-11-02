package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func TestStopStartSetsPaused(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	ctx := context.Background()

	if err := e.r.StopReplica(ctx, r.id); err != nil {
		t.Fatalf("StopReplica: %v", err)
	}
	if !e.replica(r.id).Paused {
		t.Fatal("paused not set after StopReplica")
	}
	if err := e.r.StartReplica(ctx, r.id); err != nil {
		t.Fatalf("StartReplica: %v", err)
	}
	if e.replica(r.id).Paused {
		t.Fatal("paused still set after StartReplica")
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.startReplica"})
	wantCmds(t, "master", m.cmds(0), nil)

	// A failing command leaves the flag as it was.
	r.fail("mysql.stopReplica", "STOP REPLICA gagal")
	if err := e.r.StopReplica(ctx, r.id); err == nil || !strings.Contains(err.Error(), "STOP REPLICA gagal") {
		t.Fatalf("StopReplica with a failing agent: %v", err)
	}
	if e.replica(r.id).Paused {
		t.Fatal("paused set although the stop failed")
	}

	// Only a ready (or attention) replica is controlled.
	if err := e.st.SetReplicaState(r.id, store.ReplicaCreating, ""); err != nil {
		t.Fatal(err)
	}
	mark := r.mark()
	if err := e.r.StartReplica(ctx, r.id); err == nil {
		t.Fatal("StartReplica on a replica being created succeeded")
	}
	if err := e.r.StopReplica(ctx, m.id); err == nil || !strings.Contains(err.Error(), "bukan replika") {
		t.Fatalf("StopReplica on a master: %v", err)
	}
	wantCmds(t, "replica", r.cmds(mark), nil)
}

func TestRelockAndUnlockCallCorrectCommands(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	m.reply("mysql.setReadOnly", nil)
	r := e.followingReplica(topo, "a")
	ctx := context.Background()

	if err := e.r.Relock(ctx, r.id); err != nil {
		t.Fatalf("Relock: %v", err)
	}
	var ro proto.ReadOnlyParams
	r.params("mysql.setReadOnly", &ro)
	if !ro.On {
		t.Fatal("Relock sent setReadOnly off")
	}
	if err := e.r.UnlockMaster(ctx, m.id); err != nil {
		t.Fatalf("UnlockMaster: %v", err)
	}
	ro = proto.ReadOnlyParams{On: true}
	m.params("mysql.setReadOnly", &ro)
	if ro.On {
		t.Fatal("UnlockMaster sent setReadOnly on")
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.setReadOnly"})
	wantCmds(t, "master", m.cmds(0), []string{"mysql.setReadOnly"})

	// Each fits only its role.
	if err := e.r.Relock(ctx, m.id); err == nil {
		t.Fatal("Relock on the master succeeded")
	}
	if err := e.r.UnlockMaster(ctx, r.id); err == nil || !strings.Contains(err.Error(), "bukan master") {
		t.Fatalf("UnlockMaster on a replica: %v", err)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.setReadOnly"})
	wantCmds(t, "master", m.cmds(0), []string{"mysql.setReadOnly"})
}

func TestDetachKeepsReplicationRunning(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	m.detach() // an offline master does not block

	if err := e.r.DetachReplica(context.Background(), r.id, false); err != nil {
		t.Fatalf("DetachReplica: %v", err)
	}
	wantCmds(t, "replica", r.cmds(0), nil)
	wantCmds(t, "master", m.cmds(0), nil)
	if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replica row after detach: %v", err)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after detach = %q", srv.Role)
	}
	if err := e.r.DetachReplica(context.Background(), r.id, false); err == nil {
		t.Fatal("second DetachReplica succeeded")
	}
}

func TestDetachAndStopCallsReleaseRole(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.reply("mysql.resetReplica", nil)
	m.detach()
	ctx := context.Background()

	// A failing reset keeps the replica registered.
	r.fail("mysql.resetReplica", "RESET REPLICA gagal")
	if err := e.r.DetachReplica(ctx, r.id, true); err == nil || !strings.Contains(err.Error(), "RESET REPLICA gagal") {
		t.Fatalf("DetachReplica with a failing reset: %v", err)
	}
	if _, err := e.st.GetReplicaByServer(r.id); err != nil {
		t.Fatalf("replica row after a failed detach: %v", err)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.stopReplica", "mysql.resetReplica"})

	// releaseRole is best effort: its refusal does not keep the row.
	r.reply("mysql.resetReplica", nil)
	r.fail("mysql.releaseRole", "replikasi masih dikonfigurasi")
	mark := r.mark()
	if err := e.r.DetachReplica(ctx, r.id, true); err != nil {
		t.Fatalf("DetachReplica: %v", err)
	}
	wantCmds(t, "replica", r.cmds(mark), []string{"mysql.stopReplica", "mysql.resetReplica", "mysql.releaseRole"})
	wantCmds(t, "master", m.cmds(0), nil)
	if _, err := e.st.GetReplicaByServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replica row after detach: %v", err)
	}
	if srv, _ := e.st.GetServer(r.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after detach = %q", srv.Role)
	}
}

func TestDeleteServerRefusesMasterAndReplica(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	ctx := context.Background()

	err := e.r.DeleteServer(ctx, m.id)
	if err == nil || err.Error() != "server ini masih menjadi master; lepas atau hapus semua replikanya dulu" {
		t.Fatalf("DeleteServer(master) = %v", err)
	}
	err = e.r.DeleteServer(ctx, r.id)
	if err == nil || err.Error() != "server ini masih terdaftar sebagai replika; lepas replika dulu" {
		t.Fatalf("DeleteServer(replica) = %v", err)
	}
	wantCmds(t, "replica", r.cmds(0), nil)
	wantCmds(t, "master", m.cmds(0), nil)
	if _, err := e.st.GetServer(m.id); err != nil {
		t.Fatalf("master gone: %v", err)
	}

	// Once detached, the server is deleted: its role is released on the
	// agent and its connection dropped.
	if err := e.r.DetachReplica(ctx, r.id, false); err != nil {
		t.Fatal(err)
	}
	if err := e.r.DeleteServer(ctx, r.id); err != nil {
		t.Fatalf("DeleteServer(detached replica): %v", err)
	}
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.releaseRole"})
	if _, err := e.st.GetServer(r.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("server after delete: %v", err)
	}
	if e.hub.IsOnline(r.id) {
		t.Fatal("deleted server still connected")
	}

	// A master whose replicas are all gone is deleted with its topology;
	// its agent refuses to release the master role, which does not block.
	m.fail("mysql.releaseRole", "peran master tidak bisa dilepas dari panel")
	if err := e.r.DeleteServer(ctx, m.id); err != nil {
		t.Fatalf("DeleteServer(master without replicas): %v", err)
	}
	if _, err := e.st.GetTopology(topo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("topology after deleting its master: %v", err)
	}

	// An offline agent does not block the delete.
	spare := e.agent("spare", "10.0.0.9")
	spare.detach()
	if err := e.r.DeleteServer(ctx, spare.id); err != nil {
		t.Fatalf("DeleteServer(offline): %v", err)
	}
	if err := e.r.DeleteServer(ctx, spare.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteServer(gone) = %v, want ErrNotFound", err)
	}
}
