package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// openDir is a new directory every account can traverse (t.TempDir is
// 0700), removed when the test ends.
func openDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "dbpanel-startlock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStartLockPathRule(t *testing.T) {
	got, err := mysql.StartLockPath(mysql.Layout{IncludeDir: "/x/conf.d", ConfigFile: "/x/my.cnf", DataDir: "/var/lib/mysql"})
	if err != nil || got != "/x/conf.d/dbpanel-start-lock.sql" {
		t.Errorf("with IncludeDir = %q, %v", got, err)
	}

	// Without one: beside the main option file, symlinks followed.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "b", "real.cnf")
	if err := os.WriteFile(real, []byte("[mysqld]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "a", "my.cnf")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err = mysql.StartLockPath(mysql.Layout{ConfigFile: link, DataDir: filepath.Join(dir, "data")})
	if want := filepath.Join(dir, "b", mysql.StartLockFile); err != nil || got != want {
		t.Errorf("through a symlink = %q, %v; want %s", got, err, want)
	}

	// Never inside the data directory.
	for _, l := range []mysql.Layout{
		{IncludeDir: "/var/lib/mysql/conf.d", ConfigFile: "/etc/my.cnf", DataDir: "/var/lib/mysql"},
		{ConfigFile: filepath.Join(dir, "b", "real.cnf"), DataDir: filepath.Join(dir, "b")},
		{ConfigFile: filepath.Join(dir, "b", "real.cnf"), DataDir: dir},
		{ConfigFile: link, DataDir: filepath.Join(dir, "b") + "/"},
	} {
		if got, err := mysql.StartLockPath(l); err == nil {
			t.Errorf("%+v: %q, want an error (inside the data directory)", l, got)
		}
	}
	if got, err := mysql.StartLockPath(mysql.Layout{}); err == nil {
		t.Errorf("no config file: %q, want an error", got)
	}
	if strings.HasSuffix(mysql.StartLockFile, ".cnf") {
		t.Errorf("lock file %s would be read as an option file", mysql.StartLockFile)
	}
	if mysql.StartLockContent != "SET GLOBAL super_read_only=ON;\nSET PERSIST read_only=ON;\nSET PERSIST super_read_only=ON;\n" {
		t.Errorf("lock content = %q", mysql.StartLockContent)
	}
}

func TestStartLockLineInsertedWithoutTouchingOtherLines(t *testing.T) {
	const p = "/etc/mysql/conf.d/dbpanel-start-lock.sql"
	block := "[mysqld]\n# komentar operator\n  log_bin = binlog\t\n\nserver_id=7\r\nread_only=ON\ninit_file=/op/own.sql\n;lain\n"
	with := mysql.WithStartLockLine(block, p)
	if with != block+"init_file="+p+"\n" {
		t.Errorf("with line =\n%q", with)
	}
	if back := mysql.WithoutStartLockLine(with, p); back != block {
		t.Errorf("without(with(c)) =\n%q\nwant\n%q", back, block)
	}
	// Idempotent; an existing line is kept in place.
	if again := mysql.WithStartLockLine(with, p); again != with {
		t.Errorf("with twice =\n%q", again)
	}
	mid := "[mysqld]\ninit-file = " + p + "\nread_only=ON\n"
	if got := mysql.WithStartLockLine(mid, p); got != "[mysqld]\ninit_file="+p+"\nread_only=ON\n" {
		t.Errorf("existing line replaced = %q", got)
	}
	if got := mysql.WithoutStartLockLine(mid, p); got != "[mysqld]\nread_only=ON\n" {
		t.Errorf("existing line removed = %q", got)
	}
	// The operator's own init_file is never touched.
	if got := mysql.WithoutStartLockLine(block, p); got != block {
		t.Errorf("without on a block with only the operator's init_file =\n%q", got)
	}
	// Empty content; content without a final newline.
	if got := mysql.WithStartLockLine("", p); got != "[mysqld]\ninit_file="+p+"\n" {
		t.Errorf("empty = %q", got)
	}
	noNL := "[mysqld]\nread_only=ON"
	if got := mysql.WithStartLockLine(noNL, p); mysql.WithoutStartLockLine(got, p) != noNL ||
		!strings.HasSuffix(got, "\ninit_file="+p) {
		t.Errorf("no final newline: with = %q", got)
	}
	// The last group is not mysqld's: the line gets one of its own.
	client := "[mysqld]\nread_only=ON\n[client]\nport=3306\n"
	if got := mysql.WithStartLockLine(client, p); got != client+"[mysqld]\ninit_file="+p+"\n" {
		t.Errorf("after [client] = %q", got)
	}
}

func TestPrintDefaultsArgsExact(t *testing.T) {
	one := mysql.Layout{ConfigFile: "/etc/my.cnf", ConfigFiles: []string{"/etc/my.cnf"}}
	if got := mysql.PrintDefaultsArgs(one); !reflect.DeepEqual(got, []string{"--defaults-file=/etc/my.cnf", "--print-defaults"}) {
		t.Errorf("one config file: %q", got)
	}
	three := mysql.Layout{ConfigFile: "/etc/my.cnf", ConfigFiles: []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "/root/.my.cnf"}}
	if got := mysql.PrintDefaultsArgs(three); !reflect.DeepEqual(got, []string{"--print-defaults"}) {
		t.Errorf("three config files: %q", got)
	}
}

// printDefaultsHost answers mysqld --print-defaults with out and records
// every command it is asked to run.
type printDefaultsHost struct {
	mysql.Host
	mu   sync.Mutex
	out  string
	runs [][]string
}

func (h *printDefaultsHost) Exec(_ context.Context, _ io.Reader, stdout io.Writer, name string, args ...string) error {
	h.mu.Lock()
	h.runs = append(h.runs, append([]string{name}, args...))
	out := h.out
	h.mu.Unlock()
	_, err := io.WriteString(stdout, "/usr/sbin/mysqld would have been started with the following arguments:\n"+out+" \n")
	return err
}

// fakeLockHost is a layout with a my.cnf holding the agent's init_file
// line and the lock file beside it, in directories everyone can traverse.
func fakeLockHost(t *testing.T) (*printDefaultsHost, string) {
	t.Helper()
	dir := openDir(t)
	cnf := filepath.Join(dir, "my.cnf")
	l := mysql.Layout{MysqldPath: "/nonexistent/mysqld", ConfigFile: cnf, ConfigFiles: []string{cnf}, DataDir: filepath.Join(dir, "data")}
	lock, err := mysql.StartLockPath(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cnf, []byte("[mysqld]\nuser=nobody\n# BEGIN dbpanel\n[mysqld]\ninit_file="+lock+"\n# END dbpanel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(mysql.StartLockContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return &printDefaultsHost{Host: mysql.NewHost(l, nil, nil, probe.RealSystem(), nil)}, lock
}

func TestEffectiveInitFileOrder(t *testing.T) {
	t.Cleanup(mysql.SetMACConfinement(func(mysql.Layout) string { return "" }))
	h, lock := fakeLockHost(t)
	ctx := context.Background()

	h.out = "--init_file=/op/a.sql --user=nobody --init_file=" + lock
	st := mysql.StartLockStatus(ctx, h)
	if !st.Present || !st.Locked || st.Reason != "" || st.File != lock || !reflect.DeepEqual(st.OperatorInitFiles, []string{"/op/a.sql"}) {
		t.Errorf("agent's line last: %+v", st)
	}
	for _, run := range h.runs {
		if want := append([]string{"/nonexistent/mysqld"}, mysql.PrintDefaultsArgs(h.Layout())...); !reflect.DeepEqual(run, want) {
			t.Errorf("ran %q, want %q", run, want)
		}
	}

	h.out = "--init-file=" + lock + " --user=nobody --init_file=/op/a.sql"
	st = mysql.StartLockStatus(ctx, h)
	if st.Locked || !strings.Contains(st.Reason, "init_file lain dibaca sesudah baris agent: /op/a.sql") {
		t.Errorf("operator's line last: %+v", st)
	}

	// The line alone, the file alone.
	h.out = "--user=nobody"
	if st := mysql.StartLockStatus(ctx, h); st.Locked || st.Reason == "" {
		t.Errorf("init_file not read by mysqld: %+v", st)
	}
	h.out = "--init_file=" + lock
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if st := mysql.StartLockStatus(ctx, h); st.Locked || st.Reason != "berkas kunci "+lock+" tidak ada" {
		t.Errorf("no lock file: %+v", st)
	}
	if err := os.WriteFile(h.Layout().ConfigFile, []byte("[mysqld]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := mysql.StartLockStatus(ctx, h); st.Present || st.Locked || st.Reason != "baris init_file belum ada di konfigurasi MySQL" {
		t.Errorf("no line: %+v", st)
	}
}

func TestMACAllowedPath(t *testing.T) {
	if !mysql.MACAllows("apparmor", "/etc/mysql/mysql.conf.d/x.sql") {
		t.Error("apparmor refuses /etc/mysql/mysql.conf.d/x.sql")
	}
	if mysql.MACAllows("apparmor", "/etc/x.sql") {
		t.Error("apparmor allows /etc/x.sql")
	}

	h, lock := fakeLockHost(t)
	h.out = "--user=nobody --init_file=" + lock
	ctx := context.Background()
	// AppArmor: the lock under /var/tmp is not in the mysqld profile.
	restore := mysql.SetMACConfinement(func(mysql.Layout) string { return "apparmor" })
	st := mysql.StartLockStatus(ctx, h)
	restore()
	if st.Locked || !st.MACConfined || !strings.Contains(st.Reason, "mungkin ditolak profil AppArmor/SELinux mysqld") {
		t.Errorf("apparmor, lock outside /etc/mysql: %+v", st)
	}

	t.Cleanup(mysql.SetMACConfinement(func(mysql.Layout) string { return "selinux" }))
	label := "system_u:object_r:mysqld_etc_t:s0"
	t.Cleanup(mysql.SetReadXattr(func(path, name string) (string, error) {
		if path != lock || name != "security.selinux" {
			return "", fmt.Errorf("unexpected xattr %s %s", path, name)
		}
		return label + "\x00", nil
	}))
	if st := mysql.StartLockStatus(ctx, h); !st.Locked || !st.MACConfined {
		t.Errorf("selinux, mysqld_etc_t: %+v", st)
	}
	label = "system_u:object_r:etc_t:s0"
	if st := mysql.StartLockStatus(ctx, h); st.Locked || !st.MACConfined || !strings.Contains(st.Reason, "AppArmor/SELinux") {
		t.Errorf("selinux, etc_t: %+v", st)
	}
}

// orderHost fails the test when the managed config is written naming the
// lock file while that file does not exist.
type orderHost struct {
	mysql.Host
	t    *testing.T
	lock string
}

func (h orderHost) WriteConfig(content string) (string, error) {
	if strings.Contains(content, h.lock) {
		if _, err := os.Stat(h.lock); err != nil {
			h.t.Errorf("config written naming %s before the file: %v", h.lock, err)
		}
	}
	return h.Host.WriteConfig(content)
}

// lineAndFile reports whether the agent's line is in the managed config
// and the lock file exists, failing on a line without its file.
func lineAndFile(t *testing.T, h mysql.Host, lock string) (line, file bool) {
	t.Helper()
	line, err := mysql.StartLockLinePresent(h.Layout())
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(lock)
	file = err == nil
	if line && !file {
		t.Errorf("init_file line names %s, which does not exist", lock)
	}
	return line, file
}

func TestStartLockFileBeforeLine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	lock, err := mysql.StartLockPath(inst.Layout())
	if err != nil {
		t.Fatal(err)
	}
	h := orderHost{Host: inst.Host(), t: t, lock: lock}
	ctx := context.Background()
	good, err := os.ReadFile(inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	// An option mysqld does not know, before where the block goes.
	if err := os.WriteFile(inst.ConfigFile, append(append([]byte{}, good...), "dbpanel_no_such_option=1\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mysql.WriteStartLock(ctx, h); err == nil {
		t.Fatal("writeStartLock with a config mysqld refuses succeeded")
	}
	if line, file := lineAndFile(t, h, lock); line || !file {
		t.Errorf("after a refused config: line=%v file=%v; want the file only", line, file)
	}

	if err := os.WriteFile(inst.ConfigFile, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mysql.WriteStartLock(ctx, h); err != nil {
		t.Fatalf("writeStartLock: %v", err)
	}
	if line, file := lineAndFile(t, h, lock); !line || !file {
		t.Errorf("after writeStartLock: line=%v file=%v", line, file)
	}
	b, err := os.ReadFile(lock)
	if err != nil || string(b) != mysql.StartLockContent {
		t.Errorf("lock file = %q, %v", b, err)
	}
	if fi, err := os.Stat(lock); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("lock file mode = %v, %v", fi.Mode(), err)
	} else if uid, gid := fileOwner(fi); os.Geteuid() == 0 && (uid != 0 || gid != 0) {
		t.Errorf("lock file owner = %d:%d, want root", uid, gid)
	}
	// Idempotent: the config does not change again.
	before, _ := os.ReadFile(inst.ConfigFile)
	if err := mysql.WriteStartLock(ctx, h); err != nil {
		t.Fatalf("writeStartLock again: %v", err)
	}
	if after, _ := os.ReadFile(inst.ConfigFile); !bytes.Equal(before, after) {
		t.Errorf("second writeStartLock changed my.cnf:\n%s", after)
	}
	if st := mysql.StartLockStatus(ctx, h); !st.Present || !st.Locked {
		t.Errorf("status after writeStartLock: %+v", st)
	}
}

func TestStartLockedFalseWhenUnreadableByMysqlUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: mysqld runs as nobody only then")
	}
	inst := testmysql.Start(t, testmysql.Options{})
	h := inst.Host()
	ctx := context.Background()
	if err := mysql.WriteStartLock(ctx, h); err != nil {
		t.Fatal(err)
	}
	lock, _ := mysql.StartLockPath(h.Layout())
	if err := os.Chmod(lock, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := mysql.StartLockStatus(ctx, h); st.Locked || !strings.Contains(st.Reason, "tidak bisa dibaca pengguna nobody") {
		t.Errorf("lock file 0600: %+v", st)
	}
}

// TestQuarantinedWhileDownStartsReadOnlyUnderWrites: a once-promoted
// master (read-only OFF persisted) quarantined while its MySQL is down
// starts read-only: an application writing over TCP from before the
// start until well after it is ready never gets a row in, with no guard
// tick at all.
func TestQuarantinedWhileDownStartsReadOnlyUnderWrites(t *testing.T) {
	r := newQuarantineRig(t)
	execAll(t, r.db,
		"CREATE DATABASE w", "CREATE TABLE w.t (id INT AUTO_INCREMENT PRIMARY KEY, at DATETIME(3))",
		"CREATE USER 'app'@'127.0.0.1' IDENTIFIED BY 'app-pass'", "GRANT INSERT, SELECT ON w.* TO 'app'@'127.0.0.1'",
		"SET PERSIST read_only = OFF", "SET PERSIST super_read_only = OFF")
	r.inst.Stop(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if res.Applied || !res.StartLocked || res.StartLockReason != "" {
		t.Fatalf("quarantine while down = %+v, want not applied, start locked", res)
	}

	cfg := drv.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = "app", "app-pass", "tcp", fmt.Sprintf("127.0.0.1:%d", r.inst.Port)
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = time.Second, time.Second, time.Second
	app, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetMaxIdleConns(0) // every attempt is a new connection
	var (
		phase          atomic.Int32 // 0 before ready, 1 after
		attempts       [2]atomic.Int64
		refused        [2]atomic.Int64 // answered with a read-only refusal
		inserted       atomic.Int64
		stop, finished = make(chan struct{}), make(chan struct{})
	)
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			ph := phase.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := app.ExecContext(ctx, "INSERT INTO w.t (at) VALUES (NOW(3))")
			cancel()
			attempts[ph].Add(1)
			var me *drv.MySQLError
			switch {
			case err == nil:
				inserted.Add(1)
			case errors.As(err, &me) && me.Number == 1290:
				refused[ph].Add(1)
			}
		}
	}()
	// Writes are attempted against the stopped server before it starts.
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.inst.Service().Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.inst.Service().WaitReady(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	phase.Store(1)
	time.Sleep(3 * time.Second)
	close(stop)
	<-finished

	t.Logf("writes attempted: %d before ready, %d after (%d refused read-only after, %d before; %d inserted)",
		attempts[0].Load(), attempts[1].Load(), refused[1].Load(), refused[0].Load(), inserted.Load())
	if n := inserted.Load(); n != 0 {
		t.Errorf("%d INSERTs succeeded on the quarantined master", n)
	}
	// Writes reached the server from before it started until after it
	// was ready, and those after were refused for read-only (not lost to
	// connection errors), or the test proves nothing.
	if attempts[0].Load() == 0 || attempts[1].Load() < 10 || (inserted.Load() == 0 && refused[1].Load() == 0) {
		t.Fatalf("writes attempted before ready %d, after %d (refused read-only %d): the test proves nothing",
			attempts[0].Load(), attempts[1].Load(), refused[1].Load())
	}
	var rows int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM w.t").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows in w.t = %d, %v; want 0", rows, err)
	}
	if sro, ro := readOnly(t, r.db); !sro || !ro {
		t.Errorf("after start: super_read_only=%v read_only=%v", sro, ro)
	}
	if s, ro := persisted(t, r.db, "super_read_only"), persisted(t, r.db, "read_only"); s != "ON" || ro != "ON" {
		t.Errorf("persisted super_read_only=%q read_only=%q", s, ro)
	}
	r.wantEvents() // no guard tick ran
}

func TestQuarantineUpWritesStartLock(t *testing.T) {
	r := newQuarantineRig(t)
	r.seenHosts("10.9.9.9")
	r.promoted()
	res := decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Applied || res.Already || !res.StartLocked || res.StartLockReason != "" {
		t.Fatalf("quarantine = %+v, want applied and start locked", res)
	}
	h := r.inst.Host()
	lock, _ := mysql.StartLockPath(h.Layout())
	if line, file := lineAndFile(t, h, lock); !line || !file {
		t.Errorf("after quarantine: line=%v file=%v", line, file)
	}
	before, err := os.ReadFile(r.inst.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	res = decode[proto.QuarantineResult](t, r.quarantine(quarantineParams))
	if !res.Already || !res.Applied || !res.StartLocked {
		t.Errorf("quarantine again = %+v", res)
	}
	if after, _ := os.ReadFile(r.inst.ConfigFile); !bytes.Equal(before, after) {
		t.Errorf("requarantine changed my.cnf:\n%s\nwas\n%s", after, before)
	}
}

func TestConfigureKeepsPresentStartLockLine(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	h := inst.Host()
	if err := mysql.WriteStartLock(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	lock, _ := mysql.StartLockPath(h.Layout())
	// Even with its file gone, the line kept by configure has its file.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	configure(t, h, proto.ConfigureParams{Role: "replica"})
	if line, file := lineAndFile(t, h, lock); !line || !file {
		t.Errorf("after configure: line=%v file=%v", line, file)
	}
	content, _, err := mysql.ReadManagedContent(h.Layout())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, "\ninit_file="+lock+"\n") || !strings.Contains(content, "read_only=ON") {
		t.Errorf("block after configure replica:\n%s", content)
	}
}

func fileOwner(fi os.FileInfo) (uid, gid int) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}
