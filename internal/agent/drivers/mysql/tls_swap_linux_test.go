package mysql_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
)

// sysRenameat2 is renameat2's number (the syscall package lacks it); 0 on
// an architecture this test does not know.
var sysRenameat2 = map[string]uintptr{"amd64": 316, "arm64": 276}[runtime.GOARCH]

// exchange swaps a and b atomically (renameat2 RENAME_EXCHANGE), as the
// data directory's owner can.
func exchange(a, b string) error {
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	const atFDCWD, renameExchange = -100, 2
	fd := atFDCWD
	_, _, errno := syscall.Syscall6(sysRenameat2, uintptr(fd), uintptr(unsafe.Pointer(pa)),
		uintptr(fd), uintptr(unsafe.Pointer(pb)), renameExchange, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// TestTLSDirSymlinkSwap (SEC-58): while the owner of the data directory
// keeps swapping dbpanel-tls with a symlink to another directory, the
// agent's TLS writes and chmod never reach that directory.
func TestTLSDirSymlinkSwap(t *testing.T) {
	if sysRenameat2 == 0 {
		t.Skip("renameat2 number unknown on " + runtime.GOARCH)
	}
	data := t.TempDir()
	victim := t.TempDir()
	if err := os.Chmod(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	l := mysql.Layout{DataDir: data}
	if _, err := mysql.TrustTLS(l, []byte("ca 0\n")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(data, "tukar")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	dir := mysql.TLSDir(l)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := exchange(dir, link); err != nil {
				t.Errorf("exchange: %v", err)
				return
			}
		}
	})
	for i := range 1500 {
		mysql.TrustTLS(l, fmt.Appendf(nil, "ca %d\n", i+1)) // refused or written inside: both fine
		if i%10 == 0 {
			mysql.RequestTLS(l)
		}
	}
	close(stop)
	wg.Wait()
	fi, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("victim directory mode %v, want 0755 (chmod followed the link)", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("file written through the link: %s", e.Name())
	}
}
