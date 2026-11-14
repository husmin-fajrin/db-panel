package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// H-1d: "copy started" needs evidence that the agent began to change the
// old master's data; a copy command sent without such evidence is
// uncertain, and neither of those nor a refusal before any change makes
// Periksa / Amankan data tertinggal refused.

func copyStateOf(t *testing.T, g *promoteRig) CopyState {
	t.Helper()
	s, err := RebuildCopyState(g.e.st, g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// leftoverAccepted cancels the failed attempt and checks that Periksa data
// tertinggal is accepted (want true) or refused with the copy sentence.
func leftoverAccepted(t *testing.T, g *promoteRig, want bool) {
	t.Helper()
	ctx := context.Background()
	if err := g.e.r.CancelRebuildAttempt(ctx, g.m.id); err != nil {
		t.Fatal(err)
	}
	g.m.reply("mysql.leftover", proto.LeftoverResult{})
	id, err := g.e.r.StartLeftover(ctx, g.m.id)
	switch {
	case want && err != nil:
		t.Fatalf("Periksa refused: %v", err)
	case !want && (err == nil || err.Error() != CopyStartedText):
		t.Fatalf("Periksa = %v, want the copy sentence", err)
	}
	if err == nil {
		g.e.waitJob(id, store.JobSucceeded)
		g.e.waitIdle(id)
	}
}

func TestCopyStateNothingSent(t *testing.T) {
	g := rebuildRig(t)
	scriptReplicaTarget(g.m, "db-master", "dump")
	g.c.fail("mysql.createDumpUser", "akun dump tidak bisa dibuat")
	failedRebuildNoScript(t, g)
	if s := copyStateOf(t, g); s != CopyNotStarted {
		t.Fatalf("state = %v, want not started", s)
	}
	leftoverAccepted(t, g, true)
}

func TestCopyStateRefusedDirectly(t *testing.T) {
	g := rebuildRig(t)
	g.m.fail("mysql.cloneFrom", "ditolak sebelum apa pun berubah")
	failedRebuildNoScript(t, g)
	if s := copyStateOf(t, g); s != CopyNotStarted {
		t.Fatalf("state = %v, want not started", s)
	}
	leftoverAccepted(t, g, true)
}

func TestCopyStateAnswerLost(t *testing.T) {
	g := rebuildRig(t)
	g.m.mu.Lock()
	g.m.dropReply = map[string]bool{"mysql.cloneFrom": true}
	g.m.mu.Unlock()
	failedRebuildNoScript(t, g)
	if s := copyStateOf(t, g); s != CopyUncertain {
		t.Fatalf("state = %v, want uncertain", s)
	}
	leftoverAccepted(t, g, true)
}

func TestCopyStateEvidence(t *testing.T) {
	g := rebuildRig(t)
	g.m.job("mysql.cloneFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Progress(0, "CLONE dimulai")
		time.Sleep(50 * time.Millisecond)
		return nil, errors.New("CLONE gagal di tengah")
	})
	failedRebuildNoScript(t, g)
	if s := copyStateOf(t, g); s != CopyStarted {
		t.Fatalf("state = %v, want started", s)
	}
	leftoverAccepted(t, g, false)
}

// TestCopyStateEarlierEvidenceCounts: of two attempts since the promotion,
// only the earlier one began the copy; the old master's data is gone all
// the same.
func TestCopyStateEarlierEvidenceCounts(t *testing.T) {
	g := rebuildRig(t)
	e := g.e
	g.m.job("mysql.cloneFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Progress(0, "CLONE dimulai")
		time.Sleep(50 * time.Millisecond)
		return nil, errors.New("CLONE gagal di tengah")
	})
	failedRebuildNoScript(t, g)
	if err := e.r.CancelRebuildAttempt(context.Background(), g.m.id); err != nil {
		t.Fatal(err)
	}
	scriptReplicaTarget(g.m, "db-master", "clone")
	g.m.fail("mysql.preflight", "pemeriksaan gagal")
	failedRebuildNoScript(t, g)
	if s := copyStateOf(t, g); s != CopyStarted {
		t.Fatalf("state = %v, want started", s)
	}
}

// failedRebuildNoScript starts a rebuild of the old master and waits for
// it to fail, the agent scripted by the caller.
func failedRebuildNoScript(t *testing.T, g *promoteRig) string {
	t.Helper()
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	g.e.waitJob(id, store.JobFailed)
	g.e.waitIdle(id)
	return id
}

// TestCopyStateStoredJobs: jobs stored before the copy flags (old shape),
// unreadable params, and the promotion matched by identity, not by the
// clock (H-4).
func TestCopyStateStoredJobs(t *testing.T) {
	steps := createReplicaStepNames()
	copyIdx := 4
	for _, tc := range []struct {
		name   string
		params string
		step   int
		back   bool // created before the promotion's start by the clock
		want   CopyState
	}{
		{"old shape, before the step", `{"rebuild_old_master":true}`, 3, false, CopyNotStarted},
		{"old shape, at the step, nothing sent", `{"rebuild_old_master":true}`, copyIdx, false, CopyNotStarted},
		{"old shape, at the step, agent job", `{"rebuild_old_master":true,"agent_job_id":"abc"}`, copyIdx, false, CopyUncertain},
		{"old shape, at the step, snapshot GTID", `{"rebuild_old_master":true,"snapshot_gtid":"u:1-5"}`, copyIdx, false, CopyUncertain},
		{"old shape, past the step", `{"rebuild_old_master":true}`, copyIdx + 1, false, CopyStarted},
		{"old shape, past the step, before the promotion", `{"rebuild_old_master":true}`, copyIdx + 1, true, CopyNotStarted},
		{"unreadable params", `{"rebuild_old_master":`, copyIdx + 1, false, CopyUncertain},
		{"new shape, sent", `{"rebuild_old_master":true,"rebuild_promotion_id":PROM,"copy_sent":true}`, copyIdx, false, CopyUncertain},
		{"new shape, evidence", `{"rebuild_old_master":true,"rebuild_promotion_id":PROM,"copy_sent":true,"copy_began":true}`, copyIdx, false, CopyStarted},
		{"new shape, clock stepped back", `{"rebuild_old_master":true,"rebuild_promotion_id":PROM,"copy_sent":true,"copy_began":true}`, copyIdx, true, CopyStarted},
		{"new shape, another promotion", `{"rebuild_old_master":true,"rebuild_promotion_id":999999,"copy_began":true}`, copyIdx + 1, false, CopyNotStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := rebuildRig(t)
			e := g.e
			prom, err := e.st.PromotionByOldMaster(g.m.id)
			if err != nil {
				t.Fatal(err)
			}
			created := prom.StartedAt.Add(time.Minute)
			if tc.back {
				created = prom.StartedAt.Add(-time.Hour)
			}
			params := []byte(tc.params)
			params = []byte(replacePROM(string(params), prom.ID))
			id := "77777777777777777777777777777777"
			if err := e.st.CreateJob(store.Job{ID: id, Kind: KindCreateReplica, ServerID: g.m.id,
				ParamsJSON: string(params), CreatedAt: created}, steps); err != nil {
				t.Fatal(err)
			}
			if err := e.st.UpdateJob(id, store.JobFailed, tc.step, "gagal", time.Now()); err != nil {
				t.Fatal(err)
			}
			if s := copyStateOf(t, g); s != tc.want {
				t.Fatalf("state = %v, want %v", s, tc.want)
			}
		})
	}
}

func replacePROM(s string, id int64) string {
	out := ""
	for i := 0; i < len(s); i++ {
		if i+4 <= len(s) && s[i:i+4] == "PROM" {
			out += itoa(id)
			i += 3
			continue
		}
		out += string(s[i])
	}
	return out
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
