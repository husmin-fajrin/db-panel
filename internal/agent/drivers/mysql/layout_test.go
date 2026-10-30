package mysql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeSystem is a probe.System whose files, programs and command outputs are
// fixed by the test.
type fakeSystem struct {
	files   map[string]string // path -> content (exists)
	exists  map[string]bool   // paths that exist without content
	bins    map[string]string // LookPath name -> path
	outputs map[string]string // "name arg ..." -> stdout
	ran     []string          // commands passed to Output
}

func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	if c, ok := f.files[path]; ok {
		return []byte(c), nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeSystem) LookPath(file string) (string, error) {
	if p, ok := f.bins[file]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

func (f *fakeSystem) Exists(path string) bool {
	_, ok := f.files[path]
	return ok || f.exists[path]
}

func (f *fakeSystem) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.ran = append(f.ran, key)
	if out, ok := f.outputs[key]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("exit status 1")
}

func (f *fakeSystem) Hostname() (string, error)             { return "db1", nil }
func (f *fakeSystem) Statfs(string) (uint64, uint64, error) { return 0, 0, errors.New("no statfs") }
func (f *fakeSystem) Arch() string                          { return "amd64" }

// mysqldHelp imitates the parts of `mysqld --verbose --help` that
// DiscoverLayout reads.
func mysqldHelp(files, port, socket, datadir, pidFile string) string {
	return `/usr/sbin/mysqld  Ver 8.0.46-0ubuntu0.24.04.4 for Linux on x86_64 ((Ubuntu))
Copyright (c) 2000, 2026, Oracle and/or its affiliates.

Starts the MySQL database server.

Usage: mysqld [OPTIONS]

Default options are read from the following files in the given order:
` + files + ` 
The following groups are read: mysql_cluster mysqld server mysqld-8.0
  --port=#            Port number to use for connection or 0 to default to,
                      port number in the help text is not a variable row
  --socket=name       Socket file to use for connection

Variables (--variable-name=value)
and boolean options {FALSE|TRUE}                             Value (after reading options)
------------------------------------------------------------ -------------
datadir                                                      ` + datadir + `
mysqlx-port                                                  33060
pid-file                                                     ` + pidFile + `
port                                                         ` + port + `
socket                                                       ` + socket + `
`
}

func TestDiscoverLayoutDebianPackage(t *testing.T) {
	sys := &fakeSystem{
		bins: map[string]string{"mysqld": "/usr/sbin/mysqld"},
		files: map[string]string{
			"/etc/mysql/my.cnf": "# The MySQL database server configuration file.\n" +
				"!includedir /etc/mysql/conf.d/\n" +
				"!includedir /etc/mysql/mysql.conf.d/\n",
		},
		outputs: map[string]string{
			"/usr/sbin/mysqld --verbose --help": mysqldHelp("/etc/my.cnf /etc/mysql/my.cnf ~/.my.cnf",
				"3306", "/var/run/mysqld/mysqld.sock", "/var/lib/mysql/", "/var/lib/mysql/db1.pid"),
			"systemctl list-unit-files --type=service --no-legend": "cron.service enabled enabled\n" +
				"mysql.service enabled enabled\nssh.service enabled enabled\n",
		},
	}
	l, err := DiscoverLayout(context.Background(), sys)
	if err != nil {
		t.Fatal(err)
	}
	want := Layout{
		MysqldPath:  "/usr/sbin/mysqld",
		ConfigFile:  "/etc/mysql/my.cnf",
		ConfigFiles: []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "~/.my.cnf"},
		IncludeDir:  "/etc/mysql/mysql.conf.d", // the last !includedir is read last, so it wins
		DataDir:     "/var/lib/mysql",
		Socket:      "/var/run/mysqld/mysqld.sock",
		PIDFile:     "/var/lib/mysql/db1.pid",
		Port:        3306,
		ServiceKind: "systemd",
		ServiceName: "mysql.service",
	}
	assertLayout(t, l, want)
	// Stock files in that directory (mysqld.cnf) sort before ours.
	if got := filepath.Join(l.IncludeDir, IncludeFile); got != "/etc/mysql/mysql.conf.d/zz-dbpanel.cnf" {
		t.Errorf("managed include file = %q", got)
	}
}

func TestDiscoverLayoutAAPanel(t *testing.T) {
	tests := []struct {
		name, units, kind, service string
	}{
		// systemd-sysv-generator turns /etc/init.d/mysqld into a "generated"
		// unit that systemd reports inactive while the script's server runs.
		{"generated unit from init script", "cron.service enabled enabled\nmysqld.service generated -\n",
			"initscript", "/etc/init.d/mysqld"},
		{"systemd unknown to MySQL", "cron.service enabled enabled\n", "initscript", "/etc/init.d/mysqld"},
		{"real systemd unit", "cron.service enabled enabled\nmysqld.service enabled disabled\n",
			"systemd", "mysqld.service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := &fakeSystem{
				exists: map[string]bool{
					"/www/server/mysql/bin/mysqld": true,
					"/etc/init.d/mysqld":           true,
				},
				files: map[string]string{
					"/etc/my.cnf": "[client]\nport = 3306\nsocket = /tmp/mysql.sock\n\n[mysqld]\nport = 3306\n" +
						"socket = /tmp/mysql.sock\ndatadir = /www/server/data\nlog-bin = mysql-bin\n",
				},
				outputs: map[string]string{
					"/www/server/mysql/bin/mysqld --verbose --help": mysqldHelp("/etc/my.cnf /etc/mysql/my.cnf /www/server/mysql/etc/my.cnf ~/.my.cnf",
						"3306", "/tmp/mysql.sock", "/www/server/data/", "/www/server/data/db1.pid"),
					"systemctl list-unit-files --type=service --no-legend": tt.units,
				},
			}
			l, err := DiscoverLayout(context.Background(), sys)
			if err != nil {
				t.Fatal(err)
			}
			want := Layout{
				MysqldPath:  "/www/server/mysql/bin/mysqld",
				ConfigFile:  "/etc/my.cnf",
				ConfigFiles: []string{"/etc/my.cnf", "/etc/mysql/my.cnf", "/www/server/mysql/etc/my.cnf", "~/.my.cnf"},
				IncludeDir:  "",
				DataDir:     "/www/server/data",
				Socket:      "/tmp/mysql.sock",
				PIDFile:     "/www/server/data/db1.pid",
				Port:        3306,
				ServiceKind: tt.kind,
				ServiceName: tt.service,
			}
			assertLayout(t, l, want)
		})
	}
}

func assertLayout(t *testing.T, got, want Layout) {
	t.Helper()
	if strings.Join(got.ConfigFiles, " ") != strings.Join(want.ConfigFiles, " ") {
		t.Errorf("ConfigFiles = %q, want %q", got.ConfigFiles, want.ConfigFiles)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("layout =\n%+v\nwant\n%+v", got, want)
	}
}

func TestWriteConfigManagedBlockReplaces(t *testing.T) {
	t.Run("main config file", func(t *testing.T) {
		dir := t.TempDir()
		cnf := filepath.Join(dir, "my.cnf")
		orig := "[mysqld]\ndatadir=/var/lib/mysql\n"
		if err := os.WriteFile(cnf, []byte(orig), 0o640); err != nil {
			t.Fatal(err)
		}
		h := NewHost(Layout{ConfigFile: cnf}, nil, nil, &fakeSystem{}, nil)

		if _, err := h.WriteConfig("[mysqld]\nserver_id=2\n"); err != nil {
			t.Fatal(err)
		}
		path, err := h.WriteConfig("[mysqld]\nserver_id=3\ngtid_mode=ON\n")
		if err != nil {
			t.Fatal(err)
		}
		if path != cnf {
			t.Errorf("path = %q, want %q", path, cnf)
		}
		b, _ := os.ReadFile(cnf)
		got := string(b)
		if n := strings.Count(got, "# BEGIN dbpanel"); n != 1 {
			t.Errorf("BEGIN markers = %d, want 1:\n%s", n, got)
		}
		if n := strings.Count(got, "# END dbpanel"); n != 1 {
			t.Errorf("END markers = %d, want 1:\n%s", n, got)
		}
		if strings.Contains(got, "server_id=2") || !strings.Contains(got, "server_id=3\ngtid_mode=ON\n") {
			t.Errorf("block not replaced:\n%s", got)
		}
		if !strings.HasPrefix(got, orig) {
			t.Errorf("original content lost:\n%s", got)
		}
		if st, _ := os.Stat(cnf); st.Mode().Perm() != 0o640 {
			t.Errorf("mode = %v, want 0640", st.Mode().Perm())
		}
	})

	t.Run("include dir", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHost(Layout{ConfigFile: filepath.Join(dir, "my.cnf"), IncludeDir: dir}, nil, nil, &fakeSystem{}, nil)
		for _, c := range []string{"[mysqld]\nserver_id=2\n", "[mysqld]\nserver_id=3\n"} {
			if _, err := h.WriteConfig(c); err != nil {
				t.Fatal(err)
			}
		}
		want := filepath.Join(dir, "zz-dbpanel.cnf")
		path, err := h.WriteConfig("[mysqld]\nserver_id=3\n")
		if err != nil {
			t.Fatal(err)
		}
		if path != want || !strings.HasSuffix(path, "/zz-dbpanel.cnf") {
			t.Errorf("path = %q, want %q", path, want)
		}
		b, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != "[mysqld]\nserver_id=3\n" {
			t.Errorf("zz-dbpanel.cnf = %q", got)
		}
		if st, _ := os.Stat(want); st.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want 0644", st.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(dir, "my.cnf")); !os.IsNotExist(err) {
			t.Errorf("main config file touched: %v", err)
		}
	})
}
