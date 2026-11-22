package testmysql_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
)

func variable(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT @@global." + name).Scan(&v); err != nil {
		t.Fatalf("@@%s: %v", name, err)
	}
	return v
}

// TestHostConfigRestartAndStart: ExtraArgs override the baseline, the
// managed block written through Host is validated and read on restart, and
// a stopped server (as after CLONE) comes back with Start.
func TestHostConfigRestartAndStart(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"--gtid-mode=OFF", "--enforce-gtid-consistency=OFF"}})
	ctx := context.Background()
	h := inst.Host()
	db, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := variable(t, db, "gtid_mode"); v != "OFF" {
		t.Fatalf("gtid_mode = %s, want OFF from ExtraArgs", v)
	}

	if _, err := h.WriteConfig("[mysqld]\nno_such_option=1\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateConfig(ctx); err == nil || !strings.Contains(err.Error(), "no_such_option") {
		t.Fatalf("ValidateConfig with a bad option = %v", err)
	}
	path, err := h.WriteConfig("[mysqld]\ngtid_mode=ON\nenforce_gtid_consistency=ON\nsuper_read_only=ON\n")
	if err != nil {
		t.Fatal(err)
	}
	if path != inst.ConfigFile {
		t.Errorf("WriteConfig path = %q, want %q", path, inst.ConfigFile)
	}
	if err := h.ValidateConfig(ctx); err != nil {
		t.Fatal(err)
	}

	svc := h.Service()
	if err := svc.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.WaitReady(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := variable(t, db, "gtid_mode"); v != "ON" {
		t.Errorf("gtid_mode after restart = %s, want ON from the managed block", v)
	}
	if v := variable(t, db, "super_read_only"); v != "1" {
		t.Errorf("super_read_only after restart = %s, want 1", v)
	}

	if err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if active, _ := svc.IsActive(ctx); active {
		t.Fatal("IsActive after Stop")
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.WaitReady(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if active, _ := svc.IsActive(ctx); !active {
		t.Error("not active after Start")
	}
	if err := svc.Start(ctx); err != nil { // already running: no-op
		t.Fatal(err)
	}
}

// recordingTB is a testing.TB that keeps what Errorf and Logf were given
// instead of failing the test.
type recordingTB struct {
	testing.TB
	mu         sync.Mutex
	errs, logs []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

// TestStopFailureNamesInstanceAndKeepsLogTail: a "mysqld" that ignores
// SIGTERM (a shell started with the instance's --defaults-file) is killed
// after the stop's wait; the error names the pid, the directory, the port
// and the server id (F5), and before the directory goes the cleanup logs
// what it read before the stop (processlist, clone_status: here the reads
// failed, which is noted, not fatal) and the end of the error log.
func TestStopFailureNamesInstanceAndKeepsLogTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inst")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	i := testmysql.NewStopped(dir, 33999, 4242)
	if err := os.WriteFile(i.ErrorLog, []byte("[Note] [MY-010067] Giving 3 client threads a chance to die gracefully\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done", "sh", "--defaults-file="+i.ConfigFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	pid := cmd.Process.Pid
	// Reap the shell once killed, so it does not stay a zombie.
	go cmd.Wait()
	if err := os.WriteFile(i.PIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the trap is set

	rec := &recordingTB{TB: t}
	i.Teardown(rec, time.Second)
	want := fmt.Sprintf("mysqld %d (dir %s, port %d, server id %d) killed after shutdown timeout", pid, dir, 33999, 4242)
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], want) {
		t.Errorf("errors %q, want one holding %q", rec.errs, want)
	}
	logs := strings.Join(rec.logs, "\n")
	for _, part := range []string{"processlist", "clone_status", "Giving 3 client threads a chance to die gracefully", dir} {
		if !strings.Contains(logs, part) {
			t.Errorf("logs miss %q:\n%s", part, logs)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("instance directory after the cleanup: %v, want removed", err)
	}
}
