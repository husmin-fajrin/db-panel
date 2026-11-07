package web_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The cutover screen (spec 5.5).

func TestCutoverPageListsConnectionsBothSides(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	f.setConnections(1, proto.ConnectionRow{Host: "10.0.0.50", User: "app_lama", DB: "simrs", Count: 3})
	f.setConnections(2, proto.ConnectionRow{Host: "10.0.0.51", User: "app_baru", DB: "simrs", Count: 5})

	rec := e.do("GET", "/masters/2/cutover", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "10.0.0.2:3306", "Arahkan semua aplikasi yang menulis ke database ke alamat ini, lalu restart aplikasinya.",
		"Koneksi yang masih ke master lama", "10.0.0.50", "app_lama",
		"Koneksi yang sudah ke master baru", "10.0.0.51", "app_baru",
		`x-data="pollFragment"`, `data-url="/servers/1/connections-fragment"`, `data-url="/servers/2/connections-fragment"`,
		`action="/masters/2/cutover/done"`, "Sudah saya alihkan", "Promote selesai")
	wantNoBody(t, rec, "<script>")

	// The fragment: the same table, without the layout.
	rec = e.do("GET", "/servers/1/connections-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "10.0.0.50", "app_lama")
	wantNoBody(t, rec, "<html")

	// Empty, and an unreachable old master: said, not failed.
	f.setConnections(1)
	wantBody(t, e.do("GET", "/servers/1/connections-fragment", nil, c), "Tidak ada koneksi aplikasi tersisa")
	f.setConnErr(1, proto.ErrOffline)
	rec = e.do("GET", "/masters/2/cutover", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Master lama tidak terjangkau, tidak bisa membaca koneksinya", "10.0.0.51")
	rec = e.do("GET", "/servers/1/connections-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Master lama tidak terjangkau, tidak bisa membaca koneksinya")

	// A master is linked to its cutover screen while it is pending.
	wantBody(t, e.do("GET", "/masters/2", nil, c), `href="/masters/2/cutover"`)
}

func TestCutoverDoneClearsPendingAndAudits(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/", nil, c)
	wantBody(t, rec, "Promote menunggu pengalihan aplikasi", `href="/masters/2/cutover"`)

	rec = e.do("POST", "/masters/2/cutover/done", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/2?pesan=dialihkan")
	wantCalls(t, f, "confirm-cutover "+itoa(topo.ID))
	if got, _ := e.st.GetTopology(topo.ID); got.CutoverPending {
		t.Fatal("cutover still pending")
	}
	entries, err := e.st.ListAudit(0, time.Time{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range entries {
		found = found || (a.Action == "cutover_confirmed" && a.TargetKind == "master" && a.TargetID == 2 && a.Outcome == "ok")
	}
	if !found {
		t.Fatalf("no cutover_confirmed audit: %+v", entries)
	}
	wantNoBody(t, e.do("GET", "/", nil, c), "Promote menunggu pengalihan aplikasi")
	// Nothing is pending any more: the screen says so and has no button.
	rec = e.do("GET", "/masters/2/cutover", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, `action="/masters/2/cutover/done"`)

	// The orchestrator's warning (old master not quarantined) is shown.
	e2, f2, topo2 := promotedEnv(t)
	f2.cutoverWarning = "Master lama db-master belum dikarantina karena agent-nya belum terjangkau."
	rec = e2.do("POST", "/masters/2/cutover/done", noForm, e2.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, f2.cutoverWarning)
	if got, _ := e2.st.GetTopology(topo2.ID); got.CutoverPending {
		t.Fatal("cutover still pending")
	}
	if !strings.Contains(rec.Body.String(), "Pengalihan aplikasi dicatat") {
		t.Errorf("no notice: %s", rec.Body.String())
	}
}
