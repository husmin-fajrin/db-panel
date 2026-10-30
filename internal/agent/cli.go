package agent

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const usage = `Penggunaan:
  dbpanel agent [--dir DIR]                              jalankan agent
  DBPANEL_TOKEN=TOKEN dbpanel agent pair --panel URL --ca-sha256 HEX
      [--agent-addr HOST:PORT] [--dir DIR]               pasangkan agent ke panel
  dbpanel agent pair --panel URL --ca-sha256 HEX --token-stdin [--dir DIR]
                                                         idem, token dari stdin
  dbpanel agent version                                  tampilkan versi
`

// tokenEnv carries the pairing token. Unlike argv (/proc/<pid>/cmdline),
// a process's environment is readable only by its owner and root.
const tokenEnv = "DBPANEL_TOKEN"

// Files in the agent directory for MySQL credentials: the key that seals
// them and the sealed admin account.
const (
	secretsKeyFile = "secrets.key"
	credFile       = "mysql.cred"
)

// getenv and stdin are replaced by tests.
var (
	getenv           = os.Getenv
	stdin  io.Reader = os.Stdin
)

// Main runs the "dbpanel agent" command; args exclude "agent" itself. It
// returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "pair":
			return mainPair(args[1:], stdout, stderr)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
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
	registerMySQL(reg, cfg, *dir, sys, runner, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("agent starting", "version", version.Version, "panel", cfg.PanelAddr, "server_id", cfg.ServerID)
	if err := a.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "agent berhenti: %v\n", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}

// registerMySQL adds the mysql.* commands. Without a MySQL installation the
// driver has no Host and its commands report that MySQL is missing until
// mysql.install installs it. The admin account the user connects with is
// kept sealed in dir/mysql.cred, whether or not MySQL is installed yet.
func registerMySQL(reg *commands.Registry, cfg config.Config, dir string, sys probe.System, runner *jobs.Runner, logger *slog.Logger) {
	d := &mysql.Driver{Jobs: runner, Logger: logger, Sys: sys, Cfg: cfg}
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
}

func mainPair(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agent pair", stderr)
	panelURL := fs.String("panel", "", "alamat panel, mis. http://192.168.1.10:8080")
	tokenFlag := fs.String("token", "", "token pemasangan (usang: terlihat di daftar proses; pakai "+tokenEnv+")")
	tokenStdin := fs.Bool("token-stdin", false, "baca token pemasangan dari baris pertama stdin")
	caPin := fs.String("ca-sha256", "", "sha256 sertifikat CA panel (64 digit heksadesimal, dari perintah pairing di panel)")
	skipPin := fs.Bool("insecure-skip-ca-pin", false, "terima CA apa pun dari jawaban pairing (tidak aman)")
	agentAddr := fs.String("agent-addr", "", "HOST:PORT listener agent di panel (menimpa jawaban pairing)")
	dir := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *panelURL == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "--panel wajib diisi")
		fs.Usage()
		return 2
	}
	// The pairing answer travels over plain HTTP; without a pin an on-path
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
		fmt.Fprintln(stderr, "Direktori ini milik panel (ada ca.key). Jangan pasang agent di host panel; panel sudah punya agent tertanam.")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := Pair(ctx, *panelURL, token, *dir, nil, PairOptions{CASHA256: *caPin, AgentAddr: *agentAddr})
	if err != nil {
		fmt.Fprintf(stderr, "pemasangan gagal: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Agent terpasang sebagai server #%d (panel %s). Jalankan: dbpanel agent --dir %s\n",
		cfg.ServerID, cfg.PanelAddr, *dir)
	return 0
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
