// Package pki holds the certificate helpers both sides of the agent link
// need: the agent's key and CSR, its TLS client configuration, the panel's
// server name, and CA fingerprints. The panel's certificate authority
// (internal/panel/ca) builds on it; the agent imports only this package.
package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

// PanelServerName is the DNS SAN of the panel's server certificate. Agents dial
// by IP but set tls.Config.ServerName to this constant.
const PanelServerName = "dbpanel-panel"

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
	keyPEM, err = EncodeKey(key)
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

// Fingerprint returns the sha256 (lowercase hex) of the DER of the one
// certificate in certPEM. Anything else in certPEM (a second certificate,
// trailing data) is an error: a CA bundle pinned by the fingerprint of its
// first certificate could smuggle in another trusted root.
func Fingerprint(certPEM []byte) (string, error) {
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("bukan PEM CERTIFICATE")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("PEM berisi lebih dari satu sertifikat")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", err
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// EncodeKey returns key as a PEM "EC PRIVATE KEY" block.
func EncodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal kunci: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}
