package web_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

// withoutConfirms drops the confirm_issued entries of the tokens a test
// obtained.
func withoutConfirms(entries []store.AuditEntry) []store.AuditEntry {
	return slices.DeleteFunc(entries, func(a store.AuditEntry) bool { return a.Action == "confirm_issued" })
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
	// The destructive posts carry a confirmation token; its confirm_issued
	// entries are left out of the count.
	needsToken := map[string]string{
		"/replicas/2/databases/remove": web.ActRemoveDatabases,
		"/replicas/2/resync":           web.ActResync,
		"/replicas/2/detach":           web.ActDetach,
		"/masters/1/unlock":            web.ActUnlockMaster,
		"/servers/2/delete":            web.ActDeleteServer,
	}
	for i, rt := range routes {
		form := rt.form
		if action, ok := needsToken[rt.path]; ok {
			form = withToken(t, e, form, action, rt.targetKind, rt.targetID)
		}
		rec := e.do("POST", rt.path, form, c)
		if rec.Code >= 500 {
			t.Fatalf("POST %s: status %d", rt.path, rec.Code)
		}
		got := withoutConfirms(auditEntries(t, e.st))
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

	got := withoutConfirms(auditEntries(t, e.st))
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
	form.Set("confirm_token", confirmToken(t, e, web.ActResync, "replica", 2))
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

// TestAuditPageShowsChainStatus (SEC-45): /audit shows whether the hash
// chain is intact, worked out at most once a minute; a row changed
// directly in SQLite shows as the first broken row once the minute is up.
func TestAuditPageShowsChainStatus(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	e := newEnv(t, func(d *web.Deps) { d.Now = clock.Now })
	c := e.cookie()
	for i := range 3 {
		if _, err := e.st.AddAudit(store.AuditEntry{TS: clock.Now(), Username: "admin", IP: "10.1.1.1",
			Action: "server_check", TargetKind: "server", TargetID: int64(i), DetailJSON: "{}", Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.do("GET", "/audit", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Rantai utuh sampai #3")

	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET ip = '10.9.9.9' WHERE id = 2`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// Within the minute the page shows the cached status.
	clock.Add(30 * time.Second)
	rec = e.do("GET", "/audit", nil, c)
	wantBody(t, rec, "Rantai utuh sampai #3")
	clock.Add(31 * time.Second)
	rec = e.do("GET", "/audit", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Rantai putus di #2")
	if strings.Contains(rec.Body.String(), "Rantai utuh") {
		t.Error("page still says the chain is intact")
	}
}

// TestAuditPageCachesFailedCheck: a chain walk that fails is shown and
// kept for the minute too, so page views do not re-walk the log each time.
func TestAuditPageCachesFailedCheck(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	e := newEnv(t, func(d *web.Deps) { d.Now = clock.Now })
	c := e.cookie()
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A row the walk cannot read (target_id not a number), dated outside
	// the page's window so the list itself still loads.
	if _, err := db.Exec(`INSERT INTO audit_log (ts, action, target_id) VALUES ('2000-01-01T00:00:00Z', 'x', 'bukan-angka')`); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/audit", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "tidak dapat diperiksa")

	for _, q := range []string{`DROP TRIGGER audit_log_no_delete`, `DELETE FROM audit_log`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	clock.Add(30 * time.Second)
	wantBody(t, e.do("GET", "/audit", nil, c), "tidak dapat diperiksa")
	clock.Add(31 * time.Second)
	wantBody(t, e.do("GET", "/audit", nil, c), "Rantai utuh sampai #0")
}

// TestAuditInsertLogged (SEC-45): every audit entry is also written to the
// panel's log (journald under systemd) with its fields, a second copy that
// outlives a tampered database; the copy carries no more than the entry,
// so no password typed into a form.
func TestAuditInsertLogged(t *testing.T) {
	logs := &syncBuffer{}
	e, _, _ := controlEnv(t, func(d *web.Deps) {
		d.Logger = slog.New(slog.NewJSONHandler(logs, nil))
		d.CheckPassword = fastCheck
	})
	c := e.cookie()
	wantCode(t, e.do("POST", "/login", loginForm("maling", testPassword, ""), nil), http.StatusOK)
	e.do("POST", "/servers/2/mysql/connect", url.Values{"user": {"admin"}, "password": {mysqlSecret}, "host": {"10.1.1.5"}}, c)
	e.do("POST", "/servers/2/snooze", url.Values{"hours": {"24"}}, c)

	entries := auditEntries(t, e.st)
	var lines []map[string]any
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		if m["msg"] == "audit" {
			lines = append(lines, m)
		}
	}
	if len(lines) != len(entries) || len(entries) < 3 {
		t.Fatalf("audit log lines = %d, entries = %d:\n%s", len(lines), len(entries), logs.String())
	}
	for i, en := range entries {
		l := lines[i]
		want := map[string]any{"id": float64(en.ID), "ts": en.TS.UTC().Format(time.RFC3339), "username": en.Username,
			"ip": en.IP, "action": en.Action, "target_kind": en.TargetKind, "target_id": float64(en.TargetID),
			"detail_json": en.DetailJSON, "outcome": en.Outcome}
		for k, v := range want {
			if l[k] != v {
				t.Errorf("entry #%d log %s = %v, want %v", en.ID, k, l[k], v)
			}
		}
	}
	for _, l := range lines {
		if l["stored"] != true {
			t.Errorf("audit log line %v: stored = %v, want true", l, l["stored"])
		}
	}
	// No log line at all, audit or not, carries what was typed: an unknown
	// username may be a password typed in the wrong field.
	if out := logs.String(); strings.Contains(out, mysqlSecret) || strings.Contains(out, "maling") || strings.Contains(out, testPassword) {
		t.Errorf("log carries typed secrets:\n%s", out)
	}
}

// TestAuditLogCopyWhenNotStored: when the database refuses the entry, the
// journald copy is still written and says it was not stored.
func TestAuditLogCopyWhenNotStored(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnv(t, func(d *web.Deps) { d.Logger = slog.New(slog.NewJSONHandler(logs, nil)) })
	c := e.cookie()
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER test_refuse BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk penuh'); END`); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.do("POST", "/logout", noForm, c), http.StatusSeeOther)
	var found bool
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == "audit" {
			found = true
			if m["stored"] != false || m["id"] != float64(0) || m["action"] != "logout" {
				t.Errorf("audit copy of a refused insert = %v, want stored=false id=0 action=logout", m)
			}
		}
	}
	if !found {
		t.Fatalf("no audit log line:\n%s", logs.String())
	}
}

// TestAuditOutcomeNewlinesFlattened: a multi-line error is stored on one
// line, so no field of the hash input holds the "\n" separator.
func TestAuditOutcomeNewlinesFlattened(t *testing.T) {
	e, f, _ := controlEnv(t)
	f.err = errors.New("baris satu\nbaris dua\r\nbaris tiga")
	e.do("POST", "/replicas/2/stop", noForm, e.cookie())
	got := auditEntries(t, e.st)
	if len(got) != 1 {
		t.Fatalf("audit entries = %+v", got)
	}
	if o := got[0].Outcome; strings.ContainsAny(o, "\r\n") || !strings.Contains(o, "baris satu baris dua") {
		t.Errorf("outcome = %q, want one line", o)
	}
}
