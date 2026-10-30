package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	if code := getStatus(t, base+"/agent/install?token=x"); code != http.StatusForbidden {
		t.Fatalf("GET /agent/install with a bad token = %d, want 403", code)
	}
	if code := getStatus(t, base+"/agent/bin/amd64?token=x"); code != http.StatusForbidden {
		t.Fatalf("GET /agent/bin/amd64 with a bad token = %d, want 403", code)
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

// TestAgentInstallEndpoints fetches the agent install script and binaries
// from a running panel, as "curl ... | sudo bash" on a new server does.
func TestAgentInstallEndpoints(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "http://" + uiAddr

	srv, err := p.Store.CreateServer("db-baru")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	script := getBody(t, base+"/agent/install?token="+tok.Token)
	for _, want := range []string{
		`curl -fsSL "` + p.PublicURL() + `/agent/bin/${arch}?token=` + tok.Token + `"`,
		`agent pair --panel "` + p.PublicURL() + `" --token "` + tok.Token + `"`,
		p.AgentAddr(),
		"systemctl enable --now dbpanel-agent",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install script does not contain %q", want)
		}
	}

	// Binaries come from <data dir>/bin.
	binDir := filepath.Join(o.DataDir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	const fake = "dbpanel arm64 build"
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), []byte(fake), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getBody(t, base+"/agent/bin/arm64?token="+tok.Token); got != fake {
		t.Fatalf("GET /agent/bin/arm64 = %q, want %q", got, fake)
	}

	// Serving the script and binaries does not use the token up.
	if _, err := p.Store.PeekPairToken(tok.Token, time.Now()); err != nil {
		t.Fatalf("token after downloads: %v", err)
	}
}

// getBody GETs url, requires 200 and returns the body.
func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %q, want 200", url, resp.StatusCode, b)
	}
	return string(b)
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
	if _, err := os.Stat(filepath.Join(o.ConfigDir, "admin-initial-password")); !os.IsNotExist(err) {
		t.Fatalf("admin-initial-password still exists after reset (stat err %v)", err)
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

func TestNewAdoptsExistingHostnameServer(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	// A crash between CreateServer and saving local_server_id leaves this row.
	if err := os.MkdirAll(o.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(o.DataDir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := st.CreateServer(host)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	p := newPanel(t, o)
	if p.localID != orphan.ID {
		t.Fatalf("local id = %d, want the existing row %d", p.localID, orphan.ID)
	}
	servers, err := p.Store.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1: %+v", len(servers), servers)
	}
	if srv := servers[0]; srv.ID != orphan.ID || !srv.Paired || srv.CertSerial != "local" {
		t.Fatalf("server = %+v, want row %d paired with serial local", srv, orphan.ID)
	}
	if v, err := p.Store.GetSetting("local_server_id"); err != nil || v != strconv.FormatInt(orphan.ID, 10) {
		t.Fatalf("local_server_id = %q, %v; want %d", v, err, orphan.ID)
	}
}

func TestNewKeepsPairedRemoteWithHostname(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(o.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(o.DataDir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := st.CreateServer(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPaired(remote.ID, "abc", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	for range 2 { // the second start must adopt the -panel row, not add a third
		p, err := New(o)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		got, err := p.Store.GetServer(remote.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != host || !got.Paired || got.CertSerial != "abc" || got.Address != "10.0.0.5" {
			t.Fatalf("remote server changed: %+v", got)
		}
		local, err := p.Store.GetServer(p.localID)
		if err != nil {
			t.Fatal(err)
		}
		if local.ID == remote.ID || local.Name != host+"-panel" || !local.Paired || local.CertSerial != "local" {
			t.Fatalf("local server = %+v, want a separate row %q paired as local", local, host+"-panel")
		}
		servers, err := p.Store.ListServers()
		if err != nil {
			t.Fatal(err)
		}
		if len(servers) != 2 {
			t.Fatalf("got %d servers, want 2: %+v", len(servers), servers)
		}
		// Forget the setting so the next start goes through adoption again.
		if err := p.Store.SetSetting("local_server_id", ""); err != nil {
			t.Fatal(err)
		}
		p.Close()
	}
}

// eventRecorder records what the panel publishes to browsers.
type eventRecorder struct {
	mu     sync.Mutex
	events []string // "event id online", online only for server_status
}

func (r *eventRecorder) publish(event string, data any) {
	m, _ := data.(map[string]any)
	line := fmt.Sprintf("%s %v %v", event, m["id"], m["online"])
	r.mu.Lock()
	r.events = append(r.events, line)
	r.mu.Unlock()
}

func (r *eventRecorder) count(line string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == line {
			n++
		}
	}
	return n
}

func TestProbeLoopReprobesOnlineServers(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	p.probeInterval = 50 * time.Millisecond
	rec := &eventRecorder{}
	broker := p.publish
	p.publish = func(event string, data any) {
		rec.publish(event, data)
		broker(event, data)
	}
	startPanel(t, p)

	online := fmt.Sprintf("server_status %d true", p.localID)
	probed := fmt.Sprintf("server_probe %d <nil>", p.localID)
	// One probe comes from OnOnline; at least two more need the loop.
	waitFor(t, "three probes of the local server", func() bool { return rec.count(probed) >= 3 })
	if n := rec.count(online); n != 1 {
		t.Fatalf("got %d %q events, want 1", n, online)
	}
}
