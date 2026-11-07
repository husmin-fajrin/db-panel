package mysql_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The health monitor sweeps the driver's salvage files.
var _ monitor.SalvageSweeper = (*mysql.Driver)(nil)

// salvageRig is a driver pinned quarantined (role only) whose salvage
// files go to a temporary directory, over h (nil: no MySQL).
type salvageRig struct {
	*pinRig
	dir string
}

func newSalvageRig(t *testing.T, h mysql.Host, now func() time.Time) *salvageRig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "salvage")
	reg := commands.NewRegistry()
	d := &mysql.Driver{PinDir: pinDir(t, "quarantined", "10.0.0.1:3306"), SalvageDir: dir, Now: now,
		Cfg: config.Config{ServerID: 42}}
	if h != nil {
		d.Host = h
	}
	d.Register(reg)
	return &salvageRig{pinRig: &pinRig{t: t, reg: reg, d: d}, dir: dir}
}

// put writes a salvage file named name holding data, created at at.
func (r *salvageRig) put(name string, data []byte) {
	r.t.Helper()
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, name), data, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// allow records set as what the last mysql.leftover found, the file an
// agent reads it back from after a restart.
func (r *salvageRig) allow(set string) {
	r.t.Helper()
	r.put(mysql.LeftoverSetFile, []byte(set+"\n"))
}

func fileName(at time.Time) string { return at.UTC().Format("20060102T150405Z") + "-42.sql" }

var salvageNameRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-42\.sql$`)

// TestSalvageWritesRunnableSQL: mysql.salvage extracts the given
// transactions of the old master into a SQL file (0600 in a 0700
// directory) that, loaded into an empty server, recreates them; the file
// is listed, and deleted on request.
func TestSalvageWritesRunnableSQL(t *testing.T) {
	old := testmysql.Start(t, testmysql.Options{})
	empty := testmysql.Start(t, testmysql.Options{})
	odb := rootDB(t, old)
	before := gtidExecuted(t, odb)
	execAll(t, odb,
		"CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO toko.t VALUES (1, 'satu'), (2, 'dua')", "FLUSH BINARY LOGS",
		"UPDATE toko.t SET v = 'DUA' WHERE id = 2")
	var set string
	if err := odb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&set); err != nil {
		t.Fatal(err)
	}
	// A transaction after the set, which the file must leave out.
	execAll(t, odb, "INSERT INTO toko.t VALUES (3, 'tiga')")

	at := time.Date(2026, 11, 7, 8, 9, 10, 0, time.UTC)
	r := newSalvageRig(t, old.Host(), func() time.Time { return at })
	r.allow(set)
	f := decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}))
	if f.Name != "20261107T080910Z-42.sql" || !f.CreatedAt.Equal(at) || !f.ExpiresAt.Equal(at.Add(7*24*time.Hour)) || f.Size == 0 {
		t.Errorf("salvage file = %+v", f)
	}
	path := filepath.Join(r.dir, f.Name)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() != f.Size {
		t.Errorf("file mode %v size %d, want 0600 and %d", fi.Mode().Perm(), fi.Size(), f.Size)
	}
	if di, err := os.Stat(r.dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("salvage directory: %v %v, want 0700", di.Mode(), err)
	}
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 2 {
		t.Errorf("salvage directory holds %d entries, want only the file and the leftover set", len(entries))
	}

	sqlText, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("mysql", "--no-defaults", "--socket="+empty.Socket, "-uroot")
	cmd.Stdin = bytes.NewReader(sqlText)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("load salvage into an empty server: %v\n%s", err, out)
	}
	var got string
	if err := rootDB(t, empty).QueryRow("SELECT GROUP_CONCAT(CONCAT(id, '=', v) ORDER BY id) FROM toko.t").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1=satu,2=DUA" {
		t.Errorf("rows after loading = %q, want 1=satu,2=DUA", got)
	}

	list := decode[proto.SalvageListResult](t, r.dispatch("mysql.salvageList", nil))
	if len(list.Files) != 1 || list.Files[0] != f {
		t.Errorf("salvageList = %+v, want [%+v]", list.Files, f)
	}
	decode[struct{}](t, r.dispatch("mysql.salvageDelete", proto.SalvageDeleteParams{Name: f.Name}))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file after delete: %v", err)
	}
}

// TestSalvageRefusesBadSets: the set must parse, be non-empty and hold
// only transactions this server executed.
func TestSalvageRefusesBadSets(t *testing.T) {
	old := testmysql.Start(t, testmysql.Options{})
	r := newSalvageRig(t, old.Host(), nil)
	for _, set := range []string{"", "bukan-gtid", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-3"} {
		wantRefused(t, "salvage "+set, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}))
	}
	if entries, _ := os.ReadDir(r.dir); len(entries) != 0 {
		t.Errorf("refused salvages left %d files", len(entries))
	}
}

// TestSalvageNameValidation: only names of the salvage pattern, directly
// in the salvage directory and not symbolic links, are read or deleted.
func TestSalvageNameValidation(t *testing.T) {
	r := newSalvageRig(t, nil, nil)
	r.put(fileName(time.Now()), []byte("SELECT 1;\n"))
	outside := filepath.Join(t.TempDir(), "rahasia.sql")
	if err := os.WriteFile(outside, []byte("rahasia"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := "20261101T000000Z-42.sql"
	if err := os.Symlink(outside, filepath.Join(r.dir, link)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x.sql", "/etc/passwd", "rahasia.sql", "", "20261107T080910Z-42.sql/../../x.sql",
		"20261107T080910Z-.sql", "20261107T080910Z-42.SQL", "./20261107T080910Z-42.sql", link} {
		wantRefused(t, "read "+name, r.dispatch("mysql.salvageRead", proto.SalvageReadParams{Name: name}))
		wantRefused(t, "delete "+name, r.dispatch("mysql.salvageDelete", proto.SalvageDeleteParams{Name: name}))
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the salvage directory: %v", err)
	}
	wantRefused(t, "negative offset", r.dispatch("mysql.salvageRead", proto.SalvageReadParams{Name: fileName(time.Now()), Offset: -1}))
	wantRefused(t, "missing file", r.dispatch("mysql.salvageRead", proto.SalvageReadParams{Name: "20200101T000000Z-42.sql"}), "tidak ada")
}

// TestSalvageReadChunksAndEOF: a file is read in chunks of 512 KiB, the
// last one marked EOF; the list skips what is not a salvage file and
// sorts newest first.
func TestSalvageReadChunksAndEOF(t *testing.T) {
	r := newSalvageRig(t, nil, nil)
	data := make([]byte, 1<<20+1234)
	rand.Read(data)
	newest := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	r.put(fileName(newest), data)
	r.put(fileName(newest.Add(-time.Hour)), []byte("x"))
	r.put("catatan.txt", []byte("bukan salvage"))

	var got []byte
	for off, i := int64(0), 0; ; i++ {
		res := decode[proto.SalvageReadResult](t, r.dispatch("mysql.salvageRead", proto.SalvageReadParams{Name: fileName(newest), Offset: off}))
		if res.Size != int64(len(data)) || len(res.Data) > 512<<10 {
			t.Fatalf("chunk %d: size %d, %d bytes", i, res.Size, len(res.Data))
		}
		got = append(got, res.Data...)
		off += int64(len(res.Data))
		if res.EOF {
			if i != 2 {
				t.Errorf("EOF at chunk %d, want 2", i)
			}
			break
		}
		if i > 3 {
			t.Fatal("no EOF")
		}
	}
	if !bytes.Equal(got, data) {
		t.Error("chunks do not reassemble the file")
	}
	past := decode[proto.SalvageReadResult](t, r.dispatch("mysql.salvageRead", proto.SalvageReadParams{Name: fileName(newest), Offset: int64(len(data)) + 10}))
	if !past.EOF || len(past.Data) != 0 {
		t.Errorf("read past the end = %+v, want EOF and no data", past)
	}

	list := decode[proto.SalvageListResult](t, r.dispatch("mysql.salvageList", nil))
	if len(list.Files) != 2 || list.Files[0].Name != fileName(newest) || list.Files[1].Name != fileName(newest.Add(-time.Hour)) ||
		list.Files[0].Size != int64(len(data)) || !list.Files[0].ExpiresAt.Equal(newest.Add(7*24*time.Hour)) {
		t.Errorf("salvageList = %+v", list.Files)
	}
}

// TestSweepSalvageSevenDays: the sweep removes salvage files created 7
// days ago or more, by the driver's clock, and keeps the rest.
func TestSweepSalvageSevenDays(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	r := newSalvageRig(t, nil, func() time.Time { return now })
	old, edge, young := now.Add(-8*24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-6*24*time.Hour)
	for _, at := range []time.Time{old, edge, young} {
		r.put(fileName(at), []byte("x"))
	}
	r.put("catatan.txt", []byte("bukan salvage"))
	n, busy, err := r.d.SweepSalvage(now)
	if err != nil || busy || n != 2 {
		t.Errorf("SweepSalvage = %d, %v, %v; want 2", n, busy, err)
	}
	var left []string
	entries, _ := os.ReadDir(r.dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != fileName(young)+",catatan.txt" {
		t.Errorf("left after sweep = %v", left)
	}
	if !salvageNameRe.MatchString(fileName(young)) {
		t.Fatal("fileName does not match the pattern")
	}
}

// TestSalvageAndLeftoverOnlyWhenQuarantined: mysql.leftover and
// mysql.salvage are refused on a master, a replica and an unassigned
// server; the salvage file commands are refused on a master and an
// unassigned server but allowed on a replica (a rebuilt old master keeps
// its files); mysql.connections is allowed everywhere.
func TestSalvageAndLeftoverOnlyWhenQuarantined(t *testing.T) {
	cmds := []struct {
		cmd    string
		params any
	}{
		{"mysql.leftover", proto.LeftoverParams{NewMaster: proto.PeerCred{Host: "10.0.0.1", Port: 3306, User: "dbpanel_repl", Password: "x"}}},
		{"mysql.salvage", proto.SalvageParams{GTIDSet: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1"}},
		{"mysql.salvageList", nil},
		{"mysql.salvageRead", proto.SalvageReadParams{Name: "20261107T080910Z-42.sql"}},
		{"mysql.salvageDelete", proto.SalvageDeleteParams{Name: "20261107T080910Z-42.sql"}},
	}
	fileCmd := func(cmd string) bool { return cmd != "mysql.leftover" && cmd != "mysql.salvage" }
	for _, role := range []string{"master", "replica", ""} {
		r := newPinRig(t, nil, pinDir(t, role, "10.0.0.1:3306"))
		for _, c := range cmds {
			resp := r.dispatch(c.cmd, c.params)
			if role == "replica" && fileCmd(c.cmd) {
				if !resp.OK && resp.Error != nil && strings.Contains(resp.Error.Message, "ditolak") {
					t.Errorf("replica %s refused: %s", c.cmd, resp.Error.Message)
				}
				continue
			}
			wantRefused(t, role+" "+c.cmd, resp, "perintah "+c.cmd+" ditolak")
		}
		wantPastPins(t, role+" mysql.connections", r.dispatch("mysql.connections", nil))
	}
	r := newPinRig(t, nil, pinDir(t, "quarantined", "10.0.0.1:3306"))
	for _, c := range cmds {
		if !fileCmd(c.cmd) {
			wantPastPins(t, "quarantined "+c.cmd, r.dispatch(c.cmd, c.params))
		}
	}
}

// slowHost is a Host whose commands (mysqlbinlog) wait for release
// before writing out; entered is closed when one starts.
type slowHost struct {
	mysql.Host
	entered, release chan struct{}
}

func (h *slowHost) Exec(ctx context.Context, _ io.Reader, stdout io.Writer, _ string, _ ...string) error {
	close(h.entered)
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := io.WriteString(stdout, "SELECT 1;\n")
	return err
}

// within fails the test unless f returns within 5 seconds.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked while a salvage was running", what)
	}
}

// TestSalvageRunDoesNotBlockListOrSweep: while mysqlbinlog of a salvage
// runs, the list, a delete and the monitor's sweep go ahead; the
// salvage then finishes with what mysqlbinlog wrote.
func TestSalvageRunDoesNotBlockListOrSweep(t *testing.T) {
	old := testmysql.Start(t, testmysql.Options{})
	odb := rootDB(t, old)
	before := gtidExecuted(t, odb)
	execAll(t, odb, "CREATE DATABASE toko")
	var set string
	if err := odb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&set); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	h := &slowHost{Host: old.Host(), entered: make(chan struct{}), release: make(chan struct{})}
	r := newSalvageRig(t, h, func() time.Time { return now })
	r.allow(set)
	expired, kept := fileName(now.Add(-8*24*time.Hour)), fileName(now.Add(-time.Hour))
	r.put(expired, []byte("x"))
	r.put(kept, []byte("x"))

	done := make(chan proto.Response, 1)
	go func() { done <- r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}) }()
	select {
	case <-h.entered:
	case <-time.After(time.Minute):
		t.Fatal("salvage never started mysqlbinlog")
	}
	within(t, "salvageList", func() {
		if l := decode[proto.SalvageListResult](t, r.dispatch("mysql.salvageList", nil)); len(l.Files) != 2 {
			t.Errorf("list during a salvage = %+v", l.Files)
		}
	})
	within(t, "SweepSalvage", func() {
		if n, busy, err := r.d.SweepSalvage(now); n != 1 || busy || err != nil {
			t.Errorf("sweep during a salvage = %d, %v, %v; want 1", n, busy, err)
		}
	})
	within(t, "salvageDelete", func() {
		decode[struct{}](t, r.dispatch("mysql.salvageDelete", proto.SalvageDeleteParams{Name: kept}))
	})
	close(h.release)
	f := decode[proto.SalvageFile](t, <-done)
	if f.Name != fileName(now) || f.Size != int64(len("SELECT 1;\n")) {
		t.Errorf("salvage after the wait = %+v", f)
	}
}

// TestSalvageBoundedByLeftover (SEC-70): mysql.salvage extracts only
// transactions the last mysql.leftover found: before one ran it is
// refused, a set beyond its result is refused, the result itself is
// written; the result is kept (0600) under the salvage directory and
// still bounds a salvage after an agent restart.
func TestSalvageBoundedByLeftover(t *testing.T) {
	r := newLeftoverRig(t)
	mdb := rootDB(t, r.master)
	before := gtidExecuted(t, mdb)
	execAll(t, mdb, "INSERT INTO toko.t VALUES (10, 'sisa')")
	var left string
	if err := mdb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&left); err != nil {
		t.Fatal(err)
	}
	all := gtidExecuted(t, mdb)
	wantRefused(t, "salvage before any leftover", r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: left}),
		"Jalankan Periksa data tertinggal dulu")

	if res := r.leftover(); res.GTIDSet != left {
		t.Fatalf("leftover set = %q, want %q", res.GTIDSet, left)
	}
	wantRefused(t, "salvage of the whole history", r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: all}),
		"di luar hasil Periksa data tertinggal")
	f := decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: left}))
	if f.Size == 0 {
		t.Errorf("salvage of the leftover set = %+v", f)
	}
	path := filepath.Join(r.d.SalvageDir, mysql.LeftoverSetFile)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("leftover set file: %v %v, want 0600", fi, err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != left {
		t.Errorf("leftover set file = %q, want %q", b, left)
	}
	// A new agent run reads the set back from the file.
	again := &mysql.Driver{Host: r.master.Host(), PinDir: r.d.PinDir, SalvageDir: r.d.SalvageDir,
		Now: func() time.Time { return time.Now().Add(time.Hour) }}
	reg := commands.NewRegistry()
	again.Register(reg)
	rr := &pinRig{t: t, reg: reg, d: again}
	wantRefused(t, "salvage of the whole history after a restart", rr.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: all}),
		"di luar hasil Periksa data tertinggal")
	decode[proto.SalvageFile](t, rr.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: left}))
}

// TestSalvageOneAtATime (SEC-70): a second mysql.salvage while one runs
// is refused.
func TestSalvageOneAtATime(t *testing.T) {
	old := testmysql.Start(t, testmysql.Options{})
	odb := rootDB(t, old)
	before := gtidExecuted(t, odb)
	execAll(t, odb, "CREATE DATABASE toko")
	var set string
	if err := odb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&set); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	h := &slowHost{Host: old.Host(), entered: make(chan struct{}), release: make(chan struct{})}
	r := newSalvageRig(t, h, func() time.Time { return now })
	r.allow(set)
	done := make(chan proto.Response, 1)
	go func() { done <- r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}) }()
	select {
	case <-h.entered:
	case <-time.After(time.Minute):
		t.Fatal("salvage never started mysqlbinlog")
	}
	wantRefused(t, "second salvage", r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}), "salvage lain sedang berjalan")
	close(h.release)
	decode[proto.SalvageFile](t, <-done)
}

// TestSalvageDirectoryCap (SEC-70): with the salvage directory at its
// 8 GiB cap a salvage is refused before anything is written; with less
// room left than the salvage writes, it is stopped and its file removed.
func TestSalvageDirectoryCap(t *testing.T) {
	old := testmysql.Start(t, testmysql.Options{})
	odb := rootDB(t, old)
	before := gtidExecuted(t, odb)
	execAll(t, odb, "CREATE DATABASE toko")
	var set string
	if err := odb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&set); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	r := newSalvageRig(t, old.Host(), func() time.Time { return now })
	r.allow(set)
	// An old salvage file, sparse, filling the directory.
	big := filepath.Join(r.dir, fileName(now.Add(-time.Hour)))
	fill := func(size int64) {
		t.Helper()
		f, err := os.OpenFile(big, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	setFile, err := os.Stat(filepath.Join(r.dir, mysql.LeftoverSetFile))
	if err != nil {
		t.Fatal(err)
	}
	fill(8<<30 - setFile.Size())
	wantRefused(t, "salvage into a full directory", r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}), "batas 8 GiB")
	if entries, _ := os.ReadDir(r.dir); len(entries) != 2 {
		t.Errorf("refused salvage left %d entries, want the old file and the leftover set", len(entries))
	}
	// Ten bytes of room: the salvage writes more and is stopped.
	fill(8<<30 - setFile.Size() - 10)
	wantRefused(t, "salvage past the cap", r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}), "akan melebihi batas 8 GiB")
	if entries, _ := os.ReadDir(r.dir); len(entries) != 2 {
		t.Errorf("stopped salvage left %d entries, want the old file and the leftover set", len(entries))
	}
	// Room again: written.
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	decode[proto.SalvageFile](t, r.dispatch("mysql.salvage", proto.SalvageParams{GTIDSet: set}))
}
