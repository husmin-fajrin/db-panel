package agent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"log/syslog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const usage = `Penggunaan:
  dbpanel agent [--dir DIR] [--panel URL]                jalankan agent; di host panel, berpasangan
                                                         dulu lewat DIR/local-pair-token bila belum
  DBPANEL_TOKEN=TOKEN dbpanel agent pair --panel URL --ca-sha256 HEX
      [--agent-addr HOST:PORT] [--dir DIR]               pasangkan agent ke panel
  dbpanel agent pair --panel URL --ca-sha256 HEX --token-stdin [--dir DIR]
                                                         idem, token dari stdin
  dbpanel agent pair --panel URL --token-file PATH [--dir DIR]
                                                         idem, token dan pin dari berkas (dihapus
                                                         setelah berhasil)
  dbpanel agent set-role {master|replica|quarantined|unassigned} [--ya] [--dir DIR]
                                                         ubah peran yang dipin; --ya: tanpa pertanyaan
                                                         pada server yang dikarantina (quarantined)
  dbpanel agent set-source HOST:PORT [--dir DIR]         ubah sumber replikasi yang dipin
  dbpanel agent lock-master [--dir DIR]                  kunci master (super_read_only, juga
                                                         setelah restart) tanpa kandidat promosi
  dbpanel agent unlock-master [--dir DIR]                buka kunci master dan hapus kunci promosi
  dbpanel agent forget-replica <host> [--dir DIR]        hapus replika dari daftar replika dan dari kunci
                                                         promosi yang sedang berdiri (host:port juga
                                                         diterima; portnya diabaikan)
  dbpanel agent forget-replica-port {<port>|semua} [--dir DIR]
                                                         hapus port dari daftar port replika; semua juga
                                                         menghapus tanda daftar port penuh; port replika
                                                         yang tidak tersambung tidak masuk daftar lagi
                                                         dengan sendirinya
  dbpanel agent approve <kode> [--dir DIR]               setujui bangun ulang server yang dikarantina;
                                                         kode dari form "Bangun ulang sebagai replika"
  dbpanel agent approve batal [--dir DIR]                hapus persetujuan dan permintaan bangun ulang
  dbpanel agent version                                  tampilkan versi
`

// tokenEnv carries the pairing token. Unlike argv (/proc/<pid>/cmdline),
// a process's environment is readable only by its owner and root.
const tokenEnv = "DBPANEL_TOKEN"

// webPinEnv carries the SPKI pin of the panel's HTTPS certificate from the
// install script to "agent pair".
const webPinEnv = "DBPANEL_WEB_PIN"

// Files in the agent directory for MySQL credentials: the key that seals
// them and the sealed admin account.
const (
	secretsKeyFile = "secrets.key"
	credFile       = "mysql.cred"
)

// cliSeenPath and cliSeenPortsPath are the replicas-seen and replica-ports
// files forget-replica and forget-replica-port edit; tests replace them.
var (
	cliSeenPath      = mysql.SeenFile
	cliSeenPortsPath = mysql.SeenPortsFile
)

// getenv and stdin are replaced by tests.
var (
	getenv           = os.Getenv
	stdin  io.Reader = os.Stdin
)

// cliHostname and cliNow are what "agent approve" shows and dates the
// approval by, and cliLookupIP resolves the source's name (nil: the
// system resolver); tests replace them.
var (
	cliHostname = os.Hostname
	cliNow      = time.Now
	cliLookupIP func(context.Context, string) ([]net.IP, error)
)

// Main runs the "dbpanel agent" command; args exclude "agent" itself. It
// returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	return RunCLI(args, stdin, stdout, stderr)
}

// RunCLI is Main with the input the prompts read (approve, set-role); Main
// passes the package's stdin. "pair --token-stdin" still reads the
// package's stdin.
func RunCLI(args []string, in io.Reader, stdout, stderr io.Writer) int {
	return RunCLIWith(args, in, stdout, stderr, CLISeams{})
}

// CLISeams are what a CLI run reaches on this machine beyond its --dir and
// its input and output: this server's MySQL layout (the start lock that
// set-role master releases) and the system log (approve). A nil field is
// the machine's own. Tests in other packages pass their own, so a test run
// never touches the machine's MySQL configuration or system log; Main and
// RunCLI always use the machine's.
type CLISeams struct {
	// LayoutHost is this server's MySQL as far as the start lock needs it.
	LayoutHost func(config.Config) (mysql.Host, error)
	// Syslog writes one line to the system log.
	Syslog func(line string) error
}

// RunCLIWith is RunCLI with seams in place of the machine's MySQL layout
// and system log (see CLISeams); e2e tests run the CLI through it.
func RunCLIWith(args []string, in io.Reader, stdout, stderr io.Writer, seams CLISeams) int {
	if seams.LayoutHost == nil {
		seams.LayoutHost = cliLayoutHost
	}
	if seams.Syslog == nil {
		seams.Syslog = cliSyslog
	}
	if len(args) > 0 {
		switch args[0] {
		case "pair":
			return mainPair(args[1:], stdout, stderr)
		case "set-role":
			return mainSetRole(args[1:], in, stdout, stderr, seams.LayoutHost)
		case "approve":
			return mainApprove(args[1:], in, stdout, stderr, seams.Syslog)
		case "set-source":
			return mainSetSource(args[1:], stdout, stderr)
		case "lock-master":
			return mainLockMaster(args[1:], stdout, stderr)
		case "unlock-master":
			return mainUnlockMaster(args[1:], stdout, stderr)
		case "forget-replica":
			return mainForgetReplica(args[1:], stdout, stderr)
		case "forget-replica-port":
			return mainForgetReplicaPort(args[1:], stdout, stderr)
		case "version":
			fmt.Fprintf(stdout, "dbpanel agent %s\n", version.Version)
			return 0
		case "help", "-h", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		}
	}
	return mainRun(args, stderr)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

func mainRun(args []string, stderr io.Writer) int {
	fs := newFlagSet("agent", stderr)
	dir := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	panelURL := fs.String("panel", localPanelURL, "alamat panel untuk berpasangan lewat "+LocalPairTokenFile)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	// The panel host: install.sh left the panel's token for its own server.
	if needsLocalPair(*dir) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		cfg, err := pairFromFile(ctx, *panelURL, filepath.Join(*dir, LocalPairTokenFile), *dir, "")
		stop()
		if err != nil {
			fmt.Fprintf(stderr, "pemasangan gagal: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "Agent terpasang sebagai server #%d (panel %s)\n", cfg.ServerID, cfg.PanelAddr)
	}
	cfg, err := config.Load(*dir)
	if errors.Is(err, config.ErrNotPaired) {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "gagal membaca konfigurasi: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	reg := commands.NewRegistry()
	sys := probe.RealSystem()
	probe.Register(reg, sys, version.Version)
	a := New(cfg, reg, logger)
	runner := jobs.NewRunner(a, logger)
	runner.Register(reg)
	drv := RegisterMySQL(reg, cfg, *dir, sys, runner, logger)
	drv.Events = a
	mon := monitor.New(drv, a, logger, monitor.Options{Pins: drv.Pins()})
	mon.Register(reg)
	reg.SetLogger(logger)
	if err := ApplyPolicy(reg, *dir, logger); err != nil {
		// A policy the operator wrote but the agent cannot trust: refuse to
		// run rather than run without it.
		fmt.Fprintf(stderr, "kebijakan lokal agent: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("agent starting", "version", version.Version, "panel", cfg.PanelAddr, "server_id", cfg.ServerID)
	monDone := make(chan struct{})
	go func() {
		defer close(monDone)
		mon.Run(ctx)
	}()
	err = a.Run(ctx)
	stop()
	<-monDone
	if err != nil {
		fmt.Fprintf(stderr, "agent berhenti: %v\n", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}

// RegisterMySQL adds the mysql.* commands (tests use it for in-process
// agents too). Without a MySQL installation the driver has no Host and its
// commands report that MySQL is missing until mysql.install installs it. The admin account the user connects with is
// kept sealed in dir/mysql.cred, whether or not MySQL is installed yet. It
// returns the driver, which the health monitor reads its snapshots from. The
// role and source pins (mysql.Pins) live in dir too.
func RegisterMySQL(reg *commands.Registry, cfg config.Config, dir string, sys probe.System, runner *jobs.Runner, logger *slog.Logger) *mysql.Driver {
	d := &mysql.Driver{Jobs: runner, Logger: logger, Sys: sys, Cfg: cfg, PinDir: dir}
	// agent.key is the TLS private key, so the sealing key is a separate file.
	box, err := secrets.LoadOrCreate(filepath.Join(dir, secretsKeyFile))
	if err != nil {
		logger.Error("cannot open the credential sealing key; MySQL cannot be connected", "err", err)
	} else {
		d.Creds = mysql.NewFileCredentialStore(filepath.Join(dir, credFile), box)
	}
	host, err := mysql.NewRealHost(cfg, d.Creds, sys, logger)
	if err != nil {
		logger.Warn("mysql not available", "err", err)
	} else {
		l := host.Layout()
		logger.Info("mysql layout", "mysqld", l.MysqldPath, "config", l.ConfigFile, "include_dir", l.IncludeDir,
			"datadir", l.DataDir, "socket", l.Socket, "service", l.ServiceKind, "service_name", l.ServiceName)
		d.Host = host
	}
	d.Register(reg)
	return d
}

// pinsUpdated is what set-role and set-source print once the file is
// written; the agent stats the pins on every guarded command.
const pinsUpdated = "diperbarui; agent membacanya ulang pada perintah berikutnya"

// parsePinArgs parses "[--dir DIR] VALUE [--dir DIR]": exactly one value,
// with the flag before or after it.
func parsePinArgs(name string, args []string, stderr io.Writer) (dir, value string, ok bool) {
	fs := newFlagSet(name, stderr)
	d := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	if err := fs.Parse(args); err != nil {
		return "", "", false
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return "", "", false
	}
	value = fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", "", false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return "", "", false
	}
	return *d, value, true
}

// loadPinsForCLI opens the pins in dir. Files the agent would not trust
// are reported and then overwritten: the CLI is how they get repaired.
func loadPinsForCLI(dir string, stderr io.Writer) *mysql.Pins {
	p, err := mysql.LoadPins(dir)
	if err != nil {
		fmt.Fprintf(stderr, "Peringatan: pin lama tidak bisa dipakai (%v); ditimpa\n", err)
	}
	return p
}

// Texts of set-role on a quarantined server (what the operator gives up).
const (
	setRoleQuarantinedWithSource = "Server ini dikarantina (quarantined) mengikuti sumber %s.\n"
	setRoleQuarantinedNoSource   = "Server ini dikarantina (quarantined) tanpa sumber yang dipin.\n"
	setRoleGiveUpMaster          = `Setelah perintah ini:
- kunci start (start lock) dilepas sekarang, jadi MySQL tidak lagi dipaksa baca-saja (read-only) saat menyala;
- server ini dipin sebagai master dan bisa dibuka untuk ditulis. Bila master baru masih dipakai aplikasi, Anda akan punya dua master yang bisa ditulis.
`
	setRoleGiveUpUnassigned = `Setelah perintah ini:
- agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
- server ini tanpa peran: panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.
`
	setRoleGiveUpReplica = `Setelah perintah ini:
- agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
- server ini dipin sebagai replika: panel boleh membuang datanya dan menyalin ulang dari sumber yang dipin tanpa persetujuan di server ini.
`
	setRoleUnreadable     = "Pin server ini tidak bisa dibaca (%v); server ini mungkin dikarantina (quarantined).\n"
	setRoleChanged        = "Peran server ini berubah selama Anda membaca; jalankan perintah ini lagi."
	setRoleLockGone       = "Kunci start sudah dilepas, tetapi peran belum diubah. Jalankan perintah ini lagi; sampai itu MySQL tidak dipaksa baca-saja (read-only) saat menyala."
	setRoleAsk            = "Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan: "
	setRoleCancelled      = "Dibatalkan; peran tidak diubah."
	setRoleReleased       = "Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari konfigurasi MySQL."
	setRoleNoLock         = "Kunci start tidak ditemukan; tidak ada yang dilepas."
	setRoleNoMysqld       = "MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu dilepas."
	setRoleNoCheck        = "kunci start tidak bisa diperiksa (%v); peran tidak diubah. Bila agent tidak bisa dijalankan, lepas dengan tangan: lihat README, bagian \"Melepas kunci start dengan tangan\"."
	setRoleNextMaster     = "Langkah berikutnya: bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo dbpanel agent unlock-master"
	setRoleNextUnassigned = "Langkah berikutnya: bila server ini sudah dihapus dari panel, tambahkan lagi lewat Tambah server, lalu jalankan Siapkan master atau Buat replika. Perhatian: server ini tidak lagi dikarantina; agent tidak lagi menjaga MySQL-nya tetap baca-saja (read-only)."
)

// parseSetRoleArgs parses "[--dir DIR] [--ya] ROLE [--dir DIR] [--ya]":
// exactly one role, the flags before or after it.
func parseSetRoleArgs(args []string, stderr io.Writer) (dir, role string, yes, ok bool) {
	fs := newFlagSet("agent set-role", stderr)
	d := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	y := fs.Bool("ya", false, "tanpa pertanyaan pada server yang dikarantina")
	if err := fs.Parse(args); err != nil {
		return "", "", false, false
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return "", "", false, false
	}
	role = fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", "", false, false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return "", "", false, false
	}
	return *d, role, *y, true
}

// mainSetRole is "agent set-role"; layout is this server's MySQL layout
// (cliReleaseStartLock).
func mainSetRole(args []string, in io.Reader, stdout, stderr io.Writer, layout func(config.Config) (mysql.Host, error)) int {
	dir, role, yes, ok := parseSetRoleArgs(args, stderr)
	if !ok {
		return 2
	}
	if role != "master" && role != "replica" && role != "quarantined" && role != "unassigned" {
		fmt.Fprintf(stderr, "peran harus master, replica, quarantined atau unassigned: %q\n", role)
		return 2
	}
	p := loadPinsForCLI(dir, stderr)
	was, readErr := p.RoleErr()
	// Pins that cannot be read may hide a quarantine: the server counts
	// as possibly quarantined.
	if (was == "quarantined" || readErr != nil) && role != "quarantined" && !yes {
		if readErr != nil {
			fmt.Fprintf(stdout, setRoleUnreadable, readErr)
		} else if host, port, has := p.Source(); has {
			fmt.Fprintf(stdout, setRoleQuarantinedWithSource, net.JoinHostPort(host, strconv.Itoa(port)))
		} else {
			fmt.Fprint(stdout, setRoleQuarantinedNoSource)
		}
		switch role {
		case "master":
			fmt.Fprint(stdout, setRoleGiveUpMaster)
		case "unassigned":
			fmt.Fprint(stdout, setRoleGiveUpUnassigned)
		case "replica":
			fmt.Fprint(stdout, setRoleGiveUpReplica)
		}
		fmt.Fprint(stdout, "\n"+setRoleAsk)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != "ya" {
			fmt.Fprintln(stdout, setRoleCancelled)
			return 1
		}
		// The role may have changed while root read the screen.
		if now, err := p.RoleErr(); now != was || (err != nil) != (readErr != nil) {
			fmt.Fprintln(stderr, setRoleChanged)
			return 1
		}
	}
	// A master never keeps a quarantine's start lock: it goes first, and
	// the role stays as it was when it cannot.
	var released bool
	if role == "master" {
		ok, found := cliReleaseStartLock(dir, stdout, stderr, layout)
		if !ok {
			return 1
		}
		released = found
	}
	if err := p.SetRole(role); err != nil {
		fmt.Fprintf(stderr, "gagal mengubah peran: %v\n", err)
		if released {
			fmt.Fprintln(stderr, setRoleLockGone)
		}
		return 1
	}
	fmt.Fprintln(stdout, "peran "+pinsUpdated)
	switch {
	case role == "master":
		fmt.Fprintln(stdout, setRoleNextMaster)
	// Unreadable pins may have hidden a quarantine (the operator was told
	// so): said as for one.
	case role == "unassigned" && (was == "quarantined" || readErr != nil):
		fmt.Fprintln(stdout, setRoleNextUnassigned)
	}
	return 0
}

// mainApprove is "agent approve <kode>|batal": root approves the rebuild
// the panel asked for, after seeing which source and address it gets the
// data from, or removes the approval and the request; each is recorded with
// writeSyslog (approvalSyslog).
func mainApprove(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int {
	dir, arg, ok := parsePinArgs("agent approve", args, stderr)
	if !ok {
		return 2
	}
	if arg == "batal" {
		// Clearing does not depend on the pins being readable.
		p, _ := mysql.LoadPins(dir)
		a, _, _ := p.ReadRebuildApproval()
		r, _, _ := p.ReadRebuildRequest()
		if err := p.ClearRebuild(); err != nil {
			fmt.Fprintf(stderr, "Gagal menghapus: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Persetujuan dan permintaan bangun ulang di server ini dihapus.")
		approvalSyslog("rebuild approval cancelled", a, r.Created, stderr, writeSyslog)
		return 0
	}
	p, err := mysql.LoadPins(dir)
	if err != nil {
		fmt.Fprintf(stderr, "Pin server ini tidak bisa dibaca (%v); persetujuan tidak ditulis.\n", err)
		return 1
	}
	if role := p.Role(); role != "quarantined" {
		fmt.Fprintf(stderr, "Server ini tidak dikarantina (peran: %s); persetujuan bangun ulang tidak diperlukan.\n", role)
		return 1
	}
	host, port, has := p.Source()
	if !has {
		fmt.Fprintln(stderr, "Server ini dikarantina tanpa sumber yang dipin; pin master barunya dulu: sudo dbpanel agent set-source <host:port>")
		return 1
	}
	source := net.JoinHostPort(host, strconv.Itoa(port))
	now := cliNow()
	req, valid, err := p.ValidRebuildRequest(now)
	if err != nil {
		fmt.Fprintf(stderr, "Pin server ini tidak bisa dibaca (%v); persetujuan tidak ditulis.\n", err)
		return 1
	}
	if !valid || !sameSourceText(req.Source, source) {
		fmt.Fprintln(stderr, `Tidak ada permintaan persetujuan yang berlaku di server ini. Buka form "Bangun ulang sebagai replika" di panel, klik Periksa lagi, lalu jalankan perintah yang ditampilkan.`)
		return 1
	}
	if subtle.ConstantTimeCompare([]byte(arg), []byte(req.Code)) != 1 {
		fmt.Fprintln(stderr, `Kode salah. Salin perintah dari form "Bangun ulang sebagai replika" di panel.`)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr, err := p.RebuildApprovalAddr(ctx, cliLookupIP)
	var (
		lookup *mysql.SourceLookupError
		multi  *mysql.SourceAddrsError
		moved  *mysql.SourceMovedError
	)
	switch {
	case errors.As(err, &lookup):
		fmt.Fprintf(stderr, "Alamat untuk nama sumber %s tidak ditemukan (DNS: %v); persetujuan tidak ditulis.\n", lookup.Source, lookup.Err)
		return 1
	case errors.As(err, &multi), errors.As(err, &moved):
		fmt.Fprintln(stderr, err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "Pin server ini tidak bisa dibaca (%v); persetujuan tidak ditulis.\n", err)
		return 1
	}
	name, err := cliHostname()
	if err != nil {
		name = "(tidak diketahui)"
	}
	fmt.Fprintf(stdout, "Persetujuan bangun ulang (rebuild) server ini\n\nServer ini  : %s\nPeran       : dikarantina (quarantined)\nSumber data : %s\nAlamat IP   : %s\n", name, source, addr)
	fmt.Fprint(stdout, `
Bila disetujui, SEMUA data MySQL di server ini dibuang dan diganti salinan dari sumber di atas. Data yang dibuang tidak bisa dikembalikan.
Selama persetujuan berlaku (24 jam), panel juga boleh mengubah pengaturan replikasi, akun replikasi, pengaturan dan sertifikat MySQL di server ini, dan memulai ulang MySQL.
Pastikan nama dan alamat IP di atas memang master baru Anda, yaitu server yang sekarang dipakai aplikasi.
Bila promote tadi promote darurat, jalankan dulu "Periksa data tertinggal" (dan "Amankan data tertinggal" bila ada) di halaman server ini di panel.
Persetujuan berlaku 24 jam, hanya untuk sumber di atas.

Ketik ya untuk menyetujui, atau apa saja untuk membatalkan: `)
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != "ya" {
		fmt.Fprintln(stdout, "Dibatalkan; tidak ada yang disetujui.")
		return 1
	}
	// The request may have changed while root read the screen.
	again, valid, err := p.ValidRebuildRequest(cliNow())
	if err != nil || !valid || again.Code != req.Code || !sameSourceText(again.Source, source) {
		fmt.Fprintln(stderr, "Permintaan berubah selama Anda membaca; jalankan perintah ini lagi.")
		return 1
	}
	// From here on the approval is rolled back when a step fails: the
	// operator is told it failed, so it must not stay valid on disk.
	rollback := func() {
		if err := p.RemoveRebuildApproval(); err != nil {
			fmt.Fprintf(stderr, "Berkas persetujuan tidak bisa dihapus (%v), jadi persetujuan mungkin tetap berlaku. Batalkan dengan: sudo dbpanel agent approve batal\n", err)
		}
	}
	approval := mysql.NewRebuildApproval(source, addr, now)
	if err := p.WriteRebuildApproval(approval); err != nil {
		fmt.Fprintf(stderr, "Gagal menulis persetujuan: %v\n", err)
		rollback()
		return 1
	}
	if err := p.RemoveRebuildRequest(); err != nil {
		fmt.Fprintf(stderr, "Gagal menghapus permintaan: %v; persetujuan dibatalkan. Jalankan perintah ini lagi.\n", err)
		rollback()
		return 1
	}
	fmt.Fprintf(stdout, "Disetujui sampai %s. Kembali ke panel dan klik Periksa lagi. Untuk membatalkan: sudo dbpanel agent approve batal\n",
		approval.Expires.Local().Format("2006-01-02 15:04 MST"))
	approvalSyslog("rebuild approval written", approval, req.Created, stderr, writeSyslog)
	return 0
}

// cliSyslog writes line to the system log (facility auth, tag
// dbpanel-agent); tests in this package replace it, others pass CLISeams.
var cliSyslog = func(line string) error {
	w, err := syslog.New(syslog.LOG_AUTH|syslog.LOG_NOTICE, "dbpanel-agent")
	if err != nil {
		return err
	}
	defer w.Close()
	return w.Notice(line)
}

// approvalSyslog records an approval written or cancelled (action) in the
// system log through writeSyslog: its source, address and expiry, when the
// request it answered was made, and who ran the command. The code is never
// logged. A log that cannot be written is a warning on stderr; the outcome
// stands.
func approvalSyslog(action string, a mysql.RebuildApproval, requested time.Time, stderr io.Writer, writeSyslog func(string) error) {
	or := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	at := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format(time.RFC3339)
	}
	who := "uid=" + strconv.Itoa(os.Getuid())
	if u := getenv("SUDO_USER"); u != "" {
		who = fmt.Sprintf("user=%s sudo_uid=%s %s", strconv.Quote(u), or(getenv("SUDO_UID")), who)
	}
	line := fmt.Sprintf("%s: source=%s addr=%s expires=%s request_created=%s %s", action,
		or(a.Source), or(a.Addr), at(a.Expires), at(requested), who)
	if err := writeSyslog(line); err != nil {
		fmt.Fprintf(stderr, "Peringatan: catatan ke log sistem gagal ditulis (%v).\n", err)
	}
}

// sameSourceText reports whether a and b are the same host:port (host
// without case).
func sameSourceText(a, b string) bool {
	ha, pa, err := mysql.ParseSource(a)
	if err != nil {
		return false
	}
	hb, pb, err := mysql.ParseSource(b)
	return err == nil && strings.EqualFold(ha, hb) && pa == pb
}

func mainSetSource(args []string, stdout, stderr io.Writer) int {
	dir, src, ok := parsePinArgs("agent set-source", args, stderr)
	if !ok {
		return 2
	}
	host, port, err := mysql.ParseSource(src)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := loadPinsForCLI(dir, stderr).SetSource(host, port); err != nil {
		fmt.Fprintf(stderr, "gagal mengubah sumber: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "sumber "+pinsUpdated)
	return 0
}

// cliLayoutHost is the MySQL of this server as far as the start lock needs
// it: the discovered layout, no admin account. Tests in this package
// replace it; others pass CLISeams.
var cliLayoutHost = func(cfg config.Config) (mysql.Host, error) {
	return mysql.NewRealHost(cfg, nil, probe.RealSystem(), slog.New(slog.DiscardHandler))
}

// cliReleaseStartLock releases the start lock of the MySQL layout gives.
// It needs neither the agent's secrets nor an admin account: only the
// layout (the agent configuration is read best effort, for a socket
// override). It prints the outcome and returns ok (the role may
// go on: the lock is released, or there is none to release) and found
// (a lock existed and was released).
func cliReleaseStartLock(dir string, stdout, stderr io.Writer, layout func(config.Config) (mysql.Host, error)) (ok, found bool) {
	cfg, err := config.Load(dir)
	if err != nil {
		cfg = config.Config{}
	}
	h, err := layout(cfg)
	if errors.Is(err, mysql.ErrNoMysqld) {
		fmt.Fprintln(stdout, setRoleNoMysqld)
		return true, false
	}
	if err != nil {
		fmt.Fprintf(stderr, setRoleNoCheck+"\n", err)
		return false, false
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	found, err = mysql.ReleaseStartLock(ctx, h)
	if fe := (*mysql.StartLockFileError)(nil); errors.As(err, &fe) {
		fmt.Fprintf(stderr, "Baris init_file sudah dihapus dari konfigurasi MySQL, tetapi berkas kunci start tidak bisa dihapus (%v); "+
			"peran tidak diubah. Jalankan perintah ini lagi.\n", fe.Err)
		return false, found
	}
	if err != nil {
		fmt.Fprintf(stderr, "kunci start tidak bisa dilepas (%v); peran tidak diubah\n", err)
		return false, found
	}
	if found {
		fmt.Fprintln(stdout, setRoleReleased)
	} else {
		fmt.Fprintln(stdout, setRoleNoLock)
	}
	return true, found
}

// cliMySQLHost is the MySQL of the agent in dir, logged in to with the
// admin account the agent stores there (as RegisterMySQL wires it). Tests
// replace it.
var cliMySQLHost = func(dir string) (mysql.Host, error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	box, err := secrets.LoadOrCreate(filepath.Join(dir, secretsKeyFile))
	if err != nil {
		return nil, fmt.Errorf("kunci kredensial: %w", err)
	}
	creds := mysql.NewFileCredentialStore(filepath.Join(dir, credFile), box)
	return mysql.NewRealHost(cfg, creds, probe.RealSystem(), slog.New(slog.DiscardHandler))
}

// parseDirOnly parses "[--dir DIR]" with no other argument.
func parseDirOnly(name string, args []string, stderr io.Writer) (dir string, ok bool) {
	fs := newFlagSet(name, stderr)
	d := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return "", false
	}
	return *d, true
}

// cliExec runs stmts, in order, on the agent's MySQL in dir.
func cliExec(dir string, stmts ...string) error {
	h, err := cliMySQLHost(dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	db, err := h.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

// cliLockFor is how long a lock-master lock lasts on paper; a lock with
// no candidate is never lifted automatically, whatever its deadline.
const cliLockFor = 24 * time.Hour

// mainLockMaster is "agent lock-master": root makes MySQL read-only, also
// across restarts, and writes the promote lock with no candidate
// (mysql.CLILockCandidate), so the panel cannot make it writable again.
func mainLockMaster(args []string, stdout, stderr io.Writer) int {
	dir, ok := parseDirOnly("agent lock-master", args, stderr)
	if !ok {
		return 2
	}
	p, _ := mysql.LoadPins(dir) // the lock does not depend on the role or source pins
	if l, locked, err := p.ReadLock(); err != nil {
		fmt.Fprintf(stderr, "kunci promosi tidak bisa dibaca: %v\n", err)
		return 1
	} else if locked {
		fmt.Fprintf(stderr, "master sudah dikunci (kandidat %s, sampai %s); buka dulu dengan: sudo dbpanel agent unlock-master\n",
			l.Candidate, l.Deadline.Local().Format(time.RFC3339))
		return 1
	}
	if err := cliExec(dir, "SET PERSIST super_read_only = ON"); err != nil {
		fmt.Fprintf(stderr, "gagal mengunci MySQL: %v\n", err)
		return 1
	}
	// The check above and this write are not one step: write only when
	// no lock appeared meanwhile (a planned promotion's), under the lock
	// file's flock (FR-14).
	l, written, err := p.WriteLockIfNone(mysql.PromoteLock{Candidate: mysql.CLILockCandidate, Deadline: time.Now().Add(cliLockFor)})
	if err != nil {
		fmt.Fprintf(stderr, "MySQL sudah read-only, tetapi kunci promosi gagal ditulis: %v\n", err)
		return 1
	}
	if !written {
		fmt.Fprintf(stderr, "MySQL sudah read-only, tetapi sementara itu master dikunci untuk promosi (kandidat %s, sampai %s); "+
			"kunci itu dibiarkan dan kunci lock-master tidak ditulis\n", l.Candidate, l.Deadline.Local().Format(time.RFC3339))
		return 1
	}
	fmt.Fprintln(stdout, "master dikunci (super_read_only, juga setelah restart); buka dengan: sudo dbpanel agent unlock-master")
	return 0
}

// mainUnlockMaster is "agent unlock-master": MySQL writable again (also
// after a restart) and the promote lock removed. Running it again on a
// master is harmless. It runs only on a server pinned master, or an
// unassigned one holding a lock-master lock (candidate "cli"): a replica
// or quarantined server is never made writable by it.
func mainUnlockMaster(args []string, stdout, stderr io.Writer) int {
	dir, ok := parseDirOnly("agent unlock-master", args, stderr)
	if !ok {
		return 2
	}
	p, perr := mysql.LoadPins(dir)
	role := p.Role()
	l, locked, lerr := p.ReadLock()
	cliLocked := role == "unassigned" && lerr == nil && locked && l.Candidate == mysql.CLILockCandidate
	if role != "master" && !cliLocked {
		switch {
		case perr != nil:
			fmt.Fprintf(stderr, "pin peran tidak bisa dibaca (%v); unlock-master hanya untuk master\n", perr)
		case lerr != nil:
			fmt.Fprintf(stderr, "server ini dipin sebagai %s dan kunci promosi tidak bisa dibaca (%v); unlock-master hanya untuk master\n", role, lerr)
		default:
			fmt.Fprintf(stderr, "server ini dipin sebagai %s; unlock-master hanya untuk master atau server yang dikunci dengan lock-master\n", role)
		}
		return 1
	}
	// Say what this bypasses (SA-11): the guard's reads of the candidate
	// and of the replicas the lock lists.
	if lerr == nil && locked && l.Candidate != mysql.CLILockCandidate {
		fmt.Fprintf(stderr, "Perhatian: kunci promosi untuk kandidat %s (tenggat %s) dibuka tanpa memeriksa kandidat itu", l.Candidate,
			l.Deadline.Local().Format(time.RFC3339))
		if len(l.Readable) > 0 {
			fmt.Fprintf(stderr, " atau replika %s", strings.Join(l.Readable, ", "))
		}
		fmt.Fprintln(stderr, "; bila salah satunya sudah menjadi master, kini ada dua master yang bisa ditulis.")
	}
	if err := cliExec(dir, "RESET PERSIST IF EXISTS super_read_only",
		"SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF"); err != nil {
		fmt.Fprintf(stderr, "gagal membuka kunci MySQL: %v\n", err)
		return 1
	}
	if err := p.ClearLock(); err != nil {
		fmt.Fprintf(stderr, "MySQL sudah bisa ditulis, tetapi kunci promosi gagal dihapus: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "master dibuka: MySQL bisa ditulis dan kunci promosi dihapus")
	return 0
}

// mainForgetReplica is "agent forget-replica HOST": host leaves the
// replicas-seen file and the readable replicas of the standing promote
// lock, whose guard then no longer waits for it. It is local only: the
// panel has no command for it.
func mainForgetReplica(args []string, stdout, stderr io.Writer) int {
	dir, host, ok := parsePinArgs("agent forget-replica", args, stderr)
	if !ok {
		return 2
	}
	// host:port as an alert names it: the host is what is recorded.
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	p, _ := mysql.LoadPins(dir) // the lock does not depend on the role or source pins
	n, err := p.ForgetLockHost(host)
	if err != nil {
		fmt.Fprintf(stderr, "kunci promosi tidak bisa diubah: %v\n", err)
		return 1
	}
	seen, err := mysql.ForgetReplica(cliSeenPath, host)
	if err != nil {
		fmt.Fprintf(stderr, "daftar replika tidak bisa diubah (%d entri kunci promosi sudah dihapus): %v\n", n, err)
		return 1
	}
	if !seen && n == 0 {
		fmt.Fprintf(stderr, "%s tidak ada di daftar replika\n", host)
		return 1
	}
	fmt.Fprintf(stdout, "replika %s dihapus dari daftar replika (%d entri kunci promosi ikut dihapus)\n", host, n)
	// What it bypasses (SA-11).
	fmt.Fprintf(stdout, "Penjaga kunci promosi tidak lagi menunggu %s terbaca: bila replika itu sudah menjadi master, "+
		"master ini bisa terbuka dalam beberapa menit dan ada dua master yang bisa ditulis.\n", host)
	return 0
}

// mainForgetReplicaPort is "agent forget-replica-port {PORT|semua}": the
// port leaves the replica-ports file; "semua" empties it and removes its
// full mark (SA-4), which lets planned promotions lock again. The ports of
// the replicas connected now are recorded again by the agent within
// seconds. It is local only: the panel has no command for it.
func mainForgetReplicaPort(args []string, stdout, stderr io.Writer) int {
	_, arg, ok := parsePinArgs("agent forget-replica-port", args, stderr)
	if !ok {
		return 2
	}
	port := 0
	if arg != "semua" {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(stderr, "%q bukan port (1-65535) atau semua\n", arg)
			return 2
		}
		port = n
	}
	if port == 0 {
		return forgetAllReplicaPorts(stdout, stderr)
	}
	changed, err := mysql.ForgetReplicaPort(cliSeenPortsPath, port)
	if err != nil {
		fmt.Fprintf(stderr, "daftar port replika tidak bisa diubah: %v\n", err)
		return 1
	}
	if !changed {
		fmt.Fprintf(stderr, "port %d tidak ada di daftar port replika\n", port)
		return 1
	}
	fmt.Fprintf(stdout, "port %d dihapus dari daftar port replika; bila replika di port itu sekarang tidak tersambung "+
		"ke master ini, port itu tidak masuk daftar lagi dengan sendirinya dan replika itu tidak dibaca sebelum master "+
		"dibuka pada promosi terencana berikutnya\n", port)
	return 0
}

// forgetAllReplicaPorts is "forget-replica-port semua": it names the file,
// the ports it removed and what is lost with them (R-119): a replica on one
// of those ports that is not connected now is not recorded again, so it is
// not read before the master is opened at the next planned promotion.
func forgetAllReplicaPorts(stdout, stderr io.Writer) int {
	f, err := mysql.ForgetAllReplicaPorts(cliSeenPortsPath)
	if err != nil {
		fmt.Fprintf(stderr, "daftar port replika tidak bisa diubah: %v\n", err)
		return 1
	}
	if !f.Changed() {
		fmt.Fprintf(stderr, "daftar port replika sudah kosong (%s); tidak ada yang dihapus\n", cliSeenPortsPath)
		return 1
	}
	fmt.Fprintf(stdout, "daftar port replika: %s\n", cliSeenPortsPath)
	if len(f.Ports) == 0 {
		// Only the full mark: no port was lost.
		fmt.Fprintln(stdout, "tidak ada port di daftar; hanya tanda daftar port penuh yang dihapus")
		return 0
	}
	list := make([]string, len(f.Ports))
	for i, p := range f.Ports {
		list[i] = strconv.Itoa(p)
	}
	fmt.Fprintf(stdout, "port yang dihapus: %s\n", strings.Join(list, ", "))
	if f.Full {
		fmt.Fprintln(stdout, "tanda daftar port penuh ikut dihapus")
	}
	fmt.Fprintln(stdout, "semua port dihapus dari daftar port replika; port replika yang tersambung dimasukkan lagi oleh agent dalam beberapa detik")
	fmt.Fprintln(stdout, "Perhatian: port replika yang sekarang tidak tersambung ke master ini tidak masuk daftar lagi dengan sendirinya. "+
		"Replika di port seperti itu tidak dibaca sebelum master dibuka pada promosi terencana berikutnya. "+
		"Bila ada replika yang memakai salah satu port di atas dan sekarang tidak tersambung, sambungkan lagi "+
		"(atau pastikan replika itu sudah dipensiunkan) sebelum promosi berikutnya.")
	return 0
}

func mainPair(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agent pair", stderr)
	panelURL := fs.String("panel", "", "alamat panel, mis. https://192.168.1.10:8080")
	tokenFlag := fs.String("token", "", "token pemasangan (usang: terlihat di daftar proses; pakai "+tokenEnv+")")
	tokenStdin := fs.Bool("token-stdin", false, "baca token pemasangan dari baris pertama stdin")
	tokenFile := fs.String("token-file", "", "baca token dan pin sertifikat panel dari berkas ini (baris 1 dan 2); dihapus setelah berhasil")
	caPin := fs.String("ca-sha256", "", "sha256 sertifikat CA panel (64 digit heksadesimal, dari perintah pairing di panel)")
	skipPin := fs.Bool("insecure-skip-ca-pin", false, "terima CA apa pun dari jawaban pairing (tidak aman)")
	agentAddr := fs.String("agent-addr", "", "HOST:PORT listener agent di panel (menimpa jawaban pairing)")
	webPin := fs.String("web-pin", "", "pin kunci sertifikat HTTPS panel (sha256//..., dari perintah pasang; atau "+webPinEnv+")")
	dir := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	webPinFlag := *webPin
	if *webPin == "" {
		*webPin = strings.TrimSpace(getenv(webPinEnv))
	}
	if *webPin != "" && !validSPKIPin(*webPin) {
		fmt.Fprintln(stderr, "--web-pin harus berbentuk sha256//<base64 dari 32 byte>")
		return 2
	}
	if *panelURL == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "--panel wajib diisi")
		fs.Usage()
		return 2
	}
	if *tokenFile != "" {
		return mainPairTokenFile(*panelURL, *tokenFile, *tokenFlag != "" || *tokenStdin || webPinFlag != "", *agentAddr, *dir, stdout, stderr)
	}
	// The pairing answer may come over plain HTTP or an unverified TLS link; without a pin an on-path
	// attacker could hand the agent its own CA and impersonate the panel.
	switch {
	case *caPin != "" && !validSHA256Hex(*caPin):
		fmt.Fprintln(stderr, "--ca-sha256 harus 64 digit heksadesimal")
		return 2
	case *caPin == "" && !*skipPin:
		fmt.Fprintln(stderr, "--ca-sha256 wajib (lihat perintah pairing di panel)")
		fs.Usage()
		return 2
	case *caPin == "":
		fmt.Fprintln(stderr, "Peringatan: verifikasi CA panel dimatikan")
	}
	if *agentAddr != "" {
		if host, port, err := net.SplitHostPort(*agentAddr); err != nil || host == "" || port == "" {
			fmt.Fprintln(stderr, "--agent-addr harus berbentuk HOST:PORT")
			return 2
		}
	}
	token, err := pairToken(*tokenFlag, *tokenStdin, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fs.Usage()
		return 2
	}
	// The panel keeps its CA in the same default directory; pairing here
	// would mix the agent's files into the panel's config.
	if _, err := os.Stat(filepath.Join(*dir, "ca.key")); err == nil {
		fmt.Fprintln(stderr, panelDirMessage)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := Pair(ctx, *panelURL, token, *dir, nil, PairOptions{CASHA256: *caPin, AgentAddr: *agentAddr, WebPin: *webPin})
	if err != nil {
		fmt.Fprintf(stderr, "pemasangan gagal: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Agent terpasang sebagai server #%d (panel %s). Jalankan: dbpanel agent --dir %s\n",
		cfg.ServerID, cfg.PanelAddr, *dir)
	return 0
}

// panelDirMessage refuses pairing into a directory holding the panel's CA.
var panelDirMessage = "Direktori ini milik panel (ada ca.key). Pasang agent di direktorinya sendiri (" +
	config.DefaultDir + "); di host panel, install.sh sudah memasangkannya."

// mainPairTokenFile is "agent pair --token-file": the token and the panel's
// web pin come from the file (the panel host's local-pair-token), which is
// deleted once the agent is paired. The pin authenticates the panel, so no
// --ca-sha256 is needed.
func mainPairTokenFile(panelURL, path string, otherSource bool, agentAddr, dir string, stdout, stderr io.Writer) int {
	if otherSource {
		fmt.Fprintln(stderr, "--token-file tidak bisa digabung dengan --token, --token-stdin atau --web-pin")
		return 2
	}
	if _, _, err := readTokenFile(path); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err == nil {
		fmt.Fprintln(stderr, panelDirMessage)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := pairFromFile(ctx, panelURL, path, dir, agentAddr)
	if err != nil {
		fmt.Fprintf(stderr, "pemasangan gagal: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Agent terpasang sebagai server #%d (panel %s). Jalankan: dbpanel agent --dir %s\n",
		cfg.ServerID, cfg.PanelAddr, dir)
	return 0
}

// validSPKIPin reports whether s is an SPKI pin in curl --pinnedpubkey form:
// "sha256//" and the standard base64 of a sha256.
func validSPKIPin(s string) bool {
	b64, ok := strings.CutPrefix(s, "sha256//")
	if !ok {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	return err == nil && len(b) == 32
}

// validSHA256Hex reports whether s is a sha256 in hex (either case).
func validSHA256Hex(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

// pairToken picks the pairing token: from stdin with --token-stdin, else
// from --token (with a warning: argv is visible to every local user), else
// from DBPANEL_TOKEN.
func pairToken(flagValue string, fromStdin bool, stderr io.Writer) (string, error) {
	switch {
	case fromStdin && flagValue != "":
		return "", errors.New("pilih salah satu: --token-stdin atau --token")
	case fromStdin:
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("baca token dari stdin: %w", err)
		}
		if t := strings.TrimSpace(line); t != "" {
			return t, nil
		}
		return "", errors.New("token kosong di stdin")
	case flagValue != "":
		fmt.Fprintln(stderr, "Peringatan: --token terlihat di daftar proses; gunakan "+tokenEnv)
		return flagValue, nil
	}
	if t := strings.TrimSpace(getenv(tokenEnv)); t != "" {
		return t, nil
	}
	return "", errors.New("token pemasangan wajib diisi lewat " + tokenEnv + " atau --token-stdin")
}
