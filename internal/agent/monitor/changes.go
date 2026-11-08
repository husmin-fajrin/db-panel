package monitor

import (
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Thresholds whose crossing is a change worth reporting at once.
const (
	diskWarnPct = 85 // percent of the data directory's file system in use
	diskCritPct = 95
	connWarnPct = 80 // threads_connected as a percent of max_connections
)

// changed reports whether cur differs from prev, the last reported
// snapshot, in a way the panel must hear about at once (spec 5.1 "Kapan
// melapor"): MySQL reachability, the read-only locks, the replication
// threads, their errors and source, the pins (spec 5.12: a master pinned
// at last closes master_unpinned at once), lag crossing lagThreshold (an unknown
// lag is a state of its own), the binlog dump threads, disk use crossing
// diskWarn or diskCrit percent, connections crossing connPct percent of
// max_connections, and a restart (uptime went down). An empty prev is
// always a change.
func changed(prev, cur proto.HealthSnapshot, lagThreshold time.Duration, diskWarn, diskCrit, connPct int) bool {
	if prev == (proto.HealthSnapshot{}) {
		return true
	}
	switch {
	case prev.MySQLOK != cur.MySQLOK,
		(prev.MySQLError == "") != (cur.MySQLError == ""),
		prev.ReadOnly != cur.ReadOnly,
		prev.SuperReadOnly != cur.SuperReadOnly,
		prev.BinlogDumpThreads != cur.BinlogDumpThreads,
		prev.RolePin != cur.RolePin, prev.SourcePin != cur.SourcePin,
		(prev.PinError == "") != (cur.PinError == ""),
		diskLevel(prev, diskWarn, diskCrit) != diskLevel(cur, diskWarn, diskCrit),
		connHigh(prev, connPct) != connHigh(cur, connPct),
		prev.MySQLOK && cur.MySQLOK && cur.UptimeSec < prev.UptimeSec:
		return true
	}
	return replicaChanged(prev.Replica, cur.Replica, lagThreshold)
}

// replicaChanged compares the replication fields that are reported at once.
func replicaChanged(prev, cur *proto.ReplicaStatus, lagThreshold time.Duration) bool {
	if (prev == nil) != (cur == nil) {
		return true
	}
	if prev == nil {
		return false
	}
	return prev.IORunning != cur.IORunning ||
		prev.SQLRunning != cur.SQLRunning ||
		prev.LastIOErrno != cur.LastIOErrno ||
		prev.LastSQLErrno != cur.LastSQLErrno ||
		prev.SourceHost != cur.SourceHost ||
		prev.SourcePort != cur.SourcePort ||
		lagState(prev, lagThreshold) != lagState(cur, lagThreshold)
}

// Lag states.
const (
	lagUnknown = iota
	lagBelow
	lagAtOrAbove
)

func lagState(r *proto.ReplicaStatus, threshold time.Duration) int {
	switch {
	case r.SecondsBehind == nil:
		return lagUnknown
	case time.Duration(*r.SecondsBehind)*time.Second >= threshold:
		return lagAtOrAbove
	default:
		return lagBelow
	}
}

// diskLevel is 0 below warn percent used (or when the size is unknown), 1
// from warn and 2 from crit.
func diskLevel(s proto.HealthSnapshot, warn, crit int) int {
	if s.DiskTotal == 0 {
		return 0
	}
	used := s.DiskTotal - min(s.DiskFree, s.DiskTotal)
	switch {
	case used*100 >= uint64(crit)*s.DiskTotal:
		return 2
	case used*100 >= uint64(warn)*s.DiskTotal:
		return 1
	default:
		return 0
	}
}

// connHigh reports whether threads_connected is at least pct percent of
// max_connections.
func connHigh(s proto.HealthSnapshot, pct int) bool {
	return s.MaxConnections > 0 && s.ThreadsConnected*100 >= pct*s.MaxConnections
}
