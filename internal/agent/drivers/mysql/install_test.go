package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

const (
	ubuntuRelease = "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n"
	el9Release    = "NAME=\"Rocky Linux\"\nVERSION_ID=\"9.4\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n"
	el8Release    = "NAME=\"AlmaLinux\"\nVERSION_ID=\"8.10\"\nID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\n"

	aptPolicy    = "apt-cache policy mysql-server"
	unitFilesCmd = "systemctl list-unit-files --type=service --no-legend"
	noninteract  = "DEBIAN_FRONTEND=noninteractive "
	aptConfigURL = "https://repo.mysql.com/mysql-apt-config_0.8.33-1_all.deb"
	// tempRootPassword holds what the option file must escape.
	tempRootPassword = `Tmp"pw\#;1x`
)

func policyWith(candidate string) string {
	return "mysql-server:\n  Installed: (none)\n  Candidate: " + candidate + "\n  Version table:\n"
}

// installLog records, in order, what the install job asked the fakes for.
type installLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *installLog) add(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *installLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.lines...)
}

// fakeSystem is the host before MySQL is installed: os-release, scripted
// command output, and mysqld only when present is set.
type fakeSystem struct {
	log     *installLog
	files   map[string]string
	outputs map[string]string
	mysqld  string // LookPath("mysqld") answer; "" = not installed
}

func (s *fakeSystem) ReadFile(p string) ([]byte, error) {
	if b, ok := s.files[p]; ok {
		return []byte(b), nil
	}
	return nil, os.ErrNotExist
}

func (s *fakeSystem) LookPath(file string) (string, error) {
	if file == "mysqld" && s.mysqld != "" {
		return s.mysqld, nil
	}
	return "", exec.ErrNotFound
}

func (s *fakeSystem) Exists(p string) bool { _, ok := s.files[p]; return ok }

func (s *fakeSystem) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	s.log.add("query: " + cmd)
	if out, ok := s.outputs[cmd]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("unexpected command %q", cmd)
}

func (s *fakeSystem) Hostname() (string, error)             { return "fake", nil }
func (s *fakeSystem) Statfs(string) (uint64, uint64, error) { return 1 << 40, 1 << 39, nil }
func (s *fakeSystem) Arch() string                          { return "amd64" }

// fakeInstaller records the commands the job runs and downloads.
type fakeInstaller struct {
	log         *installLog
	dir         string
	files       map[string]string
	downloadErr error
	failOn      string // a command whose first word is this fails
}

func (f *fakeInstaller) Run(_ context.Context, stdin io.Reader, env []string, logLine func(string), name string, args ...string) error {
	line := strings.Join(append(append(append([]string{}, env...), name), args...), " ")
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		line += fmt.Sprintf(" <<< %q", b)
	}
	f.log.add(line)
	if name == f.failOn {
		return fmt.Errorf("%s gagal: exit status 1", name)
	}
	logLine("fake output of " + name)
	return nil
}

func (f *fakeInstaller) ReadFile(p string) ([]byte, error) {
	f.log.add("read " + p)
	if b, ok := f.files[p]; ok {
		return []byte(b), nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeInstaller) Download(_ context.Context, url string) (string, error) {
	f.log.add("download " + url)
	if f.downloadErr != nil {
		return "", f.downloadErr
	}
	p := filepath.Join(f.dir, path.Base(url))
	return p, os.WriteFile(p, []byte("package"), 0o600)
}

// execRecord is one Host.Exec call: argv, stdin, and the --defaults-file
// content at the time of the call.
type execRecord struct {
	argv     []string
	stdin    string
	defaults string
	file     string
}

// recordingHost is a test instance's Host whose Exec calls are recorded
// before they run.
type recordingHost struct {
	mysql.Host
	mu    sync.Mutex
	execs []execRecord
}

func (h *recordingHost) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	rec := execRecord{argv: append([]string{name}, args...)}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		rec.stdin = string(b)
		stdin = bytes.NewReader(b)
	}
	for _, a := range args {
		if f, ok := strings.CutPrefix(a, "--defaults-file="); ok {
			b, _ := os.ReadFile(f)
			rec.defaults, rec.file = string(b), f
		}
	}
	h.mu.Lock()
	h.execs = append(h.execs, rec)
	h.mu.Unlock()
	return h.Host.Exec(ctx, stdin, stdout, name, args...)
}

// installRig is a driver on a server without MySQL, whose installer and
// system are fakes; host is what the driver finds once MySQL is installed.
type installRig struct {
	log    *installLog
	sys    *fakeSystem
	inst   *fakeInstaller
	creds  mysql.CredentialStore
	reg    *commands.Registry
	events *jobEvents
	host   mysql.Host
	found  int // times the driver looked for the installed MySQL
}

func newInstallRig(t *testing.T, osRelease string) *installRig {
	t.Helper()
	dir := t.TempDir()
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	log := &installLog{}
	r := &installRig{
		log: log,
		sys: &fakeSystem{log: log, files: map[string]string{"/etc/os-release": osRelease}, outputs: map[string]string{
			unitFilesCmd: "dbus.service static\nmysql.service enabled\nmysqld.service enabled\nssh.service enabled\n",
		}},
		inst:   &fakeInstaller{log: log, dir: t.TempDir(), files: map[string]string{}},
		creds:  mysql.NewFileCredentialStore(filepath.Join(dir, "mysql.cred"), box),
		reg:    commands.NewRegistry(),
		events: &jobEvents{done: make(chan proto.JobDone, 4)},
	}
	runner := jobs.NewRunner(r.events, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d := &mysql.Driver{Creds: r.creds, Jobs: runner, Sys: r.sys}
	mysql.SetInstallHooks(d, r.inst, func(_ config.Config, creds mysql.CredentialStore, sys probe.System, _ *slog.Logger) (mysql.Host, error) {
		r.found++
		if sys != r.sys || creds != r.creds {
			return nil, errors.New("host built from the wrong system or credential store")
		}
		if r.host == nil {
			return nil, mysql.ErrNoMysqld
		}
		return r.host, nil
	})
	d.Register(r.reg)
	return r
}

func (r *installRig) dispatch(cmd string, params any) proto.Response {
	b, _ := json.Marshal(params)
	return r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: b})
}

// run starts mysql.install and waits for the job.
func (r *installRig) run(t *testing.T, p proto.InstallParams) proto.JobDone {
	t.Helper()
	resp := r.dispatch("mysql.install", p)
	if started := decode[proto.StartedJob](t, resp); started.JobID == "" {
		t.Fatal("mysql.install: no job id")
	}
	return (&snapshotRig{events: r.events}).wait(t)
}

func (r *installRig) logs() string {
	r.events.mu.Lock()
	defer r.events.mu.Unlock()
	return strings.Join(r.events.logs, "\n")
}

func (r *installRig) progress() []string {
	r.events.mu.Lock()
	defer r.events.mu.Unlock()
	var out []string
	for _, p := range r.events.progress {
		out = append(out, fmt.Sprintf("%d %s", p.Percent, p.Message))
	}
	return out
}

func installResult(t *testing.T, d proto.JobDone) proto.InstallResult {
	t.Helper()
	if !d.OK {
		t.Fatalf("install failed: %s", d.Error)
	}
	var res proto.InstallResult
	if err := json.Unmarshal(d.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func wantLines(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands:\n  got  %q\n  want %q", got, want)
	}
}

// instanceVersion is inst's @@version and its series.
func instanceVersion(t *testing.T, inst *testmysql.Instance) (string, string) {
	t.Helper()
	var v string
	if err := rootDB(t, inst).QueryRow("SELECT @@version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	d, err := mysql.ParseVersion(v)
	if err != nil {
		t.Fatal(err)
	}
	return v, d.Series
}

func TestInstallDebianUsesDistroWhenSeriesMatches(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	version, series := instanceVersion(t, inst)
	r := newInstallRig(t, ubuntuRelease)
	r.host = inst.HostWith(r.creds)
	r.sys.outputs[aptPolicy] = policyWith(series + ".40-0ubuntu0.24.04.1")

	res := installResult(t, r.run(t, proto.InstallParams{Series: series}))
	if res.Source != "distro" || res.Version != version {
		t.Fatalf("result = %+v, want distro %s", res, version)
	}
	wantLines(t, r.log.get(), []string{
		noninteract + "apt-get update",
		"query: " + aptPolicy,
		noninteract + "apt-get install -y mysql-server",
		"query: " + unitFilesCmd,
		"systemctl enable --now mysql.service",
	})
	if !strings.Contains(r.logs(), "fake output of apt-get") {
		t.Fatalf("package manager output not in job logs:\n%s", r.logs())
	}
	wantLines(t, r.progress(), []string{
		"0 memeriksa server", "25 memasang paket", "50 mengaktifkan service", "75 membuat akun admin", "100 selesai",
	})
}

func TestInstallDebianAddsMySQLRepoOtherwise(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	_, series := instanceVersion(t, inst)
	r := newInstallRig(t, ubuntuRelease)
	r.host = inst.HostWith(r.creds)
	r.sys.outputs[aptPolicy] = policyWith("(none)")

	res := installResult(t, r.run(t, proto.InstallParams{Series: series}))
	if res.Source != "mysql-repo" {
		t.Fatalf("source = %q, want mysql-repo", res.Source)
	}
	deb := filepath.Join(r.inst.dir, path.Base(aptConfigURL))
	wantLines(t, r.log.get(), []string{
		noninteract + "apt-get update",
		"query: " + aptPolicy,
		"download " + aptConfigURL,
		noninteract + "debconf-set-selections <<< " + fmt.Sprintf("%q", "mysql-apt-config mysql-apt-config/select-server select mysql-8.0\n"),
		noninteract + "apt-get install -y " + deb,
		noninteract + "apt-get update",
		noninteract + "apt-get install -y mysql-server",
		"query: " + unitFilesCmd,
		"systemctl enable --now mysql.service",
	})
	if _, err := os.Stat(deb); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("downloaded package left behind: %v", err)
	}
}

func TestInstallDebianRepoSeriesNames(t *testing.T) {
	for series, name := range map[string]string{"8.4": "mysql-8.4-lts", "9": "mysql-innovation"} {
		t.Run(series, func(t *testing.T) {
			r := newInstallRig(t, ubuntuRelease)
			r.sys.outputs[aptPolicy] = policyWith("8.0.40-0ubuntu0.24.04.1")
			r.inst.failOn = "debconf-set-selections"
			if d := r.run(t, proto.InstallParams{Series: series}); d.OK {
				t.Fatal("install succeeded with failing debconf-set-selections")
			}
			got := r.log.get()
			want := noninteract + "debconf-set-selections <<< " + fmt.Sprintf("%q", "mysql-apt-config mysql-apt-config/select-server select "+name+"\n")
			if got[len(got)-1] != want {
				t.Fatalf("last command = %q, want %q", got[len(got)-1], want)
			}
		})
	}
}

func TestInstallRHELSetsAuthSocket(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	version, series := instanceVersion(t, inst)
	// The package leaves root with an expired temporary password.
	setup, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec("ALTER USER 'root'@'localhost' IDENTIFIED BY " + sqlLiteral(tempRootPassword) + " PASSWORD EXPIRE"); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	r := newInstallRig(t, el9Release)
	rec := &recordingHost{Host: inst.HostWith(r.creds)}
	r.host = rec
	r.inst.files["/var/log/mysqld.log"] = "2026-10-30T01:00:00.000000Z 0 [System] [MY-013169] [Server] mysqld initializing\n" +
		"2026-10-30T01:00:01.000000Z 6 [Note] [MY-010454] [Server] A temporary password is generated for root@localhost: " + tempRootPassword + "\n"

	res := installResult(t, r.run(t, proto.InstallParams{Series: series}))
	if res.Source != "mysql-repo" || res.Version != version {
		t.Fatalf("result = %+v, want mysql-repo %s", res, version)
	}
	release := "https://repo.mysql.com/mysql80-community-release-el9-1.noarch.rpm"
	wantLines(t, r.log.get(), []string{
		"download " + release,
		"dnf install -y " + filepath.Join(r.inst.dir, path.Base(release)),
		"dnf install -y mysql-community-server",
		"query: " + unitFilesCmd,
		"systemctl enable --now mysqld.service",
		"read /var/log/mysqld.log",
	})

	if len(rec.execs) != 1 {
		t.Fatalf("Host.Exec calls = %+v, want one mysql call", rec.execs)
	}
	ex := rec.execs[0]
	if filepath.Base(ex.argv[0]) != "mysql" || len(ex.argv) != 3 || ex.argv[1] != "--defaults-file="+ex.file || ex.argv[2] != "--connect-expired-password" {
		t.Fatalf("mysql argv = %q", ex.argv)
	}
	if !strings.Contains(ex.defaults, `password="Tmp\"pw\\#;1x"`) || !strings.Contains(ex.defaults, `user="root"`) ||
		!strings.Contains(ex.defaults, `socket="`+inst.Socket+`"`) {
		t.Fatalf("defaults file =\n%s", ex.defaults)
	}
	if !strings.HasPrefix(ex.stdin, "ALTER USER 'root'@'localhost' IDENTIFIED BY '") || strings.Contains(ex.stdin, tempRootPassword) {
		t.Fatalf("mysql stdin = %q", ex.stdin)
	}
	if _, err := os.Stat(ex.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("defaults file left behind: %v", err)
	}

	// root now logs in through auth_socket (the tests run as OS root).
	var plugin string
	if err := rootDB(t, inst).QueryRow("SELECT plugin FROM mysql.user WHERE user = 'root' AND host = 'localhost'").Scan(&plugin); err != nil {
		t.Fatal(err)
	}
	if plugin != "auth_socket" {
		t.Fatalf("root plugin = %q, want auth_socket", plugin)
	}
	p, ok, err := r.creds.Load()
	if err != nil || !ok || p.User != "dbpanel_admin" {
		t.Fatalf("stored account = %+v %v %v", p, ok, err)
	}
	if logs := r.logs(); strings.Contains(logs, tempRootPassword) || strings.Contains(logs, p.Password) {
		t.Fatalf("job logs hold a password:\n%s", logs)
	}
}

func TestInstallRHEL8DisablesMySQLModule(t *testing.T) {
	r := newInstallRig(t, el8Release)
	r.inst.failOn = "systemctl"
	d := r.run(t, proto.InstallParams{Series: "8.4"})
	if d.OK || !strings.Contains(d.Error, "systemctl") {
		t.Fatalf("done = %+v, want the systemctl failure", d)
	}
	release := "https://repo.mysql.com/mysql84-community-release-el8-1.noarch.rpm"
	wantLines(t, r.log.get(), []string{
		"download " + release,
		"dnf install -y " + filepath.Join(r.inst.dir, path.Base(release)),
		"dnf module disable -y mysql",
		"dnf install -y mysql-community-server",
		"query: " + unitFilesCmd,
		"systemctl enable --now mysqld.service",
	})
	if r.found != 0 {
		t.Fatal("driver looked for MySQL although the service failed")
	}
	if resp := r.dispatch("mysql.status", nil); resp.OK || !strings.Contains(resp.Error.Message, mysql.ErrNoHost.Error()) {
		t.Fatalf("mysql.status = %+v, want no host", resp)
	}
}

func TestInstallFailsWhenDownloadFails(t *testing.T) {
	for _, tc := range []struct{ release, series string }{{ubuntuRelease, "8.4"}, {el9Release, "9"}} {
		t.Run(tc.series, func(t *testing.T) {
			r := newInstallRig(t, tc.release)
			r.sys.outputs[aptPolicy] = policyWith("8.0.40-0ubuntu0.24.04.1")
			r.inst.downloadErr = errors.New("dial tcp: connection refused")
			d := r.run(t, proto.InstallParams{Series: tc.series})
			want := "server ini tidak bisa mengunduh paket MySQL; sediakan mirror lokal atau pasang manual seri " + tc.series
			if d.OK || d.Error != want {
				t.Fatalf("error = %q, want %q", d.Error, want)
			}
			for _, l := range r.log.get() {
				if strings.Contains(l, "install") {
					t.Fatalf("installed after a failed download: %q", r.log.get())
				}
			}
		})
	}
}

func TestInstallDownloadURLOverride(t *testing.T) {
	t.Setenv("DBPANEL_MYSQL_APT_CONFIG", "https://mirror.example.com/mysql/mysql-apt-config_0.8.34-1_all.deb")
	r := newInstallRig(t, ubuntuRelease)
	r.sys.outputs[aptPolicy] = policyWith("(none)")
	r.inst.downloadErr = errors.New("unreachable")
	r.run(t, proto.InstallParams{Series: "8.0"})
	if got := r.log.get(); !slices.Contains(got, "download https://mirror.example.com/mysql/mysql-apt-config_0.8.34-1_all.deb") {
		t.Fatalf("commands = %q, want the mirror download", got)
	}

	t.Setenv("DBPANEL_MYSQL_RPM_RELEASE", "http://mirror.example.com/mysql84-community-release-el9-1.noarch.rpm")
	r = newInstallRig(t, el9Release)
	d := r.run(t, proto.InstallParams{Series: "8.4"})
	if d.OK || !strings.Contains(d.Error, "DBPANEL_MYSQL_RPM_RELEASE") || !strings.Contains(d.Error, "https://") {
		t.Fatalf("done = %+v, want the https:// refusal", d)
	}
	if got := r.log.get(); len(got) != 0 {
		t.Fatalf("commands = %q, want none", got)
	}
}

func TestInstallRefusesWhenMySQLPresent(t *testing.T) {
	r := newInstallRig(t, ubuntuRelease)
	r.sys.mysqld = "/usr/sbin/mysqld"
	d := r.run(t, proto.InstallParams{Series: "8.0"})
	if d.OK || !strings.Contains(d.Error, "MySQL sudah terpasang") || !strings.Contains(d.Error, "/usr/sbin/mysqld") {
		t.Fatalf("done = %+v, want the already-installed refusal", d)
	}
	if got := r.log.get(); len(got) != 0 {
		t.Fatalf("commands = %q, want none", got)
	}
}

func TestInstallRejectsBadParams(t *testing.T) {
	r := newInstallRig(t, ubuntuRelease)
	for _, s := range []string{"", "5.7", "8.1", "10"} {
		resp := r.dispatch("mysql.install", proto.InstallParams{Series: s})
		if resp.OK || resp.Error.Code != proto.CodeBadParams {
			t.Fatalf("series %q: response = %+v, want bad params", s, resp)
		}
	}
	r = newInstallRig(t, "ID=arch\n")
	if d := r.run(t, proto.InstallParams{Series: "8.0"}); d.OK || !strings.Contains(d.Error, "tidak didukung") {
		t.Fatalf("unknown OS: done = %+v", d)
	}
}

func TestInstallFailsOnSeriesMismatch(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	_, series := instanceVersion(t, inst)
	want := "8.4"
	if series == want {
		want = "8.0"
	}
	r := newInstallRig(t, ubuntuRelease)
	r.host = inst.HostWith(r.creds)
	r.sys.outputs[aptPolicy] = policyWith(want + ".3-1ubuntu24.04")
	d := r.run(t, proto.InstallParams{Series: want})
	if d.OK || !strings.Contains(d.Error, "seri "+series) || !strings.Contains(d.Error, want) {
		t.Fatalf("done = %+v, want a series mismatch", d)
	}
	// MySQL is installed all the same: the driver uses it.
	if resp := r.dispatch("mysql.status", nil); !resp.OK {
		t.Fatalf("mysql.status = %+v", resp.Error)
	}
}

func TestInstallCreatesAdminCredential(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	version, series := instanceVersion(t, inst)
	r := newInstallRig(t, ubuntuRelease)
	r.host = inst.HostWith(r.creds)
	r.sys.outputs[aptPolicy] = policyWith(series + ".40-0ubuntu0.24.04.1")
	if resp := r.dispatch("mysql.status", nil); resp.OK {
		t.Fatal("mysql.status answered before MySQL was installed")
	}

	installResult(t, r.run(t, proto.InstallParams{Series: series, ExactVersion: "8.0.1"}))
	p, ok, err := r.creds.Load()
	if err != nil || !ok {
		t.Fatalf("stored account: ok=%v err=%v", ok, err)
	}
	if p.User != "dbpanel_admin" || p.Host != "localhost" || p.Port != 0 || len(p.Password) != 24 {
		t.Fatalf("stored account = %q@%q:%d (password %d chars)", p.User, p.Host, p.Port, len(p.Password))
	}
	if strings.Contains(r.logs(), p.Password) {
		t.Fatal("job logs hold the admin password")
	}
	if !strings.Contains(r.logs(), "8.0.1") {
		t.Fatalf("no warning about the exact version:\n%s", r.logs())
	}

	db, err := sql.Open("mysql", mysql.DSN(p, inst.Socket))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		t.Fatal(err)
	}
	var grants []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	rows.Close()
	if missing := mysql.MissingReplicaGrants(grants, "dbpanel_admin@localhost"); len(missing) > 0 {
		t.Fatalf("admin account lacks %q; grants %q", missing, grants)
	}

	// The driver now has a host and connects with the stored account.
	st := decode[proto.MySQLStatus](t, r.dispatch("mysql.status", nil))
	if st.Version != version || st.Series != series {
		t.Fatalf("status = %s (%s), want %s", st.Version, st.Series, version)
	}
	if r.found != 1 {
		t.Fatalf("driver looked for MySQL %d times, want 1", r.found)
	}
}

func TestTemporaryPassword(t *testing.T) {
	log := "x [Note] A temporary password is generated for root@localhost: first\n" +
		"y [Note] [MY-010454] [Server] A temporary password is generated for root@localhost: s3c;r\"t\\  \r\n"
	if got := mysql.TemporaryPassword([]byte(log)); got != `s3c;r"t\` {
		t.Fatalf("temporary password = %q", got)
	}
	if got := mysql.TemporaryPassword([]byte("no password here\n")); got != "" {
		t.Fatalf("temporary password = %q, want none", got)
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		pw, err := mysql.RandomPassword(24)
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 24 || strings.Trim(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.") != "" ||
			!strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") ||
			!strings.ContainsAny(pw, "0123456789") || !strings.ContainsAny(pw, "-_.") {
			t.Fatalf("password %q", pw)
		}
		if seen[pw] {
			t.Fatalf("password %q repeated", pw)
		}
		seen[pw] = true
	}
}

func TestRealInstallerRun(t *testing.T) {
	ins := mysql.NewRealInstaller(probe.RealSystem(), nil)
	var lines []string
	logLine := func(l string) { lines = append(lines, l) }
	if err := ins.Run(context.Background(), strings.NewReader("one\n\ntwo\rthree\nfour"), nil, logLine, "cat"); err != nil {
		t.Fatal(err)
	}
	wantLines(t, lines, []string{"one", "three", "four"})

	lines = nil
	if err := ins.Run(context.Background(), nil, []string{"DEBIAN_FRONTEND=noninteractive"}, logLine, "printenv", "DEBIAN_FRONTEND"); err != nil {
		t.Fatal(err)
	}
	wantLines(t, lines, []string{"noninteractive"})

	err := ins.Run(context.Background(), nil, nil, func(string) {}, "ls", "/nonexistent-dbpanel-path")
	if err == nil || !strings.Contains(err.Error(), "ls gagal") || !strings.Contains(err.Error(), "nonexistent-dbpanel-path") {
		t.Fatalf("err = %v, want ls failure with its output", err)
	}
}

func TestRealInstallerDownload(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pkg.deb", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "deb content") })
	mux.HandleFunc("/to-http.rpm", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/pkg.deb", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	ins := mysql.NewRealInstaller(probe.RealSystem(), srv.Client().Transport)
	ctx := context.Background()

	p, err := ins.Download(ctx, srv.URL+"/pkg.deb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "deb content" || fi.Mode().Perm() != 0o600 || filepath.Ext(p) != ".deb" {
		t.Fatalf("downloaded %s (%v): %q", p, fi.Mode(), b)
	}
	for _, u := range []string{srv.URL + "/missing.deb", srv.URL + "/to-http.rpm", strings.Replace(srv.URL, "https://", "http://", 1) + "/pkg.deb"} {
		if p, err := ins.Download(ctx, u); err == nil {
			os.Remove(p)
			t.Fatalf("%s: downloaded", u)
		}
	}
}
