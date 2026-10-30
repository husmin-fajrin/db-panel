// Package agent is the server-side role of dbpanel: it pairs with a panel
// once, then keeps an outbound mTLS WebSocket to it and runs the panel's
// requests through the command registry.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

// PairRequest is the body the agent POSTs to {panel}/agent/pair. CSRPEM
// carries only the agent's public key.
type PairRequest struct {
	Token        string `json:"token"`
	Hostname     string `json:"hostname"`
	Arch         string `json:"arch"`
	CSRPEM       string `json:"csr_pem"`
	AgentVersion string `json:"agent_version"`
}

// PairResponse is the panel's answer to a successful pairing.
type PairResponse struct {
	ServerID  int64  `json:"server_id"`
	CertPEM   string `json:"cert_pem"`
	CAPEM     string `json:"ca_pem"`
	AgentAddr string `json:"agent_addr"`
}

const (
	pairTimeout      = 30 * time.Second
	pairResponseSize = 1 << 20
)

// PairOptions pin what the pairing answer, which travels over plain HTTP,
// may not choose by itself. Both come from the install script, whose hash
// the operator checked against the panel UI.
type PairOptions struct {
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
	hostname, err := os.Hostname()
	if err != nil {
		return config.Config{}, fmt.Errorf("baca hostname: %w", err)
	}
	keyPEM, csrPEM, err := ca.NewAgentKeyAndCSR(hostname)
	if err != nil {
		return config.Config{}, err
	}
	body, err := json.Marshal(PairRequest{
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

	var pr PairResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return config.Config{}, fmt.Errorf("jawaban panel tidak valid: %w", err)
	}
	if pr.ServerID <= 0 || pr.AgentAddr == "" {
		return config.Config{}, errors.New("jawaban panel tidak lengkap")
	}
	if opts.CASHA256 != "" {
		fp, err := ca.Fingerprint([]byte(pr.CAPEM))
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
	if _, err := ca.AgentClientTLS([]byte(pr.CertPEM), keyPEM, []byte(pr.CAPEM)); err != nil {
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
