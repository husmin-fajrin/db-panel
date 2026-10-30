package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{
		DataDir:    filepath.Join(root, "data"),
		ConfigDir:  filepath.Join(root, "etc"),
		HTTPAddr:   "127.0.0.1:0",
		AgentAddr:  "127.0.0.1:0",
		PublicHost: "127.0.0.1",
		Logger:     slog.New(slog.DiscardHandler),
		Version:    "test",
	}
}

func newPanel(t *testing.T, o Options) *Panel {
	t.Helper()
	p, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func readInitialPassword(t *testing.T, o Options) string {
	t.Helper()
	path := filepath.Join(o.ConfigDir, "admin-initial-password")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read initial password: %v", err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("password file has no trailing newline: %q", b)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func TestNewCreatesAdminAndLocalServer(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)

	path := filepath.Join(o.ConfigDir, "admin-initial-password")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("password file mode = %v, want 0600", fi.Mode().Perm())
	}
	pw := readInitialPassword(t, o)
	if len(pw) != 16 {
		t.Fatalf("password length = %d, want 16", len(pw))
	}
	u, err := p.Store.GetUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(u.PasswordHash, pw) {
		t.Fatal("initial password does not match the stored admin hash")
	}

	servers, err := p.Store.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	srv := servers[0]
	if !srv.Paired || srv.CertSerial != "local" || srv.Address != "127.0.0.1" {
		t.Fatalf("local server = %+v, want paired with serial local at 127.0.0.1", srv)
	}
	id, err := p.Store.GetSetting("local_server_id")
	if err != nil {
		t.Fatal(err)
	}
	if id != strconv.FormatInt(srv.ID, 10) {
		t.Fatalf("local_server_id = %q, want %d", id, srv.ID)
	}
	if got, err := p.Store.GetSetting("public_host"); err != nil || got != "127.0.0.1" {
		t.Fatalf("public_host = %q, %v; want 127.0.0.1", got, err)
	}
}

func TestNewIsIdempotent(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	first := readInitialPassword(t, o)
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}

	p2 := newPanel(t, o)
	if n, err := p2.Store.CountUsers(); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v; want 1", n, err)
	}
	servers, err := p2.Store.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if got := readInitialPassword(t, o); got != first {
		t.Fatal("second New rewrote the initial password file")
	}
}

// startPanel runs p until the test ends and returns the bound UI address
// and a stop function that cancels Run and checks it returned cleanly.
func startPanel(t *testing.T, p *Panel) (uiAddr string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	stop = sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	t.Cleanup(stop)

	deadline := time.Now().Add(5 * time.Second)
	for uiAddr = p.UIAddr(); uiAddr == ""; uiAddr = p.UIAddr() {
		if time.Now().After(deadline) {
			t.Fatal("UIAddr stayed empty")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.AgentListenAddr() == "" {
		t.Fatal("AgentListenAddr is empty once UIAddr is set")
	}
	return uiAddr, stop
}

// waitFor polls cond every 20 ms until it holds or 5 s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunServesLoginAndProbesLocal(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	localID := p.localID
	uiAddr, stop := startPanel(t, p)

	if got, want := p.PublicURL(), "http://127.0.0.1:"+portOf(t, uiAddr); got != want {
		t.Fatalf("PublicURL = %q, want %q", got, want)
	}
	base := "http://" + uiAddr
	if code := getStatus(t, base+"/login"); code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", code)
	}
	if code := getStatus(t, base+"/agent/install?token=x"); code != http.StatusNotFound {
		t.Fatalf("GET /agent/install = %d, want 404", code)
	}
	if code := getStatus(t, base+"/agent/bin/amd64?token=x"); code != http.StatusNotFound {
		t.Fatalf("GET /agent/bin/amd64 = %d, want 404", code)
	}
	resp, err := http.Post(base+"/agent/pair", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /agent/pair with a bad body = %d, want 400 from the pair handler", resp.StatusCode)
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	var srv store.Server
	waitFor(t, "the local server's probe", func() bool {
		if srv, err = p.Store.GetServer(localID); err != nil {
			t.Fatal(err)
		}
		return srv.ProbeJSON != ""
	})
	var res proto.ProbeResult
	if err := json.Unmarshal([]byte(srv.ProbeJSON), &res); err != nil {
		t.Fatalf("decode ProbeJSON: %v", err)
	}
	if res.Hostname != host {
		t.Fatalf("probe hostname = %q, want %q", res.Hostname, host)
	}
	if srv.AgentVersion != "test" || srv.LastSeenAt == nil {
		t.Fatalf("server after probe = %+v, want agent version test and last seen set", srv)
	}
	if !p.Hub.IsOnline(localID) {
		t.Fatal("local server is not online in the hub")
	}

	stop()
	if ids := p.Hub.OnlineIDs(); len(ids) != 0 {
		t.Fatalf("online after shutdown: %v", ids)
	}
}

func TestRemoteAgentPairsAndIsProbed(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	startPanel(t, p)

	srv, err := p.Store.CreateServer("remote-1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg, err := agent.Pair(ctx, p.PublicURL(), tok.Token, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if cfg.PanelAddr != p.AgentAddr() {
		t.Fatalf("agent was told to dial %q, want %q", cfg.PanelAddr, p.AgentAddr())
	}

	reg := commands.NewRegistry()
	probe.Register(reg, probe.RealSystem(), "remote-test")
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.New(cfg, reg, slog.New(slog.DiscardHandler)).Run(ctx) }()
	stopAgent := sync.OnceFunc(func() {
		cancel()
		<-agentDone
	})
	defer stopAgent()

	waitFor(t, "the remote server's probe", func() bool {
		got, err := p.Store.GetServer(srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.AgentVersion == "remote-test" && got.ProbeJSON != ""
	})
	if !p.Hub.IsOnline(srv.ID) {
		t.Fatal("remote server is not online in the hub")
	}

	stopAgent()
	waitFor(t, "the remote server to go offline", func() bool { return !p.Hub.IsOnline(srv.ID) })
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		t.Fatalf("no port in %q", addr)
	}
	return addr[i+1:]
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	initial := readInitialPassword(t, o)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main([]string{"panel", "reset-password", "--data-dir", o.DataDir, "--config-dir", o.ConfigDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	const prefix = "Kata sandi baru untuk admin: "
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, prefix) {
		t.Fatalf("stdout = %q, want prefix %q", out, prefix)
	}
	printed := strings.TrimPrefix(out, prefix)
	if len(printed) != 16 {
		t.Fatalf("printed password %q is not 16 characters", printed)
	}

	st, err := store.Open(filepath.Join(o.DataDir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.GetUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(u.PasswordHash, printed) {
		t.Fatal("printed password does not match the stored hash")
	}
	if auth.CheckPassword(u.PasswordHash, initial) {
		t.Fatal("initial password still works after reset")
	}
}

func TestResetPasswordUnknownUser(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	var stdout, stderr bytes.Buffer
	code := Main([]string{"panel", "reset-password", "--username", "nobody", "--data-dir", o.DataDir}, &stdout, &stderr)
	if code != 1 || !strings.HasPrefix(stderr.String(), "kesalahan: ") || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestMainUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"panel", "--no-such-flag"},
		{"panel", "extra"},
		{"panel", "reset-password", "extra"},
	} {
		var stderr bytes.Buffer
		if code := Main(args, io.Discard, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, stderr.String())
		}
	}
}
