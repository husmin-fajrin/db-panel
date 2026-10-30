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
//	0 catch_up       the replica has executed the master's current GTID set
//	1 stop           replication stops (a dump refuses a running replica)
//	2 dump           the new databases are dumped from the master; the
//	                 replica keeps its own GTID history (no DropExisting, so
//	                 the agent dumps in COMMENTED mode) and the dump's header
//	                 gives the master's set at the dump, P2
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
// succeeds and failed (its databases unchanged) when it fails.

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
	if rep.State != store.ReplicaReady {
		return rep, store.Topology{}, fmt.Errorf("replika belum siap (status %s); database hanya bisa diubah saat replika siap", rep.State)
	}
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return rep, store.Topology{}, err
	}
	topo, err := r.d.Store.GetTopology(rep.TopologyID)
	return rep, topo, err
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
		{n[5], jr.changeVerify(syncTimeoutSec)},
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

func (jr *jobRun) addCatchUp(ctx context.Context, _ bool) (outcome, error) {
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

func (jr *jobRun) addDump(ctx context.Context, _ bool) (outcome, error) {
	topo, password, err := jr.replAccount()
	if err != nil {
		return outcome{}, err
	}
	// DropExisting would put the dump in ON mode, which resets the
	// replica's GTID history; the new databases were never followed, so
	// there is nothing of theirs to drop.
	p := proto.SnapshotParams{Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password,
		Databases: jr.st.Change, EstimateBytes: jr.st.DataBytes}
	jr.logf("info", "Menyalin database %s dengan dump; master dikunci sesaat (FLUSH TABLES WITH READ LOCK) saat dump dimulai.",
		strings.Join(jr.st.Change, ", "))
	var res proto.SnapshotResult
	if err := jr.runAgentJob(ctx, jr.target(), "mysql.dumpFrom", p, &res); err != nil {
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
		return outcome{}, errors.New("titik dump belum diketahui; ulangi dari tahap " + addDatabasesStepNames()[stepAddDump])
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
// master, then records the databases it follows now.
func (jr *jobRun) changeVerify(timeoutSec int) func(context.Context, bool) (outcome, error) {
	return func(ctx context.Context, _ bool) (outcome, error) {
		w, err := jr.waitMasterGTID(ctx, timeoutSec)
		if err != nil {
			return outcome{}, err
		}
		if !w.Synced {
			return outcome{}, notSynced(w, fmt.Sprintf("replika belum menyusul master dalam %d detik (IO %s, SQL %s)",
				timeoutSec, w.Status.IORunning, w.Status.SQLRunning))
		}
		dbs := jr.changedDatabases()
		if err := jr.r.d.Store.SetReplicaDatabases(jr.target(), dbs); err != nil {
			return outcome{}, err
		}
		jr.setReplicaState(store.ReplicaReady, "")
		jr.logf("info", "Replika sinkron dengan master dan sekarang mengikuti: %s.", strings.Join(dbs, ", "))
		return done("sinkron; mengikuti %s", strings.Join(dbs, ", ")), nil
	}
}

// logChangeFailure tells, in the job log, what a failed database change
// left on the replica. Retry continues from the failed step.
func (jr *jobRun) logChangeFailure() {
	names := strings.Join(jr.st.Change, ", ")
	switch {
	case jr.job.Kind == KindRemoveDatabases && jr.idx < 2:
		jr.logf("warn", "Replikasi di replika mungkin berhenti; tekan Ulangi untuk melanjutkan.")
	case jr.job.Kind != KindAddDatabases:
	case jr.idx == stepAddDump:
		jr.logf("warn", "Database %s mungkin sudah tersalin sebagian ke replika tetapi belum direplikasi; datanya dibiarkan.", names)
	case jr.idx > stepAddDump && jr.idx <= stepAddExpand:
		jr.logf("warn", "Database %s sudah tersalin ke replika tetapi belum direplikasi; datanya dibiarkan.", names)
	}
	if jr.job.Kind == KindAddDatabases && jr.idx >= stepAddStop && jr.idx < stepAddExpand {
		jr.logf("warn", "Replikasi di replika belum dijalankan lagi; tekan Ulangi untuk melanjutkan.")
	}
}
