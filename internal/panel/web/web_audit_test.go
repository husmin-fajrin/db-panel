package web_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

// testRemoteIP is the client address httptest.NewRequest uses.
const testRemoteIP = "192.0.2.1"

// auditEntries returns the audit log, oldest first.
func auditEntries(t *testing.T, st *store.Store) []store.AuditEntry {
	t.Helper()
	got, err := st.ListAudit(0, time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	return got
}

// wantNoSecretInDetail fails when an entry's detail is not a JSON object
// or names a password, token or secret, or carries the MySQL password.
func wantNoSecretInDetail(t *testing.T, e store.AuditEntry) {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(e.DetailJSON), &obj); err != nil {
		t.Errorf("%s: detail %q is not a JSON object: %v", e.Action, e.DetailJSON, err)
	}
	low := strings.ToLower(e.DetailJSON)
	for _, s := range []string{"password", "token", "secret", strings.ToLower(mysqlSecret)} {
		if strings.Contains(low, s) {
			t.Errorf("%s: detail %q contains %q", e.Action, e.DetailJSON, s)
		}
	}
}

func TestEveryPostWritesAudit(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-calon") // 3: a replica target
	// Connecting server 2 below stores these as its inventory (the resync's
	// wipe list is checked against it).
	f.result.Databases = shopDatabases
	const jobID = "fedcba9876543210fedcba9876543210"
	if err := e.st.CreateJob(store.Job{ID: jobID, Kind: orchestrator.KindResyncReplica, ServerID: 2, ParamsJSON: "{}",
		CreatedAt: time.Now()}, []string{"Tahap"}); err != nil {
		t.Fatal(err)
	}

	routes := []struct {
		path       string
		form       url.Values
		action     string
		targetKind string
		targetID   int64
	}{
		{"/servers", url.Values{"name": {"db-baru"}}, "server_create", "server", 4},
		{"/servers/2/token", noForm, "server_token", "server", 2},
		{"/servers/2/mysql/connect", url.Values{"user": {"admin"}, "password": {mysqlSecret}, "host": {"10.1.1.5"}},
			"mysql_connect", "server", 2},
		{"/servers/2/mysql/refresh", noForm, "mysql_refresh", "server", 2},
		{"/servers/2/prepare-master", noForm, "prepare_master", "server", 2},
		{"/masters/1/source", url.Values{"source_host": {"10.9.9.9"}, "source_port": {"3306"}}, "master_source", "master", 1},
		{"/masters/1/replicas", url.Values{"target": {"3"}, "databases": {"toko"}, "confirm_wipe": {"1"}},
			"create_replica", "replica", 3},
		// Refused (already followed): audited with the refusal as outcome.
		{"/replicas/2/databases/add", url.Values{"databases": {"gudang"}}, "add_databases", "replica", 2},
		{"/replicas/2/databases/remove", url.Values{"databases": {"gudang"}, "drop_data": {"1"}},
			"remove_databases", "replica", 2},
		{"/jobs/" + jobID + "/approve", noForm, "job_approve", "job", 2},
		{"/jobs/" + jobID + "/retry", noForm, "job_retry", "job", 2},
		{"/jobs/" + jobID + "/cancel", noForm, "job_cancel", "job", 2},
		{"/replicas/2/stop", noForm, "replica_stop", "replica", 2},
		{"/replicas/2/start", noForm, "replica_start", "replica", 2},
		{"/replicas/2/relock", noForm, "replica_relock", "replica", 2},
		{"/replicas/2/resync/prepare", noForm, "mysql_refresh", "replica", 2},
		{"/replicas/2/resync", url.Values{"confirm_name": {"db-replika"}, "wipe_list[]": {"toko"}}, "replica_resync", "replica", 2},
		// Refused (wrong name): audited too.
		{"/replicas/2/resync", url.Values{"confirm_name": {"salah"}}, "replica_resync", "replica", 2},
		{"/replicas/2/detach", url.Values{"confirm_name": {"db-replika"}, "mode": {"keep"}}, "replica_detach", "replica", 2},
		{"/masters/1/unlock", url.Values{"confirm_name": {"db-master"}}, "master_unlock", "master", 1},
		{"/servers/2/check", noForm, "server_check", "server", 2},
		{"/servers/2/snooze", url.Values{"hours": {"24"}}, "server_snooze", "server", 2},
		{"/servers/2/delete", url.Values{"confirm_name": {"db-replika"}}, "server_delete", "server", 2},
		{"/logout", noForm, "logout", "session", 0},
	}
	for i, rt := range routes {
		rec := e.do("POST", rt.path, rt.form, c)
		if rec.Code >= 500 {
			t.Fatalf("POST %s: status %d", rt.path, rec.Code)
		}
		got := auditEntries(t, e.st)
		if len(got) != i+1 {
			t.Fatalf("POST %s: %d audit entries, want %d", rt.path, len(got), i+1)
		}
		a := got[i]
		if a.Action != rt.action || a.TargetKind != rt.targetKind || a.TargetID != rt.targetID {
			t.Errorf("POST %s: audit %s %s/%d, want %s %s/%d", rt.path,
				a.Action, a.TargetKind, a.TargetID, rt.action, rt.targetKind, rt.targetID)
		}
		if a.Username != "admin" || a.IP != testRemoteIP || a.Outcome == "" || a.TS.IsZero() {
			t.Errorf("POST %s: audit %+v", rt.path, a)
		}
		wantNoSecretInDetail(t, a)
	}

	got := auditEntries(t, e.st)
	byPath := func(i int) store.AuditEntry { return got[i] }
	if a := byPath(2); a.Outcome != "ok" || !strings.Contains(a.DetailJSON, `"mysql_user":"admin"`) {
		t.Errorf("mysql_connect audit = %+v", a)
	}
	if a := byPath(7); a.Outcome != "Database gudang sudah diikuti replika ini" {
		t.Errorf("refused add_databases outcome = %q", a.Outcome)
	}
	if a := byPath(9); !strings.Contains(a.DetailJSON, jobID) {
		t.Errorf("job_approve detail %q lacks the job id", a.DetailJSON)
	}
	if a := byPath(15); a.Outcome != "ok" || a.DetailJSON != `{"reason":"resync"}` {
		t.Errorf("resync prepare audit = %+v", a)
	}
	if a := byPath(16); a.Outcome != "ok" || !strings.Contains(a.DetailJSON, f.nextID) ||
		!strings.Contains(a.DetailJSON, `"wipe_list":["toko"]`) {
		t.Errorf("replica_resync audit = %+v", a)
	}
	if a := byPath(17); a.Outcome != "Nama server tidak cocok" {
		t.Errorf("refused replica_resync outcome = %q", a.Outcome)
	}
	if a := byPath(21); !strings.Contains(a.DetailJSON, `"hours":24`) {
		t.Errorf("server_snooze detail = %q", a.DetailJSON)
	}
}

func TestAuditOutcomeTruncatedAndForwardedForIgnored(t *testing.T) {
	e, f, _ := controlEnv(t)
	f.err = errors.New(strings.Repeat("é", 600))
	req := httptest.NewRequest("POST", "/replicas/2/stop", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.AddCookie(e.cookie())
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusBadRequest)
	got := auditEntries(t, e.st)
	if len(got) != 1 {
		t.Fatalf("audit entries = %d", len(got))
	}
	if n := len([]rune(got[0].Outcome)); n != 500 {
		t.Errorf("outcome length = %d runes, want 500", n)
	}
	if got[0].IP != testRemoteIP {
		t.Errorf("IP = %q, want %q", got[0].IP, testRemoteIP)
	}
}

func TestAuditLoginFailed(t *testing.T) {
	e := newEnv(t, func(d *web.Deps) { d.CheckPassword = fastCheck })
	wantCode(t, e.do("POST", "/login", loginForm("admin", "salah", ""), nil), http.StatusOK)
	wantCode(t, e.do("POST", "/login", loginForm("maling", testPassword, ""), nil), http.StatusOK)
	rec := e.do("POST", "/login", loginForm("admin", testPassword, ""), nil)
	wantCode(t, rec, http.StatusSeeOther)
	var session *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		session = ck
	}
	wantCode(t, e.do("POST", "/logout", noForm, session), http.StatusSeeOther)

	got := auditEntries(t, e.st)
	want := []struct{ action, user, outcome string }{
		{"login_failed", "admin", "Nama pengguna atau kata sandi salah"},
		// No such account: what was typed (maybe a password) is not kept
		// (SEC-44), only a short hash of it.
		{"login_failed", "(tidak dikenal) 52488226", "Nama pengguna atau kata sandi salah"},
		{"login", "admin", "ok"},
		{"logout", "admin", "ok"},
	}
	if len(got) != len(want) {
		t.Fatalf("audit entries = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		a := got[i]
		if a.Action != w.action || a.Username != w.user || a.Outcome != w.outcome ||
			a.TargetKind != "session" || a.IP != testRemoteIP {
			t.Errorf("entry %d = %+v, want %+v", i, a, w)
		}
		wantNoSecretInDetail(t, a)
		if strings.Contains(a.DetailJSON, testPassword) || strings.Contains(a.DetailJSON, "salah") {
			t.Errorf("entry %d detail %q carries a password", i, a.DetailJSON)
		}
		if strings.Contains(a.DetailJSON+a.Username, "maling") {
			t.Errorf("entry %d keeps the unknown username: %+v", i, a)
		}
	}
}

// TestAuditRejectedInputIsBounded (SEC-44): a wipe list as large as a form
// may be (64 KiB) refused by validation leaves a detail of at most 2 KiB
// without the names.
func TestAuditRejectedInputIsBounded(t *testing.T) {
	e, _, _ := controlEnv(t)
	c := e.cookie()
	form := url.Values{"confirm_name": {"db-replika"}}
	for i := 0; ; i++ {
		name := fmt.Sprintf("sisa_%05d_%s", i, strings.Repeat("q", 40))
		if len(form.Encode())+len(name)+20 > 63<<10 {
			break
		}
		form.Add("wipe_list[]", name)
	}
	rec := e.do("POST", "/replicas/2/resync", form, c)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("resync with an unknown wipe list accepted")
	}
	var entry *store.AuditEntry
	for _, a := range auditEntries(t, e.st) {
		if a.Action == "replica_resync" {
			entry = &a
		}
	}
	if entry == nil {
		t.Fatal("no replica_resync entry")
	}
	if len(entry.DetailJSON) > 2048 || strings.Contains(entry.DetailJSON, "sisa_") {
		t.Fatalf("detail of a refused resync = %d bytes: %.300s", len(entry.DetailJSON), entry.DetailJSON)
	}
	if !strings.Contains(entry.DetailJSON, `"wipe_list_count":`) {
		t.Errorf("detail %s does not count the wipe list", entry.DetailJSON)
	}
}

func TestScrubDetail(t *testing.T) {
	type inner struct {
		Name          string
		MySQLPassword string
	}
	detail := map[string]any{
		"user":         "admin",
		"password":     "x",
		"API_Token":    "x",
		"clientSecret": "x",
		"nested":       map[string]any{"db": "toko", "repl_password": "x"},
		"list":         []any{map[string]any{"secret_key": "x", "ok": 1}},
		"struct":       inner{Name: "n", MySQLPassword: "x"},
		"big":          int64(1) << 60,
	}
	got := web.ScrubDetail(detail, false)
	want := `{"big":1152921504606846976,"list":[{"ok":1}],"nested":{"db":"toko"},"struct":{"Name":"n"},"user":"admin"}`
	if got != want {
		t.Errorf("ScrubDetail = %s\nwant          %s", got, want)
	}
	if got := web.ScrubDetail(nil, false); got != "{}" {
		t.Errorf("ScrubDetail(nil) = %q", got)
	}

	// SEC-44: strings and lists are cut, the whole is at most 2 KiB.
	long := strings.Repeat("é", 300)
	many := make([]string, 100)
	for i := range many {
		many[i] = fmt.Sprintf("db%d", i)
	}
	got = web.ScrubDetail(map[string]any{"name": long, "wipe_list": many}, false)
	var back struct {
		Name     string   `json:"name"`
		WipeList []string `json:"wipe_list"`
	}
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("cut detail %s: %v", got, err)
	}
	if back.Name != strings.Repeat("é", 128)+"…(dipotong)" || len(back.WipeList) != 65 ||
		back.WipeList[63] != "db63" || back.WipeList[64] != "…(36 lagi)" {
		t.Errorf("cut detail = %s", got)
	}
	huge := map[string]any{}
	for i := range 64 {
		huge[fmt.Sprintf("k%02d", i)] = strings.Repeat("x", 128)
	}
	if got := web.ScrubDetail(huge, false); len(got) > 2048 || !strings.Contains(got, `"dipotong":true`) {
		t.Errorf("detail over 2 KiB = %d bytes: %.200s", len(got), got)
	}
	// A refused or failed action keeps names and lengths only.
	got = web.ScrubDetail(map[string]any{"confirm_name": "rahasia", "wipe_list": []string{"a", "b", "c"},
		"force_dump": true, "password": "x"}, true)
	if want := `{"confirm_name_len":7,"force_dump":true,"wipe_list_count":3}`; got != want {
		t.Errorf("refused detail = %s, want %s", got, want)
	}
}

func TestAuditPageFilters(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	a := e.pairedOnline(t, "db-satu")
	b := e.pairedOnline(t, "db-dua")
	now := time.Now()
	long := `{"catatan":"` + strings.Repeat("x", 200) + `"}`
	for _, en := range []store.AuditEntry{
		{TS: now.Add(-24 * time.Hour), Username: "admin", IP: "10.1.1.1", Action: "server_snooze",
			TargetKind: "server", TargetID: a.ID, DetailJSON: long, Outcome: "ok"},
		{TS: now.Add(-48 * time.Hour), Username: "operator", IP: "10.1.1.2", Action: "replica_stop",
			TargetKind: "replica", TargetID: b.ID, DetailJSON: "{}", Outcome: "replika sedang dipakai pekerjaan lain"},
		{TS: now.Add(-50 * time.Hour), Username: "admin", IP: "10.1.1.3", Action: "settings_update",
			TargetKind: "settings", TargetID: 0, DetailJSON: `{"changed":["monitor_check_every"]}`, Outcome: "ok"},
		{TS: now.Add(-10 * 24 * time.Hour), Username: "admin", IP: "10.1.1.4", Action: "server_delete",
			TargetKind: "server", TargetID: a.ID, DetailJSON: "{}", Outcome: "ok"},
	} {
		if _, err := e.st.AddAudit(en); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(rec *httptest.ResponseRecorder) string { return between(rec.Body.String(), "<tbody>", "</tbody>") }

	// Default: the last 7 days, every server.
	rec := e.do("GET", "/audit", nil, c)
	wantCode(t, rec, http.StatusOK)
	got := rows(rec)
	for _, s := range []string{"Jangan ganggu", "Server db-satu", "10.1.1.1", "Berhasil",
		"Stop replika", "Replika db-dua", "operator", "replika sedang dipakai pekerjaan lain",
		"Ubah pengaturan", "monitor_check_every"} {
		if !strings.Contains(got, s) {
			t.Errorf("default audit page does not show %q:\n%s", s, got)
		}
	}
	if strings.Contains(got, "10.1.1.4") {
		t.Errorf("default audit page shows an entry older than 7 days:\n%s", got)
	}
	// The detail is cut to 120 characters.
	if strings.Contains(got, strings.Repeat("x", 120)) || !strings.Contains(got, strings.Repeat("x", 100)) {
		t.Errorf("long detail not cut to 120 characters:\n%s", got)
	}
	wantBody(t, rec, `name="sejak" value="`+now.AddDate(0, 0, -7).Format("2006-01-02")+`"`,
		`<option value="`+itoa(a.ID)+`">db-satu</option>`)

	// One server.
	rec = e.do("GET", "/audit?server="+itoa(b.ID), nil, c)
	wantCode(t, rec, http.StatusOK)
	got = rows(rec)
	if !strings.Contains(got, "10.1.1.2") || strings.Contains(got, "10.1.1.1") || strings.Contains(got, "10.1.1.3") {
		t.Errorf("server filter rows:\n%s", got)
	}
	wantBody(t, rec, `<option value="`+itoa(b.ID)+`" selected>db-dua</option>`)

	// From an earlier day.
	since := now.AddDate(0, 0, -12).Format("2006-01-02")
	rec = e.do("GET", "/audit?sejak="+since+"&server="+itoa(a.ID), nil, c)
	wantCode(t, rec, http.StatusOK)
	got = rows(rec)
	if !strings.Contains(got, "10.1.1.4") || !strings.Contains(got, "Hapus server") || strings.Contains(got, "10.1.1.2") {
		t.Errorf("since filter rows:\n%s", got)
	}
	wantBody(t, rec, `name="sejak" value="`+since+`"`)

	// Malformed filters.
	for _, q := range []string{"sejak=kemarin", "server=abc", "server=-1"} {
		rec := e.do("GET", "/audit?"+q, nil, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Filter tidak valid")
	}
}
