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
