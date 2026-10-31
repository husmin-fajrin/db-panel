package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func (d *Driver) registerDatabases(reg *commands.Registry) {
	reg.Register("mysql.dropDatabases", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.DropDatabasesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateDropDatabases(p.Databases); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		res, err := DropDatabases(ctx, h, p.Databases, func(level, format string, args ...any) {
			d.log(fmt.Sprintf(format, args...), "level", level)
		})
		if err != nil {
			return nil, err
		}
		d.log("mysql databases dropped", "dropped", res.Dropped)
		return res, nil
	})
}

// validateDropDatabases checks the names: valid, not a system database,
// each once, at least one.
func validateDropDatabases(dbs []string) error {
	bad := func(format string, args ...any) error { return &commands.ParamError{Msg: fmt.Sprintf(format, args...)} }
	if len(dbs) == 0 {
		return bad("daftar database kosong")
	}
	seen := map[string]bool{}
	for _, db := range dbs {
		switch {
		case !dbNameRe.MatchString(db):
			return bad("nama database tidak valid: %q", db)
		// Lower-cased: with lower_case_table_names=1, SYS is sys.
		case systemDatabases[strings.ToLower(db)]:
			return bad("database sistem %s tidak boleh dihapus", db)
		case seen[strings.ToLower(db)]:
			return bad("database %s disebut dua kali", db)
		}
		seen[strings.ToLower(db)] = true
	}
	return nil
}

// DropDatabases drops the given databases on a replica that is not
// replicating, without binary logging: the panel uses it to clear what an
// earlier attempt left before a dump loads afresh. A server that replicas
// read from is refused: its replicas would never see the drop. read_only
// and super_read_only are lifted for the drops and restored afterwards, as
// dumpFrom does. The result names the databases that existed (compared
// without case, as lower_case_table_names may fold them).
func DropDatabases(ctx context.Context, h Host, databases []string, logf func(level, format string, args ...any)) (proto.DropDatabasesResult, error) {
	res := proto.DropDatabasesResult{Dropped: []string{}}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	if _, err := refuseRunningReplica(ctx, db); err != nil {
		return res, err
	}
	if err := refuseOnSource(ctx, db); err != nil {
		return res, err
	}
	rows, err := db.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata")
	if err != nil {
		return res, fmt.Errorf("baca daftar database: %w", err)
	}
	var existing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return res, fmt.Errorf("baca daftar database: %w", err)
		}
		existing = append(existing, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("baca daftar database: %w", err)
	}
	restore, err := liftReadOnly(ctx, logf, db)
	if err != nil {
		return res, err
	}
	defer restore()
	if err := dropDatabases(ctx, db, databases); err != nil {
		return res, err
	}
	for _, name := range databases {
		if slices.ContainsFunc(existing, func(e string) bool { return strings.EqualFold(e, name) }) {
			res.Dropped = append(res.Dropped, name)
		}
	}
	return res, nil
}
