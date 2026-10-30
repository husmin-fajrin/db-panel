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
// master, piped straight into mysql here with sql_log_bin=0. The local side
// logs in with the stored admin account (creds), the one Host.Open uses; a
// server without one is refused.
//
// Nothing local changes until both sides are proven to work: both option
// files are written, the local login is tried and a schema-only dry run of
// the same dump is read from the master (login, privileges, database
// names). Then a configured but stopped replica is stopped (a running one
// is refused), read-only is lifted until the load ends, DropExisting drops
// the local copies of the databases, and, in ON mode, the binary logs and
// GTID history are reset so the dump's SET @@GLOBAL.GTID_PURGED applies.
//
// The real run takes a brief FLUSH TABLES WITH READ LOCK on the master
// until its consistent snapshot opens (mysqldump does so to read the GTID
// set); the trial run does not.
//
// The result's GTIDExecuted is the master's GTID set at the dump: in ON
// mode this server's gtid_executed after the load (the purged set); in
// COMMENTED mode (see dumpGTIDMode) the set in the dump's commented header,
// as this server keeps its own history.
func DumpFrom(j *jobs.Job, h Host, creds CredentialStore, p proto.SnapshotParams) (proto.SnapshotResult, error) {
	ctx := j.Context()
	start := time.Now()
	res := proto.SnapshotResult{Method: methodDump}
	local, err := storedAccount(creds)
	if err != nil {
		return res, err
	}
	l := h.Layout()
	dumpBin, err := findClientTool(l.MysqldPath, "mysqldump")
	if err != nil {
		return res, err
	}
	loadBin, err := findClientTool(l.MysqldPath, "mysql")
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
	executed, err := localGTIDExecuted(ctx, db)
	if err != nil {
		return res, err
	}
	gtidMode := dumpGTIDMode(executed, rs != nil, p)

	srcFile, err := writeDefaultsFile(h, proto.ConnectParams{User: p.User, Password: p.Password, Host: p.Host, Port: p.Port}, "", 0)
	if err != nil {
		return res, err
	}
	defer os.Remove(srcFile)
	localFile, err := writeDefaultsFile(h, local, l.Socket, l.Port)
	if err != nil {
		return res, err
	}
	defer os.Remove(localFile)
	args := dumpArgs(srcFile, p.Databases, gtidMode)
	if err := checkClients(ctx, h, loadBin, localFile, dumpBin, srcFile, p.Databases); err != nil {
		return res, err
	}

	// From here on local data changes.
	if rs != nil {
		if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
			return res, fmt.Errorf("STOP REPLICA: %w", err)
		}
	}
	restore, err := liftReadOnly(ctx, j.Logf, db)
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

	j.Logf("info", "dumping %s from %s:%d as %s (gtid purged %s)", databaseList(p.Databases), p.Host, p.Port, p.User, gtidMode)
	j.Progress(0, "dump dimulai")
	var dumpGTID string
	var found bool
	res.Bytes, dumpGTID, found, err = pipeDump(j, h, dumpBin, args, loadBin, loadArgs(localFile), p.EstimateBytes)
	if err != nil {
		return res, err
	}
	if gtidMode == "COMMENTED" {
		if !found {
			return res, errors.New("dump selesai dimuat, tetapi set GTID master tidak ditemukan di kepala dump")
		}
		res.GTIDExecuted = dumpGTID
	} else if res.GTIDExecuted, err = localGTIDExecuted(ctx, db); err != nil {
		return res, err
	}
	res.TookMs = time.Since(start).Milliseconds()
	j.Logf("info", "dump loaded: %d bytes, gtid %s", res.Bytes, res.GTIDExecuted)
	return res, nil
}

// checkClients proves both sides before anything local is changed: the
// local mysql logs in with the load's own arguments (so the init command's
// sql_log_bin privilege is checked too), and a trial run of the dump
// (probeArgs) succeeds on the master, which covers its login, privileges
// and database names.
func checkClients(ctx context.Context, h Host, loadBin, localFile, dumpBin, srcFile string, databases []string) error {
	if err := h.Exec(ctx, nil, io.Discard, loadBin, append(loadArgs(localFile), "-N", "-e", "SELECT 1")...); err != nil {
		return fmt.Errorf("login MySQL lokal dengan akun tersimpan gagal: %v", err)
	}
	if err := h.Exec(ctx, nil, io.Discard, dumpBin, probeArgs(srcFile, databases)...); err != nil {
		return fmt.Errorf("uji dump dari master gagal (login, hak akses, atau nama database); data di server ini belum diubah: %v", err)
	}
	return nil
}

// probeArgs are the dump's arguments for a schema-only trial run that
// takes no lock on the master: with --set-gtid-purged other than OFF,
// mysqldump runs FLUSH TABLES (binary-logged) and FLUSH TABLES WITH READ
// LOCK even with --no-data.
func probeArgs(defaultsFile string, databases []string) []string {
	args := dumpArgs(defaultsFile, databases, "OFF")
	return append([]string{args[0], "--no-data"}, args[1:]...)
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
// --defaults-file must come first; it is the only option file read, so no
// ~/.my.cnf can change the account.
func dumpArgs(defaultsFile string, databases []string, gtidPurged string) []string {
	args := []string{
		"--defaults-file=" + defaultsFile,
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
	return []string{"--defaults-file=" + defaultsFile, "--init-command=SET sql_log_bin=0"}
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

// errNotConnected refuses a dump on a server without a stored admin
// account: the local client must log in as the account Host.Open uses.
var errNotConnected = errors.New("akun MySQL belum terhubung; jalankan Hubungkan MySQL dulu")

// storedAccount is the stored admin account the local mysql logs in with.
func storedAccount(creds CredentialStore) (proto.ConnectParams, error) {
	if creds == nil {
		return proto.ConnectParams{}, errNotConnected
	}
	p, ok, err := creds.Load()
	if err != nil {
		return p, fmt.Errorf("baca kredensial MySQL: %w", err)
	}
	if !ok {
		return p, errNotConnected
	}
	return p, nil
}

// clientDefaults renders the [client] option file for p, choosing socket
// or TCP the way DSN does. It is read alone (--defaults-file), so it names
// the server fully: a local account goes over socket, or, when the socket
// is unknown, to 127.0.0.1 on port (3306 when 0). Values are double-quoted
// with \ and " escaped, so the password may hold #, quotes and spaces;
// control characters, which the option-file syntax cannot carry, are
// refused.
func clientDefaults(p proto.ConnectParams, socket string, port int) (string, error) {
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
	tcp := p.Port > 0 || (p.Host != "" && p.Host != "localhost")
	if !tcp && socket != "" {
		if err := line("socket", socket); err != nil {
			return "", err
		}
		return b.String(), nil
	}
	host := p.Host
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	if tcp {
		port = p.Port
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

// writeDefaultsFile writes clientDefaults(p, socket, port) to a new 0600
// file and returns its path; the caller removes it.
func writeDefaultsFile(h Host, p proto.ConnectParams, socket string, port int) (string, error) {
	content, err := clientDefaults(p, socket, port)
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
// restart restores the configured values whatever happens here. logf
// receives what it did (a job's Logf).
func liftReadOnly(ctx context.Context, logf func(level, format string, args ...any), db *sql.DB) (restore func(), err error) {
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
				logf("error", "%s failed: %v", q, err)
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
		logf("info", "read_only lifted for the load")
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
// through it against estimate. It returns that byte count and the GTID set
// in the dump's header (found false when there is none). Cancelling the job
// kills both. The error is the side that failed first: mysql stopping early
// ends mysqldump, which is then not the cause.
func pipeDump(j *jobs.Job, h Host, dumpBin string, dumpArgs []string, loadBin string, loadArgs []string,
	estimate uint64) (n uint64, gtid string, found bool, err error) {
	ctx, cancel := context.WithCancel(j.Context())
	defer cancel()
	pr, pw := io.Pipe()
	counter := &countingWriter{w: pw}
	var loadEnded atomic.Bool
	type outcome struct {
		err   error
		first bool
	}
	dumped := make(chan outcome, 1)
	go func() {
		err := h.Exec(ctx, nil, counter, dumpBin, dumpArgs...)
		o := outcome{err: err, first: err != nil && !loadEnded.Load()}
		pw.CloseWithError(err) // nil: mysql reads EOF
		dumped <- o
	}()
	stop := reportDumpProgress(j, &counter.n, estimate)
	loadErr := h.Exec(ctx, pr, nil, loadBin, loadArgs...)
	loadEnded.Store(true)
	if loadErr != nil {
		cancel()
	}
	pr.CloseWithError(errLoadEnded)
	d := <-dumped
	stop()

	n = counter.n.Load()
	switch {
	case j.Context().Err() != nil:
		return n, "", false, j.Context().Err()
	case d.first:
		return n, "", false, fmt.Errorf("mysqldump dari master gagal: %v", d.err)
	case loadErr != nil:
		return n, "", false, loadFailure(loadErr)
	}
	gtid, found = parseDumpGTID(counter.head)
	return n, gtid, found, nil
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

// dumpHeadSize is how much of the dump is kept to find its GTID header,
// which follows a few lines of session settings.
const dumpHeadSize = 64 << 10

// countingWriter counts the bytes written through it and keeps the first
// dumpHeadSize of them, so a header split across writes is still whole.
type countingWriter struct {
	w    io.Writer
	n    atomic.Uint64
	head []byte
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if room := dumpHeadSize - len(c.head); room > 0 {
		c.head = append(c.head, p[:min(room, len(p))]...)
	}
	n, err := c.w.Write(p)
	c.n.Add(uint64(n))
	return n, err
}

// dumpGTIDRe finds the GTID set mysqldump writes at the start of a dump,
// in either form (8.0.46):
//
//	SET @@GLOBAL.GTID_PURGED=/*!80000 '+'*/ 'uuid:1-3,\nuuid2:1-5';       (ON)
//	/* SET @@GLOBAL.GTID_PURGED='+uuid:1-3,\nuuid2:1-5';*/              (COMMENTED)
//
// A set spans lines, one per server; tags (8.4) are letters and _.
var dumpGTIDRe = regexp.MustCompile(`SET @@GLOBAL\.GTID_PURGED=(?:/\*!80000 '\+'\*/ )?'\+?([0-9A-Za-z_:,\-\s]*)'`)

// parseDumpGTID returns the GTID set in a dump's head, without line breaks.
func parseDumpGTID(head []byte) (string, bool) {
	m := dumpGTIDRe.FindSubmatch(head)
	if m == nil {
		return "", false
	}
	return strings.Join(strings.Fields(string(m[1])), ""), true
}

// databaseList names databases for a log line.
func databaseList(databases []string) string {
	if len(databases) == 0 {
		return "all databases"
	}
	return strings.Join(databases, ", ")
}
