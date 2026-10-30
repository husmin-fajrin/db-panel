// Package mysql is the agent's MySQL driver. It finds how MySQL is laid out
// on the host (distribution package, aaPanel, tarball), talks to it through
// the Host interface and registers the mysql.* commands.
package mysql

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// Service kinds reported in Layout.ServiceKind.
const (
	ServiceSystemd    = "systemd"
	ServiceInitScript = "initscript"
	ServiceMysqldSafe = "mysqld_safe"
	ServiceUnknown    = "unknown"
)

// Layout is where a MySQL installation keeps its program, configuration and
// data, and how its service is run.
type Layout struct {
	MysqldPath  string
	ConfigFile  string   // main option file: the first of ConfigFiles that exists
	ConfigFiles []string // option files in the order mysqld reads them
	IncludeDir  string   // last !includedir of ConfigFile; "" when it has none
	DataDir     string
	Socket      string
	PIDFile     string
	Port        int
	ServiceKind string // systemd | initscript | mysqld_safe | unknown (tests: "test")
	ServiceName string // systemd unit or init script path
}

// ErrNoMysqld is returned by DiscoverLayout when mysqld is not installed.
var ErrNoMysqld = errors.New("mysqld tidak ditemukan di server ini")

// mysqldPaths are checked when mysqld is not on PATH: distribution packages
// (Debian /usr/sbin, RHEL /usr/libexec), aaPanel and the tarball default.
var mysqldPaths = []string{
	"/usr/sbin/mysqld",
	"/usr/libexec/mysqld",
	"/www/server/mysql/bin/mysqld",
	"/usr/local/mysql/bin/mysqld",
}

// systemdUnits and initScripts are checked in order; the first present wins.
var (
	systemdUnits = []string{"mysql.service", "mysqld.service", "mariadb.service"}
	initScripts  = []string{"/etc/init.d/mysqld", "/etc/init.d/mysql"}
)

// FindMysqld returns the path of the mysqld binary.
func FindMysqld(sys probe.System) (string, error) {
	if p, err := sys.LookPath("mysqld"); err == nil {
		return p, nil
	}
	for _, p := range mysqldPaths {
		if sys.Exists(p) {
			return p, nil
		}
	}
	return "", ErrNoMysqld
}

// DiscoverLayout asks mysqld and the system where MySQL lives. It runs only
// `mysqld --verbose --help`, `systemctl list-unit-files` and `pgrep -x
// mysqld_safe`, and otherwise checks which files exist.
func DiscoverLayout(ctx context.Context, sys probe.System) (Layout, error) {
	mysqld, err := FindMysqld(sys)
	if err != nil {
		return Layout{}, err
	}
	l := Layout{MysqldPath: mysqld}

	// mysqld may exit non-zero (e.g. a warning about running as root) after
	// printing the full help, so use whatever it wrote.
	out, err := sys.Output(ctx, mysqld, "--verbose", "--help")
	if len(out) == 0 {
		if err == nil {
			err = errors.New("no output")
		}
		return Layout{}, errors.New("gagal membaca konfigurasi bawaan mysqld: " + err.Error())
	}
	parseHelp(out, &l)

	for _, f := range l.ConfigFiles {
		if !strings.HasPrefix(f, "~") && sys.Exists(f) {
			l.ConfigFile = f
			break
		}
	}
	if l.ConfigFile == "" {
		// No option file yet: use the first system-wide one, to be created.
		for _, f := range l.ConfigFiles {
			if strings.HasPrefix(f, "/") {
				l.ConfigFile = f
				break
			}
		}
	}
	if b, err := sys.ReadFile(l.ConfigFile); err == nil {
		l.IncludeDir = lastIncludeDir(b)
	}

	l.ServiceKind, l.ServiceName = discoverService(ctx, sys)
	return l, nil
}

// parseHelp reads the option file list and the port, socket, datadir and
// pid-file rows of the "Variables" table of `mysqld --verbose --help`.
func parseHelp(out []byte, l *Layout) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inVars := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Default options are read from the following files"):
			if sc.Scan() {
				l.ConfigFiles = strings.Fields(sc.Text())
			}
			continue
		case strings.HasPrefix(line, "Variables (--variable-name=value)"):
			inVars = true
			continue
		}
		if !inVars {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || strings.HasPrefix(f[1], "(") {
			continue
		}
		switch f[0] {
		case "port":
			if p, err := strconv.Atoi(f[1]); err == nil {
				l.Port = p
			}
		case "socket":
			l.Socket = f[1]
		case "datadir":
			l.DataDir = trimDir(f[1])
		case "pid-file":
			l.PIDFile = f[1]
		}
	}
}

func trimDir(d string) string {
	if t := strings.TrimRight(d, "/"); t != "" {
		return t
	}
	return "/"
}

// lastIncludeDir returns the directory of the last !includedir line. Option
// files read later override earlier ones, so a file there wins over the
// directories included before it.
func lastIncludeDir(b []byte) string {
	dir := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == "!includedir" {
			dir = trimDir(f[1])
		}
	}
	return dir
}

func discoverService(ctx context.Context, sys probe.System) (kind, name string) {
	if out, err := sys.Output(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend"); err == nil {
		units := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				units[f[0]] = true
			}
		}
		for _, u := range systemdUnits {
			if units[u] {
				return ServiceSystemd, u
			}
		}
	}
	for _, s := range initScripts {
		if sys.Exists(s) {
			return ServiceInitScript, s
		}
	}
	if out, err := sys.Output(ctx, "pgrep", "-x", "mysqld_safe"); err == nil && len(bytes.TrimSpace(out)) > 0 {
		return ServiceMysqldSafe, "mysqld_safe"
	}
	return ServiceUnknown, ""
}
