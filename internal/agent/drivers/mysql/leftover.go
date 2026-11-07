package mysql

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.leftover (spec 5.4, 5.12), run on a quarantined old master: the
// transactions it executed that its new master lacks
// (GTID_SUBTRACT(@@global.gtid_executed, <new master's>), computed by this
// server's MySQL), and what they touched, read from its binary log with
// mysqlbinlog: tables and row counts, never row data.
//
// The binary log files are chosen from SHOW BINARY LOGS and each file's
// Previous_gtids, read with SHOW BINLOG EVENTS IN '<file>' LIMIT 2 (the
// second event of a file; cheaper than starting mysqlbinlog per file):
// from the last file whose Previous_gtids holds none of the set, up to the
// first one after which Previous_gtids holds all of it (binlogFilesFor).

const (
	// leftoverTimeout bounds one mysqlbinlog run of mysql.leftover.
	leftoverTimeout = 120 * time.Second
	// leftoverQueryTimeout bounds the statements around it.
	leftoverQueryTimeout = 30 * time.Second
)

func (d *Driver) registerLeftover(reg *commands.Registry) {
	reg.Register("mysql.leftover", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.LeftoverParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePeer(&p.NewMaster, "master baru:"); err != nil {
			return nil, err
		}
		if d.pins == nil {
			return nil, &commands.ParamError{Msg: "agent ini tidak punya pin peran; perintah mysql.leftover ditolak"}
		}
		if err := d.allow("mysql.leftover", p); err != nil {
			return nil, err
		}
		dial, err := d.pinnedNewMaster(p.NewMaster, "mysql.leftover")
		if err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		res, err := leftover(ctx, h, dial)
		if err != nil {
			return nil, err
		}
		// What mysql.salvage may extract from now on.
		d.recordLeftover(res.GTIDSet)
		tables := make([]string, 0, len(res.Tables))
		for _, t := range res.Tables {
			tables = append(tables, t.Schema+"."+t.Table)
		}
		d.log("mysql leftover read", "new_source", peerKey(p.NewMaster), "transactions", res.Transactions,
			"tables", strings.Join(tables, ","), "binlogs", strings.Join(res.Binlogs, ","))
		return res, nil
	})
}

// pinnedNewMaster is nm as the agent reads it: when a source is pinned,
// nm must be it and is read at its verified address (Pins.SourceDial).
func (d *Driver) pinnedNewMaster(nm proto.PeerCred, cmd string) (proto.PeerCred, error) {
	host, port, dial, ok, err := d.pins.SourceDial()
	if err != nil {
		return nm, &commands.ParamError{Msg: fmt.Sprintf("pin sumber tidak bisa dibaca, perintah %s ditolak: %v", cmd, err)}
	}
	if !ok {
		return nm, nil
	}
	if !strings.EqualFold(host, nm.Host) || port != nm.Port {
		return nm, sourceRefusal(peerKey(proto.PeerCred{Host: host, Port: port}), peerKey(nm), cmd)
	}
	return dialAt(nm, dial), nil
}

// leftover reads nm's gtid_executed, subtracts it from this server's and
// reads what the rest touched from the binary log.
func leftover(ctx context.Context, h Host, nm proto.PeerCred) (proto.LeftoverResult, error) {
	var res proto.LeftoverResult
	st := readPeer(ctx, nm, peerTimeout)
	if !st.Reachable {
		return res, &commands.ParamError{Msg: fmt.Sprintf("master baru %s tidak bisa dibaca (%s)", peerKey(nm), st.Err)}
	}
	if _, err := parseGTIDSet(st.GTIDExecuted); err != nil {
		return res, fmt.Errorf("gtid_executed master baru %s: %w", peerKey(nm), err)
	}
	qctx, cancel := context.WithTimeout(ctx, leftoverQueryTimeout)
	defer cancel()
	db, err := openAdmin(qctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	var set string
	if err := db.QueryRowContext(qctx, "SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", st.GTIDExecuted).Scan(&set); err != nil {
		return res, fmt.Errorf("hitung transaksi sisa: %w", err)
	}
	set = strings.Join(strings.Fields(set), "")
	g, err := parseGTIDSet(set)
	if err != nil {
		return res, err
	}
	if g.count() == 0 {
		return res, nil
	}
	res.GTIDSet = g.String()
	paths, names, err := binlogFilesFor(qctx, db, res.GTIDSet)
	if err != nil {
		return res, err
	}
	cancel()
	bin, err := findClientTool(h.Layout().MysqldPath, "mysqlbinlog")
	if err != nil {
		return res, err
	}
	sc := newLeftoverScan(g)
	args := append([]string{"--no-defaults", "--base64-output=DECODE-ROWS", "--verbose", "--include-gtids=" + res.GTIDSet}, paths...)
	if err := streamBinlog(ctx, h, bin, args, leftoverTimeout, sc.line); err != nil {
		return res, err
	}
	if err := sc.result(&res); err != nil {
		return proto.LeftoverResult{}, err
	}
	res.Binlogs = names
	return res, nil
}

// binlogNameRe is a binary log file name as SHOW BINARY LOGS lists it.
var binlogNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// binlogFilesFor are the binary log files that hold the transactions of
// set (a set MySQL printed), as paths and as names: from the last file
// whose Previous_gtids holds none of set, to the first after which
// Previous_gtids holds all of it. A set part of which is in no file left
// (purged) is an error.
func binlogFilesFor(ctx context.Context, db *sql.DB, set string) (paths, names []string, err error) {
	var base sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@global.log_bin_basename").Scan(&base); err != nil {
		return nil, nil, fmt.Errorf("baca log_bin_basename: %w", err)
	}
	if !base.Valid || base.String == "" {
		return nil, nil, errors.New("binary log MySQL ini mati; transaksi tidak bisa dibaca")
	}
	files, err := binaryLogs(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, errors.New("SHOW BINARY LOGS tidak menampilkan berkas apa pun")
	}
	prev := make([]string, len(files))
	for i, f := range files {
		if prev[i], err = previousGTIDs(ctx, db, f); err != nil {
			return nil, nil, err
		}
	}
	want, err := gtidCount(set)
	if err != nil {
		return nil, nil, err
	}
	// disjoint: prev holds none of set; covers: prev holds all of it.
	disjoint := func(prev string) (bool, error) {
		var rest string
		if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, ?)", set, prev).Scan(&rest); err != nil {
			return false, fmt.Errorf("bandingkan set GTID: %w", err)
		}
		n, err := gtidCount(rest)
		return n == want, err
	}
	covers := func(prev string) (bool, error) {
		var sub bool
		if err := db.QueryRowContext(ctx, "SELECT GTID_SUBSET(?, ?)", set, prev).Scan(&sub); err != nil {
			return false, fmt.Errorf("bandingkan set GTID: %w", err)
		}
		return sub, nil
	}
	first := -1
	for i := len(files) - 1; i >= 0; i-- {
		ok, err := disjoint(prev[i])
		if err != nil {
			return nil, nil, err
		}
		if ok {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, nil, fmt.Errorf("sebagian transaksi %s sudah tidak ada di binary log server ini (dipurge); berkas tertua %s", set, files[0])
	}
	last := len(files) - 1
	for i := first + 1; i < len(files); i++ {
		ok, err := covers(prev[i])
		if err != nil {
			return nil, nil, err
		}
		if ok {
			last = i - 1
			break
		}
	}
	dir := filepath.Dir(base.String)
	for _, f := range files[first : last+1] {
		paths = append(paths, filepath.Join(dir, f))
		names = append(names, f)
	}
	return paths, names, nil
}

// binaryLogs is the Log_name column of SHOW BINARY LOGS.
func binaryLogs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, fmt.Errorf("SHOW BINARY LOGS: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var files []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		name := string(vals[0])
		if !binlogNameRe.MatchString(name) {
			return nil, fmt.Errorf("nama berkas binlog tidak dikenali: %q", name)
		}
		files = append(files, name)
	}
	return files, rows.Err()
}

// previousGTIDs is the Previous_gtids event of the binary log file name
// (its second event); "" when it has none.
func previousGTIDs(ctx context.Context, db *sql.DB, name string) (string, error) {
	rows, err := db.QueryContext(ctx, "SHOW BINLOG EVENTS IN '"+name+"' LIMIT 2")
	if err != nil {
		return "", fmt.Errorf("SHOW BINLOG EVENTS IN %s: %w", name, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	typeCol, infoCol := slices.Index(cols, "Event_type"), slices.Index(cols, "Info")
	if typeCol < 0 || infoCol < 0 {
		return "", fmt.Errorf("SHOW BINLOG EVENTS: kolom tidak dikenali %v", cols)
	}
	set := ""
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		if string(vals[typeCol]) == "Previous_gtids" {
			set = strings.Join(strings.Fields(string(vals[infoCol])), "")
		}
	}
	return set, rows.Err()
}

// streamBinlog runs mysqlbinlog (bin) with args within timeout and hands
// its output to fn line by line, each cut at maxBinlogLine; the output is
// never held whole. An error from fn stops the run.
func streamBinlog(ctx context.Context, h Host, bin string, args []string, timeout time.Duration, fn func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := h.Exec(ctx, nil, pw, bin, args...)
		pw.CloseWithError(err)
		done <- err
	}()
	var perr error
	br := bufio.NewReaderSize(pr, 64<<10)
	for perr == nil {
		line, err := readCappedLine(br)
		if line != "" {
			perr = fn(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				perr = err
			}
			break
		}
	}
	if perr != nil {
		cancel()
	}
	pr.CloseWithError(errors.New("pembacaan berhenti"))
	err := <-done
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("mysqlbinlog tidak selesai dalam %v", timeout)
	case perr != nil:
		return perr
	case err != nil:
		return fmt.Errorf("baca binlog: %w", err)
	}
	return nil
}

var (
	// binlogHeaderRe is an event header of mysqlbinlog's output:
	// "#YYMMDD HH:MM:SS server id N ..." (hour padded with a space).
	binlogHeaderRe = regexp.MustCompile(`^#(\d{6})\s+(\d{1,2}):(\d{2}):(\d{2})\s+server id\s`)
	// rowEventPrefixes start the pseudo-SQL --verbose prints for one row.
	rowEventPrefixes = []string{"### INSERT INTO ", "### UPDATE ", "### DELETE FROM "}
	// statementTableRe is a statement-based DDL or DML statement and the
	// table it names.
	statementTableRe = regexp.MustCompile("(?i)^(?:(?:CREATE|ALTER|DROP)\\s+(?:TEMPORARY\\s+)?TABLE\\s+(?:IF\\s+(?:NOT\\s+)?EXISTS\\s+)?" +
		"|(?:INSERT|REPLACE)(?:\\s+(?:LOW_PRIORITY|DELAYED|HIGH_PRIORITY|IGNORE))*\\s+INTO\\s+" +
		"|UPDATE(?:\\s+(?:LOW_PRIORITY|IGNORE))*\\s+" +
		"|DELETE(?:\\s+(?:LOW_PRIORITY|QUICK|IGNORE))*\\s+FROM\\s+)(.*)$")
	useRe = regexp.MustCompile("^use\\s+`((?:[^`]|``)+)`")
)

// leftoverScan reads mysqlbinlog's output (--verbose, rows decoded) of
// the transactions of set: it collects the GTIDs of set its GTID_NEXT
// lines name, the rows of each table and the commit times.
//
// Only the start of a statement is looked at: mysqlbinlog ends every
// statement with the delimiter "/*!*/;", so a line not ending in it opens
// a statement body (inStmt) that lasts up to the line that does, and
// nothing in a body (a multi-line string literal of a statement-based
// INSERT, say) is taken for a statement, a "use", a GTID_NEXT or a table.
// An event header closes a body too, in case a delimiter was missed.
//
// Limit: under STATEMENT or MIXED binlog format, a string literal holding
// a line that ends in "/*!*/;" ends the body early, and the lines after it
// can then be taken for statements: the summary (tables, counts, time
// span) can be wrong, and a fake GTID_NEXT makes the count check fail.
// Row-based logging, which the panel sets, prints row data only as "###"
// lines and is unaffected.
type leftoverScan struct {
	set      gtidSet
	seen     gtidSet // the GTIDs of set read
	inTx     bool
	inStmt   bool
	db       string     // the current default database ("use")
	header   *time.Time // time of the last event header
	from, to *time.Time
	tables   map[[2]string]int
}

// binlogDelimiter ends each statement in mysqlbinlog's output.
const binlogDelimiter = "/*!*/;"

func newLeftoverScan(set gtidSet) *leftoverScan {
	return &leftoverScan{set: set, seen: gtidSet{}, tables: map[[2]string]int{}}
}

func (s *leftoverScan) line(l string) error {
	if m := binlogHeaderRe.FindStringSubmatch(l); m != nil {
		s.inStmt = false
		hour := m[2]
		if len(hour) == 1 {
			hour = "0" + hour
		}
		// mysqlbinlog prints local time, the agent's (same TZ).
		if t, err := time.ParseInLocation("060102 15:04:05", m[1]+" "+hour+":"+m[3]+":"+m[4], time.Local); err == nil {
			s.header = &t
		}
		return nil
	}
	ended := strings.HasSuffix(strings.TrimRight(l, " \t"), binlogDelimiter)
	if s.inStmt {
		s.inStmt = !ended
		return nil
	}
	switch {
	case strings.HasPrefix(l, "#"):
		if strings.HasPrefix(l, "### ") && s.inTx {
			s.rowLine(l)
		}
		return nil
	case strings.HasPrefix(l, "/*!"):
		// A versioned comment of mysqlbinlog's own ("/*!50530 SET ...*/;").
		return nil
	case strings.HasPrefix(l, "SET @@SESSION.GTID_NEXT="):
		_, rest, _ := strings.Cut(l, "'")
		gtid, _, _ := strings.Cut(rest, "'")
		key, n, ok := parseGTID(gtid)
		s.inTx = ok && s.set.has(key, n)
		if s.inTx {
			s.seen.add(key, n, n)
			if t := s.header; t != nil {
				if s.from == nil || t.Before(*s.from) {
					s.from = t
				}
				if s.to == nil || t.After(*s.to) {
					s.to = t
				}
			}
		}
	case strings.HasPrefix(l, "use "):
		if m := useRe.FindStringSubmatch(l); m != nil {
			s.db = strings.ReplaceAll(m[1], "``", "`")
		}
	default:
		if s.inTx {
			if m := statementTableRe.FindStringSubmatch(l); m != nil {
				if schema, table, ok := parseTableName(m[1], s.db); ok {
					s.touch(schema, table, 0)
				}
			}
		}
	}
	s.inStmt = !ended
	return nil
}

// rowLine counts one row of a "### INSERT INTO `db`.`t`" line (or UPDATE,
// DELETE FROM).
func (s *leftoverScan) rowLine(l string) {
	for _, p := range rowEventPrefixes {
		if rest, ok := strings.CutPrefix(l, p); ok {
			if schema, table, ok := parseTableName(rest, s.db); ok {
				s.touch(schema, table, 1)
			}
			return
		}
	}
}

func (s *leftoverScan) touch(schema, table string, rows int) {
	s.tables[[2]string{schema, table}] += rows
}

// result fills res with what was read, the tables sorted by schema and
// name; times are UTC. Every transaction of the set must have been read:
// otherwise (a file left out, a GTID_NEXT not recognised, transactions
// missing from the binary log) it is an error, never a partial count.
func (s *leftoverScan) result(res *proto.LeftoverResult) error {
	if got, want := s.seen.count(), s.set.count(); got != want {
		return fmt.Errorf("binary log memuat %d dari %d transaksi sisa; ringkasan tidak lengkap dan tidak ditampilkan", got, want)
	}
	res.Transactions = int(s.set.count())
	for k, n := range s.tables {
		res.Tables = append(res.Tables, proto.TableTouch{Schema: k[0], Table: k[1], Rows: n})
	}
	slices.SortFunc(res.Tables, func(a, b proto.TableTouch) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Table, b.Table))
	})
	if s.from != nil {
		f, t := s.from.UTC(), s.to.UTC()
		res.From, res.To = &f, &t
	}
	return nil
}

// parseTableName reads the table name that starts s: `db`.`t`, `t`,
// db.t or t (a table without a database is in db). ok is false when
// none can be read, or no database is known.
func parseTableName(s, db string) (schema, table string, ok bool) {
	first, rest, ok := identifier(s)
	if !ok {
		return "", "", false
	}
	if r, dot := strings.CutPrefix(rest, "."); dot {
		second, _, ok := identifier(r)
		if !ok {
			return "", "", false
		}
		return first, second, true
	}
	if db == "" {
		return "", "", false
	}
	return db, first, true
}

// identifier reads one identifier at the start of s, backquoted (“
// inside for a backquote) or bare.
func identifier(s string) (id, rest string, ok bool) {
	if r, quoted := strings.CutPrefix(s, "`"); quoted {
		var b strings.Builder
		for i := 0; i < len(r); i++ {
			if r[i] != '`' {
				b.WriteByte(r[i])
				continue
			}
			if i+1 < len(r) && r[i+1] == '`' {
				b.WriteByte('`')
				i++
				continue
			}
			return b.String(), r[i+1:], b.Len() > 0
		}
		return "", "", false
	}
	end := strings.IndexFunc(s, func(c rune) bool {
		return !(c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c > 0x7f)
	})
	if end < 0 {
		end = len(s)
	}
	if end == 0 {
		return "", "", false
	}
	return s[:end], s[end:], true
}
