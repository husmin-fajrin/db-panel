// Package agent is the server-side role of dbpanel: it pairs with a panel
// once, then keeps an outbound mTLS WebSocket to it and runs the panel's
// requests through the command registry.
package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/pki"
	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const (
	pairTimeout      = 30 * time.Second
	pairResponseSize = 1 << 20
)

// PairOptions pin the panel and what its pairing answer may not choose by
// itself. All come from the install script, whose hash the operator checked
// against the panel UI.
type PairOptions struct {
	// WebPin is the SPKI pin ("sha256//<base64>", curl --pinnedpubkey form)
	// of the panel's HTTPS certificate. When set, the panel URL must be
	// https and the panel is trusted by its key alone: its certificate
	// chain is not checked (the panel's own web CA is unknown to this
	// machine), and a key other than the pinned one ends the handshake
	// before the request, with the token, is sent.
	WebPin string
	// CASHA256 is the sha256 (hex) of the panel CA certificate's DER. When
	// set, a pairing answer carrying any other CA is refused before
	// anything is written.
	CASHA256 string
	// AgentAddr, when set, is the host:port the agent dials, replacing the
	// one in the pairing answer.
	AgentAddr string
}

// errRedirect is Pair's error when the panel answers the pairing request
// with a redirect.
var errRedirect = errors.New("panel mengalihkan permintaan pairing; ditolak")

// ErrWebPinMismatch is Pair's error when the panel's HTTPS certificate key
// is not the pinned one.
var ErrWebPinMismatch = errors.New("kunci sertifikat panel tidak cocok dengan pin dari perintah pasang; " +
	"kemungkinan ada yang menyusup di jaringan")

// ErrCAMismatch is Pair's error when the panel's CA is not the pinned one.
var ErrCAMismatch = errors.New("CA panel tidak cocok dengan yang diharapkan")

// Pair exchanges a one-time token for a client certificate. It generates the
// agent's key locally, sends only a CSR, and on success writes agent.key,
// agent.crt, ca.crt and finally agent.yaml into dir. A nil client uses one
// with a 30 s timeout.
func Pair(ctx context.Context, panelURL, token, dir string, client *http.Client, opts PairOptions) (config.Config, error) {
	if client == nil {
		client = &http.Client{Timeout: pairTimeout}
	}
	// The request carries the token: never follow a redirect (to another
	// host, e.g. an on-path attacker's). A copy, so the caller's client is
	// left as it was.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	client = &noRedirect
	https := strings.HasPrefix(strings.ToLower(panelURL), "https://")
	if opts.WebPin != "" {
		if !https {
			return config.Config{}, errors.New("pin sertifikat panel hanya berlaku untuk alamat panel https://")
		}
		client.Transport = pinnedTransport(opts.WebPin)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return config.Config{}, fmt.Errorf("baca hostname: %w", err)
	}
	keyPEM, csrPEM, err := pki.NewAgentKeyAndCSR(hostname)
	if err != nil {
		return config.Config{}, err
	}
	body, err := json.Marshal(proto.PairRequest{
		Token:        strings.TrimSpace(token),
		Hostname:     hostname,
		Arch:         runtime.GOARCH,
		CSRPEM:       string(csrPEM),
		AgentVersion: version.Version,
	})
	if err != nil {
		return config.Config{}, err
	}

	url := strings.TrimRight(panelURL, "/") + "/agent/pair"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return config.Config{}, fmt.Errorf("alamat panel tidak valid: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if errors.Is(err, errRedirect) {
		return config.Config{}, errRedirect
	}
	if errors.Is(err, ErrWebPinMismatch) {
		return config.Config{}, ErrWebPinMismatch
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok && https {
		return config.Config{}, errors.New("sertifikat panel tidak dikenal mesin ini; jalankan pairing dengan " +
			"--web-pin (atau " + webPinEnv + ") dari perintah pasang di panel")
	}
	if err != nil {
		return config.Config{}, fmt.Errorf("hubungi panel: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, pairResponseSize))
	if err != nil {
		return config.Config{}, fmt.Errorf("baca jawaban panel: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return config.Config{}, errors.New("token ditolak oleh panel")
	default:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return config.Config{}, fmt.Errorf("panel menjawab %s: %s", resp.Status, e.Error)
		}
		return config.Config{}, fmt.Errorf("panel menjawab %s", resp.Status)
	}

	var pr proto.PairResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return config.Config{}, fmt.Errorf("jawaban panel tidak valid: %w", err)
	}
	if pr.ServerID <= 0 || pr.AgentAddr == "" {
		return config.Config{}, errors.New("jawaban panel tidak lengkap")
	}
	if opts.CASHA256 != "" {
		fp, err := pki.Fingerprint([]byte(pr.CAPEM))
		if err != nil || !strings.EqualFold(fp, opts.CASHA256) {
			return config.Config{}, ErrCAMismatch
		}
	}
	panelAddr := pr.AgentAddr
	if opts.AgentAddr != "" {
		if opts.AgentAddr != pr.AgentAddr {
			slog.Info("using the pinned agent address, not the one in the pairing answer",
				"pinned", opts.AgentAddr, "answer", pr.AgentAddr)
		}
		panelAddr = opts.AgentAddr
	}
	// Reject a certificate that does not match our key or a CA that does not
	// parse before anything is written.
	if _, err := pki.AgentClientTLS([]byte(pr.CertPEM), keyPEM, []byte(pr.CAPEM)); err != nil {
		return config.Config{}, fmt.Errorf("sertifikat dari panel tidak valid: %w", err)
	}

	cfg := config.Config{
		PanelAddr: panelAddr,
		ServerID:  pr.ServerID,
		CertFile:  filepath.Join(dir, "agent.crt"),
		KeyFile:   filepath.Join(dir, "agent.key"),
		CAFile:    filepath.Join(dir, "ca.crt"),
	}
	// A previous configuration's settings that pairing does not own survive
	// a re-pair.
	if old, err := config.Load(dir); err == nil {
		cfg.MySQLRootPassword = old.MySQLRootPassword
	}
	for _, f := range []struct {
		path string
		data []byte
	}{
		{cfg.KeyFile, keyPEM},
		{cfg.CertFile, []byte(pr.CertPEM)},
		{cfg.CAFile, []byte(pr.CAPEM)},
	} {
		if err := config.WriteFile(dir, filepath.Base(f.path), f.data); err != nil {
			return config.Config{}, err
		}
	}
	if err := config.Save(dir, cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// pinnedTransport trusts an HTTPS server by the SPKI pin of its leaf
// certificate only. InsecureSkipVerify turns off the chain and host name
// checks (the panel's web CA is not installed here); VerifyPeerCertificate
// then runs inside the handshake, so a mismatch fails it before any request
// byte is written.
func pinnedTransport(pin string) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return ErrWebPinMismatch
			}
			got, err := pki.SPKIPin(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rawCerts[0]}))
			if err != nil || subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
				return ErrWebPinMismatch
			}
			return nil
		},
	}
	return tr
}
