package mysql_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
)

// TestReplicasSeenPrune: RecordReplicasSeen keeps one line per host with
// its newest time, drops entries older than a year, and ReplicaSeen
// matches a host exactly.
func TestReplicasSeenPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib", "replicas-seen")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if ok, err := mysql.ReplicaSeen(path, "10.0.0.2"); ok || err != nil {
		t.Fatalf("ReplicaSeen without a file = %v, %v; want false, nil", ok, err)
	}
	old := now.AddDate(-1, 0, -1)
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.2", "10.0.0.9"}, old); err != nil {
		t.Fatal(err)
	}
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.3", "fd00::2"}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An older reading never moves a host's time back.
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.3"}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.2", "10.0.0.2"}, now); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "10.0.0.2 2026-11-06T10:00:00Z\n10.0.0.3 2026-11-06T09:00:00Z\nfd00::2 2026-11-06T09:00:00Z\n"
	if string(b) != want {
		t.Errorf("replicas-seen =\n%s\nwant\n%s", b, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("replicas-seen: %v, %v; want mode 0644", fi, err)
	}
	for host, want := range map[string]bool{"10.0.0.2": true, "fd00::2": true, "10.0.0.9": false, "10.0.0.": false, "10.0.0.22": false, "": false} {
		if ok, err := mysql.ReplicaSeen(path, host); ok != want || err != nil {
			t.Errorf("ReplicaSeen(%q) = %v, %v; want %v", host, ok, err, want)
		}
	}
	// A file others can write is not trusted.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := mysql.ReplicaSeen(path, "10.0.0.2"); err == nil {
		t.Error("ReplicaSeen trusted a world-writable file")
	}
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.4"}, now); err == nil {
		t.Error("RecordReplicasSeen merged a world-writable file")
	}
}

// TestPromoteLockAndRecordFiles: the promote lock and the promotion record
// round-trip through their JSON files (0644, the pins' atomic writer and
// safe reader); a missing file is not an error, a broken one is.
func TestPromoteLockAndRecordFiles(t *testing.T) {
	dir := pinDir(t, "master", "")
	p := mustPins(t, dir)
	if _, ok, err := p.ReadLock(); ok || err != nil {
		t.Fatalf("ReadLock without a file = %v, %v", ok, err)
	}
	deadline := time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)
	if err := p.WriteLock(mysql.PromoteLock{Candidate: "srv-2", Deadline: deadline}); err != nil {
		t.Fatal(err)
	}
	l, ok, err := p.ReadLock()
	if err != nil || !ok || l.Candidate != "srv-2" || !l.Deadline.Equal(deadline) {
		t.Errorf("ReadLock = %+v, %v, %v", l, ok, err)
	}
	if got := pinFile(t, dir, mysql.LockFile); got != `{"candidate":"srv-2","deadline":"2026-11-06T11:00:00Z"}` {
		t.Errorf("lock file = %s", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, mysql.LockFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("lock file: %v, %v; want mode 0644", fi, err)
	}
	if err := p.ClearLock(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := p.ReadLock(); ok || err != nil {
		t.Errorf("ReadLock after ClearLock = %v, %v", ok, err)
	}
	for _, bad := range []string{"{", `{"candidate":"","deadline":"2026-11-06T11:00:00Z"}`, `{"candidate":"x","deadline":"besok"}`} {
		if err := os.WriteFile(filepath.Join(dir, mysql.LockFile), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.ReadLock(); err == nil {
			t.Errorf("ReadLock accepted %q", bad)
		}
	}

	if _, ok, err := p.ReadPromoted(); ok || err != nil {
		t.Fatalf("ReadPromoted without a file = %v, %v", ok, err)
	}
	at := time.Date(2026, 11, 6, 10, 30, 0, 0, time.UTC)
	gtid := strings.Repeat("3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5,", 20) + "3e11fa47-71ca-11e1-9e33-c80aa9429563:1"
	pr := mysql.Promoted{At: at, Mode: "planned", OldSource: "10.0.0.1:3306", GTIDExecuted: gtid}
	if err := p.WritePromoted(pr); err != nil {
		t.Fatal(err)
	}
	got, ok, err := p.ReadPromoted()
	if err != nil || !ok || !got.At.Equal(at) || got.Mode != "planned" || got.OldSource != "10.0.0.1:3306" || got.GTIDExecuted != gtid {
		t.Errorf("ReadPromoted = %+v, %v, %v", got, ok, err)
	}
	if !strings.Contains(pinFile(t, dir, mysql.PromotedFile), `"old_source":"10.0.0.1:3306"`) {
		t.Errorf("promoted file = %s", pinFile(t, dir, mysql.PromotedFile))
	}
	if fi, err := os.Stat(filepath.Join(dir, mysql.PromotedFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("promoted file: %v, %v; want mode 0644", fi, err)
	}
}

// TestBinlogDumpHostsWithoutPort: the host of a processlist row loses its
// port, IPv6 included; a socket's "localhost" stays.
func TestBinlogDumpHostsWithoutPort(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:51234":  "127.0.0.1",
		"fd00::2:41234":    "fd00::2",
		"replika.lan:3306": "replika.lan",
		"localhost":        "localhost",
	} {
		if got := mysql.HostWithoutPort(in); got != want {
			t.Errorf("HostWithoutPort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDriverRecordsReplicasSeen: the driver records the host of each
// replica reading its binary log (monitor.ReplicaRecorder) in SeenPath.
func TestDriverRecordsReplicasSeen(t *testing.T) {
	master, _ := sourceWithReplica(t)
	path := filepath.Join(t.TempDir(), "replicas-seen")
	d := &mysql.Driver{Host: master.Host(), SeenPath: path}
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if err := d.RecordReplicas(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	// The replica connects from 127.0.0.1, which MySQL shows resolved
	// ("localhost") unless skip_name_resolve is on: the processlist's
	// host, without the port, is what is recorded.
	var host string
	if err := rootDB(t, master).QueryRow(`SELECT HOST FROM information_schema.PROCESSLIST
		WHERE COMMAND LIKE 'Binlog Dump%'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	host = mysql.HostWithoutPort(host)
	b, _ := os.ReadFile(path)
	if want := host + " 2026-11-06T10:00:00Z\n"; string(b) != want {
		t.Errorf("replicas-seen = %q, want %q", b, want)
	}
	if ok, err := mysql.ReplicaSeen(path, host); !ok || err != nil {
		t.Errorf("ReplicaSeen(%q) = %v, %v", host, ok, err)
	}
	// Without MySQL there is nothing to record and nothing fails.
	if err := (&mysql.Driver{SeenPath: path}).RecordReplicas(context.Background(), now); err != nil {
		t.Errorf("RecordReplicas without MySQL = %v", err)
	}
	// A server nobody replicates from writes nothing.
	lone := testmysql.Start(t, testmysql.Options{})
	lonePath := filepath.Join(t.TempDir(), "replicas-seen")
	if err := (&mysql.Driver{Host: lone.Host(), SeenPath: lonePath}).RecordReplicas(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lonePath); !os.IsNotExist(err) {
		t.Errorf("replicas-seen written without replicas: %v", err)
	}
}
