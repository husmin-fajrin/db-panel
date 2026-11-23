package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Bounds of the backup folder scan: files at most three levels
// down (in the folder, a subfolder and a sub-subfolder), at most
// maxBackupEntries entries seen, at most backupScanTimeout waited for,
// at most one scan every backupScanEvery.
const (
	backupMaxDepth     = 3
	maxBackupEntries   = 10000
	backupScanTimeout  = 10 * time.Second
	backupScanEvery    = 5 * time.Minute
	maxBackupFolderLen = 1024
)

// hiddenBackupRoots are the trees the agent's service does not see as the
// host has them (ProtectHome, PrivateTmp): a folder there reads empty or
// not at all. refusedBackupRoots hold no backups.
var (
	hiddenBackupRoots  = []string{"/home", "/root", "/run/user", "/tmp", "/var/tmp"}
	refusedBackupRoots = []string{"/proc", "/sys", "/dev"}
)

// ValidBackupFolder reports whether p may be scanned as the backup folder,
// and otherwise why not: proto.BackupReasonHidden for a folder at or under
// /home, /root, /run/user, /tmp or /var/tmp, proto.BackupReasonInvalid for
// any other refusal (not absolute or not clean, over 1024 bytes, a control
// byte, "/", or at or under /proc, /sys or /dev). It reads p only, never
// the file system. The length comes first: an over-long folder is refused
// before filepath.Clean copies it.
func ValidBackupFolder(p string) (reason string, ok bool) {
	if len(p) > maxBackupFolderLen || !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return proto.BackupReasonInvalid, false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return proto.BackupReasonInvalid, false
		}
	}
	under := func(roots []string) bool {
		return slices.ContainsFunc(roots, func(r string) bool { return p == r || strings.HasPrefix(p, r+"/") })
	}
	switch {
	case under(refusedBackupRoots):
		return proto.BackupReasonInvalid, false
	case under(hiddenBackupRoots):
		return proto.BackupReasonHidden, false
	}
	return "", true
}

// backupResult is one scan of the backup folder.
type backupResult struct {
	status, reason string    // proto.BackupOK, BackupNone or BackupUnreadable with its reason
	newest         time.Time // ModTime of the newest regular file, with BackupOK
	size           int64     // its size
	truncated      bool      // the entry limit ended the scan
	at             time.Time // when the scan ended
	detail         string    // for the log only: the system error, never a path
}

// state is r as reported in a snapshot taken at now.
func (r backupResult) state(now time.Time) *proto.BackupState {
	s := &proto.BackupState{Status: r.status, Reason: r.reason, Truncated: r.truncated, ScanAgoSec: secondsSince(now, r.at)}
	if r.status == proto.BackupOK {
		s.NewestAgeSec, s.NewestSize = secondsSince(now, r.newest), r.size
	}
	return s
}

// secondsSince is whole seconds from t to now, 0 when t is zero or later.
func secondsSince(now, t time.Time) int64 {
	if t.IsZero() || !now.After(t) {
		return 0
	}
	return int64(now.Sub(t) / time.Second)
}

func unreadable(reason string, err error, at time.Time) backupResult {
	return backupResult{status: proto.BackupUnreadable, reason: reason, detail: errnoText(err), at: at}
}

// backupReason is the reason an error leaves the folder unreadable.
func backupReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return proto.BackupReasonMissing
	case errors.Is(err, fs.ErrPermission):
		return proto.BackupReasonDenied
	case errors.Is(err, syscall.ENOTDIR):
		return proto.BackupReasonNotDir
	default:
		return proto.BackupReasonError
	}
}

// errnoText is the system error inside err, without the path a PathError
// carries (it may name a file in the folder).
func errnoText(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return ""
	case errors.As(err, &errno):
		return errno.Error()
	default:
		return "unexpected error"
	}
}

// scanBackup finds the newest regular file in folder, at most
// backupMaxDepth levels down and within maxBackupEntries entries. The
// folder's own path is resolved as the kernel does, symlinks included
// (openBackupRoot). Below it no symlink is followed and no file is
// opened: only directories are opened, one path component at a time with
// O_NOFOLLOW from the folder's descriptor, to be listed, and each entry
// is lstat'ed from its directory; symlinks, FIFOs, sockets and devices
// are skipped. now stamps the result. It can block on a hung file
// system; the caller bounds the wait.
func scanBackup(folder string, now func() time.Time) backupResult {
	root, top, res, ok := openBackupRoot(folder, now)
	if !ok {
		return res
	}
	defer root.Close()
	defer top.Close()
	w := &backupWalk{}
	err := fs.WalkDir(backupFS{root: root, top: top, w: w}, ".", w.visit)
	switch {
	case err != nil:
		return unreadable(backupReason(err), err, now())
	case w.found:
		return backupResult{status: proto.BackupOK, newest: w.newest, size: w.size, truncated: w.truncated, at: now()}
	case w.truncated:
		r := unreadable(proto.BackupReasonLimit, nil, now())
		r.truncated = true
		return r
	default:
		return backupResult{status: proto.BackupNone, at: now()}
	}
}

// openBackupRoot opens folder, following symlinks in its path as the
// kernel does, with O_DIRECTORY so that a FIFO, device or file there (also
// through a link) fails at once with ENOTDIR instead of blocking
// (os.OpenRoot opens without it). The path the kernel resolved must pass
// ValidBackupFolder: a link may lead under a refused or hidden tree. It
// returns the folder as an os.Root, opened through the descriptor rather
// than the path again, and that descriptor, top, which backupFS descends
// from; the caller closes both.
func openBackupRoot(folder string, now func() time.Time) (*os.Root, *os.File, backupResult, bool) {
	var (
		fd  int
		err error
	)
	for {
		fd, err = syscall.Open(folder, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return nil, nil, unreadable(backupReason(err), err, now()), false
	}
	top := os.NewFile(uintptr(fd), folder)
	fail := func(reason string, err error) (*os.Root, *os.File, backupResult, bool) {
		top.Close()
		return nil, nil, unreadable(reason, err, now()), false
	}
	self := "/proc/self/fd/" + strconv.Itoa(fd)
	resolved, err := os.Readlink(self)
	if err != nil {
		return fail(proto.BackupReasonError, err)
	}
	if resolved != folder {
		if reason, ok := ValidBackupFolder(resolved); !ok {
			return fail(reason, nil)
		}
	}
	root, err := os.OpenRoot(self) // the directory top holds, not a path again
	if err != nil {
		return fail(backupReason(err), err)
	}
	a, err1 := top.Stat()
	b, err2 := root.Lstat(".")
	if err := errors.Join(err1, err2); err != nil || !os.SameFile(a, b) {
		root.Close()
		return fail(proto.BackupReasonError, err)
	}
	return root, top, backupResult{}, true
}

// backupWalk is the state of one scan.
type backupWalk struct {
	seen      int // entries seen, the folder itself not counted
	truncated bool
	found     bool
	newest    time.Time
	size      int64
}

// visit is the fs.WalkDirFunc of a scan. An error reading the folder
// itself ends the scan; one below it skips what could not be read.
func (w *backupWalk) visit(p string, d fs.DirEntry, err error) error {
	if err != nil {
		if p == "." {
			return err
		}
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if p == "." {
		return nil
	}
	w.seen++
	if w.seen > maxBackupEntries {
		w.truncated = true
		return fs.SkipAll
	}
	t := d.Type()
	switch {
	case t.IsDir():
		if strings.Count(p, "/")+1 >= backupMaxDepth {
			return fs.SkipDir // its files would be a level deeper than allowed
		}
	case t.IsRegular():
		info, err := d.Info() // lstat'ed from its directory by os.Root
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if !w.found || info.ModTime().After(w.newest) {
			w.found, w.newest, w.size = true, info.ModTime(), info.Size()
		}
	}
	return nil
}

// backupFS is the file system fs.WalkDir walks. ReadDir(".") lists the
// folder's os.Root; ReadDir of a subdirectory opens it one component at a
// time from top with openat(O_DIRECTORY|O_NOFOLLOW), so a directory listed
// as one and swapped for a symlink before it is listed (at any component)
// fails with ELOOP and is skipped: the path WalkDir gives is the real
// place, and its depth the real depth. Each directory is listed through
// an os.Root on its descriptor, so its entries are lstat'ed from it, and
// no more entries are read than the scan may still see, plus one to
// notice the limit. A FIFO swapped in for a directory fails with ENOTDIR
// instead of blocking. Open is refused, and Stat answers for the folder
// only: the scan opens no entry.
type backupFS struct {
	root *os.Root
	top  *os.File // the folder's descriptor
	w    *backupWalk
}

func (b backupFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

func (b backupFS) Stat(name string) (fs.FileInfo, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrPermission}
	}
	return b.root.Lstat(".")
}

func (b backupFS) ReadDir(name string) ([]fs.DirEntry, error) {
	r := b.root
	if name != "." {
		sub, err := b.openSub(name)
		if err != nil {
			return nil, err
		}
		defer sub.Close()
		r = sub
	}
	f, err := r.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(max(maxBackupEntries-b.w.seen, 0) + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	slices.SortFunc(list, func(x, y fs.DirEntry) int { return strings.Compare(x.Name(), y.Name()) })
	return list, err
}

// openSub opens the directory name (slash-separated, below the folder)
// from top one component at a time, none followed if it is a symlink, and
// returns it as an os.Root.
func (b backupFS) openSub(name string) (*os.Root, error) {
	fd, owned := int(b.top.Fd()), -1
	defer func() {
		if owned >= 0 {
			syscall.Close(owned)
		}
	}()
	for _, c := range strings.Split(name, "/") {
		if c == "" || c == "." || c == ".." {
			return nil, &fs.PathError{Op: "openat", Path: name, Err: fs.ErrInvalid}
		}
		var (
			next int
			err  error
		)
		for {
			next, err = syscall.Openat(fd, c, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			if err != syscall.EINTR {
				break
			}
		}
		if err != nil {
			return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
		}
		if owned >= 0 {
			syscall.Close(owned)
		}
		fd, owned = next, next
	}
	return os.OpenRoot("/proc/self/fd/" + strconv.Itoa(fd)) // the directory fd holds
}

// backupScan is the state of the backup folder scans, guarded by bmu.
type backupScan struct {
	running bool         // a scan's goroutine has not returned yet
	gen     uint64       // the folder generation (Monitor.backupGen) of the last scan started
	started time.Time    // when it started
	res     backupResult // the last result for gen; status "" until one ended or timed out
}

// errScanRunning is logged while a scan that timed out has not returned.
var errScanRunning = errors.New("no new scan until it returns")

// scanBackupFolder scans the backup folder in the check loop, after
// sweepSalvage, when a folder is set, valid, and either changed since the
// last scan or last scanned backupScanEvery ago. The scan runs in its own
// goroutine; the loop waits for it at most backupScanTimeout and then
// records BackupReasonTimeout. Only one scan goroutine exists at a time:
// while one has not returned (a hung mount), none starts, whatever the
// folder; when the monitor stops, the loop stops waiting and leaves it.
func (m *Monitor) scanBackupFolder(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	folder, gen, scan, after := m.backupFolder, m.backupGen, m.scan, m.scanAfter
	m.mu.Unlock()
	if folder == "" {
		return
	}
	if reason, ok := ValidBackupFolder(folder); !ok {
		m.warn("backup folder not scanned", fmt.Errorf("folder %q: %s", clip(folder, maxBackupFolderLen), reason))
		return
	}
	now := m.now()
	m.bmu.Lock()
	if m.bk.running {
		m.bmu.Unlock()
		m.warn("backup folder scan still running", errScanRunning)
		return
	}
	if m.bk.gen == gen && !m.bk.started.IsZero() && now.Sub(m.bk.started) < backupScanEvery {
		m.bmu.Unlock()
		return
	}
	if m.bk.gen != gen {
		m.bk.res = backupResult{} // the old folder's result is dropped
	}
	m.bk.running, m.bk.gen, m.bk.started = true, gen, now
	m.bmu.Unlock()

	done := make(chan backupResult, 1)
	go func() {
		res := scan(folder, m.now)
		m.bmu.Lock()
		m.bk.running = false
		m.bmu.Unlock()
		done <- res
	}()
	var res backupResult
	select {
	case res = <-done:
	case <-after(backupScanTimeout):
		res = unreadable(proto.BackupReasonTimeout, nil, m.now())
	case <-ctx.Done():
		return
	}
	m.bmu.Lock()
	m.bk.res = res // a scan that returns after its bound is not recorded
	m.bmu.Unlock()
	if res.status == proto.BackupUnreadable {
		err := fmt.Errorf("folder %q: %s", folder, res.reason)
		if res.detail != "" {
			err = fmt.Errorf("%w (%s)", err, res.detail)
		}
		m.warn("backup folder not readable", err)
	}
}

// backupState is the backup folder's state for a snapshot taken at now:
// nil without a folder, BackupUnreadable for a folder ValidBackupFolder
// refuses, BackupPending until the first scan of the current folder ended,
// then that scan's result.
func (m *Monitor) backupState(now time.Time) *proto.BackupState {
	m.mu.Lock()
	folder, gen := m.backupFolder, m.backupGen
	m.mu.Unlock()
	if folder == "" {
		return nil
	}
	if reason, ok := ValidBackupFolder(folder); !ok {
		return &proto.BackupState{Status: proto.BackupUnreadable, Reason: reason}
	}
	m.bmu.Lock()
	bk := m.bk
	m.bmu.Unlock()
	if bk.gen != gen || bk.res.status == "" {
		return &proto.BackupState{Status: proto.BackupPending}
	}
	return bk.res.state(now)
}
