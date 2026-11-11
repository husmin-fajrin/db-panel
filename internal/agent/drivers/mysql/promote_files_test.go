package mysql_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
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
	// The same replicas a check later: nothing is rewritten; a day later
	// the times are refreshed.
	if err := d.RecordReplicas(context.Background(), now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != host+" 2026-11-06T10:00:00Z\n" {
		t.Errorf("replicas-seen rewritten for the same hosts: %q", b)
	}
	if err := d.RecordReplicas(context.Background(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != host+" 2026-11-07T10:00:00Z\n" {
		t.Errorf("replicas-seen after a day = %q, want refreshed", b)
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

// TestSeenPortsFileFormatAndPrune: the port file has one "<port> <RFC3339>"
// line per port, sorted, an entry older than a year is dropped, a line that
// is not a port is skipped, and the file is 0644.
func TestSeenPortsFileFormatAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib", "replica-ports-seen")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if m, err := mysql.ReadSeenPorts(path); err != nil || len(m) != 0 {
		t.Fatalf("ReadSeenPorts without a file = %v, %v; want empty, nil", m, err)
	}
	if err := mysql.RecordReplicaPortsSeen(path, []int{3307}, now.AddDate(-1, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if err := mysql.RecordReplicaPortsSeen(path, []int{3310, 3308}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An older reading never moves a port's time back; out-of-range ports
	// are ignored.
	if err := mysql.RecordReplicaPortsSeen(path, []int{3308, 0, 65536, -1}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "3308 2026-11-06T09:00:00Z\n3310 2026-11-06T09:00:00Z\n"; string(b) != want {
		t.Errorf("replica-ports-seen =\n%s\nwant\n%s", b, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("replica-ports-seen: %v, %v; want mode 0644", fi, err)
	}
	// A broken line is skipped; the next write drops it.
	junk := "abc x\n0 2026-11-06T09:00:00Z\n70000 2026-11-06T09:00:00Z\n3309 not-a-time\n3308 2026-11-06T09:00:00Z\n"
	if err := os.WriteFile(path, []byte(junk), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := mysql.ReadSeenPorts(path)
	if err != nil || len(m) != 1 || !m[3308].Equal(now.Add(-time.Hour)) {
		t.Fatalf("ReadSeenPorts = %v, %v; want only 3308", m, err)
	}
	if err := mysql.RecordReplicaPortsSeen(path, []int{3311}, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "3308 2026-11-06T09:00:00Z\n3311 2026-11-06T10:00:00Z\n" {
		t.Errorf("replica-ports-seen after a write = %q", b)
	}
	// A file others can write is not trusted.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := mysql.ReadSeenPorts(path); err == nil {
		t.Error("ReadSeenPorts trusted a world-writable file")
	}
}

// TestOldSeenFileReadUnchanged: a replicas-seen file in the old format is
// read as before and merged in place; with the same replicas connected the
// host file and the (new) port file are not rewritten again, however often
// the guard loop records.
func TestOldSeenFileReadUnchanged(t *testing.T) {
	master, replica := sourceWithReplica(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "replicas-seen")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	old := "10.9.9.9 2026-10-01T08:00:00Z\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := mysql.ReplicaSeen(path, "10.9.9.9"); !ok || err != nil {
		t.Fatalf("ReplicaSeen on an old-format file = %v, %v", ok, err)
	}
	d := &mysql.Driver{Host: master.Host(), SeenPath: path}
	if err := d.RecordReplicas(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := rootDB(t, master).QueryRow(`SELECT HOST FROM information_schema.PROCESSLIST
		WHERE COMMAND LIKE 'Binlog Dump%'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	host = mysql.HostWithoutPort(host)
	if b, _ := os.ReadFile(path); string(b) != old+host+" 2026-11-06T10:00:00Z\n" && string(b) != host+" 2026-11-06T10:00:00Z\n10.9.9.9 2026-10-01T08:00:00Z\n" {
		t.Errorf("replicas-seen after merging = %q", b)
	}
	portsPath := filepath.Join(dir, "replica-ports-seen")
	ports, err := mysql.ReadSeenPorts(portsPath)
	if err != nil || ports[replica.Port].IsZero() {
		t.Fatalf("ports = %v, %v; want the replica's port %d", ports, err, replica.Port)
	}
	// Mark both files with an old mtime; a round with the same set must
	// leave them alone.
	mark := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range []string{path, portsPath} {
		if err := os.Chtimes(p, mark, mark); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if err := d.RecordReplicas(context.Background(), now.Add(time.Duration(i*5)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, portsPath} {
		if fi, err := os.Stat(p); err != nil || !fi.ModTime().Equal(mark) {
			t.Errorf("%s rewritten for the same replicas: %v, %v", filepath.Base(p), fi, err)
		}
	}
}

// TestRecordReplicasStoresPortsFromShowReplicas: the port of a connected
// replica (SHOW REPLICAS) is recorded beside its host; an account that can
// list processes but not SHOW REPLICAS still gets the host recorded.
func TestRecordReplicasStoresPortsFromShowReplicas(t *testing.T) {
	master, replica := sourceWithReplica(t)
	dir := t.TempDir()
	d := &mysql.Driver{Host: master.Host(), SeenPath: filepath.Join(dir, "replicas-seen"), SeenPortsPath: filepath.Join(dir, "ports")}
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if err := d.RecordReplicas(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d.SeenPortsPath); string(b) != strconv.Itoa(replica.Port)+" 2026-11-06T10:00:00Z\n" {
		t.Errorf("replica-ports-seen = %q, want port %d", b, replica.Port)
	}
	ports, err := mysql.ReplicaPorts(context.Background(), rootDB(t, master))
	if err != nil || len(ports) != 1 || ports[0] != replica.Port {
		t.Errorf("ReplicaPorts = %v, %v; want [%d]", ports, err, replica.Port)
	}

	// PROCESS alone: SHOW REPLICAS is refused, the hosts are recorded all
	// the same and nothing fails.
	execAll(t, rootDB(t, master), "CREATE USER 'adm'@'localhost' IDENTIFIED BY 'Adm-pw-1'", "GRANT PROCESS ON *.* TO 'adm'@'localhost'")
	weak := &mysql.Driver{Host: master.HostWith(&memCreds{p: proto.ConnectParams{User: "adm", Password: "Adm-pw-1"}}),
		SeenPath: filepath.Join(dir, "weak-seen"), SeenPortsPath: filepath.Join(dir, "weak-ports")}
	if err := weak.RecordReplicas(context.Background(), now); err != nil {
		t.Fatalf("RecordReplicas without the right to SHOW REPLICAS = %v", err)
	}
	if b, _ := os.ReadFile(weak.SeenPath); len(b) == 0 {
		t.Error("hosts not recorded when SHOW REPLICAS fails")
	}
	if _, err := os.Stat(weak.SeenPortsPath); !os.IsNotExist(err) {
		t.Errorf("port file written without ports: %v", err)
	}
}

// TestRecordReplicasCommandTakesNoData: mysql.recordReplicas runs the
// observation now and accepts no parameter; a host sent by the panel is
// refused and never reaches the file.
func TestRecordReplicasCommandTakesNoData(t *testing.T) {
	master, replica := sourceWithReplica(t)
	r := newPinRig(t, master.Host(), pinDir(t, "master", ""))
	for _, bad := range []string{`{"hosts":["10.0.0.9"]}`, `{"ports":[3306]}`, `["10.0.0.9"]`, `"x"`, `1`} {
		resp := r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.recordReplicas", Params: []byte(bad)})
		if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeBadParams {
			t.Errorf("mysql.recordReplicas %s = %+v, want a parameter error", bad, resp)
		}
	}
	if _, err := os.Stat(r.d.SeenPath); !os.IsNotExist(err) {
		t.Fatalf("a refused command wrote the host file: %v", err)
	}
	for _, ok := range []string{``, `null`, `{}`, ` { } `} {
		resp := r.reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.recordReplicas", Params: []byte(ok)})
		res := decode[proto.RecordReplicasResult](t, resp)
		if res.Hosts != 1 || res.Ports != 1 {
			t.Errorf("mysql.recordReplicas %q = %+v, want 1 host and 1 port", ok, res)
		}
	}
	if b, _ := os.ReadFile(r.d.SeenPath); strings.Contains(string(b), "10.0.0.9") || len(b) == 0 {
		t.Errorf("replicas-seen = %q; want the replica's host only", b)
	}
	if ports, err := mysql.ReadSeenPorts(filepath.Join(filepath.Dir(r.d.SeenPath), "replica-ports-seen")); err != nil || ports[replica.Port].IsZero() {
		t.Errorf("ports = %v, %v; want %d", ports, err, replica.Port)
	}
	// Without MySQL the command answers an empty result.
	lone := newPinRig(t, nil, pinDir(t, "master", ""))
	if res := decode[proto.RecordReplicasResult](t, lone.dispatch("mysql.recordReplicas", nil)); res.Hosts != 0 || res.Ports != 0 {
		t.Errorf("mysql.recordReplicas without MySQL = %+v", res)
	}
}

// TestLockReadableFile: the lock keeps the replicas readable when it was
// set under "readable"; a lock without them (an older agent's) reads as
// an empty list, and ForgetLockHost removes one host's entries, at every
// port, leaving the rest of the lock as it was.
func TestLockReadableFile(t *testing.T) {
	dir := pinDir(t, "master", "")
	p := mustPins(t, dir)
	deadline := time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)
	if n, err := p.ForgetLockHost("10.0.0.3"); n != 0 || err != nil {
		t.Errorf("ForgetLockHost without a lock = %d, %v", n, err)
	}
	if err := p.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: deadline}); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, dir, mysql.LockFile); strings.Contains(got, "readable") {
		t.Errorf("lock without readable replicas = %s", got)
	}
	if l, ok, err := p.ReadLock(); !ok || err != nil || len(l.Readable) != 0 {
		t.Errorf("ReadLock = %+v, %v, %v; want no readable replicas", l, ok, err)
	}
	list := []string{"10.0.0.3:3306", "10.0.0.3:3307", "DB-4.example:3306", "[::1]:3306"}
	if err := p.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: deadline, Readable: list}); err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, dir, mysql.LockFile); !strings.Contains(got, `"readable":["10.0.0.3:3306","10.0.0.3:3307","DB-4.example:3306","[::1]:3306"]`) {
		t.Errorf("lock file = %s", got)
	}
	l, ok, err := p.ReadLock()
	if !ok || err != nil || strings.Join(l.Readable, ",") != strings.Join(list, ",") {
		t.Fatalf("ReadLock = %+v, %v, %v", l, ok, err)
	}
	for _, c := range []struct {
		host string
		n    int
		left string
	}{
		{"10.0.0.3", 2, "DB-4.example:3306,[::1]:3306"},
		{"10.0.0.9", 0, "DB-4.example:3306,[::1]:3306"},
		{"db-4.EXAMPLE", 1, "[::1]:3306"},
		{"::1", 1, ""},
	} {
		if n, err := p.ForgetLockHost(c.host); n != c.n || err != nil {
			t.Errorf("ForgetLockHost(%q) = %d, %v; want %d", c.host, n, err, c.n)
		}
		l, ok, err := p.ReadLock()
		if !ok || err != nil || strings.Join(l.Readable, ",") != c.left || l.Candidate != "10.0.0.2:3306" || l.Addr != "10.0.0.2" || !l.Deadline.Equal(deadline) {
			t.Errorf("after ForgetLockHost(%q): %+v, %v, %v; want readable %q, the rest unchanged", c.host, l, ok, err, c.left)
		}
	}
}

// TestForgetReplica: ForgetReplica removes one host (any case) from the
// replicas-seen file and keeps the others with their times; a host that is
// not there, or no file, changes nothing.
func TestForgetReplica(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replicas-seen")
	if ok, err := mysql.ForgetReplica(path, "10.0.0.3"); ok || err != nil {
		t.Errorf("ForgetReplica without a file = %v, %v", ok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("ForgetReplica without a file created it: %v", err)
	}
	t1 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.3", "db-4.example", "10.0.0.5"}, t1); err != nil {
		t.Fatal(err)
	}
	if ok, err := mysql.ForgetReplica(path, "10.0.0.3"); !ok || err != nil {
		t.Errorf("ForgetReplica(10.0.0.3) = %v, %v; want true", ok, err)
	}
	if ok, err := mysql.ForgetReplica(path, "DB-4.example"); !ok || err != nil {
		t.Errorf("ForgetReplica(DB-4.example) = %v, %v; want true", ok, err)
	}
	if ok, err := mysql.ForgetReplica(path, "10.0.0.3"); ok || err != nil {
		t.Errorf("ForgetReplica again = %v, %v; want false", ok, err)
	}
	if got, want := pinFile(t, filepath.Dir(path), "replicas-seen"), "10.0.0.5 2026-10-01T08:00:00Z"; got != want {
		t.Errorf("replicas-seen = %q, want %q", got, want)
	}
}

// TestObserveReplicasRecordsSeenWhenProcesslistFails (ruling 10): the
// candidate's matched address given to observeReplicas is recorded even
// when the processlist cannot be read; the error is still returned.
func TestObserveReplicasRecordsSeenWhenProcesslistFails(t *testing.T) {
	db, err := sql.Open("mysql", "root@unix(/nonexistent/mysql.sock)/")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	seen := filepath.Join(t.TempDir(), "replicas-seen")
	d := &mysql.Driver{SeenPath: seen}
	hosts, _, err := mysql.ObserveReplicas(d, context.Background(), db, time.Now(), "10.0.0.8")
	if err == nil {
		t.Error("ObserveReplicas on a closed connection: no error")
	}
	if len(hosts) != 1 || hosts[0] != "10.0.0.8" {
		t.Errorf("hosts = %v, want [10.0.0.8]", hosts)
	}
	if ok, err := mysql.ReplicaSeen(seen, "10.0.0.8"); !ok || err != nil {
		t.Errorf("ReplicaSeen(10.0.0.8) = %v, %v; want true", ok, err)
	}
}

// TestForgetLockHostKeepsLockReplacedMeanwhile: a new lock written (by
// another process: another Pins over the same directory) while
// ForgetLockHost is between its read and its write survives unchanged:
// the new candidate, deadline and list are never overwritten with the old
// lock's.
func TestForgetLockHostKeepsLockReplacedMeanwhile(t *testing.T) {
	dir := pinDir(t, "master", "")
	cli, agent := mustPins(t, dir), mustPins(t, dir)
	old := mysql.PromoteLock{Candidate: "10.0.0.2:3306", Addr: "10.0.0.2", Deadline: time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC),
		Readable: []string{"10.0.0.3:3306", "10.0.0.4:3306"}}
	if err := agent.WriteLock(old); err != nil {
		t.Fatal(err)
	}
	repl := mysql.PromoteLock{Candidate: "10.0.0.5:3306", Addr: "10.0.0.5", Deadline: time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC),
		Readable: []string{"10.0.0.3:3306", "10.0.0.6:3306"}}
	done := make(chan error, 1)
	t.Cleanup(mysql.SetAfterForgetRead(func() {
		go func() { done <- agent.WriteLock(repl) }()
		time.Sleep(300 * time.Millisecond)
	}))
	if _, err := cli.ForgetLockHost("10.0.0.3"); err != nil {
		t.Fatalf("ForgetLockHost: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("WriteLock of the new lock: %v", err)
	}
	l, ok, err := agent.ReadLock()
	if !ok || err != nil || l.Candidate != repl.Candidate || l.Addr != repl.Addr || !l.Deadline.Equal(repl.Deadline) ||
		strings.Join(l.Readable, ",") != strings.Join(repl.Readable, ",") {
		t.Errorf("lock = %+v, %v, %v; want the new lock unchanged %+v", l, ok, err, repl)
	}
}

// TestPromoteLockFlockExcludesAcrossOpens: while one holder (another open
// of LockFlockFile, as another process has) holds the flock, WriteLock,
// ClearLock and ForgetLockHost of another Pins wait and change nothing;
// past the wait they fail and leave the lock file as it was. The flock
// file is 0600 and stays.
func TestPromoteLockFlockExcludesAcrossOpens(t *testing.T) {
	dir := pinDir(t, "master", "")
	holder, other := mustPins(t, dir), mustPins(t, dir)
	l0 := mysql.PromoteLock{Candidate: "10.0.0.2:3306", Deadline: time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC), Readable: []string{"10.0.0.3:3306"}}
	if err := other.WriteLock(l0); err != nil {
		t.Fatal(err)
	}
	before := pinFile(t, dir, mysql.LockFile)
	release, err := mysql.HoldLockFlock(holder)
	if err != nil {
		t.Fatal(err)
	}
	flock := filepath.Join(dir, mysql.LockFlockFile)
	if fi, err := os.Lstat(flock); err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Errorf("flock file: %v, %v; want a regular file, mode 0600", fi, err)
	}

	// Past the wait: each write fails and the file is as it was.
	restore := mysql.SetLockFlockWait(200 * time.Millisecond)
	if err := other.WriteLock(mysql.PromoteLock{Candidate: "10.0.0.9:3306", Deadline: l0.Deadline}); err == nil {
		t.Error("WriteLock succeeded while another holder had the flock")
	}
	if err := other.ClearLock(); err == nil {
		t.Error("ClearLock succeeded while another holder had the flock")
	}
	if _, err := other.ForgetLockHost("10.0.0.3"); err == nil {
		t.Error("ForgetLockHost succeeded while another holder had the flock")
	}
	if got := pinFile(t, dir, mysql.LockFile); got != before {
		t.Errorf("lock file after refused writes = %s, want %s", got, before)
	}
	restore()

	// Within the wait: the write waits for the holder, then happens.
	done := make(chan error, 1)
	go func() { done <- other.ClearLock() }()
	time.Sleep(300 * time.Millisecond)
	if got := pinFile(t, dir, mysql.LockFile); got != before {
		t.Errorf("lock file while ClearLock waits = %q", got)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("ClearLock once the flock is free: %v", err)
	}
	if got := pinFile(t, dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after ClearLock = %q", got)
	}
	if _, err := os.Lstat(flock); err != nil {
		t.Errorf("flock file removed: %v", err)
	}
}
