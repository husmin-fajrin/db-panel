package agentapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// errNoBinary is BinHashes.For's answer for an architecture the panel has no
// agent binary for.
var errNoBinary = errors.New(msgNoBinary)

// BinHashes computes the sha256 of the agent binaries BinaryHandler serves,
// for the install script to check its download against. A hash is cached
// per file and recomputed when the file's size or modification time
// changes (e.g. a new release dropped into binDir).
type BinHashes struct {
	binDir string
	mu     sync.Mutex
	cache  map[string]binHash // by path
}

type binHash struct {
	size  int64
	mtime time.Time
	sum   string
}

// NewBinHashes hashes the binaries in binDir (see BinaryHandler).
func NewBinHashes(binDir string) *BinHashes {
	return &BinHashes{binDir: binDir, cache: map[string]binHash{}}
}

// For returns the sha256 (lowercase hex) of the binary BinaryHandler serves
// for arch: binDir/dbpanel_linux_<arch>, else the panel's own executable
// when it is a linux build for arch. It fails with "binary untuk arsitektur
// ini belum tersedia di panel" when there is none.
func (b *BinHashes) For(arch string) (string, error) {
	if !agentArches[arch] {
		return "", errNoBinary
	}
	f, err := openAgentBinary(b.binDir, arch)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoBinary
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	path := f.Name()

	b.mu.Lock()
	c, ok := b.cache[path]
	b.mu.Unlock()
	if ok && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c.sum, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	b.mu.Lock()
	b.cache[path] = binHash{size: fi.Size(), mtime: fi.ModTime(), sum: sum}
	b.mu.Unlock()
	return sum, nil
}
