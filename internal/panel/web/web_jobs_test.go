package web_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestJobDetailTimeline: the job's steps are a vertical timeline, each
// step with a status icon in its tone; the running step is highlighted;
// the log is a Space Mono block and the list's times are Space Mono.
func TestJobDetailTimeline(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	now := time.Now()
	newTestJob(t, e.st, waitingJob, orchestrator.KindPrepareMaster, "Periksa master", "Mulai ulang MySQL", "Verifikasi", "Lewat")
	e.st.SetStep(waitingJob, 0, store.StepDone, "", now)
	e.st.SetStep(waitingJob, 1, store.StepRunning, "", now)
	e.st.SetStep(waitingJob, 3, store.StepSkipped, "", now)
	if err := e.st.UpdateJob(waitingJob, store.JobRunning, 1, "", now); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/jobs/"+waitingJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	list := between(body, `<ol class="timeline`, "</ol>")
	if list == "" {
		t.Fatalf("steps are not an <ol class=\"timeline\">")
	}
	for i, want := range []struct{ status, tone string }{
		{store.StepDone, "success"}, {store.StepRunning, "primary"}, {store.StepPending, "neutral"}, {store.StepSkipped, "neutral"},
	} {
		li := between(list, `data-step="`+string(rune('0'+i))+`"`, "</li>")
		if !strings.Contains(li, `data-langkah="`+want.status+`"`) {
			t.Errorf("step %d = %q, want status %s", i, li, want.status)
		}
		if !strings.Contains(li, `langkah-ikon"`) || !strings.Contains(li, "<svg") {
			t.Errorf("step %d has no status icon: %q", i, li)
		}
	}
	// The icon's tone follows the status (CSS on data-langkah), for
	// every status a step can take.
	css := readCSS(t)
	for status, tone := range map[string]string{store.StepDone: "success", store.StepRunning: "primary",
		store.StepWaiting: "warning", store.StepFailed: "error", store.StepPending: "neutral", store.StepSkipped: "neutral"} {
		if !strings.Contains(css, `[data-langkah=`+status+`]`) {
			t.Errorf("app.css has no rule for step status %s (%s)", status, tone)
		}
	}
	// The running step is highlighted.
	if li := between(list, `data-step="1"`, "</li>"); !strings.Contains(li, "bg-primary/5") {
		t.Errorf("running step not highlighted: %q", li)
	}
	if li := between(list, `data-step="0"`, "</li>"); strings.Contains(li, "bg-primary/5") {
		t.Errorf("done step highlighted: %q", li)
	}
	// Log in a Space Mono block.
	if box := between(body, "Log</h2>", "data-job-log "); !strings.Contains(box, "mono") {
		t.Errorf("log block is not Space Mono: %q", box)
	}
	// The list's times in Space Mono 12 px.
	rec = e.do("GET", "/jobs", nil, c)
	wantCode(t, rec, http.StatusOK)
	if row := between(rec.Body.String(), `href="/jobs/`+waitingJob+`"`, "</tr>"); strings.Count(row, `<td class="mono text-xs whitespace-nowrap`) != 2 {
		t.Errorf("Dibuat/Selesai not in Space Mono 12 px: %q", row)
	}
	// At 390 px: Status is the second column and Selesai shows only from md.
	head := between(rec.Body.String(), "<thead>", "</thead>")
	if !strings.Contains(head, `<th>Pekerjaan</th><th>Status</th><th>Server</th><th>Dibuat</th><th class="hidden md:table-cell">Selesai</th>`) {
		t.Errorf("job table head is not Pekerjaan, Status, Server, Dibuat, Selesai (md+): %q", head)
	}
	row := between(rec.Body.String(), `href="/jobs/`+waitingJob+`"`, "</tr>")
	if i, j := strings.Index(row, "lencana"), strings.Index(row, `href="/servers/`); i < 0 || j < 0 || i > j {
		t.Errorf("status badge is not before the server: %q", row)
	}
	if !strings.Contains(row, `<td class="mono text-xs whitespace-nowrap hidden md:table-cell">`) {
		t.Errorf("Selesai cell is not hidden below md: %q", row)
	}
}

// readCSS is the built web/static/app.css without quotes, so selectors
// read the same minified or not.
func readCSS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../web/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), `"`, "")
}

// storeJob stores a job of kind on serverID with params, at status at its
// last step.
func storeJob(t *testing.T, st *store.Store, id, kind string, serverID int64, params, status string, steps ...string) {
	t.Helper()
	if err := st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: serverID, ParamsJSON: params, CreatedAt: time.Now()},
		steps); err != nil {
		t.Fatal(err)
	}
	last := len(steps) - 1
	stepStatus := map[string]string{store.JobFailed: store.StepFailed, store.JobWaiting: store.StepWaiting}[status]
	if err := st.SetStep(id, last, stepStatus, "pesan tahap", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob(id, status, last, "gagal menyalin", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// jobStatus is the stored status of job id.
func jobStatus(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	j, _, err := st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status
}

// lastAck is the JobAck of the last Approve or Retry.
func (f *fakeOrch) lastAck(t *testing.T) orchestrator.JobAck {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.acks) == 0 {
		t.Fatal("no Approve or Retry reached the orchestrator")
	}
	return f.acks[len(f.acks)-1]
}

const (
	resyncJob  = "1111111111111111111111111111111a"
	resyncJob2 = "1111111111111111111111111111111b"
)

// failedResyncJob stores a resync of the replica (server 2) that failed at
// Salin data, dropping wipe; the orchestrator calls it a dropping job.
func failedResyncJob(t *testing.T, e *env, f *fakeOrch, id string, wipe ...string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"master_server_id": 1, "resync": true, "wipe_list": wipe})
	storeJob(t, e.st, id, orchestrator.KindResyncReplica, 2, string(b), store.JobFailed,
		"Periksa master", "Periksa server tujuan", "Siapkan replika", "Salin data")
	f.mu.Lock()
	if f.drops == nil {
		f.drops = map[string]bool{}
	}
	f.drops[id] = true
	f.mu.Unlock()
}

// retryButton is the confirming button of the retry dialog of job id on
// serverID.
func retryButton(id string, serverID int) string {
	return fmt.Sprintf(`data-confirm-action="job_retry:%s" data-confirm-kind="job" data-confirm-id="%d" data-confirm-danger="1"`,
		id, serverID)
}

// TestRetryResyncFailedAtCopyNeedsToken (spec 5.10): "Ulangi" of a resync
// that failed while copying opens a dialog; a post without a token is
// refused with 403 on the job page and the job stays failed; with a token
// the orchestrator gets a confirmed Retry.
func TestRetryResyncFailedAtCopyNeedsToken(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedResyncJob(t, e, f, resyncJob, "gudang", "toko")

	rec := e.do("GET", "/jobs/"+resyncJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/jobs/`+resyncJob+`/retry/prepare"`, "Ulangi dari tahap ini", `x-data="openDialog"`,
		`data-open="false"`, retryButton(resyncJob, 2))

	rec = e.do("POST", "/jobs/"+resyncJob+"/retry", url.Values{}, c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid, "Resync ulang", "Salin data")
	wantCalls(t, f)
	if s := jobStatus(t, e.st, resyncJob); s != store.JobFailed {
		t.Fatalf("job status after a refused retry = %s", s)
	}

	form := withToken(t, e, url.Values{}, "job_retry:"+resyncJob, "job", 2)
	rec = e.do("POST", "/jobs/"+resyncJob+"/retry", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+resyncJob)
	wantCalls(t, f, "retry "+resyncJob)
	if ack := f.lastAck(t); !ack.Confirmed || len(ack.WipeOnly) != 0 || ack.AcceptConnections {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestRetryDialogRegroupsAndAsksWipeOnly (spec 5.6): /retry/prepare reads
// the replica's and the master's databases again; the dialog groups the
// job's wipe list on them and asks for each replica-only database; the
// retry needs every one acknowledged, and only those of the list.
func TestRetryDialogRegroupsAndAsksWipeOnly(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.results = map[int64]proto.ConnectResult{
		1: {Databases: shopDatabases},
		2: {Databases: append(slices.Clone(shopDatabases), proto.DatabaseInfo{Name: "arsip <lama>"})},
	}
	failedResyncJob(t, e, f, resyncJob, "arsip <lama>", "gudang", "toko")

	rec := e.do("POST", "/jobs/"+resyncJob+"/retry/prepare", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+resyncJob+"?buka=ulangi")
	wantCalls(t, f, "refresh 2", "refresh 1")

	rec = e.do("GET", "/jobs/"+resyncJob+"?buka=ulangi", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `data-open="true"`, "ada di master, disalin ulang", "hanya di replika, isinya hilang permanen",
		`<input type="checkbox" name="wipe_only[]" value="arsip &lt;lama&gt;" class="checkbox checkbox-sm" required>`,
		`action="/jobs/`+resyncJob+`/retry"`, retryButton(resyncJob, 2))
	wantNoBody(t, rec, `name="wipe_only[]" value="toko"`, `name="wipe_only[]" value="gudang"`, "arsip <lama>")

	retry := func(only ...string) *httptest.ResponseRecorder {
		form := withToken(t, e, url.Values{"wipe_only[]": only}, "job_retry:"+resyncJob, "job", 2)
		return e.do("POST", "/jobs/"+resyncJob+"/retry", form, c)
	}
	for _, only := range [][]string{nil, {"gudang"}, {"arsip <lama>", "lain"}, {"arsip <lama>", "arsip <lama>"}} {
		rec = retry(only...)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Daftar database tidak sesuai dengan inventaris")
	}
	wantCalls(t, f, "refresh 2", "refresh 1")
	rec = retry("arsip <lama>")
	wantCode(t, rec, http.StatusSeeOther)
	if ack := f.lastAck(t); !ack.Confirmed || !slices.Equal(ack.WipeOnly, []string{"arsip <lama>"}) {
		t.Fatalf("ack = %+v", ack)
	}

	// A refresh that fails opens nothing and says why.
	f.refreshErr = map[int64]error{1: errors.New("agent master tidak terhubung")}
	rec = e.do("POST", "/jobs/"+resyncJob+"/retry/prepare", url.Values{}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "agent master tidak terhubung", `data-open="false"`)
}

// TestApproveLeftoverNeedsToken: approving the drop of an earlier
// attempt's leftovers needs a job_approve token for that job.
func TestApproveLeftoverNeedsToken(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "2222222222222222222222222222222a"
	waitingJobFor(t, e.st, id, orchestrator.KindCreateReplica, orchestrator.WaitWipe)
	f.drops = map[string]bool{id: true}

	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `x-data="confirmApproval"`, `action="/jobs/`+id+`/approve"`,
		`data-confirm-action="job_approve:`+id+`" data-confirm-kind="job" data-confirm-id="1" data-confirm-danger="1"`)

	rec = e.do("POST", "/jobs/"+id+"/approve", url.Values{}, c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid, "Ya, hapus dan salin ulang")
	wantCalls(t, f)
	// A retry token does not approve.
	rec = e.do("POST", "/jobs/"+id+"/approve", withToken(t, e, url.Values{}, "job_retry:"+id, "job", 1), c)
	wantCode(t, rec, http.StatusForbidden)
	wantCalls(t, f)

	rec = e.do("POST", "/jobs/"+id+"/approve", withToken(t, e, url.Values{}, "job_approve:"+id, "job", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "approve "+id)
	if ack := f.lastAck(t); !ack.Confirmed {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestRetryRebuildRechecksConnections: the retry dialog of the old master's
// rebuild reads its connections and asks for the acknowledgement while any
// remain; the orchestrator's refusal comes back as 400.
func TestRetryRebuildRechecksConnections(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "3333333333333333333333333333333a"
	storeJob(t, e.st, id, orchestrator.KindCreateReplica, 1, `{"master_server_id":2,"rebuild_old_master":true}`,
		store.JobFailed, "Periksa master", "Periksa server tujuan")
	f.drops = map[string]bool{id: true}
	f.connections = map[int64]proto.ConnectionsResult{1: {Typed: true, TakenAt: time.Now(),
		Rows: []proto.ConnectionRow{{Host: "10.0.0.9", User: "app", Count: 2, Kind: "sql"}}}}

	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantCode(t, rec, http.StatusOK)
	if f.connCalls[1] != 0 {
		t.Fatal("the job page read the connections without the retry dialog")
	}
	wantNoBody(t, rec, `name="accept_connections"`)

	rec = e.do("GET", "/jobs/"+id+"?buka=ulangi", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="accept_connections" value="1" required`, "Saya paham 2 koneksi masih ada dan tulisannya hilang",
		retryButton(id, 1))

	// A complete, empty list asks for nothing.
	f.connections[1] = proto.ConnectionsResult{Typed: true, TakenAt: time.Now()}
	rec = e.do("GET", "/jobs/"+id+"?buka=ulangi", nil, c)
	wantNoBody(t, rec, `name="accept_connections"`)

	f.err = orchestrator.ErrConnectionsRemain
	rec = e.do("POST", "/jobs/"+id+"/retry", withToken(t, e, url.Values{}, "job_retry:"+id, "job", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, orchestrator.ErrConnectionsRemain.Error())
	if ack := f.lastAck(t); !ack.Confirmed || ack.AcceptConnections {
		t.Fatalf("ack = %+v", ack)
	}
	f.err = nil
	form := withToken(t, e, url.Values{"accept_connections": {"1"}}, "job_retry:"+id, "job", 1)
	wantCode(t, e.do("POST", "/jobs/"+id+"/retry", form, c), http.StatusSeeOther)
	if ack := f.lastAck(t); !ack.Confirmed || !ack.AcceptConnections {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestApproveRebuildRechecksConnections (SP-1): the approve dialog of the
// old master's rebuild reads its connections and asks for the
// acknowledgement while any remain, tied to the approve form; the
// approval forwards it, and the orchestrator's refusal comes back as 400.
func TestApproveRebuildRechecksConnections(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "3333333333333333333333333333333b"
	storeJob(t, e.st, id, orchestrator.KindCreateReplica, 1, `{"master_server_id":2,"rebuild_old_master":true,"wait_reason":"wipe"}`,
		store.JobWaiting, "Periksa master", "Periksa server tujuan")
	f.drops = map[string]bool{id: true}
	f.connections = map[int64]proto.ConnectionsResult{1: {Typed: true, TakenAt: time.Now(),
		Rows: []proto.ConnectionRow{{Host: "10.0.0.9", User: "app", Count: 2, Kind: "sql"}}}}

	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="accept_connections" value="1" form="form-setujui" required`,
		"Saya paham 2 koneksi masih ada dan tulisannya hilang")

	// A complete, empty list asks for nothing.
	f.connections[1] = proto.ConnectionsResult{Typed: true, TakenAt: time.Now()}
	rec = e.do("GET", "/jobs/"+id, nil, c)
	wantNoBody(t, rec, `name="accept_connections"`)

	f.err = orchestrator.ErrConnectionsRemain
	rec = e.do("POST", "/jobs/"+id+"/approve", withToken(t, e, url.Values{}, "job_approve:"+id, "job", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, orchestrator.ErrConnectionsRemain.Error())
	if ack := f.lastAck(t); !ack.Confirmed || ack.AcceptConnections {
		t.Fatalf("ack = %+v", ack)
	}
	f.err = nil
	form := withToken(t, e, url.Values{"accept_connections": {"1"}}, "job_approve:"+id, "job", 1)
	wantCode(t, e.do("POST", "/jobs/"+id+"/approve", form, c), http.StatusSeeOther)
	if ack := f.lastAck(t); !ack.Confirmed || !ack.AcceptConnections {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestWipeApprovalListsEveryName (SP-5): the approve dialog of a WaitWipe
// wait lists every database it drops from the job's state, also when the
// step message was cut.
func TestWipeApprovalListsEveryName(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "3333333333333333333333333333333c"
	var names []string
	for i := range 300 {
		names = append(names, fmt.Sprintf("database_rumah_sakit_%04d", i))
	}
	b, _ := json.Marshal(map[string]any{"master_server_id": 2, "wait_reason": "wipe", "wait_list": names})
	storeJob(t, e.st, id, orchestrator.KindCreateReplica, 1, string(b), store.JobWaiting, "Periksa master", "Periksa server tujuan")
	f.drops = map[string]bool{id: true}
	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantCode(t, rec, http.StatusOK)
	for _, n := range []string{names[0], names[150], names[299]} {
		wantBody(t, rec, ">"+n+"<")
	}
	wantBody(t, rec, "Semua 300 database yang dihapus")
}

// TestPromoteRetryStillTokenless and TestTightenTLSRetryTokenless: jobs
// that drop nothing keep their plain buttons and need no token: a planned
// promote once mysql.promote was sent (a planned one before it needs a
// token, TestGateRefusesPlannedRetryBeforeTokenIsConsumed), and a promote
// of either kind past the point of no return (an emergency one that failed before it offers no Ulangi,
// TestPromoteRetryRefusedHidesButton).
func TestPromoteRetryStillTokenless(t *testing.T) {
	tokenlessRetry(t, orchestrator.KindPromotePlanned, `{"promote_sent":true}`)
	tokenlessRetry(t, orchestrator.KindPromotePlanned, `{"promote_sent":true,"past_point":true}`)
	tokenlessRetry(t, orchestrator.KindPromoteEmergency, `{"promote_sent":true}`)
}

func TestTightenTLSRetryTokenless(t *testing.T) {
	tokenlessRetry(t, orchestrator.KindTightenTLS, `{}`)
}

// TestPromoteRetryRefusedHidesButton (PC-1): an emergency promote job that
// failed before the master was replaced offers no Ulangi; the page says why
// and where to start a new promote. Past the point (tokenlessRetry) it does.
func TestPromoteRetryRefusedHidesButton(t *testing.T) {
	e, f, _ := controlEnv(t)
	const id = "4444444444444444444444444444444b"
	storeJob(t, e.st, id, orchestrator.KindPromoteEmergency, 1, `{}`, store.JobFailed, "Tahap")
	f.retryRefused = map[string]string{id: orchestrator.ErrPromoteRetryBeforePoint.Error()}
	rec := e.do("GET", "/jobs/"+id, nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Promote ini gagal sebelum master diganti. &#34;Ulangi dari tahap ini&#34; tidak tersedia untuk keadaan ini: "+
		"mulai promote baru dari halaman master.")
	wantNoBody(t, rec, `action="/jobs/`+id+`/retry"`, "/retry/prepare", ">Ulangi dari tahap ini<")
}

func tokenlessRetry(t *testing.T, kind, params string) {
	t.Helper()
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "4444444444444444444444444444444a"
	storeJob(t, e.st, id, kind, 1, params, store.JobFailed, "Tahap")
	rec := e.do("GET", "/jobs/"+id, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `<form method="post" action="/jobs/`+id+`/retry"><button type="submit" class="btn btn-primary">Ulangi dari tahap ini</button></form>`)
	wantNoBody(t, rec, "data-confirm-action=\"job_", "/retry/prepare")
	rec = e.do("POST", "/jobs/"+id+"/retry", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "retry "+id)
	if ack := f.lastAck(t); ack.Confirmed {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestApproveLoadRefusalLinksJob (review C1, K13): an approval of a
// waiting load the orchestrator refuses with T15b shows that text on the
// job page with the promote job it names as a link.
func TestApproveLoadRefusalLinksJob(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id, promote = "2222222222222222222222222222222b", "44444444444444444444444444444444"
	waitingJobFor(t, e.st, id, orchestrator.KindResyncReplica, orchestrator.WaitWipe)
	f.err = &orchestrator.LockStandsError{Code: "load", Master: "db-master", What: "Resync ulang", JobID: promote}
	rec := e.do("POST", "/jobs/"+id+"/approve", withToken(t, e, url.Values{}, "job_approve:"+id, "job", 1), c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "Master db-master mungkin masih terkunci oleh promote terencana sebelumnya. Resync ulang tidak "+
		"dijalankan selama itu: memuat data membuat replika bisa ditulis sebentar, dan penjaga kunci master bisa "+
		"mengkarantina master. Tekan &#34;Buka kunci promote&#34; pada pekerjaan promote "+
		`<a class="link mono" href="/jobs/`+promote+`">`+promote+`</a> dulu.`)
	wantCalls(t, f, "approve "+id)
}

// plannedRetryJob is a planned promote of master 1 to its replica 2 that
// failed in "Replika mengejar" before mysql.promote was sent, having
// accepted that db-mati (4) is repointed later; the orchestrator says it
// needs a confirmation.
const plannedRetryJob = "5555555555555555555555555555555a"

func failedPlannedPromote(t *testing.T, e *env, f *fakeOrch) {
	t.Helper()
	storeJob(t, e.st, plannedRetryJob, orchestrator.KindPromotePlanned, 1,
		`{"master_server_id":1,"candidate_server_id":2,"promotion_id":1,"accept_unreachable":[4],"lock_tried":true,"lock_unlocked":true}`,
		store.JobFailed, "Buat akun replikasi di kandidat", "Kunci master", "Replika mengejar")
	f.mu.Lock()
	f.confirms = map[string]bool{plannedRetryJob: true}
	f.mu.Unlock()
}

// TestGateRefusesPlannedRetryBeforeTokenIsConsumed (spec 5.1, 5.2 row 1):
// "Ulangi" of a planned promote not sent posted with a valid token while
// a row is red is refused with 409 and G14 before the token is used; a
// backup row that asks for the tick refuses without it (G15); once green
// and ticked the same token is accepted and the orchestrator gets a
// confirmed Retry with the tick. Without a token it is refused (403).
func TestGateRefusesPlannedRetryBeforeTokenIsConsumed(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlannedPromote(t, e, f)
	path := "/jobs/" + plannedRetryJob + "/retry"

	rec := e.do("POST", path, url.Values{}, c)
	wantCode(t, rec, http.StatusForbidden)
	wantCalls(t, f)

	f.setGate(redDisk)
	form := withToken(t, e, url.Values{}, "job_retry:"+plannedRetryJob, "job", 1)
	rec = e.do("POST", path, form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk)
	wantCalls(t, f)
	if got := f.gateInputs(); len(got) == 0 || got[0] != (orchestrator.GateInput{Action: orchestrator.GatePromotePlanned,
		MasterID: 1, TargetID: 2}) {
		t.Fatalf("gate inputs = %+v", got)
	}
	if s := jobStatus(t, e.st, plannedRetryJob); s != store.JobFailed {
		t.Fatalf("job status after a refused retry = %s", s)
	}

	f.setGate(backupUnset)
	rec = e.do("POST", path, form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantG15)
	wantCalls(t, f)

	form.Set("backup_ack", "1")
	rec = e.do("POST", path, form, c) // the same token
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "retry "+plannedRetryJob)
	if ack := f.lastAck(t); !ack.Confirmed || !ack.BackupAck {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestRetryPlannedPromoteAudit (spec 5.2): Ulangi of a planned promote not
// sent is audited with the fields of its start; Ulangi of a promote past
// the point of no return as a continuation.
func TestRetryPlannedPromoteAudit(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlannedPromote(t, e, f)
	form := withToken(t, e, url.Values{"backup_ack": {"1"}}, "job_retry:"+plannedRetryJob, "job", 1)
	wantCode(t, e.do("POST", "/jobs/"+plannedRetryJob+"/retry", form, c), http.StatusSeeOther)
	entries := withoutConfirms(auditEntries(t, e.st))
	last := entries[len(entries)-1]
	var detail map[string]any
	if err := json.Unmarshal([]byte(last.DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"job_id": plannedRetryJob, "kind": orchestrator.KindPromotePlanned, "candidate": float64(2),
		"mode": proto.PromotePlanned, "accept_partial": false, "accept_unreachable": []any{float64(4)},
		"accept_branched": false, "backup_ack": true}
	if last.Action != "job_retry" || last.TargetID != 1 || fmt.Sprint(detail) != fmt.Sprint(want) {
		t.Fatalf("audit = %+v\ndetail %v\nwant   %v", last, detail, want)
	}
	wantNoSecretInDetail(t, last)

	const past = "5555555555555555555555555555555b"
	storeJob(t, e.st, past, orchestrator.KindPromotePlanned, 1, `{"candidate_server_id":2,"promote_sent":true,"past_point":true}`,
		store.JobPartial, "Karantina master lama")
	wantCode(t, e.do("POST", "/jobs/"+past+"/retry", url.Values{}, c), http.StatusSeeOther)
	entries = withoutConfirms(auditEntries(t, e.st))
	last = entries[len(entries)-1]
	detail = nil
	if err := json.Unmarshal([]byte(last.DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	if last.Action != "job_retry" || detail["continuation"] != true || detail["job_id"] != past || detail["candidate"] != nil {
		t.Fatalf("audit of the continuation = %+v", last)
	}
}

// TestJobPagePlannedRetryIsConfirmButton: the job page of a planned
// promote not sent lists the gate's red rows, the backup tick when asked,
// and "Ulangi dari tahap ini" as a confirmed button (job_retry), disabled
// while a row is red.
func TestJobPagePlannedRetryIsConfirmButton(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlannedPromote(t, e, f)
	button := `data-confirm-action="job_retry:` + plannedRetryJob + `"`
	rec := e.do("GET", "/jobs/"+plannedRetryJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/jobs/`+plannedRetryJob+`/retry"`, "data-promote-retry", button,
		`data-confirm-kind="job" data-confirm-id="1" data-confirm-danger="1"`,
		`data-confirm-text="Promote terencana db-replika menjadi master baru menggantikan db-master."`, ">Ulangi dari tahap ini<")
	wantNoBody(t, rec, "/retry/prepare", `name="backup_ack"`, "data-gate-list")
	wantOn(t, rec.Body.String(), button)

	f.setGate(redDisk)
	rec = e.do("GET", "/jobs/"+plannedRetryJob, nil, c)
	body := rec.Body.String()
	wantOff(t, body, button)
	wantBody(t, rec, wantD2, "Belum terpenuhi", "Ruang disk", "Kosongkan ruang")

	f.setGate(backupUnset)
	rec = e.do("GET", "/jobs/"+plannedRetryJob, nil, c)
	wantBody(t, rec, `name="backup_ack" value="1" required`)
	form := between(rec.Body.String(), "data-promote-retry", "</form>")
	if !strings.Contains(form, `name="backup_ack"`) || !strings.Contains(form, button) {
		t.Fatalf("the tick is not in the retry's form:\n%s", form)
	}
}

// TestMasterPageShowsT14ForSentJob (spec 5.2): the Terkunci master's
// planned promote job whose lock record stands sent mysql.promote: no
// "Buka kunci promote", but T14 with its tail T14t and a link to the job;
// "Buka kunci master" stays.
func TestMasterPageShowsT14ForSentJob(t *testing.T) {
	e, f, mon := controlEnv(t)
	c := e.cookie()
	const sentJob = "5555555555555555555555555555555c"
	storeJob(t, e.st, sentJob, orchestrator.KindPromotePlanned, 1,
		`{"master_server_id":1,"candidate_server_id":2,"lock_tried":true,"promote_sent":true}`, store.JobPartial,
		"Kunci master", "Jadikan master baru")
	f.unlockJob, f.unlockSent = sentJob, true
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked}})
	want := (&orchestrator.PromoteSentError{Code: orchestrator.PromoteSentUnreadable, Candidate: "db-replika",
		Tail: orchestrator.PromoteSentTailPlannedLocked, Master: "db-master"}).Error()
	const t14 = `Kandidat db-replika belum bisa dibaca, jadi panel belum tahu apakah ia sudah menerima tulisan. Tidak ada ` +
		`yang diubah. Tekan "Ulangi dari tahap ini" lagi sesudah agent kandidat tersambung. Master db-master tetap ` +
		`baca-saja selama itu. Bila kandidat tidak akan kembali, buka master di servernya: sudo dbpanel agent ` +
		`unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat bukan master dan tidak akan menjadi ` +
		`master: MySQL-nya terbaca baca-saja di server kandidat itu sendiri, atau host-nya mati.`
	if want != t14 {
		t.Fatalf("T14 with T14t = %q", want)
	}
	rec := e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, template.HTMLEscapeString(t14), `href="/jobs/`+sentJob+`"`, "data-promote-sent",
		`action="/masters/1/unlock"`, "Buka kunci master")
	wantNoBody(t, rec, `action="/jobs/`+sentJob+`/unlock-lock"`, ">Buka kunci promote<")
}

// TestRetrySentRefusalShownOnJobPage: "Ulangi" of a sent promote refused
// with T14b: the job page shows the text, with the job as it was.
func TestRetrySentRefusalShownOnJobPage(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const sentJob = "5555555555555555555555555555555d"
	storeJob(t, e.st, sentJob, orchestrator.KindPromoteEmergency, 1,
		`{"master_server_id":1,"candidate_server_id":2,"promote_sent":true}`, store.JobPartial, "Jadikan master baru")
	over := time.Date(2026, 11, 28, 22, 47, 20, 0, time.Local)
	f.err = &orchestrator.PromoteSentError{Code: orchestrator.PromoteSentRunning, Candidate: "db-replika", Over: over}
	rec := e.do("POST", "/jobs/"+sentJob+"/retry", noForm, c)
	wantCode(t, rec, http.StatusConflict)
	const t14b = `Pekerjaan promote di kandidat db-replika masih berjalan, atau belum terbukti berakhir. Tidak ada yang ` +
		`diubah. Tunggu sebentar, lalu tekan "Ulangi dari tahap ini" lagi. Bila agent kandidat tidak melaporkannya, ` +
		`panel menganggapnya berakhir pukul 28-11-2026 22:47:20.`
	wantBody(t, rec, template.HTMLEscapeString(t14b))
	wantCalls(t, f, "retry "+sentJob)
}

// restartJob is a prepare_master of the master (server 1) waiting for the
// approval of its MySQL restart.
const restartJob = "4444444444444444444444444444444a"

// restartButton is the light confirming button of the restart approval of
// restartJob.
const restartButton = `data-confirm-action="job_approve:` + restartJob + `" data-confirm-kind="job" data-confirm-id="1" data-confirm-light="1"`

// TestApproveMasterRestartNeedsConfirm (spec 5.1): "Restart MySQL di master
// sekarang" is a light action: without a token it is refused with 403 and
// the orchestrator is not called; a window token for job_approve:<id> lets
// it through, and the orchestrator is not told the approval was confirmed
// with the password. "Sudah saya restart — periksa lagi" stays without one.
func TestApproveMasterRestartNeedsConfirm(t *testing.T) {
	e, f, _ := windowEnv(t)
	c := e.cookie()
	waitingJobFor(t, e.st, restartJob, orchestrator.KindPrepareMaster, orchestrator.WaitRestart)
	approve := "/jobs/" + restartJob + "/approve"

	rec := e.do("GET", "/jobs/"+restartJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, restartButton, "Restart MySQL di master sekarang")

	for _, tok := range []string{"", "salah"} {
		form := url.Values{}
		if tok != "" {
			form.Set("confirm_token", tok)
		}
		rec = e.do("POST", approve, form, c)
		wantCode(t, rec, http.StatusForbidden)
		wantBody(t, rec, msgConfirmInvalid)
	}
	// A token for another job's approval does not do either.
	waitingJobFor(t, e.st, restartJob2, orchestrator.KindPrepareMaster, orchestrator.WaitRestart)
	wantCode(t, e.do("POST", approve, url.Values{"confirm_token": {windowToken(t, e, "job_approve:"+restartJob2, "job", 1, c)}}, c),
		http.StatusForbidden)
	wantCalls(t, f)
	if st := jobStatus(t, e.st, restartJob); st != store.JobWaiting {
		t.Fatalf("job after a refused approval = %s", st)
	}

	// The recheck (Ulangi of the waiting job) restarts nothing: no token.
	wantCode(t, e.do("POST", "/jobs/"+restartJob+"/retry", noForm, c), http.StatusSeeOther)
	wantCalls(t, f, "retry "+restartJob)

	rec = e.do("POST", approve, url.Values{"confirm_token": {windowToken(t, e, "job_approve:"+restartJob, "job", 1, c)}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+restartJob)
	wantCalls(t, f, "retry "+restartJob, "approve "+restartJob)
	if ack := f.lastAck(t); ack.Confirmed {
		t.Fatalf("ack of a restart approval = %+v; a light token must not confirm a heavy approval", ack)
	}
	// A password token does too, still without Confirmed.
	rec = e.do("POST", approve, withToken(t, e, noForm, "job_approve:"+restartJob, "job", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	if ack := f.lastAck(t); ack.Confirmed {
		t.Fatalf("ack of a restart approval with the password = %+v", ack)
	}
}

const restartJob2 = "4444444444444444444444444444444b"

// TestApproveHeavyJobRefusesWindowToken (spec 5.1): whether an approval is
// heavy is decided from the job when it is approved. A job that needs a
// heavy confirmation (here a prepare_master the orchestrator now calls
// heavy, and a waiting load that drops data) approved with a window token
// is refused with 403, the orchestrator is not called, the job stays
// waiting and the token unused; the same approval with the password
// reaches the orchestrator confirmed.
func TestApproveHeavyJobRefusesWindowToken(t *testing.T) {
	e, f, _ := windowEnv(t)
	c := e.cookie()
	waitingJobFor(t, e.st, restartJob, orchestrator.KindPrepareMaster, orchestrator.WaitRestart)
	const dropping = "4444444444444444444444444444444c"
	waitingJobFor(t, e.st, dropping, orchestrator.KindCreateReplica, orchestrator.WaitClone)
	f.mu.Lock()
	f.confirms = map[string]bool{restartJob: true}
	f.drops = map[string]bool{dropping: true}
	f.mu.Unlock()
	for _, id := range []string{restartJob, dropping} {
		tok := windowToken(t, e, "job_approve:"+id, "job", 1, c)
		rec := e.do("POST", "/jobs/"+id+"/approve", url.Values{"confirm_token": {tok}}, c)
		wantCode(t, rec, http.StatusForbidden)
		wantBody(t, rec, msgConfirmInvalid)
		wantCalls(t, f)
		if st := jobStatus(t, e.st, id); st != store.JobWaiting {
			t.Fatalf("job %s after a window token = %s", id, st)
		}
	}
	if _, unused := confirmRows(t, e); unused != 2 {
		t.Fatalf("unused window tokens = %d, want 2 (not burnt)", unused)
	}
	rec := e.do("POST", "/jobs/"+restartJob+"/approve", withToken(t, e, noForm, "job_approve:"+restartJob, "job", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "approve "+restartJob)
	if ack := f.lastAck(t); !ack.Confirmed {
		t.Fatalf("ack of a heavy approval with the password = %+v", ack)
	}
}
