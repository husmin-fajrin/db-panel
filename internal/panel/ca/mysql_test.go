package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMySQLCertSANOnlyFromHosts(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadOrCreateMySQLCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: "evil"},
		DNSNames:    []string{"google.com"},
		IPAddresses: []net.IP{net.ParseIP("8.8.8.8")},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})

	certPEM, serial, notAfter, err := m.IssueMySQLCert(csrPEM, 7, []string{"10.0.0.5", "db1.rs.local"})
	if err != nil {
		t.Fatalf("IssueMySQLCert: %v", err)
	}
	cert := mustParseCert(t, certPEM)
	if cert.Subject.CommonName != "dbpanel-mysql-7" {
		t.Fatalf("CN = %q", cert.Subject.CommonName)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "db1.rs.local" {
		t.Fatalf("DNSNames = %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.5")) {
		t.Fatalf("IPAddresses = %v", cert.IPAddresses)
	}
	if serial != fmt.Sprintf("%032x", cert.SerialNumber) {
		t.Fatalf("serial = %s", serial)
	}
	if !notAfter.Equal(cert.NotAfter) {
		t.Fatalf("notAfter = %v, cert %v", notAfter, cert.NotAfter)
	}
	if d := time.Until(notAfter); d < leafValidity-time.Hour || d > leafValidity {
		t.Fatalf("masa berlaku = %v", d)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("EKU = %v", cert.ExtKeyUsage)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		t.Fatal("kunci publik harus dari CSR")
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(m.CAPEM())
	for _, name := range []string{"10.0.0.5", "db1.rs.local"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "google.com"}); err == nil {
		t.Fatal("SAN asing dari CSR tidak boleh terverifikasi")
	}

	if eku := m.ca.Cert.ExtKeyUsage; len(eku) != 1 || eku[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("MySQL CA ExtKeyUsage = %v", eku)
	}

	if _, _, _, err := m.IssueMySQLCert(csrPEM, 7, nil); err == nil {
		t.Fatal("hosts kosong: want error")
	}
	if _, _, _, err := m.IssueMySQLCert(csrPEM, 0, []string{"10.0.0.5"}); err == nil {
		t.Fatal("id 0: want error")
	}
	if _, _, _, err := m.IssueMySQLCert([]byte("x"), 7, []string{"10.0.0.5"}); err == nil {
		t.Fatal("csr rusak: want error")
	}

	// Separate files, separate CA, persists.
	again, err := LoadOrCreateMySQLCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.ca.Cert.SerialNumber.Cmp(m.ca.Cert.SerialNumber) != 0 {
		t.Fatal("MySQL CA berubah saat dimuat ulang")
	}
	for _, f := range []string{"mysql-ca.crt", "mysql-ca.key"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %s = %o, want 600", f, st.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.crt")); !os.IsNotExist(err) {
		t.Fatalf("MySQL CA tidak boleh membuat ca.crt, stat err = %v", err)
	}
}
