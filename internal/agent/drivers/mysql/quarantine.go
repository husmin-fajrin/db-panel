package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"

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
//     the new master's host is in the replicas-seen file (replicaSeenAs);
//     with MySQL down, which leaves the GTID check out, this is required
//     whatever the lock.
//
// With MySQL up it then runs selfQuarantine (PERSIST read_only and
// super_read_only ON, pin quarantined, remove the lock, monitor.
// quarantined). With MySQL down only the pins are written (Applied
// false): the guard (GuardTick, guardQuarantined) puts read-only on as
// soon as MySQL answers again and reports monitor.quarantined then. On a
// server already quarantined under the same new master it only makes sure
// read-only is on (Already).

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
		d.log("mysql quarantine", "new_source", peerKey(p.NewMaster), "applied", res.Applied, "already", res.Already)
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
	lockedFor := locked && l.Candidate != CLILockCandidate && strings.EqualFold(l.Candidate, key)

	st := readPeer(ctx, nm, peerTimeout)
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
	if !lockedFor || !up {
		seen, err := replicaSeenAs(ctx, d.seenPath(), nm.Host)
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
	}

	if up {
		if err := d.selfQuarantine(ctx, key); err != nil {
			return res, err
		}
		res.Applied = true
		return res, nil
	}
	// MySQL is down: the pins now, read-only once MySQL answers again
	// (guardQuarantined).
	if err := d.pins.SetQuarantined(key); err != nil {
		return res, fmt.Errorf("pin quarantined gagal ditulis: %w", err)
	}
	if err := d.pins.ClearLock(); err != nil {
		return res, fmt.Errorf("server sudah dipin quarantined, tetapi kunci promosi gagal dihapus: %w", err)
	}
	d.guard.reset(nil)
	d.guard.up = false
	d.warn("server pinned quarantined while MySQL is down; read-only follows once MySQL answers", "new_source", key)
	return res, nil
}

// requarantine is mysql.quarantine on a server already quarantined: it
// makes sure read-only is on when MySQL is up. A new master other than
// the pinned source is refused (only root moves that pin).
func (d *Driver) requarantine(ctx context.Context, h Host, nm proto.PeerCred, up bool) (proto.QuarantineResult, error) {
	res := proto.QuarantineResult{Already: true}
	host, port, ok := d.pins.Source()
	if !ok || !strings.EqualFold(host, nm.Host) || port != nm.Port {
		pinned := "(tidak ada)"
		if ok {
			pinned = net.JoinHostPort(host, fmt.Sprint(port))
		}
		return res, &pinError{kind: ErrSourcePinned, pe: &commands.ParamError{Msg: fmt.Sprintf(
			"server ini sudah dikarantina mengikuti sumber %s; karantina di bawah %s ditolak. Untuk mengubah sumber jalankan di server ini: sudo dbpanel agent set-source <host:port>",
			pinned, peerKey(nm))}}
	}
	if !up {
		return res, nil
	}
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	if err := persistReadOnly(ctx, db); err != nil {
		return res, err
	}
	res.Applied = true
	return res, nil
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

// guardQuarantined is GuardTick on a quarantined server: whenever MySQL
// answers with read_only or super_read_only off (MySQL back after a
// quarantine written while it was down, or a flag lost behind the agent's
// back) it PERSISTs both on before anything else and reports
// monitor.quarantined under the pinned source. The caller holds
// promoteMu.
func (d *Driver) guardQuarantined(ctx context.Context, h Host) error {
	db, err := h.Open(ctx)
	if err != nil {
		d.guard.up = false
		return nil
	}
	defer db.Close()
	var sro, ro bool
	if err := db.QueryRowContext(ctx, "SELECT @@global.super_read_only, @@global.read_only").Scan(&sro, &ro); err != nil {
		return fmt.Errorf("baca super_read_only: %w", err)
	}
	if sro && ro {
		d.guard.up = true
		return nil
	}
	if err := persistReadOnly(ctx, db); err != nil {
		return err
	}
	d.guard.up = true
	src := ""
	if host, port, ok := d.pins.Source(); ok {
		src = net.JoinHostPort(host, fmt.Sprint(port))
	}
	d.warn("quarantined server was writable; read-only put back", "new_source", src)
	d.emit(proto.EventQuarantined, proto.QuarantinedEvent{NewSource: src})
	return nil
}

// replicaSeenAs reports whether host is in the replicas-seen file at
// path in a form the processlist of this server shows a replica's host
// in (a Binlog Dump thread's HOST without its port, as RecordReplicas
// stores it): the host as given; each address it resolves to (the form
// with skip_name_resolve, or for a host given by name); and each name
// those addresses resolve back to (MySQL's own reverse lookup, e.g.
// localhost for 127.0.0.1). Every comparison is exact, case-insensitive
// for names.
func replicaSeenAs(ctx context.Context, path, host string) (bool, error) {
	seen, err := readSeen(path)
	if err != nil || len(seen) == 0 {
		return false, err
	}
	forms := []string{host}
	for _, ip := range resolveHost(ctx, host) {
		forms = append(forms, ip.String())
		rctx, cancel := context.WithTimeout(ctx, peerTimeout)
		names, _ := net.DefaultResolver.LookupAddr(rctx, ip.String())
		cancel()
		for _, n := range names {
			forms = append(forms, strings.TrimSuffix(n, "."))
		}
	}
	for s := range seen {
		for _, f := range forms {
			if strings.EqualFold(s, f) {
				return true, nil
			}
		}
	}
	return false, nil
}
