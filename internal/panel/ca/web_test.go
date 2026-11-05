package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/pki"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func testWebNames(t *testing.T) WebNames {
	return WebNames{
		DNS:     []string{"panel.rs.local"},
		IPs:     []net.IP{net.ParseIP("203.0.113.7")},
		Subnets: []*net.IPNet{mustCIDR(t, "10.20.0.0/16")},
	}
}

func newWebCA(t *testing.T, names WebNames) *WebCA {
	t.Helper()
	w, err := LoadOrCreateWebCA(t.TempDir(), names)
	if err != nil {
		t.Fatalf("LoadOrCreateWebCA: %v", err)
	}
	return w
}

// forgeLeaf signs a server certificate with the web CA's key directly,
// bypassing IssueWebCert's own checks, to test what verifiers accept.
func forgeLeaf(t *testing.T, w *WebCA, dns []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	return forgeLeafWith(t, w, func(c *x509.Certificate) {
		c.DNSNames = dns
		c.IPAddresses = ips
	})
}

// forgeLeafWith is forgeLeaf with arbitrary changes to the leaf template.
func forgeLeafWith(t *testing.T, w *WebCA, edit func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := randomSerial()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "x"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	edit(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, w.ca.Cert, &key.PublicKey, w.ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func verifyLeaf(w *WebCA, leaf *x509.Certificate, dnsName string) error {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(w.CAPEM())
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: dnsName})
	return err
}

func wantConstraintFailure(t *testing.T, err error, what string) {
	t.Helper()
	var inv x509.CertificateInvalidError
	if !errors.As(err, &inv) || inv.Reason != x509.CANotAuthorizedForThisName {
		t.Fatalf("%s: want CertificateInvalidError CANotAuthorizedForThisName, got %v", what, err)
	}
}

func TestWebCANameConstraintsAcceptAndReject(t *testing.T) {
	w := newWebCA(t, testWebNames(t))

	// Issued through the API: every name in the constraints verifies.
	certPEM, _, err := w.IssueWebCert(WebNames{
		DNS: []string{"panel.rs.local"},
		IPs: []net.IP{net.ParseIP("10.20.3.4"), net.ParseIP("203.0.113.7")},
	})
	if err != nil {
		t.Fatalf("IssueWebCert: %v", err)
	}
	leaf := mustParseCert(t, certPEM)
	for _, name := range []string{"panel.rs.local", "10.20.3.4", "203.0.113.7"} {
		if err := verifyLeaf(w, leaf, name); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}

	// A leaf in the subnet (and a subdomain) signed directly verifies too.
	if err := verifyLeaf(w, forgeLeaf(t, w, []string{"a.panel.rs.local"}, []net.IP{net.ParseIP("10.20.255.1")}), "10.20.255.1"); err != nil {
		t.Fatalf("leaf di subnet: %v", err)
	}

	// IP outside the subnet: the chain is rejected.
	err = verifyLeaf(w, forgeLeaf(t, w, nil, []net.IP{net.ParseIP("10.21.0.1")}), "10.21.0.1")
	wantConstraintFailure(t, err, "IP di luar subnet")

	// Another single IP next to the permitted /32.
	err = verifyLeaf(w, forgeLeaf(t, w, nil, []net.IP{net.ParseIP("203.0.113.8")}), "203.0.113.8")
	wantConstraintFailure(t, err, "IP tetangga /32")

	// A foreign domain: the chain is rejected.
	err = verifyLeaf(w, forgeLeaf(t, w, []string{"google.com"}, nil), "google.com")
	wantConstraintFailure(t, err, "google.com")

	// The constraints are marked critical.
	if !w.ca.Cert.PermittedDNSDomainsCritical {
		t.Fatal("name constraints harus critical")
	}

	// IssueWebCert refuses names outside the constraints.
	if _, _, err := w.IssueWebCert(WebNames{DNS: []string{"google.com"}}); err == nil {
		t.Fatal("IssueWebCert google.com: want error")
	}
}

// A CA given only IPs must not be able to sign any DNS name, and a CA given
// only DNS names must not be able to sign any IP (RFC 5280 constrains a name
// type only when a subtree of that type is present).
func TestWebCAOneNameTypeBlocksTheOther(t *testing.T) {
	ipOnly := newWebCA(t, WebNames{IPs: []net.IP{net.ParseIP("192.0.2.10")}})
	err := verifyLeaf(ipOnly, forgeLeaf(t, ipOnly, []string{"google.com"}, nil), "google.com")
	wantConstraintFailure(t, err, "CA hanya-IP, DNS google.com")
	if err := verifyLeaf(ipOnly, forgeLeaf(t, ipOnly, nil, []net.IP{net.ParseIP("192.0.2.10")}), "192.0.2.10"); err != nil {
		t.Fatalf("CA hanya-IP, IP sendiri: %v", err)
	}

	dnsOnly := newWebCA(t, WebNames{DNS: []string{"panel.rs.local"}})
	err = verifyLeaf(dnsOnly, forgeLeaf(t, dnsOnly, nil, []net.IP{net.ParseIP("8.8.8.8")}), "8.8.8.8")
	wantConstraintFailure(t, err, "CA hanya-DNS, IP 8.8.8.8")
	err = verifyLeaf(dnsOnly, forgeLeaf(t, dnsOnly, nil, []net.IP{net.ParseIP("::1")}), "::1")
	wantConstraintFailure(t, err, "CA hanya-DNS, IP ::1")
	if err := verifyLeaf(dnsOnly, forgeLeaf(t, dnsOnly, []string{"panel.rs.local"}, nil), "panel.rs.local"); err != nil {
		t.Fatalf("CA hanya-DNS, nama sendiri: %v", err)
	}

	if _, err := LoadOrCreateWebCA(t.TempDir(), WebNames{}); err == nil {
		t.Fatal("LoadOrCreateWebCA tanpa nama: want error")
	}
}

func TestWebCACovers(t *testing.T) {
	w := newWebCA(t, testWebNames(t))
	cases := []struct {
		name  string
		names WebNames
		want  bool
	}{
		{"sama", testWebNames(t), true},
		{"kosong", WebNames{}, true},
		{"subdomain", WebNames{DNS: []string{"x.panel.rs.local"}}, true},
		{"huruf besar", WebNames{DNS: []string{"Panel.RS.local"}}, true},
		{"IP di subnet", WebNames{IPs: []net.IP{net.ParseIP("10.20.9.9")}}, true},
		{"subnet lebih sempit", WebNames{Subnets: []*net.IPNet{mustCIDR(t, "10.20.5.0/24")}}, true},
		{"domain asing", WebNames{DNS: []string{"google.com"}}, false},
		{"akhiran tanpa titik", WebNames{DNS: []string{"evilpanel.rs.local"}}, false},
		{"IP di luar subnet", WebNames{IPs: []net.IP{net.ParseIP("10.21.0.1")}}, false},
		{"IP tetangga", WebNames{IPs: []net.IP{net.ParseIP("203.0.113.8")}}, false},
		{"subnet lebih lebar", WebNames{Subnets: []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}}, false},
		{"campuran satu di luar", WebNames{DNS: []string{"panel.rs.local", "other.local"}}, false},
	}
	for _, tc := range cases {
		if got := w.Covers(tc.names); got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIssueWebCertKeepsKey(t *testing.T) {
	w := newWebCA(t, testWebNames(t))
	certPEM, keyPEM, err := w.IssueWebCert(WebNames{DNS: []string{"panel.rs.local"}})
	if err != nil {
		t.Fatal(err)
	}
	first := mustParseCert(t, certPEM)
	if first.Subject.CommonName != "panel.rs.local" {
		t.Fatalf("CN = %q", first.Subject.CommonName)
	}
	if len(first.ExtKeyUsage) != 1 || first.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("EKU = %v", first.ExtKeyUsage)
	}
	if d := time.Until(first.NotAfter); d < leafValidity-time.Hour || d > leafValidity {
		t.Fatalf("masa berlaku = %v", d)
	}

	pin1, err := pki.SPKIPin(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.IssueWebCertWithKey(WebNames{IPs: []net.IP{net.ParseIP("10.20.0.5")}}, keyPEM)
	if err != nil {
		t.Fatalf("IssueWebCertWithKey: %v", err)
	}
	pin2, err := pki.SPKIPin(again)
	if err != nil {
		t.Fatal(err)
	}
	if pin1 != pin2 {
		t.Fatalf("pin berubah: %s vs %s", pin1, pin2)
	}
	second := mustParseCert(t, again)
	if second.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("serial harus baru")
	}
	if second.Subject.CommonName != "10.20.0.5" || len(second.DNSNames) != 0 || len(second.IPAddresses) != 1 {
		t.Fatalf("CN/SAN = %q %v %v", second.Subject.CommonName, second.DNSNames, second.IPAddresses)
	}

	if _, err := w.IssueWebCertWithKey(WebNames{DNS: []string{"panel.rs.local"}}, []byte("bukan pem")); err == nil {
		t.Fatal("kunci rusak: want error")
	}
	if _, _, err := w.IssueWebCert(WebNames{}); err == nil {
		t.Fatal("tanpa nama: want error")
	}
}

func TestWebCAPersistsAndIsSeparate(t *testing.T) {
	dir := t.TempDir()
	agentCA, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := LoadOrCreateWebCA(dir, testWebNames(t))
	if err != nil {
		t.Fatal(err)
	}
	// Loading again keeps the existing CA and its constraints.
	b, err := LoadOrCreateWebCA(dir, WebNames{DNS: []string{"lain.local"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ca.Cert.SerialNumber.Cmp(b.ca.Cert.SerialNumber) != 0 {
		t.Fatal("web CA berubah saat dimuat ulang")
	}
	if !b.Covers(testWebNames(t)) {
		t.Fatal("batasan hilang setelah dimuat ulang")
	}
	if a.ca.Cert.SerialNumber.Cmp(agentCA.Cert.SerialNumber) == 0 {
		t.Fatal("web CA tidak boleh sama dengan CA agent")
	}
	for _, f := range []string{"web-ca.crt", "web-ca.key"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %s = %o, want 600", f, st.Mode().Perm())
		}
	}
	if err := os.Remove(filepath.Join(dir, "web-ca.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateWebCA(dir, testWebNames(t)); err == nil {
		t.Fatal("want error when web-ca.key is missing")
	}
}

// TestWebCARejectsBadDNSNames: an empty dNSName constraint matches every name
// (RFC 5280), a single label such as "local" covers a whole TLD, and
// wildcards or stray dots are not host names. None may reach the constraints.
func TestWebCARejectsBadDNSNames(t *testing.T) {
	bad := []string{"", "local", "com", "*.panel.rs.local", "panel.rs.local.", ".panel.rs.local",
		"pa nel.rs.local", "-a.rs.local", "a-.rs.local", "a_b.rs.local", "\u00fcber.rs.local", "a..rs.local",
		strings.Repeat("a", 64) + ".rs.local"}
	for _, name := range bad {
		dir := t.TempDir()
		_, err := LoadOrCreateWebCA(dir, WebNames{DNS: []string{"panel.rs.local", name}})
		if err == nil {
			t.Errorf("LoadOrCreateWebCA(%q): want error", name)
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
			t.Errorf("error should name %q: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "web-ca.key")); !os.IsNotExist(err) {
			t.Errorf("%q: web-ca.key must not be written, stat err = %v", name, err)
		}
	}

	w := newWebCA(t, testWebNames(t))
	for _, name := range bad {
		if _, _, err := w.IssueWebCert(WebNames{DNS: []string{name}}); err == nil {
			t.Errorf("IssueWebCert(%q): want error", name)
		}
	}
}

func TestWebCALowercasesNames(t *testing.T) {
	w := newWebCA(t, WebNames{DNS: []string{"Panel.RS.Local"}})
	if got := w.ca.Cert.PermittedDNSDomains; len(got) != 1 || got[0] != "panel.rs.local" {
		t.Fatalf("PermittedDNSDomains = %v", got)
	}
	certPEM, _, err := w.IssueWebCert(WebNames{DNS: []string{"PANEL.rs.local"}})
	if err != nil {
		t.Fatal(err)
	}
	if leaf := mustParseCert(t, certPEM); leaf.DNSNames[0] != "panel.rs.local" || leaf.Subject.CommonName != "panel.rs.local" {
		t.Fatalf("leaf names = %v / %q", leaf.DNSNames, leaf.Subject.CommonName)
	}
}

// TestWebCAOnlyServerAuth: a leaked web CA key must not yield certificates
// for other purposes or for email/URI names.
func TestWebCAOnlyServerAuth(t *testing.T) {
	w := newWebCA(t, testWebNames(t))
	if eku := w.ca.Cert.ExtKeyUsage; len(eku) != 1 || eku[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("CA ExtKeyUsage = %v", eku)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(w.CAPEM())

	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageEmailProtection, x509.ExtKeyUsageClientAuth} {
		leaf := forgeLeafWith(t, w, func(c *x509.Certificate) {
			c.DNSNames = []string{"panel.rs.local"}
			c.ExtKeyUsage = []x509.ExtKeyUsage{usage}
		})
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}})
		var inv x509.CertificateInvalidError
		if !errors.As(err, &inv) || inv.Reason != x509.IncompatibleUsage {
			t.Errorf("EKU %v: want IncompatibleUsage, got %v", usage, err)
		}
	}

	email := forgeLeafWith(t, w, func(c *x509.Certificate) {
		c.DNSNames = []string{"panel.rs.local"}
		c.EmailAddresses = []string{"ceo@bank.example"}
	})
	wantConstraintFailure(t, verifyLeaf(w, email, "panel.rs.local"), "email SAN")

	u, err := url.Parse("https://google.com/")
	if err != nil {
		t.Fatal(err)
	}
	uri := forgeLeafWith(t, w, func(c *x509.Certificate) {
		c.DNSNames = []string{"panel.rs.local"}
		c.URIs = []*url.URL{u}
	})
	wantConstraintFailure(t, verifyLeaf(w, uri, "panel.rs.local"), "URI SAN")
}
