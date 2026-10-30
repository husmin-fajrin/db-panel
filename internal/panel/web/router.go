package web

import (
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	webfs "github.com/husmin-fajrin/db-panel/web"
)

const msgInternal = "Terjadi kesalahan pada panel. Lihat log panel untuk detailnya."

// defaultLoginConcurrency caps how many password checks (bcrypt, ~0.3 s of
// CPU each) run at once; further login attempts get 429 straight away.
const defaultLoginConcurrency = 4

// Deps are the panel services the UI uses. All fields except Now, Version,
// Logger, CheckPassword and LoginConcurrency are required. Sessions and
// Limiter are configured (TTL, limits) by the caller; Limiter counts login
// attempts both per client IP and per username.
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
	// CheckPassword compares a password with a stored hash; nil means
	// auth.CheckPassword. Tests replace it with a slow fake.
	CheckPassword func(hash, password string) bool
	// LoginConcurrency is the number of password checks allowed to run at
	// once; 0 means 4.
	LoginConcurrency int
}

// app carries the dependencies into the handlers.
type app struct {
	Deps
	render   *Renderer
	loginSem chan struct{} // one slot per running password check
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
	if d.CheckPassword == nil {
		d.CheckPassword = auth.CheckPassword
	}
	if d.LoginConcurrency <= 0 {
		d.LoginConcurrency = defaultLoginConcurrency
	}
	render, err := NewRenderer()
	if err != nil {
		panic("web: " + err.Error())
	}
	render.logger = d.Logger
	a := &app{Deps: d, render: render, loginSem: make(chan struct{}, d.LoginConcurrency)}

	static, err := fs.Sub(webfs.FS, "static")
	if err != nil {
		panic("web: " + err.Error())
	}
	login := auth.RequireLogin(d.Sessions, "/login")
	authed := func(h http.HandlerFunc) http.Handler { return login(h) }
	post := func(h http.Handler) http.Handler { return sameOrigin(limitForm(h)) }
	authedPost := func(h http.HandlerFunc) http.Handler { return login(post(h)) }

	mux := http.NewServeMux()
	mux.Handle("GET /static/", cacheFor(time.Hour, http.StripPrefix("/static/", http.FileServerFS(noDirFS{static}))))
	mux.HandleFunc("GET /login", a.loginForm)
	mux.Handle("POST /login", post(http.HandlerFunc(a.login)))
	mux.Handle("POST /logout", post(http.HandlerFunc(a.logout)))
	mux.Handle("GET /{$}", authed(a.dashboard))
	mux.Handle("GET /servers", authed(a.listServers))
	mux.Handle("GET /servers/new", authed(a.newServerForm))
	mux.Handle("POST /servers", authedPost(a.createServer))
	mux.Handle("GET /servers/{id}", authed(a.serverDetail))
	mux.Handle("POST /servers/{id}/token", authedPost(a.newToken))
	mux.Handle("POST /servers/{id}/delete", authedPost(a.deleteServer))
	mux.Handle("GET /events", login(d.Events))
	return SecurityHeaders(mux)
}

// contentSecurityPolicy allows scripts, styles and connections from the
// panel itself only: no inline script, no eval (the UI uses the CSP build
// of Alpine.js), no framing. img-src data: is for daisyUI's inline SVG icons.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// SecurityHeaders sets the baseline security headers on every UI and
// agent-bootstrap response. Cache-Control defaults to no-store (pages
// behind the login and anything carrying a pairing token); static assets
// and the SSE stream replace it.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// Not no-referrer: under it browsers send "Origin: null" on
		// same-origin form posts (Fetch spec), which sameOrigin must reject,
		// so nobody could log in. same-origin never sends a Referer (with a
		// pairing token in it) to another site either.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		h.Del("Server")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

const (
	msgMisdirected = "Alamat host tidak dikenal panel. Buka panel lewat alamat IP-nya atau nama host yang diatur dengan --public-host."
	msgTooLarge    = "Permintaan terlalu besar"
)

// RequireHost answers 421 Misdirected Request when allow rejects the
// request's Host header (without port), so a page on another site that
// rebinds its DNS name to the panel's address cannot use the browser to
// reach the panel (DNS rebinding).
func RequireHost(allow func(host string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
			if host == "" || !allow(host) {
				http.Error(w, msgMisdirected, http.StatusMisdirectedRequest)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

const (
	maxFormBody     = 64 << 10
	formReadTimeout = 30 * time.Second
)

// limitForm caps a form post at 64 KiB read within 30 s, and parses it so
// that an oversized body is answered with 413 instead of being silently
// dropped by PostFormValue.
func limitForm(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not every ResponseWriter supports deadlines (httptest); the size
		// cap still applies.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(formReadTimeout))
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
		if err := r.ParseForm(); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, msgTooLarge, http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "permintaan tidak valid", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noDirFS hides directories, so the static file server answers 404 for
// them instead of listing their contents.
type noDirFS struct{ fs.FS }

func (f noDirFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		file.Close()
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return file, nil
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
