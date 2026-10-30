package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrTokenInvalid is returned for unknown, used or expired pairing tokens.
var ErrTokenInvalid = errors.New("token tidak valid atau kedaluwarsa")

// PairToken is a one-time token an agent presents to pair with the panel.
// The database stores only the token's SHA-256; Token is the plaintext when
// it is known (the caller supplied it, or this process created it), else "".
type PairToken struct {
	Token     string
	ServerID  int64
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// hashToken returns the hex SHA-256 of a token. Tokens are 75 random bits,
// so an unsalted hash is enough to keep them unusable from a copy of the
// database.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// remember keeps the plaintext of the token just created for serverID.
func (s *Store) remember(serverID int64, hash, token string) {
	s.plainMu.Lock()
	defer s.plainMu.Unlock()
	s.plain[serverID] = plainToken{hash: hash, token: token}
}

// forget drops the remembered plaintext token of serverID.
func (s *Store) forget(serverID int64) {
	s.plainMu.Lock()
	defer s.plainMu.Unlock()
	delete(s.plain, serverID)
}

// plainFor returns the remembered plaintext of the token with hash, or "".
func (s *Store) plainFor(serverID int64, hash string) string {
	s.plainMu.Lock()
	defer s.plainMu.Unlock()
	if p, ok := s.plain[serverID]; ok && p.hash == hash {
		return p.token
	}
	return ""
}

// Crockford base32 alphabet (no I, L, O, U).
const tokenAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	tokenGroups    = 3
	tokenGroupSize = 5
)

func newTokenString() (string, error) {
	n := tokenGroups * tokenGroupSize
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	out := make([]byte, 0, n+tokenGroups-1)
	for i, b := range buf {
		if i > 0 && i%tokenGroupSize == 0 {
			out = append(out, '-')
		}
		// 32 divides 256, so the modulo introduces no bias.
		out = append(out, tokenAlphabet[int(b)%len(tokenAlphabet)])
	}
	return string(out), nil
}

// CreatePairToken issues a new token for the server, invalidating any
// earlier unused tokens for it.
func (s *Store) CreatePairToken(serverID int64, now time.Time, ttl time.Duration) (PairToken, error) {
	token, err := newTokenString()
	if err != nil {
		return PairToken{}, err
	}
	pt := PairToken{Token: token, ServerID: serverID, ExpiresAt: now.Add(ttl).UTC().Truncate(time.Second)}
	hash := hashToken(token)

	tx, err := s.db.Begin()
	if err != nil {
		return PairToken{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM servers WHERE id = ?`, serverID).Scan(&exists); err != nil {
		return PairToken{}, fmt.Errorf("check server: %w", err)
	}
	if exists == 0 {
		return PairToken{}, ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM pair_tokens WHERE server_id = ? AND used_at IS NULL`, serverID); err != nil {
		return PairToken{}, fmt.Errorf("delete old tokens: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO pair_tokens (token_hash, server_id, expires_at) VALUES (?, ?, ?)`,
		hash, serverID, formatTime(pt.ExpiresAt)); err != nil {
		return PairToken{}, fmt.Errorf("insert token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PairToken{}, fmt.Errorf("commit: %w", err)
	}
	s.remember(serverID, hash, token)
	return pt, nil
}

// scanToken reads a token row; the plaintext is not stored, so Token is
// left empty and the hash is returned instead.
func scanToken(r rowScanner) (PairToken, string, error) {
	var (
		pt      PairToken
		hash    string
		expires string
		used    sql.NullString
	)
	if err := r.Scan(&hash, &pt.ServerID, &expires, &used); err != nil {
		return PairToken{}, "", err
	}
	var err error
	if pt.ExpiresAt, err = parseTime(expires); err != nil {
		return PairToken{}, "", err
	}
	if pt.UsedAt, err = parseNullTime(used); err != nil {
		return PairToken{}, "", err
	}
	return pt, hash, nil
}

const tokenColumns = `token_hash, server_id, expires_at, used_at`

// getToken looks token up by its hash.
func (s *Store) getToken(token string) (PairToken, error) {
	pt, _, err := scanToken(s.db.QueryRow(`SELECT `+tokenColumns+` FROM pair_tokens WHERE token_hash = ?`, hashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return PairToken{}, ErrTokenInvalid
	}
	if err != nil {
		return PairToken{}, fmt.Errorf("get token: %w", err)
	}
	pt.Token = token
	return pt, nil
}

// PeekPairToken returns the token if it is valid and unused, without
// consuming it.
func (s *Store) PeekPairToken(token string, now time.Time) (PairToken, error) {
	pt, err := s.getToken(token)
	if err != nil {
		return PairToken{}, err
	}
	if pt.UsedAt != nil || !pt.ExpiresAt.After(now) {
		return PairToken{}, ErrTokenInvalid
	}
	return pt, nil
}

// ConsumePairToken atomically marks the token used. It returns
// ErrTokenInvalid if the token is unknown, already used or expired.
func (s *Store) ConsumePairToken(token string, now time.Time) (PairToken, error) {
	res, err := s.db.Exec(`UPDATE pair_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		formatTime(now), hashToken(token), formatTime(now))
	if err != nil {
		return PairToken{}, fmt.Errorf("consume token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return PairToken{}, fmt.Errorf("consume token: %w", err)
	}
	if n == 0 {
		return PairToken{}, ErrTokenInvalid
	}
	pt, err := s.getToken(token)
	if err == nil {
		s.forget(pt.ServerID)
	}
	return pt, err
}

// ActivePairToken returns the newest unused, unexpired token for a server.
// Its Token is "" when this process did not create it (e.g. the panel was
// restarted since): the plaintext is not stored and cannot be shown again.
func (s *Store) ActivePairToken(serverID int64, now time.Time) (PairToken, bool, error) {
	pt, hash, err := scanToken(s.db.QueryRow(`SELECT `+tokenColumns+` FROM pair_tokens
		WHERE server_id = ? AND used_at IS NULL AND expires_at > ?
		ORDER BY expires_at DESC, rowid DESC LIMIT 1`, serverID, formatTime(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return PairToken{}, false, nil
	}
	if err != nil {
		return PairToken{}, false, fmt.Errorf("active token: %w", err)
	}
	pt.Token = s.plainFor(serverID, hash)
	return pt, true, nil
}
