package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// setVar sets *p to v for the test.
func setVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// lockedBuffer is a bytes.Buffer safe for a writer and a reader in other
// goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// dropRig is a quarantined agent directory over a running MySQL whose
// read-only flags are on and persisted, with every system default the
// operator commands use (lift lock, backup folder, MySQL layout, system
// log) pointed at the test's own.
type dropRig struct {
	t       *testing.T
	inst    *testmysql.Instance
	dir     string // agent directory: the pins
	lock    string // the lift lock file
	backups string // the default backup folder
	pw      string // root's MySQL password ("" for none)

	mu     sync.Mutex
	logged []string // system log lines
	conn   *sql.DB
}

func newDropRig(t *testing.T) *dropRig {
	t.Helper()
	r := &dropRig{t: t, inst: testmysql.Start(t, testmysql.Options{}), dir: t.TempDir()}
	r.lock = filepath.Join(t.TempDir(), "agent", "lift.lock")
	r.backups = filepath.Join(t.TempDir(), "dibuang")
	p, err := mysql.LoadPins(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetRole("quarantined"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSource("10.9.9.9", 3306); err != nil {
		t.Fatal(err)
	}
	setVar(t, &cliLiftLock, r.lock)
	setVar(t, &cliBackupDir, r.backups)
	stubLayoutHost(t, func(config.Config) (mysql.Host, error) { return r.inst.Host(), nil })
	t.Cleanup(func() {
		if r.conn != nil {
			r.conn.Close()
		}
	})
	r.exec("SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	return r
}

// db is a pool logged in as root (with r.pw).
func (r *dropRig) db() *sql.DB {
	r.t.Helper()
	if r.conn == nil {
		db, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: "root", Password: r.pw}, r.inst.Socket))
		if err != nil {
			r.t.Fatal(err)
		}
		r.conn = db
	}
	return r.conn
}

// setRootPassword gives root@localhost password pw.
func (r *dropRig) setRootPassword(pw string) {
	r.t.Helper()
	r.setup("ALTER USER 'root'@'localhost' IDENTIFIED BY '" + strings.ReplaceAll(strings.ReplaceAll(pw, `\`, `\\`), "'", "''") + "'")
	r.conn.Close()
	r.conn, r.pw = nil, pw
}

func (r *dropRig) exec(stmts ...string) {
	r.t.Helper()
	for _, q := range stmts {
		if _, err := r.db().Exec(q); err != nil {
			r.t.Fatalf("%s: %v", q, err)
		}
	}
}

// setup runs stmts with read-only lifted (read_only = OFF turns both
// flags off, and keeps the statement the recheck test looks for out of the
// general log), and puts super_read_only back; the persisted values stay
// ON.
func (r *dropRig) setup(stmts ...string) {
	r.t.Helper()
	r.exec("SET GLOBAL read_only = OFF")
	r.exec(stmts...)
	r.exec("SET GLOBAL super_read_only = ON")
}

// flags is "read_only super_read_only", e.g. "1 1".
func (r *dropRig) flags() string {
	r.t.Helper()
	var ro, sro int
	if err := r.db().QueryRow("SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro); err != nil {
		r.t.Fatal(err)
	}
	return fmt.Sprintf("%d %d", ro, sro)
}

// persisted is the persisted value of super_read_only.
func (r *dropRig) persisted() string {
	r.t.Helper()
	var v string
	if err := r.db().QueryRow("SELECT variable_value FROM performance_schema.persisted_variables WHERE variable_name = 'super_read_only'").Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *dropRig) gtidExecuted() string {
	r.t.Helper()
	var v string
	if err := r.db().QueryRow("SELECT @@global.gtid_executed").Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *dropRig) exists(name string) bool {
	r.t.Helper()
	var n int
	if err := r.db().QueryRow("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n == 1
}

// makeDB creates database name with tables t1..tN.
func (r *dropRig) makeDB(name string, tables int) {
	r.t.Helper()
	q := quoteIdent(name)
	stmts := []string{"CREATE DATABASE " + q}
	for i := 1; i <= tables; i++ {
		stmts = append(stmts, fmt.Sprintf("CREATE TABLE %s.t%d (id INT PRIMARY KEY, v VARCHAR(20))", q, i),
			fmt.Sprintf("INSERT INTO %s.t%d VALUES (1, 'satu'), (2, 'dua')", q, i))
	}
	r.setup(stmts...)
}

// syslog records a system log line.
func (r *dropRig) syslog(line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logged = append(r.logged, line)
	return nil
}

func (r *dropRig) syslogLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logged...)
}

// run runs drop-database in this process with input in.
func (r *dropRig) run(in io.Reader, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = RunCLIWith(append(append([]string{"drop-database"}, args...), "--dir", r.dir), in, &out, &errOut, CLISeams{Syslog: r.syslog})
	return code, out.String(), errOut.String()
}

// backupFiles lists the files in folder (none when it does not exist).
func backupFiles(t *testing.T, folder string) []string {
	t.Helper()
	ents, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, filepath.Join(folder, e.Name()))
	}
	return names
}

// interactive is a drop-database run in this process whose input the test
// writes line by line as the prompts appear.
type interactive struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *lockedBuffer
	err  *lockedBuffer
	done chan int
}

func (r *dropRig) interact(args ...string) *interactive {
	pr, pw := io.Pipe()
	s := &interactive{t: r.t, in: pw, out: &lockedBuffer{}, err: &lockedBuffer{}, done: make(chan int, 1)}
	go func() {
		s.done <- RunCLIWith(append(append([]string{"drop-database"}, args...), "--dir", r.dir), pr, s.out, s.err, CLISeams{Syslog: r.syslog})
		pr.Close()
	}()
	r.t.Cleanup(func() { pw.Close() })
	return s
}

// waitOut waits until stdout holds want.
func (s *interactive) waitOut(want string) {
	s.t.Helper()
	waitFor(s.t, func() bool { return strings.Contains(s.out.String(), want) }, func() string {
		return fmt.Sprintf("stdout never showed %q: stdout %q, stderr %q", want, s.out.String(), s.err.String())
	})
}

func (s *interactive) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *interactive) wait() int {
	s.t.Helper()
	select {
	case code := <-s.done:
		return code
	case <-time.After(3 * time.Minute):
		s.t.Fatalf("command never ended: stdout %q, stderr %q", s.out.String(), s.err.String())
	}
	return -1
}

// waitFor polls ok for up to a minute.
func waitFor(t *testing.T, ok func() bool, msg func() string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(msg())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// helperEnv carries a drop-database run to TestDropDatabaseHelperProcess.
const helperEnv = "DBPANEL_DROPDB_HELPER"

// helperSpec is what the helper process runs and the seams it sets.
type helperSpec struct {
	Args      []string
	Socket    string
	Lock      string
	Backups   string
	Syslog    string // file the system log lines are appended to
	Mysqldump string
	LockWait  int
	// SignalAfterDo and SignalAfterWindow send the helper SIGINT right after
	// the DROP returned and right after the window ended.
	SignalAfterDo, SignalAfterWindow bool
}

// TestDropDatabaseHelperProcess is the other process of the tests that
// signal or kill a drop-database run; it does nothing otherwise.
func TestDropDatabaseHelperProcess(t *testing.T) {
	raw := os.Getenv(helperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var spec helperSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	cliLayoutHost = func(config.Config) (mysql.Host, error) {
		return mysql.NewHost(mysql.Layout{Socket: spec.Socket}, nil, nil, probe.RealSystem(), nil), nil
	}
	cliLiftLock, cliBackupDir = spec.Lock, spec.Backups
	if spec.Mysqldump != "" {
		cliMysqldump = spec.Mysqldump
	}
	if spec.LockWait > 0 {
		cliLockWaitTimeout = spec.LockWait
	}
	interrupt := func() {
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(500 * time.Millisecond) // delivered before going on
	}
	if spec.SignalAfterDo {
		liftedAfterDo = interrupt
	}
	if spec.SignalAfterWindow {
		afterWindow = interrupt
	}
	logLine := func(line string) error {
		f, err := os.OpenFile(spec.Syslog, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, line)
		return err
	}
	code := RunCLIWith(spec.Args, os.Stdin, os.Stdout, os.Stderr, CLISeams{Syslog: logLine})
	os.RemoveAll(testDefaultsDir)
	os.Exit(code)
}

// process is drop-database running in a helper process.
type process struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *lockedBuffer
	err  *lockedBuffer
	done chan struct{}
}

// start runs drop-database args in a helper process.
func (r *dropRig) start(spec helperSpec, args ...string) *process {
	r.t.Helper()
	spec.Args = append(append([]string{"drop-database"}, args...), "--dir", r.dir)
	spec.Socket, spec.Lock = r.inst.Socket, r.lock
	if spec.Backups == "" {
		spec.Backups = r.backups
	}
	if spec.Syslog == "" {
		spec.Syslog = filepath.Join(r.t.TempDir(), "syslog")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		r.t.Fatal(err)
	}
	p := &process{t: r.t, out: &lockedBuffer{}, err: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(os.Args[0], "-test.run=^TestDropDatabaseHelperProcess$", "-test.count=1")
	p.cmd.Env = append(os.Environ(), helperEnv+"="+string(raw))
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.err
	if p.in, err = p.cmd.StdinPipe(); err != nil {
		r.t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() {
		p.cmd.Wait()
		close(p.done)
	}()
	r.t.Cleanup(func() {
		p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *process) waitOut(want string) {
	p.t.Helper()
	waitFor(p.t, func() bool { return strings.Contains(p.out.String(), want) }, func() string {
		return fmt.Sprintf("stdout never showed %q: stdout %q, stderr %q", want, p.out.String(), p.err.String())
	})
}

func (p *process) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

// wait waits for the process to end and returns how: its exit code, or the
// signal that ended it.
func (p *process) wait() (code int, signaled syscall.Signal) {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(3 * time.Minute):
		p.t.Fatalf("process never ended: stdout %q, stderr %q", p.out.String(), p.err.String())
	}
	ws := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return -1, ws.Signal()
	}
	return ws.ExitStatus(), 0
}

// scanForSecret fails when secret appears in any file under the roots.
func scanForSecret(t *testing.T, secret string, roots ...string) {
	t.Helper()
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err == nil && bytes.Contains(b, []byte(secret)) {
				t.Errorf("the password is in %s", path)
			}
			return nil
		})
	}
}

// recordingDump is a mysqldump that records its arguments and environment
// in log, then runs the real one with them (and the descriptors it got).
func recordingDump(t *testing.T, log string) string {
	t.Helper()
	real, err := exec.LookPath("mysqldump")
	if err != nil {
		t.Skip("mysqldump not installed")
	}
	script := filepath.Join(t.TempDir(), "mysqldump")
	body := "#!/bin/sh\n{ printf 'argv:'; printf ' [%s]' \"$@\"; echo; env; } >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestRootSessionPasswordFallback: root refused over the socket without a
// password: the command asks (C5), logs in, and the password never shows
// in its output, the system log, a program's arguments or environment, or
// any file, also when the confirmation is answered with Ctrl-C. Password
// and confirmation come through one stream.
func TestRootSessionPasswordFallback(t *testing.T) {
	r := newDropRig(t)
	const pw = `Rah4s!a #"q\ 'z`
	r.makeDB("pasien", 2)
	r.setRootPassword(pw)
	t.Cleanup(func() { r.setRootPassword("") })
	argLog := filepath.Join(t.TempDir(), "mysqldump-calls")
	setVar(t, &cliMysqldump, recordingDump(t, argLog))
	before := r.gtidExecuted()

	// One stream: the password line and the confirmation line.
	code, out, errOut := r.run(strings.NewReader(pw+"\npasien\n"), "pasien")
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !strings.HasPrefix(out, "Kata sandi root MySQL: ") {
		t.Errorf("stdout does not start with C5: %q", out)
	}
	if r.exists("pasien") || r.flags() != "1 1" || r.gtidExecuted() != before {
		t.Errorf("after the run: exists %v, flags %s, gtid_executed changed %v", r.exists("pasien"), r.flags(), r.gtidExecuted() != before)
	}
	calls, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "argv: [--defaults-file=/dev/fd/3]") {
		t.Errorf("mysqldump was not given the inherited option file first: %s", calls)
	}
	for name, s := range map[string]string{"stdout": out, "stderr": errOut, "syslog": strings.Join(r.syslogLines(), "\n"), "mysqldump arguments and environment": string(calls)} {
		if strings.Contains(s, pw) {
			t.Errorf("the password is in %s: %q", name, s)
		}
	}
	scanForSecret(t, pw, r.dir, r.backups, filepath.Dir(r.lock), filepath.Dir(argLog), testDefaultsDir)

	// Ctrl-C at the confirmation, in a process of its own.
	r.makeDB("lab", 1)
	argLog2 := filepath.Join(t.TempDir(), "mysqldump-calls")
	syslogFile := filepath.Join(t.TempDir(), "syslog")
	p := r.start(helperSpec{Mysqldump: recordingDump(t, argLog2), Syslog: syslogFile}, "lab")
	p.waitOut("Kata sandi root MySQL: ")
	p.send(pw)
	p.waitOut(c10)
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.cmd.Process.Pid))
	environ, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", p.cmd.Process.Pid))
	if bytes.Contains(cmdline, []byte(pw)) || bytes.Contains(environ, []byte(pw)) {
		t.Error("the password is in the command's arguments or environment")
	}
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code, sig := p.wait(); code == 0 && sig == 0 {
		t.Error("Ctrl-C at the confirmation ended the command with 0")
	}
	if !r.exists("lab") || r.flags() != "1 1" {
		t.Errorf("after Ctrl-C: exists %v, flags %s", r.exists("lab"), r.flags())
	}
	for name, s := range map[string]string{"stdout": p.out.String(), "stderr": p.err.String()} {
		if strings.Contains(s, pw) {
			t.Errorf("the password is in the helper's %s: %q", name, s)
		}
	}
	scanForSecret(t, pw, r.dir, r.backups, filepath.Dir(r.lock), filepath.Dir(argLog2), filepath.Dir(syslogFile))
}

// TestRootSessionOtherUser: --pengguna logs in with that account, and its
// password prompt (C5b) names it.
func TestRootSessionOtherUser(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("farmasi", 1)
	r.setup("CREATE USER 'admin2'@'localhost' IDENTIFIED BY 'sandi-admin2'",
		"GRANT ALL ON *.* TO 'admin2'@'localhost' WITH GRANT OPTION")
	s := r.interact("farmasi", "--pengguna", "admin2")
	s.waitOut("Kata sandi akun MySQL admin2: ")
	s.send("sandi-admin2")
	s.waitOut(c10)
	var n int
	if err := r.db().QueryRow("SELECT COUNT(*) FROM information_schema.processlist WHERE user = 'admin2'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("no session of admin2 while the command asks")
	}
	s.send("farmasi")
	if code := s.wait(); code != 0 {
		t.Fatalf("drop-database --pengguna admin2 = %d, stdout %q, stderr %q", code, s.out.String(), s.err.String())
	}
	if strings.Contains(s.out.String(), "Kata sandi root MySQL") {
		t.Errorf("asked for root's password: %q", s.out.String())
	}
	if r.exists("farmasi") || r.flags() != "1 1" {
		t.Errorf("after the run: exists %v, flags %s", r.exists("farmasi"), r.flags())
	}
}

// TestPromptReaderOneStream: lines and a secret come from one buffered
// reader, in order; a secret from a pipe is a plain line.
func TestPromptReaderOneStream(t *testing.T) {
	p := newPromptReader(strings.NewReader("rahasia\r\nnama db\nakhir"))
	if s, err := p.secret(); err != nil || s != "rahasia" {
		t.Errorf("secret = %q, %v", s, err)
	}
	if s, err := p.line(); err != nil || s != "nama db" {
		t.Errorf("line = %q, %v", s, err)
	}
	if s, err := p.line(); err != nil || s != "akhir" {
		t.Errorf("last line = %q, %v", s, err)
	}
	if _, err := p.line(); !errors.Is(err, io.EOF) {
		t.Errorf("after the input: %v, want EOF", err)
	}
}

// TestPrintable: control and format characters (ESC, newline, bidi
// overrides) never reach the terminal.
func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"biasa":                 "biasa",
		"a\x1b[31mb":            "a [31mb",
		"satu\ndua":             "satu dua",
		"kiri‮kanan":            "kiri kanan",
		"rumah_sakit_é":         "rumah_sakit_é",
		"nol\x00byte\xff":       "nol byte",
		"\x1b\x1bawal  akhir\t": "awal akhir",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// guardEvents records the events a driver sends.
type guardEvents struct {
	mu  sync.Mutex
	evs []proto.Event
}

func (g *guardEvents) Emit(ev proto.Event) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evs = append(g.evs, ev)
	return nil
}

// lateStarts are the quarantine start reports among the events since the
// last reset.
func (g *guardEvents) lateStarts() []proto.QuarantineStartEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []proto.QuarantineStartEvent
	for _, ev := range g.evs {
		if ev.Type != proto.EventQuarantineStart {
			continue
		}
		var s proto.QuarantineStartEvent
		if json.Unmarshal(ev.Payload, &s) == nil && s.Result == proto.QuarantineStartLate {
			out = append(out, s)
		}
	}
	return out
}

func (g *guardEvents) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evs = nil
}

// guard is the agent's quarantine guard over the rig's server, sharing its
// lift lock, after a first round that found the server locked.
func (r *dropRig) guard() (tick func(), evs *guardEvents) {
	r.t.Helper()
	evs = &guardEvents{}
	d := &mysql.Driver{Host: r.inst.Host(), PinDir: r.dir, LiftLock: r.lock,
		SeenPath: filepath.Join(r.t.TempDir(), "replicas-seen"), SalvageDir: filepath.Join(r.t.TempDir(), "salvage"),
		Events: evs}
	d.Register(commands.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunEvents(ctx)
	}()
	r.t.Cleanup(func() {
		cancel()
		<-done
	})
	tick = func() {
		r.t.Helper()
		if err := d.GuardTick(context.Background(), time.Now()); err != nil {
			r.t.Fatalf("GuardTick: %v", err)
		}
	}
	tick()
	tick()
	time.Sleep(500 * time.Millisecond)
	evs.reset()
	return tick, evs
}

// lockFree reports whether the lift lock can be taken now.
func (r *dropRig) lockFree() bool {
	release, err := mysql.AcquireLiftLock(r.lock)
	if err != nil {
		return false
	}
	release()
	return true
}

// lockState is the lift lock file's and directory's mode and owner.
func lockState(t *testing.T, path string) string {
	t.Helper()
	var parts []string
	for _, p := range []string{filepath.Dir(path), path} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		parts = append(parts, fmt.Sprintf("%s %v uid=%d nlink=%d ino=%d", p, fi.Mode(), st.Uid, st.Nlink, st.Ino))
	}
	return strings.Join(parts, "; ")
}

// TestDumpIgnoresLoginFile: a login file (.mylogin.cnf) in root's home
// that names another server and account changes neither where the backup's
// mysqldump connects nor as whom, on either login path (measured on 8.0:
// clients read it even with --no-defaults, and after --defaults-file).
func TestDumpIgnoresLoginFile(t *testing.T) {
	editor, err := exec.LookPath("mysql_config_editor")
	if err != nil {
		t.Skip("mysql_config_editor not installed")
	}
	r := newDropRig(t)
	home := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	nobody := strconv.Itoa(l.Addr().(*net.TCPAddr).Port) // nothing listens there once closed
	l.Close()
	set := exec.Command(editor, "set", "--login-path=client", "--host=127.0.0.1", "--port="+nobody, "--user=evil")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MYSQL_") && !strings.HasPrefix(kv, "HOME=") {
			set.Env = append(set.Env, kv)
		}
	}
	set.Env = append(set.Env, "HOME="+home)
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("mysql_config_editor: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".mylogin.cnf")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	r.makeDB("login_tanpa_sandi", 1)
	if code, out, errOut := r.run(strings.NewReader("login_tanpa_sandi\n"), "login_tanpa_sandi"); code != 0 || r.exists("login_tanpa_sandi") {
		t.Errorf("without a password: code %d, exists %v\nstdout %q\nstderr %q", code, r.exists("login_tanpa_sandi"), out, errOut)
	}
	r.makeDB("login_sandi", 1)
	r.setRootPassword("sandi-root")
	t.Cleanup(func() { r.setRootPassword("") })
	if code, out, errOut := r.run(strings.NewReader("sandi-root\nlogin_sandi\n"), "login_sandi"); code != 0 || r.exists("login_sandi") {
		t.Errorf("with a password: code %d, exists %v\nstdout %q\nstderr %q", code, r.exists("login_sandi"), out, errOut)
	}
}

// generalLog turns MySQL's general query log on into a file and returns a
// reader of it.
func (r *dropRig) generalLog() func() string {
	r.t.Helper()
	path := filepath.Join(r.inst.Dir, "general.log")
	r.exec("SET GLOBAL general_log_file = '"+path+"'", "SET GLOBAL general_log = ON")
	return func() string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
}

// session opens the command's root session on the rig's server.
func (r *dropRig) session() *rootSession { return r.sessionTo(io.Discard) }

// sessionTo is session with its lines (runLifted's) written to out.
func (r *dropRig) sessionTo(out io.Writer) *rootSession {
	r.t.Helper()
	s, err := openRootSession(context.Background(), r.dir, "", newPromptReader(strings.NewReader("")), out)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(s.Close)
	return s
}

func noRecheck(context.Context, *sql.Conn) error { return nil }

// lifted runs runLifted with do in the background.
func (r *dropRig) lifted(s *rootSession, do func(context.Context, *sql.Conn) error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- runLifted(context.Background(), s, r.dir, r.lock, noRecheck, do) }()
	return done
}

func waitLifted(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("runLifted never returned")
	}
	return nil
}

// TestLiftedSignalStopsWork: a signal while do's first statement waits
// ends the window, and do's next statement is never sent, although this
// do ignores the first one's error; the outcome says do ran and how it
// ended.
func TestLiftedSignalStopsWork(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("tahan", 1)
	general := r.generalLog()
	s := r.session()
	release := r.holdRead("tahan.t1")
	done := r.lifted(s, func(ctx context.Context, c *sql.Conn) error {
		_, first := c.ExecContext(ctx, "DROP DATABASE tahan")
		c.ExecContext(ctx, "CREATE DATABASE kedua_tidak_terkirim")
		return first
	})
	r.waitDropWaiting()
	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err := waitLifted(t, done)
	if time.Since(start) > 15*time.Second {
		t.Errorf("the window took %v to end after the signal", time.Since(start))
	}
	var le *liftedError
	if !errors.Is(err, errInterrupted) || !errors.As(err, &le) || !le.Ran || le.DoErr == nil {
		t.Fatalf("runLifted = %v (%+v), want an interrupted window whose do ran and failed", err, le)
	}
	release()
	log := general()
	if !strings.Contains(log, "DROP DATABASE tahan") {
		t.Fatal("the general log does not show do's first statement")
	}
	if strings.Contains(log, "kedua_tidak_terkirim") || r.exists("kedua_tidak_terkirim") {
		t.Error("do's second statement was sent after the signal")
	}
	if !r.exists("tahan") || r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("exists %v, flags %s, lock free %v", r.exists("tahan"), r.flags(), r.lockFree())
	}
}

// TestLiftedKillBetweenStatements: a signal between two statements of do
// (nothing running to kill) still keeps the next one from being sent.
func TestLiftedKillBetweenStatements(t *testing.T) {
	r := newDropRig(t)
	general := r.generalLog()
	s := r.session()
	between, goOn := make(chan struct{}), make(chan struct{})
	done := r.lifted(s, func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "SELECT 'pertama_terkirim'"); err != nil {
			return err
		}
		close(between)
		<-goOn
		_, err := c.ExecContext(ctx, "CREATE DATABASE sesudah_kill")
		return err
	})
	<-between
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	close(goOn)
	err := waitLifted(t, done)
	var le *liftedError
	if !errors.Is(err, errInterrupted) || !errors.As(err, &le) || !le.Ran || !errors.Is(le.DoErr, context.Canceled) {
		t.Fatalf("runLifted = %v (%+v), want an interrupted window whose do ended cancelled", err, le)
	}
	log := general()
	if !strings.Contains(log, "pertama_terkirim") {
		t.Fatal("the general log does not show do's first statement")
	}
	if strings.Contains(log, "sesudah_kill") || r.exists("sesudah_kill") {
		t.Error("do's statement after the signal was sent")
	}
	if r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("flags %s, lock free %v", r.flags(), r.lockFree())
	}
}

// TestDropDatabaseDroppedBeforeSignal: a DROP that completed before a
// signal ended the window is said and recorded as done: the system log
// line (with how the window ended), the "dropped" line, no advice to run
// again, a non-zero exit, 1 1 and the lock free.
func TestDropDatabaseDroppedBeforeSignal(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("habis", 1)
	setVar(t, &liftedAfterDo, func() {
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(500 * time.Millisecond) // the watcher takes the signal before do's end is known
	})
	code, out, errOut := r.run(strings.NewReader("habis\n"), "habis")
	if code == 0 || r.exists("habis") {
		t.Fatalf("drop-database = %d, exists %v\nstdout %q\nstderr %q", code, r.exists("habis"), out, errOut)
	}
	want := "Database habis sudah dihapus. Kunci baca-saja server ini sudah terpasang lagi; perintah ini tidak perlu dijalankan lagi."
	if !strings.Contains(errOut, want) || strings.Contains(out+errOut, "Jalankan perintah ini lagi") {
		t.Errorf("stdout %q\nstderr %q\nwant %q and no advice to run again", out, errOut, want)
	}
	logged := r.syslogLines()
	if len(logged) != 1 || !strings.Contains(logged[0], `name="habis"`) || !strings.Contains(logged[0], "window=interrupted") || !strings.Contains(logged[0], "outcome=dropped") {
		t.Errorf("system log %q", logged)
	}
	if r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("flags %s, lock free %v", r.flags(), r.lockFree())
	}
}

// statementWaitLine is what the operator sees while the window waits for
// the server to end its statement.
const statementWaitLine = "MySQL masih menyelesaikan atau membatalkan pernyataan perintah ini; perintah ini menunggu paling lama %d detik."

// waitStatement waits until a statement starting with prefix runs.
func (r *dropRig) waitStatement(prefix string) {
	r.t.Helper()
	waitFor(r.t, func() bool {
		var n int
		if err := r.db().QueryRow("SELECT COUNT(*) FROM information_schema.processlist WHERE info LIKE ?", prefix+"%").Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		return n > 0
	}, func() string { return "never saw " + prefix })
}

// TestLiftedWaitsForServerStatement: a statement the kill does not stop
// (here the kill is made a no-op) keeps running on the server after do's
// context was cancelled; runLifted neither relocks nor returns before the
// server thread has ended, says it is waiting, and reports the real
// outcome (the statement ended: not running).
func TestLiftedWaitsForServerStatement(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &killStatement, func(context.Context, *sql.Conn, int64) {})
	out := &lockedBuffer{}
	s := r.sessionTo(out)
	done := r.lifted(s, func(ctx context.Context, c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "SELECT SLEEP(4)")
		return err
	})
	r.waitStatement("SELECT SLEEP(4)")
	signalled := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err := waitLifted(t, done)
	if d := time.Since(signalled); d < 2500*time.Millisecond {
		t.Errorf("runLifted returned %v after the signal, before the server's statement could have ended", d)
	}
	var le *liftedError
	if !errors.As(err, &le) || !errors.Is(err, errInterrupted) || le.Running || !le.Ran {
		t.Fatalf("runLifted = %v (%+v), want interrupted, ran, not running", err, le)
	}
	if !strings.Contains(out.String(), fmt.Sprintf(statementWaitLine, 60)+"\n") || strings.Count(out.String(), "MySQL masih") != 1 {
		t.Errorf("session output %q, want the waiting line once", out.String())
	}
	if r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("flags %s, lock free %v", r.flags(), r.lockFree())
	}
}

// TestLiftedGivesUpWaitingForStatement: with the wait bounded below the
// statement's length, the relock is attempted all the same and the error
// says the work may still be running.
func TestLiftedGivesUpWaitingForStatement(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &killStatement, func(context.Context, *sql.Conn, int64) {})
	setVar(t, &cliStatementWait, time.Second)
	s := r.session()
	done := r.lifted(s, func(ctx context.Context, c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "SELECT SLEEP(5)")
		return err
	})
	r.waitStatement("SELECT SLEEP(5)")
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err := waitLifted(t, done)
	var le *liftedError
	if !errors.As(err, &le) || !errors.Is(err, errInterrupted) || !le.Running {
		t.Fatalf("runLifted = %v (%+v), want interrupted and still running", err, le)
	}
	if r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("flags %s, lock free %v", r.flags(), r.lockFree())
	}
}
