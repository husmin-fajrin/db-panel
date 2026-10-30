package web

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	msgLoginFailed  = "Nama pengguna atau kata sandi salah"
	msgLoginBlocked = "Terlalu banyak percobaan, coba lagi dalam 15 menit"
	msgLoginBusy    = "Server sibuk, coba lagi sebentar"
	msgForbidden    = "permintaan ditolak"
)

// dummyHash is a bcrypt hash (same cost as auth.HashPassword) checked when
// the username does not exist, so the response time does not reveal which
// usernames are valid.
const dummyHash = "$2a$12$J.4EhMrDlN6YD2ggaV3rZOhGkPzx8x.p0.WwMndU9Tp0bvw78LNYy"

type loginPage struct {
	Page
	Next         string
	FormUsername string
}

func (a *app) loginForm(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, "login", loginPage{Page: a.page(r), Next: r.URL.Query().Get("next")})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")
	retry := func(status int, msg string) {
		p := loginPage{Page: a.page(r), Next: next, FormUsername: username}
		p.Flash = msg
		a.render.renderStatus(w, status, "login", p)
	}

	ip := clientIP(r)
	// Per IP, and per username so that rotating source addresses (IPv6
	// link-local, IPv4 aliases) does not buy more guesses for one account.
	if !a.Limiter.Allow("ip:"+ip) || !a.Limiter.Allow("user:"+username) {
		a.Logger.Warn("login rate limited", "ip", ip)
		retry(http.StatusTooManyRequests, msgLoginBlocked)
		return
	}
	// Each check costs ~0.3 s of CPU; refuse rather than queue when the
	// slots are taken, so a flood cannot saturate the panel host.
	select {
	case a.loginSem <- struct{}{}:
		defer func() { <-a.loginSem }()
	default:
		a.Logger.Warn("login refused: password checks busy", "ip", ip)
		retry(http.StatusTooManyRequests, msgLoginBusy)
		return
	}

	hash := dummyHash
	user, err := a.Store.GetUser(username)
	switch {
	case err == nil:
		hash = user.PasswordHash
	case !errors.Is(err, store.ErrNotFound):
		a.internalError(w, "get user", err)
		return
	}
	// Always run bcrypt, even for an unknown user.
	if ok := a.CheckPassword(hash, password); !ok || err != nil {
		a.Logger.Info("login failed", "username", username, "ip", ip)
		retry(http.StatusOK, msgLoginFailed)
		return
	}

	a.Limiter.Reset("ip:" + ip)
	a.Limiter.Reset("user:" + username)
	auth.SetSessionCookie(w, a.Sessions.Create(user.Username), r.TLS != nil)
	a.Logger.Info("login", "username", user.Username, "ip", ip)
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if tok, ok := auth.SessionToken(r); ok {
		a.Sessions.Delete(tok)
	}
	auth.ClearSessionCookie(w, r.TLS != nil)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// clientIP is the host part of the TCP peer address. X-Forwarded-For is
// ignored: it is client-controlled and would let an attacker pick their own
// rate-limit key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// safeNext returns next if it is a path on this site, else "/".
//   - "//host" is another host to browsers, and so is anything with a
//     backslash ("/\host"), raw or percent-encoded, which browsers treat as "/".
//   - url.Parse rejects control characters, which browsers strip ("/\t/host").
//   - http.Redirect path.Cleans the part before "?", so that result must pass
//     the same checks ("/./\host" becomes "/\host").
func safeNext(next string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" ||
		strings.Contains(next, `\`) || strings.Contains(u.Path, `\`) {
		return "/"
	}
	p, _, _ := strings.Cut(next, "?")
	for _, v := range []string{next, path.Clean(p)} {
		if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
			return "/"
		}
	}
	return next
}

// sameOrigin rejects state-changing requests whose Origin (or, without one,
// Referer) is not this host. It complements the SameSite=Lax session cookie
// as CSRF protection; a request carrying neither header is rejected.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		u, err := url.Parse(src)
		if src == "" || err != nil || u.Host == "" || u.Host != r.Host {
			http.Error(w, msgForbidden, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
