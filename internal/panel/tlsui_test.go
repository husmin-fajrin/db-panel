package panel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/panel/agentapi"
	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// trusted holds the web CAs of every panel the tests built (newPanel), for
// testTransport.
var trusted = struct {
	mu   sync.Mutex
	pool *x509.CertPool
}{pool: x509.NewCertPool()}

// trustPanel makes testTransport trust p's web CA.
func trustPanel(p *Panel) {
	trusted.mu.Lock()
	defer trusted.mu.Unlock()
	trusted.pool.AppendCertsFromPEM(p.certs.CAPEM())
}

// testTransport speaks HTTPS to any panel the tests built: the server's
// chain must lead to one of their web CAs. (The host name is checked by
// tlsClient, which trusts one panel only.)
var testTransport = &http.Transport{
	TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // replaced by VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			trusted.mu.Lock()
			pool := trusted.pool.Clone()
			trusted.mu.Unlock()
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter})
			return err
		},
	},
}

// testClient is http.DefaultClient for the panel's HTTPS.
var testClient = &http.Client{Transport: testTransport}

// tlsClient trusts p's web CA only, with the usual host name check, and
// follows no redirect.
func tlsClient(t *testing.T, p *Panel) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.certs.CAPEM()) {
		t.Fatal("web CA PEM does not parse")
	}
	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// plainClient speaks plain HTTP and follows no redirect.
var plainClient = &http.Client{
	Transport:     &http.Transport{},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// routeGoroutines counts the goroutines peeking at a connection's first
// byte.
func routeGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return bytes.Count(buf, []byte("panel.(*splitListener).route"))
}

// TestSplitListenerServesTLSAndHTTP: one port serves the UI over HTTPS
// with a certificate for the panel's address, and over plain HTTP only the
// certificate page, redirecting the rest; a connection that sends nothing,
// or half a request, is closed within 6 s.
func TestSplitListenerServesTLSAndHTTP(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	uiAddr, _ := startPanel(t, p)
	c := tlsClient(t, p)

	resp, err := c.Get("https://" + uiAddr + "/login")
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="password"`) {
		t.Fatalf("GET https /login = %d", resp.StatusCode)
	}

	resp, err = plainClient.Get("http://" + uiAddr + "/servers?x=1")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "https://"+uiAddr+"/servers?x=1" {
		t.Fatalf("GET http /servers = %d %q, want 301 to https", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = plainClient.Post("http://"+uiAddr+"/agent/pair", "application/json", strings.NewReader(`{"token":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("POST http /agent/pair = %d, want 301 (never served over plain HTTP)", resp.StatusCode)
	}

	for _, base := range []string{"http://" + uiAddr, "https://" + uiAddr} {
		hc := plainClient
		if strings.HasPrefix(base, "https") {
			hc = c
		}
		resp, err = hc.Get(base + "/sertifikat")
		if err != nil {
			t.Fatal(err)
		}
		if body := readAll(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, `href="/sertifikat/dbpanel-panel.crt"`) {
			t.Fatalf("GET %s/sertifikat = %d", base, resp.StatusCode)
		}
		resp, err = hc.Get(base + "/sertifikat/dbpanel-panel.crt")
		if err != nil {
			t.Fatal(err)
		}
		if body := readAll(t, resp); resp.StatusCode != http.StatusOK || body != string(p.certs.CAPEM()) ||
			resp.Header.Get("Content-Type") != "application/x-x509-ca-cert" {
			t.Fatalf("GET %s/sertifikat/dbpanel-panel.crt = %d %q", base, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	resp, err = plainClient.Get("http://" + uiAddr + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET http /static/app.css = %d", resp.StatusCode)
	}

	// Silent clients are dropped.
	var wg sync.WaitGroup
	for _, send := range []string{"", "GET"} {
		wg.Go(func() {
			conn, err := net.Dial("tcp", uiAddr)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			start := time.Now()
			if send != "" {
				conn.Write([]byte(send))
			}
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			io.Copy(io.Discard, conn)
			if d := time.Since(start); d > 6*time.Second {
				t.Errorf("connection sending %q was held %v, want ≤ 6 s", send, d)
			}
		})
	}
	wg.Wait()
	waitFor(t, "no goroutine peeking at a connection", func() bool { return routeGoroutines() == 0 })
}

// TestSplitListenerCloseEndsPeeks: closing the listener closes connections
// still waiting for their first byte, and their goroutines end.
func TestSplitListenerCloseEndsPeeks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := newSplitListener(ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, "the connection to be peeked", func() bool { return routeGoroutines() == 1 })
	s.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read after Close = %v, want the connection closed", err)
	}
	waitFor(t, "the peek goroutine to end", func() bool { return routeGoroutines() == 0 })
	if _, err := s.tls.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v", err)
	}
}

// TestSessionCookieSecureAndHSTS: the session cookie is Secure (and
// "__Host-"), and HTTPS answers carry HSTS while plain HTTP ones do not.
func TestSessionCookieSecureAndHSTS(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "https://" + uiAddr
	c := login(t, o, base)
	if c.Name != auth.SecureCookieName || !c.Secure || !c.HttpOnly {
		t.Fatalf("session cookie = %+v, want Secure __Host- cookie", c)
	}
	resp := getWith(t, base+"/servers", c)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Fatalf("GET https /servers = %d, HSTS %q", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
	r, err := plainClient.Get("http://" + uiAddr + "/sertifikat")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, r)
	if h := r.Header.Get("Strict-Transport-Security"); h != "" {
		t.Fatalf("plain HTTP answer carries HSTS %q", h)
	}
}

func TestPublicURLIsHTTPS(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	uiAddr, _ := startPanel(t, p)
	if got, want := p.PublicURL(), "https://127.0.0.1:"+portOf(t, uiAddr); got != want {
		t.Fatalf("PublicURL = %q, want %q", got, want)
	}
}

// selfSigned returns a certificate (PEM) for names valid until notAfter and
// its key.
func selfSigned(t *testing.T, notAfter time.Time, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kantor"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// settingsToken asks POST /confirm, as the session c with the initial
// password, for a token for a certificate setting (target settings/0).
func settingsToken(t *testing.T, o Options, base string, c *http.Cookie, action string) string {
	t.Helper()
	form := url.Values{"action": {action}, "target_kind": {"settings"}, "target_id": {"0"},
		"password": {readInitialPassword(t, o)}}
	req, err := http.NewRequest("POST", base+"/confirm", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.AddCookie(c)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Token == "" {
		t.Fatalf("POST /confirm %s = %d: %s", action, resp.StatusCode, body)
	}
	return out.Token
}

// uploadCert posts a certificate and key to the settings page, logged in,
// with a confirmation token.
func uploadCert(t *testing.T, o Options, base string, c *http.Cookie, certPEM, keyPEM []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("confirm_token", settingsToken(t, o, base, c, "cert_upload"))
	for name, data := range map[string][]byte{"cert_file": certPEM, "key_file": keyPEM} {
		fw, err := mw.CreateFormFile(name, name+".pem")
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
	}
	mw.Close()
	req, err := http.NewRequest("POST", base+"/settings/cert", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", base)
	req.AddCookie(c)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readAll(t, resp)
}

// servedLeaf is the certificate the panel presents on a new connection.
func servedLeaf(t *testing.T, addr string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

// TestCustomCertValidation: an uploaded certificate whose key does not
// match, that has expired or that does not name the panel is refused with
// the reason; a good one is served from the next connection on, without a
// restart, until "Kembali ke sertifikat panel".
func TestCustomCertValidation(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "https://" + uiAddr
	c := login(t, o, base)
	year := time.Now().AddDate(1, 0, 0)

	good, goodKey := selfSigned(t, year, "127.0.0.1", "panel.kantor.example")
	_, otherKey := selfSigned(t, year, "127.0.0.1")
	expired, expiredKey := selfSigned(t, time.Now().Add(-time.Hour), "127.0.0.1")
	elsewhere, elsewhereKey := selfSigned(t, year, "192.0.2.5", "lain.example")
	for _, tc := range []struct {
		name      string
		cert, key []byte
		want      string
	}{
		{"kunci tidak cocok", good, otherKey, "Kunci tidak cocok dengan sertifikat"},
		{"kedaluwarsa", expired, expiredKey, "Sertifikat sudah kedaluwarsa"},
		{"tanpa alamat panel", elsewhere, elsewhereKey, "Sertifikat tidak memuat alamat panel 127.0.0.1"},
		{"bukan PEM", []byte("bukan sertifikat"), goodKey, "Berkas sertifikat bukan sertifikat PEM yang sah"},
	} {
		code, body := uploadCert(t, o, base, c, tc.cert, tc.key)
		if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d, want 400 with %q", tc.name, code, tc.want)
		}
	}
	if p.certInfo().Mode != httpsModePanel {
		t.Fatalf("mode after refused uploads = %q", p.certInfo().Mode)
	}

	// A chain after the leaf is accepted.
	chain := append(slices.Clone(good), p.certs.CAPEM()...)
	if code, body := uploadCert(t, o, base, c, chain, goodKey); code != http.StatusSeeOther {
		t.Fatalf("good upload = %d: %s", code, body)
	}
	if info := p.certInfo(); info.Mode != httpsModeCustom || !slices.Contains(info.Names, "panel.kantor.example") {
		t.Fatalf("certInfo after upload = %+v", info)
	}
	if leaf := servedLeaf(t, uiAddr); !slices.Contains(leaf.DNSNames, "panel.kantor.example") {
		t.Fatalf("served certificate %v, want the uploaded one", leaf.DNSNames)
	}
	for _, f := range []string{customCertFile, customKeyFile} {
		if fi, err := os.Stat(filepath.Join(o.ConfigDir, f)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want mode 0600", f, fi, err)
		}
	}
	if mode, _ := p.Store.GetSetting(store.SettingHTTPSMode); mode != httpsModeCustom {
		t.Fatalf("https_mode = %q", mode)
	}

	req, _ := http.NewRequest("POST", base+"/settings/cert-panel",
		strings.NewReader(url.Values{"confirm_token": {settingsToken(t, o, base, c, "cert_revert")}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.AddCookie(c)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if resp.StatusCode != http.StatusSeeOther || p.certInfo().Mode != httpsModePanel {
		t.Fatalf("back to the panel's certificate = %d, mode %q", resp.StatusCode, p.certInfo().Mode)
	}
	if leaf := servedLeaf(t, uiAddr); slices.Contains(leaf.DNSNames, "panel.kantor.example") {
		t.Fatal("still serving the uploaded certificate")
	}
}

// bufLogger logs into a buffer the test reads.
func bufLogger() (*slog.Logger, *syncBuffer) {
	var b syncBuffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

// TestBrokenCertFallsBackToPanelCert: at start, a broken uploaded
// certificate is skipped with a warning, and a broken panel certificate is
// issued again; HTTPS is served either way.
func TestBrokenCertFallsBackToPanelCert(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := selfSigned(t, time.Now().AddDate(1, 0, 0), "127.0.0.1")
	if problem, err := p1.certs.UseCustom(certPEM, keyPEM); problem != "" || err != nil {
		t.Fatalf("UseCustom: %q %v", problem, err)
	}
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{customCertFile, webCertFile} {
		if err := os.WriteFile(filepath.Join(o.ConfigDir, f), []byte("rusak"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logs *syncBuffer
	o.Logger, logs = bufLogger()
	p := newPanel(t, o)
	info := p.certInfo()
	if info.Mode != httpsModePanel || !slices.Contains(info.Names, "127.0.0.1") || !info.CustomFallback {
		t.Fatalf("certInfo = %+v, want the panel's own certificate, marked as a fall-back", info)
	}
	for _, want := range []string{"uploaded HTTPS certificate broken; using the panel's own", "panel HTTPS certificate unreadable; issuing a new one"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
	uiAddr, _ := startPanel(t, p)
	resp, err := tlsClient(t, p).Get("https://" + uiAddr + "/login")
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", resp.StatusCode)
	}
}

// TestWebCertRenewsKeepingPin: 23 months on (29 days before expiry) the
// daily check re-issues the panel's certificate with the same key, so the
// pin in pairing commands stays valid.
func TestWebCertRenewsKeepingPin(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	before := p.certs.active().cert.Leaf
	pin := p.certInfo().Pin
	if err := p.certs.renew(); err != nil {
		t.Fatal(err)
	}
	if p.certs.active().cert.Leaf.SerialNumber.Cmp(before.SerialNumber) != 0 {
		t.Fatal("renewed a certificate that is not due")
	}
	later := before.NotAfter.Add(-29 * 24 * time.Hour) // about 23 months after issue
	p.certs.now = func() time.Time { return later }
	if err := p.certs.renew(); err != nil {
		t.Fatal(err)
	}
	after := p.certs.active().cert.Leaf
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("certificate 29 days before expiry was not re-issued")
	}
	if got := p.certInfo().Pin; got != pin {
		t.Fatalf("pin changed on renewal: %s -> %s", pin, got)
	}
	onDisk, err := os.ReadFile(filepath.Join(o.ConfigDir, webCertFile))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(onDisk)
	if !bytes.Equal(block.Bytes, after.Raw) {
		t.Fatal("web.crt on disk is not the renewed certificate")
	}
	if p.certInfo().CANeedsReinstall {
		t.Fatal("a renewal asks users to reinstall")
	}
}

// TestNewNameOutsideConstraintsRegeneratesCA: an extra name in another
// subnet falls outside the web CA, so the panel makes a new one (keeping
// the old as web-ca.crt.old), re-issues its certificate with the same key
// and shows the notice until a user closes it.
func TestNewNameOutsideConstraintsRegeneratesCA(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	oldCA := p.certs.CAPEM()
	pin := p.certInfo().Pin
	if p.certInfo().CANeedsReinstall {
		t.Fatal("notice shown on a fresh panel")
	}
	if problem, err := p.certs.SetExtraNames("lokal, bukan nama!"); problem == "" || err != nil {
		t.Fatalf("SetExtraNames with invalid names: %q %v", problem, err)
	}
	if problem, err := p.certs.SetExtraNames("203.0.113.7, panel.kantor.example"); problem != "" || err != nil {
		t.Fatalf("SetExtraNames: %q %v", problem, err)
	}
	if bytes.Equal(p.certs.CAPEM(), oldCA) {
		t.Fatal("web CA not replaced for a name outside it")
	}
	saved, err := os.ReadFile(filepath.Join(o.ConfigDir, "web-ca.crt.old"))
	if err != nil || !bytes.Equal(saved, oldCA) {
		t.Fatalf("web-ca.crt.old = %v, want the old CA", err)
	}
	info := p.certInfo()
	if !info.CANeedsReinstall || info.Pin != pin ||
		!slices.Contains(info.Names, "203.0.113.7") || !slices.Contains(info.Names, "panel.kantor.example") {
		t.Fatalf("certInfo = %+v", info)
	}
	leaf := p.certs.active().cert.Leaf
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.certs.CAPEM())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "panel.kantor.example"}); err != nil {
		t.Fatalf("certificate does not verify under the new CA: %v", err)
	}
	if err := p.Store.SetSetting(store.SettingHTTPSCANoticeClosedAt, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if p.certInfo().CANeedsReinstall {
		t.Fatal("notice still shown after it was closed")
	}
	// A restart keeps the new CA and does not raise the notice again.
	p.Close()
	p2 := newPanel(t, o)
	if !bytes.Equal(p2.certs.CAPEM(), p.certs.CAPEM()) || p2.certInfo().CANeedsReinstall {
		t.Fatal("restart replaced the CA again or raised the notice")
	}
}

func TestWebNames(t *testing.T) {
	t.Parallel()
	_, lan, _ := net.ParseCIDR("10.20.30.40/24")
	lan.IP = net.ParseIP("10.20.30.40").To4()
	log, logs := bufLogger()
	chosen, found := webNames("Panel.RS.example", "dbhost", []*net.IPNet{lan}, "10.20.30.41, lokal, db.rs.example,bad_name.x", log)
	if !slices.Equal(chosen.DNS, []string{"panel.rs.example", "db.rs.example"}) {
		t.Errorf("DNS = %v", chosen.DNS)
	}
	ipStrings := func(ips []net.IP) []string {
		var out []string
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		return out
	}
	if got := ipStrings(chosen.IPs); !slices.Equal(got, []string{"10.20.30.41"}) {
		t.Errorf("chosen IPs = %v", got)
	}
	if got := ipStrings(found.IPs); !slices.Equal(got, []string{"10.20.30.40"}) {
		t.Errorf("found IPs = %v", got)
	}
	if len(chosen.Subnets) != 0 || len(found.Subnets) != 1 || found.Subnets[0].String() != "10.20.30.0/24" {
		t.Errorf("Subnets = %v / %v", chosen.Subnets, found.Subnets)
	}
	for _, dropped := range []string{"name=lokal", "name=bad_name.x"} {
		if !strings.Contains(logs.String(), dropped) {
			t.Errorf("no log line for %s:\n%s", dropped, logs.String())
		}
	}
	chosen, found = webNames("192.168.1.10", "srv.kantor.example", []*net.IPNet{{IP: net.ParseIP("192.168.1.10").To4(), Mask: net.CIDRMask(24, 32)}}, "", log)
	if len(chosen.IPs) != 1 || !chosen.IPs[0].Equal(net.ParseIP("192.168.1.10")) || !slices.Equal(chosen.DNS, []string{"srv.kantor.example"}) {
		t.Errorf("names = %+v", chosen)
	}
	if len(found.IPs) != 0 || len(found.Subnets) != 1 {
		t.Errorf("found = %+v, want only the subnet (the address is chosen)", found)
	}
}

// TestNewInterfaceKeepsWebCA: an interface that comes up after the web CA
// was made (a docker or VPN bridge) never replaces the CA; its address is
// left out of the certificate with a log line. A name the operator chose
// outside the CA still replaces it, keeping two older generations.
func TestNewInterfaceKeepsWebCA(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	var logs *syncBuffer
	o.Logger, logs = bufLogger()
	p := newPanel(t, o)
	oldCA := p.certs.CAPEM()
	bridge := &net.IPNet{IP: net.ParseIP("172.18.0.1").To4(), Mask: net.CIDRMask(16, 32)}
	base := p.certs.names
	p.certs.names = func() (ca.WebNames, ca.WebNames) {
		chosen, found := base()
		found.IPs = append(found.IPs, bridge.IP)
		found.Subnets = append(found.Subnets, &net.IPNet{IP: bridge.IP.Mask(bridge.Mask), Mask: bridge.Mask})
		return chosen, found
	}
	if err := p.certs.renew(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.certs.CAPEM(), oldCA) || p.certInfo().CANeedsReinstall {
		t.Fatal("a new interface subnet replaced the web CA")
	}
	if slices.Contains(p.certInfo().Names, "172.18.0.1") {
		t.Fatal("the bridge address outside the CA is in the certificate")
	}
	if !strings.Contains(logs.String(), "interface address outside the web CA") {
		t.Fatalf("no log line for the bridge address:\n%s", logs.String())
	}
	// A restart with the bridge still up keeps the CA too.
	if err := p.certs.load(); err != nil || !bytes.Equal(p.certs.CAPEM(), oldCA) {
		t.Fatalf("load with the bridge: %v, CA replaced %v", err, !bytes.Equal(p.certs.CAPEM(), oldCA))
	}
	// Two operator changes: web-ca.*.old and .old2 hold the two CAs before.
	if problem, err := p.certs.SetExtraNames("203.0.113.7"); problem != "" || err != nil {
		t.Fatalf("SetExtraNames: %q %v", problem, err)
	}
	second := p.certs.CAPEM()
	if problem, err := p.certs.SetExtraNames("198.51.100.9"); problem != "" || err != nil {
		t.Fatalf("SetExtraNames: %q %v", problem, err)
	}
	for f, want := range map[string][]byte{"web-ca.crt.old": second, "web-ca.crt.old2": oldCA} {
		if got, err := os.ReadFile(filepath.Join(o.ConfigDir, f)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %v, want an earlier CA", f, err)
		}
	}
	for _, f := range []string{"web-ca.key.old", "web-ca.key.old2"} {
		if _, err := os.Stat(filepath.Join(o.ConfigDir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// TestPairOverHTTPSWithPin: a real panel on loopback; agent.Pair with the
// pin from the pairing command pairs, a wrong pin fails before the token is
// sent (it stays unused).
func TestPairOverHTTPSWithPin(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	startPanel(t, p)
	srv, err := p.Store.CreateServer("db-pin")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caPin := agentapi.CAFingerprint(p.CA.CAPEM())
	wrong := "sha256//" + strings.Repeat("A", 43) + "="
	dir := t.TempDir()
	_, err = agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil, agent.PairOptions{WebPin: wrong, CASHA256: caPin})
	if !errors.Is(err, agent.ErrWebPinMismatch) {
		t.Fatalf("Pair with a wrong pin: %v", err)
	}
	if _, err := p.Store.PeekPairToken(tok.Token, time.Now()); err != nil {
		t.Fatalf("token after the refused pairing: %v (it was sent)", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("refused pairing wrote %v", entries)
	}
	cfg, err := agent.Pair(context.Background(), p.PublicURL(), tok.Token, dir, nil,
		agent.PairOptions{WebPin: p.certInfo().Pin, CASHA256: caPin})
	if err != nil {
		t.Fatalf("Pair with the right pin: %v", err)
	}
	if cfg.ServerID != srv.ID {
		t.Fatalf("paired as server %d, want %d", cfg.ServerID, srv.ID)
	}
	// The install script carries the same pin.
	tok, err = p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	script := getBody(t, p.PublicURL()+"/agent/install?token="+url.QueryEscape(tok.Token))
	if !strings.Contains(script, "WEB_PIN='"+p.certInfo().Pin+"'\n") {
		t.Fatal("install script lacks the web pin")
	}
	sc := bufio.NewScanner(strings.NewReader(script))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); !strings.HasPrefix(line, "#") && strings.Contains(line, "curl ") && !strings.Contains(line, `--pinnedpubkey "$WEB_PIN"`) {
			t.Errorf("curl without the pin: %s", line)
		}
	}
}

// TestHostAllowsCertificateNames: a name added under "Nama dan alamat
// tambahan" gets a certificate and is accepted as Host; other names still
// get 421.
func TestHostAllowsCertificateNames(t *testing.T) {
	t.Parallel()
	p := newPanel(t, testOptions(t))
	uiAddr, _ := startPanel(t, p)
	port := portOf(t, uiAddr)
	if problem, err := p.certs.SetExtraNames("panel.kantor.example"); problem != "" || err != nil {
		t.Fatalf("SetExtraNames: %q %v", problem, err)
	}
	trustPanel(p) // the name was outside the web CA, so it is a new one
	get := func(host string) int {
		t.Helper()
		req, err := http.NewRequest("GET", "https://"+uiAddr+"/login", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
		return resp.StatusCode
	}
	for _, host := range []string{"panel.kantor.example:" + port, "PANEL.kantor.example"} {
		if code := get(host); code != http.StatusOK {
			t.Errorf("Host %q: %d, want 200", host, code)
		}
	}
	for _, host := range []string{"lain.kantor.example:" + port, "kantor.example", "evil.example"} {
		if code := get(host); code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: %d, want 421", host, code)
		}
	}
}

// TestWebCAReadErrorStopsStart: a web CA file that cannot be read (not one
// that does not parse) stops the start instead of replacing the CA every
// browser trusts; a CA that does not parse is replaced, with the notice.
func TestWebCAReadErrorStopsStart(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	p1.Close()
	keyPath := filepath.Join(o.ConfigDir, "web-ca.key")
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	certBefore, err := os.ReadFile(filepath.Join(o.ConfigDir, "web-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	// A directory in place of the key: reading it fails with EISDIR.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := New(o); err == nil {
		p.Close()
		t.Fatal("New started with an unreadable web CA key")
	}
	if after, _ := os.ReadFile(filepath.Join(o.ConfigDir, "web-ca.crt")); !bytes.Equal(after, certBefore) {
		t.Fatal("unreadable key replaced the web CA")
	}
	if _, err := os.Stat(filepath.Join(o.ConfigDir, "web-ca.crt.old")); err == nil {
		t.Fatal("unreadable key moved the CA to .old")
	}

	os.Remove(keyPath)
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.ConfigDir, "web-ca.crt"), []byte("rusak"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newPanel(t, o)
	if bytes.Equal(p.certs.CAPEM(), certBefore) || !p.certInfo().CANeedsReinstall {
		t.Fatal("a web CA that does not parse was not replaced with the notice")
	}
}

// TestWebCertWritesAtomic: a renewal rewrites only the certificate (the
// key file is the same file), by rename, leaving no temporary files.
func TestWebCertWritesAtomic(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	keyBefore, err := os.Stat(filepath.Join(o.ConfigDir, webKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	certBefore, err := os.Stat(filepath.Join(o.ConfigDir, webCertFile))
	if err != nil {
		t.Fatal(err)
	}
	later := p.certs.active().cert.Leaf.NotAfter.Add(-29 * 24 * time.Hour)
	p.certs.now = func() time.Time { return later }
	if err := p.certs.renew(); err != nil {
		t.Fatal(err)
	}
	keyAfter, err := os.Stat(filepath.Join(o.ConfigDir, webKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	certAfter, err := os.Stat(filepath.Join(o.ConfigDir, webCertFile))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(keyBefore, keyAfter) || !keyAfter.ModTime().Equal(keyBefore.ModTime()) {
		t.Fatal("renewal rewrote the key")
	}
	if os.SameFile(certBefore, certAfter) {
		t.Fatal("certificate not replaced by rename")
	}
	if certAfter.Mode().Perm() != 0o600 {
		t.Fatalf("web.crt mode %v", certAfter.Mode().Perm())
	}
	entries, err := os.ReadDir(o.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

// TestExtraNamesTooBroad: an extra name that would make the web CA valid
// for a whole organisation or public suffix is refused (SEC-55); the CA is
// left as it was.
func TestExtraNamesTooBroad(t *testing.T) {
	t.Parallel()
	for name, broad := range map[string]bool{
		"co.id": true, "go.id": true, "example.com": true, "bca.co.id": true, "rsud.go.id": true, "local.lan": false,
		"rsud.local": false, "panel.rsud.go.id": false, "panel.kantor.example": false, "srv.home": false, "co.uk.local": false,
	} {
		if got := tooBroadName(name); got != broad {
			t.Errorf("tooBroadName(%q) = %v, want %v", name, got, broad)
		}
	}
	p := newPanel(t, testOptions(t))
	oldCA := p.certs.CAPEM()
	problem, err := p.certs.SetExtraNames("panel.rsud.go.id, co.id bca.co.id")
	if err != nil || !strings.Contains(problem, "Nama terlalu umum") || !strings.Contains(problem, "co.id, bca.co.id") {
		t.Fatalf("SetExtraNames = %q, %v", problem, err)
	}
	if !bytes.Equal(p.certs.CAPEM(), oldCA) {
		t.Fatal("refused names replaced the web CA")
	}
	if extra, _ := p.Store.GetSetting(store.SettingHTTPSExtraNames); extra != "" {
		t.Fatalf("refused names saved: %q", extra)
	}
}

// TestCAFingerprintMatchesOpenSSL: the fingerprint on /sertifikat and in
// the settings is what openssl x509 -fingerprint -sha256 prints for
// web-ca.crt, the file install.sh reads (SEC-54).
func TestCAFingerprintMatchesOpenSSL(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)
	fp := p.certInfo().CAFingerprint
	if len(fp) != 95 {
		t.Fatalf("fingerprint %q", fp)
	}
	if _, err := exec.LookPath("openssl"); err == nil {
		out, err := exec.Command("openssl", "x509", "-noout", "-fingerprint", "-sha256", "-in",
			filepath.Join(o.ConfigDir, "web-ca.crt")).Output()
		if err != nil {
			t.Fatal(err)
		}
		if _, got, _ := strings.Cut(strings.TrimSpace(string(out)), "="); got != fp {
			t.Fatalf("openssl says %q, panel %q", got, fp)
		}
	}
	uiAddr, _ := startPanel(t, p)
	resp, err := plainClient.Get("http://" + uiAddr + "/sertifikat")
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, resp); !strings.Contains(body, fp) {
		t.Fatal("/sertifikat lacks the fingerprint")
	}
	base := "https://" + uiAddr
	req, _ := http.NewRequest("GET", base+"/settings", nil)
	req.AddCookie(login(t, o, base))
	if resp, err = noRedirect.Do(req); err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, resp); !strings.Contains(body, fp) {
		t.Fatal("settings page lacks the fingerprint")
	}
}
