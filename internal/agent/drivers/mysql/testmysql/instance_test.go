package testmysql_test

import (
	"context"
	"database/sql"
	"strings"
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
