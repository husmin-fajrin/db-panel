package probe

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// System abstracts the host facilities Collect reads, so tests can fake them.
type System interface {
	ReadFile(path string) ([]byte, error)
	LookPath(file string) (string, error)
	// Exists reports whether path exists (for programs not on PATH).
	Exists(path string) bool
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Hostname() (string, error)
	Statfs(path string) (totalBytes, freeBytes uint64, err error)
	Arch() string
}

// RealSystem returns a System backed by the running host.
func RealSystem() System { return realSystem{} }

type realSystem struct{}

func (realSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (realSystem) LookPath(file string) (string, error) { return exec.LookPath(file) }

func (realSystem) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (realSystem) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (realSystem) Hostname() (string, error) { return os.Hostname() }

func (realSystem) Statfs(path string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Blocks) * bsize, uint64(st.Bavail) * bsize, nil
}

func (realSystem) Arch() string { return runtime.GOARCH }
