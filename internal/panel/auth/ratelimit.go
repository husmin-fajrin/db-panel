package auth

import (
	"sync"
	"time"
)

type bucket struct {
	start time.Time
	count int
}

// RateLimiter allows at most max calls per key in a fixed window. It is safe
// for concurrent use.
type RateLimiter struct {
	mu     sync.Mutex
	m      map[string]bucket
	max    int
	window time.Duration
	now    func() time.Time
}

// NewRateLimiter returns a limiter allowing max attempts per window per key.
func NewRateLimiter(max int, window time.Duration, now func() time.Time) *RateLimiter {
	return &RateLimiter{m: make(map[string]bucket), max: max, window: window, now: now}
}

// Allow records an attempt for key and reports whether it is within the limit.
// The first call in a window starts it; once the window has elapsed a new one
// begins. Blocked attempts are not counted.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, w := range r.m {
		if !now.Before(w.start.Add(r.window)) {
			delete(r.m, k)
		}
	}
	w, ok := r.m[key]
	if !ok {
		w = bucket{start: now}
	}
	if w.count >= r.max {
		return false
	}
	w.count++
	r.m[key] = w
	return true
}

// Reset forgets key (called after a successful login).
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, key)
}
