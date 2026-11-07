package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The promote lock on a master (spec 5.1): mysql.lockForPromote makes the
// master read-only for the promotion of one connected replica, the guard
// (GuardTick, run by the health monitor after every check) ends the lock
// at its deadline, and mysql.unlockPromote cancels it before then. The
// guard never opens writes while the candidate may already be a master:
// it opens them only after reading the candidate read-only twice, a grace
// apart; it quarantines this server once the candidate is writable and
// holds this server's transactions; otherwise the lock stays. A lock set
// by the local CLI (CLILockCandidate) is never touched.

const (
	// minLockSec and maxLockSec bound mysql.lockForPromote's timeout.
	minLockSec = 60
	maxLockSec = 3600
	// lockGrace is how long the guard waits, after reading the candidate
	// read-only at or past the deadline, before reading it again and
	// opening the master.
	lockGrace = 60 * time.Second
	// lockRecheck is how often the guard reads a candidate that is
	// unreachable (or writable without this server's transactions).
	lockRecheck = time.Minute
	// keptEvery is how often, at most, the guard reports a lock it keeps.
	keptEvery = time.Hour
)

// Emitter sends an event to the panel (the agent's connection).
type Emitter interface {
	Emit(ev proto.Event) error
}

// promoteGuard is the guard's state. The caller holds Driver.promoteMu.
type promoteGuard struct {
	// cred is the candidate's account, given by mysql.lockForPromote and
	// kept in memory only: after an agent restart the guard cannot check
	// the candidate and keeps the lock.
	cred *proto.PeerCred
	// graceAt is when the candidate was read read-only past the deadline;
	// zero when no grace runs.
	graceAt time.Time
	// nextRead is when the candidate is read next.
	nextRead time.Time
	// keptAt is when a kept lock was last reported; zero when never.
	keptAt time.Time
	// up is whether MySQL answered the guard's last tick.
	up bool
}

// reset starts the guard over for a lock whose candidate's account is
// cred (nil: none). Whether MySQL was up is kept.
func (g *promoteGuard) reset(cred *proto.PeerCred) {
	*g = promoteGuard{cred: cred, up: g.up}
}

// armGuard gives the guard the candidate's account for the current lock.
func (d *Driver) armGuard(c proto.PeerCred) {
	d.promoteMu.Lock()
	defer d.promoteMu.Unlock()
	d.guard.reset(&c)
}

func (d *Driver) registerPromote(reg *commands.Registry) {
	reg.Register("mysql.lockForPromote", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.LockForPromoteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePeer(&p.Candidate, "kandidat:"); err != nil {
			return nil, err
		}
		if p.TimeoutSec < minLockSec || p.TimeoutSec > maxLockSec {
			return nil, &commands.ParamError{Msg: fmt.Sprintf("batas waktu kunci harus antara %d dan %d detik", minLockSec, maxLockSec)}
		}
		if err := d.allow("mysql.lockForPromote", p); err != nil {
			return nil, err
		}
		if err := d.requireMaster("mysql.lockForPromote"); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.promoteMu.Lock()
		defer d.promoteMu.Unlock()
		res, err := d.lockForPromote(ctx, h, p)
		if err != nil {
			return nil, err
		}
		d.log("mysql locked for promotion", "candidate", peerKey(p.Candidate), "deadline", res.Deadline)
		return res, nil
	})

	reg.Register("mysql.unlockPromote", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.UnlockPromoteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePeer(&p.Candidate, "kandidat:"); err != nil {
			return nil, err
		}
		if err := d.allow("mysql.unlockPromote", p); err != nil {
			return nil, err
		}
		if err := d.requireMaster("mysql.unlockPromote"); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.promoteMu.Lock()
		defer d.promoteMu.Unlock()
		if err := d.unlockPromote(ctx, h, p.Candidate); err != nil {
			return nil, err
		}
		d.log("mysql promotion lock cancelled", "candidate", peerKey(p.Candidate))
		return nil, nil
	})
}

// requireMaster refuses cmd unless this server is pinned master.
func (d *Driver) requireMaster(cmd string) error {
	role := "unassigned"
	if d.pins != nil {
		role = d.pins.Role()
	}
	if role == roleMaster {
		return nil
	}
	return &commands.ParamError{Msg: fmt.Sprintf("perintah %s hanya untuk server yang dipin sebagai master; server ini dipin sebagai %s", cmd, role)}
}

// lockForPromote locks the master for p's candidate, which must have a
// Binlog Dump thread on it now. The caller holds promoteMu.
func (d *Driver) lockForPromote(ctx context.Context, h Host, p proto.LockForPromoteParams) (proto.LockForPromoteResult, error) {
	var res proto.LockForPromoteResult
	key := peerKey(p.Candidate)
	if l, locked, err := d.pins.ReadLock(); err != nil {
		return res, &commands.ParamError{Msg: fmt.Sprintf("kunci promosi tidak bisa dibaca, master tidak dikunci: %v", err)}
	} else if locked {
		return res, &commands.ParamError{Msg: fmt.Sprintf("server ini sudah dikunci untuk promosi (kandidat %s, sampai %s); kunci baru ditolak",
			l.Candidate, l.Deadline.UTC().Format(time.RFC3339))}
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	addr, ok, err := candidateConnected(ctx, db, p.Candidate.Host)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, &commands.ParamError{Msg: fmt.Sprintf(
			"kandidat %s tidak sedang tersambung ke master ini sebagai replika (tidak ada thread Binlog Dump dari alamatnya); master tidak dikunci", key)}
	}
	// The lock file first: a failure or a crash after it leaves a lock
	// the guard turns into super_read_only on its next tick, never a
	// read-only master without a lock nothing automatic would open. The
	// lock file keeps whole seconds.
	res.LockedAt = d.now().UTC().Truncate(time.Second)
	res.Deadline = res.LockedAt.Add(time.Duration(p.TimeoutSec) * time.Second)
	if err := writeLock(d.pins, PromoteLock{Candidate: key, Addr: addr, Deadline: res.Deadline}); err != nil {
		return res, fmt.Errorf("kunci promosi gagal ditulis, master tidak dikunci: %w", err)
	}
	d.guard.reset(&p.Candidate)
	// PERSIST: a MySQL restart never opens writes, not even for a moment.
	if _, err := db.ExecContext(ctx, "SET PERSIST super_read_only = ON"); err != nil {
		err = fmt.Errorf("kunci master (SET PERSIST super_read_only = ON): %w", err)
		if cerr := d.pins.ClearLock(); cerr != nil {
			return res, fmt.Errorf("%w; berkas kunci promosi juga gagal dihapus (%v), hapus dengan: sudo dbpanel agent unlock-master", err, cerr)
		}
		d.guard.reset(nil)
		return res, err
	}
	if err := db.QueryRowContext(ctx, "SELECT @@global.server_uuid, @@global.gtid_executed").Scan(&res.ServerUUID, &res.GTIDExecuted); err != nil {
		return res, fmt.Errorf("master sudah dikunci (kunci promosi tetap), tetapi server_uuid/gtid_executed gagal dibaca: %w", err)
	}
	return res, nil
}

// writeLock is Pins.WriteLock; tests replace it.
var writeLock = (*Pins).WriteLock

// now is Now, or time.Now when it is nil.
func (d *Driver) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// unlockPromote cancels the lock while its candidate c is reachable and
// still read-only. The caller holds promoteMu.
func (d *Driver) unlockPromote(ctx context.Context, h Host, c proto.PeerCred) error {
	key := peerKey(c)
	l, locked, err := d.pins.ReadLock()
	switch {
	case err != nil:
		return &commands.ParamError{Msg: fmt.Sprintf("kunci promosi tidak bisa dibaca: %v", err)}
	case !locked:
		return &commands.ParamError{Msg: "server ini tidak dikunci untuk promosi"}
	case l.Candidate == CLILockCandidate:
		return &commands.ParamError{Msg: "kunci ini dipasang dengan dbpanel agent lock-master; hanya bisa dibuka di server ini dengan: sudo dbpanel agent unlock-master"}
	case !strings.EqualFold(l.Candidate, key):
		return &commands.ParamError{Msg: fmt.Sprintf("kunci promosi ini untuk kandidat %s, bukan %s; kunci tidak dibuka", l.Candidate, key)}
	}
	addr := lockAddr(l)
	if addr == "" {
		return &commands.ParamError{Msg: fmt.Sprintf(
			"alamat kandidat %s tidak tercatat di kunci promosi, kandidat tidak bisa diperiksa; buka kunci di server ini dengan: sudo dbpanel agent unlock-master", key)}
	}
	st := readPeer(ctx, dialAt(c, addr), peerTimeout)
	if !st.Reachable {
		return &commands.ParamError{Msg: fmt.Sprintf("kandidat %s tidak bisa dibaca (%s); kunci tidak dibuka", key, st.Err)}
	}
	if !st.SuperReadOnly {
		return &commands.ParamError{Msg: fmt.Sprintf(
			"kandidat %s bisa ditulis (super_read_only=OFF) dan mungkin sudah menjadi master; kunci tidak dibuka", key)}
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := unlockMaster(ctx, db); err != nil {
		return err
	}
	if err := d.pins.ClearLock(); err != nil {
		return fmt.Errorf("MySQL sudah bisa ditulis, tetapi kunci promosi gagal dihapus: %w", err)
	}
	d.guard.reset(nil)
	return nil
}

// unlockMaster opens a locked master: what the lock persisted is removed
// and both flags are turned off (MySQL turns read_only on with
// super_read_only but not off), as "dbpanel agent unlock-master" does.
func unlockMaster(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{"RESET PERSIST IF EXISTS super_read_only", "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("buka kunci master (%s): %w", q, err)
		}
	}
	return nil
}

// candidateConnected reports whether a Binlog Dump thread on this server
// comes from host, and from which of its addresses: host is resolved once
// (an IP address is itself) and one of its addresses must be a thread's
// processlist host, or have a forward-confirmed reverse name that is
// (MySQL shows a name unless skip_name_resolve is on); see matchAddr.
// That address is what the candidate is read at from then on.
func candidateConnected(ctx context.Context, db *sql.DB, host string) (string, bool, error) {
	hosts, err := binlogDumpHosts(ctx, db)
	if err != nil {
		return "", false, err
	}
	if len(hosts) == 0 {
		return "", false, nil
	}
	addr, ok := matchAddr(ctx, host, hosts)
	return addr, ok, nil
}

// GuardTick is the promote lock's guard, run by the health monitor after
// every check (monitor.PromoteGuard) at now. On a quarantined server it
// only keeps read_only and super_read_only on (guardQuarantined). While
// the lock exists on a pinned master it first makes sure super_read_only
// is on (again after a MySQL restart, before anything else). At or past the deadline of a lock
// with a candidate it reads the candidate (see the package comment above);
// it never acts on a lock-master lock.
func (d *Driver) GuardTick(ctx context.Context, now time.Time) error {
	if d.pins == nil {
		return nil
	}
	h, err := d.host()
	if err != nil {
		return nil
	}
	d.promoteMu.Lock()
	defer d.promoteMu.Unlock()
	// A quarantined server is read-only before anything else, lock or
	// not.
	if d.pins.Role() == roleQuarantined {
		return d.guardQuarantined(ctx, h)
	}
	l, locked, err := d.pins.ReadLock()
	if err != nil {
		return err
	}
	if !locked {
		d.guard.reset(nil)
		return nil
	}
	if d.pins.Role() != roleMaster {
		return nil
	}
	db, err := h.Open(ctx)
	if err != nil {
		// MySQL is down (or refuses the account): the lock is put back
		// once it answers again.
		d.guard.up = false
		return nil
	}
	defer db.Close()
	if err := d.ensureLocked(ctx, db); err != nil {
		return err
	}
	if l.Candidate == CLILockCandidate || now.Before(l.Deadline) || now.Before(d.guard.nextRead) {
		return nil
	}
	return d.guardExpired(ctx, db, l, now)
}

// ensureLocked turns super_read_only on (PERSIST) when MySQL was not seen
// up by the guard before, or reads it off. The caller holds promoteMu.
func (d *Driver) ensureLocked(ctx context.Context, db *sql.DB) error {
	if d.guard.up {
		var on bool
		if err := db.QueryRowContext(ctx, "SELECT @@global.super_read_only").Scan(&on); err != nil {
			return fmt.Errorf("baca super_read_only: %w", err)
		}
		if on {
			return nil
		}
		d.warn("promote lock present but super_read_only was off; locking again")
	}
	if _, err := db.ExecContext(ctx, "SET PERSIST super_read_only = ON"); err != nil {
		return fmt.Errorf("pasang lagi kunci promosi (SET PERSIST super_read_only = ON): %w", err)
	}
	d.guard.up = true
	return nil
}

// guardExpired handles lock l past its deadline when a read is due. The
// candidate is read at the lock's recorded address (lockAddr) only. The
// caller holds promoteMu.
func (d *Driver) guardExpired(ctx context.Context, db *sql.DB, l PromoteLock, now time.Time) error {
	g := &d.guard
	key := l.Candidate
	if g.cred == nil || !strings.EqualFold(peerKey(*g.cred), key) {
		g.nextRead = now.Add(lockRecheck)
		d.keepLock(now, key, "akun kandidat tidak ada di agent ini (agent dimulai ulang setelah kunci dipasang), kandidat tidak bisa diperiksa")
		return nil
	}
	addr := lockAddr(l)
	if addr == "" {
		g.nextRead = now.Add(lockRecheck)
		d.keepLock(now, key, "alamat kandidat tidak tercatat di kunci promosi, kandidat tidak bisa diperiksa")
		return nil
	}
	st := readPeer(ctx, dialAt(*g.cred, addr), peerTimeout)
	switch {
	case !st.Reachable:
		g.graceAt, g.nextRead = time.Time{}, now.Add(lockRecheck)
		d.keepLock(now, key, "kandidat tidak terjangkau: "+st.Err)
	case !st.SuperReadOnly:
		var uuid string
		if err := db.QueryRowContext(ctx, "SELECT @@global.server_uuid").Scan(&uuid); err != nil {
			return fmt.Errorf("baca server_uuid: %w", err)
		}
		if gtidHasUUID(st.GTIDExecuted, uuid) {
			return d.selfQuarantine(ctx, key, addr)
		}
		g.graceAt, g.nextRead = time.Time{}, now.Add(lockRecheck)
		d.keepLock(now, key, "kandidat bisa ditulis tetapi gtid_executed-nya tidak memuat transaksi server ini")
	case g.graceAt.IsZero():
		// Read-only: still a replica. Read again after the grace.
		g.graceAt, g.nextRead = now, now.Add(lockGrace)
	default:
		// Read-only at both reads, a grace apart: the promotion did not
		// happen.
		if err := unlockMaster(ctx, db); err != nil {
			return err
		}
		if err := d.pins.ClearLock(); err != nil {
			return fmt.Errorf("MySQL sudah bisa ditulis, tetapi kunci promosi gagal dihapus: %w", err)
		}
		g.reset(nil)
		d.warn("promote lock expired with the candidate still a replica; master unlocked", "candidate", key)
		d.emit(proto.EventLockExpired, proto.LockExpiredEvent{Action: "unlocked", Candidate: key})
	}
	return nil
}

// keepLock records that the lock stays past its deadline, reporting it
// the first time and then at most every keptEvery. The caller holds
// promoteMu.
func (d *Driver) keepLock(now time.Time, key, reason string) {
	g := &d.guard
	if !g.keptAt.IsZero() && now.Sub(g.keptAt) < keptEvery {
		return
	}
	g.keptAt = now
	d.warn("promote lock expired but kept", "candidate", key, "reason", reason)
	d.emit(proto.EventLockExpired, proto.LockExpiredEvent{Action: "kept", Candidate: key, Reason: reason})
}

// selfQuarantine makes this server read-only for good (PERSIST, both
// flags) and pins it quarantined under newSource (host:port, as the
// panel names its new master, which rebuild commands match), recording
// addr, the address that host was verified at, for the agent's own reads
// of it (Pins.SourceDial); it removes the promote lock. The caller holds
// promoteMu.
func (d *Driver) selfQuarantine(ctx context.Context, newSource, addr string) error {
	if d.pins == nil {
		return fmt.Errorf("karantina butuh pin peran")
	}
	h, err := d.host()
	if err != nil {
		return err
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := persistReadOnly(ctx, db); err != nil {
		return err
	}
	if err := d.pins.SetQuarantinedAt(newSource, addr); err != nil {
		return fmt.Errorf("MySQL sudah read-only, tetapi pin quarantined gagal ditulis: %w", err)
	}
	if err := d.pins.ClearLock(); err != nil {
		return fmt.Errorf("server sudah dikarantina, tetapi kunci promosi gagal dihapus: %w", err)
	}
	d.guard.reset(nil)
	d.warn("server quarantined under its new master", "new_source", newSource, "verified_addr", addr)
	d.emit(proto.EventQuarantined, proto.QuarantinedEvent{NewSource: newSource})
	return nil
}

// emit sends an event to the panel when the driver has an Emitter; one
// the panel does not get is only logged (the health reports carry the
// state too).
func (d *Driver) emit(typ string, payload any) {
	if d.Events == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err == nil {
		err = d.Events.Emit(proto.Event{Type: typ, Payload: b})
	}
	if err != nil {
		d.warn("event not sent", "type", typ, "err", err)
	}
}

func (d *Driver) warn(msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.Warn(msg, args...)
	}
}
