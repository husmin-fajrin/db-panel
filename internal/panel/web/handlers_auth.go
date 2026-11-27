package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	msgLoginFailed  = "Nama pengguna atau kata sandi salah"
	msgLoginBlocked = "Terlalu banyak percobaan, coba lagi dalam 15 menit"
	msgLoginBusy    = "Server sibuk, coba lagi sebentar"
	msgLoginTooLong = "Nama pengguna atau kata sandi terlalu panjang"

	// Longer input is refused before any limiter or hashing: a username
	// would otherwise become a limiter key, and bcrypt ignores bytes past 72.
	maxUsernameLen = 64
	maxPasswordLen = 72
	msgForbidden   = "permintaan ditolak"
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

	if len(username) > maxUsernameLen || len(password) > maxPasswordLen {
		retry(http.StatusBadRequest, msgLoginTooLong)
		return
	}
	ip := clientIP(r)
	if d, ok := a.validDevice(r, username); ok {
		a.deviceLogin(w, r, d, username, password, next, ip, retry)
		return
	}
	addrKey := ipKey(ip)
	userKey := usernameKey(username)
	// Every attempt counts per address; per username only wrong passwords
	// count (below), so a flood refused as busy cannot lock an account
	// out.
	if !a.Limiter.Allow(addrKey) || a.UserLimiter.Blocked(userKey) {
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

	user, ok, err := a.checkLogin(username, password)
	if err != nil {
		a.internalError(w, "get user", err)
		return
	}
	if !ok {
		a.UserLimiter.Fail(userKey)
		a.loginFailed(r, username, user, ip)
		retry(http.StatusOK, msgLoginFailed)
		return
	}
	a.Limiter.Reset(addrKey)
	a.UserLimiter.Reset(userKey)
	// No valid device cookie came for this name: issue one (K5). This
	// browser and address have now logged in with it.
	if a.DeviceKey != nil {
		v, d := auth.NewDevice(a.DeviceKey, user.Username, a.Now())
		auth.SetDeviceCookie(w, v)
		a.devicePairs.add(devicePairKey(d.ID, ip))
	}
	a.loginSucceeded(w, r, user, next, ip)
}

// validDevice returns the device cookie the request carries when it is
// valid for the typed username: signed with DeviceKey, under a year old,
// issued for that name (any case) and after the last password reset.
func (a *app) validDevice(r *http.Request, username string) (auth.Device, bool) {
	if a.DeviceKey == nil {
		return auth.Device{}, false
	}
	v, ok := auth.DeviceCookie(r)
	if !ok {
		return auth.Device{}, false
	}
	d, ok := auth.ParseDevice(a.DeviceKey, v, a.Now())
	if !ok || !strings.EqualFold(d.Username, username) || !a.IssuedValid(d.Issued) {
		return auth.Device{}, false
	}
	return d, true
}

// deviceLogin is a login with a valid device cookie d (spec 5.1). It never
// reads the address or username limits nor loginSem, and its wrong
// passwords count only against its own budgets (K6): 5 per cookie and
// address (reserved before the check, given back when the check never
// runs) and 20 per cookie for all addresses. Once that total is used up,
// only the pairs that have never logged in since the panel started are
// refused. It waits at most deviceSlotWait for one of its own check slots
// (K7).
func (a *app) deviceLogin(w http.ResponseWriter, r *http.Request, d auth.Device, username, password, next, ip string,
	retry func(int, string)) {
	pair := devicePairKey(d.ID, ip)
	totalUsed := func() bool { return a.DeviceTotalLimiter.Blocked(d.ID) && !a.devicePairs.has(pair) }
	if totalUsed() || !a.DevicePairLimiter.Allow(pair) {
		a.Logger.Warn("login rate limited", "ip", ip, "device", d.ID)
		retry(http.StatusTooManyRequests, msgLoginBlocked)
		return
	}
	timer := time.NewTimer(deviceSlotWait)
	defer timer.Stop()
	select {
	case a.deviceSem <- struct{}{}:
		defer func() { <-a.deviceSem }()
	case <-timer.C:
		a.DevicePairLimiter.Refund(pair)
		a.Logger.Warn("login refused: device password checks busy", "ip", ip, "device", d.ID)
		retry(http.StatusTooManyRequests, msgLoginBusy)
		return
	case <-r.Context().Done():
		a.DevicePairLimiter.Refund(pair)
		retry(http.StatusTooManyRequests, msgLoginBusy)
		return
	}
	// The total may have been used up while this login waited.
	if totalUsed() {
		a.DevicePairLimiter.Refund(pair)
		a.Logger.Warn("login rate limited", "ip", ip, "device", d.ID)
		retry(http.StatusTooManyRequests, msgLoginBlocked)
		return
	}

	user, ok, err := a.checkLogin(username, password)
	if err != nil {
		a.DevicePairLimiter.Refund(pair)
		a.internalError(w, "get user", err)
		return
	}
	if !ok {
		a.DeviceTotalLimiter.Fail(d.ID)
		a.loginFailed(r, username, user, ip)
		retry(http.StatusOK, msgLoginFailed)
		return
	}
	a.DevicePairLimiter.Reset(pair)
	a.devicePairs.add(pair)
	a.loginSucceeded(w, r, user, next, ip)
}

// checkLogin checks password for username. bcrypt always runs, against
// dummyHash for an unknown user, so the response time does not reveal
// which usernames exist. user.Username is "" when the user does not exist.
func (a *app) checkLogin(username, password string) (user store.User, ok bool, err error) {
	hash := dummyHash
	user, err = a.Store.GetUser(username)
	switch {
	case err == nil:
		hash = user.PasswordHash
	case errors.Is(err, store.ErrNotFound):
		user = store.User{}
	default:
		return store.User{}, false, err
	}
	return user, a.CheckPassword(hash, password) && user.Username != "", nil
}

// loginFailed audits and logs a wrong password. Only attempts that reached
// the password check are audited: those refused earlier (rate limits,
// busy) cost an attacker nothing and would fill the append-only log.
func (a *app) loginFailed(r *http.Request, username string, user store.User, ip string) {
	who := auditUsername(username, user.Username != "")
	a.auditAs(r, who, "login_failed", auditSession, 0, nil, msgLoginFailed)
	// Never the typed name: it may be a password (SEC-44).
	a.Logger.Info("login failed", "username", journalUsername(who), "ip", ip)
}

// loginSucceeded starts the session and redirects to next.
func (a *app) loginSucceeded(w http.ResponseWriter, r *http.Request, user store.User, next, ip string) {
	// Secure always: the panel serves the UI over HTTPS only (plain HTTP
	// is redirected before it reaches here).
	auth.SetSessionCookie(w, a.Sessions.Create(user.Username))
	a.auditAs(r, user.Username, "login", auditSession, 0, nil, auditOK)
	a.Logger.Info("login", "username", user.Username, "ip", ip)
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// usernameKey is the per-username limiter key: the hex SHA-256 of the
// lowercased username, fixed-size whatever was typed.
func usernameKey(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(username)))
	return hex.EncodeToString(sum[:])
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if tok, ok := auth.SessionToken(r); ok {
		if username, ok := a.Sessions.Get(tok); ok {
			a.auditAs(r, username, "logout", auditSession, 0, nil, auditOK)
		}
		a.Sessions.Delete(tok)
	}
	auth.ClearSessionCookie(w)
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
// Referer) is not this host over HTTPS. It complements the SameSite=Lax
// session cookie as CSRF protection; a request carrying neither header is
// rejected. The UI is served over HTTPS only (the plain-HTTP side of the
// port answers the certificate page and redirects), so http://<this host>
// is another origin: a page an attacker on the network path serves there
// must not post here.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		u, err := url.Parse(src)
		if src == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.Host != r.Host {
			http.Error(w, msgForbidden, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
