package web_test

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
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
	l        *fakeListener
	hub      *hub.Hub
	sessions *auth.Sessions
	broker   *web.Broker
	h        http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
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
		l:        l,
		hub:      hub.New(l),
		sessions: auth.NewSessions(time.Hour, time.Now),
		broker:   web.NewBroker(),
	}
	e.h = web.NewRouter(web.Deps{
		Store:     st,
		Hub:       e.hub,
		Sessions:  e.sessions,
		Limiter:   auth.NewRateLimiter(5, 15*time.Minute, time.Now),
		PublicURL: func() string { return testPublicURL },
		Events:    e.broker,
		Now:       time.Now,
		Version:   "v-test",
		Logger:    slog.New(slog.DiscardHandler),
	})
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
		`data-theme="corporate"`, "/static/app.css", "/static/vendor/alpine.min.js")
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
	wantBody(t, rec, "db-utama", "/agent/install?token=",
		"curl -fsSL &#39;"+testPublicURL+"/agent/install?token="+tok.Token+"&#39; | sudo bash",
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
		"/var/lib/mysql", "500.0 GB", "2.0 GB", "8.0.39", "mysqld", "3306", ">Online<")
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

func TestFramingForbidden(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/login", "/servers"} {
		rec := e.do("GET", p, nil, e.cookie())
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("GET %s: X-Frame-Options = %q, want DENY", p, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("GET %s: Content-Security-Policy = %q", p, got)
		}
	}
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/static/app.css", "/static/vendor/alpine.min.js"} {
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
