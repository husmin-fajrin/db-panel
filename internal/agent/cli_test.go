package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
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
		if code, _, errOut := run("set-role", "unassigned", "--dir", dir); code != 0 {
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
