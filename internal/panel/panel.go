// Package panel assembles the dbpanel web panel: the store, the private CA,
// the agent hub with the panel host's own embedded agent, the UI HTTP server
// and the mTLS agent listener.
package panel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

// Defaults for Options.
const (
	DefaultDataDir   = "/var/lib/dbpanel"
	DefaultConfigDir = "/etc/dbpanel"
	DefaultHTTPAddr  = ":8080"
	DefaultAgentAddr = ":8443"
)

const (
	dbFile              = "panel.db"
	initialPasswordFile = "admin-initial-password"
	adminUsername       = "admin"

	settingLocalServerID = "local_server_id"
	settingPublicHost    = "public_host"

	sessionTTL       = 12 * time.Hour // sliding: since the last request
	sessionMaxAge    = 24 * time.Hour // absolute: since login
	epochCacheTTL    = 5 * time.Second
	loginMaxAttempts = 5  // per client IP, every attempt
	loginMaxFailures = 20 // per username, wrong passwords only
	loginWindow      = 15 * time.Minute

	defaultProbeInterval = 60 * time.Second
	probeTimeout         = 30 * time.Second
)

// Options configure a Panel. Zero fields take the defaults above.
type Options struct {
	DataDir   string // panel.db, bin/
	ConfigDir string // ca.crt, ca.key, admin-initial-password
	HTTPAddr  string // UI listener
	AgentAddr string // mTLS agent listener
	// PublicHost is the IP or host agents and the pairing command use to
	// reach the panel. Empty means the public_host setting, else the first
	// non-loopback IPv4 address of an interface that is up.
	PublicHost string
	Logger     *slog.Logger // nil means slog.Default()
	Version    string       // empty means version.Version
}

// Panel is an assembled panel, ready to Run.
type Panel struct {
	Store       *store.Store
	CA          *ca.CA
	Hub         *hub.Hub
	Sessions    *auth.Sessions
	Limiter     *auth.RateLimiter // login attempts per IP
	UserLimiter *auth.RateLimiter // failed logins per username
	Broker      *web.Broker

	opts          Options
	log           *slog.Logger
	epoch         *sessionEpoch
	now           func() time.Time
	localID       int64
	probeInterval time.Duration
	readTimeout   time.Duration                // UI server ReadTimeout; tests shorten it
	publish       func(event string, data any) // Broker.Publish; tests wrap it

	addrMu       sync.Mutex
	uiAddr       string    // bound UI address once Run listens
	agentAddr    string    // bound agent listener address once Run listens
	localAddrs   []net.IP  // interface addresses, for hostAllowed
	localAddrsAt time.Time // when localAddrs was read

	evMu     sync.Mutex
	evLogged map[int64]time.Time // last logged agent event per server

	// bgMu guards the background probe goroutines: runCtx is set while Run
	// is serving, and bgClosed refuses new probes once Run is shutting down.
	bgMu     sync.Mutex
	runCtx   context.Context
	bgClosed bool
	bg       sync.WaitGroup
}

// New opens the store and CA under the configured directories, creates the
// initial admin account and the panel host's own server on first start, and
// returns a Panel ready to Run. Close releases it.
func New(o Options) (*Panel, error) {
	o = withDefaults(o)
	// The data directory holds panel.db (password and token hashes): owner
	// only, also when it already existed with a looser mode.
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("buat direktori data: %w", err)
	}
	if err := os.Chmod(o.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("atur izin direktori data: %w", err)
	}
	if err := os.MkdirAll(o.ConfigDir, 0o700); err != nil {
		return nil, fmt.Errorf("buat direktori konfigurasi: %w", err)
	}
	st, err := store.Open(filepath.Join(o.DataDir, dbFile))
	if err != nil {
		return nil, err
	}
	epoch := &sessionEpoch{st: st, log: o.Logger, ttl: epochCacheTTL, now: time.Now}
	if _, err := epoch.load(); err != nil {
		st.Close()
		return nil, err
	}
	p := &Panel{
		Store: st,
		Sessions: auth.NewSessions(sessionTTL, time.Now,
			auth.WithMaxAge(sessionMaxAge), auth.WithValidator(epoch.valid)),
		Limiter:       auth.NewRateLimiter(loginMaxAttempts, loginWindow, time.Now),
		UserLimiter:   auth.NewRateLimiter(loginMaxFailures, loginWindow, time.Now),
		epoch:         epoch,
		evLogged:      make(map[int64]time.Time),
		Broker:        web.NewBroker(),
		opts:          o,
		log:           o.Logger,
		now:           time.Now,
		probeInterval: defaultProbeInterval,
		readTimeout:   readTimeout,
	}
	p.publish = p.Broker.Publish
	p.Hub = hub.New(&listenerImpl{p: p})
	if err := p.init(); err != nil {
		st.Close()
		return nil, err
	}
	return p, nil
}

func withDefaults(o Options) Options {
	if o.DataDir == "" {
		o.DataDir = DefaultDataDir
	}
	if o.ConfigDir == "" {
		o.ConfigDir = DefaultConfigDir
	}
	if o.HTTPAddr == "" {
		o.HTTPAddr = DefaultHTTPAddr
	}
	if o.AgentAddr == "" {
		o.AgentAddr = DefaultAgentAddr
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Version == "" {
		o.Version = version.Version
	}
	return o
}

func (p *Panel) init() error {
	var err error
	if p.CA, err = ca.LoadOrCreate(p.opts.ConfigDir); err != nil {
		return err
	}
	if err := p.resolvePublicHost(); err != nil {
		return err
	}
	if err := p.bootstrapAdmin(); err != nil {
		return err
	}
	p.localID, err = p.ensureLocalServer()
	return err
}

// resolvePublicHost fills opts.PublicHost: an explicit value is saved as the
// public_host setting; otherwise the setting is used, else auto-detection.
func (p *Panel) resolvePublicHost() error {
	if p.opts.PublicHost != "" {
		if !validPublicHost(p.opts.PublicHost) {
			return fmt.Errorf("public host %q tidak valid: %s", p.opts.PublicHost, publicHostRule)
		}
		return p.Store.SetSetting(settingPublicHost, p.opts.PublicHost)
	}
	saved, err := p.Store.GetSetting(settingPublicHost)
	if err != nil {
		return err
	}
	if saved != "" {
		if !validPublicHost(saved) {
			return fmt.Errorf("public host tersimpan %q tidak valid: %s; jalankan panel dengan --public-host yang benar",
				saved, publicHostRule)
		}
		p.opts.PublicHost = saved
		return nil
	}
	p.opts.PublicHost = detectPublicHost()
	if p.opts.PublicHost == loopbackHost {
		p.log.Warn("public host tidak terdeteksi; memakai 127.0.0.1, gunakan --public-host")
	}
	return nil
}

// hostnameRe is the host name form accepted for the public host.
var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)

const publicHostRule = "harus alamat IPv4/IPv6 atau nama host dari huruf, angka, titik dan strip (maks. 253)"

// validPublicHost reports whether h is an IP literal or a plain host name.
// The public host is written into the agent install script that runs as
// root, so nothing else (spaces, quotes, $, `) may get through.
func validPublicHost(h string) bool {
	return net.ParseIP(h) != nil || hostnameRe.MatchString(h)
}

// loopbackHost is the public host used when none can be detected.
const loopbackHost = "127.0.0.1"

// detectPublicHost returns the first non-loopback IPv4 address of an
// interface that is up, or 127.0.0.1.
func detectPublicHost() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return loopbackHost
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return loopbackHost
}

// bootstrapAdmin creates the admin account with a random password when the
// panel has no users, writing the password to ConfigDir only (never the log).
func (p *Panel) bootstrapAdmin() error {
	n, err := p.Store.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hash kata sandi admin: %w", err)
	}
	path := filepath.Join(p.opts.ConfigDir, initialPasswordFile)
	// Write the file before creating the user: if this fails the next start
	// tries again instead of leaving an admin nobody knows the password of.
	if err := writeSecret(path, pw+"\n"); err != nil {
		return fmt.Errorf("tulis kata sandi admin awal: %w", err)
	}
	if err := p.Store.CreateUser(adminUsername, hash); err != nil {
		return err
	}
	p.log.Info("Kata sandi admin awal ditulis ke " + path)
	return nil
}

// writeSecret writes data to path with mode 0600, tightening the mode of an
// existing file.
func writeSecret(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// sessionEpoch decides whether a session is still valid: sessions created
// at or before the store's session epoch (bumped by reset-password, which
// runs in another process) are not. The epoch is re-read from the store at
// most every ttl.
type sessionEpoch struct {
	st  *store.Store
	log *slog.Logger
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	epoch   int64
	fetched time.Time
}

// load reads the epoch from the store into the cache.
func (e *sessionEpoch) load() (int64, error) {
	v, err := e.st.SessionEpoch()
	if err != nil {
		return 0, err
	}
	e.epoch, e.fetched = v, e.now()
	return v, nil
}

func (e *sessionEpoch) valid(created time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.now().Sub(e.fetched) >= e.ttl {
		if _, err := e.load(); err != nil {
			e.log.Warn("read session epoch; using the cached value", "err", err)
		}
	}
	return created.Unix() > e.epoch
}

// Close closes the store. Call it after Run has returned.
func (p *Panel) Close() error { return p.Store.Close() }

// UIAddr returns the bound UI address once Run is listening, else "".
func (p *Panel) UIAddr() string {
	p.addrMu.Lock()
	defer p.addrMu.Unlock()
	return p.uiAddr
}

// AgentListenAddr returns the bound agent listener address once Run is
// listening, else "".
func (p *Panel) AgentListenAddr() string {
	p.addrMu.Lock()
	defer p.addrMu.Unlock()
	return p.agentAddr
}

// PublicURL is the UI base URL as agents and browsers reach it, e.g.
// "http://192.168.1.10:8080". It uses the bound port once Run listens.
func (p *Panel) PublicURL() string {
	return "http://" + net.JoinHostPort(p.opts.PublicHost, p.port(p.UIAddr(), p.opts.HTTPAddr))
}

// AgentAddr is host:port of the agent listener as agents reach it.
func (p *Panel) AgentAddr() string {
	return net.JoinHostPort(p.opts.PublicHost, p.port(p.AgentListenAddr(), p.opts.AgentAddr))
}

// port returns the port of bound, or of configured when bound is empty.
func (p *Panel) port(bound, configured string) string {
	addr := bound
	if addr == "" {
		addr = configured
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}
