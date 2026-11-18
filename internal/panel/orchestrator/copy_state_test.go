package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
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

// leftoverAccepted checks that Periksa data tertinggal on the old master
// left by the failed attempt is accepted (want true) or refused with the
// copy sentence.
func leftoverAccepted(t *testing.T, g *promoteRig, want bool) {
	t.Helper()
	ctx := context.Background()
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
		j.Progress(3, "CLONE tahap FILE COPY: 12 MB")
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
	g.m.job("mysql.cloneFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Progress(0, "CLONE dimulai")
		j.Progress(3, "CLONE tahap FILE COPY: 12 MB")
		time.Sleep(50 * time.Millisecond)
		return nil, errors.New("CLONE gagal di tengah")
	})
	failedRebuildNoScript(t, g)
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
		// N-H3: sent counts as uncertain whatever its promotion number.
		{"sent without a promotion number", `{"rebuild_old_master":true,"copy_sent":true}`, copyIdx, false, CopyUncertain},
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

// TestCopyStateCloneNeedsRealProgress (N-H4): for CLONE, a progress of 0%
// ("CLONE dimulai", sent right before CLONE INSTANCE) is no proof that the
// copy began; progress above 0 or the copy's result is. A dump's first
// progress is sent after its binary log reset, so it stays proof.
func TestCopyStateCloneNeedsRealProgress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		fn     func(j *jobs.Job, _ json.RawMessage) (any, error)
		after  func(g *promoteRig) // scripts a failure after the copy
		want   CopyState
	}{
		{"clone at 0 percent", "clone", func(j *jobs.Job, _ json.RawMessage) (any, error) {
			j.Progress(0, "CLONE dimulai")
			time.Sleep(50 * time.Millisecond)
			return nil, errors.New("CLONE gagal")
		}, nil, CopyUncertain},
		{"clone above 0 percent", "clone", func(j *jobs.Job, _ json.RawMessage) (any, error) {
			j.Progress(0, "CLONE dimulai")
			j.Progress(1, "CLONE tahap FILE COPY: 3 MB")
			time.Sleep(50 * time.Millisecond)
			return nil, errors.New("CLONE gagal")
		}, nil, CopyStarted},
		{"clone result", "clone", func(*jobs.Job, json.RawMessage) (any, error) {
			return proto.SnapshotResult{Method: "clone", Bytes: 300, GTIDExecuted: masterGTID}, nil
		}, func(g *promoteRig) {
			// Fails once the copy is done (setReadOnly also runs before it).
			g.m.on("mysql.setReadOnly", func(json.RawMessage) (any, error) {
				if g.m.count("mysql.cloneFrom") > 0 {
					return nil, errors.New("baca-saja tidak bisa dipasang")
				}
				return nil, nil
			})
		}, CopyStarted},
		{"dump at 0 percent", "dump", func(j *jobs.Job, _ json.RawMessage) (any, error) {
			j.Progress(0, "dump dimulai")
			time.Sleep(50 * time.Millisecond)
			return nil, errors.New("dump gagal")
		}, nil, CopyStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := rebuildRig(t)
			scriptReplicaTarget(g.m, "db-master", tc.method)
			cmd := "mysql.cloneFrom"
			if tc.method == "dump" {
				cmd = "mysql.dumpFrom"
			}
			g.m.job(cmd, tc.fn)
			if tc.after != nil {
				tc.after(g)
			}
			failedRebuildNoScript(t, g)
			if s := copyStateOf(t, g); s != tc.want {
				t.Fatalf("state = %v, want %v", s, tc.want)
			}
		})
	}
}

// TestCopySentSaveFailureStopsStep (N-H1): when CopySent cannot be
// stored, the step fails before the copy command is sent, so a lost
// answer can never hide a copy the panel has no record of.
func TestCopySentSaveFailureStopsStep(t *testing.T) {
	g := rebuildRig(t)
	g.e.r.paramsStore = func(jobID, paramsJSON string) error {
		if strings.Contains(paramsJSON, `"copy_sent":true`) {
			return errors.New("database is locked")
		}
		return g.e.st.SetJobParams(jobID, paramsJSON)
	}
	id := failedRebuildNoScript(t, g)
	j, steps, err := g.e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if steps[j.CurrentStep].Name != stepCopyName || j.Error != stepCopyName+": database is locked" {
		t.Fatalf("job failed at %q with %q, want the copy step failing on the save", steps[j.CurrentStep].Name, j.Error)
	}
	if n := g.m.count("mysql.cloneFrom"); n != 0 {
		t.Fatalf("copy command sent %d times", n)
	}
	if s := copyStateOf(t, g); s != CopyNotStarted {
		t.Fatalf("state = %v, want not started", s)
	}
}

// TestWipeApprovalTextForOldMaster (T9): the rebuild of an old master asks
// to approve the deletion of its databases in T9's words; an ordinary
// create keeps the old words.
func TestWipeApprovalTextForOldMaster(t *testing.T) {
	g := rebuildRig(t)
	scriptLeftovers(g.m, "clone", "a", "b")
	id, err := g.startRebuild()
	if err != nil {
		t.Fatal(err)
	}
	j, steps := g.e.waitJob(id, store.JobWaiting)
	if want := "database berikut di master lama ini akan dihapus lalu disalin ulang dari master baru: a, b"; steps[j.CurrentStep].Message != want {
		t.Fatalf("waiting message = %q, want %q", steps[j.CurrentStep].Message, want)
	}

	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "clone")
	scriptLeftovers(r, "clone", "a", "b")
	id, err = e.r.StartCreateReplica(context.Background(), CreateReplicaParams{MasterServerID: m.id, TargetServerID: r.id})
	if err != nil {
		t.Fatal(err)
	}
	j, steps = e.waitJob(id, store.JobWaiting)
	if want := "database dari percobaan sebelumnya akan dihapus lalu disalin ulang dari master: a, b"; steps[j.CurrentStep].Message != want {
		t.Fatalf("waiting message = %q, want %q", steps[j.CurrentStep].Message, want)
	}
}

// TestCopySentSaveFailureResetsFlag (N-H1): only the first save of
// CopySent fails; the dump's account is dropped after it (a later save of
// the state), and that save must not record a copy that was never sent.
func TestCopySentSaveFailureResetsFlag(t *testing.T) {
	g := rebuildRig(t)
	scriptReplicaTarget(g.m, "db-master", "dump")
	var failed atomic.Bool
	g.e.r.paramsStore = func(jobID, paramsJSON string) error {
		if strings.Contains(paramsJSON, `"copy_sent":true`) && failed.CompareAndSwap(false, true) {
			return errors.New("database is locked")
		}
		return g.e.st.SetJobParams(jobID, paramsJSON)
	}
	id := failedRebuildNoScript(t, g)
	if !failed.Load() {
		t.Fatal("CopySent was never saved")
	}
	if n := g.m.count("mysql.dumpFrom"); n != 0 {
		t.Fatalf("copy command sent %d times", n)
	}
	j, _, err := g.e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(j.ParamsJSON, "copy_sent") || strings.Contains(j.ParamsJSON, "dump_user") {
		t.Fatalf("params after the failed step = %s, want no copy_sent and the dump account dropped", j.ParamsJSON)
	}
	if s := copyStateOf(t, g); s != CopyNotStarted {
		t.Fatalf("state = %v, want not started", s)
	}
}
