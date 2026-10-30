package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type session struct {
	username string
	created  time.Time
	expires  time.Time
}

// Sessions is an in-memory session store with sliding expiry, an optional
// absolute lifetime and an optional validity check. It is safe for
// concurrent use.
type Sessions struct {
	mu     sync.Mutex
	m      map[string]session
	ttl    time.Duration
	maxAge time.Duration // 0 means no absolute lifetime
	valid  func(created time.Time) bool
	now    func() time.Time
}

// SessionOption configures NewSessions.
type SessionOption func(*Sessions)

// WithMaxAge ends every session d after it was created, however active it
// is (absolute lifetime, in addition to the sliding ttl).
func WithMaxAge(d time.Duration) SessionOption {
	return func(s *Sessions) { s.maxAge = d }
}

// WithValidator makes Get reject (and delete) sessions for which valid
// returns false, e.g. sessions created before the last password reset.
// valid runs on every Get with the session's creation time and must be
// cheap; it is called with the store's lock held.
func WithValidator(valid func(created time.Time) bool) SessionOption {
	return func(s *Sessions) { s.valid = valid }
}

// NewSessions returns a store whose sessions live ttl past their last use.
func NewSessions(ttl time.Duration, now func() time.Time, opts ...SessionOption) *Sessions {
	s := &Sessions{m: make(map[string]session), ttl: ttl, now: now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create starts a session for username and returns its token (32 random bytes,
// hex encoded).
func (s *Sessions) Create(username string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	token := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	now := s.now()
	s.m[token] = session{username: username, created: now, expires: now.Add(s.ttl)}
	return token
}

// Get returns the user for token and extends the session to now+ttl (never
// past its absolute lifetime). Unknown, expired and invalidated tokens
// return ok=false; expired and invalidated ones are removed.
func (s *Sessions) Get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return "", false
	}
	now := s.now()
	if s.expired(sess, now) || (s.valid != nil && !s.valid(sess.created)) {
		delete(s.m, token)
		return "", false
	}
	sess.expires = now.Add(s.ttl)
	s.m[token] = sess
	return sess.username, true
}

// Delete ends the session for token.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

func (s *Sessions) expired(sess session, now time.Time) bool {
	if !now.Before(sess.expires) {
		return true
	}
	return s.maxAge > 0 && !now.Before(sess.created.Add(s.maxAge))
}

// sweep removes expired sessions. Caller must hold s.mu.
func (s *Sessions) sweep() {
	now := s.now()
	for k, v := range s.m {
		if s.expired(v, now) {
			delete(s.m, k)
		}
	}
}
