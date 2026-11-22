package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Texts of plan 5b-2b, word for word.
const (
	wantB2 = "Panel hanya melihat umur dan ukuran berkas terbaru di folder ini (paling dalam tiga tingkat), " +
		"bukan apakah cadangan itu bisa dipulihkan. Nama dan isi berkas tidak pernah dikirim ke panel. " +
		"Kosongkan folder bila server ini tidak menyimpan cadangan."
	wantB3 = `Folder cadangan harus path absolut tanpa &#34;..&#34;, misalnya /var/backups/mysql.`
	wantB4 = "Folder di bawah %s tidak terlihat oleh layanan agent; pakai folder lain, misalnya /var/backups/mysql."
	wantB5 = "Folder %s tidak boleh dipakai sebagai folder cadangan."
	wantB6 = "Umur paling lama harus 1 sampai 8760 jam."
)

// backupEnv is one paired, online server (db1) with a fake monitor.
func backupEnv(t *testing.T) (*env, *fakeMonitor, store.Server) {
	t.Helper()
	mon := &fakeMonitor{}
	e := newEnv(t, func(d *web.Deps) { d.Monitor = mon })
	return e, mon, e.pairedOnline(t, "db1")
}

func backupForm(dir, hours string) url.Values {
	return url.Values{"backup_dir": {dir}, "backup_max_age_hours": {hours}}
}

func wantBackupStored(t *testing.T, e *env, id int64, dir string, hours int) {
	t.Helper()
	srv, err := e.st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if srv.BackupDir != dir || srv.BackupMaxAgeHours != hours {
		t.Errorf("stored %q, %d h; want %q, %d h", srv.BackupDir, srv.BackupMaxAgeHours, dir, hours)
	}
}

func TestSaveServerBackup(t *testing.T) {
	e, mon, srv := backupEnv(t)
	c := e.cookie()

	// White space and trailing slashes go (Ruling R20).
	rec := e.do("POST", "/servers/1/backup", backupForm("  /var/backups/mysql/ ", "24"), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/1?pesan=cadangan")
	wantBackupStored(t, e, srv.ID, "/var/backups/mysql", 24)
	wantAuditDetail(t, e, "server_backup", `"backup_dir":"/var/backups/mysql"`, `"backup_max_age_hours":24`)
	if got := mon.configured(); len(got) != 1 || got[0] != srv.ID {
		t.Errorf("ConfigureServer calls = %v, want [%d]", got, srv.ID)
	}
	entries := auditEntries(t, e.st)
	last := entries[len(entries)-1]
	if last.Action != "server_backup" || last.Outcome != "ok" || last.Username != "admin" ||
		last.TargetKind != "server" || last.TargetID != srv.ID {
		t.Errorf("audit entry %+v", last)
	}
	wantBody(t, e.do("GET", "/servers/1?pesan=cadangan", nil, c), "Pengaturan cadangan disimpan.")

	// Refused: 400 with the text, the input kept, nothing stored, nothing
	// sent, the refusal audited without the value.
	refused := []struct {
		dir, hours, text string
	}{
		{"relatif", "24", wantB3},
		{"/a/../b", "24", wantB3},
		{"/a//b", "24", wantB3},
		{"/" + strings.Repeat("a", 1024), "24", wantB3},
		{"/home/x", "24", fmt.Sprintf(wantB4, "/home")},
		{"/run/user/1000", "24", fmt.Sprintf(wantB4, "/run/user")},
		{"/var/tmp/", "24", fmt.Sprintf(wantB4, "/var/tmp")},
		{"/var/backups/my\u0085sql", "24", wantB3},
		{"/", "24", fmt.Sprintf(wantB5, "/")},
		{"/proc/1", "24", fmt.Sprintf(wantB5, "/proc")},
		{"/srv/b", "0", wantB6},
		{"/srv/b", "8761", wantB6},
		{"/srv/b", "dua", wantB6},
		{"/srv/b", "", wantB6},
		{"", "0", wantB6}, // the age is checked with the folder empty too
	}
	for _, r := range refused {
		rec := e.do("POST", "/servers/1/backup", backupForm(r.dir, r.hours), c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, r.text, `name="backup_max_age_hours" value="`+r.hours+`"`)
		if kept := r.dir; kept != "" && len(kept) < 100 {
			if kept != "/" {
				kept = strings.TrimSuffix(kept, "/")
			}
			kept = strings.ReplaceAll(kept, "\u0085", "\uFFFD") // shown without the control character
			wantBody(t, rec, `name="backup_dir" value="`+kept+`"`)
		}
		wantBackupStored(t, e, srv.ID, "/var/backups/mysql", 24)
		entries := auditEntries(t, e.st)
		last := entries[len(entries)-1]
		if last.Action != "server_backup" || last.Outcome == "ok" || strings.Contains(last.DetailJSON, "/srv/b") {
			t.Errorf("%q, %q: audit entry %+v", r.dir, r.hours, last)
		}
	}
	if got := mon.configured(); len(got) != 1 {
		t.Errorf("ConfigureServer calls after refusals = %v", got)
	}

	// A control byte: B3, and the form shows a replacement character, not
	// the byte.
	rec = e.do("POST", "/servers/1/backup", backupForm("/var/backups/my\x01sql", "24"), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantB3, `value="/var/backups/my`+"\uFFFD"+`sql"`)
	wantNoBody(t, rec, "\x01")

	// The empty folder: no backup here, stored as "".
	rec = e.do("POST", "/servers/1/backup", backupForm("  ", "48"), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantBackupStored(t, e, srv.ID, "", 48)
	if got := mon.configured(); len(got) != 2 {
		t.Errorf("ConfigureServer calls = %v, want 2", got)
	}

	// An unknown server.
	wantCode(t, e.do("POST", "/servers/99/backup", backupForm("/srv/b", "24"), c), http.StatusNotFound)

	wantBody(t, e.do("GET", "/audit", nil, c), "Atur folder cadangan")
}

// The three new routes sit behind the login and the same-origin check
// like their neighbours.
func TestBackupRoutesNeedLoginAndSameOrigin(t *testing.T) {
	e, mon, srv := backupEnv(t)
	posts := map[string]url.Values{
		"/servers/1/backup":             backupForm("/srv/b", "24"),
		"/settings/panel-key-backed-up": {"marked": {"1"}},
		"/settings/audit-forwarded":     {"marked": {"1"}},
	}
	for path, form := range posts {
		rec := e.do("POST", path, form, nil)
		wantCode(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("%s without login: Location %q", path, loc)
		}
		for _, origin := range []string{"", "http://evil.example", "null"} {
			req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			req.AddCookie(e.cookie())
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s origin=%q: status %d", path, origin, rec.Code)
			}
		}
	}
	wantBackupStored(t, e, srv.ID, "", 48)
	if got := mon.configured(); len(got) != 0 {
		t.Errorf("ConfigureServer calls = %v", got)
	}
	kv, err := e.st.GetSettings(store.SettingPanelKeyBackedUpAt, store.SettingAuditForwardedAt)
	if err != nil {
		t.Fatal(err)
	}
	if kv[store.SettingPanelKeyBackedUpAt] != "" || kv[store.SettingAuditForwardedAt] != "" {
		t.Errorf("marks set: %v", kv)
	}
}

// backupCardOf is the backup card's HTML on a server page.
func backupCardOf(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, "data-backup-card")
	if !ok {
		t.Fatalf("no backup card; body:\n%s", body)
	}
	card, _, _ := strings.Cut(after, "</section>")
	return card
}

func TestServerPageShowsBackupState(t *testing.T) {
	e, _, mon := controlEnv(t) // db-master (1), db-replika (2)
	c := e.cookie()
	h := int64(3600)

	// Unset: the form with the default limit, no state line.
	card := backupCardOf(t, e.do("GET", "/servers/1", nil, c).Body.String())
	for _, s := range []string{"Cadangan (backup)", "Folder cadangan", "Umur paling lama (jam)", ">Simpan<", wantB2,
		`action="/servers/1/backup"`, `name="backup_dir" value=""`, `name="backup_max_age_hours" value="48"`} {
		if !strings.Contains(card, s) {
			t.Errorf("unset card lacks %q:\n%s", s, card)
		}
	}
	for _, s := range []string{"data-backup-state", "Belum ada laporan"} {
		if strings.Contains(card, s) {
			t.Errorf("unset card holds %q:\n%s", s, card)
		}
	}

	for _, id := range []int64{1, 2} {
		if err := e.st.SetServerBackup(id, "/var/backups/mysql", 24); err != nil {
			t.Fatal(err)
		}
	}
	snap := func(b proto.BackupState) *proto.HealthSnapshot { return &proto.HealthSnapshot{Backup: &b} }
	now := time.Now()
	cases := []struct {
		id   int64
		view monitor.View
		want string
	}{
		{1, monitor.View{ServerID: 1}, "Belum ada laporan dari agent."},
		{1, monitor.View{ServerID: 1, ReceivedAt: now, Snap: snap(proto.BackupState{Status: proto.BackupPending})},
			"Belum ada laporan dari agent."},
		{1, monitor.View{ServerID: 1, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*h + 600, NewestSize: 5 << 20})},
			"Berkas terbaru: 2 jam lalu, 5.0 MB."},
		{1, monitor.View{ServerID: 1, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: -500, NewestSize: 2048})},
			"Berkas terbaru: 0 menit lalu, 2 KB."},
		{1, monitor.View{ServerID: 1, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 30 * h, NewestSize: 5})},
			"Berkas terbaru di folder cadangan master db-master berumur 30 jam (batas 24 jam)."},
		{1, monitor.View{ServerID: 1, ReceivedAt: now, Snap: snap(proto.BackupState{Status: proto.BackupNone})},
			"Folder cadangan master db-master tidak berisi berkas."},
		{2, monitor.View{ServerID: 2, ReceivedAt: now, Snap: snap(proto.BackupState{Status: proto.BackupNone})},
			"Folder cadangan server db-replika tidak berisi berkas."},
		{2, monitor.View{ServerID: 2, ReceivedAt: now, Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: h})},
			"Berkas terbaru di folder cadangan server db-replika kosong (0 byte)."},
		{2, monitor.View{ServerID: 2, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupUnreadable, Reason: proto.BackupReasonDenied})},
			"Folder cadangan server db-replika tidak bisa dibaca agent: izin ditolak."},
	}
	for _, tc := range cases {
		mon.setView(tc.view)
		rec := e.do("GET", fmt.Sprintf("/servers/%d", tc.id), nil, c)
		wantCode(t, rec, http.StatusOK)
		card := backupCardOf(t, rec.Body.String())
		if !strings.Contains(card, tc.want) {
			t.Errorf("server %d, %+v: card lacks %q:\n%s", tc.id, tc.view.Snap, tc.want, card)
		}
		if !strings.Contains(card, `name="backup_dir" value="/var/backups/mysql"`) ||
			!strings.Contains(card, `name="backup_max_age_hours" value="24"`) {
			t.Errorf("server %d: form not filled:\n%s", tc.id, card)
		}
	}

	// A folder name is escaped like any text.
	if err := e.st.SetServerBackup(1, "/srv/<b>x", 24); err != nil {
		t.Fatal(err)
	}
	card = backupCardOf(t, e.do("GET", "/servers/1", nil, c).Body.String())
	if !strings.Contains(card, "/srv/&lt;b&gt;x") || strings.Contains(card, "<b>x") {
		t.Errorf("folder not escaped:\n%s", card)
	}

	// An unpaired server has no card.
	if _, err := e.st.CreateServer("db-baru"); err != nil {
		t.Fatal(err)
	}
	wantNoBody(t, e.do("GET", "/servers/3", nil, c), "data-backup-card")
}

// configured lists the servers ConfigureServer was called for.
func (m *fakeMonitor) configured() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.configures...)
}

// After a save the monitor has forgotten the previous folder's report
// (Ruling R22): the card says no report yet, not the old folder's state.
func TestBackupCardAfterSaveForgetsOldReport(t *testing.T) {
	e, mon, srv := backupEnv(t)
	c := e.cookie()
	if err := e.st.SetServerBackup(srv.ID, "/var/backups/lama", 24); err != nil {
		t.Fatal(err)
	}
	mon.setView(monitor.View{ServerID: srv.ID, ReceivedAt: time.Now(), Snap: &proto.HealthSnapshot{
		MySQLOK: true, Backup: &proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600, NewestSize: 4096}}})
	wantBody(t, e.do("GET", "/servers/1", nil, c), "Berkas terbaru: 1 jam lalu, 4 KB.")

	wantCode(t, e.do("POST", "/servers/1/backup", backupForm("/var/backups/baru", "24"), c), http.StatusSeeOther)
	card := backupCardOf(t, e.do("GET", "/servers/1?pesan=cadangan", nil, c).Body.String())
	if !strings.Contains(card, "Belum ada laporan dari agent.") || strings.Contains(card, "Berkas terbaru") {
		t.Errorf("card after the save:\n%s", card)
	}
	if v, _ := mon.View(srv.ID); v.Snap == nil || !v.Snap.MySQLOK {
		t.Errorf("the rest of the view was dropped: %+v", v.Snap)
	}
}
