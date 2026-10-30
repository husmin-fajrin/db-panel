package mysql

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// NewFileCredentialStore keeps the admin account in path (normally
// /etc/dbpanel/mysql.cred): the account as JSON, sealed with box, mode 0600.
func NewFileCredentialStore(path string, box *secrets.Box) CredentialStore {
	return &fileCredentialStore{path: path, box: box}
}

type fileCredentialStore struct {
	mu   sync.Mutex
	path string
	box  *secrets.Box
}

func (s *fileCredentialStore) Load() (proto.ConnectParams, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p proto.ConnectParams
	sealed, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("baca %s: %w", s.path, err)
	}
	plain, err := s.box.Open(sealed)
	if err != nil {
		return p, false, fmt.Errorf("buka %s: %w", s.path, err)
	}
	if err := json.Unmarshal(plain, &p); err != nil {
		return proto.ConnectParams{}, false, fmt.Errorf("isi %s tidak valid: %w", s.path, err)
	}
	return p, true, nil
}

func (s *fileCredentialStore) Save(p proto.ConnectParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain, err := json.Marshal(p)
	if err != nil {
		return err
	}
	sealed, err := s.box.Seal(plain)
	if err != nil {
		return err
	}
	return writeSealed(s.path, sealed)
}

func (s *fileCredentialStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hapus %s: %w", s.path, err)
	}
	return nil
}

// writeSealed replaces path with data (mode 0600) through a temporary file
// created exclusively next to it, so a crash never leaves a partial file.
func writeSealed(path string, data []byte) error {
	fail := func(err error) error { return fmt.Errorf("tulis %s: %w", path, err) }
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fail(err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*") // O_EXCL, mode 0600
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return nil
}
