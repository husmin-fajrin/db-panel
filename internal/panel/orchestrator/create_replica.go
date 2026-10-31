package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// CreateReplicaParams are what the user chose in "Buat replika".
type CreateReplicaParams struct {
	MasterServerID, TargetServerID int64
	Databases                      []string // empty = all
	ForceDump                      bool     // dump even when CLONE is possible
	// ConfirmWipe: the user agreed on the form that CLONE replaces the
	// target. It only pre-approves the CLONE wait; databases left on the
	// target are confirmed in the job, against the list it shows.
	ConfirmWipe bool
}

func createReplicaStepNames() []string {
	return []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL", "Siapkan replika",
		"Salin data", "Sambungkan replikasi", "Verifikasi"}
}

// Index of the first step after which the target may have replication
// configured.
const stepConnect = 5

// StartCreateReplica starts the create_replica job and registers the target
// as a replica in state creating. It refuses a master that is not ready,
// the master itself as target, a target that already has a role or an
// unfinished job, and databases the master does not have. Selecting every
// database of the master means all (no filter).
func (r *Runner) StartCreateReplica(ctx context.Context, p CreateReplicaParams) (string, error) {
	if p.MasterServerID == p.TargetServerID {
		return "", errors.New("server tujuan tidak boleh sama dengan master")
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if _, err := r.d.Store.GetServer(p.MasterServerID); err != nil {
		return "", fmt.Errorf("master: %w", err)
	}
	target, err := r.d.Store.GetServer(p.TargetServerID)
	if err != nil {
		return "", fmt.Errorf("server tujuan: %w", err)
	}
	// A master still being prepared (for example waiting for its restart)
	// is not ready to copy from.
	if j, busy, err := r.d.Store.ActiveJobForServer(p.MasterServerID); err != nil {
		return "", err
	} else if busy && j.Kind == KindPrepareMaster {
		return "", errors.New("master masih menjalankan Siapkan master; selesaikan pekerjaan itu dulu")
	}
	if err := r.refuseUnconnectedTarget(target); err != nil {
		return "", err
	}
	inv, err := r.d.Store.GetInventory(p.MasterServerID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !inv.MasterReady) {
		return "", errors.New("master belum disiapkan; jalankan Siapkan master dulu")
	}
	if err != nil {
		return "", err
	}
	topo, err := r.d.Store.GetTopologyByMaster(p.MasterServerID)
	if err != nil {
		return "", fmt.Errorf("topologi master: %w", err)
	}
	if err := r.refuseBusy(target.ID, "server tujuan"); err != nil {
		return "", err
	}
	// A target whose earlier attempt failed is free for a new one.
	if rep, err := r.d.Store.GetReplicaByServer(target.ID); err == nil && rep.State == store.ReplicaFailed {
		if err := r.d.Store.RemoveReplica(target.ID); err != nil {
			return "", err
		}
		target.Role = store.RoleUnassigned
		r.log.Info("failed replica attempt removed for a new one", "server_id", target.ID)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if target.Role != store.RoleUnassigned {
		return "", fmt.Errorf("server tujuan sudah berperan sebagai %s", target.Role)
	}
	dbs, err := selectDatabases(p.Databases, inv.Databases)
	if err != nil {
		return "", err
	}
	if _, err := r.d.Store.AddReplica(topo.ID, target.ID, dbs); err != nil {
		return "", err
	}
	st := jobState{MasterServerID: p.MasterServerID, Databases: dbs, ForceDump: p.ForceDump, ConfirmWipe: p.ConfirmWipe}
	id, err := r.createJob(KindCreateReplica, target.ID, topo.ID, st, createReplicaStepNames())
	if err != nil {
		if rerr := r.d.Store.RemoveReplica(target.ID); rerr != nil {
			r.log.Warn("undo replica registration", "server_id", target.ID, "err", rerr)
		}
		return "", err
	}
	return id, nil
}

// ErrTargetNotConnected refuses a target whose MySQL is installed but not
// connected: every step after the checks logs in with the stored account.
var ErrTargetNotConnected = errors.New("MySQL di server tujuan sudah terpasang tetapi belum dihubungkan; " +
	"buka halaman server tujuan dan jalankan Hubungkan MySQL dulu")

// refuseUnconnectedTarget refuses srv when its last probe found MySQL
// installed and its MySQL was never connected. A target without MySQL is
// fine: the job installs it and stores the account itself.
func (r *Runner) refuseUnconnectedTarget(srv store.Server) error {
	if !ProbeSaysMySQLInstalled(srv) {
		return nil
	}
	inv, err := r.d.Store.GetInventory(srv.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !inv.Connected) {
		return ErrTargetNotConnected
	}
	return err
}

// ProbeSaysMySQLInstalled reports whether srv's last probe found MySQL.
func ProbeSaysMySQLInstalled(srv store.Server) bool {
	if srv.ProbeJSON == "" {
		return false
	}
	var pr proto.ProbeResult
	if err := json.Unmarshal([]byte(srv.ProbeJSON), &pr); err != nil {
		return false
	}
	return pr.MySQL.Installed
}

// selectDatabases checks the selection against the master's databases and
// returns it sorted without duplicates; nil when it covers all of them.
func selectDatabases(selected []string, onMaster []proto.DatabaseInfo) ([]string, error) {
	have := map[string]bool{}
	for _, d := range onMaster {
		have[d.Name] = true
	}
	var out []string
	for _, d := range selected {
		if !have[d] {
			return nil, fmt.Errorf("database %q tidak ada di master", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) == len(have) {
		return nil, nil
	}
	slices.Sort(out)
	return out, nil
}

func (jr *jobRun) createReplicaSteps() []step {
	n := createReplicaStepNames()
	return []step{
		{n[0], jr.replicaCheckMaster},
		{n[1], jr.replicaCheckTarget},
		{n[2], jr.replicaInstall},
		{n[3], jr.replicaPrepare},
		{n[4], jr.replicaSnapshot},
		{n[5], jr.replicaConnect},
		{n[6], jr.replicaVerify},
	}
}

func (jr *jobRun) master() int64 { return jr.st.MasterServerID }
func (jr *jobRun) target() int64 { return jr.job.ServerID }

// replicaCheckMaster: the master is still ready; it records what the
// replica's checks need (server_uuid, version, databases and their size).
func (jr *jobRun) replicaCheckMaster(ctx context.Context, _ bool) (outcome, error) {
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.master(), "mysql.status", nil, &st); err != nil {
		return outcome{}, err
	}
	if p := masterProblems(st); p != "" {
		return outcome{}, errors.New("master tidak lagi siap: " + p + "; jalankan Siapkan master lagi")
	}
	if st.ServerUUID == "" {
		return outcome{}, errors.New("server_uuid master tidak terbaca")
	}
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return outcome{}, err
	}
	if topo.SourceHost == "" || topo.SourcePort == 0 {
		return outcome{}, errors.New("alamat MySQL master belum diketahui; jalankan Siapkan master lagi")
	}
	inv, err := jr.r.d.Store.GetInventory(jr.master())
	if err != nil {
		return outcome{}, err
	}
	sizes := map[string]uint64{}
	var names []string
	for _, d := range inv.Databases {
		sizes[d.Name] = d.Bytes
		names = append(names, d.Name)
	}
	slices.Sort(names)
	selected := jr.st.Databases
	if len(selected) == 0 {
		selected = names
	}
	var bytes uint64
	for _, d := range selected {
		size, ok := sizes[d]
		if !ok {
			return outcome{}, fmt.Errorf("database %s tidak ada lagi di master", d)
		}
		bytes += size
	}
	series := st.Series
	if series == "" {
		series = inv.Series
	}
	jr.st.MasterUUID, jr.st.MasterMySQLID, jr.st.MasterVersion, jr.st.MasterSeries = st.ServerUUID, st.ServerID, st.Version, series
	jr.st.PeerDatabases, jr.st.DataBytes = names, bytes
	jr.st.MasterTLS = st.TLSAvailable
	lctn := st.LowerCaseTableNames
	jr.st.MasterLCTN = &lctn
	jr.save()
	if !st.TLSAvailable {
		jr.logf("warn", "Master tidak menawarkan TLS: replikasi dan salinan data berjalan tanpa enkripsi.")
	}
	return done("master %s:%d siap (MySQL %s)", topo.SourceHost, topo.SourcePort, st.Version), nil
}

// replicaCheckTarget probes the target; without MySQL the install step
// installs it and runs the replica checks itself.
func (jr *jobRun) replicaCheckTarget(ctx context.Context, approved bool) (outcome, error) {
	var pr proto.ProbeResult
	if err := jr.call(ctx, jr.target(), "probe", nil, &pr); err != nil {
		return outcome{}, err
	}
	jr.st.NeedsInstall = !pr.MySQL.Installed
	jr.save()
	if jr.st.NeedsInstall {
		jr.logf("info", "MySQL belum terpasang di %s; akan dipasang (seri %s).", jr.serverName(jr.target()), jr.st.MasterSeries)
		return done("MySQL belum terpasang; akan dipasang"), nil
	}
	return jr.replicaPreflight(ctx, approved)
}

// replicaPreflight runs mysql.preflight for the replica role and picks the
// copy method. Databases on the target that the master also has (left
// from an earlier attempt) are shown to the user; once the user approved
// that exact list it is kept for the snapshot step, which drops it before
// a dump. The form's ConfirmWipe never stands in for this approval.
func (jr *jobRun) replicaPreflight(ctx context.Context, approved bool) (outcome, error) {
	if jr.st.MasterUUID == "" {
		// Without it the agent cannot refuse the master as its own replica.
		return outcome{}, errors.New("server_uuid master belum diketahui; ulangi dari tahap Periksa master")
	}
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return outcome{}, err
	}
	preflight := func(confirm bool) (proto.PreflightResult, error) {
		var res proto.PreflightResult
		err := jr.call(ctx, jr.target(), "mysql.preflight", proto.PreflightParams{
			Role: "replica", PeerHost: topo.SourceHost, PeerPort: topo.SourcePort, PeerVersion: jr.st.MasterVersion,
			PeerServerUUID: jr.st.MasterUUID, PeerDataBytes: jr.st.DataBytes, PeerDatabases: jr.st.PeerDatabases,
			Databases: jr.st.Databases, ConfirmWipe: confirm, PeerLowerCaseTableNames: jr.st.MasterLCTN,
		}, &res)
		return res, err
	}
	res, err := preflight(false)
	if err != nil {
		return outcome{}, err
	}
	jr.st.WipeList = nil
	if res.Worst != proto.CheckFail {
		// What the next steps would remove from the target is shown first:
		// databases an earlier attempt left (dropped before the copy), and
		// replication from another source (reset in Siapkan replika). The
		// approval is bound to exactly what was shown.
		var list []string
		if hasCheck(res.Checks, "leftover", proto.CheckNeedsConfirm) {
			if list, err = jr.leftoverDatabases(ctx); err != nil {
				return outcome{}, err
			}
		}
		foreign := foreignSource(res.Status.Replica, topo)
		if len(list) > 0 || foreign != "" {
			key := slices.Clone(list)
			if foreign != "" {
				key = append(key, "source:"+foreign)
			}
			h := hashList(key)
			if !approved || h != jr.st.WipeHash {
				jr.st.WipeHash = h
				var msg []string
				if len(list) > 0 {
					jr.st.WaitReason = WaitWipe
					msg = append(msg, "database dari percobaan sebelumnya akan dihapus lalu disalin ulang dari master: "+
						strings.Join(list, ", "))
				} else {
					jr.st.WaitReason = WaitForeignSource
				}
				if foreign != "" {
					msg = append(msg, fmt.Sprintf("server tujuan sedang diatur sebagai replika dari %s; pengaturan replikasi "+
						"itu akan dihapus (server tujuan tidak lagi mengikuti %s)", foreign, foreign))
				}
				jr.save()
				return wait(strings.Join(msg, ".\n")), nil
			}
			if len(list) > 0 {
				jr.logf("warn", "Pengguna menyetujui penghapusan database: %s.", strings.Join(list, ", "))
			}
			if foreign != "" {
				jr.logf("warn", "Pengguna menyetujui penghapusan pengaturan replikasi dari %s di server tujuan.", foreign)
			}
			jr.st.WipeList = list
			jr.save()
			if len(list) > 0 {
				if res, err = preflight(true); err != nil {
					return outcome{}, err
				}
			}
		}
	}
	jr.logChecks(res.Checks)
	if res.Worst == proto.CheckFail {
		return outcome{}, errors.New(failedChecks(res.Checks, proto.CheckFail))
	}
	if left := failedChecks(res.Checks, proto.CheckNeedsConfirm); left != "" {
		return outcome{}, errors.New("masih perlu konfirmasi: " + left)
	}
	method := res.Method
	if jr.st.ForceDump {
		method = "dump"
	}
	if method != "clone" && method != "dump" {
		return outcome{}, errors.New("tidak ada cara salinan yang bisa dipakai untuk server ini")
	}
	jr.st.Method = method
	jr.save()
	jr.logf("info", "Metode salinan awal: %s.", method)
	return done("metode salinan: %s", method), nil
}

// foreignSource is the target's replication source as host:port when it is
// configured and is not this topology's master (one an earlier attempt of
// this job set up is not foreign); "" otherwise.
func foreignSource(rs *proto.ReplicaStatus, topo store.Topology) string {
	if rs == nil || rs.SourceHost == "" {
		return ""
	}
	if strings.EqualFold(rs.SourceHost, topo.SourceHost) && rs.SourcePort == topo.SourcePort {
		return ""
	}
	return net.JoinHostPort(rs.SourceHost, strconv.Itoa(rs.SourcePort))
}

func hasCheck(checks []proto.Check, id, status string) bool {
	return slices.ContainsFunc(checks, func(c proto.Check) bool { return c.ID == id && c.Status == status })
}

// leftoverDatabases are the target's databases that the master also has,
// sorted; it refreshes the target's inventory on the way.
func (jr *jobRun) leftoverDatabases(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := jr.r.RefreshInventory(cctx, jr.target())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range res.Databases {
		if slices.Contains(jr.st.PeerDatabases, d.Name) {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// hashList binds a confirmation to the exact list shown.
func hashList(list []string) string {
	sum := sha256.Sum256([]byte(strings.Join(list, "\n")))
	return hex.EncodeToString(sum[:])
}

func (jr *jobRun) replicaInstall(ctx context.Context, approved bool) (outcome, error) {
	if !jr.st.NeedsInstall {
		return skipped("MySQL sudah terpasang"), nil
	}
	// A retry after the install itself succeeded skips straight to the checks.
	var pr proto.ProbeResult
	if err := jr.call(ctx, jr.target(), "probe", nil, &pr); err != nil {
		return outcome{}, err
	}
	if !pr.MySQL.Installed {
		if jr.st.MasterSeries == "" {
			return outcome{}, errors.New("seri MySQL master tidak diketahui")
		}
		jr.logf("info", "Memasang MySQL seri %s di %s.", jr.st.MasterSeries, jr.serverName(jr.target()))
		var res proto.InstallResult
		if err := jr.runAgentJob(ctx, jr.target(), "mysql.install",
			proto.InstallParams{Series: jr.st.MasterSeries, ExactVersion: jr.st.MasterVersion}, &res); err != nil {
			return outcome{}, err
		}
		jr.logf("info", "MySQL %s terpasang (%s).", res.Version, res.Source)
	}
	// The installer stored the dbpanel_admin account on the agent.
	if _, err := jr.r.RefreshInventory(ctx, jr.target()); err != nil {
		return outcome{}, err
	}
	return jr.replicaPreflight(ctx, approved)
}

func (jr *jobRun) replicaPrepare(ctx context.Context, _ bool) (outcome, error) {
	if jr.st.ReplicaServerID == 0 {
		id, err := replicaServerID(jr.st.MasterMySQLID)
		if err != nil {
			return outcome{}, err
		}
		jr.st.ReplicaServerID = id
		jr.save()
	}
	var res proto.ConfigureResult
	if err := jr.call(ctx, jr.target(), "mysql.configure", proto.ConfigureParams{
		Role: "replica", ServerID: jr.st.ReplicaServerID, EnableClone: jr.st.Method == "clone", GTID: true,
	}, &res); err != nil {
		return outcome{}, err
	}
	for _, a := range res.Applied {
		jr.logf("info", "Diterapkan: %s", a)
	}
	if res.ServerID != 0 && res.ServerID == jr.st.MasterMySQLID {
		return outcome{}, fmt.Errorf("server_id replika (%d) sama dengan master", res.ServerID)
	}
	if res.NeedsRestart {
		// The target serves no application yet, so it restarts unasked.
		jr.logf("info", "Memulai ulang MySQL di %s: %s.", jr.serverName(jr.target()), strings.Join(res.RestartReasons, "; "))
		var rr proto.RestartResult
		if err := jr.callWithin(ctx, restartTimeout, jr.target(), "mysql.restart", nil, &rr); err != nil {
			return outcome{}, err
		}
	}
	if err := jr.call(ctx, jr.target(), "mysql.setReadOnly", proto.ReadOnlyParams{On: true}, nil); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Server tujuan sekarang hanya-baca (super_read_only).")
	// A first snapshot starts from no replication state. A source left by
	// an earlier attempt would make the dump keep the target's old GTID
	// history (COMMENTED mode), and auto-positioning would then replay
	// transactions the fresh data already holds; its filter is stale too.
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.target(), "mysql.status", nil, &st); err != nil {
		return outcome{}, err
	}
	if st.Replica != nil {
		if err := jr.call(ctx, jr.target(), "mysql.resetReplica", nil, nil); err != nil {
			return outcome{}, err
		}
		jr.logf("info", "Pengaturan replikasi lama di server tujuan (dari percobaan sebelumnya) dihapus.")
	}
	return done("server_id %d, hanya-baca", jr.st.ReplicaServerID), nil
}

// replicaServerID picks a random server_id in 2..2^32-1 other than the
// master's.
func replicaServerID(master uint32) (uint32, error) {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if id := binary.BigEndian.Uint32(b[:]); id > 1 && id != master {
			return id, nil
		}
	}
}

func (jr *jobRun) replicaSnapshot(ctx context.Context, approved bool) (outcome, error) {
	if jr.st.AdminReplaced {
		// The copy is done; it waits for the user to reconnect.
		return jr.verifyCopyAfterReconnect(ctx)
	}
	name := jr.serverName(jr.target())
	// A clone target has no user databases (the agent picks clone only
	// then); the form's ConfirmWipe pre-approves replacing the rest.
	if jr.st.Method == "clone" && !jr.st.ConfirmWipe && !jr.st.CloneApproved {
		if !approved {
			jr.st.WaitReason = WaitClone
			jr.save()
			return wait(fmt.Sprintf("CLONE akan menghapus SEMUA database di %s. Lanjutkan?", name)), nil
		}
		jr.st.CloneApproved = true
		jr.save()
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	p := proto.SnapshotParams{Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password,
		EstimateBytes: jr.st.DataBytes, SSL: jr.st.MasterTLS}
	cmd := "mysql.cloneFrom"
	if jr.st.Method == "dump" {
		cmd = "mysql.dumpFrom"
		p.User, p.Password, p.Databases = "", "", jr.st.Databases
	}
	// An earlier attempt's copy still running (a retry, or a panel
	// restart) is waited for: nothing may be dropped under it.
	reattach := jr.earlierAgentJob(ctx, jr.target(), cmd)
	if cmd == "mysql.dumpFrom" {
		if len(jr.st.WipeList) > 0 && !reattach {
			// Every confirmed leftover goes, also those not selected now.
			var dr proto.DropDatabasesResult
			if err := jr.call(ctx, jr.target(), "mysql.dropDatabases",
				proto.DropDatabasesParams{Databases: jr.st.WipeList}, &dr); err != nil {
				return outcome{}, err
			}
			jr.logf("info", "Database lama dihapus: %s.", strings.Join(dr.Dropped, ", "))
		}
		jr.logf("info", "Menyalin dengan dump; master dikunci sesaat (FLUSH TABLES WITH READ LOCK) saat dump dimulai.")
	} else {
		jr.logf("info", "Menyalin dengan CLONE; semua isi MySQL di %s diganti salinan master.", name)
	}
	if reattach {
		jr.logf("info", "Salinan dari percobaan sebelumnya masih berjalan atau sudah selesai; ditunggu, tidak diulang.")
	}
	var res proto.SnapshotResult
	if err := jr.copySnapshot(ctx, cmd, p, reattach, &res); err != nil {
		return outcome{}, err
	}
	jr.st.SnapshotGTID = res.GTIDExecuted
	jr.save()
	if res.AdminReplaced {
		jr.st.AdminReplaced = true
		jr.st.WaitReason = WaitReconnect
		jr.save()
		jr.logf("warn", "Salinan selesai (%d byte), tetapi akun admin MySQL replika ikut terganti oleh akun-akun master.", res.Bytes)
		return wait(jr.reconnectMessage()), nil
	}
	if jr.st.Method == "clone" || len(jr.st.Databases) == 0 {
		jr.logf("warn", "Replika sekarang membawa akun-akun MySQL dari master (ikut tersalin).")
	}
	return done("%s selesai (%d byte)", jr.st.Method, res.Bytes), nil
}

// reconnectMessage asks the user, in plain words, to reconnect the
// target's MySQL with the master's admin account after the copy brought
// the master's accounts.
func (jr *jobRun) reconnectMessage() string {
	return fmt.Sprintf("Data sudah tersalin, tetapi salinan itu juga membawa akun-akun MySQL dari master %s, sehingga akun "+
		"admin yang tersimpan untuk %s tidak berlaku lagi.\n"+
		"1. Buka halaman server %s dan klik Hubungkan MySQL.\n"+
		"2. Masukkan akun admin MySQL milik master %s (akun yang sama dengan yang dipakai di Hubungkan MySQL di master).\n"+
		"3. Kembali ke halaman ini dan klik Sudah saya hubungkan ulang. Data tidak disalin ulang.",
		jr.serverName(jr.master()), jr.serverName(jr.target()), jr.serverName(jr.target()), jr.serverName(jr.master()))
}

// verifyCopyAfterReconnect continues a snapshot whose copy replaced the
// target's accounts: once the stored account logs in again, the copy is
// checked (clone_status for CLONE, the GTID set) and the target made
// read-only again; the step then ends without copying again.
func (jr *jobRun) verifyCopyAfterReconnect(ctx context.Context) (outcome, error) {
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.target(), "mysql.status", nil, &st); err != nil {
		if loginRefused(err) {
			jr.logf("warn", "Login ke MySQL replika masih ditolak: %v", err)
			jr.st.WaitReason = WaitReconnect
			jr.save()
			return wait(jr.reconnectMessage()), nil
		}
		return outcome{}, err
	}
	if jr.st.Method == "clone" && (st.CloneState != "Completed" || st.CloneError != "") {
		return outcome{}, fmt.Errorf("CLONE tidak selesai di server tujuan: status %q %s; ulangi Buat replika", st.CloneState, st.CloneError)
	}
	if jr.st.SnapshotGTID == "" {
		jr.st.SnapshotGTID = st.GTIDExecuted
	}
	if st.GTIDExecuted == "" {
		return outcome{}, errors.New("set GTID server tujuan kosong setelah salinan; ulangi Buat replika")
	}
	// The load lifted read-only and may not have put it back under the
	// replaced account.
	if err := jr.call(ctx, jr.target(), "mysql.setReadOnly", proto.ReadOnlyParams{On: true}, nil); err != nil {
		return outcome{}, err
	}
	if _, err := jr.r.RefreshInventory(ctx, jr.target()); err != nil {
		jr.r.log.Warn("refresh target inventory", "job", jr.job.ID, "err", err)
	}
	jr.st.AdminReplaced = false
	jr.save()
	jr.logf("info", "Akun MySQL replika terhubung lagi; salinan diperiksa (GTID %s).", st.GTIDExecuted)
	return done("%s selesai; akun MySQL replika terhubung ulang", jr.st.Method), nil
}

// loginRefused reports whether an agent answered that the stored MySQL
// account was refused (1045, 1698).
func loginRefused(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "login MySQL gagal") && (strings.Contains(msg, "1045") || strings.Contains(msg, "1698"))
}

// replAccount is the topology and its replication password.
func (jr *jobRun) replAccount() (store.Topology, string, error) {
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return topo, "", err
	}
	plain, err := jr.r.d.Secrets.Open(topo.ReplPasswordEnc)
	if err != nil {
		return topo, "", fmt.Errorf("buka password replikasi tersimpan: %w", err)
	}
	return topo, string(plain), nil
}

func (jr *jobRun) replicaConnect(ctx context.Context, _ bool) (outcome, error) {
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.target(), "mysql.status", nil, &st); err != nil {
		if loginRefused(err) {
			jr.logf("warn", "Login ke MySQL replika ditolak: %v", err)
			jr.st.WaitReason = WaitReconnect
			jr.save()
			return wait(jr.reconnectMessage()), nil
		}
		return outcome{}, err
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	// SOURCE_SSL=1 when the master offers TLS: encrypted, without CA
	// verification in v1 (the panel distributes no certificates yet).
	var rs proto.ReplicaStatus
	if err := jr.call(ctx, jr.target(), "mysql.configureReplication", proto.ReplicationSourceParams{
		Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password, SSL: jr.st.MasterTLS,
		Databases: jr.st.Databases,
	}, &rs); err != nil {
		return outcome{}, err
	}
	if err := jr.call(ctx, jr.target(), "mysql.startReplica", nil, &rs); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Replikasi berjalan: IO %s, SQL %s.", rs.IORunning, rs.SQLRunning)
	return done("IO %s, SQL %s", rs.IORunning, rs.SQLRunning), nil
}

func (jr *jobRun) replicaVerify(ctx context.Context, _ bool) (outcome, error) {
	// A failed verify stopped replication (stopReplicaAfter); a retry
	// starts it again. START REPLICA on running threads changes nothing.
	if err := jr.call(ctx, jr.target(), "mysql.startReplica", nil, nil); err != nil {
		return outcome{}, err
	}
	if _, err := jr.waitCaughtUp(ctx); err != nil {
		return outcome{}, err
	}
	jr.setReplicaState(store.ReplicaReady, "")
	if err := jr.r.d.Store.SetServerRole(jr.target(), store.RoleReplica); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Replika sinkron dengan master.")
	return done("sinkron"), nil
}

// waitMasterGTID reads the master's gtid_executed and waits up to
// timeoutSec for the replica to execute it.
func (jr *jobRun) waitMasterGTID(ctx context.Context, timeoutSec int) (proto.WaitSyncedResult, error) {
	var ms proto.MySQLStatus
	if err := jr.call(ctx, jr.master(), "mysql.status", nil, &ms); err != nil {
		return proto.WaitSyncedResult{}, err
	}
	var w proto.WaitSyncedResult
	err := jr.callWithin(ctx, time.Duration(timeoutSec+60)*time.Second, jr.target(), "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: ms.GTIDExecuted, TimeoutSec: timeoutSec}, &w)
	return w, err
}

// waitCaughtUp waits for the replica to execute the master's GTID set, in
// windows of syncTimeoutSec: while both replication threads run without an
// error and the replica's executed set grows from window to window, it
// logs the progress and keeps waiting, up to catchUpMax in all. A thread
// error, no progress in a window, or the overall bound fails the step; the
// last two with a catchUpError, which leaves replication running.
func (jr *jobRun) waitCaughtUp(ctx context.Context) (proto.WaitSyncedResult, error) {
	deadline := jr.r.now().Add(jr.r.catchUpMax)
	prev := ""
	for {
		w, err := jr.waitMasterGTID(ctx, syncTimeoutSec)
		if err != nil {
			return w, err
		}
		if w.Synced {
			return w, nil
		}
		s := w.Status
		if s.LastIOErrno != 0 || s.LastSQLErrno != 0 || s.SQLRunning != "Yes" ||
			(s.IORunning != "Yes" && s.IORunning != "Connecting") {
			return w, notSynced(w, fmt.Sprintf("replikasi tidak berjalan (IO %s, SQL %s)", s.IORunning, s.SQLRunning))
		}
		if w.Executed == prev {
			return w, catchUpError{fmt.Sprintf("replika tidak bertambah maju dalam %d menit (IO %s, SQL %s); "+
				"replikasi tetap berjalan, periksa beban server tujuan lalu tekan Ulangi", syncTimeoutSec/60, s.IORunning, s.SQLRunning)}
		}
		if !jr.r.now().Before(deadline) {
			return w, catchUpError{fmt.Sprintf("replika masih menyusul master setelah %s; replikasi tetap berjalan, "+
				"tekan Ulangi untuk menunggu lagi", jr.r.catchUpMax)}
		}
		prev = w.Executed
		msg := "replika masih menyusul master"
		if s.SecondsBehind != nil {
			msg += fmt.Sprintf(" (tertinggal %d detik)", *s.SecondsBehind)
		}
		jr.logf("info", "%s; sudah dieksekusi: %s", strings.ToUpper(msg[:1])+msg[1:], truncate(w.Executed, 512))
		jr.setStep(store.StepRunning, msg)
	}
}

// notSynced explains a replica that did not reach the set: a replication
// thread's error when there is one, otherwise lag.
func notSynced(w proto.WaitSyncedResult, lag string) error {
	s := w.Status
	switch {
	case s.LastIOError != "":
		return fmt.Errorf("replika belum sinkron: IO %d %s", s.LastIOErrno, s.LastIOError)
	case s.LastSQLError != "":
		return fmt.Errorf("replika belum sinkron: SQL %d %s", s.LastSQLErrno, s.LastSQLError)
	}
	return errors.New(lag)
}

func (jr *jobRun) setReplicaState(state, lastError string) {
	if err := jr.r.d.Store.SetReplicaState(jr.job.ServerID, state, lastError); err != nil && !errors.Is(err, store.ErrNotFound) {
		jr.r.log.Warn("replica state", "job", jr.job.ID, "err", err)
	}
}

// stopReplicaAfter stops replication on the target when the job ended at
// or after the step that starts it, so a broken source does not keep
// retrying (best effort).
func (jr *jobRun) stopReplicaAfter(ctx context.Context) {
	if jr.idx < stepConnect {
		return
	}
	if err := jr.r.d.Hub.Call(ctx, jr.job.ServerID, "mysql.stopReplica", nil, nil); err != nil {
		jr.r.log.Warn("stop replica after failure", "job", jr.job.ID, "err", err)
	}
}
