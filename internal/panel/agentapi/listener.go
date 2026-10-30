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

const shutdownTimeout = 5 * time.Second

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
// for every handler, including those, to return before it does.
func ServeAgents(ctx context.Context, ln net.Listener, tlsCfg *tls.Config, h http.Handler) error {
	var t handlerTracker
	srv := &http.Server{
		Handler:           t.wrap(h),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(ln, "", "") }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
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
		return ctx.Err()
	}
}
