package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/pki"
)

func newCA(t *testing.T) *CA {
	t.Helper()
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return c
}

func mustParseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("bukan PEM CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func TestLoadOrCreatePersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Cert.SerialNumber.Cmp(b.Cert.SerialNumber) != 0 {
		t.Fatalf("serial CA berubah: %v vs %v", a.Cert.SerialNumber, b.Cert.SerialNumber)
	}
	if !b.Key.PublicKey.Equal(&a.Key.PublicKey) {
		t.Fatal("kunci CA berubah")
	}
	st, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode ca.key = %o, want 600", st.Mode().Perm())
	}
	dst, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("mode dir = %o, want 700", dst.Mode().Perm())
	}
	if a.Cert.Subject.CommonName != "dbpanel-ca" || !a.Cert.IsCA {
		t.Fatalf("cert CA salah: %+v", a.Cert.Subject)
	}
	if got := a.Cert.NotAfter.Sub(a.Cert.NotBefore); got < 9*365*24*time.Hour || got > 11*365*24*time.Hour {
		t.Fatalf("validitas CA = %v, want ~10 tahun", got)
	}
}

func TestLoadOrCreateErrorsWhenOnlyOneFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreate(dir)
	if err == nil {
		t.Fatal("want error when ca.key is missing")
	}
	if !strings.Contains(err.Error(), "ca.key") {
		t.Fatalf("error should name ca.key: %v", err)
	}
}

func TestIssueAgentCertSetsCN(t *testing.T) {
	c := newCA(t)
	_, csrPEM, err := pki.NewAgentKeyAndCSR("apa-saja")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, serial, err := c.IssueAgentCert(csrPEM, 42)
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParseCert(t, certPEM)
	if cert.Subject.CommonName != "agent-42" {
		t.Fatalf("CN = %q", cert.Subject.CommonName)
	}
	id, err := ServerIDFromCert(cert)
	if err != nil || id != 42 {
		t.Fatalf("ServerIDFromCert = %d, %v", id, err)
	}
	if len(serial) != 32 {
		t.Fatalf("serial %q bukan 128-bit hex", serial)
	}
	if want := fmt.Sprintf("%032x", cert.SerialNumber); serial != want {
		t.Fatalf("serial %q tidak cocok dengan cert %s", serial, want)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("ExtKeyUsage = %v", cert.ExtKeyUsage)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c.Cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("cert tidak terverifikasi oleh CA: %v", err)
	}
}

func TestIssueAgentCertRejectsBadCSR(t *testing.T) {
	c := newCA(t)
	if _, _, err := c.IssueAgentCert([]byte("bukan pem"), 1); err == nil {
		t.Fatal("want error for garbage CSR")
	}
	// CSR dengan tanda tangan rusak.
	_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(csrPEM)
	block.Bytes[len(block.Bytes)-1] ^= 0xff
	if _, _, err := c.IssueAgentCert(pem.EncodeToMemory(block), 1); err == nil {
		t.Fatal("want error for CSR with bad signature")
	}
}

func TestServerIDFromCertRejectsOther(t *testing.T) {
	for _, cn := range []string{"x", "agent-", "agent-0", "agent--3", "agent-abc", "dbpanel-ca"} {
		cert := &x509.Certificate{}
		cert.Subject.CommonName = cn
		if _, err := ServerIDFromCert(cert); err == nil {
			t.Errorf("CN %q: want error", cn)
		}
	}
	cert := &x509.Certificate{}
	cert.Subject.CommonName = "x"
	_, err := ServerIDFromCert(cert)
	if !errors.Is(err, ErrNotAgent) || err.Error() != "sertifikat bukan milik agent" {
		t.Fatalf("err = %v", err)
	}
}

func TestIssueServerCert(t *testing.T) {
	c := newCA(t)
	certPEM, keyPEM, err := c.IssueServerCert()
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParseCert(t, certPEM)
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != pki.PanelServerName {
		t.Fatalf("DNSNames = %v", cert.DNSNames)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("ExtKeyUsage = %v", cert.ExtKeyUsage)
	}
	if block, _ := pem.Decode(keyPEM); block == nil || block.Type != "EC PRIVATE KEY" {
		t.Fatal("keyPEM bukan EC PRIVATE KEY")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("pasangan cert/key tidak cocok: %v", err)
	}
}

// startPanel menjalankan server TLS dengan AgentListenerTLS milik c.
func startPanel(t *testing.T, c *CA) *httptest.Server {
	t.Helper()
	cfg, err := c.AgentListenerTLS()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := ServerIDFromCert(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if id != 7 {
			http.Error(w, "id salah", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = cfg
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func agentClient(t *testing.T, issuer *CA, trust []byte, serverID int64) *http.Client {
	t.Helper()
	keyPEM, csrPEM, err := pki.NewAgentKeyAndCSR("host")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, err := issuer.IssueAgentCert(csrPEM, serverID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pki.AgentClientTLS(certPEM, keyPEM, trust)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 5 * time.Second}
}

func TestMutualTLSHandshake(t *testing.T) {
	c := newCA(t)
	srv := startPanel(t, c)

	t.Run("agent dengan cert CA diterima", func(t *testing.T) {
		client := agentClient(t, c, c.CAPEM(), 7)
		resp, err := client.Get(srv.URL) // URL memakai IP; ServerName dipaksa pki.PanelServerName
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if resp.TLS.Version != tls.VersionTLS13 {
			t.Fatalf("TLS version = %x", resp.TLS.Version)
		}
	})

	t.Run("klien tanpa cert ditolak", func(t *testing.T) {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(c.CAPEM())
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: pool, ServerName: pki.PanelServerName, MinVersion: tls.VersionTLS13,
		}}, Timeout: 5 * time.Second}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("want error without client cert")
		}
	})

	t.Run("klien dengan cert dari CA lain ditolak", func(t *testing.T) {
		other := newCA(t)
		client := agentClient(t, other, c.CAPEM(), 7)
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("want error with cert from another CA")
		}
	})

	t.Run("klien yang tidak percaya CA panel menolak server", func(t *testing.T) {
		other := newCA(t)
		client := agentClient(t, c, other.CAPEM(), 7)
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("want error when client trusts a different CA")
		}
	})
}

func TestAgentClientTLSInvalidCA(t *testing.T) {
	c := newCA(t)
	keyPEM, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, err := c.IssueAgentCert(csrPEM, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pki.AgentClientTLS(certPEM, keyPEM, []byte("sampah"))
	if err == nil || err.Error() != "ca tidak valid" {
		t.Fatalf("err = %v", err)
	}
	cfg, err := pki.AgentClientTLS(certPEM, keyPEM, c.CAPEM())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != pki.PanelServerName || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestIssueAgentCertRejectsNonPositiveID(t *testing.T) {
	c := newCA(t)
	_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{0, -1} {
		certPEM, serial, err := c.IssueAgentCert(csrPEM, id)
		if err == nil || err.Error() != "id server tidak valid" {
			t.Errorf("id %d: err = %v", id, err)
		}
		if certPEM != nil || serial != "" {
			t.Errorf("id %d: want no cert issued", id)
		}
	}
}

func TestWriteNewDoesNotClobberExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("lama"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, []byte("baru"), 0o600); err == nil {
		t.Fatal("want error for existing file")
	}
	if b, _ := os.ReadFile(path); string(b) != "lama" {
		t.Fatalf("file existing berubah: %q", b)
	}
}

func TestCreateCARemovesKeyWhenCertWriteFails(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	badCertPath := filepath.Join(dir, "tidak-ada", "ca.crt") // parent missing
	if _, err := createCA(dir, badCertPath, keyPath); err == nil {
		t.Fatal("want error when cert cannot be written")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("ca.key harus dihapus setelah kegagalan, stat err = %v", err)
	}
	// Direktori tidak tertinggal dalam keadaan "hanya satu file".
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatalf("LoadOrCreate setelah kegagalan: %v", err)
	}
}

// csrWith returns a PEM CSR signed by key.
func csrWith(t *testing.T, key crypto.Signer) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "h"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// TestIssueAgentCertKeyPolicy (SEC-18): only ECDSA P-256 agent keys.
func TestIssueAgentCertKeyPolicy(t *testing.T) {
	c := newCA(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]crypto.Signer{"rsa-1024": rsaKey, "p384": p384, "ed25519": edKey} {
		_, _, err := c.IssueAgentCert(csrWith(t, key), 1)
		if !errors.Is(err, ErrCSRKey) || err.Error() != "kunci CSR harus ECDSA P-256" {
			t.Errorf("%s: err = %v, want %q", name, err, ErrCSRKey)
		}
		if _, err := ParseAgentCSR(csrWith(t, key)); !errors.Is(err, ErrCSRKey) {
			t.Errorf("%s: ParseAgentCSR err = %v", name, err)
		}
	}
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.IssueAgentCert(csrWith(t, p256), 1); err != nil {
		t.Fatalf("P-256: %v", err)
	}
}

// TestCertificateValidity (SEC-18): CA 10 years, agent and server
// certificates 2 years, never past the CA's own expiry.
func TestCertificateValidity(t *testing.T) {
	c := newCA(t)
	near := func(got time.Time, want time.Duration) bool {
		d := time.Until(got) - want
		return d > -time.Hour && d < time.Hour
	}
	if !near(c.Cert.NotAfter, 10*365*24*time.Hour) {
		t.Errorf("CA NotAfter = %v, want ~10 years", c.Cert.NotAfter)
	}
	if c.Cert.MaxPathLen != 0 || !c.Cert.MaxPathLenZero {
		t.Errorf("CA MaxPathLen = %d (zero %v), want 0", c.Cert.MaxPathLen, c.Cert.MaxPathLenZero)
	}
	_, csrPEM, err := pki.NewAgentKeyAndCSR("h")
	if err != nil {
		t.Fatal(err)
	}
	agentPEM, _, err := c.IssueAgentCert(csrPEM, 1)
	if err != nil {
		t.Fatal(err)
	}
	if na := mustParseCert(t, agentPEM).NotAfter; !near(na, 2*365*24*time.Hour) {
		t.Errorf("agent NotAfter = %v, want ~2 years", na)
	}
	serverPEM, _, err := c.IssueServerCert()
	if err != nil {
		t.Fatal(err)
	}
	if na := mustParseCert(t, serverPEM).NotAfter; !near(na, 2*365*24*time.Hour) {
		t.Errorf("server NotAfter = %v, want ~2 years", na)
	}

	// A CA close to its end caps the leaves at its own NotAfter.
	short := *c.Cert
	short.NotAfter = time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	cs := &CA{Cert: &short, Key: c.Key}
	agentPEM, _, err = cs.IssueAgentCert(csrPEM, 1)
	if err != nil {
		t.Fatal(err)
	}
	if na := mustParseCert(t, agentPEM).NotAfter; !na.Equal(short.NotAfter) {
		t.Errorf("agent NotAfter = %v, want capped at CA NotAfter %v", na, short.NotAfter)
	}
	serverPEM, _, err = cs.IssueServerCert()
	if err != nil {
		t.Fatal(err)
	}
	if na := mustParseCert(t, serverPEM).NotAfter; !na.Equal(short.NotAfter) {
		t.Errorf("server NotAfter = %v, want capped at CA NotAfter %v", na, short.NotAfter)
	}
}
