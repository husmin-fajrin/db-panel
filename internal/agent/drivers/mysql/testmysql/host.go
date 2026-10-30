package testmysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// ServiceKind is Layout.ServiceKind for test instances.
const ServiceKind = "test"

// Layout describes the instance the way DiscoverLayout describes a real
// installation. ConfigFile has no !includedir, so WriteConfig uses the
// managed block in my.cnf, which the next restart reads.
func (i *Instance) Layout() mysql.Layout {
	return mysql.Layout{
		MysqldPath:  i.mysqld,
		ConfigFile:  i.ConfigFile,
		ConfigFiles: []string{i.ConfigFile},
		DataDir:     i.DataDir,
		Socket:      i.Socket,
		PIDFile:     i.PIDFile,
		Port:        i.Port,
		ServiceKind: ServiceKind,
		ServiceName: "testmysql",
	}
}

// Host is the mysql.Host for this instance with no stored credentials, so
// Open logs in as root over the socket.
func (i *Instance) Host() mysql.Host { return i.HostWith(nil) }

// HostWith is Host with an admin credential store.
func (i *Instance) HostWith(creds mysql.CredentialStore) mysql.Host {
	return mysql.NewHost(i.Layout(), i.svc, creds, probe.RealSystem(), nil)
}

// Service is the instance's supervisor.
func (i *Instance) Service() mysql.ServiceController { return i.svc }

// supervisor plays the role of systemd for an instance: it starts mysqld
// with the instance's my.cnf. Nothing restarts the server by itself; after
// CLONE (which shuts the server down with error 3707 when no supervisor
// restarts it) callers use Start.
type supervisor struct {
	inst *Instance
	mu   sync.Mutex
}

func (s *supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.start(ctx)
}

func (s *supervisor) start(ctx context.Context) error {
	if _, ok := s.pid(); ok {
		return nil
	}
	i := s.inst
	// --daemonize returns once the server is ready (or failed).
	cmd := exec.CommandContext(ctx, i.mysqld, "--defaults-file="+i.ConfigFile, "--daemonize")
	cmd.Dir = i.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("start mysqld: %v\n%s\n%s", err, out, i.logTail())
	}
	return nil
}

func (s *supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop(ctx)
}

// stop sends SIGTERM (a clean shutdown) and, if the server is still up when
// ctx ends, SIGKILL.
func (s *supervisor) stop(ctx context.Context) error {
	pid, ok := s.pid()
	if !ok {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop mysqld %d: %v", pid, err)
	}
	if s.waitExit(ctx, pid) {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !s.waitExit(kctx, pid) {
		return fmt.Errorf("mysqld %d did not exit", pid)
	}
	return fmt.Errorf("mysqld %d killed after shutdown timeout", pid)
}

func (s *supervisor) waitExit(ctx context.Context, pid int) bool {
	for {
		if !s.running(pid) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *supervisor) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stop(ctx); err != nil {
		return err
	}
	return s.start(ctx)
}

func (s *supervisor) IsActive(ctx context.Context) (bool, error) {
	_, ok := s.pid()
	return ok, nil
}

func (s *supervisor) WaitReady(ctx context.Context, timeout time.Duration) error {
	return mysql.WaitReady(ctx, timeout, s.ping)
}

func (s *supervisor) ping(ctx context.Context) error {
	db, err := sql.Open("mysql", s.inst.DSN())
	if err != nil {
		return err
	}
	defer db.Close()
	return db.PingContext(ctx)
}

// pid returns the pid in the pid file when that process is this instance's
// mysqld.
func (s *supervisor) pid() (int, bool) {
	b, err := os.ReadFile(s.inst.PIDFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, s.running(pid)
}

// running reports whether pid is a live mysqld started with this instance's
// my.cnf. A zombie has an empty cmdline (the container's init may never reap
// the daemonised server), and a reused pid has another one.
func (s *supervisor) running(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && strings.Contains(string(b), "--defaults-file="+s.inst.ConfigFile+"\x00")
}
