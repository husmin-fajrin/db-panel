package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Programs that read files under directories the MySQL service account
// can write (spec 5.5): mysqlbinlog and cat of the binary log index, and
// tail of the error log, run as the owner of the directory read, so a
// symlink planted there never gets a root process to read another file.
// The account must never decide when root runs them:
//   - a directory root owns: run as root (the tail: read here, readTail)
//     with T19 once;
//   - an owner that cannot be read (the account may have renamed the
//     directory away for a moment): not run at all, error T19c;
//   - under the owner's ids, a program that could not be started at all
//     runs again as root with T19, except a start refused with EAGAIN (the
//     account is at its process limit, which it can reach on purpose):
//     error T19b. A program that ran and failed is never run again.

// serviceRootWarning (T19) is logged when a program runs as root instead.
const serviceRootWarning = "tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root"

// serviceUnreadable (T19c) is the error when the owner of a directory
// cannot be read: its directory and cause are filled in.
const serviceUnreadable = "pemilik direktori %s tidak bisa dibaca (%w), jadi program ini tidak dijalankan; periksa direktori itu di server ini, lalu ulangi"

// serviceAtLimit (T19b) starts the error of a program that could not start
// as the service account because it is at its process limit.
const serviceAtLimit = "akun layanan MySQL sudah mencapai batas jumlah proses, jadi program ini tidak bisa dijalankan sebagai akun itu dan tidak dijalankan sebagai root; periksa proses milik akun itu di server ini, lalu ulangi"

const (
	// bootLogTimeout bounds one read of the error log's tail.
	bootLogTimeout = 5 * time.Second
	// indexReadTimeout bounds one read of the binary log index.
	indexReadTimeout = binlogReadTimeout
)

// errServiceRoot: the directory is root's, so there is no service account
// to run as.
var errServiceRoot = errors.New("direktori milik root")

// atProcessLimit: err is ExecEnvAs's for a start refused with EAGAIN.
func atProcessLimit(err error) bool {
	return errors.Is(err, ErrNotStarted) && errors.Is(err, syscall.EAGAIN)
}

// lstatOwner is the uid and gid owning path itself (a symlink is not
// followed); tests replace it.
var lstatOwner = func(path string) (uid, gid uint32, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("data stat tidak dikenali")
	}
	return st.Uid, st.Gid, nil
}

// serviceCredential is the ids of dir's owner and group, without
// supplementary groups: errServiceRoot when root owns it.
func serviceCredential(dir string) (*syscall.Credential, error) {
	uid, gid, err := lstatOwner(dir)
	if err != nil {
		return nil, err
	}
	if uid == 0 {
		return nil, errServiceRoot
	}
	return &syscall.Credential{Uid: uid, Gid: gid}, nil
}

// execAsService runs name as the owner of dir (serviceCredential). When
// root owns dir, or when the program could not be started under the
// owner's ids (ErrNotStarted) for another cause than EAGAIN, it runs as
// root (h.Exec) and warn gets T19. An owner that cannot be read is T19c
// and a start refused with EAGAIN is T19b, neither run as root; any other
// failure is returned as it is.
func execAsService(ctx context.Context, h Host, dir string, warn func(string), stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cred, err := serviceCredential(dir)
	if err != nil && !errors.Is(err, errServiceRoot) {
		return fmt.Errorf(serviceUnreadable, dir, err)
	}
	if err == nil {
		err = h.ExecEnvAs(ctx, cred, nil, stdin, stdout, name, args...)
		if atProcessLimit(err) {
			return fmt.Errorf("%s: %w", serviceAtLimit, err)
		}
		if !errors.Is(err, ErrNotStarted) {
			return err
		}
	}
	warn(serviceRootWarning)
	return h.Exec(ctx, stdin, stdout, name, args...)
}

// readBootLogTail is the last bootLogTail bytes of the error log at path,
// read by tail as the owner of its directory within bootLogTimeout. When
// root owns the directory, or when tail could not be started under the
// owner's ids for another cause than EAGAIN, it is read here (readTail)
// and warn gets T19. An owner that cannot be read is T19c and a start
// refused with EAGAIN is T19b: the log is not read.
func readBootLogTail(ctx context.Context, h Host, path string, warn func(string)) ([]byte, error) {
	dir := filepath.Dir(path)
	cred, err := serviceCredential(dir)
	if err != nil && !errors.Is(err, errServiceRoot) {
		return nil, fmt.Errorf(serviceUnreadable, dir, err)
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, bootLogTimeout)
		defer cancel()
		var b bytes.Buffer
		lw := &limitWriter{w: &b, max: bootLogTail, stop: cancel}
		err = h.ExecEnvAs(ctx, cred, nil, nil, lw, "tail", "-c", strconv.Itoa(bootLogTail), "--", path)
		switch {
		case lw.over:
			return nil, fmt.Errorf("ekor log %s melebihi %d byte", path, bootLogTail)
		case ctx.Err() == context.DeadlineExceeded:
			return nil, fmt.Errorf("ekor log %s tidak terbaca dalam %v", path, bootLogTimeout)
		case err == nil:
			return b.Bytes(), nil
		case atProcessLimit(err):
			return nil, fmt.Errorf("%s: %w", serviceAtLimit, err)
		case !errors.Is(err, ErrNotStarted):
			return nil, err
		}
	}
	warn(serviceRootWarning)
	return readTail(path, bootLogTail)
}

// serviceWarn is the warn of execAsService for one run of the command
// cmd: T19 in the agent's log, once however many programs it runs.
func (d *Driver) serviceWarn(cmd string) func(string) {
	var once sync.Once
	return func(msg string) { once.Do(func() { d.warn(msg, "command", cmd) }) }
}
