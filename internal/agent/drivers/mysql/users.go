package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
	// --triggers --routines --events --no-tablespaces needs (RELOAD for its
	// brief FLUSH TABLES WITH READ LOCK). Only a temporary per-job account
	// holds them (mysql.createDumpUser, dropped with mysql.dropUser).
	dumpGrants = "SELECT, RELOAD, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT"
)

func (d *Driver) registerUsers(reg *commands.Registry) {
	reg.Register("mysql.createReplUser", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ReplUserParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateReplUser(&p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if err := CreateReplUser(ctx, h, p); err != nil {
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
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if err := createAccount(ctx, h, a, dumpGrants, "dump"); err != nil {
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
}

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
		replGrants, "replikasi")
}

// createAccount creates a, or sets the password of the existing account,
// requires TLS or not, and leaves it holding exactly grants ON *.*.
func createAccount(ctx context.Context, h Host, a account, grants, what string) error {
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

// DropUser drops user@host if it exists. The name is validated (and starts
// with managedUserPrefix).
func DropUser(ctx context.Context, h Host, user, host string) error {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	name := quoteString(user) + "@" + quoteString(host)
	if _, err := db.ExecContext(ctx, "DROP USER IF EXISTS "+name); err != nil {
		return fmt.Errorf("hapus user %s: %w", name, err)
	}
	return nil
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
