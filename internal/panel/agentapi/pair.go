// Package agentapi is the panel's side of the agent link: the plain-HTTP
// pairing endpoint and the mTLS WebSocket endpoint that attaches agents to
// the hub.
package agentapi

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// maxPairBody caps the pairing request body.
const maxPairBody = 64 << 10

// localCertSerial marks the panel host's own server.
const localCertSerial = "local"

// Deps are the panel services the agent endpoints use.
type Deps struct {
	Store *store.Store
	CA    *ca.CA
	Hub   *hub.Hub
	// AgentAddr returns host:port of the mTLS agent listener as agents reach it.
	AgentAddr    func() string
	AgentVersion string
	Now          func() time.Time // nil means time.Now
	Logger       *slog.Logger     // nil discards
	// OnEvent receives every event an agent sends; nil drops them. It runs on
	// the connection's read goroutine and must not block.
	OnEvent func(serverID int64, ev proto.Event)
	// OnAttach, if set, is called with each connection right after it is
	// attached to the hub (used by tests to reach the connection).
	OnAttach func(serverID int64, c *hub.WSConn)
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// PairHandler serves POST /agent/pair: it consumes the one-time token, signs
// the agent's CSR and marks the server paired with the new certificate's
// serial and the caller's IP.
func PairHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := d.logger()
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "metode tidak diizinkan")
			return
		}
		var req agent.PairRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPairBody)).Decode(&req); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "permintaan terlalu besar")
				return
			}
			writeError(w, http.StatusBadRequest, "permintaan tidak valid")
			return
		}
		// Check the CSR before consuming the token, so a malformed request
		// does not burn it.
		if !validCSR([]byte(req.CSRPEM)) {
			writeError(w, http.StatusBadRequest, "csr tidak valid")
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		pt, err := d.Store.ConsumePairToken(req.Token, d.now())
		if errors.Is(err, store.ErrTokenInvalid) {
			log.Warn("pairing rejected: invalid token", "remote", ip, "hostname", req.Hostname)
			writeError(w, http.StatusForbidden, "token ditolak")
			return
		}
		if err != nil {
			log.Error("pairing: consume token", "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		// The panel host's own server runs the embedded agent; a token left
		// over from before it was adopted must not hand it to another host.
		srv, err := d.Store.GetServer(pt.ServerID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusForbidden, "token ditolak")
			return
		}
		if err != nil {
			log.Error("pairing: get server", "server_id", pt.ServerID, "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		if srv.CertSerial == localCertSerial {
			log.Warn("pairing rejected: token for the panel host's own server", "server_id", pt.ServerID,
				"remote", ip, "hostname", req.Hostname)
			writeError(w, http.StatusForbidden, "token ditolak")
			return
		}
		certPEM, serial, err := d.CA.IssueAgentCert([]byte(req.CSRPEM), pt.ServerID)
		if err != nil {
			log.Error("pairing: issue agent certificate", "server_id", pt.ServerID, "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		if err := d.Store.MarkPaired(pt.ServerID, serial, ip); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusForbidden, "token ditolak")
				return
			}
			log.Error("pairing: mark paired", "server_id", pt.ServerID, "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		log.Info("agent paired", "server_id", pt.ServerID, "remote", ip, "hostname", req.Hostname,
			"arch", req.Arch, "agent_version", req.AgentVersion, "cert_serial", serial)
		writeJSON(w, http.StatusOK, agent.PairResponse{
			ServerID:  pt.ServerID,
			CertPEM:   string(certPEM),
			CAPEM:     string(d.CA.CAPEM()),
			AgentAddr: d.AgentAddr(),
		})
	})
}

// validCSR reports whether b is a PEM certificate request with a valid
// self-signature.
func validCSR(b []byte) bool {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return false
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	return err == nil && csr.CheckSignature() == nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
