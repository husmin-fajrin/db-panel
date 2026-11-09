package web_test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// sidebar returns the main navigation block of a page.
func sidebar(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<nav aria-label="Menu utama"`)
	if i < 0 {
		t.Fatalf("page has no <nav aria-label=\"Menu utama\">:\n%s", body)
	}
	j := strings.Index(body[i:], "</nav>")
	if j < 0 {
		t.Fatalf("main navigation is not closed:\n%s", body[i:])
	}
	return body[i : i+j]
}

var menuLinkRe = regexp.MustCompile(`<a [^>]*data-menu="([a-z]+)"[^>]*>`)

// menuLinks maps each menu entry (data-menu) of the navigation to its <a> tag.
func menuLinks(nav string) map[string]string {
	out := map[string]string{}
	for _, m := range menuLinkRe.FindAllStringSubmatch(nav, -1) {
		out[m[1]] = m[0]
	}
	return out
}

func TestLayoutSidebarAndActiveMenu(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()

	rec := e.do("GET", "/", nil, c)
	wantCode(t, rec, http.StatusOK)
	nav := sidebar(t, rec.Body.String())
	if !strings.Contains(nav, ">dbpanel<") {
		t.Errorf("sidebar has no dbpanel logo:\n%s", nav)
	}
	links := menuLinks(nav)
	want := map[string]string{
		"dashboard": `href="/"`, "servers": `href="/servers"`, "replikasi": `href="/replikasi"`, "jobs": `href="/jobs"`,
		"alerts": `href="/alerts"`, "audit": `href="/audit"`, "settings": `href="/settings"`,
	}
	if len(links) != len(want) {
		t.Errorf("menu entries = %v, want %d", links, len(want))
	}
	for k, href := range want {
		if !strings.Contains(links[k], href) {
			t.Errorf("menu %s = %q, want %s", k, links[k], href)
		}
	}
	if !strings.Contains(nav, "data-alert-count") || !strings.Contains(nav, `x-data="alertCount"`) {
		t.Error("Peringatan has no live count badge")
	}

	active := func(path, entry string) {
		t.Helper()
		rec := e.do("GET", path, nil, c)
		wantCode(t, rec, http.StatusOK)
		links := menuLinks(sidebar(t, rec.Body.String()))
		for k, a := range links {
			on := strings.Contains(a, "menu-active")
			if on != (k == entry) {
				t.Errorf("%s: menu %s active = %v: %s", path, k, on, a)
			}
			if on != strings.Contains(a, `aria-current="page"`) {
				t.Errorf("%s: menu %s: menu-active and aria-current disagree: %s", path, k, a)
			}
		}
	}
	active("/", "dashboard")
	active("/servers", "servers")
	active("/jobs", "jobs")
	active("/alerts", "alerts")
	active("/audit", "audit")
	active("/replikasi", "replikasi")
	srv, err := e.st.CreateServer("db-a")
	if err != nil {
		t.Fatal(err)
	}
	active("/servers/"+itoa(srv.ID), "servers")

	// The status box: user, Keluar, theme switch, panel version.
	body := rec.Body.String()
	for _, s := range []string{`action="/logout"`, "Keluar (admin)", `x-data="themeToggle"`, "v-test"} {
		if !strings.Contains(body, s) {
			t.Errorf("layout has no %q", s)
		}
	}

	// The organisation name shows only when set.
	if strings.Contains(nav, "data-instansi") {
		t.Errorf("empty nama_instansi still shown:\n%s", nav)
	}
	if err := e.st.SetSetting(store.SettingNamaInstansi, "Dinas Kominfo <Kota>"); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/", nil, c)
	nav = sidebar(t, rec.Body.String())
	if !strings.Contains(nav, "data-instansi") || !strings.Contains(nav, "Dinas Kominfo &lt;Kota&gt;") {
		t.Errorf("nama_instansi not shown (escaped) in the sidebar:\n%s", nav)
	}
	// Phones see it in the opened menu: its box is not hidden below md.
	box := regexp.MustCompile(`<div class="[^"]*">\s*<a href="/" class="logo`).FindString(nav)
	if box == "" || regexp.MustCompile(`["\s]hidden["\s]`).MatchString(box) {
		t.Errorf("nama_instansi box is hidden on phones: %q", box)
	}
}

// TestLayoutAgentStatus: the status box counts the paired servers and the
// offline agents.
func TestLayoutAgentStatus(t *testing.T) {
	e := newEnv(t)
	a, _ := e.st.CreateServer("db-a")
	b, _ := e.st.CreateServer("db-b")
	e.st.CreateServer("db-c") // not paired: no agent
	e.st.MarkPaired(a.ID, "s1", "10.0.0.1")
	e.st.MarkPaired(b.ID, "s2", "10.0.0.2")
	e.hub.Attach(a.ID, fakeConn{})
	rec := e.do("GET", "/servers", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "1 agent offline")
	e.hub.Attach(b.ID, fakeConn{})
	rec = e.do("GET", "/servers", nil, e.cookie())
	wantBody(t, rec, "Semua agent tersambung · 2 server · panel <span class=\"mono whitespace-nowrap\">v-test</span>")
	wantNoBody(t, rec, "agent offline")
}

func TestLayoutMobileMenu(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	btn := regexp.MustCompile(`<button[^>]*aria-controls="menu-utama"[^>]*>`).FindString(body)
	if btn == "" || !strings.Contains(btn, "aria-expanded") {
		t.Errorf("no menu button with aria-controls=menu-utama and aria-expanded: %q", btn)
	}
	if !strings.Contains(body, `x-data="sideMenu"`) {
		t.Error("menu button is not driven by the sideMenu component")
	}
	aside := regexp.MustCompile(`<aside[^>]*>`).FindString(body)
	if !strings.Contains(aside, "hidden md:flex") {
		t.Errorf("sidebar is not hidden below md: %q", aside)
	}
	if !strings.Contains(body, `id="menu-utama"`) {
		t.Error("no element menu-utama for the button to control")
	}
}

func TestThemeScriptAndAttribute(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/static/theme.js", nil, nil)
	wantCode(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("theme.js Content-Type = %q", ct)
	}
	js := rec.Body.String()
	for _, s := range []string{"localStorage", "temaBawaan", "prefers-color-scheme", "dbpanel-gelap", "dbpanel-terang"} {
		if !strings.Contains(js, s) {
			t.Errorf("theme.js does not mention %q", s)
		}
	}

	for _, path := range []string{"/login", "/sertifikat", "/"} {
		var rec = e.do("GET", path, nil, e.cookie())
		if path == "/login" || path == "/sertifikat" {
			rec = e.do("GET", path, nil, nil)
		}
		wantCode(t, rec, http.StatusOK)
		body := rec.Body.String()
		html := regexp.MustCompile(`<html[^>]*>`).FindString(body)
		if strings.Contains(html, "data-theme") {
			t.Errorf("%s: <html> sets a static theme: %s", path, html)
		}
		if !strings.Contains(html, `data-tema-bawaan="sistem"`) {
			t.Errorf("%s: <html> = %s, want data-tema-bawaan=\"sistem\"", path, html)
		}
		head := body[:strings.Index(body, "</head>")]
		tag := regexp.MustCompile(`<script[^>]*src="/static/theme.js"[^>]*>`).FindString(head)
		if tag == "" || strings.Contains(tag, "defer") || strings.Contains(tag, "async") {
			t.Errorf("%s: theme.js is not loaded synchronously in <head>: %q", path, tag)
		}
		if strings.Index(head, "/static/theme.js") > strings.Index(head, "/static/app.css") {
			t.Errorf("%s: theme.js comes after the stylesheet", path)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'" {
			t.Errorf("%s: CSP = %q", path, got)
		}
		// Login and the certificate page have no sidebar.
		if path != "/" && strings.Contains(body, `aria-label="Menu utama"`) {
			t.Errorf("%s shows the sidebar", path)
		}
	}
}

var fontFiles = []string{"bungee-regular", "worksans-400", "worksans-500", "worksans-600", "spacemono-400", "spacemono-700"}

func TestStaticAssetsFonts(t *testing.T) {
	e := newEnv(t)
	total := 0
	for _, f := range fontFiles {
		rec := e.do("GET", "/static/fonts/"+f+".woff2", nil, nil)
		wantCode(t, rec, http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("%s: Content-Type = %q, want font/woff2", f, ct)
		}
		b, _ := io.ReadAll(rec.Body)
		if !strings.HasPrefix(string(b), "wOF2") {
			t.Errorf("%s is not a WOFF2 file", f)
		}
		total += len(b)
	}
	if total > 400<<10 {
		t.Errorf("fonts total %d bytes, want <= 400 KB", total)
	}
	rec := e.do("GET", "/static/fonts/LICENSE", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "SIL OPEN FONT LICENSE", "Bungee", "Work Sans", "Space Mono")
}

func TestSettingsTampilan(t *testing.T) {
	e := settingsEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/settings", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Tampilan", `name="nama_instansi"`, `maxlength="60"`, `name="tema_bawaan"`,
		`<option value="sistem" selected>`, `<option value="gelap">`, `<option value="terang">`)

	long := strings.Repeat("a", 61)
	rec = e.do("POST", "/settings", with(validSettings(), "nama_instansi", long), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Nama instansi paling panjang 60 karakter.")
	for _, bad := range []string{"Dinas \u202eatok", "Dinas\u200bKota", "Dinas\x07Kota"} {
		rec = e.do("POST", "/settings", with(validSettings(), "nama_instansi", bad), c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Nama instansi tidak boleh memuat karakter kendali atau karakter tak terlihat.")
	}
	rec = e.do("POST", "/settings", with(validSettings(), "tema_bawaan", "ungu"), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Tema bawaan tidak dikenal.")
	if v, _ := e.st.GetSetting(store.SettingTemaBawaan); v != "" {
		t.Errorf("tema_bawaan = %q after refused saves", v)
	}

	// 60 characters (not bytes) is allowed.
	name := strings.Repeat("é", 60)
	rec = e.do("POST", "/settings", with(validSettings(), "nama_instansi", name, "tema_bawaan", "gelap"), c)
	wantCode(t, rec, http.StatusSeeOther)
	if v, _ := e.st.GetSetting(store.SettingTemaBawaan); v != "gelap" {
		t.Errorf("tema_bawaan = %q, want gelap", v)
	}
	if v, _ := e.st.GetSetting(store.SettingNamaInstansi); v != name {
		t.Errorf("nama_instansi = %q, want %q", v, name)
	}
	rec = e.do("GET", "/settings", nil, c)
	wantBody(t, rec, `data-tema-bawaan="gelap"`, `<option value="gelap" selected>`, `value="`+name+`"`)
	rec = e.do("GET", "/login", nil, nil)
	wantBody(t, rec, `data-tema-bawaan="gelap"`)

	// The audit entry lists both keys as changed.
	var last store.AuditEntry
	for _, a := range auditEntries(t, e.st) {
		if a.Action == "settings_update" && a.Outcome == "ok" && a.ID > last.ID {
			last = a
		}
	}
	for _, k := range []string{store.SettingNamaInstansi, store.SettingTemaBawaan} {
		if !strings.Contains(last.DetailJSON, `"`+k+`"`) {
			t.Errorf("audit detail %s does not list %s", last.DetailJSON, k)
		}
	}

	// A form without the fields (an older page) keeps the stored values.
	rec = e.do("POST", "/settings", validSettings(), c)
	wantCode(t, rec, http.StatusSeeOther)
	if v, _ := e.st.GetSetting(store.SettingTemaBawaan); v != "gelap" {
		t.Errorf("tema_bawaan = %q after a form without it, want gelap", v)
	}
}
