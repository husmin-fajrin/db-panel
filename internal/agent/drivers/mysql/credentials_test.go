package mysql_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

func TestFileCredentialStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sub", "mysql.cred")
	s := mysql.NewFileCredentialStore(path, box)

	if _, ok, err := s.Load(); err != nil || ok {
		t.Fatalf("Load on empty store = ok %v, err %v", ok, err)
	}
	want := proto.ConnectParams{User: "admin", Password: "p@ss'w\"ord", Host: "10.0.0.5", Port: 3307}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok || got != want {
		t.Fatalf("Load = %+v, %v, %v; want %+v", got, ok, err, want)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v; want mode 0600", fi, err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("admin")) || bytes.Contains(raw, []byte(want.Password)) {
		t.Fatal("credential file is not sealed")
	}

	// Saving again replaces; another key cannot read it.
	want.Password = "second"
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Load(); got != want {
		t.Fatalf("after second Save: %+v", got)
	}
	other, _ := secrets.LoadOrCreate(filepath.Join(dir, "k2"))
	if _, _, err := mysql.NewFileCredentialStore(path, other).Load(); err == nil {
		t.Fatal("Load with the wrong key succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "sub", "*.tmp*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear twice: %v", err)
	}
	if _, ok, _ := s.Load(); ok {
		t.Fatal("Load after Clear found credentials")
	}
}
