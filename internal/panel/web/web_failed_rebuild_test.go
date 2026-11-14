package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// A rebuild of an old master that failed leaves the server's panel role
// replica with a failed replica row while its agent stays quarantined; the
// orchestrator takes a new rebuild of it (StartCreateReplica), and the
// panel offers one (D-9).

const (
	rebuildLink     = `href="/masters/2/replicas/new?bangun-ulang=1"`
	replicaPageLine = `Server ini master lama yang masih dikarantina. Pakai <a class="link" href="/masters/2/replicas/new?bangun-ulang=1">Bangun ulang sebagai replika</a>.`
	jobPageLine     = `Bila "Ulangi dari tahap ini" tidak menolong, mulai lagi <a class="link" href="/masters/2/replicas/new?bangun-ulang=1">Bangun ulang sebagai replika</a>.`
)

// failedRebuildEnv is promotedEnv after the cutover with db-master (1)
// left by a failed rebuild: role replica, its replica row failed.
func failedRebuildEnv(t *testing.T) (*env, *fakeOrch, store.Topology) {
	t.Helper()
	e, f, topo := promotedEnv(t)
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AddReplica(topo.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(1, store.ReplicaFailed, "Sambungkan replikasi: gagal"); err != nil {
		t.Fatal(err)
	}
	return e, f, topo
}

// TestFailedRebuildOffersRebuildAgain: the server page shows the Master
// lama box with Bangun ulang sebagai replika, and only actions that work in
// this state; the form opens.
func TestFailedRebuildOffersRebuildAgain(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}
	c := e.cookie()

	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-old-master", "Master lama (dikarantina sejak", rebuildLink)
	// Periksa and Amankan data tertinggal need the quarantined role
	// (oldMasterFromPath, StartLeftover), and a registered replica cannot
	// be deleted: none of them is offered.
	wantNoBody(t, rec, `action="/servers/1/leftover"`, `action="/servers/1/salvage"`, `data-open-modal="modal-hapus"`,
		"Server ini adalah replika.")

	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Persetujuan di server", "Sudah disetujui di db-master")
	if button := rebuildButton.FindString(rec.Body.String()); button == "" || strings.Contains(button, " disabled") {
		t.Fatalf("rebuild button: %q", button)
	}
	// The connections are read as the old master's.
	wantNoBody(t, rec, "Master baru tidak terjangkau")
}

// TestFailedRebuildStartsAsFirstRebuild: the start goes the same way as
// from the quarantined state: token, typed name, connections
// acknowledgement, the orchestrator's approval refusal, and the audit.
func TestFailedRebuildStartsAsFirstRebuild(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}

	// Without a token: 403; a wrong name: refused before the orchestrator.
	wantCode(t, e.do("POST", "/masters/2/replicas", rebuildForm(), c), http.StatusForbidden)
	rec := e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm("confirm_name", "db-lain"),
		web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantCalls(t, f)

	// The orchestrator refuses for the approval: shown, nothing started.
	const refusal = "Bangun ulang db-master butuh persetujuan di server itu, tetapi kodenya tidak bisa dibaca dari agent. " +
		"Buka form Bangun ulang sebagai replika dan klik Periksa lagi."
	f.err = fmt.Errorf("%w: %s", orchestrator.ErrRebuildNeedsApproval, refusal)
	rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm("accept_connections", "1"),
		web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "kodenya tidak bisa dibaca dari agent")

	f.err = nil
	rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm("accept_connections", "1"),
		web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	f.mu.Lock()
	last := f.replicas[len(f.replicas)-1]
	f.mu.Unlock()
	if !last.RebuildOldMaster || last.TargetServerID != 1 || last.MasterServerID != 2 || last.Approval == nil {
		t.Fatalf("StartCreateReplica params = %+v", last)
	}
	wantAuditDetail(t, e, "create_replica", `"rebuild_old_master":true`, `"local_approval":true`, `"local_approval_age_sec":60`)
}

// TestFailedRebuildNotOtherServers: an ordinary failed replica, and an old
// master whose rebuild is running, get neither the box nor the form.
func TestFailedRebuildNotOtherServers(t *testing.T) {
	e, _, topo := promotedEnv(t)
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	c := e.cookie()
	// db-lain (3): an ordinary replica, failed.
	if err := e.st.SetReplicaState(3, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/servers/3", nil, c)
	wantNoBody(t, rec, "data-old-master", "bangun-ulang=3")
	wantCode(t, e.do("GET", "/masters/2/replicas/new?bangun-ulang=3", nil, c), http.StatusBadRequest)
	rec = e.do("GET", "/replicas/3", nil, c)
	wantBody(t, rec, "Resync ulang", "Lepas replika")
	wantNoBody(t, rec, "Server ini master lama yang masih dikarantina.")

	// db-master (1) with its rebuild running.
	if _, err := e.st.AddReplica(topo.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantNoBody(t, rec, "data-old-master", rebuildLink)
	wantCode(t, e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c), http.StatusBadRequest)
}

// TestFailedRebuildReplicaPage: Resync ulang and Lepas replika are not
// offered (the first is no rebuild, the second leaves the panel treating
// a quarantined server as unassigned); one line links the rebuild form.
func TestFailedRebuildReplicaPage(t *testing.T) {
	e, _, _ := failedRebuildEnv(t)
	rec := e.do("GET", "/replicas/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, replicaPageLine)
	wantNoBody(t, rec, "/replicas/1/resync/prepare", `data-open-modal="modal-detach"`)
}

// TestFailedRebuildJobPage: under Ulangi dari tahap ini, the failed
// rebuild job links the rebuild form; another failed create_replica job
// does not.
func TestFailedRebuildJobPage(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	const rebuildID, otherID = "4444444444444444444444444444444a", "4444444444444444444444444444444b"
	storeJob(t, e.st, rebuildID, orchestrator.KindCreateReplica, 1, `{"master_server_id":2,"rebuild_old_master":true}`,
		store.JobFailed, "Periksa master", "Sambungkan replikasi")
	f.drops = map[string]bool{rebuildID: true}
	rec := e.do("GET", "/jobs/"+rebuildID, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Ulangi dari tahap ini", jobPageLine)

	storeJob(t, e.st, otherID, orchestrator.KindCreateReplica, 3, `{"master_server_id":2}`,
		store.JobFailed, "Periksa master", "Sambungkan replikasi")
	if err := e.st.SetReplicaState(3, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/jobs/"+otherID, nil, c)
	wantBody(t, rec, "Ulangi dari tahap ini")
	wantNoBody(t, rec, "tidak menolong")
}

// TestFailedRebuildResyncDetachRefused (D-10): a direct POST of Resync
// ulang or Lepas replika for such a server is refused by the orchestrator
// and shown as any of its refusals: the replica page, 400, the sentence.
func TestFailedRebuildResyncDetachRefused(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	f.err = orchestrator.ErrAwaitingRebuild
	const refusal = "server ini master lama yang masih dikarantina; pakai Bangun ulang sebagai replika"
	rec := e.do("POST", "/replicas/1/resync", withToken(t, e, url.Values{"confirm_name": {"db-master"}},
		web.ActResync, "replica", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, refusal)
	wantAuditDetail(t, e, "replica_resync", `"force_dump":false`)
	for _, mode := range []string{"keep", "stop"} {
		rec = e.do("POST", "/replicas/1/detach", withToken(t, e, url.Values{"confirm_name": {"db-master"}, "mode": {mode}},
			web.ActDetach, "replica", 1), c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, refusal)
	}
}
