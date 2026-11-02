package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

var (
	now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	th  = Thresholds{
		Lag:             60 * time.Second,
		DisconnectAlert: 3 * time.Minute,
		DownAfter:       30 * time.Second,
		StaleAfter:      90 * time.Second,
	}
)

func i64(v int64) *int64 { return &v }

// healthyReplica is an input that derives to Sinkron; each case mutates it.
func healthyReplica() ReplicaInput {
	return ReplicaInput{
		State:       "ready",
		AgentOnline: true,
		ReceivedAt:  now.Add(-5 * time.Second),
		Now:         now,
		Snap: &proto.HealthSnapshot{
			MySQLOK:       true,
			SuperReadOnly: true,
			Replica: &proto.ReplicaStatus{
				IORunning: "Yes", SQLRunning: "Yes", SecondsBehind: i64(0),
			},
		},
	}
}

func TestDeriveReplicaPriority(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(in *ReplicaInput)
		wantCode string
		wantSev  Severity
	}{
		{"synced", func(in *ReplicaInput) {}, StatusSynced, SevOK},
		{"state creating", func(in *ReplicaInput) { in.State = "creating" }, StatusNotReady, SevNeutral},
		{"state resyncing", func(in *ReplicaInput) { in.State = "resyncing" }, StatusNotReady, SevNeutral},
		{"state failed", func(in *ReplicaInput) { in.State = "failed" }, StatusNotReady, SevNeutral},
		{"state changing", func(in *ReplicaInput) { in.State = "changing" }, StatusNotReady, SevNeutral},
		{"state attention is judged on health", func(in *ReplicaInput) { in.State = "attention" }, StatusSynced, SevOK},
		{"not ready beats offline", func(in *ReplicaInput) {
			in.State = "creating"
			in.AgentOnline = false
			in.OfflineSince = now.Add(-time.Hour)
		}, StatusNotReady, SevNeutral},
		{"offline over 30s", func(in *ReplicaInput) {
			in.AgentOnline = false
			in.OfflineSince = now.Add(-31 * time.Second)
		}, StatusAgentOffline, SevCrit},
		{"offline under 30s is not yet offline", func(in *ReplicaInput) {
			in.AgentOnline = false
			in.OfflineSince = now.Add(-10 * time.Second)
		}, StatusSynced, SevOK},
		{"offline beats needs upgrade", func(in *ReplicaInput) {
			in.AgentOnline = false
			in.OfflineSince = now.Add(-time.Minute)
			in.NeedsUpgrade = true
		}, StatusAgentOffline, SevCrit},
		{"needs upgrade", func(in *ReplicaInput) { in.NeedsUpgrade = true }, StatusNeedsUpgrade, SevNeutral},
		{"needs upgrade beats unreachable", func(in *ReplicaInput) {
			in.NeedsUpgrade = true
			in.Snap = &proto.HealthSnapshot{MySQLOK: false}
		}, StatusNeedsUpgrade, SevNeutral},
		{"no snapshot", func(in *ReplicaInput) { in.Snap = nil }, StatusUnreachable, SevCrit},
		{"mysql down", func(in *ReplicaInput) {
			in.Snap = &proto.HealthSnapshot{MySQLOK: false, MySQLError: "Error 1045: Access denied"}
		}, StatusUnreachable, SevCrit},
		{"sql stopped with errno", func(in *ReplicaInput) {
			in.Snap.Replica.SQLRunning = "No"
			in.Snap.Replica.LastSQLErrno = 1062
		}, StatusStopped, SevCrit},
		{"io stopped with errno", func(in *ReplicaInput) {
			in.Snap.Replica.IORunning = "No"
			in.Snap.Replica.LastIOErrno = 1045
		}, StatusStopped, SevCrit},
		{"paused through panel", func(in *ReplicaInput) {
			in.Paused = true
			in.Snap.Replica.SQLRunning = "No"
			in.Snap.Replica.IORunning = "No"
		}, StatusPaused, SevWarn},
		{"stopped with errno even when paused", func(in *ReplicaInput) {
			in.Paused = true
			in.Snap.Replica.SQLRunning = "No"
			in.Snap.Replica.LastSQLErrno = 1032
		}, StatusStopped, SevCrit},
		{"io connecting", func(in *ReplicaInput) { in.Snap.Replica.IORunning = "Connecting" }, StatusDisconnected, SevWarn},
		{"io no without errno while sql yes", func(in *ReplicaInput) { in.Snap.Replica.IORunning = "No" }, StatusDisconnected, SevWarn},
		{"stopped beats disconnected", func(in *ReplicaInput) {
			in.Snap.Replica.SQLRunning = "No"
			in.Snap.Replica.IORunning = "Connecting"
			in.Snap.Replica.LastSQLErrno = 1062
		}, StatusStopped, SevCrit},
		{"lag at threshold", func(in *ReplicaInput) { in.Snap.Replica.SecondsBehind = i64(60) }, StatusLagging, SevWarn},
		{"lag below threshold", func(in *ReplicaInput) { in.Snap.Replica.SecondsBehind = i64(59) }, StatusSynced, SevOK},
		{"lag unknown while both threads yes", func(in *ReplicaInput) { in.Snap.Replica.SecondsBehind = nil }, StatusLagging, SevWarn},
		{"disconnected beats lagging", func(in *ReplicaInput) {
			in.Snap.Replica.IORunning = "Connecting"
			in.Snap.Replica.SecondsBehind = i64(500)
		}, StatusDisconnected, SevWarn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthyReplica()
			c.mutate(&in)
			got := DeriveReplica(in, th)
			if got.Code != c.wantCode || got.Severity != c.wantSev {
				t.Errorf("got %q/%v (%s), want %q/%v", got.Code, got.Severity, got.Detail, c.wantCode, c.wantSev)
			}
			if got.Detail == "" {
				t.Errorf("Detail is empty for %q", got.Code)
			}
		})
	}
}

func TestStoppedOutsidePanel(t *testing.T) {
	in := healthyReplica()
	in.Snap.Replica.SQLRunning = "No"
	in.Snap.Replica.IORunning = "No"
	got := DeriveReplica(in, th)
	if got.Code != StatusStoppedOutside || got.Severity != SevCrit {
		t.Errorf("got %q/%v, want %q/crit", got.Code, got.Severity, StatusStoppedOutside)
	}
}

func TestUnreachableDetailIsMySQLError(t *testing.T) {
	in := healthyReplica()
	in.Snap = &proto.HealthSnapshot{MySQLOK: false, MySQLError: "Error 1227: Access denied; you need the SUPER privilege"}
	got := DeriveReplica(in, th)
	if got.Detail != in.Snap.MySQLError {
		t.Errorf("Detail = %q, want the MySQL error verbatim", got.Detail)
	}
}

func TestDisconnectedTurnsCritAfter3Min(t *testing.T) {
	cases := []struct {
		name       string
		since      time.Time
		wantSev    Severity
		wantDetail string
	}{
		{"just started", now.Add(-20 * time.Second), SevWarn, "Terputus 20 detik; MySQL akan menyambung sendiri"},
		{"2 minutes", now.Add(-2 * time.Minute), SevWarn, "Terputus 2 menit"},
		{"exactly 3 minutes", now.Add(-3 * time.Minute), SevCrit, "Terputus 3 menit"},
		{"4 minutes", now.Add(-4 * time.Minute), SevCrit, "Terputus 4 menit; MySQL akan menyambung sendiri"},
		{"since unknown", time.Time{}, SevWarn, "Terputus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := healthyReplica()
			in.Snap.Replica.IORunning = "Connecting"
			in.DisconnectedSince = c.since
			got := DeriveReplica(in, th)
			if got.Code != StatusDisconnected || got.Severity != c.wantSev {
				t.Errorf("got %q/%v, want %q/%v", got.Code, got.Severity, StatusDisconnected, c.wantSev)
			}
			if !strings.HasPrefix(got.Detail, c.wantDetail) {
				t.Errorf("Detail = %q, want prefix %q", got.Detail, c.wantDetail)
			}
		})
	}
}

func TestLaggingByGTIDGap(t *testing.T) {
	const master = "aaaa:1-110"
	mk := func(gapSince time.Time) ReplicaInput {
		in := healthyReplica()
		in.MasterGTIDExecuted = master
		in.Snap.Replica.RetrievedGTID = "aaaa:1-98" // 12 missing, seconds_behind stays 0
		in.GapSince = gapSince
		return in
	}
	t.Run("gap longer than threshold", func(t *testing.T) {
		got := DeriveReplica(mk(now.Add(-3*time.Minute)), th)
		if got.Code != StatusLagging || got.Severity != SevWarn {
			t.Fatalf("got %q/%v, want Ketinggalan", got.Code, got.Severity)
		}
		if got.GTIDGap != 12 {
			t.Errorf("GTIDGap = %d, want 12", got.GTIDGap)
		}
		if want := "Tertinggal 12 transaksi selama 3 menit"; got.Detail != want {
			t.Errorf("Detail = %q, want %q", got.Detail, want)
		}
	})
	t.Run("gap younger than threshold", func(t *testing.T) {
		got := DeriveReplica(mk(now.Add(-10*time.Second)), th)
		if got.Code != StatusSynced {
			t.Errorf("got %q, want Sinkron", got.Code)
		}
		if got.GTIDGap != 12 {
			t.Errorf("GTIDGap = %d, want 12 (reported even when not lagging)", got.GTIDGap)
		}
	})
	t.Run("executed GTIDs count as received", func(t *testing.T) {
		in := mk(now.Add(-3 * time.Minute))
		in.Snap.Replica.RetrievedGTID = "aaaa:90-98"
		in.Snap.Replica.ExecutedGTID = "aaaa:1-89"
		got := DeriveReplica(in, th)
		if got.GTIDGap != 12 {
			t.Errorf("GTIDGap = %d, want 12", got.GTIDGap)
		}
	})
	t.Run("no master reading means no gap", func(t *testing.T) {
		in := mk(now.Add(-3 * time.Minute))
		in.MasterGTIDExecuted = ""
		got := DeriveReplica(in, th)
		if got.Code != StatusSynced || got.GTIDGap != 0 {
			t.Errorf("got %q gap %d, want Sinkron/0", got.Code, got.GTIDGap)
		}
	})
}

func TestLagDetailAndLagSec(t *testing.T) {
	in := healthyReplica()
	in.Snap.Replica.SecondsBehind = i64(97)
	got := DeriveReplica(in, th)
	if got.Detail != "Tertinggal 97 detik" {
		t.Errorf("Detail = %q", got.Detail)
	}
	if got.LagSec == nil || *got.LagSec != 97 {
		t.Errorf("LagSec = %v, want 97", got.LagSec)
	}
}

func TestLockLost(t *testing.T) {
	in := healthyReplica()
	in.Snap.SuperReadOnly = false
	got := DeriveReplica(in, th)
	if !got.LockLost || got.Code != StatusSynced {
		t.Errorf("got lockLost=%v code=%q, want true/Sinkron", got.LockLost, got.Code)
	}
	// Independent of the status: also shown next to Berhenti.
	in.Snap.Replica.SQLRunning = "No"
	in.Snap.Replica.LastSQLErrno = 1062
	if got := DeriveReplica(in, th); !got.LockLost || got.Code != StatusStopped {
		t.Errorf("got lockLost=%v code=%q, want true/Berhenti", got.LockLost, got.Code)
	}
	// Not claimed when MySQL does not answer.
	in.Snap = &proto.HealthSnapshot{MySQLOK: false}
	if got := DeriveReplica(in, th); got.LockLost {
		t.Error("LockLost true although MySQL does not answer")
	}
	if got := DeriveReplica(healthyReplica(), th); got.LockLost {
		t.Error("LockLost true although super_read_only is on")
	}
}

func TestStaleReportIsAgentOffline(t *testing.T) {
	in := healthyReplica()
	in.ReceivedAt = now.Add(-2 * time.Minute) // agent still "online" but silent
	got := DeriveReplica(in, th)
	if got.Code != StatusAgentOffline || got.Severity != SevCrit {
		t.Errorf("got %q/%v, want Agent offline/crit", got.Code, got.Severity)
	}
	in.ReceivedAt = now.Add(-30 * time.Second)
	if got := DeriveReplica(in, th); got.Code != StatusSynced {
		t.Errorf("fresh report: got %q, want Sinkron", got.Code)
	}
}

func healthyMaster() MasterInput {
	return MasterInput{
		AgentOnline: true,
		ReceivedAt:  now.Add(-5 * time.Second),
		Now:         now,
		Snap:        &proto.HealthSnapshot{MySQLOK: true},
	}
}

func TestDeriveMasterDownAfterThreshold(t *testing.T) {
	t.Run("agent offline before DownAfter", func(t *testing.T) {
		in := healthyMaster()
		in.AgentOnline = false
		in.OfflineSince = now.Add(-31 * time.Second)
		th2 := th
		th2.DownAfter = time.Minute
		got := DeriveMaster(in, th2)
		if got.Code != StatusAgentOffline || got.Severity != SevCrit {
			t.Errorf("got %q/%v, want Agent offline/crit", got.Code, got.Severity)
		}
	})
	t.Run("agent offline past DownAfter", func(t *testing.T) {
		in := healthyMaster()
		in.AgentOnline = false
		in.OfflineSince = now.Add(-3 * time.Minute)
		in.ReplicasDisconnected = 2
		got := DeriveMaster(in, th)
		if got.Code != StatusDown || got.Severity != SevCrit {
			t.Fatalf("got %q/%v, want Diduga tumbang/crit", got.Code, got.Severity)
		}
		for _, w := range []string{"(agent offline)", "agent tidak terhubung 3 menit", "dan 2 replika kehilangan koneksi"} {
			if !strings.Contains(got.Detail, w) {
				t.Errorf("Detail %q lacks %q", got.Detail, w)
			}
		}
	})
	t.Run("agent offline past DownAfter without replicas", func(t *testing.T) {
		in := healthyMaster()
		in.AgentOnline = false
		in.OfflineSince = now.Add(-3 * time.Minute)
		got := DeriveMaster(in, th)
		if got.Code != StatusDown || strings.Contains(got.Detail, "replika") {
			t.Errorf("got %q %q, want Diduga tumbang without replica mention", got.Code, got.Detail)
		}
	})
	t.Run("mysql down before DownAfter", func(t *testing.T) {
		in := healthyMaster()
		in.Snap = &proto.HealthSnapshot{MySQLOK: false, MySQLError: "connection refused"}
		in.UnreachableSince = now.Add(-10 * time.Second)
		got := DeriveMaster(in, th)
		if got.Code != StatusUnreachable || got.Severity != SevCrit || got.Detail != "connection refused" {
			t.Errorf("got %q/%v %q", got.Code, got.Severity, got.Detail)
		}
	})
	t.Run("mysql down past DownAfter", func(t *testing.T) {
		in := healthyMaster()
		in.Snap = &proto.HealthSnapshot{MySQLOK: false, MySQLError: "connection refused"}
		in.UnreachableSince = now.Add(-2 * time.Minute)
		in.ReplicasDisconnected = 1
		got := DeriveMaster(in, th)
		if got.Code != StatusDown || got.Severity != SevCrit {
			t.Fatalf("got %q/%v", got.Code, got.Severity)
		}
		for _, w := range []string{"(MySQL tidak menjawab)", "1 replika kehilangan koneksi"} {
			if !strings.Contains(got.Detail, w) {
				t.Errorf("Detail %q lacks %q", got.Detail, w)
			}
		}
	})
	t.Run("stale report", func(t *testing.T) {
		in := healthyMaster()
		in.ReceivedAt = now.Add(-5 * time.Minute)
		got := DeriveMaster(in, th)
		if got.Code != StatusDown {
			t.Errorf("got %q, want Diduga tumbang (silent agent past DownAfter)", got.Code)
		}
	})
	t.Run("needs upgrade", func(t *testing.T) {
		in := healthyMaster()
		in.NeedsUpgrade = true
		got := DeriveMaster(in, th)
		if got.Code != StatusNeedsUpgrade || got.Severity != SevNeutral {
			t.Errorf("got %q/%v", got.Code, got.Severity)
		}
	})
	t.Run("healthy", func(t *testing.T) {
		got := DeriveMaster(healthyMaster(), th)
		if got.Code != StatusHealthy || got.Severity != SevOK {
			t.Errorf("got %q/%v", got.Code, got.Severity)
		}
	})
}

func TestMasterLocked(t *testing.T) {
	in := healthyMaster()
	in.Snap.SuperReadOnly = true
	got := DeriveMaster(in, th)
	if got.Code != StatusLocked || got.Severity != SevCrit {
		t.Errorf("got %q/%v, want Terkunci/crit", got.Code, got.Severity)
	}
}

func TestSeverityOf(t *testing.T) {
	cases := map[string]Severity{
		StatusNotReady: SevNeutral, StatusNeedsUpgrade: SevNeutral,
		StatusAgentOffline: SevCrit, StatusUnreachable: SevCrit, StatusStopped: SevCrit,
		StatusStoppedOutside: SevCrit, StatusDown: SevCrit,
		StatusPaused: SevWarn, StatusDisconnected: SevWarn, StatusLagging: SevWarn, StatusLocked: SevCrit,
		StatusSynced: SevOK, StatusHealthy: SevOK,
		"tidak dikenal": SevNeutral,
	}
	for code, want := range cases {
		if got := SeverityOf(code); got != want {
			t.Errorf("SeverityOf(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestGTIDGapParsing(t *testing.T) {
	cases := []struct {
		name            string
		master, replica string
		want            int
	}{
		{"identical", "aaaa:1-10", "aaaa:1-10", 0},
		{"replica behind", "aaaa:1-110", "aaaa:1-98", 12},
		{"replica ahead", "aaaa:1-10", "aaaa:1-20", 0},
		{"single numbers", "aaaa:5", "aaaa:4", 1},
		{"multiple intervals", "aaaa:1-5:7-9", "aaaa:1-5", 3},
		{"hole in replica", "aaaa:1-10", "aaaa:1-3:6-10", 2},
		{"multiple uuids", "aaaa:1-5,bbbb:1-3", "aaaa:1-5,bbbb:1", 2},
		{"uuid unknown to replica", "aaaa:1-5,bbbb:1-3", "aaaa:1-5", 3},
		{"newlines and spaces", "aaaa:1-5,\nbbbb:1-3", " aaaa:1-5,\n bbbb:1-3 ", 0},
		{"case insensitive uuid", "AAAA:1-5", "aaaa:1-3", 2},
		{"replica spread over two sets", "aaaa:1-10", "aaaa:1-5,aaaa:6-8", 2},
		{"empty master", "", "aaaa:1-5", 0},
		{"empty replica", "aaaa:1-5", "", 0},
		{"both empty", "", "", 0},
		{"whitespace only", "  \n", "aaaa:1", 0},
		{"unparsable master", "aaaa:x-5", "aaaa:1-3", 0},
		{"unparsable replica", "aaaa:1-5", "garbage", 0},
		{"reversed range", "aaaa:9-5", "aaaa:1", 0},
		{"zero start", "aaaa:0-5", "aaaa:1", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GTIDGap(c.master, c.replica); got != c.want {
				t.Errorf("GTIDGap(%q, %q) = %d, want %d", c.master, c.replica, got, c.want)
			}
		})
	}
}
