// Package ca implements the panel's private certificate authority. It issues
// one client certificate per agent (from a CSR generated on the agent, so
// private keys never leave it) and the server certificate for the panel's
// agent listener.
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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PanelServerName is the DNS SAN of the panel's server certificate. Agents dial
// by IP but set tls.Config.ServerName to this constant.
const PanelServerName = "dbpanel-panel"

const (
	caCertFile    = "ca.crt"
	caKeyFile     = "ca.key"
	agentCNPrefix = "agent-"
	caValidity    = 20 * 365 * 24 * time.Hour
	leafValidity  = 10 * 365 * 24 * time.Hour
	clockSkew     = 5 * time.Minute
)

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
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)
	certBytes, certErr := os.ReadFile(certPath)
	keyBytes, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		return parseCA(certBytes, keyBytes)
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		return createCA(dir, certPath, keyPath)
	case errors.Is(certErr, os.ErrNotExist) && keyErr == nil:
		return nil, fmt.Errorf("ca: %s tidak ada (hanya %s yang ditemukan di %s)", caCertFile, caKeyFile, dir)
	case errors.Is(keyErr, os.ErrNotExist) && certErr == nil:
		return nil, fmt.Errorf("ca: %s tidak ada (hanya %s yang ditemukan di %s)", caKeyFile, caCertFile, dir)
	case certErr != nil:
		return nil, fmt.Errorf("ca: baca %s: %w", caCertFile, certErr)
	default:
		return nil, fmt.Errorf("ca: baca %s: %w", caKeyFile, keyErr)
	}
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", caCertFile, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("ca: %s bukan PEM EC PRIVATE KEY", caKeyFile)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", caKeyFile, err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("ca: ca.crt dan ca.key tidak cocok")
	}
	return &CA{Cert: cert, Key: key}, nil
}

func createCA(dir, certPath, keyPath string) (*CA, error) {
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
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "dbpanel-ca"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: buat sertifikat: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse sertifikat: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	// Key first, exclusive create: never overwrite existing material.
	if err := writeNew(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: tulis %s: %w", caKeyFile, err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeNew(certPath, certPEM, 0o644); err != nil {
		// Do not leave a lone ca.key behind: that state is a permanent error.
		os.Remove(keyPath)
		return nil, fmt.Errorf("ca: tulis %s: %w", caCertFile, err)
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

// IssueAgentCert signs the CSR as a client certificate for the given server.
// The subject CN is always "agent-<serverID>"; the CSR's own subject is ignored.
// The returned serial is the certificate serial as 32 lowercase hex digits.
func (c *CA) IssueAgentCert(csrPEM []byte, serverID int64) (certPEM []byte, serial string, err error) {
	if serverID <= 0 {
		return nil, "", errors.New("id server tidak valid")
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", errors.New("csr bukan PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("tanda tangan csr tidak valid: %w", err)
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
		NotAfter:              now.Add(leafValidity),
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
// panel's agent listener (DNS SAN PanelServerName).
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
		Subject:               pkix.Name{CommonName: PanelServerName},
		DNSNames:              []string{PanelServerName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("terbitkan sertifikat server: %w", err)
	}
	keyPEM, err = encodeKey(key)
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

// NewAgentKeyAndCSR generates an ECDSA P-256 key and a CSR whose CN is
// hostname (the panel overrides the subject when signing).
func NewAgentKeyAndCSR(hostname string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate kunci agent: %w", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("buat csr: %w", err)
	}
	keyPEM, err = encodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// AgentClientTLS builds the agent's TLS config: its client certificate, the
// panel CA as the only root, and ServerName pinned to PanelServerName.
func AgentClientTLS(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pasangan kunci agent: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca tidak valid")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		RootCAs:      pool,
		ServerName:   PanelServerName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func parseCertPEM(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("bukan PEM CERTIFICATE")
	}
	return x509.ParseCertificate(block.Bytes)
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal kunci: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
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
