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
)

// RFC 5280 constrains a name type only when the CA lists a subtree of that
// type; any type left out is unconstrained. Every type the web CA does not
// permit is therefore excluded outright, in the CA/Browser Forum Baseline
// Requirements form: a zero-length name excludes every DNS name, email
// address and URI host, and 0.0.0.0/0 plus ::/0 exclude every IP.
var (
	excludeAll   = []string{""}
	excludeAllIP = []*net.IPNet{
		{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
		{IP: net.IPv6unspecified, Mask: net.CIDRMask(0, 128)},
	}
)

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
// web CA constrained to names when neither exists (both files 0600). DNS
// names are lowercased and must be fully qualified host names (see
// normalizeHosts). An existing CA is loaded as is, whatever names says: use
// Covers to decide whether it still fits.
func LoadOrCreateWebCA(dir string, names WebNames) (*WebCA, error) {
	if len(names.DNS) == 0 && len(names.IPs) == 0 && len(names.Subnets) == 0 {
		return nil, errors.New("ca: web CA butuh minimal satu nama, IP atau subnet")
	}
	dns, err := normalizeHosts(names.DNS)
	if err != nil {
		return nil, fmt.Errorf("ca: web CA: %w", err)
	}
	ranges := append([]*net.IPNet(nil), names.Subnets...)
	for _, ip := range names.IPs {
		ranges = append(ranges, hostNet(ip))
	}
	tmpl := &x509.Certificate{
		Subject: pkix.Name{CommonName: "dbpanel-web-ca"},
		// Only ever vouches for TLS servers, whatever a leaf claims.
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         dns,
		PermittedIPRanges:           ranges,
		ExcludedEmailAddresses:      excludeAll,
		ExcludedURIDomains:          excludeAll,
	}
	if len(dns) == 0 {
		tmpl.ExcludedDNSDomains = excludeAll
	}
	if len(ranges) == 0 {
		tmpl.ExcludedIPRanges = excludeAllIP
	}
	c, err := loadOrCreate(dir, webCACertFile, webCAKeyFile, tmpl, 0o600)
	if err != nil {
		return nil, err
	}
	return &WebCA{ca: c}, nil
}

// normalizeHosts lowercases names and checks each is a fully qualified host
// name: at least two labels of ASCII letters, digits and inner hyphens (1-63
// bytes each, 253 in all), no wildcard, no leading or trailing dot. An empty
// name would lift the DNS constraint entirely and a single label ("local")
// would cover a whole TLD, so both are rejected.
func normalizeHosts(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		h := strings.ToLower(name)
		if !validHost(h) {
			return nil, fmt.Errorf("nama %q bukan nama host lengkap yang sah", name)
		}
		out = append(out, h)
	}
	return out, nil
}

// ValidWebHost reports whether h (any case) is a name LoadOrCreateWebCA and
// the web certificate accept: a fully qualified host name.
func ValidWebHost(h string) bool {
	return validHost(strings.ToLower(h))
}

func validHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
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
	dns, err := normalizeHosts(names.DNS)
	if err != nil {
		return nil, fmt.Errorf("sertifikat web: %w", err)
	}
	leaf := WebNames{DNS: dns, IPs: names.IPs}
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
