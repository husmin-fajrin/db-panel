package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The server page's MySQL card (design spec 5.6, 5.8 #3).

// TestServiceLineOf: the Layanan row says what was checked; red only when
// MySQL does not answer.
func TestServiceLineOf(t *testing.T) {
	for _, tc := range []struct {
		name             string
		active, mysqlOK  bool
		wantText, wantTn string
	}{
		{"mysql", true, true, "MySQL menjawab · layanan systemd mysql aktif", "success"},
		{"mysql", false, true, "MySQL menjawab · layanan systemd tidak terdeteksi (mungkin bukan systemd)", "neutral"},
		{"mysqld", true, false, "MySQL tidak menjawab", "error"},
		{"mysqld", false, false, "MySQL tidak menjawab", "error"},
	} {
		got := web.ServiceLineOf(tc.name, tc.active, tc.mysqlOK)
		if got.Text != tc.wantText || got.Tone != tc.wantTn {
			t.Errorf("serviceLineOf(%q, %v, %v) = %+v, want {%q %q}", tc.name, tc.active, tc.mysqlOK, got, tc.wantText, tc.wantTn)
		}
	}
}

// TestServerPageServiceLine: a MySQL that answers while systemd knows no
// unit is not shown as stopped.
func TestServerPageServiceLine(t *testing.T) {
	e, _, mon := controlEnv(t)
	if err := e.st.UpdateProbe(1, proto.ProbeResult{MySQL: proto.MySQLInfo{Installed: true, Version: "8.0.39",
		ServiceName: "mysql", ServiceActive: false, Port: 3306, DataDir: "/var/lib/mysql"}}, "v-test", time.Now()); err != nil {
		t.Fatal(err)
	}
	mon.setView(monitorViewOK(1))
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "MySQL menjawab · layanan systemd tidak terdeteksi (mungkin bukan systemd)")
	wantNoBody(t, rec, ">berhenti<")
}

// TestPrepareAgainUnderAdvanced: on a master, Siapkan sebagai master sits
// in the folded Tindakan lanjutan with its caption; elsewhere it is the
// primary button it always was.
func TestPrepareAgainUnderAdvanced(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	e.makeMaster(t, master, shopDatabases...)
	other := e.pairedOnline(t, "db-lain")
	if err := e.st.SetInventory(storeInventory(other.ID)); err != nil {
		t.Fatal(err)
	}

	body := e.do("GET", "/servers/1", nil, c).Body.String()
	adv := regexp.MustCompile(`(?s)<details[^>]*data-advanced[^>]*>.*?</details>`).FindString(body)
	if adv == "" {
		t.Fatalf("master page has no Tindakan lanjutan:\n%s", body)
	}
	for _, s := range []string{"Tindakan lanjutan", "Siapkan ulang (akun replikasi, pengaturan binlog)",
		`action="/servers/1/prepare-master"`, `data-confirm-action="prepare_master_again"`, "Siapkan sebagai master"} {
		if !strings.Contains(adv, s) {
			t.Errorf("Tindakan lanjutan lacks %q:\n%s", s, adv)
		}
	}
	if n := strings.Count(body, `action="/servers/1/prepare-master"`); n != 1 {
		t.Errorf("prepare-master forms on the master's page = %d, want 1", n)
	}

	body = e.do("GET", "/servers/2", nil, c).Body.String()
	if strings.Contains(body, "data-advanced") {
		t.Errorf("non-master page folds Siapkan sebagai master away:\n%s", body)
	}
	if !regexp.MustCompile(`<button type="submit" class="[^"]*btn-primary[^"]*">Siapkan sebagai master</button>`).MatchString(body) {
		t.Errorf("non-master page has no primary Siapkan sebagai master:\n%s", body)
	}
}

// TestServersListLocalLabel: the panel's own server is "server panel",
// never "Belum dipasangkan"; names do not wrap.
func TestServersListLocalLabel(t *testing.T) {
	e := newEnv(t, func(d *web.Deps) { d.LocalServerID = 1 })
	e.st.CreateServer("vm")
	e.st.CreateServer("db-baru")
	rec := e.do("GET", "/servers", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	row := between(body, `href="/servers/1"`, "</tr>")
	if !strings.Contains(row, ">server panel<") || strings.Contains(row, "Belum dipasangkan") {
		t.Errorf("local server row:\n%s", row)
	}
	if row2 := between(body, `href="/servers/2"`, "</tr>"); !strings.Contains(row2, ">Belum dipasangkan<") {
		t.Errorf("unpaired server row:\n%s", row2)
	}
	if !regexp.MustCompile(`<td class="[^"]*whitespace-nowrap[^"]*"><a href="/servers/1"`).MatchString(body) {
		t.Errorf("server name cell may wrap:\n%s", body)
	}
	if strings.Contains(body[strings.Index(body, "<main"):], `class="badge`) {
		t.Errorf("raw daisyUI badge on the server list:\n%s", body)
	}
}

// monitorViewOK is a healthy master's view: MySQL answers.
func monitorViewOK(id int64) monitor.View {
	return monitor.View{ServerID: id, Role: store.RoleMaster, ReceivedAt: time.Now(),
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK, Detail: "Master menjawab dan bisa ditulisi"},
		Snap:   &proto.HealthSnapshot{MySQLOK: true}}
}

// storeInventory is a connected inventory with every grant.
func storeInventory(id int64) store.Inventory {
	return store.Inventory{ServerID: id, Connected: true, Version: "8.0.39", Series: "8.0",
		Databases: shopDatabases, UpdatedAt: time.Now()}
}

// TestDatabasesPageNotes (spec 5.6, R-058): Tambah and Lepas without
// dropping the data both say that a database added again has its content
// on the replica dropped first.
func TestDatabasesPageNotes(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.subsetReplica(t, []string{"gudang", "toko"}, archiveDatabases...)
	rec := e.do("GET", "/replicas/2/databases", nil, c)
	wantCode(t, rec, http.StatusOK)
	const note = "bila ditambahkan lagi, isi di replika dibuang lebih dulu"
	body := rec.Body.String()
	addForm, removeForm, ok := strings.Cut(body, `action="/replicas/2/databases/remove"`)
	if !ok {
		t.Fatalf("no remove form:\n%s", body)
	}
	addForm = addForm[strings.Index(addForm, `action="/replicas/2/databases/add"`):]
	if !strings.Contains(addForm, note) {
		t.Errorf("Tambah lacks the note:\n%s", addForm)
	}
	_, keep, _ := strings.Cut(removeForm, "Hapus datanya juga di replika")
	if keep, _, _ = strings.Cut(keep, "</form>"); !strings.Contains(keep, note) {
		t.Errorf("Lepas without dropping the data lacks the note:\n%s", keep)
	}
}

// The address for replicas on the master page (spec 5.1, "Alamat master").

// msgMasterSourceConfirm is T4, with the typed address left for the
// browser to fill in (data-confirm-value-from).
const msgMasterSourceConfirm = "Ubah alamat master untuk replika menjadi %s? Replika baru dan Resync ulang akan menyalin dari alamat itu."

// TestMasterSourceNeedsConfirm: changing the address needs a light
// confirmation before any other check; without one the answer is 403 and
// the stored address is unchanged, whatever was posted. A token through
// the session's window will do; the save button opens the light dialog
// with T4.
func TestMasterSourceNeedsConfirm(t *testing.T) {
	e, _, _ := windowEnv(t)
	c := e.cookie()
	wantBody(t, e.do("GET", "/masters/1", nil, c), `data-confirm-action="master_source"`, `data-confirm-kind="master"`,
		`data-confirm-id="1"`, `data-confirm-light="1"`, `data-confirm-value-from="source_host source_port"`,
		`data-confirm-text="`+msgMasterSourceConfirm+`"`)
	good := url.Values{"source_host": {"192.168.5.9"}, "source_port": {"3307"}}
	bad := url.Values{"source_host": {"10.0.0.9 --x"}, "source_port": {"0"}}
	unchanged := func(what string) {
		t.Helper()
		got, err := e.st.GetTopologyByMaster(1)
		if err != nil || got.SourceHost != "10.0.0.1" || got.SourcePort != 3306 || got.SourceChangedBy != "" {
			t.Fatalf("%s: topology = %+v, %v; want 10.0.0.1:3306 unchanged", what, got, err)
		}
	}
	for _, form := range []url.Values{good, bad} {
		for _, tok := range []string{"", "salah"} {
			f := url.Values{"source_host": form["source_host"], "source_port": form["source_port"]}
			if tok != "" {
				f.Set("confirm_token", tok)
			}
			rec := e.do("POST", "/masters/1/source", f, c)
			wantCode(t, rec, http.StatusForbidden)
			wantBody(t, rec, msgConfirmInvalid)
			wantNoBody(t, rec, msgSourceHostBad)
			unchanged("post without a confirmation")
		}
	}
	// A token for another action does not do either.
	f := url.Values{"source_host": good["source_host"], "source_port": good["source_port"],
		"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", 1, c)}}
	wantCode(t, e.do("POST", "/masters/1/source", f, c), http.StatusForbidden)
	unchanged("post with another action's confirmation")

	f.Set("confirm_token", windowToken(t, e, web.ActMasterSource, "master", 1, c))
	rec := e.do("POST", "/masters/1/source", f, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1?pesan=sumber")
	if got, _ := e.st.GetTopologyByMaster(1); got.SourceHost != "192.168.5.9" || got.SourcePort != 3307 {
		t.Fatalf("topology after a confirmed post = %+v", got)
	}
}

// msgSourceHostBad is the refusal of an address that is not a host.
const msgSourceHostBad = "Alamat master tidak valid: isi alamat IP atau nama host saja."

// TestMasterSourceAuditOldAndNew: the master_source entry holds the
// address before and after the change; a refused change is audited too.
func TestMasterSourceAuditOldAndNew(t *testing.T) {
	e, _, _ := windowEnv(t)
	c := e.cookie()
	post := func(host, port string) {
		t.Helper()
		f := url.Values{"source_host": {host}, "source_port": {port},
			"confirm_token": {windowToken(t, e, web.ActMasterSource, "master", 1, c)}}
		if rec := e.do("POST", "/masters/1/source", f, c); rec.Code >= 500 {
			t.Fatalf("POST source: %d", rec.Code)
		}
	}
	post("192.168.5.9", "3307")
	post("db baru", "3306")
	var got []string
	for _, en := range auditEntries(t, e.st) {
		if en.Action == "master_source" {
			if en.TargetKind != "master" || en.TargetID != 1 {
				t.Fatalf("master_source entry = %+v", en)
			}
			got = append(got, en.Outcome+" "+en.DetailJSON)
		}
	}
	want := []string{
		`ok {"old_source_host":"10.0.0.1","old_source_port":3306,"source_host":"192.168.5.9","source_port":"3307"}`,
		// A refused post's text values are kept as lengths only (the
		// audit log's rule for refused input); nothing changed.
		msgSourceHostBad + ` {"old_source_host_len":11,"old_source_port":3307,"source_host_len":7,"source_port_len":4}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("master_source entries =\n%q\nwant\n%q", got, want)
	}
}

// TestMasterSourceBanner: the "Arahkan agent replika" banner says who set
// the address and when: T5 for a user, T5b for a promote job; a topology
// without that record (older installs) shows the banner without either.
func TestMasterSourceBanner(t *testing.T) {
	e, _, clock := windowEnv(t)
	c := e.cookie()
	const t5 = "Alamat ini diubah oleh"
	const t5b = "Alamat ini dipasang panel saat promote"
	rec := e.do("GET", "/masters/1?pesan=sumber", nil, c)
	wantBody(t, rec, "data-set-source", "Arahkan agent replika")
	wantNoBody(t, rec, t5, t5b)

	f := url.Values{"source_host": {"192.168.5.9"}, "source_port": {"3307"},
		"confirm_token": {windowToken(t, e, web.ActMasterSource, "master", 1, c)}}
	wantCode(t, e.do("POST", "/masters/1/source", f, c), http.StatusSeeOther)
	at := clock.Now().Local().Format("02-01-2006 15:04")
	rec = e.do("GET", "/masters/1?pesan=sumber", nil, c)
	wantBody(t, rec, "data-set-source", "Alamat ini diubah oleh admin pada "+at+
		". Pastikan itu memang alamat server db-master sebelum menjalankan perintah di bawah.")
	wantNoBody(t, rec, t5b)

	// After a promote: the address is the job's.
	third := e.pairedOnline(t, "db-tiga")
	topo, err := e.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "0123456789abcdef0123456789abcdef"
	swapped := time.Date(2026, 11, 28, 9, 15, 0, 0, time.UTC)
	if err := e.st.SwapMaster(topo.ID, 1, 2, "10.0.0.2", 3306, "job:"+jobID, swapped); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AddReplica(topo.ID, third.ID, nil); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/masters/2?pesan=sumber", nil, c)
	wantBody(t, rec, "data-set-source", "Alamat ini dipasang panel saat promote (pekerjaan "+jobID+") pada "+
		swapped.Local().Format("02-01-2006 15:04")+".")
	wantNoBody(t, rec, t5)
}
