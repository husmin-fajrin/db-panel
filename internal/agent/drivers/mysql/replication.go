package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The replication commands point this server at a master, choose which
// databases it applies, and start, stop, wait for and tear down the
// default channel. Every one logs in with the stored admin account.

const (
	// maxGTIDSetLen bounds a GTID set taken from the panel.
	maxGTIDSetLen = 64 << 10
	// maxSourcePasswordLen is the longest SOURCE_PASSWORD MySQL accepts
	// (longer ones fail with error 3056).
	maxSourcePasswordLen = 32
	// maxWaitSec bounds mysql.waitSynced and mysql.startUntil.
	maxWaitSec = 86400
	// startCheckTimeout is how long mysql.startReplica watches the
	// threads come up.
	startCheckTimeout = 10 * time.Second
	// untilPoll is how often mysql.startUntil reads the applier's state.
	untilPoll = 500 * time.Millisecond
	// waitSlice bounds one WAIT_FOR_EXECUTED_GTID_SET call, so a stopped
	// applier or a cancelled request is noticed between slices.
	waitSlice = 5
)

// gtidSetRe is a GTID set as MySQL prints it: uuid:interval[:interval...]
// (8.4 also admits uuid:tag:interval), comma-separated, with whitespace
// and newlines around the commas. It admits no quote or backslash.
var gtidSetRe = func() *regexp.Regexp {
	const (
		uuid  = `[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`
		part  = `(?::(?:[0-9]{1,19}(?:-[0-9]{1,19})?|[A-Za-z_][A-Za-z0-9_]{0,31}))`
		entry = uuid + part + `+`
	)
	return regexp.MustCompile(`^\s*(?:` + entry + `(?:\s*,\s*` + entry + `)*)?\s*$`)
}()

// errNotReplica refuses a command that needs a configured source.
var errNotReplica = &commands.ParamError{Msg: "replikasi belum dikonfigurasi di server ini; konfigurasikan sumber replikasi dulu"}

// ioErrorTexts explain the receiver errors a user can fix.
var ioErrorTexts = map[int]string{
	1045: "user replikasi ditolak master; periksa password/hak",
	2003: "master tidak bisa dihubungi dari server ini",
	2005: "nama host master tidak dikenal di server ini",
}

func (d *Driver) registerReplication(reg *commands.Registry) {
	reg.Register("mysql.configureReplication", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ReplicationSourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateSource(&p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		rs, err := ConfigureReplication(ctx, h, p)
		if err != nil {
			return nil, err
		}
		d.log("mysql replication configured", "host", p.Host, "port", p.Port, "user", p.User, "ssl", p.SSL,
			"databases", p.Databases)
		return rs, nil
	})

	reg.Register("mysql.setReplicationFilter", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ReplicationFilterParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateFilter(p.Databases); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		rs, err := SetReplicationFilter(ctx, h, p.Databases)
		if err != nil {
			return nil, err
		}
		d.log("mysql replication filter set", "databases", p.Databases)
		return rs, nil
	})

	reg.Register("mysql.startReplica", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		rs, err := StartReplica(ctx, h)
		if err != nil {
			return nil, err
		}
		d.log("mysql replica started", "source", net.JoinHostPort(rs.SourceHost, strconv.Itoa(rs.SourcePort)),
			"io", rs.IORunning, "sql", rs.SQLRunning)
		return rs, nil
	})

	reg.Register("mysql.stopReplica", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		rs, err := StopReplica(ctx, h)
		if err != nil {
			return nil, err
		}
		d.log("mysql replica stopped", "configured", rs != nil)
		return rs, nil
	})

	reg.Register("mysql.resetReplica", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if err := ResetReplica(ctx, h); err != nil {
			return nil, err
		}
		d.log("mysql replica reset")
		return nil, nil
	})

	reg.Register("mysql.waitSynced", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.WaitSyncedParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateWait(p.GTIDSet, p.TimeoutSec); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		return WaitSynced(ctx, h, p)
	})

	reg.Register("mysql.startUntil", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.StartUntilParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateWait(p.GTIDSet, p.TimeoutSec); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.GTIDSet) == "" {
			return nil, &commands.ParamError{Msg: "set GTID tujuan wajib diisi"}
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		rs, err := StartUntil(ctx, h, p)
		if err != nil {
			return nil, err
		}
		d.log("mysql replica applied until gtid set", "timeout_sec", p.TimeoutSec)
		return rs, nil
	})
}

func (d *Driver) log(msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.Info(msg, args...)
	}
}

// validateSource checks p. Nothing in p reaches SQL without passing here;
// the password is never part of an error.
func validateSource(p *proto.ReplicationSourceParams) error {
	p.Host, p.User = strings.TrimSpace(p.Host), strings.TrimSpace(p.User)
	bad := func(format string, args ...any) error { return &commands.ParamError{Msg: fmt.Sprintf(format, args...)} }
	switch {
	case !sourceHostRe.MatchString(p.Host):
		return bad("alamat master tidak valid: %q", p.Host)
	case strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil:
		return bad("alamat master tidak valid: %q", p.Host)
	case p.Port < 1 || p.Port > 65535:
		return bad("port master harus antara 1 dan 65535")
	case !userNameRe.MatchString(p.User):
		return bad("nama user replikasi tidak valid: %q", p.User)
	case p.Password == "":
		return bad("password user replikasi wajib diisi")
	case strings.ContainsFunc(p.Password, unicode.IsControl):
		return bad("password user replikasi tidak boleh berisi karakter kontrol")
	case utf8.RuneCountInString(p.Password) > maxSourcePasswordLen:
		return bad("password user replikasi paling panjang %d karakter (batas SOURCE_PASSWORD MySQL)", maxSourcePasswordLen)
	}
	return validateFilter(p.Databases)
}

// validateFilter checks the databases of a replication filter.
func validateFilter(dbs []string) error {
	seen := map[string]bool{}
	for _, db := range dbs {
		msg := ""
		switch {
		case !dbNameRe.MatchString(db):
			msg = fmt.Sprintf("nama database tidak valid: %q", db)
		case systemDatabases[strings.ToLower(db)]:
			msg = fmt.Sprintf("database sistem %s tidak bisa dipilih untuk replikasi", db)
		case seen[db]:
			msg = fmt.Sprintf("database %s disebut dua kali", db)
		}
		if msg != "" {
			return &commands.ParamError{Msg: msg}
		}
		seen[db] = true
	}
	return nil
}

// validateWait checks a GTID set and a timeout taken from the panel.
func validateWait(set string, timeoutSec int) error {
	switch {
	case !validGTIDSet(set):
		return &commands.ParamError{Msg: "set GTID tidak valid"}
	case timeoutSec < 1 || timeoutSec > maxWaitSec:
		return &commands.ParamError{Msg: fmt.Sprintf("batas waktu harus antara 1 dan %d detik", maxWaitSec)}
	}
	return nil
}

// validGTIDSet reports whether s is a GTID set (possibly empty) no longer
// than maxGTIDSetLen.
func validGTIDSet(s string) bool {
	return len(s) <= maxGTIDSetLen && gtidSetRe.MatchString(s)
}

// wildDoTablePatterns are the REPLICATE_WILD_DO_TABLE patterns for dbs:
// every table of each database. The pattern is matched with LIKE, so _ in
// a name is escaped; dbNameRe admits no % (and - is literal in LIKE).
func wildDoTablePatterns(dbs []string) []string {
	out := make([]string, len(dbs))
	for i, db := range dbs {
		out[i] = strings.ReplaceAll(db, "_", `\_`) + ".%"
	}
	return out
}

// filterStatement sets the replication filter to dbs; none clears it.
// quote makes a string literal for the session's sql_mode.
func filterStatement(dbs []string, quote func(string) string) string {
	pats := wildDoTablePatterns(dbs)
	for i, p := range pats {
		pats[i] = quote(p)
	}
	return "CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = (" + strings.Join(pats, ", ") + ")"
}

// sourceStatement points the default channel at p's master with GTID
// auto-positioning. MySQL takes no placeholder in CHANGE REPLICATION
// SOURCE TO, so the validated values are literals made by quote (for the
// session's sql_mode). SOURCE_SSL is always set, so re-pointing a replica
// from an SSL master to one without SSL does not keep the old setting.
//
// Over TLS the password travels encrypted and the source's RSA key is not
// requested (GET_SOURCE_PUBLIC_KEY = 0), so an impostor cannot hand one in;
// without TLS caching_sha2_password needs it.
func sourceStatement(p proto.ReplicationSourceParams, quote func(string) string) string {
	ssl, pubKey := "0", "1"
	if p.SSL {
		ssl, pubKey = "1", "0"
	}
	return "CHANGE REPLICATION SOURCE TO SOURCE_HOST = " + quote(p.Host) +
		", SOURCE_PORT = " + strconv.Itoa(p.Port) +
		", SOURCE_USER = " + quote(p.User) +
		", SOURCE_PASSWORD = " + quote(p.Password) +
		", SOURCE_AUTO_POSITION = 1, GET_SOURCE_PUBLIC_KEY = " + pubKey + ", SOURCE_CONNECT_RETRY = 10, SOURCE_RETRY_COUNT = 86400" +
		", SOURCE_SSL = " + ssl
}

// sessionQuoter returns quotePassword for conn's sql_mode, so the literals
// it makes mean the same in the session they run in.
func sessionQuoter(ctx context.Context, conn *sql.Conn) (func(string) string, error) {
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return nil, fmt.Errorf("baca sql_mode: %w", err)
	}
	noBackslash := strings.Contains(mode, "NO_BACKSLASH_ESCAPES")
	return func(s string) string { return quotePassword(s, noBackslash) }, nil
}

// adminConn opens the admin account and takes one session from it. The
// caller closes both.
func adminConn(ctx context.Context, h Host) (*sql.DB, *sql.Conn, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, conn, nil
}

// execConn runs fixed statements on conn in order.
func execConn(ctx context.Context, conn *sql.Conn, stmts ...string) error {
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

// currentStatus is replicaStatus with its error worded for the user.
func currentStatus(ctx context.Context, db *sql.DB) (*proto.ReplicaStatus, error) {
	rs, err := replicaStatus(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("baca status replika: %w", err)
	}
	return rs, nil
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// executedSet is @@GLOBAL.gtid_executed as MySQL returns it.
func executedSet(ctx context.Context, db *sql.DB) (string, error) {
	var g string
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_executed").Scan(&g); err != nil {
		return "", fmt.Errorf("baca gtid_executed: %w", err)
	}
	return g, nil
}

// executed reports whether this server has executed every transaction in set.
func executed(ctx context.Context, db *sql.DB, set string) (bool, error) {
	var ok bool
	if err := db.QueryRowContext(ctx, "SELECT GTID_SUBSET(?, @@GLOBAL.gtid_executed)", set).Scan(&ok); err != nil {
		return false, fmt.Errorf("bandingkan set GTID: %w", err)
	}
	return ok, nil
}

// ConfigureReplication points the default channel at p's master with p's
// filter. It stops a configured replica first and does not start it. A
// refused source leaves the replica as it was, only stopped.
func ConfigureReplication(ctx context.Context, h Host, p proto.ReplicationSourceParams) (*proto.ReplicaStatus, error) {
	db, conn, err := adminConn(ctx, h)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer conn.Close()

	if err := refuseOnSource(ctx, db); err != nil {
		return nil, err
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return nil, err
	}
	if rs != nil {
		if err := execConn(ctx, conn, "STOP REPLICA"); err != nil {
			return nil, err
		}
	}
	quote, err := sessionQuoter(ctx, conn)
	if err != nil {
		return nil, err
	}
	// The source first: if MySQL refuses it, the replica keeps its old
	// source and filter. Without FOR CHANNEL the filter statement then
	// replaces the channel's filter, including one a new channel copied
	// from stale global filters. The source statement holds the password:
	// neither it nor a server error quoting it reaches an error or a log.
	password := quote(p.Password)
	if _, err := conn.ExecContext(ctx, sourceStatement(p, quote)); err != nil {
		msg := strings.ReplaceAll(strings.ReplaceAll(err.Error(), password, "'***'"), p.Password, "***")
		return nil, fmt.Errorf("arahkan replikasi ke %s: %s", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), msg)
	}
	if err := execConn(ctx, conn, filterStatement(p.Databases, quote)); err != nil {
		return nil, fmt.Errorf("atur filter replikasi: %w", err)
	}
	return currentStatus(ctx, db)
}

// SetReplicationFilter replaces the filter of the configured replica with
// dbs (none = every database) and starts it again; the source stays.
func SetReplicationFilter(ctx context.Context, h Host, dbs []string) (*proto.ReplicaStatus, error) {
	db, conn, err := adminConn(ctx, h)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer conn.Close()

	if rs, err := currentStatus(ctx, db); err != nil {
		return nil, err
	} else if rs == nil {
		return nil, errNotReplica
	}
	quote, err := sessionQuoter(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := execConn(ctx, conn, "STOP REPLICA", filterStatement(dbs, quote), "START REPLICA"); err != nil {
		return nil, err
	}
	return awaitStarted(ctx, db)
}

// StartReplica starts the configured replica, or leaves a running one,
// and returns once both threads run.
func StartReplica(ctx context.Context, h Host) (*proto.ReplicaStatus, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if rs, err := currentStatus(ctx, db); err != nil {
		return nil, err
	} else if rs == nil {
		return nil, errNotReplica
	}
	if _, err := db.ExecContext(ctx, "START REPLICA"); err != nil {
		return nil, fmt.Errorf("START REPLICA: %w", err)
	}
	return awaitStarted(ctx, db)
}

// awaitStarted watches the threads for startCheckTimeout after START
// REPLICA: it fails on a receiver or applier error, succeeds as soon as
// both run, and at the end accepts a receiver still connecting.
func awaitStarted(ctx context.Context, db *sql.DB) (*proto.ReplicaStatus, error) {
	deadline := time.Now().Add(startCheckTimeout)
	for {
		rs, err := currentStatus(ctx, db)
		switch {
		case err != nil:
			return nil, err
		case rs == nil:
			return nil, errNotReplica
		case rs.LastIOErrno != 0:
			return nil, ioError(rs)
		case rs.LastSQLErrno != 0:
			return nil, sqlError(rs)
		case rs.IORunning == "Yes" && rs.SQLRunning == "Yes":
			return rs, nil
		case time.Now().After(deadline):
			if rs.IORunning == "Connecting" && rs.SQLRunning == "Yes" {
				return rs, nil
			}
			return nil, fmt.Errorf("replikasi tidak berjalan setelah START REPLICA (IO: %s, SQL: %s)", rs.IORunning, rs.SQLRunning)
		}
		if err := pause(ctx, ReadyPollInterval); err != nil {
			return nil, err
		}
	}
}

// ioError explains the receiver's last error.
func ioError(rs *proto.ReplicaStatus) error {
	text, ok := ioErrorTexts[rs.LastIOErrno]
	if !ok {
		text = "replikasi gagal membaca dari master"
	}
	return fmt.Errorf("%s (error IO %d: %s)", text, rs.LastIOErrno, rs.LastIOError)
}

// sqlError explains the applier's last error.
func sqlError(rs *proto.ReplicaStatus) error {
	return fmt.Errorf("replikasi berhenti karena error SQL %d: %s", rs.LastSQLErrno, rs.LastSQLError)
}

// StopReplica stops both threads. A server without a source answers nil.
func StopReplica(ctx context.Context, h Host) (*proto.ReplicaStatus, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if rs, err := currentStatus(ctx, db); err != nil || rs == nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
		return nil, fmt.Errorf("STOP REPLICA: %w", err)
	}
	return currentStatus(ctx, db)
}

// ResetReplica removes the source and the filter; the data and the GTID
// history stay. Running it on a server that is not a replica is harmless.
func ResetReplica(ctx context.Context, h Host) error {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := refuseOnSource(ctx, db); err != nil {
		return err
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return err
	}
	stmts := []string{filterStatement(nil, quoteString)}
	if rs != nil {
		stmts = append([]string{"STOP REPLICA", "RESET REPLICA ALL"}, stmts...)
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

// WaitSynced waits up to p.TimeoutSec for this server to execute
// p.GTIDSet. It answers at once when the set is executed, or when the
// applier is not running or the receiver stopped on an error (the set
// cannot arrive), and otherwise waits in slices so a thread that stops
// meanwhile ends the wait.
func WaitSynced(ctx context.Context, h Host, p proto.WaitSyncedParams) (proto.WaitSyncedResult, error) {
	var res proto.WaitSyncedResult
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()

	deadline := time.Now().Add(time.Duration(p.TimeoutSec) * time.Second)
	slice := 1 // a short first wait, then a look at the applier
	for {
		done, err := executed(ctx, db, p.GTIDSet)
		if err != nil {
			return res, err
		}
		if done {
			res.Synced = true
			break
		}
		rs, err := currentStatus(ctx, db)
		if err != nil {
			return res, err
		}
		left := time.Until(deadline)
		// A stopped applier, or a receiver that stopped on an error, cannot
		// bring the set in: answer now rather than at the timeout.
		if rs == nil || rs.SQLRunning != "Yes" || (rs.IORunning == "No" && rs.LastIOErrno != 0) || left <= 0 {
			break
		}
		secs := min(slice, int(math.Ceil(left.Seconds())))
		slice = waitSlice
		var r sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT WAIT_FOR_EXECUTED_GTID_SET(?, ?)", p.GTIDSet, secs).Scan(&r); err != nil {
			return res, fmt.Errorf("tunggu set GTID: %w", err)
		}
		if r.Valid && r.Int64 == 0 {
			res.Synced = true
			break
		}
	}
	if res.Executed, err = executedSet(ctx, db); err != nil {
		return res, err
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return res, err
	}
	if rs != nil {
		res.Status = *rs
	}
	return res, nil
}

// StartUntil starts the configured replica with its applier stopping
// after p.GTIDSet, and returns once the applier has stopped there. The
// receiver keeps running. A running replica is stopped first. On a
// timeout the replica is left running toward the set.
func StartUntil(ctx context.Context, h Host, p proto.StartUntilParams) (*proto.ReplicaStatus, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return nil, err
	}
	if rs == nil {
		return nil, errNotReplica
	}
	if rs.IORunning == "Yes" || rs.IORunning == "Connecting" || rs.SQLRunning == "Yes" {
		if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
			return nil, fmt.Errorf("STOP REPLICA: %w", err)
		}
	}
	// MySQL takes no placeholder here; the validated set holds no quote
	// or backslash.
	if _, err := db.ExecContext(ctx, "START REPLICA UNTIL SQL_AFTER_GTIDS = "+quoteString(p.GTIDSet)); err != nil {
		return nil, fmt.Errorf("START REPLICA UNTIL: %w", err)
	}

	deadline := time.Now().Add(time.Duration(p.TimeoutSec) * time.Second)
	for {
		if rs, err = currentStatus(ctx, db); err != nil {
			return nil, err
		}
		switch {
		case rs == nil:
			return nil, errNotReplica
		case rs.LastSQLErrno != 0:
			return nil, sqlError(rs)
		case rs.SQLRunning == "No":
			ok, err := executed(ctx, db, p.GTIDSet)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, untilError(ctx, db, rs, "applier replikasi berhenti sebelum mencapai set GTID tujuan")
			}
			return rs, nil
		case time.Now().After(deadline):
			return nil, untilError(ctx, db, rs, fmt.Sprintf("replika belum mencapai set GTID tujuan dalam %d detik", p.TimeoutSec))
		}
		if err := pause(ctx, untilPoll); err != nil {
			return nil, err
		}
	}
}

// untilError is msg with the set this server has executed and the
// receiver's error, if any.
func untilError(ctx context.Context, db *sql.DB, rs *proto.ReplicaStatus, msg string) error {
	set, err := executedSet(ctx, db)
	if err != nil {
		return err
	}
	msg += "; sudah dieksekusi: " + set
	if rs.LastIOErrno != 0 {
		msg += "; " + ioError(rs).Error()
	}
	return errors.New(msg)
}
