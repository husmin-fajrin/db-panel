package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.quarantine (spec 5.1) makes the old master of a promotion
// read-only for good and pins it quarantined under the new master. The
// panel asks; the agent verifies, so that a compromised panel can never
// lock a healthy master:
//
//   - the new master answers (ReadPeer with the given account) and is
//     writable (super_read_only off);
//   - with MySQL up, the new master's gtid_executed holds a transaction of
//     this server's server_uuid: it replicated from this server;
//   - unless this server is under a promote lock (mysql.lockForPromote)
//     for that same host:port, which checked it was a connected replica,
//     one address of the new master's host is in the replicas-seen file
//     (seenAddr), and the new master is read at that address; with MySQL
//     down, which leaves the GTID check out, this is required whatever
//     the lock.
//
// The new master is read at the address that check matched. The source
// pin keeps the name (host:port, what the panel sends and certificates
// carry; rebuild commands match it), and that address is recorded beside
// it (SourceAddrFile) for the agent's own later reads of the source.
//
// With MySQL up it then runs selfQuarantine (PERSIST read_only and
// super_read_only ON, pin quarantined, remove the lock, monitor.
// quarantined, the start lock). With MySQL down the pins are written
// first, then the start lock (startlock.go), so that MySQL starts
// read-only (Applied false); the guard (GuardTick, guardQuarantined)
// reports monitor.quarantined once MySQL answers again. On a server
// already quarantined under the same new master it only makes sure
// read-only is on and the start lock written (Already). Every answer
// says whether the start lock holds (StartLocked); a start lock that
// could not be written never undoes the quarantine.

func (d *Driver) registerQuarantine(reg *commands.Registry) {
	reg.Register("mysql.quarantine", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.QuarantineParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePeer(&p.NewMaster, "master baru:"); err != nil {
			return nil, err
		}
		if d.pins == nil {
			return nil, &commands.ParamError{Msg: "agent ini tidak punya pin peran; karantina ditolak"}
		}
		if err := d.allow("mysql.quarantine", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.promoteMu.Lock()
		defer d.promoteMu.Unlock()
		res, err := d.quarantine(ctx, h, p.NewMaster)
		if err != nil {
			return nil, err
		}
		d.log("mysql quarantine", "new_source", peerKey(p.NewMaster), "applied", res.Applied, "already", res.Already,
			"start_locked", res.StartLocked)
		return res, nil
	})
}

// quarantine runs mysql.quarantine. The caller holds promoteMu.
func (d *Driver) quarantine(ctx context.Context, h Host, nm proto.PeerCred) (proto.QuarantineResult, error) {
	var res proto.QuarantineResult
	key := peerKey(nm)
	up := localGreeting(ctx, h.Layout()) == nil
	switch role := d.pins.Role(); role {
	case roleQuarantined:
		return d.requarantine(ctx, h, nm, up)
	case roleMaster:
	default:
		// Allow refused it already; the role changed in between.
		return res, roleRefusal(role, "mysql.quarantine")
	}

	l, locked, err := d.pins.ReadLock()
	if err != nil {
		return res, &commands.ParamError{Msg: fmt.Sprintf("kunci promosi tidak bisa dibaca, karantina ditolak: %v", err)}
	}
	lockedFor := locked && l.Candidate != CLILockCandidate && strings.EqualFold(l.Candidate, key) && lockAddr(l) != ""

	// The new master is read at the very address a check matched (the
	// lock's recorded address, or the seen check's, each resolved once),
	// never at whatever its name resolves to next; that address is also
	// what the source pin holds.
	dial := nm
	if lockedFor {
		dial = dialAt(nm, lockAddr(l))
	}
	if !lockedFor || !up {
		ip, seen, err := seenAddr(ctx, d.seenPath(), nm.Host)
		if err != nil {
			return res, fmt.Errorf("berkas replika yang pernah tersambung tidak bisa dibaca, karantina ditolak: %w", err)
		}
		if !seen {
			why := "server ini tidak dikunci untuk promosi kandidat itu"
			if lockedFor {
				why = "MySQL server ini mati sehingga transaksinya tidak bisa dicocokkan"
			}
			return res, &commands.ParamError{Msg: fmt.Sprintf(
				"%s tidak pernah tercatat sebagai replika server ini, dan %s; karantina ditolak", nm.Host, why)}
		}
		dial = dialAt(nm, ip)
	}
	// The pin keeps the name (what the panel and certificates know); the
	// verified address is recorded beside it for the agent's own reads.
	addr := ""
	if dial.Host != nm.Host {
		addr = dial.Host
	}

	st := readPeer(ctx, dial, peerTimeout)
	switch {
	case !st.Reachable:
		return res, &commands.ParamError{Msg: fmt.Sprintf("master baru %s tidak bisa dibaca (%s); karantina ditolak", key, st.Err)}
	case st.SuperReadOnly:
		return res, &commands.ParamError{Msg: fmt.Sprintf(
			"master baru %s masih read-only (super_read_only=ON), belum dipromosikan; karantina ditolak", key)}
	}
	if up {
		uuid, err := localServerUUID(ctx, h)
		if err != nil {
			return res, err
		}
		if !gtidHasUUID(st.GTIDExecuted, uuid) {
			return res, &commands.ParamError{Msg: fmt.Sprintf(
				"gtid_executed master baru %s tidak memuat transaksi server ini (%s); ia bukan replika server ini yang dipromosikan, karantina ditolak", key, uuid)}
		}
	}

	if up {
		lock, err := d.selfQuarantine(ctx, key, addr)
		if err != nil {
			return res, err
		}
		res.Applied = true
		return withStartLock(res, lock), nil
	}
	// MySQL is down: the pins now, then the start lock that makes MySQL
	// start read-only; guardQuarantined reports once MySQL answers.
	if err := d.pins.SetQuarantinedAt(key, addr); err != nil {
		return res, fmt.Errorf("pin quarantined gagal ditulis: %w", err)
	}
	// Read-only is proved again once MySQL answers (guardQuarantined).
	d.startLockProved = false
	if err := clearLock(d.pins); err != nil {
		d.lockStart(ctx, h)
		return res, fmt.Errorf("server sudah dipin quarantined, tetapi kunci promosi gagal dihapus: %w", err)
	}
	d.guard.reset(nil)
	d.guard.up, d.quarantinePending = false, true
	d.warn("server pinned quarantined while MySQL is down; read-only follows once MySQL answers", "new_source", key, "verified_addr", addr)
	return withStartLock(res, d.lockStart(ctx, h)), nil
}

// requarantine is mysql.quarantine on a server already quarantined: it
// makes sure read-only is on when MySQL is up and the start lock is
// written. A new master other than the pinned source is refused (only
// root moves that pin).
func (d *Driver) requarantine(ctx context.Context, h Host, nm proto.PeerCred, up bool) (proto.QuarantineResult, error) {
	res := proto.QuarantineResult{Already: true}
	host, port, ok := d.pins.Source()
	if !ok || port != nm.Port || !strings.EqualFold(host, nm.Host) {
		pinned := "(tidak ada)"
		if ok {
			pinned = net.JoinHostPort(host, fmt.Sprint(port))
		}
		return res, &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
			"server ini sudah dikarantina mengikuti sumber %s; karantina di bawah %s ditolak. Untuk mengubah sumber jalankan di server ini: sudo dbpanel agent set-source <host:port>",
			pinned, peerKey(nm))}}
	}
	if up {
		db, err := openAdmin(ctx, h)
		if err == nil {
			defer db.Close()
			err = persistReadOnly(ctx, db)
		}
		if err != nil {
			d.lockStart(ctx, h)
			return res, err
		}
		d.startLockProved = true
		res.Applied = true
	}
	return withStartLock(res, d.lockStart(ctx, h)), nil
}

// persistReadOnly turns read_only and super_read_only on for good.
func persistReadOnly(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{"SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("karantina (%s): %w", q, err)
		}
	}
	return nil
}

// localServerUUID is this server's @@global.server_uuid.
func localServerUUID(ctx context.Context, h Host) (string, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var uuid string
	if err := db.QueryRowContext(ctx, "SELECT @@global.server_uuid").Scan(&uuid); err != nil {
		return "", fmt.Errorf("baca server_uuid: %w", err)
	}
	return uuid, nil
}

// mysqlUptimeSlack is how many seconds Uptime may fall short of what the
// last reading plus the time since predicts within one run of mysqld.
const mysqlUptimeSlack = 2

// guardQuarantined is GuardTick on a quarantined server, after the start
// lock's guard. It acts only on a transition: MySQL answers after the
// guard last saw it down, or it restarted since the last reading (its
// Uptime fell short), which covers agent start, MySQL back after a
// restart, and MySQL back after a quarantine written while it was down;
// and after a rebuild load ended. It then PERSISTs read_only and
// super_read_only on, before anything else, whether or not either is off
// (which proves the start lock may go once the server is a replica:
// startLockProved). While MySQL stays up it does nothing, so the rebuild
// from the pinned source, which lifts read-only for its load
// (liftReadOnly), is not undone. On a start (not after a load) it reports
// how MySQL came up (monitor.quarantineStart: reportQuarantineStart).
// monitor.quarantined (under the pinned source) is reported once a
// quarantine written while MySQL was down takes effect, read-only or not,
// and whenever the flags had to be put back. A MySQL that starts writable
// (the start lock bypassed) stays so until the guard's next tick. The
// caller holds promoteMu.
func (d *Driver) guardQuarantined(ctx context.Context, h Host) error {
	db, err := h.Open(ctx)
	if err != nil {
		d.guard.up, d.qUp = false, false
		return nil
	}
	defer db.Close()
	// MySQL answers after it did not (or at agent start): a start to
	// report, unless it ends a rebuild load.
	if !d.qUp {
		d.qStartPending = true
	}
	d.qUp = true
	// A rebuild load from the pinned source runs with read-only lifted:
	// the guard leaves it be, and checks again once the load is over.
	if src, first, ok := d.deferringToRebuild(); ok {
		if first {
			d.log("quarantine guard deferring: a rebuild load from the pinned source is running", "source", src)
		}
		d.guard.up = false
		return nil
	}
	// A load just ended: read the flags again (its restore may have
	// failed), whether or not a tick deferred during it. A restart the
	// load made is not a start to report (cleared below, after the Uptime
	// reading, which still predates the load).
	afterLoad := d.takeRecheck()
	if afterLoad {
		d.guard.up = false
	}
	// A restart between two ticks is a transition too.
	var name string
	var uptime int64
	if err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&name, &uptime); err != nil {
		return fmt.Errorf("baca Uptime: %w", err)
	}
	// Same run of mysqld: Uptime grew by the (monotonic) time since the
	// last reading, give or take a second of rounding each way.
	now := time.Now()
	expected := d.qUptime + int64(now.Sub(d.qUptimeAt)/time.Second)
	sameRun := uptime >= expected-mysqlUptimeSlack
	// Without an earlier reading (the first tick after this agent run
	// quarantined a live server) a short Uptime is no start.
	if !sameRun && !d.qUptimeAt.IsZero() {
		d.qStartPending = true
	}
	if afterLoad {
		d.qStartPending = false
	}
	d.qUptime, d.qUptimeAt = uptime, now
	if d.guard.up && sameRun {
		return nil
	}
	var sro, ro bool
	var initFile sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@global.super_read_only, @@global.read_only, @@global.init_file").Scan(&sro, &ro, &initFile); err != nil {
		return fmt.Errorf("baca super_read_only: %w", err)
	}
	put := !sro || !ro
	if err := persistReadOnly(ctx, db); err != nil {
		return err
	}
	d.startLockProved = true
	d.guard.up = true
	if d.qStartPending {
		d.qStartPending = false
		d.reportQuarantineStart(h, now, put, initFile.String, uptime)
	}
	if !put && !d.quarantinePending {
		return nil
	}
	d.quarantinePending = false
	src := ""
	if host, port, ok := d.pins.Source(); ok {
		src = net.JoinHostPort(host, fmt.Sprint(port))
	}
	d.warn("quarantine in effect: read-only on", "new_source", src, "flags_put_back", put)
	d.emit(proto.EventQuarantined, proto.QuarantinedEvent{NewSource: src})
	return nil
}

// Resolver lookups of the seen check; tests replace them.
var (
	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	lookupAddr = func(ctx context.Context, addr string) ([]string, error) {
		return net.DefaultResolver.LookupAddr(ctx, addr)
	}
)

// seenAddr resolves host once (an IP address is itself) and returns the
// first of its addresses the replicas-seen file at path holds in a form
// the processlist of this server shows a replica's host in (a Binlog Dump
// thread's HOST without its port, as RecordReplicas stores it): the
// address itself (skip_name_resolve), else a name the address resolves
// back to and that resolves forward to the same address again (MySQL's
// own forward-confirmed reverse lookup, e.g. localhost for 127.0.0.1).
// The caller reads the new master at that address, so a name that also
// resolves to another server, or resolves differently on the next
// lookup, is never read there; an unconfirmed reverse name never
// matches. Names compare case-insensitively. The port is not checked: a
// dump thread's processlist port is the replica's ephemeral client port.
func seenAddr(ctx context.Context, path, host string) (string, bool, error) {
	seen, err := readSeen(path)
	if err != nil || len(seen) == 0 {
		return "", false, err
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	ip, ok := matchAddr(ctx, host, hosts)
	return ip, ok, nil
}

// matchAddr resolves host once (an IP address is itself) and returns the
// first of its addresses that one of shown (processlist host forms)
// names: the address itself, else a forward-confirmed reverse name of it
// (confirmedNames). Names compare case-insensitively.
func matchAddr(ctx context.Context, host string, shown []string) (string, bool) {
	has := func(s string) bool {
		for _, e := range shown {
			if strings.EqualFold(e, s) {
				return true
			}
		}
		return false
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		rctx, cancel := context.WithTimeout(ctx, peerTimeout)
		ips, _ = lookupIP(rctx, host)
		cancel()
	}
	for _, ip := range ips {
		if has(ip.String()) {
			return ip.String(), true
		}
	}
	for _, ip := range ips {
		for _, n := range confirmedNames(ctx, ip) {
			if has(n) {
				return ip.String(), true
			}
		}
	}
	return "", false
}

// dialAt is c read at addr (a verified address of c.Host), the name kept
// for TLS verification.
func dialAt(c proto.PeerCred, addr string) proto.PeerCred {
	if addr != "" && addr != c.Host {
		c.Host, c.ServerName = addr, c.Host
	}
	return c
}

// confirmedNames are the reverse names of ip that resolve back to ip.
func confirmedNames(ctx context.Context, ip net.IP) []string {
	ctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	names, _ := lookupAddr(ctx, ip.String())
	var out []string
	for _, n := range names {
		n = strings.TrimSuffix(n, ".")
		fwd, _ := lookupIP(ctx, n)
		for _, f := range fwd {
			if f.Equal(ip) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// settleRebuilt is Pins.settleRebuilt under promoteMu (the guard reads the
// role under it): after a successful mysql.configureReplication toward the
// pinned source, a quarantined server becomes a replica. From then on the
// guard's quarantine branch (read-only put back on every MySQL restart)
// stops applying and the replica's rules do; a quarantine report still
// waiting for MySQL is dropped. The data must have been replaced first: a
// dumpFrom or cloneFrom from that source succeeded in this agent run
// (loadedFrom); without it the pin stays quarantined and the agent logs
// why, so a configureReplication alone never ends a quarantine with the
// leftover data unchecked.
func (d *Driver) settleRebuilt(host string, port int) error {
	d.promoteMu.Lock()
	defer d.promoteMu.Unlock()
	if !d.pins.quarantinedUnder(host, port) {
		return nil
	}
	if !d.loadedFrom(host, port) {
		if d.Logger != nil {
			d.Logger.Warn("quarantine kept: replication points at the pinned source, but no dump or clone from it "+
				"succeeded in this agent run", "source", net.JoinHostPort(host, fmt.Sprint(port)))
		}
		return nil
	}
	settled, err := d.pins.settleRebuilt(host, port)
	if err != nil || !settled {
		return err
	}
	d.quarantinePending = false
	d.log("quarantine ended: rebuilt as a replica of its pinned source", "source", net.JoinHostPort(host, fmt.Sprint(port)))
	return nil
}
