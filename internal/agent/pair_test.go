package agent

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/pki"
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

// pinOf is the SPKI pin of the TLS test server's certificate.
func pinOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	pin, err := pki.SPKIPin(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

// TestPairOverHTTPSWithPin: with a web pin, the panel's certificate is
// accepted only when its key matches the pin, whatever CA signed it, and a
// mismatch stops the handshake before the request (with the token) is sent.
func TestPairOverHTTPSWithPin(t *testing.T) {
	var reached atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"token ditolak"}`))
	}))
	t.Cleanup(ts.Close)
	pin := pinOf(t, ts)

	// Right pin: the request reaches the panel (which refuses the token).
	_, err := Pair(context.Background(), ts.URL, "AAAAA-BBBBB-CCCCC", t.TempDir(), nil, PairOptions{WebPin: pin})
	if err == nil || !strings.Contains(err.Error(), "token ditolak oleh panel") {
		t.Fatalf("Pair with the right pin: err = %v, want the panel's refusal", err)
	}
	if reached.Load() != 1 {
		t.Fatalf("requests with the right pin = %d, want 1", reached.Load())
	}

	// Wrong pin: refused in the handshake, nothing sent.
	wrong := "sha256//" + strings.Repeat("A", 43) + "="
	_, err = Pair(context.Background(), ts.URL, "AAAAA-BBBBB-CCCCC", t.TempDir(), nil, PairOptions{WebPin: wrong})
	if !errors.Is(err, ErrWebPinMismatch) {
		t.Fatalf("Pair with a wrong pin: err = %v, want ErrWebPinMismatch", err)
	}
	if reached.Load() != 1 {
		t.Fatal("the request was sent despite a wrong pin")
	}

	// No pin, certificate from an unknown CA: a clear error, nothing sent.
	_, err = Pair(context.Background(), ts.URL, "AAAAA-BBBBB-CCCCC", t.TempDir(), nil, PairOptions{})
	if err == nil || !strings.Contains(err.Error(), "sertifikat panel tidak dikenal") {
		t.Fatalf("Pair over https without a pin: err = %v", err)
	}
	if reached.Load() != 1 {
		t.Fatal("the request was sent to an unverified panel")
	}

	// A pin with a plain http URL is refused: the pin would protect nothing.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	t.Cleanup(plain.Close)
	_, err = Pair(context.Background(), plain.URL, "AAAAA-BBBBB-CCCCC", t.TempDir(), nil, PairOptions{WebPin: pin})
	if err == nil || !strings.Contains(err.Error(), "https") || reached.Load() != 1 {
		t.Fatalf("Pair with a pin over http: err = %v, requests %d", err, reached.Load())
	}
}
