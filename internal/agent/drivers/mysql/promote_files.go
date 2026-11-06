package mysql

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Files of a promotion (spec 5.1), beside the pins in the agent directory
// and written and read like them (writePin, readTrustedFile).
const (
	// LockFile is the promote lock: while it exists a pinned master is
	// not made writable from the panel (refusedByRole).
	LockFile = "promote-lock"
	// PromotedFile records the last promotion of this server to master.
	PromotedFile = "promoted"
)

// SeenFile is where the agent records the replicas that read this
// server's binary log (Driver.SeenPath overrides it).
const SeenFile = "/var/lib/dbpanel/replicas-seen"

const (
	// maxRecordSize bounds what is read from the lock and the promotion
	// record (a gtid_executed can be long).
	maxRecordSize = 64 << 10
	// maxSeenSize bounds what is read from the replicas-seen file.
	maxSeenSize = 1 << 20
	// seenDirMode is the mode of the directory RecordReplicasSeen creates.
	seenDirMode = 0o755
)

// PromoteLock is the promote lock: the server stays read-only for the
// promotion of Candidate until Deadline. Candidate "cli" is the lock root
// set with "dbpanel agent lock-master" and means no candidate: nothing
// automatic (the guard that ends an expired promotion included) ever
// unlocks or quarantines a server under such a lock; only "dbpanel agent
// unlock-master" removes it.
type PromoteLock struct {
	Candidate string
	Deadline  time.Time
}

// CLILockCandidate is the Candidate of a lock set by the local CLI.
const CLILockCandidate = "cli"

type lockJSON struct {
	Candidate string `json:"candidate"`
	Deadline  string `json:"deadline"`
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

// WriteLock writes the promote lock, replacing any.
func (p *Pins) WriteLock(l PromoteLock) error {
	if l.Candidate == "" || l.Deadline.IsZero() {
		return errors.New("kunci promosi butuh kandidat dan tenggat")
	}
	return writeJSON(p.path(LockFile), lockJSON{Candidate: l.Candidate, Deadline: l.Deadline.UTC().Format(time.RFC3339)})
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
	if err != nil || v.Candidate == "" {
		return PromoteLock{}, false, fmt.Errorf("%s tidak valid; hapus dengan: sudo dbpanel agent unlock-master", p.path(LockFile))
	}
	return PromoteLock{Candidate: v.Candidate, Deadline: d}, true, nil
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
	var lines []string
	for _, h := range slices.Sorted(maps.Keys(seen)) {
		if seen[h].Before(cutoff) {
			continue
		}
		lines = append(lines, h+" "+seen[h].UTC().Format(time.RFC3339))
	}
	if err := os.MkdirAll(filepath.Dir(path), seenDirMode); err != nil {
		return fmt.Errorf("buat %s: %w", filepath.Dir(path), err)
	}
	return writePin(path, strings.Join(lines, "\n"))
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

// RecordReplicas records the hosts of the replicas reading this server's
// binary log now in the replicas-seen file (monitor.ReplicaRecorder). It
// does nothing without MySQL, a stored account, or such a replica.
func (d *Driver) RecordReplicas(ctx context.Context, now time.Time) error {
	h, err := d.host()
	if errors.Is(err, ErrNoHost) {
		return nil
	}
	if err != nil {
		return err
	}
	db, err := h.Open(ctx)
	if errors.Is(err, ErrNotConnected) {
		return nil
	}
	if err != nil {
		return err
	}
	defer db.Close()
	hosts, err := binlogDumpHosts(ctx, db)
	if err != nil || len(hosts) == 0 {
		return err
	}
	return RecordReplicasSeen(d.seenPath(), hosts, now)
}
