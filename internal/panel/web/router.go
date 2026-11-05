package web

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	webfs "github.com/husmin-fajrin/db-panel/web"
)

const msgInternal = "Terjadi kesalahan pada panel. Lihat log panel untuk detailnya."

// defaultLoginConcurrency caps how many password checks (bcrypt, ~0.3 s of
// CPU each) run at once: half the CPUs, at least 1 and at most 4. Further
// login attempts get 429 straight away.
var defaultLoginConcurrency = min(4, max(1, runtime.NumCPU()/2))

// Default per-username limit when Deps.UserLimiter is nil.
const (
	defaultUserMaxFailures = 20
	defaultUserWindow      = 15 * time.Minute
)

// Deps are the panel services the UI uses. All fields except Now, Version,
// Logger, CheckPassword, LoginConcurrency, UserLimiter and ConfirmLimiter
// are required.
// Sessions and the limiters are configured (TTL, limits) by the caller.
type Deps struct {
	Store    *store.Store
	Hub      *hub.Hub
	Sessions *auth.Sessions
	// Limiter counts every login attempt per client IP.
	Limiter *auth.RateLimiter
	// UserLimiter counts failed logins (wrong password) per username, so
	// rotating source addresses does not buy more guesses for one account;
	// nil means 20 per 15 minutes.
	UserLimiter *auth.RateLimiter
	PublicURL   func() string // e.g. "https://192.168.1.10:8080", used in the pairing command
	Events      *Broker
	Now         func() time.Time // nil means time.Now
	Version     string
	Logger      *slog.Logger // nil means slog.Default()
	// ConfirmLimiter counts POST /confirm attempts per username; nil
	// means 5 per minute.
	ConfirmLimiter *auth.RateLimiter
	// CheckPassword compares a password with a stored hash; nil means
	// auth.CheckPassword. Tests replace it with a slow fake.
	CheckPassword func(hash, password string) bool
	// LoginConcurrency is the number of password checks allowed to run at
	// once; 0 means min(4, max(1, NumCPU/2)).
	LoginConcurrency int
	// InstallScriptSHA256 returns the sha256 (hex) of the install script
	// GET /agent/install serves for token. It goes into the pairing command,
	// so the operator's shell refuses a script changed on the way.
	InstallScriptSHA256 func(token string) (string, error)
	// Orchestrator connects MySQL and runs the prepare-master and
	// create-replica jobs.
	Orchestrator Orchestrator
	// Monitor holds the servers' health and answers Periksa sekarang and
	// Jangan ganggu.
	Monitor MonitorView
	// Settings loads and saves the monitoring settings (Pengaturan).
	Settings SettingsStore
	// Telegram reads the Telegram settings, stores the bot token sealed and
	// sends the test message.
	Telegram TelegramAdmin
	// CertInfo describes the panel's HTTPS certificate in use; nil means
	// none (tests).
	CertInfo func() CertInfo
	// Certs changes the panel's HTTPS certificate; nil refuses (tests).
	Certs CertAdmin
}

// SettingsStore is what the settings page uses to read and change the
// monitoring settings.
type SettingsStore interface {
	Load() (monitor.Settings, error)
	// Save validates and stores the settings.
	Save(monitor.Settings) error
	// Reconfigure applies the stored settings to the monitor and the
	// online agents.
	Reconfigure()
}

// TelegramAdmin is what the settings page uses of the Telegram
// notifications; the panel seals the token with its key, which the UI
// never sees.
type TelegramAdmin interface {
	// LoadConfig returns the saved settings, the bot token opened.
	LoadConfig() (notify.Config, error)
	// SaveToken seals and stores the bot token; a malformed one is refused.
	SaveToken(token string) error
	// SendTest sends a test message; at most once per 10 s
	// (notify.ErrTestTooSoon).
	SendTest(ctx context.Context) error
}

// MonitorView is what the UI uses of the health monitor; the panel passes
// its *monitor.Monitor.
type MonitorView interface {
	View(serverID int64) (monitor.View, bool)
	Views() map[int64]monitor.View
	// CheckNow asks the agent for a fresh report; at most once per 10 s
	// per server.
	CheckNow(ctx context.Context, serverID int64) error
	// Snooze mutes the server's alert notifications until until; nil
	// clears it.
	Snooze(serverID int64, until *time.Time) error
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
	if d.UserLimiter == nil {
		d.UserLimiter = auth.NewRateLimiter(defaultUserMaxFailures, defaultUserWindow, d.Now)
	}
	if d.ConfirmLimiter == nil {
		d.ConfirmLimiter = auth.NewRateLimiter(defaultConfirmMax, defaultConfirmWindow, d.Now)
	}
	if d.CertInfo == nil {
		d.CertInfo = func() CertInfo { return CertInfo{} }
	}
	if d.Certs == nil {
		d.Certs = noCerts{}
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
	mux.Handle("POST /confirm", authedPost(a.issueConfirm))
	mux.Handle("GET /servers", authed(a.listServers))
	mux.Handle("GET /servers/new", authed(a.newServerForm))
	mux.Handle("POST /servers", authedPost(a.createServer))
	mux.Handle("GET /servers/{id}", authed(a.serverDetail))
	mux.Handle("POST /servers/{id}/token", authedPost(a.newToken))
	mux.Handle("POST /servers/{id}/delete", authedPost(a.deleteServer))
	mux.Handle("POST /servers/{id}/mysql/connect", authedPost(a.connectMySQL))
	mux.Handle("POST /servers/{id}/mysql/refresh", authedPost(a.refreshMySQL))
	mux.Handle("POST /servers/{id}/prepare-master", authedPost(a.prepareMaster))
	mux.Handle("POST /servers/{id}/mysql-tls", authedPost(a.installMySQLTLS))
	mux.Handle("POST /servers/{id}/check", authedPost(a.checkServer))
	mux.Handle("POST /servers/{id}/snooze", authedPost(a.snoozeServer))
	mux.Handle("GET /servers/{id}/status-fragment", authed(a.statusFragment))
	mux.Handle("GET /masters/{id}", authed(a.masterDetail))
	mux.Handle("POST /masters/{id}/source", authedPost(a.setMasterSource))
	mux.Handle("POST /masters/{id}/unlock", authedPost(a.unlockMaster))
	mux.Handle("POST /masters/{id}/tighten-tls", authedPost(a.tightenTLS))
	mux.Handle("GET /masters/{id}/replicas/new", authed(a.newReplicaForm))
	mux.Handle("POST /masters/{id}/replicas", authedPost(a.createReplica))
	mux.Handle("GET /replicas/{id}", authed(a.replicaDetail))
	mux.Handle("GET /replicas/{id}/databases", authed(a.replicaDatabases))
	mux.Handle("POST /replicas/{id}/databases/add", authedPost(a.addReplicaDatabases))
	mux.Handle("POST /replicas/{id}/databases/remove", authedPost(a.removeReplicaDatabases))
	mux.Handle("POST /replicas/{id}/stop", authedPost(a.stopReplica))
	mux.Handle("POST /replicas/{id}/start", authedPost(a.startReplica))
	mux.Handle("POST /replicas/{id}/relock", authedPost(a.relockReplica))
	mux.Handle("POST /replicas/{id}/resync/prepare", authedPost(a.prepareResync))
	mux.Handle("POST /replicas/{id}/resync", authedPost(a.resyncReplica))
	mux.Handle("POST /replicas/{id}/detach", authedPost(a.detachReplica))
	mux.Handle("GET /jobs", authed(a.listJobs))
	mux.Handle("GET /jobs/{id}", authed(a.jobDetail))
	mux.Handle("GET /jobs/{id}/log", authed(a.jobLog))
	mux.Handle("POST /jobs/{id}/approve", authedPost(a.approveJob))
	mux.Handle("POST /jobs/{id}/retry", authedPost(a.retryJob))
	mux.Handle("POST /jobs/{id}/cancel", authedPost(a.cancelJob))
	mux.Handle("GET /alerts", authed(a.alertsPage))
	mux.Handle("GET /alerts/count", authed(a.alertCount))
	mux.Handle("GET /settings", authed(a.showSettings))
	mux.Handle("POST /settings", authedPost(a.saveSettings))
	mux.Handle("POST /settings/telegram-test", authedPost(a.testTelegram))
	mux.Handle("POST /settings/cert", authedPost(a.uploadCert))
	mux.Handle("POST /settings/cert-panel", authedPost(a.usePanelCert))
	mux.Handle("POST /settings/cert-names", authedPost(a.saveCertNames))
	mux.Handle("POST /settings/cert-notice-close", authedPost(a.closeCertNotice))
	mux.Handle("POST /settings/base-url-https", authedPost(a.baseURLToHTTPS))
	// The certificate page and its download need no login: they are what
	// a browser that does not trust the panel yet must reach, also over
	// plain HTTP.
	mux.HandleFunc("GET /sertifikat", a.certPage)
	mux.HandleFunc("GET /sertifikat/dbpanel-panel.crt", a.certDownload)
	mux.Handle("GET /audit", authed(a.auditLog))
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
	p := Page{Username: auth.UsernameFrom(r.Context()), Version: a.Version}
	if p.Username != "" {
		p.CertNotice = a.CertInfo().CANeedsReinstall
		// The navbar's alert badge; a page still renders without it.
		if open, err := a.Store.ListOpenAlerts(); err != nil {
			a.Logger.Warn("count open alerts", "err", err)
		} else {
			p.AlertCount = len(open)
		}
	}
	return p
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
