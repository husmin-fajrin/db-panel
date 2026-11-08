package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// TestJobLogRedacts: an agent error quoting row data reaches the job log,
// the step, jobs.error, the replica's last error and the published
// updates only censored; the panel's own quotes in a log line's format
// stay.
func TestJobLogRedacts(t *testing.T) {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a", "b")
	r.fail("mysql.dropDatabases", `Error 1062 (23000): Duplicate entry 'O\'Budi' for key 'nama' at end_log_pos 123456789`)

	id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, true)
	if err != nil {
		t.Fatalf("StartRemoveDatabases: %v", err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	jr := &jobRun{r: e.r, job: j}
	jr.logf("warn", "Agent menjawab: %s; tekan \"Ulangi\".", "Duplicate entry 'a''Budi' for key")
	jr.logf("warn", "galat: %v", errors.New("Duplicate entry \"Budi\" for key"))

	log := e.jobLog(id)
	for _, leak := range []string{"Budi", `O\'`} {
		if strings.Contains(log, leak) {
			t.Errorf("job log holds %q:\n%s", leak, log)
		}
	}
	for _, kept := range []string{"Duplicate entry '…' for key '…' at end_log_pos 123456789", `tekan "Ulangi".`} {
		if !strings.Contains(log, kept) {
			t.Errorf("job log lost %q:\n%s", kept, log)
		}
	}
	if strings.Contains(j.Error, "Budi") || !strings.Contains(j.Error, "Duplicate entry '…'") {
		t.Errorf("jobs.error = %q", j.Error)
	}
	for _, s := range steps {
		if strings.Contains(s.Message, "Budi") {
			t.Errorf("step %d message = %q", s.Idx, s.Message)
		}
	}
	if rep := e.replica(r.id); strings.Contains(rep.LastError, "Budi") || !strings.Contains(rep.LastError, "'…'") {
		t.Errorf("replica last error = %q", rep.LastError)
	}
	e.pubMu.Lock()
	b, _ := json.Marshal(e.pubs)
	e.pubMu.Unlock()
	if strings.Contains(string(b), "Budi") {
		t.Errorf("published updates hold Budi: %s", b)
	}
}
