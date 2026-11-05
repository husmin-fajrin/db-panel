// Package ca implements the panel's private certificate authorities. The
// agent CA (ca.crt) issues one client certificate per agent (from a CSR
// generated on the agent, so private keys never leave it) and the server
// certificate for the panel's agent listener. The web CA (web.go) issues the
// panel's HTTPS certificate and the MySQL CA (mysql.go) the managed servers'
// MySQL certificates; each lives in its own files.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/pki"
)

const (
	caCertFile    = "ca.crt"
	caKeyFile     = "ca.key"
	agentCNPrefix = "agent-"
	caValidity    = 10 * 365 * 24 * time.Hour
	// leafValidity applies to agent and panel server certificates. There is
	// no automatic renewal yet (Plan 2): an agent re-pairs before expiry,
	// and the panel issues a fresh server certificate on every start.
	leafValidity = 2 * 365 * 24 * time.Hour
	clockSkew    = 5 * time.Minute
)

// ErrCSRKey is returned for a CSR whose public key is not ECDSA P-256, the
// only kind dbpanel agents generate.
var ErrCSRKey = errors.New("kunci CSR harus ECDSA P-256")

// ErrNotAgent is returned by ServerIDFromCert for certificates whose CN is not
// "agent-<positive int>".
var ErrNotAgent = errors.New("sertifikat bukan milik agent")

// CA is the panel's certificate authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// LoadOrCreate loads dir/ca.crt and dir/ca.key, or generates and writes both
// when neither exists. If only one exists it returns an error naming the
// missing file.
func LoadOrCreate(dir string) (*CA, error) {
	return loadOrCreate(dir, caCertFile, caKeyFile, agentCATemplate(), 0o644)
}

// agentCATemplate is the subject of the agent CA. createCAWith fills in the
// rest.
func agentCATemplate() *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: "dbpanel-ca"}}
}

// loadOrCreate loads dir/certFile and dir/keyFile, or creates a CA from tmpl
// (see createCAWith) when neither exists. If only one exists it returns an
// error naming the missing file.
func loadOrCreate(dir, certFile, keyFile string, tmpl *x509.Certificate, certMode os.FileMode) (*CA, error) {
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)
	certBytes, certErr := os.ReadFile(certPath)
	keyBytes, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		return parseCA(certBytes, keyBytes, certFile, keyFile)
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		return createCAWith(dir, certPath, keyPath, tmpl, certMode)
	case errors.Is(certErr, os.ErrNotExist) && keyErr == nil:
		return nil, fmt.Errorf("ca: %s tidak ada (hanya %s yang ditemukan di %s)", certFile, keyFile, dir)
	case errors.Is(keyErr, os.ErrNotExist) && certErr == nil:
		return nil, fmt.Errorf("ca: %s tidak ada (hanya %s yang ditemukan di %s)", keyFile, certFile, dir)
	case certErr != nil:
		return nil, fmt.Errorf("ca: baca %s: %w", certFile, certErr)
	default:
		return nil, fmt.Errorf("ca: baca %s: %w", keyFile, keyErr)
	}
}

func parseCA(certPEM, keyPEM []byte, certFile, keyFile string) (*CA, error) {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", certFile, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("ca: %s bukan PEM EC PRIVATE KEY", keyFile)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", keyFile, err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("ca: %s dan %s tidak cocok", certFile, keyFile)
	}
	return &CA{Cert: cert, Key: key}, nil
}

// createCA creates the agent CA at certPath/keyPath.
func createCA(dir, certPath, keyPath string) (*CA, error) {
	return createCAWith(dir, certPath, keyPath, agentCATemplate(), 0o644)
}

// createCAWith generates a P-256 key and a self-signed 10-year CA certificate
// from tmpl, which carries the subject and any name constraints; the serial,
// validity, basic constraints and key usage are set here. The key is written
// to keyPath (0600) and the certificate to certPath (certMode), both with
// exclusive create.
func createCAWith(dir, certPath, keyPath string, tmpl *x509.Certificate, certMode os.FileMode) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ca: buat direktori: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate kunci: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl.SerialNumber = serial
	tmpl.NotBefore = now.Add(-clockSkew)
	tmpl.NotAfter = now.Add(caValidity)
	tmpl.IsCA = true
	tmpl.BasicConstraintsValid = true
	tmpl.MaxPathLenZero = true // signs leaves only, never another CA
	tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: buat sertifikat: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse sertifikat: %w", err)
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return nil, err
	}
	// Key first, exclusive create: never overwrite existing material.
	if err := writeNew(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: tulis %s: %w", filepath.Base(keyPath), err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeNew(certPath, certPEM, certMode); err != nil {
		// Do not leave a lone key behind: that state is a permanent error.
		os.Remove(keyPath)
		return nil, fmt.Errorf("ca: tulis %s: %w", filepath.Base(certPath), err)
	}
	return &CA{Cert: cert, Key: key}, nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	// The file was created by us (O_EXCL), so on failure remove the partial file.
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// CAPEM returns the CA certificate in PEM form.
func (c *CA) CAPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Cert.Raw})
}

// ParseAgentCSR parses a PEM CSR from an agent and checks its signature and
// key: only ECDSA P-256 is accepted (ErrCSRKey otherwise).
func ParseAgentCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("csr bukan PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("tanda tangan csr tidak valid: %w", err)
	}
	if pub, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
		return nil, ErrCSRKey
	}
	return csr, nil
}

// leafNotAfter is the expiry of a certificate issued now: leafValidity,
// but never past the CA's own expiry.
func (c *CA) leafNotAfter(now time.Time) time.Time {
	if na := now.Add(leafValidity); na.Before(c.Cert.NotAfter) {
		return na
	}
	return c.Cert.NotAfter
}

// issueServerLeaf signs a ServerAuth certificate for pub with the given CN
// and SANs, valid for leafValidity (capped by the CA's expiry).
func (c *CA) issueServerLeaf(cn string, dns []string, ips []net.IP, pub any) (certPEM []byte, sn *big.Int, notAfter time.Time, err error) {
	sn, err = randomSerial()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: cn},
		DNSNames:              dns,
		IPAddresses:           ips,
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              c.leafNotAfter(now),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, pub, c.Key)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("terbitkan sertifikat server: %w", err)
	}
	// NotAfter as encoded (whole seconds), not the template's.
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("parse sertifikat server: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), sn, cert.NotAfter, nil
}

// IssueAgentCert signs the CSR as a client certificate for the given server.
// The subject CN is always "agent-<serverID>"; the CSR's own subject is ignored.
// The returned serial is the certificate serial as 32 lowercase hex digits.
func (c *CA) IssueAgentCert(csrPEM []byte, serverID int64) (certPEM []byte, serial string, err error) {
	if serverID <= 0 {
		return nil, "", errors.New("id server tidak valid")
	}
	csr, err := ParseAgentCSR(csrPEM)
	if err != nil {
		return nil, "", err
	}
	sn, err := randomSerial()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: agentCNPrefix + strconv.FormatInt(serverID, 10)},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              c.leafNotAfter(now),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, csr.PublicKey, c.Key)
	if err != nil {
		return nil, "", fmt.Errorf("terbitkan sertifikat agent: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), fmt.Sprintf("%032x", sn), nil
}

// IssueServerCert generates a fresh key and a server certificate for the
// panel's agent listener (DNS SAN pki.PanelServerName).
func (c *CA) IssueServerCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate kunci server: %w", err)
	}
	sn, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: pki.PanelServerName},
		DNSNames:              []string{pki.PanelServerName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              c.leafNotAfter(now),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("terbitkan sertifikat server: %w", err)
	}
	keyPEM, err = pki.EncodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// AgentListenerTLS returns the TLS config for the panel's agent listener: a
// freshly issued server certificate, with client certificates required and
// verified against this CA.
func (c *CA) AgentListenerTLS() (*tls.Config, error) {
	certPEM, keyPEM, err := c.IssueServerCert()
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pasangan kunci server: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c.Cert)
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ServerIDFromCert extracts the server ID from a certificate whose CN is
// "agent-<positive int>".
func ServerIDFromCert(cert *x509.Certificate) (int64, error) {
	rest, ok := strings.CutPrefix(cert.Subject.CommonName, agentCNPrefix)
	if !ok {
		return 0, ErrNotAgent
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != rest {
		return 0, ErrNotAgent
	}
	return id, nil
}

func parseCertPEM(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("bukan PEM CERTIFICATE")
	}
	return x509.ParseCertificate(block.Bytes)
}

// randomSerial returns a random positive 128-bit serial number.
func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	max.Sub(max, big.NewInt(1))
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("serial acak: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}
