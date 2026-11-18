package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The operator texts of drop-database, as spec 5.8 words them.
const (
	c1 = "perintah ini hanya untuk server yang dikarantina (quarantined); peran server ini: %s"
	c2 = "kunci baca-saja server ini sedang tidak terpasang penuh; tunggu beberapa detik (agent memasangnya lagi) lalu ulangi"
	c6 = "berkas cadangan %s sudah ada; tidak ada yang ditimpa dan tidak ada yang dihapus. Jalankan perintah ini lagi."
	c7 = "Peringatan: folder %s bukan milik root atau bisa ditulis pengguna lain; cadangan di sana bisa diubah atau dihapus pengguna lain."
	// p1 replaces C8 (spec 7, the --tanpa-cadangan block): the name, the
	// cause, then the name again as one shell word.
	p1  = "Cadangan database %s tidak bisa dibuat (%s); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan: sudo dbpanel agent drop-database %s --tanpa-cadangan"
	p2  = "Cadangan database %s tidak bisa dibuat (%s). Dengan --tanpa-cadangan database itu dihapus TANPA cadangan dan tidak bisa dikembalikan. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: "
	c9  = `Periksa dulu di master baru (hanya membaca): mysql -e 'SHOW DATABASES LIKE "%s"'. Jawabannya harus kosong.`
	c9b = `Periksa dulu di master baru (hanya membaca): mysql -e 'SELECT schema_name FROM information_schema.schemata WHERE HEX(schema_name) = "%s"'. Jawabannya harus kosong.`
	c10 = "Agent tidak bisa memastikan database ini tidak ada di master baru. Bila database ini ada di master baru, menghapusnya di sini merusak replikasi server ini. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: "
	c11 = "Keadaan server ini berubah selama Anda membaca; tidak ada yang diubah. Jalankan perintah ini lagi."
	c12 = "Database %s sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi. Cadangan tadi tetap ada di %s."
	c13 = "Perintah dihentikan (sinyal); kunci baca-saja server ini sudah terpasang lagi. Jalankan perintah ini lagi bila pekerjaannya belum selesai."
	c14 = "Perintah ini sudah membuka kunci baca-saja selama 10 menit dan dihentikan; kunci baca-saja sudah terpasang lagi. Jalankan perintah ini lagi."
	c15 = `Selesai. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau mulai lagi Bangun ulang sebagai replika.`
)

// The texts this task words itself (listed in its report).
const (
	wantNoSuchDatabase = "Database %s tidak ada di server ini; tidak ada yang diubah."
	wantSystemSchema   = "Database %s adalah skema sistem MySQL; perintah ini tidak menghapusnya."
	wantCancelled      = "Dibatalkan; tidak ada yang dihapus. Cadangan tadi tetap ada di %s."
)

// fixedNow pins the backup file's timestamp.
var fixedNow = time.Date(2026, 11, 17, 8, 15, 0, 0, time.UTC)

const fixedStamp = "20261117T081500Z"

// runHook runs drop-database with input in; at the first read of the
// input, hook gets what stdout holds by then.
func (r *dropRig) runHook(in string, hook func(stdoutSoFar string), args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	input := &hookReader{r: strings.NewReader(in), f: func() { hook(out.String()) }}
	code = RunCLIWith(append(append([]string{"drop-database"}, args...), "--dir", r.dir), input, &out, &errOut, CLISeams{Syslog: r.syslog})
	return code, out.String(), errOut.String()
}

// holdRead opens a transaction that has read table and keeps it open
// until release.
func (r *dropRig) holdRead(table string) (release func()) {
	r.t.Helper()
	ctx := context.Background()
	conn, err := r.db().Conn(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	var n int
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		r.t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
		})
	}
	r.t.Cleanup(release)
	return release
}

// waitDropWaiting waits until a DROP DATABASE waits for a lock with
// super_read_only off.
func (r *dropRig) waitDropWaiting() {
	r.t.Helper()
	waitFor(r.t, func() bool {
		var n int
		if err := r.db().QueryRow("SELECT COUNT(*) FROM information_schema.processlist WHERE info LIKE 'DROP DATABASE%' AND state LIKE 'Waiting for%'").Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		return n > 0 && r.flags() == "1 0"
	}, func() string { return "DROP DATABASE never waited with super_read_only off; flags " + r.flags() })
}

// fakeDump is a mysqldump that writes body and exits with code; it
// records each run in the returned mark file.
func fakeDump(t *testing.T, body string, code int) (script, mark string) {
	t.Helper()
	dir := t.TempDir()
	bodyFile, mark := filepath.Join(dir, "body.sql"), filepath.Join(dir, "ran")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, "mysqldump")
	sh := fmt.Sprintf("#!/bin/sh\necho ran >> %s\ncat %s\nexit %d\n", mark, bodyFile, code)
	if err := os.WriteFile(script, []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, mark
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lastLine is the last line of a dump.
func lastLine(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// TestDropDatabaseHappyPath: on a server at 1 1 the database goes, with
// neither the GTID set nor the flags or their persisted value changed; a
// checked backup stays and one system log line records it.
func TestDropDatabaseHappyPath(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	r.makeDB("rekam_medis", 3)
	r.setup("CREATE VIEW rekam_medis.v AS SELECT id FROM rekam_medis.t1",
		"CREATE PROCEDURE rekam_medis.p() SELECT 1",
		"CREATE TRIGGER rekam_medis.tr BEFORE INSERT ON rekam_medis.t2 FOR EACH ROW SET NEW.v = 'x'")
	before := r.gtidExecuted()
	code, out, errOut := r.run(strings.NewReader("rekam_medis\n"), "rekam_medis")
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if r.exists("rekam_medis") {
		t.Error("the database is still there")
	}
	if got := r.gtidExecuted(); got != before {
		t.Errorf("gtid_executed %q, was %q", got, before)
	}
	if f := r.flags(); f != "1 1" {
		t.Errorf("flags %s, want 1 1", f)
	}
	if v := r.persisted(); v != "ON" {
		t.Errorf("persisted super_read_only %s, want ON", v)
	}
	path := filepath.Join(r.backups, "rekam_medis-"+fixedStamp+".sql")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v, want 0600", fi.Mode())
	}
	if di, err := os.Stat(r.backups); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("backup folder: %v, %v; want mode 0700", di, err)
	}
	dump := readFile(t, path)
	if !strings.HasPrefix(lastLine(dump), "-- Dump completed") {
		t.Errorf("backup ends with %q", lastLine(dump))
	}
	if !strings.Contains(dump, "\nCREATE DATABASE /*!32312 IF NOT EXISTS*/ `rekam_medis` ") {
		t.Error("backup lacks the CREATE DATABASE line")
	}
	if n := strings.Count(dump, "\nCREATE TABLE "); n < 3 {
		t.Errorf("backup has %d CREATE TABLE lines, want at least 3", n)
	}
	sum := sha256.Sum256([]byte(dump))
	for _, want := range []string{"rekam_medis", path, hex.EncodeToString(sum[:]), strconv.Itoa(len(dump)), "scp ", "sha256sum ",
		fmt.Sprintf(c9, "rekam_medis"), c10, c15} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, fmt.Sprintf(c9, "rekam_medis")), strings.Index(out, c10); i < 0 || j < i {
		t.Errorf("C9 not before C10:\n%s", out)
	}
	logged := r.syslogLines()
	if len(logged) != 1 {
		t.Fatalf("system log lines %q, want one", logged)
	}
	for _, want := range []string{strconv.Quote("rekam_medis"), strconv.Quote(path), hex.EncodeToString(sum[:])} {
		if !strings.Contains(logged[0], want) {
			t.Errorf("system log line %q lacks %s", logged[0], want)
		}
	}
	if !r.lockFree() {
		t.Error("the lift lock is still held")
	}
}

// The parts of a dump of database potong (two tables) for the truncated
// dump test.
const (
	dumpHead = "-- MySQL dump 10.13  Distrib 8.0.46, for Linux (x86_64)\n/*!40101 SET NAMES utf8mb4 */;\n\n" +
		"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `potong` /*!40100 DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci */;\n\nUSE `potong`;\n"
	dumpT1      = "DROP TABLE IF EXISTS `t1`;\nCREATE TABLE `t1` (\n  `id` int NOT NULL\n) ENGINE=InnoDB;\n"
	dumpT2      = "DROP TABLE IF EXISTS `t2`;\nCREATE TABLE `t2` (\n  `id` int NOT NULL\n) ENGINE=InnoDB;\n"
	dumpRoutine = "DELIMITER ;;\nCREATE DEFINER=`root`@`localhost` PROCEDURE `mk`()\nBEGIN\nCREATE TABLE IF NOT EXISTS kerja (id INT);\nEND ;;\nDELIMITER ;\n"
	dumpTail    = "-- Dump completed on 2026-11-17  1:37:09\n"
)

// TestDropDatabaseTruncatedDumpStops: a dump that ended early, or that is
// not plausible, stops naming why. Only a mysqldump that connected (its
// output begins with its header) and exited 2 or 3 (a MySQL error, an error
// while dumping a table) is a backup that cannot be made: P1 (in place of
// C8). Every other failure is refused with P1b, with --tanpa-cadangan too:
// exit 0 with a failed check (running again is the remedy), exit 5 (a
// write error, the full-disk shape of X5: another folder is), exit 1 or 4,
// and any exit without output (the client never connected). The incomplete
// file is removed and nothing is dropped.
func TestDropDatabaseTruncatedDumpStops(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("potong", 2)
	for name, c := range map[string]struct {
		body  string
		code  int
		p1    bool // a backup that cannot be made
		cause string
	}{
		"exit 2 after the header":      {dumpHead + dumpT1 + dumpT2 + dumpRoutine + dumpTail, 2, true, "mysqldump keluar dengan kode 2"},
		"exit 3 after the header":      {dumpHead + dumpT1, 3, true, "mysqldump keluar dengan kode 3"},
		"exit 2 without output":        {"", 2, false, "mysqldump keluar dengan kode 2"},
		"exit 1 after the header":      {dumpHead, 1, false, "mysqldump keluar dengan kode 1"},
		"exit 0 without the last line": {dumpHead + dumpT1 + dumpT2 + dumpRoutine, 0, false, `baris terakhir berkas bukan "-- Dump completed"`},
		"disk full":                    {dumpHead + dumpT1 + dumpT2 + "DELIMITER ;;\nCREATE DEFINER=`root`@`localhost` PROCEDURE `mk`()\nBEGIN\nCREATE T", 5, false, "mysqldump keluar dengan kode 5"},
		"too few CREATE TABLE":         {dumpHead + dumpT1 + dumpRoutine + dumpTail, 0, false, "berkas memuat 1 baris CREATE TABLE untuk 2 tabel"},
		"block left open":              {dumpHead + dumpT1 + dumpT2 + "DELIMITER ;;\nCREATE PROCEDURE `mk`()\n" + dumpTail, 0, false, "berkas berakhir di dalam blok DELIMITER"},
		"no CREATE DATABASE":           {strings.Replace(dumpHead, "`potong`", "`lain`", 1) + dumpT1 + dumpT2 + dumpTail, 0, false, "berkas tidak memuat baris CREATE DATABASE untuk database ini"},
	} {
		// P1 with --tanpa-cadangan asks P2 (TestDropDatabaseWithoutBackup);
		// a refusal is the same with and without it.
		variants := [][]string{nil, {"--tanpa-cadangan"}}
		if c.p1 {
			variants = variants[:1]
		}
		for _, extra := range variants {
			t.Run(strings.TrimSpace(name+" "+strings.Join(extra, " ")), func(t *testing.T) {
				script, _ := fakeDump(t, c.body, c.code)
				setVar(t, &cliMysqldump, script)
				code, out, errOut := r.run(strings.NewReader("potong\n"), append([]string{"potong"}, extra...)...)
				want := fmt.Sprintf(p1b, "potong", c.cause) + "\n"
				if c.p1 {
					want = fmt.Sprintf(p1, "potong", c.cause, "potong") + "\n"
				}
				if code == 0 || errOut != want {
					t.Errorf("drop-database %v = %d\nstderr %q\nwant   %q\nstdout %q", extra, code, errOut, want, out)
				}
				if strings.Contains(out, c10) || strings.Contains(out, "TANPA cadangan") {
					t.Errorf("asked to confirm after a failed backup: %q", out)
				}
				if !r.exists("potong") || r.flags() != "1 1" || len(r.syslogLines()) != 0 {
					t.Errorf("exists %v, flags %s, system log %q", r.exists("potong"), r.flags(), r.syslogLines())
				}
				if files := backupFiles(t, r.backups); len(files) != 0 {
					t.Errorf("backup files left: %v", files)
				}
			})
		}
	}
}

// TestDropDatabaseRoutineWithCreateTableLine: a procedure whose body has
// lines that begin with CREATE TABLE passes the checks (they are inside a
// DELIMITER block, and the count is "at least").
func TestDropDatabaseRoutineWithCreateTableLine(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("rutin", 1)
	r.setup("CREATE PROCEDURE rutin.mk()\nBEGIN\nCREATE TEMPORARY TABLE tmp_kerja (id INT);\nCREATE TABLE IF NOT EXISTS kerja (id INT);\nCREATE TABLE\nkerja2 (id INT);\nEND")
	code, out, errOut := r.run(strings.NewReader("rutin\n"), "rutin")
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if r.exists("rutin") || r.flags() != "1 1" {
		t.Errorf("exists %v, flags %s", r.exists("rutin"), r.flags())
	}
	files := backupFiles(t, r.backups)
	if len(files) != 1 || !strings.Contains(readFile(t, files[0]), "\nCREATE TABLE IF NOT EXISTS kerja (id INT);\n") {
		t.Errorf("backup files %v, want one holding the procedure body", files)
	}
}

// TestDropDatabaseNeverOverwrites: a backup file of the same name is never
// overwritten nor another name picked: C6, and nothing else happens.
func TestDropDatabaseNeverOverwrites(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	r.makeDB("simpan", 1)
	if err := os.Mkdir(r.backups, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.backups, "simpan-"+fixedStamp+".sql")
	if err := os.WriteFile(path, []byte("lama\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := r.run(strings.NewReader("simpan\n"), "simpan")
	if want := fmt.Sprintf(c6, path) + "\n"; code == 0 || errOut != want {
		t.Errorf("drop-database = %d\nstderr %q\nwant   %q\nstdout %q", code, errOut, want, out)
	}
	if got := readFile(t, path); got != "lama\n" {
		t.Errorf("old file now %q", got)
	}
	if files := backupFiles(t, r.backups); len(files) != 1 {
		t.Errorf("backup folder holds %v, want only the old file", files)
	}
	if !r.exists("simpan") || r.flags() != "1 1" {
		t.Errorf("exists %v, flags %s", r.exists("simpan"), r.flags())
	}
}

// TestDropDatabaseConfirmation: C9 and C10 are always shown before the
// question; only the exact name goes on (not "ya").
func TestDropDatabaseConfirmation(t *testing.T) {
	r := newDropRig(t)
	for _, c := range []struct {
		answer string
		goes   bool
	}{{"ya", false}, {"Konfirm", false}, {"konfirm ", false}, {"konfirm", true}} {
		t.Run(c.answer, func(t *testing.T) {
			r.makeDB("konfirm", 1)
			t.Cleanup(func() {
				if r.exists("konfirm") {
					r.setup("DROP DATABASE konfirm")
				}
			})
			var atPrompt string
			code, out, errOut := r.runHook(c.answer+"\n", func(s string) { atPrompt = s }, "konfirm")
			if !strings.Contains(atPrompt, fmt.Sprintf(c9, "konfirm")+"\n") || !strings.HasSuffix(atPrompt, c10) {
				t.Errorf("before the question stdout was %q", atPrompt)
			}
			if c.goes {
				if code != 0 || r.exists("konfirm") {
					t.Errorf("exact name: code %d, exists %v, stderr %q", code, r.exists("konfirm"), errOut)
				}
				return
			}
			files := backupFiles(t, r.backups)
			if len(files) == 0 {
				t.Fatal("no backup")
			}
			want := fmt.Sprintf(wantCancelled, files[len(files)-1])
			if code == 0 || !strings.Contains(out, want) || !r.exists("konfirm") || r.flags() != "1 1" {
				t.Errorf("answer %q: code %d, exists %v, flags %s, stdout %q", c.answer, code, r.exists("konfirm"), r.flags(), out)
			}
			for _, f := range files {
				os.Remove(f)
			}
		})
	}
}

// TestDropDatabaseOddNames: names with spaces, backticks, capitals and
// non-ASCII letters are dropped exactly, with a safe file name and the
// hint that suits them; names with quotes cannot be dumped and end at P1
// (in place of C8).
func TestDropDatabaseOddNames(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	for _, c := range []struct{ name, arg, file string }{
		{"dengan spasi", "dengan spasi", "dengan_spasi"},
		{"back`tick", "back`tick", "back_tick"},
		{"BESAR", "besar", "BESAR"},
		{"rumah_sakit_é", "rumah_sakit_é", "rumah_sakit__"},
		{"a$b.c-d", "a$b.c-d", "a_b.c-d"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r.makeDB(c.name, 2)
			r.setup("CREATE TABLE " + quoteIdent(c.name) + ".`tab``el` (id INT)")
			var atPrompt string
			code, out, errOut := r.runHook(c.name+"\n", func(s string) { atPrompt = s }, c.arg)
			if code != 0 {
				t.Fatalf("drop-database %q = %d\nstdout %q\nstderr %q", c.arg, code, out, errOut)
			}
			if r.exists(c.name) {
				t.Error("still there")
			}
			hint := fmt.Sprintf(c9b, strings.ToUpper(hex.EncodeToString([]byte(c.name))))
			if c.name == "BESAR" || c.name == "a$b.c-d" {
				hint = fmt.Sprintf(c9, c.name)
			}
			if !strings.Contains(atPrompt, hint) {
				t.Errorf("stdout lacks %q:\n%s", hint, atPrompt)
			}
			path := filepath.Join(r.backups, c.file+"-"+fixedStamp+".sql")
			if !strings.Contains(readFile(t, path), "CREATE DATABASE /*!32312 IF NOT EXISTS*/ "+quoteIdent(c.name)+" ") {
				t.Errorf("backup %s lacks the database", path)
			}
		})
	}
	for name, word := range map[string]string{"o'brien": `'o'\''brien'`, `dq"uote`: `'dq"uote'`} {
		t.Run(name, func(t *testing.T) {
			r.makeDB(name, 1)
			code, out, errOut := r.run(strings.NewReader(name+"\n"), name)
			prefix := fmt.Sprintf("Cadangan database %s tidak bisa dibuat (mysqldump keluar dengan kode 2", name)
			suffix := "); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan: sudo dbpanel agent drop-database " + word + " --tanpa-cadangan\n"
			if code == 0 || !strings.HasPrefix(errOut, prefix) || !strings.HasSuffix(errOut, suffix) || strings.Count(errOut, "\n") != 1 {
				t.Errorf("drop-database %q = %d\nstderr %q\nstdout %q", name, code, errOut, out)
			}
			if !r.exists(name) || r.flags() != "1 1" {
				t.Errorf("exists %v, flags %s", r.exists(name), r.flags())
			}
			for _, f := range backupFiles(t, r.backups) {
				if strings.Contains(f, "brien") || strings.Contains(f, "uote") {
					t.Errorf("incomplete backup left: %s", f)
				}
			}
		})
	}
}

// TestDropDatabaseErrorRelocks: a DROP that fails (a foreign key from
// another database) ends at 1 1 with a non-zero exit.
func TestDropDatabaseErrorRelocks(t *testing.T) {
	r := newDropRig(t)
	r.setup("CREATE DATABASE induk", "CREATE TABLE induk.p (id INT PRIMARY KEY)", "INSERT INTO induk.p VALUES (1)",
		"CREATE DATABASE anak", "CREATE TABLE anak.c (pid INT, FOREIGN KEY (pid) REFERENCES induk.p (id))", "INSERT INTO anak.c VALUES (1)")
	code, out, errOut := r.run(strings.NewReader("induk\n"), "induk")
	if code == 0 {
		t.Fatalf("drop-database = 0\nstdout %q\nstderr %q", out, errOut)
	}
	if !strings.Contains(errOut, "Database induk tidak bisa dihapus (") || !strings.Contains(errOut, "Kunci baca-saja sudah terpasang lagi.") {
		t.Errorf("stderr %q", errOut)
	}
	if !r.exists("induk") || r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("exists %v, flags %s, lock free %v", r.exists("induk"), r.flags(), r.lockFree())
	}
}

// TestDropDatabaseKilledMidway: a command killed (SIGKILL) with
// super_read_only off leaves the lift lock free and its file as it was;
// the guard's next round locks the server again and reports it.
func TestDropDatabaseKilledMidway(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("arsip", 1)
	tick, evs := r.guard()
	state := lockState(t, r.lock)
	release := r.holdRead("arsip.t1")
	p := r.start(helperSpec{}, "arsip")
	p.waitOut(c10)
	p.send("arsip")
	r.waitDropWaiting()
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, sig := p.wait(); sig != syscall.SIGKILL {
		t.Fatalf("helper ended by %v, want SIGKILL", sig)
	}
	if got := lockState(t, r.lock); got != state {
		t.Errorf("lock file and folder now %s, were %s", got, state)
	}
	if !r.lockFree() {
		t.Error("lift lock still held after the holder was killed")
	}
	// The DROP the killed command sent waits on in MySQL (and blocks the
	// guard's statements) until the reading transaction ends.
	release()
	waitFor(t, func() bool {
		tick()
		return r.flags() == "1 1" && len(evs.lateStarts()) > 0
	}, func() string { return "the guard never locked again and reported; flags " + r.flags() })
	if s := evs.lateStarts()[0]; s.While != proto.QuarantineWhileRunning {
		t.Errorf("report %+v, want while %q", s, proto.QuarantineWhileRunning)
	}
}

// TestDropDatabaseInterruptedRelocksItself: SIGINT, SIGTERM or SIGHUP while
// DROP waits: the command itself locks again (C13), exits non-zero and
// frees the lift lock; the guard's next round has nothing to report.
func TestDropDatabaseInterruptedRelocksItself(t *testing.T) {
	r := newDropRig(t)
	tick, evs := r.guard()
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			name := "sinyal_" + strconv.Itoa(int(sig))
			r.makeDB(name, 1)
			state := lockState(t, r.lock)
			release := r.holdRead(name + ".t1")
			p := r.start(helperSpec{}, name)
			p.waitOut(c10)
			p.send(name)
			r.waitDropWaiting()
			start := time.Now()
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			code, signaled := p.wait()
			// Well under the 60 s lock wait: the kill ended the DROP's wait.
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("the command took %v to end after %v", d, sig)
			}
			if code == 0 || signaled != 0 || !strings.Contains(p.err.String(), c13+"\n") {
				t.Errorf("after %v: code %d, signal %v, stderr %q", sig, code, signaled, p.err.String())
			}
			if f := r.flags(); f != "1 1" {
				t.Errorf("flags %s, want 1 1", f)
			}
			if !r.lockFree() {
				t.Error("lift lock still held")
			}
			if got := lockState(t, r.lock); got != state {
				t.Errorf("lock file and folder now %s, were %s", got, state)
			}
			release()
			if !r.exists(name) {
				t.Error("the database was dropped")
			}
			tick()
			time.Sleep(time.Second)
			if s := evs.lateStarts(); len(s) != 0 {
				t.Errorf("guard reported %+v", s)
			}
		})
	}
}

// TestDropDatabaseBlockedByOpenTransaction: a DROP that waits on another
// session past lock_wait_timeout leaves the database, puts the lock back
// and says C12 with the backup kept.
func TestDropDatabaseBlockedByOpenTransaction(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	setVar(t, &cliLockWaitTimeout, 2)
	r.makeDB("terpakai", 1)
	r.holdRead("terpakai.t1")
	code, out, errOut := r.run(strings.NewReader("terpakai\n"), "terpakai")
	path := filepath.Join(r.backups, "terpakai-"+fixedStamp+".sql")
	if want := fmt.Sprintf(c12, "terpakai", path) + "\n"; code == 0 || errOut != want {
		t.Errorf("drop-database = %d\nstderr %q\nwant   %q\nstdout %q", code, errOut, want, out)
	}
	if !r.exists("terpakai") || r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("exists %v, flags %s, lock free %v", r.exists("terpakai"), r.flags(), r.lockFree())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("backup: %v", err)
	}
}

// TestDropDatabaseGivesUpAfterCap: a window open past its cap is closed by
// the command itself: C14, 1 1.
func TestDropDatabaseGivesUpAfterCap(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliWindowCap, 2*time.Second)
	r.makeDB("lama", 1)
	r.holdRead("lama.t1")
	start := time.Now()
	code, out, errOut := r.run(strings.NewReader("lama\n"), "lama")
	if code == 0 || errOut != c14+"\n" {
		t.Errorf("drop-database = %d\nstderr %q\nstdout %q", code, errOut, out)
	}
	if time.Since(start) > 40*time.Second {
		t.Errorf("took %v: the cap did not end the wait", time.Since(start))
	}
	if !r.exists("lama") || r.flags() != "1 1" || !r.lockFree() {
		t.Errorf("exists %v, flags %s, lock free %v", r.exists("lama"), r.flags(), r.lockFree())
	}
}

// TestDropDatabaseRecheckAfterConfirmation: what changed while the
// operator read (the role, the flags, the tables) stops the command with
// C11 before it sends SET GLOBAL super_read_only = OFF.
func TestDropDatabaseRecheckAfterConfirmation(t *testing.T) {
	r := newDropRig(t)
	logFile := filepath.Join(r.inst.Dir, "general.log")
	r.exec("SET GLOBAL general_log_file = '"+logFile+"'", "SET GLOBAL general_log = ON")
	for name, c := range map[string]struct{ change, undo func() }{
		"role": {
			func() { mysqlLoad(t, r.dir).SetRole("master") },
			func() { mysqlLoad(t, r.dir).SetRole("quarantined") },
		},
		"flags": {
			func() { r.exec("SET GLOBAL read_only = OFF") },
			func() { r.exec("SET GLOBAL super_read_only = ON") },
		},
		"table": {
			func() {
				r.exec("SET GLOBAL read_only = OFF", "CREATE TABLE ubah_table.baru (id INT)", "SET GLOBAL super_read_only = ON")
			},
			func() {},
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := "ubah_" + name
			r.makeDB(db, 1)
			code, out, errOut := r.runHook(db+"\n", func(string) { c.change() }, db)
			c.undo()
			if code == 0 || errOut != c11+"\n" {
				t.Errorf("drop-database = %d\nstderr %q\nstdout %q", code, errOut, out)
			}
			if !r.exists(db) || r.flags() != "1 1" || !r.lockFree() {
				t.Errorf("exists %v, flags %s, lock free %v", r.exists(db), r.flags(), r.lockFree())
			}
			general := readFile(t, logFile)
			if !strings.Contains(general, "FROM information_schema.schemata") {
				t.Fatal("the general log does not show the command's statements")
			}
			if strings.Contains(general, "super_read_only = OFF") {
				t.Error("SET GLOBAL super_read_only = OFF was sent")
			}
		})
	}
}

// fileLog records what the backup file layer was asked to do, and when
// the operator was asked.
type fileLog struct {
	mu  sync.Mutex
	evs []string
}

func (l *fileLog) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.evs); n > 0 && l.evs[n-1] == ev {
		return
	}
	l.evs = append(l.evs, ev)
}

func (l *fileLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.evs...)
}

// recFile is a backup file that records its use.
type recFile struct {
	f   *os.File
	log *fileLog
}

func (r *recFile) Write(p []byte) (int, error) { r.log.add("write"); return r.f.Write(p) }
func (r *recFile) Read(p []byte) (int, error)  { r.log.add("read"); return r.f.Read(p) }
func (r *recFile) Seek(off int64, whence int) (int64, error) {
	r.log.add("seek")
	return r.f.Seek(off, whence)
}
func (r *recFile) Stat() (os.FileInfo, error) { return r.f.Stat() }
func (r *recFile) Sync() error                { r.log.add("sync file"); return r.f.Sync() }
func (r *recFile) Close() error               { r.log.add("close"); return r.f.Close() }

// TestDropDatabaseSyncsBackupBeforeDrop: the backup file and its folder are
// synced before the operator is asked, and the checks and the SHA-256 read
// the descriptor the dump was written to: the path is not opened again
// (the fake layer moves the file away from it as soon as it is created).
func TestDropDatabaseSyncsBackupBeforeDrop(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("sinkron", 2)
	log := &fileLog{}
	var moved string
	setVar(t, &cliBackupFS, backupFS{
		Create: func(path string) (backupFile, error) {
			log.add("create")
			f, err := osBackupFS.Create(path)
			if err != nil {
				return nil, err
			}
			moved = path + ".moved"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			return &recFile{f: f.(*os.File), log: log}, nil
		},
		SyncDir: func(dir string) error { log.add("sync dir"); return osBackupFS.SyncDir(dir) },
		Remove:  func(path string) error { log.add("remove"); return osBackupFS.Remove(path) },
	})
	code, out, errOut := r.runHook("sinkron\n", func(string) { log.add("prompt") }, "sinkron")
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q\nfile layer %v", code, out, errOut, log.list())
	}
	evs := log.list()
	at := func(ev string) int {
		for i, e := range evs {
			if e == ev {
				return i
			}
		}
		return -1
	}
	prompt := at("prompt")
	if strings.Count(strings.Join(evs, ","), "create") != 1 || at("sync file") < 0 || at("sync file") > prompt || at("sync dir") < 0 || at("sync dir") > prompt || at("remove") >= 0 {
		t.Errorf("file layer events %v: want one create, both syncs before the prompt, no remove", evs)
	}
	for i, e := range evs {
		if e == "write" && i > at("seek") && at("seek") >= 0 {
			t.Errorf("written after it was read back: %v", evs)
		}
	}
	dump := readFile(t, moved)
	sum := sha256.Sum256([]byte(dump))
	if !strings.HasPrefix(lastLine(dump), "-- Dump completed") || !strings.Contains(out, hex.EncodeToString(sum[:])) {
		t.Errorf("backup ends %q; stdout %q", lastLine(dump), out)
	}
	if r.exists("sinkron") {
		t.Error("not dropped")
	}
}

// TestDropDatabaseWarnsOnSharedFolder: a --ke folder others can write
// draws C7 before the question; the command goes on.
func TestDropDatabaseWarnsOnSharedFolder(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("bersama", 1)
	folder := filepath.Join(t.TempDir(), "usb")
	if err := os.Mkdir(folder, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o770); err != nil {
		t.Fatal(err)
	}
	var atPrompt string
	code, out, errOut := r.runHook("bersama\n", func(s string) { atPrompt = s }, "bersama", "--ke", folder)
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !strings.Contains(atPrompt, fmt.Sprintf(c7, folder)+"\n") {
		t.Errorf("C7 not shown before the question: %q", atPrompt)
	}
	if files := backupFiles(t, folder); len(files) != 1 {
		t.Errorf("backups in --ke folder: %v", files)
	}
	if files := backupFiles(t, r.backups); len(files) != 0 {
		t.Errorf("backups in the default folder: %v", files)
	}
}

// TestDropDatabaseNoSpace: an estimate larger than the free space is
// refused before mysqldump runs, with --tanpa-cadangan too: another folder
// keeps the backup, so the drop without one is never offered (P2 not
// asked, nothing dropped).
func TestDropDatabaseNoSpace(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("besar_sekali", 2)
	script, mark := fakeDump(t, dumpHead+dumpTail, 0)
	setVar(t, &cliMysqldump, script)
	setVar(t, &cliStatfs, func(string) (uint64, error) { return 1, nil })
	for _, extra := range [][]string{nil, {"--tanpa-cadangan"}} {
		t.Run(strings.TrimSpace("refused "+strings.Join(extra, " ")), func(t *testing.T) {
			code, out, errOut := r.run(strings.NewReader("besar_sekali\n"), append([]string{"besar_sekali"}, extra...)...)
			prefix := fmt.Sprintf("Ruang kosong di folder %s (1 byte) tidak cukup untuk cadangan database besar_sekali (perkiraan ", r.backups)
			if code == 0 || !strings.HasPrefix(errOut, prefix) || !strings.HasSuffix(errOut, " byte); tidak ada yang diubah. Pilih folder lain dengan --ke <folder>.\n") ||
				strings.Count(errOut, "\n") != 1 {
				t.Errorf("drop-database %v = %d\nstderr %q\nstdout %q", extra, code, errOut, out)
			}
			if strings.Contains(out, "TANPA cadangan") || strings.Contains(out, c10) {
				t.Errorf("asked to confirm: %q", out)
			}
			if _, err := os.Stat(mark); err == nil {
				t.Error("mysqldump ran")
			}
			if files := backupFiles(t, r.backups); len(files) != 0 || !r.exists("besar_sekali") || r.flags() != "1 1" || len(r.syslogLines()) != 0 {
				t.Errorf("backups %v, exists %v, flags %s, system log %q", files, r.exists("besar_sekali"), r.flags(), r.syslogLines())
			}
		})
	}
}

// TestCLIRefusesWhileLoadHoldsLock: the lift lock held (as by an agent
// load, flags 1 0): C3, not C2, no backup, nothing changed.
func TestCLIRefusesWhileLoadHoldsLock(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("muat", 1)
	release, err := mysql.AcquireLiftLock(r.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r.exec("SET GLOBAL super_read_only = OFF")
	defer r.exec("SET GLOBAL super_read_only = ON")
	code, out, errOut := r.run(strings.NewReader("muat\n"), "muat")
	if code == 0 || errOut != mysql.ErrLiftBusy.Error()+"\n" {
		t.Errorf("drop-database = %d\nstderr %q\nstdout %q", code, errOut, out)
	}
	if files := backupFiles(t, r.backups); len(files) != 0 || !r.exists("muat") || r.flags() != "1 0" {
		t.Errorf("backups %v, exists %v, flags %s", files, r.exists("muat"), r.flags())
	}
}

// TestCLIProbeRetriesShortly: a lift lock released within 3 seconds lets
// the command go on.
func TestCLIProbeRetriesShortly(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("tunggu", 1)
	release, err := mysql.AcquireLiftLock(r.lock)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(time.Second, release)
	defer timer.Stop()
	code, out, errOut := r.run(strings.NewReader("tunggu\n"), "tunggu")
	if code != 0 || r.exists("tunggu") {
		t.Errorf("drop-database = %d, exists %v\nstdout %q\nstderr %q", code, r.exists("tunggu"), out, errOut)
	}
}

// TestCLIRefusesUnsafeLockFile: a lift lock file others can read: C4 with
// its reason, and nothing changed (the file neither).
func TestCLIRefusesUnsafeLockFile(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("aman", 1)
	if err := os.Mkdir(filepath.Dir(r.lock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.lock, 0o644); err != nil {
		t.Fatal(err)
	}
	state := lockState(t, r.lock)
	code, out, errOut := r.run(strings.NewReader("aman\n"), "aman")
	want := (&mysql.LiftLockError{Path: r.lock, Reason: "bisa dibaca atau ditulis pengguna lain"}).Error() + "\n"
	if code == 0 || errOut != want {
		t.Errorf("drop-database = %d\nstderr %q\nwant   %q\nstdout %q", code, errOut, want, out)
	}
	if got := lockState(t, r.lock); got != state {
		t.Errorf("lock file now %s, was %s", got, state)
	}
	if files := backupFiles(t, r.backups); len(files) != 0 || !r.exists("aman") {
		t.Errorf("backups %v, exists %v", files, r.exists("aman"))
	}
}

// TestDropDatabaseRefusals: not quarantined (C1), a system schema, a name
// that does not exist, flags not 1 1 with the lock free (C2), the lift lock
// held (C3) or unsafe (C4); the same with --tanpa-cadangan.
func TestDropDatabaseRefusals(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("tolak", 1)
	var held func()
	holdLock := func() func() {
		release, err := mysql.AcquireLiftLock(r.lock)
		if err != nil {
			t.Fatal(err)
		}
		return release
	}
	holdLock()() // the lock file exists from here on
	chmodLock := func(mode os.FileMode) {
		if err := os.Chmod(r.lock, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, arg, want string
		prep, undo      func()
	}{
		{"not quarantined", "tolak", fmt.Sprintf(c1, "master"),
			func() { mysqlLoad(t, r.dir).SetRole("master") }, func() { mysqlLoad(t, r.dir).SetRole("quarantined") }},
		{"system schema", "mysql", fmt.Sprintf(wantSystemSchema, "mysql"), func() {}, func() {}},
		{"system schema other case", "Performance_Schema", fmt.Sprintf(wantSystemSchema, "performance_schema"), func() {}, func() {}},
		{"no such database", "tidak_ada", fmt.Sprintf(wantNoSuchDatabase, "tidak_ada"), func() {}, func() {}},
		{"flags off", "tolak", c2,
			func() { r.exec("SET GLOBAL super_read_only = OFF") }, func() { r.exec("SET GLOBAL super_read_only = ON") }},
		{"lift lock held", "tolak", mysql.ErrLiftBusy.Error(), func() { held = holdLock() }, func() { held() }},
		{"unsafe lift lock file", "tolak", (&mysql.LiftLockError{Path: r.lock, Reason: "bisa dibaca atau ditulis pengguna lain"}).Error(),
			func() { chmodLock(0o644) }, func() { chmodLock(0o600) }},
	} {
		// --tanpa-cadangan changes none of the refusals.
		for _, extra := range [][]string{nil, {"--tanpa-cadangan"}} {
			t.Run(strings.TrimSpace(c.name+" "+strings.Join(extra, " ")), func(t *testing.T) {
				c.prep()
				code, out, errOut := r.run(strings.NewReader(c.arg+"\n"), append([]string{c.arg}, extra...)...)
				c.undo()
				if code == 0 || errOut != c.want+"\n" {
					t.Errorf("drop-database %s %v = %d\nstderr %q\nwant   %q\nstdout %q", c.arg, extra, code, errOut, c.want+"\n", out)
				}
				if files := backupFiles(t, r.backups); len(files) != 0 || !r.exists("tolak") {
					t.Errorf("backups %v, exists %v", files, r.exists("tolak"))
				}
			})
		}
	}
	if code, _, _ := r.run(strings.NewReader(""), "satu", "dua"); code != 2 {
		t.Errorf("two names: code %d, want 2", code)
	}
	if code, _, _ := r.run(strings.NewReader("")); code != 2 {
		t.Errorf("no name: code %d, want 2", code)
	}
}

// TestCLIOutputEscapesNames: names holding ESC, a newline or a bidi
// override never reach the terminal as such; the system log line stays one
// line with the name quoted.
func TestCLIOutputEscapesNames(t *testing.T) {
	r := newDropRig(t)
	clean := func(name, s string) {
		t.Helper()
		for _, bad := range []string{"\x1b", "\u202e"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s holds %q: %q", name, bad, s)
			}
		}
	}
	dumpable := "x\x1b[31my\u202ez"
	r.makeDB(dumpable, 1)
	code, out, errOut := r.run(strings.NewReader(dumpable+"\n"), dumpable)
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	clean("stdout", out)
	clean("stderr", errOut)
	if !strings.Contains(out, "x [31my z") {
		t.Errorf("stdout does not name %q: %q", printable(dumpable), out)
	}
	logged := r.syslogLines()
	if len(logged) != 1 || strings.ContainsAny(logged[0], "\n\r") || !strings.Contains(logged[0], strconv.Quote(dumpable)) {
		t.Errorf("system log %q", logged)
	}

	// mysqldump cannot dump a name with a newline: P1, named cleanly.
	withNewline := "a\nb\x1bc"
	r.makeDB(withNewline, 1)
	code, out, errOut = r.run(strings.NewReader(""), withNewline)
	if code == 0 || !strings.HasPrefix(errOut, "Cadangan database a b c tidak bisa dibuat (") || !strings.HasSuffix(errOut, "drop-database 'a b c' --tanpa-cadangan\n") ||
		strings.Count(errOut, "\n") != 1 {
		t.Errorf("drop-database = %d\nstderr %q\nstdout %q", code, errOut, out)
	}
	clean("stdout", out)
	clean("stderr", errOut)
}

// TestCLIUsageNamesDropDatabase: the help names the command and its
// options.
func TestCLIUsageNamesDropDatabase(t *testing.T) {
	var out bytes.Buffer
	RunCLI([]string{"help"}, strings.NewReader(""), &out, io.Discard)
	for _, want := range []string{"dbpanel agent drop-database <nama>", "--ke FOLDER", "--pengguna NAMA", "/var/lib/dbpanel/dibuang"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

// TestDropDatabaseInterruptedBackupRemoved: a signal while mysqldump runs
// stops it; the incomplete backup goes and nothing is dropped.
func TestDropDatabaseInterruptedBackupRemoved(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("berhenti", 1)
	dir := t.TempDir()
	body := filepath.Join(dir, "body.sql")
	if err := os.WriteFile(body, []byte(dumpHead+dumpT1), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "mysqldump")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat "+body+"\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := r.start(helperSpec{Mysqldump: script}, "berhenti")
	waitFor(t, func() bool {
		files := backupFiles(t, r.backups)
		if len(files) != 1 {
			return false
		}
		fi, err := os.Stat(files[0])
		return err == nil && fi.Size() > 0
	}, func() string { return "mysqldump never wrote the backup" })
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	code, sig := p.wait()
	want := "Perintah dihentikan (sinyal) selagi cadangan dibuat; berkas cadangan yang belum selesai dihapus dan tidak ada yang dihapus dari MySQL.\n"
	if code != 1 || sig != 0 || p.err.String() != want {
		t.Errorf("code %d, signal %v, stderr %q", code, sig, p.err.String())
	}
	if files := backupFiles(t, r.backups); len(files) != 0 || !r.exists("berhenti") || r.flags() != "1 1" {
		t.Errorf("backups %v, exists %v, flags %s", files, r.exists("berhenti"), r.flags())
	}
}

// TestFailedBackupTextNamesOption: without --tanpa-cadangan a backup that
// cannot be made ends at P1 exactly, which names the option.
func TestFailedBackupTextNamesOption(t *testing.T) {
	r := newDropRig(t)
	r.makeDB("gagal", 1)
	script, _ := fakeDump(t, dumpHead, 2) // connected, then a MySQL error
	setVar(t, &cliMysqldump, script)
	code, out, errOut := r.run(strings.NewReader("gagal\n"), "gagal")
	want := fmt.Sprintf(p1, "gagal", "mysqldump keluar dengan kode 2", "gagal") + "\n"
	if code == 0 || errOut != want {
		t.Errorf("drop-database = %d\nstderr %q\nwant   %q\nstdout %q", code, errOut, want, out)
	}
	if strings.Contains(out, "TANPA cadangan") || !r.exists("gagal") || len(backupFiles(t, r.backups)) != 0 {
		t.Errorf("stdout %q, exists %v, backups %v", out, r.exists("gagal"), backupFiles(t, r.backups))
	}
}

// TestDropDatabaseWithoutBackup: with --tanpa-cadangan a database whose
// backup cannot be made (a name with a quote) is dropped once its name is
// typed at P2: no backup file left or shown, 1 1, backup=none in the
// system log. Any other answer cancels with nothing changed. The window's
// own rules hold on this path (C12 without a backup sentence).
func TestDropDatabaseWithoutBackup(t *testing.T) {
	r := newDropRig(t)
	const name = "o'brien"
	cause := "mysqldump keluar dengan kode 2: mysqldump: Couldn't execute 'use `o\\'brien`': Unknown database 'o\\'brien' (1049)"
	ask := fmt.Sprintf(p2, name, cause)
	hint := fmt.Sprintf(c9b, strings.ToUpper(hex.EncodeToString([]byte(name))))
	check := func(t *testing.T, atPrompt, out string) {
		t.Helper()
		if !strings.Contains(atPrompt, hint+"\n") || !strings.HasSuffix(atPrompt, ask) {
			t.Errorf("before the question stdout was %q\nwant C9b then %q", atPrompt, ask)
		}
		for _, bad := range []string{"SHA-256", "Berkas", "Ukuran", c10} {
			if strings.Contains(out, bad) {
				t.Errorf("stdout shows %q without a backup: %q", bad, out)
			}
		}
		if files := backupFiles(t, r.backups); len(files) != 0 {
			t.Errorf("backup files left: %v", files)
		}
	}

	r.makeDB(name, 2)
	for _, answer := range []string{"ya", "O'BRIEN", ""} {
		var atPrompt string
		code, out, errOut := r.runHook(answer+"\n", func(s string) { atPrompt = s }, name, "--tanpa-cadangan")
		check(t, atPrompt, out)
		if code == 0 || !strings.Contains(out, "Dibatalkan; tidak ada yang dihapus.\n") || strings.Contains(out, "Cadangan tadi") ||
			!r.exists(name) || r.flags() != "1 1" {
			t.Errorf("answer %q: code %d, exists %v, flags %s\nstdout %q\nstderr %q", answer, code, r.exists(name), r.flags(), out, errOut)
		}
	}
	if len(r.syslogLines()) != 0 {
		t.Errorf("system log after cancelling: %q", r.syslogLines())
	}

	// Blocked by another session: C12 without the backup sentence.
	t.Run("blocked", func(t *testing.T) {
		setVar(t, &cliLockWaitTimeout, 2)
		release := r.holdRead("`o'brien`.t1")
		code, out, errOut := r.run(strings.NewReader(name+"\n"), name, "--tanpa-cadangan")
		release()
		want := "Database o'brien sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. " +
			"Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi.\n"
		if code == 0 || errOut != want || !r.exists(name) || r.flags() != "1 1" {
			t.Errorf("code %d, exists %v, flags %s\nstderr %q\nwant   %q\nstdout %q", code, r.exists(name), r.flags(), errOut, want, out)
		}
	})

	// A table added while the operator read: C11, nothing dropped.
	t.Run("recheck", func(t *testing.T) {
		code, _, errOut := r.runHook(name+"\n", func(string) {
			r.exec("SET GLOBAL read_only = OFF", "CREATE TABLE `o'brien`.extra (id INT)", "SET GLOBAL super_read_only = ON")
		}, name, "--tanpa-cadangan")
		if code == 0 || errOut != c11+"\n" || !r.exists(name) || r.flags() != "1 1" {
			t.Errorf("code %d, exists %v, flags %s, stderr %q", code, r.exists(name), r.flags(), errOut)
		}
	})

	before := r.gtidExecuted()
	var atPrompt string
	code, out, errOut := r.runHook(name+"\n", func(s string) { atPrompt = s }, name, "--tanpa-cadangan")
	if code != 0 {
		t.Fatalf("drop-database --tanpa-cadangan = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	check(t, atPrompt, out)
	if !strings.HasSuffix(out, c15+"\n") || r.exists(name) || r.flags() != "1 1" || r.gtidExecuted() != before || !r.lockFree() {
		t.Errorf("exists %v, flags %s, gtid changed %v, lock free %v\nstdout %q", r.exists(name), r.flags(), r.gtidExecuted() != before, r.lockFree(), out)
	}
	logged := r.syslogLines()
	if len(logged) != 1 || !strings.Contains(logged[0], "backup=none") || !strings.Contains(logged[0], strconv.Quote(name)) || strings.ContainsAny(logged[0], "\n\r") {
		t.Errorf("system log %q", logged)
	}
}

// TestDropDatabaseWithoutBackupStillBacksUpWhenItCan: --tanpa-cadangan on
// a database that can be dumped takes the ordinary path: the backup is
// made and shown, C10 is asked, P2 never is.
func TestDropDatabaseWithoutBackupStillBacksUpWhenItCan(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	r.makeDB("biasa", 2)
	var atPrompt string
	code, out, errOut := r.runHook("biasa\n", func(s string) { atPrompt = s }, "biasa", "--tanpa-cadangan")
	if code != 0 {
		t.Fatalf("drop-database = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !strings.HasSuffix(atPrompt, c10) || strings.Contains(out, "TANPA cadangan") {
		t.Errorf("stdout %q", out)
	}
	path := filepath.Join(r.backups, "biasa-"+fixedStamp+".sql")
	if !strings.HasPrefix(lastLine(readFile(t, path)), "-- Dump completed") || !strings.Contains(out, path) {
		t.Errorf("backup %s missing or not shown", path)
	}
	if logged := r.syslogLines(); len(logged) != 1 || strings.Contains(logged[0], "backup=none") || !strings.Contains(logged[0], strconv.Quote(path)) {
		t.Errorf("system log %q", logged)
	}
	if r.exists("biasa") || r.flags() != "1 1" {
		t.Errorf("exists %v, flags %s", r.exists("biasa"), r.flags())
	}
}

// TestCLIUsageHidesWithoutBackup: --tanpa-cadangan is not in the help.
func TestCLIUsageHidesWithoutBackup(t *testing.T) {
	var out bytes.Buffer
	RunCLI([]string{"help"}, strings.NewReader(""), &out, io.Discard)
	if strings.Contains(out.String(), "tanpa-cadangan") {
		t.Error("usage names --tanpa-cadangan")
	}
}

// p1b refuses a backup that failed for a reason other than the dump
// itself; it never offers the drop without a backup.
const p1b = "Cadangan database %s tidak bisa dibuat (%s); tidak ada yang dihapus. Perbaiki sebabnya atau pakai folder lain dengan --ke <folder>, lalu jalankan perintah ini lagi."

// failSyncFile is a backup file whose Sync fails.
type failSyncFile struct{ *os.File }

func (failSyncFile) Sync() error { return errors.New("input/output error") }

// TestDropDatabaseBackupRefusals: a backup that fails without mysqldump
// having run and failed (the file cannot be created, mysqldump cannot
// start, a sync fails) is refused with P1b, with --tanpa-cadangan too:
// P2 is never asked, nothing is dropped and nothing is left.
func TestDropDatabaseBackupRefusals(t *testing.T) {
	r := newDropRig(t)
	setVar(t, &cliNow, func() time.Time { return fixedNow })
	r.makeDB("tolak_cadangan", 1)
	folder := filepath.Join(t.TempDir(), "ke")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(folder, "tolak_cadangan-"+fixedStamp+".sql")
	missing := filepath.Join(t.TempDir(), "tidak-ada", "mysqldump")
	for name, c := range map[string]struct {
		set   func(t *testing.T)
		cause string
	}{
		"file not created": {func(t *testing.T) {
			fsys := osBackupFS
			fsys.Create = func(p string) (backupFile, error) {
				return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EACCES}
			}
			setVar(t, &cliBackupFS, fsys)
		}, fmt.Sprintf("berkas %s tidak bisa dibuat: open %s: permission denied", path, path)},
		"mysqldump missing": {func(t *testing.T) { setVar(t, &cliMysqldump, missing) },
			fmt.Sprintf("mysqldump tidak bisa dijalankan: fork/exec %s: no such file or directory", missing)},
		"mysqldump killed": {func(t *testing.T) {
			// Killed from outside (as by the OOM killer): it did not run to
			// its own exit, so it is no failed dump.
			script := filepath.Join(t.TempDir(), "mysqldump")
			if err := os.WriteFile(script, []byte("#!/bin/sh\necho '-- MySQL dump'\nkill -KILL $$\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			setVar(t, &cliMysqldump, script)
		}, "mysqldump dihentikan oleh sinyal (killed)"},
		"file sync fails": {func(t *testing.T) {
			fsys := osBackupFS
			fsys.Create = func(p string) (backupFile, error) {
				f, err := osBackupFS.Create(p)
				if err != nil {
					return nil, err
				}
				return failSyncFile{f.(*os.File)}, nil
			}
			setVar(t, &cliBackupFS, fsys)
		}, "berkas tidak bisa disimpan ke disk: input/output error"},
		"folder sync fails": {func(t *testing.T) {
			fsys := osBackupFS
			fsys.SyncDir = func(string) error { return errors.New("input/output error") }
			setVar(t, &cliBackupFS, fsys)
		}, "berkas tidak bisa disimpan ke disk: input/output error"},
	} {
		for _, extra := range [][]string{nil, {"--tanpa-cadangan"}} {
			t.Run(strings.TrimSpace(name+" "+strings.Join(extra, " ")), func(t *testing.T) {
				c.set(t)
				args := append([]string{"tolak_cadangan", "--ke", folder}, extra...)
				code, out, errOut := r.run(strings.NewReader("tolak_cadangan\ntolak_cadangan\n"), args...)
				if want := fmt.Sprintf(p1b, "tolak_cadangan", c.cause) + "\n"; code == 0 || errOut != want {
					t.Errorf("drop-database %v = %d\nstderr %q\nwant   %q\nstdout %q", extra, code, errOut, want, out)
				}
				if strings.Contains(out, "TANPA cadangan") || strings.Contains(out, c10) {
					t.Errorf("asked to confirm: %q", out)
				}
				if files := backupFiles(t, folder); len(files) != 0 || !r.exists("tolak_cadangan") || r.flags() != "1 1" || len(r.syslogLines()) != 0 {
					t.Errorf("files %v, exists %v, flags %s, system log %q", files, r.exists("tolak_cadangan"), r.flags(), r.syslogLines())
				}
			})
		}
	}
}
