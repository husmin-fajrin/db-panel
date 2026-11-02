package web_test

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// fakeMonitor serves views set by the test and records CheckNow and Snooze.
type fakeMonitor struct {
	mu      sync.Mutex
	views   map[int64]monitor.View
	checks  []int64
	snoozes []snoozeCall
	err     error // returned by CheckNow and Snooze when set
}

type snoozeCall struct {
	id    int64
	until *time.Time
}

func (m *fakeMonitor) View(id int64) (monitor.View, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.views[id]
	return v, ok
}

func (m *fakeMonitor) Views() map[int64]monitor.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int64]monitor.View, len(m.views))
	for id, v := range m.views {
		out[id] = v
	}
	return out
}

func (m *fakeMonitor) CheckNow(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checks = append(m.checks, id)
	return m.err
}

func (m *fakeMonitor) Snooze(id int64, until *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snoozes = append(m.snoozes, snoozeCall{id, until})
	return m.err
}

func (m *fakeMonitor) setView(v monitor.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.views == nil {
		m.views = map[int64]monitor.View{}
	}
	m.views[v.ServerID] = v
}

// controlEnv is a master (server 1, db-master) with a ready replica
// (server 2, db-replika) following toko and gudang, a fake orchestrator
// and a fake monitor.
func controlEnv(t *testing.T) (*env, *fakeOrch, *fakeMonitor) {
	t.Helper()
	mon := &fakeMonitor{}
	f := &fakeOrch{nextID: "0123456789abcdef0123456789abcdef"}
	e := newEnv(t, func(d *web.Deps) {
		f.st = d.Store
		d.Orchestrator = f
		d.Monitor = mon
	})
	master := e.pairedOnline(t, "db-master")
	rep := e.pairedOnline(t, "db-replika")
	topo := e.makeMaster(t, master, shopDatabases...)
	if _, err := e.st.AddReplica(topo.ID, rep.ID, []string{"toko", "gudang"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(rep.ID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	return e, f, mon
}

var noForm = url.Values{}

func TestStopStartButtonsCallOrchestrator(t *testing.T) {
	e, f, mon := controlEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster,
		Snap: &proto.HealthSnapshot{BinlogExpireSec: 7 * 86400}})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, Status: monitor.Status{Code: monitor.StatusSynced}})

	// Running: Stop (with the binlog warning), no Start, no Kunci kembali.
	rec := e.do("GET", "/replicas/2", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/replicas/2/stop"`, "Stop replika",
		"Lag akan membesar selama replika berhenti. Master menyimpan binlog 7 hari; lebih dari itu replika harus disync ulang.",
		`action="/replicas/2/resync"`, `action="/replicas/2/detach"`,
		`action="/servers/2/check"`, `action="/servers/2/snooze"`)
	wantNoBody(t, rec, `action="/replicas/2/start"`, `action="/replicas/2/relock"`)

	rec = e.do("POST", "/replicas/2/stop", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?pesan=dihentikan")
	wantBody(t, e.do("GET", "/replicas/2?pesan=dihentikan", nil, c), "Replikasi dihentikan")

	// Paused through the panel: Start, no Stop.
	if err := e.st.SetReplicaPaused(2, true); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, `action="/replicas/2/start"`, "Start replika")
	wantNoBody(t, rec, `action="/replicas/2/stop"`)
	rec = e.do("POST", "/replicas/2/start", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?pesan=dijalankan")

	// Stopped outside the panel (not paused): Start too.
	if err := e.st.SetReplicaPaused(2, false); err != nil {
		t.Fatal(err)
	}
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica,
		Status: monitor.Status{Code: monitor.StatusStoppedOutside, LockLost: true}})
	rec = e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, `action="/replicas/2/start"`, `action="/replicas/2/relock"`, "Kunci kembali",
		"Bila ada yang sempat menulis ke replika, isinya bisa berbeda dari master; pertimbangkan Resync ulang.")
	wantNoBody(t, rec, `action="/replicas/2/stop"`)
	rec = e.do("POST", "/replicas/2/relock", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?pesan=dikunci")

	// A refusal is shown on the replica page.
	f.err = errors.New("server ini masih punya pekerjaan yang belum selesai")
	rec = e.do("POST", "/replicas/2/stop", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server ini masih punya pekerjaan yang belum selesai", "Replika db-replika")
	wantCalls(t, f, "stop-replica 2", "start-replica 2", "relock 2", "stop-replica 2")

	// Unknown binlog retention: the sentence about it is left out.
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Snap: &proto.HealthSnapshot{}})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica})
	rec = e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, "Lag akan membesar selama replika berhenti.")
	wantNoBody(t, rec, "Master menyimpan binlog")

	// Not a replica: 404, nothing called.
	f.err = nil
	wantCode(t, e.do("POST", "/replicas/1/stop", noForm, c), http.StatusNotFound)
	wantCalls(t, f, "stop-replica 2", "start-replica 2", "relock 2", "stop-replica 2")
}

func TestResyncRequiresTypedName(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	for _, name := range []string{"", "db-master", "DB-REPLIKA", "db-replika "} {
		form := url.Values{"confirm_name": {name}, "wipe_list[]": {"toko"}}
		rec := e.do("POST", "/replicas/2/resync", form, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Nama server tidak cocok")
	}
	wantCalls(t, f)
}

func TestResyncPassesWipeListAndForceDump(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	// The replica's MySQL holds toko, gudang and an extra database.
	f.result = proto.ConnectResult{Databases: append(shopDatabases, proto.DatabaseInfo{Name: "sisa-lama", Bytes: 1 << 20})}
	if err := f.storeInventory(2); err != nil {
		t.Fatal(err)
	}

	// A GET never reads the inventory again, whatever its query; the
	// dialog opens only when asked.
	for _, q := range []string{"", "?segarkan=1", "?buka=resync", "?segarkan=1&buka=resync"} {
		wantCode(t, e.do("GET", "/replicas/2"+q, nil, c), http.StatusOK)
	}
	wantCalls(t, f)
	rec := e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, `action="/replicas/2/resync/prepare"`, "Resync ulang", `data-open="false"`)
	wantNoBody(t, rec, `data-open="true"`)

	// Resync ulang reads the replica's databases first (a POST), then opens
	// the dialog on the fresh list.
	f.result.Databases = shopDatabases
	rec = e.do("POST", "/replicas/2/resync/prepare", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?buka=resync")
	rec = e.do("GET", "/replicas/2?buka=resync", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `id="modal-resync" class="modal" x-data="openDialog" data-open="true"`,
		`x-data="confirmName"`, `data-name="db-replika"`, `name="confirm_name"`,
		`name="wipe_list[]" value="toko"`, `name="wipe_list[]" value="gudang"`,
		`name="force_dump"`, "1.8 GB", "luar jam sibuk", "MySQL di replika bisa dimulai ulang (restart)")
	wantNoBody(t, rec, `value="sisa-lama"`)

	// A refresh that fails says so and does not open the dialog.
	f.err = errors.New("agent db-replika tidak terhubung")
	rec = e.do("POST", "/replicas/2/resync/prepare", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Daftar database replika tidak bisa disegarkan", "agent db-replika tidak terhubung")
	wantNoBody(t, rec, `data-open="true"`)
	f.err = nil

	form := url.Values{"confirm_name": {"db-replika"}, "force_dump": {"1"}, "wipe_list[]": {"toko", "gudang"}}
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)

	form.Del("force_dump")
	form.Del("wipe_list[]")
	wantCode(t, e.do("POST", "/replicas/2/resync", form, c), http.StatusSeeOther)

	// Only databases of the stored inventory, each once: a stale or crafted
	// list is refused before the orchestrator.
	for _, list := range [][]string{{"sisa-lama"}, {"toko", "toko"}, {"toko", "tidak-ada"}, {""}} {
		form["wipe_list[]"] = list
		rec = e.do("POST", "/replicas/2/resync", form, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Daftar database tidak sesuai dengan inventaris; segarkan daftar lalu coba lagi")
	}

	f.err = errors.New("replika sedang diubah; Resync ulang hanya bisa saat replika siap, perlu perhatian, atau gagal")
	form.Set("wipe_list[]", "toko")
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Resync ulang hanya bisa saat replika siap")
	wantCalls(t, f, "refresh 2", "refresh 2", "resync 2 toko,gudang force=true", "resync 2  force=false",
		"resync 2 toko force=false")
}

func TestDetachModes(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, `action="/replicas/2/detach"`, `name="mode" value="keep"`, `name="mode" value="stop"`,
		"Lepas, biarkan replikasi berjalan", "Lepas dan hentikan replikasi",
		"MySQL di server ini terus mengikuti master sampai Anda menghentikannya sendiri; panel tidak lagi memantaunya.",
		// dbpanel locked with SET PERSIST: a SET GLOBAL unlock would not survive a restart.
		`mysql -e "SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF"`, "tetap terbuka walau MySQL di-restart")
	wantNoBody(t, rec, "SET GLOBAL super_read_only")

	// Wrong name or no mode: nothing is called.
	for _, form := range []url.Values{
		{"confirm_name": {"db-master"}, "mode": {"keep"}},
		{"confirm_name": {"db-replika"}},
		{"confirm_name": {"db-replika"}, "mode": {"hapus"}},
	} {
		wantCode(t, e.do("POST", "/replicas/2/detach", form, c), http.StatusBadRequest)
	}
	wantCalls(t, f)

	rec = e.do("POST", "/replicas/2/detach", url.Values{"confirm_name": {"db-replika"}, "mode": {"keep"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/2?pesan=dilepas")
	wantBody(t, e.do("GET", "/servers/2?pesan=dilepas", nil, c), "panel tidak lagi memantaunya")

	rec = e.do("POST", "/replicas/2/detach", url.Values{"confirm_name": {"db-replika"}, "mode": {"stop"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/2?pesan=dilepas-henti")
	wantBody(t, e.do("GET", "/servers/2?pesan=dilepas-henti", nil, c),
		"SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF", "tetap terbuka walau MySQL di-restart")

	f.err = errors.New("server ini masih punya pekerjaan yang belum selesai")
	rec = e.do("POST", "/replicas/2/detach", url.Values{"confirm_name": {"db-replika"}, "mode": {"stop"}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server ini masih punya pekerjaan yang belum selesai")
	wantCalls(t, f, "detach 2 stop=false", "detach 2 stop=true", "detach 2 stop=true")
}

func TestUnlockMasterRequiresTypedName(t *testing.T) {
	e, f, mon := controlEnv(t)
	c := e.cookie()
	// Not locked: no button.
	wantNoBody(t, e.do("GET", "/masters/1", nil, c), `action="/masters/1/unlock"`)
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked}})
	rec := e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, `action="/masters/1/unlock"`, "Buka kunci master", `data-name="db-master"`,
		`action="/servers/1/check"`, `action="/servers/1/snooze"`)

	rec = e.do("POST", "/masters/1/unlock", url.Values{"confirm_name": {"db-replika"}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Nama server tidak cocok")
	wantCalls(t, f)

	rec = e.do("POST", "/masters/1/unlock", url.Values{"confirm_name": {"db-master"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1?pesan=kunci-dibuka")
	wantCalls(t, f, "unlock-master 1")
}

func TestDeleteRefusalMessageShown(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, `action="/servers/1/delete"`, `x-data="confirmName"`, `data-name="db-master"`, `name="confirm_name"`)

	rec = e.do("POST", "/servers/1/delete", url.Values{"confirm_name": {"db-replika"}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Nama server tidak cocok")
	wantCalls(t, f)

	// The orchestrator's refusals are shown on the server page.
	for _, refusal := range []error{orchestrator.ErrServerIsMaster, orchestrator.ErrServerIsReplica,
		orchestrator.ErrLocalServer, fmt.Errorf("server ini %w", orchestrator.ErrBusy)} {
		f.err = refusal
		rec = e.do("POST", "/servers/1/delete", url.Values{"confirm_name": {"db-master"}}, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, template.HTMLEscapeString(refusal.Error()), "db-master")
	}
	// Any other failure is the panel's: a generic 500, its text only in
	// the log.
	f.err = fmt.Errorf("delete server: %w", errors.New("database is locked (SQLITE_BUSY)"))
	rec = e.do("POST", "/servers/1/delete", url.Values{"confirm_name": {"db-master"}}, c)
	wantCode(t, rec, http.StatusInternalServerError)
	wantBody(t, rec, "Terjadi kesalahan pada panel")
	wantNoBody(t, rec, "SQLITE_BUSY")

	if _, err := e.st.GetServer(1); err != nil {
		t.Fatalf("server gone after a refused delete: %v", err)
	}
	wantCalls(t, f, "delete-server 1", "delete-server 1", "delete-server 1", "delete-server 1", "delete-server 1")
}

func TestSnoozeHours(t *testing.T) {
	e, _, mon := controlEnv(t)
	c := e.cookie()
	for _, p := range []string{"/servers/2", "/replicas/2", "/masters/1"} {
		rec := e.do("GET", p, nil, c)
		wantBody(t, rec, "Periksa sekarang", "Jangan ganggu", `name="hours"`)
	}

	before := time.Now()
	rec := e.do("POST", "/servers/2/snooze", url.Values{"hours": {"8"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?pesan=senyap")
	rec = e.do("POST", "/servers/1/snooze", url.Values{"hours": {"0"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1?pesan=senyap-batal")
	for _, h := range []string{"", "3", "-1", "abc", "1000"} {
		wantCode(t, e.do("POST", "/servers/2/snooze", url.Values{"hours": {h}}, c), http.StatusBadRequest)
	}
	mon.mu.Lock()
	snoozes := append([]snoozeCall(nil), mon.snoozes...)
	mon.mu.Unlock()
	if len(snoozes) != 2 {
		t.Fatalf("snoozes = %+v, want 2", snoozes)
	}
	if s := snoozes[0]; s.id != 2 || s.until == nil ||
		s.until.Before(before.Add(8*time.Hour)) || s.until.After(time.Now().Add(8*time.Hour)) {
		t.Errorf("snooze 8h = %+v", s)
	}
	if s := snoozes[1]; s.id != 1 || s.until != nil {
		t.Errorf("snooze cancel = %+v", s)
	}

	// The server page names an active snooze.
	until := time.Now().Add(2 * time.Hour)
	if err := e.st.SetSnooze(2, &until); err != nil {
		t.Fatal(err)
	}
	wantBody(t, e.do("GET", "/servers/2", nil, c), "Jangan ganggu sampai")

	// Periksa sekarang: the monitor's refusal is shown.
	rec = e.do("POST", "/servers/2/check", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/2?pesan=diperiksa")
	mon.err = monitor.ErrCheckTooSoon
	rec = e.do("POST", "/servers/2/check", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, monitor.ErrCheckTooSoon.Error(), "Replika db-replika")
	rec = e.do("POST", "/servers/1/check", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, monitor.ErrCheckTooSoon.Error(), "Master db-master")
	mon.mu.Lock()
	checks := append([]int64(nil), mon.checks...)
	mon.mu.Unlock()
	if !reflect.DeepEqual(checks, []int64{2, 2, 1}) {
		t.Errorf("checks = %v", checks)
	}
}

// newRoutes are the posts this task adds, with a form that passes their
// checks.
var newRoutes = []struct {
	path string
	form url.Values
}{
	{"/replicas/2/stop", noForm},
	{"/replicas/2/start", noForm},
	{"/replicas/2/relock", noForm},
	{"/replicas/2/resync/prepare", noForm},
	{"/replicas/2/resync", url.Values{"confirm_name": {"db-replika"}}},
	{"/replicas/2/detach", url.Values{"confirm_name": {"db-replika"}, "mode": {"keep"}}},
	{"/masters/1/unlock", url.Values{"confirm_name": {"db-master"}}},
	{"/servers/2/check", noForm},
	{"/servers/2/snooze", url.Values{"hours": {"1"}}},
	{"/servers/2/delete", url.Values{"confirm_name": {"db-replika"}}},
}

func TestCSRFOnNewRoutes(t *testing.T) {
	e, f, mon := controlEnv(t)
	for _, p := range newRoutes {
		for _, origin := range []string{"", "http://evil.example"} {
			req := httptest.NewRequest("POST", p.path, strings.NewReader(p.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			req.AddCookie(e.cookie())
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s origin=%q: status %d, want 403", p.path, origin, rec.Code)
			}
		}
		// Without a session: the login page, nothing done.
		rec := e.do("POST", p.path, p.form, nil)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("POST %s without login: status %d, Location %q", p.path, rec.Code, rec.Header().Get("Location"))
		}
	}
	wantCalls(t, f)
	if len(mon.checks) != 0 || len(mon.snoozes) != 0 {
		t.Errorf("monitor called: checks %v, snoozes %v", mon.checks, mon.snoozes)
	}
	if got, _ := e.st.ListAudit(0, time.Time{}, 10); len(got) != 0 {
		t.Errorf("audit entries for refused posts: %+v", got)
	}
}

func TestControlPagesHaveNoInlineScript(t *testing.T) {
	e, _, mon := controlEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, Status: monitor.Status{Code: monitor.StatusLocked},
		Snap: &proto.HealthSnapshot{BinlogExpireSec: 86400}})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, Status: monitor.Status{LockLost: true}})
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>[^<]|\son[a-z]+=|javascript:|\sstyle=`)
	for _, p := range []string{"/servers/1", "/servers/2", "/masters/1", "/replicas/2", "/replicas/2?buka=resync"} {
		rec := e.do("GET", p, nil, c)
		wantCode(t, rec, http.StatusOK)
		if m := inline.FindString(rec.Body.String()); m != "" {
			t.Errorf("GET %s has inline script/handler/style %q", p, m)
		}
	}
}
