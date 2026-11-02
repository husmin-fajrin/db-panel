package monitor

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Alert kinds (spec 5.3); at most one alert of a kind is open per server.
const (
	KindReplicaStopped      = "replica_stopped"
	KindReplicaDisconnected = "replica_disconnected"
	KindReplicaLagging      = "replica_lagging"
	KindReplicaUnlocked     = "replica_unlocked"
	KindMasterLocked        = "master_locked"
	KindMasterDown          = "master_down"
	KindMySQLUnreachable    = "mysql_unreachable"
	KindAgentOffline        = "agent_offline"
	KindDiskWarn            = "disk_warn"
	KindDiskCrit            = "disk_crit"
	KindConnectionsHigh     = "connections_high"
	KindBinlogDeadline      = "binlog_deadline"
	KindJobFailed           = "job_failed"
)

// AllKinds returns every alert kind, in the order of spec 5.3.
func AllKinds() []string {
	return []string{KindReplicaStopped, KindReplicaDisconnected, KindReplicaLagging, KindReplicaUnlocked,
		KindMasterLocked, KindMasterDown, KindMySQLUnreachable, KindAgentOffline, KindDiskWarn, KindDiskCrit,
		KindConnectionsHigh, KindBinlogDeadline, KindJobFailed}
}

// Fixed delays and hysteresis of the rules (plan 3, Global Constraints);
// the configurable ones live in Settings.
const (
	unreachableAlertAfter = 30 * time.Second // a replica's MySQL not answering
	connHighFor           = 5 * time.Minute  // connections at or over ConnPct
	connCloseMargin       = 10               // connections_high closes under ConnPct − 10
	diskCloseMargin       = 5                // disk alerts close under their threshold − 5
	lagRecoveredFor       = time.Minute      // Sinkron this long closes replica_lagging
	binlogDeadlinePct     = 80               // of the master's binlog_expire_logs_seconds
)

// ruleCtx is what a rule sees besides the server's view.
type ruleCtx struct {
	now time.Time
	set Settings
	// master is the view of a replica's master; nil for a master, or when
	// the Monitor does not know it.
	master *View
	// agentOff: the agent counts as offline (disconnected beyond the grace,
	// or its last report is stale).
	agentOff bool
	// Condition clocks of the server; zero while the condition is absent.
	connHighSince time.Time // connections at or over ConnPct
	syncedSince   time.Time // status Sinkron
	outSince      time.Time // not following the master (Terputus, Berhenti, Dihentikan)
}

// alertRule opens an alert of kind when open holds (with its message) and
// closes it when close holds; between the two an open alert stays open.
type alertRule struct {
	kind  string
	open  func(v View, ctx ruleCtx) (bool, string /* pesan */)
	close func(v View, ctx ruleCtx) bool
}

// alertRules are the status rules, in AllKinds order; job_failed is
// opened and closed by the orchestrator, not by a rule.
var alertRules = []alertRule{
	{
		kind: KindReplicaStopped,
		open: func(v View, _ ruleCtx) (bool, string) {
			if !isReplica(v) || !stopped(v.Status.Code) {
				return false, ""
			}
			return true, stoppedMessage(v)
		},
		close: func(v View, _ ruleCtx) bool {
			return !isReplica(v) || (lockJudged(v.Status.Code) && !stopped(v.Status.Code))
		},
	},
	{
		kind: KindReplicaDisconnected,
		open: func(v View, ctx ruleCtx) (bool, string) {
			if !isReplica(v) || v.Status.Code != StatusDisconnected || ctx.now.Sub(v.Since) < ctx.set.DisconnectAlert {
				return false, ""
			}
			return true, fmt.Sprintf("Replika terputus dari master lebih dari %s; MySQL akan menyambung sendiri.",
				formatDuration(ctx.set.DisconnectAlert))
		},
		close: func(v View, _ ruleCtx) bool {
			// The IO thread runs again, or the replica was stopped through
			// the panel on purpose.
			return !isReplica(v) || ioRunning(v.Snap) || v.Status.Code == StatusPaused
		},
	},
	{
		kind: KindReplicaLagging,
		open: func(v View, ctx ruleCtx) (bool, string) {
			if !isReplica(v) || v.Status.Code != StatusLagging || ctx.now.Sub(v.Since) < ctx.set.LaggingAlert {
				return false, ""
			}
			return true, fmt.Sprintf("Replika tertinggal dari master lebih dari %s.", formatDuration(ctx.set.LaggingAlert))
		},
		close: func(v View, ctx ruleCtx) bool {
			return !isReplica(v) || (!ctx.syncedSince.IsZero() && ctx.now.Sub(ctx.syncedSince) >= lagRecoveredFor)
		},
	},
	{
		kind: KindReplicaUnlocked,
		open: func(v View, _ ruleCtx) (bool, string) {
			if !isReplica(v) || !v.Status.LockLost {
				return false, ""
			}
			return true, "Kunci replika terlepas (super_read_only mati); aplikasi bisa menulis langsung ke replika."
		},
		close: func(v View, _ ruleCtx) bool {
			return !isReplica(v) || (lockJudged(v.Status.Code) && !v.Status.LockLost)
		},
	},
	{
		kind: KindMasterLocked,
		open: func(v View, _ ruleCtx) (bool, string) {
			if !isMaster(v) || v.Status.Code != StatusLocked {
				return false, ""
			}
			return true, v.Status.Detail + "."
		},
		close: func(v View, _ ruleCtx) bool {
			return !isMaster(v) || v.Status.Code == StatusHealthy
		},
	},
	{
		kind: KindMasterDown,
		open: func(v View, ctx ruleCtx) (bool, string) {
			if !isMaster(v) || v.Status.Code != StatusDown {
				return false, ""
			}
			if ctx.agentOff {
				return true, "Master diduga tumbang: agent tidak terhubung ke panel."
			}
			return true, "Master diduga tumbang: MySQL tidak menjawab."
		},
		close: func(v View, _ ruleCtx) bool {
			// MySQL answers again (a locked master is not down).
			return !isMaster(v) || v.Status.Code == StatusHealthy || v.Status.Code == StatusLocked
		},
	},
	{
		kind: KindMySQLUnreachable,
		open: func(v View, ctx ruleCtx) (bool, string) {
			if !isReplica(v) || v.Status.Code != StatusUnreachable || ctx.now.Sub(v.Since) < unreachableAlertAfter {
				return false, ""
			}
			return true, fmt.Sprintf("MySQL di replika tidak menjawab lebih dari %s.", formatDuration(unreachableAlertAfter))
		},
		close: func(v View, _ ruleCtx) bool {
			return !isReplica(v) || lockJudged(v.Status.Code)
		},
	},
	{
		kind: KindAgentOffline,
		open: func(v View, _ ruleCtx) (bool, string) {
			// The status already waits out the 30 s grace.
			if v.Status.Code != StatusAgentOffline {
				return false, ""
			}
			return true, "Agent tidak terhubung ke panel atau laporannya terlambat."
		},
		close: func(_ View, ctx ruleCtx) bool { return !ctx.agentOff },
	},
	diskRule(KindDiskWarn, func(s Settings) int { return s.DiskWarn }),
	diskRule(KindDiskCrit, func(s Settings) int { return s.DiskCrit }),
	{
		kind: KindConnectionsHigh,
		open: func(_ View, ctx ruleCtx) (bool, string) {
			if ctx.connHighSince.IsZero() || ctx.now.Sub(ctx.connHighSince) < connHighFor {
				return false, ""
			}
			return true, fmt.Sprintf("Koneksi MySQL %d%% atau lebih dari max_connections selama %s.",
				ctx.set.ConnPct, formatDuration(connHighFor))
		},
		close: func(v View, ctx ruleCtx) bool {
			pct, ok := connPercent(v.Snap)
			return ok && pct < ctx.set.ConnPct-connCloseMargin
		},
	},
	{
		kind: KindBinlogDeadline,
		open: func(v View, ctx ruleCtx) (bool, string) {
			if !isReplica(v) || !notFollowing(v.Status.Code) || ctx.outSince.IsZero() ||
				ctx.master == nil || ctx.master.Snap == nil || ctx.master.Snap.BinlogExpireSec <= 0 {
				return false, ""
			}
			limit := time.Duration(ctx.master.Snap.BinlogExpireSec) * time.Second * binlogDeadlinePct / 100
			if ctx.now.Sub(ctx.outSince) <= limit {
				return false, ""
			}
			return true, fmt.Sprintf("Replika sudah lama tidak mengikuti master: lewat %d%% dari umur binlog master. "+
				"Bila binlog yang dibutuhkan terhapus, replika harus disync ulang. Saran: jalankan lagi replikasinya.",
				binlogDeadlinePct)
		},
		close: func(v View, _ ruleCtx) bool {
			return !isReplica(v) || v.Status.Code == StatusSynced || v.Status.Code == StatusLagging
		},
	},
}

func diskRule(kind string, threshold func(Settings) int) alertRule {
	return alertRule{
		kind: kind,
		open: func(v View, ctx ruleCtx) (bool, string) {
			pct, ok := diskPercent(v.Snap)
			if !ok || pct < threshold(ctx.set) {
				return false, ""
			}
			return true, fmt.Sprintf("Disk data terpakai %d%%.", pct)
		},
		close: func(v View, ctx ruleCtx) bool {
			pct, ok := diskPercent(v.Snap)
			return ok && pct < threshold(ctx.set)-diskCloseMargin
		},
	}
}

func isReplica(v View) bool { return v.Role == store.RoleReplica }
func isMaster(v View) bool  { return v.Role == store.RoleMaster }

// stopped: the replica stopped without the panel (never Dihentikan).
func stopped(code string) bool { return code == StatusStopped || code == StatusStoppedOutside }

// notFollowing: the replica does not take the master's binlog.
func notFollowing(code string) bool {
	return code == StatusDisconnected || stopped(code) || code == StatusPaused
}

func ioRunning(snap *proto.HealthSnapshot) bool {
	return snap != nil && snap.MySQLOK && snap.Replica != nil && snap.Replica.IORunning == "Yes"
}

// diskPercent is the data directory's used share; false when unknown.
func diskPercent(snap *proto.HealthSnapshot) (int, bool) {
	if snap == nil || snap.DiskTotal == 0 || snap.DiskFree > snap.DiskTotal {
		return 0, false
	}
	return int((snap.DiskTotal - snap.DiskFree) * 100 / snap.DiskTotal), true
}

// connPercent is threads_connected as a share of max_connections; false
// when unknown.
func connPercent(snap *proto.HealthSnapshot) (int, bool) {
	if snap == nil || !snap.MySQLOK || snap.MaxConnections <= 0 {
		return 0, false
	}
	return snap.ThreadsConnected * 100 / snap.MaxConnections, true
}

// stoppedMessage words a stopped replica: the status detail (errno, never
// MySQL's text) and, for a known error, its plain explanation and advice.
func stoppedMessage(v View) string {
	msg := v.Status.Detail + "."
	if v.Snap == nil || v.Snap.Replica == nil {
		return msg
	}
	rs := v.Snap.Replica
	if rs.LastSQLErrno == 0 && rs.LastIOErrno == 0 {
		return msg
	}
	exp := Explain(rs.LastSQLErrno, rs.LastIOErrno)
	msg += " " + exp.Text
	if exp.Advice != "" {
		msg += " Saran: " + exp.Advice + "."
	}
	return msg
}

// jobKindNames are the job kinds in words for people.
var jobKindNames = map[string]string{
	"prepare_master":   "Siapkan master",
	"create_replica":   "Buat replika",
	"add_databases":    "Tambah database",
	"remove_databases": "Lepas database",
	"resync_replica":   "Resync ulang",
}

// alertUpdate is one alert_update to publish once the lock is released.
type alertUpdate struct {
	id       int64
	kind     string
	serverID int64
	open     bool
}

// emitAlerts publishes alert_update for each update and, when there is
// any, wakes whoever sends the alerts.
func (m *Monitor) emitAlerts(ups []alertUpdate) {
	for _, u := range ups {
		m.publish("alert_update", map[string]any{"id": u.id, "kind": u.kind, "server_id": u.serverID, "open": u.open})
	}
	if len(ups) > 0 && m.onAlertsChanged != nil {
		m.onAlertsChanged()
	}
}

// Evaluate opens and closes the status alerts of every server by the
// rules of spec 5.3 at now. The report handlers and the tick run it
// themselves; a backlog batch is evaluated once, when it ends.
func (m *Monitor) Evaluate(now time.Time) {
	m.mu.Lock()
	ups := m.evaluate(now)
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// evaluate is Evaluate with mu held; it returns the alerts it opened or
// closed.
func (m *Monitor) evaluate(now time.Time) []alertUpdate {
	open, err := m.st.ListOpenAlerts()
	if err != nil {
		m.log.Warn("evaluate alerts: list open alerts", "err", err)
		return nil
	}
	type key struct {
		kind string
		id   int64
	}
	byKey := make(map[key]store.Alert, len(open))
	for _, a := range open {
		byKey[key{a.Kind, a.ServerID}] = a
	}
	var ups []alertUpdate
	for _, id := range slices.Sorted(maps.Keys(m.servers)) {
		s := m.servers[id]
		if s.snapBacklog && s.dirty {
			continue // a backlog batch not yet committed: judged at its end
		}
		v := s.view(id)
		// Only ready replicas and masters are judged; a server outside any
		// topology, or a replica being created, changed or resynced, keeps
		// no status alert open.
		active := v.Role != "" && v.Status.Code != StatusNotReady
		if active && v.Status.Code == "" {
			continue // nothing judged yet
		}
		ctx := m.ruleCtx(id, s, v, now)
		for _, r := range alertRules {
			a, isOpen := byKey[key{r.kind, id}]
			switch {
			case isOpen && (!active || r.close(v, ctx)):
				if err := m.st.CloseAlert(a.ID, now); err != nil {
					m.log.Warn("close alert", "server_id", id, "kind", r.kind, "err", err)
					continue
				}
				ups = append(ups, alertUpdate{a.ID, r.kind, id, false})
			case !active:
			case isOpen:
				if ok, msg := r.open(v, ctx); ok && msg != a.Message {
					if err := m.st.SetAlertMessage(a.ID, msg); err != nil {
						m.log.Warn("update alert message", "server_id", id, "kind", r.kind, "err", err)
					}
				}
			default:
				ok, msg := r.open(v, ctx)
				if !ok {
					continue
				}
				aid, err := m.st.OpenAlert(store.Alert{Kind: r.kind, ServerID: id, TopologyID: s.topoID, OpenedAt: now, Message: msg})
				if err != nil {
					m.log.Warn("open alert", "server_id", id, "kind", r.kind, "err", err)
					continue
				}
				ups = append(ups, alertUpdate{aid, r.kind, id, true})
			}
		}
	}
	return ups
}

// ruleCtx moves the server's condition clocks to now and returns what the
// rules see. The caller holds mu.
func (m *Monitor) ruleCtx(id int64, s *serverState, v View, now time.Time) ruleCtx {
	_, agentOff := agentOffline(s.online, s.offlineSince, m.staleBase(s, now), now, m.settings.StaleAfter)
	if pct, ok := connPercent(v.Snap); ok && !agentOff && pct >= m.settings.ConnPct {
		s.connHighSince = firstTime(s.connHighSince, now)
	} else {
		s.connHighSince = time.Time{}
	}
	if v.Status.Code == StatusSynced {
		s.syncedSince = firstTime(s.syncedSince, now)
	} else {
		s.syncedSince = time.Time{}
	}
	switch code := v.Status.Code; {
	case notFollowing(code):
		s.outSince = firstTime(s.outSince, v.Since)
	case code == StatusSynced || code == StatusLagging || code == StatusNotReady || v.Role != store.RoleReplica:
		s.outSince = time.Time{}
	} // otherwise (agent offline, MySQL not answering) unknown: the clock runs on

	ctx := ruleCtx{now: now, set: m.settings, agentOff: agentOff,
		connHighSince: s.connHighSince, syncedSince: s.syncedSince, outSince: s.outSince}
	if v.Role == store.RoleReplica {
		if ms, ok := m.servers[s.masterID]; ok && s.masterID != id {
			mv := ms.view(s.masterID)
			ctx.master = &mv
		}
	}
	return ctx
}

// JobFailed opens job_failed for the server, or points the open one at
// this job. The message names the kind of job only: errText may carry
// MySQL's raw error and stays out of it.
func (m *Monitor) JobFailed(serverID int64, jobID, kind, errText string) {
	_ = errText // deliberately unused; see above
	name, ok := jobKindNames[kind]
	if !ok {
		name = kind
	}
	msg := fmt.Sprintf("Pekerjaan %s gagal (job %s).", name, jobID)
	now := m.now()
	var ups []alertUpdate
	m.mu.Lock()
	m.failedJob[serverID] = jobID
	a, err := m.st.GetOpenAlert(KindJobFailed, serverID)
	switch {
	case err == nil:
		if a.Message != msg {
			if err := m.st.SetAlertMessage(a.ID, msg); err != nil {
				m.log.Warn("update alert message", "server_id", serverID, "kind", KindJobFailed, "err", err)
			}
		}
	case errors.Is(err, store.ErrNotFound):
		s := m.state(serverID)
		if !s.resolvedOK {
			if err := m.resolve(serverID, s); err != nil {
				m.log.Warn("job_failed: resolve role", "server_id", serverID, "err", err)
			}
		}
		id, err := m.st.OpenAlert(store.Alert{Kind: KindJobFailed, ServerID: serverID, TopologyID: s.topoID, OpenedAt: now, Message: msg})
		if err != nil {
			m.log.Warn("open alert", "server_id", serverID, "kind", KindJobFailed, "err", err)
			break
		}
		ups = append(ups, alertUpdate{id, KindJobFailed, serverID, true})
	default:
		m.log.Warn("read open alert", "server_id", serverID, "kind", KindJobFailed, "err", err)
	}
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// JobEnded closes the server's job_failed when jobID succeeded on a retry
// or was canceled; a newer failed job keeps it open. After a restart the
// failed job is not known, and any job ending closes it.
func (m *Monitor) JobEnded(serverID int64, jobID string) {
	now := m.now()
	var ups []alertUpdate
	m.mu.Lock()
	if last, ok := m.failedJob[serverID]; !ok || last == jobID {
		a, err := m.st.GetOpenAlert(KindJobFailed, serverID)
		switch {
		case err == nil:
			if err := m.st.CloseAlert(a.ID, now); err != nil {
				m.log.Warn("close alert", "server_id", serverID, "kind", KindJobFailed, "err", err)
				break
			}
			delete(m.failedJob, serverID)
			ups = append(ups, alertUpdate{a.ID, KindJobFailed, serverID, false})
		case !errors.Is(err, store.ErrNotFound):
			m.log.Warn("read open alert", "server_id", serverID, "kind", KindJobFailed, "err", err)
		}
	}
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// Snooze mutes the server's alert notifications until until (nil clears
// it). Alerts still open and close; only their sending waits.
func (m *Monitor) Snooze(serverID int64, until *time.Time) error {
	if err := m.st.SetSnooze(serverID, until); err != nil {
		return err
	}
	m.mu.Lock()
	if s, ok := m.servers[serverID]; ok {
		s.snooze = nil
		if until != nil {
			t := *until
			s.snooze = &t
		}
	}
	m.mu.Unlock()
	return nil
}
