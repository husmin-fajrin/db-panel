package mysql

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.connections (spec 5.4, 5.5): who is connected to this server,
// before a promotion moves the writes, grouped by client host, user,
// default database and kind. Read-only: allowed whatever the role. The list
// is honest about what it cannot see: a session it cannot classify is
// counted, never hidden, and a source that cannot be read is reported
// (Unreadable, Incomplete) instead of an empty list.

// connectionsTimeout bounds one reading.
const connectionsTimeout = 10 * time.Second

// agentUserExpr is the user the agent logs in as: USER() without its
// last @host, the name processlist shows.
const agentUserExpr = `LEFT(USER(), CHAR_LENGTH(USER()) - CHAR_LENGTH(SUBSTRING_INDEX(USER(), '@', -1)) - 1)`

// Texts the panel shows as the reason (Indonesian: user-facing).
const (
	incompleteThreadsLost = "sebagian thread tidak tercatat di performance_schema"
	incompleteLostUnread  = "jumlah thread yang tidak tercatat di performance_schema tidak bisa dibaca"
)

// replicationThreadNames are the performance_schema instruments of a
// replica's receiver, applier and workers. START REPLICA gives these threads
// the account of the session that ran it in performance_schema (not 'system
// user'), so they would show as sessions of the agent's account. Instrument
// names are set by the server and a client cannot choose one, so excluding
// these exact names cannot hide an application session. Keep it an exact
// list: no LIKE, no prefix.
var replicationThreadNames = []string{
	"thread/sql/replica_io", "thread/sql/replica_sql", "thread/sql/replica_worker",
	"thread/sql/slave_io", "thread/sql/slave_sql", "thread/sql/slave_worker",
}

// connectionsPSQuery lists the client threads from performance_schema: the
// foreground threads that have a processlist id and a user (this drops
// MySQL's own threads such as compress_gtid_table, which have no user), with
// no filter on the thread name, so a connection of a kind the agent does
// not know is counted. Left out: MySQL's 'system user', the event
// scheduler, replicas reading the binary log and this very connection. On a
// replica, performance_schema shows the replication threads under the
// account that ran START REPLICA, not as 'system user': they are counted
// (as local admin sessions of the agent's account) rather than hidden by
// name (the instruments listed in replicationThreadNames). An
// 'unauthenticated user' (a session logging in) is a connection. Columns:
// thread name, host, user, default database, whether the user is the one the
// agent logs in as, and whether the session carries the agent's
// program_name attribute (the single parameter).
var connectionsPSQuery = `SELECT IFNULL(t.NAME, ''), IFNULL(t.PROCESSLIST_HOST, ''), t.PROCESSLIST_USER, IFNULL(t.PROCESSLIST_DB, ''),
		t.PROCESSLIST_USER = ` + agentUserExpr + `,
		EXISTS (SELECT 1 FROM performance_schema.session_connect_attrs a
			WHERE a.PROCESSLIST_ID = t.PROCESSLIST_ID AND a.ATTR_NAME = 'program_name' AND a.ATTR_VALUE = ?)
	FROM performance_schema.threads t
	WHERE t.TYPE = 'FOREGROUND'
	  AND IFNULL(t.NAME, '') NOT IN (` + placeholders(len(replicationThreadNames)) + `)
	  AND t.PROCESSLIST_ID IS NOT NULL
	  AND t.PROCESSLIST_USER IS NOT NULL
	  AND t.PROCESSLIST_USER NOT IN ('system user', 'event_scheduler')
	  AND IFNULL(t.PROCESSLIST_COMMAND, '') NOT LIKE 'Binlog Dump%'
	  AND t.PROCESSLIST_ID <> CONNECTION_ID()`

// placeholders is n comma-separated "?".
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// connectionsISQuery is the same list from information_schema.PROCESSLIST,
// for a server without performance_schema: no thread name (kind "lain") and
// no attributes (nothing is tagged).
const connectionsISQuery = `SELECT '', IFNULL(HOST, ''), USER, IFNULL(DB, ''), USER = ` + agentUserExpr + `, FALSE
	FROM information_schema.PROCESSLIST
	WHERE USER IS NOT NULL
	  AND USER NOT IN ('system user', 'event_scheduler')
	  AND IFNULL(COMMAND, '') NOT LIKE 'Binlog Dump%'
	  AND ID <> CONNECTION_ID()`

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
		res := readConnections(ctx, db)
		res.TakenAt = d.now()
		return res, nil
	})
}

// readConnections reads the client threads, from performance_schema when it
// is on and from information_schema.PROCESSLIST otherwise (or when the
// first fails). When neither can be read the result says so (Unreadable)
// instead of failing the command.
func readConnections(ctx context.Context, db *sql.DB) proto.ConnectionsResult {
	res := proto.ConnectionsResult{Rows: []proto.ConnectionRow{}, Typed: true}
	var psErr error
	var on bool
	if err := db.QueryRowContext(ctx, `SELECT @@performance_schema = 1`).Scan(&on); err == nil && on {
		var ts []connThread
		if ts, psErr = queryThreads(ctx, db, connectionsPSQuery, psArgs()...); psErr == nil {
			res.Source = "performance_schema"
			res.Rows, res.LocalAdmin = groupConnections(ts, false)
			res.Incomplete = threadsLost(ctx, db)
			return res
		}
	}
	ts, err := queryThreads(ctx, db, connectionsISQuery)
	if err != nil {
		reason := "information_schema.PROCESSLIST tidak bisa dibaca: " + err.Error()
		if psErr != nil {
			reason = "performance_schema tidak bisa dibaca: " + psErr.Error() + "; " + reason
		}
		res.Unreadable = reason
		return res
	}
	res.Source = "information_schema"
	res.AgentIndistinct = true
	res.Rows, res.LocalAdmin = groupConnections(ts, true)
	return res
}

// psArgs are the parameters of connectionsPSQuery in order: the program name
// of the tag, then the excluded instruments.
func psArgs() []any {
	args := []any{agentProgramName}
	for _, n := range replicationThreadNames {
		args = append(args, n)
	}
	return args
}

// queryThreads runs one of the thread queries.
func queryThreads(ctx context.Context, db *sql.DB, query string, args ...any) ([]connThread, error) {
	rs, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var ts []connThread
	for rs.Next() {
		var t connThread
		if err := rs.Scan(&t.Name, &t.Host, &t.User, &t.DB, &t.AgentAcct, &t.Tagged); err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	return ts, rs.Err()
}

// threadsLost is the reason the performance_schema list is incomplete, ""
// when no thread went unrecorded. A counter that cannot be read counts as
// incomplete.
func threadsLost(ctx context.Context, db *sql.DB) string {
	var name, v string
	if err := db.QueryRowContext(ctx, `SHOW GLOBAL STATUS LIKE 'Performance_schema_thread_instances_lost'`).Scan(&name, &v); err != nil {
		return incompleteLostUnread
	}
	n, err := strconv.ParseInt(v, 10, 64)
	switch {
	case err != nil:
		return incompleteLostUnread
	case n > 0:
		return incompleteThreadsLost
	}
	return ""
}

// connThread is one client thread as read from the server. AgentAcct: its
// user is the one the agent logs in as. Tagged: its session carries the
// agent's program_name attribute.
type connThread struct {
	Name, Host, User, DB string
	AgentAcct, Tagged    bool
}

// connKind is the kind of a connection from its thread name: "sql" for a
// classic connection, "x" for an X Protocol worker, "lain" for anything the
// agent does not know.
func connKind(threadName string) string {
	switch threadName {
	case "thread/sql/one_connection":
		return "sql"
	case "thread/mysqlx/worker":
		return "x"
	}
	return "lain"
}

// serviceAccount reports whether user is an account the agent itself creates
// for replication or a dump. These are the only accounts hidden by name.
func serviceAccount(user string) bool {
	return user == replUserName || dumpAccountRe.MatchString(user)
}

// localClientHost reports whether a client host (without port) is this
// server itself: the socket (” or localhost) or loopback.
func localClientHost(h string) bool {
	switch h {
	case "", "localhost", "127.0.0.1", "::1", "::ffff:127.0.0.1":
		return true
	}
	return false
}

// groupConnections groups threads by host (without a port), user, database
// and kind: most connections first, then by host, user, database and kind.
// Only two cases are left out: the agent's service accounts, and, unless
// indistinct (performance_schema is off, tags unreadable), the agent
// account's tagged sessions from this server itself (the agent's own).
// Every other session is counted; localAdmin is how many of them are the
// agent account's sessions from this server itself without the tag.
func groupConnections(ts []connThread, indistinct bool) (rows []proto.ConnectionRow, localAdmin int) {
	counts := map[proto.ConnectionRow]int{}
	for _, t := range ts {
		if serviceAccount(t.User) {
			continue
		}
		r := proto.ConnectionRow{Host: clientHost(t.Host), User: t.User, DB: t.DB, Kind: connKind(t.Name)}
		switch {
		case t.AgentAcct && localClientHost(r.Host):
			if t.Tagged && !indistinct {
				continue
			}
			r.LocalAdmin = true
			localAdmin++
		case t.AgentAcct || t.User == adminUser:
			r.AdminAccount = true
		}
		counts[r]++
	}
	rows = make([]proto.ConnectionRow, 0, len(counts))
	for r, n := range counts {
		r.Count = n
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b proto.ConnectionRow) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Host, b.Host), cmp.Compare(a.User, b.User), cmp.Compare(a.DB, b.DB), cmp.Compare(a.Kind, b.Kind))
	})
	return rows, localAdmin
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
