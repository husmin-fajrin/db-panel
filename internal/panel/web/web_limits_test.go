package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

// msgConfirmTooMany is T1 (spec 5.11).
const msgConfirmTooMany = "Terlalu banyak kata sandi salah di sesi ini; coba lagi dalam %d menit, atau keluar lalu login lagi."

const (
	msgLoginBlocked = "Terlalu banyak percobaan, coba lagi dalam 15 menit"
	msgLoginFailed  = "Nama pengguna atau kata sandi salah"
	msgLoginBusy    = "Server sibuk, coba lagi sebentar"
)

// testDeviceKey signs the device cookies of limitEnv.
var testDeviceKey = []byte("kunci-cookie-perangkat-untuk-test")

// limits are the limiters of a limitEnv, all on its clock with the
// panel's values.
type limits struct {
	clock       *fakeClock
	ip, user    *auth.RateLimiter
	confirm     *auth.RateLimiter
	confirmLong *auth.RateLimiter
	pair, total *auth.RateLimiter
}

// limitEnv is newEnv with the panel's limits on a fake clock, a device
// key and the fast password check.
func limitEnv(t *testing.T, mods ...func(*web.Deps)) (*env, *limits) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}
	l := &limits{
		clock:       clock,
		ip:          auth.NewRateLimiter(5, 15*time.Minute, clock.Now),
		user:        auth.NewRateLimiter(20, 15*time.Minute, clock.Now),
		confirm:     auth.NewRateLimiter(5, time.Minute, clock.Now),
		confirmLong: auth.NewRateLimiter(20, 15*time.Minute, clock.Now),
		pair:        auth.NewRateLimiter(5, 15*time.Minute, clock.Now),
		total:       auth.NewRateLimiter(20, 15*time.Minute, clock.Now),
	}
	e := newEnv(t, append([]func(*web.Deps){func(d *web.Deps) {
		d.Now = clock.Now
		d.Limiter, d.UserLimiter = l.ip, l.user
		d.ConfirmLimiter, d.ConfirmLongLimiter = l.confirm, l.confirmLong
		d.DevicePairLimiter, d.DeviceTotalLimiter = l.pair, l.total
		d.DeviceKey = testDeviceKey
		d.CheckPassword = fastCheck
	}}, mods...)...)
	return e, l
}

// loginAt posts a login from ip (IPv4 or IPv6), with the device cookie
// dev when it is not nil.
func (e *env) loginAt(ip, user, pw string, dev *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/login", strings.NewReader(loginForm(user, pw, "").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.RemoteAddr = net.JoinHostPort(ip, "40000")
	if dev != nil {
		req.AddCookie(dev)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// deviceCookie is the device cookie rec sets, or nil.
func deviceCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.DeviceCookieName {
			return c
		}
	}
	return nil
}

// loginDevice logs admin in from ip without a device cookie and returns
// the device cookie it sets.
func loginDevice(t *testing.T, e *env, ip string) *http.Cookie {
	t.Helper()
	rec := e.loginAt(ip, "admin", testPassword, nil)
	wantCode(t, rec, http.StatusSeeOther)
	c := deviceCookie(rec)
	if c == nil {
		t.Fatal("login set no device cookie")
	}
	return &http.Cookie{Name: c.Name, Value: c.Value}
}

// fillUserLimit makes 20 wrong logins for admin from four addresses, five
// each: the username limit is then full.
func fillUserLimit(t *testing.T, e *env, l *limits) {
	t.Helper()
	for a := 1; a <= 4; a++ {
		for range 5 {
			wantCode(t, e.loginAt(fmt.Sprintf("203.0.113.%d", a), "admin", "salah", nil), http.StatusOK)
		}
	}
	if !l.user.Blocked(hexSHA256("admin")) {
		t.Fatal("username limit not full after 20 wrong logins")
	}
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// confirmAs posts a confirmation with password as the session c and
// returns the status and the JSON error.
func (e *env) confirmAs(c *http.Cookie, password string) (int, string) {
	rec := e.do("POST", "/confirm", confirmForm(web.ActDeleteServer, "server", 1, password), c)
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Error
}

func wantConfirm(t *testing.T, e *env, c *http.Cookie, password string, code int, msg string) {
	t.Helper()
	gotCode, gotMsg := e.confirmAs(c, password)
	if gotCode != code || (msg != "" && gotMsg != msg) {
		t.Fatalf("confirm: %d %q, want %d %q", gotCode, gotMsg, code, msg)
	}
}

// TestConfirmNotBlockedByOutsideLoginFailures (spec 5.13): wrong logins
// from outside use up the username limit, yet the logged-in admin's
// confirmation and a login with the device cookie still go through.
func TestConfirmNotBlockedByOutsideLoginFailures(t *testing.T) {
	e, l := limitEnv(t)
	dev := loginDevice(t, e, "10.0.0.9")
	c := e.cookie()
	fillUserLimit(t, e, l)

	wantConfirm(t, e, c, testPassword, http.StatusOK, "")
	// Without the cookie the fifth address is refused; with it, let in.
	wantCode(t, e.loginAt("203.0.113.5", "admin", testPassword, nil), http.StatusTooManyRequests)
	wantCode(t, e.loginAt("203.0.113.5", "admin", testPassword, dev), http.StatusSeeOther)
}

func TestConfirmBudgetPerSession(t *testing.T) {
	e, l := limitEnv(t)
	c := e.cookie()
	for range 5 {
		wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")
	}
	wantConfirm(t, e, c, testPassword, http.StatusTooManyRequests, fmt.Sprintf(msgConfirmTooMany, 1))
	// K3: the limiter key is the session token's hash, never the token.
	if !l.confirm.Blocked(hexSHA256(c.Value)) || l.confirm.Blocked(c.Value) {
		t.Error("per-session key is not the hex SHA-256 of the session token")
	}
	l.clock.Add(time.Minute)
	wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")

	// A right password empties both budgets: 19 wrong spread over four
	// minutes, a right one, then more wrong ones are still checked.
	e, l = limitEnv(t)
	c = e.cookie()
	for i := range 19 {
		if i > 0 && i%4 == 0 {
			l.clock.Add(time.Minute)
		}
		wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")
	}
	l.clock.Add(time.Minute)
	wantConfirm(t, e, c, testPassword, http.StatusOK, "")
	for range 5 {
		wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")
	}

	// 20 wrong over five minutes: refused with the minutes left of the
	// 15-minute window, while another session of admin is not.
	e, l = limitEnv(t)
	c = e.cookie()
	for i := range 20 {
		if i > 0 && i%4 == 0 {
			l.clock.Add(time.Minute)
		}
		wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")
	}
	l.clock.Add(30 * time.Second) // 4.5 minutes in: 10.5 left
	wantConfirm(t, e, c, testPassword, http.StatusTooManyRequests, fmt.Sprintf(msgConfirmTooMany, 11))
	c2 := e.cookie()
	wantConfirm(t, e, c2, testPassword, http.StatusOK, "")
	wantConfirm(t, e, c, testPassword, http.StatusTooManyRequests, fmt.Sprintf(msgConfirmTooMany, 11))
	// The refusal kept no slot in the one-minute budget.
	if l.confirm.Blocked(hexSHA256(c.Value)) {
		t.Error("refused confirmations used up the one-minute budget")
	}
	l.clock.Add(11 * time.Minute)
	wantConfirm(t, e, c, testPassword, http.StatusOK, "")
}

// TestConfirmWrongStillCountsForLogin: wrong confirmations still count
// against the username limit (holding off logins without the cookie),
// but a full username limit does not refuse a session's confirmation.
func TestConfirmWrongStillCountsForLogin(t *testing.T) {
	e, l := limitEnv(t)
	c := e.cookie()
	for i := range 20 {
		if i > 0 && i%4 == 0 {
			l.clock.Add(time.Minute)
		}
		wantConfirm(t, e, c, "salah", http.StatusUnauthorized, "")
	}
	wantCode(t, e.loginAt("198.51.100.7", "admin", testPassword, nil), http.StatusTooManyRequests)
	if !l.user.Blocked(hexSHA256("admin")) {
		t.Fatal("username limit not full after 20 wrong confirmations")
	}
	// Still inside the 15 minutes: a new session confirms.
	l.clock.Add(5 * time.Minute)
	wantConfirm(t, e, e.cookie(), testPassword, http.StatusOK, "")
}

func TestDeviceCookieSetOnLogin(t *testing.T) {
	e, _ := limitEnv(t)
	rec := e.loginAt("10.0.0.9", "admin", testPassword, nil)
	wantCode(t, rec, http.StatusSeeOther)
	c := deviceCookie(rec)
	if c == nil {
		t.Fatal("no device cookie")
	}
	if c.Name != "__Host-dbpanel_device" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.MaxAge != 31536000 || c.Domain != "" {
		t.Errorf("device cookie = %+v", c)
	}
	rec = e.loginAt("10.0.0.9", "admin", testPassword, &http.Cookie{Name: c.Name, Value: c.Value})
	wantCode(t, rec, http.StatusSeeOther)
	if deviceCookie(rec) != nil {
		t.Error("a login with a valid device cookie set a new one")
	}
	// A wrong password sets none.
	if deviceCookie(e.loginAt("10.0.0.10", "admin", "salah", nil)) != nil {
		t.Error("a failed login set a device cookie")
	}
}

func TestDeviceLoginIgnoresCookieOfOtherUserOrBeforeReset(t *testing.T) {
	var resetAt atomic.Int64 // unix seconds; cookies issued at or before it are invalid
	e, l := limitEnv(t, func(d *web.Deps) {
		d.IssuedValid = func(issued time.Time) bool { return issued.Unix() > resetAt.Load() }
	})
	now := l.clock.Now()
	valid, _ := auth.NewDevice(testDeviceKey, "admin", now.Add(-time.Hour))
	other, _ := auth.NewDevice(testDeviceKey, "operator", now.Add(-time.Hour))
	forged, _ := auth.NewDevice([]byte("kunci lain sama sekali, bukan panel"), "admin", now.Add(-time.Hour))
	old, _ := auth.NewDevice(testDeviceKey, "admin", now.Add(-2*time.Hour))
	resetAt.Store(now.Add(-90 * time.Minute).Unix())
	fillUserLimit(t, e, l)

	for i, v := range []string{other, forged, old, "v1.bukan.cookie"} {
		rec := e.loginAt(fmt.Sprintf("198.51.100.%d", i+1), "admin", testPassword,
			&http.Cookie{Name: auth.DeviceCookieName, Value: v})
		wantCode(t, rec, http.StatusTooManyRequests)
		wantBody(t, rec, msgLoginBlocked)
	}
	// The valid one is let in.
	wantCode(t, e.loginAt("198.51.100.9", "admin", testPassword, &http.Cookie{Name: auth.DeviceCookieName, Value: valid}),
		http.StatusSeeOther)
}

func TestDeviceBudgetPerPair(t *testing.T) {
	e, l := limitEnv(t)
	dev := loginDevice(t, e, "10.0.0.9")
	for range 5 {
		rec := e.loginAt("10.0.0.1", "admin", "salah", dev)
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, msgLoginFailed)
	}
	rec := e.loginAt("10.0.0.1", "admin", testPassword, dev)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, msgLoginBlocked)
	// Another address with the same cookie has its own budget.
	rec = e.loginAt("10.0.0.2", "admin", "salah", dev)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, msgLoginFailed)
	// K6: the cookie's failures counted neither per username nor per
	// address: address A logs in without the cookie.
	if l.user.Len() != 0 {
		t.Errorf("username limiter tracks %d keys, want 0", l.user.Len())
	}
	wantCode(t, e.loginAt("10.0.0.1", "admin", testPassword, nil), http.StatusSeeOther)
}

func TestDeviceTotalBlocksOnlyNewPairs(t *testing.T) {
	e, _ := limitEnv(t)
	dev := loginDevice(t, e, "10.0.0.1") // pair (cookie, A) has logged in
	for a := 2; a <= 5; a++ {
		for range 5 {
			wantCode(t, e.loginAt(fmt.Sprintf("10.0.0.%d", a), "admin", "salah", dev), http.StatusOK)
		}
	}
	rec := e.loginAt("10.0.0.6", "admin", testPassword, dev)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, msgLoginBlocked)
	// A wrong password from A is still checked, and the right one lets in.
	wantCode(t, e.loginAt("10.0.0.1", "admin", "salah", dev), http.StatusOK)
	wantCode(t, e.loginAt("10.0.0.1", "admin", testPassword, dev), http.StatusSeeOther)
}

// TestDeviceLoginReservesBeforeCheck: concurrent wrong logins from one
// cookie and address reserve their budget before the password check; at
// most two checks run, a login that waits 5 s for a slot is refused as
// busy and gets its reservation back.
func TestDeviceLoginReservesBeforeCheck(t *testing.T) {
	var (
		checks  atomic.Int32
		release = make(chan struct{})
	)
	e, _ := limitEnv(t, func(d *web.Deps) {
		d.CheckPassword = func(_, pw string) bool {
			if pw == testPassword {
				return true
			}
			checks.Add(1)
			<-release
			return false
		}
	})
	dev := loginDevice(t, e, "10.0.0.1")
	const n = 10
	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		blocked, busy   int
		failed, unknown int
	)
	for range n {
		wg.Go(func() {
			rec := e.loginAt("10.0.0.1", "admin", "salah", dev)
			mu.Lock()
			defer mu.Unlock()
			switch body := rec.Body.String(); {
			case rec.Code == http.StatusTooManyRequests && strings.Contains(body, msgLoginBlocked):
				blocked++
			case rec.Code == http.StatusTooManyRequests && strings.Contains(body, msgLoginBusy):
				busy++
			case rec.Code == http.StatusOK && strings.Contains(body, msgLoginFailed):
				failed++
			default:
				unknown++
			}
		})
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		done := blocked+busy == n-2
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			wg.Wait()
			t.Fatalf("blocked %d busy %d checks %d after 20 s", blocked, busy, checks.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := checks.Load(); got != 2 {
		t.Errorf("password checks running = %d, want 2", got)
	}
	close(release)
	wg.Wait()
	if blocked != 5 || busy != 3 || failed != 2 || unknown != 0 {
		t.Fatalf("blocked %d busy %d failed %d other %d; want 5, 3, 2, 0", blocked, busy, failed, unknown)
	}
	if got := checks.Load(); got > 5 {
		t.Errorf("password checks = %d, want at most 5", got)
	}
	// The busy ones gave their reservation back: 2 used, 3 left.
	for range 3 {
		wantCode(t, e.loginAt("10.0.0.1", "admin", "salah", dev), http.StatusOK)
	}
	wantCode(t, e.loginAt("10.0.0.1", "admin", "salah", dev), http.StatusTooManyRequests)
}

func TestLogoutKeepsDeviceCookie(t *testing.T) {
	e, _ := limitEnv(t)
	dev := loginDevice(t, e, "10.0.0.1")
	req := httptest.NewRequest("POST", "/logout", nil)
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(e.cookie())
	req.AddCookie(dev)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusSeeOther)
	if c := deviceCookie(rec); c != nil {
		t.Errorf("logout touched the device cookie: %+v", c)
	}
}

// TestLoginIPv6SharesSlash64: the per-address limit counts an IPv6 /64 as
// one address.
func TestLoginIPv6SharesSlash64(t *testing.T) {
	e, _ := limitEnv(t)
	for i := 1; i <= 5; i++ {
		wantCode(t, e.loginAt(fmt.Sprintf("2001:db8:1:2::%x", i), "admin", "salah", nil), http.StatusOK)
	}
	rec := e.loginAt("2001:db8:1:2:ffff::1", "admin", testPassword, nil)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, msgLoginBlocked)
	wantCode(t, e.loginAt("2001:db8:1:3::1", "admin", testPassword, nil), http.StatusSeeOther)
}
