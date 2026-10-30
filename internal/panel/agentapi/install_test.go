package agentapi_test

import (
	"bytes"
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
		AgentAddr: func() string { return installAgentAddr },
		Now:       time.Now,
		Logger:    slog.New(slog.DiscardHandler),
	}
	mux := http.NewServeMux()
	mux.Handle("GET /agent/install", agentapi.InstallScriptHandler(deps, func() string { return installPanelURL }))
	mux.Handle("GET /agent/bin/{arch}", agentapi.BinaryHandler(deps, binDir))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs.URL, pt.Token
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
	base, token := installEnv(t, t.TempDir())
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
		`printf 'url = "%s"\n' "$PANEL_URL/agent/bin/$arch?token=$TOKEN" |`,
		`curl -sSL -o "$BIN.new" -w '%{http_code}' -K -)`,
		`DBPANEL_TOKEN="$TOKEN" "$BIN" agent pair --panel "$PANEL_URL"`,
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
