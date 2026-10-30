// Package agentapi is the panel's side of the agent link: the plain-HTTP
// pairing endpoint and the mTLS WebSocket endpoint that attaches agents to
// the hub.
package agentapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

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
		var req proto.PairRequest
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
		if _, err := ca.ParseAgentCSR([]byte(req.CSRPEM)); err != nil {
			msg := "csr tidak valid"
			if errors.Is(err, ca.ErrCSRKey) {
				msg = err.Error()
			}
			writeError(w, http.StatusBadRequest, msg)
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
		// The old certificate is refused from now on, but a connection made
		// with it is already past that check: cut it, so re-pairing revokes
		// a leaked agent key at once rather than when the new agent connects.
		if d.Hub.IsOnline(pt.ServerID) {
			d.Hub.Disconnect(pt.ServerID)
			log.Info("previous agent connection closed after re-pairing", "server_id", pt.ServerID)
		}
		log.Info("agent paired", "server_id", pt.ServerID, "remote", ip, "hostname", req.Hostname,
			"arch", req.Arch, "agent_version", req.AgentVersion, "cert_serial", serial)
		writeJSON(w, http.StatusOK, proto.PairResponse{
			ServerID:  pt.ServerID,
			CertPEM:   string(certPEM),
			CAPEM:     string(d.CA.CAPEM()),
			AgentAddr: d.AgentAddr(),
		})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
