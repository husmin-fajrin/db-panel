package store

import (
	"errors"
	"fmt"
	"time"
)

// ErrConfirmInvalid is returned for unknown, used, expired or mismatched
// confirmation tokens.
var ErrConfirmInvalid = errors.New("konfirmasi tidak berlaku; ulangi konfirmasi")

// Confirmation is a one-time token authorising one destructive action by one
// user on one target. Only the token's hash is stored. ViaWindow marks a
// token issued without a password, inside the session's password window;
// only a light action takes it.
type Confirmation struct {
	TokenHash, Username, Action, TargetKind string
	TargetID                                int64
	ExpiresAt                               time.Time
	UsedAt                                  *time.Time
	ViaWindow                               bool
}

// AddConfirmation stores a new confirmation.
func (s *Store) AddConfirmation(c Confirmation) error {
	_, err := s.db.Exec(`INSERT INTO confirmations (token_hash, username, action, target_kind, target_id, expires_at,
		via_window) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.TokenHash, c.Username, c.Action, c.TargetKind, c.TargetID, formatTime(c.ExpiresAt), c.ViaWindow)
	if err != nil {
		return fmt.Errorf("add confirmation: %w", err)
	}
	return nil
}

// ConsumeConfirmation atomically marks the confirmation used. It returns
// ErrConfirmInvalid when it is unknown, bound to another user, action or
// target, expired (now >= ExpiresAt) or already used, and, unless
// allowWindow, for a token issued through the password window (ViaWindow).
// A refused token is left unused.
func (s *Store) ConsumeConfirmation(tokenHash, username, action, targetKind string, targetID int64, now time.Time,
	allowWindow bool) error {
	res, err := s.db.Exec(`UPDATE confirmations SET used_at = ?
		WHERE token_hash = ? AND username = ? AND action = ? AND target_kind = ? AND target_id = ?
		AND used_at IS NULL AND expires_at > ? AND (via_window = 0 OR ?)`,
		formatTime(now), tokenHash, username, action, targetKind, targetID, formatTime(now), allowWindow)
	if err != nil {
		return fmt.Errorf("consume confirmation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("consume confirmation: %w", err)
	}
	if n != 1 {
		return ErrConfirmInvalid
	}
	return nil
}

// PruneConfirmations deletes confirmations that expired before olderThan and
// returns how many it removed.
func (s *Store) PruneConfirmations(olderThan time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM confirmations WHERE expires_at < ?`, formatTime(olderThan))
	if err != nil {
		return 0, fmt.Errorf("prune confirmations: %w", err)
	}
	return res.RowsAffected()
}
