package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// The Replikasi page (design spec 5.4): a list of topologies, or the one
// topology's master page straight away.
func TestReplicationListAndRedirect(t *testing.T) {
	e, _, _ := monitorEnv(t)
	c := e.cookie()

	// One topology: straight to its master page.
	rec := e.do("GET", "/replikasi", nil, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1")

	// The master page is under Replikasi in the sidebar.
	rec = e.do("GET", "/masters/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	links := menuLinks(sidebar(t, rec.Body.String()))
	if !strings.Contains(links["replikasi"], "menu-active") || strings.Contains(links["servers"], "menu-active") {
		t.Errorf("master page: Replikasi not the active menu: %v", links)
	}

	// Two topologies: a list with each master, its replica count and a
	// link to its page.
	other := e.pairedOnline(t, "db-gudang")
	e.makeMaster(t, other)
	rec = e.do("GET", "/replikasi", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantBody(t, rec, "<h1>Replikasi</h1>", "db-master", "db-gudang", `href="/masters/1"`, `href="/masters/3"`,
		"1 replika", "0 replika", `data-topologi="1"`, `data-topologi="2"`)
	links = menuLinks(sidebar(t, body))
	if !strings.Contains(links["replikasi"], `href="/replikasi"`) || !strings.Contains(links["replikasi"], "menu-active") {
		t.Errorf("Replikasi menu entry = %q", links["replikasi"])
	}
	row := between(body, `data-topologi="1"`, `data-topologi="2"`)
	for _, s := range []string{"db-replika", `data-status-for="1"`, `data-status-for="2"`} {
		if !strings.Contains(row, s) {
			t.Errorf("topology 1 lacks %q:\n%s", s, row)
		}
	}
}

// TestReplicationEmpty: with no topology the page offers to start one.
func TestReplicationEmpty(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/replikasi", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "<h1>Replikasi</h1>", "data-empty", `href="/servers"`)
}
