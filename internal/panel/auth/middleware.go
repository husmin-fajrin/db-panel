package auth

import (
	"context"
	"net/http"
	"net/url"
)

// CookieName is the name of the session cookie.
const CookieName = "dbpanel_session"

type ctxKey struct{}

// RequireLogin redirects requests without a valid session to
// loginPath?next=<request URI> (303) and otherwise stores the username in the
// request context.
func RequireLogin(s *Sessions, loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(CookieName); err == nil {
				if user, ok := s.Get(c.Value); ok {
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

// SetSessionCookie sets the session cookie for token.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
