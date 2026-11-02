package mysql

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const (
	// healthTimeout bounds all of one Snapshot's MySQL work.
	healthTimeout = 5 * time.Second
	// monitorIdleTime closes the monitor's connection once the agent stops
	// taking readings.
	monitorIdleTime = time.Minute
	// notConnected is MySQLError while there is no MySQL or no stored
	// admin account to watch it with.
	notConnected = "belum terhubung"
)

// Snapshot reads one HealthSnapshot with the stored admin account, reusing
// one connection across calls (dropped and reopened after a failure). A
// MySQL that does not answer within 5 s yields MySQLOK=false with the error
// text, not an error. ErrNoHost / no stored credential: MySQLError = "belum terhubung".
func (d *Driver) Snapshot(ctx context.Context) proto.HealthSnapshot {
	h, err := d.host()
	var s proto.HealthSnapshot
	if err != nil {
		s.MySQLError = notConnected
	} else {
		s = d.mysqlHealth(ctx, h)
	}
	s.AgentVersion = version.Version
	if d.Sys != nil {
		datadir := ""
		if h != nil {
			datadir = h.Layout().DataDir
		}
		s.DiskTotal, s.DiskFree, s.MemTotal, s.MemAvailable, s.Load1 = systemStats(d.Sys, datadir)
	}
	return s
}

// mysqlHealth is the MySQL part of Snapshot: the reading, or MySQLOK=false
// and MySQLError with every other field zero.
func (d *Driver) mysqlHealth(ctx context.Context, h Host) proto.HealthSnapshot {
	d.monMu.Lock()
	defer d.monMu.Unlock()
	var p proto.ConnectParams
	stored := false
	if d.Creds != nil {
		var err error
		if p, stored, err = d.Creds.Load(); err != nil {
			d.dropMonitor()
			return proto.HealthSnapshot{MySQLError: err.Error()}
		}
	}
	if !stored {
		d.dropMonitor()
		return proto.HealthSnapshot{MySQLError: notConnected}
	}

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	db, err := d.monitorDB(ctx, h, p)
	if err == nil {
		var s proto.HealthSnapshot
		if s, err = readHealth(ctx, db); err == nil {
			s.MySQLOK = true
			return s
		}
		d.dropMonitor()
	}
	return proto.HealthSnapshot{MySQLError: healthError(err)}
}

// monitorDB returns the monitor's connection, opening it when there is none
// or when it was opened for another Host or account. The caller holds monMu.
func (d *Driver) monitorDB(ctx context.Context, h Host, p proto.ConnectParams) (*sql.DB, error) {
	if d.monDB != nil && d.monHost == h && d.monCreds == p {
		return d.monDB, nil
	}
	d.dropMonitor()
	db, err := h.Open(ctx)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(monitorIdleTime)
	d.monDB, d.monHost, d.monCreds = db, h, p
	return db, nil
}

// dropMonitor closes the monitor's connection, if any. The caller holds monMu.
func (d *Driver) dropMonitor() {
	if d.monDB != nil {
		d.monDB.Close()
	}
	d.monDB, d.monHost, d.monCreds = nil, nil, proto.ConnectParams{}
}

// healthError is MySQLError for err: the server's own error when it
// answered (a refused login, a missing privilege), else that it did not
// answer.
func healthError(err error) string {
	var me *drv.MySQLError
	if errors.As(err, &me) {
		return err.Error()
	}
	var le *LoginError
	if errors.As(err, &le) {
		err = le.Err
	}
	return "MySQL tidak menjawab: " + err.Error()
}

// readHealth reads the MySQL fields of a HealthSnapshot.
func readHealth(ctx context.Context, db *sql.DB) (proto.HealthSnapshot, error) {
	var s proto.HealthSnapshot
	vars, err := nameValues(ctx, db, `SHOW GLOBAL VARIABLES WHERE Variable_name IN
		('version', 'server_uuid', 'max_connections', 'read_only', 'super_read_only', 'log_bin',
		 'log_bin_basename', 'binlog_expire_logs_seconds', 'gtid_executed')`)
	if err != nil {
		return s, fmt.Errorf("baca variabel MySQL: %w", err)
	}
	s.Version = vars["version"]
	s.ServerUUID = vars["server_uuid"]
	s.MaxConnections, _ = strconv.Atoi(vars["max_connections"])
	s.ReadOnly = on(vars["read_only"])
	s.SuperReadOnly = on(vars["super_read_only"])
	s.LogBin = on(vars["log_bin"])
	s.BinlogExpireSec, _ = strconv.ParseInt(vars["binlog_expire_logs_seconds"], 10, 64)
	s.GTIDExecuted = strings.ReplaceAll(vars["gtid_executed"], "\n", "")

	stat, err := nameValues(ctx, db, `SHOW GLOBAL STATUS WHERE Variable_name IN
		('Uptime', 'Threads_connected', 'Threads_running')`)
	if err != nil {
		return s, fmt.Errorf("baca status MySQL: %w", err)
	}
	s.UptimeSec, _ = strconv.ParseInt(stat["Uptime"], 10, 64)
	s.ThreadsConnected, _ = strconv.Atoi(stat["Threads_connected"])
	s.ThreadsRunning, _ = strconv.Atoi(stat["Threads_running"])

	if s.BinlogDumpThreads, err = binlogDumpThreads(ctx, db); err != nil {
		return s, err
	}
	if s.Replica, err = replicaStatus(ctx, db); err != nil {
		return s, fmt.Errorf("baca status replika: %w", err)
	}
	if s.LogBin {
		if s.BinlogTotalBytes, err = binlogTotalBytes(ctx, db); err != nil {
			return s, fmt.Errorf("baca daftar binlog: %w", err)
		}
		s.BinlogOldestAgeSec = binlogOldestAge(vars["log_bin_basename"], time.Now())
	}
	return s, nil
}

// binlogTotalBytes sums File_size over SHOW BINARY LOGS. The statement's
// columns differ between versions (Encrypted since 8.0.14), so File_size is
// found by name.
func binlogTotalBytes(ctx context.Context, db *sql.DB) (uint64, error) {
	rows, err := db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	size := -1
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i, c := range cols {
		ptrs[i] = &vals[i]
		if strings.EqualFold(c, "File_size") {
			size = i
		}
	}
	if size < 0 {
		return 0, fmt.Errorf("kolom File_size tidak ada (%s)", strings.Join(cols, ", "))
	}
	var total uint64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		n, _ := strconv.ParseUint(string(vals[size]), 10, 64)
		total += n
	}
	return total, rows.Err()
}

// binlogOldestAge is the age in seconds, at now, of the oldest binary log
// file <logBinBasename>.NNNNNN by modification time; nil when the directory
// cannot be read or holds none. It reads the file system directly: the
// probe.System abstraction has no directory listing.
func binlogOldestAge(logBinBasename string, now time.Time) *int64 {
	if logBinBasename == "" {
		return nil
	}
	dir, prefix := filepath.Dir(logBinBasename), filepath.Base(logBinBasename)+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var oldest time.Time
	for _, e := range entries {
		seq, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || seq == "" || strings.Trim(seq, "0123456789") != "" || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	if oldest.IsZero() {
		return nil
	}
	age := int64(now.Sub(oldest) / time.Second)
	return &age
}

// systemStats reads the data directory's file system (Statfs; skipped when
// datadir is ""), /proc/meminfo and /proc/loadavg. What cannot be read is 0.
func systemStats(sys probe.System, datadir string) (diskTotal, diskFree, memTotal, memAvail uint64, load1 float64) {
	if datadir != "" {
		if t, f, err := sys.Statfs(datadir); err == nil {
			diskTotal, diskFree = t, f
		}
	}
	if b, err := sys.ReadFile("/proc/meminfo"); err == nil {
		memTotal, memAvail = meminfoBytes(b, "MemTotal"), meminfoBytes(b, "MemAvailable")
	}
	if b, err := sys.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	return diskTotal, diskFree, memTotal, memAvail, load1
}

// meminfoBytes is the /proc/meminfo entry key ("<key>: <n> kB") in bytes;
// 0 when it is missing or unreadable.
func meminfoBytes(b []byte, key string) uint64 {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
