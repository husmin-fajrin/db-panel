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
//  1. Writes already open (read_only and super_read_only off): an earlier
//     run passed the point of no return; go to 6 (Resumed).
//  2. planned: the old master answers, is super_read_only and holds no
//     transaction this server lacks. emergency: three probes of the old
//     master, 10 s apart, all fail and the replica's receiver is not
//     connected; an old master that answers read-only continues as
//     planned, a writable one refuses.
//  3. STOP REPLICA IO_THREAD, then apply the relay log.
//  4. planned: the lock's deadline is at least 2 minutes away and the old
//     master is still read-only with nothing this server lacks.
//  5. STOP REPLICA, then SET PERSIST read_only/super_read_only OFF: the
//     point of no return.
//  6. RESET REPLICA ALL, the replication account for its own replicas,
//     pin master, write the promotion record.
//
// A failure from 3 up to 5 starts replication again; one after 5 is only
// reported, and mysql.promote run again resumes at 6.

const (
	// promoteLockMargin is the least time left on the old master's lock
	// for a planned promotion to open writes.
	promoteLockMargin = 2 * time.Minute
	// emergencyProbes is how many probes of the old master must all fail.
	emergencyProbes = 3
	// relayWaitSec is the WAIT_FOR_EXECUTED_GTID_SET timeout.
	relayWaitSec = 600
	// relayLogEvery is how often applying the relay log is reported.
	relayLogEvery = 5 * time.Second
)

// probeSpacing is the pause between two emergency probes; tests shorten
// it.
var probeSpacing = 10 * time.Second

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
		if err := d.allow("mysql.promote", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if d.Jobs == nil {
			return nil, errors.New("agent tidak bisa menjalankan pekerjaan latar")
		}
		old := peerKey(p.OldSource)
		id, err := d.Jobs.Start("mysql.promote", func(j *jobs.Job) (any, error) {
			res, err := d.promote(j, h, p)
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
		host, port, ok := "", 0, false
		if d.pins != nil {
			host, port, ok = d.pins.Source()
		}
		if !ok {
			return nil, &commands.ParamError{Msg: "server ini tidak punya sumber replikasi yang dipin"}
		}
		res := proto.ProbeSourceResult{Host: host, Port: port, Reachable: true}
		if err := probePeer(ctx, host, port, peerTimeout); err != nil {
			res.Reachable, res.Err = false, err.Error()
		}
		return res, nil
	})
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

// promote runs mysql.promote's steps (see above) in job j.
func (d *Driver) promote(j *jobs.Job, h Host, p proto.PromoteParams) (proto.PromoteResult, error) {
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
	if !ro && !sro {
		// Never opens writes itself: an earlier run already did, after
		// its verification.
		j.Logf("warn", "server ini sudah bisa ditulis: promote sebelumnya sudah melewati titik tanpa balik; dilanjutkan tanpa verifikasi ulang")
		res.Resumed = true
	} else {
		applied, err := d.openWrites(j, db, p)
		if err != nil {
			return res, err
		}
		res.Applied = applied
	}
	if err := d.finishPromote(j, h, db, p, &res); err != nil {
		return res, err
	}
	return res, nil
}

// openWrites runs steps 2 to 5 and returns how many relay log
// transactions were applied. Writes are open when it returns nil.
func (d *Driver) openWrites(j *jobs.Job, db *sql.DB, p proto.PromoteParams) (int, error) {
	ctx := j.Context()
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return 0, err
	}
	if rs == nil {
		return 0, &commands.ParamError{Msg: "replikasi tidak dikonfigurasi di server ini; tidak ada yang bisa dipromosikan"}
	}
	planned, err := d.verifyOldSource(j, db, p)
	if err != nil {
		return 0, err
	}

	// From here replication is changed: a failure starts it again.
	restore := func(cause error) error {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if _, err := db.ExecContext(rctx, "START REPLICA"); err != nil {
			j.Logf("error", "START REPLICA gagal: %v", err)
			return fmt.Errorf("%w; replikasi juga gagal dijalankan kembali (START REPLICA: %v)", cause, err)
		}
		j.Logf("info", "promote dibatalkan; replikasi dijalankan kembali")
		return cause
	}
	applied, err := applyRelayLog(j, db)
	if err != nil {
		return 0, restore(err)
	}
	if planned {
		if err := d.recheckOldSource(j, db, p); err != nil {
			return 0, restore(err)
		}
	}
	if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
		return 0, restore(fmt.Errorf("STOP REPLICA: %w", err))
	}
	// The point of no return: no rollback after this, whatever fails.
	j.Logf("info", "membuka tulis di server ini (titik tanpa balik)")
	for _, q := range []string{"SET PERSIST read_only = OFF", "SET PERSIST super_read_only = OFF"} {
		if _, err := db.ExecContext(context.WithoutCancel(ctx), q); err != nil {
			return 0, fmt.Errorf("%s: %w; replikasi sudah dihentikan, jalankan promote lagi untuk melanjutkan", q, err)
		}
	}
	return applied, nil
}

// verifyOldSource is step 2. planned reports whether the old master
// answered read-only (planned, or an emergency whose master answered),
// which step 4 checks again.
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
		st := readPeer(ctx, old, peerTimeout)
		switch {
		case !st.Reachable:
			return false, &commands.ParamError{Msg: fmt.Sprintf("master lama %s masih menjawab dari replika ini tetapi tidak bisa dibaca (%s); "+
				"promote darurat ditolak. Gunakan promote terencana, atau kunci master dari servernya dengan `sudo dbpanel agent lock-master` "+
				"bila agent-nya tidak bisa dipulihkan", key, st.Err)}
		case !st.SuperReadOnly:
			return false, &commands.ParamError{Msg: "master masih menjawab dari replika ini; gunakan promote terencana, atau kunci master dari " +
				"servernya dengan `sudo dbpanel agent lock-master` bila agent-nya tidak bisa dipulihkan"}
		}
		j.Logf("info", "master lama %s masih menjawab dan read-only: dilanjutkan sebagai promote terencana", key)
		return true, caughtUp(ctx, db, key, st.GTIDExecuted)
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

// recheckOldSource is step 4: the lock's deadline is not too close and
// the old master is still read-only with nothing this server lacks.
func (d *Driver) recheckOldSource(j *jobs.Job, db *sql.DB, p proto.PromoteParams) error {
	ctx := j.Context()
	key := peerKey(p.OldSource)
	if p.LockDeadline != nil && p.LockDeadline.Sub(d.now()) < promoteLockMargin {
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
// relayLogEvery. It returns how many transactions were applied.
func applyRelayLog(j *jobs.Job, db *sql.DB) (int, error) {
	ctx := j.Context()
	if _, err := db.ExecContext(ctx, "STOP REPLICA IO_THREAD"); err != nil {
		return 0, fmt.Errorf("STOP REPLICA IO_THREAD: %w", err)
	}
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return 0, err
	}
	if rs == nil {
		return 0, errNotReplica
	}
	retrieved := rs.RetrievedGTID
	pending := func() (int64, error) {
		var left string
		if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, @@global.gtid_executed)", retrieved).Scan(&left); err != nil {
			return 0, fmt.Errorf("bandingkan set GTID relay log: %w", err)
		}
		return gtidCount(left)
	}
	before, err := pending()
	if err != nil {
		return 0, err
	}
	if before == 0 {
		j.Logf("info", "relay log sudah diterapkan seluruhnya")
		return 0, nil
	}
	j.Logf("info", "menerapkan %d transaksi relay log yang tersisa", before)
	if rs.SQLRunning != "Yes" {
		if _, err := db.ExecContext(ctx, "START REPLICA SQL_THREAD"); err != nil {
			return 0, fmt.Errorf("START REPLICA SQL_THREAD: %w", err)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
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
	tick := time.NewTicker(relayLogEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case err := <-waited:
			waited = nil
			switch {
			case errors.Is(err, errRelayWaitTimeout):
				j.Logf("warn", "relay log belum selesai diterapkan setelah %d detik; terus menunggu", relayWaitSec)
			case err != nil && ctx.Err() == nil:
				return 0, fmt.Errorf("tunggu relay log diterapkan: %w", err)
			}
		case <-tick.C:
		}
		left, err := pending()
		if err != nil {
			return 0, err
		}
		if left == 0 {
			j.Logf("info", "relay log diterapkan: %d transaksi", before)
			return int(before), nil
		}
		rs, err := currentStatus(ctx, db)
		if err != nil {
			return 0, err
		}
		switch {
		case rs == nil:
			return 0, errNotReplica
		case rs.LastSQLErrno != 0:
			return 0, sqlError(rs)
		case rs.SQLRunning != "Yes":
			return 0, errors.New("thread SQL replika berhenti sebelum relay log selesai diterapkan")
		}
		msg := fmt.Sprintf("menerapkan relay log: %d dari %d transaksi, %d tersisa", before-left, before, left)
		j.Logf("info", "%s", msg)
		j.Progress(percentOf(uint64(before-left), uint64(before)), msg)
	}
}

// finishPromote is step 6, run with writes open.
func (d *Driver) finishPromote(j *jobs.Job, h Host, db *sql.DB, p proto.PromoteParams, res *proto.PromoteResult) error {
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
	pr := Promoted{At: d.now().UTC(), Mode: p.Mode, OldSource: peerKey(p.OldSource), GTIDExecuted: res.GTIDExecuted}
	if err := d.pins.WritePromoted(pr); err != nil {
		return fmt.Errorf("server ini sudah dipin master, tetapi catatan promosi gagal ditulis: %w", err)
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
