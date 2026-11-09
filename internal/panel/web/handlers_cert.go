package web

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// CertInfo describes the panel's HTTPS certificate in use.
type CertInfo struct {
	Mode     string    // "panel" (issued by the panel itself) or "custom" (uploaded)
	Names    []string  // host names and IPs the certificate covers
	NotAfter time.Time // expiry
	Pin      string    // SPKI pin, curl --pinnedpubkey form
	// CANeedsReinstall: the panel replaced the certificate users install
	// (a name fell outside it) and no user has closed the notice yet.
	CANeedsReinstall bool
	// CAFingerprint is the SHA-256 of the certificate users install
	// (AA:BB:...), to compare out of band before installing it.
	CAFingerprint string
	// CustomFallback: an uploaded certificate is set but unusable (expired
	// or broken), so the panel serves its own.
	CustomFallback bool
}

// CertAdmin is what the UI changes of the panel's HTTPS certificate.
type CertAdmin interface {
	// CAPEM is the certificate users install (/sertifikat/dbpanel-panel.crt).
	CAPEM() []byte
	// UseCustom validates and stores an uploaded certificate (PEM, chain
	// optional) with its key and serves it from now on; problem is why it
	// was refused, in words for the user.
	UseCustom(certPEM, keyPEM []byte) (problem string, err error)
	// UsePanel serves the panel's own certificate again.
	UsePanel() error
	// SetExtraNames saves the extra names of the panel's own certificate
	// and re-issues it; problem lists the refused entries.
	SetExtraNames(raw string) (problem string, err error)
}

// noCerts stands in when Deps.Certs is nil.
type noCerts struct{}

var errNoCerts = errors.New("sertifikat panel tidak tersedia")

func (noCerts) CAPEM() []byte                            { return nil }
func (noCerts) UseCustom([]byte, []byte) (string, error) { return "", errNoCerts }
func (noCerts) UsePanel() error                          { return errNoCerts }
func (noCerts) SetExtraNames(string) (string, error)     { return "", errNoCerts }

const (
	certModeCustom = "custom"
	// certFileName is the name the downloaded certificate is saved under.
	certFileName = "dbpanel-panel.crt"
	// maxCertUpload caps an uploaded certificate (with its chain) and key.
	maxCertUpload = 64 << 10
)

type certPage struct {
	Page
	Custom bool
	// Fingerprint is the SHA-256 of the certificate to install; HTTPS is
	// false when the page came over plain HTTP (anyone on the network could
	// have changed it, the fingerprint included).
	Fingerprint string
	HTTPS       bool
	// HTTPSURL is this page over https://.
	HTTPSURL string
}

// certPage serves GET /sertifikat (also over plain HTTP, without login):
// how to install the panel's certificate on each system.
func (a *app) certPage(w http.ResponseWriter, r *http.Request) {
	ci := a.CertInfo()
	p := certPage{Page: a.page(r), Custom: ci.Mode == certModeCustom, Fingerprint: ci.CAFingerprint,
		HTTPS: r.TLS != nil, HTTPSURL: "https://" + r.Host + "/sertifikat"}
	p.CertIPURL = certIPURL(r, ci)
	a.render.Render(w, "cert", p)
}

// certIPURL is http://<IP>:<port>/sertifikat for the first IP (IPv4 first)
// the certificate in use names, at the port of r: a browser that holds
// HSTS for the panel's name and refuses it after a certificate change can
// still reach the certificate page there, since HSTS never applies to an
// IP address. "" when the certificate names no IP.
func certIPURL(r *http.Request, ci CertInfo) string {
	var ip net.IP
	for _, n := range ci.Names {
		if p := net.ParseIP(n); p != nil && (ip == nil || ip.To4() == nil && p.To4() != nil) {
			ip = p
		}
	}
	if ip == nil {
		return ""
	}
	host := ip.String()
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		host = net.JoinHostPort(host, port)
	} else if ip.To4() == nil {
		host = "[" + host + "]"
	}
	return "http://" + host + "/sertifikat"
}

// certDownload serves GET /sertifikat/dbpanel-panel.crt: the certificate
// users install so their browser trusts the panel.
func (a *app) certDownload(w http.ResponseWriter, r *http.Request) {
	pemBytes := a.Certs.CAPEM()
	if len(pemBytes) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="`+certFileName+`"`)
	w.Write(pemBytes)
}

// msgUploadTooBig refuses an upload over maxCertUpload.
const msgUploadTooBig = "Berkas terlalu besar: sertifikat (dengan rantainya) dan kunci bersama-sama paling besar 64 KiB."

// errUploadTooBig: an uploaded file is over maxCertUpload.
var errUploadTooBig = errors.New("berkas terlalu besar")

// readUpload reads the multipart file field name, at most maxCertUpload
// bytes; a longer file is errUploadTooBig, never silently cut short.
func readUpload(r *http.Request, name string) ([]byte, error) {
	f, _, err := r.FormFile(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCertUpload+1))
	if err == nil && len(b) > maxCertUpload {
		return nil, errUploadTooBig
	}
	return b, err
}

// uploadCert serves POST /settings/cert: use an uploaded certificate and
// key. The key is never shown or logged.
func (a *app) uploadCert(w http.ResponseWriter, r *http.Request) {
	refuse := func(msg string) {
		a.audit(r, "https_cert_upload", auditSettings, 0, nil, msg)
		a.renderStoredSettings(w, r, http.StatusBadRequest, msg)
	}
	if err := r.ParseMultipartForm(maxCertUpload); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			a.audit(r, "https_cert_upload", auditSettings, 0, nil, msgUploadTooBig)
			a.renderStoredSettings(w, r, http.StatusRequestEntityTooLarge, msgUploadTooBig)
			return
		}
		refuse("Pilih berkas sertifikat dan berkas kunci.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	// Parsing the form only reads it; the token is checked before anything
	// is done with it.
	if !a.requireConfirm(w, r, ActCertUpload, auditSettings, 0) {
		return
	}
	certPEM, certErr := readUpload(r, "cert_file")
	keyPEM, keyErr := readUpload(r, "key_file")
	if errors.Is(certErr, http.ErrMissingFile) || errors.Is(keyErr, http.ErrMissingFile) {
		refuse("Pilih berkas sertifikat dan berkas kunci.")
		return
	}
	if errors.Is(certErr, errUploadTooBig) || errors.Is(keyErr, errUploadTooBig) {
		refuse(msgUploadTooBig)
		return
	}
	if err := errors.Join(certErr, keyErr); err != nil {
		refuse("Berkas tidak bisa dibaca.")
		return
	}
	problem, err := a.Certs.UseCustom(certPEM, keyPEM)
	if err != nil {
		a.audit(r, "https_cert_upload", auditSettings, 0, nil, err.Error())
		a.internalError(w, "use uploaded certificate", err)
		return
	}
	if problem != "" {
		refuse(problem)
		return
	}
	a.audit(r, "https_cert_upload", auditSettings, 0, map[string]any{"names": a.CertInfo().Names}, auditOK)
	a.Logger.Info("uploaded HTTPS certificate in use", "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, "/settings?pesan=sertifikat-sendiri", http.StatusSeeOther)
}

// usePanelCert serves POST /settings/cert-panel: back to the panel's own
// certificate.
func (a *app) usePanelCert(w http.ResponseWriter, r *http.Request) {
	if !a.requireConfirm(w, r, ActCertRevert, auditSettings, 0) {
		return
	}
	err := a.Certs.UsePanel()
	a.audit(r, "https_cert_panel", auditSettings, 0, nil, outcomeOf(err))
	if err != nil {
		a.internalError(w, "use panel certificate", err)
		return
	}
	http.Redirect(w, r, "/settings?pesan=sertifikat-panel", http.StatusSeeOther)
}

// saveCertNames serves POST /settings/cert-names: the extra names of the
// panel's own certificate.
func (a *app) saveCertNames(w http.ResponseWriter, r *http.Request) {
	if !a.requireConfirm(w, r, ActCertNames, auditSettings, 0) {
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("extra_names"))
	problem, err := a.Certs.SetExtraNames(raw)
	if problem != "" {
		a.audit(r, "https_cert_names", auditSettings, 0, nil, problem)
		a.renderStoredSettings(w, r, http.StatusBadRequest, problem)
		return
	}
	a.audit(r, "https_cert_names", auditSettings, 0, map[string]any{"extra_names": raw}, outcomeOf(err))
	if err != nil {
		a.internalError(w, "save extra certificate names", err)
		return
	}
	http.Redirect(w, r, "/settings?pesan=sertifikat-nama", http.StatusSeeOther)
}

// closeCertNotice serves POST /settings/cert-notice-close: a user saw that
// the certificate to install changed.
func (a *app) closeCertNotice(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.SetSetting(store.SettingHTTPSCANoticeClosedAt, a.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		a.internalError(w, "close certificate notice", err)
		return
	}
	http.Redirect(w, r, localBack(r), http.StatusSeeOther)
}

// localBack is the same-origin page a form was posted from, else /.
func localBack(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || u.Host != r.Host || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.RequestURI()
}

// baseURLToHTTPS serves POST /settings/base-url-https: a stored panel
// address for Telegram links that starts with http:// gets https://.
func (a *app) baseURLToHTTPS(w http.ResponseWriter, r *http.Request) {
	set, err := a.Settings.Load()
	if err != nil {
		a.internalError(w, "load settings", err)
		return
	}
	rest, ok := cutPrefixFold(set.PanelBaseURL, "http://")
	if !ok {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	next := set
	next.PanelBaseURL = "https://" + rest
	err = a.Settings.Save(next)
	a.audit(r, "settings_update", auditSettings, 0, map[string]any{
		"changed": []string{store.SettingPanelBaseURL}, store.SettingPanelBaseURL: next.PanelBaseURL,
	}, outcomeOf(err))
	if err != nil {
		a.internalError(w, "save settings", err)
		return
	}
	a.Settings.Reconfigure()
	http.Redirect(w, r, "/settings?pesan=pengaturan", http.StatusSeeOther)
}

// cutPrefixFold is strings.CutPrefix ignoring ASCII case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// plainBaseURL reports whether a panel address for links is http://.
func plainBaseURL(s string) bool {
	_, ok := cutPrefixFold(s, "http://")
	return ok
}
