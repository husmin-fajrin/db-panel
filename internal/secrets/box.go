// Package secrets seals small secrets (database passwords and the like) at
// rest with a host-local key, for both the panel and the agent. The key is a
// 32-byte file readable by its owner only; ciphertexts are AES-256-GCM with
// the 12-byte nonce in front.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	keySize   = 32
	nonceSize = 12
	keyMode   = 0o600
)

// Box seals and opens secrets with one key.
type Box struct {
	key  [keySize]byte
	aead cipher.AEAD
}

// LoadOrCreate reads the key at path, or creates it (32 random bytes, mode
// 0600, exclusive create) when the file does not exist. A key file that
// group or others may access, or that is not exactly 32 bytes, is refused.
func LoadOrCreate(path string) (*Box, error) {
	key, err := readKey(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err = createKey(path)
		if errors.Is(err, os.ErrExist) { // created concurrently: use that one
			key, err = readKey(path)
		}
	}
	if err != nil {
		return nil, err
	}
	return newBox(key)
}

func readKey(path string) ([keySize]byte, error) {
	var key [keySize]byte
	f, err := os.Open(path)
	if err != nil {
		return key, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return key, fmt.Errorf("secrets: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return key, fmt.Errorf("secrets: %s bukan berkas biasa", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return key, fmt.Errorf("secrets: izin %s terlalu longgar (%04o); harus 0600", path, perm)
	}
	if fi.Size() != keySize {
		return key, fmt.Errorf("secrets: %s harus %d byte, ternyata %d", path, keySize, fi.Size())
	}
	if _, err := io.ReadFull(f, key[:]); err != nil {
		return key, fmt.Errorf("secrets: baca %s: %w", path, err)
	}
	return key, nil
}

func createKey(path string) ([keySize]byte, error) {
	var key [keySize]byte
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return key, fmt.Errorf("secrets: buat direktori: %w", err)
	}
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("secrets: kunci acak: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyMode)
	if err != nil {
		return key, err
	}
	// The file is ours (O_EXCL): on failure remove it rather than leave a
	// partial key that would be refused on every later start.
	if _, err := f.Write(key[:]); err != nil {
		f.Close()
		os.Remove(path)
		return key, fmt.Errorf("secrets: tulis %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return key, fmt.Errorf("secrets: tulis %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return key, fmt.Errorf("secrets: tulis %s: %w", path, err)
	}
	return key, nil
}

func newBox(key [keySize]byte) (*Box, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return &Box{key: key, aead: aead}, nil
}

// Seal encrypts plain and returns nonce || ciphertext || tag.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	out := make([]byte, nonceSize, nonceSize+len(plain)+b.aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, fmt.Errorf("secrets: nonce acak: %w", err)
	}
	return b.aead.Seal(out, out, plain, nil), nil
}

// ErrOpen is Open's error for a ciphertext that is truncated, was altered,
// or was sealed with another key.
var ErrOpen = errors.New("secrets: data terenkripsi rusak atau kunci berbeda")

// Open decrypts a ciphertext made by Seal with the same key.
func (b *Box) Open(ct []byte) ([]byte, error) {
	if len(ct) < nonceSize+b.aead.Overhead() {
		return nil, ErrOpen
	}
	plain, err := b.aead.Open(nil, ct[:nonceSize], ct[nonceSize:], nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
