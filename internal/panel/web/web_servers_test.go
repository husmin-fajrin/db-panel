package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
		for _, origin := range []string{"", "http://evil.example", "null", plainOrigin} {
			req := httptest.NewRequest("POST", tlsTarget+path, strings.NewReader(form.Encode()))
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

// wantBackupTruncated follows a backup verdict judged from a scan the
// agent's entry limit cut short (Ruling R39).
const wantBackupTruncated = "Folder ini berisi lebih dari 10.000 berkas, jadi agent hanya memeriksa sebagian dan berkas " +
	"yang lebih baru bisa terlewat. Arahkan folder cadangan ke subfolder yang berisi lebih sedikit berkas."

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
		// The entry limit cut the scan short: the judgement stands and
		// the card says why it may be wrong.
		{1, monitor.View{ServerID: 1, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*h + 600, NewestSize: 5 << 20, Truncated: true})},
			"Berkas terbaru: 2 jam lalu, 5.0 MB. " + wantBackupTruncated},
		{1, monitor.View{ServerID: 1, ReceivedAt: now,
			Snap: snap(proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 30 * h, NewestSize: 5, Truncated: true})},
			"Berkas terbaru di folder cadangan master db-master berumur 30 jam (batas 24 jam). " + wantBackupTruncated},
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

// Texts T3 and T3b of plan 5b-3 (spec 5.11), word for word.
const (
	wantT3 = "Pasang ulang agent di %s? Agent yang sekarang tetap bekerja sampai agent baru terpasang. " +
		"Perintah pemasangannya ditampilkan sekali dan berlaku 15 menit."
	wantT3b = "Token pasang ulang aktif sampai pukul %s. Perintahnya hanya ditampilkan sekali; bila hilang, buat token baru."
)

// repairEnv is one paired, online server (db1, id 1) with a fake monitor,
// on a fake clock whose session store the env uses.
func repairEnv(t *testing.T) (*env, *fakeMonitor, *fakeClock, store.Server) {
	t.Helper()
	clock, s, mod := windowDeps()
	mon := &fakeMonitor{}
	e := newEnv(t, mod, func(d *web.Deps) { d.Monitor = mon })
	e.sessions = s
	return e, mon, clock, e.pairedOnline(t, "db1")
}

// TestRepairTokenNeedsConfirm (spec 5.1, R-182): a re-pairing token for a
// paired server needs a confirmation; without one the answer is 403 and
// no token exists. A token through the session's window will do.
func TestRepairTokenNeedsConfirm(t *testing.T) {
	e, _, clock, srv := repairEnv(t)
	c := e.cookie()
	for _, form := range []url.Values{{}, {"confirm_token": {"salah"}}} {
		rec := e.do("POST", "/servers/1/token", form, c)
		wantCode(t, rec, http.StatusForbidden)
		wantBody(t, rec, msgConfirmInvalid)
		wantNoBody(t, rec, "/agent/install")
		if _, found, err := e.st.ActivePairToken(srv.ID, clock.Now()); err != nil || found {
			t.Fatalf("token after a post without confirmation: %v, %v", found, err)
		}
	}
	// A token for another server does not do either.
	other := e.pairedOnline(t, "db2")
	form := url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", other.ID, c)}}
	wantCode(t, e.do("POST", "/servers/1/token", form, c), http.StatusForbidden)
	if _, found, _ := e.st.ActivePairToken(srv.ID, clock.Now()); found {
		t.Fatal("token after a post with another server's confirmation")
	}

	form = url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", srv.ID, c)}}
	wantCode(t, e.do("POST", "/servers/1/token", form, c), http.StatusOK)
	if _, found, err := e.st.ActivePairToken(srv.ID, clock.Now()); err != nil || !found {
		t.Fatalf("no token after a confirmed post: %v, %v", found, err)
	}
	var audited bool
	for _, en := range auditEntries(t, e.st) {
		if en.Action == "server_token" && en.TargetID == srv.ID && en.Outcome == "ok" && en.DetailJSON == `{"repair":true}` {
			audited = true
		}
	}
	if !audited {
		t.Errorf("no server_token entry for the re-pairing: %+v", auditEntries(t, e.st))
	}
}

// TestRepairCommandShownOnce (spec 5.1, S-6): the install command of a
// re-pairing is in the answer to the post that made it, its token lasts 15
// minutes, and the server page shows only T3b with the time it ends and a
// confirmed Buat token baru.
func TestRepairCommandShownOnce(t *testing.T) {
	e, _, clock, srv := repairEnv(t)
	c := e.cookie()
	issued := clock.Now()
	form := url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", srv.ID, c)}}
	rec := e.do("POST", "/servers/1/token", form, c)
	wantCode(t, rec, http.StatusOK)
	tok, found, err := e.st.ActivePairToken(srv.ID, clock.Now())
	if err != nil || !found {
		t.Fatalf("no active token: %v, %v", found, err)
	}
	// The panel keeps no plaintext of a re-pairing token: only this answer
	// has it.
	if tok.Token != "" {
		t.Fatal("the store keeps the re-pairing token's plaintext")
	}
	shown := shownToken(t, rec)
	if _, err := e.st.PeekPairToken(shown, clock.Now()); err != nil {
		t.Fatalf("the shown token is not the active one: %v", err)
	}
	wantBody(t, rec, "sha256sum -c")
	wantNoBody(t, rec, "Token pasang ulang aktif sampai pukul")
	if want := issued.Add(15 * time.Minute).UTC().Truncate(time.Second); !tok.ExpiresAt.Equal(want) {
		t.Errorf("re-pairing token expires %v, want %v (15 minutes)", tok.ExpiresAt, want)
	}

	rec = e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, "agent/install?token=", shown)
	wantBody(t, rec, fmt.Sprintf(wantT3b, issued.Add(15*time.Minute).Local().Format("15:04")),
		"Buat token baru", `action="/servers/1/token"`,
		`data-confirm-action="repair_token" data-confirm-kind="server" data-confirm-id="1" data-confirm-light="1"`)
	// Also on a page answered after another post.
	rec = e.do("POST", "/servers/1/check", url.Values{}, c)
	wantNoBody(t, rec, "agent/install?token=", shown)

	// A new token, also confirmed, replaces it and is shown once too.
	clock.Add(time.Minute)
	form = url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", srv.ID, c)}}
	rec = e.do("POST", "/servers/1/token", form, c)
	wantCode(t, rec, http.StatusOK)
	shown2 := shownToken(t, rec)
	if shown2 == shown {
		t.Fatal("token not replaced")
	}
	if _, err := e.st.PeekPairToken(shown, clock.Now()); err == nil {
		t.Fatal("the first token still works after a new one")
	}
	wantNoBody(t, e.do("GET", "/servers/1", nil, c), "agent/install?token=", shown2)

	// Once it has expired the page says nothing of it.
	clock.Add(15 * time.Minute)
	wantNoBody(t, e.do("GET", "/servers/1", nil, c), "Token pasang ulang aktif sampai pukul", "agent/install?token=")
}

// TestServerListShowsNoRepairCommand (spec 5.1): the server list never
// rebuilds a re-pairing command, also for an agent that needs upgrading;
// while the token is active it shows T3b.
func TestServerListShowsNoRepairCommand(t *testing.T) {
	e, mon, clock, srv := repairEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: srv.ID, NeedsUpgrade: true})
	form := url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", srv.ID, c)}}
	rec := e.do("POST", "/servers/1/token", form, c)
	wantCode(t, rec, http.StatusOK)
	shown := shownToken(t, rec)
	tok, _, _ := e.st.ActivePairToken(srv.ID, clock.Now())

	rec = e.do("GET", "/servers", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, "agent/install", shown)
	wantBody(t, rec, "perlu dimutakhirkan",
		fmt.Sprintf(wantT3b, tok.ExpiresAt.Local().Format("15:04")))
}

// TestUnpairedTokenStillPasswordless (spec 5.1): an unpaired server's
// token needs no confirmation, lasts 24 hours and its command is on the
// server page.
func TestUnpairedTokenStillPasswordless(t *testing.T) {
	clock, s, mod := windowDeps()
	e := newEnv(t, mod)
	e.sessions = s
	srv, err := e.st.CreateServer("db-baru")
	if err != nil {
		t.Fatal(err)
	}
	c := e.cookie()
	clock.Add(11 * time.Minute) // the session's window is closed: it does not matter
	rec := e.do("POST", "/servers/1/token", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if l := rec.Header().Get("Location"); l != "/servers/1" {
		t.Errorf("Location = %q", l)
	}
	tok, found, err := e.st.ActivePairToken(srv.ID, clock.Now())
	if err != nil || !found {
		t.Fatalf("no token: %v, %v", found, err)
	}
	if want := clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second); !tok.ExpiresAt.Equal(want) {
		t.Errorf("token expires %v, want %v (24 hours)", tok.ExpiresAt, want)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "agent/install?token="+tok.Token, `action="/servers/1/token"`, "Buat token baru")
	wantNoBody(t, rec, `data-confirm-action="repair_token"`, "Token pasang ulang aktif")
	wantBody(t, e.do("GET", "/servers/1", nil, c), "agent/install?token="+tok.Token)
}

// pairOnce makes the server paired the first time a handler is about to
// create its pairing token (once only), as an agent pairing at that moment
// would.
func pairOnce(t *testing.T, e *env) {
	t.Helper()
	done := false
	t.Cleanup(web.SetBeforeCreatePairToken(func(id int64) {
		if done {
			return
		}
		done = true
		if err := e.st.MarkPaired(id, "serial-baru", "10.0.0.5"); err != nil {
			t.Errorf("mark paired: %v", err)
		}
	}))
}

// wantNoTokenLongerThan fails if server id has an active token that
// lasts more than d from now.
func wantNoTokenLongerThan(t *testing.T, e *env, id int64, now time.Time, d time.Duration) {
	t.Helper()
	tok, found, err := e.st.ActivePairToken(id, now)
	if err != nil {
		t.Fatal(err)
	}
	if found && tok.ExpiresAt.After(now.Add(d)) {
		t.Fatalf("paired server has a token until %v (more than %v)", tok.ExpiresAt, d)
	}
}

// TestServerPageRacedByPairing (review I1): the server is paired between
// the page's read of it and the creation of its token. The page shows no
// install command and leaves no 24-hour token for the paired server.
func TestServerPageRacedByPairing(t *testing.T) {
	clock, s, mod := windowDeps()
	e := newEnv(t, mod)
	e.sessions = s
	if _, err := e.st.CreateServer("db-baru"); err != nil {
		t.Fatal(err)
	}
	pairOnce(t, e)
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, "agent/install?token=")
	wantNoTokenLongerThan(t, e, 1, clock.Now(), 15*time.Minute)
	// Nor does a later page.
	wantNoBody(t, e.do("GET", "/servers/1", nil, e.cookie()), "agent/install?token=")
}

// TestNewTokenRacedByPairing (review I1): the server is paired between
// the post's read of it and the creation of the token. A post without a
// confirmation is then refused as for any paired server, and no token
// exists.
func TestNewTokenRacedByPairing(t *testing.T) {
	clock, s, mod := windowDeps()
	e := newEnv(t, mod)
	e.sessions = s
	if _, err := e.st.CreateServer("db-baru"); err != nil {
		t.Fatal(err)
	}
	pairOnce(t, e)
	rec := e.do("POST", "/servers/1/token", url.Values{}, e.cookie())
	wantCode(t, rec, http.StatusForbidden)
	wantNoBody(t, rec, "agent/install?token=")
	if _, found, err := e.st.ActivePairToken(1, clock.Now()); err != nil || found {
		t.Fatalf("token after the raced post: %v, %v", found, err)
	}
}

// TestRepairTokenRacedByUnpairing: the server is unpaired between a
// confirmed re-pairing post's read and the creation: it gets the token of
// an unpaired server (24 hours, its page shows the command).
func TestRepairTokenRacedByUnpairing(t *testing.T) {
	e, _, clock, srv := repairEnv(t)
	c := e.cookie()
	done := false
	t.Cleanup(web.SetBeforeCreatePairToken(func(id int64) {
		if !done {
			done = true
			if err := e.st.MarkUnpaired(id, "10.0.0.5"); err != nil {
				t.Errorf("mark unpaired: %v", err)
			}
		}
	}))
	form := url.Values{"confirm_token": {windowToken(t, e, web.ActRepairToken, "server", srv.ID, c)}}
	rec := e.do("POST", "/servers/1/token", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	tok, found, err := e.st.ActivePairToken(srv.ID, clock.Now())
	if err != nil || !found || !tok.ExpiresAt.Equal(clock.Now().Add(24*time.Hour).UTC().Truncate(time.Second)) {
		t.Fatalf("token after the raced post: %+v %v %v", tok, found, err)
	}
	wantBody(t, e.do("GET", "/servers/1", nil, c), "agent/install?token="+tok.Token)
}

// shownToken is the pairing token in the install command of rec's page.
func shownToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := regexp.MustCompile(`agent/install\?token=([0-9A-Z-]+)`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no install command in the page:\n%.3000s", rec.Body)
	}
	return m[1]
}
