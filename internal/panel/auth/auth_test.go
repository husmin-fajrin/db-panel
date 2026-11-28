package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a mutable clock for simulating time passing.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestHashAndCheck(t *testing.T) {
	h, err := HashPassword("rahasia-banget")
	if err != nil {
		t.Fatal(err)
	}
	if h == "rahasia-banget" || !strings.HasPrefix(h, "$2") {
		t.Fatalf("hash is not bcrypt: %q", h)
	}
	if !CheckPassword(h, "rahasia-banget") {
		t.Error("correct password rejected")
	}
	if CheckPassword(h, "salah") {
		t.Error("wrong password accepted")
	}
	if CheckPassword("bukan-hash", "rahasia-banget") {
		t.Error("invalid hash accepted")
	}
	if _, err := HashPassword(strings.Repeat("a", 73)); err == nil || err.Error() != "kata sandi terlalu panjang" {
		t.Errorf("73-byte password: err = %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 72)); err != nil {
		t.Errorf("72-byte password rejected: %v", err)
	}
}

func TestRandomPassword(t *testing.T) {
	re := regexp.MustCompile(`^[a-zA-Z0-9]{16}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := RandomPassword()
		if !re.MatchString(p) {
			t.Fatalf("bad password %q", p)
		}
		seen[p] = true
	}
	if len(seen) < 50 {
		t.Errorf("duplicates among 50 random passwords: %d unique", len(seen))
	}
}

func TestSessionsCreateToken(t *testing.T) {
	c := newClock()
	s := NewSessions(time.Hour, c.now)
	a, b := s.Create("admin"), s.Create("admin")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Errorf("token = %q", a)
	}
	if a == b {
		t.Error("tokens not unique")
	}
	if u, ok := s.Get(a); !ok || u != "admin" {
		t.Errorf("Get = %q, %v", u, ok)
	}
	if _, ok := s.Get("tidak-ada"); ok {
		t.Error("unknown token accepted")
	}
	s.Delete(a)
	if _, ok := s.Get(a); ok {
		t.Error("deleted token accepted")
	}
	if _, ok := s.Get(b); !ok {
		t.Error("other token affected by Delete")
	}
}

func TestSessionsExpire(t *testing.T) {
	c := newClock()
	s := NewSessions(time.Hour, c.now)
	tok := s.Create("admin")
	c.advance(59 * time.Minute)
	if _, ok := s.Get(tok); !ok {
		t.Fatal("session expired too early")
	}
	c.advance(61 * time.Minute)
	if _, ok := s.Get(tok); ok {
		t.Fatal("session should be expired")
	}
	s.mu.Lock()
	_, present := s.m[tok]
	s.mu.Unlock()
	if present {
		t.Error("expired session not deleted on access")
	}
}

func TestSessionsSlide(t *testing.T) {
	c := newClock()
	s := NewSessions(time.Hour, c.now)
	tok := s.Create("admin")
	for i := 0; i < 5; i++ {
		c.advance(40 * time.Minute)
		if _, ok := s.Get(tok); !ok {
			t.Fatalf("iteration %d: session expired despite activity", i)
		}
	}
	c.advance(61 * time.Minute)
	if _, ok := s.Get(tok); ok {
		t.Fatal("session should expire after inactivity")
	}
}

func TestSessionsSweepOnCreate(t *testing.T) {
	c := newClock()
	s := NewSessions(time.Hour, c.now)
	for i := 0; i < 10; i++ {
		s.Create("admin")
	}
	c.advance(2 * time.Hour)
	live := s.Create("admin")
	s.mu.Lock()
	n := len(s.m)
	s.mu.Unlock()
	if n != 1 {
		t.Errorf("sessions after sweep = %d, want 1", n)
	}
	if _, ok := s.Get(live); !ok {
		t.Error("live session swept")
	}
}

func TestSessionsConcurrent(t *testing.T) {
	s := NewSessions(time.Hour, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tok := s.Create("admin")
				s.Get(tok)
				s.Delete(tok)
			}
		}()
	}
	wg.Wait()
}

func TestRateLimiterBlocksAfterMax(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(5, 15*time.Minute, c.now)
	for i := 1; i <= 5; i++ {
		if !r.Allow("1.2.3.4") {
			t.Fatalf("attempt %d blocked", i)
		}
	}
	if r.Allow("1.2.3.4") {
		t.Fatal("attempt 6 should be blocked")
	}
	if !r.Allow("5.6.7.8") {
		t.Error("other key affected")
	}
	c.advance(14 * time.Minute)
	if r.Allow("1.2.3.4") {
		t.Error("still inside window, should be blocked")
	}
	c.advance(2 * time.Minute)
	if !r.Allow("1.2.3.4") {
		t.Error("window elapsed, should be allowed")
	}
}

func TestRateLimiterReset(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(2, time.Minute, c.now)
	r.Allow("ip")
	r.Allow("ip")
	if r.Allow("ip") {
		t.Fatal("should be blocked")
	}
	r.Reset("ip")
	if !r.Allow("ip") {
		t.Error("Reset did not clear the counter")
	}
}

func TestRateLimiterRefund(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(2, time.Minute, c.now)
	r.Allow("u")
	r.Allow("u")
	r.Refund("u")
	if !r.Allow("u") {
		t.Fatal("Refund did not give back a slot")
	}
	if r.Allow("u") {
		t.Fatal("Refund gave back more than one slot")
	}
	r.Refund("baru") // unknown key: no-op
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

func TestRateLimiterSweepsExpired(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(2, time.Minute, c.now)
	for _, k := range []string{"a", "b", "c"} {
		r.Allow(k)
	}
	c.advance(2 * time.Minute)
	r.Allow("d")
	r.mu.Lock()
	n := len(r.m)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("keys after sweep = %d, want 1", n)
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	r := NewRateLimiter(100, time.Minute, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.Allow("ip")
				if j%10 == 0 {
					r.Reset("ip")
				}
			}
		}()
	}
	wg.Wait()
}

func okHandler(got *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = UsernameFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireLoginRedirects(t *testing.T) {
	s := NewSessions(time.Hour, time.Now)
	var got string
	h := RequireLogin(s, "/login")(okHandler(&got))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/servers", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fservers" {
		t.Errorf("Location = %q", loc)
	}

	// invalid cookie also redirects, and the query string is preserved in next.
	req := httptest.NewRequest("GET", "/servers?x=1&y=2", nil)
	req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: "palsu"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fservers%3Fx%3D1%26y%3D2" {
		t.Errorf("Location = %q", loc)
	}
	if got != "" {
		t.Error("next handler was called")
	}
}

func TestRequireLoginPassesWithCookie(t *testing.T) {
	s := NewSessions(time.Hour, time.Now)
	tok := s.Create("admin")
	var got string
	h := RequireLogin(s, "/login")(okHandler(&got))

	req := httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: tok})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	// The plain (non-Secure) cookie of the HTTP days is no longer read.
	req = httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("plain cookie: code = %d, want 303 to login", rec.Code)
	}
	if got != "admin" {
		t.Errorf("username in context = %q", got)
	}
}

func TestUsernameFromEmpty(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if u := UsernameFrom(req.Context()); u != "" {
		t.Errorf("got %q", u)
	}
}

func TestCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok")
	c := rec.Result().Cookies()[0]
	if c.Name != SecureCookieName || c.Value != "tok" || c.Path != "/" || !c.HttpOnly || !c.Secure ||
		c.SameSite != http.SameSiteLaxMode || c.Domain != "" {
		t.Errorf("cookie = %+v", c)
	}

	// Logout expires the "__Host-" cookie and the plain one a browser may
	// still hold from before HTTPS.
	rec = httptest.NewRecorder()
	ClearSessionCookie(rec)
	cs := rec.Result().Cookies()
	if len(cs) != 2 {
		t.Fatalf("clear sets %d cookies, want 2", len(cs))
	}
	names := map[string]bool{}
	for _, c := range cs {
		names[c.Name] = true
		if c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly {
			t.Errorf("clear cookie = %+v", c)
		}
	}
	if !names[SecureCookieName] || !names[CookieName] {
		t.Errorf("cleared %v", names)
	}
}

// TestSessionsAbsoluteLifetime (SEC-04): activity slides the idle expiry but
// never past the absolute lifetime.
func TestSessionsAbsoluteLifetime(t *testing.T) {
	c := newClock()
	s := NewSessions(12*time.Hour, c.now, WithMaxAge(24*time.Hour))
	tok := s.Create("admin")
	for elapsed := time.Duration(0); elapsed < 23*time.Hour; elapsed += time.Hour {
		c.advance(time.Hour)
		if _, ok := s.Get(tok); !ok {
			t.Fatalf("session ended after %v of activity, want it alive until 24h", elapsed+time.Hour)
		}
	}
	c.advance(time.Hour) // 24h since login
	if _, ok := s.Get(tok); ok {
		t.Fatal("session still valid 24h after login")
	}
	s.mu.Lock()
	_, present := s.m[tok]
	s.mu.Unlock()
	if present {
		t.Error("session past its absolute lifetime not deleted")
	}
}

// TestSessionsValidator (SEC-04): sessions the validator rejects (created
// before a password reset) are refused and deleted.
func TestSessionsValidator(t *testing.T) {
	c := newClock()
	var epoch time.Time
	s := NewSessions(time.Hour, c.now, WithValidator(func(created time.Time) bool {
		return created.After(epoch)
	}))
	old := s.Create("admin")
	if _, ok := s.Get(old); !ok {
		t.Fatal("session rejected before any reset")
	}
	c.advance(time.Minute)
	epoch = c.now() // password reset
	c.advance(time.Second)
	fresh := s.Create("admin")
	if _, ok := s.Get(old); ok {
		t.Fatal("session created before the reset still accepted")
	}
	if _, ok := s.Get(fresh); !ok {
		t.Fatal("session created after the reset rejected")
	}
	s.mu.Lock()
	_, present := s.m[old]
	s.mu.Unlock()
	if present {
		t.Error("invalidated session not deleted")
	}
}

// TestRateLimiterBounded (R1): the number of tracked keys never exceeds
// maxKeys; expired keys make room, and new keys are refused while full.
func TestRateLimiterBounded(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(5, time.Minute, c.now)
	r.SetMaxKeys(100)
	for i := 0; i < 1000; i++ {
		r.Allow(fmt.Sprintf("k%d", i))
		r.Fail(fmt.Sprintf("f%d", i))
	}
	if n := r.Len(); n != 100 {
		t.Fatalf("keys = %d, want 100 (the cap)", n)
	}
	if r.Allow("baru") || !r.Blocked("baru") {
		t.Fatal("new key accepted while full")
	}
	if !r.Allow("k0") {
		t.Fatal("known key refused while full")
	}
	c.advance(2 * time.Minute)
	if !r.Allow("baru") || r.Len() != 1 {
		t.Fatalf("after expiry: new key refused or not evicted (keys %d)", r.Len())
	}
	if DefaultMaxKeys != 10000 {
		t.Fatalf("DefaultMaxKeys = %d", DefaultMaxKeys)
	}
}

// TestRateLimiterBlockedAndFail: Blocked records nothing; Fail counts.
func TestRateLimiterBlockedAndFail(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(3, time.Minute, c.now)
	for i := 0; i < 10; i++ {
		if r.Blocked("u") {
			t.Fatal("Blocked counted attempts")
		}
	}
	if r.Len() != 0 {
		t.Fatalf("Blocked created %d keys", r.Len())
	}
	for i := 0; i < 3; i++ {
		r.Fail("u")
	}
	if !r.Blocked("u") {
		t.Fatal("not blocked after 3 failures")
	}
	c.advance(time.Minute)
	if r.Blocked("u") {
		t.Fatal("still blocked after the window")
	}
}

// TestRateLimiterRetryAfter: the time left until a blocked key is
// accepted again; 0 for a key that is not blocked.
func TestRateLimiterRetryAfter(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(5, time.Minute, c.now)
	for range 5 {
		r.Allow("k")
	}
	c.advance(20 * time.Second)
	if got := r.RetryAfter("k"); got != 40*time.Second {
		t.Errorf("RetryAfter = %v, want 40s", got)
	}
	if got := r.RetryAfter("lain"); got != 0 {
		t.Errorf("RetryAfter of an unknown key = %v, want 0", got)
	}
	r.Allow("sebagian")
	if got := r.RetryAfter("sebagian"); got != 0 {
		t.Errorf("RetryAfter of a key with room left = %v, want 0", got)
	}
	c.advance(40 * time.Second)
	if got := r.RetryAfter("k"); got != 0 {
		t.Errorf("RetryAfter after the window = %v, want 0", got)
	}
}

// TestRateLimiterClockSetBack (K8): a window whose start lies after now
// (the clock was set back) has ended; it does not hold the key for longer.
func TestRateLimiterClockSetBack(t *testing.T) {
	c := newClock()
	r := NewRateLimiter(5, time.Minute, c.now)
	for range 5 {
		r.Allow("k")
	}
	if r.Allow("k") {
		t.Fatal("full bucket allowed")
	}
	c.advance(-time.Hour)
	if r.Blocked("k") || r.RetryAfter("k") != 0 {
		t.Error("key still blocked after the clock went back")
	}
	if !r.Allow("k") {
		t.Error("Allow refused after the clock went back")
	}
}

// TestTokenFrom: RequireLogin puts the accepted session token in the
// context.
func TestTokenFrom(t *testing.T) {
	s := NewSessions(time.Hour, time.Now)
	tok := s.Create("admin")
	var got string
	h := RequireLogin(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = TokenFrom(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: tok})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != tok {
		t.Errorf("TokenFrom = %q, want the session token", got)
	}
	if v := TokenFrom(httptest.NewRequest("GET", "/", nil).Context()); v != "" {
		t.Errorf("TokenFrom without login = %q", v)
	}
}

var deviceKey = []byte("0123456789abcdef0123456789abcdef")

func TestDeviceCookieRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	v, d := NewDevice(deviceKey, "admin", now)
	if d.Username != "admin" || !d.Issued.Equal(now) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(d.ID) {
		t.Fatalf("device = %+v", d)
	}
	if !regexp.MustCompile(`^v1\.[A-Za-z0-9_-]+\.[0-9a-f]{32}\.[0-9]+\.[A-Za-z0-9_-]+$`).MatchString(v) {
		t.Fatalf("value = %q", v)
	}
	got, ok := ParseDevice(deviceKey, v, now.Add(364*24*time.Hour))
	if !ok || got != d {
		t.Fatalf("ParseDevice = %+v, %v; want %+v", got, ok, d)
	}
	// K8: a cookie issued "in the future" stays valid.
	if _, ok := ParseDevice(deviceKey, v, now.Add(-time.Hour)); !ok {
		t.Error("cookie refused after the clock went back")
	}
	_, d2 := NewDevice(deviceKey, "admin", now)
	if d2.ID == d.ID {
		t.Error("two devices got the same ID")
	}

	rec := httptest.NewRecorder()
	SetDeviceCookie(rec, v)
	c := rec.Result().Cookies()[0]
	if c.Name != DeviceCookieName || c.Value != v || c.Path != "/" || !c.HttpOnly || !c.Secure ||
		c.SameSite != http.SameSiteLaxMode || c.Domain != "" || c.MaxAge != 31536000 {
		t.Errorf("cookie = %+v", c)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	if got, ok := DeviceCookie(req); !ok || got != v {
		t.Errorf("DeviceCookie = %q, %v", got, ok)
	}
	if _, ok := DeviceCookie(httptest.NewRequest("GET", "/", nil)); ok {
		t.Error("DeviceCookie found one in a bare request")
	}
}

func TestDeviceCookieRejected(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	v, _ := NewDevice(deviceKey, "admin", now)
	parts := strings.Split(v, ".")
	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}
	join := func(p ...string) string { return strings.Join(p, ".") }
	other, _ := NewDevice(deviceKey, "operator", now)
	op := strings.Split(other, ".")
	for name, val := range map[string]string{
		"signature changed": join(parts[0], parts[1], parts[2], parts[3], flip(parts[4])),
		"username swapped":  join(parts[0], op[1], parts[2], parts[3], parts[4]),
		"issued changed":    join(parts[0], parts[1], parts[2], "1", parts[4]),
		"part missing":      join(parts[0], parts[1], parts[2], parts[3]),
		"extra part":        v + ".x",
		"other version":     join("v2", parts[1], parts[2], parts[3], parts[4]),
		"empty":             "",
		"not hex id":        join(parts[0], parts[1], strings.Repeat("z", 32), parts[3], parts[4]),
	} {
		if _, ok := ParseDevice(deviceKey, val, now); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, ok := ParseDevice([]byte("another key, another key, anothe"), v, now); ok {
		t.Error("another key: accepted")
	}
	if _, ok := ParseDevice(deviceKey, v, now.Add(DeviceCookieMaxAge+time.Second)); ok {
		t.Error("issued more than a year ago: accepted")
	}
}

// TestSessionWindow: Create opens the password window; it closes exactly
// window after the last proved password, using it never extends it, and
// MarkVerified opens it again. An unknown token has no window.
func TestSessionWindow(t *testing.T) {
	c := newClock()
	s := NewSessions(time.Hour, c.now)
	tok := s.Create("admin")
	const w = 10 * time.Minute
	if !s.WindowOpen(tok, w) {
		t.Fatal("window closed right after Create")
	}
	for range 9 {
		c.advance(time.Minute)
		if !s.WindowOpen(tok, w) {
			t.Fatalf("window closed %v after Create", 9*time.Minute)
		}
	}
	c.advance(time.Minute)
	if s.WindowOpen(tok, w) {
		t.Fatal("window still open exactly 10 minutes after Create")
	}
	s.MarkVerified(tok)
	if !s.WindowOpen(tok, w) {
		t.Fatal("window closed right after MarkVerified")
	}
	c.advance(w - time.Second)
	if !s.WindowOpen(tok, w) {
		t.Fatal("window closed a second before its end")
	}
	c.advance(time.Second)
	if s.WindowOpen(tok, w) {
		t.Fatal("window open at its end")
	}
	if _, ok := s.Get(tok); !ok {
		t.Fatal("session ended with its window")
	}
	if s.WindowOpen("unknown", w) {
		t.Fatal("window open for an unknown token")
	}
	s.MarkVerified("unknown") // no session: nothing to mark, no panic
	if s.WindowOpen("unknown", w) {
		t.Fatal("MarkVerified created a session")
	}
	s.MarkVerified(tok)
	s.Delete(tok)
	if s.WindowOpen(tok, w) {
		t.Fatal("window open after Delete")
	}
}

// TestWindowClockSetBack: a clock set back after MarkVerified does not
// keep the window open (now-verified must not be negative).
func TestWindowClockSetBack(t *testing.T) {
	c := newClock()
	s := NewSessions(2*time.Hour, c.now)
	tok := s.Create("admin")
	c.advance(time.Hour)
	s.MarkVerified(tok)
	c.advance(-time.Hour)
	if s.WindowOpen(tok, 10*time.Minute) {
		t.Fatal("window open after the clock went back an hour")
	}
}

// TestWindowEndsWithSession: an expired or invalidated session (e.g.
// after reset-password) has no window, even before Get removes it.
func TestWindowEndsWithSession(t *testing.T) {
	c := newClock()
	s := NewSessions(5*time.Minute, c.now)
	tok := s.Create("admin")
	c.advance(5 * time.Minute)
	if s.WindowOpen(tok, 10*time.Minute) {
		t.Fatal("window open on an expired session")
	}
	valid := true
	s = NewSessions(time.Hour, c.now, WithValidator(func(time.Time) bool { return valid }))
	tok = s.Create("admin")
	valid = false
	if s.WindowOpen(tok, 10*time.Minute) {
		t.Fatal("window open on an invalidated session")
	}
}
