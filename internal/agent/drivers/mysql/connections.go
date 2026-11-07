package mysql

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"slices"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.connections (spec 5.4): who is connected to this server, before
// a promotion moves the writes, grouped by client host, user and default
// database. Read-only: allowed whatever the role.

// connectionsTimeout bounds one reading.
const connectionsTimeout = 10 * time.Second

// agentUserExpr is the user the agent logs in as: USER() without its
// last @host, the name processlist shows.
const agentUserExpr = `LEFT(USER(), CHAR_LENGTH(USER()) - CHAR_LENGTH(SUBSTRING_INDEX(USER(), '@', -1)) - 1)`

// connectionsQuery lists the client threads: foreground threads serving
// a client connection (NAME thread/sql/one_connection; a replica's
// receiver, applier and workers, the event scheduler and MySQL's other
// threads have other names) with a user, but the panel's accounts
// (dbpanel_*), the event scheduler, MySQL's 'system user', replicas
// reading the binary log, this very connection, and the sessions of the
// user the agent itself logs in as that come from this server itself
// (socket, localhost, 127.0.0.1 or ::1: the agent's monitoring and
// command sessions, and anyone logged in on the server). That account's
// sessions from any other host are listed, marked as the panel's admin
// account (the last column): people or applications that share it still
// write to this server.
const connectionsQuery = `SELECT IFNULL(PROCESSLIST_HOST, ''), PROCESSLIST_USER, IFNULL(PROCESSLIST_DB, ''),
		PROCESSLIST_USER = ` + agentUserExpr + `
	FROM performance_schema.threads
	WHERE TYPE = 'FOREGROUND'
	  AND NAME = 'thread/sql/one_connection'
	  AND PROCESSLIST_USER IS NOT NULL
	  AND PROCESSLIST_USER <> 'system user'
	  AND PROCESSLIST_USER NOT LIKE 'dbpanel\_%'
	  AND PROCESSLIST_USER <> 'event_scheduler'
	  AND NOT (PROCESSLIST_USER = ` + agentUserExpr + `
	           AND IFNULL(PROCESSLIST_HOST, '') IN ('', 'localhost', '127.0.0.1', '::1', '::ffff:127.0.0.1'))
	  AND IFNULL(PROCESSLIST_COMMAND, '') NOT LIKE 'Binlog Dump%'
	  AND IFNULL(PROCESSLIST_ID, 0) <> CONNECTION_ID()`

func (d *Driver) registerConnections(reg *commands.Registry) {
	reg.Register("mysql.connections", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, connectionsTimeout)
		defer cancel()
		db, err := openAdmin(ctx, h)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		rows, err := readConnections(ctx, db)
		if err != nil {
			return nil, err
		}
		return proto.ConnectionsResult{Rows: rows, TakenAt: d.now()}, nil
	})
}

// readConnections groups the client threads by host (without a port),
// user and database: most connections first, then by host, user and
// database.
func readConnections(ctx context.Context, db *sql.DB) ([]proto.ConnectionRow, error) {
	rs, err := db.QueryContext(ctx, connectionsQuery)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	type key struct {
		host, user, db string
		admin          bool
	}
	counts := map[key]int{}
	for rs.Next() {
		var k key
		if err := rs.Scan(&k.host, &k.user, &k.db, &k.admin); err != nil {
			return nil, err
		}
		k.host = clientHost(k.host)
		counts[k]++
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	out := make([]proto.ConnectionRow, 0, len(counts))
	for k, n := range counts {
		out = append(out, proto.ConnectionRow{Host: k.host, User: k.user, DB: k.db, Count: n, AdminAccount: k.admin})
	}
	slices.SortFunc(out, func(a, b proto.ConnectionRow) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Host, b.Host), cmp.Compare(a.User, b.User), cmp.Compare(a.DB, b.DB))
	})
	return out, nil
}

// clientHost is a client's host without a port. performance_schema
// gives none, but processlist-style "host:port" is cut; an IPv6 address
// is kept whole.
func clientHost(h string) string {
	if net.ParseIP(h) != nil {
		return h
	}
	return hostWithoutPort(h)
}
