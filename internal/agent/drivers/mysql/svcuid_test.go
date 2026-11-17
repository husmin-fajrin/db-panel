package mysql_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// serviceWarning is T19, the line written when a program runs as root.
const serviceWarning = "tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root"

// credRun is one command a credHost was asked to run: cred nil means as
// the agent (root).
type credRun struct {
	cred *syscall.Credential
	argv []string
}

// credHost records every command and the ids it runs as, then runs it on
// Host. before, when set, runs ahead of a command that is given ids;
// notStarted makes such a command fail as if it could not be started.
type credHost struct {
	mysql.Host
	mu         sync.Mutex
	runs       []credRun
	before     func(argv []string)
	notStarted bool
}

func (h *credHost) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	return h.ExecEnv(ctx, nil, stdin, stdout, name, args...)
}

func (h *credHost) ExecEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	return h.ExecEnvAs(ctx, nil, env, stdin, stdout, name, args...)
}

func (h *credHost) ExecEnvAs(ctx context.Context, cred *syscall.Credential, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	argv := append([]string{name}, args...)
	h.mu.Lock()
	h.runs = append(h.runs, credRun{cred: cred, argv: argv})
	before, notStarted := h.before, h.notStarted
	h.mu.Unlock()
	if cred != nil {
		if notStarted {
			return fmt.Errorf("%s gagal: %w (test)", name, mysql.ErrNotStarted)
		}
		if before != nil {
			before(argv)
		}
	}
	return h.Host.ExecEnvAs(ctx, cred, env, stdin, stdout, name, args...)
}

// of is the runs of program (by base name).
func (h *credHost) of(program string) []credRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []credRun
	for _, r := range h.runs {
		if filepath.Base(r.argv[0]) == program {
			out = append(out, r)
		}
	}
	return out
}

// owner is the uid and gid of path itself.
func owner(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return st.Uid, st.Gid
}

func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("running as another user needs root")
	}
}

// wantRunsAs fails unless runs is not empty and every run has the ids
// uid and gid and no supplementary groups.
func wantRunsAs(t *testing.T, what string, runs []credRun, uid, gid uint32) {
	t.Helper()
	if len(runs) == 0 {
		t.Errorf("%s: never ran", what)
	}
	for _, r := range runs {
		if r.cred == nil || r.cred.Uid != uid || r.cred.Gid != gid || len(r.cred.Groups) != 0 {
			t.Errorf("%s: %q ran as %+v, want uid %d gid %d without groups", what, r.argv, r.cred, uid, gid)
		}
	}
}

// committedLeftover is a leftover rig whose old master committed two
// transactions (in two binary log files) after its replica stopped, over
// a credHost; d logs to logs.
func committedLeftover(t *testing.T) (*leftoverRig, *credHost, *bytes.Buffer) {
	t.Helper()
	r := newLeftoverRig(t)
	execAll(t, rootDB(t, r.master), "FLUSH BINARY LOGS", "INSERT INTO toko.t VALUES (10, 'a')",
		"FLUSH BINARY LOGS", "UPDATE toko.t SET v = 'b' WHERE id = 10")
	h := &credHost{Host: r.master.Host()}
	r.d.Host = h
	var logs bytes.Buffer
	r.d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	var n atomic.Int64
	base := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	r.d.Now = func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
	return r, h, &logs
}

// TestBinlogToolsRunAsDirectoryOwner: mysql.leftover, mysql.salvage and
// mysql.binlogTail with MySQL down run mysqlbinlog as the owner of the
// binary log directory, and binlogTail reads the index with cat as the
// same ids.
func TestBinlogToolsRunAsDirectoryOwner(t *testing.T) {
	needRoot(t)
	r, h, logs := committedLeftover(t)
	uid, gid := owner(t, r.master.DataDir)
	if uid == 0 {
		t.Fatalf("data directory %s is root's", r.master.DataDir)
	}
	res := r.leftover()
	if res.Transactions != 2 {
		t.Fatalf("leftover = %+v, want 2 transactions", res)
	}
	wantRunsAs(t, "leftover", h.of("mysqlbinlog"), uid, gid)
	h.runs = nil
	decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: res.GTIDSet}))
	wantRunsAs(t, "salvage", h.of("mysqlbinlog"), uid, gid)

	r.master.Stop(t)
	h.runs = nil
	down := decode[proto.BinlogTailResult](t, r.dispatch("mysql.binlogTail", nil))
	if down.MySQLRunning || down.File == "" {
		t.Fatalf("binlogTail with MySQL down = %+v", down)
	}
	wantRunsAs(t, "binlogTail", h.of("mysqlbinlog"), uid, gid)
	cats := h.of("cat")
	wantRunsAs(t, "binlog index", cats, uid, gid)
	for _, c := range cats {
		if len(c.argv) != 3 || c.argv[1] != "--" || filepath.Ext(c.argv[2]) != ".index" {
			t.Errorf("index read as %q, want cat -- <index>", c.argv)
		}
	}
	if strings.Contains(logs.String(), serviceWarning) {
		t.Errorf("warned while running as the owner:\n%s", logs)
	}
}

// TestServiceCredentialIsDirectoryOwner: the ids are the directory's
// owner and group, whoever that is, without supplementary groups; a
// symlink is not followed, so its owner's ids are used, never the
// target's (root's).
func TestServiceCredentialIsDirectoryOwner(t *testing.T) {
	needRoot(t)
	dir := filepath.Join(t.TempDir(), "binlog")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, 1234, 4321); err != nil {
		t.Fatal(err)
	}
	cred, err := mysql.ServiceCredential(dir)
	if err != nil || cred.Uid != 1234 || cred.Gid != 4321 || len(cred.Groups) != 0 || cred.NoSetGroups {
		t.Errorf("ServiceCredential(dir of 1234:4321) = %+v, %v", cred, err)
	}

	rootDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(rootDir, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(link, 1234, 4321); err != nil {
		t.Fatal(err)
	}
	cred, err = mysql.ServiceCredential(link)
	if err != nil || cred.Uid != 1234 || cred.Gid != 4321 {
		t.Errorf("ServiceCredential(symlink of 1234 to root's directory) = %+v, %v", cred, err)
	}
}

// fakeExecHost answers every command with out (nil host underneath).
type fakeExecHost struct {
	mysql.Host
	mu    sync.Mutex
	runs  []credRun
	out   string
	asErr error // what a run with ids returns
	hang  bool  // a run with ids waits for its context
}

func (h *fakeExecHost) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	return h.ExecEnvAs(ctx, nil, nil, stdin, stdout, name, args...)
}

func (h *fakeExecHost) ExecEnvAs(ctx context.Context, cred *syscall.Credential, _ []string, _ io.Reader, stdout io.Writer, name string, args ...string) error {
	h.mu.Lock()
	h.runs = append(h.runs, credRun{cred: cred, argv: append([]string{name}, args...)})
	h.mu.Unlock()
	if cred != nil {
		if h.hang {
			<-ctx.Done()
			return fmt.Errorf("%s gagal: %v", name, ctx.Err())
		}
		if h.asErr != nil {
			return h.asErr
		}
	}
	_, err := io.WriteString(stdout, h.out)
	return err
}

// warnings counts the warnings given to it.
type warnings struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnings) warn(msg string) {
	w.mu.Lock()
	w.msgs = append(w.msgs, msg)
	w.mu.Unlock()
}

func (w *warnings) wantT19Once(t *testing.T, what string) {
	t.Helper()
	if len(w.msgs) != 1 || w.msgs[0] != serviceWarning {
		t.Errorf("%s: warnings %q, want T19 once", what, w.msgs)
	}
}

// TestServiceCredentialRootCountsAsMissing: a directory root owns gives
// no ids: the command runs as now (Exec, as root) with T19 once.
func TestServiceCredentialRootCountsAsMissing(t *testing.T) {
	t.Cleanup(mysql.SetLstatOwner(func(string) (uint32, uint32, error) { return 0, 0, nil }))
	if _, err := mysql.ServiceCredential("/var/lib/mysql"); !errors.Is(err, mysql.ErrServiceRoot) {
		t.Errorf("ServiceCredential(root's) err = %v, want ErrServiceRoot", err)
	}
	h := &fakeExecHost{out: "keluaran"}
	var w warnings
	var out bytes.Buffer
	if err := mysql.ExecAsService(context.Background(), h, "/var/lib/mysql", w.warn, nil, &out, "mysqlbinlog", "x"); err != nil {
		t.Fatal(err)
	}
	if len(h.runs) != 1 || h.runs[0].cred != nil || out.String() != "keluaran" {
		t.Errorf("runs %+v out %q, want one as root", h.runs, out.String())
	}
	w.wantT19Once(t, "root's directory")
}

// TestServiceCredentialMissingFallsBackToRoot: when the directory's
// owner cannot be read, Periksa and Amankan still run (as root), warn
// once each and give what they give as the owner.
func TestServiceCredentialMissingFallsBackToRoot(t *testing.T) {
	needRoot(t)
	r, h, logs := committedLeftover(t)
	owned := r.leftover()
	salvaged := decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: owned.GTIDSet}))
	if strings.Contains(logs.String(), serviceWarning) {
		t.Fatalf("warned before the owner was hidden:\n%s", logs)
	}

	t.Cleanup(mysql.SetLstatOwner(func(string) (uint32, uint32, error) { return 0, 0, errors.New("lstat gagal (test)") }))
	h.runs = nil
	if got := r.leftover(); !reflect.DeepEqual(got, owned) {
		t.Errorf("leftover as root = %+v, want %+v", got, owned)
	}
	if n := strings.Count(logs.String(), serviceWarning); n != 1 {
		t.Errorf("leftover as root warned %d times, want once:\n%s", n, logs)
	}
	asRoot := decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: owned.GTIDSet}))
	if n := strings.Count(logs.String(), serviceWarning); n != 2 {
		t.Errorf("salvage as root: %d warnings in all, want 2:\n%s", n, logs)
	}
	for _, run := range h.of("mysqlbinlog") {
		if run.cred != nil {
			t.Errorf("%q ran as %+v, want root", run.argv, run.cred)
		}
	}
	if len(h.of("mysqlbinlog")) != 2 {
		t.Errorf("mysqlbinlog runs %d, want 2", len(h.of("mysqlbinlog")))
	}
	a, err := os.ReadFile(filepath.Join(r.d.SalvageDir, salvaged.Name))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.d.SalvageDir, asRoot.Name))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Errorf("salvage as root (%d bytes) differs from the owner's (%d bytes)", len(b), len(a))
	}
}

// binlogPathRe is a binary log file mysqlbinlog is given.
var binlogPathRe = regexp.MustCompile(`/binlog\.[0-9]+$`)

// TestServiceToolFailureIsNotRetriedAsRoot: mysqlbinlog that started as
// the owner and failed (a binary log swapped for a symlink to a file only
// root reads) fails Periksa and Amankan with its error; it is never run
// again as root, and Amankan leaves no file.
func TestServiceToolFailureIsNotRetriedAsRoot(t *testing.T) {
	needRoot(t)
	r, h, logs := committedLeftover(t)
	res := r.leftover()
	if res.Transactions != 2 {
		t.Fatalf("leftover = %+v, want 2 transactions", res)
	}
	secret := filepath.Join(t.TempDir(), "rahasia")
	if err := os.WriteFile(secret, []byte("hanya root"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The first binary log mysqlbinlog is given becomes a symlink to
	// secret while it runs as the owner; restore puts the file back.
	swaps := 0
	var restore func()
	h.before = func(argv []string) {
		for _, a := range argv {
			if !binlogPathRe.MatchString(a) {
				continue
			}
			saved := a + ".asli"
			if err := os.Rename(a, saved); err != nil {
				t.Error(err)
				return
			}
			if err := os.Symlink(secret, a); err != nil {
				t.Error(err)
				return
			}
			swaps++
			restore = func() {
				if err := os.Remove(a); err != nil {
					t.Error(err)
				}
				if err := os.Rename(saved, a); err != nil {
					t.Error(err)
				}
			}
			return
		}
	}
	uid, gid := owner(t, r.master.DataDir)
	h.runs = nil
	if resp := r.dispatch("mysql.leftover", proto.LeftoverParams{NewMaster: r.nm}); resp.OK ||
		!strings.Contains(resp.Error.Message, "Permission denied") {
		t.Errorf("leftover over a root-only file = %+v, want mysqlbinlog's own error", resp)
	}
	wantRunsAs(t, "leftover", h.of("mysqlbinlog"), uid, gid)
	if n := len(h.of("mysqlbinlog")); n != 1 {
		t.Errorf("mysqlbinlog ran %d times, want once", n)
	}
	if restore != nil {
		restore()
		restore = nil
	}

	h.runs = nil
	if resp := r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: res.GTIDSet}); resp.OK ||
		!strings.Contains(resp.Error.Message, "Permission denied") {
		t.Errorf("salvage over a root-only file = %+v, want mysqlbinlog's own error", resp)
	}
	wantRunsAs(t, "salvage", h.of("mysqlbinlog"), uid, gid)
	if n := len(h.of("mysqlbinlog")); n != 1 {
		t.Errorf("mysqlbinlog ran %d times, want once", n)
	}
	if restore != nil {
		restore()
	}
	entries, err := os.ReadDir(r.d.SalvageDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != mysql.LeftoverSetFile {
			t.Errorf("salvage directory holds %s after the failure", e.Name())
		}
	}
	if swaps != 2 || strings.Contains(logs.String(), serviceWarning) {
		t.Errorf("%d binary logs swapped, want 2; logs:\n%s", swaps, logs)
	}
}

// TestServiceToolNotStartedFallsBackToRoot: a program that could not be
// started under the owner's ids runs as root, once, with T19 once; any
// other failure under the ids is returned and never run as root.
func TestServiceToolNotStartedFallsBackToRoot(t *testing.T) {
	t.Cleanup(mysql.SetLstatOwner(func(string) (uint32, uint32, error) { return 1234, 4321, nil }))
	h := &fakeExecHost{out: "keluaran", asErr: fmt.Errorf("mysqlbinlog gagal: %w: fork/exec: resource temporarily unavailable", mysql.ErrNotStarted)}
	var w warnings
	var out bytes.Buffer
	if err := mysql.ExecAsService(context.Background(), h, "/var/lib/mysql", w.warn, nil, &out, "mysqlbinlog", "x"); err != nil {
		t.Fatal(err)
	}
	if len(h.runs) != 2 || h.runs[0].cred == nil || h.runs[0].cred.Uid != 1234 || h.runs[1].cred != nil || out.String() != "keluaran" {
		t.Errorf("runs %+v out %q, want one as 1234 then one as root", h.runs, out.String())
	}
	w.wantT19Once(t, "not started")

	failed := errors.New("mysqlbinlog gagal: exit status 1: Permission denied")
	h = &fakeExecHost{out: "keluaran", asErr: failed}
	w = warnings{}
	if err := mysql.ExecAsService(context.Background(), h, "/var/lib/mysql", w.warn, nil, io.Discard, "mysqlbinlog", "x"); err != failed {
		t.Errorf("err = %v, want the program's own", err)
	}
	if len(h.runs) != 1 || len(w.msgs) != 0 {
		t.Errorf("runs %+v warnings %q, want one run as the owner and no warning", h.runs, w.msgs)
	}
}

// TestExecEnvAsReportsNotStarted: the real host's ExecEnvAs says
// ErrNotStarted only for a program that never ran, not for one that ran
// and exited non-zero or was stopped by its context.
func TestExecEnvAsReportsNotStarted(t *testing.T) {
	h := mysql.NewHost(mysql.Layout{}, nil, nil, probe.RealSystem(), nil)
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false program")
	}
	creds := []*syscall.Credential{nil}
	if os.Geteuid() == 0 {
		creds = append(creds, &syscall.Credential{Uid: 65534, Gid: 65534})
	}
	ctx := context.Background()
	for _, cred := range creds {
		missing := filepath.Join(t.TempDir(), "tidak-ada")
		if err := h.ExecEnvAs(ctx, cred, nil, nil, io.Discard, missing); !errors.Is(err, mysql.ErrNotStarted) {
			t.Errorf("cred %+v: missing program err = %v, want ErrNotStarted", cred, err)
		}
		if err := h.ExecEnvAs(ctx, cred, nil, nil, io.Discard, falseBin); err == nil || errors.Is(err, mysql.ErrNotStarted) {
			t.Errorf("cred %+v: program exiting 1 err = %v, want a failure that is not ErrNotStarted", cred, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		err := h.ExecEnvAs(cctx, cred, nil, nil, io.Discard, "sleep", "10")
		cancel()
		if err == nil || errors.Is(err, mysql.ErrNotStarted) {
			t.Errorf("cred %+v: program stopped by its context err = %v, want a failure that is not ErrNotStarted", cred, err)
		}
	}
}

// TestBootLogTailAsDirectoryOwner: the guard reads the end of the error
// log with tail -c 65536 as the owner of the log's directory, at most 64
// KiB, within 5 seconds (a program that hangs is stopped); a directory of
// root's, one whose owner cannot be read, or a tail that could not start
// is read in the agent (readTail) with T19.
func TestBootLogTailAsDirectoryOwner(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "error.log")
	content := strings.Repeat("a", 70<<10) + "akhir\n"
	if err := os.WriteFile(log, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	restore := mysql.SetLstatOwner(func(p string) (uint32, uint32, error) {
		if p != dir {
			t.Errorf("owner read of %s, want %s", p, dir)
		}
		return 1234, 4321, nil
	})
	t.Cleanup(restore)

	h := &fakeExecHost{out: "ekor\n"}
	var w warnings
	got, err := mysql.ReadBootLogTail(ctx, h, log, w.warn)
	if err != nil || string(got) != "ekor\n" {
		t.Errorf("tail as owner = %q, %v", got, err)
	}
	want := []string{"tail", "-c", "65536", "--", log}
	if len(h.runs) != 1 || !reflect.DeepEqual(h.runs[0].argv, want) || h.runs[0].cred == nil ||
		h.runs[0].cred.Uid != 1234 || h.runs[0].cred.Gid != 4321 || len(w.msgs) != 0 {
		t.Errorf("runs %+v warnings %q, want %q as 1234:4321", h.runs, w.msgs, want)
	}

	h = &fakeExecHost{out: strings.Repeat("b", 65<<10)}
	if got, err := mysql.ReadBootLogTail(ctx, h, log, w.warn); err == nil || len(got) > 64<<10 {
		t.Errorf("tail printing more than 64 KiB = %d bytes, %v; want refused", len(got), err)
	}

	h = &fakeExecHost{hang: true}
	start := time.Now()
	if _, err := mysql.ReadBootLogTail(ctx, h, log, w.warn); err == nil {
		t.Error("hanging tail: no error")
	}
	if took := time.Since(start); took < 4*time.Second || took > 8*time.Second {
		t.Errorf("hanging tail stopped after %v, want 5s", took)
	}
	if len(w.msgs) != 0 {
		t.Errorf("warnings %q, want none", w.msgs)
	}

	wantInAgent := func(what string, h *fakeExecHost) {
		t.Helper()
		var w warnings
		got, err := mysql.ReadBootLogTail(ctx, h, log, w.warn)
		if err != nil || string(got) != content[len(content)-64<<10:] {
			t.Errorf("%s: read %d bytes, %v; want the file's last 64 KiB", what, len(got), err)
		}
		for _, r := range h.runs {
			if r.cred == nil {
				t.Errorf("%s: %q ran as root", what, r.argv)
			}
		}
		w.wantT19Once(t, what)
	}
	wantInAgent("tail not started", &fakeExecHost{asErr: fmt.Errorf("tail gagal: %w", mysql.ErrNotStarted)})
	mysql.SetLstatOwner(func(string) (uint32, uint32, error) { return 0, 0, nil })
	wantInAgent("root's directory", &fakeExecHost{})
	mysql.SetLstatOwner(func(string) (uint32, uint32, error) { return 0, 0, errors.New("lstat gagal (test)") })
	wantInAgent("owner unknown", &fakeExecHost{})

	if os.Geteuid() == 0 {
		// The real tail as nobody reads a file nobody may read.
		restore()
		owned := filepath.Join(openDir(t), "log")
		if err := os.Mkdir(owned, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(owned, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(owned, "error.log")
		if err := os.WriteFile(f, []byte("baris 7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(f, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		var w warnings
		got, err := mysql.ReadBootLogTail(ctx, mysql.NewHost(mysql.Layout{}, nil, nil, probe.RealSystem(), nil), f, w.warn)
		if err != nil || string(got) != "baris 7\n" || len(w.msgs) != 0 {
			t.Errorf("real tail as nobody = %q, %v, warnings %q", got, err, w.msgs)
		}
	}
}
