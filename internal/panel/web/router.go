package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	webfs "github.com/husmin-fajrin/db-panel/web"
)

const msgInternal = "Terjadi kesalahan pada panel. Lihat log panel untuk detailnya."

// Deps are the panel services the UI uses. All fields except Now, Version
// and Logger are required. Sessions and Limiter are configured (TTL, limits)
// by the caller.
type Deps struct {
	Store     *store.Store
	Hub       *hub.Hub
	Sessions  *auth.Sessions
	Limiter   *auth.RateLimiter
	PublicURL func() string // e.g. "http://192.168.1.10:8080", used in the pairing command
	Events    *Broker
	Now       func() time.Time // nil means time.Now
	Version   string
	Logger    *slog.Logger // nil means slog.Default()
}

// app carries the dependencies into the handlers.
type app struct {
	Deps
	render *Renderer
}

// NewRouter returns the UI handler. It does not serve the agent endpoints
// (/agent/...), which the panel mounts next to it. It panics if the embedded
// templates do not parse, which is a build defect caught by the tests.
func NewRouter(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	render, err := NewRenderer()
	if err != nil {
		panic("web: " + err.Error())
	}
	render.logger = d.Logger
	a := &app{Deps: d, render: render}

	static, err := fs.Sub(webfs.FS, "static")
	if err != nil {
		panic("web: " + err.Error())
	}
	login := auth.RequireLogin(d.Sessions, "/login")
	authed := func(h http.HandlerFunc) http.Handler { return login(h) }
	authedPost := func(h http.HandlerFunc) http.Handler { return login(sameOrigin(h)) }

	mux := http.NewServeMux()
	mux.Handle("GET /static/", cacheFor(time.Hour, http.StripPrefix("/static/", http.FileServerFS(static))))
	mux.HandleFunc("GET /login", a.loginForm)
	mux.Handle("POST /login", sameOrigin(http.HandlerFunc(a.login)))
	mux.Handle("POST /logout", sameOrigin(http.HandlerFunc(a.logout)))
	mux.Handle("GET /{$}", authed(a.dashboard))
	mux.Handle("GET /servers", authed(a.listServers))
	mux.Handle("GET /servers/new", authed(a.newServerForm))
	mux.Handle("POST /servers", authedPost(a.createServer))
	mux.Handle("GET /servers/{id}", authed(a.serverDetail))
	mux.Handle("POST /servers/{id}/token", authedPost(a.newToken))
	mux.Handle("POST /servers/{id}/delete", authedPost(a.deleteServer))
	mux.Handle("GET /events", login(d.Events))
	return noFraming(mux)
}

// noFraming forbids other sites from framing the panel (clickjacking).
func noFraming(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// page returns the layout fields for the current request.
func (a *app) page(r *http.Request) Page {
	return Page{Username: auth.UsernameFrom(r.Context()), Version: a.Version}
}

// internalError logs err and answers 500 without exposing details.
func (a *app) internalError(w http.ResponseWriter, what string, err error) {
	a.Logger.Error(what, "err", err)
	http.Error(w, msgInternal, http.StatusInternalServerError)
}

func cacheFor(d time.Duration, h http.Handler) http.Handler {
	v := "public, max-age=" + strconv.Itoa(int(d.Seconds()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", v)
		h.ServeHTTP(w, r)
	})
}
