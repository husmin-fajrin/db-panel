package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
		 'read_only', 'super_read_only', 'datadir', 'socket', 'port', 'bind_address', 'have_ssl', 'tls_version',
		 'lower_case_table_names')`)
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
	st.LowerCaseTableNames, _ = strconv.Atoi(vars["lower_case_table_names"])

	if err := db.QueryRowContext(ctx, "SELECT @@global.gtid_executed").Scan(&st.GTIDExecuted); err != nil {
		return st, fmt.Errorf("baca gtid_executed: %w", err)
	}
	st.GTIDExecuted = strings.ReplaceAll(st.GTIDExecuted, "\n", "")

	stat, err := nameValues(ctx, db, `SHOW GLOBAL STATUS WHERE Variable_name IN ('Uptime', 'Threads_running', 'Current_tls_cert')`)
	if err != nil {
		return st, fmt.Errorf("baca status MySQL: %w", err)
	}
	have, ok := vars["have_ssl"]
	st.TLSAvailable = tlsAvailable(have, ok, vars["tls_version"], stat["Current_tls_cert"])
	st.CloneState, st.CloneError = cloneStatus(ctx, db)
	st.UptimeSec, _ = strconv.ParseInt(stat["Uptime"], 10, 64)
	st.ThreadsRunning, _ = strconv.Atoi(stat["Threads_running"])

	if st.BinlogDumpThreads, err = binlogDumpThreads(ctx, db); err != nil {
		return st, err
	}

	if st.Replica, err = replicaStatus(ctx, db); err != nil {
		return st, fmt.Errorf("baca status replika: %w", err)
	}
	return st, nil
}

func on(v string) bool { return strings.EqualFold(v, "ON") || v == "1" }

// tlsAvailable reports whether the server takes TLS connections: have_ssl
// YES where the variable exists (8.0), else a TLS version enabled and a
// certificate loaded (Current_tls_cert, 8.0.16 and later).
func tlsAvailable(haveSSL string, haveSSLKnown bool, tlsVersion, currentCert string) bool {
	if haveSSLKnown {
		return strings.EqualFold(haveSSL, "YES")
	}
	return strings.TrimSpace(tlsVersion) != "" && strings.TrimSpace(currentCert) != ""
}

// cloneStatus is the last CLONE's state in performance_schema.clone_status
// and its error ("" when none); both "" when the table cannot be read (no
// clone plugin) or holds no row.
func cloneStatus(ctx context.Context, db *sql.DB) (state, errText string) {
	var errNo int
	var errMsg string
	if err := db.QueryRowContext(ctx, "SELECT STATE, ERROR_NO, ERROR_MESSAGE FROM performance_schema.clone_status").
		Scan(&state, &errNo, &errMsg); err != nil {
		return "", ""
	}
	if errNo != 0 {
		errText = fmt.Sprintf("%d %s", errNo, errMsg)
	}
	return state, errText
}

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
		// SEC-63c: the panel marks a replica verified only on this and a
		// connected receiver.
		SSLVerifyServerCert: strings.EqualFold(row["Source_SSL_Verify_Server_Cert"].String, "Yes"),
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

// binlogDumpHosts lists the hosts (without the port) of the replicas
// reading this server's binary log, each once.
func binlogDumpHosts(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT HOST FROM information_schema.PROCESSLIST
		WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')`)
	if err != nil {
		return nil, fmt.Errorf("baca daftar proses: %w", err)
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var h sql.NullString
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("baca daftar proses: %w", err)
		}
		if host := hostWithoutPort(h.String); host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baca daftar proses: %w", err)
	}
	return hosts, nil
}

// replicaPorts lists the ports (1..65535, each once, sorted) the replicas
// of this server reported (SHOW REPLICAS, column Port).
func replicaPorts(ctx context.Context, db *sql.DB) ([]int, error) {
	rows, err := db.QueryContext(ctx, "SHOW REPLICAS")
	if err != nil {
		return nil, fmt.Errorf("SHOW REPLICAS: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("SHOW REPLICAS: %w", err)
	}
	at := slices.IndexFunc(cols, func(c string) bool { return strings.EqualFold(c, "Port") })
	if at < 0 {
		return nil, errors.New("SHOW REPLICAS: no Port column")
	}
	var ports []int
	vals := make([]sql.NullString, len(cols))
	dest := make([]any, len(cols))
	for i := range vals {
		dest[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("SHOW REPLICAS: %w", err)
		}
		if p, err := strconv.Atoi(vals[at].String); err == nil && validSeenPort(p) && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SHOW REPLICAS: %w", err)
	}
	slices.Sort(ports)
	return ports, nil
}

// hostWithoutPort is a processlist HOST ("host:port", an IPv6 address
// without brackets too) without its port; a host without one ("localhost"
// over the socket) is returned as is.
func hostWithoutPort(h string) string {
	i := strings.LastIndexByte(h, ':')
	if i < 0 {
		return h
	}
	if port := h[i+1:]; port == "" || strings.Trim(port, "0123456789") != "" {
		return h
	}
	return h[:i]
}

// binlogDumpThreads counts the replicas reading this server's binary log.
func binlogDumpThreads(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.processlist
		WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')`).Scan(&n); err != nil {
		return 0, fmt.Errorf("baca daftar proses: %w", err)
	}
	return n, nil
}
