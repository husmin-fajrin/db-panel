package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

// healthDriver is a Driver over inst with root stored as the admin
// account, reading the real host.
func healthDriver(inst *testmysql.Instance) (*mysql.Driver, *memCreds) {
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	return &mysql.Driver{Host: inst.HostWith(creds), Creds: creds, Sys: probe.RealSystem()}, creds
}

// otherRootConns lists the root connections other than db's own: with no
// other client, the monitor's.
func otherRootConns(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT ID FROM information_schema.processlist
		WHERE USER = 'root' AND ID <> CONNECTION_ID() ORDER BY ID`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestSnapshotMaster(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"binlog-expire-logs-seconds=86400"}})
	root := rootDB(t, inst)
	root.SetMaxOpenConns(1)
	execAll(t, root, "CREATE DATABASE health_probe")

	d, creds := healthDriver(inst)
	s := d.Snapshot(context.Background())
	if !s.MySQLOK || s.MySQLError != "" {
		t.Fatalf("MySQLOK=%v MySQLError=%q", s.MySQLOK, s.MySQLError)
	}
	if !strings.HasPrefix(s.Version, "8.") || s.ServerUUID == "" || s.UptimeSec < 0 {
		t.Errorf("Version=%q ServerUUID=%q UptimeSec=%d", s.Version, s.ServerUUID, s.UptimeSec)
	}
	if s.MaxConnections <= 0 || s.ThreadsConnected < 1 || s.ThreadsRunning < 1 {
		t.Errorf("MaxConnections=%d ThreadsConnected=%d ThreadsRunning=%d", s.MaxConnections, s.ThreadsConnected, s.ThreadsRunning)
	}
	if !s.LogBin || s.ReadOnly || s.SuperReadOnly || !strings.Contains(s.GTIDExecuted, ":1") {
		t.Errorf("LogBin=%v ReadOnly=%v SuperReadOnly=%v GTIDExecuted=%q", s.LogBin, s.ReadOnly, s.SuperReadOnly, s.GTIDExecuted)
	}
	if s.BinlogExpireSec != 86400 || s.BinlogTotalBytes == 0 || s.BinlogOldestAgeSec == nil || *s.BinlogOldestAgeSec < 0 {
		t.Errorf("BinlogExpireSec=%d BinlogTotalBytes=%d BinlogOldestAgeSec=%v", s.BinlogExpireSec, s.BinlogTotalBytes, s.BinlogOldestAgeSec)
	}
	if s.Replica != nil || s.BinlogDumpThreads != 0 {
		t.Errorf("Replica=%+v BinlogDumpThreads=%d", s.Replica, s.BinlogDumpThreads)
	}
	if s.DiskTotal == 0 || s.DiskFree == 0 || s.DiskFree > s.DiskTotal {
		t.Errorf("DiskTotal=%d DiskFree=%d", s.DiskTotal, s.DiskFree)
	}
	if s.MemTotal == 0 || s.MemAvailable == 0 || s.MemAvailable > s.MemTotal || s.Load1 < 0 {
		t.Errorf("MemTotal=%d MemAvailable=%d Load1=%v", s.MemTotal, s.MemAvailable, s.Load1)
	}
	if s.AgentVersion != version.Version {
		t.Errorf("AgentVersion=%q, want %q", s.AgentVersion, version.Version)
	}

	// The next reading reuses the monitor's one connection.
	conns := otherRootConns(t, root)
	if len(conns) != 1 {
		t.Fatalf("monitor connections = %v, want one", conns)
	}
	if s := d.Snapshot(context.Background()); !s.MySQLOK {
		t.Fatalf("second snapshot: %q", s.MySQLError)
	}
	if again := otherRootConns(t, root); !slices.Equal(again, conns) {
		t.Errorf("monitor connections = %v after a second snapshot, want %v", again, conns)
	}

	// Another stored account replaces the connection.
	execAll(t, root, "CREATE USER 'monitor2'@'localhost' IDENTIFIED BY 'pw-mon-2'",
		"GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'monitor2'@'localhost'")
	if err := creds.Save(proto.ConnectParams{User: "monitor2", Password: "pw-mon-2"}); err != nil {
		t.Fatal(err)
	}
	if s := d.Snapshot(context.Background()); !s.MySQLOK {
		t.Fatalf("snapshot as monitor2: %q", s.MySQLError)
	}
	if again := otherRootConns(t, root); len(again) != 0 {
		t.Errorf("root connections = %v after the account changed, want none", again)
	}
}

func TestSnapshotReplica(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	follow(t, master, replica)

	rd, _ := healthDriver(replica)
	s := rd.Snapshot(context.Background())
	if !s.MySQLOK {
		t.Fatalf("MySQLError=%q", s.MySQLError)
	}
	if s.Replica == nil {
		t.Fatal("Replica = nil on a replica")
	}
	if s.Replica.SQLRunning != "Yes" || (s.Replica.IORunning != "Yes" && s.Replica.IORunning != "Connecting") ||
		s.Replica.SourcePort != master.Port || s.Replica.SourceHost == "" ||
		// follow configures no certificate verification; the panel's
		// tls_verified follows this field (SEC-63c).
		s.Replica.SSLVerifyServerCert {
		t.Errorf("Replica = %+v", s.Replica)
	}

	// The master sees the replica's dump thread.
	md, _ := healthDriver(master)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ms := md.Snapshot(context.Background())
		if ms.MySQLOK && ms.BinlogDumpThreads == 1 && ms.Replica == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("master: MySQLOK=%v MySQLError=%q BinlogDumpThreads=%d Replica=%+v",
				ms.MySQLOK, ms.MySQLError, ms.BinlogDumpThreads, ms.Replica)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// checkDown fails unless s is a reading of a MySQL that did not answer:
// the error text, no MySQL fields, the host fields still read.
func checkDown(t *testing.T, s proto.HealthSnapshot) {
	t.Helper()
	t.Logf("MySQLError: %s", s.MySQLError)
	if s.MySQLOK || !strings.HasPrefix(s.MySQLError, "MySQL tidak menjawab: ") {
		t.Errorf("MySQLOK=%v MySQLError=%q", s.MySQLOK, s.MySQLError)
	}
	if s.Version != "" || s.LogBin || s.MaxConnections != 0 || s.BinlogOldestAgeSec != nil || s.Replica != nil {
		t.Errorf("MySQL fields filled while down: %+v", s)
	}
	if s.DiskTotal == 0 || s.MemTotal == 0 || s.AgentVersion != version.Version {
		t.Errorf("DiskTotal=%d MemTotal=%d AgentVersion=%q", s.DiskTotal, s.MemTotal, s.AgentVersion)
	}
}

func TestSnapshotMySQLDownIsNotError(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	d, _ := healthDriver(inst)
	ctx := context.Background()
	if s := d.Snapshot(ctx); !s.MySQLOK {
		t.Fatalf("MySQLError=%q", s.MySQLError)
	}

	// A frozen server holds the open connection without answering: the
	// reading gives up after 5 s.
	b, err := os.ReadFile(inst.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	cont := func() { _ = syscall.Kill(pid, syscall.SIGCONT) }
	t.Cleanup(cont)
	start := time.Now()
	checkDown(t, d.Snapshot(ctx))
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("snapshot of a frozen server took %v", took)
	}
	cont()
	if s := d.Snapshot(ctx); !s.MySQLOK {
		t.Fatalf("after SIGCONT: MySQLError=%q", s.MySQLError)
	}

	// A stopped server refuses connections.
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := inst.Service().Stop(sctx); err != nil {
		t.Fatal(err)
	}
	checkDown(t, d.Snapshot(ctx))

	// Once it is back the next reading connects again.
	if err := inst.Service().Start(sctx); err != nil {
		t.Fatal(err)
	}
	if err := inst.Service().WaitReady(sctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if s := d.Snapshot(ctx); !s.MySQLOK || s.Version == "" {
		t.Fatalf("after restart: MySQLOK=%v MySQLError=%q", s.MySQLOK, s.MySQLError)
	}
}

// openFails is a Host whose Open fails the test: Snapshot must not log in.
type openFails struct {
	mysql.Host
	t       *testing.T
	datadir string
}

func (h openFails) Layout() mysql.Layout { return mysql.Layout{DataDir: h.datadir} }
func (h openFails) Open(context.Context) (*sql.DB, error) {
	h.t.Error("Open called without a stored account")
	return nil, errors.New("not here")
}

// noCreds is a CredentialStore holding no account.
type noCreds struct{}

func (noCreds) Load() (proto.ConnectParams, bool, error) { return proto.ConnectParams{}, false, nil }
func (noCreds) Save(proto.ConnectParams) error           { return errors.New("not supported") }
func (noCreds) Clear() error                             { return nil }

// TestSnapshotNoCredential: no MySQL, or MySQL that "Hubungkan MySQL" has
// not stored an account for, is not watched, even when root could log in
// over the socket.
func TestSnapshotNoCredential(t *testing.T) {
	for name, creds := range map[string]mysql.CredentialStore{"no store": nil, "nothing stored": noCreds{}} {
		t.Run(name, func(t *testing.T) {
			d := &mysql.Driver{Host: openFails{t: t, datadir: t.TempDir()}, Creds: creds, Sys: probe.RealSystem()}
			s := d.Snapshot(context.Background())
			if s.MySQLOK || s.MySQLError != "belum terhubung" || s.Version != "" {
				t.Errorf("MySQLOK=%v MySQLError=%q Version=%q", s.MySQLOK, s.MySQLError, s.Version)
			}
			if s.AgentVersion != version.Version || s.MemTotal == 0 || s.DiskTotal == 0 {
				t.Errorf("AgentVersion=%q MemTotal=%d DiskTotal=%d", s.AgentVersion, s.MemTotal, s.DiskTotal)
			}
		})
	}
	t.Run("no MySQL", func(t *testing.T) {
		s := (&mysql.Driver{Creds: &memCreds{}, Sys: probe.RealSystem()}).Snapshot(context.Background())
		if s.MySQLOK || s.MySQLError != "belum terhubung" || s.AgentVersion != version.Version {
			t.Errorf("MySQLOK=%v MySQLError=%q AgentVersion=%q", s.MySQLOK, s.MySQLError, s.AgentVersion)
		}
		if s.MemTotal == 0 || s.DiskTotal != 0 {
			t.Errorf("MemTotal=%d DiskTotal=%d (no data directory)", s.MemTotal, s.DiskTotal)
		}
	})
	t.Run("no probe.System", func(t *testing.T) {
		if s := (&mysql.Driver{}).Snapshot(context.Background()); s.MySQLError != "belum terhubung" || s.MemTotal != 0 {
			t.Errorf("MySQLError=%q MemTotal=%d", s.MySQLError, s.MemTotal)
		}
	})
}

func TestBinlogOldestAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{
		"binlog.000001":      100 * time.Second,
		"binlog.000002":      10 * time.Second,
		"binlog.index":       1000 * time.Second, // not a binary log
		"binlog.000003x":     2000 * time.Second,
		"other.000001":       3000 * time.Second,
		"binlog.000004.gz":   4000 * time.Second,
		"relay-bin.000001":   5000 * time.Second,
		"binlog.000005.tmp~": 6000 * time.Second,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	got := mysql.BinlogOldestAge(filepath.Join(dir, "binlog"), now)
	if got == nil || *got != 100 {
		t.Errorf("BinlogOldestAge = %v, want 100", got)
	}
}

func TestBinlogOldestAgeUnreadable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "binlog.index"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		"",
		"/nonexistent-dbpanel/binlog",
		filepath.Join(dir, "binlog"), // no binary log yet, only the index
	} {
		if got := mysql.BinlogOldestAge(base, time.Now()); got != nil {
			t.Errorf("BinlogOldestAge(%q) = %d, want nil", base, *got)
		}
	}
}

// procSystem is a probe.System with fixed /proc files and one file system.
type procSystem struct {
	probe.System
	files      map[string]string
	statfsPath string
}

func (s procSystem) ReadFile(p string) ([]byte, error) {
	if c, ok := s.files[p]; ok {
		return []byte(c), nil
	}
	return nil, os.ErrNotExist
}

func (s procSystem) Statfs(p string) (uint64, uint64, error) {
	if p != s.statfsPath {
		return 0, 0, errors.New("no such file system")
	}
	return 500 << 30, 100 << 30, nil
}

func TestSystemStatsFromFakeProc(t *testing.T) {
	sys := procSystem{
		files: map[string]string{
			"/proc/meminfo": "MemTotal:        8123456 kB\nMemFree:          100000 kB\nMemAvailable:    4000000 kB\nBuffers:           1000 kB\n",
			"/proc/loadavg": "0.42 0.30 0.25 2/345 6789\n",
		},
		statfsPath: "/var/lib/mysql",
	}
	diskTotal, diskFree, memTotal, memAvail, load1 := mysql.SystemStats(sys, "/var/lib/mysql")
	if diskTotal != 500<<30 || diskFree != 100<<30 {
		t.Errorf("disk = %d/%d", diskFree, diskTotal)
	}
	if memTotal != 8123456*1024 || memAvail != 4000000*1024 {
		t.Errorf("mem = %d/%d", memAvail, memTotal)
	}
	if load1 != 0.42 {
		t.Errorf("load1 = %v", load1)
	}

	// Nothing readable: zeros, no panic.
	diskTotal, diskFree, memTotal, memAvail, load1 = mysql.SystemStats(procSystem{files: map[string]string{
		"/proc/meminfo": "garbage\nMemTotal: x kB\n",
		"/proc/loadavg": "\n",
	}}, "/elsewhere")
	if diskTotal != 0 || diskFree != 0 || memTotal != 0 || memAvail != 0 || load1 != 0 {
		t.Errorf("unreadable = %d %d %d %d %v, want zeros", diskTotal, diskFree, memTotal, memAvail, load1)
	}
	// No data directory known: the disk is not read.
	if diskTotal, _, _, _, _ := mysql.SystemStats(sys, ""); diskTotal != 0 {
		t.Errorf("DiskTotal = %d without a data directory", diskTotal)
	}
}

// TestSnapshotReportsPins: every reading carries the pinned role and
// source, whatever MySQL answers; an untrusted pin file is reported as
// PinError without the pin directory, and both pins empty.
func TestSnapshotReportsPins(t *testing.T) {
	snap := func(d *mysql.Driver) proto.HealthSnapshot {
		t.Helper()
		if d.PinDir != "" {
			d.Register(commands.NewRegistry())
		}
		return d.Snapshot(context.Background())
	}
	cases := []struct {
		name, role, source string
		wantRole, wantSrc  string
	}{
		{"master", "master", "", "master", ""},
		{"replica", "replica", "10.0.0.1:3306", "replica", "10.0.0.1:3306"},
		{"quarantined IPv6", "quarantined", "[fd00::1]:3307", "quarantined", "[fd00::1]:3307"},
		{"no pin yet", "", "", "unassigned", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := snap(&mysql.Driver{PinDir: pinDir(t, c.role, c.source)})
			if s.RolePin != c.wantRole || s.SourcePin != c.wantSrc || s.PinError != "" {
				t.Errorf("RolePin=%q SourcePin=%q PinError=%q; want %q, %q, none", s.RolePin, s.SourcePin, s.PinError, c.wantRole, c.wantSrc)
			}
			if s.MySQLError != "belum terhubung" {
				t.Errorf("MySQLError=%q: the pins are read without MySQL", s.MySQLError)
			}
		})
	}

	t.Run("untrusted file", func(t *testing.T) {
		dir := pinDir(t, "master", "")
		d := &mysql.Driver{PinDir: dir}
		path := filepath.Join(dir, mysql.RoleFile)
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		s := snap(d)
		if s.RolePin != "" || s.SourcePin != "" || s.PinError == "" {
			t.Fatalf("RolePin=%q SourcePin=%q PinError=%q; want no pins and an error", s.RolePin, s.SourcePin, s.PinError)
		}
		if strings.Contains(s.PinError, dir) || !strings.Contains(s.PinError, mysql.RoleFile) || !strings.Contains(s.PinError, "0666") {
			t.Errorf("PinError = %q; want the file's name and mode without %s", s.PinError, dir)
		}
		// Fixed on disk: the next reading reports the pin again.
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if s := d.Snapshot(context.Background()); s.RolePin != "master" || s.PinError != "" {
			t.Errorf("after the fix: RolePin=%q PinError=%q", s.RolePin, s.PinError)
		}
	})

	t.Run("no pins", func(t *testing.T) {
		if s := snap(&mysql.Driver{}); s.RolePin != "" || s.SourcePin != "" || s.PinError != "" {
			t.Errorf("RolePin=%q SourcePin=%q PinError=%q; want all empty", s.RolePin, s.SourcePin, s.PinError)
		}
	})
}
