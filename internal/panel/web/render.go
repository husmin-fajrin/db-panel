// Package web serves the panel's browser UI: login, dashboard, server pages
// and the SSE stream that keeps them live. Templates and static assets come
// from the embedded github.com/husmin-fajrin/db-panel/web package.
package web

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	webfs "github.com/husmin-fajrin/db-panel/web"
)

// Page holds the fields the layout needs; every page's data embeds it.
type Page struct {
	Username string // logged-in user; empty hides the navbar
	Version  string
	Flash    string // error message shown above the page content
	Notice   string // success message shown above the page content
}

// pageNames are the page templates; each file templates/<name>.html defines
// "title" and "content".
var pageNames = []string{"login", "dashboard", "servers", "server_new", "server_detail",
	"master_detail", "replica_new", "replica_detail", "replica_databases", "jobs", "job_detail"}

// Renderer renders a page inside the shared layout.
type Renderer struct {
	pages map[string]*template.Template
	// fragments holds the shared partials (status, timeline) rendered on
	// their own, without the layout.
	fragments *template.Template
	logger    *slog.Logger
}

var templateFuncs = template.FuncMap{
	"fmtTime":     fmtTime,
	"fmtMB":       fmtMB,
	"fmtBytes":    fmtBytes,
	"sub":         func(a, b int64) int64 { return a - b },
	"statusLabel": statusLabel,
	"statusClass": statusClass,
	"kindLabel":   kindLabel,
	// Monitoring.
	"severityClass": severityClass,
	"fmtDuration":   fmtDuration,
	"fmtPct":        fmtPct,
	"ago":           ago,
}

// partials are the templates shared by several pages and rendered on their
// own by the fragment endpoint.
var partials = []string{"templates/layout.html", "templates/_status.html", "templates/_timeline.html"}

// NewRenderer parses the layout and the shared partials together with each
// page into its own template set, plus one set of the partials alone for
// fragments.
func NewRenderer() (*Renderer, error) {
	base, err := template.New("").Funcs(templateFuncs).ParseFS(webfs.FS, partials...)
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

// severityClass is the daisyUI badge class of a monitoring severity.
func severityClass(s monitor.Severity) string {
	switch s {
	case monitor.SevOK:
		return "badge-success"
	case monitor.SevWarn:
		return "badge-warning"
	case monitor.SevCrit:
		return "badge-error"
	}
	return "badge-ghost"
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
