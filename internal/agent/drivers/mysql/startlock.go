package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The start lock (R-031) keeps a quarantined old master from coming up
// writable when its MySQL starts while the agent is not there to lock it:
// one init_file line in the managed config names a root-owned SQL file
// that turns read-only on, and PERSISTs it, after the persisted values
// (mysqld-auto.cnf, where a once-promoted server holds read_only OFF) are
// loaded and before any client gets a handshake.
//
// Order: the file is written before the line, and the line is removed
// before the file (releaseStartLock), so the config never names an init_file that
// does not exist (mysqld refuses to start on one, and --validate-config
// does not notice). The file is never in the data directory and never a
// .cnf, so no !includedir reads it as an option file.

// StartLockFile is the start lock's file name.
const StartLockFile = "dbpanel-start-lock.sql"

// startLockContent is the start lock file, statement by statement.
const startLockContent = "SET GLOBAL super_read_only=ON;\nSET PERSIST read_only=ON;\nSET PERSIST super_read_only=ON;\n"

// printDefaultsTimeout bounds mysqld --print-defaults.
const printDefaultsTimeout = 30 * time.Second

// managedConfigMu is held by every writer of the managed config in this
// process (writeStartLock, releaseStartLock, configure), from reading the
// content to writing it, so none overwrites another's change. Across
// processes (the agent and "dbpanel agent set-role master") the same
// writers also hold ConfigLockFile: lockManagedConfig takes both.
var managedConfigMu sync.Mutex

// ConfigLockFile is the file flocked by every writer of the managed config
// or the start lock file, in the directory of ConfigFile (symlinks
// followed), or the nearest one above it outside DataDir and IncludeDir:
// the agent and the CLI find the same place from the MySQL layout they
// both discover. It is never a .cnf (no option file reads it), never in
// DataDir or IncludeDir, and never removed.
const ConfigLockFile = "dbpanel-config.lock"

// configLockWait bounds how long a writer waits for another process's
// hold of ConfigLockFile (a context ending sooner ends it sooner).
const configLockWait = 30 * time.Second

// configLockPath is the ConfigLockFile of l.
func configLockPath(l Layout) (string, error) {
	if l.ConfigFile == "" {
		return "", errors.New("file konfigurasi MySQL tidak diketahui; kunci konfigurasi tidak bisa ditempatkan")
	}
	p := l.ConfigFile
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	dir := filepath.Dir(p)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("direktori konfigurasi MySQL %s bukan path absolut; kunci konfigurasi tidak bisa ditempatkan", dir)
	}
	// The nearest directory up that is neither in DataDir nor IncludeDir.
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if !(l.DataDir != "" && insideDir(d, l.DataDir)) && !(l.IncludeDir != "" && insideDir(d, l.IncludeDir)) {
			return filepath.Join(d, ConfigLockFile), nil
		}
		if d == filepath.Dir(d) {
			return "", fmt.Errorf("tidak ada direktori di atas %s yang berada di luar datadir dan direktori include; kunci konfigurasi tidak bisa ditempatkan", dir)
		}
	}
}

// flockConfig takes an exclusive flock on path (created 0600 when
// missing), waiting while another holder has it, until ctx ends or
// configLockWait passes; it returns the function that releases it.
func flockConfig(ctx context.Context, path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("kunci konfigurasi %s: %w", path, err)
	}
	deadline := time.Now().Add(configLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil // closing the last descriptor drops the flock
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("kunci konfigurasi %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("kunci konfigurasi %s masih dipegang proses lain setelah %s", path, configLockWait)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("kunci konfigurasi %s masih dipegang proses lain: %w", path, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// lockManagedConfig takes managedConfigMu, then the flock on l's
// ConfigLockFile, for one read-to-write of the managed config or of the
// start lock file; it returns the function that releases both. Failing
// to take the flock is the operation's error.
func lockManagedConfig(ctx context.Context, l Layout) (unlock func(), err error) {
	path, err := configLockPath(l)
	if err != nil {
		return nil, err
	}
	managedConfigMu.Lock()
	unflock, err := flockConfig(ctx, path)
	if err != nil {
		managedConfigMu.Unlock()
		return nil, err
	}
	return func() { unflock(); managedConfigMu.Unlock() }, nil
}

// StartLockPath is where the start lock file goes for l: IncludeDir, else
// the directory of ConfigFile with symlinks followed. A place inside
// DataDir is refused.
func StartLockPath(l Layout) (string, error) {
	var dir string
	switch {
	case l.IncludeDir != "":
		dir = l.IncludeDir
	case l.ConfigFile != "":
		p := l.ConfigFile
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		dir = filepath.Dir(p)
	default:
		return "", errors.New("file konfigurasi MySQL tidak diketahui; kunci start tidak bisa ditempatkan")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("direktori konfigurasi MySQL %s bukan path absolut; kunci start tidak bisa ditempatkan", dir)
	}
	dir = filepath.Clean(dir)
	if l.DataDir != "" && insideDir(dir, l.DataDir) {
		return "", fmt.Errorf("direktori konfigurasi MySQL %s ada di dalam datadir %s; kunci start tidak ditempatkan di sana", dir, l.DataDir)
	}
	return filepath.Join(dir, StartLockFile), nil
}

// insideDir reports whether dir is base or below it, as written or with
// symlinks followed.
func insideDir(dir, base string) bool {
	in := func(d, b string) bool {
		d, b = filepath.Clean(d), filepath.Clean(b)
		return d == b || strings.HasPrefix(d, strings.TrimSuffix(b, "/")+"/")
	}
	rd, rb := dir, base
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		rd = r
	}
	if r, err := filepath.EvalSymlinks(base); err == nil {
		rb = r
	}
	return in(dir, base) || in(rd, rb) || in(dir, rb) || in(rd, base)
}

// readManagedContent is the managed config's content: the managed block
// between its markers (markers excluded, bytes as written) or the whole
// include file. exists is false when there is none yet.
func readManagedContent(l Layout) (content string, exists bool, err error) {
	path, block, err := managedConfigPath(l)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("baca %s: %w", path, err)
	}
	if !block {
		return string(b), true, nil
	}
	lines := strings.SplitAfter(string(b), "\n")
	begin := -1
	for i, ln := range lines {
		switch strings.TrimSpace(ln) {
		case blockBegin:
			if begin < 0 {
				begin = i
			}
		case blockEnd:
			if begin >= 0 {
				return strings.Join(lines[begin+1:i], ""), true, nil
			}
		}
	}
	if begin < 0 {
		return "", false, nil
	}
	return strings.Join(lines[begin+1:], ""), true, nil
}

// optionLine splits an option-file line into its option name (server
// variable form: lower case, underscores) and value (quotes and a
// trailing comment removed); ok is false for blank, comment and group
// lines.
func optionLine(ln string) (name, value string, ok bool) {
	ln = strings.TrimSpace(ln)
	if ln == "" || ln[0] == '#' || ln[0] == ';' || ln[0] == '[' || ln[0] == '!' {
		return "", "", false
	}
	k, v, _ := strings.Cut(ln, "=")
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "-", "_")
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	} else if i := strings.Index(v, "#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return name, v, true
}

// isStartLockLine reports whether ln is an init_file option naming path.
func isStartLockLine(ln, path string) bool {
	name, value, ok := optionLine(ln)
	return ok && name == "init_file" && value == path
}

// agentStartLockPaths are the files named by the agent's init_file lines
// in content: every init_file whose file name is StartLockFile, wherever
// it is (a line written under an earlier layout too), each once.
func agentStartLockPaths(content string) []string {
	var out []string
	for _, ln := range strings.Split(content, "\n") {
		if p, ok := agentStartLockLine(ln); ok && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// agentStartLockLine returns the file of ln when ln is an init_file option
// naming a file called StartLockFile.
func agentStartLockLine(ln string) (string, bool) {
	name, value, ok := optionLine(ln)
	if !ok || name != "init_file" || filepath.Base(value) != StartLockFile {
		return "", false
	}
	return value, true
}

// hasStartLockLine reports whether content holds an init_file line
// naming path.
func hasStartLockLine(content, path string) bool {
	for _, ln := range strings.Split(content, "\n") {
		if isStartLockLine(ln, path) {
			return true
		}
	}
	return false
}

// withStartLockLine is content with the line init_file=<path>: an
// existing line naming path is rewritten in that form where it stands,
// else the line is added at the end (under a [mysqld] group of its own
// when the last group is not mysqld's). Every other line stays as it is,
// byte for byte; an init_file naming another file is never touched.
func withStartLockLine(content, path string) string {
	line := "init_file=" + path
	lines := strings.SplitAfter(content, "\n")
	found := false
	for i, ln := range lines {
		if !found && isStartLockLine(ln, path) {
			nl := ""
			if strings.HasSuffix(ln, "\n") {
				nl = "\n"
			}
			lines[i], found = line+nl, true
		}
	}
	if found {
		return strings.Join(lines, "")
	}
	group := ""
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "[") {
			group = strings.ToLower(strings.ReplaceAll(t, " ", ""))
		}
	}
	if group != "[mysqld]" && group != "[server]" {
		line = "[mysqld]\n" + line
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		return content + "\n" + line
	}
	return content + line + "\n"
}

// withoutStartLockLine is content without its init_file lines naming
// path; every other line stays as it is, byte for byte.
func withoutStartLockLine(content, path string) string {
	lines := strings.SplitAfter(content, "\n")
	var b strings.Builder
	for i, ln := range lines {
		if !isStartLockLine(ln, path) {
			b.WriteString(ln)
			continue
		}
		// A last line without a newline: the line before it loses the
		// newline added in front of it.
		if i == len(lines)-1 && !strings.HasSuffix(ln, "\n") {
			s := strings.TrimSuffix(b.String(), "\n")
			b.Reset()
			b.WriteString(s)
		}
	}
	return b.String()
}

// startLockLinePresent reports whether the managed config holds the
// agent's init_file line.
func startLockLinePresent(l Layout) (bool, error) {
	path, err := StartLockPath(l)
	if err != nil {
		return false, err
	}
	content, _, err := readManagedContent(l)
	if err != nil {
		return false, err
	}
	return hasStartLockLine(content, path), nil
}

// writeStartLock writes the start lock: the file (root-owned, 0644) and
// then, when the managed config does not hold it yet, the init_file line
// (validated by mysqld, rolled back otherwise). A failure at any step
// leaves no line without its file. Idempotent.
func writeStartLock(ctx context.Context, h Host) error {
	l := h.Layout()
	path, err := StartLockPath(l)
	if err != nil {
		return err
	}
	unlock, err := lockManagedConfig(ctx, l)
	if err != nil {
		return err
	}
	defer unlock()
	if err := writeStartLockFile(path); err != nil {
		return fmt.Errorf("berkas kunci start: %w", err)
	}
	content, _, err := readManagedContent(l)
	if err != nil {
		return err
	}
	if hasStartLockLine(content, path) {
		return nil
	}
	if _, err := writeValidConfigLocked(ctx, h, withStartLockLine(content, path)); err != nil {
		return fmt.Errorf("baris init_file kunci start: %w", err)
	}
	return nil
}

// writeStartLockFile makes path the start lock file: startLockContent,
// owned by root (when the agent is root) with mode 0644. A regular file
// that already holds that content is not rewritten, only its owner and
// mode corrected, so its mtime still says when the lock was written
// (reportQuarantineStart compares it with mysqld's start).
func writeStartLockFile(path string) error {
	root := os.Geteuid() == 0
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		if b, err := os.ReadFile(path); err == nil && string(b) == startLockContent {
			if uid, gid := fileOwnerIDs(fi); root && (uid != 0 || gid != 0) {
				if err := os.Lchown(path, 0, 0); err != nil {
					return err
				}
			}
			if fi.Mode().Perm() != 0o644 {
				return os.Chmod(path, 0o644)
			}
			return nil
		}
	}
	// writeFileAtomic keeps an existing file's owner: make it root's first.
	if fi, err := os.Lstat(path); root && err == nil && fi.Mode().IsRegular() {
		if err := os.Chown(path, 0, 0); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(path, []byte(startLockContent), 0o644); err != nil {
		return err
	}
	if root {
		if err := os.Lchown(path, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

// effectiveOpts is what mysqld --print-defaults says mysqld reads: every
// init_file in order (the last one is used), the user it runs as and its
// error log.
type effectiveOpts struct {
	InitFiles []string
	User      string
	LogError  string
}

// printDefaultsArgs are mysqld's arguments for --print-defaults. With any
// other option among them mysqld starts instead of printing, so there are
// never others.
func printDefaultsArgs(l Layout) []string {
	if len(l.ConfigFiles) == 1 && l.ConfigFile != "" {
		return []string{"--defaults-file=" + l.ConfigFile, "--print-defaults"}
	}
	return []string{"--print-defaults"}
}

// readEffectiveOptions runs mysqld --print-defaults (which reads the
// option files only, never the data directory) and parses its answer.
// User is the last --user, else mysql when the agent runs as root, else
// the agent's own user. --print-defaults runs as the agent (root), so it
// may read option files mysqld's own user could not; that can only add
// init_file entries after the agent's line, which makes the start lock
// look broken, never held: the result is conservative.
func readEffectiveOptions(ctx context.Context, h Host) (effectiveOpts, error) {
	var o effectiveOpts
	l := h.Layout()
	if l.MysqldPath == "" {
		return o, errors.New("mysqld tidak diketahui")
	}
	ctx, cancel := context.WithTimeout(ctx, printDefaultsTimeout)
	defer cancel()
	var out bytes.Buffer
	if err := h.Exec(ctx, nil, &out, l.MysqldPath, printDefaultsArgs(l)...); err != nil {
		return o, err
	}
	head, rest, ok := strings.Cut(out.String(), "\n")
	if !ok || !strings.Contains(head, "would have been started with the following arguments") {
		return o, fmt.Errorf("keluaran --print-defaults tidak dikenal: %.200q", out.String())
	}
	// Values are printed as they are, spaces included: a word that does
	// not start an option continues the one before.
	var args []string
	for _, w := range strings.Fields(rest) {
		if strings.HasPrefix(w, "--") || len(args) == 0 {
			args = append(args, w)
		} else {
			args[len(args)-1] += " " + w
		}
	}
	for _, a := range args {
		k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch strings.ReplaceAll(k, "-", "_") {
		case "init_file":
			o.InitFiles = append(o.InitFiles, v)
		case "user":
			o.User = v
		case "log_error":
			o.LogError = v
		}
	}
	if o.User == "" {
		if os.Geteuid() == 0 {
			o.User = "mysql"
		} else if u, err := user.Current(); err == nil {
			o.User = u.Username
		}
	}
	return o, nil
}

// macConfinement is the mandatory access control mysqld runs under:
// "selinux" (enforcing), "apparmor" (a mysqld profile in enforce mode)
// or "". Tests replace it.
var macConfinement = func(l Layout) string {
	if b, err := os.ReadFile("/sys/fs/selinux/enforce"); err == nil && strings.TrimSpace(string(b)) == "1" {
		return "selinux"
	}
	b, err := os.ReadFile("/sys/kernel/security/apparmor/profiles")
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		name, mode, ok := strings.Cut(strings.TrimSpace(ln), " ")
		if !ok || !strings.Contains(mode, "enforce") {
			continue
		}
		if name == l.MysqldPath || name == "mysqld" || filepath.Base(name) == "mysqld" {
			return "apparmor"
		}
	}
	return ""
}

// readXattr reads the extended attribute name of path. Tests replace it.
var readXattr = func(path, name string) (string, error) {
	buf := make([]byte, 256)
	for {
		n, err := syscall.Getxattr(path, name, buf)
		if errors.Is(err, syscall.ERANGE) && len(buf) < 64<<10 {
			buf = make([]byte, 2*len(buf))
			continue
		}
		if err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
}

// selinuxLockTypes are the SELinux file types mysqld_t reads a start lock
// file from.
var selinuxLockTypes = map[string]bool{"mysqld_etc_t": true}

// macAllows reports whether mysqld under kind ("apparmor", "selinux" or
// "") can be shown to read the file at path: the stock AppArmor profile
// reads /etc/mysql/, SELinux's mysqld_t reads files labelled
// mysqld_etc_t. Anything unproven is false.
func macAllows(kind, path string) bool {
	switch kind {
	case "":
		return true
	case "apparmor":
		return strings.HasPrefix(filepath.Clean(path), "/etc/mysql/")
	case "selinux":
		label, err := readXattr(path, "security.selinux")
		if err != nil {
			return false
		}
		parts := strings.Split(strings.TrimRight(label, "\x00"), ":")
		return len(parts) >= 3 && selinuxLockTypes[parts[2]]
	}
	return false
}

// readableBy reports whether the account name can read path by its mode
// bits: read on the file, search on every directory above it, each for
// the class (owner, group, other) the account falls in. ACLs and MAC are
// not considered.
func readableBy(path, name string) (bool, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return false, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return false, err
	}
	if uid == 0 {
		return true, nil
	}
	groups := map[uint32]bool{}
	if g, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
		groups[uint32(g)] = true
	}
	if ids, err := u.GroupIds(); err == nil {
		for _, id := range ids {
			if g, err := strconv.ParseUint(id, 10, 32); err == nil {
				groups[uint32(g)] = true
			}
		}
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	allowed := func(p string, bit os.FileMode) (bool, error) {
		fi, err := os.Stat(p)
		if err != nil {
			return false, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return false, fmt.Errorf("pemilik %s tidak terbaca", p)
		}
		perm := fi.Mode().Perm()
		switch {
		case st.Uid == uint32(uid):
			return perm&(bit<<6) != 0, nil
		case groups[st.Gid]:
			return perm&(bit<<3) != 0, nil
		}
		return perm&bit != 0, nil
	}
	if ok, err := allowed(real, 4); !ok || err != nil {
		return false, err
	}
	for d := filepath.Dir(real); ; d = filepath.Dir(d) {
		if ok, err := allowed(d, 1); !ok || err != nil {
			return false, err
		}
		if d == "/" || d == "." {
			return true, nil
		}
	}
}

// startLockStatus checks whether the start lock holds for the next start
// of MySQL: the agent's line in the managed config, its file with exactly
// startLockContent, that file the last init_file mysqld reads
// (--print-defaults, run as the agent: see readEffectiveOptions), readable
// by mysqld's user and, under AppArmor or SELinux, in a place its profile
// allows.
func startLockStatus(ctx context.Context, h Host) proto.StartLockState {
	var st proto.StartLockState
	l := h.Layout()
	path, err := StartLockPath(l)
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	st.File = path
	content, _, err := readManagedContent(l)
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	if st.Present = hasStartLockLine(content, path); !st.Present {
		st.Reason = "baris init_file belum ada di konfigurasi MySQL"
		return st
	}
	if b, err := os.ReadFile(path); err != nil {
		st.Reason = fmt.Sprintf("berkas kunci %s tidak ada", path)
		return st
	} else if string(b) != startLockContent {
		st.Reason = fmt.Sprintf("isi berkas kunci %s bukan isi kunci start", path)
		return st
	}
	opts, err := readEffectiveOptions(ctx, h)
	if err != nil {
		st.Reason = fmt.Sprintf("konfigurasi efektif MySQL tidak bisa dibaca: %v", err)
		return st
	}
	last := -1
	for i, f := range opts.InitFiles {
		if f == path {
			last = i
		}
	}
	for _, f := range opts.InitFiles[:max(last, 0)] {
		if f != path {
			st.OperatorInitFiles = append(st.OperatorInitFiles, f)
		}
	}
	switch {
	case last < 0:
		st.Reason = fmt.Sprintf("baris init_file agent tidak dibaca mysqld (--print-defaults tidak memuat %s)", path)
		return st
	case last != len(opts.InitFiles)-1:
		st.Reason = "init_file lain dibaca sesudah baris agent: " + opts.InitFiles[len(opts.InitFiles)-1]
		return st
	}
	if ok, err := readableBy(path, opts.User); err != nil || !ok {
		st.Reason = "berkas kunci tidak bisa dibaca pengguna " + opts.User
		if err != nil {
			st.Reason += ": " + err.Error()
		}
		return st
	}
	mac := macConfinement(l)
	st.MACConfined = mac != ""
	if !macAllows(mac, path) {
		st.Reason = fmt.Sprintf("berkas kunci %s mungkin ditolak profil AppArmor/SELinux mysqld", path)
		return st
	}
	st.Locked = true
	return st
}

// setStartLockState records st as the start lock's last known state.
func (d *Driver) setStartLockState(st proto.StartLockState) {
	d.startLockMu.Lock()
	d.startLockState, d.startLockSet = st, true
	d.startLockMu.Unlock()
}

// clearStartLockState records that there is no start lock to report.
func (d *Driver) clearStartLockState() {
	d.startLockMu.Lock()
	d.startLockState, d.startLockSet = proto.StartLockState{}, false
	d.startLockMu.Unlock()
}

// StartLock is the start lock as last checked, for the health reports;
// nil when there is none to report (not quarantined, no line left).
func (d *Driver) StartLock() *proto.StartLockState {
	d.startLockMu.Lock()
	defer d.startLockMu.Unlock()
	if !d.startLockSet {
		return nil
	}
	st := d.startLockState
	st.OperatorInitFiles = slices.Clone(st.OperatorInitFiles)
	return &st
}

// lockStart writes the start lock and checks it, for a quarantine: a
// write that fails is logged and makes the state say so; it never undoes
// the quarantine.
func (d *Driver) lockStart(ctx context.Context, h Host) proto.StartLockState {
	werr := writeStartLock(ctx, h)
	st := startLockStatus(ctx, h)
	if werr != nil {
		d.warn("start lock not written", "err", werr)
		if !st.Locked {
			st.Reason += " (kunci start gagal ditulis: " + werr.Error() + ")"
		}
	}
	d.setStartLockState(st)
	if !st.Locked {
		d.warn("start lock does not hold", "reason", st.Reason)
	}
	return st
}

// withStartLock is res carrying st.
func withStartLock(res proto.QuarantineResult, st proto.StartLockState) proto.QuarantineResult {
	res.StartLocked, res.StartLockReason = st.Locked, st.Reason
	res.MACConfined, res.OperatorInitFiles = st.MACConfined, st.OperatorInitFiles
	return res
}

// fileOwnerIDs is the owner of fi; -1 when the platform does not say.
func fileOwnerIDs(fi os.FileInfo) (uid, gid int) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}
