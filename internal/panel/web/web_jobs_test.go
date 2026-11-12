package web_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestPromoteRetryStillTokenless and TestTightenTLSRetryTokenless: jobs
// that drop nothing keep their plain buttons and need no token.
func TestPromoteRetryStillTokenless(t *testing.T) {
	tokenlessRetry(t, orchestrator.KindPromotePlanned)
}

func TestTightenTLSRetryTokenless(t *testing.T) {
	tokenlessRetry(t, orchestrator.KindTightenTLS)
}

func tokenlessRetry(t *testing.T, kind string) {
	t.Helper()
	e, f, _ := controlEnv(t)
	c := e.cookie()
	const id = "4444444444444444444444444444444a"
	storeJob(t, e.st, id, kind, 1, `{}`, store.JobFailed, "Tahap")
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
