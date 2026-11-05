package panel

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// readLocalPairToken reads <ConfigDir>/local-pair-token, checking its mode
// and its "<token>\n<pin>\n" form.
func readLocalPairToken(t *testing.T, o Options) (token, pin string) {
	t.Helper()
	path := filepath.Join(o.ConfigDir, "local-pair-token")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("local-pair-token: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("local-pair-token mode = %v, want 0600", fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) != 3 || lines[0] == "" || !strings.HasPrefix(lines[1], "sha256//") || lines[2] != "" {
		t.Fatalf("local-pair-token = %q, want \"<token>\\n<pin>\\n\"", b)
	}
	return lines[0], lines[1]
}

// startLocalAgent pairs an in-process agent as the panel host's own server
// with the token the panel wrote (as the dbpanel-agent unit does) and runs
// it until the test ends.
func startLocalAgent(t *testing.T, p *Panel) {
	t.Helper()
	token, pin := readLocalPairToken(t, p.opts)
	ctx, cancel := context.WithCancel(context.Background())
	cfg, err := agent.Pair(ctx, p.PublicURL(), token, t.TempDir(), nil,
		agent.PairOptions{WebPin: pin, AgentAddr: p.AgentListenAddr()})
	if err != nil {
		cancel()
		t.Fatalf("pair the local agent: %v", err)
	}
	reg := commands.NewRegistry()
	probe.Register(reg, probe.RealSystem(), "test")
	done := make(chan error, 1)
	go func() { done <- agent.New(cfg, reg, slog.New(slog.DiscardHandler)).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the local agent", func() bool { return p.Hub.IsOnline(p.localID) })
}

// TestLocalServerPairsThroughToken: the panel writes a 15-minute token and
// its web pin for its own server; "agent pair --token-file" pairs with it
// (and deletes the file), the server keeps the public host as address, the
// panel deletes its copy, and a later start writes no new token.
func TestLocalServerPairsThroughToken(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	o.PublicHost = "192.0.2.7" // not where the agent connects from
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	trustPanel(p)
	uiAddr, stop := startPanel(t, p)
	token, pin := readLocalPairToken(t, o)

	// install.sh hands the file to the agent's directory.
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "local-pair-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"+pin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := agent.Main([]string{"pair", "--panel", "https://" + uiAddr, "--token-file", tokenFile, "--dir", dir},
		&stdout, &stderr); code != 0 {
		t.Fatalf("agent pair --token-file: exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("token file after pairing: %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.yaml")); err != nil {
		t.Fatalf("agent.yaml: %v", err)
	}
	if strings.Contains(stdout.String()+stderr.String(), token) {
		t.Fatal("the token was printed")
	}
	srv, err := p.Store.GetServer(p.localID)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Paired || srv.CertSerial == "" || srv.CertSerial == "local" || srv.Address != "192.0.2.7" {
		t.Fatalf("local server after pairing = %+v, want paired with a real serial at 192.0.2.7", srv)
	}
	if _, err := os.Stat(filepath.Join(o.ConfigDir, "local-pair-token")); !os.IsNotExist(err) {
		t.Fatalf("panel's local-pair-token after pairing: %v, want removed", err)
	}
	stop()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	p2 := newPanel(t, o)
	if p2.localID != srv.ID {
		t.Fatalf("local id after restart = %d, want %d", p2.localID, srv.ID)
	}
	if _, err := os.Stat(filepath.Join(o.ConfigDir, "local-pair-token")); !os.IsNotExist(err) {
		t.Fatalf("local-pair-token written for a paired local server: %v", err)
	}
	if got, _ := p2.Store.GetServer(srv.ID); got.CertSerial != srv.CertSerial || !got.Paired {
		t.Fatalf("local server after restart = %+v, want unchanged %+v", got, srv)
	}
}

// TestLegacyLocalServerNeedsPairing: a local server left paired as "local"
// by the embedded agent of earlier versions needs its agent paired: the
// start resets it and writes a token; an old token file is replaced.
func TestLegacyLocalServerNeedsPairing(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := p1.localID
	if err := p1.Store.MarkPaired(id, "local", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	old, _ := readLocalPairToken(t, o)
	p1.Close()

	p := newPanel(t, o)
	if p.localID != id {
		t.Fatalf("local id = %d, want %d", p.localID, id)
	}
	srv, err := p.Store.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Paired || srv.CertSerial != "" || srv.Address != o.PublicHost {
		t.Fatalf("legacy local server = %+v, want unpaired at the public host", srv)
	}
	token, _ := readLocalPairToken(t, o)
	if token == old {
		t.Fatal("the token was not renewed at the start")
	}
	if _, err := p.Store.ConsumePairToken(old, time.Now()); err != store.ErrTokenInvalid {
		t.Fatalf("old token still valid: %v", err)
	}
}
