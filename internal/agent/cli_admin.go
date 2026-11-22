package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// The admin account commands, grant-admin and reset-admin: on a quarantined
// server they change the agent's MySQL account in a window of read-only
// lifted (runLifted), as drop-database drops a database.

// Texts of grant-admin and reset-admin: spec 5.8 (C16 to C19), and the lines
// these commands word themselves.
const (
	textNoStoredAccount = "agent belum punya akun MySQL tersimpan; jalankan sudo dbpanel agent reset-admin"
	textStoredRefused   = "akun MySQL yang disimpan agent tidak bisa masuk; jalankan sudo dbpanel agent reset-admin"
	textNothingMissing  = "Akun %s sudah punya semua hak yang dibutuhkan."
	textResetDone       = "Akun dbpanel_admin@localhost dibuat ulang dan disimpan sebagai akun agent. Di panel, klik Segarkan di halaman server ini."

	textStoredUnread   = "akun MySQL yang disimpan agent tidak bisa dibaca (%v); tidak ada yang diubah."
	textCredsUnusable  = "tempat akun MySQL agent tidak bisa dibuka (%v); tidak ada yang diubah."
	textUnsupportedAcc = "nama akun MySQL yang disimpan agent (%s) memuat karakter yang tidak didukung pemeriksaan daftar database, jadi perintah ini tidak bisa melengkapinya; tidak ada yang diubah. Jalankan sudo dbpanel agent reset-admin"
	textConfirmYa      = "Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan: "
	textNothingDone    = "Dibatalkan; tidak ada yang diubah."
	textGrantPlan      = "Akun MySQL agent %s belum punya semua hak yang dibutuhkan. Pernyataan berikut dijalankan di MySQL server ini (tanpa masuk log biner), selagi kunci baca-saja dibuka sebentar:\n"
	textGrantDone      = `Hak akun %s sudah dilengkapi. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau klik Segarkan di halaman server ini.`
	textGrantFailed    = "Hak akun %s tidak bisa diberikan (%s). Kunci baca-saja server ini sudah terpasang lagi."
	// After a window that ended abnormally or a GRANT that failed, as the
	// server shows it.
	textGrantedAbnormal = "Hak akun %s sudah lengkap. Kunci baca-saja server ini sudah terpasang lagi; perintah ini tidak perlu dijalankan lagi."
	textGrantedNoRelock = "Hak akun %s sudah lengkap; perintah ini tidak perlu dijalankan lagi."
	textGrantNext       = `Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau klik Segarkan di halaman server ini.`
	textGrantPartial    = "Sebagian hak akun %s sudah diberikan, sebagian belum. Jalankan perintah ini lagi untuk memberi sisanya."
	textGrantUnknown    = "Tidak bisa dipastikan apakah hak akun %s sudah dilengkapi (%s). Lihat dengan menjalankan perintah ini lagi: sebelum mengubah apa pun, perintah ini menampilkan hak yang masih kurang, atau mengatakan tidak ada yang kurang."
	textGrantRunning    = "MySQL mungkin masih menjalankan pernyataan GRANT perintah ini, jadi hasilnya belum bisa dipastikan. Jangan jalankan perintah ini lagi sekarang. Lihat di server ini (hanya membaca): mysql -e 'SHOW PROCESSLIST'. Selama masih ada baris GRANT di sana, tunggu. Sesudah baris itu hilang, jalankan perintah ini lagi: sebelum mengubah apa pun, perintah ini menampilkan hak yang masih kurang, atau mengatakan tidak ada yang kurang."

	textResetPlan = `Akun MySQL agent akan dibuat ulang: dbpanel_admin@localhost, dengan kata sandi acak baru dan semua hak. Pernyataan berikut dijalankan di MySQL server ini (tanpa masuk log biner), selagi kunci baca-saja dibuka sebentar:
  CREATE USER IF NOT EXISTS 'dbpanel_admin'@'localhost' IDENTIFIED BY <kata sandi acak>
  ALTER USER 'dbpanel_admin'@'localhost' IDENTIFIED WITH caching_sha2_password BY <kata sandi acak> REQUIRE NONE ACCOUNT UNLOCK
  GRANT ALL PRIVILEGES ON *.* TO 'dbpanel_admin'@'localhost' WITH GRANT OPTION
Sesudah itu perintah ini mencoba masuk ke MySQL dengan akun itu. Hanya bila berhasil, akun itu disimpan sebagai akun agent, menggantikan akun yang disimpan sekarang (akun itu tidak dihapus dari MySQL). Kata sandinya tidak ditampilkan.
`
	textResetFailed      = "Akun dbpanel_admin@localhost tidak bisa dibuat ulang (%s). Kunci baca-saja server ini sudah terpasang lagi."
	textResetNotStored   = "Akun yang disimpan agent belum diganti. Akun dbpanel_admin@localhost di MySQL mungkin sudah dibuat atau kata sandinya sudah diganti; bila agent memakai akun itu, agent tidak bisa masuk sampai perintah ini selesai. Jalankan perintah ini lagi."
	textResetAbnormal    = "Perintah ini tidak selesai seperti biasa, tetapi akun dbpanel_admin@localhost sudah dibuat ulang dan disimpan sebagai akun agent; perintah ini tidak perlu dijalankan lagi."
	textResetLoginFailed = "Akun dbpanel_admin@localhost sudah dibuat ulang di MySQL, tetapi tidak bisa dipakai masuk (%s), jadi tidak disimpan; akun yang disimpan agent tidak diganti. Bila agent memakai akun dbpanel_admin@localhost, agent tidak bisa masuk sekarang. Jalankan perintah ini lagi; bila sebab yang sama muncul lagi, sebab itu ada di pengaturan MySQL server ini dan perintah ini tidak bisa mengubahnya: periksa sebab itu bersama administrator MySQL server ini."
	textResetUnknown     = "Tidak bisa dipastikan apakah akun dbpanel_admin@localhost sudah disimpan sebagai akun agent (%s). Jalankan perintah ini lagi."
	textResetSaveFailed  = "Akun dbpanel_admin@localhost sudah dibuat ulang dan bisa masuk, tetapi tidak bisa disimpan agent (%s). Kunci baca-saja server ini sudah terpasang lagi."
	textResetOldInUse    = "Agent masih memakai akun lama dan mungkin tidak bisa masuk ke MySQL. Perbaiki sebabnya, lalu jalankan perintah ini lagi."
	textStoredReplaced   = "Akun MySQL yang disimpan agent tidak bisa dibaca (%v); akun itu akan diganti dengan yang baru."
	textResetRunning     = "MySQL mungkin masih menjalankan pernyataan perintah ini pada akun dbpanel_admin@localhost, jadi hasilnya belum bisa dipastikan; akun yang disimpan agent belum diganti. Jangan jalankan perintah ini lagi sekarang. Lihat di server ini (hanya membaca): mysql -e 'SHOW PROCESSLIST'. Selama masih ada baris CREATE USER, ALTER USER atau GRANT di sana, tunggu. Sesudah baris itu hilang, jalankan perintah ini lagi."
	textSyslogFailed     = "Peringatan: catatan ke log sistem gagal ditulis (%v).\n"
)

// Seams: tests point the agent's credential store and its MySQL at their
// own.
var (
	// cliAgentCreds is the agent's credential store in dir, as
	// RegisterMySQL opens it.
	cliAgentCreds = func(dir string) (mysql.CredentialStore, error) {
		box, err := secrets.LoadOrCreate(filepath.Join(dir, secretsKeyFile))
		if err != nil {
			return nil, err
		}
		return mysql.NewFileCredentialStore(filepath.Join(dir, credFile), box), nil
	}
	// cliStoredHost is this server's MySQL logged in to with creds.
	cliStoredHost = func(cfg config.Config, creds mysql.CredentialStore) (mysql.Host, error) {
		return mysql.NewRealHost(cfg, creds, probe.RealSystem(), slog.New(slog.DiscardHandler))
	}
)

// cliAdminLogin logs in to h's MySQL as p and reads CURRENT_USER(), as the
// agent would log in with p stored; tests replace it.
var cliAdminLogin = mysql.LoginAs

// adminLoginWait bounds reset-admin's login with the new account.
const adminLoginWait = 30 * time.Second

// loginCheckError is a new account whose login failed, so it was not
// stored.
type loginCheckError struct{ err error }

func (e *loginCheckError) Error() string { return e.err.Error() }
func (e *loginCheckError) Unwrap() error { return e.err }

// storeWriteError is a new account whose login worked but which the
// agent's store could not keep.
type storeWriteError struct{ err error }

func (e *storeWriteError) Error() string { return e.err.Error() }
func (e *storeWriteError) Unwrap() error { return e.err }

// verifyingStore is the agent's store, whose Save first logs in with the
// account (on h, bounded by ctx and adminLoginWait) and stores it only when
// that login worked as dbpanel_admin@localhost; otherwise Save is a
// *loginCheckError and the store is untouched. A store that fails to
// write after a good login is a *storeWriteError.
type verifyingStore struct {
	mysql.CredentialStore
	ctx context.Context
	h   mysql.Host
}

func (v *verifyingStore) Save(p proto.ConnectParams) error {
	ctx, cancel := context.WithTimeout(v.ctx, adminLoginWait)
	defer cancel()
	account, err := cliAdminLogin(ctx, v.h, p)
	if err == nil && account != "dbpanel_admin@localhost" {
		err = fmt.Errorf("masuk sebagai %s", account)
	}
	if err != nil {
		return &loginCheckError{err}
	}
	if err := v.CredentialStore.Save(p); err != nil {
		return &storeWriteError{err}
	}
	return nil
}

// parseAdminArgs parses "[--pengguna NAMA] [--dir DIR]".
func parseAdminArgs(name string, args []string, stderr io.Writer) (dir, user string, ok bool) {
	fs := newFlagSet(name, stderr)
	d := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	u := fs.String("pengguna", "", "akun administrator MySQL selain root")
	if err := fs.Parse(args); err != nil {
		return "", "", false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return "", "", false
	}
	return *d, *u, true
}

// adminCommand is what both commands share around the window.
type adminCommand struct {
	dir    string
	stdout io.Writer
	stderr io.Writer
}

func (a *adminCommand) fail(format string, args ...any) int {
	fmt.Fprintf(a.stderr, format+"\n", args...)
	return 1
}

// quarantinedAndFree is steps 1 and 2 of both commands: the role pin is
// quarantined (C1) and the lift lock is free (C3, C4); code is the exit
// code when not.
func (a *adminCommand) quarantinedAndFree() (code int, ok bool) {
	p, err := mysql.LoadPins(a.dir)
	role := ""
	if err == nil {
		role, err = p.RoleErr()
	}
	if err != nil {
		return a.fail(textPinsUnreadable, err), false
	}
	if role != "quarantined" {
		return a.fail(textNotQuarantined, role), false
	}
	var lockErr *mysql.LiftLockError
	if err := probeLiftLock(cliLiftLock, cliProbeWait); errors.Is(err, mysql.ErrLiftBusy) || errors.As(err, &lockErr) {
		return a.fail("%s", err), false
	} else if err != nil {
		return a.fail(textLiftUnchecked, err), false
	}
	return 0, true
}

// locked is step 4: both flags on (C2).
func (a *adminCommand) locked(ctx context.Context, s *rootSession) (code int, ok bool) {
	ro, sro, err := readFlags(ctx, s.Conn)
	if err != nil {
		return a.fail(textMySQLUnread, err), false
	}
	if !ro || !sro {
		return a.fail(textNotLocked), false
	}
	return 0, true
}

// confirmed asks for "ya" on stdout.
func (a *adminCommand) confirmed(answers *promptReader) bool {
	fmt.Fprint(a.stdout, textConfirmYa)
	answer, err := answers.line()
	if err != nil {
		fmt.Fprintln(a.stdout)
	}
	if err != nil || strings.TrimSpace(answer) != "ya" {
		fmt.Fprintln(a.stdout, textNothingDone)
		return false
	}
	return true
}

// refused reports whether err is a refusal of runLifted before anything
// was sent (C11, C3, C4).
func refused(err error) bool {
	var lockErr *mysql.LiftLockError
	return errors.Is(err, errStateChanged) || errors.Is(err, mysql.ErrLiftBusy) || errors.As(err, &lockErr)
}

// windowName is how a window that did not end normally ended, for the
// system log.
func windowName(err error) string {
	var relock *relockError
	switch {
	case errors.As(err, &relock):
		return "relock-failed"
	case errors.Is(err, errInterrupted):
		return "interrupted"
	case errors.Is(err, errWindowCap):
		return "cap"
	}
	return "error"
}

// abnormal reports whether err is a window that ended abnormally (its
// text is then the operator's line: C13, C14 or the relock failure).
func abnormal(err error) bool {
	var lifted *liftedError
	var relock *relockError
	return errors.As(err, &lifted) || errors.As(err, &relock) || errors.Is(err, errInterrupted) || errors.Is(err, errWindowCap)
}

// lockLine says the lock is back, or the relock failure in err.
func (a *adminCommand) lockLine(err error) {
	var relock *relockError
	if errors.As(err, &relock) {
		fmt.Fprintln(a.stderr, relock.Error())
	} else {
		fmt.Fprintln(a.stderr, textLockBack)
	}
}

// holdSignals keeps SIGINT, SIGTERM and SIGHUP from ending the command
// from the first statement sent until its system log line is written;
// stop ends the hold.
func holdSignals() (start, stop func()) {
	hold := make(chan os.Signal, 4)
	return func() { signal.Notify(hold, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP) },
		func() { signal.Stop(hold) }
}

// mainGrantAdmin is "agent grant-admin": on a quarantined server it gives
// the agent's stored MySQL account, named as MySQL knows it
// (StoredAccountName), the privileges it lacks (MissingAdminGrants): the
// replica privileges a rebuild checks and SHOW DATABASES when its listing
// of databases cannot be proved complete. It shows the GRANT statements,
// asks for "ya", and runs them in a window of read-only lifted. A grant is
// recorded with writeSyslog.
func mainGrantAdmin(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int {
	dir, user, ok := parseAdminArgs("agent grant-admin", args, stderr)
	if !ok {
		return 2
	}
	a := &adminCommand{dir: dir, stdout: stdout, stderr: stderr}
	if code, ok := a.quarantinedAndFree(); !ok {
		return code
	}
	ctx := context.Background()

	// 3. The stored account, as MySQL knows it.
	creds, err := cliAgentCreds(dir)
	if err != nil {
		return a.fail(textCredsUnusable, err)
	}
	// Without agent.yaml (corrupt or missing) the MySQL its socket
	// override names is not known: refused (ruling D57).
	cfg, err := config.Load(dir)
	if err != nil {
		return a.fail(textNoLayout, err)
	}
	h, err := cliStoredHost(cfg, creds)
	if err != nil {
		return a.fail(textNoLayout, err)
	}
	account, err := mysql.StoredAccountName(ctx, h)
	switch {
	case errors.Is(err, mysql.ErrNotConnected):
		return a.fail(textNoStoredAccount)
	case mysql.LoginRefused(err):
		return a.fail(textStoredRefused)
	case err != nil:
		return a.fail(textStoredUnread, err)
	}
	paccount := printable(account)

	// 4. Logged in as root, the server locked.
	answers := newPromptReader(in)
	s, err := cliRootSession(ctx, dir, user, answers, stdout)
	if err != nil {
		return a.fail("%s", err)
	}
	defer s.Close()
	if code, ok := a.locked(ctx, s); !ok {
		return code
	}

	// 5. What is missing, shown; "ya".
	stmts, err := mysql.MissingAdminGrants(ctx, s.Conn, account)
	if errors.Is(err, mysql.ErrUnsupportedAccount) {
		return a.fail(textUnsupportedAcc, paccount)
	}
	if err != nil {
		return a.fail(textMySQLUnread, err)
	}
	if len(stmts) == 0 {
		fmt.Fprintf(stdout, textNothingMissing+"\n", paccount)
		return 0
	}
	fmt.Fprintf(stdout, textGrantPlan, paccount)
	for _, q := range stmts {
		fmt.Fprintf(stdout, "  %s\n", printable(q))
	}
	if !a.confirmed(answers) {
		return 1
	}

	// 6. The grants, in the window.
	recheck := func(ctx context.Context, c *sql.Conn) error {
		again, err := mysql.MissingAdminGrants(ctx, c, account)
		if err != nil {
			return err
		}
		if !slices.Equal(again, stmts) {
			return errStateChanged
		}
		return nil
	}
	hold, unhold := holdSignals()
	defer unhold()
	sent := false
	grant := func(ctx context.Context, c *sql.Conn) error {
		hold()
		for _, q := range stmts {
			if err := ctx.Err(); err != nil {
				return err // the window ended: nothing more is sent
			}
			sent = true
			if _, err := c.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	}
	record := func(window, outcome string) {
		line := fmt.Sprintf("admin account grants: account=%s statements=%s window=%s outcome=%s mysql_user=%s %s",
			strconv.Quote(account), strconv.Quote(strings.Join(stmts, "; ")), window, outcome, strconv.Quote(s.User), syslogWho())
		if err := writeSyslog(line); err != nil {
			fmt.Fprintf(stderr, textSyslogFailed, err)
		}
	}
	err = runLifted(ctx, s, dir, cliLiftLock, recheck, grant)
	switch {
	case err == nil:
		record("closed", "done")
		fmt.Fprintf(stdout, textGrantDone+"\n", paccount)
		return 0
	case refused(err):
		return a.fail("%s", err)
	}
	failed := func() {
		if abnormal(err) {
			fmt.Fprintln(stderr, err)
		} else {
			fmt.Fprintf(stderr, textGrantFailed+"\n", paccount, printable(err.Error()))
		}
	}
	if !sent {
		failed()
		return 1
	}

	// 7. A statement was sent and the window did not end well: what the
	// server shows, and no more.
	window := windowName(err)
	var lifted *liftedError
	running := errors.As(err, &lifted) && lifted.Running
	still, cerr := grantsNow(ctx, s, account)
	switch {
	case cerr == nil && len(still) == 0:
		record(window, "done")
		if window == "relock-failed" {
			fmt.Fprintf(stderr, textGrantedNoRelock+"\n", paccount)
			a.lockLine(err)
		} else {
			fmt.Fprintf(stderr, textGrantedAbnormal+"\n", paccount)
		}
		fmt.Fprintln(stdout, textGrantNext)
	case cerr == nil && !running && slices.Equal(still, stmts):
		failed() // nothing changed
	case cerr == nil && !running:
		record(window, "partial")
		failed()
		fmt.Fprintf(stderr, textGrantPartial+"\n", paccount)
	case running:
		record(window, "unknown statement=running")
		fmt.Fprintln(stderr, textGrantRunning)
		a.lockLine(err)
	default:
		record(window, "unknown statement=ended")
		fmt.Fprintf(stderr, textGrantUnknown+"\n", paccount, printable(cerr.Error()))
		a.lockLine(err)
	}
	return 1
}

// grantsNow is what account still lacks, read on a connection of its own
// (the session's may have been cut when the window ended).
func grantsNow(ctx context.Context, s *rootSession, account string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return mysql.MissingAdminGrants(ctx, conn, account)
}

// mainResetAdmin is "agent reset-admin": on a quarantined server it
// creates dbpanel_admin@localhost when missing, gives it a new random
// password and every privilege, and then stores it as the agent's account
// (mysql.ResetAdmin), in a window of read-only lifted, after showing the
// statements and asking for "ya". The account in MySQL changes first and
// the stored credential last, so the store never holds a password MySQL
// does not have yet; running the command again always repairs an
// interrupted run. Each run that sent a statement is recorded with
// writeSyslog. The password goes only into the statements and the store.
func mainResetAdmin(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int {
	dir, user, ok := parseAdminArgs("agent reset-admin", args, stderr)
	if !ok {
		return 2
	}
	a := &adminCommand{dir: dir, stdout: stdout, stderr: stderr}
	if code, ok := a.quarantinedAndFree(); !ok {
		return code
	}
	ctx := context.Background()

	// 3. The store the account goes to, openable before anything changes
	// (a stored account it cannot read is replaced: F3).
	creds, err := cliAgentCreds(dir)
	if err != nil {
		return a.fail(textCredsUnusable, err)
	}
	// A stored account that cannot be read is replaced (the store is
	// rewritten at the end); before is then empty.
	before, _, err := creds.Load()
	if err != nil {
		fmt.Fprintf(stdout, textStoredReplaced+"\n", printable(err.Error()))
		before = proto.ConnectParams{}
	}
	// Without agent.yaml (corrupt or missing) the MySQL its socket
	// override names is not known: refused (ruling D57).
	cfg, err := config.Load(dir)
	if err != nil {
		return a.fail(textNoLayout, err)
	}
	h, err := cliStoredHost(cfg, creds)
	if err != nil {
		return a.fail(textNoLayout, err)
	}

	// 4. Logged in as root, the server locked.
	answers := newPromptReader(in)
	s, err := cliRootSession(ctx, dir, user, answers, stdout)
	if err != nil {
		return a.fail("%s", err)
	}
	defer s.Close()
	if code, ok := a.locked(ctx, s); !ok {
		return code
	}

	// 5. What will be done; "ya".
	fmt.Fprint(stdout, textResetPlan)
	if !a.confirmed(answers) {
		return 1
	}

	// 6. The account, in the window, then the store.
	hold, unhold := holdSignals()
	defer unhold()
	sent := false
	reset := func(ctx context.Context, c *sql.Conn) error {
		hold()
		if err := ctx.Err(); err != nil {
			return err // the window ended: nothing is sent
		}
		sent = true
		return mysql.ResetAdmin(ctx, c, &verifyingStore{CredentialStore: creds, ctx: ctx, h: h})
	}
	record := func(window, outcome string) {
		line := fmt.Sprintf("admin account reset: account=%s window=%s outcome=%s mysql_user=%s %s",
			strconv.Quote(mysql.AdminAccount), window, outcome, strconv.Quote(s.User), syslogWho())
		if err := writeSyslog(line); err != nil {
			fmt.Fprintf(stderr, textSyslogFailed, err)
		}
	}
	fixed := func(context.Context, *sql.Conn) error { return nil } // the statements do not depend on the server
	err = runLifted(ctx, s, dir, cliLiftLock, fixed, reset)
	switch {
	case err == nil:
		record("closed", "done")
		fmt.Fprintln(stdout, textResetDone)
		return 0
	case refused(err):
		return a.fail("%s", err)
	}
	failed := func() {
		if abnormal(err) {
			fmt.Fprintln(stderr, err)
		} else {
			fmt.Fprintf(stderr, textResetFailed+"\n", printable(err.Error()))
		}
	}
	if !sent {
		failed()
		return 1
	}

	// 7. What the store and the server show, and no more. The store is
	// written last, after a login with the new account worked, so a new
	// account in it means every statement ran and the account logs in.
	window := windowName(err)
	var lifted *liftedError
	doErr := err
	running := false
	if errors.As(err, &lifted) {
		doErr, running = lifted.DoErr, lifted.Running
	}
	var loginErr *loginCheckError
	var storeErr *storeWriteError
	now, stored, lerr := creds.Load()
	saved := lerr == nil && stored && now.User == "dbpanel_admin" && now.Password != before.Password
	switch {
	case saved:
		record(window, "done")
		fmt.Fprintln(stderr, textResetAbnormal)
		a.lockLine(err)
		fmt.Fprintln(stdout, textResetDone)
	case running:
		record(window, "unknown statement=running")
		fmt.Fprintln(stderr, textResetRunning)
		a.lockLine(err)
	case lerr != nil:
		record(window, "unknown stored=unread")
		failed()
		fmt.Fprintf(stderr, textResetUnknown+"\n", printable(lerr.Error()))
	case !abnormal(err) && errors.As(doErr, &loginErr):
		record(window, "login-failed")
		fmt.Fprintf(stderr, textResetLoginFailed+"\n", printable(loginErr.Error()))
		a.lockLine(err)
	case !abnormal(err) && errors.As(doErr, &storeErr):
		record(window, "not-stored")
		fmt.Fprintf(stderr, textResetSaveFailed+"\n", printable(storeErr.Error()))
		fmt.Fprintln(stderr, textResetOldInUse)
	default:
		record(window, "not-stored")
		failed()
		fmt.Fprintln(stderr, textResetNotStored)
	}
	return 1
}
