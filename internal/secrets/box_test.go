package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBoxSealOpenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() != 32 {
		t.Fatalf("key file mode %v size %d, want 0600 and 32 bytes", fi.Mode().Perm(), fi.Size())
	}

	plain := []byte("rahasia replikasi")
	ct, err := b.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext contains the plaintext")
	}
	if len(ct) != 12+len(plain)+16 {
		t.Fatalf("ciphertext length = %d, want nonce+plain+tag = %d", len(ct), 12+len(plain)+16)
	}
	ct2, err := b.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, ct2) {
		t.Fatal("two seals of the same plaintext are identical (nonce reused)")
	}

	// A second load reads the same key back.
	b2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	got, err := b2.Open(ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, want %q", got, plain)
	}

	// The empty plaintext round-trips too.
	ct, err = b.Seal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Open(ct); err != nil || len(got) != 0 {
		t.Fatalf("Open(Seal(nil)) = %q, %v", got, err)
	}
}

func TestBoxRejectsTamper(t *testing.T) {
	dir := t.TempDir()
	b, err := LoadOrCreate(filepath.Join(dir, "a.key"))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Seal([]byte("kata sandi"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		if _, err := b.Open(bad); err == nil {
			t.Fatalf("Open accepted a ciphertext with byte %d flipped", i)
		}
	}
	for _, n := range []int{0, 11, 12, 27} {
		if _, err := b.Open(ct[:n]); err == nil {
			t.Fatalf("Open accepted a ciphertext truncated to %d bytes", n)
		}
	}
	other, err := LoadOrCreate(filepath.Join(dir, "b.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(ct); err == nil {
		t.Fatal("Open with another key succeeded")
	}
}

func TestBoxRefusesLoosePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(path); err == nil {
			t.Fatalf("LoadOrCreate accepted a key file with mode %v", mode)
		}
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatalf("LoadOrCreate refused a 0400 key file: %v", err)
	}

	// A key file of the wrong size is refused, not padded or truncated.
	short := filepath.Join(t.TempDir(), "short.key")
	if err := os.WriteFile(short, make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(short); err == nil {
		t.Fatal("LoadOrCreate accepted a 16-byte key file")
	}
}
