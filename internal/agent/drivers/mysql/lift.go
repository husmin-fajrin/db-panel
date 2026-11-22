package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Every legitimate lift of read-only (spec 5.2) holds one file lock: the
// agent's jobs (dump load, CLONE, clone plugin, dropped databases, local
// accounts) through the driver's liftGate, the quarantine guard through
// tryGuard, and the dbpanel agent commands through AcquireLiftLock. A lift
// only ever sends SET GLOBAL super_read_only = OFF and puts back only
// super_read_only (plan evidence M1a: SET GLOBAL read_only = OFF turns
// both off silently); read_only stays on throughout.

// DefaultLiftLock is the lift lock file: in a 0700 directory, mode 0600,
// both owned by the process's uid (root).
var DefaultLiftLock = "/var/lib/dbpanel/agent/lift.lock"

// liftLockWait bounds an agent job's wait for a lift lock another process
// holds; tests shorten it.
var liftLockWait = 10 * time.Second

// liftLockPoll is how often a held lift lock is tried again.
const liftLockPoll = 20 * time.Millisecond

// liftSessionWaitTimeout keeps a lift's own connection alive while it idles
// through a long load (X7: an idle session dies with 4031 after the global
// wait_timeout, and the pool's fresh login may be refused by then).
const liftSessionWaitTimeout = 28800

// ErrLiftBusy (C3) refuses a dbpanel agent command while the lift lock is
// held, for example by a rebuild that is copying data.
var ErrLiftBusy = errors.New("kunci baca-saja server ini sedang dibuka pekerjaan lain (misalnya bangun ulang yang sedang menyalin data); coba lagi sesudah pekerjaan itu selesai")

// ErrLiftHeldByCommand (T12) fails an agent job that found the lift lock
// held by another process (a dbpanel agent command) for liftLockWait,
// before it changed anything.
var ErrLiftHeldByCommand = errors.New(`kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`)

// Reasons a lift lock file (or its directory) is refused.
const (
	liftNotRegular = "bukan berkas biasa"
	liftNotOwned   = "bukan milik root"
	liftOpenToAll  = "bisa dibaca atau ditulis pengguna lain"
	liftManyNames  = "punya lebih dari satu nama"
)

// LiftLockError (C4) is a lift lock file, or its directory, that fails the
// checks of openLiftLock; Reason is one of the four reasons above.
type LiftLockError struct{ Path, Reason string }

func (e *LiftLockError) Error() string {
	return "berkas kunci " + e.Path + " tidak aman dipakai (" + e.Reason + "); tidak ada yang diubah. Agent membuat ulang " +
		"berkas itu saat membutuhkannya; bila pesan ini tetap muncul, mulai ulang agent lalu jalankan perintah ini lagi."
}

// openLiftLock opens the lift lock file at path for flock, creating its
// directory (0700) and the file (O_EXCL, 0600) when missing. The file is
// opened without following a symlink and is not inherited by child
// processes. The directory must be a directory owned by the process's uid
// with no group or other bits; the open descriptor a regular file owned by
// that uid, with no group or other bits and one name. With replace (the
// agent) a file that fails is replaced by a new one written beside it and
// renamed over it, and a directory of the process's uid with group or other
// bits is made 0700; without (the CLI) either is a *LiftLockError and
// nothing is created or changed.
func openLiftLock(path string, replace bool) (*os.File, error) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	refuse := func(reason string) error { return &LiftLockError{Path: path, Reason: reason} }
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("buat direktori kunci %s: %w", dir, err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("baca direktori kunci %s: %w", dir, err)
	}
	if !dirInfo.IsDir() {
		return nil, refuse(liftNotRegular)
	}
	if ownerUID(dirInfo) != os.Geteuid() {
		return nil, refuse(liftNotOwned)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		if !replace {
			return nil, refuse(liftOpenToAll)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("perbaiki mode direktori kunci %s: %w", dir, err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("buka direktori kunci %s: %w", dir, err)
	}
	defer root.Close()
	// The directory opened is the one checked, not one swapped in since.
	if opened, err := root.Stat("."); err != nil || !os.SameFile(opened, dirInfo) {
		return nil, fmt.Errorf("direktori kunci %s berubah selagi dibuka", dir)
	}

	const flags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	for range 3 {
		f, err := root.OpenFile(name, flags, 0)
		if errors.Is(err, fs.ErrNotExist) {
			f, err = root.OpenFile(name, flags|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(err, fs.ErrExist) {
				continue // created by another opener meanwhile: open that one
			}
		}
		var reason string
		switch {
		case err != nil && isSymlink(root, name):
			reason = liftNotRegular // never followed (ELOOP, or os.Root's escape refusal)
		case err != nil:
			return nil, fmt.Errorf("buka berkas kunci %s: %w", path, err)
		default:
			if reason = liftFileFault(f); reason == "" {
				return f, nil
			}
			f.Close()
		}
		if !replace {
			return nil, refuse(reason)
		}
		if err := replaceLiftLock(root, name); err != nil {
			return nil, fmt.Errorf("ganti berkas kunci %s: %w", path, err)
		}
	}
	return nil, fmt.Errorf("berkas kunci %s terus berubah selagi dibuka", path)
}

// liftFileFault is why the open lift lock file f fails the checks, "" when
// it passes.
func liftFileFault(f *os.File) string {
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return liftNotRegular
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return liftNotRegular
	case int(st.Uid) != os.Geteuid():
		return liftNotOwned
	case fi.Mode().Perm()&0o077 != 0:
		return liftOpenToAll
	case st.Nlink != 1:
		return liftManyNames
	}
	return ""
}

// replaceLiftLock writes a new empty 0600 file beside name and renames it
// over name: a symlink is replaced, not followed, and another name of a
// hard-linked file keeps the old file.
func replaceLiftLock(root *os.Root, name string) error {
	tmp := name + ".new-" + strconv.Itoa(os.Getpid())
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		root.Remove(tmp)
		return err
	}
	return nil
}

// isSymlink reports whether name in root is a symlink.
func isSymlink(root *os.Root, name string) bool {
	fi, err := root.Lstat(name)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// ownerUID is the uid owning fi's file, -1 when unknown.
func ownerUID(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// tryFlock takes an exclusive flock on f without waiting; held is true when
// another open file description has it.
func tryFlock(f *os.File) (held bool, err error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return true, nil
		}
		return false, err
	}
}

// AcquireLiftLock takes the lift lock at path for a dbpanel agent command,
// without waiting: ErrLiftBusy when it is held, *LiftLockError when the
// file or its directory fails the checks. release drops it.
func AcquireLiftLock(path string) (release func(), err error) {
	f, err := openLiftLock(path, false)
	if err != nil {
		return nil, err
	}
	held, err := tryFlock(f)
	if err != nil || held {
		f.Close()
		if err == nil {
			err = ErrLiftBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil // closing the descriptor drops the flock
}

// liftGate counts the agent's lifts of read-only on one server (spec 5.2).
// The first opener takes the file lock and records whether super_read_only
// was on; every opener turns it off; the last one to end puts it back when
// it was on. A CLONE that is past its data phase leaves through abandon,
// which puts nothing back.
type liftGate struct {
	path string

	mu      sync.Mutex
	holders int
	lock    *os.File // the gate's flock while holders > 0
	superOn bool     // super_read_only as the first opener read it
}

func newLiftGate(path string) *liftGate { return &liftGate{path: path} }

// liftHold is one opener's lift; end or abandon ends it, once.
type liftHold struct {
	g       *liftGate
	ctx     context.Context
	conn    *sql.Conn
	logf    func(level, format string, args ...any)
	superOn bool
	first   bool      // this hold's begin took the lock (first opener)
	mark    *loadMark // cleared after the hold ends (liftReadOnly)
	done    bool      // guarded by g.mu
}

// begin lifts read-only for one opener: it keeps a connection of db (its
// session wait_timeout raised), takes the file lock as the first opener
// (waiting up to liftLockWait, else ErrLiftHeldByCommand) and records
// super_read_only, then sends SET GLOBAL super_read_only = OFF, the only
// statement a lift sends. Nothing has changed when it fails: a first
// opener whose OFF failed (perhaps after it was applied) sends SET GLOBAL
// super_read_only = ON when it was on.
func (g *liftGate) begin(ctx context.Context, db *sql.DB, logf func(level, format string, args ...any)) (*liftHold, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("buka sambungan MySQL: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION wait_timeout = "+strconv.Itoa(liftSessionWaitTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("SET SESSION wait_timeout: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	first := g.holders == 0
	if first {
		f, err := g.acquire(ctx)
		if err != nil {
			conn.Close()
			return nil, err
		}
		var on bool
		if err := conn.QueryRowContext(ctx, "SELECT @@GLOBAL.super_read_only").Scan(&on); err != nil {
			f.Close()
			conn.Close()
			return nil, fmt.Errorf("baca super_read_only: %w", err)
		}
		g.lock, g.superOn = f, on
	}
	// Sent by every opener: the first one's may have been put back since.
	if _, err := conn.ExecContext(ctx, "SET GLOBAL super_read_only = OFF"); err != nil {
		if first {
			// The OFF may have been applied before it failed (a cancel
			// landing on it): put back what the first opener read, under
			// the lock, as liftHold.restore would; only = ON is sent.
			if g.superOn {
				rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
				const q = "SET GLOBAL super_read_only = ON"
				if _, rerr := conn.ExecContext(rctx, q); rerr != nil {
					logf("error", "%s after a failed lift failed: %v", q, rerr)
				}
				cancel()
			}
			g.lock.Close()
			g.lock, g.superOn = nil, false
		}
		conn.Close()
		return nil, fmt.Errorf("SET GLOBAL super_read_only = OFF: %w", err)
	}
	g.holders++
	if first && g.superOn {
		logf("info", "read_only lifted for the load")
	}
	return &liftHold{g: g, ctx: ctx, conn: conn, logf: logf, superOn: g.superOn, first: first}, nil
}

// acquire opens the lock file (replacing one that fails the checks) and
// takes its flock, trying again until liftLockWait passes. The caller
// holds g.mu.
func (g *liftGate) acquire(ctx context.Context) (*os.File, error) {
	f, err := openLiftLock(g.path, true)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(liftLockWait)
	for {
		held, err := tryFlock(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("kunci %s: %w", g.path, err)
		}
		if !held {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrLiftHeldByCommand
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(liftLockPoll):
		}
	}
}

// openGuardLock opens the lock file for tryGuard; tests count its calls.
var openGuardLock = openLiftLock

// tryGuard takes the lift lock for the quarantine guard through a
// descriptor of its own, without waiting. free is true when it got the
// lock and no lift of this process is open (or starting or ending);
// release drops the lock and is never nil. Three states, not two: when no
// lift of this process is open and the lock file cannot be opened or
// locked, free is false and openErr says why. No lift of this agent can be
// open then (begin needs the same file), so the caller treats the server
// as not legitimately lifted (ruling D54).
func (g *liftGate) tryGuard() (release func(), free bool, openErr error) {
	none := func() {}
	if !g.mu.TryLock() {
		return none, false, nil
	}
	defer g.mu.Unlock()
	if g.holders > 0 {
		return none, false, nil
	}
	f, err := openGuardLock(g.path, true)
	if err != nil {
		return none, false, err
	}
	held, err := tryFlock(f)
	if err != nil {
		f.Close()
		return none, false, fmt.Errorf("kunci %s: %w", g.path, err)
	}
	if held {
		f.Close()
		return none, false, nil
	}
	return func() { f.Close() }, true, nil
}

// errLiftLockUnopenable is the settle's answer when the lift lock file
// cannot be opened: no command's lift (not T12), and nothing is recorded,
// so "Ulangi dari tahap ini" works once the cause is fixed.
func errLiftLockUnopenable(path string, cause error) error {
	return fmt.Errorf(`berkas kunci baca-saja %s di server ini tidak bisa dibuka (%v); karantina tidak diakhiri. `+
		`Perbaiki sebabnya di server itu, lalu tekan "Ulangi dari tahap ini"`, path, cause)
}

// superWasOn reports whether super_read_only was on when the first opener
// of the lift read it.
func (h *liftHold) superWasOn() bool { return h.superOn }

// loadTrailer is loadTrailer for a load under h: only the first opener's
// full load puts super_read_only back in its stream; under a later hold
// the first still lifts it, and the last holder's end restores it.
func (h *liftHold) loadTrailer(full bool) string { return loadTrailer(full, h.superWasOn() && h.first) }

// end ends the hold. The last opener reads super_read_only on its kept
// connection and, only when it is off and was on, sends SET GLOBAL
// super_read_only = ON, bounded by restoreTimeout whatever the job's
// context; a failure is logged as the agent's own and returned, and the
// file lock is released all the same (the guard locks the server again).
// Ending again, or after abandon, does nothing.
func (h *liftHold) end() error {
	err := h.finish(true)
	h.mark.clear()
	return err
}

// abandon ends the hold without touching the flags (a CLONE past its data
// phase, whose restart applies the persisted values); when it was the last
// opener the recorded value is forgotten and the file lock released.
func (h *liftHold) abandon() {
	h.finish(false)
	h.mark.clear()
}

func (h *liftHold) finish(restore bool) error {
	g := h.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if h.done {
		return nil
	}
	h.done = true
	defer h.conn.Close()
	g.holders--
	if g.holders > 0 {
		return nil
	}
	var err error
	if restore && g.superOn {
		err = h.restore()
	}
	g.lock.Close() // drops the flock
	g.lock, g.superOn = nil, false
	return err
}

// restore puts super_read_only back on h's kept connection unless it is on
// already (a full dump's load puts it back in its own stream, after which
// no agent session may set it: X7).
func (h *liftHold) restore() error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), restoreTimeout)
	defer cancel()
	var on bool
	if err := h.conn.QueryRowContext(ctx, "SELECT @@GLOBAL.super_read_only").Scan(&on); err != nil {
		h.logf("error", "reading super_read_only to put read-only back failed: %v", err)
		return fmt.Errorf("baca super_read_only: %w", err)
	}
	if on {
		return nil
	}
	const q = "SET GLOBAL super_read_only = ON"
	if _, err := h.conn.ExecContext(ctx, q); err != nil {
		h.logf("error", "%s failed: %v", q, err)
		return fmt.Errorf("%s: %w", q, err)
	}
	return nil
}
