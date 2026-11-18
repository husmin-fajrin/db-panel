package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// A rebuild of an old master that failed before its agent ended the
// quarantine leaves the server's panel role quarantined with a failed
// replica row (spec 5.1); the orchestrator takes a new rebuild of it
// (StartCreateReplica), and the panel offers one (D-9).

const (
	rebuildLink     = `href="/masters/2/replicas/new?bangun-ulang=1"`
	replicaPageLine = `Server ini master lama yang dikarantina. Tindakannya ada di halaman server.`
	serverPageLink  = `href="/servers/1"`
	jobPageLine     = `Bila "Ulangi dari tahap ini" tidak menolong, mulai lagi <a class="link" href="/masters/2/replicas/new?bangun-ulang=1">Bangun ulang sebagai replika</a>.`
)

// failedRebuildEnv is promotedEnv after the cutover with db-master (1)
// left by a failed rebuild: role quarantined, its replica row failed.
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

// quarantineAgain makes db-master (1) a plain quarantined old master: its
// replica row gone (the role stays quarantined).
func quarantineAgain(t *testing.T, e *env) {
	t.Helper()
	if err := e.st.RemoveReplica(1); err != nil {
		t.Fatal(err)
	}
}

// TestFailedRebuildOffersRebuildAgain: the server page shows the Master
// lama box with Bangun ulang sebagai replika, Periksa data tertinggal and
// Hapus server (spec 5.1, "gagal sebelum settle"); the form opens.
func TestFailedRebuildOffersRebuildAgain(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}
	c := e.cookie()

	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-old-master", "Master lama (dikarantina sejak", rebuildLink)
	// The role stays quarantined: Periksa data tertinggal (the copy did
	// not start) and Hapus server are offered, as the orchestrator takes
	// them.
	wantBody(t, rec, `action="/servers/1/leftover"`, `data-open-modal="modal-hapus"`)
	wantNoBody(t, rec, "Server ini adalah replika.", "Batalkan percobaan", "rebuild-attempt")

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

// TestFailedRebuildNotOtherServers: an ordinary failed replica gets neither
// the box nor the form; an old master whose rebuild is running keeps its
// box, which shows only that the rebuild runs, and its form is refused as
// the orchestrator refuses a busy target (400).
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
	wantNoBody(t, rec, replicaPageLine)

	// db-master (1) with its rebuild running: its role stays quarantined,
	// so it keeps its Master lama box, with only T22 and the job.
	if _, err := e.st.AddReplica(topo.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	const jobID = "4444444444444444444444444444444e"
	storeActiveRebuild(t, e, jobID, store.JobRunning)
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "data-old-master", runningRebuildText, `href="/jobs/`+jobID+`"`)
	wantNoBody(t, rec, rebuildLink, `action="/servers/1/leftover"`, `data-open-modal="modal-hapus"`)
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server tujuan masih punya pekerjaan yang belum selesai")
	wantNoBody(t, rec, rebuildButtonAction)
}

// TestFailedRebuildReplicaPage: Resync ulang and Lepas replika are not
// offered (the first is no rebuild, the second leaves the panel treating
// a quarantined server as unassigned); T23 links the server page, where
// its actions are.
func TestFailedRebuildReplicaPage(t *testing.T) {
	e, _, _ := failedRebuildEnv(t)
	rec := e.do("GET", "/replicas/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, replicaPageLine, serverPageLink)
	wantNoBody(t, rec, "/replicas/1/resync/prepare", `data-open-modal="modal-detach"`, rebuildLink)
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

// TestRebuildFailedAfterSettleIsOrdinary (G-1): a rebuild that failed after
// the agent ended the quarantine cleared the old-master marker: the server
// is an ordinary failed replica, with no Master lama box, no D-9 lines,
// and Resync ulang and Lepas replika offered.
func TestRebuildFailedAfterSettleIsOrdinary(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	// The settle cleared the marker and made the role replica.
	if err := e.st.ClearOldMaster(1); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(1, store.RoleReplica); err != nil {
		t.Fatal(err)
	}
	const id = "4444444444444444444444444444444c"
	storeJob(t, e.st, id, orchestrator.KindCreateReplica, 1,
		`{"master_server_id":2,"rebuild_old_master":true,"quarantine_ended":true}`,
		store.JobFailed, "Periksa master", "Sambungkan replikasi")
	f.drops = map[string]bool{id: true}
	rec := e.do("GET", "/servers/1", nil, c)
	wantNoBody(t, rec, "data-old-master", rebuildLink)
	rec = e.do("GET", "/replicas/1", nil, c)
	wantBody(t, rec, "/replicas/1/resync/prepare", `data-open-modal="modal-detach"`)
	wantNoBody(t, rec, replicaPageLine)
	rec = e.do("GET", "/jobs/"+id, nil, c)
	wantBody(t, rec, "Ulangi dari tahap ini")
	wantNoBody(t, rec, "tidak menolong")
	wantCode(t, e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c), http.StatusBadRequest)
}

const (
	copyStartedBox  = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel."
	copyStartedForm = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini; transaksi yang tidak sampai ke master baru tidak bisa lagi diperiksa atau diamankan lewat panel."
	// attemptPending is the form and box sentence of "Batalkan percobaan
	// bangun ulang" (removed): no page says it any more.
	attemptPending = "percobaan bangun ulang yang gagal masih tercatat"
	leftoverAdvice = "Jalankan Periksa data tertinggal"
)

// storeRebuildJob records a failed rebuild job of server 1 that stopped at
// step at (an index of the create_replica steps).
func storeRebuildJob(t *testing.T, e *env, id string, at int) {
	t.Helper()
	if err := e.st.CreateJob(store.Job{ID: id, Kind: orchestrator.KindCreateReplica, ServerID: 1,
		ParamsJSON: `{"master_server_id":2,"rebuild_old_master":true}`, CreatedAt: time.Now()},
		[]string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika", "Salin data",
			"Pasang sertifikat MySQL", "Sambungkan replikasi", "Verifikasi"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, at, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestLeftoverWordingAfterFailedRebuild (G-3): the box and the rebuild form
// no longer send the operator to Periksa/Amankan they cannot use: after
// the copy started they say the data is gone; before it Periksa is offered,
// with the failed row as without it, and no page speaks of a failed
// attempt still recorded. A plain quarantined old master whose
// earlier rebuild reached the copy says the same and offers no Periksa.
func TestLeftoverWordingAfterFailedRebuild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		at      int
		box     string
		form    string
		quarant bool
		periksa bool
	}{
		{name: "failed, copy started", at: 6, box: copyStartedBox, form: copyStartedForm},
		{name: "failed, copy not started", at: 1, periksa: true},
		{name: "quarantined, copy started", at: 6, box: copyStartedBox, form: copyStartedForm, quarant: true},
		{name: "quarantined, copy not started", at: 1, quarant: true, periksa: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _ := failedRebuildEnv(t)
			c := e.cookie()
			storeRebuildJob(t, e, "5555555555555555555555555555555"+fmt.Sprint(tc.at), tc.at)
			if tc.quarant {
				// A plain quarantined old master again (its row gone).
				quarantineAgain(t, e)
			}
			box := e.do("GET", "/servers/1", nil, c)
			form := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
			wantCode(t, form, http.StatusOK)
			wantNoBody(t, form, attemptPending)
			if tc.box != "" {
				wantBody(t, box, tc.box)
				wantBody(t, form, tc.form)
				wantNoBody(t, form, leftoverAdvice, "Data tertinggal belum diperiksa")
			}
			if tc.periksa {
				wantBody(t, box, `action="/servers/1/leftover"`)
				wantNoBody(t, box, copyStartedBox, attemptPending)
				wantBody(t, form, leftoverAdvice)
			} else {
				wantNoBody(t, box, `action="/servers/1/leftover"`, `action="/servers/1/salvage"`)
			}
		})
	}
}

// TestBuatReplikaLeavesOutFailedRebuild (G-4): the non-rebuild Buat
// replika form does not offer an old master left by a failed rebuild as a
// target; an ordinary failed replica is still offered.
func TestBuatReplikaLeavesOutFailedRebuild(t *testing.T) {
	e, _, _ := failedRebuildEnv(t)
	if err := e.st.SetReplicaState(3, store.ReplicaFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/masters/2/replicas/new", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `<option value="3"`)
	wantNoBody(t, rec, `<option value="1"`)
}

const copyUncertain = "Bangun ulang sebelumnya berhenti di tahap Salin data. Bila penyalinan sempat mulai, catatan transaksi lama di server ini sudah dihapus, dan hasil &#34;tidak ada data tertinggal&#34; dari pemeriksaan ini belum tentu benar."

// storeRebuildJobParams records a failed rebuild job of server 1 at step at
// with params.
func storeRebuildJobParams(t *testing.T, e *env, id string, at int, params string) {
	t.Helper()
	if err := e.st.CreateJob(store.Job{ID: id, Kind: orchestrator.KindCreateReplica, ServerID: 1, ParamsJSON: params,
		CreatedAt: time.Now()}, []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika",
		"Salin data", "Pasang sertifikat MySQL", "Sambungkan replikasi", "Verifikasi"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateJob(id, store.JobFailed, at, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestCopyUncertainWording (H-1d): a copy that was sent with no evidence it
// began keeps Periksa and Amankan offered on the plain quarantined old
// master, with the warning above them and as the form warning; the
// status advice to run Periksa stays. With evidence, the box sentence
// replaces the advice too.
func TestCopyUncertainWording(t *testing.T) {
	e, _, _ := failedRebuildEnv(t)
	c := e.cookie()
	prom, err := e.st.PromotionByOldMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	storeRebuildJobParams(t, e, "6666666666666666666666666666666a", 4,
		fmt.Sprintf(`{"master_server_id":2,"rebuild_old_master":true,"rebuild_promotion_id":%d,"copy_sent":true}`, prom.ID))
	// With the failed row and without it the same: the warning, and
	// Periksa offered.
	box := e.do("GET", "/servers/1", nil, c)
	wantBody(t, box, copyUncertain, `action="/servers/1/leftover"`)
	wantNoBody(t, box, copyStartedBox, attemptPending)
	quarantineAgain(t, e)
	box = e.do("GET", "/servers/1", nil, c)
	wantBody(t, box, copyUncertain, `action="/servers/1/leftover"`)
	wantNoBody(t, box, copyStartedBox)
	form := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantBody(t, form, copyUncertain)
	wantNoBody(t, form, copyStartedForm)

	// Evidence that the copy began (a second attempt of this promotion).
	storeRebuildJobParams(t, e, "6666666666666666666666666666666b", 4,
		fmt.Sprintf(`{"master_server_id":2,"rebuild_old_master":true,"rebuild_promotion_id":%d,"copy_sent":true,"copy_began":true}`, prom.ID))
	box = e.do("GET", "/servers/1", nil, c)
	wantBody(t, box, copyStartedBox)
	wantNoBody(t, box, copyUncertain, `action="/servers/1/leftover"`)
}

// TestCopyStartedStatusAdvice (H-1d, N-G3): on a quarantined old master
// whose rebuild began its copy, the status advice does not send the
// operator to Periksa data tertinggal (not offered) but says why; with an
// uncertain copy the advice stays. The page and the status fragment agree.
func TestCopyStartedStatusAdvice(t *testing.T) {
	const periksaAdvice = `Tekan &#34;Periksa data tertinggal&#34; untuk melihat transaksi`
	for _, tc := range []struct {
		name   string
		params string
		advice bool
	}{
		{"uncertain", `"copy_sent":true`, true},
		{"started", `"copy_sent":true,"copy_began":true`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mon := &fakeMonitor{}
			e, _, topo := promotedEnv(t, func(d *web.Deps) { d.Monitor = mon })
			if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
				t.Fatal(err)
			}
			mon.setView(monitor.View{ServerID: 1, Role: store.RoleQuarantined, ReceivedAt: time.Now(),
				Status: monitor.DeriveQuarantined(false)})
			prom, err := e.st.PromotionByOldMaster(1)
			if err != nil {
				t.Fatal(err)
			}
			storeRebuildJobParams(t, e, "6666666666666666666666666666666c", 4,
				fmt.Sprintf(`{"master_server_id":2,"rebuild_old_master":true,"rebuild_promotion_id":%d,%s}`, prom.ID, tc.params))
			for _, u := range []string{"/servers/1", "/servers/1/status-fragment"} {
				rec := e.do("GET", u, nil, e.cookie())
				wantBody(t, rec, "data-explain")
				if tc.advice {
					wantBody(t, rec, periksaAdvice)
				} else {
					wantNoBody(t, rec, periksaAdvice)
					wantBody(t, rec, copyStartedBox)
				}
			}
		})
	}
}

const (
	// runningRebuildText is T22, the box while a rebuild runs or waits.
	runningRebuildText = "Bangun ulang sebagai replika sedang berjalan."
	// rebuildButtonAction is the rebuild form's submit button.
	rebuildButtonAction = `data-confirm-action="rebuild_old_master"`
)

// rebuildSteps are the steps of a create_replica job.
var rebuildSteps = []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika", "Salin data",
	"Pasang sertifikat MySQL", "Sambungkan replikasi", "Verifikasi"}

// storeActiveRebuild records a rebuild job of server 1 that runs, or waits
// for an approval at the copy (status store.JobRunning or store.JobWaiting).
func storeActiveRebuild(t *testing.T, e *env, id, status string) {
	t.Helper()
	params := `{"master_server_id":2,"rebuild_old_master":true}`
	if status == store.JobWaiting {
		params = `{"master_server_id":2,"rebuild_old_master":true,"wait_reason":"wipe","wait_list":["toko"]}`
	}
	if err := e.st.CreateJob(store.Job{ID: id, Kind: orchestrator.KindCreateReplica, ServerID: 1, ParamsJSON: params,
		CreatedAt: time.Now()}, rebuildSteps); err != nil {
		t.Fatal(err)
	}
	if status == store.JobWaiting {
		if err := e.st.SetStep(id, 4, store.StepWaiting, "Database berikut dihapus: toko", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.UpdateJob(id, status, 4, "", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// oldMasterBox is the Master lama box of a server page.
func oldMasterBoxOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "data-old-master")
	j := strings.Index(body, "data-old-master-end")
	if i < 0 || j < i {
		t.Fatalf("no Master lama box:\n%s", body)
	}
	return body[i:j]
}

// TestQuarantinedWithRowBox (spec 5.1, "gagal sebelum settle"): the box of
// a quarantined server whose rebuild failed offers Hapus and the rebuild;
// Periksa and Amankan before the copy began, under the warning when it is
// uncertain, and the copy sentence without them once it began. Nothing
// offers or names "Batalkan percobaan bangun ulang"; its old path is gone.
func TestQuarantinedWithRowBox(t *testing.T) {
	for _, tc := range []struct {
		name    string
		at      int
		params  string
		periksa bool
		want    string
	}{
		{name: "copy not started", at: 1, periksa: true},
		{name: "copy uncertain", at: 4, params: `"copy_sent":true`, periksa: true, want: copyUncertain},
		{name: "copy started", at: 6, want: copyStartedBox},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, topo := failedRebuildEnv(t)
			c := e.cookie()
			setLeftover(t, e, topo, threeLeftover())
			prom, err := e.st.PromotionByOldMaster(1)
			if err != nil {
				t.Fatal(err)
			}
			params := fmt.Sprintf(`{"master_server_id":2,"rebuild_old_master":true,"rebuild_promotion_id":%d`, prom.ID)
			if tc.params != "" {
				params += "," + tc.params
			}
			storeRebuildJobParams(t, e, "7777777777777777777777777777777"+fmt.Sprint(tc.at), tc.at, params+"}")
			f.drops = map[string]bool{}
			rec := e.do("GET", "/servers/1", nil, c)
			wantCode(t, rec, http.StatusOK)
			box := oldMasterBoxOf(t, rec.Body.String())
			for _, s := range []string{`data-open-modal="modal-hapus"`, rebuildLink} {
				if !strings.Contains(box, s) {
					t.Errorf("box lacks %q", s)
				}
			}
			if tc.want != "" && !strings.Contains(box, tc.want) {
				t.Errorf("box lacks %q", tc.want)
			}
			for _, s := range []string{`action="/servers/1/leftover"`, `action="/servers/1/salvage"`} {
				if strings.Contains(box, s) != tc.periksa {
					t.Errorf("box offers %s: %v, want %v", s, !tc.periksa, tc.periksa)
				}
			}
			wantNoBody(t, rec, "Batalkan percobaan", "rebuild-attempt", attemptPending, runningRebuildText)
			wantNoBody(t, e.do("GET", "/replicas/1", nil, c), "Batalkan percobaan", "rebuild-attempt")
		})
	}
	e, _, _ := failedRebuildEnv(t)
	wantCode(t, e.do("POST", "/servers/1/rebuild-attempt/cancel", url.Values{}, e.cookie()), http.StatusNotFound)
}

// TestBoxShowsRunningRebuild (spec 5.1, T22): while a rebuild of the old
// master runs or waits for an approval, its box says so and links the job,
// and offers nothing else: no Hapus, Periksa, Amankan or rebuild.
func TestBoxShowsRunningRebuild(t *testing.T) {
	for _, status := range []string{store.JobRunning, store.JobWaiting} {
		t.Run(status, func(t *testing.T) {
			e, _, topo := failedRebuildEnv(t)
			c := e.cookie()
			setLeftover(t, e, topo, threeLeftover())
			if err := e.st.RemoveReplica(1); err != nil {
				t.Fatal(err)
			}
			if _, err := e.st.AddReplica(topo.ID, 1, nil); err != nil {
				t.Fatal(err)
			}
			const id = "8888888888888888888888888888888a"
			storeActiveRebuild(t, e, id, status)
			rec := e.do("GET", "/servers/1", nil, c)
			wantCode(t, rec, http.StatusOK)
			box := oldMasterBoxOf(t, rec.Body.String())
			if !strings.Contains(box, runningRebuildText) || !strings.Contains(box, `href="/jobs/`+id+`"`) {
				t.Fatalf("box lacks T22 or the job link:\n%s", box)
			}
			for _, s := range []string{"Data tertinggal", "Periksa data tertinggal"} {
				if strings.Contains(box, s) {
					t.Errorf("box still shows %q", s)
				}
			}
			wantNoBody(t, rec, `data-open-modal="modal-hapus"`, `action="/servers/1/leftover"`, `action="/servers/1/salvage"`,
				rebuildLink)
			form := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
			wantCode(t, form, http.StatusBadRequest)
			wantBody(t, form, "server tujuan masih punya pekerjaan yang belum selesai")
			wantNoBody(t, form, rebuildButtonAction)
		})
	}
}

// TestRebuildFormFromQuarantinedWithRow: the rebuild form of a quarantined
// server whose rebuild failed opens; its warnings follow the copy state,
// and none speaks of a failed attempt still recorded.
func TestRebuildFormFromQuarantinedWithRow(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		at           int
		want, not    string
	}{
		{name: "not started", at: 1, want: "Data tertinggal belum diperiksa", not: copyStartedForm},
		{name: "uncertain", at: 4, params: `,"copy_sent":true`, want: copyUncertain, not: copyStartedForm},
		{name: "started", at: 6, want: copyStartedForm, not: leftoverAdvice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := failedRebuildEnv(t)
			f.setConnections(1)
			prom, err := e.st.PromotionByOldMaster(1)
			if err != nil {
				t.Fatal(err)
			}
			storeRebuildJobParams(t, e, "9999999999999999999999999999999"+fmt.Sprint(tc.at), tc.at,
				fmt.Sprintf(`{"master_server_id":2,"rebuild_old_master":true,"rebuild_promotion_id":%d%s}`, prom.ID, tc.params))
			form := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, e.cookie())
			wantCode(t, form, http.StatusOK)
			wantBody(t, form, tc.want, rebuildButtonAction)
			wantNoBody(t, form, tc.not, attemptPending, "Batalkan percobaan")
		})
	}
}

// TestReplicaListsLeaveOutQuarantined (spec 5.1): the dashboard, the
// Replikasi page and the master page show no quarantined server as a
// replica card, its row creating or failed; it shows through its own box.
func TestReplicaListsLeaveOutQuarantined(t *testing.T) {
	for _, state := range []string{store.ReplicaCreating, store.ReplicaFailed} {
		t.Run(state, func(t *testing.T) {
			e, _, _ := failedRebuildEnv(t)
			c := e.cookie()
			if err := e.st.SetReplicaState(1, state, ""); err != nil {
				t.Fatal(err)
			}
			// A second topology, so that Replikasi lists them.
			e.makeMaster(t, e.pairedOnline(t, "db-gudang"))
			for _, u := range []string{"/", "/replikasi", "/masters/2"} {
				rec := e.do("GET", u, nil, c)
				wantCode(t, rec, http.StatusOK)
				wantBody(t, rec, `href="/replicas/3"`)
				wantNoBody(t, rec, `href="/replicas/1"`)
			}
			wantBody(t, e.do("GET", "/replikasi", nil, c), "1 replika")
		})
	}
}

// TestReplicaPageOfQuarantinedServer (spec 5.1, R-20): the replica page of
// a quarantined server, named by its topology or not (the topology
// promoted again), offers neither Resync ulang nor Lepas replika; T23 links
// the server page.
func TestReplicaPageOfQuarantinedServer(t *testing.T) {
	for _, named := range []bool{true, false} {
		e, _, _ := failedRebuildEnv(t)
		if !named {
			if err := e.st.ClearOldMaster(1); err != nil {
				t.Fatal(err)
			}
		}
		rec := e.do("GET", "/replicas/1", nil, e.cookie())
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, replicaPageLine, `<a class="link" href="/servers/1">`)
		wantNoBody(t, rec, "/replicas/1/resync/prepare", `data-open-modal="modal-detach"`, "Resync ulang</button>",
			"Lepas replika</button>")
	}
}

// TestJobPageOffersRetryFromQuarantined (spec 5.1): the failed rebuild of a
// quarantined server offers "Ulangi dari tahap ini" until a newer job
// supersedes it.
func TestJobPageOffersRetryFromQuarantined(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	const id = "4444444444444444444444444444444f"
	storeRebuildJob(t, e, id, 1)
	f.drops = map[string]bool{id: true}
	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantBody(t, rec, `action="/jobs/`+id+`/retry/prepare"`, "Ulangi dari tahap ini", jobPageLine)
	// A new rebuild supersedes it (the orchestrator's Superseded).
	f.superseded = map[string]string{id: "pekerjaan yang lebih baru sudah dimulai untuk server ini"}
	rec = e.do("GET", "/jobs/"+id, nil, c)
	wantNoBody(t, rec, `action="/jobs/`+id+`/retry/prepare"`, `action="/jobs/`+id+`/retry"`)
}

// formWarnings are the warning lines of the rebuild form.
func formWarnings(t *testing.T, body string) []string {
	t.Helper()
	i := strings.Index(body, `<ul class="list-disc ml-6">`)
	if i < 0 {
		return nil
	}
	list := body[i:]
	list = list[:strings.Index(list, "</ul>")]
	var out []string
	for _, li := range strings.Split(list, "<li>")[1:] {
		out = append(out, "<li>"+strings.TrimSuffix(strings.TrimSpace(li), "</li>")+"</li>")
	}
	return out
}

// TestRetryDialogShowsLeftoverWarnings (R-16): the Ulangi dialog of a
// rebuild that failed before its copy, and the Setujui dialog of one that
// waits, show the leftover warning lines of the rebuild form, from the
// same function: the transactions Periksa found, "belum diperiksa" before
// any check, "sempat bisa ditulis" while master_start_late is open.
func TestRetryDialogShowsLeftoverWarnings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *env, topo store.Topology)
		want  string
	}{
		{name: "found", want: "214 transaksi", setup: func(t *testing.T, e *env, topo store.Topology) {
			setLeftover(t, e, topo, proto.LeftoverResult{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-214",
				Transactions: 214})
		}},
		{name: "not checked", want: "Data tertinggal belum diperiksa", setup: func(*testing.T, *env, store.Topology) {}},
		{name: "start late", want: "sempat bisa ditulis", setup: func(t *testing.T, e *env, topo store.Topology) {
			setLeftover(t, e, topo, proto.LeftoverResult{})
			if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindMasterStartLate, ServerID: 1,
				OpenedAt: time.Now().Add(time.Minute), Message: "master lama sempat bisa ditulis saat menyala"}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, topo := failedRebuildEnv(t)
			c := e.cookie()
			f.setConnections(1)
			tc.setup(t, e, topo)
			storeRebuildJob(t, e, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", 1)
			form := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
			wantCode(t, form, http.StatusOK)
			lines := formWarnings(t, form.Body.String())
			if len(lines) == 0 || !strings.Contains(strings.Join(lines, ""), tc.want) {
				t.Fatalf("form warnings = %q, want %q among them", lines, tc.want)
			}

			// The Ulangi dialog of the failed rebuild.
			f.drops = map[string]bool{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1": true, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2": true}
			rec := e.do("GET", "/jobs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1?buka=ulangi", nil, c)
			wantCode(t, rec, http.StatusOK)
			dialog := between(rec.Body.String(), `id="modal-ulangi"`, "</dialog>")
			for _, l := range lines {
				if !strings.Contains(dialog, l) {
					t.Errorf("Ulangi dialog lacks %s:\n%s", l, dialog)
				}
			}

			// The Setujui dialog of a rebuild waiting for its approval.
			storeActiveRebuild(t, e, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2", store.JobWaiting)
			rec = e.do("GET", "/jobs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2", nil, c)
			wantCode(t, rec, http.StatusOK)
			dialog = between(rec.Body.String(), `x-data="confirmApproval"`, "</dialog>")
			for _, l := range lines {
				if !strings.Contains(dialog, l) {
					t.Errorf("Setujui dialog lacks %s:\n%s", l, dialog)
				}
			}
		})
	}
}
