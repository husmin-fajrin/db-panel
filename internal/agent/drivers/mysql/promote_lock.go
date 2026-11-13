package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The promote lock on a master (spec 5.1): mysql.lockForPromote makes the
// master read-only for the promotion of one connected replica, the guard
// (GuardTick, run by the monitor's guard loop every monitor.GuardEvery)
// ends the lock
// at its deadline, and mysql.unlockPromote cancels it before then. The
// guard never opens writes while the candidate may already be a master:
// it opens them only after reading the candidate read-only twice, a grace
// apart, and then reading, with the same account, every other replica the
// lock lists as readable when it was set (PromoteLock.Readable,
// readableAtLock). One that is writable and holds this server's
// transactions was promoted instead of the candidate (an emergency
// promotion while the lock stood), and the guard quarantines this server
// under it rather than open it. One that cannot be read now may be such a
// master out of reach: the lock stays, and is read again every
// lockRecheck, until every listed replica reads read-only or "dbpanel agent
// forget-replica" drops it. It quarantines this server too once the
// candidate is writable and holds this server's transactions; otherwise
// the lock stays. A lock set by the local CLI (CLILockCandidate) is never
// touched.

const (
	// minLockSec and maxLockSec bound mysql.lockForPromote's timeout.
	minLockSec = 60
	maxLockSec = 3600
	// lockGrace is how long the guard waits, after reading the candidate
	// read-only at or past the deadline, before reading it again and
	// opening the master.
	lockGrace = proto.PromoteLockGrace
	// lockRecheck is how often the guard reads a candidate that is
	// unreachable (or writable without this server's transactions).
	lockRecheck = time.Minute
	// keptEvery is how often, at most, the guard reports a lock it keeps.
	keptEvery = time.Hour
	// unlockTimeout bounds the guard's unlock (MySQL opened, lock file
	// removed), which runs on its own context: never on what the replica
	// reads left of the tick's.
	unlockTimeout = 10 * time.Second
	// maxPeerReads is how many replicas are read at once (readPeers).
	maxPeerReads = 16
	// maxLockReads is how many host:port reads lockForPromote makes at
	// most (recorded hosts × recorded ports); past it the lock is
	// refused at once (SA-4).
	maxLockReads = 64
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
	// keptHosts are the unreadable hosts that report carried (sorted).
	keptAt    time.Time
	keptHosts []string
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
// Binlog Dump thread on it now and accept p's credential (read with
// ReadPeer at the matched address). The caller holds promoteMu.
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
	// The candidate is a replica from now on whatever the monitor has seen:
	// record every replica connected now (hosts and ports), and the
	// candidate's matched address even when its thread is gone by now,
	// before the lock is written. A failed record never fails the lock.
	beforeLockRecord()
	seenHosts, seenPorts, err := d.observeReplicas(ctx, db, d.now(), addr)
	if err != nil && d.Logger != nil {
		d.Logger.Warn("replicas seen not recorded at promote lock", "err", err)
	}
	// The credential must log in to the candidate now: unlockPromote, the
	// guard, the quarantine and the repoints all read it with this
	// account, and a wrong one would only show after writes moved.
	if st := readPeer(ctx, dialAt(p.Candidate, addr), peerTimeout); !st.Reachable {
		return res, &commands.ParamError{Msg: fmt.Sprintf(
			"kandidat %s tidak bisa dibaca dengan akun replikasi (%s); periksa akun %s dan sandinya di kandidat. Master tidak dikunci",
			key, st.Err, p.Candidate.User)}
	}
	ports, fullAt := d.recordedPorts(seenPorts)
	if !fullAt.IsZero() {
		// A replica's port was not recorded: the reads below would miss
		// it, and the guard would open writes past it.
		return res, &commands.ParamError{Msg: fmt.Sprintf("daftar port replika %s penuh (%d port; port lain tidak masuk daftar sejak %s), "+
			"jadi tidak semua replika bisa dibaca; master tidak dikunci. Periksa berkas itu, lalu jalankan di server ini: "+
			"sudo dbpanel agent forget-replica-port semua (port replika yang tersambung masuk daftar lagi dalam beberapa detik)",
			d.seenPortsPath(), maxSeenPorts, fullAt.Local().Format(time.RFC3339))}
	}
	targets := lockReadTargets(p.Candidate, addr, p.Candidate.Port, d.recordedHosts(seenHosts), ports)
	if len(targets) > maxLockReads {
		return res, &commands.ParamError{Msg: fmt.Sprintf("terlalu banyak replika di daftar untuk dibaca sebelum master dikunci "+
			"(%d pembacaan host:port, paling banyak %d); master tidak dikunci. Hapus replika yang sudah tidak dipakai dengan: "+
			"sudo dbpanel agent forget-replica <host> (daftar replika %s) dan port yang tidak dipakai dengan: "+
			"sudo dbpanel agent forget-replica-port <port> (daftar port replika %s)",
			len(targets), maxLockReads, d.seenPath(), d.seenPortsPath())}
	}
	rctx, cancel := context.WithTimeout(ctx, lockReadBudget)
	readable := readReachable(rctx, targets)
	rerr := rctx.Err()
	cancel()
	if err := ctx.Err(); err != nil {
		// The reads did not all finish: a list cut short would let the
		// guard open writes past a replica it never tried.
		return res, fmt.Errorf("replika lain belum selesai dibaca (%w); master tidak dikunci", err)
	}
	if rerr != nil {
		return res, &commands.ParamError{Msg: fmt.Sprintf("replika lain belum selesai dibaca dalam %s; master tidak dikunci. "+
			"Replika yang terdaftar ada di %s dan %s; hapus yang sudah tidak dipakai dengan: sudo dbpanel agent forget-replica <host>",
			lockReadBudget, d.seenPath(), d.seenPortsPath())}
	}
	// The lock file first: a failure or a crash after it leaves a lock
	// the guard turns into super_read_only on its next tick, never a
	// read-only master without a lock nothing automatic would open. The
	// lock file keeps whole seconds.
	res.LockedAt = d.now().UTC().Truncate(time.Second)
	res.Deadline = res.LockedAt.Add(time.Duration(p.TimeoutSec) * time.Second)
	if err := writeLock(d.pins, PromoteLock{Candidate: key, Addr: addr, Deadline: res.Deadline, Readable: readable}); err != nil {
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

// lockReadBudget bounds lockForPromote's reads of the other replicas,
// well under the panel's two-minute call timeout, so a lock is never set
// after the panel gave up on it (SA-4); past it the lock is refused.
// Tests shorten it.
var lockReadBudget = 60 * time.Second

// beforeLockRecord runs in lockForPromote between matching the candidate's
// Binlog Dump thread and recording the replicas; tests replace it.
var beforeLockRecord = func() {}

// recordedHosts is every host of the replicas-seen file, with also (just
// observed) added: a file that cannot be read is logged and only also is
// used.
func (d *Driver) recordedHosts(also []string) []string {
	seen, err := readSeen(d.seenPath())
	if err != nil {
		d.warn("replicas seen not read at promote lock; reading the connected replicas only", "err", err)
	}
	hosts := slices.Collect(maps.Keys(seen))
	return append(hosts, also...)
}

// recordedPorts is every port of the replica-ports file, with also (just
// observed) added, and when the file was marked full (zero: not full): a
// file that cannot be read is logged and only also is used.
func (d *Driver) recordedPorts(also []int) ([]int, time.Time) {
	seen, fullAt, err := readSeenPortsFull(d.seenPortsPath())
	if err != nil {
		d.warn("replica ports seen not read at promote lock; reading the connected replicas' ports only", "err", err)
	}
	ports := slices.Collect(maps.Keys(seen))
	return append(ports, also...), fullAt
}

// readableAtLock is readReachable of lockReadTargets.
func readableAtLock(ctx context.Context, cred proto.PeerCred, candAddr string, candPort int, hosts []string, ports []int) []string {
	return readReachable(ctx, lockReadTargets(cred, candAddr, candPort, hosts, ports))
}

// readReachable reads every cred (readPeers) and returns those that
// answered, as sorted host:port.
func readReachable(ctx context.Context, creds []proto.PeerCred) []string {
	var list []string
	for i, st := range readPeers(ctx, creds) {
		if st.Reachable {
			list = append(list, peerKey(creds[i]))
		}
	}
	slices.Sort(list)
	return slices.Compact(list)
}

// lockReadTargets is what lockForPromote reads with the candidate's
// account cred: every host of hosts at the candidate's port candPort and
// at every port of ports, except the candidate itself (candAddr at
// candPort, just read). Those that answer are the lock's Readable, as
// sorted host:port. With certificate verification the host as recorded is
// the name verified. A replica that cannot be read now (no account for
// cred, or long gone) is left out: the guard never waits for it (spec
// 5.4).
func lockReadTargets(cred proto.PeerCred, candAddr string, candPort int, hosts []string, ports []int) []proto.PeerCred {
	portSet := slices.Compact(slices.Sorted(slices.Values(append([]int{candPort}, ports...))))
	hostSet := slices.Compact(slices.Sorted(slices.Values(hosts)))
	var creds []proto.PeerCred
	for _, h := range hostSet {
		if !validSeenHost(h) {
			continue
		}
		for _, port := range portSet {
			if !validSeenPort(port) || (port == candPort && strings.EqualFold(h, candAddr)) {
				continue
			}
			c := cred
			c.Host, c.Port, c.ServerName = h, port, ""
			creds = append(creds, c)
		}
	}
	return creds
}

// readListed reads every entry (host:port) of list with the candidate's
// account cred (readPeers). unreadable is, in list order, every entry not
// read: unreachable, refused, not a host:port, or not started before ctx
// ended. writable is the first entry, in list order, that is writable
// (super_read_only off) and whose gtid_executed holds a transaction of
// uuid, this server's: a replica of this server promoted to master.
func readListed(ctx context.Context, cred proto.PeerCred, list []string, uuid string) (unreadable []string, writable string) {
	var creds []proto.PeerCred
	var at []int // at[i]: the index in list of creds[i]
	bad := make([]bool, len(list))
	for i, e := range list {
		h, ps, err := net.SplitHostPort(e)
		port, perr := strconv.Atoi(ps)
		if err != nil || perr != nil || !validSeenHost(h) || !validSeenPort(port) {
			bad[i] = true
			continue
		}
		c := cred
		c.Host, c.Port, c.ServerName = h, port, ""
		creds, at = append(creds, c), append(at, i)
	}
	sts := make([]PeerState, len(list))
	for j, st := range readPeers(ctx, creds) {
		sts[at[j]] = st
	}
	for i, e := range list {
		st := sts[i]
		switch {
		case bad[i] || !st.Reachable:
			unreadable = append(unreadable, e)
		case writable == "" && !st.SuperReadOnly && gtidHasUUID(st.GTIDExecuted, uuid):
			writable = e
		}
	}
	return unreadable, writable
}

// readPeers reads every cred with readPeer, each within peerTimeout, at
// most maxPeerReads at a time, and returns the states in creds' order. A
// read not started before ctx ends is not made: its state is unreachable.
// Every read has ended when it returns.
func readPeers(ctx context.Context, creds []proto.PeerCred) []PeerState {
	sts := make([]PeerState, len(creds))
	sem := make(chan struct{}, maxPeerReads)
	var wg sync.WaitGroup
	for i, c := range creds {
		acquired := false
		if ctx.Err() == nil {
			select {
			case sem <- struct{}{}:
				acquired = true
			case <-ctx.Done():
			}
		}
		if err := ctx.Err(); err != nil {
			if acquired {
				<-sem
			}
			sts[i] = PeerState{Err: fmt.Sprintf("%s: tidak dibaca: %v", peerKey(c), err)}
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			sts[i] = readPeer(ctx, c, peerTimeout)
		})
	}
	wg.Wait()
	return sts
}

// now is Now, or time.Now when it is nil.
func (d *Driver) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// unlockPromote cancels the lock while its candidate c is reachable and
// still read-only, and every replica the lock lists (Readable) is read
// with c's account: one that cannot be read keeps the lock; one that is a
// writable master holding this server's transactions quarantines this
// server, as the guard does. The caller holds promoteMu.
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
	// Another replica may have been promoted meanwhile (an emergency
	// promote with this server out of reach): every replica readable when
	// the lock was set is read first, as the guard does (spec 5.4).
	uuid, err := serverUUID(ctx, db)
	if err != nil {
		return err
	}
	unreadable, writable := readListed(ctx, c, l.Readable, uuid)
	switch {
	case writable != "":
		d.warn("unlock refused: another replica is a writable master with this server's transactions; quarantining",
			"candidate", key, "new_source", writable)
		if _, err := d.selfQuarantine(ctx, writable, ""); err != nil {
			return fmt.Errorf("replika %s sudah menjadi master yang bisa ditulis dengan transaksi server ini; karantina gagal: %w", writable, err)
		}
		return &commands.ParamError{Msg: fmt.Sprintf("replika %s sudah menjadi master yang bisa ditulis dengan transaksi server ini; "+
			"server ini dikarantina, kunci tidak dibuka", writable)}
	case len(unreadable) > 0:
		return &commands.ParamError{Msg: fmt.Sprintf("replika yang terbaca saat kunci dipasang tidak bisa dibaca sekarang (%s); "+
			"kunci tidak dibuka", strings.Join(unreadable, ", "))}
	}
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

// GuardTick is the guard, run by the monitor's guard loop every
// monitor.GuardEvery (monitor.PromoteGuard) at now. On a quarantined
// server it keeps read_only and super_read_only on (guardQuarantined) and
// then keeps the start lock (guardStartLock); on any other role it keeps
// or releases the start lock first. While the promote lock exists on a pinned master it
// first makes sure super_read_only is on (again after a MySQL restart,
// before anything else). At or past the deadline of a lock with a
// candidate it reads the candidate (see the package comment above); it
// never acts on a lock-master lock. The start reports and
// guardQuarantined's quarantine notice are sent at the end of the round
// (sendRoundEvents), on every path of a round that ran. Three sends stay
// inline, each the last action of its path, followed only by
// sendRoundEvents: keepLock's and the unlock's EventLockExpired
// (guardExpired), and selfQuarantine's EventQuarantined, sent after its
// start lock is written. Ordering within a round does not shorten the
// pause after it: the guard loop waits GuardEvery once the round, sends
// included, has finished.
func (d *Driver) GuardTick(ctx context.Context, now time.Time) error {
	if d.pins == nil {
		return nil
	}
	h, err := d.host()
	if err != nil {
		return nil
	}
	// A promote command (a repoint probes for up to a minute) holds the
	// lock: this tick is skipped rather than stall the guard loop; its
	// next round runs the guard again.
	if !d.promoteMu.TryLock() {
		if d.Logger != nil {
			d.Logger.Debug("promote guard tick skipped: a promote command is running")
		}
		return nil
	}
	defer d.promoteMu.Unlock()
	// What the round has for the panel goes last, on every path: a send
	// can block on a panel connection that does not drain, and must not
	// delay read-only, the start lock's housekeeping or the promote lock
	// (R-117).
	defer d.sendRoundEvents()
	// A quarantined server is read-only before anything else, lock or
	// not; the start lock's housekeeping (which may run --print-defaults)
	// comes after it, whatever it answered (FR-13 b).
	if d.pins.Role() == roleQuarantined {
		err := d.guardQuarantined(ctx, h)
		d.guardStartLock(ctx, h, now)
		return err
	}
	d.guardStartLock(ctx, h, now)
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
		d.warnLimited("promote lock present but super_read_only was off; locking again")
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
		uuid, err := serverUUID(ctx, db)
		if err != nil {
			return err
		}
		if gtidHasUUID(st.GTIDExecuted, uuid) {
			_, err := d.selfQuarantine(ctx, key, addr)
			return err
		}
		g.graceAt, g.nextRead = time.Time{}, now.Add(lockRecheck)
		d.keepLock(now, key, "kandidat bisa ditulis tetapi gtid_executed-nya tidak memuat transaksi server ini")
	case g.graceAt.IsZero():
		// Read-only: still a replica. Read again after the grace.
		g.graceAt, g.nextRead = now, now.Add(lockGrace)
	default:
		// Read-only at both reads, a grace apart: the promotion to the
		// candidate did not happen. Another replica may have been
		// promoted instead (emergency, with this server out of the
		// panel's reach): every replica readable when the lock was set is
		// read before writes open here.
		uuid, err := serverUUID(ctx, db)
		if err != nil {
			return err
		}
		unreadable, writable := readListed(ctx, *g.cred, l.Readable, uuid)
		switch {
		case writable != "":
			d.warnLimited("promote lock expired but another replica is a writable master with this server's transactions; quarantining",
				"candidate", key, "new_source", writable)
			_, err := d.selfQuarantine(ctx, writable, "")
			return err
		case len(unreadable) > 0:
			g.graceAt, g.nextRead = time.Time{}, now.Add(lockRecheck)
			// The hosts travel in the event's Hosts only, not again in its
			// reason (SP-9: an event over the panel's size is not judged).
			d.keepLock(now, key, "replika yang terbaca saat kunci dipasang tidak bisa dibaca sekarang", unreadable...)
			return nil
		}
		// Every listed replica read read-only (or writable without this
		// server's transactions): open, on a context of its own so that
		// what the reads left of the tick's never cuts the unlock short.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()
		if err := unlockMaster(uctx, db); err != nil {
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

// serverUUID is db's @@global.server_uuid.
func serverUUID(ctx context.Context, db *sql.DB) (string, error) {
	var uuid string
	if err := db.QueryRowContext(ctx, "SELECT @@global.server_uuid").Scan(&uuid); err != nil {
		return "", fmt.Errorf("baca server_uuid: %w", err)
	}
	return uuid, nil
}

// keepLock records that the lock stays past its deadline, reporting it
// the first time, at once whenever the set of unreadable hosts differs
// from the last one reported (another keep reason came first, or the set
// changed: the hosts are what the operator acts on), and otherwise at
// most every keptEvery; hosts are the listed replicas (host:port) that
// could not be read, when that is why. A report the panel did not get is
// tried again at the next recheck. The caller holds promoteMu.
func (d *Driver) keepLock(now time.Time, key, reason string, hosts ...string) {
	g := &d.guard
	hosts = slices.Clone(hosts)
	slices.Sort(hosts)
	if !g.keptAt.IsZero() && now.Sub(g.keptAt) < keptEvery && slices.Equal(hosts, g.keptHosts) {
		return
	}
	d.warn("promote lock expired but kept", "candidate", key, "reason", reason, "hosts", hosts)
	if d.emit(proto.EventLockExpired, proto.LockExpiredEvent{Action: "kept", Candidate: key, Reason: reason, Hosts: hosts}) != nil {
		return
	}
	g.keptAt, g.keptHosts = now, hosts
}

// selfQuarantine makes this server read-only for good (PERSIST, both
// flags) and pins it quarantined under newSource (host:port, as the
// panel names its new master, which rebuild commands match), recording
// addr, the address that host was verified at, for the agent's own reads
// of it (Pins.SourceDial); it removes the promote lock. Last it writes
// the start lock (startlock.go), then reports the quarantine to the panel,
// and returns the start lock's state; a start lock that
// cannot be written leaves the quarantine in place. Once the pin is
// written the start lock is written even when removing the promote lock
// fails. The caller holds promoteMu.
func (d *Driver) selfQuarantine(ctx context.Context, newSource, addr string) (proto.StartLockState, error) {
	var lock proto.StartLockState
	if d.pins == nil {
		return lock, fmt.Errorf("karantina butuh pin peran")
	}
	h, err := d.host()
	if err != nil {
		return lock, err
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return lock, err
	}
	defer db.Close()
	if err := persistReadOnly(ctx, db); err != nil {
		return lock, err
	}
	if err := d.setQuarantined(newSource, addr); err != nil {
		return lock, fmt.Errorf("MySQL sudah read-only, tetapi pin quarantined gagal ditulis: %w", err)
	}
	// MySQL is up and was not quarantined: there is no start to report.
	d.startLockProved, d.qUp = true, true
	if err := clearLock(d.pins); err != nil {
		d.lockStart(ctx, h)
		return lock, fmt.Errorf("server sudah dikarantina, tetapi kunci promosi gagal dihapus: %w", err)
	}
	d.guard.reset(nil)
	d.warn("server quarantined under its new master", "new_source", newSource, "verified_addr", addr)
	// The start lock first, then the panel: a send can block on a panel
	// connection that does not drain.
	lock = d.lockStart(ctx, h)
	d.emit(proto.EventQuarantined, proto.QuarantinedEvent{NewSource: newSource})
	return lock, nil
}

// emit sends an event to the panel when the driver has an Emitter; one
// the panel does not get is logged and its error returned (the health
// reports carry the state too, except for the start reports, which the
// caller keeps: emitStart).
func (d *Driver) emit(typ string, payload any) error {
	if d.Events == nil {
		return nil
	}
	b, err := json.Marshal(payload)
	if err == nil {
		err = d.Events.Emit(proto.Event{Type: typ, Payload: b})
	}
	if err != nil {
		d.warn("event not sent", "type", typ, "err", err)
	}
	return err
}

func (d *Driver) warn(msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.Warn(msg, args...)
	}
}

// warnEvery is how often warnLimited logs the same message at most.
const warnEvery = time.Minute

// warnLimited is warn at most once per warnEvery for the same msg: for
// the guard's warnings that repeat every round while a fault lasts or
// flaps (FR-11; the monitor limits its own the same way).
func (d *Driver) warnLimited(msg string, args ...any) {
	now := time.Now()
	d.warnMu.Lock()
	last, seen := d.warnAt[msg]
	if seen && now.Sub(last) < warnEvery {
		d.warnMu.Unlock()
		return
	}
	if d.warnAt == nil {
		d.warnAt = map[string]time.Time{}
	}
	d.warnAt[msg] = now
	d.warnMu.Unlock()
	d.warn(msg, args...)
}
