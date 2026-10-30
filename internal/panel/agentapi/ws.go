package agentapi

import (
	"fmt"
	"net/http"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// WSHandler serves /agent/ws on the mTLS listener. The client certificate
// must name a paired server and be the one most recently issued to it;
// otherwise the request gets 403 before any upgrade. The accepted connection
// is attached to the hub until it closes, or until the listener shuts down,
// which closes it without waiting on an unresponsive agent.
func WSHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := d.logger()
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "sertifikat klien diperlukan", http.StatusForbidden)
			return
		}
		cert := r.TLS.PeerCertificates[0]
		serverID, err := ca.ServerIDFromCert(cert)
		if err != nil {
			log.Warn("agent rejected: not an agent certificate", "remote", r.RemoteAddr)
			http.Error(w, "sertifikat ditolak", http.StatusForbidden)
			return
		}
		serial := fmt.Sprintf("%032x", cert.SerialNumber)
		srv, err := d.Store.GetServer(serverID)
		if err != nil || !srv.Paired || srv.CertSerial != serial {
			log.Warn("agent rejected: unknown server or stale certificate",
				"server_id", serverID, "cert_serial", serial, "remote", r.RemoteAddr, "err", err)
			http.Error(w, "sertifikat ditolak", http.StatusForbidden)
			return
		}

		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			log.Warn("agent websocket upgrade failed", "server_id", serverID, "err", err)
			return
		}
		conn := hub.NewWSConn(ws, func(ev proto.Event) {
			if d.OnEvent != nil {
				d.OnEvent(serverID, ev)
			}
		})
		if d.Hub.Attach(serverID, conn) {
			log.Warn("agent connection replaced an existing one (cloned server or stale connection?)",
				"server_id", serverID, "remote", r.RemoteAddr)
		}
		if d.OnAttach != nil {
			d.OnAttach(serverID, conn)
		}
		log.Info("agent connected", "server_id", serverID, "remote", r.RemoteAddr)

		select {
		case <-conn.Wait():
			d.Hub.Detach(serverID, conn)
			conn.Close()
		case <-r.Context().Done(): // listener shutting down
			d.Hub.Detach(serverID, conn)
			conn.CloseNow()
		}
		log.Info("agent disconnected", "server_id", serverID)
	})
}
