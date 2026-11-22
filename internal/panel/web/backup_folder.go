package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Texts of the backup card (plan 5b-2b, B3-B7), word for word.
const (
	msgBackupFolderInvalid = `Folder cadangan harus path absolut tanpa "..", misalnya /var/backups/mysql.`
	msgBackupFolderHidden  = "Folder di bawah %s tidak terlihat oleh layanan agent; pakai folder lain, misalnya /var/backups/mysql."
	msgBackupFolderRefused = "Folder %s tidak boleh dipakai sebagai folder cadangan."
	msgBackupMaxAge        = "Umur paling lama harus 1 sampai 8760 jam."
	backupNoReport         = "Belum ada laporan dari agent."
	backupNewestFile       = "Berkas terbaru: %s lalu, %s."
)

// The folder rule, the same as the agent's ValidBackupFolder
// (internal/agent/monitor): TestBackupFolderRulesMatchAgent holds the two
// to the same table.
const (
	maxBackupFolderLen = 1024
	minBackupMaxAge    = 1
	maxBackupMaxAge    = 8760
)

var (
	// hiddenBackupRoots: the agent's service does not see these trees as
	// the host has them (ProtectHome, PrivateTmp).
	hiddenBackupRoots = []string{"/home", "/root", "/run/user", "/tmp", "/var/tmp"}
	// refusedBackupRoots hold no backups.
	refusedBackupRoots = []string{"/proc", "/sys", "/dev"}
)

// validBackupFolder checks a backup folder as the agent will (it reads p
// only, never the file system) and, when it is refused, says why for the
// operator: B5 for "/" and a folder at or under /proc, /sys or /dev, B4
// for one at or under a tree the agent's service does not see, B3 for
// anything else refused (not absolute or not clean, over 1024 bytes, a
// control byte). The empty folder ("no backup here") is refused like the
// agent refuses it: the form handles it before the rule. The texts never
// hold p.
func validBackupFolder(p string) (text string, ok bool) {
	if p == "/" {
		return fmt.Sprintf(msgBackupFolderRefused, "/"), false
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || len(p) > maxBackupFolderLen {
		return msgBackupFolderInvalid, false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return msgBackupFolderInvalid, false
		}
	}
	under := func(root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
	for _, root := range refusedBackupRoots {
		if under(root) {
			return fmt.Sprintf(msgBackupFolderRefused, root), false
		}
	}
	for _, root := range hiddenBackupRoots {
		if under(root) {
			return fmt.Sprintf(msgBackupFolderHidden, root), false
		}
	}
	return "", true
}

// trimBackupFolder is the folder as the form stores it (Ruling R20):
// without surrounding white space and trailing slashes ("/" stays "/").
// Nothing else is changed: "/a//b" still meets the rule as typed.
func trimBackupFolder(s string) string {
	s = strings.TrimSpace(s)
	for len(s) > 1 && strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}

// shownBackupFolder is a folder as a page may show it: each control
// character and each byte that is not UTF-8 replaced by U+FFFD, so what
// an operator typed is never echoed with its control bytes.
func shownBackupFolder(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

// backupCard is the server page's backup card (B1-B7): the form's values
// and the folder's state now.
type backupCard struct {
	Dir, MaxAgeHours string
	// State is the state line ("" with no folder set); Problem marks it as
	// a problem (a G8 text).
	State   string
	Problem bool
}

// backupMaxAge is srv's limit as the monitor judges it: the default for a
// row without one.
func backupMaxAge(srv store.Server) int {
	if srv.BackupMaxAgeHours <= 0 {
		return monitor.DefaultBackupMaxAgeHours
	}
	return srv.BackupMaxAgeHours
}

// newBackupCard is srv's backup card from its stored folder and the
// monitor's view v: no state line without a folder; "no report yet"
// until the agent reports the folder; the newest file's age and size
// when it is fine; else the monitor's words for the problem, with
// "master <name>" on a master and "server <name>" on any other server
// (Ruling R19). A negative age or size from the agent shows as 0.
func (a *app) newBackupCard(srv store.Server, v monitor.View) backupCard {
	maxAge := backupMaxAge(srv)
	c := backupCard{Dir: shownBackupFolder(srv.BackupDir), MaxAgeHours: strconv.Itoa(maxAge)}
	b := monitor.JudgeBackup(v, srv, a.Now())
	switch b.Status {
	case monitor.BackupStatusUnset:
	case monitor.BackupStatusUnknown:
		c.State = backupNoReport
	case monitor.BackupStatusOK:
		c.State = fmt.Sprintf(backupNewestFile, monitor.BackupAge(max(b.AgeSec, 0)), fmtBytes(uint64(max(b.Size, 0))))
	default:
		if srv.Role == store.RoleMaster {
			c.State = monitor.BackupMessage(b, srv.Name, maxAge)
		} else {
			c.State = monitor.BackupMessageAs(b, "server "+srv.Name, maxAge)
		}
		c.Problem = true
	}
	return c
}

// saveServerBackup stores the server's backup folder and its age limit
// (POST /servers/{id}/backup), then has the monitor send it to the agent.
// An empty folder means the server keeps no backup here; the limit is
// checked all the same. A refused form stores nothing and shows the page
// again with the text and the input.
func (a *app) saveServerBackup(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	dir := trimBackupFolder(r.PostFormValue("backup_dir"))
	rawAge := strings.TrimSpace(r.PostFormValue("backup_max_age_hours"))
	msg := ""
	if dir != "" {
		msg, _ = validBackupFolder(dir)
	}
	maxAge, err := strconv.Atoi(rawAge)
	if msg == "" && (err != nil || maxAge < minBackupMaxAge || maxAge > maxBackupMaxAge) {
		msg = msgBackupMaxAge
	}
	if msg != "" {
		a.audit(r, "server_backup", auditServer, srv.ID,
			map[string]any{"backup_dir": dir, "backup_max_age_hours": rawAge}, msg)
		form := backupCard{Dir: shownBackupFolder(dir), MaxAgeHours: shownBackupFolder(rawAge)}
		a.renderServerWith(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msg}, &form)
		return
	}
	err = a.Store.SetServerBackup(srv.ID, dir, maxAge)
	a.audit(r, "server_backup", auditServer, srv.ID,
		map[string]any{"backup_dir": dir, "backup_max_age_hours": maxAge}, a.outcomeOf(err))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return
	case err != nil:
		a.internalError(w, "save backup folder", err)
		return
	}
	// In the background; an agent offline now gets the folder when it
	// connects again.
	a.Monitor.ConfigureServer(r.Context(), srv.ID)
	a.Logger.Info("backup folder saved", "id", srv.ID, "max_age_hours", maxAge, "by", auth.UsernameFrom(r.Context()))
	notice := "cadangan"
	if dir == "" {
		notice = "cadangan-kosong"
	}
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan="+notice, http.StatusSeeOther)
}
