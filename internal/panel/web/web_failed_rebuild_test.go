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
	replicaPageLine = `Server ini master lama yang masih dikarantina. Pakai <a class="link" href="/masters/2/replicas/new?bangun-ulang=1">Bangun ulang sebagai replika</a>.`
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
	wantNoBody(t, rec, "Server ini adalah replika.")

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

	// db-master (1) with its rebuild running: its role stays quarantined,
	// so it keeps its Master lama box (what the box shows while the
	// rebuild runs is the web layer's next change).
	if _, err := e.st.AddReplica(topo.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "data-old-master")
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
	wantNoBody(t, rec, "Server ini master lama yang masih dikarantina.")
	rec = e.do("GET", "/jobs/"+id, nil, c)
	wantBody(t, rec, "Ulangi dari tahap ini")
	wantNoBody(t, rec, "tidak menolong")
	wantCode(t, e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c), http.StatusBadRequest)
}

const (
	copyStartedBox  = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel."
	copyStartedForm = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini; transaksi yang tidak sampai ke master baru tidak bisa lagi diperiksa atau diamankan lewat panel."
	attemptPending  = "Data tertinggal belum bisa diperiksa selama percobaan bangun ulang yang gagal masih tercatat. Untuk memeriksa atau mengamankannya, tekan dulu &#34;Batalkan percobaan bangun ulang&#34; di halaman server ini."
	leftoverAdvice  = "Jalankan Periksa data tertinggal"
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
// with the failed row as without it. A plain quarantined old master whose
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
