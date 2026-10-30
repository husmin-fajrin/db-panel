package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPairRefusesRedirect (SEC-23): the pairing request carries the token,
// so a redirect (e.g. from an on-path attacker to its own host) is not
// followed, whatever client the caller passes.
func TestPairRefusesRedirect(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	t.Cleanup(elsewhere.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/agent/pair", http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)

	for name, client := range map[string]*http.Client{"default": nil, "caller's": {}} {
		_, err := Pair(context.Background(), redirecting.URL, "AAAAA-BBBBB-CCCCC", t.TempDir(), client, PairOptions{})
		if err == nil || !strings.Contains(err.Error(), "panel mengalihkan permintaan pairing; ditolak") {
			t.Errorf("%s client: err = %v", name, err)
		}
	}
	if reached.Load() {
		t.Fatal("the redirect was followed")
	}
}
