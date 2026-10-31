package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
)

// PolicyFile is the host's local policy in the agent's directory
// (/etc/dbpanel/agent-policy.yaml). Only an operator on the host writes it;
// no panel command does. It lists commands this agent refuses whatever the
// panel asks, for example on a production master:
//
//	deny:
//	  - mysql.dropDatabases
//	  - mysql.dumpFrom
//	  - mysql.cloneFrom
const PolicyFile = "agent-policy.yaml"

// Policy is the content of PolicyFile.
type Policy struct {
	Deny []string `yaml:"deny"`
}

// LoadPolicy reads dir/PolicyFile; no file is an empty policy. The file
// must be a regular file owned by root (or by the user the agent runs as)
// that neither group nor others can write: a policy anyone else could edit
// would protect nothing.
func LoadPolicy(dir string) (Policy, error) {
	var p Policy
	path := filepath.Join(dir, PolicyFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("baca %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return p, fmt.Errorf("%s harus berkas biasa (bukan symlink atau direktori)", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return p, fmt.Errorf("%s bisa ditulis grup atau pengguna lain (mode %04o); jalankan: chmod 0644 %s", path, fi.Mode().Perm(), path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return p, fmt.Errorf("%s harus milik root; jalankan: chown root:root %s", path, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("baca %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	// An empty file (io.EOF) is an empty policy.
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("%s tidak valid: %w", path, err)
	}
	for i, name := range p.Deny {
		p.Deny[i] = strings.TrimSpace(name)
		if p.Deny[i] == "" {
			return Policy{}, fmt.Errorf("%s tidak valid: nama perintah kosong di deny", path)
		}
	}
	return p, nil
}

// ApplyPolicy loads dir's local policy into reg and logs it; commands it
// names that reg does not know are logged too (a typo protects nothing).
func ApplyPolicy(reg *commands.Registry, dir string, logger *slog.Logger) error {
	p, err := LoadPolicy(dir)
	if err != nil {
		return err
	}
	if len(p.Deny) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, n := range reg.Names() {
		known[n] = true
	}
	for _, n := range p.Deny {
		if !known[n] {
			logger.Warn("local policy denies an unknown command", "cmd", n)
		}
	}
	reg.Deny(p.Deny...)
	logger.Info("local policy loaded", "file", filepath.Join(dir, PolicyFile), "deny", p.Deny)
	return nil
}
