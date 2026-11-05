package panel

// The UI listens on a single port for both HTTPS and plain HTTP.
//
// splitListener owns the raw listener. A single goroutine accepts
// connections and hands each one to its own short-lived goroutine, which
// reads the first byte within peekTimeout: a TLS handshake always starts
// with a handshake record (0x16), anything else is plain HTTP. The
// connection, with that byte put back in front, then goes to one of two
// channel-fed listeners: the TLS one is wrapped by tls.NewListener and served
// by the panel's main http.Server, the plain one by a small http.Server that
// only serves the certificate page (httpHelperHandler) and redirects every
// other request to https://. Peeking in a goroutine per connection (and not
// in Accept itself) keeps a client that connects and stays silent from
// holding up everyone else; the peek ends at peekTimeout, when the listener
// closes, or when the client sends a byte, so no goroutine outlives its
// connection.
//
// The certificate is chosen per handshake (GetCertificate) from webCerts
// under a mutex, so a renewed or uploaded certificate is used by the next
// connection without a restart. There is no plain-HTTP fallback: a broken
// certificate falls back to the panel's own, re-issued when needed.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/pki"
)

const (
	// peekTimeout bounds the wait for a new connection's first byte, and
	// the plain HTTP helper's wait for a request header.
	peekTimeout = 5 * time.Second
	// tlsHandshakeRecord is the first byte of every TLS connection.
	tlsHandshakeRecord = 0x16

	webCertFile    = "web.crt"
	webKeyFile     = "web.key"
	customCertFile = "web.custom.crt"
	customKeyFile  = "web.custom.key"
	webCAFiles     = "web-ca" // web-ca.crt, web-ca.key (package ca)

	// webRenewBefore: the panel's certificate is re-issued this long
	// before it expires.
	webRenewBefore = 30 * 24 * time.Hour
	// webRenewEvery is how often the certificate is checked.
	webRenewEvery = 24 * time.Hour

	httpsModePanel  = "panel"
	httpsModeCustom = "custom"
)

// CertInfo describes the panel's HTTPS certificate in use.
type CertInfo = web.CertInfo

// certInfo describes the HTTPS certificate in use.
func (p *Panel) certInfo() CertInfo { return p.certs.info() }

// webCerts holds the panel's HTTPS certificates: its own (web.crt, issued by
// its web CA) and an uploaded one (web.custom.crt) when https_mode is
// custom.
type webCerts struct {
	dir        string // ConfigDir
	st         *store.Store
	log        *slog.Logger
	now        func() time.Time
	publicHost string
	// names returns the names the panel's own certificate must cover now.
	names func() ca.WebNames

	mu     sync.Mutex
	ca     *ca.WebCA
	own    *servedCert // web.crt
	custom *servedCert // web.custom.crt; nil in panel mode
}

// servedCert is a certificate the listener can serve.
type servedCert struct {
	cert *tls.Certificate // Leaf set
	pin  string           // SPKI pin of the leaf
}

func newServedCert(certPEM, keyPEM []byte) (*servedCert, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	pin, err := pki.SPKIPin(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Leaf.Raw}))
	if err != nil {
		return nil, err
	}
	return &servedCert{cert: &pair, pin: pin}, nil
}

// load prepares the certificates at start: the web CA (replaced when the
// current names fall outside it or it cannot be read), the panel's own
// certificate (issued or re-issued as needed) and, in custom mode, the
// uploaded one; an unusable uploaded one is skipped with a warning.
func (w *webCerts) load() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := w.names()
	if err := w.ensureCA(names); err != nil {
		return err
	}
	if err := w.ensureOwn(names); err != nil {
		return err
	}
	mode, err := w.st.GetSetting(store.SettingHTTPSMode)
	if err != nil {
		return err
	}
	if mode != httpsModeCustom {
		return nil
	}
	certPEM, certErr := os.ReadFile(w.path(customCertFile))
	keyPEM, keyErr := os.ReadFile(w.path(customKeyFile))
	c, err := newServedCert(certPEM, keyPEM)
	switch {
	case certErr != nil || keyErr != nil:
		w.log.Warn("uploaded HTTPS certificate unreadable; using the panel's own", "err", errors.Join(certErr, keyErr))
	case err != nil:
		w.log.Warn("uploaded HTTPS certificate broken; using the panel's own", "err", err)
	case !c.cert.Leaf.NotAfter.After(w.now()):
		w.log.Warn("uploaded HTTPS certificate expired; using the panel's own", "not_after", c.cert.Leaf.NotAfter)
	default:
		w.custom = c
	}
	return nil
}

func (w *webCerts) path(name string) string { return filepath.Join(w.dir, name) }

// ensureCA loads the web CA, replacing it when it cannot be read or does
// not cover names.
//
// Only a CA that cannot be parsed (or half a pair) is replaced: an I/O error
// (e.g. a permission problem) stops the start instead, since a new CA would
// make every browser that installed the current one warn again.
func (w *webCerts) ensureCA(names ca.WebNames) error {
	_, certErr := os.ReadFile(w.path(webCAFiles + ".crt"))
	_, keyErr := os.ReadFile(w.path(webCAFiles + ".key"))
	if err := ioError(certErr, keyErr); err != nil {
		return fmt.Errorf("baca CA web: %w", err)
	}
	c, err := ca.LoadOrCreateWebCA(w.dir, names)
	if err != nil {
		if certErr != nil && keyErr != nil {
			return err // creating a new CA failed
		}
		w.log.Warn("web CA broken; creating a new one", "err", err)
		return w.regenerateCA(names)
	}
	w.ca = c
	if !c.Covers(names) {
		w.log.Warn("panel names outside the web CA; creating a new one")
		return w.regenerateCA(names)
	}
	return nil
}

// regenerateCA moves the web CA files to *.old (replacing older ones),
// creates a new CA for names and records when, so the UI asks users to
// install it again.
func (w *webCerts) regenerateCA(names ca.WebNames) error {
	// The key first: a failure between the two leaves the old certificate
	// without its key, which the next start replaces, never a new key
	// beside the old certificate.
	for _, ext := range []string{".key", ".crt"} {
		p := w.path(webCAFiles + ext)
		if err := os.Rename(p, p+".old"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("simpan CA web lama: %w", err)
		}
	}
	c, err := ca.LoadOrCreateWebCA(w.dir, names)
	if err != nil {
		return err
	}
	w.ca = c
	return w.st.SetSetting(store.SettingHTTPSCARegeneratedAt, w.now().UTC().Format(time.RFC3339Nano))
}

// ensureOwn loads web.crt/web.key and re-issues the certificate, keeping
// its key, when the CA changed, the names changed or it expires within
// webRenewBefore. A missing or broken pair is replaced by a new key and
// certificate; an I/O error is returned (a new key would break the pin in
// pairing commands already shown).
func (w *webCerts) ensureOwn(names ca.WebNames) error {
	certPEM, certErr := os.ReadFile(w.path(webCertFile))
	keyPEM, keyErr := os.ReadFile(w.path(webKeyFile))
	if err := ioError(certErr, keyErr); err != nil {
		return fmt.Errorf("baca sertifikat HTTPS panel: %w", err)
	}
	cur, err := newServedCert(certPEM, keyPEM)
	if certErr != nil || keyErr != nil || err != nil {
		if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
			w.log.Warn("panel HTTPS certificate unreadable; issuing a new one", "err", errors.Join(certErr, keyErr, err))
		}
		certPEM, keyPEM, err = w.ca.IssueWebCert(names)
		if err != nil {
			return err
		}
		return w.storeOwn(certPEM, keyPEM, true)
	}
	if reason := w.reissueReason(cur.cert.Leaf, names); reason != "" {
		certPEM, err = w.ca.IssueWebCertWithKey(names, keyPEM)
		if err != nil {
			return err
		}
		w.log.Info("panel HTTPS certificate re-issued", "reason", reason)
		return w.storeOwn(certPEM, keyPEM, false)
	}
	w.own = cur
	return nil
}

// reissueReason says why leaf must be re-issued for names, or "".
func (w *webCerts) reissueReason(leaf *x509.Certificate, names ca.WebNames) string {
	block, _ := pem.Decode(w.ca.CAPEM())
	caCert, err := x509.ParseCertificate(block.Bytes)
	switch {
	case err != nil || leaf.CheckSignatureFrom(caCert) != nil:
		return "new CA"
	case leaf.NotAfter.Sub(w.now()) < webRenewBefore:
		return "expiring"
	case !sameNames(leaf, names):
		return "names changed"
	}
	return ""
}

// sameNames reports whether leaf's DNS and IP SANs are exactly names'.
func sameNames(leaf *x509.Certificate, names ca.WebNames) bool {
	norm := func(dns []string, ips []net.IP) []string {
		out := make([]string, 0, len(dns)+len(ips))
		for _, d := range dns {
			out = append(out, "dns:"+strings.ToLower(d))
		}
		for _, ip := range ips {
			out = append(out, "ip:"+ip.String())
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(leaf.DNSNames, leaf.IPAddresses), norm(names.DNS, names.IPs))
}

// storeOwn writes the panel's own pair (0600, each file replaced
// atomically) and serves it. A re-issue keeps the key, so only the
// certificate is written and a crash leaves a matching pair either way.
func (w *webCerts) storeOwn(certPEM, keyPEM []byte, newKey bool) error {
	c, err := newServedCert(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if newKey {
		if err := writeFileAtomic(w.path(webKeyFile), keyPEM); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(w.path(webCertFile), certPEM); err != nil {
		return err
	}
	w.own = c
	return nil
}

// renew re-checks the names and the expiry of the panel's own certificate
// (see load), and drops an uploaded certificate that has expired.
func (w *webCerts) renew() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.custom != nil && !w.custom.cert.Leaf.NotAfter.After(w.now()) {
		w.log.Warn("uploaded HTTPS certificate expired; using the panel's own", "not_after", w.custom.cert.Leaf.NotAfter)
		w.custom = nil
	}
	names := w.names()
	if !w.ca.Covers(names) {
		w.log.Warn("panel names outside the web CA; creating a new one")
		if err := w.regenerateCA(names); err != nil {
			return err
		}
	}
	return w.ensureOwn(names)
}

// run renews the certificate every webRenewEvery until done is closed.
func (w *webCerts) run(done <-chan struct{}) {
	t := time.NewTicker(webRenewEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := w.renew(); err != nil {
				w.log.Error("renew panel HTTPS certificate", "err", err)
			}
		}
	}
}

// coversHost reports whether the panel's own or the uploaded certificate
// is valid for host (a name or IP from a Host header), so hostAllowed
// accepts every name the panel can be opened by over HTTPS.
func (w *webCerts) coversHost(host string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range []*servedCert{w.own, w.custom} {
		if c != nil && c.cert.Leaf.VerifyHostname(host) == nil {
			return true
		}
	}
	return false
}

// active is the certificate the listener serves.
func (w *webCerts) active() *servedCert {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.custom != nil {
		return w.custom
	}
	return w.own
}

// getCertificate is the UI listener's tls.Config.GetCertificate.
func (w *webCerts) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return w.active().cert, nil
}

// pin is the SPKI pin of the certificate in use.
func (w *webCerts) pin() string { return w.active().pin }

// tlsConfig is the UI listener's TLS configuration.
func (w *webCerts) tlsConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: w.getCertificate}
}

// info describes the certificate in use; CANeedsReinstall holds from a web
// CA replacement until a user closes its notice.
func (w *webCerts) info() CertInfo {
	w.mu.Lock()
	c, mode := w.own, httpsModePanel
	if w.custom != nil {
		c, mode = w.custom, httpsModeCustom
	}
	w.mu.Unlock()
	leaf := c.cert.Leaf
	info := CertInfo{Mode: mode, NotAfter: leaf.NotAfter, Pin: c.pin, Names: slices.Clone(leaf.DNSNames)}
	for _, ip := range leaf.IPAddresses {
		info.Names = append(info.Names, ip.String())
	}
	kv, err := w.st.GetSettings(store.SettingHTTPSCARegeneratedAt, store.SettingHTTPSCANoticeClosedAt)
	if err != nil {
		w.log.Warn("read certificate notice settings", "err", err)
		return info
	}
	if regen, err := time.Parse(time.RFC3339Nano, kv[store.SettingHTTPSCARegeneratedAt]); err == nil {
		closed, err := time.Parse(time.RFC3339Nano, kv[store.SettingHTTPSCANoticeClosedAt])
		info.CANeedsReinstall = err != nil || regen.After(closed)
	}
	return info
}

// CAPEM is the web CA certificate users install.
func (w *webCerts) CAPEM() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ca.CAPEM()
}

// UseCustom validates an uploaded certificate (PEM, chain optional) and
// key, stores them (0600) and serves them from now on. problem is the
// reason it was refused, in words for the user.
func (w *webCerts) UseCustom(certPEM, keyPEM []byte) (problem string, err error) {
	if problem := validateCustomCert(certPEM, keyPEM, w.publicHost, w.now()); problem != "" {
		return problem, nil
	}
	c, err := newServedCert(certPEM, keyPEM)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// A crash between the two leaves a mismatched pair, which the next
	// start refuses (falling back to the panel's own certificate).
	if err := writeFileAtomic(w.path(customKeyFile), keyPEM); err != nil {
		return "", err
	}
	if err := writeFileAtomic(w.path(customCertFile), certPEM); err != nil {
		return "", err
	}
	if err := w.st.SetSetting(store.SettingHTTPSMode, httpsModeCustom); err != nil {
		return "", err
	}
	w.custom = c
	return "", nil
}

// UsePanel serves the panel's own certificate again and removes the
// uploaded one.
func (w *webCerts) UsePanel() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.st.SetSetting(store.SettingHTTPSMode, httpsModePanel); err != nil {
		return err
	}
	w.custom = nil
	for _, f := range []string{customKeyFile, customCertFile} {
		if err := os.Remove(w.path(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			w.log.Warn("remove uploaded HTTPS certificate", "file", f, "err", err)
		}
	}
	return nil
}

// SetExtraNames saves the additional names of the panel's own certificate
// (comma or space separated host names and IPs) and re-issues it at once.
// problem lists the entries refused, in words for the user.
func (w *webCerts) SetExtraNames(raw string) (problem string, err error) {
	entries := splitNames(raw)
	var bad []string
	for _, e := range entries {
		if net.ParseIP(e) == nil && !ca.ValidWebHost(e) {
			bad = append(bad, e)
		}
	}
	if len(bad) > 0 {
		return "Bukan alamat IP atau nama host lengkap (mis. panel.kantor.local): " + strings.Join(bad, ", "), nil
	}
	if err := w.st.SetSetting(store.SettingHTTPSExtraNames, strings.Join(entries, ",")); err != nil {
		return "", err
	}
	return "", w.renew()
}

// ioError returns the first of errs that is neither nil nor "not found".
func ioError(errs ...error) error {
	for _, err := range errs {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// writeFileAtomic replaces path with data (mode 0600) through a temporary
// file in the same directory and a rename, so a crash leaves the old file
// or the new one, never part of either.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// splitNames splits a list of names on commas and white space.
func splitNames(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
}

// validateCustomCert checks an uploaded certificate: the key matches it, it
// has not expired and it names publicHost. It returns the problem in words
// for the user, or "".
func validateCustomCert(certPEM, keyPEM []byte, publicHost string, now time.Time) string {
	var leaf *x509.Certificate
	for rest := certPEM; leaf == nil; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return "Berkas sertifikat bukan sertifikat PEM yang sah."
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "Berkas sertifikat bukan sertifikat PEM yang sah."
		}
		leaf = c
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return "Kunci tidak cocok dengan sertifikat, atau bukan kunci PEM yang sah."
	}
	if !leaf.NotAfter.After(now) {
		return "Sertifikat sudah kedaluwarsa (berlaku sampai " + leaf.NotAfter.Format("2006-01-02") + ")."
	}
	if err := leaf.VerifyHostname(publicHost); err != nil {
		return "Sertifikat tidak memuat alamat panel " + publicHost + "."
	}
	return ""
}

// webNames builds the names of the panel's own certificate: the public
// host, the machine's host name, every address of the interfaces (with
// their subnets, for the web CA) and the extra names. Single-label and
// invalid host names are left out (a CA cannot be limited to them), each
// extra one with a log line.
func webNames(publicHost, hostname string, ifaces []*net.IPNet, extra string, log *slog.Logger) ca.WebNames {
	var n ca.WebNames
	addIP := func(ip net.IP) {
		if !slices.ContainsFunc(n.IPs, ip.Equal) {
			n.IPs = append(n.IPs, ip)
		}
	}
	addDNS := func(h string) bool {
		h = strings.ToLower(h)
		if !ca.ValidWebHost(h) {
			return false
		}
		if !slices.Contains(n.DNS, h) {
			n.DNS = append(n.DNS, h)
		}
		return true
	}
	if ip := net.ParseIP(publicHost); ip != nil {
		addIP(ip)
	} else if !addDNS(publicHost) {
		log.Warn("public host is not a full host name; left out of the HTTPS certificate", "host", publicHost)
	}
	addDNS(hostname)
	for _, ifn := range ifaces {
		addIP(ifn.IP)
		sub := &net.IPNet{IP: ifn.IP.Mask(ifn.Mask), Mask: ifn.Mask}
		if !slices.ContainsFunc(n.Subnets, func(s *net.IPNet) bool { return s.String() == sub.String() }) {
			n.Subnets = append(n.Subnets, sub)
		}
	}
	for _, e := range splitNames(extra) {
		if ip := net.ParseIP(e); ip != nil {
			addIP(ip)
		} else if !addDNS(e) {
			log.Warn("extra HTTPS name is not an IP or full host name; left out", "name", e)
		}
	}
	return n
}

// interfaceNets returns the addresses, with prefixes, of the interfaces
// that are up, except loopback and link-local ones.
func interfaceNets() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if v4 := ipn.IP.To4(); v4 != nil && len(ipn.Mask) == net.IPv4len {
				ipn = &net.IPNet{IP: v4, Mask: ipn.Mask}
			}
			out = append(out, ipn)
		}
	}
	return out
}

// currentWebNames are the names of the panel's own certificate now.
func (p *Panel) currentWebNames() ca.WebNames {
	extra, err := p.Store.GetSetting(store.SettingHTTPSExtraNames)
	if err != nil {
		p.log.Warn("read extra HTTPS names", "err", err)
	}
	host, _ := os.Hostname()
	return webNames(p.opts.PublicHost, host, interfaceNets(), extra, p.log)
}

// initWebCerts loads (or creates) the HTTPS certificates under ConfigDir.
func (p *Panel) initWebCerts() error {
	p.certs = &webCerts{
		dir:        p.opts.ConfigDir,
		st:         p.Store,
		log:        p.log,
		now:        time.Now,
		publicHost: p.opts.PublicHost,
		names:      p.currentWebNames,
	}
	if err := p.certs.load(); err != nil {
		return fmt.Errorf("sertifikat HTTPS panel: %w", err)
	}
	return nil
}

// splitListener sorts the connections of one listener into TLS and plain
// ones (see the top of this file).
type splitListener struct {
	ln          net.Listener
	tls, plain  *chanListener
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	peeking     map[net.Conn]struct{} // connections whose first byte is awaited
	peekTimeout time.Duration
}

func newSplitListener(ln net.Listener) *splitListener {
	s := &splitListener{ln: ln, done: make(chan struct{}), peeking: map[net.Conn]struct{}{}, peekTimeout: peekTimeout}
	s.tls = &chanListener{s: s, ch: make(chan net.Conn), closed: make(chan struct{})}
	s.plain = &chanListener{s: s, ch: make(chan net.Conn), closed: make(chan struct{})}
	go s.acceptLoop()
	return s
}

// Close stops accepting, closes the raw listener and every connection still
// waiting for its first byte. Connections already handed on belong to
// their server.
func (s *splitListener) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.ln.Close()
		s.mu.Lock()
		for c := range s.peeking {
			c.Close()
		}
		s.mu.Unlock()
	})
	return err
}

func (s *splitListener) acceptLoop() {
	var backoff time.Duration
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				s.Close()
				return
			}
			// Temporary (e.g. too many open files): retry, as http.Server does.
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			select {
			case <-time.After(backoff):
			case <-s.done:
				return
			}
			continue
		}
		backoff = 0
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			c.Close()
			return
		default:
		}
		s.peeking[c] = struct{}{}
		s.mu.Unlock()
		go s.route(c)
	}
}

// route reads c's first byte and hands c to the TLS or the plain listener;
// a client that sends nothing within peekTimeout, or fails, is closed.
func (s *splitListener) route(c net.Conn) {
	var first [1]byte
	c.SetReadDeadline(time.Now().Add(s.peekTimeout))
	_, err := io.ReadFull(c, first[:])
	s.mu.Lock()
	delete(s.peeking, c)
	s.mu.Unlock()
	if err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	to := s.plain
	if first[0] == tlsHandshakeRecord {
		to = s.tls
	}
	pc := &peekedConn{Conn: c, r: io.MultiReader(bytes.NewReader(first[:]), c)}
	select {
	case to.ch <- pc:
	case <-to.closed:
		c.Close()
	case <-s.done:
		c.Close()
	}
}

// chanListener is one side of a splitListener.
type chanListener struct {
	s         *splitListener
	ch        chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
	case <-l.s.done:
	}
	return nil, net.ErrClosed
}

// Close stops this side only; the splitListener is closed by its owner.
func (l *chanListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.s.ln.Addr() }

// peekedConn is a connection whose first byte was read and is read again.
type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// certPaths are the plain HTTP paths served as they are: the certificate
// page, its download and the static assets the page uses.
func certPath(path string) bool {
	return path == "/sertifikat" || path == "/sertifikat/dbpanel-panel.crt" || strings.HasPrefix(path, "/static/")
}

// httpHelperHandler serves plain HTTP: GET of the certificate page, its
// download and the page's static assets through ui; everything else is
// redirected (301) to the same address over https.
func httpHelperHandler(ui http.Handler, allowHost func(string) bool) http.Handler {
	redirect := web.SecurityHeaders(web.RequireHost(allowHost)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && certPath(r.URL.Path) {
			ui.ServeHTTP(w, r)
			return
		}
		redirect.ServeHTTP(w, r)
	})
}

// serverErrorLog is the UI servers' ErrorLog: failed TLS handshakes are
// routine (every browser that has not installed the certificate yet) and
// logged at debug level; anything else is a warning.
func serverErrorLog(l *slog.Logger) *log.Logger {
	return log.New(serverLogWriter{l}, "", 0)
}

type serverLogWriter struct{ l *slog.Logger }

func (w serverLogWriter) Write(b []byte) (int, error) {
	msg := strings.TrimSpace(string(b))
	level := slog.LevelWarn
	if strings.Contains(msg, "TLS handshake error") {
		level = slog.LevelDebug
	}
	w.l.Log(context.Background(), level, "UI server", "msg", msg)
	return len(b), nil
}
