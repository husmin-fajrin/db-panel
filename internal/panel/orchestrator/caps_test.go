package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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

// TestJobLogCappedOnEveryPath (Plan 2 residual 5): agent-supplied text is
// cut to maxLogLine on every path into job.log (a preflight finding the
// panel logs itself, an agent job's log line, a failed job's error line)
// and into jobs.error and the replica's last error; a step message to
// maxStepMessage. Published copies are cut the same.
func TestJobLogCappedOnEveryPath(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	long := func(tag string) string { return tag + strings.Repeat("é", 3000) }
	r.reply("mysql.preflight", proto.PreflightResult{Worst: proto.CheckWarn, Method: "dump",
		Checks: []proto.Check{{ID: "disk", Status: proto.CheckWarn, Title: "Ruang disk", Detail: long("CHECK")}}})
	r.job("mysql.dumpFrom", func(j *jobs.Job, _ json.RawMessage) (any, error) {
		j.Logf("info", "%s", long("AGENTLOG"))
		j.Progress(10, long("PROGRESS")+strings.Repeat("x", maxStepMessage))
		return nil, errors.New(long("AGENTERR"))
	})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e.waitJob(id, store.JobFailed)
	lines, err := e.st.JobLog(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	line := func(tag string) string {
		for _, l := range lines {
			if strings.Contains(l.Message, tag) {
				return l.Message
			}
		}
		t.Fatalf("no job.log line holds %s", tag)
		return ""
	}
	published := func(event, key, tag string) string {
		e.pubMu.Lock()
		defer e.pubMu.Unlock()
		for _, p := range e.pubs {
			if s, _ := p.Data.(map[string]any)[key].(string); p.Event == event && strings.Contains(s, tag) {
				return s
			}
		}
		t.Fatalf("no %s event with %s holding %s", event, key, tag)
		return ""
	}
	rep, err := e.st.GetReplicaByServer(r.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		got  string
		max  int
	}{
		{"logf: preflight finding", line("CHECK"), maxLogLine},
		{"published logf", published("job_log", "message", "CHECK"), maxLogLine},
		{"agent job log line", line("AGENTLOG"), maxLogLine},
		{"failed job's error line", line("AGENTERR"), maxLogLine},
		{"published error line", published("job_log", "message", "AGENTERR"), maxLogLine},
		{"jobs.error", j.Error, maxLogLine},
		{"published job error", published("job_update", "error", "AGENTERR"), maxLogLine},
		{"replica last error", rep.LastError, maxLogLine},
		{"failed step message", steps[j.CurrentStep].Message, maxLogLine},
		{"agent progress step message", published("job_update", "message", "PROGRESS"), maxStepMessage},
		{"change job's replica last error", changeJobLastError(t, long("CHANGEERR")), maxLogLine},
	} {
		if len(c.got) > c.max || !utf8.ValidString(c.got) || !strings.HasSuffix(c.got, "…(dipotong)") {
			t.Errorf("%s: kept %d bytes (max %d, valid UTF-8 %v), ends %q", c.path, len(c.got), c.max,
				utf8.ValidString(c.got), c.got[max(0, len(c.got)-16):])
		}
	}
}

// changeJobLastError fails an add_databases job with the agent error
// errText and returns the replica's last error ("gagal saat …").
func changeJobLastError(t *testing.T, errText string) string {
	e := newEnv(t)
	_, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	r.job("mysql.dumpFrom", func(*jobs.Job, json.RawMessage) (any, error) { return nil, errors.New(errText) })
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobFailed)
	rep := e.replica(r.id)
	if rep.State != store.ReplicaAttention || !strings.HasPrefix(rep.LastError, "gagal saat ") {
		t.Fatalf("replica after the failed change job = %+v", rep)
	}
	return rep.LastError
}
