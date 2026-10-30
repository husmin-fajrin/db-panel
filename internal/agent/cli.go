package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const usage = `Penggunaan:
  dbpanel agent [--dir DIR]                              jalankan agent
  dbpanel agent pair --panel URL --token TOKEN [--dir DIR]  pasangkan agent ke panel
  dbpanel agent version                                  tampilkan versi
`

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
	probe.Register(reg, probe.RealSystem(), version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("agent starting", "version", version.Version, "panel", cfg.PanelAddr, "server_id", cfg.ServerID)
	if err := New(cfg, reg, logger).Run(ctx); err != nil {
		fmt.Fprintf(stderr, "agent berhenti: %v\n", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}

func mainPair(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agent pair", stderr)
	panelURL := fs.String("panel", "", "alamat panel, mis. http://192.168.1.10:8080")
	token := fs.String("token", "", "token pemasangan dari panel")
	dir := fs.String("dir", config.DefaultDir, "direktori konfigurasi agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *panelURL == "" || *token == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "--panel dan --token wajib diisi")
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
	cfg, err := Pair(ctx, *panelURL, *token, *dir, nil)
	if err != nil {
		fmt.Fprintf(stderr, "pemasangan gagal: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Agent terpasang sebagai server #%d (panel %s). Jalankan: dbpanel agent --dir %s\n",
		cfg.ServerID, cfg.PanelAddr, *dir)
	return 0
}
