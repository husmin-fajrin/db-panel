package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

const msgConfirmInvalid = "Konfirmasi tidak berlaku; ulangi konfirmasi."

// confirmForm is the form the confirmation modal posts to /confirm.
func confirmForm(action, kind string, id int64, password string) url.Values {
	return url.Values{"action": {action}, "target_kind": {kind}, "target_id": {strconv.FormatInt(id, 10)},
		"password": {password}}
}

// postConfirm posts the confirmation form as the session of c.
func (e *env) postConfirm(action, kind string, id int64, password string, c *http.Cookie) (int, string) {
	rec := e.do("POST", "/confirm", confirmForm(action, kind, id, password), c)
	var body struct {
		Token string `json:"token"`
	}
	if rec.Code == http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body.Token
}

// confirmToken obtains a confirmation token for admin through POST
// /confirm, as the modal does before a destructive post.
func confirmToken(t *testing.T, e *env, action, kind string, id int64) string {
	t.Helper()
	code, tok := e.postConfirm(action, kind, id, testPassword, e.cookie())
	if code != http.StatusOK || tok == "" {
		t.Fatalf("POST /confirm %s %s %d: status %d, token %q", action, kind, id, code, tok)
	}
	return tok
}

// withToken is form plus a confirm_token for action on target.
func withToken(t *testing.T, e *env, form url.Values, action, kind string, id int64) url.Values {
	t.Helper()
	out := url.Values{}
	for k, v := range form {
		out[k] = append([]string(nil), v...)
	}
	out.Set("confirm_token", confirmToken(t, e, action, kind, id))
	return out
}

func TestConfirmIssuesTokenAndHandlerConsumesIt(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	rec := e.do("POST", "/confirm", confirmForm(web.ActResync, "replica", 2, testPassword), c)
	wantCode(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// 32 random bytes, base64url without padding.
	if len(body.Token) != 43 || strings.ContainsAny(body.Token, "+/=") {
		t.Fatalf("token = %q", body.Token)
	}

	form := url.Values{"confirm_name": {"db-replika"}, "confirm_token": {body.Token}}
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	wantCalls(t, f, "resync 2  force=false")

	// confirm_issued names the action and target, never the password or
	// token.
	entries, err := e.st.ListAudit(0, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var issued *store.AuditEntry
	for i := range entries {
		if entries[i].Action == "confirm_issued" {
			issued = &entries[i]
		}
	}
	if issued == nil {
		t.Fatalf("no confirm_issued entry: %+v", entries)
	}
	if issued.Outcome != "ok" || issued.Username != "admin" || issued.TargetKind != "replica" || issued.TargetID != 2 {
		t.Errorf("confirm_issued = %+v", *issued)
	}
	if want := `{"action":"resync","target_id":2,"target_kind":"replica"}`; issued.DetailJSON != want {
		t.Errorf("detail = %s, want %s", issued.DetailJSON, want)
	}
	for _, en := range entries {
		if strings.Contains(en.DetailJSON+en.Outcome, testPassword) || strings.Contains(en.DetailJSON+en.Outcome, body.Token) {
			t.Errorf("audit entry leaks the password or token: %+v", en)
		}
	}
}

func TestConfirmWrongPasswordAudited(t *testing.T) {
	e, f, _ := controlEnv(t)
	rec := e.do("POST", "/confirm", confirmForm(web.ActDetach, "replica", 2, "salah"), e.cookie())
	wantCode(t, rec, http.StatusUnauthorized)
	wantNoBody(t, rec, `"token"`)
	entries, err := e.st.ListAudit(0, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit = %+v, want one confirm_failed", entries)
	}
	en := entries[0]
	if en.Action != "confirm_failed" || en.Username != "admin" || en.TargetKind != "replica" || en.TargetID != 2 ||
		en.Outcome == "ok" || en.Outcome == "rate_limited" {
		t.Errorf("confirm_failed = %+v", en)
	}
	if want := `{"action":"detach","target_id":2,"target_kind":"replica"}`; en.DetailJSON != want {
		t.Errorf("detail = %s, want %s", en.DetailJSON, want)
	}
	wantCalls(t, f)
}

func TestConfirmRateLimited(t *testing.T) {
	// The router's own limiter: 5 per minute per user.
	e := newEnv(t, func(d *web.Deps) { d.ConfirmLimiter = nil })
	c := e.cookie()
	for i := 1; i <= 5; i++ {
		code, _ := e.postConfirm(web.ActDeleteServer, "server", 1, "salah", c)
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, code)
		}
	}
	rec := e.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, testPassword), c)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantNoBody(t, rec, `"token"`)
	entries, err := e.st.ListAudit(0, time.Time{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "confirm_failed" || entries[0].Outcome != "rate_limited" {
		t.Errorf("last audit = %+v, want confirm_failed rate_limited", entries)
	}
}

func TestConfirmUnknownAction400(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	for _, form := range []url.Values{
		confirmForm("drop_everything", "server", 1, testPassword),
		confirmForm("", "server", 1, testPassword),
		confirmForm(web.ActResync, "", 1, testPassword),
		{"action": {web.ActResync}, "target_kind": {"replica"}, "target_id": {"x"}, "password": {testPassword}},
	} {
		rec := e.do("POST", "/confirm", form, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantNoBody(t, rec, `"token"`)
	}
	// Without a session: the login page.
	rec := e.do("POST", "/confirm", confirmForm(web.ActResync, "replica", 2, testPassword), nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("without login: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// fakeClock is a settable Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestHandlerRefusesMissingOrForeignToken(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	e, f, _ := controlEnv(t, func(d *web.Deps) { d.Now = clock.Now })
	c := e.cookie()
	if err := e.st.CreateUser("operator", adminHash()); err != nil {
		t.Fatal(err)
	}
	other := &http.Cookie{Name: auth.SecureCookieName, Value: e.sessions.Create("operator")}
	_, otherTok := e.postConfirm(web.ActResync, "replica", 2, testPassword, other)
	if otherTok == "" {
		t.Fatal("no token for operator")
	}
	used := confirmToken(t, e, web.ActResync, "replica", 2)
	resync := func(tok string) url.Values {
		return url.Values{"confirm_name": {"db-replika"}, "confirm_token": {tok}}
	}
	wantCode(t, e.do("POST", "/replicas/2/resync", resync(used), c), http.StatusSeeOther)
	wantCalls(t, f, "resync 2  force=false")

	expired := confirmToken(t, e, web.ActResync, "replica", 2)
	clock.Add(2*time.Minute + time.Second)
	cases := map[string]url.Values{
		"no token":     {"confirm_name": {"db-replika"}},
		"garbage":      resync("bukan-token"),
		"other action": resync(confirmToken(t, e, web.ActDetach, "replica", 2)),
		"other target": resync(confirmToken(t, e, web.ActResync, "replica", 1)),
		"other kind":   resync(confirmToken(t, e, web.ActResync, "server", 2)),
		"other user":   resync(otherTok),
		"used twice":   resync(used),
		"expired":      resync(expired),
	}
	for name, form := range cases {
		rec := e.do("POST", "/replicas/2/resync", form, c)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, rec.Code)
		}
		wantBody(t, rec, msgConfirmInvalid, "db-replika")
	}
	wantCalls(t, f, "resync 2  force=false")

	// Each destructive post refuses without a token, on its own page.
	for _, p := range []struct{ path, page string }{
		{"/replicas/2/detach", "Replika db-replika"},
		{"/servers/2/delete", "db-replika"},
		{"/masters/1/unlock", "Master db-master"},
		{"/servers/1/prepare-master", "db-master"},
	} {
		form := url.Values{"confirm_name": {"db-replika"}, "mode": {"keep"}}
		if strings.Contains(p.path, "/1/") {
			form.Set("confirm_name", "db-master")
		}
		rec := e.do("POST", p.path, form, c)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s: status %d, want 403", p.path, rec.Code)
		}
		wantBody(t, rec, msgConfirmInvalid, p.page)
	}
	wantCalls(t, f, "resync 2  force=false")
}

func TestUnlockMasterNeedsToken(t *testing.T) {
	e, f, mon := controlEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked}})
	rec := e.do("POST", "/masters/1/unlock", url.Values{"confirm_name": {"db-master"}}, c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid, "Master db-master")
	wantCalls(t, f)

	form := withToken(t, e, url.Values{"confirm_name": {"db-master"}}, web.ActUnlockMaster, "master", 1)
	rec = e.do("POST", "/masters/1/unlock", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1?pesan=kunci-dibuka")
	wantCalls(t, f, "unlock-master 1")

	// The modal asks for it: the button carries the confirm attributes.
	rec = e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, `data-confirm-action="unlock_master"`, `data-confirm-kind="master"`, `data-confirm-id="1"`,
		`data-confirm-name="db-master"`, `x-data="confirmModal"`)
}

func TestPrepareMasterAgainNeedsTokenOnlyWithTopology(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	// Server 3 is no master yet: no token needed.
	fresh := e.pairedOnline(t, "db-baru")
	rec := e.do("POST", "/servers/"+strconv.FormatInt(fresh.ID, 10)+"/prepare-master", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "prepare 3")

	// Server 1 already has a topology: preparing it again needs one.
	rec = e.do("POST", "/servers/1/prepare-master", noForm, c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid)
	wantCalls(t, f, "prepare 3")
	form := withToken(t, e, noForm, web.ActPrepareMasterAgain, "server", 1)
	rec = e.do("POST", "/servers/1/prepare-master", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "prepare 3", "prepare 1")
}

func TestRemoveDatabasesNeedsToken(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	form := url.Values{"databases": {"gudang"}, "drop_data": {"1"}}
	rec := e.do("POST", "/replicas/2/databases/remove", form, c)
	wantCode(t, rec, http.StatusForbidden)
	wantBody(t, rec, msgConfirmInvalid, "Lepas database")
	wantCalls(t, f)
	rec = e.do("POST", "/replicas/2/databases/remove", withToken(t, e, form, web.ActRemoveDatabases, "replica", 2), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "remove-databases 2 gudang drop=true")
}

func TestConfirmModalOnEveryPage(t *testing.T) {
	e, _, mon := controlEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked}})
	for _, p := range []struct {
		path  string
		attrs []string
	}{
		{"/replicas/2?buka=resync", []string{`data-confirm-action="resync"`, `data-confirm-action="detach"`,
			`data-confirm-kind="replica"`, `data-confirm-id="2"`, `data-confirm-name="db-replika"`}},
		{"/servers/2", []string{`data-confirm-action="delete_server"`, `data-confirm-kind="server"`,
			`data-confirm-name="db-replika"`}},
		{"/replicas/2/databases", []string{`data-confirm-action="remove_databases"`, `data-confirm-kind="replica"`}},
	} {
		rec := e.do("GET", p.path, nil, c)
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, append(p.attrs, `x-data="confirmModal"`, `name="password"`)...)
	}
}
