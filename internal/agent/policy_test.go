package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func policyRegistry() *commands.Registry {
	reg := commands.NewRegistry()
	for _, n := range []string{"probe", "mysql.dropDatabases", "mysql.dumpFrom"} {
		reg.Register(n, func(context.Context, json.RawMessage) (any, error) { return "ran", nil })
	}
	return reg
}

func TestApplyPolicyDeniesListedCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PolicyFile),
		[]byte("# master produksi\ndeny:\n  - mysql.dropDatabases\n  - mysql.dumpFrom\n  - mysql.typo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	reg := policyRegistry()
	if err := ApplyPolicy(reg, dir, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"mysql.dropDatabases", "mysql.dumpFrom"} {
		r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd})
		if r.OK || r.Error.Message != "perintah dinonaktifkan oleh kebijakan lokal server ini" {
			t.Errorf("%s = %+v / %+v, want denied", cmd, r, r.Error)
		}
	}
	if r := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "probe"}); !r.OK {
		t.Errorf("probe refused: %+v", r.Error)
	}
	if !strings.Contains(logs.String(), "mysql.typo") || !strings.Contains(logs.String(), "local policy loaded") {
		t.Errorf("log does not name the unknown command or the policy:\n%s", logs.String())
	}
}

func TestLoadPolicy(t *testing.T) {
	// No file: no policy.
	if p, err := LoadPolicy(t.TempDir()); err != nil || len(p.Deny) != 0 {
		t.Fatalf("no file: %+v, %v", p, err)
	}
	write := func(t *testing.T, content string, mode os.FileMode) string {
		dir := t.TempDir()
		path := filepath.Join(dir, PolicyFile)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if p, err := LoadPolicy(write(t, "", 0o600)); err != nil || len(p.Deny) != 0 {
		t.Errorf("empty file: %+v, %v", p, err)
	}
	for name, c := range map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"group writable": {"deny: [mysql.dumpFrom]\n", 0o664, "chmod 0644"},
		"world writable": {"deny: [mysql.dumpFrom]\n", 0o646, "chmod 0644"},
		"unknown key":    {"allow: [probe]\n", 0o644, "tidak valid"},
		"empty name":     {"deny: ['']\n", 0o644, "nama perintah kosong"},
		"not yaml":       {"deny: [\n", 0o644, "tidak valid"},
	} {
		if _, err := LoadPolicy(write(t, c.content, c.mode)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// A symlink is refused: the file itself must be the operator's.
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("deny: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, PolicyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(dir); err == nil || !strings.Contains(err.Error(), "berkas biasa") {
		t.Errorf("symlink: err = %v", err)
	}
}
