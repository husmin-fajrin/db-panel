// Package config reads and writes the agent's configuration file,
// <dir>/agent.yaml, written by "dbpanel agent pair".
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultDir holds the agent's configuration, key and certificates.
const DefaultDir = "/etc/dbpanel"

const fileName = "agent.yaml"

// ErrNotPaired is returned by Load when the agent has no configuration yet.
var ErrNotPaired = errors.New("agent belum dipasangkan (jalankan: dbpanel agent pair)")

// Config is the agent's configuration.
type Config struct {
	PanelAddr         string `yaml:"panel_addr"` // host:port of the panel's mTLS listener, e.g. "192.168.1.10:8443"
	ServerID          int64  `yaml:"server_id"`
	CertFile          string `yaml:"cert_file"` // default <dir>/agent.crt
	KeyFile           string `yaml:"key_file"`  // default <dir>/agent.key
	CAFile            string `yaml:"ca_file"`   // default <dir>/ca.crt
	MySQLRootPassword string `yaml:"mysql_root_password,omitempty"`
	MySQLSocket       string `yaml:"mysql_socket,omitempty"` // overrides the socket found by layout discovery
}

// Load reads dir/agent.yaml, filling empty file paths with their defaults in
// dir. It returns ErrNotPaired when the file does not exist.
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotPaired
	}
	if err != nil {
		return Config{}, fmt.Errorf("baca %s: %w", fileName, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s tidak valid: %w", fileName, err)
	}
	if c.PanelAddr == "" || c.ServerID <= 0 {
		return Config{}, fmt.Errorf("%s tidak lengkap: panel_addr dan server_id wajib diisi", fileName)
	}
	if c.CertFile == "" {
		c.CertFile = filepath.Join(dir, "agent.crt")
	}
	if c.KeyFile == "" {
		c.KeyFile = filepath.Join(dir, "agent.key")
	}
	if c.CAFile == "" {
		c.CAFile = filepath.Join(dir, "ca.crt")
	}
	return c, nil
}

// Save writes c to dir/agent.yaml with mode 0600, creating dir with mode 0700
// if needed. The file is replaced atomically.
func Save(dir string, c Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode %s: %w", fileName, err)
	}
	return WriteFile(dir, fileName, b)
}

// WriteFile atomically replaces dir/name with data, mode 0600, creating dir
// with mode 0700 if needed. It is used for every file in the agent's
// directory, which holds the private key.
func WriteFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("buat direktori %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	return nil
}
