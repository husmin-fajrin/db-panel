package mysql

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// ServiceController starts and stops the MySQL server the way the host runs it.
type ServiceController interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	IsActive(ctx context.Context) (bool, error)
	// WaitReady polls until the server accepts connections or timeout passes.
	WaitReady(ctx context.Context, timeout time.Duration) error
}

// ErrUnknownService is returned when the host's way of running MySQL is unknown.
var ErrUnknownService = errors.New("cara menjalankan service MySQL tidak dikenali")

// ReadyPollInterval is how often WaitReady retries.
const ReadyPollInterval = 500 * time.Millisecond

// NewServiceController returns the controller for l.ServiceKind. ready
// reports whether the server accepts connections (used by WaitReady).
func NewServiceController(l Layout, sys probe.System, ready func(ctx context.Context) error) ServiceController {
	return &service{l: l, sys: sys, ready: ready}
}

type service struct {
	l     Layout
	sys   probe.System
	ready func(ctx context.Context) error
}

func (s *service) run(ctx context.Context, name string, args ...string) error {
	if _, err := s.sys.Output(ctx, name, args...); err != nil {
		return fmt.Errorf("%s %s gagal: %s", name, strings.Join(args, " "), errText(err))
	}
	return nil
}

// errText includes a command's stderr when it is available.
func errText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return err.Error() + ": " + strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

func (s *service) Start(ctx context.Context) error   { return s.do(ctx, "start") }
func (s *service) Stop(ctx context.Context) error    { return s.do(ctx, "stop") }
func (s *service) Restart(ctx context.Context) error { return s.do(ctx, "restart") }

func (s *service) do(ctx context.Context, action string) error {
	switch s.l.ServiceKind {
	case ServiceSystemd:
		return s.run(ctx, "systemctl", action, s.l.ServiceName)
	case ServiceInitScript:
		return s.run(ctx, s.l.ServiceName, action)
	case ServiceMysqldSafe:
		switch action {
		case "start":
			return s.startSafe(ctx)
		case "stop":
			return s.stopSafe(ctx)
		default:
			if err := s.stopSafe(ctx); err != nil {
				return err
			}
			return s.startSafe(ctx)
		}
	}
	return ErrUnknownService
}

func (s *service) IsActive(ctx context.Context) (bool, error) {
	if s.l.ServiceKind == ServiceSystemd {
		// is-active exits non-zero for every state but "active".
		out, _ := s.sys.Output(ctx, "systemctl", "is-active", s.l.ServiceName)
		return strings.TrimSpace(string(out)) == "active", nil
	}
	_, alive := s.pid()
	return alive, nil
}

// pid reads the server's pid file and reports whether that process runs.
func (s *service) pid() (int, bool) {
	if s.l.PIDFile == "" {
		return 0, false
	}
	b, err := s.sys.ReadFile(s.l.PIDFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, s.sys.Exists("/proc/" + strconv.Itoa(pid))
}

func (s *service) startSafe(ctx context.Context) error {
	if _, alive := s.pid(); alive {
		return nil
	}
	safe := filepath.Join(filepath.Dir(s.l.MysqldPath), "mysqld_safe")
	if !s.sys.Exists(safe) {
		p, err := s.sys.LookPath("mysqld_safe")
		if err != nil {
			return errors.New("mysqld_safe tidak ditemukan")
		}
		safe = p
	}
	// mysqld_safe stays in the foreground; the shell detaches it, and the
	// redirect keeps it from holding our output pipe open.
	return s.run(ctx, "sh", "-c", `nohup "$0" --defaults-file="$1" >/dev/null 2>&1 &`, safe, s.l.ConfigFile)
}

// stopSafe ends mysqld with SIGTERM; mysqld_safe exits after a clean shutdown.
func (s *service) stopSafe(ctx context.Context) error {
	pid, alive := s.pid()
	if !alive {
		return nil
	}
	if err := s.run(ctx, "kill", strconv.Itoa(pid)); err != nil {
		return err
	}
	for {
		if !s.sys.Exists("/proc/" + strconv.Itoa(pid)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("MySQL (pid %d) belum berhenti: %w", pid, ctx.Err())
		case <-time.After(ReadyPollInterval):
		}
	}
}

func (s *service) WaitReady(ctx context.Context, timeout time.Duration) error {
	return WaitReady(ctx, timeout, s.ready)
}

// WaitReady calls ready every ReadyPollInterval until it succeeds or timeout
// passes.
func WaitReady(ctx context.Context, timeout time.Duration, ready func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := ready(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("MySQL belum siap setelah %s: %v", timeout, err)
		case <-time.After(ReadyPollInterval):
		}
	}
}
