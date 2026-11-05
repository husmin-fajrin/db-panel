package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/pki"
)

const (
	webCACertFile = "web-ca.crt"
	webCAKeyFile  = "web-ca.key"
	// unmatchableDNS is the permitted DNS subtree of a web CA given no DNS
	// names: without any DNS subtree RFC 5280 leaves DNS names unconstrained,
	// so the CA could sign any domain. "invalid" is reserved (RFC 6761) and
	// never resolves.
	unmatchableDNS = "invalid"
)

// unmatchableIPs are the permitted IP ranges of a web CA given no IPs or
// subnets, for the same reason as unmatchableDNS: a single unusable address
// per family.
var unmatchableIPs = []*net.IPNet{
	{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(32, 32)},
	{IP: net.IPv6unspecified, Mask: net.CIDRMask(128, 128)},
}

// WebNames are the names the panel's HTTPS certificate is for. The web CA is
// constrained to DNS (each name and its subdomains), IPs (each a /32 or
// /128) and Subnets.
type WebNames struct {
	DNS     []string
	IPs     []net.IP
	Subnets []*net.IPNet
}

// WebCA is the certificate authority for the panel's HTTPS certificate. Users
// install it in their browsers, so it carries critical name constraints: it
// can only ever vouch for the panel's own names and networks.
type WebCA struct {
	ca *CA
}

// LoadOrCreateWebCA loads dir/web-ca.crt and dir/web-ca.key, or creates a
// web CA constrained to names when neither exists (both files 0600). An
// existing CA is loaded as is, whatever names says: use Covers to decide
// whether it still fits.
func LoadOrCreateWebCA(dir string, names WebNames) (*WebCA, error) {
	if len(names.DNS) == 0 && len(names.IPs) == 0 && len(names.Subnets) == 0 {
		return nil, errors.New("ca: web CA butuh minimal satu nama, IP atau subnet")
	}
	dns := names.DNS
	if len(dns) == 0 {
		dns = []string{unmatchableDNS}
	}
	ranges := append([]*net.IPNet(nil), names.Subnets...)
	for _, ip := range names.IPs {
		ranges = append(ranges, hostNet(ip))
	}
	if len(ranges) == 0 {
		ranges = unmatchableIPs
	}
	tmpl := &x509.Certificate{
		Subject:                     pkix.Name{CommonName: "dbpanel-web-ca"},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         dns,
		PermittedIPRanges:           ranges,
	}
	c, err := loadOrCreate(dir, webCACertFile, webCAKeyFile, tmpl, 0o600)
	if err != nil {
		return nil, err
	}
	return &WebCA{ca: c}, nil
}

// hostNet is the single-address network of ip (/32 for IPv4, /128 for IPv6).
func hostNet(ip net.IP) *net.IPNet {
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)}
}

// Covers reports whether every DNS name, IP and subnet of names lies within
// the CA's name constraints.
func (w *WebCA) Covers(names WebNames) bool {
	for _, name := range names.DNS {
		if !w.coversDNS(name) {
			return false
		}
	}
	for _, ip := range names.IPs {
		if !w.coversNet(hostNet(ip)) {
			return false
		}
	}
	for _, n := range names.Subnets {
		if !w.coversNet(n) {
			return false
		}
	}
	return true
}

// coversDNS: name equals a permitted domain or is a subdomain of it (RFC
// 5280; a constraint with a leading dot permits subdomains only).
func (w *WebCA) coversDNS(name string) bool {
	name = strings.ToLower(name)
	for _, d := range w.ca.Cert.PermittedDNSDomains {
		d = strings.ToLower(d)
		if strings.HasPrefix(d, ".") {
			if strings.HasSuffix(name, d) {
				return true
			}
		} else if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// coversNet: n lies entirely inside one permitted range of the same family.
func (w *WebCA) coversNet(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()
	for _, r := range w.ca.Cert.PermittedIPRanges {
		rOnes, rBits := r.Mask.Size()
		if bits == rBits && ones >= rOnes && r.Contains(n.IP) {
			return true
		}
	}
	return false
}

// IssueWebCert generates a fresh P-256 key and issues the panel's HTTPS
// certificate for names.DNS and names.IPs (names.Subnets is ignored).
func (w *WebCA) IssueWebCert(names WebNames) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate kunci web: %w", err)
	}
	certPEM, err = w.issue(names, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = pki.EncodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// IssueWebCertWithKey re-issues the HTTPS certificate for the existing key in
// keyPEM (PEM "EC PRIVATE KEY"), so its SPKI pin stays the same.
func (w *WebCA) IssueWebCertWithKey(names WebNames, keyPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, errors.New("kunci web bukan PEM EC PRIVATE KEY")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("kunci web: %w", err)
	}
	return w.issue(names, key)
}

func (w *WebCA) issue(names WebNames, key *ecdsa.PrivateKey) ([]byte, error) {
	leaf := WebNames{DNS: names.DNS, IPs: names.IPs}
	var cn string
	switch {
	case len(leaf.DNS) > 0:
		cn = leaf.DNS[0]
	case len(leaf.IPs) > 0:
		cn = leaf.IPs[0].String()
	default:
		return nil, errors.New("sertifikat web butuh minimal satu nama atau IP")
	}
	// A certificate outside the constraints would be rejected by every
	// client; refuse to issue it.
	if !w.Covers(leaf) {
		return nil, errors.New("nama sertifikat web di luar batasan CA web")
	}
	certPEM, _, _, err := w.ca.issueServerLeaf(cn, leaf.DNS, leaf.IPs, &key.PublicKey)
	return certPEM, err
}

// CAPEM returns the web CA certificate in PEM form.
func (w *WebCA) CAPEM() []byte {
	return w.ca.CAPEM()
}
