package mysql

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Files of a promotion (spec 5.1), beside the pins in the agent directory
// and written and read like them (writePin, readTrustedFile).
const (
	// LockFile is the promote lock: while it exists a pinned master is
	// not made writable from the panel (refusedByRole).
	LockFile = "promote-lock"
	// PromotedFile records the last promotion of this server to master.
	PromotedFile = "promoted"
	// PromotingFile marks a promotion about to open writes on this
	// replica: only with it does mysql.promote resume on a writable
	// server.
	PromotingFile = "promoting"
)

// SeenFile is where the agent records the replicas that read this
// server's binary log (Driver.SeenPath overrides it).
const SeenFile = "/var/lib/dbpanel/replicas-seen"

// SeenPortsFile is where the agent records the ports those replicas
// reported (Driver.SeenPortsPath overrides it), one "<port> <RFC3339>"
// line per port.
const SeenPortsFile = "/var/lib/dbpanel/replica-ports-seen"

const (
	// maxRecordSize bounds what is read from the lock and the promotion
	// record (a gtid_executed can be long).
	maxRecordSize = 64 << 10
	// maxSeenSize bounds what is read from the replicas-seen file.
	maxSeenSize = 1 << 20
	// seenDirMode is the mode of the directory RecordReplicasSeen creates.
	seenDirMode = 0o755
	// seenRefresh is how often RecordReplicas rewrites the file while the
	// same replicas stay connected, so their times (and the one-year
	// pruning) stay current.
	seenRefresh = 24 * time.Hour
)

// PromoteLock is the promote lock: the server stays read-only for the
// promotion of Candidate until Deadline. Candidate "cli" is the lock root
// set with "dbpanel agent lock-master" and means no candidate: nothing
// automatic (the guard that ends an expired promotion included) ever
// unlocks or quarantines a server under such a lock; only "dbpanel agent
// unlock-master" removes it.
//
// Addr is the candidate's address that lockForPromote matched against a
// Binlog Dump thread: every later read of the candidate (the guard,
// mysql.unlockPromote, mysql.quarantine) dials it, never the name in
// Candidate again (lockAddr).
//
// Readable lists, as host:port, the other replicas lockForPromote could
// read with the candidate's account when it set the lock
// (readableAtLock): the guard keeps an expired lock while one of them
// cannot be read. A lock without it (an older agent's) lists none. Only
// "dbpanel agent forget-replica" removes an entry (ForgetLockHost).
type PromoteLock struct {
	Candidate string
	Addr      string
	Deadline  time.Time
	Readable  []string
}

// lockAddr is the address the candidate of l is read at: Addr, else the
// host of Candidate when it is an IP address, else "" (no verified
// address: nothing reads the candidate).
func lockAddr(l PromoteLock) string {
	if l.Addr != "" {
		return l.Addr
	}
	if h, _, err := net.SplitHostPort(l.Candidate); err == nil && net.ParseIP(h) != nil {
		return h
	}
	return ""
}

// CLILockCandidate is the Candidate of a lock set by the local CLI.
const CLILockCandidate = "cli"

type lockJSON struct {
	Candidate string   `json:"candidate"`
	Addr      string   `json:"addr,omitempty"`
	Deadline  string   `json:"deadline"`
	Readable  []string `json:"readable,omitempty"`
}

// Promoted is the record of this server's promotion to master: when, how
// (Mode), the source it followed before, and its gtid_executed then.
type Promoted struct {
	At           time.Time
	Mode         string
	OldSource    string
	GTIDExecuted string
}

type promotedJSON struct {
	At           string `json:"at"`
	Mode         string `json:"mode"`
	OldSource    string `json:"old_source"`
	GTIDExecuted string `json:"gtid_executed"`
}

// Promoting is the marker of a promotion that passed its verification
// and is about to open writes (or did): its mode, the old master
// (host:port) and when.
type Promoting struct {
	Mode      string
	OldSource string
	At        time.Time
}

type promotingJSON struct {
	Mode      string `json:"mode"`
	OldSource string `json:"old_source"`
	At        string `json:"at"`
}

// WritePromoting writes the marker (synced to disk), replacing any.
func (p *Pins) WritePromoting(m Promoting) error {
	return writeJSON(p.path(PromotingFile), promotingJSON{Mode: m.Mode, OldSource: m.OldSource, At: m.At.UTC().Format(time.RFC3339)})
}

// ReadPromoting reads the marker; ok is false when there is none.
func (p *Pins) ReadPromoting() (Promoting, bool, error) {
	var v promotingJSON
	ok, err := readJSON(p.path(PromotingFile), &v)
	if err != nil || !ok {
		return Promoting{}, false, err
	}
	at, err := time.Parse(time.RFC3339, v.At)
	if err != nil || v.OldSource == "" {
		return Promoting{}, false, fmt.Errorf("%s tidak valid", p.path(PromotingFile))
	}
	return Promoting{Mode: v.Mode, OldSource: v.OldSource, At: at}, true, nil
}

// ClearPromoting removes the marker; none is not an error.
func (p *Pins) ClearPromoting() error {
	if err := os.Remove(p.path(PromotingFile)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hapus %s: %w", p.path(PromotingFile), err)
	}
	if err := syncDir(p.dir); err != nil {
		return fmt.Errorf("hapus %s: %w", p.path(PromotingFile), err)
	}
	return nil
}

// WriteLock writes the promote lock, replacing any.
func (p *Pins) WriteLock(l PromoteLock) error {
	if l.Candidate == "" || l.Deadline.IsZero() {
		return errors.New("kunci promosi butuh kandidat dan tenggat")
	}
	return writeJSON(p.path(LockFile), lockJSON{Candidate: l.Candidate, Addr: l.Addr, Deadline: l.Deadline.UTC().Format(time.RFC3339),
		Readable: l.Readable})
}

// ReadLock reads the promote lock; ok is false when there is none. A lock
// file that cannot be trusted or parsed is an error.
func (p *Pins) ReadLock() (PromoteLock, bool, error) {
	var v lockJSON
	ok, err := readJSON(p.path(LockFile), &v)
	if err != nil || !ok {
		return PromoteLock{}, false, err
	}
	d, err := time.Parse(time.RFC3339, v.Deadline)
	if err != nil || v.Candidate == "" || (v.Addr != "" && net.ParseIP(v.Addr) == nil) {
		return PromoteLock{}, false, fmt.Errorf("%s tidak valid; hapus dengan: sudo dbpanel agent unlock-master", p.path(LockFile))
	}
	return PromoteLock{Candidate: v.Candidate, Addr: v.Addr, Deadline: d, Readable: v.Readable}, true, nil
}

// ForgetLockHost removes every entry of host (any port; names compare
// case-insensitively) from the readable replicas of the promote lock and
// reports how many went. Without a lock, or an entry of host, nothing is
// written.
func (p *Pins) ForgetLockHost(host string) (removed int, err error) {
	l, ok, err := p.ReadLock()
	if err != nil || !ok {
		return 0, err
	}
	kept := slices.DeleteFunc(slices.Clone(l.Readable), func(e string) bool {
		h, _, err := net.SplitHostPort(e)
		return err == nil && strings.EqualFold(h, host)
	})
	removed = len(l.Readable) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	l.Readable = kept
	if err := p.WriteLock(l); err != nil {
		return 0, err
	}
	return removed, nil
}

// ClearLock removes the promote lock; no lock is not an error.
func (p *Pins) ClearLock() error {
	if err := os.Remove(p.path(LockFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hapus %s: %w", p.path(LockFile), err)
	}
	return nil
}

// WritePromoted writes the promotion record, replacing any.
func (p *Pins) WritePromoted(pr Promoted) error {
	return writeJSON(p.path(PromotedFile), promotedJSON{At: pr.At.UTC().Format(time.RFC3339), Mode: pr.Mode,
		OldSource: pr.OldSource, GTIDExecuted: pr.GTIDExecuted})
}

// ReadPromoted reads the promotion record; ok is false when there is none.
func (p *Pins) ReadPromoted() (Promoted, bool, error) {
	var v promotedJSON
	ok, err := readJSON(p.path(PromotedFile), &v)
	if err != nil || !ok {
		return Promoted{}, false, err
	}
	at, err := time.Parse(time.RFC3339, v.At)
	if err != nil {
		return Promoted{}, false, fmt.Errorf("%s tidak valid: %v", p.path(PromotedFile), err)
	}
	return Promoted{At: at, Mode: v.Mode, OldSource: v.OldSource, GTIDExecuted: v.GTIDExecuted}, true, nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writePin(path, string(b))
}

func readJSON(path string, v any) (bool, error) {
	content, ok, err := readTrustedFile(path, maxRecordSize)
	if err != nil || !ok {
		return false, err
	}
	if err := json.Unmarshal([]byte(content), v); err != nil {
		return false, fmt.Errorf("%s tidak valid: %v", path, err)
	}
	return true, nil
}

// seenCutoff is the oldest time a replica stays in the replicas-seen file
// without being seen again: a year before now.
func seenCutoff(now time.Time) time.Time { return now.AddDate(-1, 0, 0) }

// RecordReplicasSeen merges hosts, seen at now, into the replicas-seen
// file at path: one line per host, "<host> <RFC3339>", with each host's
// newest time. Entries older than a year are dropped. The file's
// directory is created when missing.
func RecordReplicasSeen(path string, hosts []string, now time.Time) error {
	seen, err := readSeen(path)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if !validSeenHost(h) {
			continue
		}
		if t, ok := seen[h]; !ok || now.After(t) {
			seen[h] = now
		}
	}
	cutoff := seenCutoff(now)
	maps.DeleteFunc(seen, func(_ string, t time.Time) bool { return t.Before(cutoff) })
	return writeSeen(path, seen)
}

// writeSeen writes seen to the replicas-seen file at path, one
// "<host> <RFC3339>" line per host, sorted, creating its directory when
// missing.
func writeSeen(path string, seen map[string]time.Time) error {
	var lines []string
	for _, h := range slices.Sorted(maps.Keys(seen)) {
		lines = append(lines, h+" "+seen[h].UTC().Format(time.RFC3339))
	}
	if err := os.MkdirAll(filepath.Dir(path), seenDirMode); err != nil {
		return fmt.Errorf("buat %s: %w", filepath.Dir(path), err)
	}
	return writePin(path, strings.Join(lines, "\n"))
}

// ForgetReplica removes host (names compare case-insensitively) from the
// replicas-seen file at path and reports whether it was there; the other
// hosts keep their times. Without the host nothing is written.
func ForgetReplica(seenPath, host string) (bool, error) {
	seen, err := readSeen(seenPath)
	if err != nil {
		return false, err
	}
	n := len(seen)
	maps.DeleteFunc(seen, func(h string, _ time.Time) bool { return strings.EqualFold(h, host) })
	if len(seen) == n {
		return false, nil
	}
	return true, writeSeen(seenPath, seen)
}

// ReplicaSeen reports whether host (exactly) is in the replicas-seen file
// at path; no file is false.
func ReplicaSeen(path, host string) (bool, error) {
	seen, err := readSeen(path)
	if err != nil {
		return false, err
	}
	_, ok := seen[host]
	return ok, nil
}

// readSeen parses the replicas-seen file; lines it cannot parse are
// skipped (the next write drops them).
func readSeen(path string) (map[string]time.Time, error) {
	seen := map[string]time.Time{}
	content, ok, err := readTrustedFile(path, maxSeenSize)
	if err != nil || !ok {
		return seen, err
	}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		host, ts, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || !validSeenHost(host) {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
		if err != nil {
			continue
		}
		if old, ok := seen[host]; !ok || t.After(old) {
			seen[host] = t
		}
	}
	return seen, nil
}

func validSeenHost(h string) bool {
	return h != "" && len(h) <= 255 && !strings.ContainsFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// seenPath is SeenPath, or SeenFile when it is empty.
func (d *Driver) seenPath() string {
	if d.SeenPath != "" {
		return d.SeenPath
	}
	return SeenFile
}

// RecordReplicaPortsSeen merges ports (1..65535; others are ignored), seen
// at now, into the replica-ports file at path: one line per port,
// "<port> <RFC3339>", sorted, with each port's newest time. Entries older
// than a year are dropped. The file's directory is created when missing.
func RecordReplicaPortsSeen(path string, ports []int, now time.Time) error {
	seen, err := readSeenPorts(path)
	if err != nil {
		return err
	}
	for _, p := range ports {
		if !validSeenPort(p) {
			continue
		}
		if t, ok := seen[p]; !ok || now.After(t) {
			seen[p] = now
		}
	}
	cutoff := seenCutoff(now)
	var lines []string
	for _, p := range slices.Sorted(maps.Keys(seen)) {
		if seen[p].Before(cutoff) {
			continue
		}
		lines = append(lines, strconv.Itoa(p)+" "+seen[p].UTC().Format(time.RFC3339))
	}
	if err := os.MkdirAll(filepath.Dir(path), seenDirMode); err != nil {
		return fmt.Errorf("buat %s: %w", filepath.Dir(path), err)
	}
	return writePin(path, strings.Join(lines, "\n"))
}

// readSeenPorts parses the replica-ports file; lines it cannot parse (and
// ports outside 1..65535) are skipped, a missing file is empty.
func readSeenPorts(path string) (map[int]time.Time, error) {
	seen := map[int]time.Time{}
	content, ok, err := readTrustedFile(path, maxSeenSize)
	if err != nil || !ok {
		return seen, err
	}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		field, ts, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		port, err := strconv.Atoi(field)
		if err != nil || !validSeenPort(port) {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
		if err != nil {
			continue
		}
		if old, ok := seen[port]; !ok || t.After(old) {
			seen[port] = t
		}
	}
	return seen, nil
}

func validSeenPort(p int) bool { return p >= 1 && p <= 65535 }

// seenPortsPath is SeenPortsPath; else replica-ports-seen beside SeenPath
// (so a test that moves SeenPath never touches the system's file); else
// SeenPortsFile.
func (d *Driver) seenPortsPath() string {
	switch {
	case d.SeenPortsPath != "":
		return d.SeenPortsPath
	case d.SeenPath != "":
		return filepath.Join(filepath.Dir(d.SeenPath), filepath.Base(SeenPortsFile))
	}
	return SeenPortsFile
}

// recordSeen records hosts and ports in their files at now, under seenMu.
// Each file is written only when its set differs from what this driver
// last wrote to it, or that write is seenRefresh old; an empty set writes
// nothing. A set whose write failed is tried again at the next call.
func (d *Driver) recordSeen(hosts []string, ports []int, now time.Time) error {
	hosts, ports = slices.Sorted(slices.Values(hosts)), slices.Sorted(slices.Values(ports))
	d.seenMu.Lock()
	defer d.seenMu.Unlock()
	var errs []error
	if len(hosts) > 0 && (!slices.Equal(hosts, d.seenHosts) || now.Sub(d.seenAt) >= seenRefresh) {
		if err := RecordReplicasSeen(d.seenPath(), hosts, now); err != nil {
			errs = append(errs, err)
		} else {
			d.seenHosts, d.seenAt = hosts, now
		}
	}
	if len(ports) > 0 && (!slices.Equal(ports, d.seenPorts) || now.Sub(d.seenPortsAt) >= seenRefresh) {
		if err := RecordReplicaPortsSeen(d.seenPortsPath(), ports, now); err != nil {
			errs = append(errs, err)
		} else {
			d.seenPorts, d.seenPortsAt = ports, now
		}
	}
	return errors.Join(errs...)
}

// observeReplicas reads the hosts (processlist) and ports (SHOW REPLICAS) of
// the replicas connected to db's server now and records them (recordSeen)
// with the hosts of also (a replica's address this agent matched against a
// Binlog Dump thread a moment ago), returning what it saw. A SHOW REPLICAS
// failure is logged (once per distinct message) and the hosts are recorded
// all the same. Without a replica nothing is read further and nothing
// written.
func (d *Driver) observeReplicas(ctx context.Context, db *sql.DB, now time.Time, also ...string) ([]string, []int, error) {
	hosts, err := binlogDumpHosts(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range also {
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return nil, nil, nil
	}
	ports, perr := replicaPorts(ctx, db)
	d.seenMu.Lock()
	msg := ""
	if perr != nil {
		msg = perr.Error()
	}
	logIt := msg != "" && msg != d.portsErr
	d.portsErr = msg
	d.seenMu.Unlock()
	if logIt && d.Logger != nil {
		d.Logger.Warn("replica ports not read; recording the hosts only", "err", perr)
	}
	return hosts, ports, d.recordSeen(hosts, ports, now)
}

// RecordReplicas records the hosts and ports of the replicas reading this
// server's binary log now in the replicas-seen and replica-ports files
// (monitor.ReplicaRecorder, run by the guard loop). It does nothing without
// MySQL, a stored account, or such a replica, and writes a file only when
// its set changed since this driver's last write or that write is
// seenRefresh old.
func (d *Driver) RecordReplicas(ctx context.Context, now time.Time) error {
	_, err := d.recordReplicasNow(ctx, now)
	return err
}

// recordReplicasNow is RecordReplicas with the counts it saw.
func (d *Driver) recordReplicasNow(ctx context.Context, now time.Time) (proto.RecordReplicasResult, error) {
	var res proto.RecordReplicasResult
	h, err := d.host()
	if errors.Is(err, ErrNoHost) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	db, err := h.Open(ctx)
	if errors.Is(err, ErrNotConnected) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	defer db.Close()
	hosts, ports, err := d.observeReplicas(ctx, db, now)
	res.Hosts, res.Ports = len(hosts), len(ports)
	return res, err
}

// registerRecordReplicas registers mysql.recordReplicas: it takes no
// parameters and only makes the agent observe its replicas now. The agent
// never accepts replica hosts or ports from the panel.
func (d *Driver) registerRecordReplicas(reg *commands.Registry) {
	reg.Register("mysql.recordReplicas", func(ctx context.Context, params json.RawMessage) (any, error) {
		var extra map[string]json.RawMessage
		if len(bytes.TrimSpace(params)) > 0 {
			if err := json.Unmarshal(params, &extra); err != nil || len(extra) > 0 {
				return nil, &commands.ParamError{Msg: "mysql.recordReplicas tidak menerima parameter"}
			}
		}
		return d.recordReplicasNow(ctx, d.now())
	})
}
