package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The promotion record as the orchestrator keeps it (promotions.status and
// replicas_json, orchestrator.PromotionServer). Literals: this package
// cannot import the orchestrator, which imports it.
const (
	promotionRunning = "running"
	promotionPartial = "partial"
	promotionDone    = "done"
	promotionFailed  = "failed"

	promotionOldMaster      = "old_master"
	promotionReplica        = "replica"
	quarantineStatePending  = "quarantine_pending"
	promotionModeEmergency  = "emergency"
	maxPromoteEventPayload  = 4 << 10
	noticeFor               = 24 * time.Hour // master_quarantined, master_lock_expired
	lockExpiredUnlocked     = "unlocked"
	lockExpiredKept         = "kept"
	lockKeptUnlockedReason  = "agent membuka kunci promote"
	startBlockedOverReason  = "MySQL master lama menyala lagi"
	leftoverCheckedReason   = "Periksa data tertinggal sudah dijalankan"
	noticeExpiredReason     = "ditutup otomatis setelah 24 jam"
	lockExpiredChangeReason = "diganti laporan kunci yang lebih baru"
)

// promotionServer is one entry of promotions.replicas_json.
type promotionServer struct {
	ServerID int64  `json:"server_id"`
	Role     string `json:"role"`
	State    string `json:"state"`
}

func promotionServers(p store.Promotion) []promotionServer {
	var out []promotionServer
	if json.Unmarshal([]byte(p.ReplicasJSON), &out) != nil {
		return nil
	}
	return out
}

// oldMasterState is the old master's state in p's record; "" when it has
// none.
func oldMasterState(p store.Promotion) string {
	for _, s := range promotionServers(p) {
		if s.Role == promotionOldMaster {
			return s.State
		}
	}
	return ""
}

// CheckPromotions opens and closes the topology alerts of promotions
// (spec 5.14) from the store: promote_cutover while a topology's
// cutover_pending is set, on its master; promote_partial while the
// topology's latest promotion is partial, on its new master. The tick
// runs it.
func (m *Monitor) CheckPromotions(now time.Time) {
	m.mu.Lock()
	ups := m.checkPromotions(now)
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// promoteWant is an alert a topology should have open.
type promoteWant struct {
	topoID int64
	msg    string
}

// checkPromotions is CheckPromotions with mu held.
func (m *Monitor) checkPromotions(now time.Time) []alertUpdate {
	topos, err := m.st.ListTopologies()
	if err != nil {
		m.log.Warn("promote alerts: list topologies", "err", err)
		return nil
	}
	open, err := m.st.ListOpenAlerts()
	if err != nil {
		m.log.Warn("promote alerts: list open alerts", "err", err)
		return nil
	}
	// The alerts wanted, by kind and server.
	want := map[string]map[int64]promoteWant{KindPromoteCutover: {}, KindPromotePartial: {}}
	latest := map[int64]*store.Promotion{} // by topology; nil when none
	for _, topo := range topos {
		var prom *store.Promotion
		switch p, err := m.st.LatestPromotion(topo.ID); {
		case err == nil:
			prom = &p
		case !errors.Is(err, store.ErrNotFound):
			m.log.Warn("promote alerts: latest promotion", "topology_id", topo.ID, "err", err)
			continue
		}
		latest[topo.ID] = prom
		if topo.CutoverPending {
			want[KindPromoteCutover][topo.MasterServerID] = promoteWant{topo.ID, m.cutoverMessage(topo, prom)}
		}
		if prom != nil && prom.Status == promotionPartial {
			want[KindPromotePartial][prom.NewMasterServerID] = promoteWant{topo.ID, m.partialMessage(topo, *prom)}
		}
	}
	var ups []alertUpdate
	for _, a := range open {
		wanted, ok := want[a.Kind]
		if !ok {
			continue
		}
		if w, ok := wanted[a.ServerID]; ok && w.topoID == a.TopologyID {
			delete(wanted, a.ServerID)
			if a.Message != w.msg {
				if err := m.st.SetAlertMessage(a.ID, w.msg); err != nil {
					m.log.Warn("update alert message", "server_id", a.ServerID, "kind", a.Kind, "err", err)
				}
			}
			continue
		}
		reason, keep := promoteCloseReason(a, latest)
		if keep {
			continue
		}
		if err := m.st.CloseAlertWithReason(a.ID, now, reason); err != nil {
			m.log.Warn("close alert", "server_id", a.ServerID, "kind", a.Kind, "err", err)
			continue
		}
		ups = append(ups, alertUpdate{a.ID, a.Kind, a.ServerID, false})
	}
	ups = append(ups, m.quarantinedFromStore(now)...)
	for _, kind := range []string{KindPromoteCutover, KindPromotePartial} {
		for serverID, w := range want[kind] {
			id, err := m.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, TopologyID: w.topoID, OpenedAt: now, Message: w.msg})
			if err != nil {
				// ErrAlertOpen: open for another topology, which closes first.
				m.log.Warn("open alert", "server_id", serverID, "kind", kind, "err", err)
				continue
			}
			ups = append(ups, alertUpdate{id, kind, serverID, true})
		}
	}
	return ups
}

// promoteCloseReason decides an open promote alert that is no longer
// wanted on its server: keep (a partial promotion being retried), close
// as resolved (""), or close with the reason it did not resolve.
func promoteCloseReason(a store.Alert, latest map[int64]*store.Promotion) (reason string, keep bool) {
	p, known := latest[a.TopologyID]
	if !known {
		return "topologi sudah tidak ada", false
	}
	switch a.Kind {
	case KindPromoteCutover:
		if p != nil && p.NewMasterServerID != a.ServerID {
			return "master berganti oleh promote lain", false
		}
		return "", false // "Sudah saya alihkan"
	default: // KindPromotePartial
		switch {
		case p == nil || p.NewMasterServerID != a.ServerID:
			return "promote lain dimulai", false
		case p.Status == promotionRunning:
			return "", true // Ulangi runs the remaining steps
		case p.Status == promotionDone:
			return "", false
		case p.Status == promotionFailed:
			return "promote berakhir gagal", false
		}
		return "promote tidak lagi selesai sebagian", false
	}
}

// serverName is the server's name, or "server <id>" when it cannot be
// read. The caller holds mu.
func (m *Monitor) serverName(id int64) string {
	if srv, err := m.st.GetServer(id); err == nil {
		return srv.Name
	}
	return fmt.Sprintf("server %d", id)
}

func modeWord(mode string) string {
	if mode == promotionModeEmergency {
		return "darurat"
	}
	return "terencana"
}

// cutoverMessage words promote_cutover (spec 5.5): the mode, the old and
// new master, how the other replicas fared, and the new address clients
// must move to. prom is the topology's latest promotion (nil: unknown).
func (m *Monitor) cutoverMessage(topo store.Topology, prom *store.Promotion) string {
	msg := "Promote"
	if prom != nil && prom.NewMasterServerID == topo.MasterServerID {
		msg += " " + modeWord(prom.Mode)
	} else {
		prom = nil
	}
	msg += " selesai: " + m.serverName(topo.MasterServerID) + " kini master"
	if topo.OldMasterServerID != 0 {
		msg += ", menggantikan " + m.serverName(topo.OldMasterServerID)
	}
	msg += fmt.Sprintf(". Alamat master baru: %s:%d.", topo.SourceHost, topo.SourcePort)
	if prom != nil {
		var following, repoint, rebuild int
		for _, s := range promotionServers(*prom) {
			if s.Role != promotionReplica {
				continue
			}
			switch s.State {
			case store.ReplicaNeedsRepoint:
				repoint++
			case store.ReplicaNeedsRebuild:
				rebuild++
			default:
				following++
			}
		}
		msg += fmt.Sprintf(" Replika ikut: %d", following)
		if repoint > 0 {
			msg += fmt.Sprintf("; perlu diarahkan ulang: %d", repoint)
		}
		if rebuild > 0 {
			msg += fmt.Sprintf("; perlu dibangun ulang: %d", rebuild)
		}
		msg += "."
		if oldMasterState(*prom) == quarantineStatePending {
			msg += " Master lama belum dikarantina; jangan hidupkan sebelum agent-nya tersambung."
		}
	}
	return msg + ` Arahkan semua aplikasi yang menulis ke database ke alamat baru, lalu tekan "Sudah saya alihkan".`
}

// retryAdvice names the job page's button that resumes a partial promote.
const retryAdvice = `Buka job promote di halaman Pekerjaan dan tekan "Ulangi dari tahap ini".`

// partialMessage words promote_partial by what stopped: the swap not
// made (the master stays locked: an outage), the old master not yet
// quarantined, or replicas not yet moved. topo is the promotion's
// topology.
func (m *Monitor) partialMessage(topo store.Topology, p store.Promotion) string {
	head := fmt.Sprintf("Promote %s dari %s ke %s selesai sebagian: ", modeWord(p.Mode),
		m.serverName(p.OldMasterServerID), m.serverName(p.NewMasterServerID))
	switch {
	case topo.MasterServerID == p.OldMasterServerID && p.Mode == promotionModeEmergency:
		// An emergency promote takes no lock: the master is down.
		return head + fmt.Sprintf("%s belum menjadi master dan master %s tidak bisa dipakai, jadi aplikasi tidak bisa menulis. ",
			m.serverName(p.NewMasterServerID), m.serverName(p.OldMasterServerID)) + retryAdvice
	case topo.MasterServerID == p.OldMasterServerID:
		return head + fmt.Sprintf("%s belum menjadi master dan master %s masih dikunci, jadi aplikasi tidak bisa menulis. ",
			m.serverName(p.NewMasterServerID), m.serverName(p.OldMasterServerID)) + retryAdvice
	case oldMasterState(p) == quarantineStatePending:
		return head + fmt.Sprintf("master lama %s belum dikarantina karena agent-nya belum terjangkau; jangan hidupkan "+
			"MySQL-nya sebelum agent tersambung. ", m.serverName(p.OldMasterServerID)) + retryAdvice
	}
	var repoint, rebuild int
	for _, s := range promotionServers(p) {
		switch {
		case s.Role != promotionReplica:
		case s.State == store.ReplicaNeedsRepoint:
			repoint++
		case s.State == store.ReplicaNeedsRebuild:
			rebuild++
		}
	}
	return head + fmt.Sprintf("%s sudah menjadi master; replika yang belum diarahkan ulang: %d, perlu dibangun ulang: %d. ",
		m.serverName(p.NewMasterServerID), repoint, rebuild) + retryAdvice
}

// quarantinedMessage words master_quarantined; newSource ("" unknown) is
// the new master's host:port.
func quarantinedMessage(newSource string) string {
	msg := "Master lama dikarantina: MySQL-nya kini hanya-baca, aplikasi tidak bisa menulis ke sana."
	if src := agentText(newSource, maxAgentHost); src != "" {
		msg += " Master baru: " + src + "."
	}
	return msg
}

// quarantinedFromStore opens master_quarantined, once per promotion, for
// an old master the store holds quarantined (not pending): the panel's
// own mysql.quarantine (a planned promote) sends no monitor.quarantined;
// the event remains the trigger of a deferred quarantine. The caller
// holds mu.
func (m *Monitor) quarantinedFromStore(now time.Time) []alertUpdate {
	var ups []alertUpdate
	for _, id := range slices.Sorted(maps.Keys(m.servers)) {
		s := m.servers[id]
		if s.role != store.RoleQuarantined || s.quarantinePending {
			continue
		}
		p, err := m.st.PromotionByOldMaster(id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				m.log.Warn("master_quarantined: promotion", "server_id", id, "err", err)
			}
			continue
		}
		n, err := m.st.CountAlertsOpenedSince(KindMasterQuarantined, id, p.StartedAt)
		if err != nil {
			m.log.Warn("master_quarantined: count alerts", "server_id", id, "err", err)
			continue
		}
		if n > 0 {
			continue
		}
		src := ""
		if s.topoID != 0 {
			if topo, err := m.st.GetTopology(s.topoID); err == nil {
				src = fmt.Sprintf("%s:%d", topo.SourceHost, topo.SourcePort)
			}
		}
		aid, err := m.st.OpenAlert(store.Alert{Kind: KindMasterQuarantined, ServerID: id, TopologyID: s.topoID,
			OpenedAt: now, Message: quarantinedMessage(src)})
		if err != nil {
			if !errors.Is(err, store.ErrAlertOpen) {
				m.log.Warn("open alert", "server_id", id, "kind", KindMasterQuarantined, "err", err)
			}
			continue
		}
		ups = append(ups, alertUpdate{aid, KindMasterQuarantined, id, true})
	}
	return ups
}

// handlePromoteEvent takes the promote lock guard's events (proto
// EventQuarantined, EventLockExpired); false for any other event.
func (m *Monitor) handlePromoteEvent(serverID int64, ev proto.Event) bool {
	if ev.Type != proto.EventQuarantined && ev.Type != proto.EventLockExpired {
		return false
	}
	// A kept lock's event too large or unreadable to judge still says the
	// lock expired: the generic kept notice opens (SP-9).
	keptAnyway := func() {
		if ev.Type == proto.EventLockExpired {
			m.notice(serverID, KindMasterLockExpired, lockExpiredMessage(proto.LockExpiredEvent{Action: lockExpiredKept}))
		}
	}
	if len(ev.Payload) > maxPromoteEventPayload {
		m.log.Warn("promote event too large", "server_id", serverID, "type", ev.Type, "bytes", len(ev.Payload))
		keptAnyway()
		return true
	}
	var kind, msg string
	switch ev.Type {
	case proto.EventQuarantined:
		var qe proto.QuarantinedEvent
		if err := json.Unmarshal(ev.Payload, &qe); err != nil {
			m.log.Warn("monitor.quarantined unreadable; ignored", "server_id", serverID, "err", err)
			return true
		}
		kind, msg = KindMasterQuarantined, quarantinedMessage(qe.NewSource)
	default:
		var le proto.LockExpiredEvent
		if err := json.Unmarshal(ev.Payload, &le); err != nil {
			m.log.Warn("monitor.lockExpired unreadable", "server_id", serverID, "err", err)
			keptAnyway()
			return true
		}
		if le.Action == lockExpiredUnlocked {
			m.closeAlert(serverID, KindMasterLockKept, lockKeptUnlockedReason)
		}
		if hosts := keptHosts(le.Hosts); le.Action == lockExpiredKept && len(hosts) > 0 {
			// The lock is kept for replicas that cannot be read: one alert
			// that stays while the lock holds, its hosts kept current.
			m.raise(serverID, KindMasterLockKept, lockKeptMessage(hosts))
			return true
		}
		kind, msg = KindMasterLockExpired, lockExpiredMessage(le)
	}
	m.notice(serverID, kind, msg)
	return true
}

// lockExpiredMessage words monitor.lockExpired; the guard's reason is left
// out (it may carry MySQL's own error text).
func lockExpiredMessage(le proto.LockExpiredEvent) string {
	cand := "kandidat"
	if c := agentText(le.Candidate, maxAgentHost); c != "" {
		cand = "kandidat " + c
	}
	if le.Action == lockExpiredUnlocked {
		return "Batas waktu kunci promote di master habis dan " + cand + " masih replika: promote tidak terjadi, " +
			"master membuka kunci sendiri dan bisa ditulisi lagi."
	}
	return lockKeptPrefix + cand + " belum bisa dipastikan; aplikasi belum bisa menulis. Periksa job promote di " +
		`halaman Pekerjaan; bila promote tidak dilanjutkan, tekan "Buka kunci master" di halaman master.`
}

// lockKeptPrefix starts the message of a lock the guard kept.
const lockKeptPrefix = "Batas waktu kunci promote di master habis, tetapi master tetap dikunci karena "

// notice opens a one-time notice of kind on serverID with msg. One open
// already says the same (any master_quarantined does): nothing changes
// (once per noticeFor); a master_lock_expired that says otherwise (kept,
// then unlocked) is replaced.
func (m *Monitor) notice(serverID int64, kind, msg string) {
	now := m.now()
	var ups []alertUpdate
	m.mu.Lock()
	a, err := m.st.GetOpenAlert(kind, serverID)
	switch {
	case err == nil && (a.Message == msg || kind == KindMasterQuarantined):
		m.mu.Unlock()
		return
	case err == nil:
		if err := m.st.CloseAlertWithReason(a.ID, now, lockExpiredChangeReason); err != nil {
			m.log.Warn("close alert", "server_id", serverID, "kind", kind, "err", err)
			m.mu.Unlock()
			return
		}
		ups = append(ups, alertUpdate{a.ID, kind, serverID, false})
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
		m.mu.Unlock()
		return
	}
	s := m.state(serverID)
	if !s.resolvedOK {
		if err := m.resolve(serverID, s, time.Time{}); err != nil {
			m.log.Warn("notice: resolve role", "server_id", serverID, "err", err)
		}
	}
	id, err := m.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, TopologyID: s.topoID, OpenedAt: now, Message: msg})
	if err != nil {
		m.log.Warn("open alert", "server_id", serverID, "kind", kind, "err", err)
	} else {
		ups = append(ups, alertUpdate{id, kind, serverID, true})
		if kind == KindMasterQuarantined {
			ups = append(ups, m.closeNotices(now)...) // a lock kept is over
		}
	}
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// closeNotices closes the one-time notices open for noticeFor, and a lock
// kept (master_lock_expired, master_lock_kept) once it is over: the server was quarantined
// or the master can be written again. The caller holds mu.
func (m *Monitor) closeNotices(now time.Time) []alertUpdate {
	open, err := m.st.ListOpenAlerts()
	if err != nil {
		m.log.Warn("close notices: list open alerts", "err", err)
		return nil
	}
	quarantined := map[int64]bool{}
	for _, a := range open {
		if a.Kind == KindMasterQuarantined {
			quarantined[a.ServerID] = true
		}
	}
	var ups []alertUpdate
	for _, a := range open {
		if a.Kind != KindMasterQuarantined && a.Kind != KindMasterLockExpired && a.Kind != KindMasterLockKept {
			continue
		}
		reason := ""
		switch {
		// master_lock_kept has no age limit: the lock may hold for days,
		// and the agent keeps its hosts current.
		case a.Kind != KindMasterLockKept && now.Sub(a.OpenedAt) >= noticeFor:
			reason = noticeExpiredReason
		case a.Kind == KindMasterLockKept || (a.Kind == KindMasterLockExpired && strings.HasPrefix(a.Message, lockKeptPrefix)):
			reason = m.lockKeptOver(a, quarantined[a.ServerID])
		}
		if reason == "" {
			continue
		}
		if err := m.st.CloseAlertWithReason(a.ID, now, reason); err != nil {
			m.log.Warn("close alert", "server_id", a.ServerID, "kind", a.Kind, "err", err)
			continue
		}
		ups = append(ups, alertUpdate{a.ID, a.Kind, a.ServerID, false})
	}
	return ups
}

// lockKeptOver says why the lock kept that a (master_lock_expired or
// master_lock_kept) reports
// is over; "" while it may still hold. quarantined: master_quarantined is
// open for the server. The caller holds mu.
func (m *Monitor) lockKeptOver(a store.Alert, quarantined bool) string {
	s, ok := m.servers[a.ServerID]
	switch {
	case quarantined:
		return "master sudah dikarantina sebagai master lama"
	case ok && s.role == store.RoleQuarantined:
		// Its quarantine may still be pending: say only what is known.
		return "promote selesai; server kini master lama"
	case !ok:
		return ""
	case s.role == store.RoleMaster && s.status.Code == StatusHealthy && !s.receivedAt.Before(a.OpenedAt):
		// Judged on a report taken after the notice: the lock was lifted
		// (unlocked from the panel, or the promote did not happen).
		return "master sudah bisa ditulisi lagi"
	}
	return ""
}

// raise opens an alert of kind for serverID with msg, or, when one is
// open already, puts msg in it (the agent repeats its events, and what
// they name can change). The alert stays one.
func (m *Monitor) raise(serverID int64, kind, msg string) {
	m.mu.Lock()
	ups := m.raiseLocked(serverID, kind, msg, nil)
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// raiseLocked is raise with mu held; it appends to ups what it opened.
func (m *Monitor) raiseLocked(serverID int64, kind, msg string, ups []alertUpdate) []alertUpdate {
	ups, _ = m.raiseOpenLocked(serverID, kind, msg, ups)
	return ups
}

// raiseOpenLocked is raiseLocked that also says whether an alert of kind
// is open for serverID afterwards (false when reading or opening failed).
func (m *Monitor) raiseOpenLocked(serverID int64, kind, msg string, ups []alertUpdate) ([]alertUpdate, bool) {
	a, err := m.st.GetOpenAlert(kind, serverID)
	switch {
	case err == nil:
		if a.Message != msg {
			if err := m.st.SetAlertMessage(a.ID, msg); err != nil {
				m.log.Warn("update alert message", "server_id", serverID, "kind", kind, "err", err)
			}
		}
		return ups, true
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
		return ups, false
	}
	s := m.state(serverID)
	if !s.resolvedOK {
		if err := m.resolve(serverID, s, time.Time{}); err != nil {
			m.log.Warn("raise: resolve role", "server_id", serverID, "err", err)
		}
	}
	var id int64
	err = nil
	if m.openFault != nil {
		err = m.openFault()
	}
	if err == nil {
		id, err = m.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, TopologyID: s.topoID, OpenedAt: m.now(), Message: msg})
	}
	if err != nil {
		m.log.Warn("open alert", "server_id", serverID, "kind", kind, "err", err)
		return ups, false
	}
	return append(ups, alertUpdate{id, kind, serverID, true}), true
}

// closeAlert closes the open alert of kind for serverID, if any; reason
// "" is a recovery.
func (m *Monitor) closeAlert(serverID int64, kind, reason string) {
	m.mu.Lock()
	ups := m.closeAlertLocked(serverID, kind, reason, nil)
	m.mu.Unlock()
	m.emitAlerts(ups)
}

// closeAlertLocked is closeAlert with mu held; it appends to ups what it
// closed.
func (m *Monitor) closeAlertLocked(serverID int64, kind, reason string, ups []alertUpdate) []alertUpdate {
	ups, _ = m.closeAlertOKLocked(serverID, kind, reason, ups)
	return ups
}

// closeAlertOKLocked is closeAlertLocked that also says whether no alert
// of kind is open for serverID afterwards (false when reading or closing
// failed).
func (m *Monitor) closeAlertOKLocked(serverID int64, kind, reason string, ups []alertUpdate) ([]alertUpdate, bool) {
	a, err := m.st.GetOpenAlert(kind, serverID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
			return ups, false
		}
		return ups, true
	}
	if err := m.st.CloseAlertWithReason(a.ID, m.now(), reason); err != nil {
		m.log.Warn("close alert", "server_id", serverID, "kind", kind, "err", err)
		return ups, false
	}
	return append(ups, alertUpdate{a.ID, kind, serverID, false}), true
}

// handleStartEvent takes the start lock guard's events of a quarantined
// server (proto EventQuarantineStart, EventQuarantineStartFailed); false
// for any other event. An event with a start id comes from an agent that
// reports the state of its starts in every snapshot: its alerts follow
// that state (startResultFromSnapshot) and the event changes nothing. An
// event without one (an older agent) is judged as it always was: one that
// cannot be read opens the worst it could say and closes nothing; one
// whose result is not one of the three changes nothing: only a result the
// panel knows closes an alert about a server that may have been writable.
func (m *Monitor) handleStartEvent(serverID int64, ev proto.Event) bool {
	if ev.Type != proto.EventQuarantineStart && ev.Type != proto.EventQuarantineStartFailed {
		return false
	}
	// A report too large or unreadable to judge is taken as the worst it
	// could say (SP-9): a failed start, or a late one (whose remedy,
	// "Periksa data tertinggal", is what closes it). It closes nothing and
	// never rewrites an open alert.
	worst := func() {
		if ev.Type == proto.EventQuarantineStartFailed {
			m.raiseIfNone(serverID, KindMasterStartBlocked, startBlockedMessage(""))
			return
		}
		m.raiseIfNone(serverID, KindMasterStartLate, startUnreadableText)
	}
	leftToSnapshot := func(startID string) bool {
		if cleanStartID(startID) == "" {
			return false
		}
		m.log.Debug("start event left to the snapshot's start state", "server_id", serverID, "type", ev.Type, "start_id", startID)
		return true
	}
	if len(ev.Payload) > maxPromoteEventPayload {
		m.log.Warn("start lock event too large", "server_id", serverID, "type", ev.Type, "bytes", len(ev.Payload))
		worst()
		return true
	}
	if ev.Type == proto.EventQuarantineStartFailed {
		var fe proto.QuarantineStartFailedEvent
		if err := json.Unmarshal(ev.Payload, &fe); err != nil {
			m.log.Warn("monitor.quarantineStartFailed unreadable", "server_id", serverID, "err", err)
			worst()
			return true
		}
		if leftToSnapshot(fe.StartID) {
			return true
		}
		m.raise(serverID, KindMasterStartBlocked, startBlockedMessage(fe.File))
		return true
	}
	var qe proto.QuarantineStartEvent
	if err := json.Unmarshal(ev.Payload, &qe); err != nil {
		m.log.Warn("monitor.quarantineStart unreadable", "server_id", serverID, "err", err)
		worst()
		return true
	}
	if leftToSnapshot(qe.StartID) {
		return true
	}
	known := qe.Result == proto.QuarantineStartLate || qe.Result == proto.QuarantineStartBypassed ||
		qe.Result == proto.QuarantineStartOK
	bypassed := qe.Bypassed || qe.Result == proto.QuarantineStartBypassed
	if !known && !bypassed {
		m.log.Warn("monitor.quarantineStart with an unknown result; ignored", "server_id", serverID, "result", qe.Result)
		return true
	}
	var ups []alertUpdate
	m.mu.Lock()
	if known {
		// MySQL started: a start that failed on the lock file is over. A
		// bypass of an earlier start is over when this one used the lock.
		ups = m.closeAlertLocked(serverID, KindMasterStartBlocked, startBlockedOverReason, ups)
		if !bypassed {
			ups = m.closeAlertLocked(serverID, KindMasterStartBypassed, "", ups)
		}
	}
	if qe.Result == proto.QuarantineStartLate {
		ups = m.raiseLocked(serverID, KindMasterStartLate, startLateText, ups)
	}
	if bypassed {
		ups = m.raiseLocked(serverID, KindMasterStartBypassed, startBypassedText, ups)
	}
	m.mu.Unlock()
	m.emitAlerts(ups)
	return true
}

const (
	startResultSlack = time.Minute // decided time read back from a relative age
	// maxStartAge bounds the age an agent gives a start result.
	maxStartAge = 10 * 365 * 24 * 3600 // seconds
	// maxStartIDLen bounds a start id; a longer one counts as none.
	maxStartIDLen = 64
	// maxStartKinds bounds the entries of a start state looked at.
	maxStartKinds = 16
)

// cleanStartID is id, or "" when it is empty or longer than maxStartIDLen
// (an id comes from an agent: untrusted data).
func cleanStartID(id string) string {
	if len(id) > maxStartIDLen {
		return ""
	}
	return id
}

// startDecidedAt is when a start result was decided by the panel's clock:
// the report's time minus its age in seconds, the age clamped to
// [0, maxStartAge] before it is multiplied (the agent is untrusted). It is
// compared only with times of the panel's clock (the promotion's start, the
// leftover check), never used to tell one start from another.
func startDecidedAt(at time.Time, ageSec int64) time.Time {
	return at.Add(-time.Duration(min(max(ageSec, 0), maxStartAge)) * time.Second)
}

// startKindAlert maps a kind of the start state to its alert kind, the
// text the alert opens with and the reason its end closes it with (the
// reasons the start events have always used); ok is false for anything
// unknown.
func startKindAlert(e proto.StartKindState) (kind, msg, overReason string, ok bool) {
	switch e.Kind {
	case proto.QuarantineStartLate:
		return KindMasterStartLate, startLateText, "", true
	case proto.QuarantineStartFailed:
		return KindMasterStartBlocked, startBlockedMessage(e.InitFile), startBlockedOverReason, true
	case proto.QuarantineStartBypassed:
		return KindMasterStartBypassed, startBypassedText, "", true
	}
	return "", "", "", false
}

// startMark is the mark of kind on s (loaded from the store on first use,
// then kept); the caller holds mu.
func (m *Monitor) startMark(id int64, s *serverState, kind string) (string, bool) {
	if s.marks == nil {
		marks, err := m.st.StartMarks(id)
		if err != nil {
			m.log.Warn("start marks: read", "server_id", id, "err", err)
			return "", false
		}
		s.marks = marks
	}
	v, ok := s.marks[kind]
	return v, ok
}

// accounted: the last start of kind the panel accounted for is startID.
// The caller holds mu.
func (m *Monitor) accounted(id int64, s *serverState, kind, startID string) bool {
	v, ok := m.startMark(id, s, kind)
	return ok && v == startID
}

// markStart sets the mark of kind on serverID to startID, in the store and
// in memory: the start is accounted for. It is called only once the start
// needs no alert any more (over, checked) or its alert is open, never
// before. The caller holds mu.
func (m *Monitor) markStart(serverID int64, kind, startID string) {
	s := m.state(serverID)
	if cur, ok := m.startMark(serverID, s, kind); ok && cur == startID {
		return
	}
	if err := m.st.SetStartMark(serverID, kind, startID); err != nil {
		m.log.Warn("start marks: write", "server_id", serverID, "kind", kind, "err", err)
		return
	}
	if s.marks != nil {
		s.marks[kind] = startID // else the read failed: the next use reads the store again
	}
}

// startResultFromSnapshot reconciles the start alerts of id to the start
// state a report gives (StartResult.Kinds: the last start of each bad
// kind, from an agent that reports it). It acts only when the server
// resolves as quarantined. For each entry, ignoring unknown kinds, entries
// after the first of a kind and entries without a usable id, in order:
//  1. over (not late): nothing when this over entry was dealt with
//     already (no store query); else the alert of the kind closes and,
//     once none is open, mark = id;
//  2. id is the mark of the kind: nothing (no store query);
//  3. failed in a report that shows MySQL running: nothing, no mark (the
//     agent has not judged the new start yet);
//  4. decided before the promotion's start minus startResultSlack (an
//     earlier quarantine): nothing, no mark;
//  5. an alert of the kind is open: it now stands for this start (a
//     blocked one takes this entry's lock file); mark = id. A late alert
//     that "Periksa data tertinggal" already answered (the check is later
//     than its opening, as closeStartAlerts judges) is closed first and
//     the entry goes on to 6 and 7: it is a newer start;
//  6. late, and a "Periksa data tertinggal" result is later than decided:
//     mark = id, nothing opens;
//  7. the alert opens; only if it did, mark = id.
//
// At most maxStartKinds entries are looked at. A failed mark write may
// show a start twice; a start is never accounted without its alert. The
// caller holds mu.
func (m *Monitor) startResultFromSnapshot(id int64, snap *proto.HealthSnapshot, at time.Time) []alertUpdate {
	sr := snap.StartResult
	if sr == nil || len(sr.Kinds) == 0 {
		return nil
	}
	s, ok := m.servers[id]
	if !ok || !s.resolvedOK || s.role != store.RoleQuarantined {
		return nil
	}
	var ups []alertUpdate
	seen := map[string]bool{}
	for _, e := range sr.Kinds[:min(len(sr.Kinds), maxStartKinds)] {
		kind, msg, overReason, ok := startKindAlert(e)
		if !ok || seen[kind] {
			continue
		}
		seen[kind] = true
		startID := cleanStartID(e.StartID)
		if startID == "" {
			continue
		}
		ups = m.reconcileStart(id, s, snap, at, e, kind, msg, overReason, startID, ups)
	}
	return ups
}

// reconcileStart is one entry of startResultFromSnapshot. The caller holds
// mu.
func (m *Monitor) reconcileStart(id int64, s *serverState, snap *proto.HealthSnapshot, at time.Time,
	e proto.StartKindState, kind, msg, overReason, startID string, ups []alertUpdate) []alertUpdate {
	if e.Over && kind != KindMasterStartLate {
		if s.overDone[kind] == startID {
			return ups
		}
		ups, closed := m.closeAlertOKLocked(id, kind, overReason, ups)
		if closed {
			m.markStart(id, kind, startID)
			if s.overDone == nil {
				s.overDone = map[string]string{}
			}
			s.overDone[kind] = startID
		}
		return ups
	}
	if m.accounted(id, s, kind, startID) {
		return ups
	}
	if kind == KindMasterStartBlocked && snap.MySQLOK {
		return ups
	}
	decided := startDecidedAt(at, e.AgeSec)
	p, err := m.st.PromotionByOldMaster(id)
	switch {
	case err == nil:
		if decided.Before(p.StartedAt.Add(-startResultSlack)) {
			return ups
		}
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("start state: promotion", "server_id", id, "err", err)
	}
	a, err := m.st.GetOpenAlert(kind, id)
	switch {
	case err == nil && kind == KindMasterStartLate && m.leftoverCheckedAfter(a):
		// The open alert is about a start the check already answered;
		// this entry is a newer one.
		if err := m.st.CloseAlertWithReason(a.ID, m.now(), leftoverCheckedReason); err != nil {
			m.log.Warn("close alert", "server_id", id, "kind", kind, "err", err)
			return ups
		}
		ups = append(ups, alertUpdate{a.ID, kind, id, false})
	case err == nil:
		if kind == KindMasterStartBlocked && a.Message != msg {
			if err := m.st.SetAlertMessage(a.ID, msg); err != nil {
				m.log.Warn("update alert message", "server_id", id, "kind", kind, "err", err)
			}
		}
		m.markStart(id, kind, startID)
		return ups
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("read open alert", "server_id", id, "kind", kind, "err", err)
		return ups
	}
	if kind == KindMasterStartLate && m.leftoverCheckedAfter(store.Alert{ServerID: id, OpenedAt: decided}) {
		m.markStart(id, kind, startID)
		return ups
	}
	ups, open := m.raiseOpenLocked(id, kind, msg, ups)
	if open {
		m.markStart(id, kind, startID)
	}
	return ups
}

// closeStartAlertsLeftQuarantine acts once per end of quarantine seen on
// the agent's side (a finished rebuild, a local set-role): when a taken
// report of id has a readable role pin that is not quarantined and the
// last readable pin was quarantined (or is not known: a panel restart),
// it closes the blocked and bypassed alerts of the starts the panel
// accounted for from the start state (it has a mark of the kind), with
// the reasons the start events have always used, and deletes those marks
// once no such alert is open: a new quarantine starts clean. Their end may
// never reach a snapshot: the agent reports no start state outside
// quarantine. Late is left to closeStartAlerts. The caller holds mu.
func (m *Monitor) closeStartAlertsLeftQuarantine(id int64, snap *proto.HealthSnapshot) []alertUpdate {
	pin := snap.RolePin
	s, ok := m.servers[id]
	if pin == "" || !ok {
		return nil
	}
	prev := s.startPin
	s.startPin = pin
	if pin == pinQuarantined || (prev != "" && prev != pinQuarantined) {
		return nil
	}
	var ups []alertUpdate
	for _, k := range []struct{ kind, reason string }{
		{KindMasterStartBlocked, startBlockedOverReason},
		{KindMasterStartBypassed, ""},
	} {
		if _, marked := m.startMark(id, s, k.kind); !marked {
			continue
		}
		var closed bool
		if ups, closed = m.closeAlertOKLocked(id, k.kind, k.reason, ups); !closed {
			continue
		}
		if err := m.st.DeleteStartMark(id, k.kind); err != nil {
			m.log.Warn("start marks: delete", "server_id", id, "kind", k.kind, "err", err)
			continue
		}
		delete(s.marks, k.kind)
		delete(s.overDone, k.kind)
	}
	return ups
}

// startUnreadableText words master_start_late opened for a start report
// the panel could not read (SP-9).
const startUnreadableText = "laporan menyala master lama dari agent tidak terbaca; anggap master lama sempat bisa ditulis dan " +
	"jalankan Periksa data tertinggal"

// raiseIfNone opens kind on serverID with msg unless one is open already
// (which keeps its message).
func (m *Monitor) raiseIfNone(serverID int64, kind, msg string) {
	_, err := m.st.GetOpenAlert(kind, serverID)
	switch {
	case err == nil:
		return
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
	}
	m.raise(serverID, kind, msg)
}

// closeStartAlerts closes the event alerts of the start lock (late,
// bypassed, blocked) that are over: the server is no longer quarantined
// (for late: not while it is a replica whose rebuild has not copied yet),
// or, for late, "Periksa data tertinggal" stored a result after the alert
// opened. The caller holds mu.
func (m *Monitor) closeStartAlerts(now time.Time) []alertUpdate {
	open, err := m.st.ListOpenAlerts()
	if err != nil {
		m.log.Warn("close start alerts: list open alerts", "err", err)
		return nil
	}
	var ups []alertUpdate
	for _, a := range open {
		if a.Kind != KindMasterStartLate && a.Kind != KindMasterStartBypassed && a.Kind != KindMasterStartBlocked {
			continue
		}
		s, ok := m.servers[a.ServerID]
		if !ok || !s.resolvedOK {
			continue // the role is not known: nothing is closed on a guess
		}
		reason := ""
		switch {
		case a.Kind == KindMasterStartLate && s.role == store.RoleReplica && !judgedState(s.repState):
			// A rebuild that has not copied yet (creating, failed): the
			// writes the alert speaks of are still on the server, and a
			// cancel makes it an old master again (SP-2).
		case s.role != store.RoleQuarantined:
			reason = notQuarantinedReason
		case a.Kind == KindMasterStartLate && m.leftoverCheckedAfter(a):
			reason = leftoverCheckedReason
		}
		if reason == "" {
			continue
		}
		if err := m.st.CloseAlertWithReason(a.ID, now, reason); err != nil {
			m.log.Warn("close alert", "server_id", a.ServerID, "kind", a.Kind, "err", err)
			continue
		}
		ups = append(ups, alertUpdate{a.ID, a.Kind, a.ServerID, false})
	}
	return ups
}

// leftoverCheckedAfter: the newest promotion that replaced the server
// holds a "Periksa data tertinggal" result taken after a opened. The
// orchestrator keeps it in promotions.leftover_json, which this package
// cannot read through the orchestrator (it imports this one), so it reads
// the time itself; a result it cannot read says nothing.
func (m *Monitor) leftoverCheckedAfter(a store.Alert) bool {
	p, err := m.st.PromotionByOldMaster(a.ServerID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("start alerts: promotion", "server_id", a.ServerID, "err", err)
		}
		return false
	}
	var check struct {
		CheckedAt time.Time `json:"checked_at"`
	}
	if p.LeftoverJSON == "" || json.Unmarshal([]byte(p.LeftoverJSON), &check) != nil {
		return false
	}
	return check.CheckedAt.After(a.OpenedAt)
}
