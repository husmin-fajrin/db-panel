package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestSPKIPinFormat(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "t"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	pin, err := SPKIPin(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(spki)
	want := "sha256//" + base64.StdEncoding.EncodeToString(sum[:])
	if pin != want {
		t.Fatalf("pin = %q, want %q", pin, want)
	}
	if !strings.HasPrefix(pin, "sha256//") || len(pin) != len("sha256//")+44 || !strings.HasSuffix(pin, "=") {
		t.Fatalf("format pin salah: %q", pin)
	}

	if _, err := SPKIPin([]byte("bukan pem")); err == nil {
		t.Fatal("PEM rusak: want error")
	}
}
