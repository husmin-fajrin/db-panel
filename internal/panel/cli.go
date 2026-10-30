package panel

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

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const usage = `Penggunaan:
  dbpanel panel [--data-dir DIR] [--config-dir DIR] [--http ADDR] [--agent-listen ADDR] [--public-host HOST]
      jalankan panel web
  dbpanel panel reset-password [--username NAMA] [--data-dir DIR] [--config-dir DIR]
      buat kata sandi baru untuk pengguna dan cetak ke layar
`

// Main runs the "dbpanel panel" command; args start with "panel". It returns
// the process exit code: 0 ok, 1 runtime error, 2 usage error.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "panel" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	args = args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "reset-password":
			return mainResetPassword(args[1:], stdout, stderr)
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

// parse parses args and rejects positional arguments.
func parse(fs *flag.FlagSet, args []string) bool {
	if err := fs.Parse(args); err != nil {
		return false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return false
	}
	return true
}

func mainRun(args []string, stderr io.Writer) int {
	fs := newFlagSet("panel", stderr)
	var o Options
	fs.StringVar(&o.DataDir, "data-dir", DefaultDataDir, "direktori data (panel.db)")
	fs.StringVar(&o.ConfigDir, "config-dir", DefaultConfigDir, "direktori konfigurasi (CA, kata sandi admin awal)")
	fs.StringVar(&o.HTTPAddr, "http", DefaultHTTPAddr, "alamat UI web")
	fs.StringVar(&o.AgentAddr, "agent-listen", DefaultAgentAddr, "alamat listener agent (mTLS)")
	fs.StringVar(&o.PublicHost, "public-host", "", "IP/host panel yang dipakai agent (default: IP non-loopback pertama)")
	if !parse(fs, args) {
		return 2
	}
	o.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	o.Version = version.Version

	p, err := New(o)
	if err != nil {
		fmt.Fprintf(stderr, "kesalahan: %v\n", err)
		return 1
	}
	defer p.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o.Logger.Info("panel starting", "version", o.Version)
	if err := p.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "kesalahan: %v\n", err)
		return 1
	}
	o.Logger.Info("panel stopped")
	return 0
}

func mainResetPassword(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("panel reset-password", stderr)
	username := fs.String("username", adminUsername, "nama pengguna")
	dataDir := fs.String("data-dir", DefaultDataDir, "direktori data (panel.db)")
	configDir := fs.String("config-dir", DefaultConfigDir, "direktori konfigurasi (admin-initial-password dihapus)")
	if !parse(fs, args) {
		return 2
	}
	pw, err := resetPassword(*dataDir, *username)
	if err != nil {
		fmt.Fprintf(stderr, "kesalahan: %v\n", err)
		return 1
	}
	// The initial password no longer works; do not leave it lying around.
	path := filepath.Join(*configDir, initialPasswordFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "peringatan: gagal menghapus %s: %v\n", path, err)
	}
	fmt.Fprintf(stdout, "Kata sandi baru untuk %s: %s\n", *username, pw)
	return 0
}

// resetPassword sets a new random password for username in the panel
// database under dataDir and returns it.
func resetPassword(dataDir, username string) (string, error) {
	path := filepath.Join(dataDir, dbFile)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("basis data panel tidak ditemukan di %s: %w", path, err)
	}
	st, err := store.Open(path)
	if err != nil {
		return "", err
	}
	defer st.Close()
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return "", err
	}
	if err := st.SetPasswordHash(username, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("pengguna %q tidak ditemukan", username)
		}
		return "", err
	}
	return pw, nil
}
