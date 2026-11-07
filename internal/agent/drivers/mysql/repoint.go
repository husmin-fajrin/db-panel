package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// mysql.repoint (spec 5.1) points a replica whose master was replaced at
// the new master: a promoted replica of its pinned source. The agent
// verifies, so that a compromised panel can never point a replica
// anywhere else:
//
//  1. The pinned source is gone: three probes, probeSpacing apart, all
//     fail while this replica's receiver is not connected; or it answers
//     (ReadPeer with the new master's account) read-only.
//  2. The new master answers, is writable, and its gtid_executed holds
//     transactions of the old source. The old source's server_uuid is,
//     in this order: what ReadPeer read from it in 1; else Source_UUID of
//     this replica's channel (performance_schema.
//     replication_connection_status, what SHOW REPLICA STATUS shows);
//     else, when that is empty (MySQL restarted since the receiver last
//     connected), every server_uuid in this replica's gtid_executed but
//     its own, all of which the new master must hold.
//  3. STOP REPLICA, so that nothing is applied after the next reading;
//     then errant = GTID_SUBTRACT(this replica's gtid_executed, the new
//     master's), computed here. Not empty: the source pin moves to the
//     new master (so a rebuild from it is accepted), replication is
//     started again when it ran (left as it was) and NeedsRebuild is
//     answered.
//  4. Otherwise the CA file is written when Verify needs it and it is
//     absent; the channel is pointed at the new master (SSL, verified
//     when Verify); a failure up to here starts replication on the old
//     channel again. START REPLICA; the source pin moves; the receiver is
//     watched for up to repointIOWait. A connection error MySQL retries
//     (network, refused login while the new master warms up) is reported
//     in IOError with IORunning false; any other receiver error is
//     returned. Either way replication stays on the new master (the
//     panel decides).

// repointIOWait bounds the wait for the receiver after START REPLICA.
var repointIOWait = 15 * time.Second

// repointHook, when set, is called at the stage "after-errant-check";
// tests use it to let an applier backlog run there.
var repointHook func(stage string)

func repointPoint(stage string) {
	if repointHook != nil {
		repointHook(stage)
	}
}

func (d *Driver) registerRepoint(reg *commands.Registry) {
	reg.Register("mysql.repoint", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.RepointParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePeer(&p.NewMaster, "master baru:"); err != nil {
			return nil, err
		}
		var caPEM []byte
		if p.Verify && p.CAPEM != "" {
			ca, err := parseCA(p.CAPEM)
			if err != nil {
				return nil, err
			}
			caPEM = ca.pem
		}
		if err := validateFilter(p.Databases); err != nil {
			return nil, err
		}
		if d.pins == nil {
			return nil, &commands.ParamError{Msg: "agent ini tidak punya pin peran; repoint ditolak"}
		}
		if err := d.allow("mysql.repoint", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.promoteMu.Lock()
		defer d.promoteMu.Unlock()
		res, err := d.repoint(ctx, h, p, caPEM)
		key := peerKey(p.NewMaster)
		if err != nil {
			d.warn("mysql repoint failed", "new_source", key, "err", err)
			return nil, err
		}
		d.log("mysql repointed", "new_source", key, "needs_rebuild", res.NeedsRebuild, "errant", res.Errant, "io_running", res.IORunning, "io_error", res.IOError)
		return res, nil
	})
}

// repoint runs mysql.repoint's steps (see above). The caller holds
// promoteMu.
func (d *Driver) repoint(ctx context.Context, h Host, p proto.RepointParams, caPEM []byte) (proto.RepointResult, error) {
	var res proto.RepointResult
	nm := p.NewMaster
	key := peerKey(nm)
	srcHost, srcPort, ok := d.pins.Source()
	if !ok {
		return res, &commands.ParamError{Msg: "server ini tidak punya sumber replikasi yang dipin; repoint ditolak"}
	}
	oldKey := net.JoinHostPort(srcHost, strconv.Itoa(srcPort))
	if strings.EqualFold(srcHost, nm.Host) && srcPort == nm.Port {
		return res, &commands.ParamError{Msg: fmt.Sprintf("master baru %s adalah sumber yang sudah dipin; tidak ada yang diarahkan ulang", key)}
	}

	// The channel's settings, checked before anything is changed.
	src := proto.ReplicationSourceParams{Host: nm.Host, Port: nm.Port, User: nm.User, Password: nm.Password,
		SSL: true, VerifyServerCert: p.Verify, Databases: p.Databases}
	caPath := filepath.Join(TLSDir(h.Layout()), tlsCAFile)
	writeCA := false
	if p.Verify {
		src.SSLCA = caPath
		if _, err := os.Stat(caPath); errors.Is(err, os.ErrNotExist) {
			if caPEM == nil {
				return res, &commands.ParamError{Msg: fmt.Sprintf("verifikasi sertifikat butuh CA, tetapi %s belum ada dan CA tidak dikirim", caPath)}
			}
			writeCA = true
		} else if err != nil {
			return res, fmt.Errorf("periksa berkas CA %s: %w", caPath, err)
		}
	}
	if err := validateSource(&src); err != nil {
		return res, err
	}

	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	rs, err := currentStatus(ctx, db)
	if err != nil {
		return res, err
	}
	if rs == nil {
		return res, &commands.ParamError{Msg: "replikasi tidak dikonfigurasi di server ini; tidak ada yang diarahkan ulang"}
	}

	old := nm
	old.Host, old.Port = srcHost, srcPort
	oldUUID, err := sourceGone(ctx, db, old)
	if err != nil {
		return res, err
	}
	oldUUIDs, err := oldSourceUUIDs(ctx, db, oldUUID)
	if err != nil {
		return res, err
	}

	st := readPeer(ctx, nm, peerTimeout)
	switch {
	case !st.Reachable:
		return res, &commands.ParamError{Msg: fmt.Sprintf("master baru %s tidak bisa dibaca (%s); repoint ditolak", key, st.Err)}
	case st.SuperReadOnly:
		return res, &commands.ParamError{Msg: fmt.Sprintf(
			"master baru %s masih read-only (super_read_only=ON), belum dipromosikan; repoint ditolak", key)}
	}
	for _, u := range oldUUIDs {
		if !gtidHasUUID(st.GTIDExecuted, u) {
			return res, &commands.ParamError{Msg: fmt.Sprintf(
				"gtid_executed master baru %s tidak memuat transaksi sumber lama %s (%s); ia bukan replika sumber lama yang dipromosikan, repoint ditolak",
				key, oldKey, u)}
		}
	}

	// The applier is stopped before gtid_executed is read: a transaction
	// applied after the reading would be an errant one nobody checked.
	// From here every way out but the new channel's START REPLICA starts
	// replication again when it ran before (resume).
	wasRunning := rs.IORunning == "Yes" || rs.IORunning == "Connecting" || rs.SQLRunning == "Yes"
	if _, err := db.ExecContext(ctx, "STOP REPLICA"); err != nil {
		return res, fmt.Errorf("STOP REPLICA: %w", err)
	}
	resume := func(cause error) error {
		if !wasRunning {
			return cause
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if _, err := db.ExecContext(rctx, "START REPLICA"); err != nil {
			if cause == nil {
				return fmt.Errorf("replikasi gagal dijalankan kembali (START REPLICA: %v)", err)
			}
			return fmt.Errorf("%w; replikasi juga gagal dijalankan kembali (START REPLICA: %v)", cause, err)
		}
		return cause
	}
	var errant string
	if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", st.GTIDExecuted).Scan(&errant); err != nil {
		return res, resume(fmt.Errorf("bandingkan set GTID dengan master baru: %w", err))
	}
	repointPoint("after-errant-check")
	if errant = strings.ReplaceAll(errant, "\n", ""); errant != "" {
		if err := d.pins.SetSource(nm.Host, nm.Port); err != nil {
			return res, resume(fmt.Errorf("pin sumber gagal dipindah ke %s: %w", key, err))
		}
		d.warn("replica holds transactions the new master lacks; source pin moved, rebuild needed", "new_source", key, "errant", errant)
		if err := resume(nil); err != nil {
			return res, fmt.Errorf("pin sumber sudah dipindah ke %s (perlu dibangun ulang, errant %s), tetapi %w", key, errant, err)
		}
		return proto.RepointResult{NeedsRebuild: true, Errant: errant}, nil
	}

	if writeCA {
		d.tlsMu.Lock()
		_, err := trustTLS(h.Layout(), caPEM)
		d.tlsMu.Unlock()
		if err != nil {
			return res, resume(fmt.Errorf("simpan CA: %w", err))
		}
	}
	if _, err := ConfigureReplication(ctx, h, src); err != nil {
		return res, resume(err)
	}
	if _, err := db.ExecContext(ctx, "START REPLICA"); err != nil {
		return res, fmt.Errorf("replikasi sudah diarahkan ke %s, tetapi START REPLICA gagal: %w", key, err)
	}
	if err := d.pins.SetSource(nm.Host, nm.Port); err != nil {
		return res, fmt.Errorf("replikasi sudah diarahkan ke %s, tetapi pin sumber gagal dipindah: %w", key, err)
	}
	res.IORunning, res.IOError, err = awaitReceiver(ctx, db)
	if err != nil {
		return res, fmt.Errorf("replikasi sudah diarahkan ke %s dan pin sumber dipindah, tetapi: %w", key, err)
	}
	return res, nil
}

// sourceGone is step 1 for old, the pinned source with the new master's
// account. It returns the old source's server_uuid when it answered
// read-only, "" when it is down.
func sourceGone(ctx context.Context, db *sql.DB, old proto.PeerCred) (string, error) {
	key := peerKey(old)
	answered := false
	for i := range emergencyProbes {
		if i > 0 {
			if err := pause(ctx, probeSpacing); err != nil {
				return "", err
			}
		}
		if probePeer(ctx, old.Host, old.Port, peerTimeout) == nil {
			answered = true
			break
		}
	}
	if !answered {
		rs, err := currentStatus(ctx, db)
		if err != nil {
			return "", err
		}
		if rs != nil && rs.IORunning == "Yes" {
			return "", &commands.ParamError{Msg: fmt.Sprintf(
				"sumber %s tidak menjawab dari agent, tetapi thread IO replika ini masih tersambung kepadanya; repoint ditolak", key)}
		}
		return "", nil
	}
	st := readPeer(ctx, old, peerTimeout)
	switch {
	case !st.Reachable:
		return "", &commands.ParamError{Msg: fmt.Sprintf(
			"sumber %s masih menjawab tetapi tidak bisa dibaca (%s); repoint ditolak", key, st.Err)}
	case !st.SuperReadOnly:
		return "", &commands.ParamError{Msg: fmt.Sprintf(
			"sumber %s masih bisa ditulis (super_read_only=OFF); repoint ditolak sampai sumber itu dikunci atau mati", key)}
	}
	return st.ServerUUID, nil
}

// oldSourceUUIDs is the old source's server_uuid as step 2 finds it: known
// (from ReadPeer), else this replica's Source_UUID, else every server_uuid
// in this replica's gtid_executed but its own.
func oldSourceUUIDs(ctx context.Context, db *sql.DB, known string) ([]string, error) {
	if known != "" {
		return []string{known}, nil
	}
	var srcUUID sql.NullString
	err := db.QueryRowContext(ctx, "SELECT SOURCE_UUID FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME = ''").Scan(&srcUUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("baca Source_UUID replika: %w", err)
	}
	if u := strings.TrimSpace(srcUUID.String); u != "" {
		return []string{u}, nil
	}
	var own, executed string
	if err := db.QueryRowContext(ctx, "SELECT @@global.server_uuid, @@global.gtid_executed").Scan(&own, &executed); err != nil {
		return nil, fmt.Errorf("baca gtid_executed: %w", err)
	}
	var uuids []string
	for _, entry := range strings.Split(strings.ReplaceAll(executed, "\n", ""), ",") {
		u, rest, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if ok && rest != "" && !strings.EqualFold(u, own) {
			uuids = append(uuids, u)
		}
	}
	if len(uuids) == 0 {
		return nil, &commands.ParamError{Msg: "server_uuid sumber lama tidak diketahui (Source_UUID kosong dan gtid_executed replika ini tidak memuat transaksi sumber lain); repoint ditolak"}
	}
	return uuids, nil
}

// retryableIOErrnos are receiver errors MySQL keeps retrying: the new
// master not reachable yet, the connection lost, a login refused while
// its accounts are being set up.
var retryableIOErrnos = map[int]bool{1045: true, 2003: true, 2005: true, 2013: true}

// awaitReceiver watches the receiver for up to repointIOWait: running
// once it runs; still connecting at the end, or failing with a retryable
// error (ioErr, its text), not running; any other receiver error is
// returned.
func awaitReceiver(ctx context.Context, db *sql.DB) (running bool, ioErr string, err error) {
	deadline := time.Now().Add(repointIOWait)
	for {
		rs, err := currentStatus(ctx, db)
		switch {
		case err != nil:
			return false, "", err
		case rs == nil:
			return false, "", errNotReplica
		case rs.IORunning == "Yes" && rs.LastIOErrno == 0:
			return true, "", nil
		case rs.LastIOErrno != 0 && !retryableIOErrnos[rs.LastIOErrno]:
			return false, "", ioError(rs)
		case time.Now().After(deadline):
			if rs.LastIOErrno != 0 {
				return false, ioError(rs).Error(), nil
			}
			return false, "", nil
		}
		if err := pause(ctx, ReadyPollInterval); err != nil {
			return false, "", err
		}
	}
}
