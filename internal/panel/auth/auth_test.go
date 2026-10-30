package auth

import (
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
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "palsu"})
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
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
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
	SetSessionCookie(rec, "tok", true)
	c := rec.Result().Cookies()[0]
	if c.Name != SecureCookieName || c.Value != "tok" || c.Path != "/" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", c)
	}
	rec = httptest.NewRecorder()
	SetSessionCookie(rec, "tok", false)
	if rec.Result().Cookies()[0].Secure {
		t.Error("Secure set when secure=false")
	}

	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, false)
	c = rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("clear cookie = %+v", c)
	}
	if n := len(rec.Result().Cookies()); n != 1 {
		t.Errorf("plain-HTTP clear sets %d cookies, want 1", n)
	}
	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, true)
	if cs := rec.Result().Cookies(); len(cs) != 2 || cs[1].Name != SecureCookieName || !cs[1].Secure || cs[1].MaxAge >= 0 {
		t.Errorf("secure clear cookies = %+v", cs)
	}
}

// TestSecureCookieUsesHostPrefix: over TLS the cookie is "__Host-" prefixed
// (Secure, Path=/, no Domain) and RequireLogin accepts it.
func TestSecureCookieUsesHostPrefix(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok", true)
	c := rec.Result().Cookies()[0]
	if c.Name != "__Host-dbpanel_session" || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("secure cookie = %+v", c)
	}
	rec = httptest.NewRecorder()
	SetSessionCookie(rec, "tok", false)
	if c := rec.Result().Cookies()[0]; c.Name != CookieName {
		t.Fatalf("plain cookie name = %q, want %q", c.Name, CookieName)
	}

	s := NewSessions(time.Hour, time.Now)
	tok := s.Create("admin")
	var got string
	h := RequireLogin(s, "/login")(okHandler(&got))
	req := httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: tok})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got != "admin" {
		t.Fatalf("code = %d, user = %q", rec.Code, got)
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
