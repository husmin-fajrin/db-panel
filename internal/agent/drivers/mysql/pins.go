package mysql

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Pinned role and source (spec 5.5, SEC-28 items 1 and 3). Two files in
// the agent's directory say what this server is, and only the agent's
// own first configuration or root on the host (dbpanel agent set-role,
// set-source) writes them: a compromised panel can therefore never turn a
// production master into a replica, drop or replace its data, or point a
// replica at a source of its choosing.
const (
	// RoleFile holds master, replica or unassigned; no file is unassigned.
	RoleFile = "role"
	// SourceFile holds the host:port a replica follows; no file is no pin.
	SourceFile = "source"
)

const (
	roleUnassigned = "unassigned"
	// dumpUserPrefix starts the per-job dump accounts (mysql.createDumpUser),
	// the only accounts a pinned master lets the panel drop.
	dumpUserPrefix = managedUserPrefix + "dump_"
	// replUserName is the replication account, the only one
	// mysql.createReplUser makes on a pinned master.
	replUserName = managedUserPrefix + "repl"
	// pinFileMode is the mode the agent writes pins with.
	pinFileMode = 0o644
	// maxPinSize bounds what is read from a pin file.
	maxPinSize = 512
)

// ErrRolePinned and ErrSourcePinned are what a refusal by the pins wraps,
// beside the commands.ParamError the panel shows.
var (
	ErrRolePinned   = errors.New("peran server ini dipin")
	ErrSourcePinned = errors.New("sumber replikasi server ini dipin")
)

// dumpAccountRe is the name of a per-job dump account the panel makes
// (dbpanel_dump_ and 8 hex digits): the only one a pinned master creates
// through mysql.createDumpUser.
var dumpAccountRe = regexp.MustCompile(`^dbpanel_dump_[0-9a-f]{8}$`)

// roleRefuses is the refusal table of spec 5.5: per pinned role, the
// commands refused whatever their parameters, plus the parameter-dependent
// rules in refusedByRole (configure's role, setReadOnly's direction, and on
// a master every dropUser but a per-job dump account's, every
// createDumpUser but a per-job dump account from one IP address, and every
// createReplUser but the replication account's). On a master, an existing
// account is never changed (createAccount) and mysql.restart needs a
// configure that asked for it (Pins.AllowRestart). An unassigned server
// refuses nothing extra (refuseOnSource still guards it).
var roleRefuses = map[string]map[string]bool{
	roleMaster: {
		"mysql.dropDatabases":        true,
		"mysql.dumpFrom":             true,
		"mysql.cloneFrom":            true,
		"mysql.configureReplication": true,
		"mysql.setReplicationFilter": true,
		"mysql.resetReplica":         true,
		"mysql.startReplica":         true,
		"mysql.startUntil":           true,
		// A master is released only through the local CLI.
		"mysql.releaseRole": true,
	},
	roleReplica: {
		"mysql.createReplUser": true,
	},
}

// pinError is a refusal by the pins: a ParamError for the panel that also
// matches ErrRolePinned or ErrSourcePinned.
type pinError struct {
	pe   *commands.ParamError
	kind error
}

func (e *pinError) Error() string   { return e.pe.Msg }
func (e *pinError) Unwrap() []error { return []error{e.pe, e.kind} }

func roleRefusal(role, cmd string) error {
	return &pinError{kind: ErrRolePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"server ini dipin sebagai %s; perintah %s ditolak. Untuk mengubah peran jalankan di server ini: sudo dbpanel agent set-role <peran>",
		role, cmd)}}
}

// sourceClaimRefusal is a claim of asked while pinned is the source: two
// first-time commands with different sources raced.
func sourceClaimRefusal(pinned, asked string) error {
	return &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"server ini sudah dipin mengikuti sumber %s; sumber %s tidak dipin. Untuk mengubah sumber jalankan di server ini: sudo dbpanel agent set-source <host:port>",
		pinned, asked)}}
}

func sourceRefusal(pinned, asked, cmd string) error {
	return &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"server ini dipin mengikuti sumber %s; perintah %s ke %s ditolak. Untuk mengubah sumber jalankan di server ini: sudo dbpanel agent set-source <host:port>",
		pinned, cmd, asked)}}
}

// Pins reads and writes the pin files in one directory. It re-reads them
// whenever one changed on disk (a cheap stat per check), so an edit by the
// CLI takes effect on the agent's next command. While a file cannot be
// trusted (wrong owner or mode, unknown content) every check fails.
type Pins struct {
	dir    string
	logger *slog.Logger

	mu        sync.Mutex
	loaded    bool // the fields below match roleStamp and srcStamp
	roleStamp fileStamp
	srcStamp  fileStamp
	role      string // master, replica or unassigned
	srcHost   string
	srcPort   int
	hasSrc    bool
	err       error // why the files could not be read; nil when loaded
	// settled is set once this process has seen or written a role file:
	// from then on Bootstrap never writes one.
	settled bool
	// restartGranted is set by a mysql.configure that needed a restart and
	// used up by the mysql.restart it allows on a pinned master.
	restartGranted bool
}

// fileStamp is what a stat says about a pin file; a change means re-read.
type fileStamp struct {
	exists  bool
	modNano int64
	size    int64
	mode    os.FileMode
	ino     uint64
	uid     uint32
}

func stampOf(path string) (fileStamp, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	s := fileStamp{exists: true, modNano: fi.ModTime().UnixNano(), size: fi.Size(), mode: fi.Mode()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.ino, s.uid = st.Ino, st.Uid
	}
	return s, nil
}

// LoadPins reads the pins in dir. It always returns a Pins: on an error
// that Pins refuses every check until the files are fixed.
func LoadPins(dir string) (*Pins, error) {
	p := &Pins{dir: dir}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p, p.reload()
}

func (p *Pins) path(name string) string { return filepath.Join(p.dir, name) }

// reload re-reads the files when a stat says one changed. The caller holds
// mu.
func (p *Pins) reload() error {
	rs, err := stampOf(p.path(RoleFile))
	if err != nil {
		return p.fail(fmt.Errorf("baca %s: %w", p.path(RoleFile), err))
	}
	ss, err := stampOf(p.path(SourceFile))
	if err != nil {
		return p.fail(fmt.Errorf("baca %s: %w", p.path(SourceFile), err))
	}
	if p.loaded && rs == p.roleStamp && ss == p.srcStamp {
		return nil
	}
	role := roleUnassigned
	content, ok, err := readPinFile(p.path(RoleFile))
	if err != nil {
		return p.fail(err)
	}
	if ok {
		if !validRole(content) {
			return p.fail(fmt.Errorf("%s tidak valid: %q (harus master, replica atau unassigned); perbaiki dengan: sudo dbpanel agent set-role <peran>",
				p.path(RoleFile), content))
		}
		role = content
	}
	var host string
	var port int
	content, hasSrc, err := readPinFile(p.path(SourceFile))
	if err != nil {
		return p.fail(err)
	}
	if hasSrc {
		if host, port, err = ParseSource(content); err != nil {
			return p.fail(fmt.Errorf("%s tidak valid: %v; perbaiki dengan: sudo dbpanel agent set-source <host:port>", p.path(SourceFile), err))
		}
	}
	p.role, p.srcHost, p.srcPort, p.hasSrc = role, host, port, hasSrc
	p.roleStamp, p.srcStamp, p.loaded, p.err = rs, ss, true, nil
	if rs.exists {
		p.settled = true
	}
	return nil
}

// fail records err; the next check reads the files again.
func (p *Pins) fail(err error) error {
	p.loaded, p.err = false, err
	return err
}

// readPinFile reads a pin's trimmed content, with the trust checks of the
// local policy file: a regular file owned by root (or the agent's user)
// that neither group nor others can write. ok is false when it is absent.
func readPinFile(path string) (content string, ok bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("baca %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s harus berkas biasa (bukan symlink atau direktori)", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "", false, fmt.Errorf("%s bisa ditulis grup atau pengguna lain (mode %04o); jalankan: chmod 0644 %s", path, fi.Mode().Perm(), path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return "", false, fmt.Errorf("%s harus milik root; jalankan: chown root:root %s", path, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("baca %s: %w", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPinSize+1))
	if err != nil {
		return "", false, fmt.Errorf("baca %s: %w", path, err)
	}
	if len(b) > maxPinSize {
		return "", false, fmt.Errorf("%s terlalu besar", path)
	}
	return strings.TrimSpace(string(b)), true, nil
}

func validRole(r string) bool { return r == roleMaster || r == roleReplica || r == roleUnassigned }

// ParseSource splits a pinned source, host:port with an IPv6 host in
// brackets, and validates both as the replication commands do.
func ParseSource(s string) (host string, port int, err error) {
	h, ps, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return "", 0, fmt.Errorf("sumber harus berbentuk HOST:PORT (IPv6 dalam kurung siku): %q", s)
	}
	port, err = strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port sumber harus antara 1 dan 65535: %q", ps)
	}
	if err := validSourceHost(h); err != nil {
		return "", 0, err
	}
	return h, port, nil
}

// validSourceHost is the host check of validateSource and validateSnapshot.
func validSourceHost(h string) error {
	if !sourceHostRe.MatchString(h) || (strings.Contains(h, ":") && net.ParseIP(h) == nil) {
		return fmt.Errorf("alamat sumber tidak valid: %q", h)
	}
	return nil
}

// Role is the pinned role: master, replica or unassigned; "" when the
// files cannot be read.
func (p *Pins) Role() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reload() != nil {
		return ""
	}
	return p.role
}

// Source is the pinned source; ok is false when there is none or the files
// cannot be read.
func (p *Pins) Source() (host string, port int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reload() != nil || !p.hasSrc {
		return "", 0, false
	}
	return p.srcHost, p.srcPort, true
}

// SetRole writes role, whatever is pinned now; unassigned removes the role
// and the source pin. It is the local CLI's: panel commands only ever pin
// an unassigned server (claimRole). It does not need the current files to
// be valid, so it also repairs them.
func (p *Pins) SetRole(role string) error {
	if !validRole(role) {
		return fmt.Errorf("peran harus master, replica atau unassigned: %q", role)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.forget()
	p.settled = true
	if role == roleUnassigned {
		return p.removePins()
	}
	return writePin(p.path(RoleFile), role)
}

// SetSource writes the source pin, whatever is pinned now (the local CLI).
func (p *Pins) SetSource(host string, port int) error {
	src := net.JoinHostPort(host, strconv.Itoa(port))
	if _, _, err := ParseSource(src); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.forget()
	return writePin(p.path(SourceFile), src)
}

// Release turns a replica (or an unassigned server) into an unassigned one
// without a source pin. A master is refused: only the local CLI changes
// it. mysql.releaseRole checks the replication state first.
func (p *Pins) Release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return err
	}
	if p.role == roleMaster {
		return roleRefusal(roleMaster, "mysql.releaseRole")
	}
	defer p.forget()
	return p.removePins()
}

// pinned reports whether a role other than unassigned, or a source, is
// pinned.
func (p *Pins) pinned() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return false, err
	}
	return p.role != roleUnassigned || p.hasSrc, nil
}

// Bootstrap pins the role of a server configured before pins existed, from
// a health reading and the panel's role hint (monitor.configure): replica
// when the hint says replica and MySQL has a replication source, master
// when the hint says master, the binary log is on and no replication
// source is configured. It writes at most once per process, and never once
// this process has seen a role file.
func (p *Pins) Bootstrap(hint string, snap proto.HealthSnapshot) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return err
	}
	if p.settled || !snap.MySQLOK {
		return nil
	}
	role := ""
	switch {
	case hint == roleReplica && snap.Replica != nil:
		role = roleReplica
	// Replicas often log too (log_replica_updates): a server with a
	// replication source is never pinned master, whatever the hint says.
	case hint == roleMaster && snap.LogBin && snap.Replica == nil:
		role = roleMaster
	default:
		return nil
	}
	defer p.forget()
	if err := writePin(p.path(RoleFile), role); err != nil {
		return err
	}
	p.settled = true
	if p.logger != nil {
		p.logger.Info("role pinned from the panel's hint and MySQL's state", "role", role, "file", p.path(RoleFile))
	}
	return nil
}

// claimRole pins role after a successful mysql.configure when no role is
// pinned. An existing pin is never changed: claiming it again is a no-op,
// claiming another role an ErrRolePinned error.
func (p *Pins) claimRole(role string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return err
	}
	p.settled = true
	switch p.role {
	case role:
		return nil
	case roleUnassigned:
	default:
		return roleRefusal(p.role, "mysql.configure")
	}
	defer p.forget()
	if err := writePin(p.path(RoleFile), role); err != nil {
		return err
	}
	if p.logger != nil {
		p.logger.Info("role pinned", "role", role, "file", p.path(RoleFile))
	}
	return nil
}

// claimSource pins host:port after a successful configureReplication,
// dumpFrom or cloneFrom when no source is pinned. An existing pin is never
// changed: claiming it again is a no-op, claiming another source an
// ErrSourcePinned error.
func (p *Pins) claimSource(host string, port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return err
	}
	src := net.JoinHostPort(host, strconv.Itoa(port))
	if p.hasSrc {
		if p.sameSource(host, port) {
			return nil
		}
		return sourceClaimRefusal(net.JoinHostPort(p.srcHost, strconv.Itoa(p.srcPort)), src)
	}
	defer p.forget()
	if err := writePin(p.path(SourceFile), src); err != nil {
		return err
	}
	if p.logger != nil {
		p.logger.Info("source pinned", "source", src, "file", p.path(SourceFile))
	}
	return nil
}

// Allow checks cmd with its parsed parameters against the pins: the role
// table of spec 5.5, then the pinned source for the commands that name a
// source. It refuses every command it is asked about while the files
// cannot be read.
func (p *Pins) Allow(cmd string, params any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return &commands.ParamError{Msg: fmt.Sprintf("pin peran/sumber agent tidak bisa dipakai, perintah %s ditolak: %v", cmd, err)}
	}
	refused, err := refusedByRole(p.role, cmd, params)
	if err != nil {
		return err
	}
	if refused {
		return roleRefusal(p.role, cmd)
	}
	host, port, names, err := sourceOf(cmd, params)
	if err != nil || !names || !p.hasSrc || p.sameSource(host, port) {
		return err
	}
	return sourceRefusal(net.JoinHostPort(p.srcHost, strconv.Itoa(p.srcPort)), net.JoinHostPort(host, strconv.Itoa(port)), cmd)
}

// sameSource reports whether host:port is the pinned source. The caller
// holds mu.
func (p *Pins) sameSource(host string, port int) bool {
	return strings.EqualFold(host, p.srcHost) && port == p.srcPort
}

// sourceOf is the source a command names: names is false for a command
// that names none, and parameters of another type than the command's are
// an error.
func sourceOf(cmd string, params any) (host string, port int, names bool, err error) {
	switch cmd {
	case "mysql.configureReplication":
		v, ok := params.(proto.ReplicationSourceParams)
		if !ok {
			return "", 0, false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return v.Host, v.Port, true, nil
	case "mysql.dumpFrom", "mysql.cloneFrom":
		v, ok := params.(proto.SnapshotParams)
		if !ok {
			return "", 0, false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return v.Host, v.Port, true, nil
	}
	return "", 0, false, nil
}

// refusedByRole applies the table to cmd: the fixed rows of roleRefuses
// and the rules that depend on mysql.configure's role,
// mysql.setReadOnly's direction and mysql.dropUser's account.
func refusedByRole(role, cmd string, params any) (bool, error) {
	if roleRefuses[role][cmd] {
		return true, nil
	}
	switch cmd {
	case "mysql.configure":
		c, ok := params.(proto.ConfigureParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return (role == roleMaster && c.Role == roleReplica) || (role == roleReplica && c.Role == roleMaster), nil
	case "mysql.setReadOnly":
		r, ok := params.(proto.ReadOnlyParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return (role == roleMaster && r.On) || (role == roleReplica && !r.On), nil
	case "mysql.dropUser":
		u, ok := params.(proto.DropUserParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		// The replication account (and any other) stays: dropping it would
		// cut every replica off.
		return role == roleMaster && !strings.HasPrefix(u.User, dumpUserPrefix), nil
	case "mysql.createDumpUser":
		u, ok := params.(proto.DumpUserParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		// A dump account holds RELOAD: on a master only a per-job one, from
		// one replica's address (no pattern such as % or _), is made.
		return role == roleMaster && !(dumpAccountRe.MatchString(u.User) && literalIP(u.FromHost)), nil
	case "mysql.createReplUser":
		u, ok := params.(proto.ReplUserParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return role == roleMaster && u.User != replUserName, nil
	}
	return false, nil
}

// literalIP reports whether host is one IPv4 or IPv6 address, not a name
// or a pattern.
func literalIP(host string) bool {
	return !strings.ContainsAny(host, "%_") && net.ParseIP(host) != nil
}

// GrantRestart records what the last mysql.configure in this process
// said: when it needs a restart, one mysql.restart is allowed on a pinned
// master (AllowRestart).
func (p *Pins) GrantRestart(needed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restartGranted = needed
}

// AllowRestart checks mysql.restart against the pins: a pinned master is
// restarted only once after a mysql.configure in this process that needed
// a restart, never at the panel's will (spec: a master is never restarted
// without a human's click, which the panel's job asks for). It uses up the
// grant.
func (p *Pins) AllowRestart() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return &commands.ParamError{Msg: fmt.Sprintf("pin peran/sumber agent tidak bisa dipakai, perintah mysql.restart ditolak: %v", err)}
	}
	if p.role != roleMaster {
		return nil
	}
	if !p.restartGranted {
		return &pinError{kind: ErrRolePinned, pe: &commands.ParamError{Msg: "server ini dipin sebagai master; perintah mysql.restart " +
			"ditolak karena tidak ada pengaturan yang menunggu restart. Bila MySQL memang perlu dimulai ulang, lakukan langsung di server ini."}}
	}
	p.restartGranted = false
	return nil
}

// RoleIs reports whether role is pinned; false while the files cannot be
// read.
func (p *Pins) RoleIs(role string) bool {
	return p != nil && p.Role() == role
}

// forget makes the next check read the files again (after a write).
func (p *Pins) forget() { p.loaded = false }

// removePins removes the role and the source pin.
func (p *Pins) removePins() error {
	for _, name := range []string{RoleFile, SourceFile} {
		if err := os.Remove(p.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hapus %s: %w", p.path(name), err)
		}
	}
	return nil
}

// writePin replaces path with value and a newline, atomically (temp file
// and rename), mode 0644, owned by the agent's user (root in production).
func writePin(path, value string) error {
	fail := func(err error) error { return fmt.Errorf("tulis %s: %w", path, err) }
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(pinFileMode); err != nil {
		f.Close()
		return fail(err)
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
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
