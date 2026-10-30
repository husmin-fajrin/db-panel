package agentapi_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/pki"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const wait = 5 * time.Second

type serverEvent struct {
	serverID int64
	ev       proto.Event
}

// fakeListener records hub notifications on buffered channels.
type fakeListener struct {
	online  chan int64
	offline chan int64
	events  chan serverEvent
	// offlineDelay, set before any agent connects, slows OnOffline down.
	offlineDelay time.Duration
}

func newFakeListener() *fakeListener {
	return &fakeListener{
		online:  make(chan int64, 64),
		offline: make(chan int64, 64),
		events:  make(chan serverEvent, 64),
	}
}

func (l *fakeListener) OnOnline(id int64) { l.online <- id }
func (l *fakeListener) OnOffline(id int64) {
	time.Sleep(l.offlineDelay)
	l.offline <- id
}
func (l *fakeListener) OnEvent(id int64, ev proto.Event) {
	l.events <- serverEvent{id, ev}
}

// testEnv is a panel: store, CA, hub, the plain-HTTP pair endpoint and the
// mTLS agent listener, all on 127.0.0.1.
type testEnv struct {
	st        *store.Store
	hub       *hub.Hub
	l         *fakeListener
	pairURL   string
	agentAddr string
	attached  chan *hub.WSConn
	wsHits    atomic.Int64 // requests reaching the agent listener's handler
	logs      *syncBuffer  // the panel side's log output
	// stopPanel cancels the agent listener and returns ServeAgents' result;
	// later calls return the same result.
	stopPanel func() error
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	e := &testEnv{
		st:        st,
		l:         newFakeListener(),
		agentAddr: ln.Addr().String(),
		attached:  make(chan *hub.WSConn, 16),
		logs:      new(syncBuffer),
	}
	e.hub = hub.New(e.l)
	deps := agentapi.Deps{
		Store:        st,
		CA:           authority,
		Hub:          e.hub,
		AgentAddr:    func() string { return e.agentAddr },
		AgentVersion: "test",
		Now:          time.Now,
		Logger:       slog.New(slog.NewTextHandler(e.logs, nil)),
		OnEvent:      e.l.OnEvent,
		OnAttach:     func(_ int64, c *hub.WSConn) { e.attached <- c },
	}

	tlsCfg, err := authority.AgentListenerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ws := agentapi.WSHandler(deps)
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.wsHits.Add(1)
		ws.ServeHTTP(w, r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- agentapi.ServeAgents(ctx, ln, tlsCfg, counted) }()
	e.stopPanel = sync.OnceValue(func() error {
		cancel()
		return <-served
	})
	t.Cleanup(func() {
		if err := e.stopPanel(); err != nil {
			t.Errorf("ServeAgents: %v", err)
		}
	})

	pairSrv := httptest.NewServer(agentapi.PairHandler(deps))
	t.Cleanup(pairSrv.Close)
	e.pairURL = pairSrv.URL
	return e
}

// newToken creates a server (when id is 0) and a fresh pairing token for it.
func (e *testEnv) newToken(t *testing.T, id int64) (int64, string) {
	t.Helper()
	if id == 0 {
		srv, err := e.st.CreateServer(fmt.Sprintf("db-%d", time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		id = srv.ID
	}
	pt, err := e.st.CreatePairToken(id, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return id, pt.Token
}

func (e *testEnv) pair(t *testing.T, token, dir string) config.Config {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cfg, err := agent.Pair(ctx, e.pairURL, token, dir, nil, agent.PairOptions{})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	return cfg
}

// startAgent runs an agent until the returned stop func (also run at cleanup).
func startAgent(t *testing.T, cfg config.Config, reg *commands.Registry) (*agent.Agent, func()) {
	t.Helper()
	a := agent.New(cfg, reg, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var stopped atomic.Bool
	stop := func() {
		if stopped.Swap(true) {
			return
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(wait):
			t.Errorf("Run did not stop after cancel")
		}
	}
	t.Cleanup(stop)
	return a, stop
}

type stubProbe struct {
	Hostname string `json:"hostname"`
}

func stubRegistry() *commands.Registry {
	reg := commands.NewRegistry()
	reg.Register("probe", func(context.Context, json.RawMessage) (any, error) {
		return stubProbe{Hostname: "stub-host"}, nil
	})
	return reg
}

func waitID(t *testing.T, ch <-chan int64, what string) int64 {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		return 0
	}
}

func certSerial(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s: no PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%032x", cert.SerialNumber)
}

func TestPairThenConnectThenProbe(t *testing.T) {
	e := newEnv(t)
	id, token := e.newToken(t, 0)
	dir := filepath.Join(t.TempDir(), "agent")
	cfg := e.pair(t, token, dir)

	if cfg.ServerID != id || cfg.PanelAddr != e.agentAddr {
		t.Fatalf("config = %+v, want server %d at %s", cfg, id, e.agentAddr)
	}
	for _, name := range []string{"agent.key", "agent.crt", "ca.crt", "agent.yaml"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, fi.Mode().Perm())
		}
	}
	srv, err := e.st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Paired || srv.CertSerial != certSerial(t, cfg.CertFile) || srv.Address != "127.0.0.1" {
		t.Fatalf("server after pairing = %+v", srv)
	}

	a, _ := startAgent(t, cfg, stubRegistry())
	if got := waitID(t, e.l.online, "OnOnline"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var res stubProbe
	if err := e.hub.Call(ctx, id, "probe", nil, &res); err != nil {
		t.Fatalf("call probe: %v", err)
	}
	if res.Hostname != "stub-host" {
		t.Fatalf("probe result = %+v", res)
	}
	var perr *proto.Error
	if err := e.hub.Call(ctx, id, "rm -rf /", nil, nil); !errors.As(err, &perr) || perr.Code != proto.CodeUnknownCommand {
		t.Fatalf("unregistered command: err = %v", err)
	}

	select {
	case se := <-e.l.events:
		var p map[string]string
		if se.serverID != id || se.ev.Type != "hello" || json.Unmarshal(se.ev.Payload, &p) != nil || p["agent_version"] == "" {
			t.Fatalf("first event = %d %+v %s", se.serverID, se.ev, se.ev.Payload)
		}
	case <-time.After(wait):
		t.Fatal("no hello event")
	}

	if err := a.Emit(proto.Event{Type: "job.log", JobID: "j1"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	select {
	case se := <-e.l.events:
		if se.ev.Type != "job.log" || se.ev.JobID != "j1" {
			t.Fatalf("emitted event = %+v", se.ev)
		}
	case <-time.After(wait):
		t.Fatal("emitted event not received")
	}
}

func TestEmitOfflineBeforeRun(t *testing.T) {
	a := agent.New(config.Config{PanelAddr: "127.0.0.1:1", ServerID: 1}, commands.NewRegistry(), slog.New(slog.DiscardHandler))
	if err := a.Emit(proto.Event{Type: "x"}); !errors.Is(err, agent.ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
}

func TestPairRejectsUsedToken(t *testing.T) {
	e := newEnv(t)
	_, token := e.newToken(t, 0)
	e.pair(t, token, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	dir := t.TempDir()
	_, err := agent.Pair(ctx, e.pairURL, token, dir, nil, agent.PairOptions{})
	if err == nil || !strings.Contains(err.Error(), "token ditolak") {
		t.Fatalf("second pair: err = %v, want token ditolak", err)
	}
	if _, err := config.Load(dir); !errors.Is(err, config.ErrNotPaired) {
		t.Fatalf("rejected pairing wrote a config: %v", err)
	}

	_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(proto.PairRequest{Token: token, Hostname: "h", Arch: "amd64", CSRPEM: string(csrPEM)})
	resp, err := http.Post(e.pairURL+"/agent/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if resp.StatusCode != http.StatusForbidden || json.NewDecoder(resp.Body).Decode(&out) != nil || out["error"] != "token ditolak" {
		t.Fatalf("raw reuse: status %d body %v", resp.StatusCode, out)
	}
}

// TestPairRefusesLocalServer: the panel host's own server (cert serial
// "local") must never be taken over by an agent pairing with a token.
func TestPairRefusesLocalServer(t *testing.T) {
	e := newEnv(t)
	srv, err := e.st.CreateServer("host-panel")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkPaired(srv.ID, "local", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	_, token := e.newToken(t, srv.ID)

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if _, err := agent.Pair(ctx, e.pairURL, token, t.TempDir(), nil, agent.PairOptions{}); err == nil || !strings.Contains(err.Error(), "token ditolak") {
		t.Fatalf("pairing the local server: err = %v, want token ditolak", err)
	}
	got, err := e.st.GetServer(srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CertSerial != "local" || got.Address != "127.0.0.1" {
		t.Fatalf("local server after refused pairing = %+v", got)
	}
}

func TestPairHandlerMethodAndSize(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.pairURL + "/agent/pair")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}

	big := `{"token":"` + strings.Repeat("A", 70<<10) + `"}`
	resp, err = http.Post(e.pairURL+"/agent/pair", "application/json", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", resp.StatusCode)
	}
}

// assertNeverOnline fails if any OnOnline arrives within d.
func assertNeverOnline(t *testing.T, e *testEnv, d time.Duration) {
	t.Helper()
	select {
	case id := <-e.l.online:
		t.Fatalf("server %d went online, want rejected", id)
	case <-time.After(d):
	}
}

func TestWSRejectsDeletedServer(t *testing.T) {
	e := newEnv(t)
	id, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	if err := e.st.DeleteServer(id); err != nil {
		t.Fatal(err)
	}

	_, stop := startAgent(t, cfg, stubRegistry())
	assertNeverOnline(t, e, 2*time.Second)
	if n := e.wsHits.Load(); n < 1 {
		t.Fatalf("agent made no connection attempt in 2 s")
	}
	stop()
}

func TestWSRejectsStaleCert(t *testing.T) {
	e := newEnv(t)
	id, token1 := e.newToken(t, 0)
	cfg1 := e.pair(t, token1, filepath.Join(t.TempDir(), "first"))
	_, token2 := e.newToken(t, id)
	cfg2 := e.pair(t, token2, filepath.Join(t.TempDir(), "second"))

	srv, err := e.st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if srv.CertSerial != certSerial(t, cfg2.CertFile) {
		t.Fatalf("stored serial %s is not the second cert's", srv.CertSerial)
	}

	_, stop1 := startAgent(t, cfg1, stubRegistry())
	deadline := time.Now().Add(wait)
	for e.wsHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if e.wsHits.Load() == 0 {
		t.Fatal("first agent never reached the listener")
	}
	assertNeverOnline(t, e, 500*time.Millisecond)
	stop1()

	startAgent(t, cfg2, stubRegistry())
	if got := waitID(t, e.l.online, "OnOnline with second cert"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}
}

func TestAgentReconnects(t *testing.T) {
	e := newEnv(t)
	_, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	startAgent(t, cfg, stubRegistry())

	id := waitID(t, e.l.online, "first OnOnline")
	var conn *hub.WSConn
	select {
	case conn = <-e.attached:
	case <-time.After(wait):
		t.Fatal("no attached conn")
	}
	conn.Close()

	if got := waitID(t, e.l.offline, "OnOffline"); got != id {
		t.Fatalf("offline id = %d, want %d", got, id)
	}
	if got := waitID(t, e.l.online, "OnOnline after reconnect"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var res stubProbe
	if err := e.hub.Call(ctx, id, "probe", nil, &res); err != nil || res.Hostname != "stub-host" {
		t.Fatalf("call after reconnect: %+v %v", res, err)
	}
}

func TestWSLogsReplacedConnection(t *testing.T) {
	e := newEnv(t)
	id, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	startAgent(t, cfg, stubRegistry())
	waitID(t, e.l.online, "first OnOnline")
	if strings.Contains(e.logs.String(), "replaced") {
		t.Fatalf("first connection logged as a replacement:\n%s", e.logs.String())
	}

	startAgent(t, cfg, stubRegistry()) // a cloned server with the same certificate
	if got := waitID(t, e.l.online, "second OnOnline"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}
	out := e.logs.String()
	if !strings.Contains(out, `level=WARN msg="agent connection replaced an existing one`) ||
		!strings.Contains(out, "remote=127.0.0.1:") {
		t.Fatalf("replacement not logged at Warn with the remote address:\n%s", out)
	}
}

func TestPairRejectsMalformedCSRWithoutConsumingToken(t *testing.T) {
	e := newEnv(t)
	id, token := e.newToken(t, 0)

	body, _ := json.Marshal(proto.PairRequest{Token: token, Hostname: "h", Arch: "amd64",
		CSRPEM: "-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n"})
	resp, err := http.Post(e.pairURL+"/agent/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if resp.StatusCode != http.StatusBadRequest || json.NewDecoder(resp.Body).Decode(&out) != nil || out["error"] != "csr tidak valid" {
		t.Fatalf("malformed csr: status %d body %v", resp.StatusCode, out)
	}

	cfg := e.pair(t, token, t.TempDir())
	if cfg.ServerID != id {
		t.Fatalf("pair after malformed csr: server %d, want %d", cfg.ServerID, id)
	}
}

func TestServeAgentsWaitsForAgentHandlers(t *testing.T) {
	e := newEnv(t)
	e.l.offlineDelay = 300 * time.Millisecond // a slow listener, e.g. a store write
	_, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	startAgent(t, cfg, stubRegistry())
	id := waitID(t, e.l.online, "OnOnline")

	if err := e.stopPanel(); err != nil {
		t.Fatalf("ServeAgents: %v", err)
	}
	select {
	case got := <-e.l.offline:
		if got != id {
			t.Fatalf("offline id = %d, want %d", got, id)
		}
	default:
		t.Fatal("ServeAgents returned before the agent handler reported OnOffline")
	}
}

func TestAgentStopsDespiteCommandIgnoringCtx(t *testing.T) {
	e := newEnv(t)
	_, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())

	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // runs after the agent is stopped
	started := make(chan struct{}, 1)
	reg := commands.NewRegistry()
	reg.Register("hang", func(context.Context, json.RawMessage) (any, error) {
		started <- struct{}{}
		<-release // deliberately ignores ctx
		return nil, nil
	})
	_, stop := startAgent(t, cfg, reg)
	id := waitID(t, e.l.online, "OnOnline")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go e.hub.Call(ctx, id, "hang", nil, nil)
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("hang command never started")
	}

	begin := time.Now()
	stop()
	if d := time.Since(begin); d > 4*time.Second {
		t.Fatalf("Run took %v to stop with a command ignoring ctx", d)
	}
}

// TestAgentListenerRefusesHTTP2 (SEC-20): agents only speak HTTP/1.1, so a
// client offering h2 must not get it.
func TestAgentListenerRefusesHTTP2(t *testing.T) {
	e := newEnv(t)
	_, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tlsCfg, err := pki.AgentClientTLS(read(cfg.CertFile), read(cfg.KeyFile), read(cfg.CAFile))
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: wait}, "tcp", e.agentAddr, tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := conn.ConnectionState().NegotiatedProtocol; p == "h2" {
		t.Fatalf("NegotiatedProtocol = %q, want HTTP/1.1 only", p)
	}
}

// TestRepairDisconnectsOldAgent (SEC-03): once a new agent pairs for a
// server, the connection made with the old certificate is cut at once (it
// does not wait for the new agent), the old certificate cannot reconnect,
// and the new agent is accepted.
func TestRepairDisconnectsOldAgent(t *testing.T) {
	e := newEnv(t)
	id, token1 := e.newToken(t, 0)
	cfg1 := e.pair(t, token1, filepath.Join(t.TempDir(), "old"))
	startAgent(t, cfg1, stubRegistry())
	if got := waitID(t, e.l.online, "old agent online"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}

	_, token2 := e.newToken(t, id)
	cfg2 := e.pair(t, token2, filepath.Join(t.TempDir(), "new"))
	if got := waitID(t, e.l.offline, "old agent cut after re-pairing"); got != id {
		t.Fatalf("offline id = %d, want %d", got, id)
	}
	if e.hub.IsOnline(id) {
		t.Fatal("server still online through the old agent after re-pairing")
	}
	// The old agent keeps retrying with its certificate and is refused.
	hits := e.wsHits.Load()
	deadline := time.Now().Add(wait)
	for e.wsHits.Load() == hits && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assertNeverOnline(t, e, 500*time.Millisecond)

	startAgent(t, cfg2, stubRegistry())
	if got := waitID(t, e.l.online, "new agent online"); got != id {
		t.Fatalf("online id = %d, want %d", got, id)
	}
}

// TestPairRejectsNonP256KeyWithoutConsumingToken (SEC-18).
func TestPairRejectsNonP256KeyWithoutConsumingToken(t *testing.T) {
	e := newEnv(t)
	_, token := e.newToken(t, 0)
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	body, _ := json.Marshal(proto.PairRequest{Token: token, Hostname: "h", Arch: "amd64", CSRPEM: string(csr)})
	resp, err := http.Post(e.pairURL+"/agent/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "kunci CSR harus ECDSA P-256") {
		t.Fatalf("status %d body %s, want 400 with the key policy message", resp.StatusCode, b)
	}
	if _, err := e.st.PeekPairToken(token, time.Now()); err != nil {
		t.Fatalf("token consumed by a rejected CSR: %v", err)
	}
}

// TestAgentConnectionOutlivesReadTimeout (SEC-10): the agent listener's
// ReadTimeout does not touch an upgraded WebSocket.
func TestAgentConnectionOutlivesReadTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the read timeout")
	}
	t.Cleanup(agentapi.SetReadTimeout(time.Second))
	e := newEnv(t)
	id, token := e.newToken(t, 0)
	cfg := e.pair(t, token, t.TempDir())
	startAgent(t, cfg, stubRegistry())
	waitID(t, e.l.online, "agent online")
	time.Sleep(2500 * time.Millisecond)
	select {
	case got := <-e.l.offline:
		t.Fatalf("server %d went offline within the read timeout", got)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var res stubProbe
	if err := e.hub.Call(ctx, id, "probe", nil, &res); err != nil || res.Hostname != "stub-host" {
		t.Fatalf("call after the read timeout: %+v %v", res, err)
	}
}
