// Package auth provides panel authentication: password hashing, in-memory
// sessions, a per-IP login rate limiter and the RequireLogin middleware.
package auth

import (
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost     = 12
	maxPasswordLen = 72 // bcrypt ignores/rejects input beyond 72 bytes
	passwordLen    = 16
	alphabet       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// HashPassword returns a bcrypt hash (cost 12) of pw.
func HashPassword(pw string) (string, error) {
	if len(pw) > maxPasswordLen {
		return "", errors.New("kata sandi terlalu panjang")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword reports whether pw matches hash. Any error yields false.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// RandomPassword returns 16 characters from [a-zA-Z0-9] drawn from crypto/rand
// with rejection sampling (no modulo bias).
func RandomPassword() string {
	out := make([]byte, 0, passwordLen)
	// 62 symbols; accept bytes < 248 (= 62*4) so every symbol is equally likely.
	const limit = 256 - 256%len(alphabet)
	buf := make([]byte, 32)
	for len(out) < passwordLen {
		if _, err := rand.Read(buf); err != nil {
			panic("auth: crypto/rand failed: " + err.Error())
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
			if len(out) == passwordLen {
				break
			}
		}
	}
	return string(out)
}
