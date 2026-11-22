package monitor

import (
	"errors"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The start state of a quarantined old master: its start alerts follow
// the start state each report carries (StartResult), with durable marks
// of the starts accounted for (start_marks).

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
// text the alert opens with (for late the text of a start; reconcileStart
// picks the late message by lateMessage) and the reason its end closes it
// with (the reasons the start events have always used); ok is false for
// anything unknown.
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
//     blocked one takes this entry's lock file, a late one T1b when this
//     entry is unverified); mark = id. A late alert
//     that "Periksa data tertinggal" already answered (the check is later
//     than its opening, as closeStartAlerts judges) is closed first and
//     the entry goes on to 6 and 7: it is a newer start;
//  6. late, and a "Periksa data tertinggal" result is later than decided:
//     mark = id, nothing opens;
//  7. the alert opens; only if it did, mark = id.
//
// A late alert's message follows lateMessage: the entry's remark, the
// copy state and whether a topology still names the server.
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
	if kind == KindMasterStartLate {
		msg = lateMessage(e.While, m.lateOpenFacts(id, s))
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
		// A blocked alert takes this entry's lock file; a late alert takes
		// this newer entry's text (lateMessage): T1b when it is unverified,
		// and back from T1b once the guard can verify again (spec 5.2).
		rewrite := kind == KindMasterStartBlocked || kind == KindMasterStartLate
		if rewrite && a.Message != msg {
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
	if !m.allowStartOpen(id, s, kind) {
		return ups // not marked: it opens once the limit allows, if still so
	}
	ups, open := m.raiseOpenLocked(id, kind, msg, ups)
	if open {
		m.markStart(id, kind, startID)
	}
	return ups
}

// The start alerts the start state opens are limited per server and kind
// (a hostile agent alternating states or ids would otherwise open and
// close one per report): a bucket of startOpenBurst opens, refilled with
// one every startOpenRefill. The first alert of a kind for a server always
// finds a full bucket.
const (
	startOpenBurst  = 5
	startOpenRefill = 10 * time.Minute
)

// startOpenBucket is one server's limit on opening one kind of start
// alert.
type startOpenBucket struct {
	tokens   float64
	last     time.Time // when tokens was last refilled
	warnedAt time.Time // when the limit was last logged (zero: not since it last allowed)
}

// allowStartOpen takes a token from the bucket of kind on s; without one
// it logs at most once per startOpenRefill and reports false. The caller
// holds mu.
func (m *Monitor) allowStartOpen(id int64, s *serverState, kind string) bool {
	now := m.now()
	if s.startOpens == nil {
		s.startOpens = map[string]*startOpenBucket{}
	}
	b, ok := s.startOpens[kind]
	if !ok {
		b = &startOpenBucket{tokens: startOpenBurst, last: now}
		s.startOpens[kind] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(startOpenBurst, b.tokens+float64(elapsed)/float64(startOpenRefill))
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		b.warnedAt = time.Time{}
		return true
	}
	if b.warnedAt.IsZero() || now.Sub(b.warnedAt) >= startOpenRefill {
		b.warnedAt = now
		m.log.Warn("start alert not opened: over the rate limit; it opens when the limit allows if the agent still reports it",
			"server_id", id, "kind", kind)
	}
	return false
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
// quarantine. Late is left to closeStartAlerts. The new pin counts as
// handled only once every marked kind was dealt with (its alert closed or
// none open, its mark deleted); when reading the marks, the alert or a
// store write fails, the end stays pending and the next taken report tries
// again. The caller holds mu.
func (m *Monitor) closeStartAlertsLeftQuarantine(id int64, snap *proto.HealthSnapshot) []alertUpdate {
	pin := snap.RolePin
	s, ok := m.servers[id]
	if pin == "" || !ok {
		return nil
	}
	prev := s.startPin
	if pin == pinQuarantined || (prev != "" && prev != pinQuarantined) {
		s.startPin = pin
		return nil
	}
	var ups []alertUpdate
	done := true
	for _, k := range []struct{ kind, reason string }{
		{KindMasterStartBlocked, startBlockedOverReason},
		{KindMasterStartBypassed, ""},
	} {
		_, marked := m.startMark(id, s, k.kind)
		if s.marks == nil {
			done = false // the marks did not read: nothing is known
			continue
		}
		if !marked {
			continue
		}
		var closed bool
		if ups, closed = m.closeAlertOKLocked(id, k.kind, k.reason, ups); !closed {
			done = false
			continue
		}
		if err := m.st.DeleteStartMark(id, k.kind); err != nil {
			m.log.Warn("start marks: delete", "server_id", id, "kind", k.kind, "err", err)
			done = false
			continue
		}
		delete(s.marks, k.kind)
		delete(s.overDone, k.kind)
	}
	if done {
		s.startPin = pin
	}
	return ups
}

// lateFacts are what the message and the close of master_start_late of a
// quarantined server depend on (spec 5.2).
type lateFacts struct {
	// leftoverChecked: a "Periksa data tertinggal" result is later than
	// the open alert (leftoverCheckedAfter).
	leftoverChecked bool
	// named: a topology still names the server as its old master; not
	// once the topology promoted again.
	named bool
	// copyStarted: a rebuild of the server began to change its data
	// (Deps.CopyStarted).
	copyStarted bool
	// unverified: the late entry of the server's latest report has the
	// remark proto.QuarantineWhileUnverified.
	unverified bool
}

// lateMessage is the message master_start_late opens with for a late
// entry with the remark while (proto.StartKindState.While): unverified
// always T1b; else, without a topology that names the server, T25; in the
// state CopyStarted T2b; a relock (running) T1; a start (no remark) the
// text of plan 5a. An open alert takes T1b too (reconcileStart).
func lateMessage(while string, f lateFacts) string {
	switch {
	case while == proto.QuarantineWhileUnverified:
		return startUnverifiedText
	case !f.named:
		return startLateNoTopologyText
	case f.copyStarted:
		return startLateCopiedText
	case while == proto.QuarantineWhileRunning:
		return startRelockedText
	}
	return startLateText
}

// lateCloseReason is why an open master_start_late of a quarantined server
// closes, "" while it stays open: a later "Periksa data tertinggal"; else,
// unless the latest late entry is unverified (the guard still cannot lock,
// so the alert stays), no topology names the server (T26) or a rebuild
// began its copy (T2). An alert opened in the state CopyStarted closes at
// the next round: one critical and one close notification per event, on
// purpose (R-27).
func lateCloseReason(f lateFacts) string {
	switch {
	case f.leftoverChecked:
		return leftoverCheckedReason
	case f.unverified:
		return ""
	case !f.named:
		return noTopologyCloseReason
	case f.copyStarted:
		return copyStartedCloseReason
	}
	return ""
}

// copyStartedOf asks Deps.CopyStarted about serverID; false when there is
// none or it fails (nothing is closed on a guess, logged). The caller
// holds mu.
func (m *Monitor) copyStartedOf(serverID int64) bool {
	if m.copyStarted == nil {
		return false
	}
	ok, err := m.copyStarted(serverID)
	if err != nil {
		m.log.Warn("start alerts: copy state", "server_id", serverID, "err", err)
		return false
	}
	return ok
}

// lateUnverified reports whether the late entry of snap (the agent keeps
// one, its latest) has the remark unverified.
func lateUnverified(snap *proto.HealthSnapshot) bool {
	if snap == nil || snap.StartResult == nil {
		return false
	}
	for _, e := range snap.StartResult.Kinds[:min(len(snap.StartResult.Kinds), maxStartKinds)] {
		if e.Kind == proto.QuarantineStartLate {
			return e.While == proto.QuarantineWhileUnverified
		}
	}
	return false
}

// lateOpenFacts are the facts of a late entry about to open (or rewrite)
// the alert of the quarantined server s; the caller holds mu.
func (m *Monitor) lateOpenFacts(id int64, s *serverState) lateFacts {
	return lateFacts{named: s.topoID != 0, copyStarted: m.copyStartedOf(id)}
}
