package web_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"golang.org/x/crypto/bcrypt"
)

const (
	testPassword  = "rahasia-panel"
	testPublicURL = "http://192.168.1.10:8080"
	testOrigin    = "http://example.com" // httptest.NewRequest uses Host example.com
)

// fakeScriptSHA256 stands in for the hash of the install script the panel
// would serve for token.
func fakeScriptSHA256(token string) (string, error) {
	s := sha256.Sum256([]byte("skrip " + token))
	return hex.EncodeToString(s[:]), nil
}

// adminHash uses the minimum bcrypt cost: auth.HashPassword's cost 12 makes
// every login test take seconds (much more under -race).
var adminHash = sync.OnceValue(func() string {
	h, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
})

// fakeListener records the servers reported offline.
type fakeListener struct {
	mu      sync.Mutex
	offline []int64
}

func (*fakeListener) OnOnline(int64) {}
func (l *fakeListener) OnOffline(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.offline = append(l.offline, id)
}
func (*fakeListener) OnEvent(int64, proto.Event) {}

func (l *fakeListener) offlineIDs() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int64(nil), l.offline...)
}

// fakeConn is a live agent connection that never answers.
type fakeConn struct{}

func (fakeConn) Call(context.Context, string, any, any) error { return errors.New("tidak dipakai") }
func (fakeConn) Close() error                                 { return nil }

// closingConn is a fakeConn that reports when it is closed.
type closingConn struct {
	fakeConn
	closed chan struct{}
	once   sync.Once
}

func (c *closingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

type env struct {
	st       *store.Store
	dbPath   string
	l        *fakeListener
	hub      *hub.Hub
	sessions *auth.Sessions
	broker   *web.Broker
	h        http.Handler
}

// newEnv builds the router; mods adjust its Deps before it is built.
func newEnv(t *testing.T, mods ...func(*web.Deps)) *env {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateUser("admin", adminHash()); err != nil {
		t.Fatal(err)
	}
	l := &fakeListener{}
	e := &env{
		st:       st,
		dbPath:   dbPath,
		l:        l,
		hub:      hub.New(l),
		sessions: auth.NewSessions(time.Hour, time.Now),
		broker:   web.NewBroker(),
	}
	d := web.Deps{
		Store:               st,
		Hub:                 e.hub,
		Sessions:            e.sessions,
		Limiter:             auth.NewRateLimiter(5, 15*time.Minute, time.Now),
		PublicURL:           func() string { return testPublicURL },
		Events:              e.broker,
		InstallScriptSHA256: fakeScriptSHA256,
		Now:                 time.Now,
		Version:             "v-test",
		Logger:              slog.New(slog.DiscardHandler),
	}
	for _, m := range mods {
		m(&d)
	}
	e.h = web.NewRouter(d)
	return e
}

func (e *env) cookie() *http.Cookie {
	return &http.Cookie{Name: auth.CookieName, Value: e.sessions.Create("admin")}
}

// do sends a request through the router. POSTs carry a same-site Origin.
func (e *env) do(method, path string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", testOrigin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, code, rec.Body.String())
	}
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(rec.Body.String(), s) {
			t.Errorf("body does not contain %q; body:\n%s", s, rec.Body.String())
		}
	}
}

func loginForm(user, pw, next string) url.Values {
	return url.Values{"username": {user}, "password": {pw}, "next": {next}}
}

func TestLoginPageRenders(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/login?next=/servers", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Masuk", `name="username"`, `name="password"`, `value="/servers"`,
		`data-theme="corporate"`, "/static/app.css", "/static/app.js", "/static/vendor/alpine-csp.min.js")
}

func TestLoginWrongPassword(t *testing.T) {
	e := newEnv(t)
	for _, user := range []string{"admin", "tidak-ada"} {
		rec := e.do("POST", "/login", loginForm(user, "salah", ""), nil)
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, "Nama pengguna atau kata sandi salah")
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("user %q: cookie set on failed login", user)
		}
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 5; i++ {
		wantCode(t, e.do("POST", "/login", loginForm("admin", "salah", ""), nil), http.StatusOK)
	}
	rec := e.do("POST", "/login", loginForm("admin", "salah", ""), nil)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, "Terlalu banyak percobaan, coba lagi dalam 15 menit")
	// Blocked even with the right password.
	rec = e.do("POST", "/login", loginForm("admin", testPassword, ""), nil)
	wantCode(t, rec, http.StatusTooManyRequests)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("cookie set while rate limited")
	}
}

// loginFrom posts a login form from the given client IP.
func (e *env) loginFrom(ip, user, pw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/login", strings.NewReader(loginForm(user, pw, "").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// fastCheck is a password check without bcrypt's cost, for tests that make
// many attempts; only testPassword matches.
func fastCheck(_, pw string) bool { return pw == testPassword }

// TestLoginRateLimitedPerUsername (SEC-05): rotating the source address does
// not buy more than 20 wrong guesses per 15 min for one account, whatever
// the case of the username.
func TestLoginRateLimitedPerUsername(t *testing.T) {
	e := newEnv(t, func(d *web.Deps) { d.CheckPassword = fastCheck })
	for i := 1; i <= 20; i++ {
		user := "admin"
		if i%2 == 0 {
			user = "ADMIN" // same account key
		}
		wantCode(t, e.loginFrom(fmt.Sprintf("10.9.0.%d", i), user, "salah"), http.StatusOK)
	}
	rec := e.loginFrom("10.9.0.21", "admin", "salah")
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, "Terlalu banyak percobaan, coba lagi dalam 15 menit")
	// Blocked even with the right password.
	wantCode(t, e.loginFrom("10.9.0.22", "admin", testPassword), http.StatusTooManyRequests)
	// Another username from a fresh address is not affected.
	wantCode(t, e.loginFrom("10.9.0.23", "operator", "salah"), http.StatusOK)
}

// TestLoginRejectsLongInput (R1): an over-long username or password is
// refused with 400 before any limiter key or password check exists.
func TestLoginRejectsLongInput(t *testing.T) {
	var checks atomic.Int32
	ipLim := auth.NewRateLimiter(5, 15*time.Minute, time.Now)
	userLim := auth.NewRateLimiter(20, 15*time.Minute, time.Now)
	e := newEnv(t, func(d *web.Deps) {
		d.Limiter, d.UserLimiter = ipLim, userLim
		d.CheckPassword = func(string, string) bool { checks.Add(1); return false }
	})
	for _, f := range []url.Values{
		loginForm(strings.Repeat("u", 65), "x", ""),
		loginForm("admin", strings.Repeat("p", 73), ""),
	} {
		rec := e.do("POST", "/login", f, nil)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Nama pengguna atau kata sandi terlalu panjang")
	}
	if n := ipLim.Len() + userLim.Len(); n != 0 || checks.Load() != 0 {
		t.Fatalf("limiter keys %d, password checks %d; want 0 and 0", n, checks.Load())
	}
	// 64 and 72 bytes are still accepted.
	wantCode(t, e.do("POST", "/login", loginForm(strings.Repeat("u", 64), strings.Repeat("p", 72), ""), nil), http.StatusOK)
}

// TestLoginFloodKeepsLimitersBounded (R1): a flood of distinct usernames
// from rotating addresses cannot grow the limiters past their cap, and
// long usernames never become keys at all.
func TestLoginFloodKeepsLimitersBounded(t *testing.T) {
	ipLim := auth.NewRateLimiter(5, 15*time.Minute, time.Now)
	userLim := auth.NewRateLimiter(20, 15*time.Minute, time.Now)
	ipLim.SetMaxKeys(50)
	userLim.SetMaxKeys(50)
	e := newEnv(t, func(d *web.Deps) {
		d.Limiter, d.UserLimiter = ipLim, userLim
		d.CheckPassword = fastCheck
	})
	long := strings.Repeat("x", 60<<10)
	for i := 0; i < 500; i++ {
		ip := fmt.Sprintf("10.%d.%d.1", i/250, i%250)
		e.loginFrom(ip, fmt.Sprintf("%s%d", long, i), "x")
	}
	if n := ipLim.Len() + userLim.Len(); n != 0 {
		t.Fatalf("long usernames created %d limiter keys", n)
	}
	refused := 0
	for i := 0; i < 500; i++ {
		ip := fmt.Sprintf("10.%d.%d.2", i/250, i%250)
		if e.loginFrom(ip, fmt.Sprintf("user%d", i), "x").Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if ipLim.Len() > 50 || userLim.Len() > 50 {
		t.Fatalf("limiter keys ip %d user %d, want <= 50", ipLim.Len(), userLim.Len())
	}
	if refused < 400 {
		t.Fatalf("refused %d of 500 new keys once full, want >= 400", refused)
	}
}

// TestLoginConcurrencyCap (SEC-05): once the password-check slots are taken,
// further logins get 429 at once instead of queueing more bcrypt work.
func TestLoginConcurrencyCap(t *testing.T) {
	var (
		running atomic.Int32
		release = make(chan struct{})
	)
	userLim := auth.NewRateLimiter(20, 15*time.Minute, time.Now)
	e := newEnv(t, func(d *web.Deps) {
		d.LoginConcurrency = 2
		d.UserLimiter = userLim
		d.CheckPassword = func(string, string) bool {
			running.Add(1)
			<-release
			return false
		}
	})
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Go(func() { e.loginFrom(fmt.Sprintf("10.8.0.%d", i), fmt.Sprintf("u%d", i), "x") })
	}
	waitFor(t, func() bool { return running.Load() == 2 })

	rec := e.loginFrom("10.8.0.3", "u3", "x")
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, "Server sibuk, coba lagi sebentar")
	if n := running.Load(); n != 2 {
		t.Fatalf("password checks started = %d, want 2 (the refused login must not hash)", n)
	}
	close(release)
	wg.Wait()
	// Slots are free again.
	wantCode(t, e.loginFrom("10.8.0.4", "u4", "x"), http.StatusOK)
	// Failures counted per username: u1, u2, u4 (real mismatches), not u3,
	// which was refused as busy.
	if n := userLim.Len(); n != 3 {
		t.Fatalf("per-username failures recorded for %d names, want 3", n)
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/login", loginForm("admin", testPassword, ""), nil)
	wantCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != auth.CookieName || !cs[0].HttpOnly {
		t.Fatalf("cookies = %+v", cs)
	}
	if u, ok := e.sessions.Get(cs[0].Value); !ok || u != "admin" {
		t.Errorf("session = %q, %v", u, ok)
	}
	// A local next is honoured.
	rec = e.do("POST", "/login", loginForm("admin", testPassword, "/servers/1"), nil)
	if loc := rec.Header().Get("Location"); loc != "/servers/1" {
		t.Errorf("Location = %q, want /servers/1", loc)
	}
}

func TestLoginRejectsExternalNext(t *testing.T) {
	e := newEnv(t)
	for _, next := range []string{
		"http://evil", "//evil.example", `/\evil.example`, "/\t/evil.example", "servers",
		// http.Redirect path.Cleans these to "/\evil.example".
		`/./\evil.example`, `/../\evil.example`, `/a/../\evil.example`,
		"/%5Cevil.example",
	} {
		rec := e.do("POST", "/login", loginForm("admin", testPassword, next), nil)
		wantCode(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q: Location = %q, want /", next, loc)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	rec := e.do("POST", "/logout", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q", loc)
	}
	if _, ok := e.sessions.Get(c.Value); ok {
		t.Error("session still valid after logout")
	}
}

func TestServersRequireLogin(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/servers", "/servers/new", "/servers/1", "/events"} {
		rec := e.do("GET", p, nil, nil)
		wantCode(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("%s: Location = %q", p, loc)
		}
	}
}

func TestPostRejectsCrossOrigin(t *testing.T) {
	e := newEnv(t)
	srv, err := e.st.CreateServer("db-1")
	if err != nil {
		t.Fatal(err)
	}
	posts := []struct {
		path string
		form url.Values
	}{
		{"/login", loginForm("admin", testPassword, "")},
		{"/logout", url.Values{}},
		{"/servers", url.Values{"name": {"db-2"}}},
		{"/servers/1/token", url.Values{}},
		{"/servers/1/delete", url.Values{}},
	}
	for _, p := range posts {
		for _, origin := range []string{"", "http://evil.example", "null"} {
			req := httptest.NewRequest("POST", p.path, strings.NewReader(p.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			req.AddCookie(e.cookie())
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permintaan ditolak") {
				t.Errorf("POST %s origin=%q: status %d, body %q", p.path, origin, rec.Code, rec.Body.String())
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Errorf("POST %s origin=%q: cookie set", p.path, origin)
			}
		}
	}
	if list, _ := e.st.ListServers(); len(list) != 1 {
		t.Errorf("servers after rejected POSTs = %d, want 1", len(list))
	}
	// A matching Referer is accepted when Origin is absent.
	req := httptest.NewRequest("POST", "/servers/1/delete", nil)
	req.Header.Set("Referer", testOrigin+"/servers/1")
	req.AddCookie(e.cookie())
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusSeeOther)
	if _, err := e.st.GetServer(srv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("server not deleted: %v", err)
	}
}

func TestDashboardCounts(t *testing.T) {
	e := newEnv(t)
	a, _ := e.st.CreateServer("db-a")
	b, _ := e.st.CreateServer("db-b")
	e.st.CreateServer("db-c")
	e.st.MarkPaired(a.ID, "s1", "10.0.0.1")
	e.st.MarkPaired(b.ID, "s2", "10.0.0.2")
	e.hub.Attach(a.ID, fakeConn{})
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `data-stat="total">3<`, `data-stat="online">1<`, `data-stat="unpaired">1<`, "admin", "Keluar")
}

func TestServerListStatus(t *testing.T) {
	e := newEnv(t)
	a, _ := e.st.CreateServer("db-online")
	b, _ := e.st.CreateServer("db-offline")
	e.st.CreateServer("db-baru")
	e.st.MarkPaired(a.ID, "s1", "10.0.0.1")
	e.st.MarkPaired(b.ID, "s2", "10.0.0.2")
	e.st.UpdateProbe(a.ID, proto.ProbeResult{OS: "Ubuntu 24.04", MySQL: proto.MySQLInfo{Installed: true, Version: "8.0.39"}}, "v1", time.Now())
	e.hub.Attach(a.ID, fakeConn{})
	rec := e.do("GET", "/servers", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "db-online", "db-offline", "db-baru", "10.0.0.1",
		">Online<", ">Offline<", ">Belum dipasangkan<", "8.0.39", "Ubuntu 24.04")
}

func TestCreateServerShowsPairingCommand(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	rec := e.do("POST", "/servers", url.Values{"name": {"db-utama"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	loc := rec.Header().Get("Location")
	if loc != "/servers/1" {
		t.Fatalf("Location = %q", loc)
	}
	tok, ok, err := e.st.ActivePairToken(1, time.Now())
	if err != nil || !ok {
		t.Fatalf("no active token: %v %v", ok, err)
	}
	if left := time.Until(tok.ExpiresAt); left < 23*time.Hour || left > 25*time.Hour {
		t.Errorf("token TTL = %v, want ~24h", left)
	}
	rec = e.do("GET", loc, nil, c)
	wantCode(t, rec, http.StatusOK)
	sum, _ := fakeScriptSHA256(tok.Token)
	wantBody(t, rec, "db-utama", "/agent/install?token=",
		"f=$(mktemp) &amp;&amp; curl -fsSL --max-redirs 0 -K - &lt;&lt;&lt;&#39;url = &#34;"+testPublicURL+"/agent/install?token="+tok.Token+
			"&#34;&#39; -o &#34;$f&#34; &amp;&amp; echo &#34;"+sum+
			"  $f&#34; | sha256sum -c - &amp;&amp; sudo bash &#34;$f&#34;",
		`action="/servers/1/token"`, "Buat token baru")

	// A new token replaces the old one in the command.
	rec = e.do("POST", "/servers/1/token", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if l := rec.Header().Get("Location"); l != "/servers/1" {
		t.Errorf("Location = %q", l)
	}
	tok2, _, _ := e.st.ActivePairToken(1, time.Now())
	if tok2.Token == tok.Token {
		t.Fatal("token not replaced")
	}
	wantBody(t, e.do("GET", "/servers/1", nil, c), "token="+tok2.Token)
}

func TestCreateServerEmptyName(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/servers", url.Values{"name": {"  "}}, e.cookie())
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "nama server tidak boleh kosong")
}

// TestCreateServerBadName (SEC-16): control characters, bidi overrides and
// long names are refused with the store's message.
func TestCreateServerBadName(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"db\x1b[31m\u202eevil", strings.Repeat("a", 65)} {
		rec := e.do("POST", "/servers", url.Values{"name": {name}}, e.cookie())
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "nama server hanya boleh huruf, angka, spasi, titik, garis bawah dan strip (maks. 64)")
	}
	if list, _ := e.st.ListServers(); len(list) != 0 {
		t.Errorf("servers = %d, want 0", len(list))
	}
}

func TestCreateServerDuplicateName(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.CreateServer("db-utama"); err != nil {
		t.Fatal(err)
	}
	rec := e.do("POST", "/servers", url.Values{"name": {"db-utama"}}, e.cookie())
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "nama server sudah dipakai", `value="db-utama"`)
	if list, _ := e.st.ListServers(); len(list) != 1 {
		t.Errorf("servers = %d, want 1", len(list))
	}
}

func TestServerDetailUnpairedCreatesMissingToken(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("db-tanpa-token")
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	tok, ok, _ := e.st.ActivePairToken(srv.ID, time.Now())
	if !ok {
		t.Fatal("detail page did not create a token")
	}
	wantBody(t, rec, "token="+tok.Token)
}

// TestServerDetailTokenFromEarlierProcess (SEC-13): a token created before
// a panel restart is stored only as a hash; the page says so and offers a
// new token instead of showing a command it cannot rebuild.
func TestServerDetailTokenFromEarlierProcess(t *testing.T) {
	e := newEnv(t)
	other, err := store.Open(e.dbPath) // the panel process before a restart
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unpaired, _ := other.CreateServer("db-unpaired")
	paired, _ := other.CreateServer("db-paired")
	other.MarkPaired(paired.ID, "serial", "10.0.0.2")
	for _, id := range []int64{unpaired.ID, paired.ID} {
		if _, err := other.CreatePairToken(id, time.Now(), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	c := e.cookie()
	for _, id := range []int64{unpaired.ID, paired.ID} {
		before, _, _ := e.st.ActivePairToken(id, time.Now())
		rec := e.do("GET", fmt.Sprintf("/servers/%d", id), nil, c)
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, "Perintah pemasangan tidak bisa ditampilkan lagi", "Buat token baru",
			fmt.Sprintf(`action="/servers/%d/token"`, id))
		if strings.Contains(rec.Body.String(), "/agent/install") {
			t.Errorf("server %d: page shows a pairing command without the token", id)
		}
		after, _, _ := e.st.ActivePairToken(id, time.Now())
		if !after.ExpiresAt.Equal(before.ExpiresAt) {
			t.Errorf("server %d: viewing the page replaced the active token", id)
		}
	}
}

func TestServerDetailPairedShowsProbe(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("db-replika")
	e.st.MarkPaired(srv.ID, "serial", "10.0.0.7")
	probe := proto.ProbeResult{
		Hostname: "srv-replika", OS: "Rocky Linux 9.4", Arch: "amd64", Kernel: "5.14.0",
		CPUCores: 8, MemTotalMB: 16384,
		Disks: []proto.DiskInfo{{Mount: "/var/lib/mysql", TotalMB: 512000, UsedMB: 2048}},
		MySQL: proto.MySQLInfo{Installed: true, Version: "8.0.39", ServiceName: "mysqld",
			ServiceActive: true, Port: 3306, DataDir: "/var/lib/mysql"},
	}
	if err := e.st.UpdateProbe(srv.ID, probe, "v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	e.hub.Attach(srv.ID, fakeConn{})
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "srv-replika", "Rocky Linux 9.4", "amd64", "5.14.0", "16.0 GB",
		"/var/lib/mysql", "500.0 GB", "2.0 GB", "8.0.39", "mysqld", "3306", ">Online<",
		// Read by app.js; a data-on* name would get JS escaping (" true ").
		`x-data="serverStatus" data-server-id="1" data-connected="true"`)
	if strings.Contains(rec.Body.String(), "/agent/install") {
		t.Error("paired server shows pairing command")
	}
}

func TestServerNotFound(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/servers/99", "/servers/abc"} {
		rec := e.do("GET", p, nil, e.cookie())
		wantCode(t, rec, http.StatusNotFound)
		wantBody(t, rec, "Server tidak ditemukan")
	}
}

func TestDeleteServer(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("db-hapus")
	c := e.cookie()
	wantBody(t, e.do("GET", "/servers/1", nil, c), `action="/servers/1/delete"`, "<dialog")
	rec := e.do("POST", "/servers/1/delete", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/servers" {
		t.Errorf("Location = %q", loc)
	}
	if _, err := e.st.GetServer(srv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetServer after delete: %v", err)
	}
	rec = e.do("POST", "/servers/1/delete", url.Values{}, c)
	wantCode(t, rec, http.StatusNotFound)
	wantBody(t, rec, "Server tidak ditemukan")
}

func TestDeleteServerDisconnectsAgent(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("db-terhubung")
	e.st.MarkPaired(srv.ID, "serial", "10.0.0.8")
	conn := &closingConn{closed: make(chan struct{})}
	e.hub.Attach(srv.ID, conn)

	rec := e.do("POST", "/servers/1/delete", url.Values{}, e.cookie())
	wantCode(t, rec, http.StatusSeeOther)
	if e.hub.IsOnline(srv.ID) {
		t.Fatal("deleted server is still online in the hub")
	}
	if got := e.l.offlineIDs(); len(got) != 1 || got[0] != srv.ID {
		t.Fatalf("OnOffline calls = %v, want [%d]", got, srv.ID)
	}
	select {
	case <-conn.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("agent connection of the deleted server was not closed")
	}
}

func TestDeleteLocalServerRefused(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("host-panel")
	e.st.MarkPaired(srv.ID, "local", "127.0.0.1")
	c := e.cookie()

	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	for _, s := range []string{`action="/servers/1/delete"`, "Hapus server", "Pasang ulang agent"} {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("detail page of the panel host shows %q", s)
		}
	}
	rec = e.do("POST", "/servers/1/delete", url.Values{}, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "Server ini adalah host panel dan tidak bisa dihapus")
	if _, err := e.st.GetServer(srv.ID); err != nil {
		t.Fatalf("local server gone after refused delete: %v", err)
	}
	wantCode(t, e.do("POST", "/servers/1/token", url.Values{}, c), http.StatusConflict)
}

func TestRepairPairedServer(t *testing.T) {
	e := newEnv(t)
	srv, _ := e.st.CreateServer("db-diganti")
	e.st.MarkPaired(srv.ID, "serial-lama", "10.0.0.9")
	c := e.cookie()

	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Pasang ulang agent", `id="modal-pasang-ulang"`, `action="/servers/1/token"`)
	if strings.Contains(rec.Body.String(), "/agent/install") {
		t.Fatal("paired server shows a pairing command before re-pairing was asked for")
	}

	rec = e.do("POST", "/servers/1/token", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if l := rec.Header().Get("Location"); l != "/servers/1" {
		t.Errorf("Location = %q", l)
	}
	tok, ok, err := e.st.ActivePairToken(srv.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("no active token after re-pair: %v %v", ok, err)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "/agent/install?token="+tok.Token, "Agent lama tetap bekerja")
	if got, _ := e.st.GetServer(srv.ID); got.CertSerial != "serial-lama" || !got.Paired {
		t.Fatalf("server after asking to re-pair = %+v, want the old pairing kept", got)
	}
}

// baseHeaders are the security headers every UI response carries (SEC-08).
var baseHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; object-src 'none'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	"X-Content-Type-Options":       "nosniff",
	"X-Frame-Options":              "DENY",
	"Referrer-Policy":              "same-origin",
	"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
}

func wantHeaders(t *testing.T, what string, h http.Header, cacheControl string) {
	t.Helper()
	for k, v := range baseHeaders {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", what, k, got, v)
		}
	}
	if got := h.Get("Cache-Control"); got != cacheControl {
		t.Errorf("%s: Cache-Control = %q, want %q", what, got, cacheControl)
	}
	if got := h.Get("Server"); got != "" {
		t.Errorf("%s: Server = %q, want none", what, got)
	}
	if got := h.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("%s: HSTS over plain HTTP = %q", what, got)
	}
}

// TestSecurityHeaders (SEC-08) checks each UI response class: the login
// page, a page behind the login, a static asset and the SSE stream.
func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/login", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantHeaders(t, "GET /login", rec.Header(), "no-store")
	for _, p := range []string{"/", "/servers", "/servers/new"} {
		rec := e.do("GET", p, nil, c)
		wantCode(t, rec, http.StatusOK)
		wantHeaders(t, "GET "+p, rec.Header(), "no-store")
	}
	e.st.CreateServer("db-header")
	rec = e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "/agent/install?token=") // the page carries a pairing token
	wantHeaders(t, "GET /servers/1", rec.Header(), "no-store")
	rec = e.do("GET", "/static/app.css", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantHeaders(t, "GET /static/app.css", rec.Header(), "public, max-age=3600")
	rec = e.do("GET", "/nope", nil, c)
	wantCode(t, rec, http.StatusNotFound)
	wantHeaders(t, "404", rec.Header(), "no-store")

	ts := httptest.NewServer(e.h)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/events", nil)
	req.AddCookie(c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events = %d", resp.StatusCode)
	}
	wantHeaders(t, "GET /events", resp.Header, "no-cache")
}

// TestPagesHaveNoInlineScript: the CSP allows scripts from the panel only,
// so pages must not carry inline scripts or event-handler attributes.
func TestPagesHaveNoInlineScript(t *testing.T) {
	e := newEnv(t)
	c := e.cookie()
	srv, _ := e.st.CreateServer("db-paired")
	e.st.MarkPaired(srv.ID, "serial", "10.0.0.1")
	e.st.CreateServer("db-unpaired")
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>[^<]|\son[a-z]+=|javascript:|\sstyle=`)
	for _, p := range []string{"/login", "/", "/servers", "/servers/new", "/servers/1", "/servers/2"} {
		var ck *http.Cookie
		if p != "/login" {
			ck = c
		}
		rec := e.do("GET", p, nil, ck)
		wantCode(t, rec, http.StatusOK)
		if m := inline.FindString(rec.Body.String()); m != "" {
			t.Errorf("GET %s has inline script/handler/style %q", p, m)
		}
	}
}

// TestStaticNoDirectoryListing (SEC-12).
func TestStaticNoDirectoryListing(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/static/", "/static/vendor/", "/static/vendor"} {
		rec := e.do("GET", p, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404; body:\n%s", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "<a href=") {
			t.Errorf("GET %s lists the directory", p)
		}
	}
}

// TestFormBodyLimit (SEC-10): form posts are capped at 64 KiB.
func TestFormBodyLimit(t *testing.T) {
	e := newEnv(t)
	big := strings.Repeat("a", 70<<10)
	rec := e.do("POST", "/login", loginForm("admin", big, ""), nil)
	wantCode(t, rec, http.StatusRequestEntityTooLarge)
	wantBody(t, rec, "Permintaan terlalu besar")
	rec = e.do("POST", "/servers", url.Values{"name": {big}}, e.cookie())
	wantCode(t, rec, http.StatusRequestEntityTooLarge)
	if list, _ := e.st.ListServers(); len(list) != 0 {
		t.Errorf("servers = %d after an oversized post, want 0", len(list))
	}
	// A normal post still works.
	wantCode(t, e.do("POST", "/login", loginForm("admin", testPassword, ""), nil), http.StatusSeeOther)
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/vendor/alpine-csp.min.js"} {
		rec := e.do("GET", p, nil, nil)
		wantCode(t, rec, http.StatusOK)
		if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
			t.Errorf("%s: Cache-Control = %q", p, cc)
		}
	}
}

func TestSSEStreamsStatus(t *testing.T) {
	e := newEnv(t)
	ts := httptest.NewServer(e.h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	req.AddCookie(e.cookie())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for h, want := range map[string]string{
		"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}

	// The subscription exists once the headers arrive.
	e.broker.Publish("server_status", map[string]any{"id": 7, "online": true})
	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if sc.Text() == "" && len(lines) > 1 {
			break
		}
	}
	got := strings.Join(lines, "\n")
	want := "event: server_status\ndata: {\"id\":7,\"online\":true}\n"
	if got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
}

func TestBrokerUnsubscribesOnDisconnect(t *testing.T) {
	b := web.NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		b.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	waitFor(t, func() bool { return web.SubscriberCount(b) == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after the client went away")
	}
	if n := web.SubscriberCount(b); n != 0 {
		t.Errorf("subscribers after disconnect = %d, want 0", n)
	}
}

func TestBrokerPublishDropsForFullSubscriber(t *testing.T) {
	b := web.NewBroker()
	// A subscriber whose writes block: its 16-slot buffer fills up and
	// Publish must not wait for it.
	blocked := make(chan struct{})
	defer close(blocked)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	go b.ServeHTTP(&blockingWriter{ResponseRecorder: httptest.NewRecorder(), block: blocked}, req)
	waitFor(t, func() bool { return web.SubscriberCount(b) == 1 })

	finished := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Publish("server_probe", map[string]int64{"id": 1})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

// blockingWriter blocks every body write until block is closed.
type blockingWriter struct {
	*httptest.ResponseRecorder
	block chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	<-w.block
	return len(p), nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPairCommandKeepsTokenOutOfArgv (SEC-02, SEC-23): run the command
// shown to operators in bash with mktemp, curl, sha256sum and sudo stubbed:
// the script goes to a fresh mktemp file (a fixed /tmp name could be
// pre-created by a local user), the URL with the token reaches curl on
// stdin, curl's arguments do not contain the token and refuse redirects,
// the file is checked against the panel's hash, and only then run with
// sudo.
func TestPairCommandKeepsTokenOutOfArgv(t *testing.T) {
	const token = "ABCDE-FGHJK-MNPQR"
	sum, _ := fakeScriptSHA256(token)
	cmd := web.PairCommand(testPublicURL, token, sum)
	if strings.Contains(cmd, "/tmp/") {
		t.Fatalf("command uses a fixed /tmp path: %s", cmd)
	}
	const tmp = "/tmp/tmp.Acak123"
	script := `mktemp() { echo ` + tmp + `; }
curl() { printf 'ARGS:%s\n' "$*"; cat; }
sha256sum() { printf 'SUM:%s\n' "$*"; cat; }
sudo() { printf 'SUDO:%s\n' "$*"; }
` + cmd + "\n"
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	want := []string{
		"ARGS:-fsSL --max-redirs 0 -K - -o " + tmp,
		`url = "` + testPublicURL + `/agent/install?token=` + token + `"`,
		"SUM:-c -",
		sum + "  " + tmp,
		"SUDO:bash " + tmp,
	}
	if got := strings.Split(strings.TrimSpace(string(out)), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("command ran:\n%s\nwant:\n%s", out, strings.Join(want, "\n"))
	}
	// A failed check, or a failed mktemp, stops before sudo.
	for _, stubs := range []string{
		"mktemp() { echo " + tmp + "; }\ncurl() { cat >/dev/null; }\nsha256sum() { cat >/dev/null; return 1; }\n",
		"mktemp() { return 1; }\ncurl() { cat >/dev/null; }\nsha256sum() { cat >/dev/null; }\n",
	} {
		script = stubs + "sudo() { echo SUDO; }\n" + cmd + "\n"
		if out, _ := exec.Command("bash", "-c", script).CombinedOutput(); strings.Contains(string(out), "SUDO") {
			t.Fatalf("sudo ran after a failed step:\n%s", out)
		}
	}
}
