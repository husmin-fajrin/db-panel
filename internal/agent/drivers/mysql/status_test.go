package mysql_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
)

func TestStatusAgainstInstance(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ServerID: 4242})
	ctx := context.Background()

	db, err := inst.Host().Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st, err := mysql.Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Version, "8.") || st.Series == "" {
		t.Errorf("Version = %q, Series = %q", st.Version, st.Series)
	}
	if st.ServerID != 4242 {
		t.Errorf("ServerID = %d, want 4242", st.ServerID)
	}
	if !st.LogBin || st.GTIDMode != "ON" || !st.EnforceGTID {
		t.Errorf("LogBin=%v GTIDMode=%q EnforceGTID=%v", st.LogBin, st.GTIDMode, st.EnforceGTID)
	}
	if st.Port != inst.Port || st.Socket != inst.Socket || st.BindAddress != "127.0.0.1" {
		t.Errorf("Port=%d Socket=%q BindAddress=%q", st.Port, st.Socket, st.BindAddress)
	}
	if st.BinlogDumpThreads != 0 || st.Replica != nil {
		t.Errorf("BinlogDumpThreads=%d Replica=%+v", st.BinlogDumpThreads, st.Replica)
	}
	if st.ThreadsRunning < 1 {
		t.Errorf("ThreadsRunning=%d", st.ThreadsRunning)
	}

	// A write shows up in gtid_executed; a second later Uptime has advanced.
	time.Sleep(1100 * time.Millisecond)
	raw, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "CREATE DATABASE status_probe"); err != nil {
		t.Fatal(err)
	}
	st, err = mysql.Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.GTIDExecuted, ":1") {
		t.Errorf("GTIDExecuted = %q, want a transaction", st.GTIDExecuted)
	}
	if st.UptimeSec < 1 {
		t.Errorf("UptimeSec = %d", st.UptimeSec)
	}
}
