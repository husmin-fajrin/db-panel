package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

const (
	// DefaultSalvageDir is where salvage files are written (0700 root)
	// when Driver.SalvageDir is empty.
	DefaultSalvageDir = "/var/lib/dbpanel/salvage"
	// SalvageMaxAge is how long a salvage file is kept.
	SalvageMaxAge = 7 * 24 * time.Hour
	// salvageMaxSize is the largest salvage file written; a larger one is
	// abandoned and removed.
	salvageMaxSize = 2 << 30
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
		f, err := d.salvage(ctx, h, set)
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

// allowSalvage checks cmd against the pins, which must exist: only a
// quarantined server runs the salvage commands.
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
func (d *Driver) salvage(ctx context.Context, h Host, set gtidSet) (proto.SalvageFile, error) {
	var f proto.SalvageFile
	qctx, cancel := context.WithTimeout(ctx, leftoverQueryTimeout)
	defer cancel()
	db, err := openAdmin(qctx, h)
	if err != nil {
		return f, err
	}
	defer db.Close()
	s := set.String()
	var sub bool
	if err := db.QueryRowContext(qctx, "SELECT GTID_SUBSET(?, @@global.gtid_executed)", s).Scan(&sub); err != nil {
		return f, fmt.Errorf("periksa set GTID: %w", err)
	}
	if !sub {
		return f, &commands.ParamError{Msg: "set GTID memuat transaksi yang tidak dijalankan server ini; salvage ditolak"}
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
	lw := &limitWriter{w: out, max: salvageMaxSize, stop: stop}
	args := append([]string{"--no-defaults", "--include-gtids=" + s, "--skip-gtids", "--verbose"}, paths...)
	err = h.Exec(rctx, nil, lw, bin, args...)
	switch {
	case lw.over:
		return f, errSalvageTooLarge
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
