package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

// TestSecurityHeadersOnEveryAnswerClass (WB-8): the answers
// TestSecurityHeaders does not cover carry the full header set, and
// no-store wherever logged-in content or a secret is served; a static
// file the server does not have too (Go's file server drops the
// Cache-Control on its error answers). The 301 of the plain-HTTP side is
// in the panel package (TestPlainHTTPRedirectHeaders).
func TestSecurityHeadersOnEveryAnswerClass(t *testing.T) {
	e, _, _ := promotedEnv(t)
	c := e.cookie()
	const jobID = "4444444444444444444444444444444c"
	storeJob(t, e.st, jobID, "create_replica", 1, `{}`, store.JobFailed, "Tahap")

	type answer struct {
		what  string
		code  int
		cache string
		rec   *httptest.ResponseRecorder
	}
	plain := func(method, path string, form url.Values, ck *http.Cookie, header map[string]string) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		if ck != nil {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	salvageTok := confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
	answers := []answer{
		{"303 to /login", http.StatusSeeOther, "no-store", e.do("GET", "/servers", nil, nil)},
		{"303 after a POST", http.StatusSeeOther, "no-store", e.do("POST", "/servers/1/snooze", url.Values{"hours": {"1"}}, c)},
		{"400", http.StatusBadRequest, "no-store", e.do("POST", "/servers", url.Values{"name": {""}}, c)},
		{"401 of /confirm", http.StatusUnauthorized, "no-store",
			e.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, "salah"), c)},
		{"200 of /confirm (the token)", http.StatusOK, "no-store",
			e.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, testPassword), c)},
		{"403 of sameOrigin", http.StatusForbidden, "no-store",
			plain("POST", "/servers/1/snooze", url.Values{"hours": {"1"}}, c, nil)},
		{"403 of a refused confirmation", http.StatusForbidden, "no-store",
			e.do("POST", "/servers/3/delete", url.Values{"confirm_name": {"db-lain"}}, c)},
		{"405", http.StatusMethodNotAllowed, "no-store", e.do("GET", "/logout", nil, c)},
		{"413", http.StatusRequestEntityTooLarge, "no-store",
			e.do("POST", "/servers", url.Values{"name": {strings.Repeat("a", 70<<10)}}, c)},
		{"/alerts/count", http.StatusOK, "no-store", e.do("GET", "/alerts/count", nil, c)},
		{"status fragment", http.StatusOK, "no-store", e.do("GET", "/servers/1/status-fragment", nil, c)},
		{"connections fragment", http.StatusOK, "no-store", e.do("GET", "/servers/1/connections-fragment", nil, c)},
		{"/jobs/{id}/log", http.StatusOK, "no-store", e.do("GET", "/jobs/"+jobID+"/log?after=0", nil, c)},
		{"salvage download", http.StatusOK, "no-store", e.do("GET", downloadURL(salvageName, salvageTok), nil, c)},
		{"static 404", http.StatusNotFound, "no-store", e.do("GET", "/static/nope.css", nil, nil)},
		{"static directory", http.StatusNotFound, "no-store", e.do("GET", "/static/", nil, nil)},
	}
	// The 429 of /confirm, with the router's own limit (5 per minute).
	limited := newEnv(t, func(d *web.Deps) { d.ConfirmLimiter = nil })
	lc := limited.cookie()
	for range 5 {
		limited.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, "salah"), lc)
	}
	answers = append(answers, answer{"429 of /confirm", http.StatusTooManyRequests, "no-store",
		limited.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, testPassword), lc)})
	// The 421 of a refused Host, wrapped as the panel wraps the router.
	deny := web.SecurityHeaders(web.RequireHost(func(string) bool { return false })(e.h))
	rec421 := httptest.NewRecorder()
	deny.ServeHTTP(rec421, httptest.NewRequest("GET", "/servers", nil))
	answers = append(answers, answer{"421", http.StatusMisdirectedRequest, "no-store", rec421})

	for _, a := range answers {
		if a.rec.Code != a.code {
			t.Errorf("%s: status %d, want %d", a.what, a.rec.Code, a.code)
			continue
		}
		wantHeaders(t, a.what, a.rec.Header(), a.cache)
	}
}

// TestCertDownloadHeaders: the certificate download (no login) carries
// the full set too.
func TestCertDownloadHeaders(t *testing.T) {
	rig, _ := certEnv(t)
	rec := rig.do("GET", "/sertifikat/dbpanel-panel.crt", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantHeaders(t, "certificate download", rec.Header(), "no-store")
}
