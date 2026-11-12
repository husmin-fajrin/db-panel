package mysql_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
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

// TestSeenPortsCapped (SA-4): a replica chooses the port it reports; the
// replica-ports file keeps at most MaxSeenPorts, marks itself full when a
// port is left out (said by the error, after the write) and stays so until
// ForgetReplicaPort(0) empties it.
func TestSeenPortsCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica-ports-seen")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var ports []int
	for p := 20000; p < 21000; p++ {
		ports = append(ports, p)
	}
	err := mysql.RecordReplicaPortsSeen(path, ports, now)
	if !errors.Is(err, mysql.ErrSeenPortsFull) {
		t.Fatalf("RecordReplicaPortsSeen of 1000 ports = %v, want ErrSeenPortsFull", err)
	}
	seen, fullAt, err := mysql.ReadSeenPortsFull(path)
	if err != nil || len(seen) != mysql.MaxSeenPorts || !fullAt.Equal(now) {
		t.Fatalf("after 1000 ports: %d ports, full at %v, %v; want %d, %v", len(seen), fullAt, err, mysql.MaxSeenPorts, now)
	}
	// The ports kept are refreshed without an error; the mark stays.
	kept := make([]int, 0, len(seen))
	for p := range seen {
		kept = append(kept, p)
	}
	if err := mysql.RecordReplicaPortsSeen(path, kept, now.Add(time.Hour)); err != nil {
		t.Fatalf("refresh of the kept ports: %v", err)
	}
	if _, fullAt, _ := mysql.ReadSeenPortsFull(path); fullAt.IsZero() {
		t.Fatal("the full mark went without forget-replica-port")
	}
	// One port forgotten keeps the mark; all forgotten clears it.
	if ok, err := mysql.ForgetReplicaPort(path, kept[0]); !ok || err != nil {
		t.Fatalf("ForgetReplicaPort(%d) = %v, %v", kept[0], ok, err)
	}
	if _, fullAt, _ := mysql.ReadSeenPortsFull(path); fullAt.IsZero() {
		t.Fatal("forgetting one port cleared the full mark")
	}
	if ok, err := mysql.ForgetReplicaPort(path, 0); !ok || err != nil {
		t.Fatalf("ForgetReplicaPort(all) = %v, %v", ok, err)
	}
	if seen, fullAt, err := mysql.ReadSeenPortsFull(path); err != nil || len(seen) != 0 || !fullAt.IsZero() {
		t.Fatalf("after forget all: %v, %v, %v", seen, fullAt, err)
	}
	if ok, err := mysql.ForgetReplicaPort(path, 0); ok || err != nil {
		t.Fatalf("ForgetReplicaPort(all) of an empty file = %v, %v; want false", ok, err)
	}
	if err := mysql.RecordReplicaPortsSeen(path, []int{3306}, now); err != nil {
		t.Fatalf("record after forget: %v", err)
	}
}

// TestSeenFileNeverOverReadLimit (SA-4): the replicas-seen file is never
// written larger than readSeen reads back; the oldest hosts go first and
// the error says so, after the write.
func TestSeenFileNeverOverReadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replicas-seen")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	long := strings.Repeat("h", 200)
	var older, newer []string
	for i := range 3000 {
		older = append(older, fmt.Sprintf("%s-%d.lama", long, i))
		newer = append(newer, fmt.Sprintf("%s-%d.baru", long, i))
	}
	if err := mysql.RecordReplicasSeen(path, older, now.Add(-time.Hour)); err != nil {
		t.Fatalf("first record: %v", err)
	}
	err := mysql.RecordReplicasSeen(path, newer, now)
	if !errors.Is(err, mysql.ErrSeenTrimmed) {
		t.Fatalf("record past the limit = %v, want ErrSeenTrimmed", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() > mysql.MaxSeenSize {
		t.Fatalf("replicas-seen: %v, %v; want at most %d bytes", fi, err, mysql.MaxSeenSize)
	}
	if ok, err := mysql.ReplicaSeen(path, newer[len(newer)-1]); !ok || err != nil {
		t.Fatalf("newest host after the trim: %v, %v", ok, err)
	}
	if ok, err := mysql.ReplicaSeen(path, older[0]); ok || err != nil {
		t.Fatalf("oldest host after the trim: %v, %v; want dropped", ok, err)
	}
	// Still readable, and recordable, afterwards.
	if err := mysql.RecordReplicasSeen(path, []string{"10.0.0.9"}, now.Add(time.Minute)); err != nil && !errors.Is(err, mysql.ErrSeenTrimmed) {
		t.Fatalf("record after the trim: %v", err)
	}
	if ok, err := mysql.ReplicaSeen(path, "10.0.0.9"); !ok || err != nil {
		t.Fatalf("host recorded after the trim: %v, %v", ok, err)
	}
}

// TestLockTooLargeNotWritten (SA-4): a promote lock whose encoding is
// larger than ReadLock reads back is refused and nothing is written.
func TestLockTooLargeNotWritten(t *testing.T) {
	dir := pinDir(t, "master", "")
	p, err := mysql.LoadPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	var readable []string
	for i := 0; len(strings.Join(readable, `","`)) <= mysql.MaxRecordSize; i++ {
		readable = append(readable, fmt.Sprintf("10.%d.%d.%d:3306", i/65536, i/256%256, i%256))
	}
	err = p.WriteLock(mysql.PromoteLock{Candidate: "10.9.9.9:3306", Deadline: time.Now().Add(time.Hour), Readable: readable})
	if err == nil {
		t.Fatal("a lock larger than what is read back was written")
	}
	if got := pinFile(t, dir, mysql.LockFile); got != "" {
		t.Errorf("lock file after the refusal: %d bytes", len(got))
	}
}

// TestLockForPromoteFanOutBounded (SA-4): the reads of the other replicas
// at lock time are bounded by count and by time, well under the panel's
// call timeout, and a full port record refuses the lock: each refusal
// writes no lock file and leaves the master writable.
func TestLockForPromoteFanOutBounded(t *testing.T) {
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	replUser(t, master)
	mdb := rootDB(t, master)
	follow(t, master, replica)
	waitDumpThread(t, mdb)
	cand := proto.PeerCred{Host: "127.0.0.1", Port: replica.Port, User: "dbpanel_repl", Password: replPassword}
	params := proto.LockForPromoteParams{Candidate: cand, TimeoutSec: 600}
	refused := func(t *testing.T, r *pinRig, dir string, resp proto.Response, parts ...string) {
		t.Helper()
		wantRefused(t, "lock", resp, parts...)
		if got := pinFile(t, dir, mysql.LockFile); got != "" {
			t.Errorf("refused lock left lock file %q", got)
		}
		if sro, ro := readOnly(t, mdb); sro || ro {
			t.Errorf("refused lock: super_read_only=%v read_only=%v", sro, ro)
		}
	}
	rig := func(t *testing.T) (*pinRig, string, string) {
		dir := pinDir(t, "master", "")
		r := newPinRig(t, master.Host(), dir)
		seenDir := t.TempDir()
		r.d.SeenPath = filepath.Join(seenDir, "replicas-seen")
		return r, dir, seenDir
	}

	t.Run("count", func(t *testing.T) {
		r, dir, _ := rig(t)
		var hosts []string
		for i := range mysql.MaxLockReads + 1 {
			hosts = append(hosts, "10.77.0."+strconv.Itoa(i))
		}
		if err := mysql.RecordReplicasSeen(r.d.SeenPath, hosts, time.Now()); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		resp := r.dispatch("mysql.lockForPromote", params)
		refused(t, r, dir, resp, "terlalu banyak replika tercatat", r.d.SeenPath, "forget-replica")
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("refusal took %v, want at once", took)
		}
	})

	t.Run("budget", func(t *testing.T) {
		r, dir, _ := rig(t)
		if err := mysql.RecordReplicasSeen(r.d.SeenPath, []string{"10.77.1.1"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mysql.SetLockReadBudget(500 * time.Millisecond))
		t.Cleanup(mysql.SetReadPeer(func(ctx context.Context, c proto.PeerCred, timeout time.Duration) mysql.PeerState {
			if c.Host == "10.77.1.1" {
				<-ctx.Done() // a host that drops packets
				return mysql.PeerState{Err: ctx.Err().Error()}
			}
			return mysql.ReadPeer(ctx, c, timeout)
		}))
		start := time.Now()
		resp := r.dispatch("mysql.lockForPromote", params)
		refused(t, r, dir, resp, "belum selesai dibaca", "master tidak dikunci")
		if took := time.Since(start); took > 15*time.Second {
			t.Errorf("refusal took %v, want within the budget", took)
		}
	})

	t.Run("ports full", func(t *testing.T) {
		r, dir, seenDir := rig(t)
		ports := filepath.Join(seenDir, "replica-ports-seen")
		var many []int
		for p := 20000; p < 20000+mysql.MaxSeenPorts+1; p++ {
			many = append(many, p)
		}
		if err := mysql.RecordReplicaPortsSeen(ports, many, time.Now()); !errors.Is(err, mysql.ErrSeenPortsFull) {
			t.Fatalf("record = %v", err)
		}
		resp := r.dispatch("mysql.lockForPromote", params)
		refused(t, r, dir, resp, "penuh", ports, "forget-replica-port semua")
	})
}

// TestReadPeerBoundsAnswer (SA-10): a peer that answers with a huge error
// message (a squatter at a recorded address) adds at most a few hundred
// bytes to the read's error, which goes to the logs and the kept event.
func TestReadPeerBoundsAnswer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				msg := bytes.Repeat([]byte("x"), 2<<20)
				payload := append([]byte{0xff, 0x10, 0x04}, msg...)
				n := len(payload)
				hdr := []byte{byte(n), byte(n >> 8), byte(n >> 16), 0}
				c.Write(append(hdr, payload...))
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	start := time.Now()
	st := mysql.ReadPeer(context.Background(), proto.PeerCred{Host: "127.0.0.1", Port: port, User: "u", Password: "p"}, 5*time.Second)
	if st.Reachable {
		t.Fatal("a fake server was read")
	}
	if len(st.Err) > 512 {
		t.Errorf("Err is %d bytes, want at most 512", len(st.Err))
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("ReadPeer took %v", took)
	}
}

// TestStartLockFileFixNeverFollowsLink (SA-7): the owner and mode of an
// existing start lock file are fixed on the file that was checked, never
// on whatever a symlink put at its path in between points to.
func TestStartLockFileFixNeverFollowsLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbpanel-start-lock.sql")
	victim := filepath.Join(dir, "rahasia")
	if err := os.WriteFile(victim, []byte("rahasia"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(mysql.StartLockContent), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mysql.SetAfterStartLockOpen(func(p string) {
		// Swapped for a link to another file after the check.
		if err := os.Remove(p); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(victim, p); err != nil {
			t.Error(err)
		}
	}))
	if err := mysql.WriteStartLockFile(path); err != nil {
		t.Logf("WriteStartLockFile: %v", err)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("victim after the fix: %v, %v; want mode 0600 untouched", fi, err)
	}
}
