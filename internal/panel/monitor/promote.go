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
	a, err := m.st.GetOpenAlert(kind, serverID)
	switch {
	case err == nil:
		if a.Message != msg {
			if err := m.st.SetAlertMessage(a.ID, msg); err != nil {
				m.log.Warn("update alert message", "server_id", serverID, "kind", kind, "err", err)
			}
		}
		return ups
	case !errors.Is(err, store.ErrNotFound):
		m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
		return ups
	}
	s := m.state(serverID)
	if !s.resolvedOK {
		if err := m.resolve(serverID, s, time.Time{}); err != nil {
			m.log.Warn("raise: resolve role", "server_id", serverID, "err", err)
		}
	}
	id, err := m.st.OpenAlert(store.Alert{Kind: kind, ServerID: serverID, TopologyID: s.topoID, OpenedAt: m.now(), Message: msg})
	if err != nil {
		m.log.Warn("open alert", "server_id", serverID, "kind", kind, "err", err)
		return ups
	}
	return append(ups, alertUpdate{id, kind, serverID, true})
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
	a, err := m.st.GetOpenAlert(kind, serverID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("read open alert", "server_id", serverID, "kind", kind, "err", err)
		}
		return ups
	}
	if err := m.st.CloseAlertWithReason(a.ID, m.now(), reason); err != nil {
		m.log.Warn("close alert", "server_id", serverID, "kind", kind, "err", err)
		return ups
	}
	return append(ups, alertUpdate{a.ID, kind, serverID, false})
}

// handleStartEvent takes the start lock guard's events of a quarantined
// server (proto EventQuarantineStart, EventQuarantineStartFailed); false
// for any other event. An event that cannot be read opens the worst it
// could say and closes nothing; one whose result is not one of the three
// changes nothing: only a result the panel knows closes an alert about a
// server that may have been writable.
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
		m.raise(serverID, KindMasterStartBlocked, startBlockedMessage(fe.File))
		return true
	}
	var qe proto.QuarantineStartEvent
	if err := json.Unmarshal(ev.Payload, &qe); err != nil {
		m.log.Warn("monitor.quarantineStart unreadable", "server_id", serverID, "err", err)
		worst()
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
