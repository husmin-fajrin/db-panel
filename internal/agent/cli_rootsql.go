package agent

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The operator commands that lift read-only on a quarantined server
// (drop-database, and the admin account commands) share what is here: one
// reader for the operator's answers, a root session to MySQL over the
// socket, the probe of the lift lock, and runLifted, the window in which
// super_read_only is off.

// Texts of the root session (spec 5.8 C5, C5b) and of its failures.
const (
	textRootPassword  = "Kata sandi root MySQL: "
	textUserPassword  = "Kata sandi akun MySQL %s: "
	textSocketFailed  = "MySQL di server ini tidak bisa dihubungi lewat socket %s (%v); tidak ada yang diubah."
	textLoginRefused  = "MySQL menolak login akun %s (%v); tidak ada yang diubah."
	textNoLayout      = "letak MySQL di server ini tidak bisa dibaca (%v); tidak ada yang diubah."
	textRelockedAgain = "Kunci baca-saja server ini tidak terpasang penuh sesudah perintah ini; perintah ini sudah memasangnya lagi."
	textNotRelocked   = "Kunci baca-saja server ini tidak bisa dipasang lagi (%v); agent memasangnya lagi dalam beberapa detik bila agent berjalan. Periksa dengan: sudo systemctl status dbpanel-agent"
)

// promptReader reads the operator's answers: one buffered reader for the
// password line and the confirmation line alike, so that lines typed (or
// piped) ahead are not lost between two prompts.
type promptReader struct {
	in io.Reader
	r  *bufio.Reader
}

func newPromptReader(in io.Reader) *promptReader {
	return &promptReader{in: in, r: bufio.NewReader(in)}
}

// line reads one line without its line ending; io.EOF when the input ended
// with nothing more to read.
func (p *promptReader) line() (string, error) {
	s, err := p.r.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// secret reads one line like line, without echo when the input is a
// terminal. A signal while echo is off puts the terminal back before the
// signal ends the process.
func (p *promptReader) secret() (string, error) {
	f, ok := p.in.(*os.File)
	if !ok {
		return p.line()
	}
	fd := int(f.Fd())
	old, err := getTermios(fd)
	if err != nil {
		return p.line() // not a terminal
	}
	quiet := *old
	quiet.Lflag &^= syscall.ECHO
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-sigs:
			setTermios(fd, old)
			signal.Stop(sigs)
			if s, ok := sig.(syscall.Signal); ok {
				syscall.Kill(os.Getpid(), s)
			}
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-done
		signal.Stop(sigs)
		setTermios(fd, old)
	}()
	if err := setTermios(fd, &quiet); err != nil {
		return "", err
	}
	return p.line()
}

func getTermios(fd int) (*syscall.Termios, error) {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return nil, e
	}
	return &t, nil
}

func setTermios(fd int, t *syscall.Termios) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(t))); e != 0 {
		return e
	}
	return nil
}

// rootSession is an operator command's login to MySQL: Conn is the one
// connection every statement of the command runs on (sql_log_bin is per
// session); DB serves only the second connection that puts the flags back
// on a signal or at the window's cap. Close ends both.
type rootSession struct {
	DB    *sql.DB
	Conn  *sql.Conn
	User  string
	Close func()

	socket   string
	password string
	out      io.Writer // where runLifted says it locked again
}

// dumpCommand is the client program bin (mysqldump) logged in as the
// session, with args after the login options. No file holds the
// credentials: without a password they are arguments (--no-defaults, so no
// option file is read at all); with one they come through an inherited
// pipe the client reads as its only option file, --defaults-file=/dev/fd/3.
// The login file (.mylogin.cnf) is kept out too: 8.0 clients read it even
// with --no-defaults and after --defaults-file, where a host, port or user
// in it wins (measured), so MYSQL_TEST_LOGIN_FILE names /dev/null, which
// reads as empty. --protocol=SOCKET makes the client fail rather than
// connect anywhere but the socket. The environment is otherwise the
// command's own without the client's MYSQL_* variables. The caller closes
// cmd.ExtraFiles once the program has started.
func (s *rootSession) dumpCommand(ctx context.Context, bin string, args ...string) (*exec.Cmd, error) {
	var login []string
	var extra []*os.File
	if s.password == "" {
		login = []string{"--no-defaults", "--protocol=SOCKET", "--socket=" + s.socket, "--user=" + s.User}
	} else {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		opts := "[client]\nuser=" + optionValue(s.User) + "\npassword=" + optionValue(s.password) + "\nsocket=" + optionValue(s.socket) + "\n"
		_, err = io.WriteString(w, opts) // far below a pipe's buffer: never blocks
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			r.Close()
			return nil, err
		}
		login, extra = []string{"--defaults-file=/dev/fd/3", "--protocol=SOCKET"}, []*os.File{r}
	}
	cmd := exec.CommandContext(ctx, bin, append(login, args...)...)
	cmd.ExtraFiles = extra
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MYSQL_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, noLoginFile)
	return cmd, nil
}

// noLoginFile points the client's login file at one that reads as empty.
const noLoginFile = "MYSQL_TEST_LOGIN_FILE=/dev/null"

// optionValue quotes v for a MySQL option file: in double quotes, with a
// backslash before a backslash or a double quote (a # inside quotes is no
// comment).
func optionValue(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

// openRootSession logs in to the MySQL of the agent in dir over its socket
// (the agent's layout) as user, root when empty: first without a password;
// when that is refused it asks for one (C5, or C5b naming the account
// given) on stdout, reads one line from in, and tries once more.
func openRootSession(ctx context.Context, dir, user string, in *promptReader, stdout io.Writer) (*rootSession, error) {
	// Without agent.yaml (corrupt or missing: an agent dbpanel installed
	// always has it) the MySQL its socket override names is not known:
	// refused before anything is opened (ruling D57).
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf(textNoLayout, err)
	}
	h, err := cliLayoutHost(cfg)
	if err != nil {
		return nil, fmt.Errorf(textNoLayout, err)
	}
	socket := h.Layout().Socket
	prompt := fmt.Sprintf(textUserPassword, user)
	if user == "" {
		user, prompt = "root", textRootPassword
	}
	s := &rootSession{User: user, socket: socket, out: stdout}
	err = s.open(ctx, "")
	if mysql.LoginRefused(err) {
		fmt.Fprint(stdout, prompt)
		pw, rerr := in.secret()
		fmt.Fprintln(stdout)
		if rerr != nil {
			return nil, fmt.Errorf(textLoginRefused, user, err)
		}
		if err = s.open(ctx, pw); mysql.LoginRefused(err) {
			return nil, fmt.Errorf(textLoginRefused, user, err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf(textSocketFailed, socket, err)
	}
	return s, nil
}

// open logs s in with password and keeps its one connection.
func (s *rootSession) open(ctx context.Context, password string) error {
	db, err := sql.Open("mysql", mysql.DSN(proto.ConnectParams{User: s.User, Password: password}, s.socket))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(3)
	conn, err := db.Conn(ctx)
	if err == nil {
		err = conn.PingContext(ctx)
		if err != nil {
			conn.Close()
		}
	}
	if err != nil {
		db.Close()
		return err
	}
	s.DB, s.Conn, s.password = db, conn, password
	s.Close = func() {
		conn.Close()
		db.Close()
	}
	return nil
}

// Seams: tests replace the session and the lift lock file (the agent's
// own, DefaultLiftLock, otherwise).
var (
	cliRootSession = openRootSession
	cliLiftLock    = mysql.DefaultLiftLock
)

// cliProbeWait is how long probeLiftLock tries a held lift lock again.
var cliProbeWait = 3 * time.Second

// probeLiftLock takes and releases the lift lock at path, trying again
// without waiting until wait has passed: nil when it is free,
// mysql.ErrLiftBusy (C3) when it stays held, *mysql.LiftLockError (C4)
// when the file fails the checks.
func probeLiftLock(path string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		release, err := mysql.AcquireLiftLock(path)
		if err == nil {
			release()
			return nil
		}
		if !errors.Is(err, mysql.ErrLiftBusy) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// What runLifted ends with besides the command's own errors.
var (
	// errStateChanged (C11): the pin, the flags or what the command would
	// change differ from what the operator saw; nothing was sent.
	errStateChanged = errors.New("Keadaan server ini berubah selama Anda membaca; tidak ada yang diubah. Jalankan perintah ini lagi.")
	// errLockWait wraps MySQL's lock wait timeout (1205) from do; the
	// command words it (C12 for drop-database).
	errLockWait = errors.New("lock wait timeout")
	// errInterrupted (C13): a signal ended the window.
	errInterrupted = errors.New("Perintah dihentikan (sinyal); kunci baca-saja server ini sudah terpasang lagi. Jalankan perintah ini lagi bila pekerjaannya belum selesai.")
	// errWindowCap (C14): the window stayed open for cliWindowCap.
	errWindowCap = errors.New("Perintah ini sudah membuka kunci baca-saja selama 10 menit dan dihentikan; kunci baca-saja sudah terpasang lagi. Jalankan perintah ini lagi.")
)

// relockError is a window whose flags could not be put back.
type relockError struct{ err error }

func (e *relockError) Error() string { return fmt.Sprintf(textNotRelocked, e.err) }
func (e *relockError) Unwrap() error { return e.err }

// Bounds of the window; tests shorten them.
var (
	// cliLockWaitTimeout is the session lock_wait_timeout (seconds) of the
	// command's statements.
	cliLockWaitTimeout = 60
	// cliWindowCap is how long the window may stay open.
	cliWindowCap = 10 * time.Minute
	// cliStatementWait bounds how long an abnormal end of the window waits
	// for the server to end the statement of do's connection.
	cliStatementWait = 60 * time.Second
	// cliRelockWait is the lock_wait_timeout (seconds) of the statements
	// that put the flags back on a connection of their own.
	cliRelockWait = 10
	// killStatement sends KILL QUERY for connection id on conn; tests make
	// it a no-op to stand for a statement the kill does not stop.
	killStatement = func(ctx context.Context, conn *sql.Conn, id int64) {
		conn.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id)) // "unknown thread" once it is gone
	}
)

// statementNotice is how long the wait for the server's statement goes on
// before the operator is told.
const statementNotice = 2 * time.Second

// textStatementWait is that line.
const textStatementWait = "MySQL masih menyelesaikan atau membatalkan pernyataan perintah ini; perintah ini menunggu paling lama %d detik."

const (
	// relockTimeout bounds putting the flags back, whatever the command's
	// context.
	relockTimeout = 30 * time.Second
	// errLockWaitTimeout is ER_LOCK_WAIT_TIMEOUT.
	errLockWaitTimeout = 1205
)

// runLifted is the window the operator commands open on a quarantined
// server (spec 5.4): under the lift lock at lockPath, and after recheck,
// it turns super_read_only off on s.Conn, runs do there, and always puts it
// back. It sends SET GLOBAL super_read_only = OFF and = ON only, never
// read_only. SIGINT, SIGTERM and SIGHUP are caught from before the flag
// goes off until the final check.
//
// A signal, or the window open for cliWindowCap, ends the window: the
// watcher cancels the context do runs with, so that do sends no further
// statement (database/sql sends none on a cancelled context, so a do that
// only runs statements on c with that context needs nothing more; any
// other do must stop on it too). Cancelling does not end a statement in
// flight: go-sql-driver closes the connection at once and do returns, but
// the server thread goes on with the statement (measured). So the watcher
// then decides from the server: from a second connection of s.DB it sends
// KILL QUERY for s.Conn's connection id and reads that thread, again every
// killRepeat, until the thread is idle or gone, for at most
// cliStatementWait (it tells the operator on s.out once the wait passes
// statementNotice). Only then, or when that bound runs out, does it put
// the flag back, and steps 5 and 6 run, on a context that is not
// cancelled. A signal that arrives after do has returned and the window
// was not ended by one (during steps 5 and 6) changes nothing: do's
// outcome stands.
//
// What the caller learns:
//   - nil: do ran and returned nil; the flags are back at 1 1.
//   - mysql.ErrLiftBusy, *mysql.LiftLockError, errStateChanged, or an
//     error reading the server before the window: do never ran and nothing
//     changed.
//   - an error wrapping errLockWait: do returned MySQL's lock wait timeout
//     (1205); the flags are back.
//   - *liftedError: the window ended abnormally. errors.Is answers
//     errInterrupted or errWindowCap, errors.As a *relockError when the
//     flags could not be put back (that wins over the other two). Ran says
//     whether do was called and DoErr what do returned (context.Canceled
//     once the watcher cancelled it); do may have done its work all the
//     same, so the caller checks the server. Running says the server
//     thread was still running do's statement when cliStatementWait ran
//     out: its work may still happen after runLifted returns (and may have
//     kept the flag from going back on).
//   - any other error: do's own (the flags are back), or the error that
//     kept the flag from going off (do never ran).
func runLifted(ctx context.Context, s *rootSession, dir, lockPath string, recheck, do func(ctx context.Context, c *sql.Conn) error) error {
	// 1. The lift lock, without waiting.
	release, err := mysql.AcquireLiftLock(lockPath)
	if err != nil {
		return err
	}
	defer release()
	c := s.Conn

	// 2. What the operator saw still holds.
	if p, err := mysql.LoadPins(dir); err != nil {
		return errStateChanged
	} else if role, err := p.RoleErr(); err != nil || role != "quarantined" {
		return errStateChanged
	}
	ro, sro, err := readFlags(ctx, c)
	if err != nil {
		return err
	}
	if !ro || !sro {
		return errStateChanged
	}
	if err := recheck(ctx, c); err != nil {
		return err
	}

	// 3. Signals caught; the session's settings.
	sigs := make(chan os.Signal, 3)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	var id int64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		return err
	}
	for _, q := range []string{"SET SESSION sql_log_bin = 0", fmt.Sprintf("SET SESSION lock_wait_timeout = %d", cliLockWaitTimeout)} {
		if _, err := c.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	if len(sigs) > 0 {
		return errInterrupted // nothing lifted yet
	}

	// 4. The window, watched; do's context is the watcher's to cancel.
	dctx, cancelDo := context.WithCancel(ctx)
	defer cancelDo()
	returned, fired := make(chan struct{}), make(chan windowEnd, 1)
	go watchWindow(s.DB, s.out, id, sigs, cancelDo, returned, fired)
	ran := false
	_, err = c.ExecContext(dctx, "SET GLOBAL super_read_only = OFF")
	if err == nil {
		ran = true
		err = do(dctx, c)
	}
	liftedAfterDo()
	close(returned)
	end := <-fired
	why := end.why

	// 5. Always put back what step 2 read (on), whatever happened.
	putBack(ctx, s, c)

	// 6. The final check; locked again here when it is still not 1 1.
	if rerr := finalCheck(ctx, s, c); rerr != nil {
		why = rerr
	}
	switch {
	case why != nil:
		return &liftedError{Why: why, Ran: ran, DoErr: err, Running: end.running}
	case isLockWait(err):
		return fmt.Errorf("%w: %w", errLockWait, err)
	}
	return err
}

// liftedError is a window that ended abnormally (runLifted): Why is
// errInterrupted, errWindowCap or a *relockError, and its text is the
// error's; Ran is whether do was called, DoErr what it returned, Running
// whether the server was still running do's statement when the wait for it
// ran out.
type liftedError struct {
	Why     error
	Ran     bool
	DoErr   error
	Running bool
}

func (e *liftedError) Error() string { return e.Why.Error() }
func (e *liftedError) Unwrap() error { return e.Why }

// liftedAfterDo runs between do's return and the watcher learning of it;
// tests use it to land a signal there.
var liftedAfterDo = func() {}

// killRepeat is how often the watcher sends KILL QUERY again and reads the
// thread while it waits for the server to end do's statement.
const killRepeat = 200 * time.Millisecond

// windowEnd is how the watcher saw the window end: why (nil when do
// returned first) and whether the server still ran do's statement when the
// wait for it ran out.
type windowEnd struct {
	why     error
	running bool
}

// watchWindow waits for a signal or the window's cap until returned
// closes (do has returned). On either it cancels do's context, then from a
// connection of its own kills the statement of connection id and reads its
// thread, again every killRepeat, until the server shows it idle or gone or
// cliStatementWait has passed, telling the operator on out once the wait
// passes statementNotice. It waits for do to return, puts super_read_only
// back, and sends how the window ended on fired.
func watchWindow(db *sql.DB, out io.Writer, id int64, sigs <-chan os.Signal, cancelDo func(), returned <-chan struct{}, fired chan<- windowEnd) {
	timer := time.NewTimer(cliWindowCap)
	defer timer.Stop()
	var end windowEnd
	select {
	case <-returned:
		fired <- end
		return
	case <-sigs:
		end.why = errInterrupted
	case <-timer.C:
		end.why = errWindowCap
	}
	cancelDo()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cliStatementWait+relockTimeout)
	defer cancel()
	var conn *sql.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	told := false
	for {
		if conn == nil {
			if c, err := db.Conn(ctx); err == nil {
				conn = c
				conn.ExecContext(ctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", cliRelockWait))
			}
		}
		if conn != nil {
			killStatement(ctx, conn, id)
			if ended, err := threadEnded(ctx, conn, id); err == nil && ended {
				break
			} else if err != nil {
				conn.Close() // read again on a fresh connection
				conn = nil
			}
		}
		if time.Since(start) >= cliStatementWait {
			end.running = true
			break
		}
		if !told && time.Since(start) >= statementNotice {
			fmt.Fprintf(out, textStatementWait+"\n", int(cliStatementWait/time.Second))
			told = true
		}
		time.Sleep(killRepeat)
	}
	<-returned // step 5 waits for do in any case; it returned on the cancel
	if conn != nil {
		rctx, rcancel := context.WithTimeout(context.Background(), relockTimeout)
		conn.ExecContext(rctx, "SET GLOBAL super_read_only = ON") // the final check reads the outcome
		rcancel()
	}
	fired <- end
}

// threadEnded reports whether the server thread of connection id runs no
// statement: it is gone, or idle (Sleep).
func threadEnded(ctx context.Context, conn *sql.Conn, id int64) (bool, error) {
	var command string
	err := conn.QueryRowContext(ctx, "SELECT COMMAND FROM information_schema.PROCESSLIST WHERE ID = ?", id).Scan(&command)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return err == nil && command == "Sleep", err
}

// putBack sends SET GLOBAL super_read_only = ON on c, or on a connection of
// its own when c fails, bounded by relockTimeout whatever ctx.
func putBack(ctx context.Context, s *rootSession, c *sql.Conn) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	const q = "SET GLOBAL super_read_only = ON"
	if _, err := c.ExecContext(rctx, q); err == nil {
		return nil
	}
	conn, err := s.DB.Conn(rctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(rctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", cliRelockWait)); err != nil {
		return err
	}
	_, err = conn.ExecContext(rctx, q)
	return err
}

// finalCheck reads both flags; when they are not 1 1 it puts
// super_read_only back once more (which turns read_only on too) and says
// so on s.out, or returns a *relockError when they stay off.
func finalCheck(ctx context.Context, s *rootSession, c *sql.Conn) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	ro, sro, err := readFlags(rctx, c)
	if err != nil {
		ro, sro, err = readFlagsFresh(rctx, s.DB)
	}
	if err == nil && ro && sro {
		return nil
	}
	if perr := putBack(rctx, s, c); perr != nil {
		return &relockError{perr}
	}
	if ro, sro, err = readFlagsFresh(rctx, s.DB); err != nil {
		return &relockError{err}
	}
	if !ro || !sro {
		return &relockError{fmt.Errorf("read_only=%v, super_read_only=%v", ro, sro)}
	}
	fmt.Fprintln(s.out, textRelockedAgain)
	return nil
}

// readFlags reads @@global.read_only and @@global.super_read_only on c.
func readFlags(ctx context.Context, c *sql.Conn) (ro, sro bool, err error) {
	err = c.QueryRowContext(ctx, "SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro)
	return ro, sro, err
}

// readFlagsFresh is readFlags on a connection of db's own.
func readFlagsFresh(ctx context.Context, db *sql.DB) (ro, sro bool, err error) {
	err = db.QueryRowContext(ctx, "SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro)
	return ro, sro, err
}

// isLockWait reports whether err is MySQL's lock wait timeout (1205).
func isLockWait(err error) bool {
	var me *drv.MySQLError
	return errors.As(err, &me) && me.Number == errLockWaitTimeout
}

// printable is a name from MySQL as the terminal may show it: invalid
// UTF-8 dropped, and control and format characters (newlines, ESC, bidi
// overrides) and runs of white space each one space, trimmed (the rule of
// the panel's AgentText, without its length cap). The system log quotes
// names with strconv.Quote instead.
func printable(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToValidUTF8(s, "") {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			gap = b.Len() > 0
			continue
		}
		if gap {
			b.WriteByte(' ')
			gap = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quoteIdent quotes a MySQL identifier: in backticks, a backtick doubled.
func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
