package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrTokenInvalid is returned for unknown, used or expired pairing tokens.
var ErrTokenInvalid = errors.New("token tidak valid atau kedaluwarsa")

// PairToken is a one-time token an agent presents to pair with the panel.
type PairToken struct {
	Token     string
	ServerID  int64
	ExpiresAt time.Time
	UsedAt    *time.Time
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
	if _, err := tx.Exec(`INSERT INTO pair_tokens (token, server_id, expires_at) VALUES (?, ?, ?)`,
		pt.Token, serverID, formatTime(pt.ExpiresAt)); err != nil {
		return PairToken{}, fmt.Errorf("insert token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PairToken{}, fmt.Errorf("commit: %w", err)
	}
	return pt, nil
}

func scanToken(r rowScanner) (PairToken, error) {
	var (
		pt      PairToken
		expires string
		used    sql.NullString
	)
	if err := r.Scan(&pt.Token, &pt.ServerID, &expires, &used); err != nil {
		return PairToken{}, err
	}
	var err error
	if pt.ExpiresAt, err = parseTime(expires); err != nil {
		return PairToken{}, err
	}
	if pt.UsedAt, err = parseNullTime(used); err != nil {
		return PairToken{}, err
	}
	return pt, nil
}

const tokenColumns = `token, server_id, expires_at, used_at`

func (s *Store) getToken(token string) (PairToken, error) {
	pt, err := scanToken(s.db.QueryRow(`SELECT `+tokenColumns+` FROM pair_tokens WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return PairToken{}, ErrTokenInvalid
	}
	if err != nil {
		return PairToken{}, fmt.Errorf("get token: %w", err)
	}
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
	res, err := s.db.Exec(`UPDATE pair_tokens SET used_at = ? WHERE token = ? AND used_at IS NULL AND expires_at > ?`,
		formatTime(now), token, formatTime(now))
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
	return s.getToken(token)
}

// ActivePairToken returns the newest unused, unexpired token for a server.
func (s *Store) ActivePairToken(serverID int64, now time.Time) (PairToken, bool, error) {
	pt, err := scanToken(s.db.QueryRow(`SELECT `+tokenColumns+` FROM pair_tokens
		WHERE server_id = ? AND used_at IS NULL AND expires_at > ?
		ORDER BY expires_at DESC, rowid DESC LIMIT 1`, serverID, formatTime(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return PairToken{}, false, nil
	}
	if err != nil {
		return PairToken{}, false, fmt.Errorf("active token: %w", err)
	}
	return pt, true, nil
}
