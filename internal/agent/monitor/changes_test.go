package monitor

import (
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func i64(v int64) *int64 { return &v }

// base is a healthy replica reading: 50% disk, 10% connections, lag 5 s.
func base() proto.HealthSnapshot {
	return proto.HealthSnapshot{
		MySQLOK: true, Version: "8.4.3", UptimeSec: 1000,
		ThreadsConnected: 10, MaxConnections: 100,
		ReadOnly: true, SuperReadOnly: true, BinlogDumpThreads: 0,
		Replica: &proto.ReplicaStatus{
			IORunning: "Yes", SQLRunning: "Yes", SourceHost: "10.0.0.1", SourcePort: 3306,
			SecondsBehind: i64(5),
		},
		DiskTotal: 100, DiskFree: 50, AgentVersion: "1.0.0",
	}
}

func TestChanged(t *testing.T) {
	cases := []struct {
		name string
		prev func(*proto.HealthSnapshot)
		cur  func(*proto.HealthSnapshot)
		want bool
	}{
		{name: "identical", want: false},
		{name: "noise only", cur: func(s *proto.HealthSnapshot) {
			s.UptimeSec += 15
			s.ThreadsRunning = 7
			s.GTIDExecuted = "x:1-99"
			s.BinlogTotalBytes = 12345
			s.MemAvailable = 99
			s.Load1 = 3.5
			s.Replica.ExecutedGTID = "x:1-99"
		}, want: false},
		{name: "mysql down", cur: func(s *proto.HealthSnapshot) { *s = proto.HealthSnapshot{MySQLError: "tidak menjawab"} }, want: true},
		{name: "mysql error appears", prev: func(s *proto.HealthSnapshot) { s.MySQLOK = false }, cur: func(s *proto.HealthSnapshot) {
			s.MySQLOK = false
			s.MySQLError = "belum terhubung"
		}, want: true},
		{name: "read_only", cur: func(s *proto.HealthSnapshot) { s.ReadOnly = false }, want: true},
		{name: "super_read_only", cur: func(s *proto.HealthSnapshot) { s.SuperReadOnly = false }, want: true},
		{name: "replica gone", cur: func(s *proto.HealthSnapshot) { s.Replica = nil }, want: true},
		{name: "io state", cur: func(s *proto.HealthSnapshot) { s.Replica.IORunning = "Connecting" }, want: true},
		{name: "sql state", cur: func(s *proto.HealthSnapshot) { s.Replica.SQLRunning = "No" }, want: true},
		{name: "io errno", cur: func(s *proto.HealthSnapshot) { s.Replica.LastIOErrno = 2003 }, want: true},
		{name: "sql errno", cur: func(s *proto.HealthSnapshot) { s.Replica.LastSQLErrno = 1062 }, want: true},
		{name: "io error text only", cur: func(s *proto.HealthSnapshot) { s.Replica.LastIOError = "other text" }, want: false},
		{name: "source host", cur: func(s *proto.HealthSnapshot) { s.Replica.SourceHost = "10.0.0.2" }, want: true},
		{name: "source port", cur: func(s *proto.HealthSnapshot) { s.Replica.SourcePort = 3307 }, want: true},
		{name: "lag 59 to 61", prev: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(59) },
			cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(61) }, want: true},
		{name: "lag 61 to 70", prev: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(61) },
			cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(70) }, want: false},
		{name: "lag 59 to 60", prev: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(59) },
			cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(60) }, want: true},
		{name: "lag 70 to 3", prev: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(70) },
			cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = i64(3) }, want: true},
		{name: "lag unknown", cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = nil }, want: true},
		{name: "lag still unknown", prev: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = nil },
			cur: func(s *proto.HealthSnapshot) { s.Replica.SecondsBehind = nil }, want: false},
		{name: "dump threads", cur: func(s *proto.HealthSnapshot) { s.BinlogDumpThreads = 1 }, want: true},
		{name: "role pinned", prev: func(s *proto.HealthSnapshot) { s.RolePin = "unassigned" },
			cur: func(s *proto.HealthSnapshot) { s.RolePin = "master" }, want: true},
		{name: "source pinned", cur: func(s *proto.HealthSnapshot) { s.SourcePin = "10.0.0.1:3306" }, want: true},
		{name: "pin error appears", cur: func(s *proto.HealthSnapshot) { s.PinError = "role harus berkas biasa" }, want: true},
		{name: "pin error text only", prev: func(s *proto.HealthSnapshot) { s.PinError = "satu" },
			cur: func(s *proto.HealthSnapshot) { s.PinError = "dua" }, want: false},
		{name: "disk 84 to 86", prev: func(s *proto.HealthSnapshot) { s.DiskFree = 16 },
			cur: func(s *proto.HealthSnapshot) { s.DiskFree = 14 }, want: true},
		{name: "disk 86 to 94", prev: func(s *proto.HealthSnapshot) { s.DiskFree = 14 },
			cur: func(s *proto.HealthSnapshot) { s.DiskFree = 6 }, want: false},
		{name: "disk 94 to 95", prev: func(s *proto.HealthSnapshot) { s.DiskFree = 6 },
			cur: func(s *proto.HealthSnapshot) { s.DiskFree = 5 }, want: true},
		{name: "disk 96 to 80", prev: func(s *proto.HealthSnapshot) { s.DiskFree = 4 },
			cur: func(s *proto.HealthSnapshot) { s.DiskFree = 20 }, want: true},
		{name: "disk unknown", prev: func(s *proto.HealthSnapshot) { s.DiskTotal, s.DiskFree = 0, 0 },
			cur: func(s *proto.HealthSnapshot) { s.DiskTotal, s.DiskFree = 0, 0 }, want: false},
		{name: "connections 79 to 80", prev: func(s *proto.HealthSnapshot) { s.ThreadsConnected = 79 },
			cur: func(s *proto.HealthSnapshot) { s.ThreadsConnected = 80 }, want: true},
		{name: "connections 81 to 99", prev: func(s *proto.HealthSnapshot) { s.ThreadsConnected = 81 },
			cur: func(s *proto.HealthSnapshot) { s.ThreadsConnected = 99 }, want: false},
		{name: "restart", cur: func(s *proto.HealthSnapshot) { s.UptimeSec = 5 }, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev, cur := base(), base()
			if c.prev != nil {
				c.prev(&prev)
			}
			if c.cur != nil {
				c.cur(&cur)
			}
			if got := changed(prev, cur, time.Minute, 85, 95, 80); got != c.want {
				t.Fatalf("changed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestChangedFromEmptyPrev(t *testing.T) {
	if !changed(proto.HealthSnapshot{}, base(), time.Minute, 85, 95, 80) {
		t.Fatal("first reading after an empty prev must be a change")
	}
	if !changed(proto.HealthSnapshot{}, proto.HealthSnapshot{}, time.Minute, 85, 95, 80) {
		t.Fatal("an empty prev is always a change")
	}
}

// TestChangedOnStartLock: a change of the start lock's state (gone or
// back, Present, Locked, Reason) is reported at once, not with the next
// summary (FR-2).
func TestChangedOnStartLock(t *testing.T) {
	locked := proto.StartLockState{File: "/etc/mysql/dbpanel-start-lock.sql", Present: true, Locked: true}
	notLocked := locked
	notLocked.Locked, notLocked.Reason = false, "init_file lain dibaca sesudahnya"
	other := notLocked
	other.Reason = "berkas kunci tidak ada"
	notPresent := notLocked
	notPresent.Present = false
	for _, c := range []struct {
		name      string
		prev, cur *proto.StartLockState
		want      bool
	}{
		{"both nil", nil, nil, false},
		{"same", &locked, &locked, false},
		{"appears", nil, &locked, true},
		{"goes", &locked, nil, true},
		{"locked flips", &locked, &notLocked, true},
		{"unlocked back", &notLocked, &locked, true},
		{"reason changes", &notLocked, &other, true},
		{"present flips", &notLocked, &notPresent, true},
	} {
		prev, cur := base(), base()
		prev.StartLock, cur.StartLock = c.prev, c.cur
		if got := changed(prev, cur, 30*time.Second, 85, 95, 80); got != c.want {
			t.Errorf("%s: changed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestChangedOnStartResult: a change of the start state (worst result and
// its id, each kind's id and over flag, or the state appearing or going)
// is reported at once (F-1); the ages alone are not a change.
func TestChangedOnStartResult(t *testing.T) {
	late := proto.StartKindState{Kind: "late", StartID: "2026-11-14T01:00:00Z", AgeSec: 10}
	failed := proto.StartKindState{Kind: "failed", StartID: "2026-11-14T00:00:00Z", AgeSec: 3600, InitFile: "/x.sql"}
	st := proto.StartResultState{Result: "late", StartID: late.StartID, AgeSec: 10, Kinds: []proto.StartKindState{late, failed}}
	with := func(f func(*proto.StartResultState)) *proto.StartResultState {
		c := st
		c.Kinds = append([]proto.StartKindState(nil), st.Kinds...)
		f(&c)
		return &c
	}
	for _, c := range []struct {
		name      string
		prev, cur *proto.StartResultState
		want      bool
	}{
		{"both nil", nil, nil, false},
		{"same", &st, with(func(*proto.StartResultState) {}), false},
		{"ages advance", &st, with(func(s *proto.StartResultState) { s.AgeSec, s.Kinds[0].AgeSec, s.Kinds[1].AgeSec = 25, 25, 3615 }), false},
		{"appears", nil, &st, true},
		{"goes", &st, nil, true},
		{"worst result", &st, with(func(s *proto.StartResultState) { s.Result = "failed" }), true},
		{"worst id", &st, with(func(s *proto.StartResultState) { s.StartID = "2026-11-14T02:00:00Z" }), true},
		{"kind id", &st, with(func(s *proto.StartResultState) { s.Kinds[1].StartID = "2026-11-14T00:30:00Z" }), true},
		{"kind over", &st, with(func(s *proto.StartResultState) { s.Kinds[1].Over = true }), true},
		{"kind added", &st, with(func(s *proto.StartResultState) {
			s.Kinds = append(s.Kinds, proto.StartKindState{Kind: "bypassed", StartID: "2026-11-14T03:00:00Z"})
		}), true},
		{"kind gone", &st, with(func(s *proto.StartResultState) { s.Kinds = s.Kinds[:1] }), true},
	} {
		prev, cur := base(), base()
		prev.StartResult, cur.StartResult = c.prev, c.cur
		if got := changed(prev, cur, 30*time.Second, 85, 95, 80); got != c.want {
			t.Errorf("%s: changed = %v, want %v", c.name, got, c.want)
		}
	}
}
