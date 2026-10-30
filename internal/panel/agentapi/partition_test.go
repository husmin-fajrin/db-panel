package agentapi_test

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startBlackholeProxy forwards TCP between clients and target until frozen
// is set; from then on it silently drops every byte in both directions
// without closing either socket, as a powered-off host or a cut cable does.
// closeAll closes the listener and every socket; it also runs at cleanup.
func startBlackholeProxy(t *testing.T, target string) (addr string, frozen *atomic.Bool, closeAll func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	frozen = new(atomic.Bool)
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	track := func(c net.Conn) {
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
	}
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if err != nil {
				if !frozen.Load() {
					dst.Close()
				}
				return
			}
			if frozen.Load() {
				continue
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			track(c)
			track(u)
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	closeAll = func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	}
	t.Cleanup(closeAll)
	return ln.Addr().String(), frozen, closeAll
}

// TestPanelDetectsPartitionedAgent: an agent whose network silently dies
// must be reported offline by the panel itself within 30 s.
func TestPanelDetectsPartitionedAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("takes up to 30 s")
	}
	e := newEnv(t)
	id, tok := e.newToken(t, 0)
	cfg := e.pair(t, tok, t.TempDir())
	addr, frozen, closeProxy := startBlackholeProxy(t, e.agentAddr)
	cfg.PanelAddr = addr
	startAgent(t, cfg, stubRegistry())
	// Runs before the agent is stopped: an agent stopping behind a dead
	// network spends up to 10 s on its close handshake.
	t.Cleanup(closeProxy)
	waitID(t, e.l.online, "OnOnline")

	start := time.Now()
	frozen.Store(true)
	select {
	case got := <-e.l.offline:
		if got != id {
			t.Fatalf("offline id = %d, want %d", got, id)
		}
		t.Logf("panel reported offline %v after the partition", time.Since(start).Round(time.Millisecond))
	case <-time.After(30 * time.Second):
		t.Fatalf("server %d still online 30 s after the partition (IsOnline=%v)", id, e.hub.IsOnline(id))
	}
}

// TestServeAgentsStopsDespiteUnresponsiveAgent: stopping the panel while an
// agent's network is dead must not wait on that agent's close handshake, or
// ServeAgents misses its 5 s budget and the panel exits with an error.
func TestServeAgentsStopsDespiteUnresponsiveAgent(t *testing.T) {
	e := newEnv(t)
	_, tok := e.newToken(t, 0)
	cfg := e.pair(t, tok, t.TempDir())
	addr, frozen, closeProxy := startBlackholeProxy(t, e.agentAddr)
	cfg.PanelAddr = addr
	startAgent(t, cfg, stubRegistry())
	t.Cleanup(closeProxy)
	waitID(t, e.l.online, "OnOnline")

	frozen.Store(true)
	start := time.Now()
	if err := e.stopPanel(); err != nil {
		t.Fatalf("ServeAgents: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ServeAgents took %v to stop", d)
	}
}
