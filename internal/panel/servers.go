package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

const (
	readHeaderTimeout = 10 * time.Second
	// idleTimeout closes keep-alive connections idle between requests. There
	// is no ReadTimeout: it would also cut the long-lived SSE stream. Form
	// posts get their own body size and read deadline in the web package.
	idleTimeout     = 120 * time.Second
	maxHeaderBytes  = 64 << 10
	shutdownTimeout = 5 * time.Second
)

// Run serves the UI and the agent listener, attaches the embedded agent and
// keeps server info fresh until ctx is done. It returns once both servers
// and every background probe have stopped; the error is nil after a clean
// shutdown. If either server fails, Run stops the other and returns the
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

	detachLocal := p.attachLocal()
	wg.Go(func() { p.probeLoop(ctx) })

	<-ctx.Done()
	detachLocal()
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
		OnEvent:      p.onEvent,
	}
}

// serveUI serves the UI and the unauthenticated agent endpoints on ln until
// ctx is done, then shuts down within 5 s, closing whatever connections are
// left after that. Open SSE streams end with ctx through BaseContext; there
// is no WriteTimeout because of them.
func (p *Panel) serveUI(ctx context.Context, ln net.Listener, deps agentapi.Deps) error {
	srv := &http.Server{
		Handler:           p.uiHandler(deps),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		p.log.Warn("UI requests still running after the shutdown timeout; closing their connections")
		srv.Close()
		err = nil
	}
	if serr := <-served; !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}
	return err
}

// uiHandler mounts the agent pairing and install endpoints next to the web UI.
// Agent binaries are served from <data dir>/bin, falling back to the panel's
// own executable for its architecture.
func (p *Panel) uiHandler(deps agentapi.Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /agent/pair", agentapi.PairHandler(deps))
	mux.Handle("GET /agent/install", agentapi.InstallScriptHandler(deps, p.PublicURL))
	mux.Handle("GET /agent/bin/{arch}", agentapi.BinaryHandler(deps, filepath.Join(p.opts.DataDir, "bin")))
	mux.Handle("/", web.NewRouter(web.Deps{
		Store:     p.Store,
		Hub:       p.Hub,
		Sessions:  p.Sessions,
		Limiter:   p.Limiter,
		PublicURL: p.PublicURL,
		Events:    p.Broker,
		Now:       p.now,
		Version:   p.opts.Version,
		Logger:    p.log,
	}))
	return mux
}

// agentHandler serves the mTLS agent listener.
func agentHandler(deps agentapi.Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /agent/ws", agentapi.WSHandler(deps))
	return mux
}
