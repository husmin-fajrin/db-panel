package mysql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// recorder keeps every statement sent through a recording connection.
// failAfter, when set, is a statement that is executed and then answered
// with an error, as a cancel landing after MySQL applied it would be.
type recorder struct {
	mu        sync.Mutex
	stmts     []string
	failAfter string
}

func (r *recorder) failsAfter(q string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failAfter != "" && q == r.failAfter
}

func (r *recorder) add(q string) {
	r.mu.Lock()
	r.stmts = append(r.stmts, q)
	r.mu.Unlock()
}

// take returns the statements recorded since the last take.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.stmts
	r.stmts = nil
	return out
}

type recConnector struct {
	base driver.Connector
	rec  *recorder
}

func (c *recConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &recConn{Conn: conn, rec: c.rec}, nil
}

func (c *recConnector) Driver() driver.Driver { return c.base.Driver() }

// recConn records the statements of the go-sql-driver connection it wraps.
type recConn struct {
	driver.Conn
	rec *recorder
}

func (c *recConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.add(q)
	res, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil && c.rec.failsAfter(q) {
		return nil, errors.New("answer lost after the statement was applied (test)")
	}
	return res, err
}

func (c *recConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.add(q)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c *recConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }

func (c *recConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *recConn) IsValid() bool { return c.Conn.(driver.Validator).IsValid() }

// recordingDB is root's pool on inst whose statements rec records.
func recordingDB(t *testing.T, inst *testmysql.Instance) (*sql.DB, *recorder) {
	t.Helper()
	cfg, err := gomysql.ParseDSN(inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	base, err := gomysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	db := sql.OpenDB(&recConnector{base: base, rec: rec})
	t.Cleanup(func() { db.Close() })
	return db, rec
}

// flags is "read_only super_read_only" as 0 and 1.
func flags(t *testing.T, db *sql.DB) string {
	t.Helper()
	var ro, sro int
	if err := db.QueryRow("SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only").Scan(&ro, &sro); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d %d", ro, sro)
}

// liftLog collects a gate's log lines as "level: message".
type liftLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *liftLog) logf(level, format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, level+": "+fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *liftLog) level(level string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, s := range l.lines {
		if strings.HasPrefix(s, level+": ") {
			out = append(out, s)
		}
	}
	return out
}

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "agent", "lift.lock")
}

// liftedServer is a server with read_only and super_read_only as given
// ("1 1" or "1 0"), a gate on a lock file of the test's own, and root's
// pool on it.
func liftedServer(t *testing.T, want string) (*testmysql.Instance, *sql.DB, *mysql.LiftGate, string) {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	switch want {
	case "1 1":
		execAll(t, db, "SET GLOBAL super_read_only = ON")
	case "1 0":
		execAll(t, db, "SET GLOBAL read_only = ON")
	}
	if got := flags(t, db); got != want {
		t.Fatalf("flags before = %s, want %s", got, want)
	}
	path := lockPath(t)
	return inst, db, mysql.NewLiftGate(path), path
}

func begin(t *testing.T, g *mysql.LiftGate, db *sql.DB, log *liftLog) *mysql.LiftHold {
	t.Helper()
	logf := func(string, string, ...any) {}
	if log != nil {
		logf = log.logf
	}
	h, err := g.Begin(context.Background(), db, logf)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return h
}

func TestLiftCountsHolders(t *testing.T) {
	_, db, g, _ := liftedServer(t, "1 1")
	h1 := begin(t, g, db, nil)
	h2 := begin(t, g, db, nil)
	if err := h1.End(); err != nil {
		t.Fatalf("first end: %v", err)
	}
	if got := flags(t, db); got != "1 0" {
		t.Errorf("after the first end = %s, want 1 0 (the second holder still lifts)", got)
	}
	if err := h2.End(); err != nil {
		t.Fatalf("second end: %v", err)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after the last end = %s, want 1 1", got)
	}
}

// TestLiftFailedOffPutsBack: the first opener's SET GLOBAL
// super_read_only = OFF fails after MySQL applied it: begin puts
// super_read_only back on (only SET GLOBAL super_read_only = ON; read_only
// is never sent off), releases the lock, and answers the error.
func TestLiftFailedOffPutsBack(t *testing.T) {
	inst, db, g, _ := liftedServer(t, "1 1")
	rdb, rec := recordingDB(t, inst)
	rec.failAfter = "SET GLOBAL super_read_only = OFF"
	if h, err := g.Begin(context.Background(), rdb, func(string, string, ...any) {}); err == nil {
		h.End()
		t.Fatal("begin succeeded with its OFF failing")
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after the failed begin = %s, want 1 1", got)
	}
	var sets []string
	for _, q := range rec.take() {
		if strings.HasPrefix(q, "SET GLOBAL") {
			sets = append(sets, q)
		}
	}
	if want := []string{"SET GLOBAL super_read_only = OFF", "SET GLOBAL super_read_only = ON"}; !slices.Equal(sets, want) {
		t.Errorf("statements = %q, want %q", sets, want)
	}
	release, free := g.TryGuard()
	release()
	if !free {
		t.Error("the lock is still held after the failed begin")
	}
}

func TestLiftKeepsReadOnlyOn(t *testing.T) {
	inst, db, g, _ := liftedServer(t, "1 1")
	rdb, rec := recordingDB(t, inst)
	h := begin(t, g, rdb, nil)
	if got := flags(t, db); got != "1 0" {
		t.Errorf("during the lift = %s, want 1 0", got)
	}
	var sets []string
	for _, q := range rec.take() {
		if strings.HasPrefix(q, "SET GLOBAL") {
			sets = append(sets, q)
		}
	}
	if len(sets) != 1 || sets[0] != "SET GLOBAL super_read_only = OFF" {
		t.Errorf("global statements of the lift = %q, want only SET GLOBAL super_read_only = OFF", sets)
	}
	if err := h.End(); err != nil {
		t.Fatal(err)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after end = %s, want 1 1", got)
	}

	// A server that was 1 0 is left 1 0.
	execAll(t, db, "SET GLOBAL super_read_only = OFF")
	h = begin(t, g, rdb, nil)
	if h.SuperWasOn() {
		t.Error("SuperWasOn on a server whose super_read_only was off")
	}
	if err := h.End(); err != nil {
		t.Fatal(err)
	}
	if got := flags(t, db); got != "1 0" {
		t.Errorf("after end on a 1 0 server = %s, want 1 0", got)
	}
}

func TestLiftLaterHolderReassertsOff(t *testing.T) {
	_, db, g, _ := liftedServer(t, "1 1")
	h1 := begin(t, g, db, nil)
	execAll(t, db, "SET GLOBAL super_read_only = ON")
	h2 := begin(t, g, db, nil)
	if got := flags(t, db); got != "1 0" {
		t.Errorf("after the second begin = %s, want 1 0", got)
	}
	h2.End()
	h1.End()
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after both ends = %s, want 1 1", got)
	}
}

func TestLiftEndSendsNothingWhenAlreadyOn(t *testing.T) {
	inst, db, g, _ := liftedServer(t, "1 1")
	rdb, rec := recordingDB(t, inst)
	h := begin(t, g, rdb, nil)
	execAll(t, db, "SET GLOBAL super_read_only = ON")
	rec.take()
	if err := h.End(); err != nil {
		t.Errorf("end = %v, want nil", err)
	}
	for _, q := range rec.take() {
		if strings.HasPrefix(strings.ToUpper(q), "SET") {
			t.Errorf("end sent %q although super_read_only was on", q)
		}
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after end = %s, want 1 1", got)
	}
}

func TestLiftWaitsForForeignLock(t *testing.T) {
	_, db, g, path := liftedServer(t, "1 1")
	defer mysql.SetLiftLockWait(300 * time.Millisecond)()
	release, err := mysql.AcquireLiftLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	h, err := g.Begin(context.Background(), db, func(string, string, ...any) {})
	if !errors.Is(err, mysql.ErrLiftHeldByCommand) {
		t.Fatalf("begin while another descriptor holds the lock = %v, %v; want ErrLiftHeldByCommand", h, err)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 5*time.Second {
		t.Errorf("begin gave up after %s, want about the injected 300ms", took)
	}
	want := `kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`
	if err.Error() != want {
		t.Errorf("text = %q\nwant   %q", err.Error(), want)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("flags after the refused begin = %s, want 1 1", got)
	}
}

// holdInChild holds the lock at path in a child process (flock(1)) until
// the returned function kills it.
func holdInChild(t *testing.T, path string) (kill func()) {
	t.Helper()
	bin, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("flock(1) not installed")
	}
	f, err := mysql.OpenLiftLock(path, true)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	cmd := exec.Command(bin, "-x", "-o", path, "sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	kill = func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	}
	t.Cleanup(kill)
	deadline := time.Now().Add(10 * time.Second)
	for {
		release, err := mysql.AcquireLiftLock(path)
		if errors.Is(err, mysql.ErrLiftBusy) {
			return kill
		}
		if err == nil {
			release()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never held the lock: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLiftLockReleasedWhenHolderDies(t *testing.T) {
	path := lockPath(t)
	g := mysql.NewLiftGate(path)
	kill := holdInChild(t, path)
	if release, free := g.TryGuard(); free {
		release()
		t.Fatal("tryGuard free while a child holds the lock")
	}
	kill()
	release, free := g.TryGuard()
	defer release()
	if !free {
		t.Error("tryGuard not free after the holder was killed")
	}
}

func TestLiftLockNotInherited(t *testing.T) {
	_, db, g, path := liftedServer(t, "1 1")
	h := begin(t, g, db, nil)
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	if err := h.End(); err != nil {
		t.Fatal(err)
	}
	release, err := mysql.AcquireLiftLock(path)
	if err != nil {
		t.Fatalf("AcquireLiftLock while a child started during the lift runs: %v", err)
	}
	release()
}

func TestTryGuardSeesLocalLift(t *testing.T) {
	_, db, g, _ := liftedServer(t, "1 1")
	h := begin(t, g, db, nil)
	if release, free := g.TryGuard(); free {
		release()
		t.Error("tryGuard free during a lift")
	}
	h.End()
	release, free := g.TryGuard()
	release()
	if !free {
		t.Error("tryGuard not free after end")
	}
}

func TestAcquireLiftLockBusy(t *testing.T) {
	_, db, g, path := liftedServer(t, "1 1")
	h := begin(t, g, db, nil)
	defer h.End()
	release, err := mysql.AcquireLiftLock(path)
	if err == nil {
		release()
	}
	if !errors.Is(err, mysql.ErrLiftBusy) {
		t.Fatalf("AcquireLiftLock during a lift = %v, want ErrLiftBusy", err)
	}
	want := "kunci baca-saja server ini sedang dibuka pekerjaan lain (misalnya bangun ulang yang sedang menyalin data); coba lagi sesudah pekerjaan itu selesai"
	if err.Error() != want {
		t.Errorf("text = %q\nwant   %q", err.Error(), want)
	}
}

// snapshotDir lists dir's entries with their modes, link targets and
// contents, to tell that nothing changed.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("%s %v\n", dir, fi.Mode())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		out += fmt.Sprintf("%s %v ino=%d nlink=%d", e.Name(), fi.Mode(), st.Ino, st.Nlink)
		if target, err := os.Readlink(p); err == nil {
			out += " -> " + target
		}
		out += "\n"
	}
	return out
}

func TestLiftLockRefusesForeignFile(t *testing.T) {
	type setup func(t *testing.T, path string) (outside string)
	cases := []struct {
		name   string
		reason string
		setup  setup
	}{
		{"mode 0644", "bisa dibaca atau ditulis pengguna lain", func(t *testing.T, path string) string {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			os.Chmod(path, 0o644)
			return ""
		}},
		{"symlink", "bukan berkas biasa", func(t *testing.T, path string) string {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{"hard link", "punya lebih dari satu nama", func(t *testing.T, path string) string {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, path+".other"); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
		{"directory 0755", "bisa dibaca atau ditulis pengguna lain", func(t *testing.T, path string) string {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
	}
	if os.Geteuid() == 0 {
		cases = append(cases, struct {
			name   string
			reason string
			setup  setup
		}{"other owner", "bukan milik root", func(t *testing.T, path string) string {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, 65534, 65534); err != nil {
				t.Fatal(err)
			}
			return ""
		}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := lockPath(t)
			dir := filepath.Dir(path)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := c.setup(t, path)
			before := snapshotDir(t, dir)

			release, err := mysql.AcquireLiftLock(path)
			if err == nil {
				release()
			}
			var le *mysql.LiftLockError
			if !errors.As(err, &le) || le.Reason != c.reason || le.Path != path {
				t.Fatalf("AcquireLiftLock = %v, want a LiftLockError for %s (%s)", err, path, c.reason)
			}
			want := "berkas kunci " + path + " tidak aman dipakai (" + c.reason + "); tidak ada yang diubah. Agent membuat " +
				"ulang berkas itu saat membutuhkannya; bila pesan ini tetap muncul, mulai ulang agent lalu jalankan perintah ini lagi."
			if err.Error() != want {
				t.Errorf("text = %q\nwant   %q", err.Error(), want)
			}
			if after := snapshotDir(t, dir); after != before {
				t.Errorf("the CLI changed the directory:\nbefore %s\nafter  %s", before, after)
			}

			// The agent's gate replaces the file and goes on.
			release, free := mysql.NewLiftGate(path).TryGuard()
			release()
			if !free {
				t.Fatal("tryGuard not free over a replaced file")
			}
			fi, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			st := fi.Sys().(*syscall.Stat_t)
			if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
				t.Errorf("lock file after the gate: %v nlink=%d uid=%d, want a 0600 regular file of one name", fi.Mode(), st.Nlink, st.Uid)
			}
			if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
				t.Errorf("lock directory after the gate: %v, %v; want 0700", di.Mode(), err)
			}
			if outside != "" {
				if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
					t.Errorf("symlink target after the gate = %q, %v; want untouched", b, err)
				}
			}
			if release, err := mysql.AcquireLiftLock(path); err != nil {
				t.Errorf("AcquireLiftLock after the gate replaced the file: %v", err)
			} else {
				release()
			}
		})
	}

	// begin replaces a foreign file too, and lifts.
	_, db, g, path := liftedServer(t, "1 1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o644)
	h := begin(t, g, db, nil)
	if got := flags(t, db); got != "1 0" {
		t.Errorf("begin over a foreign file: flags %s, want 1 0", got)
	}
	h.End()
}

func TestLiftCreatesDirectoryAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib", "agent", "lift.lock")
	release, err := mysql.AcquireLiftLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for p, want := range map[string]os.FileMode{filepath.Dir(path): os.ModeDir | 0o700, path: 0o600} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode() != want {
			t.Errorf("%s: %v, %v; want %v", p, fi.Mode(), err, want)
		}
	}
}

func TestAbandonAfterRestartKeepsFlags(t *testing.T) {
	inst, db, g, path := liftedServer(t, "1 0")
	rdb, rec := recordingDB(t, inst)
	h := begin(t, g, rdb, nil)
	if h.SuperWasOn() {
		t.Fatal("SuperWasOn on a 1 0 server")
	}
	execAll(t, db, "SET GLOBAL super_read_only = ON") // as a restart with persisted values would
	rec.take()
	h.Abandon()
	h.Abandon()
	if got := rec.take(); len(got) != 0 {
		t.Errorf("abandon sent %q", got)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after abandon = %s, want 1 1", got)
	}
	release, err := mysql.AcquireLiftLock(path)
	if err != nil {
		t.Fatalf("lock after abandon: %v", err)
	}
	release()
	// The next lift reads the original value afresh.
	h = begin(t, g, db, nil)
	if !h.SuperWasOn() {
		t.Error("the next begin kept the forgotten value")
	}
	if err := h.End(); err != nil {
		t.Fatal(err)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after the next lift = %s, want 1 1", got)
	}
	if err := h.End(); err != nil {
		t.Errorf("second end = %v, want nil (no-op)", err)
	}
}

// TestCloneReleasesLift runs a real CLONE (the harness restarts mysqld
// after 3707 as restartAfterClone asks it to): afterwards the driver's
// gate is free and a new lift turns super_read_only off again.
func TestCloneReleasesLift(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replica := testmysql.Start(t, testmysql.Options{ExtraArgs: cloneArgs})
	replUser(t, master)
	execAll(t, rootDB(t, replica), "SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")

	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	ev := &jobEvents{done: make(chan proto.JobDone, 4)}
	runner := jobs.NewRunner(ev, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := commands.NewRegistry()
	d := &mysql.Driver{Host: replica.HostWith(creds), Creds: creds, Jobs: runner, LiftLock: lockPath(t)}
	d.Register(reg)
	rig := &snapshotRig{reg: reg, runner: runner, events: ev}
	snapshotResult(t, rig.run(t, "mysql.cloneFrom", cloneParams(master)))

	release, free := d.LiftGate().TryGuard()
	release()
	if !free {
		t.Fatal("tryGuard not free after the clone job")
	}
	db := rootDB(t, replica)
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after the clone = %s, want 1 1", got)
	}
	h := begin(t, d.LiftGate(), db, nil)
	if got := flags(t, db); got != "1 0" {
		t.Errorf("a lift after the clone: %s, want 1 0", got)
	}
	if err := h.End(); err != nil {
		t.Fatal(err)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after its end = %s, want 1 1", got)
	}
}

func TestLiftRestoreOnKeptConnection(t *testing.T) {
	inst, db, g, _ := liftedServer(t, "1 1")
	gdb := rootDB(t, inst)
	// Sessions opened from now on, the gate's kept one included, inherit it.
	execAll(t, db, "SET GLOBAL wait_timeout = 2")
	defer execAll(t, db, "SET GLOBAL wait_timeout = 28800")
	h := begin(t, g, gdb, nil)
	gdb.SetMaxIdleConns(0) // every other connection of the gate's pool is closed
	time.Sleep(4 * time.Second)
	if err := h.End(); err != nil {
		t.Fatalf("end after the kept connection idled past wait_timeout: %v", err)
	}
	if got := flags(t, db); got != "1 1" {
		t.Errorf("after end = %s, want 1 1", got)
	}
}

func TestLiftReleasesLockWhenRestoreFails(t *testing.T) {
	inst, db, g, _ := liftedServer(t, "1 1")
	gdb := rootDB(t, inst)
	gdb.SetMaxOpenConns(1) // the gate's kept connection is the pool's only one
	ctl, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	ids := func() map[int64]bool {
		rows, err := ctl.QueryContext(context.Background(), "SELECT ID FROM information_schema.processlist WHERE ID <> CONNECTION_ID()")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]bool{}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out[id] = true
		}
		return out
	}
	before := ids()
	log := &liftLog{}
	h := begin(t, g, gdb, log)
	var kept []int64
	for id := range ids() {
		if !before[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) != 1 {
		t.Fatalf("new connections after begin = %v, want the kept one", kept)
	}
	if _, err := ctl.ExecContext(context.Background(), "KILL "+strconv.FormatInt(kept[0], 10)); err != nil {
		t.Fatal(err)
	}
	if err := h.End(); err == nil {
		t.Error("end over a killed connection = nil, want an error")
	}
	if errs := log.level("error"); len(errs) != 1 {
		t.Errorf("error lines = %q, want one", errs)
	}
	release, free := g.TryGuard()
	release()
	if !free {
		t.Error("tryGuard not free after a failed restore")
	}
}

func TestLoadTrailer(t *testing.T) {
	for _, c := range []struct {
		full, on bool
		want     string
	}{
		{false, false, ""},
		{false, true, ""},
		{true, false, "\nFLUSH PRIVILEGES;\n"},
		{true, true, "\nSET GLOBAL super_read_only = ON;\nFLUSH PRIVILEGES;\n"},
	} {
		if got := mysql.LoadTrailer(c.full, c.on); got != c.want {
			t.Errorf("loadTrailer(%v, %v) = %q, want %q", c.full, c.on, got, c.want)
		}
	}
}
