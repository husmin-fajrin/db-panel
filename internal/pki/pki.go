package pki

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

// SPKIPin returns the public-key pin of the first certificate in certPEM in
// curl --pinnedpubkey form: "sha256//" + base64 (standard, padded) of the
// SHA-256 of the certificate's SubjectPublicKeyInfo DER. The pin survives
// re-issuing a certificate with the same key.
func SPKIPin(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("bukan PEM CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256//" + base64.StdEncoding.EncodeToString(sum[:]), nil
}
