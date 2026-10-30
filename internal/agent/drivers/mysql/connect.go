package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Privileges the admin account needs to set up replication, as reported by
// SHOW GRANTS. The panel creates the replication user, so the account must
// also hold GRANT OPTION and every privilege it hands out.
var (
	staticGrants = []string{
		"CREATE USER", "RELOAD", "PROCESS", "SELECT", "LOCK TABLES", "SHOW VIEW", "TRIGGER", "EVENT",
		"REPLICATION CLIENT", "REPLICATION SLAVE",
	}
	// dynamicGrants are satisfied by SUPER as well.
	dynamicGrants = []string{
		"SYSTEM_VARIABLES_ADMIN", "REPLICATION_SLAVE_ADMIN", "BACKUP_ADMIN", "CLONE_ADMIN", "PERSIST_RO_VARIABLES_ADMIN",
	}
)

const grantOption = "GRANT OPTION"

// RequiredGrants lists the global privileges checked by mysql.connect:
// the static ones, the dynamic ones (SUPER can stand in for them), and
// GRANT OPTION. ALL PRIVILEGES ON *.* with GRANT OPTION covers everything.
func RequiredGrants() []string {
	out := append([]string{}, staticGrants...)
	out = append(out, dynamicGrants...)
	return append(out, grantOption)
}

func (d *Driver) registerConnect(reg *commands.Registry) {
	reg.Register("mysql.connect", func(ctx context.Context, params json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		var p proto.ConnectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := normalizeParams(&p); err != nil {
			return nil, err
		}
		if d.Creds == nil {
			return nil, errors.New("agent tidak punya tempat menyimpan kredensial MySQL")
		}

		// Try the new account without touching the stored one.
		db, err := openWith(ctx, h.Layout().Socket, p)
		if err != nil {
			return nil, &commands.ParamError{Msg: loginFailure(err)}
		}
		defer db.Close()
		res, err := inventory(ctx, db)
		if err != nil {
			return nil, err
		}
		if err := d.Creds.Save(p); err != nil {
			return nil, fmt.Errorf("simpan kredensial MySQL: %w", err)
		}
		if d.Logger != nil {
			d.Logger.Info("mysql connected", "account", res.Account, "version", res.Status.Version,
				"databases", len(res.Databases), "missing_grants", len(res.MissingGrants))
		}
		return res, nil
	})

	reg.Register("mysql.inventory", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		// Open falls back to root over the socket without a stored account;
		// inventory is only for a connected server.
		if d.Creds != nil {
			if _, ok, err := d.Creds.Load(); err != nil {
				return nil, fmt.Errorf("baca kredensial MySQL: %w", err)
			} else if ok {
				db, err := h.Open(ctx)
				if err != nil {
					return nil, err
				}
				defer db.Close()
				return inventory(ctx, db)
			}
		}
		return nil, &commands.ParamError{Msg: "MySQL belum dihubungkan"}
	})
}

// normalizeParams validates p and fills the default host.
func normalizeParams(p *proto.ConnectParams) error {
	p.User, p.Host = strings.TrimSpace(p.User), strings.TrimSpace(p.Host)
	if p.User == "" {
		return &commands.ParamError{Msg: "user MySQL wajib diisi"}
	}
	if p.Port < 0 || p.Port > 65535 {
		return &commands.ParamError{Msg: "port MySQL harus antara 0 dan 65535"}
	}
	if p.Host == "" {
		p.Host = "localhost"
	}
	return nil
}

// loginFailure words a failed login for the user: the server's error number
// and message when it answered, the connection error otherwise.
func loginFailure(err error) string {
	var me *drv.MySQLError
	if errors.As(err, &me) {
		return fmt.Sprintf("login MySQL gagal: %d %s", me.Number, me.Message)
	}
	return "login MySQL gagal: " + err.Error()
}

// isPrivilegeError reports whether err is the server refusing an operation
// for lack of privileges.
func isPrivilegeError(err error) bool {
	var me *drv.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	switch me.Number {
	case 1044, 1045, 1142, 1143, 1227: // db denied, access denied, table/column denied, need privilege
		return true
	}
	return false
}

// inventory reads everything ConnectResult holds through db.
func inventory(ctx context.Context, db *sql.DB) (proto.ConnectResult, error) {
	var res proto.ConnectResult
	var err error
	// An account that lacks privileges still connects, so the panel can show
	// what is missing; Status then returns what it read before the refusal.
	if res.Status, err = Status(ctx, db); err != nil && !isPrivilegeError(err) {
		return res, err
	}
	if res.Databases, err = listDatabases(ctx, db); err != nil {
		return res, err
	}
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&res.Account); err != nil {
		return res, fmt.Errorf("baca akun MySQL: %w", err)
	}
	grants, err := showGrants(ctx, db)
	if err != nil {
		return res, err
	}
	res.MissingGrants = missingGrants(grants, res.Account)
	return res, nil
}

// listDatabases returns the user databases with their size, number of base
// tables and storage engines, sorted by name. A database without tables is
// listed with zeroes.
func listDatabases(ctx context.Context, db *sql.DB) ([]proto.DatabaseInfo, error) {
	// A dedicated connection so the statistics cache can be bypassed: since
	// 8.0 information_schema serves cached sizes for a day.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar database: %w", err)
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, "SET SESSION information_schema_stats_expiry = 0") // absent before 8.0

	rows, err := conn.QueryContext(ctx, `SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0),
			COUNT(t.table_name), COALESCE(GROUP_CONCAT(DISTINCT t.engine ORDER BY t.engine), '')
		FROM information_schema.schemata s
		LEFT JOIN information_schema.tables t
			ON t.table_schema = s.schema_name AND t.table_type = 'BASE TABLE'
		WHERE s.schema_name NOT IN ('mysql', 'sys', 'information_schema', 'performance_schema')
		GROUP BY s.schema_name
		ORDER BY s.schema_name`)
	if err != nil {
		return nil, fmt.Errorf("daftar database: %w", err)
	}
	defer rows.Close()
	out := []proto.DatabaseInfo{}
	for rows.Next() {
		var d proto.DatabaseInfo
		var engines string
		if err := rows.Scan(&d.Name, &d.Bytes, &d.Tables, &engines); err != nil {
			return nil, fmt.Errorf("daftar database: %w", err)
		}
		if engines != "" {
			d.Engines = strings.Split(engines, ",")
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("daftar database: %w", err)
	}
	return out, nil
}

func showGrants(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		return nil, fmt.Errorf("baca hak akses MySQL: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, fmt.Errorf("baca hak akses MySQL: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baca hak akses MySQL: %w", err)
	}
	return out, nil
}

// globalGrant matches a GRANT statement on all databases, e.g.
// "GRANT SELECT, RELOAD ON *.* TO `u`@`h` WITH GRANT OPTION". Role grants and
// grants on one database or table do not match.
var globalGrant = regexp.MustCompile("(?s)^GRANT (.+?) ON \\*\\.\\* TO .*?( WITH GRANT OPTION)?$")

// missingGrants returns the GRANT statements that would give account what
// RequiredGrants lists and the statements in grants (SHOW GRANTS rows) lack.
func missingGrants(grants []string, account string) []string {
	have := map[string]bool{}
	for _, g := range grants {
		m := globalGrant.FindStringSubmatch(strings.TrimSpace(g))
		if m == nil {
			continue
		}
		for _, priv := range strings.Split(m[1], ",") {
			have[strings.ToUpper(strings.TrimSpace(priv))] = true
		}
		if m[2] != "" {
			have[grantOption] = true
		}
	}
	if (have["ALL PRIVILEGES"] || have["ALL"]) && have[grantOption] {
		return nil
	}
	all := have["ALL PRIVILEGES"] || have["ALL"]

	var static, dynamic []string
	for _, p := range staticGrants {
		if !all && !have[p] {
			static = append(static, p)
		}
	}
	for _, p := range dynamicGrants {
		if !all && !have["SUPER"] && !have[p] {
			dynamic = append(dynamic, p)
		}
	}

	to := " ON *.* TO " + quoteAccount(account)
	var out []string
	if len(static) > 0 {
		out = append(out, "GRANT "+strings.Join(static, ", ")+to)
	}
	if len(dynamic) > 0 {
		out = append(out, "GRANT "+strings.Join(dynamic, ", ")+to)
	}
	if !have[grantOption] {
		out = append(out, "GRANT USAGE"+to+" WITH GRANT OPTION")
	}
	return out
}

// quoteAccount turns CURRENT_USER()'s user@host into 'user'@'host'.
func quoteAccount(account string) string {
	user, host := account, ""
	if i := strings.LastIndex(account, "@"); i >= 0 {
		user, host = account[:i], account[i+1:]
	}
	return quoteString(user) + "@" + quoteString(host)
}

func quoteString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}
