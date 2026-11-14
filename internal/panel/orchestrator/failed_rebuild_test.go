package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// The state a rebuild of an old master leaves when it fails, and the ways
// forward and out of it (part G).

// jobStateOf reads the stored state of job id.
func jobStateOf(t *testing.T, e *env, id string) jobState {
	t.Helper()
	j, _, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// failedRebuild runs a rebuild of the old master that fails at cmd and
// returns the job id.
func failedRebuild(t *testing.T, g *promoteRig, cmd string) string {
	t.Helper()
	g.m.fail(cmd, "gagal di "+cmd)
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	g.e.waitJob(id, store.JobFailed)
	g.e.waitIdle(id)
	return id
}

// TestRebuildFailureBeforeSettleKeepsOldMaster (G-1): a failure before the
// agent's configureReplication answered OK leaves the old master waiting
// for its rebuild: flag unset, marker kept.
func TestRebuildFailureBeforeSettleKeepsOldMaster(t *testing.T) {
	g := rebuildRig(t)
	id := failedRebuild(t, g, "mysql.configureReplication")
	if jobStateOf(t, g.e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded set although configureReplication failed")
	}
	if _, err := g.e.st.GetTopologyByOldMaster(g.m.id); err != nil {
		t.Fatalf("old-master marker after a failure before the settle: %v", err)
	}
	if ok, err := AwaitingRebuild(g.e.st, mustServer(t, g.e, g.m.id)); err != nil || !ok {
		t.Fatalf("AwaitingRebuild = %v, %v; want true", ok, err)
	}
}

// TestRebuildFailureAfterSettleIsOrdinaryReplica (G-1): once the agent
// answered configureReplication OK it ended the quarantine; a failure after
// that (startReplica, verify) leaves an ordinary failed replica.
func TestRebuildFailureAfterSettleIsOrdinaryReplica(t *testing.T) {
	for _, cmd := range []string{"mysql.startReplica", "mysql.waitSynced"} {
		t.Run(cmd, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			id := failedRebuild(t, g, cmd)
			if !jobStateOf(t, e, id).QuarantineEnded {
				t.Fatal("QuarantineEnded not set after configureReplication answered OK")
			}
			if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("old-master marker kept after the settle: %v", err)
			}
			if ok, err := AwaitingRebuild(e.st, mustServer(t, e, g.m.id)); err != nil || ok {
				t.Fatalf("AwaitingRebuild = %v, %v; want false", ok, err)
			}
			if err := e.r.refuseAwaitingRebuild(g.m.id); err != nil {
				t.Fatalf("resync/detach refused for an ordinary failed replica: %v", err)
			}
			if err := e.r.DetachReplica(context.Background(), g.m.id, false); err != nil {
				t.Fatalf("DetachReplica: %v", err)
			}
		})
	}
}

// TestRetryConnectHealsQuarantineEnded (G-1): a connect step that failed
// without the flag (as after a panel stop between the agent's answer and
// the save) sets it on a Retry whose configureReplication answers OK.
func TestRetryConnectHealsQuarantineEnded(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	id := failedRebuild(t, g, "mysql.configureReplication")
	scriptReplicaTarget(g.m, "db-master", "clone")
	if err := e.r.Retry(id, JobAck{Confirmed: true}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	if !jobStateOf(t, e, id).QuarantineEnded {
		t.Fatal("QuarantineEnded not set by the retried connect step")
	}
	if _, err := e.st.GetTopologyByOldMaster(g.m.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old-master marker after the retried rebuild: %v", err)
	}
}

// TestCancelAfterSettleIsOrdinaryCancel (G-1): a rebuild cancelled after
// the agent ended the quarantine is not put back to quarantined (the
// agent is a replica now): it is cancelled as any Buat replika, its agent
// asked to release the role.
func TestCancelAfterSettleIsOrdinaryCancel(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	g.m.on("mysql.waitSynced", func(json.RawMessage) (any, error) {
		started <- struct{}{}
		<-block
		return nil, errors.New("dibatalkan")
	})
	g.m.reply("mysql.resetReplica", nil)
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	<-started
	mark := g.m.mark()
	if err := e.r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	close(block)
	e.waitJob(id, store.JobCanceled)
	if srv := mustServer(t, e, g.m.id); srv.Role != store.RoleUnassigned {
		t.Fatalf("role after a cancel after the settle = %s, want unassigned", srv.Role)
	}
	released := false
	for _, c := range g.m.cmds(mark) {
		released = released || c == "mysql.releaseRole"
	}
	if !released {
		t.Fatalf("cancel after the settle sent %v, want mysql.releaseRole", g.m.cmds(mark))
	}
}
