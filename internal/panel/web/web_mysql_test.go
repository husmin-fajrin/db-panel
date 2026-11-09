package web_test

import (
	"net/http"
	"regexp"
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
