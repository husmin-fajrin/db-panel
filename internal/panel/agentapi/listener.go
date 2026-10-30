package agentapi

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
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
// (which Shutdown does not track) are closed when ctx is done.
func ServeAgents(ctx context.Context, ln net.Listener, tlsCfg *tls.Config, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
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
	return err
}
