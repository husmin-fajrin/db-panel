package mysql

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.install puts MySQL on a server that has none: from the
// distribution when it ships the requested series (Debian family), else
// from MySQL's own repository; then it creates the admin account the agent
// keeps, so the server is connected without asking the user.

const (
	sourceDistro = "distro"
	sourceRepo   = "mysql-repo"

	// adminUser is the account mysql.install creates and stores.
	adminUser        = "dbpanel_admin"
	adminPasswordLen = 24

	// mysqldLog is where MySQL's RPM packages write root's temporary password.
	mysqldLog          = "/var/log/mysqld.log"
	tempPasswordMarker = "A temporary password is generated for root@localhost: "

	// The repository packages, overridable for an air-gapped mirror.
	envAptConfig     = "DBPANEL_MYSQL_APT_CONFIG"
	envRPMRelease    = "DBPANEL_MYSQL_RPM_RELEASE"
	defaultAptConfig = "https://repo.mysql.com/mysql-apt-config_0.8.33-1_all.deb"
	rpmReleaseBase   = "https://repo.mysql.com/"

	// dnf repositories of mysql84-community-release.
	ltsRepo        = "mysql-8.4-lts-community"
	innovationRepo = "mysql-innovation-community"

	downloadTimeout  = 10 * time.Minute
	maxDownloadBytes = 64 << 20 // the repository packages are a few dozen KB
	maxRedirects     = 10
	maxLogLine       = 4096
)

// installCancelWait is how long a cancelled installer command gets after
// SIGTERM before it is killed (and its output pipes closed).
var installCancelWait = 30 * time.Second

// installSeries maps each series mysql.install offers to the server choice
// of mysql-apt-config (as of mysql-apt-config 0.8.33).
var installSeries = map[string]string{
	"8.0": "mysql-8.0",
	"8.4": "mysql-8.4-lts",
	"9":   "mysql-innovation",
}

// rpmReleases is the dnf release package per series and EL major release.
// The innovation series has no release package of its own: it is a
// repository of mysql84-community-release. The "-1" packages of 8.0 carry
// the GPG key that expired in 2023.
// verify against https://dev.mysql.com/downloads/repo/yum/ before release
var rpmReleases = map[string]map[string]string{
	"8.0": {"8": "mysql80-community-release-el8-9.noarch.rpm", "9": "mysql80-community-release-el9-5.noarch.rpm"},
	"8.4": {"8": "mysql84-community-release-el8-1.noarch.rpm", "9": "mysql84-community-release-el9-1.noarch.rpm",
		"10": "mysql84-community-release-el10-1.noarch.rpm"},
	"9": {"8": "mysql84-community-release-el8-1.noarch.rpm", "9": "mysql84-community-release-el9-1.noarch.rpm",
		"10": "mysql84-community-release-el10-1.noarch.rpm"},
}

// aptEnv keeps apt, dpkg and debconf from asking questions, and their
// output in English, so "Candidate:" is not translated.
var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}

// installer runs what mysql.install needs before MySQL, and so a Host,
// exists. Commands run without a shell, with fixed argument lists.
type installer interface {
	// Run runs name with args and env added to the agent's environment,
	// feeding stdin when it is not nil; every line of its output (stdout
	// and stderr) goes to logLine.
	Run(ctx context.Context, stdin io.Reader, env []string, logLine func(string), name string, args ...string) error
	ReadFile(path string) ([]byte, error)
	// Download fetches an https URL into a new 0600 file named with the
	// URL's extension and returns its path; the caller removes it.
	Download(ctx context.Context, rawURL string) (string, error)
}

// hostFactory finds the MySQL installation and returns its Host (NewRealHost).
type hostFactory func(cfg config.Config, creds CredentialStore, sys probe.System, logger *slog.Logger) (Host, error)

func (d *Driver) installer() installer {
	if d.inst != nil {
		return d.inst
	}
	return realInstaller{sys: d.Sys}
}

func (d *Driver) hostFactory() hostFactory {
	if d.newHost != nil {
		return d.newHost
	}
	return NewRealHost
}

func (d *Driver) setHost(h Host) {
	d.mu.Lock()
	d.Host = h
	d.mu.Unlock()
}

func (d *Driver) registerInstall(reg *commands.Registry) {
	reg.Register("mysql.install", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.InstallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		p.Series, p.ExactVersion = strings.TrimSpace(p.Series), strings.TrimSpace(p.ExactVersion)
		if _, ok := installSeries[p.Series]; !ok {
			return nil, &commands.ParamError{Msg: fmt.Sprintf("seri MySQL harus 8.0, 8.4 atau 9, bukan %q", p.Series)}
		}
		switch {
		case d.Sys == nil:
			return nil, errors.New("agent tidak bisa memasang MySQL di server ini")
		case d.Creds == nil:
			return nil, errors.New("agent tidak punya tempat menyimpan kredensial MySQL")
		case d.Jobs == nil:
			return nil, errors.New("agent tidak bisa menjalankan pekerjaan latar")
		}
		id, err := d.Jobs.Start("mysql.install", func(j *jobs.Job) (any, error) {
			res, err := d.install(j, p)
			if d.Logger != nil {
				if err != nil {
					d.Logger.Warn("mysql install failed", "job", j.ID(), "series", p.Series, "err", err)
				} else {
					d.Logger.Info("mysql installed", "job", j.ID(), "version", res.Version, "source", res.Source)
				}
			}
			if err != nil {
				return nil, err
			}
			return res, nil
		})
		if err != nil {
			return nil, err
		}
		if d.Logger != nil {
			d.Logger.Info("mysql install started", "job", id, "series", p.Series, "exact_version", p.ExactVersion)
		}
		return proto.StartedJob{JobID: id}, nil
	})
}

// install is the mysql.install job. It never touches a server where mysqld
// already exists. Once the service runs, the driver uses the new server
// even when a later step fails.
func (d *Driver) install(j *jobs.Job, p proto.InstallParams) (proto.InstallResult, error) {
	ctx := j.Context()
	var res proto.InstallResult
	j.Progress(0, "memeriksa server")
	if path, err := FindMysqld(d.Sys); err == nil {
		return res, fmt.Errorf("MySQL sudah terpasang di server ini (%s); pemasangan dibatalkan", path)
	}
	s := &installSteps{ctx: ctx, j: j, ins: d.installer(), sys: d.Sys, series: p.Series}
	family := probe.OSFamily(d.Sys)

	j.Progress(25, "memasang paket")
	var err error
	switch family {
	case "debian":
		res.Source, err = s.debian()
	case "rhel":
		res.Source, err = s.rhel()
	default:
		return res, errors.New("sistem operasi server ini tidak didukung untuk pemasangan MySQL (butuh keluarga Debian/Ubuntu atau RHEL)")
	}
	if err != nil {
		return res, err
	}

	j.Progress(50, "mengaktifkan service")
	unit, err := mysqlUnit(ctx, d.Sys, family)
	if err != nil {
		return res, err
	}
	if err := s.run(nil, nil, "systemctl", "enable", "--now", unit); err != nil {
		return res, err
	}
	h, err := d.hostFactory()(d.Cfg, d.Creds, d.Sys, d.Logger)
	if err != nil {
		return res, fmt.Errorf("MySQL sudah dipasang, tetapi agent tidak menemukannya: %w", err)
	}
	d.setHost(h)
	l := h.Layout()
	j.Logf("info", "mysql found: mysqld %s, config %s, socket %s", l.MysqldPath, l.ConfigFile, l.Socket)

	j.Progress(75, "membuat akun admin")
	if family == "rhel" {
		if err := secureRoot(ctx, j, h, s.ins, d.Creds); err != nil {
			return res, err
		}
	}
	if err := createAdmin(ctx, l.Socket, d.Creds); err != nil {
		return res, err
	}
	j.Logf("info", "admin account %s@localhost created and stored", adminUser)

	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	st, err := Status(ctx, db)
	if err != nil {
		return res, err
	}
	res.Version = st.Version
	v, err := ParseVersion(st.Version)
	if err != nil {
		return res, fmt.Errorf("MySQL %s terpasang: %w", st.Version, err)
	}
	if v.Series != p.Series {
		return res, fmt.Errorf("MySQL yang terpasang versi %s (seri %s), bukan seri %s yang diminta", st.Version, v.Series, p.Series)
	}
	if num, _, _ := strings.Cut(st.Version, "-"); p.ExactVersion != "" && num != p.ExactVersion {
		j.Logf("warn", "installed MySQL %s, not the requested %s", st.Version, p.ExactVersion)
	}
	j.Progress(100, "selesai")
	return res, nil
}

// installSteps runs the package steps of one install job.
type installSteps struct {
	ctx    context.Context
	j      *jobs.Job
	ins    installer
	sys    probe.System
	series string
}

// run runs one command, its output going to the job log line by line.
func (s *installSteps) run(stdin io.Reader, env []string, name string, args ...string) error {
	s.j.Logf("info", "running %s", strings.Join(append([]string{name}, args...), " "))
	return s.ins.Run(s.ctx, stdin, env, func(line string) { s.j.Logf("info", "%s", line) }, name, args...)
}

// download fetches a repository package; its failure is told in the
// user's words, the cause goes to the job log.
func (s *installSteps) download(rawURL string) (string, error) {
	shown := redactURL(rawURL)
	s.j.Logf("info", "downloading %s", shown)
	file, err := s.ins.Download(s.ctx, rawURL)
	if err != nil {
		s.j.Logf("error", "download %s: %v", shown, err)
		return "", fmt.Errorf("server ini tidak bisa mengunduh paket MySQL; sediakan mirror lokal atau pasang manual seri %s", s.series)
	}
	return file, nil
}

// debian installs mysql-server from the distribution when its candidate
// is of the requested series, else from MySQL's apt repository.
func (s *installSteps) debian() (string, error) {
	if err := s.run(nil, aptEnv, "apt-get", "update"); err != nil {
		return "", err
	}
	candidate := s.aptCandidate()
	s.j.Logf("info", "distribution mysql-server candidate: %q", candidate)
	if hasSeries(candidate, s.series) {
		return sourceDistro, s.run(nil, aptEnv, "apt-get", "install", "-y", "mysql-server")
	}

	pkgURL, err := packageURL(envAptConfig, defaultAptConfig, ".deb")
	if err != nil {
		return "", err
	}
	deb, err := s.download(pkgURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(deb)
	selection := "mysql-apt-config mysql-apt-config/select-server select " + installSeries[s.series] + "\n"
	if err := s.run(strings.NewReader(selection), aptEnv, "debconf-set-selections"); err != nil {
		return "", err
	}
	// apt-get installs mysql-apt-config's dependencies too, which dpkg -i
	// would not on a minimal image. dpkg checks no signature on a local
	// package: this one is trusted for coming over HTTPS from repo.mysql.com,
	// and it installs MySQL's key, which apt then checks the server
	// packages against. mysql-community-server, not the virtual
	// mysql-server, so apt cannot pick a newer series from the distribution.
	for _, args := range [][]string{{"install", "-y", deb}, {"update"}, {"install", "-y", "mysql-community-server"}} {
		if err := s.run(nil, aptEnv, "apt-get", args...); err != nil {
			return "", err
		}
	}
	return sourceRepo, nil
}

// rhel installs mysql-community-server from MySQL's dnf repository.
func (s *installSteps) rhel() (string, error) {
	major, err := elMajor(s.sys)
	if err != nil {
		return "", err
	}
	release, ok := rpmReleases[s.series][major]
	if !ok {
		return "", fmt.Errorf("MySQL seri %s tidak tersedia untuk EL%s; pilih seri lain atau pasang manual", s.series, major)
	}
	pkgURL, err := packageURL(envRPMRelease, rpmReleaseBase+release, ".rpm")
	if err != nil {
		return "", err
	}
	rpm, err := s.download(pkgURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(rpm)
	// dnf does not GPG-check a local package by default (localpkg_gpgcheck=0),
	// so no --nogpgcheck is needed: the release package is trusted for
	// coming over HTTPS from repo.mysql.com. It installs MySQL's key, which
	// dnf checks mysql-community-server against.
	if err := s.run(nil, nil, "dnf", "install", "-y", rpm); err != nil {
		return "", err
	}
	if major == "8" {
		// EL8's AppStream mysql module hides the community packages.
		if err := s.run(nil, nil, "dnf", "module", "disable", "-y", "mysql"); err != nil {
			return "", err
		}
	}
	args := []string{"install", "-y", "mysql-community-server"}
	if s.series == "9" {
		// The enable lasts this one transaction: a later dnf upgrade does
		// not follow 9.x (accepted for v1).
		args = []string{"install", "-y", "--disablerepo=" + ltsRepo, "--enablerepo=" + innovationRepo, "mysql-community-server"}
	}
	return sourceRepo, s.run(nil, nil, "dnf", args...)
}

// aptCandidate is the version apt would install for mysql-server; "" when
// there is none.
func (s *installSteps) aptCandidate() string {
	candidate := ""
	collect := func(line string) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
			if v = strings.TrimSpace(v); v != "(none)" {
				candidate = v
			}
		}
	}
	if err := s.ins.Run(s.ctx, nil, aptEnv, collect, "apt-cache", "policy", "mysql-server"); err != nil {
		s.j.Logf("warn", "apt-cache policy mysql-server: %v", err)
		return ""
	}
	return candidate
}

// hasSeries reports whether the Debian package version v (with or without
// an epoch) belongs to series.
func hasSeries(v, series string) bool {
	if _, after, ok := strings.Cut(v, ":"); ok {
		v = after
	}
	return v != "" && strings.HasPrefix(v, series+".")
}

// elMajor is the major release of an RHEL-family system ("9" for 9.4).
// Only EL 8, 9 and 10 are supported; Fedora and Amazon Linux, which share
// the family, are not.
func elMajor(sys probe.System) (string, error) {
	b, err := sys.ReadFile("/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("baca /etc/os-release: %w", err)
	}
	version := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION_ID="); ok {
			version = strings.Trim(v, `"'`)
		}
	}
	switch major, _, _ := strings.Cut(version, "."); major {
	case "8", "9", "10":
		return major, nil
	}
	return "", fmt.Errorf("OS tidak didukung untuk pemasangan otomatis (VERSION_ID %q); pasang MySQL secara manual", version)
}

// packageURL is the URL in the environment variable env, else def. An
// override must be an https URL of a file with extension ext.
func packageURL(env, def, ext string) (string, error) {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return def, nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || !strings.HasSuffix(u.Path, ext) {
		return "", fmt.Errorf("%s harus berupa URL https:// ke file %s", env, ext)
	}
	return v, nil
}

// redactURL is rawURL with any password in its userinfo masked, for logs
// and errors.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(URL tidak valid)"
	}
	return u.Redacted()
}

// mysqlUnit is the systemd unit the packages installed: mysqld on RHEL,
// mysql on Debian, whichever exists.
func mysqlUnit(ctx context.Context, sys probe.System, family string) (string, error) {
	out, err := sys.Output(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend")
	if err != nil {
		return "", fmt.Errorf("systemctl list-unit-files gagal: %s", errText(err))
	}
	units := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units[f[0]] = true
		}
	}
	order := []string{"mysql.service", "mysqld.service"}
	if family == "rhel" {
		order = []string{"mysqld.service", "mysql.service"}
	}
	for _, u := range order {
		if units[u] {
			return u, nil
		}
	}
	return "", errors.New("unit systemd MySQL (mysqld/mysql) tidak ditemukan setelah pemasangan")
}

// secureRoot replaces the expired temporary password MySQL's RPM packages
// give root with auth_socket, the way Debian's packages leave root. Only
// the mysql client can log in with an expired password, and such a session
// may only reset it; auth_socket is a plugin Oracle's builds do not load,
// which cannot be installed from that session. So the client sets an
// interim random password first, kept in creds, and the rest goes over a
// normal login. No password is ever in argv or a log.
func secureRoot(ctx context.Context, j *jobs.Job, h Host, ins installer, creds CredentialStore) error {
	b, err := ins.ReadFile(mysqldLog)
	if err != nil {
		return fmt.Errorf("baca %s: %w", mysqldLog, err)
	}
	temp := temporaryPassword(b)
	if temp == "" {
		return fmt.Errorf("password sementara root tidak ditemukan di %s", mysqldLog)
	}
	interim, err := randomPassword(adminPasswordLen)
	if err != nil {
		return err
	}
	l := h.Layout()
	bin, err := findClientTool(l.MysqldPath, "mysql")
	if err != nil {
		return err
	}
	file, err := writeDefaultsFile(h, proto.ConnectParams{User: "root", Password: temp}, l.Socket, l.Port, false)
	if err != nil {
		return err
	}
	defer os.Remove(file)
	hide := strings.NewReplacer(temp, "***", interim, "***")

	j.Logf("info", "resetting the temporary root password")
	stmt := "ALTER USER 'root'@'localhost' IDENTIFIED BY " + quoteString(interim) + ";\n"
	if err := h.Exec(ctx, strings.NewReader(stmt), nil, bin, "--defaults-file="+file, "--connect-expired-password"); err != nil {
		return fmt.Errorf("ganti password sementara root: %s", hide.Replace(err.Error()))
	}
	// Until root uses auth_socket, the interim password is the only way in:
	// keep it, so a failure or a cancel below does not lock root out.
	// createAdmin replaces it with the admin account.
	if err := creds.Save(proto.ConnectParams{User: "root", Password: interim, Host: "localhost"}); err != nil {
		return fmt.Errorf("simpan kredensial MySQL: %w", err)
	}
	db, err := openWith(ctx, l.Socket, proto.ConnectParams{User: "root", Password: interim})
	if err != nil {
		return errors.New(hide.Replace(loginFailure(err)) + " (root dengan password baru)")
	}
	defer db.Close()
	if err := loadAuthSocket(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket"); err != nil {
		return fmt.Errorf("ubah login root ke auth_socket: %w", err)
	}
	j.Logf("info", "root@localhost now authenticates with auth_socket")
	return nil
}

// temporaryPassword is the last temporary root password in mysqld's log.
func temporaryPassword(log []byte) string {
	pw := ""
	for _, line := range strings.Split(string(log), "\n") {
		if _, after, ok := strings.Cut(line, tempPasswordMarker); ok {
			pw = strings.TrimSpace(after)
		}
	}
	return pw
}

// loadAuthSocket installs the auth_socket plugin unless it is active.
func loadAuthSocket(ctx context.Context, db *sql.DB) error {
	var status string
	err := db.QueryRowContext(ctx, `SELECT PLUGIN_STATUS FROM information_schema.PLUGINS WHERE PLUGIN_NAME = 'auth_socket'`).Scan(&status)
	switch {
	case err == nil && status == "ACTIVE":
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("baca status plugin auth_socket: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSTALL PLUGIN auth_socket SONAME 'auth_socket.so'"); err != nil {
		return fmt.Errorf("pasang plugin auth_socket: %w", err)
	}
	return nil
}

// createAdmin creates adminUser@localhost with a random password and every
// privilege, and stores it in creds. A new server's root logs in over the
// socket without a password (auth_socket), whatever creds held before.
func createAdmin(ctx context.Context, socket string, creds CredentialStore) error {
	db, err := openWith(ctx, socket, proto.ConnectParams{User: "root"})
	if err != nil {
		return errors.New(loginFailure(err) + " (root lewat socket)")
	}
	defer db.Close()
	// One session, so the sql_mode read is the one the statement runs in.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return fmt.Errorf("baca sql_mode: %w", err)
	}
	password, err := randomPassword(adminPasswordLen)
	if err != nil {
		return err
	}
	// MySQL takes no placeholder in IDENTIFIED BY; the literal never reaches
	// a log or an error.
	literal := quotePassword(password, strings.Contains(mode, "NO_BACKSLASH_ESCAPES"))
	account := quoteString(adminUser) + "@" + quoteString("localhost")
	// IF NOT EXISTS and ALTER: a retry after a half-created account works.
	for _, stmt := range []string{"CREATE USER IF NOT EXISTS ", "ALTER USER "} {
		if _, err := conn.ExecContext(ctx, stmt+account+" IDENTIFIED BY "+literal); err != nil {
			return fmt.Errorf("buat akun admin %s: %s", account, strings.ReplaceAll(err.Error(), password, "***"))
		}
	}
	if _, err := conn.ExecContext(ctx, "GRANT ALL PRIVILEGES ON *.* TO "+account+" WITH GRANT OPTION"); err != nil {
		return fmt.Errorf("beri hak akun admin %s: %w", account, err)
	}
	if err := creds.Save(proto.ConnectParams{User: adminUser, Password: password, Host: "localhost"}); err != nil {
		return fmt.Errorf("simpan kredensial MySQL: %w", err)
	}
	return nil
}

// Password alphabet. validate_password, on by default in Oracle's RPM
// installs, wants an upper and lower case letter, a digit and a special
// character; the special ones here need no quoting in SQL or option files.
const (
	pwUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	pwLower   = "abcdefghijklmnopqrstuvwxyz"
	pwDigits  = "0123456789"
	pwSpecial = "-_."
)

// randomPassword is n characters from crypto/rand holding every class of
// the password alphabet.
func randomPassword(n int) (string, error) {
	alphabet := pwUpper + pwLower + pwDigits + pwSpecial
	size := big.NewInt(int64(len(alphabet)))
	b := make([]byte, n)
	for {
		for i := range b {
			k, err := rand.Int(rand.Reader, size)
			if err != nil {
				return "", fmt.Errorf("buat password acak: %w", err)
			}
			b[i] = alphabet[k.Int64()]
		}
		s := string(b)
		if strings.ContainsAny(s, pwUpper) && strings.ContainsAny(s, pwLower) &&
			strings.ContainsAny(s, pwDigits) && strings.ContainsAny(s, pwSpecial) {
			return s, nil
		}
	}
}

// realInstaller runs commands with os/exec and downloads with net/http.
type realInstaller struct {
	sys       probe.System
	transport http.RoundTripper // nil: http.DefaultTransport
}

func (r realInstaller) ReadFile(p string) ([]byte, error) { return r.sys.ReadFile(p) }

func (realInstaller) Run(ctx context.Context, stdin io.Reader, env []string, logLine func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	// Cancelling must not SIGKILL apt or dpkg mid-transaction (a locked or
	// half-configured package database a user cannot repair): SIGTERM lets
	// them stop cleanly, and only one still running installCancelWait later
	// is killed.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = installCancelWait
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	out := &lineWriter{emit: logLine}
	// One writer for both: exec then writes to it from one goroutine.
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	out.flush()
	if err != nil {
		if out.last != "" {
			return fmt.Errorf("%s gagal: %v: %s", name, err, out.last)
		}
		return fmt.Errorf("%s gagal: %v", name, err)
	}
	return nil
}

func (r realInstaller) Download(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("hanya unduhan https yang diizinkan: %s", redactURL(rawURL))
	}
	client := &http.Client{
		Transport: r.transport,
		Timeout:   downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect ke %s ditolak: bukan https", req.URL.Redacted())
			}
			if len(via) >= maxRedirects {
				return errors.New("terlalu banyak redirect")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %s", u.Redacted(), resp.Status)
	}
	f, err := os.CreateTemp("", "dbpanel-*"+path.Ext(u.Path)) // mode 0600
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err == nil && n > maxDownloadBytes {
		err = fmt.Errorf("%s lebih besar dari %d byte", u.Redacted(), maxDownloadBytes)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// lineWriter hands every line written to it to emit and keeps the last
// one for the error message.
type lineWriter struct {
	emit func(string)
	buf  []byte
	last string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLine {
		w.line(w.buf)
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.line(w.buf)
		w.buf = w.buf[:0]
	}
}

// line emits a non-empty line. apt redraws its progress with carriage
// returns; only what was drawn last is kept.
func (w *lineWriter) line(b []byte) {
	s := strings.TrimRight(string(b), "\r")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	if s = strings.TrimSpace(s); s != "" {
		w.last = s
		w.emit(s)
	}
}
