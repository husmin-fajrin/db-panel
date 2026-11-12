package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// A replica that follows a subset of the master's databases can follow
// more of them, or fewer, while it runs.
//
// add_databases:
//
//	0 catch_up       the replica's databases are read fresh: when new ones
//	                 already exist there (compared without case), the job
//	                 waits (WaitAddExisting) until the user approves exactly
//	                 that list by typing each name; then the filter is set
//	                 to the databases followed now (an earlier failed or
//	                 canceled change may have left another one, or
//	                 replication stopped; setting it starts replication),
//	                 then the replica executes the master's current GTID set
//	1 stop           replication stops (a dump refuses a running replica)
//	2 dump           the approved existing databases (AddDrop) are dropped
//	                 without binary logging (read again first unless step 0
//	                 ran in this run; another list waits again); the new
//	                 databases are dumped from the master; the replica keeps
//	                 its own GTID history (no DropExisting, so the agent
//	                 dumps in COMMENTED mode) and the dump's header gives
//	                 the master's set at the dump, P2
//	3 replay_until   the applier runs with the old filter until P2: the old
//	                 databases reach the dump point, and the new databases'
//	                 transactions up to P2, which the dump holds, are
//	                 filtered out and counted executed
//	4 expand_filter  the filter takes the new databases; replication starts
//	5 verify         the replica catches up with the master; the panel
//	                 records the new list
//
// remove_databases:
//
//	0 shrink_filter  the filter keeps only the remaining databases
//	1 drop           only when asked: the removed databases are dropped on
//	                 the replica (replication stopped meanwhile)
//	2 verify         as above
//
// The replica is in state changing while the job runs, ready again when it
// succeeds, and attention (its databases unchanged, LastError naming the
// step) when it fails or is canceled. A replica in attention can be changed
// again; it is never offered for re-creation as a failed one is.

func addDatabasesStepNames() []string {
	return []string{"Menunggu replika menyusul", "Hentikan replikasi", "Salin database baru",
		"Putar ulang sampai titik dump", "Perluas filter", "Verifikasi"}
}

func removeDatabasesStepNames() []string {
	return []string{"Kecilkan filter", "Hapus data", "Verifikasi"}
}

// Steps of add_databases a failure report refers to.
const (
	stepAddStop   = 1
	stepAddDump   = 2
	stepAddReplay = 3
	stepAddExpand = 4
)

// catchUpTimeoutSec bounds the wait for a replica that should already be
// in sync: before adding databases, and after removing some.
const catchUpTimeoutSec = 60

// StartAddDatabases starts the add_databases job: the replica on
// replicaServerID also follows databases. The replica must be ready and
// follow a subset; every name must be a database of the master that it
// does not follow yet.
func (r *Runner) StartAddDatabases(ctx context.Context, replicaServerID int64, databases []string) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	rep, topo, err := r.changeableReplica(replicaServerID)
	if err != nil {
		return "", err
	}
	// The dump load makes the replica writable for a while.
	if err := r.refuseWhileLockMayStand(topo.ID, topo.MasterServerID, "Tambah database"); err != nil {
		return "", err
	}
	add, err := pickDatabases(databases)
	if err != nil {
		return "", err
	}
	inv, err := r.d.Store.GetInventory(topo.MasterServerID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("daftar database master belum ada; hubungkan MySQL master dulu")
	}
	if err != nil {
		return "", err
	}
	var bytes uint64
	for _, d := range add {
		i := slices.IndexFunc(inv.Databases, func(m proto.DatabaseInfo) bool { return m.Name == d })
		if i < 0 {
			return "", fmt.Errorf("database %q tidak ada di master", d)
		}
		if slices.Contains(rep.Databases, d) {
			return "", fmt.Errorf("database %s sudah diikuti replika ini", d)
		}
		bytes += inv.Databases[i].Bytes
	}
	st := jobState{MasterServerID: topo.MasterServerID, Databases: rep.Databases, Change: add, DataBytes: bytes}
	return r.createJob(KindAddDatabases, replicaServerID, topo.ID, st, addDatabasesStepNames())
}

// StartRemoveDatabases starts the remove_databases job: the replica on
// replicaServerID stops following databases, and drops them when dropData.
// Every name must be followed now and at least one database must remain.
func (r *Runner) StartRemoveDatabases(ctx context.Context, replicaServerID int64, databases []string, dropData bool) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	rep, topo, err := r.changeableReplica(replicaServerID)
	if err != nil {
		return "", err
	}
	// Dropping the data lifts read-only on the replica for a while
	// (mysql.dropDatabases).
	if dropData {
		if err := r.refuseWhileLockMayStand(topo.ID, topo.MasterServerID, "Lepas database"); err != nil {
			return "", err
		}
	}
	remove, err := pickDatabases(databases)
	if err != nil {
		return "", err
	}
	for _, d := range remove {
		if !slices.Contains(rep.Databases, d) {
			return "", fmt.Errorf("database %s tidak diikuti replika ini", d)
		}
	}
	if len(remove) == len(rep.Databases) {
		return "", errors.New("lepas semua database tidak didukung; hapus replika ini bila tidak diperlukan lagi")
	}
	st := jobState{MasterServerID: topo.MasterServerID, Databases: rep.Databases, Change: remove, DropData: dropData}
	if dropData {
		// The token and typed name the user gave confirm the drop now.
		st.ConfirmedAt = r.now()
	}
	return r.createJob(KindRemoveDatabases, replicaServerID, topo.ID, st, removeDatabasesStepNames())
}

// changeableReplica is the replica on serverID and its topology, when its
// databases can be changed now. Callers hold startMu.
func (r *Runner) changeableReplica(serverID int64) (store.Replica, store.Topology, error) {
	rep, err := r.d.Store.GetReplicaByServer(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return rep, store.Topology{}, errors.New("server ini bukan replika")
	}
	if err != nil {
		return rep, store.Topology{}, err
	}
	if len(rep.Databases) == 0 {
		return rep, store.Topology{}, errors.New("replika ini mengikuti semua database")
	}
	if rep.State != store.ReplicaReady && rep.State != store.ReplicaAttention {
		return rep, store.Topology{}, fmt.Errorf("replika belum siap (status %s); database hanya bisa diubah saat replika siap",
			replicaStateText(rep.State))
	}
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return rep, store.Topology{}, err
	}
	topo, err := r.d.Store.GetTopology(rep.TopologyID)
	return rep, topo, err
}

// replicaStateText names a replica state in Indonesian.
func replicaStateText(state string) string {
	switch state {
	case store.ReplicaReady:
		return "siap"
	case store.ReplicaChanging:
		return "sedang diubah"
	case store.ReplicaAttention:
		return "perlu perhatian"
	case store.ReplicaFailed:
		return "gagal"
	case store.ReplicaCreating:
		return "sedang dibuat"
	case store.ReplicaResyncing:
		return "sedang disync ulang"
	case store.ReplicaNeedsRepoint:
		return "perlu diarahkan ulang"
	case store.ReplicaNeedsRebuild:
		return "perlu dibangun ulang"
	}
	return state
}

// stepTitle is the title of step idx of a database change job of kind.
func stepTitle(kind string, idx int) string {
	names := removeDatabasesStepNames()
	if kind == KindAddDatabases {
		names = addDatabasesStepNames()
	}
	if idx < 0 || idx >= len(names) {
		return fmt.Sprintf("tahap %d", idx)
	}
	return names[idx]
}

// failedAt is the replica's LastError after a change job failed at idx.
func failedAt(kind string, idx int, detail string) string {
	return "gagal saat " + stepTitle(kind, idx) + ": " + detail
}

// pickDatabases is the selection sorted without duplicates; it may not be
// empty. The agent checks the names themselves.
func pickDatabases(selected []string) ([]string, error) {
	out := slices.Clone(selected)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil, errors.New("pilih minimal satu database")
	}
	return out, nil
}

// changedDatabases is what the replica follows once the job succeeds.
func (jr *jobRun) changedDatabases() []string {
	if jr.job.Kind == KindAddDatabases {
		out := slices.Concat(jr.st.Databases, jr.st.Change)
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.DeleteFunc(slices.Clone(jr.st.Databases), func(d string) bool { return slices.Contains(jr.st.Change, d) })
}

func (jr *jobRun) addDatabasesSteps() []step {
	n := addDatabasesStepNames()
	return []step{
		{n[0], jr.addCatchUp},
		{n[1], jr.addStop},
		{n[2], jr.addDump},
		{n[3], jr.addReplayUntil},
		{n[4], jr.addExpandFilter},
		{n[5], jr.changeVerify(0)},
	}
}

func (jr *jobRun) removeDatabasesSteps() []step {
	n := removeDatabasesStepNames()
	return []step{
		{n[0], jr.removeShrinkFilter},
		{n[1], jr.removeDrop},
		{n[2], jr.changeVerify(catchUpTimeoutSec)},
	}
}

func (jr *jobRun) addCatchUp(ctx context.Context, approved bool) (outcome, error) {
	// Added databases that already exist on the replica are dropped
	// before the dump, and only once the user approved exactly that list
	// (spec 5.6, R-058).
	list, err := jr.addExisting(ctx)
	if err != nil {
		return outcome{}, err
	}
	jr.addFresh = true
	if len(list) == 0 {
		jr.st.AddExisting, jr.st.AddExistingHash, jr.st.AddDrop = nil, "", nil
		jr.save()
	} else {
		if !approved || hashList(list) != jr.st.AddExistingHash {
			return jr.waitAddExisting(list), nil
		}
		jr.approveAddDrop(list)
	}
	jr.sweepDumpAccounts(ctx)
	// The stored list is the filter the replica must have before the
	// replay in step 3; the agent stops, sets it and starts replication.
	if err := jr.call(ctx, jr.target(), "mysql.setReplicationFilter",
		proto.ReplicationFilterParams{Databases: jr.st.Databases}, nil); err != nil {
		return outcome{}, err
	}
	w, err := jr.waitMasterGTID(ctx, catchUpTimeoutSec)
	if err != nil {
		return outcome{}, err
	}
	if !w.Synced {
		return outcome{}, notSynced(w, "replika masih tertinggal; coba lagi setelah replika menyusul")
	}
	return done("replika sinkron dengan master"), nil
}

func (jr *jobRun) addStop(ctx context.Context, _ bool) (outcome, error) {
	if err := jr.call(ctx, jr.target(), "mysql.stopReplica", nil, nil); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Replikasi dihentikan selama database baru disalin.")
	return done("replikasi berhenti"), nil
}

func (jr *jobRun) addDump(ctx context.Context, approved bool) (outcome, error) {
	topo, err := jr.r.d.Store.GetTopology(jr.job.TopologyID)
	if err != nil {
		return outcome{}, err
	}
	reattach := jr.earlierAgentJob(ctx, jr.target(), "mysql.dumpFrom")
	// A run that did not read the replica itself (a Retry or Approve from
	// here, a restart) reads it again, whatever the first run found: a
	// database of that name may exist by now (FR-5).
	if (len(jr.st.AddDrop) > 0 || !jr.addFresh) && !reattach {
		if out, ok, err := jr.dropAddExisting(ctx, approved); !ok {
			return out, err
		}
	}
	var ms proto.MySQLStatus
	if err := jr.call(ctx, jr.master(), "mysql.status", nil, &ms); err != nil {
		return outcome{}, err
	}
	// DropExisting (refused by the agent) would put the dump in ON mode,
	// which resets the replica's GTID history; what of the new databases
	// existed was dropped above with mysql.dropDatabases. The account is a
	// temporary one (copySnapshot).
	p := proto.SnapshotParams{Host: topo.SourceHost, Port: topo.SourcePort, Databases: jr.st.Change,
		EstimateBytes: jr.st.DataBytes, SSL: ms.TLSAvailable}
	// The dump verifies the master's certificate when it has the panel's.
	if jr.st.TLSVerify, err = jr.r.masterHasCert(jr.master()); err != nil {
		return outcome{}, err
	}
	if err := jr.verifySnapshot(ctx, &p); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Menyalin database %s dengan dump; master dikunci sesaat (FLUSH TABLES WITH READ LOCK) saat dump dimulai.",
		strings.Join(jr.st.Change, ", "))
	var res proto.SnapshotResult
	if err := jr.copySnapshot(ctx, "mysql.dumpFrom", p, reattach, &res); err != nil {
		return outcome{}, err
	}
	if res.GTIDExecuted == "" {
		return outcome{}, errors.New("dump tidak memberi set GTID master; replika tidak bisa disambungkan ke titik dump")
	}
	jr.st.SnapshotGTID = res.GTIDExecuted
	jr.save()
	jr.logf("info", "Titik dump (GTID master): %s.", res.GTIDExecuted)
	return done("dump selesai (%d byte)", res.Bytes), nil
}

func (jr *jobRun) addReplayUntil(ctx context.Context, _ bool) (outcome, error) {
	if jr.st.SnapshotGTID == "" {
		return outcome{}, errors.New("titik dump tidak diketahui; jalankan Tambah database lagi dari awal")
	}
	jr.logf("info", "Memutar ulang transaksi database lama sampai titik dump, dengan filter lama.")
	if err := jr.callWithin(ctx, time.Duration(syncTimeoutSec+60)*time.Second, jr.target(), "mysql.startUntil",
		proto.StartUntilParams{GTIDSet: jr.st.SnapshotGTID, TimeoutSec: syncTimeoutSec}, nil); err != nil {
		return outcome{}, err
	}
	return done("replika sampai di titik dump"), nil
}

func (jr *jobRun) addExpandFilter(ctx context.Context, _ bool) (outcome, error) {
	return jr.setFilter(ctx, jr.changedDatabases())
}

func (jr *jobRun) removeShrinkFilter(ctx context.Context, _ bool) (outcome, error) {
	if err := jr.call(ctx, jr.target(), "mysql.stopReplica", nil, nil); err != nil {
		return outcome{}, err
	}
	return jr.setFilter(ctx, jr.changedDatabases())
}

// setFilter replaces the replica's filter with dbs and starts replication.
func (jr *jobRun) setFilter(ctx context.Context, dbs []string) (outcome, error) {
	if err := jr.call(ctx, jr.target(), "mysql.setReplicationFilter", proto.ReplicationFilterParams{Databases: dbs}, nil); err != nil {
		return outcome{}, err
	}
	var rs proto.ReplicaStatus
	if err := jr.call(ctx, jr.target(), "mysql.startReplica", nil, &rs); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Filter replikasi sekarang: %s. Replikasi berjalan: IO %s, SQL %s.",
		strings.Join(dbs, ", "), rs.IORunning, rs.SQLRunning)
	return done("filter: %s", strings.Join(dbs, ", ")), nil
}

func (jr *jobRun) removeDrop(ctx context.Context, _ bool) (outcome, error) {
	names := strings.Join(jr.st.Change, ", ")
	if !jr.st.DropData {
		jr.logf("info", "Data database %s tetap ada di replika tetapi tidak lagi diperbarui.", names)
		return skipped("data tetap disimpan di replika"), nil
	}
	// The agent drops nothing while replication runs.
	if err := jr.call(ctx, jr.target(), "mysql.stopReplica", nil, nil); err != nil {
		return outcome{}, err
	}
	var dr proto.DropDatabasesResult
	derr := jr.call(ctx, jr.target(), "mysql.dropDatabases", proto.DropDatabasesParams{Databases: jr.st.Change}, &dr)
	if derr == nil {
		if len(dr.Dropped) == 0 {
			jr.logf("info", "Database %s sudah tidak ada di replika.", names)
		} else {
			jr.logf("info", "Database dihapus dari replika: %s.", strings.Join(dr.Dropped, ", "))
		}
	}
	// Replication starts again also when the drop failed.
	serr := jr.call(ctx, jr.target(), "mysql.startReplica", nil, nil)
	if derr != nil {
		return outcome{}, derr
	}
	if serr != nil {
		return outcome{}, serr
	}
	return done("dihapus: %s", names), nil
}

// changeVerify waits up to timeoutSec for the replica to catch up with the
// master, then records the databases it follows now. timeoutSec 0 waits
// as long as the replica makes progress (waitCaughtUp): after a dump the
// replica may need long to catch up.
func (jr *jobRun) changeVerify(timeoutSec int) func(context.Context, bool) (outcome, error) {
	return func(ctx context.Context, _ bool) (outcome, error) {
		if timeoutSec == 0 {
			if _, err := jr.waitCaughtUp(ctx); err != nil {
				return outcome{}, err
			}
		} else {
			w, err := jr.waitMasterGTID(ctx, timeoutSec)
			if err != nil {
				return outcome{}, err
			}
			if !w.Synced {
				return outcome{}, notSynced(w, fmt.Sprintf("replika belum menyusul master dalam %d detik (IO %s, SQL %s)",
					timeoutSec, w.Status.IORunning, w.Status.SQLRunning))
			}
		}
		dbs := jr.changedDatabases()
		if err := jr.r.d.Store.SetReplicaDatabases(jr.target(), dbs); err != nil {
			return outcome{}, err
		}
		jr.setReplicaState(store.ReplicaReady, "")
		jr.logf("info", "Replika sinkron dengan master dan sekarang mengikuti: %s.", strings.Join(dbs, ", "))
		if jr.job.Kind == KindAddDatabases {
			// Only an add re-attaches the replica's connection; a removal
			// leaves the one the master already knows.
			jr.r.recordReplicas(ctx, jr.master())
		}
		return done("sinkron; mengikuti %s", strings.Join(dbs, ", ")), nil
	}
}

// logChangeFailure tells, in the job log, what a failed database change
// left on the replica. Retry continues from the failed step; a new add job
// restores the filter first.
func (jr *jobRun) logChangeFailure() {
	names := strings.Join(jr.st.Change, ", ")
	const retry = " Tekan Ulangi untuk melanjutkan dari tahap yang gagal."
	if jr.job.Kind == KindRemoveDatabases {
		if jr.idx < 2 {
			jr.logf("warn", "Replikasi di replika mungkin berhenti."+retry)
		}
		return
	}
	switch jr.idx {
	case stepAddStop:
		jr.logf("warn", "Replikasi mungkin masih berjalan; belum ada database yang disalin."+retry)
	case stepAddDump:
		jr.logf("warn", "Replikasi berhenti. Database %s mungkin sudah tersalin sebagian ke replika tetapi belum "+
			"direplikasi; datanya dibiarkan."+retry, names)
	case stepAddReplay:
		jr.logf("warn", "Replika mungkin masih memutar ulang sampai titik dump. Database %s sudah tersalin ke replika "+
			"tetapi belum direplikasi; datanya dibiarkan."+retry, names)
	case stepAddExpand:
		jr.logf("warn", "Replikasi berhenti. Database %s sudah tersalin ke replika tetapi belum direplikasi; "+
			"datanya dibiarkan."+retry, names)
	}
}

// addExisting is the databases being added that already exist on the
// replica, read fresh from its agent (RefreshInventory), compared without
// case (with lower_case_table_names the replica may hold Rekap for rekap);
// sorted, in the spelling of the job's list (st.Change), so a drop names
// only databases the job adds. It fails closed when the replica's list was
// cut (FR-6), and when a database to drop equals, ignoring case, one the
// replica already follows (st.Databases): dropping it there could drop the
// followed one (ledger Task 13 m3).
func (jr *jobRun) addExisting(ctx context.Context) ([]string, error) {
	res, cut, err := jr.r.refreshInventory(ctx, jr.target())
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, errInventoryCut("replika")
	}
	var out []string
	for _, d := range jr.st.Change {
		if slices.ContainsFunc(res.Databases, func(i proto.DatabaseInfo) bool { return strings.EqualFold(i.Name, d) }) {
			if i := slices.IndexFunc(jr.st.Databases, func(f string) bool { return strings.EqualFold(f, d) }); i >= 0 {
				return nil, fmt.Errorf("database %s sama dengan database %s yang sudah diikuti replika ini bila huruf besar-kecil "+
					"tidak dibedakan; pekerjaan ditolak agar database itu tidak ikut dibuang", d, jr.st.Databases[i])
			}
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out, nil
}

// addExistingText tells, one paragraph per database, that it exists on the
// replica and is replaced (spec 5.6).
func addExistingText(names []string) string {
	paras := make([]string, 0, len(names))
	for _, x := range names {
		paras = append(paras, "Database "+x+" sudah ada di replika ini. Panel tidak bisa memastikan isinya sama dengan "+
			"master. Isinya di replika akan dibuang permanen, lalu diganti salinan dari master.")
	}
	return strings.Join(paras, "\n\n")
}

// waitAddExisting stores list as the one shown and waits for the user to
// approve its drop (Approve checks the typed names against it).
func (jr *jobRun) waitAddExisting(list []string) outcome {
	jr.st.AddExisting, jr.st.AddExistingHash = list, hashList(list)
	jr.st.WaitReason = WaitAddExisting
	jr.save()
	return wait(addExistingText(list))
}

// approveAddDrop makes the approved list the one dropped before the dump.
func (jr *jobRun) approveAddDrop(list []string) {
	jr.st.AddDrop = list
	jr.save()
	jr.logf("warn", "Pengguna menyetujui pembuangan database yang sudah ada di replika: %s.", strings.Join(list, ", "))
}

// dropAddExisting drops AddDrop on the replica, whose replication step 1
// stopped, before the dump. A run that did not read the replica's
// databases itself (a Retry or Approve from this step, a restart) reads
// them again: a list other than the approved AddDrop waits for a new
// approval, and nothing is dropped. ok is false when the step must end
// with out and err.
func (jr *jobRun) dropAddExisting(ctx context.Context, approved bool) (out outcome, ok bool, err error) {
	if !jr.addFresh {
		list, err := jr.addExisting(ctx)
		if err != nil {
			return outcome{}, false, err
		}
		h := hashList(list)
		switch {
		case len(list) == 0:
			// An earlier attempt dropped them and loaded nothing yet (or
			// there was never anything to drop).
			if len(jr.st.AddDrop) > 0 {
				jr.logf("info", "Database %s tidak ada lagi di replika; tidak ada yang dibuang.", strings.Join(jr.st.AddDrop, ", "))
			}
			return outcome{}, true, nil
		case slices.Equal(list, jr.st.AddDrop):
		case approved && jr.st.WaitReason == WaitAddExisting && h == jr.st.AddExistingHash:
			jr.approveAddDrop(list)
		default:
			return jr.waitAddExisting(list), false, nil
		}
		jr.addFresh = true
	}
	// mysql.dropDatabases drops without binary logging (sql_log_bin=0),
	// so the replica's GTID history is not touched, and refuses while
	// replication runs.
	var dr proto.DropDatabasesResult
	if err := jr.call(ctx, jr.target(), "mysql.dropDatabases", proto.DropDatabasesParams{Databases: jr.st.AddDrop}, &dr); err != nil {
		return outcome{}, false, err
	}
	if len(dr.Dropped) == 0 {
		jr.logf("info", "Database %s sudah tidak ada di replika.", strings.Join(jr.st.AddDrop, ", "))
	} else {
		jr.logf("warn", "Database dibuang dari replika sebelum disalin dari master: %s.", strings.Join(dr.Dropped, ", "))
	}
	return outcome{}, true, nil
}
