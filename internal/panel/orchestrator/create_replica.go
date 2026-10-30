package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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
	jr.save()
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
			Databases: jr.st.Databases, ConfirmWipe: confirm,
		}, &res)
		return res, err
	}
	res, err := preflight(false)
	if err != nil {
		return outcome{}, err
	}
	jr.st.WipeList = nil
	if res.Worst != proto.CheckFail && hasCheck(res.Checks, "leftover", proto.CheckNeedsConfirm) {
		list, err := jr.leftoverDatabases(ctx)
		if err != nil {
			return outcome{}, err
		}
		h := hashList(list)
		if !approved || h != jr.st.WipeHash {
			jr.st.WipeHash = h
			jr.save()
			return wait("database dari percobaan sebelumnya akan dihapus lalu disalin ulang dari master: " +
				strings.Join(list, ", ")), nil
		}
		jr.logf("warn", "Pengguna menyetujui penghapusan database: %s.", strings.Join(list, ", "))
		jr.st.WipeList = list
		jr.save()
		if res, err = preflight(true); err != nil {
			return outcome{}, err
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
	name := jr.serverName(jr.target())
	// A clone target has no user databases (the agent picks clone only
	// then); the form's ConfirmWipe pre-approves replacing the rest.
	if jr.st.Method == "clone" && !jr.st.ConfirmWipe && !jr.st.CloneApproved {
		if !approved {
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
		EstimateBytes: jr.st.DataBytes}
	cmd := "mysql.cloneFrom"
	if jr.st.Method == "dump" {
		cmd = "mysql.dumpFrom"
		p.Databases = jr.st.Databases
		if len(jr.st.WipeList) > 0 {
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
	var res proto.SnapshotResult
	if err := jr.runAgentJob(ctx, jr.target(), cmd, p, &res); err != nil {
		return outcome{}, err
	}
	jr.st.SnapshotGTID = res.GTIDExecuted
	jr.save()
	if jr.st.Method == "clone" || len(jr.st.Databases) == 0 {
		jr.logf("warn", "Replika sekarang membawa akun-akun MySQL dari master (ikut tersalin); "+
			"akun admin replika yang tersimpan bisa jadi tidak berlaku lagi.")
	}
	return done("%s selesai (%d byte)", jr.st.Method, res.Bytes), nil
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

// adminReplacedMessage asks the user to reconnect the replica's MySQL after
// the snapshot brought the master's accounts along.
const adminReplacedMessage = "akun admin replika ikut terganti oleh salinan dari master; " +
	"hubungkan ulang akun MySQL replika (menu Hubungkan MySQL) lalu Lanjutkan"

func (jr *jobRun) replicaConnect(ctx context.Context, _ bool) (outcome, error) {
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.target(), "mysql.status", nil, &st); err != nil {
		if msg := err.Error(); strings.Contains(msg, "login MySQL gagal") && strings.Contains(msg, "1045") {
			jr.logf("warn", "Login ke MySQL replika ditolak: %s", msg)
			return wait(adminReplacedMessage), nil
		}
		return outcome{}, err
	}
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	// SSL stays off in v1: the panel distributes no certificates yet, so a
	// replica could not verify the master's anyway.
	var rs proto.ReplicaStatus
	if err := jr.call(ctx, jr.target(), "mysql.configureReplication", proto.ReplicationSourceParams{
		Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password, SSL: false,
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
	var ms proto.MySQLStatus
	if err := jr.call(ctx, jr.master(), "mysql.status", nil, &ms); err != nil {
		return outcome{}, err
	}
	// A failed verify stopped replication (stopReplicaAfter); a retry
	// starts it again. START REPLICA on running threads changes nothing.
	if err := jr.call(ctx, jr.target(), "mysql.startReplica", nil, nil); err != nil {
		return outcome{}, err
	}
	var w proto.WaitSyncedResult
	if err := jr.callWithin(ctx, time.Duration(syncTimeoutSec+60)*time.Second, jr.target(), "mysql.waitSynced",
		proto.WaitSyncedParams{GTIDSet: ms.GTIDExecuted, TimeoutSec: syncTimeoutSec}, &w); err != nil {
		return outcome{}, err
	}
	if !w.Synced {
		s := w.Status
		switch {
		case s.LastIOError != "":
			return outcome{}, fmt.Errorf("replika belum sinkron: IO %d %s", s.LastIOErrno, s.LastIOError)
		case s.LastSQLError != "":
			return outcome{}, fmt.Errorf("replika belum sinkron: SQL %d %s", s.LastSQLErrno, s.LastSQLError)
		}
		return outcome{}, fmt.Errorf("replika belum menyusul master dalam %d menit (IO %s, SQL %s)",
			syncTimeoutSec/60, s.IORunning, s.SQLRunning)
	}
	jr.setReplicaState(store.ReplicaReady, "")
	if err := jr.r.d.Store.SetServerRole(jr.target(), store.RoleReplica); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Replika sinkron dengan master.")
	return done("sinkron"), nil
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
