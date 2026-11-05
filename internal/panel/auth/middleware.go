package auth

import (
	"context"
	"net/http"
	"net/url"
)

// CookieName is the name of the session cookie the panel set while it
// served plain HTTP. It is no longer read; logout still expires it.
const CookieName = "dbpanel_session"

// SecureCookieName is the name of the session cookie. Browsers only accept
// a "__Host-" cookie that is Secure, has Path=/ and no Domain, which pins it
// to this exact origin.
const SecureCookieName = "__Host-" + CookieName

// SessionToken returns the session token the request carries in the
// "__Host-" cookie.
func SessionToken(r *http.Request) (string, bool) {
	if c, err := r.Cookie(SecureCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

type ctxKey struct{}

// RequireLogin redirects requests without a valid session to
// loginPath?next=<request URI> (303) and otherwise stores the username in the
// request context.
func RequireLogin(s *Sessions, loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tok, ok := SessionToken(r); ok {
				if user, ok := s.Get(tok); ok {
					ctx := context.WithValue(r.Context(), ctxKey{}, user)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			http.Redirect(w, r, loginPath+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		})
	}
}

// UsernameFrom returns the authenticated username, or "" when absent.
func UsernameFrom(ctx context.Context) string {
	u, _ := ctx.Value(ctxKey{}).(string)
	return u
}

// SetSessionCookie sets the session cookie for token: Secure and named
// SecureCookieName, since the panel serves its UI over HTTPS only.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SecureCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}

// ClearSessionCookie expires the session cookie, and the plain one a
// browser may still hold from before HTTPS.
func ClearSessionCookie(w http.ResponseWriter) {
	for _, c := range []*http.Cookie{
		{Name: SecureCookieName, Secure: true},
		{Name: CookieName},
	} {
		c.Path, c.HttpOnly, c.SameSite, c.MaxAge = "/", true, http.SameSiteLaxMode, -1
		http.SetCookie(w, c)
	}
}
