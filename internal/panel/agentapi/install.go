package agentapi

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/web"
)

const (
	msgTokenInvalid = "token tidak valid"
	msgNoBinary     = "binary untuk arsitektur ini belum tersedia di panel"
)

// installScript is the agent installer served at GET /agent/install. It is
// a shell script, so it is rendered with text/template; every value goes
// through shq.
var installScript = template.Must(template.New("agent-install.sh.tmpl").
	Funcs(template.FuncMap{"shq": shellQuote}).
	ParseFS(web.FS, "templates/agent-install.sh.tmpl"))

// shellQuote returns s as one single-quoted shell word: nothing inside is
// expanded. A single quote in s ends the quoting, is written escaped (\')
// and the quoting resumes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// InstallScriptParams fill agent-install.sh.tmpl.
type InstallScriptParams struct {
	PanelURL  string // UI base URL, e.g. http://192.168.1.10:8080
	Token     string // pairing token (Crockford base32, safe in the script)
	AgentAddr string // host:port of the mTLS agent listener
	CASHA256  string // CAFingerprint of the panel's CA, pinned by the agent
	// BinSHA256 maps an architecture (amd64, arm64) to the sha256 of the
	// agent binary the panel serves for it; a missing one is rendered empty
	// and the script refuses that architecture.
	BinSHA256 map[string]string
}

// RenderInstallScript renders the agent install script. The output depends
// only on p, so the UI can put its hash in the pairing command and the
// operator's shell can check that the script GET /agent/install serves is
// the one the panel rendered.
func RenderInstallScript(p InstallScriptParams) ([]byte, error) {
	var buf bytes.Buffer
	if err := installScript.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CAFingerprint returns the sha256 (lowercase hex) of the DER of the CA
// certificate in caPEM, or "" when caPEM is not exactly one certificate.
func CAFingerprint(caPEM []byte) string {
	fp, err := ca.Fingerprint(caPEM)
	if err != nil {
		return ""
	}
	return fp
}

// NewInstallScriptParams are the parameters GET /agent/install renders the
// script with for token: the panel's public URL, its agent address, its CA
// fingerprint and the hash of every agent binary it can serve. The UI uses
// the same function, so the hash it shows matches the served script.
func NewInstallScriptParams(d Deps, publicURL, token string, hashes *BinHashes) InstallScriptParams {
	p := InstallScriptParams{
		PanelURL:  publicURL,
		Token:     token,
		AgentAddr: d.AgentAddr(),
		CASHA256:  CAFingerprint(d.CA.CAPEM()),
		BinSHA256: map[string]string{},
	}
	for _, arch := range []string{"amd64", "arm64"} {
		sum, err := hashes.For(arch)
		if err != nil {
			if !errors.Is(err, errNoBinary) {
				d.logger().Warn("hash agent binary", "arch", arch, "err", err)
			}
			continue
		}
		p.BinSHA256[arch] = sum
	}
	return p
}

// agentArches are the architectures agents are built for.
var agentArches = map[string]bool{"amd64": true, "arm64": true}

// peekToken checks the request's token query parameter without consuming
// it. On failure it has written the response and returns false.
func (d Deps) peekToken(w http.ResponseWriter, r *http.Request, what string) (store.PairToken, bool) {
	pt, err := d.Store.PeekPairToken(r.URL.Query().Get("token"), d.now())
	if errors.Is(err, store.ErrTokenInvalid) {
		d.logger().Warn(what+" rejected: invalid token", "remote", remoteIP(r))
		http.Error(w, msgTokenInvalid, http.StatusForbidden)
		return store.PairToken{}, false
	}
	if err != nil {
		d.logger().Error(what+": peek token", "err", err)
		http.Error(w, "kesalahan internal", http.StatusInternalServerError)
		return store.PairToken{}, false
	}
	return pt, true
}

// InstallScriptHandler serves GET /agent/install?token=T: the agent install
// script for a valid, unused pairing token. The token is only checked, not
// consumed; "dbpanel agent pair" run by the script consumes it.
func InstallScriptHandler(d Deps, publicURL func() string, hashes *BinHashes) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pt, ok := d.peekToken(w, r, "install script")
		if !ok {
			return
		}
		script, err := RenderInstallScript(NewInstallScriptParams(d, publicURL(), pt.Token, hashes))
		if err != nil {
			d.logger().Error("render install script", "err", err)
			http.Error(w, "kesalahan internal", http.StatusInternalServerError)
			return
		}
		d.logger().Info("agent install script served", "server_id", pt.ServerID, "remote", remoteIP(r))
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(script)
	})
}

// BinaryHandler serves GET /agent/bin/{arch}?token=T: the linux dbpanel
// binary for arch (amd64 or arm64) to a holder of a valid pairing token. It
// serves binDir/dbpanel_linux_<arch> when present, else the panel's own
// executable when it is a linux build for arch.
func BinaryHandler(d Deps, binDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pt, ok := d.peekToken(w, r, "agent binary")
		if !ok {
			return
		}
		arch := r.PathValue("arch")
		if !agentArches[arch] {
			http.Error(w, msgNoBinary, http.StatusNotFound)
			return
		}
		f, err := openAgentBinary(binDir, arch)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, msgNoBinary, http.StatusNotFound)
			return
		}
		if err != nil {
			d.logger().Error("open agent binary", "arch", arch, "err", err)
			http.Error(w, "kesalahan internal", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			d.logger().Error("stat agent binary", "path", f.Name(), "err", err)
			http.Error(w, "kesalahan internal", http.StatusInternalServerError)
			return
		}
		d.logger().Info("agent binary served", "server_id", pt.ServerID, "arch", arch,
			"path", f.Name(), "remote", remoteIP(r))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", fi.ModTime(), f)
	})
}

// openAgentBinary opens binDir/dbpanel_linux_<arch>, falling back to the
// running executable when it is a linux build for arch. It returns an error
// wrapping os.ErrNotExist when neither is available.
func openAgentBinary(binDir, arch string) (*os.File, error) {
	f, err := openRegular(filepath.Join(binDir, "dbpanel_linux_"+arch))
	if !errors.Is(err, os.ErrNotExist) || runtime.GOOS != "linux" || arch != runtime.GOARCH {
		return f, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	return openRegular(exe)
}

// openRegular opens path, treating anything but a regular file as missing.
func openRegular(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return f, nil
}

func remoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
