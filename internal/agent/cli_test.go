package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
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
	want := "Direktori ini milik panel (ada ca.key)."
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
	dir := t.TempDir()
	RegisterMySQL(reg, config.Config{}, dir, noMySQLSystem{}, jobs.NewRunner(nil, nil), slog.New(slog.DiscardHandler))
	for _, cmd := range []string{"mysql.status", "mysql.connect", "mysql.inventory"} {
		resp := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: json.RawMessage(`{"user":"root"}`)})
		if resp.Error == nil || resp.Error.Message != "MySQL tidak ditemukan di server ini" {
			t.Fatalf("%s: response = %+v", cmd, resp)
		}
	}
	// The sealing key is its own file: agent.key is the TLS private key.
	if fi, err := os.Stat(filepath.Join(dir, "secrets.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.key: %v, %v", fi, err)
	}
}

// TestCLISetRoleAndSetSource: "agent set-role" and "agent set-source"
// write the pins (0644) the driver reads, validate their argument, and
// set-role unassigned removes both pins.
func TestCLISetRoleAndSetSource(t *testing.T) {
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return nil, mysql.ErrNoMysqld })
	dir := t.TempDir()
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Main(args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	code, out, errOut := run("set-role", "master", "--dir", dir)
	if code != 0 || !strings.Contains(out, "peran diperbarui; agent membacanya ulang pada perintah berikutnya") {
		t.Fatalf("set-role master = %d, %q, %q", code, out, errOut)
	}
	if got := read(mysql.RoleFile); got != "master\n" {
		t.Errorf("role file = %q", got)
	}
	if got := read(mysql.SourceFile); got != "" {
		t.Errorf("source file after set-role master = %q, want none", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, mysql.RoleFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("role file: %v, %v; want mode 0644", fi, err)
	}
	// --dir may come first too.
	if code, _, errOut := run("set-role", "--dir", dir, "replica"); code != 0 || read(mysql.RoleFile) != "replica\n" {
		t.Fatalf("set-role --dir DIR replica = %d, %q; role %q", code, errOut, read(mysql.RoleFile))
	}

	code, out, errOut = run("set-source", "10.0.0.5:3307", "--dir", dir)
	if code != 0 || !strings.Contains(out, "sumber diperbarui; agent membacanya ulang pada perintah berikutnya") {
		t.Fatalf("set-source = %d, %q, %q", code, out, errOut)
	}
	if got := read(mysql.SourceFile); got != "10.0.0.5:3307\n" {
		t.Errorf("source file = %q", got)
	}
	if code, _, errOut := run("set-source", "[fd00::1]:3306", "--dir", dir); code != 0 || read(mysql.SourceFile) != "[fd00::1]:3306\n" {
		t.Errorf("set-source IPv6 = %d, %q; source %q", code, errOut, read(mysql.SourceFile))
	}

	for _, args := range [][]string{
		{"set-role", "raja", "--dir", dir},
		{"set-role", "--dir", dir},
		{"set-role", "master", "replica", "--dir", dir},
		{"set-source", "10.0.0.5", "--dir", dir},
		{"set-source", "10.0.0.5:0", "--dir", dir},
		{"set-source", "10.0.0.5:70000", "--dir", dir},
		{"set-source", "a'b:3306", "--dir", dir},
		{"set-source", "fd00::1:3306", "--dir", dir},
		{"set-source", "--dir", dir},
	} {
		if code, _, errOut := run(args...); code != 2 || errOut == "" {
			t.Errorf("%q = %d, %q; want 2 with a message", args, code, errOut)
		}
	}
	if read(mysql.RoleFile) != "replica\n" || read(mysql.SourceFile) != "[fd00::1]:3306\n" {
		t.Errorf("pins changed by refused calls: %q, %q", read(mysql.RoleFile), read(mysql.SourceFile))
	}

	if code, _, errOut := run("set-role", "unassigned", "--dir", dir); code != 0 {
		t.Fatalf("set-role unassigned = %d, %q", code, errOut)
	}
	for _, name := range []string{mysql.RoleFile, mysql.SourceFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after set-role unassigned: %v", name, err)
		}
	}
}

// TestCLIPairWebPin: the HTTPS pin comes from DBPANEL_WEB_PIN (as the
// install script passes it) or --web-pin; a malformed one is a usage error
// and a wrong one stops before the token is sent.
func TestCLIPairWebPin(t *testing.T) {
	ch := make(chan string, 4)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proto.PairRequest
		json.NewDecoder(r.Body).Decode(&req)
		ch <- req.Token
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)
	pin := pinOf(t, ts)
	oldEnv := getenv
	t.Cleanup(func() { getenv = oldEnv })
	run := func(env map[string]string, args ...string) (int, string, bool) {
		getenv = func(k string) string { return env[k] }
		var out, errb bytes.Buffer
		code := Main(append([]string{"pair", "--panel", ts.URL, "--ca-sha256", testCAPin, "--dir", t.TempDir()}, args...), &out, &errb)
		select {
		case <-ch:
			return code, errb.String(), true
		default:
			return code, errb.String(), false
		}
	}
	tok := map[string]string{"DBPANEL_TOKEN": "ENVTK-AAAAA-BBBBB"}
	withPin := map[string]string{"DBPANEL_TOKEN": "ENVTK-AAAAA-BBBBB", "DBPANEL_WEB_PIN": pin}
	if code, stderr, sent := run(withPin); code != 1 || !sent || !strings.Contains(stderr, "token ditolak oleh panel") {
		t.Fatalf("pin from env: code %d sent %v stderr %q", code, sent, stderr)
	}
	if code, stderr, sent := run(tok, "--web-pin", pin); code != 1 || !sent {
		t.Fatalf("pin from flag: code %d sent %v stderr %q", code, sent, stderr)
	}
	for _, bad := range []string{"abc", "sha256//pendek", strings.Repeat("A", 44)} {
		if code, stderr, sent := run(tok, "--web-pin", bad); code != 2 || sent {
			t.Errorf("--web-pin %q: code %d sent %v stderr %q", bad, code, sent, stderr)
		}
	}
	wrong := map[string]string{"DBPANEL_TOKEN": "ENVTK-AAAAA-BBBBB", "DBPANEL_WEB_PIN": "sha256//" + strings.Repeat("A", 43) + "="}
	if code, stderr, sent := run(wrong); code != 1 || sent || !strings.Contains(stderr, "tidak cocok dengan pin") {
		t.Fatalf("wrong pin: code %d sent %v stderr %q", code, sent, stderr)
	}
}

// writeTokenFile writes a local-pair-token file ("<token>\n<pin>\n").
func writeTokenFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "local-pair-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCLIPairTokenFile: "pair --token-file" takes the token and the web pin
// from the file the panel wrote, needs no --ca-sha256 (the pin authenticates
// the panel), keeps the file when pairing fails and refuses a file without
// a valid pin or a second token source.
func TestCLIPairTokenFile(t *testing.T) {
	ch := make(chan string, 4)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proto.PairRequest
		json.NewDecoder(r.Body).Decode(&req)
		ch <- req.Token
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)
	pin := pinOf(t, ts)
	run := func(content string, args ...string) (int, string, string) {
		dir := t.TempDir()
		path := writeTokenFile(t, dir, content)
		var out, errb bytes.Buffer
		code := Main(append([]string{"pair", "--panel", ts.URL, "--token-file", path, "--dir", dir}, args...), &out, &errb)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("token file gone after a failed pairing: %v", err)
		}
		select {
		case tok := <-ch:
			return code, errb.String(), tok
		default:
			return code, errb.String(), ""
		}
	}
	if code, stderr, tok := run("FILET-AAAAA-BBBBB\n" + pin + "\n"); code != 1 || tok != "FILET-AAAAA-BBBBB" ||
		!strings.Contains(stderr, "token ditolak oleh panel") || strings.Contains(stderr, "Peringatan") {
		t.Fatalf("token file: code %d token %q stderr %q", code, tok, stderr)
	}
	wrong := "sha256//" + strings.Repeat("A", 43) + "="
	if code, stderr, tok := run("FILET-AAAAA-BBBBB\n" + wrong + "\n"); code != 1 || tok != "" ||
		!strings.Contains(stderr, "tidak cocok dengan pin") {
		t.Fatalf("wrong pin: code %d token %q stderr %q", code, tok, stderr)
	}
	for _, bad := range []string{"", "FILET-AAAAA-BBBBB\n", "FILET-AAAAA-BBBBB\nbukan-pin\n", "\n" + pin + "\n"} {
		if code, stderr, tok := run(bad); code != 2 || tok != "" {
			t.Errorf("file %q: code %d token %q stderr %q", bad, code, tok, stderr)
		}
	}
	if code, stderr, tok := run("FILET-AAAAA-BBBBB\n"+pin+"\n", "--token-stdin"); code != 2 || tok != "" {
		t.Errorf("--token-file with --token-stdin: code %d token %q stderr %q", code, tok, stderr)
	}
	if code, stderr, _ := run("FILET-AAAAA-BBBBB\n"+pin+"\n", "--token-file", "/tidak/ada"); code != 2 ||
		!strings.Contains(stderr, "/tidak/ada") {
		t.Errorf("missing file: code %d stderr %q", code, stderr)
	}
}

// TestRunPairsWithLocalTokenFirst: "dbpanel agent" on the panel host, with
// install.sh's local-pair-token and no agent.yaml yet, pairs first (here the
// panel refuses, so it stops) instead of reporting that it is not paired.
func TestRunPairsWithLocalTokenFirst(t *testing.T) {
	ch := make(chan string, 1)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proto.PairRequest
		json.NewDecoder(r.Body).Decode(&req)
		ch <- req.Token
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)
	dir := t.TempDir()
	writeTokenFile(t, dir, "LOCAL-AAAAA-BBBBB\n"+pinOf(t, ts)+"\n")
	var errb bytes.Buffer
	code := Main([]string{"--dir", dir, "--panel", ts.URL}, io.Discard, &errb)
	if code != 1 || !strings.Contains(errb.String(), "token ditolak oleh panel") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	select {
	case tok := <-ch:
		if tok != "LOCAL-AAAAA-BBBBB" {
			t.Fatalf("token sent = %q", tok)
		}
	default:
		t.Fatal("no pairing request")
	}
}

// TestLockMasterKeepsLockWrittenMeanwhile (FR-14): a promote lock written
// between lock-master's check and its write (lockForPromote of a planned
// promotion) is not replaced by the CLI's lock.
func TestLockMasterKeepsLockWrittenMeanwhile(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := t.TempDir()
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	old := cliMySQLHost
	cliMySQLHost = func(string) (mysql.Host, error) {
		// After lock-master checked for a lock, before it writes one.
		if err := p.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: deadline}); err != nil {
			t.Error(err)
		}
		return inst.Host(), nil
	}
	t.Cleanup(func() { cliMySQLHost = old })
	var stdout, stderr bytes.Buffer
	code := Main([]string{"lock-master", "--dir", dir}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "10.0.0.2:3306") {
		t.Errorf("lock-master racing a promote lock = %d, %q, %q; want 1 naming the other lock", code, stdout.String(), stderr.String())
	}
	l, ok, err := p.ReadLock()
	if !ok || err != nil || l.Candidate != "10.0.0.2:3306" || !l.Deadline.Equal(deadline) {
		t.Errorf("lock after the race = %+v, %v, %v; want the promote lock kept", l, ok, err)
	}
}

// TestUnlockMasterSaysWhatItBypasses (SA-11): unlock-master of a promote
// lock with a candidate says that it opens without reading the candidate
// or the replicas the lock lists.
func TestUnlockMasterSaysWhatItBypasses(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := t.TempDir()
	old := cliMySQLHost
	cliMySQLHost = func(string) (mysql.Host, error) { return inst.Host(), nil }
	t.Cleanup(func() { cliMySQLHost = old })
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetRole("master"); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: time.Now().Add(time.Hour),
		Readable: []string{"10.0.0.3:3306"}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"unlock-master", "--dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("unlock-master = %d, %q", code, stderr.String())
	}
	for _, want := range []string{"10.0.0.2:3306", "tanpa memeriksa", "10.0.0.3:3306", "dua master"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("unlock-master said %q, want it to name %q", stderr.String(), want)
		}
	}
}

// TestCLILockUnlockMaster: lock-master makes MySQL super_read_only, also
// across a restart (SET PERSIST), and writes the promote lock with no
// candidate ("cli") for 24 hours; a second lock-master is refused;
// unlock-master makes MySQL writable, clears the lock, and is idempotent.
func TestCLILockUnlockMaster(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	dir := t.TempDir()
	var gotDir string
	old := cliMySQLHost
	cliMySQLHost = func(d string) (mysql.Host, error) { gotDir = d; return inst.Host(), nil }
	t.Cleanup(func() { cliMySQLHost = old })
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return inst.Host(), nil })
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Main(args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	vars := func() (ro, sro string) {
		t.Helper()
		db, err := sql.Open("mysql", inst.DSN())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.QueryRow("SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only").Scan(&ro, &sro); err != nil {
			t.Fatal(err)
		}
		return ro, sro
	}
	pins := func() *mysql.Pins {
		p, err := mysql.LoadPins(dir)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Only a master (or a server under a lock-master lock) is unlocked.
	for _, role := range []string{"replica", "quarantined", ""} {
		if role != "" {
			if code, _, errOut := run("set-role", role, "--dir", dir); code != 0 {
				t.Fatalf("set-role %s = %d, %q", role, code, errOut)
			}
		}
		if code, _, errOut := run("unlock-master", "--dir", dir); code != 1 || !strings.Contains(errOut, "unlock-master hanya untuk master") {
			t.Errorf("unlock-master on %q = %d, %q; want 1 and a refusal", role, code, errOut)
		}
		if role == "" {
			continue
		}
		if code, _, errOut := run("set-role", "unassigned", "--ya", "--dir", dir); code != 0 {
			t.Fatalf("set-role unassigned = %d, %q", code, errOut)
		}
	}
	if ro, sro := vars(); ro != "0" || sro != "0" {
		t.Fatalf("refused unlocks changed MySQL: read_only %s, super_read_only %s", ro, sro)
	}

	// A lock-master lock does not open a replica either.
	if code, _, errOut := run("set-role", "replica", "--dir", dir); code != 0 {
		t.Fatalf("set-role replica = %d, %q", code, errOut)
	}
	if code, _, errOut := run("lock-master", "--dir", dir); code != 0 {
		t.Fatalf("lock-master on replica = %d, %q", code, errOut)
	}
	if code, _, errOut := run("unlock-master", "--dir", dir); code != 1 || !strings.Contains(errOut, "dipin sebagai replica") {
		t.Errorf("unlock-master on a locked replica = %d, %q; want 1 and a refusal", code, errOut)
	}
	if _, sro := vars(); sro != "1" {
		t.Errorf("refused unlock on a locked replica: super_read_only %s, want 1", sro)
	}
	if code, _, errOut := run("set-role", "master", "--dir", dir); code != 0 {
		t.Fatalf("set-role master = %d, %q", code, errOut)
	}
	if code, _, errOut := run("unlock-master", "--dir", dir); code != 0 {
		t.Fatalf("unlock-master on master = %d, %q", code, errOut)
	}
	if code, _, errOut := run("set-role", "unassigned", "--dir", dir); code != 0 {
		t.Fatalf("set-role unassigned = %d, %q", code, errOut)
	}

	// An unpinned server locked with lock-master can be unlocked again.
	if code, _, errOut := run("lock-master", "--dir", dir); code != 0 {
		t.Fatalf("lock-master unpinned = %d, %q", code, errOut)
	}
	if code, _, errOut := run("unlock-master", "--dir", dir); code != 0 {
		t.Fatalf("unlock-master under its own lock = %d, %q", code, errOut)
	}
	if code, _, errOut := run("unlock-master", "--dir", dir); code != 1 {
		t.Errorf("unlock-master unpinned without a lock = %d, %q; want 1", code, errOut)
	}

	if code, _, errOut := run("set-role", "master", "--dir", dir); code != 0 {
		t.Fatalf("set-role master = %d, %q", code, errOut)
	}
	before := time.Now()
	code, out, errOut := run("lock-master", "--dir", dir)
	if code != 0 || !strings.Contains(out, "master dikunci") {
		t.Fatalf("lock-master = %d, %q, %q", code, out, errOut)
	}
	if gotDir != dir {
		t.Errorf("MySQL looked up in %q, want %q", gotDir, dir)
	}
	if ro, sro := vars(); ro != "1" || sro != "1" {
		t.Errorf("after lock-master: read_only %s, super_read_only %s; want 1, 1", ro, sro)
	}
	l, ok, err := pins().ReadLock()
	if err != nil || !ok || l.Candidate != "cli" || l.Deadline.Before(before.Add(24*time.Hour-time.Minute)) || l.Deadline.After(time.Now().Add(24*time.Hour+time.Minute)) {
		t.Errorf("lock after lock-master = %+v, %v, %v; want candidate cli, deadline in 24h", l, ok, err)
	}
	if code, _, errOut := run("lock-master", "--dir", dir); code != 1 || !strings.Contains(errOut, "sudah dikunci") {
		t.Errorf("second lock-master = %d, %q; want 1 and a refusal", code, errOut)
	}
	inst.Restart(t)
	if ro, sro := vars(); ro != "1" || sro != "1" {
		t.Errorf("after a restart: read_only %s, super_read_only %s; want 1, 1", ro, sro)
	}

	code, out, errOut = run("unlock-master", "--dir", dir)
	if code != 0 || !strings.Contains(out, "master dibuka") {
		t.Fatalf("unlock-master = %d, %q, %q", code, out, errOut)
	}
	if ro, sro := vars(); ro != "0" || sro != "0" {
		t.Errorf("after unlock-master: read_only %s, super_read_only %s; want 0, 0", ro, sro)
	}
	if _, ok, err := pins().ReadLock(); ok || err != nil {
		t.Errorf("lock after unlock-master: %v, %v", ok, err)
	}
	if code, _, errOut := run("unlock-master", "--dir", dir); code != 0 {
		t.Errorf("second unlock-master = %d, %q; want 0", code, errOut)
	}
	inst.Restart(t)
	if ro, sro := vars(); ro != "0" || sro != "0" {
		t.Errorf("after unlock and a restart: read_only %s, super_read_only %s; want 0, 0", ro, sro)
	}
	if code, _, errOut := run("lock-master", "--dir", dir, "lagi"); code != 2 || errOut == "" {
		t.Errorf("lock-master with an argument = %d, %q; want 2", code, errOut)
	}
}

// startLockRig is a quarantined agent directory over a running MySQL whose
// start lock the guard has written.
type startLockRig struct {
	t    *testing.T
	inst *testmysql.Instance
	dir  string
	d    *mysql.Driver
	lock string
}

func newStartLockRig(t *testing.T) *startLockRig {
	t.Helper()
	r := &startLockRig{t: t, inst: testmysql.Start(t, testmysql.Options{}), dir: t.TempDir()}
	if code, _, errOut := runCLI("set-role", "quarantined", "--dir", r.dir); code != 0 {
		t.Fatalf("set-role quarantined = %d, %q", code, errOut)
	}
	r.d = &mysql.Driver{Host: r.inst.Host(), PinDir: r.dir, SeenPath: filepath.Join(t.TempDir(), "seen"),
		SalvageDir: filepath.Join(t.TempDir(), "salvage")}
	r.d.Register(commands.NewRegistry())
	var err error
	if r.lock, err = mysql.StartLockPath(r.inst.Layout()); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if line, file := r.lineAndFile(); !line || !file {
		t.Fatalf("start lock after the first tick: line=%v file=%v", line, file)
	}
	return r
}

func (r *startLockRig) tick() {
	r.t.Helper()
	if err := r.d.GuardTick(context.Background(), time.Now()); err != nil {
		r.t.Fatalf("GuardTick: %v", err)
	}
}

// lineAndFile reports whether my.cnf holds the agent's init_file line and
// the lock file exists, failing on a line without its file.
func (r *startLockRig) lineAndFile() (line, file bool) {
	r.t.Helper()
	b, err := os.ReadFile(r.inst.ConfigFile)
	if err != nil {
		r.t.Fatal(err)
	}
	line = strings.Contains(string(b), "\ninit_file="+r.lock+"\n")
	_, err = os.Stat(r.lock)
	file = err == nil
	if line && !file {
		r.t.Errorf("init_file line names %s, which does not exist", r.lock)
	}
	return line, file
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// nextMasterText is C11, asserted in full on every successful master path.
const nextMasterText = "Langkah berikutnya: bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo dbpanel agent unlock-master"

// stubLayoutHost replaces cliLayoutHost for the test: without it set-role
// master would look at the MySQL configuration of this machine.
func stubLayoutHost(t *testing.T, fn func(config.Config) (mysql.Host, error)) {
	t.Helper()
	old := cliLayoutHost
	cliLayoutHost = fn
	t.Cleanup(func() { cliLayoutHost = old })
}

// noMysqldHost is a Host with no mysqld whose config sits in tmp.
func noMysqldHost(tmp string) mysql.Host {
	cnf := filepath.Join(tmp, "my.cnf")
	return mysql.NewHost(mysql.Layout{ConfigFile: cnf, ConfigFiles: []string{cnf}}, nil, nil, probe.RealSystem(), nil)
}

// writePinFiles writes pin files (mode 0644) into dir.
func writePinFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, v := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runSetRole runs the CLI with input on stdin.
func runSetRole(input string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := RunCLI(args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// failingConfigHost refuses every config write.
type failingConfigHost struct{ mysql.Host }

func (failingConfigHost) WriteConfig(string) (string, error) {
	return "", errors.New("disk penuh (test)")
}

// TestSetRoleReplicaWhileDownKeepsStartLock: set-role replica on a
// quarantined server whose MySQL is down only changes the pin; the guard
// keeps the start lock while MySQL cannot show read-only persisted.
func TestSetRoleReplicaWhileDownKeepsStartLock(t *testing.T) {
	r := newStartLockRig(t)
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) {
		t.Error("set-role replica looked up MySQL")
		return nil, errors.New("set-role replica must not look up MySQL")
	})
	r.inst.Stop(t)
	if code, _, errOut := runCLI("set-role", "replica", "--ya", "--dir", r.dir); code != 0 {
		t.Fatalf("set-role replica = %d, %q", code, errOut)
	}
	for range 3 {
		r.tick()
	}
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after three ticks as a replica with MySQL down: line=%v file=%v", line, file)
	}
}

// TestSetRoleMasterReleasesFirst: set-role master releases the start lock
// (line, then file) before it pins master; when the lock cannot be
// released the role stays as it was.
func TestSetRoleMasterReleasesFirst(t *testing.T) {
	r := newStartLockRig(t)
	role := func() string {
		b, _ := os.ReadFile(filepath.Join(r.dir, mysql.RoleFile))
		return strings.TrimSpace(string(b))
	}

	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return failingConfigHost{r.inst.Host()}, nil })
	code, _, errOut := runCLI("set-role", "master", "--ya", "--dir", r.dir)
	if code != 1 || !strings.Contains(errOut, "kunci start tidak bisa dilepas (") || !strings.Contains(errOut, "); peran tidak diubah") {
		t.Errorf("set-role master with the config unwritable = %d, %q; want 1 and the refusal", code, errOut)
	}
	if got := role(); got != "quarantined" {
		t.Errorf("role after the refusal = %q", got)
	}
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after the refusal: line=%v file=%v", line, file)
	}

	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return r.inst.Host(), nil })
	code, out, errOut := runCLI("set-role", "master", "--ya", "--dir", r.dir)
	if code != 0 {
		t.Fatalf("set-role master = %d, %q", code, errOut)
	}
	if !strings.Contains(out, "Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari konfigurasi MySQL.") ||
		!strings.Contains(out, nextMasterText) {
		t.Errorf("set-role master output = %q; want the release outcome and the next step", out)
	}
	if got := role(); got != "master" {
		t.Errorf("role = %q, want master", got)
	}
	if line, file := r.lineAndFile(); line || file {
		t.Errorf("after set-role master: line=%v file=%v; want released", line, file)
	}
}

// TestSetRoleMasterRefusedWhileConfigLocked: while another process holds
// the managed config's lock (the agent mid-write), set-role master does
// not release around it: it waits, then refuses and leaves the role and
// the start lock as they were.
func TestSetRoleMasterRefusedWhileConfigLocked(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the config lock (30 s)")
	}
	r := newStartLockRig(t)
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return r.inst.Host(), nil })
	f, err := os.OpenFile(filepath.Join(filepath.Dir(r.inst.ConfigFile), mysql.ConfigLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("set-role", "master", "--ya", "--dir", r.dir)
	if code != 1 || !strings.Contains(errOut, "kunci start tidak bisa dilepas (") || !strings.Contains(errOut, "kunci konfigurasi") ||
		!strings.Contains(errOut, "); peran tidak diubah") {
		t.Errorf("set-role master with the config locked = %d, %q; want 1 and the refusal", code, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, mysql.RoleFile)); strings.TrimSpace(string(b)) != "quarantined" {
		t.Errorf("role after the refusal = %q", b)
	}
	if line, file := r.lineAndFile(); !line || !file {
		t.Errorf("after the refusal: line=%v file=%v", line, file)
	}
}

// TestForgetReplicaCLI: "agent forget-replica HOST" removes the host from
// the replicas-seen file and its entries from the standing promote lock's
// readable replicas, saying how many lock entries went; a host recorded
// nowhere is said so. It never needs MySQL.
func TestForgetReplicaCLI(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(t.TempDir(), "replicas-seen")
	oldSeen, oldHost := cliSeenPath, cliMySQLHost
	cliSeenPath = seen
	cliMySQLHost = func(string) (mysql.Host, error) { t.Error("forget-replica opened MySQL"); return nil, errors.New("no") }
	t.Cleanup(func() { cliSeenPath, cliMySQLHost = oldSeen, oldHost })
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Main(args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if err := mysql.RecordReplicasSeen(seen, []string{"10.0.0.3", "10.0.0.4"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := p.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: deadline,
		Readable: []string{"10.0.0.3:3306", "10.0.0.3:3307", "10.0.0.4:3306"}}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run("forget-replica", "10.0.0.3", "--dir", dir)
	if code != 0 || !strings.Contains(out, "replika 10.0.0.3 dihapus dari daftar replika (2 entri kunci promosi ikut dihapus)") ||
		!strings.Contains(out, "Penjaga kunci promosi tidak lagi menunggu 10.0.0.3") {
		t.Errorf("forget-replica = %d, %q, %q", code, out, errOut)
	}
	if ok, err := mysql.ReplicaSeen(seen, "10.0.0.3"); ok || err != nil {
		t.Errorf("10.0.0.3 still seen: %v, %v", ok, err)
	}
	if ok, err := mysql.ReplicaSeen(seen, "10.0.0.4"); !ok || err != nil {
		t.Errorf("10.0.0.4 no longer seen: %v, %v", ok, err)
	}
	l, ok, err := p.ReadLock()
	if !ok || err != nil || strings.Join(l.Readable, ",") != "10.0.0.4:3306" || l.Candidate != "10.0.0.2:3306" || !l.Deadline.Equal(deadline) {
		t.Errorf("lock after forget-replica = %+v, %v, %v", l, ok, err)
	}

	code, out, errOut = run("forget-replica", "--dir", dir, "10.0.0.3")
	if code != 1 || !strings.Contains(errOut, "10.0.0.3 tidak ada di daftar replika") {
		t.Errorf("forget-replica again = %d, %q, %q", code, out, errOut)
	}
	if code, _, errOut := run("forget-replica", "--dir", dir); code != 2 || !strings.Contains(errOut, "forget-replica <host>") {
		t.Errorf("forget-replica without a host = %d, %q", code, errOut)
	}
	if _, out, _ := run("help"); !strings.Contains(out, "dbpanel agent forget-replica <host> [--dir DIR]") {
		t.Errorf("usage lacks forget-replica: %s", out)
	}

	// host:port as the master_lock_kept alert names it (FR-9 a).
	code, out, errOut = run("forget-replica", "10.0.0.4:3306", "--dir", dir)
	if code != 0 || !strings.Contains(out, "replika 10.0.0.4 dihapus dari daftar replika (1 entri kunci promosi ikut dihapus)") {
		t.Errorf("forget-replica host:port = %d, %q, %q", code, out, errOut)
	}
	if ok, err := mysql.ReplicaSeen(seen, "10.0.0.4"); ok || err != nil {
		t.Errorf("10.0.0.4 still seen: %v, %v", ok, err)
	}
}

// TestForgetReplicaPortCLI (SA-4): "agent forget-replica-port PORT" removes
// one port from the replica-ports file; "semua" empties it and clears its
// full mark. It never needs MySQL.
func TestForgetReplicaPortCLI(t *testing.T) {
	dir := t.TempDir()
	ports := filepath.Join(t.TempDir(), "replica-ports-seen")
	oldPorts, oldHost := cliSeenPortsPath, cliMySQLHost
	cliSeenPortsPath = ports
	cliMySQLHost = func(string) (mysql.Host, error) {
		t.Error("forget-replica-port opened MySQL")
		return nil, errors.New("no")
	}
	t.Cleanup(func() { cliSeenPortsPath, cliMySQLHost = oldPorts, oldHost })
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Main(args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	var many []int
	for p := 20000; p < 20020; p++ {
		many = append(many, p)
	}
	if err := mysql.RecordReplicaPortsSeen(ports, many, time.Now()); err == nil {
		t.Fatal("20 ports recorded without the full mark")
	}
	// What forgetting a port loses (R-119): a replica on it that is not
	// connected now is not recorded again by itself.
	const caution = "tidak masuk daftar lagi dengan sendirinya"
	code, out, errOut := run("forget-replica-port", "20000", "--dir", dir)
	if code != 0 || !strings.Contains(out, "port 20000 dihapus") || !strings.Contains(out, caution) {
		t.Errorf("forget-replica-port 20000 = %d, %q, %q", code, out, errOut)
	}
	if code, _, errOut := run("forget-replica-port", "20000", "--dir", dir); code != 1 || !strings.Contains(errOut, "port 20000 tidak ada di daftar port replika") {
		t.Errorf("forget-replica-port again = %d, %q", code, errOut)
	}
	if code, _, errOut := run("forget-replica-port", "bukan", "--dir", dir); code != 2 || !strings.Contains(errOut, "bukan port") {
		t.Errorf("forget-replica-port bukan = %d, %q", code, errOut)
	}
	code, out, errOut = run("forget-replica-port", "semua", "--dir", dir)
	if code != 0 || !strings.Contains(out, "semua port dihapus dari daftar port replika") {
		t.Errorf("forget-replica-port semua = %d, %q, %q", code, out, errOut)
	}
	// semua names what it removed: each port (the cap kept 20000-20007,
	// 20000 is gone already), the full mark, the file, and the caution.
	for p := 20001; p <= 20007; p++ {
		if !strings.Contains(out, fmt.Sprint(p)) {
			t.Errorf("forget-replica-port semua does not name removed port %d: %q", p, out)
		}
	}
	for _, want := range []string{ports, "penuh", caution, "promosi terencana berikutnya"} {
		if !strings.Contains(out, want) {
			t.Errorf("forget-replica-port semua lacks %q: %q", want, out)
		}
	}
	if b, err := os.ReadFile(ports); err != nil || strings.TrimSpace(string(b)) != "" {
		t.Errorf("replica-ports-seen after semua = %q, %v; want empty", b, err)
	}
	// An empty record: nothing removed, said plainly.
	code, out, errOut = run("forget-replica-port", "semua", "--dir", dir)
	if code != 1 || out != "" || !strings.Contains(errOut, "sudah kosong") || !strings.Contains(errOut, "tidak ada yang dihapus") ||
		!strings.Contains(errOut, ports) {
		t.Errorf("forget-replica-port semua on an empty record = %d, %q, %q", code, out, errOut)
	}
	// A file holding more ports than the agent's cap (written by hand or
	// by an older agent): every removed port is named, so the caution can
	// be acted on for each.
	var lines []string
	for p := 30001; p <= 30012; p++ {
		lines = append(lines, fmt.Sprintf("%d %s", p, time.Now().UTC().Format(time.RFC3339)))
	}
	if err := os.WriteFile(ports, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = run("forget-replica-port", "semua", "--dir", dir)
	if code != 0 || !strings.Contains(out, caution) || strings.Contains(out, "port lain") {
		t.Errorf("forget-replica-port semua of 12 ports = %d, %q, %q", code, out, errOut)
	}
	for p := 30001; p <= 30012; p++ {
		if !strings.Contains(out, fmt.Sprint(p)) {
			t.Errorf("forget-replica-port semua of 12 ports does not name %d: %q", p, out)
		}
	}
	// Only the full mark, no port: said plainly, with no caution about
	// ports "listed above" when none is listed.
	if err := os.WriteFile(ports, []byte("penuh "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = run("forget-replica-port", "semua", "--dir", dir)
	if code != 0 || !strings.Contains(out, "tidak ada port di daftar; hanya tanda daftar port penuh yang dihapus") ||
		strings.Contains(out, "di atas") || strings.Contains(out, caution) || !strings.Contains(out, ports) {
		t.Errorf("forget-replica-port semua with only the full mark = %d, %q, %q", code, out, errOut)
	}
	_, out, _ = run("help")
	if !strings.Contains(out, "dbpanel agent forget-replica-port {<port>|semua} [--dir DIR]") {
		t.Errorf("usage lacks forget-replica-port: %s", out)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "port replika yang tidak tersambung tidak masuk daftar lagi dengan sendirinya") {
		t.Errorf("usage of forget-replica-port lacks the caution: %s", out)
	}
}

// TestCLIApprove: "agent approve <kode>" shows the source and its address,
// asks, and writes the local approval only for "ya"; "approve batal" removes
// the approval and the request.
func TestCLIApprove(t *testing.T) {
	oldHost, oldNow := cliHostname, cliNow
	t.Cleanup(func() { cliHostname, cliNow = oldHost, oldNow })
	cliHostname = func() (string, error) { return "db-lama", nil }

	setup := func(t *testing.T, role string) (dir string, req mysql.RebuildRequest) {
		t.Helper()
		dir = t.TempDir()
		p, _ := mysql.LoadPins(dir)
		if err := p.SetRole(role); err != nil {
			t.Fatal(err)
		}
		if err := p.SetSource("127.0.0.1", 3306); err != nil {
			t.Fatal(err)
		}
		if role == "quarantined" {
			var err error
			if req, err = p.EnsureRebuildRequest(time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return dir, req
	}
	run := func(in string, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := RunCLI(args, strings.NewReader(in), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	exists := func(dir, name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	t.Run("approved", func(t *testing.T) {
		dir, req := setup(t, "quarantined")
		code, out, errOut := run("ya\n", "approve", req.Code, "--dir", dir)
		if code != 0 {
			t.Fatalf("code = %d, stdout %q, stderr %q", code, out, errOut)
		}
		last := -1
		for _, want := range []string{"Server ini  : db-lama", "Sumber data : 127.0.0.1:3306", "Alamat IP   : 127.0.0.1",
			"SEMUA data MySQL di server ini dibuang",
			"Data yang dibuang tidak bisa dikembalikan.\nSelama persetujuan berlaku (24 jam), panel juga boleh mengubah " +
				"pengaturan replikasi, akun replikasi, pengaturan dan sertifikat MySQL di server ini, dan memulai ulang MySQL.\n",
			"Periksa data tertinggal", "Ketik ya"} {
			i := strings.Index(out, want)
			if i < 0 || i < last {
				t.Fatalf("stdout lacks %q in order: %q", want, out)
			}
			last = i
		}
		if !strings.Contains(out, "Disetujui sampai ") {
			t.Errorf("stdout = %q, want Disetujui sampai", out)
		}
		a, ok, err := mysqlLoad(t, dir).ReadRebuildApproval()
		if err != nil || !ok {
			t.Fatalf("approval = %v, %v", ok, err)
		}
		if a.Source != "127.0.0.1:3306" || a.Addr != "127.0.0.1" || a.Expires.Sub(a.Approved) != 24*time.Hour {
			t.Errorf("approval = %+v", a)
		}
		if exists(dir, mysql.RebuildRequestFile) {
			t.Error("request file still there")
		}
	})
	for name, in := range map[string]string{"y": "y\n", "empty input": ""} {
		t.Run("declined "+name, func(t *testing.T) {
			dir, req := setup(t, "quarantined")
			code, out, _ := run(in, "approve", req.Code, "--dir", dir)
			if code != 1 || !strings.Contains(out, "Dibatalkan; tidak ada yang disetujui.") {
				t.Errorf("code = %d, stdout %q", code, out)
			}
			if exists(dir, mysql.RebuildApprovalFile) || !exists(dir, mysql.RebuildRequestFile) {
				t.Error("approval written or request removed")
			}
		})
	}
	t.Run("wrong code", func(t *testing.T) {
		dir, req := setup(t, "quarantined")
		wrong := "000000"
		if req.Code == wrong {
			wrong = "000001"
		}
		code, _, errOut := run("ya\n", "approve", wrong, "--dir", dir)
		if code != 1 || !strings.Contains(errOut, "Kode salah.") || exists(dir, mysql.RebuildApprovalFile) {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
	t.Run("no request", func(t *testing.T) {
		dir, req := setup(t, "quarantined")
		if err := mysqlLoad(t, dir).RemoveRebuildRequest(); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := run("ya\n", "approve", req.Code, "--dir", dir)
		if code != 1 || !strings.Contains(errOut, "Tidak ada permintaan persetujuan yang berlaku") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
	t.Run("old request", func(t *testing.T) {
		dir, _ := setup(t, "quarantined")
		old := fmt.Sprintf(`{"code":"123456","source":"127.0.0.1:3306","created":%q}`, time.Now().Add(-31*time.Minute).Format(time.RFC3339))
		path := filepath.Join(dir, mysql.RebuildRequestFile)
		if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := run("ya\n", "approve", "123456", "--dir", dir)
		if code != 1 || !strings.Contains(errOut, "Tidak ada permintaan persetujuan yang berlaku") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
	t.Run("replica", func(t *testing.T) {
		dir, _ := setup(t, "replica")
		code, _, errOut := run("ya\n", "approve", "123456", "--dir", dir)
		if code != 1 || !strings.Contains(errOut, "tidak dikarantina (peran: replica)") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
	t.Run("batal", func(t *testing.T) {
		dir, _ := setup(t, "quarantined")
		p := mysqlLoad(t, dir)
		if err := p.WriteRebuildApproval(mysql.NewRebuildApproval("127.0.0.1:3306", "127.0.0.1", time.Now())); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := run("", "approve", "batal", "--dir", dir)
		if code != 0 || !strings.Contains(out, "dihapus") {
			t.Fatalf("code = %d, stdout %q, stderr %q", code, out, errOut)
		}
		if exists(dir, mysql.RebuildRequestFile) || exists(dir, mysql.RebuildApprovalFile) {
			t.Error("files still there")
		}
	})
	t.Run("no argument", func(t *testing.T) {
		if code, _, _ := run("", "approve"); code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
	})
}

func mysqlLoad(t *testing.T, dir string) *mysql.Pins {
	t.Helper()
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// hookReader runs f when it is first read, then reads from r.
type hookReader struct {
	r    io.Reader
	f    func()
	done bool
}

func (h *hookReader) Read(p []byte) (int, error) {
	if !h.done {
		h.done = true
		h.f()
	}
	return h.r.Read(p)
}

func approveSetup(t *testing.T, source string) (dir string, req mysql.RebuildRequest) {
	t.Helper()
	dir = t.TempDir()
	p := mysqlLoad(t, dir)
	host, port, err := mysql.ParseSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetRole("quarantined"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSource(host, port); err != nil {
		t.Fatal(err)
	}
	if req, err = p.EnsureRebuildRequest(time.Now()); err != nil {
		t.Fatal(err)
	}
	return dir, req
}

func approveRun(in io.Reader, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := RunCLI(args, in, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestCLIApproveRecheckAfterYes: a request that changed or went away while
// root read the screen is refused (B8) and nothing is approved.
func TestCLIApproveRecheckAfterYes(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, dir string){
		"new code": func(t *testing.T, dir string) {
			body := fmt.Sprintf(`{"code":"999999","source":"127.0.0.1:3306","created":%q}`, time.Now().Format(time.RFC3339))
			path := filepath.Join(dir, mysql.RebuildRequestFile)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"removed": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, mysql.RebuildRequestFile)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, req := approveSetup(t, "127.0.0.1:3306")
			if name == "new code" && req.Code == "999999" {
				t.Skip("code collision")
			}
			in := &hookReader{r: strings.NewReader("ya\n"), f: func() { change(t, dir) }}
			code, _, errOut := approveRun(in, "approve", req.Code, "--dir", dir)
			if code != 1 || !strings.Contains(errOut, "Permintaan berubah selama Anda membaca; jalankan perintah ini lagi.") {
				t.Errorf("code = %d, stderr %q", code, errOut)
			}
			if fileExists(filepath.Join(dir, mysql.RebuildApprovalFile)) {
				t.Error("approval written")
			}
		})
	}
}

func TestCLIApproveNoSource(t *testing.T) {
	dir := t.TempDir()
	if err := mysqlLoad(t, dir).SetRole("quarantined"); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := approveRun(strings.NewReader("ya\n"), "approve", "123456", "--dir", dir)
	if code != 1 || !strings.Contains(errOut, "Server ini dikarantina tanpa sumber yang dipin; pin master barunya dulu: sudo dbpanel agent set-source <host:port>") {
		t.Errorf("code = %d, stderr %q", code, errOut)
	}
}

// TestCLIApproveAddressErrors: a failed DNS lookup is B7 with the sentence
// once; any other failure to find the address is B9.
func TestCLIApproveAddressErrors(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		dir, req := approveSetup(t, "nama-tidak-ada.invalid:3306")
		code, out, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir)
		if code != 1 || strings.Count(errOut, "tidak ditemukan") != 1 ||
			!strings.Contains(errOut, "Alamat untuk nama sumber nama-tidak-ada.invalid:3306 tidak ditemukan (DNS: ") ||
			!strings.Contains(errOut, "); persetujuan tidak ditulis.") {
			t.Errorf("code = %d, stdout %q, stderr %q", code, out, errOut)
		}
	})
	t.Run("source address file", func(t *testing.T) {
		dir, req := approveSetup(t, "nama-tidak-ada.invalid:3306")
		path := filepath.Join(dir, mysql.SourceAddrFile)
		if err := os.WriteFile(path, []byte("10.0.0.9\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir)
		if code != 1 || strings.Contains(errOut, "(DNS") || !strings.Contains(errOut, "Pin server ini tidak bisa dibaca (") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
}

// TestCLIApproveNameChecks: approve always looks the source's name up
// before the prompt; a name with more than one address (B16) or one that
// no longer points at the address recorded at quarantine (B15) is refused
// with exit 1 and nothing written.
func TestCLIApproveNameChecks(t *testing.T) {
	answer := []net.IP{}
	old := cliLookupIP
	t.Cleanup(func() { cliLookupIP = old })
	cliLookupIP = func(context.Context, string) ([]net.IP, error) { return answer, nil }
	refused := func(t *testing.T, dir, code, want string) {
		t.Helper()
		rc, out, errOut := approveRun(strings.NewReader("ya\n"), "approve", code, "--dir", dir)
		if rc != 1 || errOut != want+"\n" || strings.Contains(out, "Ketik ya") {
			t.Errorf("code = %d, stdout %q, stderr %q; want B text before any prompt", rc, out, errOut)
		}
		if fileExists(filepath.Join(dir, mysql.RebuildApprovalFile)) {
			t.Error("approval written")
		}
	}
	t.Run("two addresses", func(t *testing.T) {
		dir, req := approveSetup(t, "db-baru.lan:3306")
		answer = []net.IP{net.ParseIP("10.0.0.7"), net.ParseIP("10.0.0.6")}
		refused(t, dir, req.Code, "Nama sumber db-baru.lan:3306 mengarah ke lebih dari satu alamat (10.0.0.6, 10.0.0.7). "+
			"Persetujuan hanya bisa diberikan bila nama itu mengarah ke satu alamat. Tetapkan satu alamat untuk nama itu di "+
			"server ini (misalnya di /etc/hosts), lalu jalankan perintah approve ini lagi. Persetujuan tidak ditulis.")
	})
	t.Run("name moved, then set-source and approve", func(t *testing.T) {
		dir, req := approveSetup(t, "db-baru.lan:3306")
		path := filepath.Join(dir, mysql.SourceAddrFile)
		if err := os.WriteFile(path, []byte("10.0.0.9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		answer = []net.IP{net.ParseIP("10.0.0.6")}
		refused(t, dir, req.Code, "Nama sumber db-baru.lan:3306 sekarang mengarah ke 10.0.0.6, bukan ke alamat yang "+
			"dicatat saat karantina (10.0.0.9). Bila alamat baru itu memang master baru, jalankan: sudo dbpanel agent "+
			"set-source db-baru.lan:3306, lalu jalankan perintah approve ini lagi. Persetujuan tidak ditulis.")
		// The way out the text names works: set-source keeps the request
		// and clears the recorded address; approve then succeeds.
		if rc, _, errOut := approveRun(strings.NewReader(""), "set-source", "db-baru.lan:3306", "--dir", dir); rc != 0 {
			t.Fatalf("set-source = %d, %q", rc, errOut)
		}
		rc, out, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir)
		if rc != 0 || !strings.Contains(out, "Alamat IP   : 10.0.0.6") {
			t.Fatalf("approve after set-source = %d, stdout %q, stderr %q", rc, out, errOut)
		}
	})
	t.Run("recorded address resolved", func(t *testing.T) {
		dir, req := approveSetup(t, "db-baru.lan:3306")
		path := filepath.Join(dir, mysql.SourceAddrFile)
		if err := os.WriteFile(path, []byte("10.0.0.9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		answer = []net.IP{net.ParseIP("10.0.0.9")}
		if rc, out, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir); rc != 0 ||
			!strings.Contains(out, "Alamat IP   : 10.0.0.9") {
			t.Errorf("approve = %d, stdout %q, stderr %q", rc, out, errOut)
		}
	})
}

// TestCLIApproveRollsBack: when a step after the approval write fails, the
// approval file is removed before the failure is reported.
func TestCLIApproveRollsBack(t *testing.T) {
	t.Run("request not removable", func(t *testing.T) {
		dir, req := approveSetup(t, "127.0.0.1:3306")
		path := filepath.Join(dir, mysql.RebuildRequestFile)
		// An immutable request file cannot be removed, even by root.
		if out, err := exec.Command("chattr", "+i", path).CombinedOutput(); err != nil {
			t.Skipf("chattr +i unavailable here: %v %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("chattr", "-i", path).Run() })
		code, _, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir)
		if code != 1 || !strings.Contains(errOut, "Gagal menghapus permintaan: ") ||
			!strings.Contains(errOut, "; persetujuan dibatalkan. Jalankan perintah ini lagi.") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
		if fileExists(filepath.Join(dir, mysql.RebuildApprovalFile)) {
			t.Error("approval left on disk")
		}
		if strings.Contains(errOut, "tidak bisa dihapus") {
			t.Errorf("rollback reported as failed: %q", errOut)
		}
	})
	t.Run("write fails and rollback fails", func(t *testing.T) {
		dir, req := approveSetup(t, "127.0.0.1:3306")
		// A non-empty directory where the approval goes: it cannot be
		// replaced and cannot be removed.
		blocker := filepath.Join(dir, mysql.RebuildApprovalFile)
		if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := approveRun(strings.NewReader("ya\n"), "approve", req.Code, "--dir", dir)
		lines := strings.Split(strings.TrimSpace(errOut), "\n")
		if code != 1 || len(lines) != 2 || !strings.HasPrefix(lines[0], "Gagal menulis persetujuan: ") ||
			!strings.HasPrefix(lines[1], "Berkas persetujuan tidak bisa dihapus (") ||
			!strings.HasSuffix(lines[1], "), jadi persetujuan mungkin tetap berlaku. Batalkan dengan: sudo dbpanel agent approve batal") {
			t.Errorf("code = %d, stderr %q", code, errOut)
		}
	})
}

// TestSetRoleOnQuarantinedAsks: leaving quarantine shows what the operator
// gives up and asks for "ya"; --ya skips the question; other roles never ask.
func TestSetRoleOnQuarantinedAsks(t *testing.T) {
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return noMysqldHost(t.TempDir()), nil })
	role := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, mysql.RoleFile))
		return strings.TrimSpace(string(b))
	}
	quarantined := func() string {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{mysql.RoleFile: "quarantined\n", mysql.SourceFile: "10.0.0.1:3306\n"})
		return dir
	}

	t.Run("master declined", func(t *testing.T) {
		dir := quarantined()
		code, out, _ := runSetRole("tidak\n", "set-role", "master", "--dir", dir)
		for _, want := range []string{"Server ini dikarantina (quarantined) mengikuti sumber 10.0.0.1:3306.", "dilepas sekarang",
			"dua master yang bisa ditulis", "Ketik ya untuk melanjutkan", "Dibatalkan; peran tidak diubah."} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q: %q", want, out)
			}
		}
		if code != 1 || role(dir) != "quarantined" {
			t.Errorf("code %d, role %q; want 1 and quarantined", code, role(dir))
		}
	})
	t.Run("empty input does not hang", func(t *testing.T) {
		dir := quarantined()
		code, out, _ := runSetRole("", "set-role", "master", "--dir", dir)
		if code != 1 || !strings.Contains(out, "Dibatalkan; peran tidak diubah.") || role(dir) != "quarantined" {
			t.Errorf("code %d, out %q, role %q", code, out, role(dir))
		}
	})
	t.Run("no source", func(t *testing.T) {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{mysql.RoleFile: "quarantined\n"})
		_, out, _ := runSetRole("", "set-role", "replica", "--dir", dir)
		if !strings.Contains(out, "Server ini dikarantina (quarantined) tanpa sumber yang dipin.") ||
			!strings.Contains(out, "menyalin ulang dari sumber yang dipin") {
			t.Errorf("stdout = %q", out)
		}
	})
	t.Run("unassigned confirmed", func(t *testing.T) {
		dir := quarantined()
		code, out, errOut := runSetRole("ya\n", "set-role", "unassigned", "--dir", dir)
		if code != 0 || !strings.Contains(out, "membuang semua datanya") ||
			!strings.Contains(out, "Langkah berikutnya: bila server ini sudah dihapus dari panel") {
			t.Errorf("code %d, out %q, err %q", code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(dir, mysql.RoleFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("role file after unassigned: %v", err)
		}
	})
	t.Run("master --ya without input", func(t *testing.T) {
		dir := quarantined()
		code, out, errOut := runSetRole("", "set-role", "master", "--ya", "--dir", dir)
		if code != 0 || strings.Contains(out, "Ketik ya") || role(dir) != "master" || !strings.Contains(out, nextMasterText) {
			t.Errorf("code %d, out %q, err %q, role %q", code, out, errOut, role(dir))
		}
	})
	t.Run("--ya before the role", func(t *testing.T) {
		dir := quarantined()
		code, out, errOut := runSetRole("", "set-role", "--ya", "master", "--dir", dir)
		if code != 0 || strings.Contains(out, "Ketik ya") || role(dir) != "master" {
			t.Errorf("code %d, out %q, err %q, role %q", code, out, errOut, role(dir))
		}
	})
	t.Run("replica pin does not ask", func(t *testing.T) {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{mysql.RoleFile: "replica\n", mysql.SourceFile: "10.0.0.1:3306\n"})
		code, out, errOut := runSetRole("", "set-role", "master", "--dir", dir)
		if code != 0 || strings.Contains(out, "Ketik ya") || role(dir) != "master" {
			t.Errorf("code %d, out %q, err %q, role %q", code, out, errOut, role(dir))
		}
	})
}

// TestSetRoleClearsSourceAndApprovalFiles: master drops the source pins,
// every role drops the rebuild request and approval, a replica keeps its
// source.
func TestSetRoleClearsSourceAndApprovalFiles(t *testing.T) {
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return noMysqldHost(t.TempDir()), nil })
	exists := func(dir, name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	setup := func(role string) string {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{
			mysql.RoleFile: role + "\n", mysql.SourceFile: "10.0.0.1:3306\n", mysql.SourceAddrFile: "10.0.0.1\n",
			mysql.RebuildRequestFile: "{}\n", mysql.RebuildApprovalFile: "{}\n",
		})
		return dir
	}
	dir := setup("quarantined")
	if code, out, errOut := runSetRole("", "set-role", "master", "--ya", "--dir", dir); code != 0 {
		t.Fatalf("set-role master = %d, %q, %q", code, out, errOut)
	}
	for _, name := range []string{mysql.SourceFile, mysql.SourceAddrFile, mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if exists(dir, name) {
			t.Errorf("%s left after set-role master", name)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, mysql.RoleFile)); strings.TrimSpace(string(b)) != "master" {
		t.Errorf("role = %q", b)
	}

	dir = setup("replica")
	if code, out, errOut := runSetRole("", "set-role", "replica", "--dir", dir); code != 0 {
		t.Fatalf("set-role replica = %d, %q, %q", code, out, errOut)
	}
	if !exists(dir, mysql.SourceFile) {
		t.Error("source pin removed by set-role replica")
	}
	for _, name := range []string{mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if exists(dir, name) {
			t.Errorf("%s left after set-role replica", name)
		}
	}
}

// TestSetRoleMasterNeverTouchesSecrets: releasing the start lock needs no
// agent credentials and survives an unreadable agent.yaml.
func TestSetRoleMasterNeverTouchesSecrets(t *testing.T) {
	var got config.Config
	stubLayoutHost(t, func(cfg config.Config) (mysql.Host, error) { got = cfg; return noMysqldHost(t.TempDir()), nil })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("panel_addr: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runSetRole("", "set-role", "master", "--dir", dir)
	if code != 0 {
		t.Fatalf("set-role master = %d, %q, %q", code, out, errOut)
	}
	if !reflect.DeepEqual(got, config.Config{}) {
		t.Errorf("stub got %+v, want the zero config", got)
	}
	for _, name := range []string{secretsKeyFile, credFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after set-role master: %v", name, err)
		}
	}
	if !strings.Contains(out, "Kunci start tidak ditemukan; tidak ada yang dilepas.") || !strings.Contains(out, nextMasterText) {
		t.Errorf("stdout = %q; want the not-found outcome and the next step", out)
	}

	if err := config.Save(dir, config.Config{PanelAddr: "127.0.0.1:1", ServerID: 1, MySQLSocket: "/run/x.sock"}); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runSetRole("", "set-role", "master", "--dir", dir); code != 0 || got.MySQLSocket != "/run/x.sock" {
		t.Errorf("code %d, err %q, socket %q; want 0 and /run/x.sock", code, errOut, got.MySQLSocket)
	}
}

// TestSetRoleMasterOutcomes: no mysqld still pins master; any other error
// from the layout keeps the role and says how to release by hand.
func TestSetRoleMasterOutcomes(t *testing.T) {
	const next = nextMasterText
	role := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, mysql.RoleFile))
		return strings.TrimSpace(string(b))
	}
	setup := func() string {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{mysql.RoleFile: "quarantined\n"})
		return dir
	}
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return nil, fmt.Errorf("discovery: %w", mysql.ErrNoMysqld) })
	dir := setup()
	code, out, errOut := runSetRole("", "set-role", "master", "--ya", "--dir", dir)
	if code != 0 || !strings.Contains(out, "MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu dilepas.") ||
		!strings.Contains(out, next) || role(dir) != "master" {
		t.Errorf("no mysqld: code %d, out %q, err %q, role %q", code, out, errOut, role(dir))
	}

	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return nil, errors.New("boom") })
	dir = setup()
	code, out, errOut = runSetRole("", "set-role", "master", "--ya", "--dir", dir)
	if code != 1 || !strings.Contains(errOut, "kunci start tidak bisa diperiksa (") || !strings.Contains(errOut, "boom") ||
		!strings.Contains(errOut, "Melepas kunci start dengan tangan") || role(dir) != "quarantined" || strings.Contains(out, next) {
		t.Errorf("other error: code %d, out %q, err %q, role %q", code, out, errOut, role(dir))
	}
}

// TestSetRoleUnreadablePinsAsk: pins that cannot be read may hide a
// quarantine, so leaving them asks like a quarantined server does.
func TestSetRoleUnreadablePinsAsk(t *testing.T) {
	called := false
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { called = true; return noMysqldHost(t.TempDir()), nil })
	setup := func() string {
		dir := t.TempDir()
		writePinFiles(t, dir, map[string]string{mysql.RoleFile: "quarantined\n", mysql.SourceFile: "garbage\n"})
		return dir
	}
	roleOf := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, mysql.RoleFile))
		return strings.TrimSpace(string(b))
	}
	dir := setup()
	code, out, _ := runSetRole("tidak\n", "set-role", "master", "--dir", dir)
	for _, want := range []string{"Pin server ini tidak bisa dibaca (", "); server ini mungkin dikarantina (quarantined).",
		"dilepas sekarang", "Ketik ya untuk melanjutkan", "Dibatalkan; peran tidak diubah."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q: %q", want, out)
		}
	}
	if code != 1 || roleOf(dir) != "quarantined" || called {
		t.Errorf("declined: code %d, role %q, stub called %v; want 1, quarantined, false", code, roleOf(dir), called)
	}
	if code, out, errOut := runSetRole("ya\n", "set-role", "master", "--dir", dir); code != 0 || roleOf(dir) != "master" {
		t.Errorf("ya: code %d, out %q, err %q, role %q", code, out, errOut, roleOf(dir))
	}
	dir = setup()
	if code, out, errOut := runSetRole("", "set-role", "master", "--ya", "--dir", dir); code != 0 || strings.Contains(out, "Ketik ya") || roleOf(dir) != "master" {
		t.Errorf("--ya: code %d, out %q, err %q, role %q", code, out, errOut, roleOf(dir))
	}
}

// TestSetRoleRoleChangedWhileAsking: when the pinned role changes while the
// operator reads the question, nothing is changed.
func TestSetRoleRoleChangedWhileAsking(t *testing.T) {
	called := false
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { called = true; return noMysqldHost(t.TempDir()), nil })
	dir := t.TempDir()
	writePinFiles(t, dir, map[string]string{mysql.RoleFile: "quarantined\n", mysql.SourceFile: "10.0.0.1:3306\n"})
	in := &hookReader{r: strings.NewReader("ya\n"), f: func() {
		if err := os.WriteFile(filepath.Join(dir, mysql.RoleFile), []byte("replica\n"), 0o644); err != nil {
			t.Error(err)
		}
	}}
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"set-role", "master", "--dir", dir}, in, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Peran server ini berubah selama Anda membaca; jalankan perintah ini lagi.") || called {
		t.Errorf("code %d, stderr %q, stub called %v", code, stderr.String(), called)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, mysql.RoleFile)); strings.TrimSpace(string(b)) != "replica" {
		t.Errorf("role = %q; want the changed role untouched", b)
	}
}

// TestSetRoleMasterLockGoneRoleKept: the start lock is released, then the
// role cannot be written (an approval that cannot be removed): the operator
// is told the lock is gone and the role is not changed.
func TestSetRoleMasterLockGoneRoleKept(t *testing.T) {
	r := newStartLockRig(t)
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return r.inst.Host(), nil })
	if err := os.MkdirAll(filepath.Join(r.dir, mysql.RebuildApprovalFile, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runSetRole("", "set-role", "master", "--ya", "--dir", r.dir)
	if code != 1 || !strings.Contains(out, "Kunci start dilepas:") || !strings.Contains(errOut, "gagal mengubah peran: ") ||
		!strings.Contains(errOut, "Kunci start sudah dilepas, tetapi peran belum diubah. Jalankan perintah ini lagi; sampai itu MySQL tidak dipaksa baca-saja (read-only) saat menyala.") {
		t.Errorf("code %d, out %q, err %q", code, out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, mysql.RoleFile)); strings.TrimSpace(string(b)) != "quarantined" {
		t.Errorf("role = %q, want quarantined", b)
	}
	if line, file := r.lineAndFile(); line || file {
		t.Errorf("start lock: line=%v file=%v; want released", line, file)
	}
}
