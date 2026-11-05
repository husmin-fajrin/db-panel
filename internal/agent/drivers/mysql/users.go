package mysql

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// accountHostRe is the host part of an account the panel creates: "%", an
// IPv4/IPv6 literal, a hostname, or a pattern of those. It admits no quote
// or backslash.
var accountHostRe = regexp.MustCompile(`^[A-Za-z0-9_.:%-]{1,255}$`)

// managedUserPrefix starts the name of every account the agent creates,
// changes or drops: the panel can never reach root or an application's
// account through these commands.
const managedUserPrefix = "dbpanel_"

const (
	// replGrants are the privileges of the replication account:
	// REPLICATION SLAVE for replication and BACKUP_ADMIN for CLONE (as
	// donor). Its password is on every replica, so it can read the binary
	// log but neither the tables directly nor reset or lock the master.
	replGrants = "REPLICATION SLAVE, BACKUP_ADMIN"
	// dumpGrants are what mysqldump --single-transaction --set-gtid-purged
	// --triggers --routines --events --no-tablespaces needs: FLUSH_TABLES
	// for its FLUSH TABLES and brief FLUSH TABLES WITH READ LOCK. Only a
	// temporary per-job account holds them (mysql.createDumpUser, dropped
	// with mysql.dropUser, swept hourly by the health monitor).
	dumpGrants = "SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT"
	// dumpGrantsReload is dumpGrants before FLUSH_TABLES existed: RELOAD,
	// which also allows RESET MASTER and every other FLUSH statement.
	dumpGrantsReload = "SELECT, RELOAD, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT"
	// flushTablesMinVersion is the first MySQL release with the
	// FLUSH_TABLES dynamic privilege (added in 8.0.31 with
	// FLUSH_OPTIMIZER_COSTS, FLUSH_STATUS and FLUSH_USER_RESOURCES).
	flushTablesMinVersion = "8.0.31"
)

// dumpGrantsFor is dumpGrants for a server of version (@@version), and
// dumpGrantsReload for one older than flushTablesMinVersion or whose
// version cannot be read.
func dumpGrantsFor(version string) string {
	v, err := ParseVersion(version)
	if err != nil {
		return dumpGrantsReload
	}
	floor, _ := ParseVersion(flushTablesMinVersion)
	if cmp.Or(cmp.Compare(v.Major, floor.Major), cmp.Compare(v.Minor, floor.Minor), cmp.Compare(v.Patch, floor.Patch)) < 0 {
		return dumpGrantsReload
	}
	return dumpGrants
}

func (d *Driver) registerUsers(reg *commands.Registry) {
	reg.Register("mysql.createReplUser", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ReplUserParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateReplUser(&p); err != nil {
			return nil, err
		}
		if err := d.allow("mysql.createReplUser", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		// A pinned master's existing account is never changed: a new
		// password or other grants would cut every replica off.
		a := account{User: p.User, Host: p.FromHost, Password: p.Password, RequireSSL: p.RequireSSL}
		if err := createAccount(ctx, h, a, replGrants, "replikasi", d.pins.RoleIs(roleMaster)); err != nil {
			return nil, err
		}
		d.log("mysql replication user ready", "user", p.User, "from_host", p.FromHost, "require_ssl", p.RequireSSL)
		return nil, nil
	})

	reg.Register("mysql.createDumpUser", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.DumpUserParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		a := account{User: strings.TrimSpace(p.User), Host: strings.TrimSpace(p.FromHost), Password: p.Password, RequireSSL: p.RequireSSL}
		if a.Host == "" {
			return nil, &commands.ParamError{Msg: "host user dump wajib diisi (alamat replika)"}
		}
		if err := a.validate("dump"); err != nil {
			return nil, err
		}
		if err := d.allow("mysql.createDumpUser", proto.DumpUserParams{User: a.User, FromHost: a.Host}); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		grants, err := d.dumpGrantsHere(ctx, h)
		if err != nil {
			return nil, err
		}
		if err := createAccount(ctx, h, a, grants, "dump", d.pins.RoleIs(roleMaster)); err != nil {
			return nil, err
		}
		d.log("mysql dump user created", "user", a.User, "from_host", a.Host, "require_ssl", a.RequireSSL)
		return nil, nil
	})

	reg.Register("mysql.dropUser", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.DropUserParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		user, host := strings.TrimSpace(p.User), strings.TrimSpace(p.FromHost)
		if err := validateAccountName(user, host, "user"); err != nil {
			return nil, err
		}
		if err := d.allow("mysql.dropUser", proto.DropUserParams{User: user, FromHost: host}); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if err := DropUser(ctx, h, user, host); err != nil {
			return nil, err
		}
		d.log("mysql user dropped", "user", user, "from_host", host)
		return nil, nil
	})

	// Not in the pins' refusal table: it only ever drops dbpanel_dump_*
	// accounts, which a pinned master may drop (refusedByRole).
	reg.Register("mysql.sweepDumpUsers", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.SweepDumpUsersParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if p.OlderThanSec < minSweepAgeSec {
			return nil, &commands.ParamError{Msg: fmt.Sprintf("older_than_sec minimal %d detik", minSweepAgeSec)}
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		return SweepDumpUsers(ctx, h, p.OlderThanSec, d.log)
	})
}

// minSweepAgeSec is the youngest dump account mysql.sweepDumpUsers may be
// asked to drop: one a running job has just created is never swept.
const minSweepAgeSec = 60

// account is an account the agent creates.
type account struct {
	User, Host, Password string
	RequireSSL           bool
}

// validateAccountName checks a managed account's name and host; what names
// the account in messages ("replikasi", "dump", "user").
func validateAccountName(user, host, what string) error {
	switch {
	case !userNameRe.MatchString(user):
		return &commands.ParamError{Msg: fmt.Sprintf("nama %s tidak valid: %q", what, user)}
	case !strings.HasPrefix(user, managedUserPrefix) || len(user) == len(managedUserPrefix):
		return &commands.ParamError{Msg: fmt.Sprintf("nama %s harus diawali %s; agent hanya mengelola akun miliknya sendiri", what, managedUserPrefix)}
	case !accountHostRe.MatchString(host):
		return &commands.ParamError{Msg: fmt.Sprintf("host %s tidak valid: %q", what, host)}
	}
	return nil
}

// validate checks a. The password is never part of an error.
func (a account) validate(what string) error {
	if err := validateAccountName(a.User, a.Host, "user "+what); err != nil {
		return err
	}
	switch {
	case a.Password == "":
		return &commands.ParamError{Msg: "password user " + what + " wajib diisi"}
	case strings.ContainsFunc(a.Password, unicode.IsControl):
		return &commands.ParamError{Msg: "password user " + what + " tidak boleh berisi karakter kontrol"}
	}
	return nil
}

// validateReplUser checks p and fills the default host. The password is
// never part of an error.
func validateReplUser(p *proto.ReplUserParams) error {
	p.User, p.FromHost = strings.TrimSpace(p.User), strings.TrimSpace(p.FromHost)
	if p.FromHost == "" {
		p.FromHost = "%"
	}
	return account{User: p.User, Host: p.FromHost, Password: p.Password}.validate("replikasi")
}

// CreateReplUser creates p.User@p.FromHost with p.Password, or sets the
// password of the existing account, and leaves it exactly replGrants (an
// existing account's other privileges are revoked). Running it again is
// harmless. Dumps read the master with a temporary account instead
// (mysql.createDumpUser).
//
// MySQL does not take a placeholder in IDENTIFIED BY, so the password is
// sent as a string literal escaped for the session's sql_mode; the server
// masks it in its own logs, and it never reaches ours or an error.
func CreateReplUser(ctx context.Context, h Host, p proto.ReplUserParams) error {
	return createAccount(ctx, h, account{User: p.User, Host: p.FromHost, Password: p.Password, RequireSSL: p.RequireSSL},
		replGrants, "replikasi", false)
}

// accountExistsOnMaster is the refusal of keepExisting: the panel matches
// its words ("sudah ada di master yang dipin") to tell it from a failure.
// names are the existing rows of the account (user@host).
func accountExistsOnMaster(names []string) error {
	return &pinError{kind: ErrRolePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"akun %s sudah ada di master yang dipin; agent tidak mengubah akun yang sudah ada (sandi dan haknya tetap)",
		strings.Join(names, ", "))}}
}

// createAccount creates a, or sets the password of the existing account,
// requires TLS or not, and leaves it holding exactly grants ON *.*. With
// keepExisting (a pinned master) the account is refused instead when a
// row of a.User exists at any host: an existing one is never altered, and
// no second row (which could shadow the first for some clients) is added.
func createAccount(ctx context.Context, h Host, a account, grants, what string, keepExisting bool) error {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	// One session, so the sql_mode read is the one the statement runs in.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// User and host are validated: they hold no quote or backslash.
	name := quoteString(a.User) + "@" + quoteString(a.Host)
	n, err := accountExists(ctx, conn, a.User, a.Host)
	if err != nil {
		return fmt.Errorf("baca user %s: %w", what, err)
	}
	if keepExisting {
		hosts, err := accountHosts(ctx, conn, a.User)
		if err != nil {
			return fmt.Errorf("baca user %s: %w", what, err)
		}
		if len(hosts) > 0 {
			names := make([]string, len(hosts))
			for i, host := range hosts {
				names[i] = quoteString(a.User) + "@" + quoteString(host)
			}
			return accountExistsOnMaster(names)
		}
	}
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return fmt.Errorf("baca sql_mode: %w", err)
	}
	password := quotePassword(a.Password, strings.Contains(mode, "NO_BACKSLASH_ESCAPES"))
	stmt := "CREATE USER IF NOT EXISTS " + name + " IDENTIFIED BY "
	if n {
		stmt = "ALTER USER " + name + " IDENTIFIED BY "
	}
	if _, err := conn.ExecContext(ctx, stmt+password); err != nil {
		// A server error may quote the statement: keep the password out.
		msg := strings.ReplaceAll(strings.ReplaceAll(err.Error(), password, "'***'"), a.Password, "***")
		return fmt.Errorf("buat user %s %s: %s", what, name, msg)
	}
	tls := "NONE"
	if a.RequireSSL {
		tls = "SSL"
	}
	stmts := []string{"ALTER USER " + name + " REQUIRE " + tls}
	if n {
		// An older dbpanel granted more: leave exactly grants.
		stmts = append(stmts, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM "+name)
	}
	stmts = append(stmts, "GRANT "+grants+" ON *.* TO "+name)
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("atur user %s %s: %w", what, name, err)
		}
	}
	return nil
}

func accountExists(ctx context.Context, conn *sql.Conn, user, host string) (bool, error) {
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE user = ? AND host = ?", user, host).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// accountHosts lists the hosts of user's rows in mysql.user.
func accountHosts(ctx context.Context, conn *sql.Conn, user string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT host FROM mysql.user WHERE user = ? ORDER BY host", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// DropUser drops user@host if it exists. The name is validated (and starts
// with managedUserPrefix).
func DropUser(ctx context.Context, h Host, user, host string) error {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = dropAccount(ctx, db, user, host)
	return err
}

// dropAccount drops user@host if it exists and returns its quoted name.
// The caller has validated user and host: they hold no quote or backslash.
func dropAccount(ctx context.Context, db *sql.DB, user, host string) (string, error) {
	name := quoteString(user) + "@" + quoteString(host)
	if _, err := db.ExecContext(ctx, "DROP USER IF EXISTS "+name); err != nil {
		return name, fmt.Errorf("hapus user %s: %w", name, err)
	}
	return name, nil
}

// SweepDumpUsers drops the per-job dump accounts (dumpUserPrefix) whose
// password was set more than olderThanSec seconds ago: what a job that died between
// mysql.createDumpUser and mysql.dropUser left behind. No other account is
// read or dropped. logf gets one line per dropped account.
func SweepDumpUsers(ctx context.Context, h Host, olderThanSec int64, logf func(msg string, args ...any)) (proto.SweepDumpUsersResult, error) {
	res := proto.SweepDumpUsersResult{Dropped: []string{}}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	// _ is a LIKE wildcard: escaped, so only the literal prefix matches.
	pattern := strings.ReplaceAll(dumpUserPrefix, "_", "!_") + "%"
	rows, err := db.QueryContext(ctx, `SELECT user, host FROM mysql.user
		WHERE user LIKE ? ESCAPE '!' AND password_last_changed < NOW() - INTERVAL ? SECOND
		ORDER BY user, host`, pattern, olderThanSec)
	if err != nil {
		return res, fmt.Errorf("baca user dump: %w", err)
	}
	type acct struct{ user, host string }
	var found []acct
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.user, &a.host); err != nil {
			rows.Close()
			return res, fmt.Errorf("baca user dump: %w", err)
		}
		found = append(found, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("baca user dump: %w", err)
	}
	for _, a := range found {
		// Names the panel could not have created are left alone rather
		// than quoted into SQL.
		if !strings.HasPrefix(a.user, dumpUserPrefix) || validateAccountName(a.user, a.host, "user") != nil {
			logf("mysql dump user skipped by sweep: unexpected name", "user", a.user, "from_host", a.host)
			continue
		}
		name, err := dropAccount(ctx, db, a.user, a.host)
		if err != nil {
			return res, err
		}
		logf("mysql stale dump user dropped", "user", a.user, "from_host", a.host)
		res.Dropped = append(res.Dropped, name)
	}
	return res, nil
}

// dumpGrantsHere is dumpGrantsFor this server's version, except that it
// keeps RELOAD (with a warning naming the remedy) while the admin account
// cannot pass FLUSH_TABLES on: such an account would fail every
// mysql.createDumpUser.
func (d *Driver) dumpGrantsHere(ctx context.Context, h Host) (string, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var version, account string
	if err := db.QueryRowContext(ctx, "SELECT @@version, CURRENT_USER()").Scan(&version, &account); err != nil {
		return "", fmt.Errorf("baca versi MySQL: %w", err)
	}
	grants := dumpGrantsFor(version)
	if grants == dumpGrantsReload {
		return grants, nil
	}
	held, err := showGrants(ctx, db)
	if err != nil {
		return "", err
	}
	if !grantable(held, "FLUSH_TABLES") {
		if d.Logger != nil {
			d.Logger.Warn("mysql dump user gets RELOAD: the admin account cannot grant FLUSH_TABLES",
				"version", version, "remedy", "GRANT FLUSH_TABLES ON *.* TO "+quoteAccount(account)+" WITH GRANT OPTION")
		}
		return dumpGrantsReload, nil
	}
	return grants, nil
}

// grantable reports whether the SHOW GRANTS rows grants hold the global
// privilege priv (or ALL) WITH GRANT OPTION.
func grantable(grants []string, priv string) bool {
	for _, g := range grants {
		m := globalGrant.FindStringSubmatch(strings.TrimSpace(g))
		if m == nil || m[2] == "" {
			continue
		}
		for _, p := range strings.Split(m[1], ",") {
			p = strings.ToUpper(strings.TrimSpace(p))
			if p == priv || p == "ALL" || p == "ALL PRIVILEGES" {
				return true
			}
		}
	}
	return false
}

// SweepDumpUsers is the health monitor's hourly sweep (monitor.DumpSweeper):
// SweepDumpUsers for the per-job dump accounts older than olderThan. It
// does nothing without MySQL or a stored account, and on a server with a
// replication source: the master's own sweep drops them there through
// replication, and a local drop would be an errant transaction.
func (d *Driver) SweepDumpUsers(ctx context.Context, olderThan time.Duration) (int, error) {
	h, err := d.host()
	if errors.Is(err, ErrNoHost) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	db, err := h.Open(ctx)
	if errors.Is(err, ErrNotConnected) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rs, err := replicaStatus(ctx, db)
	db.Close()
	if err != nil {
		return 0, fmt.Errorf("baca status replika: %w", err)
	}
	if rs != nil {
		return 0, nil
	}
	res, err := SweepDumpUsers(ctx, h, int64(olderThan/time.Second), d.log)
	return len(res.Dropped), err
}

// quotePassword is s as a string literal. Without NO_BACKSLASH_ESCAPES a
// backslash starts an escape and is doubled; quotes are always doubled.
// validateReplUser has refused control characters.
func quotePassword(s string, noBackslashEscapes bool) string {
	if noBackslashEscapes {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return quoteString(s)
}
