package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func (d *Driver) registerStatus(reg *commands.Registry) {
	reg.Register("mysql.status", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		db, err := h.Open(ctx)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return Status(ctx, db)
	})
}

// Status reads the server's version, binary log, GTID, read-only and
// replication state.
func Status(ctx context.Context, db *sql.DB) (proto.MySQLStatus, error) {
	var st proto.MySQLStatus
	vars, err := nameValues(ctx, db, `SHOW GLOBAL VARIABLES WHERE Variable_name IN
		('version', 'server_id', 'server_uuid', 'log_bin', 'gtid_mode', 'enforce_gtid_consistency', 'binlog_format',
		 'read_only', 'super_read_only', 'datadir', 'socket', 'port', 'bind_address')`)
	if err != nil {
		return st, fmt.Errorf("baca variabel MySQL: %w", err)
	}
	st.Version = vars["version"]
	if d, err := ParseVersion(st.Version); err == nil {
		st.Series = d.Series
	}
	if id, err := strconv.ParseUint(vars["server_id"], 10, 32); err == nil {
		st.ServerID = uint32(id)
	}
	st.ServerUUID = vars["server_uuid"]
	st.LogBin = on(vars["log_bin"])
	st.GTIDMode = vars["gtid_mode"]
	st.EnforceGTID = on(vars["enforce_gtid_consistency"])
	st.BinlogFormat = vars["binlog_format"]
	st.ReadOnly = on(vars["read_only"])
	st.SuperReadOnly = on(vars["super_read_only"])
	st.DataDir = trimDir(vars["datadir"])
	st.Socket = vars["socket"]
	st.Port, _ = strconv.Atoi(vars["port"])
	st.BindAddress = vars["bind_address"]

	if err := db.QueryRowContext(ctx, "SELECT @@global.gtid_executed").Scan(&st.GTIDExecuted); err != nil {
		return st, fmt.Errorf("baca gtid_executed: %w", err)
	}
	st.GTIDExecuted = strings.ReplaceAll(st.GTIDExecuted, "\n", "")

	stat, err := nameValues(ctx, db, `SHOW GLOBAL STATUS WHERE Variable_name IN ('Uptime', 'Threads_running')`)
	if err != nil {
		return st, fmt.Errorf("baca status MySQL: %w", err)
	}
	st.UptimeSec, _ = strconv.ParseInt(stat["Uptime"], 10, 64)
	st.ThreadsRunning, _ = strconv.Atoi(stat["Threads_running"])

	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.processlist
		WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')`).Scan(&st.BinlogDumpThreads); err != nil {
		return st, fmt.Errorf("baca daftar proses: %w", err)
	}

	if st.Replica, err = replicaStatus(ctx, db); err != nil {
		return st, fmt.Errorf("baca status replika: %w", err)
	}
	return st, nil
}

func on(v string) bool { return strings.EqualFold(v, "ON") || v == "1" }

// nameValues runs a SHOW ... VARIABLES/STATUS query and maps names to values.
func nameValues(ctx context.Context, db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// replicaStatus returns the first row of SHOW REPLICA STATUS, or nil when the
// server has no replication source configured.
func replicaStatus(ctx context.Context, db *sql.DB) (*proto.ReplicaStatus, error) {
	rows, err := db.QueryContext(ctx, "SHOW REPLICA STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, rows.Err()
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make(map[string]sql.NullString, len(cols))
	for i, c := range cols {
		row[c] = vals[i]
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	atoi := func(c string) int { n, _ := strconv.Atoi(row[c].String); return n }
	gtid := func(c string) string { return strings.ReplaceAll(row[c].String, "\n", "") }
	r := &proto.ReplicaStatus{
		IORunning:     row["Replica_IO_Running"].String,
		SQLRunning:    row["Replica_SQL_Running"].String,
		SourceHost:    row["Source_Host"].String,
		SourcePort:    atoi("Source_Port"),
		LastIOError:   row["Last_IO_Error"].String,
		LastSQLError:  row["Last_SQL_Error"].String,
		LastIOErrno:   atoi("Last_IO_Errno"),
		LastSQLErrno:  atoi("Last_SQL_Errno"),
		RetrievedGTID: gtid("Retrieved_Gtid_Set"),
		ExecutedGTID:  gtid("Executed_Gtid_Set"),
		Filters:       []string{},
	}
	if v := row["Seconds_Behind_Source"]; v.Valid {
		if n, err := strconv.ParseInt(v.String, 10, 64); err == nil {
			r.SecondsBehind = &n
		}
	}

	frows, err := db.QueryContext(ctx, `SELECT FILTER_RULE FROM performance_schema.replication_applier_filters
		WHERE FILTER_NAME = 'REPLICATE_WILD_DO_TABLE'`)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	seen := map[string]bool{}
	for frows.Next() {
		var rule string
		if err := frows.Scan(&rule); err != nil {
			return nil, err
		}
		for _, f := range strings.Split(rule, ",") {
			if f = strings.TrimSpace(f); f != "" && !seen[f] {
				seen[f] = true
				r.Filters = append(r.Filters, f)
			}
		}
	}
	return r, frows.Err()
}
