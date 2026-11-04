package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Kinds of status_events rows.
const (
	kindStatus      = "status"
	kindLock        = "lock"
	kindRestart     = "restart"
	kindDisk        = "disk"
	kindConnections = "connections"
	kindAgent       = "agent"
)

// serverState is what the Monitor keeps per server; Monitor.mu guards it.
type serverState struct {
	// From the store, refreshed by resolve.
	role       string // store.RoleMaster, store.RoleReplica or "" (not monitored)
	topoID     int64
	masterID   int64 // the topology's master
	repState   string
	repPaused  bool
	snooze     *time.Time
	resolvedOK bool // role fields hold a successful resolve
	// awaitFresh is when the store last changed how this replica is
	// judged (its state became ready or attention, or paused flipped):
	// until a report taken at or after it arrives, or freshBound passes,
	// the status judged before the change stands, except that Agent
	// offline, Tidak terjangkau and Agent perlu dimutakhirkan are judged
	// as usual (heldThrough). Zero when not waiting.
	awaitFresh time.Time

	// From the agent.
	online       bool
	offlineSince time.Time
	needsUpgrade bool
	lastSeq      uint64
	snap         *proto.HealthSnapshot // never modified once stored
	receivedAt   time.Time
	snapBacklog  bool // snap came from the agent's backlog
	// graceUntil: the report was loaded from the store at startup and does
	// not count as stale before this time (one StaleAfter after startup);
	// zero once a report arrives.
	graceUntil time.Time

	// Derived.
	status   Status
	since    time.Time
	lockLost bool // last judged state of a replica's lock

	// When a condition first appeared; zero while it is absent.
	unreachableSince  time.Time
	disconnectedSince time.Time
	gapSince          time.Time
	// Clocks of the alert rules, moved by evaluate.
	connHighSince time.Time
	syncedSince   time.Time
	outSince      time.Time

	// Baselines of the snapshot events; -1 while unknown.
	lastUptime int64
	diskLevel  int
	connLevel  int

	dirty bool // changed since last persisted and published
}

// state returns the state of id, creating it; the caller holds mu. A new
// server counts as offline since the Monitor started, so the agent gets
// the usual grace to connect.
func (m *Monitor) state(id int64) *serverState {
	s, ok := m.servers[id]
	if !ok {
		s = &serverState{offlineSince: m.startedAt, lastUptime: -1, diskLevel: -1, connLevel: -1}
		m.servers[id] = s
	}
	return s
}

func (s *serverState) view(id int64) View {
	v := View{ServerID: id, Role: s.role, Status: s.status, Since: s.since, ReceivedAt: s.receivedAt,
		Snap: s.snap, NeedsUpgrade: s.needsUpgrade}
	if s.snooze != nil {
		t := *s.snooze
		v.SnoozeUntil = &t
	}
	return v
}

// load fills the state of every server from the store: role, snooze and
// the health row kept from the last run.
func (m *Monitor) load() {
	servers, err := m.st.ListServers()
	if err != nil {
		m.log.Warn("monitor: list servers", "err", err)
		return
	}
	rows, err := m.st.ListHealth()
	if err != nil {
		m.log.Warn("monitor: load health", "err", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, srv := range servers {
		s := m.state(srv.ID)
		s.snooze = srv.SnoozeUntil
		if err := m.resolve(srv.ID, s, time.Time{}); err != nil {
			m.log.Warn("monitor: resolve role", "server_id", srv.ID, "err", err)
		}
	}
	for _, h := range rows {
		s, ok := m.servers[h.ServerID]
		if !ok {
			continue
		}
		if h.SnapshotJSON != "" {
			var snap proto.HealthSnapshot
			if err := json.Unmarshal([]byte(h.SnapshotJSON), &snap); err != nil {
				m.log.Warn("monitor: stored snapshot unreadable", "server_id", h.ServerID, "err", err)
			} else {
				s.snap = &snap
				m.snapshotEvents(h.ServerID, s, &snap, h.ReceivedAt, false)
			}
		}
		s.receivedAt = h.ReceivedAt
		s.graceUntil = m.startedAt.Add(m.settings.StaleAfter)
		if h.Status != "" && s.role != "" {
			s.status = Status{Code: h.Status, Severity: SeverityOf(h.Status)}
			s.since = h.StatusSince
		}
		s.lockLost = s.role == store.RoleReplica && !h.LockOK
	}
}

// ingest takes one report placed at at; the caller holds mu. Reports
// older (by Seq) than the last one taken since the agent connected are
// ignored. A backlog report is not committed: the end of its batch is.
func (m *Monitor) ingest(id int64, he proto.HealthEvent, at time.Time) []healthUpdate {
	s := m.state(id)
	if he.Seq < s.lastSeq {
		m.log.Debug("health report older than the last one; ignored", "server_id", id, "seq", he.Seq, "last", s.lastSeq)
		return nil
	}
	s.lastSeq = he.Seq
	snap := he.Snapshot
	s.snap, s.receivedAt, s.snapBacklog, s.graceUntil = &snap, at, he.Backlog, time.Time{}
	s.needsUpgrade = false // only a current agent reports
	s.dirty = true
	if err := m.resolve(id, s, at); err != nil {
		m.log.Warn("monitor: resolve role", "server_id", id, "err", err)
	}
	m.snapshotEvents(id, s, &snap, at, true)
	if snap.MySQLOK {
		s.unreachableSince = time.Time{}
		if snap.Replica != nil && snap.Replica.IORunning != "Yes" {
			s.disconnectedSince = firstTime(s.disconnectedSince, at)
		} else {
			s.disconnectedSince = time.Time{}
		}
	} else {
		s.unreachableSince = firstTime(s.unreachableSince, at)
	}
	if s.resolvedOK {
		m.derive(id, s, at, true)
	}
	if he.Backlog {
		return nil
	}
	return m.commit(id, s, nil)
}

func firstTime(cur, at time.Time) time.Time {
	if cur.IsZero() {
		return at
	}
	return cur
}

// resolve reads the server's role from the store: a master when a
// topology names it, a replica when it has a replica row, otherwise not
// monitored. On error s keeps what it had. The caller holds mu.
//
// When a replica already resolved changes how it is judged (its state
// becomes ready or attention, or its paused flag flips: a job or a
// control just changed MySQL), the last report predates the change: the
// status judged before it is kept (awaitFresh) and a fresh report is
// fetched in the background, unless reportAt, the time of the report
// being ingested (zero otherwise), is already after the change.
func (m *Monitor) resolve(id int64, s *serverState, reportAt time.Time) error {
	topo, err := m.st.GetTopologyByMaster(id)
	switch {
	case err == nil:
		s.role, s.topoID, s.masterID, s.repState, s.repPaused = store.RoleMaster, topo.ID, id, "", false
		s.resolvedOK, s.awaitFresh = true, time.Time{}
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	rep, err := m.st.GetReplicaByServer(id)
	if errors.Is(err, store.ErrNotFound) {
		s.role, s.topoID, s.masterID, s.repState, s.repPaused = "", 0, 0, "", false
		s.resolvedOK, s.awaitFresh = true, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	topo, err = m.st.GetTopology(rep.TopologyID)
	if err != nil {
		return err
	}
	changed := s.resolvedOK && s.role == store.RoleReplica && (rep.State != s.repState || rep.Paused != s.repPaused)
	s.role, s.topoID, s.masterID, s.repState, s.repPaused = store.RoleReplica, topo.ID, topo.MasterServerID, rep.State, rep.Paused
	s.resolvedOK = true
	switch {
	case !judgedState(rep.State):
		s.awaitFresh = time.Time{} // Belum siap whatever the reports say
	case changed:
		now := m.now()
		s.awaitFresh = now
		if reportAt.IsZero() || reportAt.Before(now) {
			m.fetchFreshLocked(id)
		}
	}
	return nil
}

// judgedState reports whether a replica in state is judged by its reports
// (DeriveReplica); in any other state it is Belum siap.
func judgedState(state string) bool { return state == replicaReady || state == replicaAttention }

// freshBound is how long a replica's status waits for a report taken
// after a change in the store before the usual rules take over: two agent
// checks, at least minFreshBound and at most maxFreshBound (so a long
// check interval does not keep a stale status for hours). The caller
// holds mu.
func (m *Monitor) freshBound() time.Duration {
	return min(max(minFreshBound, 2*m.settings.CheckEvery), maxFreshBound)
}

const (
	minFreshBound = time.Minute
	maxFreshBound = 10 * time.Minute
)

// heldThrough reports whether a status with code replaces a held one: the
// agent's own condition (offline, too old, MySQL not answering) is judged
// as usual while a replica waits for a fresh report.
func heldThrough(code string) bool {
	return code == StatusAgentOffline || code == StatusUnreachable || code == StatusNeedsUpgrade
}

// derive computes the status of a resolved server at now and records a
// change; fromReport means a report taken at now is being ingested, so
// the agent was online then. The caller holds mu.
func (m *Monitor) derive(id int64, s *serverState, now time.Time, fromReport bool) {
	if s.role == "" {
		if s.status != (Status{}) {
			s.status, s.since, s.dirty = Status{}, time.Time{}, true
		}
		return
	}
	online := s.online || fromReport
	if online && s.snap == nil && !s.needsUpgrade {
		return // connected, first report not here yet
	}
	var offlineSince time.Time
	if !online {
		offlineSince = s.offlineSince
	}
	held := false
	if !s.awaitFresh.IsZero() {
		switch {
		case !s.receivedAt.Before(s.awaitFresh) || now.Sub(s.awaitFresh) > m.freshBound():
			s.awaitFresh = time.Time{}
		case s.status.Code != "":
			// The last report predates the change: judging it now would
			// read the old MySQL state with the new store state (a false
			// "Berhenti" right after Start replika or a resync). Only the
			// agent's own condition is judged (heldThrough).
			held = true
		}
	}
	th := m.settings.Thresholds
	var st Status
	switch s.role {
	case store.RoleMaster:
		st = DeriveMaster(MasterInput{
			AgentOnline: online, OfflineSince: offlineSince, ReceivedAt: m.staleBase(s, now),
			NeedsUpgrade: s.needsUpgrade, Snap: s.snap, UnreachableSince: s.unreachableSince,
			ReplicasDisconnected: m.replicasDisconnected(s.topoID, now), Now: now,
		}, th)
	case store.RoleReplica:
		masterGTID := ""
		ms := m.servers[s.masterID]
		if ms != nil && ms.snap != nil && ms.snap.MySQLOK {
			masterGTID = ms.snap.GTIDExecuted
		}
		// Agents report GTID movement only with their summaries, so the
		// gap clock moves only when the replica's report is at least as
		// new as the master's it is compared with; a backlog report is
		// never compared.
		if !held && masterGTID != "" && !s.snapBacklog && !s.receivedAt.Before(ms.receivedAt) &&
			s.snap != nil && s.snap.MySQLOK && s.snap.Replica != nil {
			rs := s.snap.Replica
			if GTIDGap(masterGTID, joinGTIDSets(rs.RetrievedGTID, rs.ExecutedGTID)) > 0 {
				s.gapSince = firstTime(s.gapSince, now)
			} else {
				s.gapSince = time.Time{}
			}
		}
		st = DeriveReplica(ReplicaInput{
			State: s.repState, Paused: s.repPaused, AgentOnline: online, OfflineSince: offlineSince,
			ReceivedAt: m.staleBase(s, now), NeedsUpgrade: s.needsUpgrade, Snap: s.snap,
			MasterGTIDExecuted: masterGTID, GapSince: s.gapSince, DisconnectedSince: s.disconnectedSince, Now: now,
		}, th)
	}
	if held && !heldThrough(st.Code) {
		return
	}
	m.apply(id, s, st, now)
}

// staleBase is the report time the staleness check measures from: the
// report's own, except that a report loaded at startup counts from
// startup until its grace ends. The caller holds mu.
func (m *Monitor) staleBase(s *serverState, now time.Time) time.Time {
	if now.Before(s.graceUntil) && s.receivedAt.Before(m.startedAt) {
		return m.startedAt
	}
	return s.receivedAt
}

// replicasDisconnected counts the topology's replicas whose agent is
// online, whose last report is not stale, and which shows the IO thread
// not running. The caller holds mu.
func (m *Monitor) replicasDisconnected(topoID int64, now time.Time) int {
	reps, err := m.st.ListReplicas(topoID)
	if err != nil {
		m.log.Warn("monitor: list replicas", "topology_id", topoID, "err", err)
		return 0
	}
	n := 0
	for _, rep := range reps {
		rs := m.servers[rep.ServerID]
		if rs == nil || !rs.online || rs.snap == nil || now.Sub(m.staleBase(rs, now)) > m.settings.StaleAfter {
			continue
		}
		if rs.snap.MySQLOK && rs.snap.Replica != nil && rs.snap.Replica.IORunning != "Yes" {
			n++
		}
	}
	return n
}

// apply makes st the server's status, writing a status event when the
// code changes and a lock event when a judged replica's lock flips. The
// caller holds mu.
func (m *Monitor) apply(id int64, s *serverState, st Status, now time.Time) {
	old := s.status
	if st.Code != old.Code {
		m.addEvent(id, s, now, kindStatus, old.Code, st.Code, eventDetail(st))
		s.since = now
		s.dirty = true
	}
	if st.Severity != old.Severity || st.LockLost != old.LockLost {
		s.dirty = true
	}
	if s.role == store.RoleReplica && lockJudged(st.Code) && st.LockLost != s.lockLost {
		detail := "Kunci kembali"
		if st.LockLost {
			detail = "Kunci terlepas"
		}
		m.addEvent(id, s, now, kindLock, "", "", detail)
		s.lockLost = st.LockLost
		s.dirty = true
	}
	s.status = st
}

// eventDetail is the detail of a status event: the status's own detail,
// except MySQL's raw error text, which stays out of the timeline (it may
// name users and hosts, and the timeline feeds notifications).
func eventDetail(st Status) string {
	if st.Code == StatusUnreachable {
		return "MySQL tidak menjawab"
	}
	return st.Detail
}

// lockJudged reports whether DeriveReplica looked at the lock to reach
// code: the replica was ready, its agent current and MySQL answering.
func lockJudged(code string) bool {
	switch code {
	case StatusNotReady, StatusAgentOffline, StatusNeedsUpgrade, StatusUnreachable:
		return false
	}
	return true
}

// snapshotEvents records restarts and threshold crossings of disk and
// connections against the previous readings, and moves the baselines to
// snap; with record false it only sets the baselines. The caller holds mu.
func (m *Monitor) snapshotEvents(id int64, s *serverState, snap *proto.HealthSnapshot, at time.Time, record bool) {
	th := m.settings.Thresholds
	if snap.MySQLOK {
		if record && s.lastUptime >= 0 && snap.UptimeSec < s.lastUptime {
			m.addEvent(id, s, at, kindRestart, "", "", "MySQL dimulai ulang")
		}
		s.lastUptime = snap.UptimeSec
		if pct, ok := connPercent(snap); ok {
			lvl := 0
			if pct >= th.ConnPct {
				lvl = 1
			}
			if record && s.connLevel >= 0 && lvl != s.connLevel {
				m.addEvent(id, s, at, kindConnections, "", "", fmt.Sprintf("Koneksi MySQL %d%% dari batas", pct))
			}
			s.connLevel = lvl
		}
	}
	if pct, ok := diskPercent(snap); ok {
		lvl := 0
		switch {
		case pct >= th.DiskCrit:
			lvl = 2
		case pct >= th.DiskWarn:
			lvl = 1
		}
		if record && s.diskLevel >= 0 && lvl != s.diskLevel {
			m.addEvent(id, s, at, kindDisk, "", "", fmt.Sprintf("Disk data terpakai %d%%", pct))
		}
		s.diskLevel = lvl
	}
}

// addEvent writes a status_events row; a failure is logged. The caller
// holds mu.
func (m *Monitor) addEvent(id int64, s *serverState, at time.Time, kind, from, to, detail string) {
	if _, err := m.st.AddStatusEvent(store.StatusEvent{ServerID: id, TopologyID: s.topoID, TS: at,
		Kind: kind, FromStatus: from, ToStatus: to, Detail: detail}); err != nil {
		m.log.Warn("record status event", "server_id", id, "kind", kind, "err", err)
	}
}

// commit persists a dirty state as the server's health row and appends
// its health_update to ups. The caller holds mu.
func (m *Monitor) commit(id int64, s *serverState, ups []healthUpdate) []healthUpdate {
	if !s.dirty {
		return ups
	}
	s.dirty = false
	if s.snap != nil || s.status.Code != "" {
		h := store.Health{ServerID: id, ReceivedAt: s.receivedAt, Status: s.status.Code,
			StatusSince: s.since, LockOK: !s.lockLost}
		if s.snap != nil {
			b, err := json.Marshal(s.snap)
			if err == nil {
				h.SnapshotJSON = string(b)
			}
			h.AgentVersion = s.snap.AgentVersion
		}
		if err := m.st.SetHealth(h); err != nil {
			m.log.Warn("store server health", "server_id", id, "err", err)
		}
	}
	return append(ups, healthUpdate{id: id, status: s.status.Code, severity: s.status.Severity})
}
