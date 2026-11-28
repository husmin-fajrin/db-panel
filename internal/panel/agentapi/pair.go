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
	"strings"
	"time"
	"unicode"

	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// maxPairBody caps the pairing request body.
const maxPairBody = 64 << 10

const (
	// maxHostnameRunes bounds the request's hostname in the audit log and
	// the journal (spec 5.1).
	maxHostnameRunes = 255
	// maxAgentFieldRunes bounds the request's agent_version and arch there.
	maxAgentFieldRunes = 64
)

// boundHostname is a pairing request's hostname as the audit log and the
// journal keep it: the request comes from the machine being paired and is
// untrusted, so it is one line without control characters, cut at 255
// runes.
func boundHostname(s string) string { return boundText(s, maxHostnameRunes) }

// boundText is s as one line of at most n runes: invalid UTF-8, control
// and format characters (bidi overrides among them) and line or paragraph
// separators each become a space.
func boundText(s string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, " ") {
		if n == 0 {
			break
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			r = ' '
		}
		b.WriteRune(r)
		n--
	}
	return b.String()
}

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
	// WebPin returns the SPKI pin (curl --pinnedpubkey form) of the
	// panel's HTTPS certificate, put into the install script; nil renders
	// it empty.
	WebPin func() string
	// LocalServerID is the panel host's own server; its agent pairs from
	// loopback only (SEC-63g), and the server's address is PublicHost
	// instead.
	LocalServerID int64
	PublicHost    string
	// OnPaired, if set, is called with the server id after each pairing.
	OnPaired func(serverID int64)
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
// serial and the caller's IP (the public host for the panel host's own
// server).
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
		// What the request says of the machine is untrusted: bounded
		// before it reaches a log line or an audit row.
		hostname := boundHostname(req.Hostname)
		arch := boundText(req.Arch, maxAgentFieldRunes)
		agentVersion := boundText(req.AgentVersion, maxAgentFieldRunes)
		// SEC-63g: the panel host's own server pairs with the token the
		// panel writes to local-pair-token for its local agent, which
		// connects over loopback. From anywhere else that token is
		// refused before it is used, so the local agent can still pair.
		remoteLocal := d.LocalServerID != 0 && !isLoopback(ip)
		refuseLocal := func(serverID int64) {
			log.Warn("pairing rejected: the panel host's token used from another address", "server_id", serverID,
				"remote", ip, "hostname", hostname)
			d.auditPairRefused(serverID, ip, hostname, outcomeLocalNotLoopback)
			writeError(w, http.StatusForbidden, msgLocalNotLoopback)
		}
		if remoteLocal {
			pt, err := peekPairToken(d.Store, req.Token, d.now())
			switch {
			case err == nil && pt.ServerID == d.LocalServerID:
				refuseLocal(pt.ServerID)
				return
			case err != nil && !errors.Is(err, store.ErrTokenInvalid):
				// Not knowing whose token it is must not let it through.
				log.Error("pairing: peek token", "err", err)
				writeError(w, http.StatusInternalServerError, "kesalahan internal")
				return
			}
		}

		pt, err := d.Store.ConsumePairToken(req.Token, d.now())
		if err == nil && remoteLocal && pt.ServerID == d.LocalServerID {
			// The peek missed it (a race): refused all the same, though
			// the token is used up now.
			refuseLocal(pt.ServerID)
			return
		}
		if errors.Is(err, store.ErrTokenInvalid) {
			log.Warn("pairing rejected: invalid token", "remote", ip, "hostname", hostname)
			writeError(w, http.StatusForbidden, "token ditolak")
			return
		}
		if err != nil {
			log.Error("pairing: consume token", "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		certPEM, serial, err := d.CA.IssueAgentCert([]byte(req.CSRPEM), pt.ServerID)
		if err != nil {
			log.Error("pairing: issue agent certificate", "server_id", pt.ServerID, "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		address := ip
		if d.LocalServerID != 0 && pt.ServerID == d.LocalServerID && d.PublicHost != "" {
			address = d.PublicHost
		}
		// Whether this pairing replaces an agent, read before MarkPaired.
		before, err := d.Store.GetServer(pt.ServerID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusForbidden, "token ditolak")
			return
		}
		if err != nil {
			log.Error("pairing: get server", "server_id", pt.ServerID, "err", err)
			writeError(w, http.StatusInternalServerError, "kesalahan internal")
			return
		}
		if err := d.Store.MarkPaired(pt.ServerID, serial, address); err != nil {
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
		if d.OnPaired != nil {
			d.OnPaired(pt.ServerID)
		}
		d.auditPaired(pt.ServerID, ip, map[string]any{"address": address, "replaced": before.Paired,
			"cert_serial": serial, "agent_version": agentVersion, "arch": arch, "hostname": hostname})
		log.Info("agent paired", "server_id", pt.ServerID, "remote", ip, "hostname", hostname,
			"arch", arch, "agent_version", agentVersion, "cert_serial", serial)
		writeJSON(w, http.StatusOK, proto.PairResponse{
			ServerID:  pt.ServerID,
			CertPEM:   string(certPEM),
			CAPEM:     string(d.CA.CAPEM()),
			AgentAddr: d.AgentAddr(),
		})
	})
}

const (
	msgLocalNotLoopback     = "token ditolak: token server panel hanya berlaku dari host panel itu sendiri (127.0.0.1 atau ::1)"
	outcomeLocalNotLoopback = "ditolak: token server panel dipakai bukan dari alamat loopback"
)

// peekPairToken is Store.PeekPairToken; tests replace it to make the
// peek fail.
var peekPairToken = func(st *store.Store, token string, now time.Time) (store.PairToken, error) {
	return st.PeekPairToken(token, now)
}

// isLoopback reports whether ip (a RemoteAddr host) is 127.0.0.0/8 or ::1.
func isLoopback(ip string) bool {
	a := net.ParseIP(ip)
	return a != nil && a.IsLoopback()
}

// outcomePaired is the audit outcome of a pairing that succeeded.
const outcomePaired = "ok"

// auditPairRefused writes a refused pairing to the audit log; hostname is
// already bounded (boundHostname).
func (d Deps) auditPairRefused(serverID int64, ip, hostname, outcome string) {
	d.auditPair(serverID, ip, map[string]any{"hostname": hostname}, outcome)
}

// auditPaired writes a successful pairing to the audit log (spec 5.1):
// detail holds the address, whether it replaced an agent, the new
// certificate's serial and the agent's bounded version, arch and
// hostname.
func (d Deps) auditPaired(serverID int64, ip string, detail map[string]any) {
	d.auditPair(serverID, ip, detail, outcomePaired)
}

// auditPair writes an agent_pair entry to the audit log and its journald
// copy (the agent has no panel user: the username is "agent").
func (d Deps) auditPair(serverID int64, ip string, detailMap map[string]any, outcome string) {
	detail, _ := json.Marshal(detailMap)
	e := store.AuditEntry{TS: d.now(), Username: "agent", IP: ip, Action: "agent_pair",
		TargetKind: "server", TargetID: serverID, DetailJSON: string(detail), Outcome: outcome}
	rec, err := d.Store.AddAuditRecord(e)
	if err != nil {
		d.logger().Error("write audit log", "action", "agent_pair", "server_id", serverID, "err", err)
	}
	// The journald copy (SEC-45, SEC-77), as for the web's entries.
	store.LogAudit(d.logger(), e, rec, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
