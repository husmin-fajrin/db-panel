package panel

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"html"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/pki"
)

// TestE2EHTTPS drives the panel's HTTPS as an operator meets it: the
// certificate the panel issued itself for its address, the certificate page
// over plain HTTP, the redirect of everything else to HTTPS, the Secure
// session cookie, and a new database server paired with the exact command
// the server page shows. That command runs unchanged through bash: curl
// fetches the install script pinned to the panel's key, sha256sum checks
// it, and the script downloads the dbpanel binary (built from this tree)
// and runs "dbpanel agent pair" with the pin. Only what needs root is
// stubbed, through a PATH of fakes: sudo runs the script with its fixed
// paths (/usr/local/bin/dbpanel, the systemd unit, the agent directory)
// moved into a temporary directory, and systemctl, id and sleep are
// no-ops. The installed binary then runs as the agent and comes online.
func TestE2EHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("the install script supports linux amd64 and arm64 only")
	}
	for _, tool := range []string{"bash", "curl", "sha256sum", "mktemp", "sed", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	o := testOptions(t)
	p := newPanel(t, o)
	// The install script downloads the agent binary the panel serves from
	// <data>/bin: this tree's dbpanel.
	binPath := filepath.Join(o.DataDir, "bin", "dbpanel_linux_"+runtime.GOARCH)
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/dbpanel")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build dbpanel: %v\n%s", err, out)
	}
	uiAddr, _ := startPanel(t, p)
	port := portOf(t, uiAddr)
	base := "https://127.0.0.1:" + port

	// The automatic certificate: issued by the panel's web CA for its
	// address (a client trusting only that CA, with the usual name check,
	// accepts it), and the pin the panel shows is its key's.
	c := tlsClient(t, p)
	resp, err := c.Get(base + "/login")
	if err != nil {
		t.Fatalf("HTTPS with the panel's CA and the name check: %v", err)
	}
	if readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET https /login = %d", resp.StatusCode)
	}
	leaf := servedLeaf(t, uiAddr)
	pin, err := pki.SPKIPin(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
	if err != nil {
		t.Fatal(err)
	}
	if info := p.certInfo(); info.Mode != "panel" || info.Pin != pin {
		t.Fatalf("certificate info = %+v; served key pin %s", info, pin)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.certs.CAPEM())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "127.0.0.1"}); err != nil {
		t.Fatalf("served certificate for 127.0.0.1: %v", err)
	}

	// Plain HTTP on the same port: the certificate page and its download,
	// a 301 to HTTPS for everything else.
	resp, err = plainClient.Get("http://127.0.0.1:" + port + "/sertifikat")
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, `href="/sertifikat/dbpanel-panel.crt"`) {
		t.Fatalf("GET http /sertifikat = %d:\n%s", resp.StatusCode, body)
	}
	resp, err = plainClient.Get("http://127.0.0.1:" + port + "/sertifikat/dbpanel-panel.crt")
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK || body != string(p.certs.CAPEM()) {
		t.Fatalf("GET http /sertifikat/dbpanel-panel.crt = %d", resp.StatusCode)
	}
	resp, err = plainClient.Get("http://127.0.0.1:" + port + "/servers")
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, resp); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != base+"/servers" {
		t.Fatalf("GET http /servers = %d to %q, want 301 to %s/servers", resp.StatusCode, resp.Header.Get("Location"), base)
	}

	// The session cookie is Secure.
	if ck := login(t, o, base); ck.Name != auth.SecureCookieName || !ck.Secure || !ck.HttpOnly {
		t.Fatalf("session cookie = %+v, want Secure, HttpOnly %s", ck, auth.SecureCookieName)
	}

	// Tambah server, then the command its page shows.
	ui := newUIClient(t, base)
	ui.login(readInitialPassword(t, o))
	loc := ui.postRedirect("/servers", url.Values{"name": {"db-https"}})
	page := ui.get(loc)
	m := pairCommandRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("server page %s shows no pairing command:\n%s", loc, page)
	}
	command := html.UnescapeString(m[1])
	if !strings.Contains(command, "--pinnedpubkey "+pin+" ") {
		t.Fatalf("pairing command does not pin the served key %s: %s", pin, command)
	}
	srvID, err := strconv.ParseInt(strings.TrimPrefix(loc, "/servers/"), 10, 64)
	if err != nil {
		t.Fatalf("Tambah server redirected to %q", loc)
	}

	root := t.TempDir()
	stubs := writeInstallStubs(t, root)
	// With another pin, curl refuses the panel and nothing runs as root.
	wrongPin := "sha256//" + strings.Repeat("A", 43) + "="
	if out, err := runPairCommand(t, strings.Replace(command, pin, wrongPin, 1), root, stubs); err == nil ||
		!strings.Contains(out, "curl: (90)") {
		t.Fatalf("pairing command with another pin = %v, want curl's pin mismatch (90):\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "sudo.ran")); err == nil {
		t.Fatal("sudo ran although curl was given another pin")
	}

	out, err := runPairCommand(t, command, root, stubs)
	if err != nil {
		t.Fatalf("pairing command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent terpasang dan terhubung ke panel") {
		t.Fatalf("pairing command output:\n%s", out)
	}
	if got, err := p.Store.GetServer(srvID); err != nil || !got.Paired || got.CertSerial == "" {
		t.Fatalf("server after the pairing command = %+v, %v; want paired", got, err)
	}
	installed := filepath.Join(root, "bin", "dbpanel")
	if a, b := mustRead(t, installed), mustRead(t, binPath); !bytes.Equal(a, b) {
		t.Fatal("the installed binary is not the one the panel serves")
	}
	if log := string(mustRead(t, filepath.Join(root, "systemctl.log"))); !strings.Contains(log, "enable --now dbpanel-agent") {
		t.Fatalf("systemctl calls:\n%s", log)
	}

	// The installed binary runs as the agent (what the unit starts) and
	// comes online.
	ctx, cancel := context.WithCancel(context.Background())
	agentCmd := exec.CommandContext(ctx, installed, "agent", "--dir", filepath.Join(root, "agent"))
	agentCmd.Env = noProxyEnv()
	var agentLog syncBuffer
	agentCmd.Stdout, agentCmd.Stderr = &agentLog, &agentLog
	agentCmd.Cancel = func() error { return agentCmd.Process.Signal(syscall.SIGTERM) }
	agentCmd.WaitDelay = 10 * time.Second
	if err := agentCmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		agentCmd.Wait()
		if t.Failed() {
			t.Logf("agent log:\n%s", tail(agentLog.String(), 100))
		}
	})
	waitForWithin(t, 30*time.Second, "the installed agent online", func() bool { return p.Hub.IsOnline(srvID) })
}

// pairCommandRe finds the pairing command on a server page.
var pairCommandRe = regexp.MustCompile(`<code x-ref="perintah">([^<]*)</code>`)

// installStubs are the fakes for what the install script needs root for.
// ROOT is the test's directory; the script's fixed paths move under it.
var installStubs = map[string]string{
	// sudo bash FILE: run FILE with its paths moved under ROOT.
	"sudo": `#!/bin/bash
set -eu
touch "$ROOT/sudo.ran"
[ "$#" -eq 2 ] && [ "$1" = bash ] || { echo "sudo stub: unexpected $*" >&2; exit 97; }
cp "$2" "$ROOT/served-install.sh"
sed -e "s|^BIN=/usr/local/bin/dbpanel\$|BIN=$ROOT/bin/dbpanel|" \
    -e "s|/etc/systemd/system/dbpanel-agent.service|$ROOT/dbpanel-agent.service|" \
    -e "s|--agent-addr \"\\\$AGENT_ADDR\"; then|--agent-addr \"\\\$AGENT_ADDR\" --dir \"$ROOT/agent\"; then|" \
    "$2" >"$ROOT/install.sh"
# Every rewrite applied: the script writes nothing outside ROOT (the unit
# text still names /usr/local/bin/dbpanel, as the real unit does).
grep -q "^BIN=$ROOT/bin/dbpanel\$" "$ROOT/install.sh"
grep -q -- "--dir \"$ROOT/agent\"; then" "$ROOT/install.sh"
if grep -q -e '^BIN=/usr/' -e '>/etc/' "$ROOT/install.sh"; then
	echo "sudo stub: a root path is left in the script" >&2
	exit 98
fi
exec bash "$ROOT/install.sh"
`,
	"systemctl": `#!/bin/bash
echo "$*" >>"$ROOT/systemctl.log"
case "$1" in
is-active) [ -e "$ROOT/active" ] ;;
enable) touch "$ROOT/active" ;;
esac
`,
	"id":    "#!/bin/bash\n[ \"$1\" = -u ] && echo 0\n",
	"sleep": "#!/bin/bash\nexit 0\n",
}

// writeInstallStubs writes installStubs into a directory under root and
// returns it, with root/bin made for the installed binary.
func writeInstallStubs(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "stubs")
	for _, d := range []string{dir, filepath.Join(root, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range installStubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPairCommand runs command with bash as the operator's shell would,
// with the stubs first in PATH and no proxy (the panel is on loopback).
func runPairCommand(t *testing.T, command, root, stubs string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = root
	cmd.Env = append(noProxyEnv(), "ROOT="+root, "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// noProxyEnv is the environment without proxy settings: the panel is on
// loopback. (A later entry of the same key, such as PATH, wins.)
func noProxyEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "NO_PROXY=*")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
