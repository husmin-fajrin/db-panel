package auth

import (
	"context"
	"net/http"
	"net/url"
)

// CookieName is the name of the session cookie over plain HTTP.
const CookieName = "dbpanel_session"

// SecureCookieName is the name of the session cookie when it is set with
// Secure (the request came over TLS). Browsers only accept a "__Host-"
// cookie that is Secure, has Path=/ and no Domain, which pins it to this
// exact origin; over plain HTTP the prefix cannot be used.
const SecureCookieName = "__Host-" + CookieName

// SessionToken returns the session token the request carries, preferring
// the "__Host-" cookie.
func SessionToken(r *http.Request) (string, bool) {
	for _, name := range []string{SecureCookieName, CookieName} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value, true
		}
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

// SetSessionCookie sets the session cookie for token. With secure (TLS) the
// cookie is Secure and named SecureCookieName.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	name := CookieName
	if secure {
		name = SecureCookieName
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ClearSessionCookie expires the session cookie. With secure it also
// expires the "__Host-" cookie, which browsers only accept over TLS.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	if secure {
		http.SetCookie(w, &http.Cookie{
			Name:     SecureCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   true,
			MaxAge:   -1,
		})
	}
}
