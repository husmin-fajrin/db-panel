package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dbpanel")
	want := Config{
		PanelAddr:         "192.168.1.10:8443",
		ServerID:          7,
		CertFile:          filepath.Join(dir, "agent.crt"),
		KeyFile:           filepath.Join(dir, "agent.key"),
		CAFile:            filepath.Join(dir, "ca.crt"),
		MySQLRootPassword: "rahasia",
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("agent.yaml mode = %o, want 600", mode)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Fatalf("dir mode = %o, want 700", mode)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadFillsDefaultPaths(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{PanelAddr: "10.0.0.1:8443", ServerID: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.CertFile != filepath.Join(dir, "agent.crt") ||
		got.KeyFile != filepath.Join(dir, "agent.key") ||
		got.CAFile != filepath.Join(dir, "ca.crt") {
		t.Fatalf("default paths not filled: %+v", got)
	}
}

func TestLoadMissingReturnsErrNotPaired(t *testing.T) {
	_, err := Load(t.TempDir())
	if !errors.Is(err, ErrNotPaired) {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
}
