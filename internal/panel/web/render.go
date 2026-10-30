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
	"master_detail", "replica_new", "replica_detail", "jobs", "job_detail"}

// Renderer renders a page inside the shared layout.
type Renderer struct {
	pages  map[string]*template.Template
	logger *slog.Logger
}

var templateFuncs = template.FuncMap{
	"fmtTime":     fmtTime,
	"fmtMB":       fmtMB,
	"fmtBytes":    fmtBytes,
	"sub":         func(a, b int64) int64 { return a - b },
	"statusLabel": statusLabel,
	"statusClass": statusClass,
	"kindLabel":   kindLabel,
}

// NewRenderer parses layout.html together with each page into its own
// template set.
func NewRenderer() (*Renderer, error) {
	base, err := template.New("").Funcs(templateFuncs).ParseFS(webfs.FS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}
	r := &Renderer{pages: make(map[string]*template.Template, len(pageNames)), logger: slog.Default()}
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
