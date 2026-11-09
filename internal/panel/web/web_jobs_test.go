package web_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
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
	if row := between(rec.Body.String(), `href="/jobs/`+waitingJob+`"`, "</tr>"); strings.Count(row, `<td class="mono text-xs whitespace-nowrap">`) != 2 {
		t.Errorf("Dibuat/Selesai not in Space Mono 12 px: %q", row)
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
