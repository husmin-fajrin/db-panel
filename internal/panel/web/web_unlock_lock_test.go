package web_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// "Buka kunci promote" (spec 5.2, K11): its own POST, without a token and
// without the gate, from the job page (under T9) and the master page.

const (
	actUnlockLock = "promote_unlock_lock"
	plannedJob    = "5555555555555555555555555555555a"
	newerJob      = "5555555555555555555555555555555b"
)

// failedPlanned stores a failed planned promote job of the master (server
// 1) to the replica (server 2).
func failedPlanned(t *testing.T, e *env, id string) {
	t.Helper()
	storeJob(t, e.st, id, orchestrator.KindPromotePlanned, 1,
		`{"master_server_id":1,"candidate_server_id":2,"lock_tried":true}`, store.JobFailed, "Kunci master", "Replika mengejar")
}

func TestUnlockLockNeedsNoPasswordAndNoGate(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlanned(t, e, plannedJob)
	f.gate = allRed
	rec := e.do("POST", "/jobs/"+plannedJob+"/unlock-lock", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+plannedJob+"?pesan=kunci-promote-dibuka")
	wantCalls(t, f, "unlock-lock "+plannedJob)
	wantBody(t, e.do("GET", "/jobs/"+plannedJob+"?pesan=kunci-promote-dibuka", nil, c),
		"Kunci promote di master db-master dibuka; aplikasi bisa menulis lagi.")
	var entries []store.AuditEntry
	for _, a := range withoutConfirms(auditEntries(t, e.st)) {
		if a.Action == actUnlockLock {
			entries = append(entries, a)
		}
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries = %+v, want one %s", entries, actUnlockLock)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(entries[0].DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	if a := entries[0]; a.TargetKind != "job" || a.TargetID != 1 || a.Outcome != "ok" ||
		detail["job_id"] != plannedJob || detail["result"] != "opened" {
		t.Fatalf("audit = %+v", a)
	}

	// Not locked: T10b.
	f.unlockOut = orchestrator.UnlockNotLocked
	rec = e.do("POST", "/jobs/"+plannedJob+"/unlock-lock", noForm, c)
	wantLocation(t, rec, "/jobs/"+plannedJob+"?pesan=kunci-promote-tidak-ada")
	wantBody(t, e.do("GET", "/jobs/"+plannedJob+"?pesan=kunci-promote-tidak-ada", nil, c),
		"Master db-master tidak sedang dikunci untuk promote; tidak ada yang dibuka. Bila master masih berstatus "+
			"&#34;Terkunci&#34;, sebabnya lain: tekan &#34;Buka kunci master&#34;.")
}

func TestJobPageShowsUnlockLock(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlanned(t, e, plannedJob)
	form := `action="/jobs/` + plannedJob + `/unlock-lock"`
	const t9 = "Master db-master mungkin masih terkunci oleh promote ini. Tombol di bawah meminta agent master membuka " +
		"kuncinya; agent memeriksa dulu bahwa kandidat masih replika."
	// Sent, superseded or without a standing record: not offered.
	wantNoBody(t, e.do("GET", "/jobs/"+plannedJob, nil, c), form, t9)
	f.unlockOffered = map[string]bool{plannedJob: true}
	wantBody(t, e.do("GET", "/jobs/"+plannedJob, nil, c), t9, form, ">Buka kunci promote<")
}

func TestMasterPageShowsBothUnlockButtons(t *testing.T) {
	e, f, mon := controlEnv(t)
	c := e.cookie()
	failedPlanned(t, e, plannedJob)
	f.unlockJob = plannedJob
	form := `action="/jobs/` + plannedJob + `/unlock-lock"`
	// Not "Terkunci": neither button.
	wantNoBody(t, e.do("GET", "/masters/1", nil, c), form, `action="/masters/1/unlock"`)
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked}})
	wantBody(t, e.do("GET", "/masters/1", nil, c), `action="/masters/1/unlock"`, "Buka kunci master", form, ">Buka kunci promote<")
	// The job sent mysql.promote: only Buka kunci master.
	f.unlockSent = true
	rec := e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, `action="/masters/1/unlock"`)
	wantNoBody(t, rec, form)
}

func TestUnlockLockRefusalLinksNewerJob(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedPlanned(t, e, plannedJob)
	f.unlockErr = &orchestrator.UnlockLockError{Code: orchestrator.UnlockNewerOpen, Master: "db-master", NewerJobID: newerJob}
	rec := e.do("POST", "/jobs/"+plannedJob+"/unlock-lock", noForm, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "Kunci promote ini tidak dibuka dari panel selagi promote yang lebih baru di topologi ini belum tuntas. "+
		"Selesaikan dulu pekerjaan promote ", `href="/jobs/`+newerJob+`"`)
	var result string
	for _, a := range auditEntries(t, e.st) {
		if a.Action == actUnlockLock {
			var d map[string]any
			json.Unmarshal([]byte(a.DetailJSON), &d)
			result, _ = d["result"].(string)
		}
	}
	if result != orchestrator.UnlockNewerOpen {
		t.Fatalf("audit result = %q, want newer_open", result)
	}
}
