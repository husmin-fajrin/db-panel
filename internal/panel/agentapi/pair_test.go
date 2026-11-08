package agentapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/pki"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestLocalPairTokenLoopbackOnly (SEC-63g): the panel host's own server
// pairs with the token the panel writes to local-pair-token, and that
// token is accepted only from a loopback address. From any other address
// it is refused, without being used up (the local agent can still pair
// with it), and the refusal is in the audit log. Other servers' tokens
// pair from anywhere.
func TestLocalPairTokenLoopbackOnly(t *testing.T) {
	var deps *agentapi.Deps
	e := newEnv(t, func(d *agentapi.Deps) {
		d.LocalServerID = 1
		d.PublicHost = "192.0.2.7"
		deps = d
	})
	local, err := e.st.CreateServer("host-panel")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := e.st.CreateServer("db-2")
	if err != nil {
		t.Fatal(err)
	}
	h := agentapi.PairHandler(*deps)
	pair := func(token, from string) *httptest.ResponseRecorder {
		t.Helper()
		_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(proto.PairRequest{Token: token, Hostname: "h", Arch: "amd64", CSRPEM: string(csrPEM)})
		r := httptest.NewRequest(http.MethodPost, "/agent/pair", bytes.NewReader(body))
		r.RemoteAddr = from
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	_, localToken := e.newToken(t, local.ID)

	// A peek that fails (a database error) does not let the token
	// through: 500, nothing used up (fix round 1).
	restore := agentapi.SetPeekPairTokenErr(errors.New("database is locked"))
	if rec := pair(localToken, "192.0.2.50:4321"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("local token from 192.0.2.50 with a failing peek: %d %s", rec.Code, rec.Body)
	}
	// A peek that misses the token (it reads as invalid, as in a race)
	// leaves the refusal to the check after it is used.
	agentapi.SetPeekPairTokenErr(store.ErrTokenInvalid)
	_, raced := e.newToken(t, local.ID)
	rec := pair(raced, "192.0.2.51:4321")
	restore()
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "hanya berlaku dari host panel") {
		t.Fatalf("local token after a missed peek: %d %s", rec.Code, rec.Body)
	}
	if srv, _ := e.st.GetServer(local.ID); srv.Paired {
		t.Fatal("local server paired from another host after a missed peek")
	}
	_, localToken = e.newToken(t, local.ID)
	for _, from := range []string{"192.0.2.50:4321", "[2001:db8::5]:4321", "10.0.0.1:1"} {
		rec := pair(localToken, from)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "hanya berlaku dari host panel") {
			t.Fatalf("local token from %s: %d %s", from, rec.Code, rec.Body)
		}
	}
	if _, err := e.st.PeekPairToken(localToken, time.Now()); err != nil {
		t.Fatalf("the refused attempts used the local token up: %v", err)
	}
	if srv, _ := e.st.GetServer(local.ID); srv.Paired {
		t.Fatal("local server paired from another host")
	}
	entries, err := e.st.ListAudit(0, time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range entries {
		if a.Action == "agent_pair" && a.TargetID == local.ID && a.IP != "" && !strings.HasPrefix(a.IP, "127.") &&
			strings.Contains(a.Outcome, "loopback") {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%d refusal audit entries, want 4 (3 + the missed peek): %+v", n, entries)
	}

	// From loopback it pairs, at the panel's public host.
	if rec := pair(localToken, "[::1]:5555"); rec.Code != http.StatusOK {
		t.Fatalf("local token from ::1: %d %s", rec.Code, rec.Body)
	}
	if srv, _ := e.st.GetServer(local.ID); !srv.Paired || srv.Address != "192.0.2.7" {
		t.Fatalf("local server after pairing = %+v", srv)
	}
	_, localToken = e.newToken(t, local.ID)
	if rec := pair(localToken, "127.0.0.2:5555"); rec.Code != http.StatusOK {
		t.Fatalf("local token from 127.0.0.2: %d %s", rec.Code, rec.Body)
	}

	// Another server's token pairs from its own address.
	_, remoteToken := e.newToken(t, remote.ID)
	if rec := pair(remoteToken, "192.0.2.50:4321"); rec.Code != http.StatusOK {
		t.Fatalf("remote token: %d %s", rec.Code, rec.Body)
	}
	if srv, _ := e.st.GetServer(remote.ID); !srv.Paired || srv.Address != "192.0.2.50" {
		t.Fatalf("remote server after pairing = %+v", srv)
	}
}
