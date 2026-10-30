package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The snapshot jobs copy the master's data to this server: CLONE (clone.go)
// or mysqldump piped into mysql (dump.go). This file holds what they share.

// sourceHostRe is the master's address as given to CLONE or the MySQL
// clients: an IPv4/IPv6 literal or a host name. It admits no quote,
// backslash, space or comma.
var sourceHostRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)

// systemDatabases are never dumped, dropped or loaded one by one.
var systemDatabases = map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}

// errReplicaRunning refuses a snapshot while replication would write to
// the same data.
var errReplicaRunning = errors.New("replikasi masih berjalan; hentikan dulu")

// snapshotFunc runs one snapshot job against h.
type snapshotFunc func(j *jobs.Job, h Host, p proto.SnapshotParams) (proto.SnapshotResult, error)

func (d *Driver) registerSnapshot(reg *commands.Registry) {
	d.registerSnapshotJob(reg, "mysql.cloneFrom", methodClone, CloneFrom)
	d.registerSnapshotJob(reg, "mysql.dumpFrom", methodDump, func(j *jobs.Job, h Host, p proto.SnapshotParams) (proto.SnapshotResult, error) {
		return DumpFrom(j, h, d.Creds, p)
	})
}

// registerSnapshotJob registers cmd: it validates the parameters and starts
// run as a background job, answering with the job id.
func (d *Driver) registerSnapshotJob(reg *commands.Registry, cmd, method string, run snapshotFunc) {
	reg.Register(cmd, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.SnapshotParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validateSnapshot(&p, method); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if d.Jobs == nil {
			return nil, errors.New("agent tidak bisa menjalankan pekerjaan latar")
		}
		source := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
		id, err := d.Jobs.Start(cmd, func(j *jobs.Job) (any, error) {
			res, err := run(j, h, p)
			if d.Logger != nil {
				if err != nil {
					d.Logger.Warn("mysql snapshot failed", "method", method, "job", j.ID(), "source", source, "err", err)
				} else {
					d.Logger.Info("mysql snapshot done", "method", method, "job", j.ID(), "source", source,
						"bytes", res.Bytes, "took_ms", res.TookMs)
				}
			}
			if err != nil {
				return nil, err
			}
			return res, nil
		})
		if err != nil {
			return nil, err
		}
		if d.Logger != nil {
			d.Logger.Info("mysql snapshot started", "method", method, "job", id, "source", source, "user", p.User,
				"databases", p.Databases, "drop_existing", p.DropExisting)
		}
		return proto.StartedJob{JobID: id}, nil
	})
}

// validateSnapshot checks p for method (clone or dump). Nothing in p
// reaches SQL or a command line without passing here; the password is
// never part of an error.
func validateSnapshot(p *proto.SnapshotParams, method string) error {
	p.Host, p.User = strings.TrimSpace(p.Host), strings.TrimSpace(p.User)
	bad := func(format string, args ...any) error { return &commands.ParamError{Msg: fmt.Sprintf(format, args...)} }
	switch {
	case !sourceHostRe.MatchString(p.Host):
		return bad("alamat master tidak valid: %q", p.Host)
	case strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil:
		return bad("alamat master tidak valid: %q", p.Host)
	case method == methodClone && strings.Contains(p.Host, ":"):
		// clone_valid_donor_list and CLONE INSTANCE take host:port only.
		return bad("CLONE tidak menerima alamat IPv6 (%s); pakai alamat IPv4 atau nama host", p.Host)
	case p.Port < 1 || p.Port > 65535:
		return bad("port master harus antara 1 dan 65535")
	case !userNameRe.MatchString(p.User):
		return bad("nama user master tidak valid: %q", p.User)
	case p.Password == "":
		return bad("password user master wajib diisi")
	case strings.ContainsFunc(p.Password, unicode.IsControl):
		return bad("password user master tidak boleh berisi karakter kontrol")
	case method == methodClone && len(p.Databases) > 0:
		return bad("CLONE menyalin seluruh server; untuk sebagian database pakai dump")
	}
	seen := map[string]bool{}
	for _, db := range p.Databases {
		switch {
		case !dbNameRe.MatchString(db):
			return bad("nama database tidak valid: %q", db)
		case systemDatabases[db]:
			return bad("database sistem %s tidak bisa disalin sendiri", db)
		case seen[db]:
			return bad("database %s disebut dua kali", db)
		}
		seen[db] = true
	}
	return nil
}

// refuseRunningReplica returns this server's replication state, or
// errReplicaRunning while its IO or SQL thread runs. The state is nil when
// no replication source is configured.
func refuseRunningReplica(ctx context.Context, db *sql.DB) (*proto.ReplicaStatus, error) {
	rs, err := replicaStatus(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("baca status replika: %w", err)
	}
	if rs != nil && (rs.IORunning == "Yes" || rs.IORunning == "Connecting" || rs.SQLRunning == "Yes") {
		return rs, errReplicaRunning
	}
	return rs, nil
}

// localGTIDExecuted is @@GLOBAL.gtid_executed on one line.
func localGTIDExecuted(ctx context.Context, db *sql.DB) (string, error) {
	var g string
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_executed").Scan(&g); err != nil {
		return "", fmt.Errorf("baca gtid_executed: %w", err)
	}
	return strings.ReplaceAll(g, "\n", ""), nil
}

// megabytes formats b for progress messages.
func megabytes(b uint64) string { return fmt.Sprintf("%.1f MB", float64(b)/(1<<20)) }

// percentOf is done as a percentage of total, held at 99 until the job ends;
// 0 when total is unknown.
func percentOf(done, total uint64) int {
	if total == 0 {
		return 0
	}
	return int(min(done*100/total, 99))
}
