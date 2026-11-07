package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

// promoteEnv is controlEnv (master 1 db-master, full-coverage replica 2
// db-replika) plus replica 3 db-sebagian (toko only) and replica 4
// db-mati, with the fake orchestrator's consideration data set to view.
func promoteEnv(t *testing.T, mods ...func(*web.Deps)) (*env, *fakeOrch, *fakeMonitor) {
	t.Helper()
	e, f, mon := controlEnv(t, mods...)
	topo, err := e.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	e.addReadyReplica(t, topo, "db-sebagian", "toko")
	e.addReadyReplica(t, topo, "db-mati")
	return e, f, mon
}

func (f *fakeOrch) setView(v orchestrator.PromoteView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.view = v
}

func (f *fakeOrch) promoteList() []orchestrator.PromoteParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]orchestrator.PromoteParams(nil), f.promotes...)
}

// plannedView: db-replika is complete and the most current, db-sebagian
// lacks gudang and has a backlog, db-mati's agent is offline.
func plannedView() orchestrator.PromoteView {
	return orchestrator.PromoteView{
		Mode:           proto.PromotePlanned,
		Master:         store.Server{ID: 1, Name: "db-master"},
		LastMasterGTID: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-120",
		Candidates: []orchestrator.Candidate{
			{ServerID: 2, Name: "db-replika", Status: monitor.StatusSynced, LagSec: ptr[int64](0), Fresh: true,
				ReportAgo: 12 * time.Second, Eligible: true, MostCurrent: true, ProbeOK: ptr(true)},
			{ServerID: 3, Name: "db-sebagian", Status: monitor.StatusLagging, LagSec: ptr[int64](5), Fresh: true,
				ReportAgo: 3 * time.Second, Eligible: true, Partial: true, MissingDatabases: []string{"gudang"},
				GapToMaster: 2, ApplyBacklog: 3, ProbeOK: ptr(true)},
			{ServerID: 4, Name: "db-mati", Status: monitor.StatusAgentOffline, Reason: "Agent offline",
				ReportAgo: 10 * time.Minute},
		},
		WritePause: "Aplikasi tidak bisa menulis sejak langkah \"Kunci master\"; master baru menerima tulisan setelah langkah",
	}
}

func emergencyView() orchestrator.PromoteView {
	v := plannedView()
	v.Mode = proto.PromoteEmergency
	reported := time.Date(2026, 11, 2, 9, 58, 30, 0, time.Local)
	v.LastReportAt = &reported
	v.LastMasterGTID = "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-120"
	v.Candidates[0].ProbeOK = ptr(false)
	v.Candidates[1].ProbeOK = ptr(true)
	v.Candidates[1].GapToLastReport = 4
	v.WritePause = "Aplikasi sudah tidak bisa menulis sejak master hilang"
	return v
}

func TestPromotePageShowsModeAndCandidates(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Terencana (master hidup)",
		// The columns.
		"Nama", "Status", "Lag", "Posisi terhadap master", "Antrean terapan", "Cakupan database", "Kesegaran laporan",
		"db-replika", "db-sebagian", "db-mati", monitor.StatusSynced, "5 detik",
		"sudah menerima semua transaksi master", "tertinggal 2 transaksi dari master",
		"semua diterima, 3 belum diterapkan; akan diterapkan sebelum promote",
		"semua database", "sebagian: tidak ada <code>gudang</code>",
		"laporan 12 detik lalu", "Paling mutakhir", "Agent offline",
		// The unreachable replica, with its acceptance.
		"Replika db-mati tidak terjangkau. Promote tetap bisa dijalankan; db-mati akan ditandai",
		"Bila ternyata db-mati punya data yang tidak dimiliki master baru, db-mati perlu dibangun ulang.",
		`name="accept_unreachable" value="4"`,
		// The partial candidate's red box and its acceptance.
		"Database berikut tidak akan ada di master baru: gudang; data itu tetap utuh di master lama yang dikarantina dan bisa diamankan dari sana",
		`name="accept_partial" value="3"`,
		// Write pause, default choice and the confirmation button.
		"Aplikasi tidak bisa menulis sejak langkah",
		`name="candidate" value="2" checked`,
		`data-confirm-action="promote"`, `data-confirm-kind="master"`, `data-confirm-id="1"`,
		`x-data="promotePicker"`)
	// The offline replica cannot be chosen; nothing asks to type a name
	// for the complete candidate.
	wantNoBody(t, rec, `name="candidate" value="4"`, "Pastikan master benar-benar mati")
	if !strings.Contains(between(rec.Body.String(), `name="candidate" value="2"`, ">"), `data-name=""`) {
		t.Errorf("complete candidate in a planned promote asks for a typed name")
	}
	wantCalls(t, f, "promote-view 1")

	// Emergency: the mode's explanation, the master's last report, the
	// probes, the split-network warning and the closing sentence.
	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Darurat (master tidak terjangkau)", "Periksa sekarang", "09:58:30",
		"sudah menerima semua yang master laporkan pukul 09:58:30",
		"tertinggal 4 transaksi dari laporan terakhir master",
		"Master tidak menjawab dari replika db-replika",
		"Master menjawab dari replika db-sebagian; masalahnya mungkin di jaringan panel atau agent master, bukan MySQL-nya. Promote darurat akan ditolak agent.",
		"sudo dbpanel agent lock-master",
		"Pastikan master benar-benar mati atau tidak lagi dipakai aplikasi; bila ia hidup di jaringan lain, tulisan di sana akan tertinggal",
		"Transaksi yang belum sampai ke replika terpilih akan hilang dari master baru. Bila master lama hidup kembali, panel bisa memeriksa dan mengamankannya.",
		"Aplikasi sudah tidak bisa menulis sejak master hilang",
		`data-name="db-master"`)

	// An orchestrator failure is a message on the master's page.
	f.mu.Lock()
	f.err = errors.New("server ini bukan master")
	f.mu.Unlock()
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusServiceUnavailable)
	wantBody(t, rec, "Data pertimbangan promote tidak bisa dikumpulkan: server ini bukan master", "Master db-master")
}

// promoteForm is a POST of the consideration form for candidate.
func promoteForm(candidate, mode string, extra ...string) url.Values {
	form := url.Values{"candidate": {candidate}, "mode": {mode}}
	for i := 0; i+1 < len(extra); i += 2 {
		form.Add(extra[i], extra[i+1])
	}
	return form
}

func TestPromotePostNeedsTokenAndNames(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	post := func(form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/1/promote", withToken(t, e, form, web.ActPromote, "master", 1), c)
	}

	// Without a token nothing happens.
	rec := e.do("POST", "/masters/1/promote", promoteForm("2", "planned", "accept_unreachable", "4"), c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid)
	wantCalls(t, f)

	// The unreachable replica must be accepted.
	rec = post(promoteForm("2", "planned"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Setujui bahwa replika db-mati diarahkan ulang", "Terencana (master hidup)")
	if len(f.promoteList()) != 0 {
		t.Fatalf("promote started without accepting the unreachable replica")
	}

	// A partial candidate needs accept_partial and its typed name.
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "confirm_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "tidak akan ada di master baru")
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "2", "confirm_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama replika db-sebagian")
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3", "confirm_name", "db-master"))
	wantCode(t, rec, http.StatusBadRequest)
	if len(f.promoteList()) != 0 {
		t.Fatalf("promote started without the partial candidate's acceptance")
	}

	// The offline replica cannot be chosen.
	rec = post(promoteForm("4", "planned"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "db-mati tidak bisa dipilih: Agent offline")

	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3", "confirm_name", " db-sebagian "))
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	got := f.promoteList()
	want := orchestrator.PromoteParams{MasterServerID: 1, CandidateServerID: 3, Mode: proto.PromotePlanned,
		AcceptPartial: true, AcceptUnreachable: []int64{4}}
	if len(got) != 1 || !promoteParamsEqual(got[0], want) {
		t.Fatalf("StartPromote got %+v, want %+v", got, want)
	}

	// The complete candidate needs no typed name in a planned promote.
	rec = post(promoteForm("2", "planned", "accept_unreachable", "4"))
	wantCode(t, rec, http.StatusSeeOther)

	// A refusal of the orchestrator is shown on the page.
	f.mu.Lock()
	f.promoteErr = errors.New("promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi")
	f.mu.Unlock()
	rec = post(promoteForm("2", "planned", "accept_unreachable", "4"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "promote sebelumnya di topologi ini belum tuntas", "Terencana (master hidup)")
	f.mu.Lock()
	f.promoteErr = nil
	f.mu.Unlock()

	// The master's status changed since the page was shown.
	rec = post(promoteForm("2", "emergency", "accept_unreachable", "4", "confirm_name", "db-master"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Status master berubah")

	// Emergency: the old master's name must be typed, for any candidate.
	f.setView(emergencyView())
	before := len(f.promoteList())
	rec = post(promoteForm("2", "emergency", "accept_unreachable", "4"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama master lama db-master")
	rec = post(promoteForm("2", "emergency", "accept_unreachable", "4", "confirm_name", "db-replika"))
	wantCode(t, rec, http.StatusBadRequest)
	// A partial candidate in an emergency: still the master's name.
	rec = post(promoteForm("3", "emergency", "accept_unreachable", "4", "accept_partial", "3", "confirm_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	if len(f.promoteList()) != before {
		t.Fatalf("emergency promote started without the master's name")
	}
	rec = post(promoteForm("2", "emergency", "accept_unreachable", "4", "confirm_name", "db-master"))
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	got = f.promoteList()
	if p := got[len(got)-1]; p.Mode != proto.PromoteEmergency || p.CandidateServerID != 2 || p.AcceptPartial || p.AcceptBranched {
		t.Fatalf("emergency StartPromote got %+v", p)
	}
}

func TestPromotePageBranchedNeedsAcceptance(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := plannedView()
	v.Branched = true
	v.Candidates[0].MostCurrent, v.Candidates[0].Branched = false, true
	v.Candidates[1].Partial, v.Candidates[1].MissingDatabases, v.Candidates[1].Branched = false, nil, true
	v.Candidates = v.Candidates[:2]
	f.setView(v)

	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Replika bercabang", "dibangun ulang", `name="accept_branched"`, `data-name="db-replika"`)
	// No default choice: the user picks one of the branches.
	wantNoBody(t, rec, `value="2" checked`, `value="3" checked`)

	post := func(form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/1/promote", withToken(t, e, form, web.ActPromote, "master", 1), c)
	}
	rec = post(promoteForm("2", "planned", "confirm_name", "db-replika"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Replika bercabang")
	rec = post(promoteForm("2", "planned", "accept_branched", "1"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama replika db-replika")
	if n := len(f.promoteList()); n != 0 {
		t.Fatalf("branched promote started without acceptance: %d", n)
	}
	rec = post(promoteForm("2", "planned", "accept_branched", "1", "confirm_name", "db-replika"))
	wantCode(t, rec, http.StatusSeeOther)
	got := f.promoteList()
	if len(got) != 1 || !got[0].AcceptBranched || got[0].CandidateServerID != 2 {
		t.Fatalf("StartPromote got %+v, want AcceptBranched for 2", got)
	}
}

func TestPromoteWritableCandidateNotSelectable(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := plannedView()
	v.Candidates[1].Writable = true
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Bisa ditulis (super_read_only mati)", "db-sebagian bisa ditulis")
	wantNoBody(t, rec, `name="candidate" value="3"`)

	form := withToken(t, e, promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_name", "db-sebagian"), web.ActPromote, "master", 1)
	rec = e.do("POST", "/masters/1/promote", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	if len(f.promoteList()) != 0 {
		t.Fatalf("promote started with a writable candidate")
	}
}

func TestPromoteButtonTextByStatus(t *testing.T) {
	e, _, mon := controlEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK}, Snap: masterSnap()})
	rec := e.do("GET", "/masters/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `href="/masters/1/promote"`, "Promote…", "Promote terencana")
	wantNoBody(t, rec, "Promote darurat")

	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusDown, Severity: monitor.SevCrit}, Snap: masterSnap()})
	rec = e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, "Promote darurat")
	wantNoBody(t, rec, "Promote terencana")

	// MySQL down on a connected agent: emergency too.
	snap := masterSnap()
	snap.MySQLOK = false
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusUnreachable, Severity: monitor.SevCrit}, Snap: snap})
	wantBody(t, e.do("GET", "/masters/1", nil, c), "Promote darurat")
}

const localMasterWarning = "Panel berjalan di server ini; bila server ini mati, promote darurat tidak bisa dilakukan dari panel"

func TestLocalMasterWarning(t *testing.T) {
	e, f, _ := promoteEnv(t, func(d *web.Deps) { d.LocalServerID = 1 })
	f.setView(plannedView())
	c := e.cookie()
	wantBody(t, e.do("GET", "/masters/1", nil, c), localMasterWarning)
	wantBody(t, e.do("GET", "/masters/1/promote", nil, c), localMasterWarning)
	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Siapkan sebagai master", localMasterWarning)

	// Another server is the panel's: no warning.
	e2, f2, _ := promoteEnv(t, func(d *web.Deps) { d.LocalServerID = 2 })
	f2.setView(plannedView())
	wantNoBody(t, e2.do("GET", "/masters/1", nil, e2.cookie()), localMasterWarning)
	wantNoBody(t, e2.do("GET", "/masters/1/promote", nil, e2.cookie()), localMasterWarning)
	wantNoBody(t, e2.do("GET", "/servers/1", nil, e2.cookie()), localMasterWarning)
}

func TestDashboardPromotePartialBanner(t *testing.T) {
	e, _, _ := controlEnv(t)
	topo, err := e.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	wantNoBody(t, e.do("GET", "/", nil, e.cookie()), "data-promote-partial")
	if _, err := e.st.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: "planned", OldMasterServerID: 1,
		NewMasterServerID: 2, Status: "partial", JobID: "fedcba9876543210fedcba9876543210", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	msg := "Promote terencana dari db-master ke db-replika selesai sebagian: master lama db-master belum dikarantina."
	if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindPromotePartial, ServerID: 2, TopologyID: topo.ID,
		OpenedAt: time.Now(), Message: msg}); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-promote-partial", msg, `href="/jobs/fedcba9876543210fedcba9876543210"`)
}

func TestPromoteConfirmTokenIssued(t *testing.T) {
	e, _, _ := controlEnv(t)
	if tok := confirmToken(t, e, web.ActPromote, "master", 1); tok == "" {
		t.Fatal("no token for promote")
	}
}

func promoteParamsEqual(a, b orchestrator.PromoteParams) bool {
	if a.MasterServerID != b.MasterServerID || a.CandidateServerID != b.CandidateServerID || a.Mode != b.Mode ||
		a.AcceptPartial != b.AcceptPartial || a.AcceptBranched != b.AcceptBranched ||
		len(a.AcceptUnreachable) != len(b.AcceptUnreachable) {
		return false
	}
	for i := range a.AcceptUnreachable {
		if a.AcceptUnreachable[i] != b.AcceptUnreachable[i] {
			return false
		}
	}
	return true
}
