package mysql_test

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// serviceLockHost is a fake host without --defaults-file (two runs) whose
// service account mysql lives in /var/lib/mysql with uid 120, gid 130
// and groups 130 and 7.
func serviceLockHost(t *testing.T) (newHost func(rootOut, homeOut string) *printDefaultsHost, lock string) {
	t.Helper()
	t.Cleanup(mysql.SetMACConfinement(func(mysql.Layout) string { return "" }))
	fake, lock := fakeLockHost(t)
	l := fake.Layout()
	l.ConfigFiles = []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "~/.my.cnf"}
	t.Cleanup(mysql.SetLookupUser(func(name string) (*user.User, error) {
		if name != "mysql" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Username: "mysql", Uid: "120", Gid: "130", HomeDir: "/var/lib/mysql"}, nil
	}))
	t.Cleanup(mysql.SetLookupGroupIDs(func(u *user.User) ([]string, error) { return []string{"130", "7"}, nil }))
	// readableBy looks the account up too: the lock file is 0644, which
	// every account may read.
	return func(rootOut, homeOut string) *printDefaultsHost {
		return &printDefaultsHost{Host: mysql.NewHost(l, nil, nil, probe.RealSystem(), nil), out: rootOut,
			outByHome: map[string]string{"/var/lib/mysql": homeOut}}
	}, lock
}

// TestPrintDefaultsHomeRunAsServiceAccount: the run with the service
// account's HOME runs as that account (uid, gid, supplementary groups);
// the root run with HOME=/nonexistent runs as the agent.
func TestPrintDefaultsHomeRunAsServiceAccount(t *testing.T) {
	newHost, lock := serviceLockHost(t)
	h := newHost("--user=mysql --init_file="+lock, "--user=mysql --init_file="+lock)
	if st := mysql.StartLockStatus(context.Background(), h); !st.Locked {
		t.Fatalf("both runs locked: %+v", st)
	}
	if len(h.creds) != 2 || h.creds[0] != nil {
		t.Fatalf("creds %+v, want the agent's then the account's", h.creds)
	}
	want := &syscall.Credential{Uid: 120, Gid: 130, Groups: []uint32{130, 7}}
	if !reflect.DeepEqual(h.creds[1], want) || homeEnv(h.envs[1]) != "/var/lib/mysql" {
		t.Errorf("home run as %+v with %q, want %+v with HOME=/var/lib/mysql", h.creds[1], h.envs[1], want)
	}
}

// TestStartLockNeedsBothRuns: the lock holds only when the root run and
// the service account's run each say so; either one that does not gives
// its own reason.
func TestStartLockNeedsBothRuns(t *testing.T) {
	newHost, lock := serviceLockHost(t)
	ctx := context.Background()
	ok := "--user=mysql --init_file=" + lock
	after := ok + " --init_file=/op/sys.sql"
	if st := mysql.StartLockStatus(ctx, newHost(after, ok)); st.Locked || st.Reason != "init_file lain dibaca sesudah baris agent: /op/sys.sql" {
		t.Errorf("root run not locked: %+v", st)
	}
	if st := mysql.StartLockStatus(ctx, newHost(ok, ok+" --init_file=/op/home.sql")); st.Locked ||
		st.Reason != "init_file lain dibaca sesudah baris agent: /op/home.sql" {
		t.Errorf("home run not locked: %+v", st)
	}
	if st := mysql.StartLockStatus(ctx, newHost("--user=mysql", ok)); st.Locked || !strings.Contains(st.Reason, "baris init_file agent tidak dibaca mysqld") {
		t.Errorf("root run without the agent's line: %+v", st)
	}
	st := mysql.StartLockStatus(ctx, newHost("--user=mysql --init_file=/op/a.sql --init_file="+lock,
		"--user=mysql --init_file=/op/b.sql --init_file="+lock))
	if !st.Locked || !reflect.DeepEqual(st.OperatorInitFiles, []string{"/op/a.sql", "/op/b.sql"}) {
		t.Errorf("both locked: %+v, want operator files of both runs", st)
	}
}

// TestPrintDefaultsOutputBounded: a run that prints more than 256 KiB
// fails, so the lock does not hold.
func TestPrintDefaultsOutputBounded(t *testing.T) {
	newHost, lock := serviceLockHost(t)
	ok := "--user=mysql --init_file=" + lock
	big := ok + strings.Repeat(" --loose-x=yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", 6000)
	for _, c := range []struct{ name, root, home string }{{"root run", big, ok}, {"home run", ok, big}} {
		st := mysql.StartLockStatus(context.Background(), newHost(c.root, c.home))
		if st.Locked || st.Reason != "konfigurasi efektif MySQL tidak bisa dibaca: keluaran --print-defaults terlalu besar" {
			t.Errorf("%s over 256 KiB: %+v", c.name, st)
		}
	}
	// Just under the bound is read.
	under := ok + strings.Repeat(" --loose-x=y", 20000)
	if st := mysql.StartLockStatus(context.Background(), newHost(under, ok)); !st.Locked {
		t.Errorf("run of %d bytes: %+v", len(under), st)
	}
}

// TestStartLockStateBounded: a hostile option list (thousands of
// init_file entries, a huge value) leaves at most 20 operator files and a
// reason of at most 300 bytes, and a health snapshot far below the
// panel's 1 MiB frame.
func TestStartLockStateBounded(t *testing.T) {
	newHost, lock := serviceLockHost(t)
	var b strings.Builder
	b.WriteString("--user=mysql")
	for i := range 2500 {
		b.WriteString(" --init_file=/op/" + strings.Repeat("é", 30) + strconv.Itoa(i) + ".sql")
	}
	list := b.String() + " --init_file=" + lock
	st := mysql.StartLockStatus(context.Background(), newHost(list, list))
	if !st.Locked || len(st.OperatorInitFiles) != 20 {
		t.Errorf("locked %v with %d operator files, want locked with 20", st.Locked, len(st.OperatorInitFiles))
	}
	long := "--user=mysql --init_file=" + lock + " --init_file=/op/" + strings.Repeat("ü", 20000)
	st = mysql.StartLockStatus(context.Background(), newHost(long, long))
	if st.Locked || len(st.Reason) > 300 || !utf8.ValidString(st.Reason) {
		t.Errorf("reason of %d bytes (valid %v), want at most 300 on a rune boundary", len(st.Reason), utf8.ValidString(st.Reason))
	}

	// The same through a health snapshot of a quarantined server.
	dir := pinDir(t, "quarantined", "10.0.0.1:3306")
	d := &mysql.Driver{Host: newHost(list, list), PinDir: dir, SeenPath: filepath.Join(t.TempDir(), "seen")}
	d.Register(commands.NewRegistry())
	snap := d.Snapshot(context.Background())
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if snap.StartLock == nil || len(snap.StartLock.OperatorInitFiles) != 20 || len(raw) > 64<<10 {
		t.Errorf("snapshot of %d bytes with start lock %+v, want 20 operator files and far below 1 MiB", len(raw), snap.StartLock)
	}
	var _ proto.HealthSnapshot = snap
}

// TestPrintDefaultsFIFOBounded: an !include of a FIFO in the service
// account's ~/.my.cnf blocks mysqld; the run ends at its 10 s timeout and
// the lock does not hold.
func TestPrintDefaultsFIFOBounded(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("runs mysqld as another account")
	}
	for _, f := range []string{"/etc/my.cnf", "/etc/mysql/my.cnf"} {
		if _, err := os.Stat(f); err == nil {
			t.Skipf("%s exists: --print-defaults without --defaults-file would read this machine's MySQL config", f)
		}
	}
	t.Cleanup(mysql.SetMACConfinement(func(mysql.Layout) string { return "" }))
	inst := testmysql.Start(t, testmysql.Options{})
	ctx := context.Background()
	if err := mysql.WriteStartLock(ctx, inst.Host()); err != nil {
		t.Fatal(err)
	}
	sys := openDir(t)
	if err := os.WriteFile(filepath.Join(sys, "my.cnf"), []byte("!include "+inst.ConfigFile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYSQL_HOME", sys)
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	home := openDir(t)
	t.Cleanup(mysql.SetLookupUser(func(name string) (*user.User, error) {
		if name != "nobody" {
			return user.Lookup(name)
		}
		u := *nobody
		u.HomeDir = home
		return &u, nil
	}))
	fifo := filepath.Join(home, "f")
	if err := syscall.Mkfifo(fifo, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".my.cnf"), []byte("!include "+fifo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := inst.Layout()
	l.ConfigFiles = []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "~/.my.cnf"}
	h := mysql.NewHost(l, inst.Service(), nil, probe.RealSystem(), nil)
	start := time.Now()
	st := mysql.StartLockStatus(ctx, h)
	took := time.Since(start)
	if st.Locked || !strings.Contains(st.Reason, "konfigurasi efektif MySQL tidak bisa dibaca") {
		t.Errorf("FIFO include: %+v", st)
	}
	if took < 9*time.Second || took > 15*time.Second {
		t.Errorf("the round took %v, want about the 10 s timeout", took)
	}
}
