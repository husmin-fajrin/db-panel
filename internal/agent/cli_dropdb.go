package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
)

// Texts of drop-database: spec 5.8 (C1, C2, C6, C7, C9, C10, C12, C15),
// spec 7's --tanpa-cadangan block (P1, which takes C8's place, and P2), and
// the lines this command words itself.
const (
	textNotQuarantined  = "perintah ini hanya untuk server yang dikarantina (quarantined); peran server ini: %s"
	textNotLocked       = "kunci baca-saja server ini sedang tidak terpasang penuh; tunggu beberapa detik (agent memasangnya lagi) lalu ulangi"
	textBackupExists    = "berkas cadangan %s sudah ada; tidak ada yang ditimpa dan tidak ada yang dihapus. Jalankan perintah ini lagi."
	textSharedFolder    = "Peringatan: folder %s bukan milik root atau bisa ditulis pengguna lain; cadangan di sana bisa diubah atau dihapus pengguna lain."
	textBackupFailed    = "Cadangan database %s tidak bisa dibuat (%s); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan: sudo dbpanel agent drop-database %s --tanpa-cadangan"
	textBackupFailedOdd = "Cadangan database %s tidak bisa dibuat (%s); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan lagi perintah yang sama dengan tambahan --tanpa-cadangan di akhir."
	textBackupRefused   = "Cadangan database %s tidak bisa dibuat (%s); tidak ada yang dihapus. Perbaiki sebabnya atau pakai folder lain dengan --ke <folder>, lalu jalankan perintah ini lagi."
	textConfirmNoBackup = "Cadangan database %s tidak bisa dibuat (%s). Dengan --tanpa-cadangan database itu dihapus TANPA cadangan dan tidak bisa dikembalikan. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: "
	textCheckNewMaster  = `Periksa dulu di master baru (hanya membaca): mysql -e 'SHOW DATABASES LIKE "%s"'. Jawabannya harus kosong.`
	textCheckNewHex     = `Periksa dulu di master baru (hanya membaca): mysql -e 'SELECT schema_name FROM information_schema.schemata WHERE HEX(schema_name) = "%s"'. Jawabannya harus kosong.`
	textConfirmDrop     = "Agent tidak bisa memastikan database ini tidak ada di master baru. Bila database ini ada di master baru, menghapusnya di sini merusak replikasi server ini. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: "
	textDropWaited      = "Database %s sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi. Cadangan tadi tetap ada di %s."
	textDropDone        = `Selesai. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau mulai lagi Bangun ulang sebagai replika.`
	textPinsUnreadable  = "Pin server ini tidak bisa dibaca (%v); tidak ada yang diubah."
	textLiftUnchecked   = "kunci baca-saja server ini tidak bisa diperiksa (%v); tidak ada yang diubah."
	textMySQLUnread     = "MySQL di server ini tidak bisa dibaca (%v); tidak ada yang diubah."
	textNoSuchDatabase  = "Database %s tidak ada di server ini; tidak ada yang diubah."
	textSystemSchema    = "Database %s adalah skema sistem MySQL; perintah ini tidak menghapusnya."
	textFolderUnusable  = "Folder cadangan %s tidak bisa dipakai (%v); tidak ada yang diubah."
	textNoSpace         = "Ruang kosong di folder %s (%d byte) tidak cukup untuk cadangan database %s (perkiraan %d byte); tidak ada yang diubah. Pilih folder lain dengan --ke <folder>."
	textDumpInterrupted = "Perintah dihentikan (sinyal) selagi cadangan dibuat; berkas cadangan yang belum selesai dihapus dan tidak ada yang dihapus dari MySQL."
	textBackupSummary   = `
Cadangan database %s sudah dibuat dan diperiksa:
  Database : %s
  Tabel    : %d tabel
  Berkas   : %s
  Ukuran   : %d byte
  SHA-256  : %s
Simpan juga salinannya di server lain, misalnya:
  scp %s <pengguna>@<server-lain>:
  lalu di server lain: sha256sum %s (hasilnya harus sama dengan SHA-256 di atas)

`
	textCancelled  = "Dibatalkan; tidak ada yang dihapus. Cadangan tadi tetap ada di %s."
	textDropFailed = "Database %s tidak bisa dihapus (%s). Kunci baca-saja sudah terpasang lagi. Cadangan tadi tetap ada di %s."
	// A drop that happened although the window ended abnormally.
	textDroppedAbnormal     = "Database %s sudah dihapus. Kunci baca-saja server ini sudah terpasang lagi; perintah ini tidak perlu dijalankan lagi."
	textDroppedDespiteError = "Database %s sudah dihapus, walaupun MySQL melaporkan galat (%s). Kunci baca-saja server ini sudah terpasang lagi; perintah ini tidak perlu dijalankan lagi."
	textDroppedNotRelocked  = "Database %s sudah dihapus; perintah ini tidak perlu dijalankan lagi."
	// A drop whose outcome cannot be confirmed, and the lock's state.
	textDropUnknown = "Tidak bisa dipastikan apakah database %s sudah dihapus. Lihat dulu di server ini (hanya membaca): mysql -e 'SHOW DATABASES'. Bila database itu masih ada, jalankan perintah ini lagi; bila sudah tidak ada, perintah ini tidak perlu dijalankan lagi."
	textDropRunning = "MySQL mungkin masih menjalankan penghapusan database %s, jadi hasilnya belum bisa dipastikan. Jangan jalankan perintah ini lagi sekarang. Lihat di server ini (hanya membaca): mysql -e 'SHOW PROCESSLIST'. Selama masih ada baris DROP DATABASE di sana, tunggu. Sesudah baris itu hilang, lihat mysql -e 'SHOW DATABASES': bila database itu masih ada, jalankan perintah ini lagi; bila sudah tidak ada, perintah ini tidak perlu dijalankan lagi."
	textLockBack    = "Kunci baca-saja server ini sudah terpasang lagi."
	// The same three without a backup (--tanpa-cadangan): no sentence
	// about a file that does not exist.
	textDropWaitedNoBackup = "Database %s sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi."
	textCancelledNoBackup  = "Dibatalkan; tidak ada yang dihapus."
	textDropFailedNoBackup = "Database %s tidak bisa dihapus (%s). Kunci baca-saja sudah terpasang lagi."
)

// Why a backup fails its checks (the cause in C8).
const (
	causeExit        = "mysqldump keluar dengan kode %d"
	causeNoStart     = "mysqldump tidak bisa dijalankan: %v"
	causeKilled      = "mysqldump dihentikan oleh sinyal (%v)"
	causeNotComplete = `baris terakhir berkas bukan "-- Dump completed"`
	causeNoDatabase  = "berkas tidak memuat baris CREATE DATABASE untuk database ini"
	causeFewTables   = "berkas memuat %d baris CREATE TABLE untuk %d tabel"
	causeOpenBlock   = "berkas berakhir di dalam blok DELIMITER"
	causeCreate      = "berkas %s tidak bisa dibuat: %v"
	causeReadBack    = "berkas tidak bisa dibaca lagi: %v"
	causeSync        = "berkas tidak bisa disimpan ke disk: %v"
)

// systemSchemas are never dropped (compared without case).
var systemSchemas = []string{"mysql", "sys", "information_schema", "performance_schema"}

// backupFile is the backup as the command keeps it open from creation to
// the end: mysqldump writes it, and it is read back, hashed and synced
// through the same descriptor.
type backupFile interface {
	io.Reader
	io.Writer
	io.Seeker
	Stat() (fs.FileInfo, error)
	Sync() error
	Close() error
}

// backupFS is how the command creates, syncs and removes backup files;
// tests record it.
type backupFS struct {
	// Create creates a new file at path (O_EXCL, O_NOFOLLOW, 0600); an
	// existing path, a symlink included, is fs.ErrExist.
	Create  func(path string) (backupFile, error)
	SyncDir func(dir string) error
	Remove  func(path string) error
}

var osBackupFS = backupFS{
	Create: func(path string) (backupFile, error) {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, err
		}
		return f, nil
	},
	SyncDir: func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	},
	Remove: os.Remove,
}

// Seams: tests point the backup folder, the client program, the free space
// and the file layer at their own.
var (
	// cliBackupDir is drop-database's backup folder without --ke.
	cliBackupDir = "/var/lib/dbpanel/dibuang"
	cliMysqldump = "mysqldump"
	cliStatfs    = func(path string) (free uint64, err error) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			return 0, err
		}
		return st.Bavail * uint64(st.Bsize), nil
	}
	cliBackupFS = osBackupFS
)

type dropOptions struct {
	dir, name, folder, user string
	noBackup                bool // --tanpa-cadangan
}

// parseDropArgs parses "<nama> [--ke FOLDER] [--pengguna NAMA] [--dir DIR]
// [--tanpa-cadangan]", the flags before or after the name. --tanpa-cadangan
// is left out of the help on purpose (spec 7).
func parseDropArgs(args []string, stderr io.Writer) (o dropOptions, ok bool) {
	fs := newFlagSet("agent drop-database", stderr)
	dir := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	folder := fs.String("ke", "", "folder cadangan (bawaan "+cliBackupDir+")")
	user := fs.String("pengguna", "", "akun administrator MySQL selain root")
	noBackup := fs.Bool("tanpa-cadangan", false, "")
	if err := fs.Parse(args); err != nil {
		return o, false
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return o, false
	}
	o.name = fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return o, false
	}
	if fs.NArg() > 0 || o.name == "" {
		fs.Usage()
		return o, false
	}
	o.dir, o.folder, o.user, o.noBackup = *dir, *folder, *user, *noBackup
	return o, true
}

// mainDropDatabase is "agent drop-database <nama>": on a quarantined server
// it makes a checked backup of one database, shows it and how to look for
// the database on the new master, asks for the name to be typed, and
// drops the database in a window of read-only lifted (runLifted). Each
// drop is recorded with writeSyslog. A backup that cannot be made stops the
// command (P1), unless --tanpa-cadangan was given: then the operator is
// told why and asked for the name (P2), and the database is dropped without
// a backup; every other check and refusal is the same.
func mainDropDatabase(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int {
	o, ok := parseDropArgs(args, stderr)
	if !ok {
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n", a...)
		return 1
	}

	// 1. Quarantined only.
	p, err := mysql.LoadPins(o.dir)
	role := ""
	if err == nil {
		role, err = p.RoleErr()
	}
	if err != nil {
		return fail(textPinsUnreadable, err)
	}
	if role != "quarantined" {
		return fail(textNotQuarantined, role)
	}

	// 2. The lift lock free (an agent job may be holding read-only open).
	var lockErr *mysql.LiftLockError
	if err := probeLiftLock(cliLiftLock, cliProbeWait); errors.Is(err, mysql.ErrLiftBusy) || errors.As(err, &lockErr) {
		return fail("%s", err)
	} else if err != nil {
		return fail(textLiftUnchecked, err)
	}

	// 3. Logged in, the server locked.
	ctx := context.Background()
	answers := newPromptReader(in)
	s, err := cliRootSession(ctx, o.dir, o.user, answers, stdout)
	if err != nil {
		return fail("%s", err)
	}
	defer s.Close()
	c := s.Conn
	ro, sro, err := readFlags(ctx, c)
	if err != nil {
		return fail(textMySQLUnread, err)
	}
	if !ro || !sro {
		return fail(textNotLocked)
	}

	// 4. The database as the server names it.
	name, found, err := findDatabase(ctx, c, o.name)
	if err != nil {
		return fail(textMySQLUnread, err)
	}
	if !found {
		return fail(textNoSuchDatabase, printable(o.name))
	}
	pname := printable(name)
	for _, sys := range systemSchemas {
		if strings.EqualFold(name, sys) {
			return fail(textSystemSchema, pname)
		}
	}

	// 5. Its tables and whether the folder has room for them.
	if _, err := c.ExecContext(ctx, "SET SESSION information_schema_stats_expiry = 0"); err != nil {
		return fail(textMySQLUnread, err)
	}
	tables, estimate, err := baseTables(ctx, c, name)
	if err != nil {
		return fail(textMySQLUnread, err)
	}
	folder, err := backupFolder(o.folder)
	if err != nil {
		return fail(textFolderUnusable, printable(folder), err)
	}
	pfolder := printable(folder)
	if shared, err := sharedFolder(folder); err != nil {
		return fail(textFolderUnusable, pfolder, err)
	} else if shared {
		fmt.Fprintf(stdout, textSharedFolder+"\n", pfolder)
	}
	free, err := cliStatfs(folder)
	if err != nil {
		return fail(textFolderUnusable, pfolder, err)
	}
	// Another folder keeps the backup, so too little room is refused
	// with --tanpa-cadangan as well: the drop without a backup is never
	// offered while a safe way exists.
	if free <= estimate {
		return fail(textNoSpace, pfolder, free, pname, estimate)
	}

	// 6–7. The backup, checked and synced. Only a dump that was attempted
	// and failed leads, with --tanpa-cadangan, to a drop without one;
	// noBackup says why, and nothing of the attempt is left.
	var b *backup
	var noBackup string
	var exists *backupExistsError
	var dumpErr *dumpFailedError
	b, err = makeBackup(ctx, s, folder, name, tables)
	switch {
	case errors.As(err, &exists):
		return fail(textBackupExists, printable(exists.path))
	case errors.Is(err, errDumpInterrupted):
		return fail(textDumpInterrupted)
	case errors.As(err, &dumpErr) && !o.noBackup:
		return fail("%s", backupFailedText(name, printable(err.Error())))
	case errors.As(err, &dumpErr):
		noBackup = printable(err.Error())
	case err != nil:
		// Not the dump's own failure: the backup can be made once the
		// cause is fixed, so the drop without one is never offered.
		return fail(textBackupRefused, pname, printable(err.Error()))
	}
	var ppath string
	if b != nil {
		defer b.f.Close()
		ppath = printable(b.path)
	}

	// 8. What was made, how to look on the new master, the name typed.
	if b != nil {
		fmt.Fprintf(stdout, textBackupSummary, pname, pname, tables, ppath, b.size, b.sum,
			shellArg(ppath), shellArg(filepath.Base(ppath)))
	}
	if plainName.MatchString(name) {
		fmt.Fprintf(stdout, textCheckNewMaster+"\n", name)
	} else {
		fmt.Fprintf(stdout, textCheckNewHex+"\n", strings.ToUpper(hex.EncodeToString([]byte(name))))
	}
	if b != nil {
		fmt.Fprint(stdout, textConfirmDrop)
	} else {
		fmt.Fprintf(stdout, textConfirmNoBackup, pname, noBackup)
	}
	answer, err := answers.line()
	if err != nil || answer != name {
		if err != nil {
			fmt.Fprintln(stdout)
		}
		if b != nil {
			fmt.Fprintf(stdout, textCancelled+"\n", ppath)
		} else {
			fmt.Fprintln(stdout, textCancelledNoBackup)
		}
		return 1
	}

	// 9. The drop, in the window.
	recheck := func(ctx context.Context, c *sql.Conn) error {
		there, err := stillThere(ctx, c, name)
		if err != nil {
			return err
		}
		if !there {
			return errStateChanged
		}
		n, _, err := baseTables(ctx, c, name)
		if err != nil {
			return err
		}
		if n != tables {
			return errStateChanged
		}
		return nil
	}
	// From the moment the DROP is sent until its system log line is
	// written, signals are held here too: a second Ctrl-C after the window
	// must not end the command before the audit line of a drop.
	hold := make(chan os.Signal, 4)
	defer signal.Stop(hold)
	// sent is set at the moment the DROP is handed to the driver: before
	// it, no failure is a drop attempt (no check, no audit line).
	sent := false
	drop := func(ctx context.Context, c *sql.Conn) error {
		signal.Notify(hold, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		if err := ctx.Err(); err != nil {
			return err // the window already ended: nothing is sent
		}
		sent = true
		_, err := c.ExecContext(ctx, "DROP DATABASE "+quoteIdent(name))
		return err
	}
	record := func(window, outcome string) {
		kept := "backup=none backup_error=" + strconv.Quote(noBackup)
		if b != nil {
			kept = fmt.Sprintf("backup=%s sha256=%s size=%d", strconv.Quote(b.path), b.sum, b.size)
		}
		what := "database dropped"
		if outcome != "dropped" {
			what = "database drop unconfirmed"
		}
		line := fmt.Sprintf("%s: name=%s %s tables=%d window=%s outcome=%s mysql_user=%s %s", what,
			strconv.Quote(name), kept, tables, window, outcome, strconv.Quote(s.User), syslogWho())
		if err := writeSyslog(line); err != nil {
			fmt.Fprintf(stderr, "Peringatan: catatan ke log sistem gagal ditulis (%v).\n", err)
		}
	}
	err = runLifted(ctx, s, o.dir, cliLiftLock, recheck, drop)
	afterWindow()
	switch {
	case err == nil:
	case errors.Is(err, errLockWait) && b == nil:
		return fail(textDropWaitedNoBackup, pname)
	case errors.Is(err, errLockWait):
		return fail(textDropWaited, pname, ppath)
	case errors.Is(err, errStateChanged), errors.Is(err, mysql.ErrLiftBusy), errors.As(err, &lockErr):
		return fail("%s", err)
	default:
		return dropOutcome(ctx, s, err, name, b, sent, stdout, stderr, record)
	}

	// 10. Recorded, and what comes next.
	record("closed", "dropped")
	fmt.Fprintln(stdout, textDropDone)
	return 0
}

// dropOutcome ends drop-database after the window ended abnormally or the
// DROP failed (err), claiming no more than the server shows. When the DROP
// was never sent, it says that failure as before, without a check or an
// audit line. Otherwise: gone is recorded and said as dropped; still there,
// with the server's statement ended, gets the texts of what happened; when
// MySQL may still be running the DROP it says so and advises waiting, not
// running again; when the check failed it says the outcome is not known.
// Both of the last two are recorded with outcome=unknown. It returns the
// exit code (never 0).
func dropOutcome(ctx context.Context, s *rootSession, err error, name string, b *backup, sent bool, stdout, stderr io.Writer,
	record func(window, outcome string)) int {
	pname := printable(name)
	var lifted *liftedError
	var relock *relockError
	failed := func() {
		switch {
		case errors.As(err, &lifted), errors.Is(err, errInterrupted), errors.Is(err, errWindowCap), errors.As(err, &relock):
			fmt.Fprintln(stderr, err)
		case b == nil:
			fmt.Fprintf(stderr, textDropFailedNoBackup+"\n", pname, printable(err.Error()))
		default:
			fmt.Fprintf(stderr, textDropFailed+"\n", pname, printable(err.Error()), printable(b.path))
		}
	}
	if !sent {
		failed()
		return 1
	}
	window := "error"
	switch {
	case errors.As(err, &relock):
		window = "relock-failed"
	case errors.Is(err, errInterrupted):
		window = "interrupted"
	case errors.Is(err, errWindowCap):
		window = "cap"
	}
	running := errors.As(err, &lifted) && lifted.Running
	lockLine := func() {
		if relock != nil {
			fmt.Fprintln(stderr, relock.Error())
		} else {
			fmt.Fprintln(stderr, textLockBack)
		}
	}
	gone, cerr := cliDroppedNow(ctx, s, name)
	switch {
	case cerr == nil && gone:
		record(window, "dropped")
		switch window {
		case "relock-failed":
			fmt.Fprintf(stderr, textDroppedNotRelocked+"\n", pname)
			lockLine()
		case "error":
			fmt.Fprintf(stderr, textDroppedDespiteError+"\n", pname, printable(err.Error()))
		default:
			fmt.Fprintf(stderr, textDroppedAbnormal+"\n", pname)
		}
		fmt.Fprintln(stdout, textDropDone)
	case cerr == nil && !running:
		failed()
	case running:
		record(window, "unknown statement=running")
		fmt.Fprintf(stderr, textDropRunning+"\n", pname)
		lockLine()
	default:
		record(window, "unknown statement=ended")
		fmt.Fprintf(stderr, textDropUnknown+"\n", pname)
		lockLine()
	}
	return 1
}

// Seams: tests replace the check of the database after the window, and
// land a signal right after the window.
var (
	cliDroppedNow = droppedNow
	afterWindow   = func() {}
)

// droppedNow reports whether database name is gone, read on a connection of
// its own (the session's may have been cut when the window ended).
func droppedNow(ctx context.Context, s *rootSession, name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	there, err := stillThere(ctx, conn, name)
	return !there, err
}

// plainName is a database name C9 can quote on a command line as is.
var plainName = regexp.MustCompile(`^[A-Za-z0-9_.$-]+$`)

// plainArg is a shell word that needs no quotes.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:@=+-]+$`)

// backupFailedText is P1 for database name (raw) and the printable cause.
// When printable changes the name, a command built from what is shown
// would name another database (ruling D55): P1 then prints no command and
// says to run the same command again with --tanpa-cadangan added.
func backupFailedText(name, cause string) string {
	pname := printable(name)
	if pname != name {
		return fmt.Sprintf(textBackupFailedOdd, pname, cause)
	}
	return fmt.Sprintf(textBackupFailed, pname, cause, shellArg(pname))
}

// shellArg is s as one shell word: as is when it holds only safe
// characters, else in single quotes.
func shellArg(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// findDatabase is the database the server knows by name: the exact name,
// else the one name that differs only in case.
func findDatabase(ctx context.Context, c *sql.Conn, name string) (string, bool, error) {
	rows, err := c.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata")
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var folded []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", false, err
		}
		if n == name {
			return n, true, nil
		}
		if strings.EqualFold(n, name) {
			folded = append(folded, n)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(folded) == 1 {
		return folded[0], true, nil
	}
	return "", false, nil
}

// exactSchema compares a schema column with the name byte for byte,
// whatever the column's and the connection's collations.
const exactSchema = "= CONVERT(? USING utf8mb4) COLLATE utf8mb4_bin"

// stillThere reports whether database name (exactly) exists.
func stillThere(ctx context.Context, c *sql.Conn, name string) (bool, error) {
	var n int
	err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name "+exactSchema, name).Scan(&n)
	return n > 0, err
}

// baseTables counts the base tables of database name and estimates their
// size (data and indexes).
func baseTables(ctx context.Context, c *sql.Conn, name string) (n int, size uint64, err error) {
	err = c.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables "+
		"WHERE table_type = 'BASE TABLE' AND table_schema "+exactSchema, name).Scan(&n, &size)
	return n, size, err
}

// backupFolder is the folder the backup goes to: the operator's (made
// absolute), else cliBackupDir, created 0700 when missing.
func backupFolder(given string) (string, error) {
	if given == "" {
		return cliBackupDir, os.MkdirAll(cliBackupDir, 0o700)
	}
	abs, err := filepath.Abs(given)
	if err != nil {
		return given, err
	}
	return abs, nil
}

// sharedFolder reports whether folder is not owned by the process's user
// (root) or is writable by its group or others (C7).
func sharedFolder(folder string) (bool, error) {
	fi, err := os.Stat(folder)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, errors.New("bukan folder")
	}
	return ownerOf(fi) != os.Geteuid() || fi.Mode().Perm()&0o022 != 0, nil
}

func ownerOf(fi fs.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// backup is a checked backup, still open.
type backup struct {
	path string
	f    backupFile
	size int64
	sum  string // SHA-256, hex
}

// backupExistsError (C6) is a backup path that exists already.
type backupExistsError struct{ path string }

func (e *backupExistsError) Error() string { return "exists: " + e.path }

// dumpFailedError is a backup whose mysqldump connected (its output
// begins with its header) and then exited 2 or 3: a MySQL error, such as a
// name the client cannot "use" or a table it cannot read. Only this is "a
// backup that cannot be made" (P1, or P2 with --tanpa-cadangan). Every
// other failure of makeBackup is refused (P1b): a dump that exited 0 but
// fails a check of step 7 (running again is the remedy), exit 5 (a write
// error: another folder is), any other code, a dump with no output (the
// client never connected), and the failures that are not the dump's own.
type dumpFailedError struct{ cause string }

func (e *dumpFailedError) Error() string { return e.cause }

// dumpFailed is a dumpFailedError with the cause format and args give.
func dumpFailed(format string, a ...any) error {
	return &dumpFailedError{fmt.Sprintf(format, a...)}
}

// errDumpInterrupted is a backup a signal stopped.
var errDumpInterrupted = errors.New("dump interrupted")

// makeBackup dumps database name (with tables base tables) into a new file
// in folder, named after the name (safeName) and the UTC time, and checks
// it (X5) through the descriptor it was written to: mysqldump exited 0, the
// last line begins with "-- Dump completed", the CREATE DATABASE line is
// there, and outside DELIMITER blocks there are at least tables lines
// beginning with "CREATE TABLE ", with no block left open. The file and
// its folder are synced. A backup that fails is removed; its error words
// why (the cause in C8).
func makeBackup(ctx context.Context, s *rootSession, folder, name string, tables int) (*backup, error) {
	path := filepath.Join(folder, safeName(name)+"-"+cliNow().UTC().Format("20060102T150405Z")+".sql")
	f, err := cliBackupFS.Create(path)
	if errors.Is(err, fs.ErrExist) {
		return nil, &backupExistsError{path}
	}
	if err != nil {
		return nil, fmt.Errorf(causeCreate, path, err)
	}
	b := &backup{path: path, f: f}
	if err := b.fill(ctx, s, name, tables); err != nil {
		b.discard()
		return nil, err
	}
	if err := cliBackupFS.SyncDir(folder); err != nil {
		b.discard()
		return nil, fmt.Errorf(causeSync, err)
	}
	return b, nil
}

// fill runs mysqldump into b and checks, hashes and syncs what it wrote.
func (b *backup) fill(ctx context.Context, s *rootSession, name string, tables int) error {
	// A signal while mysqldump runs stops it; the incomplete file goes.
	sctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	cmd, err := s.dumpCommand(sctx, cliMysqldump, "--single-transaction", "--routines", "--triggers", "--events",
		"--set-gtid-purged=OFF", "--databases", "--", name)
	if err != nil {
		return fmt.Errorf(causeNoStart, err)
	}
	var stderr capped
	cmd.Stdout, cmd.Stderr = b.f, &stderr
	err = cmd.Start()
	for _, f := range cmd.ExtraFiles {
		f.Close()
	}
	if err != nil {
		return fmt.Errorf(causeNoStart, err)
	}
	err = cmd.Wait()
	if sctx.Err() != nil && ctx.Err() == nil {
		return errDumpInterrupted
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// Killed from outside (the OOM killer, a person): not its own exit,
		// so not a dump that failed.
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return fmt.Errorf(causeKilled, ws.Signal())
		}
		cause := fmt.Sprintf(causeExit, exit.ExitCode())
		if first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); first != "" {
			cause += ": " + first
		}
		if code := exit.ExitCode(); (code == 2 || code == 3) && b.connected() {
			return dumpFailed("%s", cause)
		}
		return errors.New(cause)
	}
	if err != nil {
		return fmt.Errorf(causeNoStart, err)
	}
	if _, err := b.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf(causeReadBack, err)
	}
	h := sha256.New()
	st, err := scanDump(b.f, h, name)
	if err != nil {
		return fmt.Errorf(causeReadBack, err)
	}
	switch {
	case !strings.HasPrefix(st.last, "-- Dump completed"):
		return errors.New(causeNotComplete)
	case !st.database:
		return errors.New(causeNoDatabase)
	case st.tables < tables:
		return fmt.Errorf(causeFewTables, st.tables, tables)
	case st.open:
		return errors.New(causeOpenBlock)
	}
	if err := b.f.Sync(); err != nil {
		return fmt.Errorf(causeSync, err)
	}
	b.size, b.sum = st.size, hex.EncodeToString(h.Sum(nil))
	return nil
}

// dumpHeader begins every dump of a mysqldump that connected (measured on
// 8.0.46: the header line names the server's version, so the client writes
// it only once connected).
const dumpHeader = "-- MySQL dump "

// connected reports whether what mysqldump wrote to b begins with its
// header, read through b's descriptor.
func (b *backup) connected() bool {
	if _, err := b.f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	head := make([]byte, len(dumpHeader))
	n, _ := io.ReadFull(b.f, head)
	return string(head[:n]) == dumpHeader
}

// discard removes the backup file, when the path still names it, and
// closes it.
func (b *backup) discard() {
	if fi, err := b.f.Stat(); err == nil {
		if at, err := os.Lstat(b.path); err == nil && os.SameFile(fi, at) {
			cliBackupFS.Remove(b.path)
		}
	}
	b.f.Close()
}

// dumpStats is what scanDump found in a dump.
type dumpStats struct {
	last     string // the last line (its first lineHead bytes)
	database bool   // the CREATE DATABASE line of the database
	tables   int    // lines beginning with "CREATE TABLE " outside DELIMITER blocks
	open     bool   // a DELIMITER block still open at the end
	size     int64
}

// lineHead is how much of a line scanDump looks at.
const lineHead = 1024

// scanDump reads a mysqldump --databases dump of database name from r,
// writing every byte to h as well.
func scanDump(r io.Reader, h hash.Hash, name string) (dumpStats, error) {
	var st dumpStats
	createDB := "CREATE DATABASE /*!32312 IF NOT EXISTS*/ " + quoteIdent(name) + " "
	br := bufio.NewReaderSize(io.TeeReader(r, h), 64<<10)
	var head []byte
	for {
		chunk, err := br.ReadSlice('\n')
		st.size += int64(len(chunk))
		if room := lineHead - len(head); room > 0 {
			head = append(head, chunk[:min(len(chunk), room)]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return st, err
		}
		if len(chunk) > 0 || len(head) > 0 {
			line := string(bytes.TrimSuffix(head, []byte("\n")))
			switch {
			case line == "DELIMITER ;;":
				st.open = true
			case line == "DELIMITER ;":
				st.open = false
			case !st.open && strings.HasPrefix(line, "CREATE TABLE "):
				st.tables++
			}
			if strings.HasPrefix(line, createDB) {
				st.database = true
			}
			st.last = line
		}
		head = head[:0]
		if err != nil {
			return st, nil
		}
	}
}

// safeName is name as part of a file name: every character outside
// [A-Za-z0-9_.-] becomes "_".
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// capped keeps the first 4 KiB written to it.
type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := 4096 - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *capped) String() string { return c.b.String() }
