package web_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
		"Replika", "Status", "Lag", "Posisi terhadap master", "Cakupan database", "Laporan", "Bisa dipilih?",
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
	if !strings.Contains(between(rec.Body.String(), `name="candidate" value="2"`, ">"), `data-name="" data-replica-name=""`) {
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
		"sudo dbpanel agent unlock-master",
		"Pastikan master benar-benar mati atau tidak lagi dipakai aplikasi; bila ia hidup di jaringan lain, tulisan di sana akan tertinggal",
		"Transaksi yang belum sampai ke replika terpilih akan hilang dari master baru. Bila master lama hidup kembali, panel bisa memeriksa dan mengamankannya.",
		"Aplikasi sudah tidak bisa menulis sejak master hilang",
		`data-name="db-master"`)

	// An orchestrator failure is a fixed message on the master's page; the
	// error's text (which may carry an agent's) goes to the log only
	// (SEC-72), in the audit of a refused start too.
	f.mu.Lock()
	f.err = errors.New("agent: /var/lib/mysql/rahasia tidak terbaca")
	f.mu.Unlock()
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusServiceUnavailable)
	wantBody(t, rec, "Data pertimbangan promote tidak bisa dikumpulkan; rinciannya ada di log panel.", "Master db-master")
	wantNoBody(t, rec, "rahasia")
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, promoteForm("2", "emergency", "old_master_dead", "1"), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusServiceUnavailable)
	wantNoBody(t, rec, "rahasia")
	entries, err := e.st.ListAudit(0, time.Time{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "promote" || strings.Contains(entries[0].Outcome, "rahasia") ||
		!strings.Contains(entries[0].Outcome, "rinciannya ada di log panel") {
		t.Errorf("audit of a promote whose view failed = %+v", entries)
	}
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
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "confirm_replica_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "tidak akan ada di master baru")
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "2", "confirm_replica_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama replika db-sebagian")
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3", "confirm_replica_name", "db-master"))
	wantCode(t, rec, http.StatusBadRequest)
	if len(f.promoteList()) != 0 {
		t.Fatalf("promote started without the partial candidate's acceptance")
	}

	// The offline replica cannot be chosen.
	rec = post(promoteForm("4", "planned"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "db-mati tidak bisa dipilih: Agent offline")

	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3", "confirm_replica_name", " db-sebagian "))
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

	// Emergency (the D5 tick given): the old master's name must be typed,
	// for any candidate.
	f.setView(emergencyView())
	before := len(f.promoteList())
	rec = post(promoteForm("2", "emergency", "old_master_dead", "1", "accept_unreachable", "4"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama master lama db-master")
	rec = post(promoteForm("2", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "confirm_name", "db-replika"))
	wantCode(t, rec, http.StatusBadRequest)
	// A partial candidate in an emergency: both names (spec 5.2 "juga").
	rec = post(promoteForm("3", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "accept_partial", "3", "confirm_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama master lama db-master")
	rec = post(promoteForm("3", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "accept_partial", "3", "confirm_name", "db-master"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama replika db-sebagian")
	rec = post(promoteForm("3", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_name", "db-sebagian", "confirm_replica_name", "db-master"))
	wantCode(t, rec, http.StatusBadRequest)
	if len(f.promoteList()) != before {
		t.Fatalf("emergency promote started without the master's name")
	}
	rec = post(promoteForm("2", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "confirm_name", "db-master"))
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	got = f.promoteList()
	if p := got[len(got)-1]; p.Mode != proto.PromoteEmergency || p.CandidateServerID != 2 || p.AcceptPartial || p.AcceptBranched {
		t.Fatalf("emergency StartPromote got %+v", p)
	}
	rec = post(promoteForm("3", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_name", "db-master", "confirm_replica_name", "db-sebagian"))
	wantCode(t, rec, http.StatusSeeOther)
	got = f.promoteList()
	if p := got[len(got)-1]; p.Mode != proto.PromoteEmergency || p.CandidateServerID != 3 || !p.AcceptPartial {
		t.Fatalf("emergency partial StartPromote got %+v", p)
	}
}

// TestPromoteEmergencyPageAsksBothNames: in an emergency a partial
// candidate's radio carries both names, and the button labels the first
// as the old master's.
func TestPromoteEmergencyPageAsksBothNames(t *testing.T) {
	e, f, _ := promoteEnv(t)
	f.setView(emergencyView())
	rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	radio := between(rec.Body.String(), `name="candidate" value="3"`, ">")
	if !strings.Contains(radio, `data-name="db-master" data-replica-name="db-sebagian"`) {
		t.Errorf("partial candidate in an emergency: radio = %s", radio)
	}
	radio = between(rec.Body.String(), `name="candidate" value="2"`, ">")
	if !strings.Contains(radio, `data-name="db-master" data-replica-name=""`) {
		t.Errorf("complete candidate in an emergency: radio = %s", radio)
	}
	wantBody(t, rec, `data-confirm-name="db-master"`, `data-confirm-name-label="Ketik nama master lama"`,
		`data-confirm-replica-name=""`, `x-ref="typedReplica"`)
}

// TestPromoteTokenBoundToActionAndMaster: a token for another action or
// another master is refused, and nothing starts.
func TestPromoteTokenBoundToActionAndMaster(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	form := promoteForm("2", "planned", "accept_unreachable", "4")
	other := e.pairedOnline(t, "db-master-lain")
	e.makeMaster(t, other)
	for _, tc := range []struct {
		action string
		id     int64
	}{{web.ActUnlockMaster, 1}, {web.ActTightenTLS, 1}, {web.ActPromote, other.ID}} {
		kind := "master"
		if tc.action == web.ActTightenTLS {
			kind = "server"
		}
		rec := e.do("POST", "/masters/1/promote", withToken(t, e, form, tc.action, kind, tc.id), c)
		wantCode(t, rec, http.StatusForbidden)
		wantBody(t, rec, msgConfirmInvalid)
	}
	if n := len(f.promoteList()); n != 0 {
		t.Fatalf("promote started with a foreign token: %d", n)
	}
}

// TestPromoteProbeFailedCandidate: a selectable replica whose probe of the
// master failed is never the default; chosen, its "tidak terjangkau" box is
// hidden and its own red box shown.
func TestPromoteProbeFailedCandidate(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := plannedView()
	v.Candidates[0].ProbeOK, v.Candidates[0].ProbeErr = nil, "tidak ada jawaban dalam 15 detik"
	// A complete, less current replica that answered.
	v.Candidates[1].Partial, v.Candidates[1].MissingDatabases = false, nil
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `name="candidate" value="3" checked`) || strings.Contains(body, `value="2" checked`) {
		t.Errorf("default choice is not the replica that answered")
	}
	// Not chosen: its unreachable box shows, its red box is hidden.
	if box := between(body, `class="banner banner-warning mb-0`, `data-unreachable="2"`); strings.Contains(box, "hidden") || box == "" {
		t.Errorf("unreachable box of db-replika = %q", box)
	}
	wantBody(t, rec, `hidden" data-promote-box="2"`, "Replika db-replika tidak terjangkau")

	// Chosen (a refused post shows the choice): the other way round.
	form := withToken(t, e, promoteForm("2", "planned"), web.ActPromote, "master", 1)
	rec = e.do("POST", "/masters/1/promote", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, `name="candidate" value="2" checked`, `hidden" data-unreachable="2"`,
		`banner-error mb-0" data-promote-box="2"`, "promote ke replika ini akan gagal")
	// The candidate itself needs no acceptance as unreachable.
	wantNoBody(t, rec, "Setujui bahwa replika db-replika")
	wantBody(t, rec, "Setujui bahwa replika db-mati")

	// Nothing complete answered: no default at all.
	v.Candidates[1].ProbeOK, v.Candidates[1].ProbeErr = nil, "putus"
	f.setView(v)
	wantNoBody(t, e.do("GET", "/masters/1/promote", nil, c), " checked data-name")
}

// TestPromotePostModeAndPartialValues: a post without mode is refused; of
// several accept_partial values the candidate's counts wherever it is.
func TestPromotePostModeAndPartialValues(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := plannedView()
	// Replica 2 partial too, so two partial boxes can be ticked.
	v.Candidates[0].Partial, v.Candidates[0].MissingDatabases = true, []string{"arsip"}
	f.setView(v)
	post := func(form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/1/promote", withToken(t, e, form, web.ActPromote, "master", 1), c)
	}
	form := promoteForm("3", "", "accept_unreachable", "4", "accept_partial", "3", "confirm_replica_name", "db-sebagian")
	form.Del("mode")
	rec := post(form)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Formulir promote tidak lengkap")
	if n := len(f.promoteList()); n != 0 {
		t.Fatalf("promote started without mode: %d", n)
	}

	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "2", "accept_partial", "3",
		"confirm_replica_name", "db-sebagian"))
	wantCode(t, rec, http.StatusSeeOther)
	if got := f.promoteList(); len(got) != 1 || got[0].CandidateServerID != 3 || !got[0].AcceptPartial {
		t.Fatalf("StartPromote got %+v", got)
	}
	// Only the other replica's box ticked: refused.
	rec = post(promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "2",
		"confirm_replica_name", "db-sebagian"))
	wantCode(t, rec, http.StatusBadRequest)
}

// TestPromotePlannedShowsMasterReportTime: the planned page dates the
// master's snapshot as the replicas' freshness is.
func TestPromotePlannedShowsMasterReportTime(t *testing.T) {
	e, f, _ := promoteEnv(t)
	v := plannedView()
	at := time.Date(2026, 11, 2, 9, 59, 50, 0, time.Local)
	v.LastReportAt = &at
	f.setView(v)
	wantBody(t, e.do("GET", "/masters/1/promote", nil, e.cookie()), `Laporan master: pukul <span class="mono">09:59:50</span>`)
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
	wantBody(t, rec, "Replika bercabang", "dibangun ulang", `name="accept_branched"`, `data-replica-name="db-replika"`)
	// No default choice: the user picks one of the branches.
	wantNoBody(t, rec, `value="2" checked`, `value="3" checked`)

	post := func(form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/1/promote", withToken(t, e, form, web.ActPromote, "master", 1), c)
	}
	rec = post(promoteForm("2", "planned", "confirm_replica_name", "db-replika"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Replika bercabang")
	rec = post(promoteForm("2", "planned", "accept_branched", "1"))
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Ketik nama replika db-replika")
	if n := len(f.promoteList()); n != 0 {
		t.Fatalf("branched promote started without acceptance: %d", n)
	}
	rec = post(promoteForm("2", "planned", "accept_branched", "1", "confirm_replica_name", "db-replika"))
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
	wantBody(t, rec, "Bisa ditulis (super_read_only mati)", "Replika db-sebagian melaporkan bisa ditulis")
	wantNoBody(t, rec, `name="candidate" value="3"`)

	form := withToken(t, e, promoteForm("3", "planned", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_replica_name", "db-sebagian"), web.ActPromote, "master", 1)
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

	// Connected again, no report since: the last report is old, so
	// emergency, as for an agent that is away.
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusAwaitingReport, Severity: monitor.SevNeutral}, Snap: masterSnap()})
	rec = e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, "Promote darurat")
	wantNoBody(t, rec, "Promote terencana")
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
		a.OldMasterDeadAck != b.OldMasterDeadAck || len(a.AcceptUnreachable) != len(b.AcceptUnreachable) {
		return false
	}
	for i := range a.AcceptUnreachable {
		if a.AcceptUnreachable[i] != b.AcceptUnreachable[i] {
			return false
		}
	}
	return true
}

// TestPromotePageColumnsAndBadges: the candidate table of the new design
// (spec 5.6, 5.8 #1): every column including "Bisa dipilih?", the markers
// on their own line under the name (never inside the name's link), the
// chosen row highlighted, and the destructive "Promote" button.
func TestPromotePageColumnsAndBadges(t *testing.T) {
	e, f, _ := promoteEnv(t)
	f.setView(plannedView())
	rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	head := between(body, "<thead>", "</thead>")
	for _, col := range []string{"Pilih", "Replika", "Status", "Lag", "Posisi terhadap master", "Cakupan database",
		"Laporan", "Bisa dipilih?"} {
		if !strings.Contains(head, ">"+col+"</th>") {
			t.Errorf("column %q missing from %q", col, head)
		}
	}
	// The table scrolls on its own below 1280 px.
	if !strings.Contains(body, `<div class="overflow-x-auto" data-candidates>`) {
		t.Errorf("candidate table not in an overflow-x-auto box")
	}
	// The name's link holds the name only; the badge follows it in an
	// element of its own.
	cell := between(body, `href="/replicas/2"`, "</td>")
	link, rest, ok := strings.Cut(cell, "</a>")
	if !ok || strings.Contains(link, "Paling mutakhir") || !strings.HasSuffix(link, ">db-replika") {
		t.Errorf("name link of db-replika = %q", link)
	}
	if !strings.Contains(rest, `<div class="mt-1 flex flex-wrap gap-1"><span class="lencana lencana-success">Paling mutakhir</span>`) {
		t.Errorf("Paling mutakhir badge not on its own line under the name: %q", rest)
	}
	// Partial and unreachable replicas are marked the same way.
	wantBody(t, rec, `<span class="lencana lencana-warning">Replika sebagian</span>`,
		`<span class="lencana lencana-warning">Tidak terjangkau</span>`)
	// The default choice's row is highlighted; the others are not.
	if row := between(body, `data-candidate-row="2"`, ">"); !strings.Contains(row, "bg-primary/5") {
		t.Errorf("chosen row = %q, want bg-primary/5", row)
	}
	if row := between(body, `data-candidate-row="3"`, ">"); strings.Contains(row, "bg-primary/5") {
		t.Errorf("unchosen row = %q highlighted", row)
	}
	// Why a replica cannot be chosen, readable in its column.
	if cell := between(body, `data-candidate-row="4"`, "</tr>"); !strings.Contains(cell, "Tidak: Agent offline") {
		t.Errorf("row of db-mati = %q, want its reason", cell)
	}
	// The button keeps its name and opens the danger-variant dialog.
	btn := between(body, `<button type="submit" class="btn btn-error`, "</button>")
	if !strings.HasSuffix(btn, ">Promote") || !strings.Contains(btn, `data-confirm-action="promote"`) ||
		!strings.Contains(btn, `data-confirm-danger="1"`) {
		t.Errorf("promote button = %q", btn)
	}
	// The most current replica cannot be chosen: its figure says why.
	v := plannedView()
	v.Candidates[0].Writable = true
	f.setView(v)
	rec2 := e.do("GET", "/masters/1/promote", nil, e.cookie())
	if note := between(rec2.Body.String(), "data-most-current-note>", "</span>"); !strings.Contains(note, "tidak bisa dipilih: Bisa ditulis (super_read_only mati)") {
		t.Errorf("most current figure note = %q", note)
	}
	f.setView(plannedView())
	// The summary figures from the consideration data.
	wantBody(t, rec, "Apa yang akan terjadi", "Laporan master", "Jeda tulis", "Kandidat paling mutakhir")
}

// TestPromotePageStepsList: the "Urutan langkah" card lists the steps of
// the promotion in order: six for a planned one, four for an emergency.
func TestPromotePageStepsList(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	steps := func(body string) []string {
		list := between(body, "<ol data-promote-steps", "</ol>")
		if list == "" {
			t.Fatalf("no <ol data-promote-steps> in the page")
		}
		var names []string
		for _, li := range strings.Split(list, "<li")[1:] {
			names = append(names, between(li, `<span class="font-semibold">`, "</span>")[len(`<span class="font-semibold">`):])
		}
		return names
	}
	f.setView(plannedView())
	got := steps(e.do("GET", "/masters/1/promote", nil, c).Body.String())
	want := []string{"Kunci master", "Replika mengejar", "Jadikan master baru", "Karantina master lama",
		"Arahkan replika lain", "Alihkan aplikasi"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("planned steps = %q, want %q", got, want)
	}
	f.setView(emergencyView())
	got = steps(e.do("GET", "/masters/1/promote", nil, c).Body.String())
	want = []string{"Jadikan master baru", "Karantina master lama", "Arahkan replika lain", "Alihkan aplikasi"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("emergency steps = %q, want %q", got, want)
	}
	// What the steps say about other replicas and the old master.
	wantBody(t, e.do("GET", "/masters/1/promote", nil, c),
		"Replika lain mengikuti master baru; yang tidak terjangkau diarahkan saat tersambung lagi; yang punya transaksi di luar master baru perlu dibangun ulang.",
		"Master lama dijadikan baca-saja di bawah master baru;")
	// Every step but the last (the user's own) is a step of the job.
	for _, mode := range []string{proto.PromotePlanned, proto.PromoteEmergency} {
		job := orchestrator.PromoteStepNames(mode)
		i := 0
		for _, s := range web.PromoteScreenSteps(mode) {
			if s == "Alihkan aplikasi" {
				continue
			}
			for i < len(job) && job[i] != s {
				i++
			}
			if i == len(job) {
				t.Errorf("%s: step %q not in the job's steps %q (in order)", mode, s, job)
			}
		}
	}
}

// The second promotion while the first old master is pending (spec 5.1).
const (
	// oldMasterPendingLine is T4 for db-<lama> as the page escapes it.
	oldMasterPendingLine = "master lama db-&lt;lama&gt; dari promote sebelumnya belum dibangun ulang atau dihapus; selesaikan itu dulu"
	// emergencyPendingLine is T5 for db-<lama> as the page escapes it.
	emergencyPendingLine = "Master lama db-&lt;lama&gt; dari promote sebelumnya belum dibangun ulang. Sesudah promote ini, " +
		"server itu tidak bisa lagi diperiksa atau dibangun ulang dari panel; yang tersisa hanya Hapus server."
)

// TestPlannedPromoteRefusedWhileOldMasterPending (T4): the planned
// consideration screen of a topology whose old master is pending says why
// it cannot run and offers no choice and no Promote button; without a
// pending old master it says nothing of it.
func TestPlannedPromoteRefusedWhileOldMasterPending(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := plannedView()
	v.PendingOldMaster = "db-<lama>"
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, oldMasterPendingLine, "data-old-master-pending")
	wantNoBody(t, rec, `name="candidate"`, `data-confirm-action="promote"`, "db-<lama>", "Sesudah promote ini")

	f.setView(plannedView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="candidate" value="2" checked`, `data-confirm-action="promote"`)
	wantNoBody(t, rec, "data-old-master-pending", "dari promote sebelumnya")
}

// TestEmergencyPromoteNotRefused (T5): the emergency consideration screen
// of a topology whose old master is pending warns what the promotion
// leaves of it, and still offers the choice and the Promote button.
func TestEmergencyPromoteNotRefused(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := emergencyView()
	v.PendingOldMaster = "db-<lama>"
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, emergencyPendingLine, "data-old-master-pending", `name="candidate" value="2"`,
		`data-confirm-action="promote"`)
	wantNoBody(t, rec, "selesaikan itu dulu", "db-<lama>")

	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, "data-old-master-pending", "dari promote sebelumnya")
}

// TestPromoteScreenNamesQuarantinedCandidate (T24): a quarantined server
// with a replica row is listed with its reason and cannot be chosen.
func TestPromoteScreenNamesQuarantinedCandidate(t *testing.T) {
	e, f, _ := promoteEnv(t)
	v := plannedView()
	v.Candidates = append(v.Candidates, orchestrator.Candidate{ServerID: 9, Name: "db-lama",
		Status: monitor.StatusQuarantined, ReportAgo: 4 * time.Second, Fresh: true, Reason: "Master lama yang dikarantina"})
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	row := between(rec.Body.String(), `data-candidate-row="9"`, "</tr>")
	for _, s := range []string{"db-lama", "Tidak: Master lama yang dikarantina"} {
		if !strings.Contains(row, s) {
			t.Errorf("row of the quarantined server lacks %q: %s", s, row)
		}
	}
	wantNoBody(t, rec, `name="candidate" value="9"`, "Replika gagal", "Replika sedang dibuat")
}

// promoteJobBlockedLine is the promote screen's blocker for an unfinished
// job of the topology (ruling D61).
const promoteJobBlockedLine = "Topologi ini masih punya pekerjaan yang belum selesai. Buka pekerjaan itu dan tekan Batalkan, lalu kembali ke sini."

// TestPromoteScreenNamesBlockingJob (ruling D61): an unfinished job of the
// topology, which the promote's POST refuses on, is named on the screen
// before anything is typed: the blocker, a link to that job ("Buka
// pekerjaan", or "Buka pekerjaan promote" for a promote job) and no
// candidate to choose. The earlier blockers keep their precedence.
func TestPromoteScreenNamesBlockingJob(t *testing.T) {
	for _, c := range []struct {
		kind, link string
	}{
		{orchestrator.KindCreateReplica, ">Buka pekerjaan</a>"},
		{orchestrator.KindPromoteEmergency, ">Buka pekerjaan promote</a>"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			e, f, _ := promoteEnv(t)
			f.setView(emergencyView())
			topo, err := e.st.GetTopologyByMaster(1)
			if err != nil {
				t.Fatal(err)
			}
			const id = "77777777777777777777777777777777"
			if err := e.st.CreateJob(store.Job{ID: id, Kind: c.kind, ServerID: 3, TopologyID: topo.ID, ParamsJSON: "{}",
				CreatedAt: time.Now()}, []string{"Langkah"}); err != nil {
				t.Fatal(err)
			}
			rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
			wantCode(t, rec, http.StatusOK)
			wantBody(t, rec, "data-promote-blocked", promoteJobBlockedLine, `href="/jobs/`+id+`"`, c.link)
			wantNoBody(t, rec, `name="candidate" value="2"`)

			// A cutover still pending keeps its blocker first.
			if err := e.st.SetCutoverPending(topo.ID, true); err != nil {
				t.Fatal(err)
			}
			rec = e.do("GET", "/masters/1/promote", nil, e.cookie())
			wantBody(t, rec, "Promote sebelumnya masih menunggu pengalihan aplikasi")
			wantNoBody(t, rec, promoteJobBlockedLine)
		})
	}
}

// Texts of the emergency promote (plan 5b-2b, D5-D7), as the page shows
// them.
const (
	wantD5 = "Saya sudah memastikan master lama mati atau terputus dari aplikasi"
	wantD6 = "Baris yang belum terpenuhi tidak menghalangi promote darurat."
	wantD7 = "Centang &#34;Saya sudah memastikan master lama mati atau terputus dari aplikasi&#34; untuk melanjutkan promote darurat."
	// d5Box is the D5 checkbox, required and not ticked.
	d5Box = `name="old_master_dead" value="1" required>`
)

// emergencyPost is a complete emergency post for db-replika (2), without
// the D5 tick.
func emergencyPost() url.Values {
	return promoteForm("2", "emergency", "accept_unreachable", "4", "confirm_name", "db-master")
}

// tickedPost is emergencyPost with the D5 tick.
func tickedPost() url.Values {
	form := emergencyPost()
	form.Set("old_master_dead", "1")
	return form
}

// TestEmergencyPromoteNeedsDeadMasterTick (D5, D7): the emergency screen
// has the D5 box, required and not ticked; a post without it (or with any
// value but "1") is refused with 400 and D7 before the token is used, and
// with it the orchestrator gets OldMasterDeadAck. A planned promote has
// no such box and does not ask for it.
func TestEmergencyPromoteNeedsDeadMasterTick(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantNoBody(t, rec, "old_master_dead", wantD5)
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"),
		web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	if p := f.promoteList(); len(p) != 1 || p[0].OldMasterDeadAck {
		t.Fatalf("planned promotes = %+v", p)
	}

	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, d5Box, wantD5)
	wantUnticked(t, rec.Body.String(), `name="old_master_dead"`)

	form := withToken(t, e, emergencyPost(), web.ActPromote, "master", 1)
	for _, v := range []string{"", "on", "true", "0"} {
		if v != "" {
			form.Set("old_master_dead", v)
		}
		rec = e.do("POST", "/masters/1/promote", form, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, wantD7, d5Box)
		wantUnticked(t, rec.Body.String(), `name="old_master_dead"`)
	}
	if len(f.promoteList()) != 1 {
		t.Fatal("emergency promote started without the tick")
	}
	entries := withoutConfirms(auditEntries(t, e.st))
	if last := entries[len(entries)-1]; last.Action != "promote" || !strings.Contains(last.Outcome, "untuk melanjutkan promote darurat") {
		t.Fatalf("audit of the refusal = %+v", last)
	}
	form.Set("old_master_dead", "1")
	rec = e.do("POST", "/masters/1/promote", form, c) // the same token
	wantCode(t, rec, http.StatusSeeOther)
	got := f.promoteList()
	want := orchestrator.PromoteParams{MasterServerID: 1, CandidateServerID: 2, Mode: proto.PromoteEmergency,
		AcceptUnreachable: []int64{4}, OldMasterDeadAck: true}
	if len(got) != 2 || !promoteParamsEqual(got[1], want) {
		t.Fatalf("StartPromote got %+v, want %+v", got, want)
	}

	// The orchestrator's own refusal reads the same.
	f.mu.Lock()
	f.promoteErr = orchestrator.ErrOldMasterAck
	f.mu.Unlock()
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantD7)
}

// allRed is a gate in which every row that can be red is, between a
// green and a yellow one; the agent's words are in a detail.
func allRed(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "agent_version", Label: "Versi agent", Status: orchestrator.GateRed, Detail: "Agent db-master versi <agent-kata>."},
		{Key: "master_pin", Label: "Pin master", Status: orchestrator.GateRed, Detail: "Pin tidak ada."},
		{Key: "clock", Label: "Jam (clock)", Status: orchestrator.GateRed, Detail: "Jam berbeda 5 menit."},
		{Key: "tls", Label: "Sertifikat TLS", Status: orchestrator.GateRed, Detail: "Tanpa sertifikat."},
		{Key: "account", Label: "Akun panel", Status: orchestrator.GateRed, Detail: "Akun root."},
		{Key: "backup", Label: "Cadangan (backup)", Status: orchestrator.GateRed, Detail: "Cadangan usang."},
		{Key: "master_port", Label: "Port master", Status: orchestrator.GateYellow, Detail: "Port menjawab."},
		{Key: "audit_forward", Label: "Log audit"},
	}}, nil
}

var allRedKeys = []string{"agent_version", "master_pin", "clock", "tls", "account", "backup"}

// TestEmergencyPromoteStartsWithEveryRowRed: with every row red the
// emergency screen keeps its button on, shows the list with D6 above it
// and no D2, and the post starts the promote.
func TestEmergencyPromoteStartsWithEveryRowRed(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(emergencyView())
	f.setGate(allRed)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantOn(t, body, `data-confirm-action="promote"`)
	list := between(body, `data-promote-box="2" data-gate-box`, "</section>")
	if !strings.Contains(list, wantD6) || strings.Index(list, wantD6) > strings.Index(list, `data-gate-row="agent_version"`) {
		t.Fatalf("D6 not above the list:\n%s", list)
	}
	wantNoBody(t, rec, wantD2)
	for _, in := range f.gateInputs() {
		if in.Action != orchestrator.GatePromoteEmergency {
			t.Fatalf("emergency screen read the gate of %s", in.Action)
		}
	}
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	if len(f.promoteList()) != 1 {
		t.Fatal("emergency promote with every row red did not start")
	}

	// D6 is the emergency's only: a planned list does not carry it.
	f.setView(plannedView())
	f.setGate(nil)
	wantNoBody(t, e.do("GET", "/masters/1/promote", nil, c), wantD6)
}

// TestEmergencyPromoteAuditFields: the started emergency promote's audit
// entry records the tick, the time the panel received the master's last
// report (RFC 3339, UTC) and the keys of the rows that were red then, in
// the list's order, and none of a row's text.
func TestEmergencyPromoteAuditFields(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := emergencyView()
	f.setView(v)
	f.setGate(allRed)
	rec := e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	entries := withoutConfirms(auditEntries(t, e.st))
	last := entries[len(entries)-1]
	if last.Action != "promote" || last.Outcome != "ok" {
		t.Fatalf("audit = %+v", last)
	}
	var detail struct {
		OldMasterDead    *bool    `json:"old_master_dead"`
		MasterLastReport *string  `json:"master_last_report"`
		SkippedRed       []string `json:"skipped_red"`
	}
	if err := json.Unmarshal([]byte(last.DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	wantReport := v.LastReportAt.UTC().Format(time.RFC3339)
	if detail.OldMasterDead == nil || !*detail.OldMasterDead || detail.MasterLastReport == nil ||
		*detail.MasterLastReport != wantReport || !slices.Equal(detail.SkippedRed, allRedKeys) {
		t.Fatalf("audit detail = %s, want old_master_dead true, master_last_report %s, skipped_red %v",
			last.DetailJSON, wantReport, allRedKeys)
	}
	for _, s := range []string{"agent-kata", "Jam berbeda", "Versi agent", "Cadangan usang"} {
		if strings.Contains(last.DetailJSON, s) {
			t.Fatalf("audit carries a row's text %q: %s", s, last.DetailJSON)
		}
	}

	// No report ever received: "". No red row: an empty list.
	v.LastReportAt = nil
	f.setView(v)
	f.setGate(nil)
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	entries = withoutConfirms(auditEntries(t, e.st))
	if d := entries[len(entries)-1].DetailJSON; !strings.Contains(d, `"master_last_report":""`) || !strings.Contains(d, `"skipped_red":[]`) {
		t.Fatalf("audit detail without report or red row = %s", d)
	}

	// The gate cannot judge (an error, not a red row): an emergency
	// promote is never refused by the gate, so it still starts, and the
	// audit records that no rows were read (null, not an empty list).
	f.setGate(func(orchestrator.GateInput) (orchestrator.GateResult, error) {
		return orchestrator.GateResult{}, errors.New("prasyarat: server #1: tidak ditemukan")
	})
	before := len(f.promoteList())
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	if len(f.promoteList()) != before+1 {
		t.Fatal("emergency promote with a gate error did not start")
	}
	entries = withoutConfirms(auditEntries(t, e.st))
	if last := entries[len(entries)-1]; last.Outcome != "ok" || !strings.Contains(last.DetailJSON, `"skipped_red":null`) {
		t.Fatalf("audit of an emergency promote with a gate error = %+v", last)
	}
	f.setGate(nil)

	// A planned promote records none of the three.
	f.setView(plannedView())
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"),
		web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	entries = withoutConfirms(auditEntries(t, e.st))
	if d := entries[len(entries)-1].DetailJSON; strings.Contains(d, "old_master_dead") || strings.Contains(d, "master_last_report") ||
		strings.Contains(d, "skipped_red") {
		t.Fatalf("planned audit detail = %s", d)
	}
}

// TestPromoteAuditReportReceived (SEC-274): beside master_last_report
// (the panel's clock less the age the agent stated), an emergency
// promote's audit records when the panel received that report, RFC 3339
// in UTC, and "" when none arrived since the panel started.
func TestPromoteAuditReportReceived(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := emergencyView()
	arrived := time.Date(2026, 11, 2, 10, 4, 0, 0, time.FixedZone("WITA", 8*3600))
	v.LastReportArrivedAt = &arrived
	f.setView(v)
	f.setGate(nil)
	lastDetail := func() map[string]any {
		t.Helper()
		entries := withoutConfirms(auditEntries(t, e.st))
		last := entries[len(entries)-1]
		if last.Action != "promote" || last.Outcome != "ok" {
			t.Fatalf("audit = %+v", last)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(last.DetailJSON), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	wantCode(t, e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c),
		http.StatusSeeOther)
	d := lastDetail()
	if d["master_report_received"] != "2026-11-02T02:04:00Z" ||
		d["master_last_report"] != v.LastReportAt.UTC().Format(time.RFC3339) {
		t.Fatalf("audit detail = %v; want master_report_received 2026-11-02T02:04:00Z beside master_last_report", d)
	}

	v.LastReportArrivedAt = nil
	f.setView(v)
	wantCode(t, e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c),
		http.StatusSeeOther)
	if got, ok := lastDetail()["master_report_received"]; !ok || got != "" {
		t.Fatalf("master_report_received without an arrival = %v (present %v), want \"\"", got, ok)
	}
}

// TestEmergencyPromoteCandidateRulesUnchanged (K1, K2): with the tick, a
// candidate whose agent is of another version still cannot be chosen, and
// an unfinished job of the topology still blocks the screen and refuses.
func TestEmergencyPromoteCandidateRulesUnchanged(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	v := emergencyView()
	v.Candidates[0].Eligible, v.Candidates[0].Reason = false, "Agent perlu dimutakhirkan"
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, `name="candidate" value="2"`)
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "db-replika tidak bisa dipilih: Agent perlu dimutakhirkan")
	if len(f.promoteList()) != 0 {
		t.Fatal("promote of an outdated candidate started")
	}

	f.setView(emergencyView())
	topo, err := e.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateJob(store.Job{ID: "77777777777777777777777777777777", Kind: orchestrator.KindCreateReplica, ServerID: 3,
		TopologyID: topo.ID, ParamsJSON: "{}", CreatedAt: time.Now()}, []string{"Langkah"}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantBody(t, rec, promoteJobBlockedLine)
	wantNoBody(t, rec, `name="candidate" value="2"`, d5Box)
	f.mu.Lock()
	f.promoteErr = fmt.Errorf("topologi ini %w", orchestrator.ErrBusy)
	f.mu.Unlock()
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, tickedPost(), web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, promoteJobBlockedLine)
}

// T12c and T12d as the page shows them (quotes escaped).
const (
	wantT12cStart = "Master db-master masih terkunci, atau kunci promote terencana sebelumnya belum terbukti dibuka. " +
		"Tekan &#34;Buka kunci promote&#34; pada pekerjaan promote "
	wantT12cEnd = " dulu, lalu mulai promote ini lagi."
	wantT12d    = "Master db-master masih terkunci. Buka kuncinya dulu dengan &#34;Buka kunci master&#34; di halaman master, " +
		"lalu mulai promote ini lagi."
)

func (f *fakeOrch) setLockRecords(recs ...orchestrator.LockRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lockRecords = recs
}

// TestPromoteScreenShowsLockRefusal (S-2): the planned screen of a master
// whose promote lock record stands shows T12c with a link to the newest
// record's job before anything is typed, with no candidate to choose and
// no Promote button; a master reading Terkunci without a record shows
// T12d. The emergency screen keeps its choice (the clock rule only).
func TestPromoteScreenShowsLockRefusal(t *testing.T) {
	const job, older = "11111111111111111111111111111111", "22222222222222222222222222222222"
	e, f, mon := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	f.setLockRecords(orchestrator.LockRecord{JobID: job, MasterID: 1, CandidateID: 2},
		orchestrator.LockRecord{JobID: older, MasterID: 1, CandidateID: 3})
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-promote-blocked", wantT12cStart+job+wantT12cEnd, `href="/jobs/`+job+`"`,
		">Buka pekerjaan promote</a>")
	wantNoBody(t, rec, `name="candidate"`, `data-confirm-action="promote"`, older)

	f.setLockRecords()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusLocked, Severity: monitor.SevCrit}, Snap: masterSnap()})
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-promote-blocked", wantT12d)
	wantNoBody(t, rec, `name="candidate"`, `data-confirm-action="promote"`, "Buka pekerjaan promote")

	f.setLockRecords(orchestrator.LockRecord{JobID: job, MasterID: 1, CandidateID: 2})
	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="candidate" value="2"`, `data-confirm-action="promote"`)
	wantNoBody(t, rec, "data-promote-blocked", "masih terkunci, atau kunci promote")
}

// TestLockRefusalLinksJob (K13): a planned promote StartPromote refuses
// with T12c shows the job it names as a link to it.
func TestLockRefusalLinksJob(t *testing.T) {
	const job = "33333333333333333333333333333333"
	e, f, _ := promoteEnv(t)
	f.setView(plannedView())
	f.mu.Lock()
	f.promoteErr = &orchestrator.LockStandsError{Code: "planned", Master: "db-master", JobID: job}
	f.mu.Unlock()
	form := promoteForm("2", "planned", "accept_unreachable", "4", "confirm_name", "db-master")
	rec := e.do("POST", "/masters/1/promote", withToken(t, e, form, web.ActPromote, "master", 1), e.cookie())
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantT12cStart, `<a class="link mono" href="/jobs/`+job+`">`+job+`</a>`+wantT12cEnd)
}

// TestPromoteScreenShowsWritableTextBeforeTyping (R-198): another replica
// read writable (T24), not answering with a writable last report (T24b),
// or, planned, not readable (T24c) blocks the screen on GET, before
// anything is typed: the text is shown and no candidate or button is
// offered.
func TestPromoteScreenShowsWritableTextBeforeTyping(t *testing.T) {
	cases := []struct {
		name string
		set  func(c *orchestrator.Candidate)
		want string
	}{
		{"writable", func(c *orchestrator.Candidate) { c.Writable = true },
			"Replika db-mati melaporkan bisa ditulis (super_read_only mati), jadi mungkin sudah menjadi master; promote ditolak, " +
				"juga promote darurat. Bila db-mati memang bukan master (misalnya dibuka dengan tangan untuk perbaikan): kunci lagi " +
				"di halaman replikanya, atau lepas dengan Lepas replika, lalu buka halaman ini lagi. Bila aplikasi sudah menulis ke " +
				"db-mati: jangan promote; periksa server itu dulu."},
		{"writable silent", func(c *orchestrator.Candidate) { c.WritableSilent = true },
			"Replika db-mati terakhir melaporkan bisa ditulis dan sekarang tidak menjawab, jadi panel tidak bisa memastikannya; " +
				"promote ditolak, juga promote darurat. Bila Anda yakin db-mati bukan master: lepas dengan Lepas replika, lalu " +
				"buka halaman ini lagi."},
		{"unreadable", func(c *orchestrator.Candidate) { c.Unread = true },
			"Replika db-mati tidak bisa dibaca sekarang, jadi panel tidak bisa memastikan ia masih baca-saja; promote terencana " +
				"ditolak. Sambungkan agent replika itu, atau lepas dengan Lepas replika, lalu buka halaman ini lagi."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := promoteEnv(t)
			v := plannedView()
			tc.set(&v.Candidates[2])
			f.setView(v)
			rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
			wantCode(t, rec, http.StatusOK)
			if got := between(rec.Body.String(), "data-promote-blocked>", "</div>\n</div>"); !strings.Contains(got, tc.want) {
				t.Fatalf("blocked banner = %q, want %q", got, tc.want)
			}
			wantNoBody(t, rec, `name="candidate" value="2"`, `<button type="submit" class="btn btn-error`)

			// The post is refused with the same text.
			form := withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"), web.ActPromote, "master", 1)
			rec = e.do("POST", "/masters/1/promote", form, e.cookie())
			wantCode(t, rec, http.StatusBadRequest)
			wantBody(t, rec, tc.want)
			if len(f.promoteList()) != 0 {
				t.Fatal("promote started over another replica")
			}
		})
	}
}

// TestEmergencyScreenNamesUnreadReplicas (T24d, NR-7): an emergency is
// not held by replicas that could not be read and last reported read-only
// or never reported; the screen names them and still offers the start.
func TestEmergencyScreenNamesUnreadReplicas(t *testing.T) {
	e, f, _ := promoteEnv(t)
	v := emergencyView()
	v.Candidates[1].Unread, v.Candidates[2].Unread = true, true
	v.Unread = []string{"db-sebagian", "db-mati"}
	f.setView(v)
	rec := e.do("GET", "/masters/1/promote", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Replika berikut tidak bisa dibaca sekarang; panel memakai laporan terakhirnya (baca-saja), atau tidak "+
		"punya laporan sama sekali: db-sebagian, db-mati.", `<button type="submit" class="btn btn-error`)
	wantNoBody(t, rec, "data-promote-blocked")
}
