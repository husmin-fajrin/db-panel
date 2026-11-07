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
	"time"

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
	// RoleFile holds master, replica, quarantined or unassigned; no file
	// is unassigned.
	RoleFile = "role"
	// SourceFile holds the host:port a replica follows; no file is no pin.
	SourceFile = "source"
	// SourceAddrFile, beside a source pin whose host is a name, holds the
	// address that name was verified at when the agent pinned it (a
	// quarantine): the agent's own reads of the pinned source dial it
	// (Pins.SourceDial). Rebuild commands match the pin by name.
	SourceAddrFile = "source-addr"
)

const (
	roleUnassigned = "unassigned"
	// roleQuarantined is the old master after a promotion (spec 5.1): it
	// is refused what a replica is, plus the promotion commands, until it
	// is rebuilt from its pinned source (the new master) or released.
	roleQuarantined = "quarantined"
	// dumpUserPrefix starts the per-job dump accounts (mysql.createDumpUser),
	// the only accounts a pinned master lets the panel drop.
	dumpUserPrefix = managedUserPrefix + "dump_"
	// replUserName is the replication account, the only one
	// mysql.createReplUser makes on a pinned master, replica or
	// quarantined server.
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
// rules in refusedByRole (configure's role, setReadOnly's direction, on a
// master every dropUser but a per-job dump account's and every
// createDumpUser but a per-job dump account from one IP address, and on a
// master, replica or quarantined server every createReplUser but the
// replication account's, mysql.promote and mysql.repoint anywhere but on
// a replica, which Allow also refuses without a source pin,
// mysql.quarantine anywhere but on a master or a quarantined server, and
// mysql.leftover and the salvage commands anywhere but on a quarantined
// server). On a master, an existing account is never
// changed (createAccount), mysql.restart needs a configure that asked for
// it (Pins.AllowRestart), and setReadOnly{off} is refused while the
// promote lock exists (Pins.Allow). A quarantined server refuses what a
// replica does plus the promotion commands (init below). An unassigned
// server refuses nothing extra (refuseOnSource still guards it).
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
	roleReplica: {},
	roleQuarantined: {
		// The old master never takes part in a promotion again before it
		// is rebuilt.
		"mysql.lockForPromote": true,
		"mysql.promote":        true,
		"mysql.repoint":        true,
	},
}

func init() {
	// A quarantined server inherits every fixed refusal of a replica.
	for cmd := range roleRefuses[roleReplica] {
		roleRefuses[roleQuarantined][cmd] = true
	}
}

// replicaLike reports whether role takes a replica's parameter-dependent
// rules: a replica, or a quarantined server.
func replicaLike(role string) bool { return role == roleReplica || role == roleQuarantined }

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

// unsourcedQuarantineRefusal is a rebuild command on a quarantined server
// without a source pin: it may be rebuilt only from its new master.
func unsourcedQuarantineRefusal(cmd string) error {
	return &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"server ini dipin sebagai quarantined tanpa sumber yang dipin; perintah %s ditolak. Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>",
		cmd)}}
}

// lockRefusal is setReadOnly{off} on a master under the promote lock.
func lockRefusal(l PromoteLock) error {
	return &pinError{kind: ErrRolePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
		"server ini dikunci untuk promosi (kandidat %s, sampai %s); perintah mysql.setReadOnly{off} ditolak. Untuk membuka kunci jalankan di server ini: sudo dbpanel agent unlock-master",
		l.Candidate, l.Deadline.UTC().Format(time.RFC3339))}}
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
	role      string // master, replica, quarantined or unassigned
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
			return p.fail(fmt.Errorf("%s tidak valid: %q (harus master, replica, quarantined atau unassigned); perbaiki dengan: sudo dbpanel agent set-role <peran>",
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
	return readTrustedFile(path, maxPinSize)
}

// readTrustedFile is readPinFile for a file of at most max bytes.
func readTrustedFile(path string, max int) (content string, ok bool, err error) {
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
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return "", false, fmt.Errorf("baca %s: %w", path, err)
	}
	if len(b) > max {
		return "", false, fmt.Errorf("%s terlalu besar", path)
	}
	return strings.TrimSpace(string(b)), true, nil
}

func validRole(r string) bool {
	return r == roleMaster || r == roleReplica || r == roleQuarantined || r == roleUnassigned
}

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

// Role is the pinned role: master, replica, quarantined or unassigned; ""
// when the files cannot be read.
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
// an unassigned server (claimRole) or a rebuilt quarantined one
// (settleRebuilt). It does not need the current files to
// be valid, so it also repairs them.
func (p *Pins) SetRole(role string) error {
	if !validRole(role) {
		return fmt.Errorf("peran harus master, replica, quarantined atau unassigned: %q", role)
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
	// The verified address belonged to the old source.
	if err := removeFile(p.path(SourceAddrFile)); err != nil {
		return err
	}
	return writePin(p.path(SourceFile), src)
}

// SetQuarantined pins this server as quarantined following newSource
// (host:port), from a master or a replica: the old master after a
// promotion, which only the new master (newSource) may rebuild.
func (p *Pins) SetQuarantined(newSource string) error { return p.SetQuarantinedAt(newSource, "") }

// SetQuarantinedAt is SetQuarantined that also records addr, the address
// the new master's name was verified at, in SourceAddrFile ("" removes
// it). The address is written before the source and the role.
func (p *Pins) SetQuarantinedAt(newSource, addr string) error {
	host, port, err := ParseSource(newSource)
	if err != nil {
		return err
	}
	if addr != "" && net.ParseIP(addr) == nil {
		return fmt.Errorf("alamat sumber tidak valid: %q", addr)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.forget()
	p.settled = true
	if addr == "" || strings.EqualFold(addr, host) {
		err = removeFile(p.path(SourceAddrFile))
	} else {
		err = writePin(p.path(SourceAddrFile), addr)
	}
	if err != nil {
		return err
	}
	// The source first: a failure in between leaves the old role with a
	// source pin (harmless on a master, which refuses every command that
	// names a source), never a quarantined server without one.
	if err := writePin(p.path(SourceFile), net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
		return err
	}
	return writePin(p.path(RoleFile), roleQuarantined)
}

// PromoteToMaster pins this server as master and removes its source pin:
// the candidate at the end of a promotion.
func (p *Pins) PromoteToMaster() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.forget()
	p.settled = true
	if err := writePin(p.path(RoleFile), roleMaster); err != nil {
		return err
	}
	for _, name := range []string{SourceFile, SourceAddrFile} {
		if err := removeFile(p.path(name)); err != nil {
			return err
		}
	}
	return nil
}

// SourceDial is the pinned source as the agent itself reads it: the host
// and port of the pin, and the address to dial, the recorded verified
// address (SourceAddrFile) when there is one, else the host. ok is false
// without a source pin.
func (p *Pins) SourceDial() (host string, port int, dial string, ok bool, err error) {
	host, port, ok = p.Source()
	if !ok {
		return "", 0, "", false, nil
	}
	addr, has, err := readPinFile(p.path(SourceAddrFile))
	if err != nil {
		return "", 0, "", false, err
	}
	if !has {
		return host, port, host, true, nil
	}
	if net.ParseIP(addr) == nil {
		return "", 0, "", false, fmt.Errorf("%s tidak valid: %q; perbaiki dengan: sudo dbpanel agent set-source <host:port>", p.path(SourceAddrFile), addr)
	}
	return host, port, addr, true, nil
}

// removeFile removes path; none is not an error.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hapus %s: %w", path, err)
	}
	return nil
}

// Release turns a replica, a quarantined or an unassigned server into an
// unassigned one by writing role=unassigned. The source pin stays (spec
// 5.10): a released server still follows only its pinned source until
// root runs set-source. A master is refused: only the local CLI changes
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
	p.settled = true
	return writePin(p.path(RoleFile), roleUnassigned)
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
// this process has seen a role file: an existing pin (quarantined
// included) is never changed from MySQL's state. The promotion record is
// read for the log only.
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
		if pr, ok, err := p.ReadPromoted(); err == nil && ok {
			p.logger.Info("this server was promoted to master before", "at", pr.At, "mode", pr.Mode, "old_source", pr.OldSource)
		}
	}
	return nil
}

// claimRole pins role after a successful mysql.configure when no role is
// pinned. An existing pin is never changed: claiming it again is a no-op,
// as is claiming replica on a quarantined server (its rebuild, which
// settleRebuilt completes once replication points at the pinned source),
// and claiming another role is an ErrRolePinned error.
func (p *Pins) claimRole(role string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return err
	}
	p.settled = true
	switch {
	case p.role == role, p.role == roleQuarantined && role == roleReplica:
		return nil
	case p.role == roleUnassigned:
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

// settleRebuilt ends the quarantine of a rebuilt old master (spec 5.1: a
// successful rebuild turns the pin into replica): after a successful
// mysql.configureReplication toward host:port on a server pinned
// quarantined whose source pin is host:port (matched like Allow matches
// the rebuild commands, by name), it rewrites the role pin to replica and
// reports true. It changes nothing on any other role (a master included),
// without a source pin, or toward any other source. Beside releaseRole it
// is the only way out of quarantined a panel command takes. The source
// pin and its verified address (SourceAddrFile) stay: the replica follows
// the same new master and the agent's own reads of it (repoint, promote)
// keep dialing that address.
func (p *Pins) settleRebuilt(host string, port int) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.reload(); err != nil {
		return false, err
	}
	if p.role != roleQuarantined || !p.hasSrc || !p.sameSource(host, port) {
		return false, nil
	}
	defer p.forget()
	p.settled = true
	if err := writePin(p.path(RoleFile), roleReplica); err != nil {
		return false, err
	}
	if p.logger != nil {
		p.logger.Info("role pinned", "role", roleReplica, "file", p.path(RoleFile), "was", roleQuarantined,
			"source", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return true, nil
}

// quarantinedUnder reports whether the role pin is quarantined and
// host:port is its source pin.
func (p *Pins) quarantinedUnder(host string, port int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reload() == nil && p.role == roleQuarantined && p.hasSrc && p.sameSource(host, port)
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
	// Under the promote lock a master stays read-only (spec 5.1).
	if ro, ok := params.(proto.ReadOnlyParams); ok && p.role == roleMaster && cmd == "mysql.setReadOnly" && !ro.On {
		l, locked, err := p.ReadLock()
		if err != nil {
			return &commands.ParamError{Msg: fmt.Sprintf("kunci promosi tidak bisa dibaca, perintah %s ditolak: %v", cmd, err)}
		}
		if locked {
			return lockRefusal(l)
		}
	}
	host, port, names, err := sourceOf(cmd, params)
	if err != nil {
		return err
	}
	// A quarantined server without a source pin (an interrupted
	// quarantine, or set-role without set-source) is never wiped or
	// pointed anywhere: there is no pinned new master to rebuild from.
	if p.role == roleQuarantined && !p.hasSrc && (names || cmd == "mysql.dropDatabases") {
		return unsourcedQuarantineRefusal(cmd)
	}
	// A replica is promoted, or pointed at the promoted replica of its
	// source, only from the source it is pinned to follow.
	if (cmd == "mysql.promote" || cmd == "mysql.repoint") && !p.hasSrc {
		return &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
			"server ini dipin sebagai replica tanpa sumber yang dipin; perintah %s ditolak. Pin sumbernya dulu di server ini: sudo dbpanel agent set-source <host:port>",
			cmd)}}
	}
	if !names || !p.hasSrc || p.sameSource(host, port) {
		return nil
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
	case "mysql.promote":
		v, ok := params.(proto.PromoteParams)
		if !ok {
			return "", 0, false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return v.OldSource.Host, v.OldSource.Port, true, nil
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
	case "mysql.promote", "mysql.repoint":
		// Only a replica is promoted or pointed at a new master (a
		// quarantined one is refused above).
		return role != roleReplica, nil
	case "mysql.leftover", "mysql.salvage", "mysql.salvageList", "mysql.salvageRead", "mysql.salvageDelete":
		// What the old master holds that its new master lacks is read
		// and extracted only on the old master.
		return role != roleQuarantined, nil
	case "mysql.quarantine":
		// The old master quarantines itself; a quarantined server answers
		// it again (idempotent).
		return role != roleMaster && role != roleQuarantined, nil
	case "mysql.configure":
		c, ok := params.(proto.ConfigureParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return (role == roleMaster && c.Role == roleReplica) || (replicaLike(role) && c.Role == roleMaster), nil
	case "mysql.setReadOnly":
		r, ok := params.(proto.ReadOnlyParams)
		if !ok {
			return false, fmt.Errorf("pins: %s with %T", cmd, params)
		}
		return (role == roleMaster && r.On) || (replicaLike(role) && !r.On), nil
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
		// Only dbpanel_repl@'%' (what preparing the master sends): a row at
		// a more specific host would shadow it for the replicas it matches
		// and hand its own password REPLICATION SLAVE and BACKUP_ADMIN.
		// createAccount refuses it once any dbpanel_repl row exists. A
		// replica or quarantined server (a promotion candidate preparing
		// for its own replicas) takes the same rule.
		return strictReplUser(role) && !(u.User == replUserName && u.FromHost == "%"), nil
	}
	return false, nil
}

// strictReplUser reports whether role makes only dbpanel_repl@'%', once.
func strictReplUser(role string) bool { return role == roleMaster || replicaLike(role) }

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
	for _, name := range []string{RoleFile, SourceFile, SourceAddrFile} {
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
	// The rename survives a crash only once the directory is synced.
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fail(err)
	}
	return nil
}

// syncDir fsyncs dir, so a rename or removal in it is on disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
