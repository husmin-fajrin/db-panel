package mysql

import (
	"context"
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
		if d.Logger != nil {
			d.Logger.Info("mysql replication user ready", "user", p.User, "from_host", p.FromHost)
		}
		return nil, nil
	})
}

// validateReplUser checks p and fills the default host. The password is
// never part of an error.
func validateReplUser(p *proto.ReplUserParams) error {
	p.User, p.FromHost = strings.TrimSpace(p.User), strings.TrimSpace(p.FromHost)
	if p.FromHost == "" {
		p.FromHost = "%"
	}
	switch {
	case !userNameRe.MatchString(p.User):
		return &commands.ParamError{Msg: fmt.Sprintf("nama user replikasi tidak valid: %q", p.User)}
	case !accountHostRe.MatchString(p.FromHost):
		return &commands.ParamError{Msg: fmt.Sprintf("host user replikasi tidak valid: %q", p.FromHost)}
	case p.Password == "":
		return &commands.ParamError{Msg: "password user replikasi wajib diisi"}
	case strings.ContainsFunc(p.Password, unicode.IsControl):
		return &commands.ParamError{Msg: "password user replikasi tidak boleh berisi karakter kontrol"}
	}
	return nil
}

// CreateReplUser creates p.User@p.FromHost with p.Password, or sets the
// password of the existing account, and grants it what replication and
// CLONE need. Running it again is harmless.
//
// MySQL does not take a placeholder in IDENTIFIED BY, so the password is
// sent as a string literal escaped for the session's sql_mode; the server
// masks it in its own logs, and it never reaches ours or an error.
func CreateReplUser(ctx context.Context, h Host, p proto.ReplUserParams) error {
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
	account := quoteString(p.User) + "@" + quoteString(p.FromHost)
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE user = ? AND host = ?", p.User, p.FromHost).Scan(&n); err != nil {
		return fmt.Errorf("baca user replikasi: %w", err)
	}
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return fmt.Errorf("baca sql_mode: %w", err)
	}
	password := quotePassword(p.Password, strings.Contains(mode, "NO_BACKSLASH_ESCAPES"))
	stmt := "CREATE USER IF NOT EXISTS " + account + " IDENTIFIED BY "
	if n > 0 {
		stmt = "ALTER USER " + account + " IDENTIFIED BY "
	}
	if _, err := conn.ExecContext(ctx, stmt+password); err != nil {
		// A server error may quote the statement: keep the password out.
		msg := strings.ReplaceAll(strings.ReplaceAll(err.Error(), password, "'***'"), p.Password, "***")
		return fmt.Errorf("buat user replikasi %s: %s", account, msg)
	}
	if _, err := conn.ExecContext(ctx, "GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO "+account); err != nil {
		return fmt.Errorf("beri hak user replikasi %s: %w", account, err)
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
