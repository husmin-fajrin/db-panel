package agentapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	installPanelURL  = "http://192.0.2.10:8080"
	installAgentAddr = "192.0.2.10:8443"
)

// installEnv serves the install script and binary endpoints over real HTTP
// with binDir as the panel's bin directory, and returns the server URL and
// a valid pairing token.
func installEnv(t *testing.T, binDir string) (string, string) {
	t.Helper()
	base, token, _ := installEnvCA(t, binDir)
	return base, token
}

// installEnvCA is installEnv that also returns the panel's CA.
func installEnvCA(t *testing.T, binDir string) (string, string, *ca.CA) {
	t.Helper()
	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := st.CreateServer("db-baru")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := st.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	deps := agentapi.Deps{
		Store:     st,
		CA:        authority,
		AgentAddr: func() string { return installAgentAddr },
		Now:       time.Now,
		Logger:    slog.New(slog.DiscardHandler),
	}
	mux := http.NewServeMux()
	mux.Handle("GET /agent/install", agentapi.InstallScriptHandler(deps,
		func() string { return installPanelURL }, agentapi.NewBinHashes(binDir)))
	mux.Handle("GET /agent/bin/{arch}", agentapi.BinaryHandler(deps, binDir))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs.URL, pt.Token, authority
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// selfBinary returns the bytes of the running (test) executable.
func selfBinary(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func wantTextError(t *testing.T, resp *http.Response, body []byte, status int, msg string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, status, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if got := strings.TrimSpace(string(body)); got != msg {
		t.Fatalf("body = %q, want %q", got, msg)
	}
}

func TestInstallScriptRendersToken(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	arm := []byte("\x7fELF arm64 agent")
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), arm, 0o644); err != nil {
		t.Fatal(err)
	}
	base, token, authority := installEnvCA(t, binDir)
	resp, body := get(t, base+"/agent/install?token="+token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type = %q, want text/x-shellscript", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	script := string(body)
	for _, want := range []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"systemctl enable --now dbpanel-agent",
		"PANEL_URL='" + installPanelURL + "'\n",
		"AGENT_ADDR='" + installAgentAddr + "'\n",
		"TOKEN='" + token + "'\n",
		// SEC-23: no redirects (the token would follow them to another host).
		`printf 'url = "%s"\nmax-redirs = 0\n' "$PANEL_URL/agent/bin/$arch?token=$TOKEN" |`,
		`curl -sSL -o "$BIN.new" -w '%{http_code}' -K -)`,
		`DBPANEL_TOKEN="$TOKEN" "$BIN" agent pair --panel "$PANEL_URL" --ca-sha256 "$CA_SHA256" --agent-addr "$AGENT_ADDR"`,
		// The CA the agent will pin is the panel's: sha256 of its DER.
		"CA_SHA256='" + sha256Hex(authority.Cert.Raw) + "'\n",
		"BIN_SHA256_ARM64='" + sha256Hex(arm) + "'\n",
		"sha256sum -c",
		"Checksum binary agent tidak cocok; kemungkinan ada yang menyusup di jaringan",
		"binary untuk arsitektur ini belum tersedia di panel",
		"Jalankan dengan sudo",
		"Pairing gagal. Periksa token dan alamat panel, lalu ulangi.",
		"Agent terpasang dan terhubung ke panel",
		"systemctl is-active --quiet dbpanel-agent",
		"Agent terpasang tetapi service tidak berjalan. Periksa: journalctl -u dbpanel-agent",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not contain %q", want)
		}
	}
	if strings.Contains(script, "{{") || strings.Contains(script, "set -x") {
		t.Error("script has an unrendered action or set -x")
	}
	// The panel's own build is served for its architecture, so its hash is
	// in the script too.
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if want := "BIN_SHA256_AMD64='" + sha256Hex(selfBinary(t)) + "'\n"; !strings.Contains(script, want) {
			t.Errorf("script does not contain %q", want)
		}
	}
	// SEC-02: the token never goes on a command line.
	if strings.Contains(script, "--token") {
		t.Error("script passes the token with --token (visible in ps)")
	}
	if n := strings.Count(script, token); n != 1 {
		t.Errorf("token appears %d times in the script, want once (TOKEN=...)", n)
	}
	// set -e ignores a failure anywhere but last in an && list, so each
	// systemctl step must stand alone.
	if strings.Contains(script, "daemon-reload &&") {
		t.Error("script chains systemctl daemon-reload with &&")
	}

	// The script is served from the token without consuming it, so the
	// binary download and the pairing that follow can still use it.
	if resp, body := get(t, base+"/agent/install?token="+token); resp.StatusCode != http.StatusOK {
		t.Fatalf("second fetch = %d, want 200 (body %q)", resp.StatusCode, body)
	}
}

// TestRenderInstallScriptDeterministic: the UI hashes RenderInstallScript's
// output for the pairing command, so the same parameters must always give
// the same bytes, and those bytes must carry every parameter.
func TestRenderInstallScriptDeterministic(t *testing.T) {
	t.Parallel()
	p := agentapi.InstallScriptParams{
		PanelURL:  installPanelURL,
		Token:     "ABCDE-FGHJK-MNPQR",
		AgentAddr: installAgentAddr,
		CASHA256:  strings.Repeat("ab", 32),
		BinSHA256: map[string]string{"amd64": strings.Repeat("11", 32), "arm64": strings.Repeat("22", 32)},
	}
	first, err := agentapi.RenderInstallScript(p)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := agentapi.RenderInstallScript(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, first) {
			t.Fatal("RenderInstallScript gave different bytes for the same parameters")
		}
	}
	for _, want := range []string{
		"PANEL_URL='" + installPanelURL + "'\n",
		"AGENT_ADDR='" + installAgentAddr + "'\n",
		"TOKEN='ABCDE-FGHJK-MNPQR'\n",
		"CA_SHA256='" + strings.Repeat("ab", 32) + "'\n",
		"BIN_SHA256_AMD64='" + strings.Repeat("11", 32) + "'\n",
		"BIN_SHA256_ARM64='" + strings.Repeat("22", 32) + "'\n",
	} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("script does not contain %q", want)
		}
	}
	// A missing architecture renders as an empty hash, which the script
	// refuses ("belum tersedia").
	p.BinSHA256 = map[string]string{"amd64": strings.Repeat("11", 32)}
	partial, err := agentapi.RenderInstallScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(partial, []byte("BIN_SHA256_ARM64=''\n")) {
		t.Error("missing arm64 hash is not rendered empty")
	}
	if bytes.Equal(partial, first) {
		t.Error("different parameters rendered the same script")
	}
}

// TestInstallScriptBinaryHashGate runs the rendered script's download step
// with a stubbed curl: a binary whose hash differs from the panel's is
// refused and never installed, a matching one is installed.
func TestInstallScriptBinaryHashGate(t *testing.T) {
	t.Parallel()
	good := []byte("\x7fELF agent asli")
	for _, tc := range []struct {
		name, served, wantErr string
		hashes                map[string]string
	}{
		{"cocok", string(good), "", map[string]string{"amd64": sha256Hex(good), "arm64": sha256Hex(good)}},
		{"disusupi", "\x7fELF agent jahat", "Checksum binary agent tidak cocok; kemungkinan ada yang menyusup di jaringan",
			map[string]string{"amd64": sha256Hex(good), "arm64": sha256Hex(good)}},
		{"tanpa-hash", string(good), "binary untuk arsitektur ini belum tersedia di panel", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, err := agentapi.RenderInstallScript(agentapi.InstallScriptParams{
				PanelURL: installPanelURL, Token: "ABCDE-FGHJK-MNPQR", AgentAddr: installAgentAddr,
				CASHA256: strings.Repeat("ab", 32), BinSHA256: tc.hashes,
			})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			served := filepath.Join(dir, "served")
			if err := os.WriteFile(served, []byte(tc.served), 0o644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "dbpanel")
			// Run everything up to (not including) the systemd unit: the
			// script with main's root check and the unit step cut off.
			harness := strings.Replace(string(script), `main "$@"`, "", 1)
			harness = strings.Replace(harness, "BIN=/usr/local/bin/dbpanel", "BIN="+agentapi.ShellQuote(bin), 1)
			harness += `
id() { echo 0; }
uname() { echo x86_64; }
curl() { cat >/dev/null; cp ` + agentapi.ShellQuote(served) + ` "$3"; printf 200; }
download_agent
`
			out, err := exec.Command("bash", "-c", harness).CombinedOutput()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("download refused: %v\n%s", err, out)
				}
				if got, _ := os.ReadFile(bin); !bytes.Equal(got, good) {
					t.Fatalf("installed %q, want %q", got, good)
				}
				return
			}
			if err == nil || !strings.Contains(string(out), tc.wantErr) {
				t.Fatalf("err %v, output %q; want failure with %q", err, out, tc.wantErr)
			}
			if _, err := os.Stat(bin); err == nil {
				t.Fatal("refused binary was installed")
			}
		})
	}
}

// TestBinHashesForOwnArch: the hash of what BinaryHandler serves: the
// panel's own executable for its architecture, a file in binDir when there
// is one (re-hashed after it changes), and an error otherwise.
func TestBinHashesForOwnArch(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("the panel serves its own binary only on linux")
	}
	binDir := t.TempDir()
	h := agentapi.NewBinHashes(binDir)
	got, err := h.For(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if want := sha256Hex(selfBinary(t)); got != want {
		t.Fatalf("For(%s) = %s, want %s (the running executable)", runtime.GOARCH, got, want)
	}

	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	for _, arch := range []string{other, "mips", "../x"} {
		if _, err := h.For(arch); err == nil || err.Error() != "binary untuk arsitektur ini belum tersedia di panel" {
			t.Errorf("For(%q) err = %v", arch, err)
		}
	}

	path := filepath.Join(binDir, "dbpanel_linux_"+other)
	if err := os.WriteFile(path, []byte("versi 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := h.For(other); err != nil || got != sha256Hex([]byte("versi 1")) {
		t.Fatalf("For(%s) = %s, %v; want hash of versi 1", other, got, err)
	}
	// A replaced binary (new size and mtime) is hashed again.
	if err := os.WriteFile(path, []byte("versi kedua"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := h.For(other); err != nil || got != sha256Hex([]byte("versi kedua")) {
		t.Fatalf("after replace For(%s) = %s, %v; want hash of versi kedua", other, got, err)
	}
	// binDir wins over the running executable for the panel's own arch.
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_"+runtime.GOARCH), []byte("rilis"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := h.For(runtime.GOARCH); err != nil || got != sha256Hex([]byte("rilis")) {
		t.Fatalf("For(%s) with binDir file = %s, %v", runtime.GOARCH, got, err)
	}
}

// TestCAFingerprint: sha256 of the CA certificate's DER, hex.
func TestCAFingerprint(t *testing.T) {
	t.Parallel()
	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := agentapi.CAFingerprint(authority.CAPEM()), sha256Hex(authority.Cert.Raw); got != want {
		t.Fatalf("CAFingerprint = %s, want %s", got, want)
	}
	if got := agentapi.CAFingerprint([]byte("bukan PEM")); got != "" {
		t.Fatalf("CAFingerprint(garbage) = %q, want empty", got)
	}
	// A second certificate after the CA would be trusted too: refused.
	other, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := agentapi.CAFingerprint(append(authority.CAPEM(), other.CAPEM()...)); got != "" {
		t.Fatalf("CAFingerprint(two certificates) = %q, want empty", got)
	}
}

// TestInstallScriptAgentUnitMatchesReference keeps the unit the script
// writes identical to scripts/systemd/dbpanel-agent.service.
func TestInstallScriptAgentUnitMatchesReference(t *testing.T) {
	t.Parallel()
	ref, err := os.ReadFile("../../../scripts/systemd/dbpanel-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	base, token := installEnv(t, t.TempDir())
	_, body := get(t, base+"/agent/install?token="+token)
	if !bytes.Contains(body, ref) {
		t.Fatalf("script does not write the reference unit:\n%s", ref)
	}
}

func TestInstallScriptRejectsBadToken(t *testing.T) {
	t.Parallel()
	base, _ := installEnv(t, t.TempDir())
	for _, q := range []string{"?token=SALAH", ""} {
		resp, body := get(t, base+"/agent/install"+q)
		wantTextError(t, resp, body, http.StatusForbidden, "token tidak valid")
	}
}

func TestBinaryHandlerServesSelfForSameArch(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("the panel serves its own binary only on linux")
	}
	base, token := installEnv(t, t.TempDir())
	resp, body := get(t, base+"/agent/bin/"+runtime.GOARCH+"?token="+token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
	n, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err != nil || n <= 0 {
		t.Fatalf("Content-Length = %q, want > 0", resp.Header.Get("Content-Length"))
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, self) {
		t.Fatalf("served %d bytes, want the %d bytes of the running executable", len(body), len(self))
	}
}

func TestBinaryHandlerServesFromBinDir(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	content := []byte("\x7fELF arm64 agent")
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	base, token := installEnv(t, binDir)
	resp, body := get(t, base+"/agent/bin/arm64?token="+token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, content) {
		t.Fatalf("body = %q, want %q", body, content)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
}

func TestBinaryHandlerUnknownArch(t *testing.T) {
	t.Parallel()
	base, token := installEnv(t, t.TempDir())
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	// An unsupported name, and a supported arch the panel has no binary for.
	for _, arch := range []string{"mips", "dbpanel_linux_amd64", other} {
		resp, body := get(t, base+"/agent/bin/"+arch+"?token="+token)
		wantTextError(t, resp, body, http.StatusNotFound, "binary untuk arsitektur ini belum tersedia di panel")
	}
}

func TestBinaryHandlerRejectsBadToken(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "dbpanel_linux_arm64"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, _ := installEnv(t, binDir)
	for _, q := range []string{"?token=SALAH", ""} {
		resp, body := get(t, base+"/agent/bin/arm64"+q)
		wantTextError(t, resp, body, http.StatusForbidden, "token tidak valid")
	}
}

// TestShellQuote (SEC-09): values rendered into the root install script
// cannot run commands, whatever they contain.
func TestShellQuote(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	for _, v := range []string{
		"http://127.0.0.1$(touch " + marker + "):8080",
		"`touch " + marker + "`",
		"a'b; touch " + marker + "; 'c",
		"$HOME \\ \" ! *",
	} {
		script := "v=" + agentapi.ShellQuote(v) + "\nprintf '%s' \"$v\"\n"
		out, err := exec.Command("bash", "-c", script).Output()
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		if string(out) != v {
			t.Errorf("round trip of %q = %q", v, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("value %q ran a command", v)
		}
	}
}
