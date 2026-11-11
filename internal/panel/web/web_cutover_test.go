package web_test

import (
	"errors"
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
	f.setConnections(1, proto.ConnectionRow{Host: "10.0.0.50", User: "app_lama", DB: "simrs", Count: 3},
		proto.ConnectionRow{Host: "10.0.0.52", User: "root", DB: "simrs", Count: 2, AdminAccount: true})
	f.setConnections(2, proto.ConnectionRow{Host: "10.0.0.51", User: "app_baru", DB: "simrs", Count: 5})

	rec := e.do("GET", "/masters/2/cutover", nil, c)
	wantCode(t, rec, http.StatusOK)
	// The admin account's sessions from other hosts are shown, marked;
	// the page says exactly what is hidden (SEC-67).
	wantBody(t, rec, "10.0.0.52", "akun admin panel", "Jumlah koneksi: <strong>5</strong>",
		"Tidak ditampilkan: akun panel (dbpanel_*), replika yang membaca binlog, thread MySQL sendiri, koneksi agent sendiri, "+
			"dan sesi akun admin MySQL yang dipakai agent bila tersambung dari server itu sendiri (socket, localhost, 127.0.0.1 atau ::1).")
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

const cutoverGreen = "Tidak ada koneksi aplikasi tersisa"

// TestCutoverNoConnectionsOnlyWhenCompleteAndEmpty: the green sentence is
// shown only for a typed, complete, empty result; every doubt says "tidak
// bisa dipastikan" with its reason instead (spec 5.5).
func TestCutoverNoConnectionsOnlyWhenCompleteAndEmpty(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	read := func() string {
		rec := e.do("GET", "/servers/1/connections-fragment", nil, c)
		wantCode(t, rec, http.StatusOK)
		return rec.Body.String()
	}

	f.setConnectionsResult(1, proto.ConnectionsResult{Typed: true, Source: "performance_schema"})
	wantBody(t, e.do("GET", "/servers/1/connections-fragment", nil, c), cutoverGreen)
	wantNoBody(t, e.do("GET", "/servers/1/connections-fragment", nil, c), "tidak bisa dipastikan")

	for name, tc := range map[string]struct {
		res  proto.ConnectionsResult
		want []string
	}{
		"old agent: empty but not typed": {proto.ConnectionsResult{},
			[]string{"agent perlu dimutakhirkan: daftar koneksinya belum lengkap"}},
		"incomplete": {proto.ConnectionsResult{Typed: true, Incomplete: "thread_instances_lost 3 <i>"},
			[]string{"thread_instances_lost 3 &lt;i&gt;"}},
		"unreadable": {proto.ConnectionsResult{Typed: true, Unreadable: "kedua sumber gagal"},
			[]string{"kedua sumber gagal"}},
		"only local admin sessions": {proto.ConnectionsResult{Typed: true,
			Rows: []proto.ConnectionRow{{Host: "localhost", User: "dbpanel_admin", Count: 2, Kind: "sql", LocalAdmin: true}}, LocalAdmin: 2},
			[]string{"sesi akun admin dari server ini", "Jumlah koneksi: <strong>2</strong>"}},
	} {
		f.setConnectionsResult(1, tc.res)
		body := read()
		if strings.Contains(body, cutoverGreen) {
			t.Errorf("%s: shows the green sentence:\n%s", name, body)
		}
		if tc.res.Unreadable != "" || tc.res.Incomplete != "" || !tc.res.Typed {
			tc.want = append(tc.want, "tidak bisa dipastikan")
		}
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q in:\n%s", name, w, body)
			}
		}
	}

	// Any command error (offline, timeout, a failed admin login) is "not
	// known", never "none".
	for name, err := range map[string]error{"offline": proto.ErrOffline, "command error": errors.New("login admin gagal")} {
		f.setConnErr(1, err)
		body := read()
		if strings.Contains(body, cutoverGreen) || !strings.Contains(body, "tidak bisa dipastikan") {
			t.Errorf("%s: fragment = %s", name, body)
		}
	}
}

// TestCutoverBadges: the row badges and the note about sessions the agent
// cannot tell from its own.
func TestCutoverBadges(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	f.setConnectionsResult(1, proto.ConnectionsResult{Typed: true, Source: "information_schema", AgentIndistinct: true, LocalAdmin: 1,
		Rows: []proto.ConnectionRow{
			{Host: "10.0.0.50", User: "app_sql", DB: "simrs", Count: 1, Kind: "sql"},
			{Host: "10.0.0.51", User: "app_x", DB: "simrs", Count: 1, Kind: "x"},
			{Host: "10.0.0.52", User: "app_aneh", DB: "simrs", Count: 1, Kind: "lain"},
			{Host: "10.0.0.53", User: "dbpanel_admin", Count: 1, Kind: "sql", AdminAccount: true},
			{Host: "localhost", User: "dbpanel_admin", Count: 1, Kind: "sql", LocalAdmin: true},
		}})
	rec := e.do("GET", "/servers/1/connections-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "X Protocol", "jenis koneksi tidak dikenal", "akun admin panel", "sesi akun admin dari server ini",
		"tidak bisa dibedakan dari agent", "Jumlah koneksi: <strong>5</strong>")
	if n := strings.Count(rec.Body.String(), "X Protocol"); n != 1 {
		t.Errorf("X Protocol badge shown %d times, want 1 (the sql row has none)", n)
	}
	wantNoBody(t, rec, cutoverGreen)

	// A complete result without those rows shows none of them.
	f.setConnections(1, proto.ConnectionRow{Host: "10.0.0.50", User: "app_sql", DB: "simrs", Count: 1, Kind: "sql"})
	rec = e.do("GET", "/servers/1/connections-fragment", nil, c)
	wantNoBody(t, rec, "X Protocol", "jenis koneksi tidak dikenal", "sesi akun admin dari server ini", "tidak bisa dibedakan dari agent")
}

// TestCutoverEscapesAgentText: host, user, database and the agent's reasons
// are data; they reach the page escaped, on the screen and in the rebuild
// form.
func TestCutoverEscapesAgentText(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	f.setConnectionsResult(1, proto.ConnectionsResult{Typed: true, Incomplete: "<script>alert(1)</script>",
		Rows: []proto.ConnectionRow{{Host: "<b>h</b>", User: "<i>u</i>", DB: "<u>d</u>", Count: 1, Kind: "sql"}}})
	for _, path := range []string{"/servers/1/connections-fragment", "/masters/2/replicas/new?bangun-ulang=1"} {
		rec := e.do("GET", path, nil, c)
		wantCode(t, rec, http.StatusOK)
		wantNoBody(t, rec, "<script>alert", "<b>h</b>", "<i>u</i>", "<u>d</u>")
	}
	wantBody(t, e.do("GET", "/servers/1/connections-fragment", nil, c), "&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;b&gt;h&lt;/b&gt;")
}
