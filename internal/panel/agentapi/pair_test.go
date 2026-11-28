package agentapi_test

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

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
	// SEC-77: each refusal has its journald copy, with the stored ts
	// and the row's hash.
	copies := regexp.MustCompile(`msg=audit stored=true id=\d+ ts=\S+Z hash=[0-9a-f]{64} username=agent .*action=agent_pair .*outcome=".*loopback`)
	if got := len(copies.FindAllString(e.logs.String(), -1)); got != 4 {
		t.Fatalf("%d journald copies of the refusals, want 4:\n%s", got, e.logs.String())
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

// postPair posts body (JSON-encoded) to h from the address from.
func postPair(t *testing.T, h http.Handler, body any, from string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/agent/pair", bytes.NewReader(b))
	r.RemoteAddr = from
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// newCSR is a fresh agent CSR in PEM.
func newCSR(t *testing.T) string {
	t.Helper()
	_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	return string(csrPEM)
}

// pairDetail is the detail of an agent_pair audit row.
type pairDetail struct {
	Address      string `json:"address"`
	Replaced     *bool  `json:"replaced"`
	CertSerial   string `json:"cert_serial"`
	AgentVersion string `json:"agent_version"`
	Arch         string `json:"arch"`
	Hostname     string `json:"hostname"`
}

// pairRows are the agent_pair audit rows of server id, oldest first.
func pairRows(t *testing.T, st *store.Store, id int64) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(id, time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Action == "agent_pair" {
			out = append(out, entries[i])
		}
	}
	return out
}

func decodePairDetail(t *testing.T, e store.AuditEntry) pairDetail {
	t.Helper()
	var d pairDetail
	if err := json.Unmarshal([]byte(e.DetailJSON), &d); err != nil {
		t.Fatalf("agent_pair detail %q: %v", e.DetailJSON, err)
	}
	return d
}

// respSerial is the serial (as the store keeps it) of the certificate in
// a pairing answer.
func respSerial(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp proto.PairResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(resp.CertPEM))
	if block == nil {
		t.Fatal("pairing answer without a certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%032x", cert.SerialNumber)
}

// TestPairWritesAuditRow (spec 5.1, R-182): every pairing writes one
// agent_pair row: the address, whether it replaced an agent, the serial of
// the certificate it got, the agent's version, architecture and hostname.
func TestPairWritesAuditRow(t *testing.T) {
	var deps *agentapi.Deps
	e := newEnv(t, func(d *agentapi.Deps) { deps = d })
	h := agentapi.PairHandler(*deps)
	id, token := e.newToken(t, 0)
	pair := func(token string) string {
		t.Helper()
		rec := postPair(t, h, proto.PairRequest{Token: token, Hostname: "srv-db", Arch: "arm64", AgentVersion: "v0.4.0",
			CSRPEM: newCSR(t)}, "192.0.2.20:4000")
		if rec.Code != http.StatusOK {
			t.Fatalf("pair: %d %s", rec.Code, rec.Body)
		}
		return respSerial(t, rec)
	}
	check := func(row store.AuditEntry, serial string, replaced bool) {
		t.Helper()
		if row.Username != "agent" || row.IP != "192.0.2.20" || row.TargetKind != "server" || row.TargetID != id ||
			row.Outcome != "ok" {
			t.Errorf("agent_pair row = %+v", row)
		}
		d := decodePairDetail(t, row)
		if d.Replaced == nil || *d.Replaced != replaced || d.CertSerial != serial || d.Address != "192.0.2.20" ||
			d.AgentVersion != "v0.4.0" || d.Arch != "arm64" || d.Hostname != "srv-db" {
			t.Errorf("agent_pair detail = %s, want replaced %v and serial %s", row.DetailJSON, replaced, serial)
		}
	}

	first := pair(token)
	rows := pairRows(t, e.st, id)
	if len(rows) != 1 {
		t.Fatalf("%d agent_pair rows after the first pairing, want 1", len(rows))
	}
	check(rows[0], first, false)
	if srv, _ := e.st.GetServer(id); srv.CertSerial != first {
		t.Fatalf("stored serial %s, issued %s", srv.CertSerial, first)
	}

	_, token = e.newToken(t, id)
	second := pair(token)
	rows = pairRows(t, e.st, id)
	if len(rows) != 2 {
		t.Fatalf("%d agent_pair rows after re-pairing, want 2", len(rows))
	}
	check(rows[1], second, true)
	if first == second {
		t.Fatal("re-pairing kept the serial")
	}

	// A refused token writes no success row.
	if rec := postPair(t, h, proto.PairRequest{Token: token, Hostname: "x", CSRPEM: newCSR(t)}, "192.0.2.20:1"); rec.Code != http.StatusForbidden {
		t.Fatalf("used token: %d", rec.Code)
	}
	if n := len(pairRows(t, e.st, id)); n != 2 {
		t.Fatalf("%d agent_pair rows after a refused token, want 2", n)
	}
	// Its journald copy is there too (SEC-77).
	copies := regexp.MustCompile(`msg=audit stored=true .*username=agent .*action=agent_pair .*outcome=ok`)
	if got := len(copies.FindAllString(e.logs.String(), -1)); got != 2 {
		t.Fatalf("%d journald copies of the pairings, want 2:\n%s", got, e.logs.String())
	}
}

// loggedValues are the values of key in the text log's lines.
func loggedValues(t *testing.T, logs, key string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?:^| )` + regexp.QuoteMeta(key) + `=("(?:[^"\\]|\\.)*"|\S*)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(logs, -1) {
		v := m[1]
		if strings.HasPrefix(v, `"`) {
			var err error
			if v, err = strconv.Unquote(v); err != nil {
				t.Fatalf("log value %s: %v", m[1], err)
			}
		}
		out = append(out, v)
	}
	return out
}

// wantBounded fails unless s is one line of n runes, without control
// characters, starting with prefix.
func wantBounded(t *testing.T, what, s string, n int, prefix string) {
	t.Helper()
	if got := utf8.RuneCountInString(s); got != n {
		t.Errorf("%s: %d runes, want %d", what, got, n)
	}
	if strings.ContainsFunc(s, unicode.IsControl) || strings.ContainsAny(s, "  ‮") {
		t.Errorf("%s: %q has a control character or line break", what, s)
	}
	if !strings.HasPrefix(s, prefix) {
		t.Errorf("%s: %.40q does not start with %q", what, s, prefix)
	}
}

// TestPairHostnameBounded (spec 5.1): what the pairing request says of the
// machine is untrusted: a 60 KiB hostname with line breaks is one line of
// 255 runes in the audit rows (success and refusal) and the journal;
// agent_version and arch are cut at 64 runes.
func TestPairHostnameBounded(t *testing.T) {
	var deps *agentapi.Deps
	e := newEnv(t, func(d *agentapi.Deps) {
		d.LocalServerID = 1
		deps = d
	})
	local, err := e.st.CreateServer("host-panel")
	if err != nil {
		t.Fatal(err)
	}
	h := agentapi.PairHandler(*deps)
	long := "srv\nbaris-dua\r\n ‮" + strings.Repeat("é", 1000) + strings.Repeat("x", 60<<10-2000)
	const wantPrefix = "srv baris-dua"
	longVersion := "v1\n" + strings.Repeat("9", 200)
	longArch := "amd64\x00" + strings.Repeat("a", 200)

	// Refused: the panel host's token from another address.
	_, localToken := e.newToken(t, local.ID)
	if rec := postPair(t, h, proto.PairRequest{Token: localToken, Hostname: long, CSRPEM: newCSR(t)}, "192.0.2.50:1"); rec.Code != http.StatusForbidden {
		t.Fatalf("local token from afar: %d %s", rec.Code, rec.Body)
	}
	// Refused: an unknown token.
	if rec := postPair(t, h, proto.PairRequest{Token: "AAAAA-BBBBB-CCCCC", Hostname: long, CSRPEM: newCSR(t)}, "192.0.2.51:1"); rec.Code != http.StatusForbidden {
		t.Fatalf("unknown token: %d %s", rec.Code, rec.Body)
	}
	// Paired.
	id, token := e.newToken(t, 0)
	rec := postPair(t, h, proto.PairRequest{Token: token, Hostname: long, Arch: longArch, AgentVersion: longVersion,
		CSRPEM: newCSR(t)}, "192.0.2.52:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}

	refused := pairRows(t, e.st, local.ID)
	if len(refused) != 1 {
		t.Fatalf("%d refusal rows, want 1", len(refused))
	}
	wantBounded(t, "refusal row hostname", decodePairDetail(t, refused[0]).Hostname, 255, wantPrefix)
	paired := pairRows(t, e.st, id)
	if len(paired) != 1 {
		t.Fatalf("%d pairing rows, want 1", len(paired))
	}
	d := decodePairDetail(t, paired[0])
	wantBounded(t, "pairing row hostname", d.Hostname, 255, wantPrefix)
	wantBounded(t, "pairing row agent_version", d.AgentVersion, 64, "v1 9")
	wantBounded(t, "pairing row arch", d.Arch, 64, "amd64 a")

	logs := e.logs.String()
	hostnames := loggedValues(t, logs, "hostname")
	// The three journal lines (refused from afar, unknown token, paired).
	if len(hostnames) != 3 {
		t.Fatalf("%d hostname values in the journal, want 3:\n%.2000s", len(hostnames), logs)
	}
	for i, v := range hostnames {
		wantBounded(t, fmt.Sprintf("journal hostname %d", i), v, 255, wantPrefix)
	}
	for _, key := range []string{"agent_version", "arch"} {
		vs := loggedValues(t, logs, key)
		if len(vs) != 1 {
			t.Fatalf("%d %s values in the journal, want 1", len(vs), key)
		}
		if n := utf8.RuneCountInString(vs[0]); n != 64 || strings.ContainsFunc(vs[0], unicode.IsControl) {
			t.Errorf("journal %s = %q (%d runes)", key, vs[0], n)
		}
	}
	// The audit rows' journald copies carry the same cut detail.
	if strings.Contains(logs, strings.Repeat("x", 300)) {
		t.Error("the journal has more of the hostname than 255 runes")
	}
}

// TestPairOldAgentRequest (spec 5.12): a 0.2.0 agent's request, without
// arch and agent_version, still pairs, and its row says so with empty
// values.
func TestPairOldAgentRequest(t *testing.T) {
	var deps *agentapi.Deps
	e := newEnv(t, func(d *agentapi.Deps) { deps = d })
	id, token := e.newToken(t, 0)
	rec := postPair(t, agentapi.PairHandler(*deps),
		map[string]string{"token": token, "hostname": "lama", "csr_pem": newCSR(t)}, "192.0.2.9:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("0.2.0 pairing: %d %s", rec.Code, rec.Body)
	}
	if srv, _ := e.st.GetServer(id); !srv.Paired || srv.CertSerial != respSerial(t, rec) {
		t.Fatalf("server after a 0.2.0 pairing = %+v", srv)
	}
	rows := pairRows(t, e.st, id)
	if len(rows) != 1 || rows[0].Outcome != "ok" {
		t.Fatalf("agent_pair rows = %+v", rows)
	}
	if d := decodePairDetail(t, rows[0]); d.AgentVersion != "" || d.Arch != "" || d.Hostname != "lama" ||
		d.Replaced == nil || *d.Replaced {
		t.Errorf("agent_pair detail = %s", rows[0].DetailJSON)
	}
}
