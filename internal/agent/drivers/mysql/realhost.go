package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// Managed block markers in the main option file.
const (
	blockBegin = "# BEGIN dbpanel"
	blockEnd   = "# END dbpanel"
	// IncludeFile is the file written in Layout.IncludeDir. Files in an
	// included directory are read in name order and later ones win, so the
	// "zz-" prefix keeps it after stock files such as Ubuntu's mysqld.cnf.
	IncludeFile = "zz-dbpanel.cnf"
)

// NewRealHost discovers the host's MySQL layout and returns a Host for it.
// cfg.MySQLSocket overrides the discovered socket; cfg.MySQLRootPassword is
// used for the root fallback when creds holds no account. creds may be nil.
func NewRealHost(cfg config.Config, creds CredentialStore, sys probe.System, logger *slog.Logger) (Host, error) {
	l, err := DiscoverLayout(context.Background(), sys)
	if err != nil {
		return nil, err
	}
	if cfg.MySQLSocket != "" {
		l.Socket = cfg.MySQLSocket
	}
	h := newHost(l, creds, sys, logger)
	h.rootPassword = cfg.MySQLRootPassword
	h.svc = NewServiceController(l, sys, h.ping)
	return h, nil
}

// NewHost returns a Host for an already known layout and service controller.
// testmysql uses it for its instances; creds may be nil.
func NewHost(l Layout, svc ServiceController, creds CredentialStore, sys probe.System, logger *slog.Logger) Host {
	h := newHost(l, creds, sys, logger)
	h.svc = svc
	return h
}

func newHost(l Layout, creds CredentialStore, sys probe.System, logger *slog.Logger) *host {
	if logger == nil {
		logger = slog.Default()
	}
	return &host{l: l, creds: creds, sys: sys, logger: logger}
}

type host struct {
	l            Layout
	svc          ServiceController
	creds        CredentialStore
	rootPassword string
	sys          probe.System
	logger       *slog.Logger
}

func (h *host) Layout() Layout             { return h.l }
func (h *host) Service() ServiceController { return h.svc }
func (h *host) OSFamily() string           { return probe.OSFamily(h.sys) }

func (h *host) Open(ctx context.Context) (*sql.DB, error) {
	return OpenDB(ctx, h.creds, h.l.Socket, h.rootPassword, h.logger)
}

// ping is the readiness check for WaitReady.
func (h *host) ping(ctx context.Context) error {
	db, err := h.Open(ctx)
	if err != nil {
		return err
	}
	return db.Close()
}

func (h *host) WriteConfig(content string) (string, error) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if h.l.IncludeDir != "" {
		path := filepath.Join(h.l.IncludeDir, IncludeFile)
		if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
			return "", err
		}
		return path, nil
	}
	if h.l.ConfigFile == "" {
		return "", errors.New("file konfigurasi MySQL tidak diketahui")
	}
	path := h.l.ConfigFile
	// Write through symlinks (e.g. Debian alternatives) instead of replacing them.
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("baca %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	updated, err := replaceBlock(string(old), content)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := writeFileAtomic(path, []byte(updated), mode); err != nil {
		return "", err
	}
	return path, nil
}

// replaceBlock replaces the managed block in file with content, or appends
// a new block when there is none.
func replaceBlock(file, content string) (string, error) {
	block := blockBegin + "\n" + content + blockEnd + "\n"
	lines := strings.SplitAfter(file, "\n")
	begin, end := -1, -1
	for i, ln := range lines {
		switch strings.TrimSpace(ln) {
		case blockBegin:
			if begin >= 0 {
				return "", errors.New("blok dbpanel ganda; perbaiki file secara manual")
			}
			begin = i
		case blockEnd:
			if begin >= 0 && end < 0 {
				end = i
			}
		}
	}
	switch {
	case begin >= 0 && end < 0:
		return "", errors.New("blok dbpanel tidak ditutup (" + blockEnd + "); perbaiki file secara manual")
	case begin >= 0:
		return strings.Join(lines[:begin], "") + block + strings.Join(lines[end+1:], ""), nil
	}
	if file != "" && !strings.HasSuffix(file, "\n") {
		file += "\n"
	}
	if file != "" {
		file += "\n"
	}
	return file + block, nil
}

// writeFileAtomic replaces path with data through a temporary file in the
// same directory, keeping the old file's owner.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	fail := func(err error) error { return fmt.Errorf("tulis %s: %w", path, err) }
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return fail(err)
	}
	if st, err := os.Stat(path); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && (int(sys.Uid) != os.Geteuid() || int(sys.Gid) != os.Getegid()) {
			if err := f.Chown(int(sys.Uid), int(sys.Gid)); err != nil {
				f.Close()
				return fail(err)
			}
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return nil
}

func (h *host) ValidateConfig(ctx context.Context) error {
	// --defaults-file must be the first option.
	if _, err := h.sys.Output(ctx, h.l.MysqldPath, "--defaults-file="+h.l.ConfigFile, "--validate-config"); err != nil {
		return fmt.Errorf("konfigurasi MySQL tidak valid: %s", errText(err))
	}
	return nil
}

func (h *host) DiskFree(path string) (free, total uint64, err error) {
	total, free, err = h.sys.Statfs(path)
	return free, total, err
}

// stderrTail bounds how much of a command's stderr ends up in its error.
const stderrTail = 4096

func (h *host) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s gagal: %v: %s", name, err, strings.TrimSpace(string(stderr.b)))
	}
	return nil
}

// tailBuffer keeps the last stderrTail bytes written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > stderrTail {
		t.b = append(t.b[:0], t.b[len(t.b)-stderrTail:]...)
	}
	return len(p), nil
}

func (h *host) TCPReachable(ctx context.Context, host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tidak bisa terhubung ke %s: %v", addr, err)
	}
	return c.Close()
}

func (h *host) TempFile(pattern string, mode os.FileMode) (*os.File, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}
