package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

type statfsResult struct{ total, free uint64 }

type fakeSystem struct {
	files    map[string][]byte
	bins     map[string]bool
	outputs  map[string]string // "name arg arg" -> stdout
	failCmds map[string]bool   // commands whose Output returns stdout and an error
	statfs   map[string]statfsResult
	host     string
	hostErr  error
}

func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	if b, ok := f.files[path]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeSystem) LookPath(file string) (string, error) {
	if f.bins[file] {
		return "/usr/sbin/" + file, nil
	}
	return "", errors.New("not found")
}

func (f *fakeSystem) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f.outputs[key]
	if !ok {
		return nil, errors.New("no such command: " + key)
	}
	if f.failCmds[key] {
		return []byte(out), errors.New("exit status 3")
	}
	return []byte(out), nil
}

func (f *fakeSystem) Hostname() (string, error) { return f.host, f.hostErr }

func (f *fakeSystem) Statfs(path string) (uint64, uint64, error) {
	s, ok := f.statfs[path]
	if !ok {
		return 0, 0, errors.New("statfs failed")
	}
	return s.total, s.free, nil
}

func (f *fakeSystem) Arch() string { return "amd64" }

func td(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const mb = 1024 * 1024

const mysqldHelp = `mysqld  Ver 8.4.3 for Linux on x86_64 (MySQL Community Server - GPL)
Copyright (c) 2000, 2024, Oracle and/or its affiliates.

Variables (--variable-name=value)
and boolean options {FALSE|TRUE}  Value (after reading options)
--------------------------------- ----------------------------------------
datadir                                                          /var/lib/mysql/
port                                                             3306
`

func ubuntuSystem(t *testing.T) *fakeSystem {
	return &fakeSystem{
		host: "db1",
		files: map[string][]byte{
			"/etc/os-release": td(t, "os-release-ubuntu"),
			"/proc/cpuinfo":   td(t, "cpuinfo"),
			"/proc/meminfo":   td(t, "meminfo"),
		},
		bins: map[string]bool{"mysqld": true},
		outputs: map[string]string{
			"uname -r":                  "6.8.0-45-generic\n",
			"mysqld --version":          "/usr/sbin/mysqld  Ver 8.4.3 for Linux on x86_64 (MySQL Community Server - GPL)\n",
			"systemctl is-active mysql": "active\n",
			"mysqld --verbose --help":   mysqldHelp,
		},
		statfs: map[string]statfsResult{
			"/":              {total: 100 * 1024 * mb, free: 40 * 1024 * mb},
			"/var/lib/mysql": {total: 500 * 1024 * mb, free: 100 * 1024 * mb},
		},
	}
}

func TestCollectUbuntuWithMySQL(t *testing.T) {
	res, err := Collect(context.Background(), ubuntuSystem(t), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if res.Hostname != "db1" || res.Arch != "amd64" || res.AgentVersion != "1.2.3" {
		t.Errorf("basic fields: %+v", res)
	}
	if res.OS != "Ubuntu 24.04.1 LTS" || res.OSFamily != "debian" {
		t.Errorf("OS = %q family = %q", res.OS, res.OSFamily)
	}
	if res.Kernel != "6.8.0-45-generic" {
		t.Errorf("Kernel = %q", res.Kernel)
	}
	if res.CPUCores != 4 {
		t.Errorf("CPUCores = %d", res.CPUCores)
	}
	if res.MemTotalMB != 8123456/1024 {
		t.Errorf("MemTotalMB = %d", res.MemTotalMB)
	}
	m := res.MySQL
	if !m.Installed || m.Version != "8.4.3" || m.ServiceName != "mysql" || !m.ServiceActive || m.Port != 3306 || m.DataDir != "/var/lib/mysql" {
		t.Errorf("MySQL = %+v", m)
	}
	if len(res.Disks) != 2 {
		t.Fatalf("Disks = %+v", res.Disks)
	}
	if d := res.Disks[0]; d.Mount != "/" || d.TotalMB != 100*1024 || d.UsedMB != 60*1024 {
		t.Errorf("Disks[0] = %+v", d)
	}
	if d := res.Disks[1]; d.Mount != "/var/lib/mysql" || d.TotalMB != 500*1024 || d.UsedMB != 400*1024 {
		t.Errorf("Disks[1] = %+v", d)
	}
}

func TestCollectInactiveService(t *testing.T) {
	sys := ubuntuSystem(t)
	sys.outputs["systemctl is-active mysql"] = "inactive\n"
	sys.failCmds = map[string]bool{"systemctl is-active mysql": true}
	res, err := Collect(context.Background(), sys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if res.MySQL.ServiceName != "mysql" || res.MySQL.ServiceActive {
		t.Errorf("MySQL = %+v", res.MySQL)
	}
}

func TestCollectFallsBackToMysqldService(t *testing.T) {
	sys := ubuntuSystem(t)
	sys.outputs["systemctl is-active mysql"] = "unknown\n"
	sys.failCmds = map[string]bool{"systemctl is-active mysql": true}
	sys.outputs["systemctl is-active mysqld"] = "active\n"
	res, err := Collect(context.Background(), sys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if res.MySQL.ServiceName != "mysqld" || !res.MySQL.ServiceActive {
		t.Errorf("MySQL = %+v", res.MySQL)
	}
}

func TestCollectRockyWithoutMySQL(t *testing.T) {
	sys := &fakeSystem{
		host: "db2",
		files: map[string][]byte{
			"/etc/os-release": td(t, "os-release-rocky"),
			"/proc/cpuinfo":   td(t, "cpuinfo"),
			"/proc/meminfo":   td(t, "meminfo"),
		},
		outputs: map[string]string{"uname -r": "5.14.0-427.el9.x86_64\n"},
		statfs:  map[string]statfsResult{"/": {total: 10 * mb, free: 4 * mb}},
	}
	res, err := Collect(context.Background(), sys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if res.OSFamily != "rhel" || res.OS != "Rocky Linux 9.4 (Blue Onyx)" {
		t.Errorf("OS = %q family = %q", res.OS, res.OSFamily)
	}
	if res.MySQL.Installed {
		t.Errorf("MySQL = %+v", res.MySQL)
	}
	if len(res.Disks) != 1 || res.Disks[0].Mount != "/" {
		t.Errorf("Disks = %+v", res.Disks)
	}
}

func TestCollectToleratesMissingProcFiles(t *testing.T) {
	sys := &fakeSystem{host: "h", files: map[string][]byte{}}
	res, err := Collect(context.Background(), sys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if res.CPUCores != 0 || res.MemTotalMB != 0 {
		t.Errorf("res = %+v", res)
	}
	if res.OSFamily != "unknown" {
		t.Errorf("OSFamily = %q", res.OSFamily)
	}
	if res.Disks == nil || len(res.Disks) != 0 {
		t.Errorf("Disks = %#v, want empty non-nil", res.Disks)
	}
}

func TestCollectHostnameError(t *testing.T) {
	sys := &fakeSystem{hostErr: errors.New("no hostname")}
	if _, err := Collect(context.Background(), sys, "v"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectDefaultPortWhenHelpFails(t *testing.T) {
	sys := ubuntuSystem(t)
	delete(sys.outputs, "mysqld --verbose --help")
	res, err := Collect(context.Background(), sys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if res.MySQL.Port != 3306 || res.MySQL.DataDir != "" {
		t.Errorf("MySQL = %+v", res.MySQL)
	}
	if len(res.Disks) != 1 {
		t.Errorf("Disks = %+v", res.Disks)
	}
}

func TestRegisterProbe(t *testing.T) {
	r := commands.NewRegistry()
	Register(r, ubuntuSystem(t), "9.9.9")
	resp := r.Dispatch(context.Background(), proto.Request{ID: "p", Cmd: "probe"})
	if !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	if !strings.Contains(string(resp.Result), `"agent_version":"9.9.9"`) {
		t.Errorf("result = %s", resp.Result)
	}
}
