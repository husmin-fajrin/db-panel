// Package probe implements the "probe" agent command, which describes the
// host: OS, hardware, disks and any MySQL installation.
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const defaultMySQLPort = 3306

// Register adds the "probe" command to r. The handler ignores its params.
func Register(r *commands.Registry, sys System, agentVersion string) {
	r.Register("probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return Collect(ctx, sys, agentVersion)
	})
}

// Collect describes the host. A failing sub-check leaves its fields zero; only
// a Hostname failure makes Collect return an error.
func Collect(ctx context.Context, sys System, agentVersion string) (proto.ProbeResult, error) {
	host, err := sys.Hostname()
	if err != nil {
		return proto.ProbeResult{}, err
	}
	res := proto.ProbeResult{
		Hostname:     host,
		Arch:         sys.Arch(),
		AgentVersion: agentVersion,
		Disks:        []proto.DiskInfo{},
		OSFamily:     "unknown",
	}

	if b, err := sys.ReadFile("/etc/os-release"); err == nil {
		res.OS, res.OSFamily = parseOSRelease(b)
	}
	if out, err := sys.Output(ctx, "uname", "-r"); err == nil {
		res.Kernel = strings.TrimSpace(string(out))
	}
	if b, err := sys.ReadFile("/proc/cpuinfo"); err == nil {
		res.CPUCores = countProcessors(b)
	}
	if b, err := sys.ReadFile("/proc/meminfo"); err == nil {
		res.MemTotalMB = parseMemTotalMB(b)
	}

	res.MySQL = probeMySQL(ctx, sys)

	res.Disks = appendDisk(res.Disks, sys, "/")
	if res.MySQL.Installed && res.MySQL.DataDir != "" && res.MySQL.DataDir != "/" {
		res.Disks = appendDisk(res.Disks, sys, res.MySQL.DataDir)
	}
	return res, nil
}

func appendDisk(disks []proto.DiskInfo, sys System, mount string) []proto.DiskInfo {
	total, free, err := sys.Statfs(mount)
	if err != nil {
		return disks
	}
	const mb = 1024 * 1024
	return append(disks, proto.DiskInfo{
		Mount:   mount,
		TotalMB: int64(total / mb),
		UsedMB:  int64((total - free) / mb),
	})
}

func parseOSRelease(b []byte) (pretty, family string) {
	var id, idLike string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "PRETTY_NAME":
			pretty = v
		case "ID":
			id = v
		case "ID_LIKE":
			idLike = v
		}
	}
	return pretty, osFamily(id + " " + idLike)
}

func osFamily(ids string) string {
	for _, f := range strings.Fields(strings.ToLower(ids)) {
		switch f {
		case "debian", "ubuntu":
			return "debian"
		}
	}
	for _, f := range strings.Fields(strings.ToLower(ids)) {
		switch f {
		case "rhel", "fedora", "centos", "rocky", "almalinux":
			return "rhel"
		}
	}
	return "unknown"
}

func countProcessors(b []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, _, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == "processor" {
			n++
		}
	}
	return n
}

func parseMemTotalMB(b []byte) int64 {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(k) != "MemTotal" {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// mysqldPaths are where distributions put mysqld outside PATH: RHEL, Rocky
// and Alma AppStream install /usr/libexec/mysqld.
var mysqldPaths = []string{"/usr/sbin/mysqld", "/usr/libexec/mysqld"}

// findMysqld returns the command to run mysqld with, or "" when it is not
// installed.
func findMysqld(sys System) string {
	if _, err := sys.LookPath("mysqld"); err == nil {
		return "mysqld"
	}
	for _, p := range mysqldPaths {
		if sys.Exists(p) {
			return p
		}
	}
	return ""
}

func probeMySQL(ctx context.Context, sys System) proto.MySQLInfo {
	info := proto.MySQLInfo{}
	mysqld := findMysqld(sys)
	if mysqld == "" {
		return info
	}
	info.Installed = true
	info.Port = defaultMySQLPort

	if out, err := sys.Output(ctx, mysqld, "--version"); err == nil {
		info.Version = parseMySQLVersion(string(out))
	}

	for _, name := range []string{"mysql", "mysqld"} {
		// systemctl exits non-zero for "inactive", so use stdout regardless of err.
		out, _ := sys.Output(ctx, "systemctl", "is-active", name)
		state := strings.TrimSpace(string(out))
		if state == "" || state == "unknown" {
			continue // unit does not exist (or lookup failed)
		}
		// Any other state (active, inactive, failed, activating, ...) means the
		// unit exists; only "active" counts as running.
		info.ServiceName, info.ServiceActive = name, state == "active"
		break
	}

	if out, err := sys.Output(ctx, mysqld, "--verbose", "--help"); err == nil {
		port, dir := parseMysqldHelp(out)
		if port > 0 {
			info.Port = port
		}
		info.DataDir = dir
	}
	return info
}

// parseMySQLVersion extracts the token after "Ver " (e.g. "8.4.3").
func parseMySQLVersion(s string) string {
	_, rest, ok := strings.Cut(s, "Ver ")
	if !ok {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func parseMysqldHelp(b []byte) (port int, dataDir string) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "port":
			if p, err := strconv.Atoi(f[1]); err == nil {
				port = p
			}
		case "datadir":
			dataDir = strings.TrimRight(f[1], "/")
			if dataDir == "" {
				dataDir = "/"
			}
		}
	}
	return port, dataDir
}

// OSFamily reports the host's distribution family from /etc/os-release:
// "debian", "rhel" or "unknown".
func OSFamily(sys System) string {
	b, err := sys.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	_, family := parseOSRelease(b)
	return family
}
