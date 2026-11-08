package panel

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

const (
	readHeaderTimeout = 10 * time.Second
	// readTimeout bounds reading a whole request, body included, so a slow
	// body cannot hold a connection; the SSE stream clears it for itself.
	readTimeout = 30 * time.Second
	// idleTimeout closes keep-alive connections idle between requests.
	idleTimeout     = 120 * time.Second
	maxHeaderBytes  = 64 << 10
	shutdownTimeout = 5 * time.Second
)

// Run serves the UI and the agent listener and keeps server info fresh
// until ctx is done. It returns once both servers and every background
// probe have stopped; the error is nil after a clean shutdown. If either server fails, Run stops the other and returns the
// failure.
func (p *Panel) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	uiLn, err := net.Listen("tcp", p.opts.HTTPAddr)
	if err != nil {
		return err
	}
	agentLn, err := net.Listen("tcp", p.opts.AgentAddr)
	if err != nil {
		uiLn.Close()
		return err
	}
	tlsCfg, err := p.CA.AgentListenerTLS()
	if err != nil {
		uiLn.Close()
		agentLn.Close()
		return err
	}
	p.addrMu.Lock()
	p.uiAddr, p.agentAddr = uiLn.Addr().String(), agentLn.Addr().String()
	p.addrMu.Unlock()
	p.bgMu.Lock()
	p.runCtx = ctx
	p.bgMu.Unlock()

	var (
		wg    sync.WaitGroup
		errMu sync.Mutex
		errs  []error
	)
	fail := func(err error) {
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			cancel()
		}
	}
	deps := p.agentDeps()
	wg.Go(func() { fail(p.serveUI(ctx, uiLn, deps)) })
	wg.Go(func() { fail(agentapi.ServeAgents(ctx, agentLn, tlsCfg, agentHandler(deps))) })
	p.log.Info("panel listening", "ui", p.UIAddr(), "agents", p.AgentListenAddr(), "public_url", p.PublicURL())

	wg.Go(func() { p.probeLoop(ctx) })
	wg.Go(func() { p.Monitor.Run(ctx) })
	wg.Go(func() { p.Notifier.Run(ctx) })
	wg.Go(func() { p.certs.run(ctx.Done()) })
	wg.Go(func() { p.renewMySQLCerts(ctx) })
	wg.Go(func() { p.retentionLoop(ctx) })

	<-ctx.Done()
	// Stop the jobs before their agents disconnect, so none fails as
	// "agent terputus"; Resume fails them at the next start.
	p.Orchestrator.Close()
	wg.Wait()
	p.stopBackground()
	return errors.Join(errs...)
}

// agentDeps are the dependencies of the agent endpoints on both listeners.
func (p *Panel) agentDeps() agentapi.Deps {
	return agentapi.Deps{
		Store:        p.Store,
		CA:           p.CA,
		Hub:          p.Hub,
		AgentAddr:    p.AgentAddr,
		AgentVersion: p.opts.Version,
		Now:          p.now,
		Logger:       p.log,
		WebPin:       p.certs.pin,
		// The panel host's agent pairs from 127.0.0.1; its server keeps
		// the public host as address.
		LocalServerID: p.localID,
		PublicHost:    p.opts.PublicHost,
		OnPaired:      p.onPaired,
	}
}

// serveUI serves the UI and the unauthenticated agent endpoints over HTTPS
// on ln, and the certificate page with redirects to HTTPS over plain HTTP on
// the same port (see splitListener), until ctx is done; then it shuts both
// down within 5 s, closing whatever connections are left after that. Open
// SSE streams end with ctx through BaseContext; there is no WriteTimeout
// because of them.
func (p *Panel) serveUI(ctx context.Context, ln net.Listener, deps agentapi.Deps) error {
	split := newSplitListener(ln)
	defer split.Close()
	ui := p.uiHandler(deps)
	base := func(net.Listener) context.Context { return ctx }
	srv := &http.Server{
		Handler:           ui,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       p.readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       base,
		ErrorLog:          serverErrorLog(p.log),
	}
	// Plain HTTP only answers the certificate page and redirects: short
	// timeouts, so a client that sends half a request is soon closed.
	helper := &http.Server{
		Handler:           httpHelperHandler(ui, p.hostAllowed),
		ReadHeaderTimeout: peekTimeout,
		ReadTimeout:       peekTimeout,
		WriteTimeout:      readTimeout,
		IdleTimeout:       peekTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       base,
		ErrorLog:          serverErrorLog(p.log),
	}
	served := make(chan error, 2)
	go func() { served <- srv.Serve(tls.NewListener(split.tls, p.certs.tlsConfig())) }()
	go func() { served <- helper.Serve(split.plain) }()

	var err error
	select {
	case err = <-served:
		// One server failed: stop the other below and report the failure.
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range []*http.Server{srv, helper} {
		wg.Go(func() {
			if errors.Is(s.Shutdown(sctx), context.DeadlineExceeded) {
				p.log.Warn("UI requests still running after the shutdown timeout; closing their connections")
				s.Close()
			}
		})
	}
	wg.Wait()
	remaining := 2
	if err != nil {
		remaining = 1
	}
	for range remaining {
		if serr := <-served; !errors.Is(serr, http.ErrServerClosed) && err == nil {
			err = serr
		}
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// uiHandler mounts the agent pairing and install endpoints next to the web UI.
// Agent binaries are served from <data dir>/bin, falling back to the panel's
// own executable for its architecture.
//
// Every request must name the panel in its Host header (see hostAllowed),
// and every response, the agent endpoints included, gets the security
// headers of web.SecurityHeaders.
func (p *Panel) uiHandler(deps agentapi.Deps) http.Handler {
	binDir := filepath.Join(p.opts.DataDir, "bin")
	hashes := agentapi.NewBinHashes(binDir)
	mux := http.NewServeMux()
	mux.Handle("POST /agent/pair", agentapi.PairHandler(deps))
	mux.Handle("GET /agent/install", agentapi.InstallScriptHandler(deps, p.PublicURL, hashes))
	mux.Handle("GET /agent/bin/{arch}", agentapi.BinaryHandler(deps, binDir))
	mux.Handle("/", web.NewRouter(web.Deps{
		Store:          p.Store,
		Hub:            p.Hub,
		Sessions:       p.Sessions,
		Limiter:        p.Limiter,
		UserLimiter:    p.UserLimiter,
		ConfirmLimiter: p.ConfirmLimiter,
		PublicURL:      p.PublicURL,
		Events:         p.Broker,
		Now:            p.now,
		Version:        p.opts.Version,
		Logger:         p.log,
		Orchestrator:   p.Orchestrator,
		Monitor:        p.Monitor,
		Settings:       settingsAdmin{st: p.Store, mon: p.Monitor},
		Telegram:       telegramAdmin{st: p.Store, box: p.box, n: p.Notifier},
		CertInfo:       p.certInfo,
		Certs:          p.certs,
		LocalServerID:  p.localID,
		// The same parameters as GET /agent/install, so the hash in the
		// pairing command is that of the script the endpoint serves.
		InstallScriptSHA256: func(token string) (string, error) {
			script, err := agentapi.RenderInstallScript(agentapi.NewInstallScriptParams(deps, p.PublicURL(), token, hashes))
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(script)
			return hex.EncodeToString(sum[:]), nil
		},
	}))
	// Headers outermost, so the 421 of a refused Host carries them too.
	return web.SecurityHeaders(web.RequireHost(p.hostAllowed)(mux))
}

// localAddrsTTL is how long the list of local interface addresses is kept
// before it is read again (addresses can change, e.g. through DHCP).
const localAddrsTTL = 30 * time.Second

// hostAllowed reports whether host (from the Host header, port removed) names
// this panel: the public host, localhost, a loopback address, an address
// of one of the host's interfaces, or a name the HTTPS certificate is valid
// for (the extra names in Settings, the machine's host name, an uploaded
// certificate's names). Anything else is most likely a DNS name rebound to
// the panel's address by another site.
func (p *Panel) hostAllowed(host string) bool {
	if strings.EqualFold(host, p.opts.PublicHost) || strings.EqualFold(host, "localhost") {
		return true
	}
	if p.certs.coversHost(host) {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.Equal(net.ParseIP(p.opts.PublicHost)) {
		return true
	}
	p.addrMu.Lock()
	defer p.addrMu.Unlock()
	if p.now().Sub(p.localAddrsAt) >= localAddrsTTL {
		p.localAddrs, p.localAddrsAt = interfaceIPs(), p.now()
	}
	for _, a := range p.localAddrs {
		if a.Equal(ip) {
			return true
		}
	}
	return false
}

// interfaceIPs returns the addresses of the host's network interfaces.
func interfaceIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			ips = append(ips, ipn.IP)
		}
	}
	return ips
}

// agentHandler serves the mTLS agent listener.
func agentHandler(deps agentapi.Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /agent/ws", agentapi.WSHandler(deps))
	return mux
}
