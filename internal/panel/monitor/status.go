// Package monitor derives the status shown for each server from its latest
// health report. This file holds only pure functions; the stateful part
// (last reports, change detection) lives next to it.
package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Status codes. They are also the text the UI shows.
const (
	StatusNotReady     = "Belum siap"
	StatusAgentOffline = "Agent offline"
	StatusNeedsUpgrade = "Agent perlu dimutakhirkan"
	StatusUnreachable  = "Tidak terjangkau"
	StatusStopped      = "Berhenti"
	StatusPaused       = "Dihentikan"
	// StatusStoppedOutside: stopped without the panel's doing.
	StatusStoppedOutside = "Berhenti (di luar panel)"
	StatusDisconnected   = "Terputus"
	StatusLagging        = "Ketinggalan"
	StatusSynced         = "Sinkron"
	StatusDown           = "Diduga tumbang"
	StatusLocked         = "Terkunci"
	StatusHealthy        = "Sehat"
)

type Severity int

const (
	SevOK Severity = iota
	SevWarn
	SevCrit
	SevNeutral // grey: nothing to judge yet
)

// SeverityOf is the usual severity of a status code. Terputus is SevWarn
// here; DeriveReplica raises it to SevCrit after Thresholds.DisconnectAlert.
func SeverityOf(code string) Severity {
	switch code {
	case StatusAgentOffline, StatusUnreachable, StatusStopped, StatusStoppedOutside, StatusDown, StatusLocked:
		return SevCrit
	case StatusPaused, StatusDisconnected, StatusLagging:
		return SevWarn
	case StatusSynced, StatusHealthy:
		return SevOK
	}
	return SevNeutral
}

type Status struct {
	Code     string
	Severity Severity
	Detail   string
	// LockLost: a replica with super_read_only off. Shown next to any status.
	LockLost bool
	LagSec   *int64
	// GTIDGap: transactions of the master that the replica has not received.
	GTIDGap int
	// GTIDGapKnown: GTIDGap was computed (a judged replica whose MySQL
	// answered, against a known master GTID set); a zero GTIDGap means
	// nothing missing only then.
	GTIDGapKnown bool
}

type Thresholds struct {
	Lag             time.Duration // seconds_behind (or a GTID gap) from which a replica lags
	DisconnectAlert time.Duration // Terputus turns critical after this
	DownAfter       time.Duration // a silent master is "Diduga tumbang" after this
	StaleAfter      time.Duration // a report older than this counts as agent offline
	DiskWarn        int           // percent used
	DiskCrit        int
	ConnPct         int
}

// Replica lifecycle states of store.Replica.State that the monitor judges.
// Kept as literals so this package does not depend on the store.
const (
	replicaReady     = "ready"
	replicaAttention = "attention"
)

// downAgentOffline starts the detail of a master presumed down because its
// agent is offline (the alternative is MySQL not answering).
const downAgentOffline = "Diduga tumbang (agent offline)"

// offlineGrace is how long the hub may report an agent offline before the
// status becomes Agent offline.
const offlineGrace = 30 * time.Second

type ReplicaInput struct {
	State        string // store.Replica.State
	Paused       bool   // stopped through the panel
	AgentOnline  bool
	OfflineSince time.Time
	ReceivedAt   time.Time // when the last report arrived; zero when none yet
	NeedsUpgrade bool
	Snap         *proto.HealthSnapshot
	// MasterGTIDExecuted is the master's gtid_executed in its last report.
	MasterGTIDExecuted string
	// GapSince is when the GTID gap became non-zero; zero when unknown.
	GapSince time.Time
	// DisconnectedSince is when the IO thread stopped being connected; zero
	// when unknown (then Terputus never turns critical).
	DisconnectedSince time.Time
	Now               time.Time
}

type MasterInput struct {
	AgentOnline  bool
	OfflineSince time.Time
	ReceivedAt   time.Time
	NeedsUpgrade bool
	Snap         *proto.HealthSnapshot
	// UnreachableSince is when MySQL stopped answering; zero when unknown
	// (then the master is not yet presumed down).
	UnreachableSince     time.Time
	ReplicasDisconnected int // replicas of the topology whose IO thread is not Yes
	Now                  time.Time
}

func DeriveReplica(in ReplicaInput, th Thresholds) Status {
	var st Status
	snap := in.Snap
	usable := snap != nil && snap.MySQLOK
	set := func(code, detail string) Status {
		st.Code, st.Detail, st.Severity = code, detail, SeverityOf(code)
		return st
	}

	if in.State != replicaReady && in.State != replicaAttention {
		return set(StatusNotReady, "Replika belum siap dipantau")
	}
	if detail, off := agentOffline(in.AgentOnline, in.OfflineSince, in.ReceivedAt, in.Now, th.StaleAfter); off {
		return set(StatusAgentOffline, detail)
	}
	if in.NeedsUpgrade {
		return set(StatusNeedsUpgrade, "Versi agent perlu dimutakhirkan untuk pemantauan")
	}
	if !usable {
		detail := "MySQL tidak menjawab"
		if snap != nil && snap.MySQLError != "" {
			detail = snap.MySQLError
		}
		return set(StatusUnreachable, detail)
	}

	// Only a replica that is judged (ready/attention, agent up and current,
	// MySQL answering) carries these; a replica being created or resynced
	// has no lock to lose yet.
	st.LockLost = !snap.SuperReadOnly
	rs := snap.Replica
	if rs == nil {
		// MySQL answered, but no replication source is configured.
		return set(StatusStoppedOutside, "konfigurasi replikasi tidak ditemukan di MySQL")
	}
	st.LagSec = rs.SecondsBehind
	// A transaction the replica applied was received once, so the executed
	// set counts as received too.
	st.GTIDGap = GTIDGap(in.MasterGTIDExecuted, joinGTIDSets(rs.RetrievedGTID, rs.ExecutedGTID))
	st.GTIDGapKnown = in.MasterGTIDExecuted != ""
	hasErrno := rs.LastSQLErrno != 0 || rs.LastIOErrno != 0
	if rs.SQLRunning == "No" || (rs.IORunning == "No" && rs.LastIOErrno != 0) {
		switch {
		case hasErrno:
			return set(StatusStopped, fmt.Sprintf("Replikasi berhenti karena error %d", firstNonZero(rs.LastSQLErrno, rs.LastIOErrno)))
		case in.Paused:
			return set(StatusPaused, "Replikasi dihentikan lewat panel")
		default:
			return set(StatusStoppedOutside, "Replikasi berhenti tanpa lewat panel")
		}
	}
	if rs.IORunning == "No" && in.Paused {
		// IO thread stopped through the panel, no error (spec 5.2: Dihentikan).
		return set(StatusPaused, "Replikasi dihentikan lewat panel")
	}
	if rs.IORunning == "Connecting" || rs.IORunning == "No" {
		detail := "Terputus"
		if !in.DisconnectedSince.IsZero() {
			detail += " " + formatDuration(in.Now.Sub(in.DisconnectedSince))
		}
		detail += "; MySQL akan menyambung sendiri"
		set(StatusDisconnected, detail)
		if !in.DisconnectedSince.IsZero() && in.Now.Sub(in.DisconnectedSince) >= th.DisconnectAlert {
			st.Severity = SevCrit
		}
		return st
	}

	switch {
	case rs.SecondsBehind != nil && time.Duration(*rs.SecondsBehind)*time.Second >= th.Lag:
		return set(StatusLagging, fmt.Sprintf("Tertinggal %d detik", *rs.SecondsBehind))
	case rs.SecondsBehind == nil && rs.SQLRunning == "Yes" && rs.IORunning == "Yes":
		return set(StatusLagging, "Tertinggal; jeda belum bisa diukur")
	case st.GTIDGap > 0 && !in.GapSince.IsZero() && in.Now.Sub(in.GapSince) >= th.Lag:
		return set(StatusLagging, fmt.Sprintf("Tertinggal %d transaksi selama %s", st.GTIDGap, formatDuration(in.Now.Sub(in.GapSince))))
	}
	return set(StatusSynced, "Replikasi berjalan dan mengikuti master")
}

func DeriveMaster(in MasterInput, th Thresholds) Status {
	var st Status
	set := func(code, detail string) Status {
		st.Code, st.Detail, st.Severity = code, detail, SeverityOf(code)
		return st
	}
	lost := func(n int) string {
		if n <= 0 {
			return ""
		}
		return fmt.Sprintf(" dan %d replika kehilangan koneksi", n)
	}

	if detail, off := agentOffline(in.AgentOnline, in.OfflineSince, in.ReceivedAt, in.Now, th.StaleAfter); off {
		since := in.OfflineSince
		if in.AgentOnline { // silent rather than disconnected
			since = in.ReceivedAt
		}
		if !since.IsZero() && in.Now.Sub(since) >= th.DownAfter {
			return set(StatusDown, fmt.Sprintf("%s: agent tidak terhubung %s%s", downAgentOffline,
				formatDuration(in.Now.Sub(since)), lost(in.ReplicasDisconnected)))
		}
		return set(StatusAgentOffline, detail)
	}
	if in.NeedsUpgrade {
		return set(StatusNeedsUpgrade, "Versi agent perlu dimutakhirkan untuk pemantauan")
	}
	if in.Snap == nil || !in.Snap.MySQLOK {
		if !in.UnreachableSince.IsZero() && in.Now.Sub(in.UnreachableSince) >= th.DownAfter {
			return set(StatusDown, fmt.Sprintf("Diduga tumbang (MySQL tidak menjawab): MySQL tidak menjawab %s%s",
				formatDuration(in.Now.Sub(in.UnreachableSince)), lost(in.ReplicasDisconnected)))
		}
		detail := "MySQL tidak menjawab"
		if in.Snap != nil && in.Snap.MySQLError != "" {
			detail = in.Snap.MySQLError
		}
		return set(StatusUnreachable, detail)
	}
	if in.Snap.SuperReadOnly {
		return set(StatusLocked, "Master dikunci (super_read_only aktif); aplikasi tidak bisa menulis")
	}
	return set(StatusHealthy, "Master menjawab dan bisa ditulisi")
}

// agentOffline reports whether the agent counts as offline: the hub has
// seen it gone for over offlineGrace, or its last report is older than
// staleAfter (a zero ReceivedAt means no report yet, not a stale one).
func agentOffline(online bool, offlineSince, receivedAt, now time.Time, staleAfter time.Duration) (detail string, off bool) {
	if !online && (offlineSince.IsZero() || now.Sub(offlineSince) >= offlineGrace) {
		if offlineSince.IsZero() {
			return "Agent tidak terhubung", true
		}
		return "Agent tidak terhubung " + formatDuration(now.Sub(offlineSince)), true
	}
	if staleAfter > 0 && !receivedAt.IsZero() && now.Sub(receivedAt) > staleAfter {
		return "Laporan terakhir dari agent " + formatDuration(now.Sub(receivedAt)) + " lalu", true
	}
	return "", false
}

// formatDuration words a duration for people: minutes from 60 s, else seconds.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Minute {
		return fmt.Sprintf("%d menit", int(d/time.Minute))
	}
	return fmt.Sprintf("%d detik", int(d/time.Second))
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// joinGTIDSets concatenates GTID sets; parsing merges equal uuids.
func joinGTIDSets(a, b string) string {
	switch {
	case strings.TrimSpace(a) == "":
		return b
	case strings.TrimSpace(b) == "":
		return a
	}
	return a + "," + b
}
