package auth

import (
	"sync"
	"time"
)

// DefaultMaxKeys bounds how many keys a RateLimiter tracks at once, so a
// flood of distinct keys (addresses, usernames) cannot grow it without end.
const DefaultMaxKeys = 10000

type bucket struct {
	start time.Time
	count int
}

// RateLimiter allows at most max attempts per key in a fixed window, for at
// most maxKeys keys at once: when that many are tracked, expired ones are
// evicted, and if none has expired a new key is refused. It is safe for
// concurrent use.
type RateLimiter struct {
	mu        sync.Mutex
	m         map[string]bucket
	max       int
	window    time.Duration
	now       func() time.Time
	maxKeys   int
	lastSweep time.Time
}

// NewRateLimiter returns a limiter allowing max attempts per window per key,
// tracking at most DefaultMaxKeys keys.
func NewRateLimiter(max int, window time.Duration, now func() time.Time) *RateLimiter {
	return &RateLimiter{m: make(map[string]bucket), max: max, window: window, now: now, maxKeys: DefaultMaxKeys}
}

// SetMaxKeys changes how many keys are tracked at once (tests use a small
// value).
func (r *RateLimiter) SetMaxKeys(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxKeys = n
}

// Allow records an attempt for key and reports whether it is within the limit.
// The first call in a window starts it; once the window has elapsed a new one
// begins. Blocked attempts are not counted. A new key is refused while the
// limiter is full.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w, ok := r.current(key, now)
	if !ok && !r.room(now) {
		return false
	}
	if w.count >= r.max {
		return false
	}
	w.count++
	r.m[key] = w
	return true
}

// Blocked reports whether key has used up its attempts in the current
// window, or is new while the limiter is full. It records nothing; Fail
// does.
func (r *RateLimiter) Blocked(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w, ok := r.current(key, now)
	if !ok {
		return !r.room(now)
	}
	return w.count >= r.max
}

// Fail records one failed attempt for key. A new key is dropped while the
// limiter is full (Blocked already refuses such keys).
func (r *RateLimiter) Fail(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w, ok := r.current(key, now)
	if !ok && !r.room(now) {
		return
	}
	w.count++
	r.m[key] = w
}

// Reset forgets key (called after a successful login).
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, key)
}

// Len returns the number of keys tracked.
func (r *RateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

// current returns key's bucket in the window containing now, or a fresh one
// (ok=false) when the key is unknown or its window has elapsed. Caller holds
// r.mu.
func (r *RateLimiter) current(key string, now time.Time) (bucket, bool) {
	w, ok := r.m[key]
	if !ok || !now.Before(w.start.Add(r.window)) {
		delete(r.m, key)
		return bucket{start: now}, false
	}
	return w, true
}

// room reports whether a new key fits, evicting expired keys first when the
// limiter is full or a window has passed since the last sweep. Caller holds
// r.mu.
func (r *RateLimiter) room(now time.Time) bool {
	if len(r.m) >= r.maxKeys || !now.Before(r.lastSweep.Add(r.window)) {
		for k, w := range r.m {
			if !now.Before(w.start.Add(r.window)) {
				delete(r.m, k)
			}
		}
		r.lastSweep = now
	}
	return len(r.m) < r.maxKeys
}
