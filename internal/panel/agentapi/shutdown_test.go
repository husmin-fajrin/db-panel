package agentapi_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
)

// syncBuffer is a bytes.Buffer safe for a logger and a test to share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestAgentLogsPanelShutdownAtInfo: a panel restart is routine, so the agent
// logs the panel closing the connection at Info, with the panel's reason.
func TestAgentLogsPanelShutdownAtInfo(t *testing.T) {
	e := newEnv(t)
	_, tok := e.newToken(t, 0)
	cfg := e.pair(t, tok, t.TempDir())

	var logs syncBuffer
	a := agent.New(cfg, stubRegistry(), slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitID(t, e.l.online, "OnOnline")

	if err := e.stopPanel(); err != nil {
		t.Fatalf("ServeAgents: %v", err)
	}
	deadline := time.Now().Add(wait)
	for !strings.Contains(logs.String(), "panel closed the connection") {
		if time.Now().After(deadline) {
			t.Fatalf("agent did not log the panel closing; logs:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := logs.String()
	if !strings.Contains(out, `level=INFO msg="panel closed the connection" reason="panel shutting down"`) {
		t.Errorf("close not logged at Info with the panel's reason; logs:\n%s", out)
	}
	if strings.Contains(out, "disconnected from panel") {
		t.Errorf("panel shutdown logged as a warning; logs:\n%s", out)
	}
}
