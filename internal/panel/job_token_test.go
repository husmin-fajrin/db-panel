package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestCloneCreateRetryPageNeedsToken (ruling 16): with the real
// orchestrator behind the UI, a create_replica by CLONE that failed at
// "Salin data" is a job that drops data: its page offers "Ulangi" through
// the tokened dialog, and a retry posted without a token is refused with
// 403 and leaves the job failed.
func TestCloneCreateRetryPageNeedsToken(t *testing.T) {
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	ui := newUIClient(t, "https://"+uiAddr)
	ui.login(readInitialPassword(t, o))

	// The local server is the target of a failed replica attempt of a
	// master registered without an agent.
	master, err := p.Store.CreateServer("db-master")
	if err != nil {
		t.Fatal(err)
	}
	topo, err := p.Store.CreateTopology(master.ID, "dbpanel_repl", nil, "10.0.0.1", 3306)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.AddReplica(topo.ID, p.localID, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.SetReplicaState(p.localID, store.ReplicaFailed, "CLONE gagal"); err != nil {
		t.Fatal(err)
	}
	const id = "cccccccccccccccccccccccccccccccc"
	steps := []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika", "Salin data"}
	params := fmt.Sprintf(`{"master_server_id":%d,"confirm_wipe":true,"method":"clone"}`, master.ID)
	if err := p.Store.CreateJob(store.Job{ID: id, Kind: orchestrator.KindCreateReplica, ServerID: p.localID,
		TopologyID: topo.ID, ParamsJSON: params, CreatedAt: time.Now()}, steps); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.UpdateJob(id, store.JobFailed, 4, "CLONE gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The retry's gate has no red row, so the token decides: both agents
	// were at the panel's version and the master's last report carries
	// its role pin.
	local, err := p.Store.GetServer(p.localID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []store.Server{master, local} {
		if err := p.Store.UpdateProbe(s.ID, proto.ProbeResult{Hostname: s.Name}, o.Version, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(proto.HealthEvent{Seq: 1, Snapshot: proto.HealthSnapshot{MySQLOK: true, RolePin: "master",
		AgentVersion: o.Version}})
	if err != nil {
		t.Fatal(err)
	}
	p.onEvent(master.ID, proto.Event{Type: proto.EventHealth, Payload: b})
	j, _, err := p.Store.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	in, gated := orchestrator.JobGateInput(j)
	if !gated {
		t.Fatal("the retry of a CLONE that failed at Salin data is not gated")
	}
	if res, err := p.Orchestrator.Gate(in); err != nil || len(res.Red()) > 0 {
		t.Fatalf("gate of the retry = %+v, %v; want no red row", res.Rows, err)
	}

	body := ui.get("/jobs/" + id)
	for _, want := range []string{`action="/jobs/` + id + `/retry/prepare"`,
		`data-confirm-action="job_retry:` + id + `" data-confirm-kind="job"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("job page lacks %s", want)
		}
	}
	status, body := ui.post("/jobs/"+id+"/retry", url.Values{})
	if status != http.StatusForbidden || !strings.Contains(body, "Konfirmasi tidak berlaku") {
		t.Fatalf("tokenless retry = %d:\n%s", status, tail(body, 20))
	}
	if j, _, _ := p.Store.GetJob(id); j.Status != store.JobFailed {
		t.Fatalf("job after a refused retry = %+v", j)
	}
}
