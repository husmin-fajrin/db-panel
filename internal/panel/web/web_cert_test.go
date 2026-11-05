package web_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
)

// fakeCerts records what the settings page asks of the certificates.
type fakeCerts struct {
	info       web.CertInfo
	uploads    int
	cert, key  string
	problem    string // UseCustom's and SetExtraNames' answer
	usedPanel  bool
	extraNames string
}

func (f *fakeCerts) CAPEM() []byte {
	return []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
}
func (f *fakeCerts) UseCustom(c, k []byte) (string, error) {
	f.uploads++
	f.cert, f.key = string(c), string(k)
	if f.problem == "" {
		f.info.Mode = "custom"
	}
	return f.problem, nil
}
func (f *fakeCerts) UsePanel() error { f.usedPanel, f.info.Mode = true, "panel"; return nil }
func (f *fakeCerts) SetExtraNames(raw string) (string, error) {
	if f.problem != "" {
		return f.problem, nil
	}
	f.extraNames = raw
	return "", nil
}

func certEnv(t *testing.T) (*settingsRig, *fakeCerts) {
	t.Helper()
	fc := &fakeCerts{info: web.CertInfo{Mode: "panel", Names: []string{"panel.kantor.local", "192.168.1.10"},
		NotAfter: time.Date(2028, 11, 5, 0, 0, 0, 0, time.UTC), Pin: testWebPin}}
	rig := settingsEnv(t, func(d *web.Deps) {
		d.CertInfo = func() web.CertInfo { return fc.info }
		d.Certs = fc
	})
	return rig, fc
}

func TestPairCommandHasPin(t *testing.T) {
	cmd := web.PairCommand(testPublicURL, "ABCDE-FGHJK-MNPQR", strings.Repeat("ab", 32), testWebPin)
	want := `f=$(mktemp) && curl -fsSL -k --pinnedpubkey ` + testWebPin + ` --max-redirs 0 -K - <<<'url = "` +
		testPublicURL + `/agent/install?token=ABCDE-FGHJK-MNPQR"' -o "$f" && echo "` + strings.Repeat("ab", 32) +
		`  $f" | sha256sum -c - && sudo bash "$f"`
	if cmd != want {
		t.Fatalf("command:\n%s\nwant:\n%s", cmd, want)
	}
}

// TestCertPage: /sertifikat needs no login and explains, per system, how to
// install the downloadable certificate; in custom mode there is nothing to
// install.
func TestCertPage(t *testing.T) {
	rig, fc := certEnv(t)
	rec := rig.do("GET", "/sertifikat", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Pasang sertifikat keamanan panel",
		"Browser menampilkan peringatan? Unduh berkas ini, klik dua kali, pilih Pasang, lalu buka panel lagi.",
		`href="/sertifikat/dbpanel-panel.crt"`, "Windows", "macOS", "Firefox", "Android", "iPhone",
		"/static/cert/windows.svg", "/static/cert/ios.svg",
		"Admin domain Windows bisa menyebarkan berkas ini ke semua komputer lewat Group Policy (Computer Configuration → "+
			"Policies → Windows Settings → Security Settings → Public Key Policies → Trusted Root Certification Authorities)")
	for _, h := range regexp.MustCompile(`<h[12][^>]*>([^<]*)</h[12]>`).FindAllStringSubmatch(rec.Body.String(), -1) {
		for _, word := range []string{"CA", "SSL", "TLS"} {
			if strings.Contains(h[1], word) {
				t.Errorf("heading %q uses %q", h, word)
			}
		}
	}
	fc.info.Mode = "custom"
	rec = rig.do("GET", "/sertifikat", nil, nil)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Panel memakai sertifikat yang dipasang admin; tidak ada yang perlu dipasang.")
	if strings.Contains(rec.Body.String(), "dbpanel-panel.crt") {
		t.Error("custom mode still offers the download")
	}
	// The images are served, small.
	for _, img := range []string{"windows", "macos", "firefox", "android", "ios"} {
		rec := rig.do("GET", "/static/cert/"+img+".svg", nil, nil)
		wantCode(t, rec, http.StatusOK)
		if rec.Body.Len() > 50<<10 {
			t.Errorf("%s.svg is %d bytes", img, rec.Body.Len())
		}
	}
}

func TestCertDownload(t *testing.T) {
	rig, fc := certEnv(t)
	rec := rig.do("GET", "/sertifikat/dbpanel-panel.crt", nil, nil)
	wantCode(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="dbpanel-panel.crt"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if rec.Body.String() != string(fc.CAPEM()) {
		t.Errorf("body = %q", rec.Body.String())
	}
}

// TestSettingsCertSection: the settings page shows the certificate in use
// and, under Lanjutan, the upload form and the extra names.
func TestSettingsCertSection(t *testing.T) {
	rig, fc := certEnv(t)
	c := rig.cookie()
	rec := rig.do("GET", "/settings", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Sertifikat keamanan panel", "Sertifikat buatan panel", "panel.kantor.local", "192.168.1.10",
		testWebPin, `href="/sertifikat/dbpanel-panel.crt"`, "Lanjutan", `action="/settings/cert"`,
		`enctype="multipart/form-data"`, `action="/settings/cert-names"`)
	if strings.Contains(rec.Body.String(), "Kembali ke sertifikat panel") {
		t.Error("panel mode offers Kembali ke sertifikat panel")
	}
	fc.info.Mode = "custom"
	rec = rig.do("GET", "/settings", nil, c)
	wantBody(t, rec, "Sertifikat yang dipasang admin", "Kembali ke sertifikat panel")

	rec = rig.do("POST", "/settings/cert-panel", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if !fc.usedPanel || fc.info.Mode != "panel" {
		t.Fatal("Kembali ke sertifikat panel did not switch back")
	}

	rec = rig.do("POST", "/settings/cert-names", url.Values{"extra_names": {"panel.kantor.local, 10.9.0.4"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if fc.extraNames != "panel.kantor.local, 10.9.0.4" {
		t.Fatalf("extra names = %q", fc.extraNames)
	}
	fc.problem = "Bukan alamat IP atau nama host lengkap (mis. panel.kantor.local): lokal"
	rec = rig.do("POST", "/settings/cert-names", url.Values{"extra_names": {"lokal"}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, fc.problem)
}

// postUpload posts a certificate upload as the settings form does.
func postUpload(rig *settingsRig, cert, key string, c *http.Cookie) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range map[string]string{"cert_file": cert, "key_file": key} {
		if data == "" {
			continue
		}
		fw, _ := mw.CreateFormFile(name, name+".pem")
		fw.Write([]byte(data))
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/settings/cert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	return rec
}

// TestUploadCertHandler: the files reach UseCustom; its refusal is a 400
// with the reason; the key never reaches the audit log.
func TestUploadCertHandler(t *testing.T) {
	rig, fc := certEnv(t)
	c := rig.cookie()
	fc.problem = "Kunci tidak cocok dengan sertifikat, atau bukan kunci PEM yang sah."
	rec := postUpload(rig, "SERT", "KUNCI-RAHASIA", c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, fc.problem)
	if fc.cert != "SERT" || fc.key != "KUNCI-RAHASIA" {
		t.Fatalf("UseCustom got %q, %q", fc.cert, fc.key)
	}
	wantCode(t, postUpload(rig, "SERT", "", c), http.StatusBadRequest)

	fc.problem = ""
	rec = postUpload(rig, "SERT", "KUNCI-RAHASIA", c)
	wantCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/settings?pesan=sertifikat-sendiri" {
		t.Fatalf("Location = %q", loc)
	}
	entries, err := rig.st.ListAudit(0, time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == "https_cert_upload" {
			n++
		}
		if strings.Contains(e.DetailJSON, "KUNCI") || strings.Contains(e.Outcome, "KUNCI") {
			t.Fatalf("audit entry holds the key: %+v", e)
		}
	}
	if n != 3 {
		t.Fatalf("https_cert_upload entries = %d, want 3", n)
	}
}

// TestBaseURLHTTPWarning: a stored http:// address for links is shown with
// a warning and a one-click change to https://.
func TestBaseURLHTTPWarning(t *testing.T) {
	rig, _ := certEnv(t)
	c := rig.cookie()
	if err := rig.st.SetSetting(store.SettingPanelBaseURL, "http://panel.example.com:8080"); err != nil {
		t.Fatal(err)
	}
	rec := rig.do("GET", "/settings", nil, c)
	wantBody(t, rec, "ubah ke https", `action="/settings/base-url-https"`)
	rec = rig.do("POST", "/settings/base-url-https", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	if got, _ := rig.st.GetSetting(store.SettingPanelBaseURL); got != "https://panel.example.com:8080" {
		t.Fatalf("base URL = %q", got)
	}
	rec = rig.do("GET", "/settings", nil, c)
	if strings.Contains(rec.Body.String(), "ubah ke https") {
		t.Error("warning still shown for an https address")
	}
}

// TestCertNotice: after the panel replaced its certificate every page
// shows the notice until a user closes it.
func TestCertNotice(t *testing.T) {
	rig, fc := certEnv(t)
	c := rig.cookie()
	const notice = "Sertifikat keamanan panel diperbarui; komputer yang pernah memasangnya perlu memasang ulang"
	if strings.Contains(rig.do("GET", "/servers", nil, c).Body.String(), notice) {
		t.Fatal("notice shown without a replacement")
	}
	fc.info.CANeedsReinstall = true
	for _, path := range []string{"/", "/servers", "/settings"} {
		wantBody(t, rig.do("GET", path, nil, c), notice, `action="/settings/cert-notice-close"`)
	}
	req := httptest.NewRequest("POST", "/settings/cert-notice-close", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Referer", testOrigin+"/servers?x=1")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/servers?x=1" {
		t.Fatalf("close = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if v, _ := rig.st.GetSetting(store.SettingHTTPSCANoticeClosedAt); v == "" {
		t.Fatal("closing the notice stored nothing")
	}
}
