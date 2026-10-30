package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestPairRefusesPanelConfigDir: "agent pair" into a directory holding the
// panel's CA key would overwrite the panel host's config; it must stop
// before writing anything (and before contacting any panel).
func TestPairRefusesPanelConfigDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), []byte("kunci"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main([]string{"pair", "--panel", "http://127.0.0.1:1", "--ca-sha256", testCAPin, "--token", "AAAAA-BBBBB-CCCCC", "--dir", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr.String())
	}
	want := "Direktori ini milik panel (ada ca.key). Jangan pasang agent di host panel; panel sudah punya agent tertanam."
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("pair wrote into the panel's directory: %v", entries)
	}
}

func TestClosedByPanel(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{websocket.CloseError{Code: websocket.StatusNormalClosure}, true},
		{websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "panel shutting down"}, true},
		{websocket.CloseError{Code: websocket.StatusPolicyViolation}, false},
		{errors.New("EOF"), false},
	} {
		if got := closedByPanel(tc.err); got != tc.want {
			t.Errorf("closedByPanel(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// fakePairPanel records the token of each pairing request and refuses it,
// so "agent pair" exits 1 after sending it.
func fakePairPanel(t *testing.T) (url string, tokens <-chan string) {
	t.Helper()
	ch := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proto.PairRequest
		json.NewDecoder(r.Body).Decode(&req)
		ch <- req.Token
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"token ditolak"}`))
	}))
	t.Cleanup(ts.Close)
	return ts.URL, ch
}

// testCAPin is a well-formed --ca-sha256 value.
var testCAPin = strings.Repeat("ab", 32)

// runPair runs "agent pair" with a CA pin and the given environment and stdin.
func runPair(t *testing.T, env map[string]string, in string, args ...string) (code int, stderr string, token string, sent bool) {
	t.Helper()
	return runPairNoPin(t, env, in, append([]string{"--ca-sha256", testCAPin}, args...)...)
}

// runPairNoPin is runPair without the default --ca-sha256.
func runPairNoPin(t *testing.T, env map[string]string, in string, args ...string) (code int, stderr string, token string, sent bool) {
	t.Helper()
	url, tokens := fakePairPanel(t)
	oldEnv, oldIn := getenv, stdin
	t.Cleanup(func() { getenv, stdin = oldEnv, oldIn })
	getenv = func(k string) string { return env[k] }
	stdin = strings.NewReader(in)
	var out, errb bytes.Buffer
	code = Main(append([]string{"pair", "--panel", url, "--dir", t.TempDir()}, args...), &out, &errb)
	select {
	case token = <-tokens:
		sent = true
	default:
	}
	return code, errb.String(), token, sent
}

const tokenWarning = "Peringatan: --token terlihat di daftar proses; gunakan DBPANEL_TOKEN"

// TestPairTokenFromEnv (SEC-02): the token comes from DBPANEL_TOKEN, not argv.
func TestPairTokenFromEnv(t *testing.T) {
	code, stderr, token, sent := runPair(t, map[string]string{"DBPANEL_TOKEN": "ENVTK-AAAAA-BBBBB"}, "")
	if code != 1 || !sent || token != "ENVTK-AAAAA-BBBBB" {
		t.Fatalf("code %d sent %v token %q stderr %q", code, sent, token, stderr)
	}
	if strings.Contains(stderr, "Peringatan") {
		t.Errorf("warning printed for the env token: %q", stderr)
	}
}

// TestPairTokenFromStdin (SEC-02): --token-stdin reads the first line.
func TestPairTokenFromStdin(t *testing.T) {
	code, stderr, token, sent := runPair(t, map[string]string{"DBPANEL_TOKEN": "IGNOR-EDXXX-XXXXX"},
		"  STDIN-AAAAA-BBBBB \nsisa\n", "--token-stdin")
	if code != 1 || !sent || token != "STDIN-AAAAA-BBBBB" {
		t.Fatalf("code %d sent %v token %q stderr %q", code, sent, token, stderr)
	}
	if code, stderr, _, sent := runPair(t, nil, "\n", "--token-stdin"); code != 2 || sent ||
		!strings.Contains(stderr, "token kosong") {
		t.Fatalf("empty stdin: code %d sent %v stderr %q", code, sent, stderr)
	}
}

// TestPairTokenFlagWarns (SEC-02): --token still works but warns.
func TestPairTokenFlagWarns(t *testing.T) {
	code, stderr, token, sent := runPair(t, nil, "", "--token", "FLAGT-AAAAA-BBBBB")
	if code != 1 || !sent || token != "FLAGT-AAAAA-BBBBB" {
		t.Fatalf("code %d sent %v token %q stderr %q", code, sent, token, stderr)
	}
	if !strings.Contains(stderr, tokenWarning) {
		t.Fatalf("stderr %q lacks %q", stderr, tokenWarning)
	}
}

// TestPairTokenMissingOrConflicting: no source, or two flags, is a usage error.
func TestPairTokenMissingOrConflicting(t *testing.T) {
	if code, stderr, _, sent := runPair(t, nil, ""); code != 2 || sent ||
		!strings.Contains(stderr, "DBPANEL_TOKEN") {
		t.Fatalf("no token: code %d sent %v stderr %q", code, sent, stderr)
	}
	if code, stderr, _, sent := runPair(t, nil, "X\n", "--token", "A", "--token-stdin"); code != 2 || sent {
		t.Fatalf("both flags: code %d sent %v stderr %q", code, sent, stderr)
	}
}

// TestCLIPairRequiresCAPin (SEC-23): without --ca-sha256 an on-path
// attacker could hand the agent its own CA, so "agent pair" refuses to run
// unless the operator explicitly opts out with --insecure-skip-ca-pin.
func TestCLIPairRequiresCAPin(t *testing.T) {
	env := map[string]string{"DBPANEL_TOKEN": "ENVTK-AAAAA-BBBBB"}
	code, stderr, _, sent := runPairNoPin(t, env, "")
	if code != 2 || sent || !strings.Contains(stderr, "--ca-sha256 wajib (lihat perintah pairing di panel)") {
		t.Fatalf("no pin: code %d sent %v stderr %q", code, sent, stderr)
	}
	for _, bad := range []string{"abc", strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
		if code, stderr, _, sent := runPairNoPin(t, env, "", "--ca-sha256", bad); code != 2 || sent {
			t.Errorf("--ca-sha256 %q: code %d sent %v stderr %q", bad, code, sent, stderr)
		}
	}
	if code, stderr, _, sent := runPairNoPin(t, env, "", "--ca-sha256", testCAPin, "--agent-addr", "tanpa-port"); code != 2 || sent {
		t.Errorf("bad --agent-addr: code %d sent %v stderr %q", code, sent, stderr)
	}

	code, stderr, token, sent := runPairNoPin(t, env, "", "--insecure-skip-ca-pin")
	if code != 1 || !sent || token != "ENVTK-AAAAA-BBBBB" {
		t.Fatalf("skip pin: code %d sent %v token %q stderr %q", code, sent, token, stderr)
	}
	if !strings.Contains(stderr, "Peringatan: verifikasi CA panel dimatikan") {
		t.Fatalf("skip pin: stderr %q lacks the warning", stderr)
	}

	code, stderr, _, sent = runPair(t, env, "", "--agent-addr", "192.0.2.10:8443")
	if code != 1 || !sent || strings.Contains(stderr, "Peringatan") {
		t.Fatalf("with pin: code %d sent %v stderr %q", code, sent, stderr)
	}
}

// noMySQLSystem is a host without any MySQL installation.
type noMySQLSystem struct{}

func (noMySQLSystem) ReadFile(string) ([]byte, error) { return nil, os.ErrNotExist }
func (noMySQLSystem) LookPath(string) (string, error) { return "", errors.New("not found") }
func (noMySQLSystem) Exists(string) bool              { return false }
func (noMySQLSystem) Output(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("not found")
}
func (noMySQLSystem) Hostname() (string, error)             { return "h", nil }
func (noMySQLSystem) Statfs(string) (uint64, uint64, error) { return 0, 0, errors.New("no") }
func (noMySQLSystem) Arch() string                          { return "amd64" }

// TestRegisterMySQLWithoutMySQL: an agent on a server without MySQL still
// accepts mysql.status and answers that MySQL is missing.
func TestRegisterMySQLWithoutMySQL(t *testing.T) {
	reg := commands.NewRegistry()
	registerMySQL(reg, config.Config{}, noMySQLSystem{}, jobs.NewRunner(nil, nil), slog.New(slog.DiscardHandler))
	resp := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.status"})
	if resp.Error == nil || resp.Error.Message != "MySQL tidak ditemukan di server ini" {
		t.Fatalf("response = %+v", resp)
	}
}
