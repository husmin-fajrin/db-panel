package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// scanNow is the fake clock of the folder scans.
var scanNow = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

// mkfile writes size bytes at path (creating its parents) and sets its
// modification time.
func mkfile(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// scanWithin scans dir at scanNow and fails the test when the scan does
// not return within a minute of real time.
func scanWithin(t *testing.T, dir string) backupResult {
	t.Helper()
	done := make(chan backupResult, 1)
	go func() { done <- scanBackup(dir, func() time.Time { return scanNow }) }()
	select {
	case r := <-done:
		return r
	case <-time.After(time.Minute):
		t.Fatal("scan did not return")
		return backupResult{}
	}
}

func TestBackupScanFindsNewestWithinThreeLevels(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "l1.bak"), 1, scanNow.Add(-4*time.Hour))
	mkfile(t, filepath.Join(dir, "a", "l2.bak"), 2, scanNow.Add(-3*time.Hour))
	mkfile(t, filepath.Join(dir, "a", "b", "l3.bak"), 3, scanNow.Add(-time.Hour))
	mkfile(t, filepath.Join(dir, "a", "b", "c", "l4.bak"), 4, scanNow.Add(-time.Minute))
	got := *scanWithin(t, dir).state(scanNow)
	want := proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600, NewestSize: 3}
	if got != want {
		t.Fatalf("state = %+v, want %+v (the level-4 file ignored)", got, want)
	}
}

func TestBackupScanSkipsSymlinksAndSpecials(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	// The only regular file is very old: anything followed or opened
	// would be newer and win.
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	mkfile(t, filepath.Join(dir, "real.bak"), 7, old)
	mkfile(t, filepath.Join(outside, "newer.bak"), 99, scanNow.Add(-time.Minute))
	mkfile(t, filepath.Join(outside, "sub", "newer2.bak"), 98, scanNow.Add(-time.Minute))
	for name, target := range map[string]string{
		"link-file":  filepath.Join(outside, "newer.bak"),
		"link-dir":   outside,
		"link-rel":   filepath.Join("..", filepath.Base(outside), "newer.bak"),
		"passwd":     "/etc/passwd",
		"a/link-dir": filepath.Join(outside, "sub"),
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(dir, "fifo"), filepath.Join(dir, "a", "fifo")} {
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := *scanWithin(t, dir).state(scanNow)
	want := proto.BackupState{Status: proto.BackupOK, NewestAgeSec: int64(scanNow.Sub(old) / time.Second), NewestSize: 7}
	if got != want {
		t.Fatalf("state = %+v, want %+v (symlinks and FIFOs skipped)", got, want)
	}
}

func TestBackupScanEmptyFolderIsNone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := *scanWithin(t, dir).state(scanNow)
	if want := (proto.BackupState{Status: proto.BackupNone}); got != want {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
}

func TestBackupScanZeroByteNewest(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "old.bak"), 100, scanNow.Add(-2*time.Hour))
	mkfile(t, filepath.Join(dir, "new.bak"), 0, scanNow.Add(-time.Hour))
	got := *scanWithin(t, dir).state(scanNow)
	if want := (proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600}); got != want {
		t.Fatalf("state = %+v, want %+v (NewestSize 0)", got, want)
	}
}

func TestBackupScanFutureMtimeIsAgeZero(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "future.bak"), 5, scanNow.Add(time.Hour))
	got := *scanWithin(t, dir).state(scanNow)
	if want := (proto.BackupState{Status: proto.BackupOK, NewestSize: 5}); got != want {
		t.Fatalf("state = %+v, want %+v (age 0)", got, want)
	}
}

// symlinks makes n dangling symlinks in dir: entries that are no regular
// file, cheap to make.
func symlinks(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.Symlink("nowhere", filepath.Join(dir, fmt.Sprintf("l%05d", i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupScanLimits(t *testing.T) {
	t.Run("no regular file", func(t *testing.T) {
		dir := t.TempDir()
		symlinks(t, dir, 10001)
		got := *scanWithin(t, dir).state(scanNow)
		if want := (proto.BackupState{Status: proto.BackupUnreadable, Reason: proto.BackupReasonLimit, Truncated: true}); got != want {
			t.Fatalf("state = %+v, want %+v", got, want)
		}
	})
	t.Run("regular file first", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, filepath.Join(dir, "0000.bak"), 3, scanNow.Add(-time.Hour))
		symlinks(t, dir, 10000)
		got := *scanWithin(t, dir).state(scanNow)
		if want := (proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600, NewestSize: 3, Truncated: true}); got != want {
			t.Fatalf("state = %+v, want %+v", got, want)
		}
	})
	t.Run("exactly the limit", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, filepath.Join(dir, "0000.bak"), 3, scanNow.Add(-time.Hour))
		symlinks(t, dir, 9999)
		got := *scanWithin(t, dir).state(scanNow)
		if want := (proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600, NewestSize: 3}); got != want {
			t.Fatalf("state = %+v, want %+v (10,000 entries are not truncated)", got, want)
		}
	})
}

func TestBackupScanUnreadableReasons(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	mkfile(t, file, 1, scanNow)
	real := filepath.Join(dir, "real")
	mkfile(t, filepath.Join(real, "x.bak"), 1, scanNow)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(dir, "proc")
	if err := os.Symlink("/proc", proc); err != nil {
		t.Fatal(err)
	}
	// scanBackup takes the folder as given; these are paths the folder
	// rule lets through but the file system refuses.
	for folder, reason := range map[string]string{
		filepath.Join(dir, "missing"): proto.BackupReasonMissing,
		file:                          proto.BackupReasonNotDir,
		filepath.Join(file, "x"):      proto.BackupReasonNotDir,
		link:                          proto.BackupReasonNotDir, // a symlink as the folder is not scanned
		filepath.Join(proc, "1"):      proto.BackupReasonInvalid,
	} {
		got := *scanWithin(t, folder).state(scanNow)
		if want := (proto.BackupState{Status: proto.BackupUnreadable, Reason: reason}); got != want {
			t.Errorf("scan %s = %+v, want %+v", folder, got, want)
		}
	}
	// The folder rule alone, on path strings.
	for folder, reason := range map[string]string{
		"/tmp/x":                            proto.BackupReasonHidden,
		"/tmp":                              proto.BackupReasonHidden,
		"/home/x":                           proto.BackupReasonHidden,
		"/root":                             proto.BackupReasonHidden,
		"/run/user/1000":                    proto.BackupReasonHidden,
		"/var/tmp/b":                        proto.BackupReasonHidden,
		"relatif/x":                         proto.BackupReasonInvalid,
		"":                                  proto.BackupReasonInvalid,
		"/a/../b":                           proto.BackupReasonInvalid,
		"/srv/backup/":                      proto.BackupReasonInvalid,
		"/srv//backup":                      proto.BackupReasonInvalid,
		"/":                                 proto.BackupReasonInvalid,
		"/proc/1":                           proto.BackupReasonInvalid,
		"/proc":                             proto.BackupReasonInvalid,
		"/sys/fs":                           proto.BackupReasonInvalid,
		"/dev/sda":                          proto.BackupReasonInvalid,
		"/srv/back\nup":                     proto.BackupReasonInvalid,
		"/srv/back\x7fup":                   proto.BackupReasonInvalid,
		"/tmp/x\x01":                        proto.BackupReasonInvalid,
		"/" + strings.Repeat("a", 1024):     proto.BackupReasonInvalid,
		"/srv/" + strings.Repeat("a", 1019): "", // 1024 bytes
		"/srv/backup":                       "",
		"/var/backups":                      "",
		"/home2/x":                          "",
		"/run/users":                        "",
		"/tmpx":                             "",
		"/devices":                          "",
	} {
		got, ok := ValidBackupFolder(folder)
		if ok != (reason == "") || got != reason {
			t.Errorf("ValidBackupFolder(%q) = %q, %v; want %q, %v", folder, got, ok, reason, reason == "")
		}
	}
}

// TestBackupReasonMapsErrors: the tests run as root, so a folder root
// cannot read is made up here.
func TestBackupReasonMapsErrors(t *testing.T) {
	for err, want := range map[error]string{
		&fs.PathError{Op: "open", Path: "/srv/b", Err: syscall.EACCES}:  proto.BackupReasonDenied,
		&fs.PathError{Op: "open", Path: "/srv/b", Err: syscall.EPERM}:   proto.BackupReasonDenied,
		&fs.PathError{Op: "lstat", Path: "/srv/b", Err: syscall.ENOENT}: proto.BackupReasonMissing,
		&fs.PathError{Op: "open", Path: "/srv/b", Err: syscall.ENOTDIR}: proto.BackupReasonNotDir,
		&fs.PathError{Op: "readdirent", Path: ".", Err: syscall.EIO}:    proto.BackupReasonError,
		errors.New("something else"):                                    proto.BackupReasonError,
	} {
		if got := backupReason(err); got != want {
			t.Errorf("backupReason(%v) = %q, want %q", err, got, want)
		}
	}
}

// fakeScan stands in for scanBackup: it records the folders it was asked
// for and, while gate is set, waits for it before answering.
type fakeScan struct {
	mu    sync.Mutex
	calls []string
	gate  chan struct{}
}

func (f *fakeScan) scan(folder string, now func() time.Time) backupResult {
	f.mu.Lock()
	f.calls = append(f.calls, folder)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	t := now()
	return backupResult{status: proto.BackupOK, newest: t.Add(-2 * time.Hour), size: 42, at: t}
}

func (f *fakeScan) folders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeScan) setGate(g chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = g
}

// newBackupRig is newRig whose monitor scans with a fakeScan and bounds
// the wait for a scan with timers the test fires; every bound asked for
// arrives on the returned channel.
func newBackupRig(t *testing.T) (*rig, *fakeScan, chan wait) {
	t.Helper()
	r := newRig(t, base())
	fsc := &fakeScan{}
	timers := make(chan wait, 64)
	r.m.mu.Lock()
	r.m.scan = fsc.scan
	r.m.scanAfter = func(d time.Duration) <-chan time.Time {
		w := wait{d: d, ch: make(chan time.Time, 1)}
		timers <- w
		return w.ch
	}
	r.m.mu.Unlock()
	return r, fsc, timers
}

// configureBackup sends monitor.configure naming folder (summaries an hour
// apart, so only changes are reported), waits for the loop to pause again
// and returns the answer's backup state.
func (r *rig) configureBackup(folder string) *proto.BackupState {
	r.t.Helper()
	ev := resultEvent(r.t, r.dispatch("monitor.configure",
		proto.MonitorConfigureParams{CheckEverySec: 15, SummaryEverySec: 3600, BackupFolder: folder}))
	r.idle()
	return ev.Snapshot.Backup
}

// lastBackup is the backup state of the last report sent.
func (r *rig) lastBackup() *proto.BackupState {
	r.t.Helper()
	got := r.em.got()
	return got[len(got)-1].Snapshot.Backup
}

func wantBackup(t *testing.T, what string, got *proto.BackupState, want proto.BackupState) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s: backup = %+v, want %+v", what, got, want)
	}
}

func TestBackupScanOneAtATime(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) }) // after the rig has stopped
	r, fsc, timers := newBackupRig(t)
	fsc.setGate(stuck)
	wantBackup(t, "configure", r.configureBackup("/srv/a"), proto.BackupState{Status: proto.BackupPending})

	// One round by hand: the scan starts and hangs; the loop gives up
	// after 10 s of the fake clock.
	r.clock.advance(15 * time.Second)
	r.cur.ch <- r.clock.Now()
	var sw wait
	select {
	case sw = <-timers:
	case <-time.After(5 * time.Second):
		t.Fatal("no scan started")
	}
	if sw.d != 10*time.Second {
		t.Fatalf("loop waits %v for a scan, want 10s", sw.d)
	}
	r.clock.advance(10 * time.Second)
	sw.ch <- r.clock.Now()
	r.idle()
	r.tick(15 * time.Second)
	wantBackup(t, "after the bound", r.lastBackup(),
		proto.BackupState{Status: proto.BackupUnreadable, Reason: proto.BackupReasonTimeout, ScanAgoSec: 15})

	// Past five minutes, and after the folder changed, no second scan
	// while the first has not returned.
	for range 24 {
		r.tick(15 * time.Second)
	}
	wantBackup(t, "new folder", r.configureBackup("/srv/b"), proto.BackupState{Status: proto.BackupPending})
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	if got := fsc.folders(); len(got) != 1 {
		t.Fatalf("scans = %q, want one while it hangs", got)
	}
	if len(timers) != 0 {
		t.Fatalf("%d more scan bounds asked for, want none", len(timers))
	}
	if n := r.logs.count("backup folder scan still running"); n < 1 || n > 8 {
		t.Errorf("still-running warning logged %d times over 7 minutes, want 1..8", n)
	}

	// The hung scan returns: the next round scans the new folder.
	fsc.setGate(nil)
	stuck <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.m.bmu.Lock()
		running := r.m.bk.running
		r.m.bmu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan goroutine did not return")
		}
		time.Sleep(time.Millisecond)
	}
	r.tick(15 * time.Second)
	if got := fsc.folders(); len(got) != 2 || got[1] != "/srv/b" {
		t.Fatalf("scans = %q, want /srv/b scanned once the first returned", got)
	}
	r.tick(15 * time.Second)
	wantBackup(t, "new folder scanned", r.lastBackup(),
		proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*3600 + 15, NewestSize: 42, ScanAgoSec: 15})

	// A scan hangs again and the monitor stops while waiting for it: Run
	// returns at once (the rig's cleanup checks it), leaving the one scan.
	fsc.setGate(stuck)
	r.configureBackup("/srv/c")
	for len(timers) > 0 {
		<-timers
	}
	r.clock.advance(15 * time.Second)
	r.cur.ch <- r.clock.Now()
	select {
	case <-timers:
	case <-time.After(5 * time.Second):
		t.Fatal("no scan of /srv/c started")
	}
}

func TestBackupScanEveryFiveMinutes(t *testing.T) {
	r, fsc, _ := newBackupRig(t)
	r.configureBackup("/srv/a")
	r.tick(15 * time.Second) // scans
	r.tick(15 * time.Second) // reports the result
	reports := len(r.em.got())
	wantBackup(t, "first scan", r.lastBackup(),
		proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*3600 + 15, NewestSize: 42, ScanAgoSec: 15})
	for range 18 { // up to 4 min 45 s after the scan
		r.tick(15 * time.Second)
	}
	if got := fsc.folders(); len(got) != 1 {
		t.Fatalf("scans = %q within five minutes, want 1", got)
	}
	r.tick(15 * time.Second) // five minutes after the first scan
	if got := fsc.folders(); len(got) != 2 {
		t.Fatalf("scans = %q at five minutes, want 2", got)
	}
	r.tick(15 * time.Second)
	if n := len(r.em.got()); n != reports {
		t.Fatalf("%d reports, want %d: a new age alone is no change", n, reports)
	}
	// The same folder again keeps its result and its schedule.
	wantBackup(t, "same folder", r.configureBackup("/srv/a"),
		proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*3600 + 15, NewestSize: 42, ScanAgoSec: 15})
	r.tick(15 * time.Second)
	if got := fsc.folders(); len(got) != 2 {
		t.Fatalf("scans = %q after the same folder, want 2", got)
	}
}

func TestFolderChangeReportsPendingThenResult(t *testing.T) {
	r, fsc, _ := newBackupRig(t)
	wantBackup(t, "configure a", r.configureBackup("/srv/a"), proto.BackupState{Status: proto.BackupPending})
	r.tick(15 * time.Second)
	n := len(r.em.got())
	r.tick(15 * time.Second)
	r.wantReports(n + 1)
	wantBackup(t, "a scanned", r.lastBackup(),
		proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*3600 + 15, NewestSize: 42, ScanAgoSec: 15})

	wantBackup(t, "configure b", r.configureBackup("/srv/b"), proto.BackupState{Status: proto.BackupPending})
	r.tick(15 * time.Second) // scans b; pending was already answered
	r.wantReports(n + 1)
	r.tick(15 * time.Second)
	r.wantReports(n + 2)
	wantBackup(t, "b scanned", r.lastBackup(),
		proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 2*3600 + 15, NewestSize: 42, ScanAgoSec: 15})
	if got := fsc.folders(); len(got) != 2 || got[0] != "/srv/a" || got[1] != "/srv/b" {
		t.Fatalf("scans = %q, want /srv/a then /srv/b", got)
	}
}

func TestConfigureKeepsSettingsWithBadFolder(t *testing.T) {
	r, fsc, _ := newBackupRig(t)
	resp := r.dispatch("monitor.configure", proto.MonitorConfigureParams{CheckEverySec: 30, SummaryEverySec: 600, BackupFolder: "/home/x"})
	ev := resultEvent(t, resp)
	wantBackup(t, "configure", ev.Snapshot.Backup, proto.BackupState{Status: proto.BackupUnreadable, Reason: proto.BackupReasonHidden})
	r.idle()
	if r.cur.d != 30*time.Second {
		t.Fatalf("loop pauses %v, want the 30s asked for", r.cur.d)
	}
	if o := r.m.options(); o.SummaryEvery != 600*time.Second {
		t.Fatalf("options = %+v, want summaries every 600s", o)
	}
	r.tick(30 * time.Second)
	r.tick(30 * time.Second)
	if got := fsc.folders(); len(got) != 0 {
		t.Fatalf("scans = %q, want none of a refused folder", got)
	}
}

func TestOldParamsWithoutFolderTurnScanOff(t *testing.T) {
	r, _, _ := newBackupRig(t)
	wantBackup(t, "configure", r.configureBackup("/srv/a"), proto.BackupState{Status: proto.BackupPending})
	r.tick(15 * time.Second)
	r.tick(15 * time.Second)
	// An older panel: no backup_folder at all.
	ev := resultEvent(t, r.dispatch("monitor.configure", json.RawMessage(`{"check_every_sec":15,"summary_every_sec":3600}`)))
	r.idle()
	if ev.Snapshot.Backup != nil {
		t.Fatalf("backup = %+v after params without a folder, want nil", ev.Snapshot.Backup)
	}
	r.tick(15 * time.Second)
	if b := resultEvent(t, r.dispatch("monitor.snapshot", nil)).Snapshot.Backup; b != nil {
		t.Fatalf("snapshot backup = %+v, want nil", b)
	}
}
