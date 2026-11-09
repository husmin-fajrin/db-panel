package panel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
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
	trustPanel(p)
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
	if srv.Paired || srv.CertSerial != "" || srv.Address != o.PublicHost {
		t.Fatalf("local server = %+v, want unpaired at the public host %s", srv, o.PublicHost)
	}
	// Its agent is a separate process that pairs with the token file.
	tok, pin := readLocalPairToken(t, o)
	if pt, found, err := p.Store.ActivePairToken(srv.ID, time.Now()); err != nil || !found || pt.Token != tok {
		t.Fatalf("active token = %+v, %v, %v; want the one in local-pair-token", pt, found, err)
	} else if ttl := time.Until(pt.ExpiresAt); ttl > 15*time.Minute || ttl < 14*time.Minute {
		t.Fatalf("local token expires in %v, want 15 minutes", ttl)
	}
	if pin != p.certInfo().Pin {
		t.Fatalf("pin in local-pair-token = %q, want the web certificate's %q", pin, p.certInfo().Pin)
	}
	if fi, err := os.Stat(filepath.Join(o.ConfigDir, "panel.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("panel.key: %v, %v", fi, err)
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
	startLocalAgent(t, p)

	if got, want := p.PublicURL(), "https://127.0.0.1:"+portOf(t, uiAddr); got != want {
		t.Fatalf("PublicURL = %q, want %q", got, want)
	}
	base := "https://" + uiAddr
	if code := getStatus(t, base+"/login"); code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", code)
	}
	if code := getStatus(t, base+"/agent/install?token=x"); code != http.StatusForbidden {
		t.Fatalf("GET /agent/install with a bad token = %d, want 403", code)
	}
	if code := getStatus(t, base+"/agent/bin/amd64?token=x"); code != http.StatusForbidden {
		t.Fatalf("GET /agent/bin/amd64 with a bad token = %d, want 403", code)
	}
	resp, err := testClient.Post(base+"/agent/pair", "application/json", strings.NewReader("{"))
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
	cfg, err := agent.Pair(ctx, p.PublicURL(), tok.Token, t.TempDir(), nil, agent.PairOptions{WebPin: p.certInfo().Pin})
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
	base := "https://" + uiAddr

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
		"PANEL_URL='" + p.PublicURL() + "'\n",
		"AGENT_ADDR='" + p.AgentAddr() + "'\n",
		"TOKEN='" + tok.Token + "'\n",
		`DBPANEL_TOKEN="$TOKEN" DBPANEL_WEB_PIN="$WEB_PIN" "$BIN" agent pair --panel "$PANEL_URL" --ca-sha256 "$CA_SHA256" --agent-addr "$AGENT_ADDR"`,
		"CA_SHA256='" + agentapi.CAFingerprint(p.CA.CAPEM()) + "'\n",
		"WEB_PIN='" + p.certInfo().Pin + "'\n",
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

// pairCommandRE matches the pairing command on a server page (unescaped).
var pairCommandRE = regexp.MustCompile(`^f=\$\(mktemp\) && curl -fsSL -k --pinnedpubkey (sha256//[A-Za-z0-9+/=]{44}) --max-redirs 0 -K - <<<'url = "([^"]+)"' -o "\$f" && ` +
	`echo "([0-9a-f]{64})  \$f" \| sha256sum -c - && sudo bash "\$f"$`)

// TestPairingCommandContainsScriptHash (SEC-23): the hash in the command
// the UI shows is the sha256 of the script GET /agent/install serves for
// that token, so the operator's shell can refuse a script swapped on the
// way, and the script it vouches for pins the CA and the binary hash.
func TestPairingCommandContainsScriptHash(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "https://" + uiAddr

	// An arm64 build in <data dir>/bin: its hash goes into the script too.
	binDir := filepath.Join(o.DataDir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), []byte("dbpanel arm64"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := p.Store.CreateServer("db-hash")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	page := getBodyWith(t, base+"/servers/"+strconv.FormatInt(srv.ID, 10), login(t, o, base))
	code := regexp.MustCompile(`(?s)<code x-ref="perintah">(.*?)</code>`).FindStringSubmatch(page)
	if code == nil {
		t.Fatalf("no pairing command on the server page:\n%s", page)
	}
	m := pairCommandRE.FindStringSubmatch(html.UnescapeString(code[1]))
	if m == nil {
		t.Fatalf("no pairing command with a script hash on the server page:\n%s", page)
	}
	if m[1] != p.certInfo().Pin {
		t.Fatalf("command pin = %q, want the panel certificate's %q", m[1], p.certInfo().Pin)
	}
	if want := p.PublicURL() + "/agent/install?token=" + tok.Token; m[2] != want {
		t.Fatalf("command URL = %q, want %q", m[2], want)
	}
	script := getBody(t, m[2])
	sum := sha256.Sum256([]byte(script))
	if got := hex.EncodeToString(sum[:]); got != m[3] {
		t.Fatalf("command says the script hash is %s, the served script hashes to %s", m[3], got)
	}
	armSum := sha256.Sum256([]byte("dbpanel arm64"))
	for _, want := range []string{
		"CA_SHA256='" + agentapi.CAFingerprint(p.CA.CAPEM()) + "'\n",
		"WEB_PIN='" + p.certInfo().Pin + "'\n",
		"BIN_SHA256_ARM64='" + hex.EncodeToString(armSum[:]) + "'\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not contain %q", want)
		}
	}
}

// TestPairRejectsCAMismatch (SEC-23): a pairing answer carrying a CA other
// than the pinned one (an on-path attacker's) is refused and nothing is
// written.
func TestPairRejectsCAMismatch(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	startPanel(t, p)
	srv, err := p.Store.CreateServer("db-ca")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pin := agentapi.CAFingerprint(p.CA.CAPEM())
	wrong := []byte(pin)
	// One hex digit changed.
	if wrong[0] == '0' {
		wrong[0] = '1'
	} else {
		wrong[0] = '0'
	}
	dir := t.TempDir()
	_, err = agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil, agent.PairOptions{CASHA256: string(wrong), WebPin: p.certInfo().Pin})
	if err == nil || !strings.Contains(err.Error(), "CA panel tidak cocok dengan yang diharapkan") {
		t.Fatalf("Pair with a wrong pin: err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("refused pairing wrote %v", entries)
	}

	// The right pin (any case) pairs.
	tok, err = p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil,
		agent.PairOptions{CASHA256: strings.ToUpper(pin), WebPin: p.certInfo().Pin}); err != nil {
		t.Fatalf("Pair with the right pin: %v", err)
	}
}

// TestPairPinsAgentAddr (SEC-23): the agent dials the address from the
// verified install script, not the one in the unauthenticated pairing
// answer.
func TestPairPinsAgentAddr(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	startPanel(t, p)
	srv, err := p.Store.CreateServer("db-addr")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const pinned = "192.0.2.77:8443"
	dir := t.TempDir()
	cfg, err := agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil,
		agent.PairOptions{CASHA256: agentapi.CAFingerprint(p.CA.CAPEM()), AgentAddr: pinned, WebPin: p.certInfo().Pin})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PanelAddr != pinned {
		t.Fatalf("PanelAddr = %q, want %q (pinned), panel said %q", cfg.PanelAddr, pinned, p.AgentAddr())
	}
	saved, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PanelAddr != pinned {
		t.Fatalf("saved PanelAddr = %q, want %q", saved.PanelAddr, pinned)
	}
}

// getBodyWith GETs url with cookie c, requires 200 and returns the body.
func getBodyWith(t *testing.T, url string, c *http.Cookie) string {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	return string(b)
}

// getBody GETs url, requires 200 and returns the body.
func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := testClient.Get(url)
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
	resp, err := testClient.Get(url)
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
	if srv := servers[0]; srv.ID != orphan.ID || srv.Paired || srv.CertSerial != "" || srv.Address != o.PublicHost {
		t.Fatalf("server = %+v, want row %d unpaired at the public host", srv, orphan.ID)
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
		if local.ID == remote.ID || local.Name != host+"-panel" || local.Paired || local.CertSerial != "" {
			t.Fatalf("local server = %+v, want a separate unpaired row %q", local, host+"-panel")
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
	startLocalAgent(t, p)

	online := fmt.Sprintf("server_status %d true", p.localID)
	probed := fmt.Sprintf("server_probe %d <nil>", p.localID)
	// One probe comes from OnOnline; at least two more need the loop.
	waitFor(t, "three probes of the local server", func() bool { return rec.count(probed) >= 3 })
	if n := rec.count(online); n != 1 {
		t.Fatalf("got %d %q events, want 1", n, online)
	}
}

// freeAddr returns a 127.0.0.1 address with a port that was free a moment
// ago, so a restarted panel can listen where its agents expect it.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestPanelRestartKeepsRemoteAgent restarts a panel that has a paired remote
// agent: the restart must not create a second local server, and the agent
// must reconnect on its own.
func TestPanelRestartKeepsRemoteAgent(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	o.AgentAddr = freeAddr(t)
	o.HTTPAddr = freeAddr(t)

	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	_, stop1 := startPanel(t, p1)
	srv, err := p1.Store.CreateServer("remote-1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p1.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg, err := agent.Pair(ctx, p1.PublicURL(), tok.Token, t.TempDir(), nil, agent.PairOptions{WebPin: p1.certInfo().Pin})
	if err != nil {
		cancel()
		t.Fatalf("Pair: %v", err)
	}
	reg := commands.NewRegistry()
	probe.Register(reg, probe.RealSystem(), "remote-test")
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.New(cfg, reg, slog.New(slog.DiscardHandler)).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-agentDone
	})
	waitFor(t, "the agent to connect", func() bool { return p1.Hub.IsOnline(srv.ID) })
	localID := p1.localID
	stop1()
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}

	p2 := newPanel(t, o)
	if p2.localID != localID {
		t.Fatalf("local server id changed across restart: %d -> %d", localID, p2.localID)
	}
	startPanel(t, p2)
	// The agent backs off 1, 2, 4 ... s between attempts; it was dropped
	// once, so it is back within a few seconds.
	deadline := time.Now().Add(20 * time.Second)
	for !p2.Hub.IsOnline(srv.ID) {
		if time.Now().After(deadline) {
			t.Fatal("agent did not reconnect after the panel restarted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	servers, err := p2.Store.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers after restart = %+v, want the local one and remote-1", servers)
	}
}

// login logs in to the panel at base with the initial password and returns
// the session cookie.
func login(t *testing.T, o Options, base string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {"admin"}, "password": {readInitialPassword(t, o)}}
	req, err := http.NewRequest("POST", base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SecureCookieName {
			return c
		}
	}
	t.Fatalf("login: status %d, no session cookie", resp.StatusCode)
	return nil
}

var noRedirect = &http.Client{Transport: testTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// getWith GETs url with cookie c, without following redirects.
func getWith(t *testing.T, url string, c *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// TestResetPasswordEndsSessions (SEC-04): reset-password runs in another
// process, yet a session from before it stops working in the running panel.
func TestResetPasswordEndsSessions(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	p.epoch.ttl = 0 // read the epoch on every request instead of every 5 s
	uiAddr, _ := startPanel(t, p)
	base := "https://" + uiAddr
	c := login(t, o, base)
	if resp := getWith(t, base+"/servers", c); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /servers before reset = %d, want 200", resp.StatusCode)
	}

	var stdout, stderr bytes.Buffer
	if code := Main([]string{"panel", "reset-password", "--data-dir", o.DataDir, "--config-dir", o.ConfigDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("reset-password exit %d: %s", code, stderr.String())
	}
	resp := getWith(t, base+"/servers", c)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("GET /servers with the pre-reset cookie = %d %q, want 303 to /login",
			resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestSessionEpochCache: the epoch is re-read at most every ttl.
func TestSessionEpochCache(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	now := time.Unix(1_800_000_000, 0)
	e := &sessionEpoch{st: p.Store, log: slog.New(slog.DiscardHandler), ttl: 5 * time.Second,
		now: func() time.Time { return now }}
	if _, err := e.load(); err != nil {
		t.Fatal(err)
	}
	created := now.Add(-time.Minute)
	if !e.valid(created) {
		t.Fatal("session rejected without any reset")
	}
	if err := p.Store.BumpSessionEpoch(now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	if !e.valid(created) {
		t.Fatal("epoch re-read before the cache ttl elapsed")
	}
	now = now.Add(time.Second)
	if e.valid(created) {
		t.Fatal("session created before the reset still valid after the cache ttl")
	}
	if !e.valid(now.Add(time.Second)) {
		t.Fatal("session created after the reset rejected")
	}
}

// wantSecurityHeaders checks the baseline headers on an agent endpoint
// response (SEC-08); the web package tests the UI classes.
func wantSecurityHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()
	for k, v := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-store",
	} {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", what, k, got, v)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") ||
		!strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("%s: Content-Security-Policy = %q", what, csp)
	}
}

// TestAgentEndpointsSecurityHeaders: the pairing JSON, the install script
// and the binary carry the same baseline headers as the UI, and no-store.
func TestAgentEndpointsSecurityHeaders(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "https://" + uiAddr

	resp, err := testClient.Post(base+"/agent/pair", "application/json", strings.NewReader(`{"token":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("POST /agent/pair Content-Type = %q", ct)
	}
	wantSecurityHeaders(t, "POST /agent/pair", resp.Header)

	srv, err := p.Store.CreateServer("db-hdr")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp = getWith(t, base+"/agent/install?token="+tok.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /agent/install = %d", resp.StatusCode)
	}
	wantSecurityHeaders(t, "GET /agent/install", resp.Header)
	resp = getWith(t, base+"/agent/install?token=bad", nil)
	wantSecurityHeaders(t, "GET /agent/install (bad token)", resp.Header)

	binDir := filepath.Join(o.DataDir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = getWith(t, base+"/agent/bin/arm64?token="+tok.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /agent/bin/arm64 = %d", resp.StatusCode)
	}
	wantSecurityHeaders(t, "GET /agent/bin/arm64", resp.Header)
}

// TestHostAllowlist (SEC-14): a Host header that does not name the panel
// (a DNS name rebound to its address) gets 421, on the UI and /agent/*.
func TestHostAllowlist(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	o.PublicHost = "panel.rs.example"
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	port := portOf(t, uiAddr)
	do := func(host, path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", "https://"+uiAddr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	for _, host := range []string{
		"panel.rs.example:" + port, "PANEL.rs.example", "localhost:" + port,
		"127.0.0.1:" + port, "[::1]:" + port, "127.0.0.1",
	} {
		if resp := do(host, "/login"); resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q: GET /login = %d, want 200", host, resp.StatusCode)
		}
	}
	// Any address of a local interface is accepted too.
	for _, ip := range interfaceIPs() {
		if ip.IsLinkLocalUnicast() {
			continue
		}
		host := net.JoinHostPort(ip.String(), port)
		if resp := do(host, "/login"); resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q: GET /login = %d, want 200", host, resp.StatusCode)
		}
	}
	for _, path := range []string{"/login", "/agent/install?token=x", "/static/app.css"} {
		for _, host := range []string{"evil.example:" + port, "evil.example", "192.0.2.77:" + port, "panel.rs.example.evil.example"} {
			resp := do(host, path)
			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("Host %q: GET %s = %d, want 421", host, path, resp.StatusCode)
			}
		}
	}
	// The body explains what to do, in Indonesian.
	req, _ := http.NewRequest("GET", "https://"+uiAddr+"/login", nil)
	req.Host = "evil.example"
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Alamat host tidak dikenal panel") {
		t.Errorf("421 body = %q", b)
	}
	wantSecurityHeaders(t, "421", resp.Header)
}

// TestPublicHostValidated (SEC-09): the public host ends up in a root shell
// script, so only IP literals and plain host names are accepted, from the
// flag and from the saved setting.
func TestPublicHostValidated(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"127.0.0.1$(id)", "`id`", "a b", "host;id", "x'y", "[::1]", "", "a\nb",
		strings.Repeat("a", 254),
	} {
		if bad == "" {
			continue // empty means auto-detect
		}
		o := testOptions(t)
		o.PublicHost = bad
		p, err := New(o)
		if err == nil {
			p.Close()
			t.Errorf("New with --public-host %q succeeded", bad)
			continue
		}
		if !strings.Contains(err.Error(), "public host") || !strings.Contains(err.Error(), "tidak valid") {
			t.Errorf("--public-host %q: err = %v", bad, err)
		}
	}
	for _, good := range []string{"192.168.1.10", "::1", "fe80::1", "panel.rs-umum.local", "PANEL01"} {
		o := testOptions(t)
		o.PublicHost = good
		p, err := New(o)
		if err != nil {
			t.Errorf("New with --public-host %q: %v", good, err)
			continue
		}
		p.Close()
	}

	// A bad value already saved in the database (written by an older
	// version) is refused too, and a valid flag replaces it.
	o := testOptions(t)
	p := newPanel(t, o)
	if err := p.Store.SetSetting("public_host", "127.0.0.1$(id)"); err != nil {
		t.Fatal(err)
	}
	p.Close()
	o.PublicHost = ""
	if p, err := New(o); err == nil {
		p.Close()
		t.Fatal("New accepted a saved public host with $(id)")
	} else if !strings.Contains(err.Error(), "--public-host") {
		t.Errorf("saved bad host: err = %v, want a hint to use --public-host", err)
	}
	o.PublicHost = "10.0.0.5"
	p2, err := New(o)
	if err != nil {
		t.Fatalf("New with a valid flag over a bad saved host: %v", err)
	}
	p2.Close()
}

// TestDataFileModes (SEC-13): an existing 0755 data directory is tightened
// to 0700 and panel.db is 0600.
func TestDataFileModes(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(o.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newPanel(t, o)
	for path, want := range map[string]os.FileMode{
		o.DataDir:                            0o700,
		filepath.Join(o.DataDir, "panel.db"): 0o600,
		o.ConfigDir:                          0o700,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}
}

// bigProbeConn answers probe with a result over the stored size cap.
type bigProbeConn struct{}

func (bigProbeConn) Call(_ context.Context, _ string, _ any, result any) error {
	b, _ := json.Marshal(proto.ProbeResult{Hostname: strings.Repeat("x", 70<<10), AgentVersion: "evil"})
	return json.Unmarshal(b, result)
}
func (bigProbeConn) Close() error { return nil }

// TestOversizedProbeKeepsPrevious (SEC-11).
func TestOversizedProbeKeepsPrevious(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	var logs syncBuffer
	o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	p := newPanel(t, o)
	srv, _ := p.Store.CreateServer("db-besar")
	p.Store.MarkPaired(srv.ID, "serial", "10.0.0.3")
	if err := p.Store.UpdateProbe(srv.ID, proto.ProbeResult{Hostname: "lama"}, "v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	p.Hub.Attach(srv.ID, bigProbeConn{})
	p.probeOne(context.Background(), srv.ID)
	got, _ := p.Store.GetServer(srv.ID)
	if !strings.Contains(got.ProbeJSON, `"lama"`) || got.AgentVersion != "v1" {
		t.Fatalf("probe after oversized result = %q (agent %q), want the previous one", got.ProbeJSON, got.AgentVersion)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="probe result too large; keeping the previous one"`) {
		t.Fatalf("no warning logged:\n%s", logs.String())
	}
}

// TestAgentEventLogRateLimited (SEC-11): at most one debug line per server
// per 10 s, however fast an agent sends events.
func TestAgentEventLogRateLimited(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	var logs syncBuffer
	o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := newPanel(t, o)
	now := time.Unix(1_800_000_000, 0)
	p.now = func() time.Time { return now }
	count := func() int { return strings.Count(logs.String(), `msg="agent event"`) }
	for range 100 {
		p.onEvent(7, proto.Event{Type: "flood"})
	}
	p.onEvent(8, proto.Event{Type: "other"})
	if n := count(); n != 2 {
		t.Fatalf("event lines = %d, want 2 (one per server)", n)
	}
	now = now.Add(9 * time.Second)
	p.onEvent(7, proto.Event{Type: "flood"})
	if n := count(); n != 2 {
		t.Fatalf("event lines after 9 s = %d, want 2", n)
	}
	now = now.Add(time.Second)
	p.onEvent(7, proto.Event{Type: "flood"})
	if n := count(); n != 3 {
		t.Fatalf("event lines after 10 s = %d, want 3", n)
	}
	if strings.Contains(logs.String(), "level=INFO msg=\"agent event\"") {
		t.Fatal("agent events logged above debug level")
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes (log output).
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

// slowBody sends a request whose body never arrives and returns how long
// the server took to close the connection (or max if it did not).
func slowBody(t *testing.T, addr, reqLine string, max time.Duration) time.Duration {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	fmt.Fprintf(c, "%s HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: 10000\r\n\r\n{", reqLine)
	c.SetReadDeadline(time.Now().Add(max))
	io.Copy(io.Discard, c) // returns when the server closes (or the deadline hits)
	return time.Since(start)
}

// TestSlowBodyCut (SEC-10): a request body that never finishes is cut by
// the UI server's ReadTimeout on any route, not only on form posts, while
// the SSE stream stays open past that timeout.
func TestSlowBodyCut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for read timeouts")
	}
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	p.readTimeout = time.Second
	uiAddr, _ := startPanel(t, p)
	for _, line := range []string{"GET /login", "POST /agent/pair"} {
		if d := slowBody(t, uiAddr, line, 10*time.Second); d > 5*time.Second {
			t.Errorf("%s with a slow body held the connection %v, want it cut after ~1 s", line, d)
		}
	}

	c := login(t, o, "https://"+uiAddr)
	req, _ := http.NewRequest("GET", "https://"+uiAddr+"/events", nil)
	req.AddCookie(c)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(2500 * time.Millisecond) // well past the 1 s ReadTimeout
	p.publish("server_status", map[string]any{"id": 99, "online": true})
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: server_status") {
				got <- sc.Text()
				return
			}
		}
		got <- "stream ended: " + fmt.Sprint(sc.Err())
	}()
	select {
	case s := <-got:
		if s != "event: server_status" {
			t.Fatalf("SSE after the read timeout: %s", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE event after the read timeout")
	}
}

// TestHealthEventRoutedToMonitor: an agent's health event reaches the
// Monitor through the panel's listener.
func TestHealthEventRoutedToMonitor(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	b, err := json.Marshal(proto.HealthEvent{Seq: 1, Snapshot: proto.HealthSnapshot{AgentVersion: "9.9.9"}})
	if err != nil {
		t.Fatal(err)
	}
	p.onEvent(p.localID, proto.Event{Type: proto.EventHealth, Payload: b})
	v, ok := p.Monitor.View(p.localID)
	if !ok || v.Snap == nil || v.Snap.AgentVersion != "9.9.9" {
		t.Fatalf("view after the event = %+v, %v", v, ok)
	}
}

// retentionFixture fills a panel's store with rows on both sides of the
// default retention (90 days for closed alerts, 180 for status events) as
// of now.
type retentionFixture struct {
	oldClosed, recentClosed, oldOpen int64
}

func fillRetention(t *testing.T, p *Panel, now time.Time) retentionFixture {
	t.Helper()
	day := 24 * time.Hour
	srv, err := p.Store.CreateServer("db-lama")
	if err != nil {
		t.Fatal(err)
	}
	alert := func(kind string, opened time.Time, closedAgo time.Duration) int64 {
		t.Helper()
		id, err := p.Store.OpenAlert(store.Alert{Kind: kind, ServerID: srv.ID, OpenedAt: opened})
		if err != nil {
			t.Fatal(err)
		}
		if closedAgo > 0 {
			if err := p.Store.CloseAlert(id, now.Add(-closedAgo)); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	var f retentionFixture
	f.oldClosed = alert("down", now.Add(-200*day), 91*day)
	f.recentClosed = alert("lag", now.Add(-200*day), 89*day)
	f.oldOpen = alert("disk", now.Add(-500*day), 0)
	for _, ago := range []time.Duration{181 * day, 179 * day} {
		if _, err := p.Store.AddStatusEvent(store.StatusEvent{ServerID: srv.ID, TS: now.Add(-ago), Kind: "status", ToStatus: "synced"}); err != nil {
			t.Fatal(err)
		}
	}
	for i, exp := range []time.Duration{-time.Hour, time.Hour} {
		if err := p.Store.AddConfirmation(store.Confirmation{TokenHash: fmt.Sprintf("h%d", i), Username: "admin",
			Action: "delete", TargetKind: "server", TargetID: srv.ID, ExpiresAt: now.Add(exp)}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func alertIDs(t *testing.T, p *Panel) map[int64]bool {
	t.Helper()
	all, err := p.Store.ListAlerts(time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for _, a := range all {
		out[a.ID] = true
	}
	return out
}

func statusEventCount(t *testing.T, p *Panel) int {
	t.Helper()
	evs, err := p.Store.ListStatusEventsAll(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

// TestDailyPruneKeepsOpenAndRecent (spec 5.12): the daily pruning removes
// alerts closed more than alerts_retention_days ago, status events older
// than events_retention_days and expired confirmations; open alerts and
// newer rows stay. The settings change the cutoffs.
func TestDailyPruneKeepsOpenAndRecent(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	f := fillRetention(t, p, now)

	// The background loop runs the first pruning after pruneFirst.
	p.pruneFirst = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.retentionLoop(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for alertIDs(t, p)[f.oldClosed] && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	ids := alertIDs(t, p)
	if ids[f.oldClosed] || !ids[f.recentClosed] || !ids[f.oldOpen] {
		t.Fatalf("alerts after pruning = %v; want only the recent closed (%d) and the open (%d) one", ids, f.recentClosed, f.oldOpen)
	}
	if n := statusEventCount(t, p); n != 1 {
		t.Fatalf("status events after pruning = %d, want the 179-day-old one", n)
	}
	if n, _ := p.Store.PruneConfirmations(now.Add(2 * time.Hour)); n != 1 {
		t.Fatalf("confirmations left = %d, want only the unexpired one", n)
	}

	// Shorter retention from the settings.
	if err := p.Store.SetSetting(store.SettingAlertsRetentionDays, "30"); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.SetSetting(store.SettingEventsRetentionDays, "1"); err != nil {
		t.Fatal(err)
	}
	p.pruneOld()
	if ids := alertIDs(t, p); ids[f.recentClosed] || !ids[f.oldOpen] {
		t.Fatalf("alerts with 30-day retention = %v; want only the open one", ids)
	}
	if n := statusEventCount(t, p); n != 0 {
		t.Fatalf("status events with 1-day retention = %d, want 0", n)
	}
}

// TestPruneNeverTouchesAudit (spec 5.12): audit_log and job_log are never
// pruned, however old and however short the retention.
func TestPruneNeverTouchesAudit(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	old := now.Add(-3000 * 24 * time.Hour)
	if _, err := p.Store.AddAudit(store.AuditEntry{TS: old, Username: "admin", Action: "login", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "job-lama", Kind: "prepare-master", ServerID: p.localID, ParamsJSON: "{}", CreatedAt: old}
	if err := p.Store.CreateJob(job, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.AppendJobLog(job.ID, "info", "baris lama", old); err != nil {
		t.Fatal(err)
	}
	auditBefore, err := p.Store.ListAudit(0, time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{store.SettingAlertsRetentionDays, store.SettingEventsRetentionDays} {
		if err := p.Store.SetSetting(k, "1"); err != nil {
			t.Fatal(err)
		}
	}
	p.pruneOld()

	audit, err := p.Store.ListAudit(0, time.Time{}, 1000)
	if err != nil || len(audit) != len(auditBefore) || len(audit) == 0 {
		t.Fatalf("audit entries after pruning = %d (%v), want all %d", len(audit), err, len(auditBefore))
	}
	found := false
	for _, e := range audit {
		found = found || e.TS.Equal(old)
	}
	if !found {
		t.Fatal("the 3000-day-old audit entry was pruned")
	}
	lines, err := p.Store.JobLog(job.ID, 0, 0)
	if err != nil || len(lines) != 1 {
		t.Fatalf("job log after pruning = %+v, %v; want the old line", lines, err)
	}
}

// TestRetentionPrunesSamples (spec 5.7): the daily pruning deletes the
// health samples older than samples_retention_days (7 by default), and
// never touches audit_log or job_log.
func TestRetentionPrunesSamples(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	day := 24 * time.Hour
	for _, ago := range []time.Duration{8 * day, 6 * day, time.Hour} {
		if err := p.Store.AddHealthSample(store.HealthSample{ServerID: p.localID, TS: now.Add(-ago), MySQLOK: true}); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-3000 * day)
	if _, err := p.Store.AddAudit(store.AuditEntry{TS: old, Username: "admin", Action: "login", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "job-sampel", Kind: "prepare-master", ServerID: p.localID, ParamsJSON: "{}", CreatedAt: old}
	if err := p.Store.CreateJob(job, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.AppendJobLog(job.ID, "info", "baris lama", old); err != nil {
		t.Fatal(err)
	}
	auditBefore, err := p.Store.ListAudit(0, time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	has := func(since time.Time) bool {
		t.Helper()
		ok, err := p.Store.HasSamples(p.localID, since)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	samplesBetween := func(from, to time.Time) int {
		t.Helper()
		got, err := p.Store.LagSeries([]int64{p.localID}, from, to, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return len(got[p.localID])
	}

	p.pruneFirst = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.retentionLoop(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for samplesBetween(now.Add(-9*day), now.Add(-7*day)) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := samplesBetween(now.Add(-9*day), now); n != 2 {
		t.Fatalf("samples after the default 7-day pruning = %d, want the 6-day and 1-hour old ones", n)
	}

	if err := p.Store.SetSetting(store.SettingSamplesRetentionDays, "1"); err != nil {
		t.Fatal(err)
	}
	p.pruneOld()
	if n := samplesBetween(now.Add(-9*day), now); n != 1 || !has(now.Add(-2*time.Hour)) {
		t.Fatalf("samples with 1-day retention = %d, want the 1-hour old one", n)
	}

	audit, err := p.Store.ListAudit(0, time.Time{}, 1000)
	if err != nil || len(audit) != len(auditBefore) || len(audit) == 0 {
		t.Fatalf("audit entries after pruning = %d (%v), want all %d", len(audit), err, len(auditBefore))
	}
	lines, err := p.Store.JobLog(job.ID, 0, 0)
	if err != nil || len(lines) != 1 {
		t.Fatalf("job log after pruning = %+v, %v; want the old line", lines, err)
	}
}
