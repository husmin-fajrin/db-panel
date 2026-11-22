// Package testmysql starts throwaway mysqld instances for tests. It is a
// regular package so tests in any package can use it; production code must
// not import it.
package testmysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Environment variables that adjust the harness.
const (
	// EnvDir is the base directory for instances (default /var/tmp). Every
	// parent of it must be traversable by EnvUser.
	EnvDir = "DBPANEL_TEST_MYSQL_DIR"
	// EnvUser is the account mysqld runs as when the tests run as root
	// (mysqld refuses to run as root); default "nobody".
	EnvUser = "DBPANEL_TEST_MYSQL_USER"
)

const (
	startTimeout = 2 * time.Minute
	readyTimeout = time.Minute
	stopTimeout  = time.Minute
)

// Options configure an instance.
type Options struct {
	// ServerID is server_id; 0 picks a distinct one per instance.
	ServerID uint32
	// ExtraArgs are mysqld options such as "--gtid-mode=OFF" or
	// "--skip-log-bin". They are written to my.cnf after the baseline (so
	// they override it) and before the dbpanel block (which overrides them
	// on the next restart), not passed on the command line.
	ExtraArgs []string
}

// Instance is a running mysqld with its own directory, port and socket.
type Instance struct {
	Dir        string
	DataDir    string
	ConfigFile string // <Dir>/my.cnf: the instance's only option file
	Socket     string
	PIDFile    string
	ErrorLog   string
	Port       int
	ServerID   uint32

	mysqld   string
	uid, gid int // owner of Dir; -1 when not running as root
	svc      *supervisor
}

var seq atomic.Int64

// Start initialises and starts a mysqld instance with binary logging and GTID
// on, bound to 127.0.0.1, and stops and removes it when the test ends. It
// skips the test when mysqld is not installed.
func Start(t testing.TB, o Options) *Instance {
	t.Helper()
	mysqld, err := mysql.FindMysqld(probe.RealSystem())
	if err != nil {
		t.Skip("mysqld not installed: " + err.Error())
	}

	n := seq.Add(1)
	i := &Instance{mysqld: mysqld, uid: -1, gid: -1, ServerID: o.ServerID}
	if i.ServerID == 0 {
		i.ServerID = uint32(1000 + n)
	}
	runAs := ""
	if os.Geteuid() == 0 {
		runAs = os.Getenv(EnvUser)
		if runAs == "" {
			runAs = "nobody"
		}
		u, err := user.Lookup(runAs)
		if err != nil {
			t.Fatalf("testmysql: user %q: %v", runAs, err)
		}
		i.uid, _ = strconv.Atoi(u.Uid)
		i.gid, _ = strconv.Atoi(u.Gid)
	}

	i.Dir = makeDir(t, n)
	i.svc = &supervisor{inst: i}
	t.Cleanup(func() { i.teardown(t, stopTimeout) })

	i.DataDir = filepath.Join(i.Dir, "data")
	i.ConfigFile = filepath.Join(i.Dir, "my.cnf")
	i.Socket = filepath.Join(i.Dir, "mysql.sock")
	i.PIDFile = filepath.Join(i.Dir, "mysqld.pid")
	i.ErrorLog = filepath.Join(i.Dir, "error.log")
	files := filepath.Join(i.Dir, "files")
	if err := os.Mkdir(files, 0o755); err != nil {
		t.Fatal(err)
	}
	if i.uid >= 0 {
		for _, p := range []string{i.Dir, files} {
			if err := os.Chown(p, i.uid, i.gid); err != nil {
				t.Fatal(err)
			}
		}
	}
	if i.Port, err = freePort(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	initErr := filepath.Join(i.Dir, "init.err")
	args := []string{"--no-defaults", "--initialize-insecure", "--datadir=" + i.DataDir, "--log-error=" + initErr}
	if runAs != "" {
		args = append(args, "--user="+runAs)
	}
	cmd := exec.CommandContext(ctx, mysqld, args...)
	cmd.Dir = i.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		log, _ := os.ReadFile(initErr)
		t.Fatalf("testmysql: initialize: %v\n%s\n%s", err, out, log)
	}

	cnf := []string{
		"[mysqld]",
		"datadir=" + i.DataDir,
		"port=" + strconv.Itoa(i.Port),
		"bind-address=127.0.0.1",
		"socket=" + i.Socket,
		"mysqlx=OFF",
		"server-id=" + strconv.FormatUint(uint64(i.ServerID), 10),
		"log-bin=binlog",
		"gtid-mode=ON",
		"enforce-gtid-consistency=ON",
		"secure-file-priv=" + files,
		"log-error=" + i.ErrorLog,
		"pid-file=" + i.PIDFile,
	}
	if runAs != "" {
		cnf = append(cnf, "user="+runAs)
	}
	for _, a := range o.ExtraArgs {
		cnf = append(cnf, strings.TrimPrefix(a, "--"))
	}
	if err := os.WriteFile(i.ConfigFile, []byte(strings.Join(cnf, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := i.svc.Start(ctx); err != nil {
		t.Fatalf("testmysql: %v", err)
	}
	if err := i.svc.WaitReady(ctx, readyTimeout); err != nil {
		t.Fatalf("testmysql: %v\n%s", err, i.logTail())
	}
	return i
}

// teardown stops the server, waiting at most timeout for a clean
// shutdown, and removes its directory. What the server was running is
// read first (after SIGTERM it takes no new connection); when the stop
// fails, that and the end of the error log are logged before the
// directory, error log included, goes.
func (i *Instance) teardown(t testing.TB, timeout time.Duration) {
	processes, clone := i.serverState()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := i.svc.Stop(ctx); err != nil {
		t.Errorf("testmysql: stop: %v", err)
		t.Logf("testmysql: instance %s before the stop\ninformation_schema.processlist:\n%s\n"+
			"performance_schema.clone_status:\n%s\nerror log (end):\n%s", i.Dir, processes, clone, i.logTail())
	}
	if err := os.RemoveAll(i.Dir); err != nil {
		t.Errorf("testmysql: remove %s: %v", i.Dir, err)
	}
}

// stateWait bounds serverState.
const stateWait = 2 * time.Second

// serverState reads, as root over the socket within stateWait, the
// server's threads (information_schema.processlist) and its clone state
// (performance_schema.clone_status), one row per line. A read that fails
// says so in its text; nothing here fails the test.
func (i *Instance) serverState() (processes, clone string) {
	ctx, cancel := context.WithTimeout(context.Background(), stateWait)
	defer cancel()
	db, err := sql.Open("mysql", i.DSN())
	if err != nil {
		return "(" + err.Error() + ")", "(" + err.Error() + ")"
	}
	defer db.Close()
	return queryText(ctx, db, "SELECT ID, USER, COMMAND, TIME, STATE, LEFT(INFO, 200) AS INFO FROM information_schema.processlist"),
		queryText(ctx, db, "SELECT * FROM performance_schema.clone_status")
}

// queryText is the rows of q as "column=value" lines, or the error in
// parentheses.
func queryText(ctx context.Context, db *sql.DB, q string) string {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "(" + err.Error() + ")"
	}
	var b strings.Builder
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for k := range vals {
		ptrs[k] = &vals[k]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return b.String() + "(" + err.Error() + ")"
		}
		for k, c := range cols {
			if k > 0 {
				b.WriteByte(' ')
			}
			v := "NULL"
			if vals[k].Valid {
				v = strconv.Quote(vals[k].String)
			}
			b.WriteString(c + "=" + v)
		}
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return b.String() + "(" + err.Error() + ")"
	}
	if b.Len() == 0 {
		return "(no rows)"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// makeDir creates a directory every parent of which the unprivileged mysqld
// user can traverse (t.TempDir may live under a 0700 directory).
func makeDir(t testing.TB, n int64) string {
	base := os.Getenv(EnvDir)
	if base == "" {
		base = "/var/tmp"
	}
	for k := 0; ; k++ {
		dir := filepath.Join(base, fmt.Sprintf("dbpanel-test-%d-%d", os.Getpid(), n))
		if k > 0 {
			dir += "-" + strconv.Itoa(k)
		}
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			// Mkdir is subject to the umask.
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		}
		if !os.IsExist(err) {
			t.Fatalf("testmysql: %v", err)
		}
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Restart stops and starts the server and waits until it accepts connections.
func (i *Instance) Restart(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err := i.svc.Restart(ctx); err != nil {
		t.Fatalf("testmysql: restart: %v", err)
	}
	if err := i.svc.WaitReady(ctx, readyTimeout); err != nil {
		t.Fatalf("testmysql: %v\n%s", err, i.logTail())
	}
}

// DSN is a go-sql-driver DSN for root (no password) over the socket.
func (i *Instance) DSN() string {
	return mysql.DSN(proto.ConnectParams{User: "root"}, i.Socket)
}

// logTail returns the end of the error log for failure messages.
func (i *Instance) logTail() string {
	b, _ := os.ReadFile(i.ErrorLog)
	const max = 4096
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}

// Stop shuts the server down (SIGTERM, a clean shutdown) and waits for it
// to exit; Restart or the end of the test starts or removes it.
func (i *Instance) Stop(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := i.svc.Stop(ctx); err != nil {
		t.Fatalf("testmysql: stop: %v", err)
	}
}
