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
// Without an owner to run as (unreadable, or root) they run as root with
// T19 once; under the owner's ids, only a program that could not be
// started at all runs again as root, never one that ran and failed.

// serviceRootWarning (T19) is logged when a program runs as root instead.
const serviceRootWarning = "tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root"

const (
	// bootLogTimeout bounds one read of the error log's tail.
	bootLogTimeout = 5 * time.Second
	// indexReadTimeout bounds one read of the binary log index.
	indexReadTimeout = binlogReadTimeout
)

// errServiceRoot: the directory is root's, so there is no service account
// to run as.
var errServiceRoot = errors.New("direktori milik root")

// lstatOwner is the uid and gid owning path itself (a symlink is not
// followed); tests replace it.
var lstatOwner = func(path string) (uid, gid uint32, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("pemilik %s tidak bisa dibaca", path)
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

// execAsService runs name as the owner of dir (serviceCredential). With
// no owner to run as, or when the program could not be started under
// its ids (ErrNotStarted), it runs as root (h.Exec) and warn gets T19;
// any other failure is returned as it is.
func execAsService(ctx context.Context, h Host, dir string, warn func(string), stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cred, err := serviceCredential(dir)
	if err == nil {
		err = h.ExecEnvAs(ctx, cred, nil, stdin, stdout, name, args...)
		if !errors.Is(err, ErrNotStarted) {
			return err
		}
	}
	warn(serviceRootWarning)
	return h.Exec(ctx, stdin, stdout, name, args...)
}

// readBootLogTail is the last bootLogTail bytes of the error log at path,
// read by tail as the owner of its directory within bootLogTimeout. With
// no owner to run as, or when tail could not be started under its ids,
// it is read here (readTail) and warn gets T19.
func readBootLogTail(ctx context.Context, h Host, path string, warn func(string)) ([]byte, error) {
	cred, err := serviceCredential(filepath.Dir(path))
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
