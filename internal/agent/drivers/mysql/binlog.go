package mysql

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.binlogTail (spec 5.1): the GTID set this server executed, read
// from MySQL when it answers, else from its binary log files with
// mysqlbinlog. The panel reads it from an old master after an emergency
// promotion, to tell which of its transactions the new master lacks.

const (
	// binlogAliveTimeout bounds asking MySQL for gtid_executed.
	binlogAliveTimeout = 5 * time.Second
	// binlogReadTimeout bounds one mysqlbinlog run.
	binlogReadTimeout = 60 * time.Second
	// maxBinlogLine is the longest mysqlbinlog line kept; the rest of a
	// longer one (row events in base64) is skipped.
	maxBinlogLine = 64 << 10
	// maxIndexSize bounds what is read from a binary log index.
	maxIndexSize = 16 << 20
)

func (d *Driver) registerBinlog(reg *commands.Registry) {
	// Read-only: allowed whatever the role.
	reg.Register("mysql.binlogTail", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		return BinlogTail(ctx, h)
	})
}

// BinlogTail is @@global.gtid_executed when MySQL answers (MySQLRunning);
// otherwise the set the binary log records: the last file's
// Previous-GTIDs plus every GTID in that file. File is the last binary
// log file either way ("" when MySQL answers but its index cannot be
// read). A server that refuses the stored account is read from its files
// too: they are what the binary log holds at that moment.
func BinlogTail(ctx context.Context, h Host) (proto.BinlogTailResult, error) {
	var res proto.BinlogTailResult
	if set, index, ok := liveExecuted(ctx, h); ok {
		res.GTIDExecuted, res.MySQLRunning = set, true
		if index != "" {
			res.File, _ = lastBinlogFile(index, h.Layout().DataDir)
		}
		return res, nil
	}
	index, err := binlogIndexPath(h.Layout())
	if err != nil {
		return res, err
	}
	file, err := lastBinlogFile(index, h.Layout().DataDir)
	if err != nil {
		return res, err
	}
	bin, err := findClientTool(h.Layout().MysqldPath, "mysqlbinlog")
	if err != nil {
		return res, err
	}
	set, err := readBinlogGTIDs(ctx, h, bin, file)
	if err != nil {
		return res, err
	}
	res.GTIDExecuted, res.File = set.String(), file
	return res, nil
}

// liveExecuted asks MySQL for gtid_executed (one line) and the binary log
// index path; ok is false when MySQL does not answer in time.
func liveExecuted(ctx context.Context, h Host) (set, index string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, binlogAliveTimeout)
	defer cancel()
	db, err := h.Open(ctx)
	if err != nil {
		return "", "", false
	}
	defer db.Close()
	var idx sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@global.gtid_executed, @@global.log_bin_index").Scan(&set, &idx); err != nil {
		return "", "", false
	}
	return strings.ReplaceAll(set, "\n", ""), idx.String, true
}

// binlogIndexPath finds the binary log index from the option files
// (log_bin_index, else log_bin's base name plus .index, relative to the
// data directory), else binlog.index in the data directory (MySQL 8's
// default), else the only *.index there that is not a relay log's.
func binlogIndexPath(l Layout) (string, error) {
	opts := map[string]string{}
	files := slices.Clone(l.ConfigFiles)
	if l.IncludeDir != "" {
		inc, _ := filepath.Glob(filepath.Join(l.IncludeDir, "*.cnf"))
		slices.Sort(inc)
		files = append(files, inc...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for k, v := range parseOptions(string(b)) {
			opts[k] = strings.Trim(v, `"'`)
		}
	}
	inData := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(l.DataDir, p)
	}
	if v := opts["skip_log_bin"]; v != "" || opts["disable_log_bin"] != "" {
		return "", errors.New("binary log dimatikan di konfigurasi MySQL (skip-log-bin); tidak ada binlog untuk dibaca")
	}
	if v := opts["log_bin_index"]; v != "" {
		return inData(v), nil
	}
	if v := opts["log_bin"]; v != "" && !strings.EqualFold(v, "ON") && v != "1" {
		return inData(strings.TrimSuffix(v, filepath.Ext(v)) + ".index"), nil
	}
	if l.DataDir == "" {
		return "", errors.New("direktori data MySQL tidak diketahui; binlog tidak bisa dicari")
	}
	def := filepath.Join(l.DataDir, "binlog.index")
	if _, err := os.Stat(def); err == nil {
		return def, nil
	}
	all, _ := filepath.Glob(filepath.Join(l.DataDir, "*.index"))
	var found []string
	for _, p := range all {
		if !strings.Contains(filepath.Base(p), "relay") {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("indeks binlog tidak ditemukan di %s (%d kandidat)", l.DataDir, len(found))
	}
	return found[0], nil
}

// lastBinlogFile is the last file listed in the index, relative names
// taken from dataDir (where mysqld runs).
func lastBinlogFile(index, dataDir string) (string, error) {
	f, err := os.Open(index)
	if err != nil {
		return "", fmt.Errorf("baca indeks binlog: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxIndexSize+1))
	if err != nil {
		return "", fmt.Errorf("baca indeks binlog %s: %w", index, err)
	}
	if len(b) > maxIndexSize {
		return "", fmt.Errorf("indeks binlog %s terlalu besar", index)
	}
	last := ""
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			last = ln
		}
	}
	if last == "" {
		return "", fmt.Errorf("indeks binlog %s kosong", index)
	}
	if !filepath.IsAbs(last) {
		last = filepath.Join(dataDir, last)
	}
	return filepath.Clean(last), nil
}

// readBinlogGTIDs runs mysqlbinlog --no-defaults on file and collects its
// Previous-GTIDs and every GTID_NEXT, reading the output line by line.
func readBinlogGTIDs(ctx context.Context, h Host, bin, file string) (gtidSet, error) {
	ctx, cancel := context.WithTimeout(ctx, binlogReadTimeout)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := h.Exec(ctx, nil, pw, bin, "--no-defaults", file)
		pw.CloseWithError(err)
		done <- err
	}()
	set, perr := parseBinlogText(pr)
	if perr != nil {
		cancel()
	}
	pr.CloseWithError(errors.New("pembacaan berhenti"))
	err := <-done
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("mysqlbinlog %s tidak selesai dalam %v", file, binlogReadTimeout)
	case perr != nil:
		return nil, perr
	case err != nil:
		return nil, fmt.Errorf("baca binlog %s: %w", file, err)
	}
	return set, nil
}

// parseBinlogText collects the GTIDs of mysqlbinlog's text output: the
// set printed after the Previous-GTIDs event header ("# <set>", a line
// ending in a comma continued on the next, "# [empty]" for none) and the GTID
// of every "SET @@SESSION.GTID_NEXT= '<uuid>:<n>'" line.
func parseBinlogText(r io.Reader) (gtidSet, error) {
	set := gtidSet{}
	br := bufio.NewReaderSize(r, 64<<10)
	inPrevious, continued := false, false
	for {
		line, err := readCappedLine(br)
		if line != "" {
			switch {
			case inPrevious:
				// The set's first line follows "# "; MySQL prints the
				// lines after a comma without it.
				body, isComment := strings.CutPrefix(line, "#")
				body = strings.TrimSpace(body)
				if (!isComment && !continued) || strings.HasPrefix(body, "at ") {
					inPrevious = false
					break
				}
				if body != "[empty]" {
					prev, perr := parseGTIDSet(body)
					if perr != nil {
						return nil, fmt.Errorf("Previous-GTIDs binlog: %w", perr)
					}
					set.addSet(prev)
				}
				continued = strings.HasSuffix(body, ",")
				inPrevious = continued
			case strings.HasPrefix(line, "#") && strings.HasSuffix(strings.TrimSpace(line), "Previous-GTIDs"):
				inPrevious, continued = true, false
			case strings.HasPrefix(line, "SET @@SESSION.GTID_NEXT="):
				_, rest, _ := strings.Cut(line, "'")
				gtid, _, _ := strings.Cut(rest, "'")
				if key, n, ok := parseGTID(gtid); ok {
					set.add(key, n, n)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return set, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// readCappedLine reads one line without its newline, keeping at most
// maxBinlogLine bytes of it.
func readCappedLine(br *bufio.Reader) (string, error) {
	var b []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(b) < maxBinlogLine {
			b = append(b, chunk[:min(len(chunk), maxBinlogLine-len(b))]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return strings.TrimRight(string(b), "\r\n"), err
	}
}
