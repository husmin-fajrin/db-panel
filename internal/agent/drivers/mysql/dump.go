package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	// dumpProgressInterval is how often the bytes loaded are reported.
	dumpProgressInterval = time.Second
	// restoreTimeout bounds putting read-only back after the load.
	restoreTimeout = 30 * time.Second
)

// definerErrRe finds, in the mysql client's error, a load refused over a
// DEFINER: 1227 (the admin account may not name another user as definer)
// or 1449 (the definer does not exist).
var definerErrRe = regexp.MustCompile(`ERROR (1227|1449) `)

// errLoadEnded stops mysqldump's output once mysql no longer reads it.
var errLoadEnded = errors.New("mysql load ended")

// DumpFrom copies the master's databases into this server: mysqldump on the
// master, piped straight into mysql here with sql_log_bin=0. creds is the
// stored admin account the local side logs in with (root over the socket
// when none is stored, as Host.Open does).
//
// Before the load, a configured but stopped replica is stopped (a running
// one is refused), read-only is lifted until the load ends, DropExisting
// drops the local copies of the databases, and the binary logs and GTID
// history are reset so the dump's SET @@GLOBAL.GTID_PURGED applies; the
// result's GTIDExecuted is then the master's gtid_executed at the dump.
// See dumpGTIDMode for the one case that keeps the local GTID history.
func DumpFrom(j *jobs.Job, h Host, creds CredentialStore, p proto.SnapshotParams) (proto.SnapshotResult, error) {
	ctx := j.Context()
	start := time.Now()
	res := proto.SnapshotResult{Method: methodDump}
	dumpBin, err := findClientTool(h.Layout().MysqldPath, "mysqldump")
	if err != nil {
		return res, err
	}
	loadBin, err := findClientTool(h.Layout().MysqldPath, "mysql")
	if err != nil {
		return res, err
	}
	local, err := localAccount(creds)
	if err != nil {
		return res, err
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()

	rs, err := refuseRunningReplica(ctx, db)
	if err != nil {
		return res, err
	}
	if rs != nil {
		if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
			return res, fmt.Errorf("STOP REPLICA: %w", err)
		}
	}
	executed, err := localGTIDExecuted(ctx, db)
	if err != nil {
		return res, err
	}
	gtidMode := dumpGTIDMode(executed, rs != nil, p)

	restore, err := liftReadOnly(ctx, j, db)
	if err != nil {
		return res, err
	}
	defer restore()
	if p.DropExisting {
		if err := dropDatabases(ctx, db, p.Databases); err != nil {
			return res, err
		}
	}
	if gtidMode == "ON" {
		if err := resetBinaryLogs(ctx, db); err != nil {
			return res, err
		}
	}

	srcFile, err := writeDefaultsFile(h, proto.ConnectParams{User: p.User, Password: p.Password, Host: p.Host, Port: p.Port}, "")
	if err != nil {
		return res, err
	}
	defer os.Remove(srcFile)
	localFile, err := writeDefaultsFile(h, local, h.Layout().Socket)
	if err != nil {
		return res, err
	}
	defer os.Remove(localFile)

	j.Logf("info", "dumping %s from %s:%d as %s (gtid purged %s)", databaseList(p.Databases), p.Host, p.Port, p.User, gtidMode)
	j.Progress(0, "dump dimulai")
	if res.Bytes, err = pipeDump(j, h, dumpBin, dumpArgs(srcFile, p.Databases, gtidMode), loadBin, loadArgs(localFile), p.EstimateBytes); err != nil {
		return res, err
	}
	if res.GTIDExecuted, err = localGTIDExecuted(ctx, db); err != nil {
		return res, err
	}
	res.TookMs = time.Since(start).Milliseconds()
	j.Logf("info", "dump loaded: %d bytes, gtid_executed %s", res.Bytes, res.GTIDExecuted)
	return res, nil
}

// dumpGTIDMode is mysqldump's --set-gtid-purged for a load into a server
// whose gtid_executed is executed and which has a replication source
// configured or not.
//
// ON writes SET @@GLOBAL.GTID_PURGED, which needs an empty gtid_executed:
// the caller resets the binary logs first. That is right for a first
// snapshot (whatever the server ran by itself is dropped) and when every
// database or a replaced database is loaded. A replica that already
// replicates and takes one more database (DropExisting false) must keep
// its history instead: COMMENTED leaves the dump's GTID set as a comment,
// nothing is reset, and the result's GTIDExecuted is the replica's own.
func dumpGTIDMode(executed string, configuredReplica bool, p proto.SnapshotParams) string {
	if executed != "" && configuredReplica && !p.DropExisting && len(p.Databases) > 0 {
		return "COMMENTED"
	}
	return "ON"
}

// dumpArgs are mysqldump's arguments: every database (and the mysql schema,
// as CLONE copies it) when databases is empty, else those databases.
// --defaults-extra-file must come first.
func dumpArgs(defaultsFile string, databases []string, gtidPurged string) []string {
	args := []string{
		"--defaults-extra-file=" + defaultsFile,
		"--single-transaction",
		"--set-gtid-purged=" + gtidPurged,
		"--triggers", "--routines", "--events",
		"--skip-lock-tables",
		"--column-statistics=0",
		"--no-tablespaces",
	}
	if len(databases) == 0 {
		return append(args, "--all-databases")
	}
	return append(append(args, "--databases"), databases...)
}

// loadArgs are mysql's arguments: what it loads is not binary-logged here.
func loadArgs(defaultsFile string) []string {
	return []string{"--defaults-extra-file=" + defaultsFile, "--init-command=SET sql_log_bin=0"}
}

// findClientTool finds a MySQL client program next to mysqld, else on PATH.
func findClientTool(mysqldPath, name string) (string, error) {
	if mysqldPath != "" {
		p := filepath.Join(filepath.Dir(mysqldPath), name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s tidak ditemukan; pasang paket mysql-client", name)
}

// localAccount is the account the local mysql client logs in with: the
// stored admin account, else root without a password over the socket.
func localAccount(creds CredentialStore) (proto.ConnectParams, error) {
	if creds != nil {
		p, ok, err := creds.Load()
		if err != nil {
			return p, fmt.Errorf("baca kredensial MySQL: %w", err)
		}
		if ok {
			return p, nil
		}
	}
	return proto.ConnectParams{User: "root"}, nil
}

// clientDefaults renders a [client] option file for p, choosing socket or
// TCP the way DSN does. Values are double-quoted with \ and " escaped, so
// the password may hold #, quotes and spaces; control characters, which the
// option-file syntax cannot carry, are refused.
func clientDefaults(p proto.ConnectParams, socket string) (string, error) {
	var b strings.Builder
	b.WriteString("[client]\n")
	line := func(name, value string) error {
		if strings.ContainsFunc(value, unicode.IsControl) {
			return fmt.Errorf("%s MySQL tidak boleh berisi karakter kontrol", name)
		}
		b.WriteString(name + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + "\"\n")
		return nil
	}
	if err := line("user", p.User); err != nil {
		return "", err
	}
	if p.Password != "" {
		if err := line("password", p.Password); err != nil {
			return "", err
		}
	}
	if p.Port > 0 || (p.Host != "" && p.Host != "localhost") {
		host, port := p.Host, p.Port
		if host == "" || host == "localhost" {
			host = "127.0.0.1"
		}
		if port == 0 {
			port = 3306
		}
		if err := line("host", host); err != nil {
			return "", err
		}
		// Without TLS, caching_sha2_password needs the server's public key.
		b.WriteString("port=" + strconv.Itoa(port) + "\nget-server-public-key\n")
		return b.String(), nil
	}
	if err := line("socket", socket); err != nil {
		return "", err
	}
	return b.String(), nil
}

// writeDefaultsFile writes clientDefaults(p, socket) to a new 0600 file and
// returns its path; the caller removes it.
func writeDefaultsFile(h Host, p proto.ConnectParams, socket string) (string, error) {
	content, err := clientDefaults(p, socket)
	if err != nil {
		return "", err
	}
	f, err := h.TempFile("dbpanel-*.cnf", 0o600)
	if err != nil {
		return "", fmt.Errorf("buat file kredensial sementara: %w", err)
	}
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("tulis file kredensial sementara: %w", werr)
	}
	return f.Name(), nil
}

// liftReadOnly turns super_read_only and read_only off for the load (the
// admin account needs neither SUPER nor CONNECTION_ADMIN) and returns the
// function that turns back on what was on. SET GLOBAL, not PERSIST: a
// restart restores the configured values whatever happens here.
func liftReadOnly(ctx context.Context, j *jobs.Job, db *sql.DB) (restore func(), err error) {
	var ro, sro bool
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only").Scan(&ro, &sro); err != nil {
		return nil, fmt.Errorf("baca read_only: %w", err)
	}
	restore = func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		var stmts []string
		if ro {
			stmts = append(stmts, "SET GLOBAL read_only = ON")
		}
		if sro {
			stmts = append(stmts, "SET GLOBAL super_read_only = ON")
		}
		for _, q := range stmts {
			if _, err := db.ExecContext(rctx, q); err != nil {
				j.Logf("error", "%s failed: %v", q, err)
			}
		}
	}
	for _, q := range []string{"SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			restore()
			return nil, fmt.Errorf("%s: %w", q, err)
		}
	}
	if ro || sro {
		j.Logf("info", "read_only lifted for the load")
	}
	return restore, nil
}

// dropDatabases drops the local copies of databases, without binary
// logging. The names are validated.
func dropDatabases(ctx context.Context, db *sql.DB, databases []string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET SESSION sql_log_bin = 0"); err != nil {
		return fmt.Errorf("SET sql_log_bin: %w", err)
	}
	for _, name := range databases {
		if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			return fmt.Errorf("hapus database %s: %w", name, err)
		}
	}
	return nil
}

// resetBinaryLogs clears the binary logs and GTID history with the
// statement this server's version knows.
func resetBinaryLogs(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT @@version").Scan(&version); err != nil {
		return fmt.Errorf("baca versi MySQL: %w", err)
	}
	d, err := ParseVersion(version)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, d.ResetBinaryLogs()); err != nil {
		return fmt.Errorf("%s: %w", d.ResetBinaryLogs(), err)
	}
	return nil
}

// pipeDump runs mysqldump into mysql through a pipe, reporting the bytes
// through it against estimate, and returns that byte count. Cancelling the
// job kills both. The error is the side that failed first: mysql stopping
// early ends mysqldump, which is then not the cause.
func pipeDump(j *jobs.Job, h Host, dumpBin string, dumpArgs []string, loadBin string, loadArgs []string, estimate uint64) (uint64, error) {
	ctx, cancel := context.WithCancel(j.Context())
	defer cancel()
	pr, pw := io.Pipe()
	var n atomic.Uint64
	var loadEnded atomic.Bool
	type outcome struct {
		err   error
		first bool
	}
	dumped := make(chan outcome, 1)
	go func() {
		err := h.Exec(ctx, nil, &countingWriter{w: pw, n: &n}, dumpBin, dumpArgs...)
		o := outcome{err: err, first: err != nil && !loadEnded.Load()}
		pw.CloseWithError(err) // nil: mysql reads EOF
		dumped <- o
	}()
	stop := reportDumpProgress(j, &n, estimate)
	loadErr := h.Exec(ctx, pr, nil, loadBin, loadArgs...)
	loadEnded.Store(true)
	if loadErr != nil {
		cancel()
	}
	pr.CloseWithError(errLoadEnded)
	d := <-dumped
	stop()

	switch {
	case j.Context().Err() != nil:
		return n.Load(), j.Context().Err()
	case d.first:
		return n.Load(), fmt.Errorf("mysqldump dari master gagal: %v", d.err)
	case loadErr != nil:
		return n.Load(), loadFailure(loadErr)
	}
	return n.Load(), nil
}

// loadFailure words a failed load, explaining DEFINER refusals.
func loadFailure(err error) error {
	if definerErrRe.MatchString(err.Error()) {
		return fmt.Errorf("dump berisi view, routine, trigger atau event dengan DEFINER akun lain yang tidak bisa dipasang "+
			"akun admin di server ini; buat akun DEFINER itu di replika bila belum ada, atau beri akun admin hak "+
			"SET_ANY_DEFINER (MySQL 8.2 ke atas) atau SET_USER_ID (MySQL 8.0): %v", err)
	}
	return fmt.Errorf("memuat dump gagal: %v", err)
}

// reportDumpProgress reports n against estimate every dumpProgressInterval
// until the returned function is called.
func reportDumpProgress(j *jobs.Job, n *atomic.Uint64, estimate uint64) (stop func()) {
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(dumpProgressInterval)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				msg := "memuat dump: " + megabytes(n.Load())
				if estimate > 0 {
					msg += " dari perkiraan " + megabytes(estimate)
				}
				j.Progress(percentOf(n.Load(), estimate), msg)
			}
		}
	}()
	return func() { close(quit); wg.Wait() }
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(uint64(n))
	return n, err
}

// databaseList names databases for a log line.
func databaseList(databases []string) string {
	if len(databases) == 0 {
		return "all databases"
	}
	return strings.Join(databases, ", ")
}
