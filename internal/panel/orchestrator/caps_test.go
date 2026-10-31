package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestAgentDataCapped (SEC-34): what an agent sends is bounded before it
// reaches the store: a job.log line, a progress message, a job result and
// an inventory.
func TestAgentDataCapped(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	long := strings.Repeat("é", 6000) // multi-byte: cut on a rune boundary
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Logf("info", "%s", long)
		j.Progress(10, long)
		return map[string]string{"pad": strings.Repeat("x", maxAgentResult)}, nil
	})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	if j.CurrentStep != 4 || !strings.Contains(j.Error, "hasil pekerjaan agent terlalu besar") {
		t.Fatalf("job = %+v", j)
	}
	lines, err := e.st.JobLog(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l.Message, "éé") {
			found = true
			if len(l.Message) > maxLogLine || !utf8.ValidString(l.Message) || !strings.HasSuffix(l.Message, "(dipotong)") {
				t.Errorf("job.log line kept %d bytes (valid %v)", len(l.Message), utf8.ValidString(l.Message))
			}
		}
	}
	if !found {
		t.Fatal("the agent's long log line is missing")
	}
	for _, s := range steps {
		if len(s.Message) > maxStepMessage {
			t.Errorf("step %d message kept %d bytes", s.Idx, len(s.Message))
		}
	}

	// An inventory longer than the cap is cut to it.
	var dbs []proto.DatabaseInfo
	for i := range maxInventoryDBs + 50 {
		dbs = append(dbs, proto.DatabaseInfo{Name: fmt.Sprintf("db%d", i)})
	}
	m.reply("mysql.inventory", proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3"}, Databases: dbs})
	if _, err := e.r.RefreshInventory(context.Background(), m.id); err != nil {
		t.Fatal(err)
	}
	if inv, _ := e.st.GetInventory(m.id); len(inv.Databases) != maxInventoryDBs {
		t.Fatalf("inventory kept %d databases, want %d", len(inv.Databases), maxInventoryDBs)
	}
}
