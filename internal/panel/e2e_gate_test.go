package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Texts of plan 5b-2b the end-to-end gate tests look for, as the panel
// writes them for the master of the e2e topologies (master-e2e).
const (
	// G8d with the reason "missing".
	e2eG8dMissing = "Folder cadangan master master-e2e tidak bisa dibaca agent: folder itu tidak ada."
	// G8u.
	e2eG8u = "Folder cadangan master master-e2e belum diisi, jadi panel tidak bisa melihat umur cadangan."
	// G8b.
	e2eG8b = "Berkas terbaru di folder cadangan master master-e2e kosong (0 byte)."
	// G14 naming the backup row alone.
	e2eG14Backup = "Tindakan ini ditolak karena ada prasyarat yang belum terpenuhi: Cadangan (backup). Cara " +
		"memperbaikinya ada di daftar Prasyarat di halaman ini."
	// G15 and D7.
	e2eG15 = `Centang "Saya sudah memastikan ada cadangan (backup) terbaru" untuk melanjutkan.`
	e2eD7  = `Centang "Saya sudah memastikan master lama mati atau terputus dari aplikasi" untuk melanjutkan promote darurat.`
	// D6.
	e2eD6 = "Baris yang belum terpenuhi tidak menghalangi promote darurat."
)

// e2eMissingBackupDir is a backup folder that the panel's and the agent's
// rule accept and that does not exist.
const e2eMissingBackupDir = "/nonexistent-dbpanel-e2e"

// gateButtonRe is the promote screen's submit button.
var gateButtonRe = regexp.MustCompile(`<button type="submit"[^>]*data-gate-button[^>]*>`)

// promoteButtonDisabled reports whether the promote screen has its button
// and the gate turned it off.
func promoteButtonDisabled(t *testing.T, page string) bool {
	t.Helper()
	b := gateButtonRe.FindString(page)
	if b == "" {
		t.Fatalf("promote screen has no gated button:\n%s", textOf(page))
	}
	return strings.Contains(b, " disabled")
}

// setBackupFolder stores the backup folder of the server through the
// store and has the monitor send it to the agent, as the backup form does
// after its checks.
func setBackupFolder(t *testing.T, p *Panel, serverID int64, dir string) {
	t.Helper()
	if err := p.Store.SetServerBackup(serverID, dir, healthmon.DefaultBackupMaxAgeHours); err != nil {
		t.Fatal(err)
	}
	p.Monitor.ConfigureServer(context.Background(), serverID)
}

// waitBackupReport asks the agent of the server for fresh reports ("Periksa
// sekarang", at most one per 10 s) until the backup state of one of them
// meets cond; only a report taken here is judged (a folder change makes
// the panel forget the last report's backup state at once, before the
// agent has the new folder). The agent scans a folder it was just sent at
// its next check (10 s here); an unchanged folder only every 5 minutes,
// which no test waits for.
func waitBackupReport(t *testing.T, p *Panel, serverID int64, what string, cond func(*proto.BackupState) bool) {
	t.Helper()
	var last *proto.BackupState
	waitForWithin(t, 3*e2eWait, what, func() bool {
		err := p.Monitor.CheckNow(context.Background(), serverID)
		if errors.Is(err, healthmon.ErrCheckTooSoon) {
			time.Sleep(time.Second)
			return false
		}
		if err != nil {
			t.Fatalf("check now of server %d: %v", serverID, err)
		}
		v, ok := p.Monitor.View(serverID)
		if !ok || v.Snap == nil {
			return false
		}
		last = v.Snap.Backup
		return cond(last)
	})
	t.Logf("%s: %+v", what, last)
}

// missingBackup: the agent reports the folder unreadable because it does
// not exist.
func missingBackup(b *proto.BackupState) bool {
	return b != nil && b.Status == proto.BackupUnreadable && b.Reason == proto.BackupReasonMissing
}

// promoteAuditOf is the detail of the audit entry of the promote that
// started job id.
func promoteAuditOf(t *testing.T, p *Panel, id string) string {
	t.Helper()
	entries, err := p.Store.ListAudit(0, time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range entries {
		if a.Action == "promote" && strings.Contains(a.DetailJSON, `"job_id":"`+id+`"`) {
			return a.DetailJSON
		}
	}
	t.Fatalf("no audit entry of the promote %s", id)
	return ""
}

// wantNoJob fails if a job runs on the server.
func wantNoJob(t *testing.T, p *Panel, serverID int64, after string) {
	t.Helper()
	if j, busy, err := p.Store.ActiveJobForServer(serverID); err != nil || busy {
		t.Fatalf("%s started job %+v (%v)", after, j, err)
	}
}

// TestE2EGateRefusesPlannedPromoteThenAllows (plan 5b-2b): the master's
// backup folder is set to one that does not exist; its agent reports it
// unreadable, the promote screen shows G8d with the button off, a planned
// promote posted with a valid token is refused with 409 and G14 without
// using the token, and backup_stale opens. Once the folder is cleared the
// row is yellow G8u: without the backup tick the post is refused with
// G15, with it the same token starts the promote, which completes, and
// its audit entry records backup_ack.
func TestE2EGateRefusesPlannedPromoteThenAllows(t *testing.T) {
	skipWithoutE2E(t)
	e := startE2EPromoteTopology(t)
	p, ui, m, ra := e.p, e.ui, e.master, e.ra
	path := fmt.Sprintf("/masters/%d/promote", m.srv())

	setBackupFolder(t, p, m.srv(), e2eMissingBackupDir)
	waitBackupReport(t, p, m.srv(), "the master's agent to report the backup folder missing", missingBackup)

	page := ui.get(path)
	if !strings.Contains(page, e2eG8dMissing) || !promoteButtonDisabled(t, page) {
		t.Fatalf("promote screen with the missing folder lacks G8d or keeps the button on:\n%s", textOf(page))
	}

	token := ui.confirmToken(web.ActPromote, "master", m.srv())
	form := url.Values{"candidate": {strconv.FormatInt(ra.srv(), 10)}, "mode": {proto.PromotePlanned},
		"confirm_token": {token}}
	status, body := ui.post(path, form)
	if status != http.StatusConflict || !strings.Contains(html.UnescapeString(body), e2eG14Backup) {
		t.Fatalf("planned promote with the backup row red = %d:\n%s", status, textOf(body))
	}
	wantNoJob(t, p, m.srv(), "a promote refused by the gate")

	var alert store.Alert
	waitForWithin(t, e2eWait, "backup_stale open on the master", func() bool {
		a, ok := openAlert(t, p, healthmon.KindBackupStale, m.srv())
		alert = a
		return ok
	})
	if alert.Message != e2eG8dMissing {
		t.Fatalf("backup_stale message = %q, want %q", alert.Message, e2eG8dMissing)
	}

	// The folder cleared: the row asks for the tick, the alert closes.
	setBackupFolder(t, p, m.srv(), "")
	waitForWithin(t, e2eWait, "backup_stale closed once the folder is cleared", func() bool {
		_, ok := openAlert(t, p, healthmon.KindBackupStale, m.srv())
		return !ok
	})
	page = ui.get(path)
	if !strings.Contains(page, e2eG8u) || !strings.Contains(page, `name="backup_ack" value="1" required`) ||
		promoteButtonDisabled(t, page) {
		t.Fatalf("promote screen without a folder lacks G8u or the tick, or keeps the button off:\n%s", textOf(page))
	}

	// The old master's quarantine needs the candidate's host in its
	// replicas-seen file.
	e.waitReplicasSeen(t)
	status, body = ui.post(path, form)
	if status != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), e2eG15) {
		t.Fatalf("planned promote without the backup tick = %d:\n%s", status, textOf(body))
	}
	wantNoJob(t, p, m.srv(), "a promote without the backup tick")

	// The same token, now with the tick: neither refusal used it.
	form.Set("backup_ack", "1")
	id := ui.postJob(path, form)
	job := waitJob(t, p, id, e2ePromoteTimeout, nil)
	if job.Kind != orchestrator.KindPromotePlanned || job.Status != store.JobSucceeded {
		t.Fatalf("promote job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, p, id))
	}
	e.wantPromoted(t, ra, m, e.rb, 2)
	if d := promoteAuditOf(t, p, id); !strings.Contains(d, `"backup_ack":true`) {
		t.Fatalf("planned promote audit = %s; want backup_ack", d)
	}
}

// TestE2EEmergencyPromoteSkipsRedRows (plan 5b-2b): with the master's
// backup row red as above and its MySQL killed (its agent online), the
// emergency screen shows the row, D6 and the button on; a post without
// the D5 tick is refused with D7 and starts nothing; with it the
// emergency promote completes and its audit entry lists backup among the
// red rows it skipped.
func TestE2EEmergencyPromoteSkipsRedRows(t *testing.T) {
	skipWithoutE2E(t)
	e := startE2EPromoteTopology(t)
	p, ui, m, ra, rb := e.p, e.ui, e.master, e.ra, e.rb
	path := fmt.Sprintf("/masters/%d/promote", m.srv())

	setBackupFolder(t, p, m.srv(), e2eMissingBackupDir)
	waitBackupReport(t, p, m.srv(), "the master's agent to report the backup folder missing", missingBackup)
	// The old master's quarantine (MySQL down) needs the candidate's host
	// in its replicas-seen file.
	e.waitReplicasSeen(t)

	killMySQL(t, m.inst)
	waitForWithin(t, e2eWait, "the master's report of MySQL down", func() bool {
		v, ok := p.Monitor.View(m.srv())
		return ok && v.Snap != nil && !v.Snap.MySQLOK
	})
	if !p.Hub.IsOnline(m.srv()) {
		t.Fatal("the master's agent went offline with its MySQL")
	}
	for _, n := range []*e2eNode{ra, rb} {
		waitStatus(t, p, n.srv(), healthmon.StatusDisconnected)
	}
	// The scan does not need MySQL: the folder is still reported missing.
	waitBackupReport(t, p, m.srv(), "the backup folder still missing with MySQL down", missingBackup)

	page := ui.get(path)
	for _, want := range []string{"Darurat (master tidak terjangkau)", e2eD6, e2eG8dMissing} {
		if !strings.Contains(page, want) {
			t.Fatalf("emergency screen lacks %q:\n%s", want, textOf(page))
		}
	}
	if promoteButtonDisabled(t, page) {
		t.Fatalf("emergency screen turned its button off:\n%s", textOf(page))
	}

	// Without the D5 tick: refused with D7, nothing started, token kept.
	token := ui.confirmToken(web.ActPromote, "master", m.srv())
	form := url.Values{"candidate": {strconv.FormatInt(ra.srv(), 10)}, "mode": {proto.PromoteEmergency},
		"confirm_name": {m.name}, "confirm_token": {token}}
	status, body := ui.post(path, form)
	if status != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), e2eD7) {
		t.Fatalf("emergency promote without the D5 tick = %d:\n%s", status, textOf(body))
	}
	wantNoJob(t, p, m.srv(), "an emergency promote without the D5 tick")

	// With it, on the same token.
	form.Set("old_master_dead", "1")
	id := ui.postJob(path, form)
	e.wantEmergencyAudit(t, id)
	job := waitJob(t, p, id, e2ePromoteTimeout, nil)
	if job.Kind != orchestrator.KindPromoteEmergency || job.Status != store.JobSucceeded {
		t.Fatalf("promote job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, p, id))
	}
	e.wantPromoted(t, ra, m, rb, 2)
	var d struct {
		SkippedRed []string `json:"skipped_red"`
	}
	detail := promoteAuditOf(t, p, id)
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.SkippedRed, "backup") {
		t.Fatalf("emergency promote audit = %s; want backup in skipped_red", detail)
	}
	t.Logf("emergency promote skipped the red rows %v", d.SkippedRed)
}

// e2eBackupRoot is where TestE2EBackupFolderReported makes its backup
// folder: not under a tree the folder rule refuses (/tmp, /var/tmp, /home,
// /root, /run/user), so t.TempDir cannot be used.
const e2eBackupRoot = "/var/lib"

// newestFileRe is B7 with a backup file of 1000 bytes.
var newestFileRe = regexp.MustCompile(`Berkas terbaru: [0-9]+ menit lalu, 1 KB\.`)

// TestE2EBackupFolderReported (plan 5b-2b): a real folder set through the
// backup form, holding one file: the master's page shows B7 with the
// file's age and size once the agent scanned it. The file is then emptied
// (0 bytes); the next scan reports it and the page shows G8b. The agent
// scans an unchanged folder at most every 5 minutes; the test has the
// next scan happen now by saving the folder again (cleared, then set),
// as an operator can.
func TestE2EBackupFolderReported(t *testing.T) {
	skipWithoutE2E(t)
	dir, err := os.MkdirTemp(e2eBackupRoot, "dbpanel-e2e-backup-")
	if err != nil {
		t.Fatalf("backup folder under %s: %v", e2eBackupRoot, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	file := filepath.Join(dir, "cadangan-e2e.sql.gz")
	if err := os.WriteFile(file, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	e := startE2ETopology(t)
	p, ui := e.p, e.ui
	srvPath := serverPath(e.masterSrv)
	save := func(folder string) {
		t.Helper()
		form := url.Values{"backup_dir": {folder}, "backup_max_age_hours": {"48"}}
		want := srvPath + "?pesan=cadangan"
		if folder == "" {
			want += "-kosong"
		}
		if loc := ui.postRedirect(srvPath+"/backup", form); loc != want {
			t.Fatalf("backup form with %q redirected to %q, want %q", folder, loc, want)
		}
	}

	save(dir)
	waitBackupReport(t, p, e.masterSrv, "the agent to report the backup file", func(b *proto.BackupState) bool {
		return b != nil && b.Status == proto.BackupOK && b.NewestSize == 1000
	})
	page := ui.get(srvPath)
	if !newestFileRe.MatchString(page) || !strings.Contains(page, `value="`+dir+`"`) {
		t.Fatalf("server page lacks B7 for the backup file or the folder:\n%s", textOf(page))
	}
	if _, ok := openAlert(t, p, healthmon.KindBackupStale, e.masterSrv); ok {
		t.Fatal("backup_stale open on a fresh backup")
	}

	// The file emptied, then the folder saved again: cleared (the agent
	// reports no folder), then set, so the agent scans it at its next
	// check.
	if err := os.Truncate(file, 0); err != nil {
		t.Fatal(err)
	}
	save("")
	waitBackupReport(t, p, e.masterSrv, "the agent to report no backup folder", func(b *proto.BackupState) bool { return b == nil })
	save(dir)
	waitBackupReport(t, p, e.masterSrv, "the agent to report the emptied file", func(b *proto.BackupState) bool {
		return b != nil && b.Status == proto.BackupOK && b.NewestSize == 0
	})
	page = ui.get(srvPath)
	if !strings.Contains(page, e2eG8b) || newestFileRe.MatchString(page) {
		t.Fatalf("server page lacks G8b for the emptied file:\n%s", textOf(page))
	}
	waitForWithin(t, e2eWait, "backup_stale open on the emptied file", func() bool {
		a, ok := openAlert(t, p, healthmon.KindBackupStale, e.masterSrv)
		return ok && a.Message == e2eG8b
	})
}
