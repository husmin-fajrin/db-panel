// Package web serves the panel's browser UI: login, dashboard, server pages
// and the SSE stream that keeps them live. Templates and static assets come
// from the embedded github.com/husmin-fajrin/db-panel/web package.
package web

import (
	"bytes"
	"fmt"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	webfs "github.com/husmin-fajrin/db-panel/web"
)

// Page holds the fields the layout needs; every page's data embeds it.
type Page struct {
	Username string // logged-in user; empty hides the sidebar
	Version  string
	Flash    string // error message shown above the page content
	Notice   string // success message shown above the page content
	// AlertCount is the number of open alerts, on the sidebar's Peringatan
	// link.
	AlertCount int
	// Instansi is the organisation's name under the sidebar's logo
	// (setting nama_instansi); empty hides it.
	Instansi string
	// ThemeDefault is the theme a browser starts with before its user
	// picks one (setting tema_bawaan): "sistem", "gelap" or "terang".
	// web/static/theme.js reads it from <html data-tema-bawaan>.
	ThemeDefault string
	// Active is the sidebar entry of the current page (menuDashboard, ...);
	// "" marks none.
	Active string
	// ServersTotal is the number of registered servers and
	// ServersConnected how many of them have a paired agent that is
	// connected and reporting, for the sidebar's status box.
	ServersTotal, ServersConnected int
	// CertNotice shows that the panel's certificate to install changed.
	CertNotice bool
	// CertFallback shows that the uploaded certificate is unusable and the
	// panel serves its own.
	CertFallback bool
	// CertIPURL is the certificate page at the panel's IP over plain HTTP
	// (HSTS never applies to an IP), for a browser that refuses the panel's
	// name; "" when the certificate names no IP.
	CertIPURL string
}

// Sidebar entries, the values of Page.Active.
const (
	menuDashboard = "dashboard"
	menuServers   = "servers"
	menuReplikasi = "replikasi"
	menuJobs      = "jobs"
	menuAlerts    = "alerts"
	menuAudit     = "audit"
	menuSettings  = "settings"
)

// Themes a browser can start with (setting tema_bawaan); temaSistem
// follows the operating system and is the default.
const (
	temaSistem = "sistem"
	temaGelap  = "gelap"
	temaTerang = "terang"
)

// validTema reports whether v is a tema_bawaan value.
func validTema(v string) bool { return v == temaSistem || v == temaGelap || v == temaTerang }

// activeMenu is the sidebar entry of the page at path.
func activeMenu(path string) string {
	under := func(prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }
	switch {
	case path == "/":
		return menuDashboard
	case under("/servers"):
		return menuServers
	case under("/replikasi"), under("/masters"), under("/replicas"):
		return menuReplikasi
	case under("/jobs"):
		return menuJobs
	case under("/alerts"):
		return menuAlerts
	case under("/audit"):
		return menuAudit
	case under("/settings"):
		return menuSettings
	}
	return ""
}

// MenuAttr marks the sidebar entry key as the current page: the
// menu-active class and aria-current; nothing for the other entries.
func (p Page) MenuAttr(key string) template.HTMLAttr {
	if key == p.Active {
		return `class="menu-active" aria-current="page"`
	}
	return ""
}

// AllConnected reports whether every registered server's agent is
// connected (and there is at least one server).
func (p Page) AllConnected() bool { return p.ServersTotal > 0 && p.ServersConnected >= p.ServersTotal }

// Banner is a message box above or inside a page (template "banner"):
// Tone is info, success, warning or error. Kind, when set, picks a richer
// body defined in the template with its typed fields, so every value goes
// through html/template's escaping (SEC-82): BannerCutover (Name, ServerID:
// the new master) or BannerPartial (Message, JobID: the promote job, or
// empty for a link to the alerts). Otherwise Text is shown.
type Banner struct {
	Tone     string
	Text     string
	Kind     string
	Name     string
	ServerID int64
	Message  string
	JobID    string
}

// Banner kinds with their own body in the "banner" template.
const (
	BannerCutover = "cutover"
	BannerPartial = "partial"
)

// pageNames are the page templates; each file templates/<name>.html defines
// "title" and "content".
var pageNames = []string{"login", "dashboard", "servers", "server_new", "server_detail",
	"master_detail", "replication", "replica_new", "replica_detail", "replica_databases", "jobs", "job_detail",
	"alerts", "settings", "audit", "cert", "promote", "cutover"}

// Renderer renders a page inside the shared layout.
type Renderer struct {
	pages map[string]*template.Template
	// fragments holds the shared partials (status, timeline) rendered on
	// their own, without the layout.
	fragments *template.Template
	logger    *slog.Logger
}

var templateFuncs = template.FuncMap{
	"fmtTime": fmtTime,
	// fmtDateTime is the audit page's date: dd-mm-yyyy HH:MM.
	"fmtDateTime": fmtDateTime,
	"fmtMB":       fmtMB,
	"fmtBytes":    fmtBytes,
	"sub":         func(a, b int64) int64 { return a - b },
	"statusLabel": statusLabel,
	"statusClass": statusClass,
	"kindLabel":   kindLabel,
	// Monitoring.
	"severityClass": severityClass,
	"bannerTone":    bannerTone,
	"fmtDuration":   fmtDuration,
	"fmtPct":        fmtPct,
	"ago":           ago,
	// banner is the data of the "banner" partial: banner "error" .Flash.
	"banner": func(tone, text string) Banner { return Banner{Tone: tone, Text: text} },
	// bytesOf is a size in bytes from the agent (int64) for fmtBytes.
	"bytesOf": func(n int64) uint64 { return uint64(max(n, 0)) },
	// cancelAttemptText is the sentence of "Batalkan percobaan bangun
	// ulang" (G-2).
	"cancelAttemptText": func() string { return msgCancelAttempt },
	// copyStartedText and attemptPendingText replace Periksa and Amankan
	// data tertinggal in the Master lama box (G-3).
	"copyStartedText":    func() string { return orchestrator.CopyStartedText },
	"attemptPendingText": func() string { return msgAttemptPending },
}

// partials are the templates shared by several pages and rendered on their
// own by the fragment endpoint.
var partials = []string{"templates/layout.html", "templates/_ui.html", "templates/_gauge.html",
	"templates/_chart.html", "templates/_status.html", "templates/_timeline.html",
	"templates/_confirm.html", "templates/_connections.html", "templates/_wipe.html"}

// NewRenderer parses the layout and the shared partials together with each
// page into its own template set, plus one set of the partials alone for
// fragments.
func NewRenderer() (*Renderer, error) {
	base, err := template.New("").Funcs(templateFuncs).Funcs(uiFuncs).ParseFS(webfs.FS, partials...)
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}
	frag, err := base.Clone()
	if err != nil {
		return nil, err
	}
	r := &Renderer{pages: make(map[string]*template.Template, len(pageNames)), fragments: frag, logger: slog.Default()}
	for _, name := range pageNames {
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(webfs.FS, "templates/"+name+".html"); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		r.pages[name] = t
	}
	return r, nil
}

// Render writes page name with HTTP 200.
func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
	r.renderStatus(w, http.StatusOK, name, data)
}

// renderStatus executes into a buffer first so a template error becomes a
// clean 500 instead of a half-written page.
func (r *Renderer) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	t, ok := r.pages[name]
	if !ok {
		r.logger.Error("unknown page template", "page", name)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		r.logger.Error("render page", "page", name, "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// RenderFragment writes the partial template name alone (no layout) with
// HTTP 200, for pages that replace part of themselves.
func (r *Renderer) RenderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := r.fragments.ExecuteTemplate(&buf, name, data); err != nil {
		r.logger.Error("render fragment", "fragment", name, "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}

// fmtTime formats a time (or *time.Time) in the panel host's local zone;
// zero or nil shows "-".
func fmtTime(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x != nil {
			t = *x
		}
	}
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// fmtDateTime formats a time (or *time.Time) as dd-mm-yyyy HH:MM in the
// panel host's local zone (design spec 5.6, 5.8 #5); zero or nil shows "-".
func fmtDateTime(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x != nil {
			t = *x
		}
	}
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("02-01-2006 15:04")
}

// fmtMB shows a size in MB as "x.y GB" from 1024 MB upwards, else "n MB".
func fmtMB(mb int64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

// fmtBytes shows a size in bytes as GB (from 1 GiB), MB (from 1 MiB) or KB.
func fmtBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (b+1023)/1024)
}

// severityClass is the badge class (.lencana in app.css) of a monitoring
// severity; web/src/app.css lists it in @source inline.
func severityClass(s monitor.Severity) string {
	switch s {
	case monitor.SevOK:
		return "lencana-success"
	case monitor.SevWarn:
		return "lencana-warning"
	case monitor.SevCrit:
		return "lencana-error"
	}
	return "lencana-neutral"
}

// bannerTone is the banner tone of a status's explanation (Penyebab/Saran,
// template "status-full"): pink only for a critical status (design spec
// 5.1); a warning stays amber and an expected state (an old master in
// quarantine, neutral) is information.
func bannerTone(s monitor.Severity) string {
	switch s {
	case monitor.SevCrit:
		return "error"
	case monitor.SevWarn:
		return "warning"
	}
	return "info"
}

// fmtDuration words a number of seconds: "2 hari 3 jam", "3 jam 5 menit",
// "5 menit" or "40 dtk".
func fmtDuration(sec int64) string {
	sec = max(sec, 0)
	const minute, hour, day = 60, 3600, 86400
	switch {
	case sec >= day:
		return fmt.Sprintf("%d hari %d jam", sec/day, sec%day/hour)
	case sec >= hour:
		return fmt.Sprintf("%d jam %d menit", sec/hour, sec%hour/minute)
	case sec >= minute:
		return fmt.Sprintf("%d menit", sec/minute)
	}
	return fmt.Sprintf("%d dtk", sec)
}

// fmtPct words used out of total as a whole percentage ("71%"); "-" when
// total is 0.
func fmtPct(used, total uint64) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (used*100+total/2)/total)
}

// ago words how long before now t was: "12 dtk lalu", "3 menit lalu",
// "2 jam lalu", "4 hari lalu".
func ago(t, now time.Time) string {
	sec := int64(max(now.Sub(t), 0) / time.Second)
	switch {
	case sec >= 86400:
		return fmt.Sprintf("%d hari lalu", sec/86400)
	case sec >= 3600:
		return fmt.Sprintf("%d jam lalu", sec/3600)
	case sec >= 60:
		return fmt.Sprintf("%d menit lalu", sec/60)
	}
	return fmt.Sprintf("%d dtk lalu", sec)
}
