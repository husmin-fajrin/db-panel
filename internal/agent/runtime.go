package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/pki"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// ErrOffline is returned by Emit while the agent has no connection to the
// panel. It is proto.ErrOffline, the offline error the panel uses too.
var ErrOffline = proto.ErrOffline

const (
	dialTimeout   = 30 * time.Second
	minBackoff    = 1 * time.Second
	maxBackoff    = 30 * time.Second
	stableSession = 60 * time.Second // a session this long resets the backoff
)

// Agent keeps the connection to the panel and serves its requests.
type Agent struct {
	cfg    config.Config
	reg    *commands.Registry
	logger *slog.Logger
	dial   func(ctx context.Context) (*websocket.Conn, error)

	mu  sync.Mutex
	cur *session // nil while offline
}

// New returns an agent for cfg that runs requests through reg.
func New(cfg config.Config, reg *commands.Registry, logger *slog.Logger) *Agent {
	a := &Agent{cfg: cfg, reg: reg, logger: logger}
	a.dial = a.dialPanel
	return a
}

// Run connects to the panel and serves it until ctx is done, reconnecting
// with a backoff of 1, 2, 4, 8, 16, then 30 s after each drop or failed dial
// (back to 1 s after a session that lasted at least a minute). It returns nil
// once ctx is done.
func (a *Agent) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		ws, err := a.dial(ctx)
		if ctx.Err() != nil {
			if ws != nil {
				ws.CloseNow()
			}
			return nil
		}
		if err != nil {
			a.logger.Warn("cannot connect to panel", "panel", a.cfg.PanelAddr, "err", err, "retry_in", backoff)
		} else {
			a.logger.Info("connected to panel", "panel", a.cfg.PanelAddr, "server_id", a.cfg.ServerID)
			start := time.Now()
			err = a.serve(ctx, ws)
			if ctx.Err() != nil {
				return nil
			}
			if time.Since(start) >= stableSession {
				backoff = minBackoff
			}
			if closedByPanel(err) {
				a.logger.Info("panel closed the connection", "reason", closeReason(err), "retry_in", backoff)
			} else {
				a.logger.Warn("disconnected from panel", "err", err, "retry_in", backoff)
			}
		}

		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// Emit sends ev to the panel on the current connection.
func (a *Agent) Emit(ev proto.Event) error {
	a.mu.Lock()
	s := a.cur
	a.mu.Unlock()
	if s == nil {
		return ErrOffline
	}
	if err := s.send(proto.Envelope{Kind: proto.KindEvent, Event: &ev}); err != nil {
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	return nil
}

func (a *Agent) setSession(s *session) {
	a.mu.Lock()
	a.cur = s
	a.mu.Unlock()
}

// dialPanel opens the mTLS WebSocket to the panel. The key and certificates
// are read on every dial, so a re-pair is picked up without a restart.
func (a *Agent) dialPanel(ctx context.Context) (*websocket.Conn, error) {
	certPEM, err := os.ReadFile(a.cfg.CertFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(a.cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(a.cfg.CAFile)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := pki.AgentClientTLS(certPEM, keyPEM, caPEM)
	if err != nil {
		return nil, err
	}

	tr := &http.Transport{TLSClientConfig: tlsCfg}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, "wss://"+a.cfg.PanelAddr+"/agent/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr},
	})
	if err != nil {
		// A rejected handshake may leave a pooled keep-alive connection.
		tr.CloseIdleConnections()
		if resp != nil {
			return nil, fmt.Errorf("panel menolak koneksi: %s", resp.Status)
		}
		return nil, err
	}
	return ws, nil
}
