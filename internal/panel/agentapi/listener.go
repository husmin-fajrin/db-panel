package agentapi

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	shutdownTimeout = 5 * time.Second
	// idleTimeout closes keep-alive connections idle between requests; an
	// upgraded WebSocket is hijacked and not affected.
	idleTimeout = 120 * time.Second
)

// readTimeout bounds reading a whole request, so a slow body cannot hold a
// connection. An upgraded WebSocket is unaffected: net/http clears the
// connection's deadlines when the handler hijacks it. A variable so tests
// can shorten it.
var readTimeout = 30 * time.Second

// ListenAndServeAgents serves h over TLS on addr until ctx is done, then
// shuts down within 5 s. It returns nil after a clean shutdown.
func ListenAndServeAgents(ctx context.Context, addr string, tlsCfg *tls.Config, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ServeAgents(ctx, ln, tlsCfg, h)
}

// ServeAgents is ListenAndServeAgents on an existing listener, which it
// closes. Requests' contexts derive from ctx, so upgraded agent connections
// (which http.Server.Shutdown does not track) end when ctx is done; it waits
// (up to 5 s) for every handler, including those, to return before it does.
// If serving fails before ctx is done, the handlers are cancelled and waited
// for the same way before the error is returned.
func ServeAgents(ctx context.Context, ln net.Listener, tlsCfg *tls.Config, h http.Handler) error {
	hctx, cancelHandlers := context.WithCancel(ctx)
	defer cancelHandlers()
	var t handlerTracker
	srv := &http.Server{
		Handler:           t.wrap(h),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return hctx },
		// Agents speak HTTP/1.1 (WebSocket) only; a non-nil empty map turns
		// off the server's automatic HTTP/2 over TLS.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(ln, "", "") }()

	select {
	case err := <-served:
		cancelHandlers()
		srv.Close()
		wctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		t.wait(wctx)
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		// Connections still in their TLS handshake or first request are not
		// worth failing the shutdown for.
		srv.Close()
		err = nil
	}
	if serr := <-served; !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}
	if werr := t.wait(sctx); err == nil {
		err = werr
	}
	return err
}

// handlerTracker counts running handlers, including hijacked ones.
type handlerTracker struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (t *handlerTracker) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			http.Error(w, "panel sedang berhenti", http.StatusServiceUnavailable)
			return
		}
		t.wg.Add(1)
		t.mu.Unlock()
		defer t.wg.Done()
		h.ServeHTTP(w, r)
	})
}

// wait refuses new handlers and waits for running ones until ctx is done.
func (t *handlerTracker) wait(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// ctx may have expired before the call (a slow Shutdown); handlers
		// that already returned still count as done.
		select {
		case <-done:
			return nil
		case <-time.After(10 * time.Millisecond):
			return ctx.Err()
		}
	}
}
