package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.promote (spec 5.1) makes this replica the master, as an agent
// job. It verifies the old master itself before it opens writes:
//
//  1. Writes already open (read_only and super_read_only off): resumed
//     at 6 only when the promoting marker of an earlier run from the
//     same old master exists and is recent; replication a MySQL restart
//     started again is stopped first (STOP REPLICA). Refused otherwise.
//  2. planned: the old master answers, is super_read_only and holds no
//     transaction this server lacks. emergency: three probes of the old
//     master, 10 s apart, all fail and the replica's receiver is not
//     connected; an old master that answers, read-only or not, refuses:
//     a live master is no emergency (a locked one is too: its guard may
//     open it again at the lock's deadline).
//  3. STOP REPLICA IO_THREAD, then apply the relay log (bounded: an
//     incomplete last transaction is given up and reported).
//  4. emergency: one more probe; an old master that answers now restores
//     the replica and refuses as in 2. planned: the lock has at least 2
//     minutes left (the seconds the panel gave, LockRemainingSec, less
//     what passed on this server's clock since the request came) and
//     the old master is still read-only with nothing this server lacks.
//  5. The promoting marker (synced), STOP REPLICA, then SET PERSIST
//     read_only/super_read_only OFF: the point of no return.
//  6. RESET REPLICA ALL, the replication account for its own replicas,
//     pin master, write the promotion record, remove the marker.
//
// A failure from 3 until read_only is off removes the marker and starts
// replication again; one after is only reported, and mysql.promote run
// again resumes at 6.

const (
	// promoteLockMargin is the least time left on the old master's lock
	// for a planned promotion to open writes.
	promoteLockMargin = 2 * time.Minute
	// emergencyProbes is how many probes of the old master must all fail.
	emergencyProbes = 3
	// markerMaxAge is how long a promoting marker allows a resume.
	markerMaxAge = time.Hour
)

// probeSpacing is the pause between two emergency probes; tests shorten
// it.
var probeSpacing = 10 * time.Second

var (
	// relayWaitSec is the WAIT_FOR_EXECUTED_GTID_SET timeout, the bound
	// of step 3; tests shorten it.
	relayWaitSec = 600
	// relayStall is how long gtid_executed must stay unchanged, with
	// the applier idle, before step 3 gives up the rest.
	relayStall = 60 * time.Second
	// relayLogEvery is how often applying the relay log is checked and
	// reported.
	relayLogEvery = 5 * time.Second
)

// errRelayWaitTimeout is WAIT_FOR_EXECUTED_GTID_SET returning 1.
var errRelayWaitTimeout = errors.New("WAIT_FOR_EXECUTED_GTID_SET habis waktu")

func (d *Driver) registerPromoteJob(reg *commands.Registry) {
	reg.Register("mysql.promote", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.PromoteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePromote(&p); err != nil {
			return nil, err
		}
		if d.pins == nil {
			return nil, &commands.ParamError{Msg: "agent ini tidak punya pin peran; promote ditolak"}
		}
		if d.Jobs == nil {
			return nil, errors.New("agent tidak bisa menjalankan pekerjaan latar")
		}
		old := peerKey(p.OldSource)
		// Already promoted by this promotion: answered from its record.
		if pr, ok, err := d.promotedSince(p); err != nil {
			return nil, err
		} else if ok {
			h, err := d.host()
			if err != nil {
				return nil, err
			}
			id, err := d.Jobs.Start("mysql.promote", func(j *jobs.Job) (any, error) {
				res := proto.PromoteResult{GTIDExecuted: pr.GTIDExecuted, Mode: pr.Mode, Resumed: true}
				db, err := openAdmin(j.Context(), h)
				if err != nil {
					return nil, err
				}
				defer db.Close()
				if err := db.QueryRowContext(j.Context(), "SELECT @@global.server_uuid").Scan(&res.ServerUUID); err != nil {
					return nil, fmt.Errorf("baca server_uuid: %w", err)
				}
				j.Logf("info", "server ini sudah dipromosikan dari %s pada %s; tidak ada yang diulang",
					old, pr.At.UTC().Format(time.RFC3339))
				return res, nil
			})
			if err != nil {
				return nil, err
			}
			d.log("mysql promote already done", "job", id, "old_source", old, "at", pr.At)
			return proto.StartedJob{JobID: id}, nil
		}
		if err := d.allow("mysql.promote", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		lockBy := promoteLockBy(p, d.now())
		id, err := d.Jobs.Start("mysql.promote", func(j *jobs.Job) (any, error) {
			res, err := d.promote(j, h, p, lockBy)
			if err != nil {
				d.warn("mysql promote failed", "job", j.ID(), "mode", p.Mode, "old_source", old, "err", err)
				return nil, err
			}
			d.log("mysql promoted to master", "job", j.ID(), "mode", p.Mode, "old_source", old,
				"applied", res.Applied, "resumed", res.Resumed, "gtid_executed", res.GTIDExecuted)
			return res, nil
		})
		if err != nil {
			return nil, err
		}
		d.log("mysql promote started", "job", id, "mode", p.Mode, "old_source", old)
		return proto.StartedJob{JobID: id}, nil
	})

	reg.Register("mysql.probeSource", func(ctx context.Context, _ json.RawMessage) (any, error) {
		host, port, dial, ok := "", 0, "", false
		if d.pins != nil {
			var err error
			if host, port, dial, ok, err = d.pins.SourceDial(); err != nil {
				return nil, &commands.ParamError{Msg: fmt.Sprintf("pin sumber tidak bisa dibaca: %v", err)}
			}
		}
		if !ok {
			return nil, &commands.ParamError{Msg: "server ini tidak punya sumber replikasi yang dipin"}
		}
		// The verified address of the pinned name, when one is recorded.
		res := proto.ProbeSourceResult{Host: host, Port: port, Reachable: true}
		if err := probePeer(ctx, dial, port, peerTimeout); err != nil {
			res.Reachable, res.Err = false, err.Error()
		}
		return res, nil
	})
}

// promoteLockBy is when, on this server's clock, the old master's lock
// ends for a promotion requested at recv: recv plus the seconds the panel
// said were left (LockRemainingSec), so clock skew between the servers
// does not eat the margin. LockDeadline, the master's own clock, is only
// used when the panel sent no remaining seconds. nil: no lock (emergency).
func promoteLockBy(p proto.PromoteParams, recv time.Time) *time.Time {
	if p.LockRemainingSec != nil {
		t := recv.Add(time.Duration(*p.LockRemainingSec) * time.Second)
		return &t
	}
	return p.LockDeadline
}

// promotedSince returns this server's promotion record when the server is
// pinned master by a promotion from p.OldSource recorded at or after
// p.NotBefore (never for a zero NotBefore).
func (d *Driver) promotedSince(p proto.PromoteParams) (Promoted, bool, error) {
	if p.NotBefore.IsZero() || d.pins.Role() != roleMaster {
		return Promoted{}, false, nil
	}
	pr, ok, err := d.pins.ReadPromoted()
	if err != nil {
		return pr, false, &commands.ParamError{Msg: fmt.Sprintf("catatan promosi tidak bisa dibaca: %v", err)}
	}
	if !ok || !strings.EqualFold(pr.OldSource, peerKey(p.OldSource)) || pr.At.Before(p.NotBefore) {
		return pr, false, nil
	}
	return pr, true, nil
}

// validatePromote checks p; the passwords never reach an error.
func validatePromote(p *proto.PromoteParams) error {
	if p.Mode != proto.PromotePlanned && p.Mode != proto.PromoteEmergency {
		return &commands.ParamError{Msg: fmt.Sprintf("mode promote harus %s atau %s", proto.PromotePlanned, proto.PromoteEmergency)}
	}
	if err := validatePeer(&p.OldSource, "master lama:"); err != nil {
		return err
	}
	if p.ReplUser = strings.TrimSpace(p.ReplUser); p.ReplUser == "" {
		p.ReplUser = replUserName
	}
	if p.ReplUser != replUserName {
		return &commands.ParamError{Msg: fmt.Sprintf("user replikasi harus %s", replUserName)}
	}
	return account{User: replUserName, Host: "%", Password: p.ReplPassword}.validate("replikasi")
}

// promote runs mysql.promote's steps (see above) in job j; lockBy is
// when the old master's lock ends on this server's clock (promoteLockBy).
func (d *Driver) promote(j *jobs.Job, h Host, p proto.PromoteParams, lockBy *time.Time) (proto.PromoteResult, error) {
	var res proto.PromoteResult
	ctx := j.Context()
	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()

	var ro, sro bool
	if err := db.QueryRowContext(ctx, "SELECT @@global.read_only, @@global.super_read_only").Scan(&ro, &sro); err != nil {
		return res, fmt.Errorf("baca read_only: %w", err)
	}
	mode := p.Mode
	if !ro && !sro {
		// Never opens writes itself: an earlier run opened them, after
		// its verification, and left the promoting marker.
		m, err := d.checkResume(ctx, db, p)
		if err != nil {
			return res, err
		}
		j.Logf("warn", "server ini sudah bisa ditulis oleh promote sebelumnya (%s, %s); dilanjutkan tanpa verifikasi ulang",
			m.Mode, m.At.UTC().Format(time.RFC3339))
		res.Resumed, mode = true, m.Mode
	} else {
		applied, unapplied, err := d.openWrites(j, db, p, lockBy)
		if err != nil {
			return res, err
		}
		res.Applied, res.Unapplied = applied, unapplied
	}
	if err := d.finishPromote(j, h, db, p, mode, &res); err != nil {
		return res, err
	}
	return res, nil
}

// checkResume allows step 1's resume only for a promotion of this replica
// from the same old master that wrote the promoting marker (so passed its
// verification) within markerMaxAge: a replica writable for any other
// reason (a load interrupted by a crash, a manual SET GLOBAL) is never
// promoted unverified. Replication running under a valid marker was
// started again by a MySQL restart between opening writes and RESET
// REPLICA ALL (the replica's channel starts with MySQL): it is stopped
// and the promotion resumed, since the old master may already have
// quarantined itself under this server and only finishing it leaves a
// writable master.
func (d *Driver) checkResume(ctx context.Context, db *sql.DB, p proto.PromoteParams) (Promoting, error) {
	key := peerKey(p.OldSource)
	m, ok, err := d.pins.ReadPromoting()
	if err != nil {
		return m, &commands.ParamError{Msg: fmt.Sprintf("catatan promote tidak bisa dibaca, promote ditolak: %v", err)}
	}
	if !ok || !strings.EqualFold(m.OldSource, key) {
		return m, &commands.ParamError{Msg: fmt.Sprintf(
			"server ini bisa ditulis (read_only=OFF) tanpa catatan promote dari %s yang sedang berjalan; promote ditolak. "+
				"Bila tidak ada aplikasi yang menulis ke server ini, kunci kembali replika ini (mysql.setReadOnly on) lalu ulangi promote", key)}
	}
	// A marker left by a run that never opened writes (a crash before
	// them) must not let a much later unlock pass for its resume.
	if age := d.now().Sub(m.At); age > markerMaxAge || age < -markerMaxAge {
		msg := fmt.Sprintf("catatan promote dari %s sudah kedaluwarsa (%s); promote ditolak dan catatannya dihapus. "+
			"Bila tidak ada aplikasi yang menulis ke server ini, kunci kembali replika ini (mysql.setReadOnly on) lalu ulangi promote",
			key, m.At.UTC().Format(time.RFC3339))
		if err := d.pins.ClearPromoting(); err != nil {
			msg += fmt.Sprintf(" (catatan gagal dihapus: %v)", err)
		}
		return m, &commands.ParamError{Msg: msg}
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return m, err
	}
	if rs != nil && (rs.IORunning == "Yes" || rs.IORunning == "Connecting" || rs.SQLRunning == "Yes") {
		if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
			return m, fmt.Errorf("server ini sudah bisa ditulis oleh promote sebelumnya, tetapi replikasinya berjalan lagi "+
				"(IO: %s, SQL: %s) dan gagal dihentikan (STOP REPLICA: %w); jalankan promote lagi", rs.IORunning, rs.SQLRunning, err)
		}
		if d.Logger != nil {
			d.Logger.Warn("promote resume: replication was running again (MySQL restarted); stopped",
				"io", rs.IORunning, "sql", rs.SQLRunning)
		}
	}
	return m, nil
}

// promoteCrash, when set, is called at the stages "before-open" (marker
// written, replication stopped) and "after-open" (writes open); tests
// make it panic to stand for a crash there.
var promoteCrash func(stage string)

func crashPoint(stage string) {
	if promoteCrash != nil {
		promoteCrash(stage)
	}
}

// openWrites runs steps 2 to 5 and returns how many relay log
// transactions were applied and the incomplete ones left unapplied.
// Writes are open when it returns nil.
func (d *Driver) openWrites(j *jobs.Job, db *sql.DB, p proto.PromoteParams, lockBy *time.Time) (int, string, error) {
	ctx := j.Context()
	key := peerKey(p.OldSource)
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return 0, "", err
	}
	if rs == nil {
		return 0, "", &commands.ParamError{Msg: "replikasi tidak dikonfigurasi di server ini; tidak ada yang bisa dipromosikan"}
	}
	planned, err := d.verifyOldSource(j, db, p)
	if err != nil {
		return 0, "", err
	}

	// From here replication is changed: a failure removes the marker and
	// starts replication again.
	restore := func(cause error) error {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if err := d.pins.ClearPromoting(); err != nil {
			j.Logf("error", "%v", err)
			cause = fmt.Errorf("%w; catatan promote juga gagal dihapus (%v)", cause, err)
		}
		if _, err := db.ExecContext(rctx, "START REPLICA"); err != nil {
			j.Logf("error", "START REPLICA gagal: %v", err)
			return fmt.Errorf("%w; replikasi juga gagal dijalankan kembali (START REPLICA: %v)", cause, err)
		}
		j.Logf("info", "promote dibatalkan; replikasi dijalankan kembali")
		return cause
	}
	applied, unapplied, err := applyRelayLog(j, db)
	if err != nil {
		return 0, "", restore(err)
	}
	if planned {
		if err := d.recheckOldSource(j, db, p, lockBy); err != nil {
			return 0, "", restore(err)
		}
	} else if perr := probePeer(ctx, p.OldSource.Host, p.OldSource.Port, peerTimeout); perr == nil {
		// The old master came back while the relay log was applied: alive,
		// locked or not, so no emergency.
		j.Logf("warn", "master lama %s menjawab lagi sebelum tulis dibuka", key)
		return 0, "", restore(oldMasterAlive(key))
	}
	if err := d.pins.WritePromoting(Promoting{Mode: p.Mode, OldSource: key, At: d.now()}); err != nil {
		return 0, "", restore(fmt.Errorf("catatan promote gagal ditulis: %w", err))
	}
	if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
		return 0, "", restore(fmt.Errorf("STOP REPLICA: %w", err))
	}
	crashPoint("before-open")
	// The point of no return: no rollback once read_only is off.
	j.Logf("info", "membuka tulis di server ini (titik tanpa balik)")
	wctx := context.WithoutCancel(ctx)
	if _, err := db.ExecContext(wctx, "SET PERSIST read_only = OFF"); err != nil {
		return 0, "", restore(fmt.Errorf("SET PERSIST read_only = OFF: %w", err))
	}
	if _, err := db.ExecContext(wctx, "SET PERSIST super_read_only = OFF"); err != nil {
		return 0, "", fmt.Errorf("SET PERSIST super_read_only = OFF: %w; tulis sudah terbuka, jalankan promote lagi untuk melanjutkan", err)
	}
	crashPoint("after-open")
	return applied, unapplied, nil
}

// verifyOldSource is step 2. planned reports whether this is a planned
// promotion, whose old master step 4 reads again; an emergency one
// whose old master answers is refused.
func (d *Driver) verifyOldSource(j *jobs.Job, db *sql.DB, p proto.PromoteParams) (planned bool, err error) {
	ctx := j.Context()
	old := p.OldSource
	key := peerKey(old)
	if p.Mode == proto.PromoteEmergency {
		answered := false
		for i := range emergencyProbes {
			if i > 0 {
				if err := pause(ctx, probeSpacing); err != nil {
					return false, err
				}
			}
			perr := probePeer(ctx, old.Host, old.Port, peerTimeout)
			if perr == nil {
				answered = true
				break
			}
			j.Logf("info", "master lama %s tidak menjawab (percobaan %d dari %d): %v", key, i+1, emergencyProbes, perr)
		}
		if !answered {
			rs, err := currentStatus(ctx, db)
			if err != nil {
				return false, err
			}
			if rs == nil {
				return false, &commands.ParamError{Msg: "replikasi tidak dikonfigurasi di server ini; tidak ada yang bisa dipromosikan"}
			}
			if rs.IORunning == "Yes" {
				return false, &commands.ParamError{Msg: fmt.Sprintf(
					"master lama %s tidak menjawab dari agent, tetapi thread IO replika ini masih tersambung ke master; promote darurat ditolak", key)}
			}
			j.Logf("warn", "master lama %s tidak terjangkau dan thread IO replika tidak tersambung (%s): promote darurat dilanjutkan", key, rs.IORunning)
			return false, nil
		}
		return false, oldMasterAlive(key)
	}
	st := readPeer(ctx, old, peerTimeout)
	switch {
	case !st.Reachable:
		return false, &commands.ParamError{Msg: fmt.Sprintf(
			"master lama %s tidak bisa dibaca (%s); promote terencana butuh master yang menjawab dan sudah dikunci. Bila master mati, gunakan promote darurat",
			key, st.Err)}
	case !st.SuperReadOnly:
		return false, &commands.ParamError{Msg: fmt.Sprintf(
			"master lama %s masih bisa ditulis (super_read_only=OFF); kunci master dulu sebelum promote terencana", key)}
	}
	j.Logf("info", "master lama %s menjawab dan read-only", key)
	return true, caughtUp(ctx, db, key, st.GTIDExecuted)
}

// oldMasterAlive refuses an emergency promotion whose old master
// answered. A live master is no emergency, locked or not: a promote lock's
// guard opens the master again at its deadline, and a lock-master lock is
// opened by hand, either way beside this server opening writes.
func oldMasterAlive(key string) error {
	return &commands.ParamError{Msg: fmt.Sprintf(
		"master lama %s masih hidup: MySQL-nya menjawab dari replika ini (terkunci atau tidak), jadi ini bukan keadaan darurat; "+
			"promote darurat ditolak dan replika ini dibiarkan seperti semula. Gunakan promote terencana; bila master itu "+
			"terkunci dan harus dipakai lagi, buka kuncinya di server master dengan: sudo dbpanel agent unlock-master", key)}
}

// caughtUp refuses unless this server executed every transaction of
// oldExecuted, the old master's gtid_executed.
func caughtUp(ctx context.Context, db *sql.DB, key, oldExecuted string) error {
	var missing string
	if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, @@global.gtid_executed)", oldExecuted).Scan(&missing); err != nil {
		return fmt.Errorf("bandingkan set GTID dengan master lama: %w", err)
	}
	if missing = strings.ReplaceAll(missing, "\n", ""); missing != "" {
		return &commands.ParamError{Msg: fmt.Sprintf(
			"replika ini belum menerapkan semua transaksi master lama %s (kurang: %s); tunggu replika menyusul lalu ulangi promote", key, missing)}
	}
	return nil
}

// recheckOldSource is step 4: the lock's end (lockBy, this server's
// clock) is not too close and the old master is still read-only with
// nothing this server lacks.
func (d *Driver) recheckOldSource(j *jobs.Job, db *sql.DB, p proto.PromoteParams, lockBy *time.Time) error {
	ctx := j.Context()
	key := peerKey(p.OldSource)
	if lockBy != nil && lockBy.Sub(d.now()) < promoteLockMargin {
		if d.Logger != nil {
			d.Logger.Warn("promote refused: lock margin", "lock_deadline", p.LockDeadline, "lock_remaining_sec", p.LockRemainingSec,
				"lock_ends_local", lockBy)
		}
		return &commands.ParamError{Msg: "tenggat kunci master hampir habis; ulangi promote"}
	}
	st := readPeer(ctx, p.OldSource, peerTimeout)
	switch {
	case !st.Reachable:
		return &commands.ParamError{Msg: fmt.Sprintf("master lama %s tidak bisa dibaca lagi sebelum tulis dibuka (%s); promote dibatalkan", key, st.Err)}
	case !st.SuperReadOnly:
		return &commands.ParamError{Msg: fmt.Sprintf("master lama %s bisa ditulis lagi sebelum tulis dibuka di sini; promote dibatalkan", key)}
	}
	return caughtUp(ctx, db, key, st.GTIDExecuted)
}

// applyRelayLog is step 3: it stops the receiver and waits until this
// server executed every transaction it retrieved, reporting every
// relayLogEvery. It returns how many transactions were applied, and the
// ones it gave up on: at the WAIT_FOR_EXECUTED_GTID_SET timeout, or once
// gtid_executed stood still for relayStall with the whole applier idle
// (an incomplete transaction from a master that died sending it, which
// RESET REPLICA ALL discards). An applier error fails it first.
func applyRelayLog(j *jobs.Job, db *sql.DB) (int, string, error) {
	ctx := j.Context()
	if _, err := db.ExecContext(ctx, "STOP REPLICA IO_THREAD"); err != nil {
		return 0, "", fmt.Errorf("STOP REPLICA IO_THREAD: %w", err)
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return 0, "", err
	}
	if rs == nil {
		return 0, "", errNotReplica
	}
	retrieved := rs.RetrievedGTID
	pending := func() (string, int64, error) {
		var left string
		if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, @@global.gtid_executed)", retrieved).Scan(&left); err != nil {
			return "", 0, fmt.Errorf("bandingkan set GTID relay log: %w", err)
		}
		left = strings.ReplaceAll(left, "\n", "")
		n, err := gtidCount(left)
		return left, n, err
	}
	_, before, err := pending()
	if err != nil {
		return 0, "", err
	}
	if before == 0 {
		j.Logf("info", "relay log sudah diterapkan seluruhnya")
		return 0, "", nil
	}
	j.Logf("info", "menerapkan %d transaksi relay log yang tersisa", before)
	if rs.SQLRunning != "Yes" {
		if _, err := db.ExecContext(ctx, "START REPLICA SQL_THREAD"); err != nil {
			return 0, "", fmt.Errorf("START REPLICA SQL_THREAD: %w", err)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	waited := make(chan error, 1)
	go func() {
		var r sql.NullInt64
		err := conn.QueryRowContext(wctx, "SELECT WAIT_FOR_EXECUTED_GTID_SET(?, ?)", retrieved, relayWaitSec).Scan(&r)
		if err == nil && r.Int64 != 0 {
			err = errRelayWaitTimeout
		}
		waited <- err
	}()
	giveUp := func(why, left string, n int64) (int, string, error) {
		j.Logf("warn", "transaksi yang belum diterapkan diabaikan (%s): %s", why, left)
		return int(before - n), left, nil
	}
	tick := time.NewTicker(relayLogEvery)
	defer tick.Stop()
	start := time.Now()
	lastExec, lastChange := "", start
	for {
		timedOut := false
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case err := <-waited:
			waited = nil
			switch {
			case errors.Is(err, errRelayWaitTimeout):
				timedOut = true
			case err != nil && ctx.Err() == nil:
				return 0, "", fmt.Errorf("tunggu relay log diterapkan: %w", err)
			}
		case <-tick.C:
		}
		left, n, err := pending()
		if err != nil {
			return 0, "", err
		}
		if n == 0 {
			j.Logf("info", "relay log diterapkan: %d transaksi", before)
			return int(before), "", nil
		}
		rs, err := currentStatus(ctx, db)
		if err != nil {
			return 0, "", err
		}
		switch {
		case rs == nil:
			return 0, "", errNotReplica
		case rs.LastSQLErrno != 0:
			return 0, "", sqlError(rs)
		case rs.SQLRunning != "Yes":
			return 0, "", errors.New("thread SQL replika berhenti sebelum relay log selesai diterapkan")
		}
		if timedOut || time.Since(start) > time.Duration(relayWaitSec)*time.Second {
			return giveUp(fmt.Sprintf("batas waktu %d detik habis", relayWaitSec), left, n)
		}
		// Progress is any change of gtid_executed; the rest is given up
		// only once it stood still for relayStall with the coordinator
		// and every worker idle.
		exec, err := executedSet(ctx, db)
		if err != nil {
			return 0, "", err
		}
		if exec != lastExec {
			lastExec, lastChange = exec, time.Now()
		} else if time.Since(lastChange) >= relayStall {
			idle, err := applierIdle(ctx, db)
			if err != nil {
				return 0, "", err
			}
			if idle {
				return giveUp("relay log sudah habis dibaca", left, n)
			}
		}
		msg := fmt.Sprintf("menerapkan relay log: %d dari %d transaksi, %d tersisa", before-n, before, n)
		j.Logf("info", "%s", msg)
		j.Progress(percentOf(uint64(before-n), uint64(before)), msg)
	}
}

// applierIdle reports whether the applier is idle: the coordinator (or
// the single applier thread) says it has read all the relay log and
// every worker of a multi-threaded applier waits for the coordinator.
func applierIdle(ctx context.Context, db *sql.DB) (bool, error) {
	var coordIdle, busyWorkers int
	err := db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(NAME IN ('thread/sql/replica_sql', 'thread/sql/slave_sql') AND PROCESSLIST_STATE LIKE '%has read all relay log%'), 0),
		COALESCE(SUM(NAME IN ('thread/sql/replica_worker', 'thread/sql/slave_worker')
			AND COALESCE(PROCESSLIST_STATE, '') <> 'Waiting for an event from Coordinator'), 0)
		FROM performance_schema.threads`).Scan(&coordIdle, &busyWorkers)
	if err != nil {
		return false, fmt.Errorf("baca keadaan thread SQL replika: %w", err)
	}
	return coordIdle > 0 && busyWorkers == 0, nil
}

// finishPromote is step 6, run with writes open.
func (d *Driver) finishPromote(j *jobs.Job, h Host, db *sql.DB, p proto.PromoteParams, mode string, res *proto.PromoteResult) error {
	// Past the point of no return: a cancelled job still finishes.
	ctx := context.WithoutCancel(j.Context())
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return err
	}
	if rs != nil {
		for _, q := range []string{"STOP REPLICA", "RESET REPLICA ALL"} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		j.Logf("info", "konfigurasi replikasi dihapus (RESET REPLICA ALL)")
	}
	if _, err := db.ExecContext(ctx, filterStatement(nil, quoteString)); err != nil {
		return fmt.Errorf("hapus filter replikasi: %w", err)
	}
	if err := ensureReplUser(ctx, j, h, db, p.ReplPassword); err != nil {
		return err
	}
	var set string
	if err := db.QueryRowContext(ctx, "SELECT @@global.gtid_executed, @@global.server_uuid").Scan(&set, &res.ServerUUID); err != nil {
		return fmt.Errorf("baca gtid_executed: %w", err)
	}
	res.GTIDExecuted = strings.ReplaceAll(set, "\n", "")
	if err := d.pins.PromoteToMaster(); err != nil {
		return fmt.Errorf("server ini sudah bisa ditulis, tetapi pin master gagal ditulis: %w", err)
	}
	pr := Promoted{At: d.now().UTC(), Mode: mode, OldSource: peerKey(p.OldSource), GTIDExecuted: res.GTIDExecuted}
	res.Mode = mode
	if err := d.pins.WritePromoted(pr); err != nil {
		return fmt.Errorf("server ini sudah dipin master, tetapi catatan promosi gagal ditulis: %w", err)
	}
	if err := d.pins.ClearPromoting(); err != nil {
		return fmt.Errorf("server ini sudah master, tetapi catatan promote berjalan gagal dihapus: %w", err)
	}
	j.Logf("info", "server ini sekarang master")
	return nil
}

// ensureReplUser makes dbpanel_repl@'%' for this server's replicas when no
// row of it exists, on this server only (no binary log): a replica made
// from the old master already has it.
func ensureReplUser(ctx context.Context, j *jobs.Job, h Host, db *sql.DB, password string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	hosts, err := accountHosts(ctx, conn, replUserName)
	conn.Close()
	if err != nil {
		return fmt.Errorf("baca user replikasi: %w", err)
	}
	if len(hosts) > 0 {
		j.Logf("info", "user replikasi %s sudah ada", replUserName)
		return nil
	}
	a := account{User: replUserName, Host: "%", Password: password}
	if err := createAccount(ctx, h, a, replGrants, "replikasi", accountMode{keepExisting: true, local: j.Logf}); err != nil {
		return err
	}
	j.Logf("info", "user replikasi %s@'%%' dibuat", replUserName)
	return nil
}
