package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.salvage, salvageList, salvageRead and salvageDelete (spec 5.4,
// 5.12), on a quarantined old master: the transactions its new master
// lacks are extracted from its binary log into a SQL file the operator
// downloads through the panel and replays by hand. The file is the only
// place their data lives: logs and events carry its name and size only.
// Files are kept 7 days (SweepSalvage, from the health monitor).
//
// What a salvage may extract is bounded, so that a compromised panel
// cannot read this server's binary log history through it: the set must
// be a subset of the one the last mysql.leftover found (the transactions
// the new master lacks), kept in leftoverSetFile under the salvage
// directory (0600) for the quarantine it was found in (recordLeftover). One salvage runs at a
// time, and the directory holds at most salvageDirMax bytes: a salvage is
// refused before it writes once the directory is full, and stopped (its
// file removed) when it would pass the cap.

// DefaultSalvageDir is where salvage files are written (0700 root) when
// Driver.SalvageDir is empty. A variable only so the tests can point it
// away from the system's directory.
var DefaultSalvageDir = "/var/lib/dbpanel/salvage"

const (
	// SalvageMaxAge is how long a salvage file is kept.
	SalvageMaxAge = 7 * 24 * time.Hour
	// salvageMaxSize is the largest salvage file written; a larger one is
	// abandoned and removed.
	salvageMaxSize = 2 << 30
	// salvageDirMax is the most the salvage directory holds, all files
	// together.
	salvageDirMax = 8 << 30
	// leftoverSetFile, in the salvage directory, keeps the GTID set the
	// last mysql.leftover found; its name is neither a salvage file's nor
	// a temporary one's, so the list and the sweep leave it alone.
	leftoverSetFile = "leftover-gtid"
	// maxLeftoverSetFile bounds what is read of it.
	maxLeftoverSetFile = 1 << 20
	// salvageChunk is the most mysql.salvageRead returns at once.
	salvageChunk = 512 << 10
	// salvageTimeout bounds one mysqlbinlog run of mysql.salvage.
	salvageTimeout = 30 * time.Minute
	// salvageTimeLayout is the time in a salvage file's name (UTC).
	salvageTimeLayout = "20060102T150405Z"
	// salvageTempPrefix starts the file mysql.salvage writes before the
	// rename; the sweep removes one left behind a day later.
	salvageTempPrefix = ".salvage-"
	salvageTempMaxAge = 24 * time.Hour
)

// salvageNameRe is the name of a salvage file: <time>-<server id>.sql.
var salvageNameRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9]+\.sql$`)

// errSalvageTooLarge stops a salvage past salvageMaxSize.
var errSalvageTooLarge = fmt.Errorf("berkas salvage melebihi %d GiB; dibatalkan dan dihapus", salvageMaxSize>>30)

// errSalvageDirFull stops a salvage that would take the directory past
// salvageDirMax.
var errSalvageDirFull error = &commands.ParamError{Msg: fmt.Sprintf("direktori salvage akan melebihi batas %d GiB; "+
	"salvage dibatalkan dan berkasnya dihapus. Unduh lalu hapus berkas salvage lama dulu", salvageDirMax>>30)}

func (d *Driver) registerSalvage(reg *commands.Registry) {
	reg.Register("mysql.salvage", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.SalvageParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		set, err := parseGTIDSet(p.GTIDSet)
		switch {
		case err != nil:
			return nil, &commands.ParamError{Msg: err.Error()}
		case set.count() == 0:
			return nil, &commands.ParamError{Msg: "set GTID kosong; tidak ada transaksi untuk diselamatkan"}
		}
		if err := d.allowSalvage("mysql.salvage", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if !d.salvageRun.TryLock() {
			return nil, &commands.ParamError{Msg: "salvage lain sedang berjalan di server ini; tunggu sampai selesai lalu ulangi"}
		}
		defer d.salvageRun.Unlock()
		f, err := d.salvage(ctx, h, set, d.serviceWarn("mysql.salvage"))
		if err != nil {
			return nil, err
		}
		d.log("mysql salvage written", "name", f.Name, "size", f.Size, "transactions", set.count())
		return f, nil
	})
	reg.Register("mysql.salvageList", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := d.allowSalvage("mysql.salvageList", nil); err != nil {
			return nil, err
		}
		files, err := d.salvageFiles()
		if err != nil {
			return nil, err
		}
		return proto.SalvageListResult{Files: files}, nil
	})
	reg.Register("mysql.salvageRead", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.SalvageReadParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := d.allowSalvage("mysql.salvageRead", p); err != nil {
			return nil, err
		}
		if err := d.validSalvageName(p.Name); err != nil {
			return nil, err
		}
		if p.Offset < 0 {
			return nil, &commands.ParamError{Msg: "offset tidak boleh negatif"}
		}
		return d.readSalvage(p.Name, p.Offset)
	})
	reg.Register("mysql.salvageDelete", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.SalvageDeleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := d.allowSalvage("mysql.salvageDelete", p); err != nil {
			return nil, err
		}
		if err := d.validSalvageName(p.Name); err != nil {
			return nil, err
		}
		d.salvageMu.Lock()
		defer d.salvageMu.Unlock()
		if err := d.deleteSalvage(p.Name); err != nil {
			return nil, err
		}
		d.log("mysql salvage deleted", "name", p.Name)
		return struct{}{}, nil
	})
}

// allowSalvage checks cmd against the pins, which must exist: mysql.salvage
// runs only on a quarantined server; salvageList, salvageRead and
// salvageDelete (its own files on disk) also on one rebuilt as a replica
// (pins.go).
func (d *Driver) allowSalvage(cmd string, params any) error {
	if d.pins == nil {
		return &commands.ParamError{Msg: "agent ini tidak punya pin peran; perintah " + cmd + " ditolak"}
	}
	return d.allow(cmd, params)
}

// salvageDir is SalvageDir, or DefaultSalvageDir.
func (d *Driver) salvageDir() string {
	if d.SalvageDir != "" {
		return d.SalvageDir
	}
	return DefaultSalvageDir
}

// validSalvageName refuses a name that is not a salvage file's or would
// leave the salvage directory.
func (d *Driver) validSalvageName(name string) error {
	dir := filepath.Clean(d.salvageDir())
	if !salvageNameRe.MatchString(name) || filepath.Dir(filepath.Clean(filepath.Join(dir, name))) != dir {
		return &commands.ParamError{Msg: fmt.Sprintf("nama berkas salvage tidak valid: %q", name)}
	}
	return nil
}

// openSalvageDir creates the salvage directory when missing (0700) and
// opens it as a root no name can leave. It must be a directory (not a
// link) owned by the agent's user, and is set to 0700.
func (d *Driver) openSalvageDir() (*os.Root, error) {
	dir := d.salvageDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("buat direktori salvage: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("direktori salvage: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s bukan direktori", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("direktori salvage %s bukan milik user agent (uid %d)", dir, st.Uid)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("atur mode direktori salvage: %w", err)
		}
	}
	return os.OpenRoot(dir)
}

// salvage writes the transactions of set to a new salvage file. The
// mysqlbinlog run writes a temporary file of its own without any lock;
// salvageMu is held only to check the final name is free and rename to
// it, so the list, a delete and the monitor's sweep never wait for it.
// mysqlbinlog runs as the owner of the binary log directory
// (execAsService, warn); root writes its output.
func (d *Driver) salvage(ctx context.Context, h Host, set gtidSet, warn func(string)) (proto.SalvageFile, error) {
	var f proto.SalvageFile
	qctx, cancel := context.WithTimeout(ctx, leftoverQueryTimeout)
	defer cancel()
	db, err := openAdmin(qctx, h)
	if err != nil {
		return f, err
	}
	defer db.Close()
	s := set.String()
	allowed, ok, err := d.lastLeftover()
	if err != nil {
		return f, err
	}
	if !ok {
		return f, &commands.ParamError{Msg: "Periksa data tertinggal (mysql.leftover) belum menemukan transaksi di agent ini; " +
			"salvage hanya mengambil transaksi yang ditemukan pemeriksaan terakhir. Jalankan Periksa data tertinggal dulu"}
	}
	var sub, found bool
	if err := db.QueryRowContext(qctx, "SELECT GTID_SUBSET(?, @@global.gtid_executed), GTID_SUBSET(?, ?)", s, s, allowed).
		Scan(&sub, &found); err != nil {
		return f, fmt.Errorf("periksa set GTID: %w", err)
	}
	if !sub {
		return f, &commands.ParamError{Msg: "set GTID memuat transaksi yang tidak dijalankan server ini; salvage ditolak"}
	}
	if !found {
		return f, &commands.ParamError{Msg: "set GTID memuat transaksi di luar hasil Periksa data tertinggal terakhir; salvage ditolak. " +
			"Jalankan Periksa data tertinggal lagi lalu amankan hasilnya"}
	}
	paths, _, err := binlogFilesFor(qctx, db, s)
	if err != nil {
		return f, err
	}
	cancel()
	bin, err := findClientTool(h.Layout().MysqldPath, "mysqlbinlog")
	if err != nil {
		return f, err
	}

	root, err := d.openSalvageDir()
	if err != nil {
		return f, err
	}
	defer root.Close()
	used, err := salvageDirSize(root)
	if err != nil {
		return f, err
	}
	if used >= salvageDirMax {
		return f, &commands.ParamError{Msg: fmt.Sprintf("direktori salvage sudah memuat %.1f GiB (batas %d GiB); salvage ditolak. "+
			"Unduh lalu hapus berkas salvage lama dulu", float64(used)/(1<<30), salvageDirMax>>30)}
	}
	limit, overErr := int64(salvageMaxSize), errSalvageTooLarge
	if room := int64(salvageDirMax) - used; room < limit {
		limit, overErr = room, errSalvageDirFull
	}
	created := d.now().UTC().Truncate(time.Second)
	name := created.Format(salvageTimeLayout) + "-" + strconv.FormatInt(d.Cfg.ServerID, 10) + ".sql"
	taken := func() error {
		if _, err := root.Lstat(name); err == nil {
			return &commands.ParamError{Msg: fmt.Sprintf("berkas salvage %s sudah ada; coba lagi sebentar lagi", name)}
		}
		return nil
	}
	if err := taken(); err != nil {
		return f, err
	}
	tmp := salvageTempPrefix + strconv.FormatInt(time.Now().UnixNano(), 36) + ".tmp"
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return f, fmt.Errorf("buat berkas salvage: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			out.Close()
			root.Remove(tmp)
		}
	}()
	rctx, stop := context.WithTimeout(ctx, salvageTimeout)
	defer stop()
	lw := &limitWriter{w: out, max: limit, stop: stop}
	args := append([]string{"--no-defaults", "--include-gtids=" + s, "--skip-gtids", "--verbose"}, paths...)
	err = execAsService(rctx, h, filepath.Dir(paths[0]), warn, nil, lw, bin, args...)
	switch {
	case lw.over:
		return f, overErr
	case rctx.Err() == context.DeadlineExceeded:
		return f, fmt.Errorf("mysqlbinlog tidak selesai dalam %v; salvage dibatalkan", salvageTimeout)
	case err != nil:
		return f, fmt.Errorf("salvage: %w", err)
	}
	if err := out.Sync(); err != nil {
		return f, fmt.Errorf("tulis berkas salvage: %w", err)
	}
	if err := out.Close(); err != nil {
		return f, fmt.Errorf("tulis berkas salvage: %w", err)
	}
	keep = true
	if err := d.publishSalvage(root, tmp, name, taken); err != nil {
		root.Remove(tmp)
		return f, err
	}
	if err := syncDir(d.salvageDir()); err != nil {
		return f, fmt.Errorf("simpan berkas salvage: %w", err)
	}
	return proto.SalvageFile{Name: name, Size: lw.n, CreatedAt: created, ExpiresAt: created.Add(SalvageMaxAge)}, nil
}

// salvageDirSize is the size of every regular file in root, salvage
// files, temporary ones and the leftover set alike.
func salvageDirSize(root *os.Root) (int64, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, fmt.Errorf("baca direktori salvage: %w", err)
	}
	var n int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	return n, nil
}

// recordLeftover keeps set, what mysql.leftover found ("" for nothing)
// comparing this server with source (host:port, the pinned source it
// read) in the quarantine named qid (Pins.QuarantineID, read before the
// comparison), as the bound of later salvages: in leftoverSetFile (0600,
// written to a temporary name and renamed over the old one). The file's
// lines are "source <host:port>", "quarantine <id>", then the set. When it
// cannot be written the old file is removed, so that no earlier record
// stands in for this one; both failures are logged, and salvage then
// answers that Periksa must run first.
func (d *Driver) recordLeftover(qid, source, set string) {
	d.leftoverMu.Lock()
	defer d.leftoverMu.Unlock()
	err := d.writeLeftoverSet(leftoverSourcePrefix + source + "\n" + leftoverQuarantinePrefix + qid + "\n" + set + "\n")
	if err == nil {
		return
	}
	if d.Logger != nil {
		d.Logger.Warn("leftover set not saved; salvage needs a new mysql.leftover", "err", err)
	}
	if err := d.removeLeftoverSet(); err != nil && d.Logger != nil {
		d.Logger.Error("earlier leftover set file not removed after a failed save", "err", err)
	}
}

// Prefixes of the first two lines of leftoverSetFile.
const (
	leftoverSourcePrefix     = "source "
	leftoverQuarantinePrefix = "quarantine "
)

func (d *Driver) writeLeftoverSet(content string) error {
	root, err := d.openSalvageDir()
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := leftoverSetFile + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("tulis set data tertinggal: %w", err)
	}
	_, werr := f.Write([]byte(content))
	if werr == nil {
		werr = f.Chmod(0o600)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, leftoverSetFile)
	}
	if werr != nil {
		root.Remove(tmp)
		return fmt.Errorf("tulis set data tertinggal: %w", werr)
	}
	return syncDir(d.salvageDir())
}

// lastLeftover is the set the last mysql.leftover found, read from
// leftoverSetFile on every use. ok is false when there is no record, it
// found nothing, or it does not belong to the quarantine and source now:
// its quarantine id is not the one pinned now (Pins.QuarantineID; none, as
// on a server quarantined before ids existed, matches nothing), or its
// source is not the source pin.
func (d *Driver) lastLeftover() (string, bool, error) {
	d.leftoverMu.Lock()
	defer d.leftoverMu.Unlock()
	source, qid, set, ok, err := d.readLeftoverSet()
	if err != nil || !ok || set == "" {
		return "", false, err
	}
	ignore := func(why string, args ...any) (string, bool, error) {
		if d.Logger != nil {
			d.Logger.Warn("leftover set ignored: "+why, append([]any{"found_against", source, "quarantine", qid}, args...)...)
		}
		return "", false, nil
	}
	if d.pins == nil {
		return ignore("this agent has no pins")
	}
	cur, has, err := d.pins.QuarantineID(false)
	switch {
	case err != nil:
		return ignore("the quarantine id cannot be read", "err", err)
	case !has:
		return ignore("no quarantine id is pinned now")
	case cur != qid:
		return ignore("it was found in another quarantine", "quarantine_now", cur)
	}
	host, port, _ := ParseSource(source)
	if !d.pins.quarantinedUnder(host, port) {
		return ignore("its source is not the source pinned now")
	}
	return set, true, nil
}

// readLeftoverSet reads leftoverSetFile; ok is false when it is missing,
// or is not a record: its first line is not exactly "source <host:port>"
// and its second exactly "quarantine <id>" (a file written before either
// was recorded, or a damaged one), or it is larger than
// maxLeftoverSetFile. It must be a regular file (not a link), and after
// those lines hold a GTID set.
func (d *Driver) readLeftoverSet() (source, qid, set string, ok bool, err error) {
	if _, err := os.Lstat(d.salvageDir()); errors.Is(err, os.ErrNotExist) {
		return "", "", "", false, nil
	}
	root, err := d.openSalvageDir()
	if err != nil {
		return "", "", "", false, err
	}
	defer root.Close()
	f, err := root.OpenFile(leftoverSetFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("baca set data tertinggal: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", "", "", false, fmt.Errorf("set data tertinggal %s bukan berkas biasa", leftoverSetFile)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLeftoverSetFile+1))
	if err != nil {
		return "", "", "", false, fmt.Errorf("baca set data tertinggal: %w", err)
	}
	noRecord := func(why string) (string, string, string, bool, error) {
		if d.Logger != nil {
			d.Logger.Warn("leftover set file is no record; salvage needs a new mysql.leftover", "file", leftoverSetFile, "why", why)
		}
		return "", "", "", false, nil
	}
	if len(b) > maxLeftoverSetFile {
		return noRecord("larger than the most read")
	}
	head, rest, found := strings.Cut(string(b), "\n")
	source, stamped := strings.CutPrefix(head, leftoverSourcePrefix)
	if !found || !stamped || !canonicalSource(source) {
		return noRecord("the first line is not the source it was found against")
	}
	head, rest, found = strings.Cut(rest, "\n")
	qid, stamped = strings.CutPrefix(head, leftoverQuarantinePrefix)
	if !found || !stamped || !quarantineIDRe.MatchString(qid) {
		return noRecord("the second line is not the quarantine it was found in")
	}
	g, err := parseGTIDSet(strings.TrimSpace(rest))
	if err != nil {
		return "", "", "", false, fmt.Errorf("set data tertinggal di %s tidak valid: %w", leftoverSetFile, err)
	}
	return source, qid, g.String(), true, nil
}

// canonicalSource reports whether s is a source as recordLeftover writes
// it: host:port that ParseSource accepts and that joins back to s exactly.
func canonicalSource(s string) bool {
	host, port, err := ParseSource(s)
	return err == nil && net.JoinHostPort(host, strconv.Itoa(port)) == s
}

// forgetLeftover removes the leftover set: the settle that ends the
// quarantine calls it, since what the old master held was replaced. A file
// left behind belongs to the quarantine that ended; its id is removed with
// the role write (Pins.writeRole), so it is no record later.
func (d *Driver) forgetLeftover() error {
	d.leftoverMu.Lock()
	defer d.leftoverMu.Unlock()
	return d.removeLeftoverSet()
}

// removeLeftoverSet removes leftoverSetFile; none is not an error. The
// caller holds leftoverMu.
func (d *Driver) removeLeftoverSet() error {
	if _, err := os.Lstat(d.salvageDir()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	root, err := d.openSalvageDir()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(leftoverSetFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hapus set data tertinggal: %w", err)
	}
	return syncDir(d.salvageDir())
}

// publishSalvage renames tmp to name under salvageMu, unless taken says
// the name is in use by now (another salvage in the same second).
func (d *Driver) publishSalvage(root *os.Root, tmp, name string, taken func() error) error {
	d.salvageMu.Lock()
	defer d.salvageMu.Unlock()
	if err := taken(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("simpan berkas salvage: %w", err)
	}
	return nil
}

// limitWriter writes to w until more than max bytes would be written; it
// then sets over, calls stop (ending the writer's process) and fails.
type limitWriter struct {
	w    io.Writer
	max  int64
	n    int64
	over bool
	stop func()
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.over || l.n+int64(len(p)) > l.max {
		l.over = true
		l.stop()
		return 0, errSalvageTooLarge
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}

// salvageFiles lists the salvage files, newest first; anything else in
// the directory (and any link) is left out.
func (d *Driver) salvageFiles() ([]proto.SalvageFile, error) {
	root, err := d.openSalvageDir()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("baca direktori salvage: %w", err)
	}
	files := []proto.SalvageFile{}
	for _, e := range entries {
		created, ok := salvageCreated(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, proto.SalvageFile{Name: e.Name(), Size: fi.Size(), CreatedAt: created, ExpiresAt: created.Add(SalvageMaxAge)})
	}
	slices.SortFunc(files, func(a, b proto.SalvageFile) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return files, nil
}

// salvageCreated is the time in a salvage file's name; ok is false for
// any other name.
func salvageCreated(name string) (time.Time, bool) {
	if !salvageNameRe.MatchString(name) {
		return time.Time{}, false
	}
	t, err := time.Parse(salvageTimeLayout, name[:len(salvageTimeLayout)])
	return t, err == nil
}

// errNoSalvage is a salvage file that does not exist.
func errNoSalvage(name string) error {
	return &commands.ParamError{Msg: fmt.Sprintf("berkas salvage %s tidak ada", name)}
}

// readSalvage reads at most salvageChunk bytes of the file name from
// offset. The file is opened without following a link and must be a
// regular file.
func (d *Driver) readSalvage(name string, offset int64) (proto.SalvageReadResult, error) {
	var res proto.SalvageReadResult
	root, err := d.openSalvageDir()
	if err != nil {
		return res, err
	}
	defer root.Close()
	if err := regularSalvage(root, name); err != nil {
		return res, err
	}
	// O_NOFOLLOW: a link put there since is not followed either.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return res, errNoSalvage(name)
	case err != nil:
		return res, &commands.ParamError{Msg: fmt.Sprintf("berkas salvage %s tidak bisa dibuka: %v", name, err)}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return res, err
	}
	if !fi.Mode().IsRegular() {
		return res, &commands.ParamError{Msg: fmt.Sprintf("%s bukan berkas biasa", name)}
	}
	res.Size = fi.Size()
	if offset >= res.Size {
		res.EOF, res.Data = true, []byte{}
		return res, nil
	}
	buf := make([]byte, min(salvageChunk, res.Size-offset))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return res, fmt.Errorf("baca berkas salvage: %w", err)
	}
	res.Data = buf[:n]
	res.EOF = offset+int64(n) >= res.Size
	return res, nil
}

// regularSalvage refuses name unless it is a regular file in root (not a
// link).
func regularSalvage(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errNoSalvage(name)
	case err != nil:
		return fmt.Errorf("berkas salvage: %w", err)
	case !fi.Mode().IsRegular():
		return &commands.ParamError{Msg: fmt.Sprintf("%s bukan berkas biasa (tautan atau direktori); ditolak", name)}
	}
	return nil
}

// deleteSalvage removes the salvage file name; a link is refused. The
// caller holds salvageMu.
func (d *Driver) deleteSalvage(name string) error {
	root, err := d.openSalvageDir()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := regularSalvage(root, name); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("hapus berkas salvage: %w", err)
	}
	return nil
}

// SweepSalvage removes the salvage files created SalvageMaxAge before now
// or earlier, and temporary files of a salvage that died, a day old. It
// reports how many it removed. The health monitor runs it daily
// (monitor.SalvageSweeper).
//
// It never waits: while a delete or a salvage's rename holds salvageMu it
// sweeps nothing and reports busy, and the monitor tries again at its
// next check.
func (d *Driver) SweepSalvage(now time.Time) (removed int, busy bool, err error) {
	if !d.salvageMu.TryLock() {
		return 0, true, nil
	}
	defer d.salvageMu.Unlock()
	if _, err := os.Lstat(d.salvageDir()); errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	root, err := d.openSalvageDir()
	if err != nil {
		return 0, false, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, false, fmt.Errorf("baca direktori salvage: %w", err)
	}
	var errs []error
	for _, e := range entries {
		expired := false
		if created, ok := salvageCreated(e.Name()); ok {
			expired = !now.Before(created.Add(SalvageMaxAge))
		} else if strings.HasPrefix(e.Name(), salvageTempPrefix) {
			if fi, err := e.Info(); err == nil {
				expired = now.Sub(fi.ModTime()) >= salvageTempMaxAge
			}
		}
		if !expired {
			continue
		}
		if err := root.Remove(e.Name()); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, false, errors.Join(errs...)
}
