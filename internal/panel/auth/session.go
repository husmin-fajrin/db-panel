package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type session struct {
	username string
	expires  time.Time
}

// Sessions is an in-memory session store with sliding expiry. It is safe for
// concurrent use.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]session
	ttl time.Duration
	now func() time.Time
}

// NewSessions returns a store whose sessions live ttl past their last use.
func NewSessions(ttl time.Duration, now func() time.Time) *Sessions {
	return &Sessions{m: make(map[string]session), ttl: ttl, now: now}
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
	s.m[token] = session{username: username, expires: s.now().Add(s.ttl)}
	return token
}

// Get returns the user for token and extends the session to now+ttl. Unknown
// and expired tokens return ok=false; expired ones are removed.
func (s *Sessions) Get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return "", false
	}
	now := s.now()
	if !now.Before(sess.expires) {
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

// sweep removes expired sessions. Caller must hold s.mu.
func (s *Sessions) sweep() {
	now := s.now()
	for k, v := range s.m {
		if !now.Before(v.expires) {
			delete(s.m, k)
		}
	}
}
