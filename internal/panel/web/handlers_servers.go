package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	pairTokenTTL      = 24 * time.Hour
	msgServerNotFound = "Server tidak ditemukan"
	msgAlreadyPaired  = "Server sudah dipasangkan"
)

type dashboardPage struct {
	Page
	Total, Online, Unpaired int
}

type serverRow struct {
	store.Server
	Online       bool
	MySQLVersion string
	OS           string
}

type serversPage struct {
	Page
	Rows []serverRow
}

type serverNewPage struct {
	Page
	Name string
}

type serverDetailPage struct {
	Page
	Server       store.Server
	Online       bool
	Probe        *proto.ProbeResult // nil until the first probe
	Command      string             // pairing command, unpaired servers only
	TokenExpires time.Time
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	p := dashboardPage{Page: a.page(r), Total: len(servers)}
	for _, s := range servers {
		switch {
		case !s.Paired:
			p.Unpaired++
		case a.Hub.IsOnline(s.ID):
			p.Online++
		}
	}
	a.render.Render(w, "dashboard", p)
}

func (a *app) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	p := serversPage{Page: a.page(r)}
	for _, s := range servers {
		row := serverRow{Server: s, Online: s.Paired && a.Hub.IsOnline(s.ID)}
		if probe := a.probe(s); probe != nil {
			row.OS = probe.OS
			if probe.MySQL.Installed {
				row.MySQLVersion = probe.MySQL.Version
			}
		}
		p.Rows = append(p.Rows, row)
	}
	a.render.Render(w, "servers", p)
}

func (a *app) newServerForm(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, "server_new", serverNewPage{Page: a.page(r)})
}

func (a *app) createServer(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	srv, err := a.Store.CreateServer(name)
	if err != nil {
		// The store returns unwrapped, user-facing errors for validation
		// (empty or duplicate name) and wrapped ones for database failures.
		if errors.Unwrap(err) != nil {
			a.internalError(w, "create server", err)
			return
		}
		p := serverNewPage{Page: a.page(r), Name: name}
		p.Flash = err.Error()
		a.render.renderStatus(w, http.StatusBadRequest, "server_new", p)
		return
	}
	if _, err := a.Store.CreatePairToken(srv.ID, a.Now(), pairTokenTTL); err != nil {
		a.internalError(w, "create pair token", err)
		return
	}
	a.Logger.Info("server created", "id", srv.ID, "name", srv.Name, "by", a.page(r).Username)
	http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
}

func (a *app) serverDetail(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	p := serverDetailPage{Page: a.page(r), Server: srv}
	if srv.Paired {
		p.Online = a.Hub.IsOnline(srv.ID)
		p.Probe = a.probe(srv)
	} else {
		now := a.Now()
		tok, found, err := a.Store.ActivePairToken(srv.ID, now)
		if err == nil && !found {
			tok, err = a.Store.CreatePairToken(srv.ID, now, pairTokenTTL)
		}
		if err != nil {
			a.internalError(w, "pair token", err)
			return
		}
		p.Command = "curl -sSL " + a.PublicURL() + "/agent/install?token=" + tok.Token + " | sudo bash"
		p.TokenExpires = tok.ExpiresAt
	}
	a.render.Render(w, "server_detail", p)
}

func (a *app) newToken(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if srv.Paired {
		http.Error(w, msgAlreadyPaired, http.StatusConflict)
		return
	}
	if _, err := a.Store.CreatePairToken(srv.ID, a.Now(), pairTokenTTL); err != nil {
		a.internalError(w, "create pair token", err)
		return
	}
	http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
}

func (a *app) deleteServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return
	}
	err := a.Store.DeleteServer(id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "delete server", err)
		return
	}
	a.Logger.Info("server deleted", "id", id, "by", a.page(r).Username)
	http.Redirect(w, r, "/servers", http.StatusSeeOther)
}

// serverFromPath loads the server named by {id}, answering 404 (or 500) and
// returning false when it cannot.
func (a *app) serverFromPath(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return store.Server{}, false
	}
	srv, err := a.Store.GetServer(id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return store.Server{}, false
	}
	if err != nil {
		a.internalError(w, "get server", err)
		return store.Server{}, false
	}
	return srv, true
}

// probe decodes the server's last probe result, or nil when there is none
// (or it cannot be read, which is logged).
func (a *app) probe(s store.Server) *proto.ProbeResult {
	if s.ProbeJSON == "" {
		return nil
	}
	var p proto.ProbeResult
	if err := json.Unmarshal([]byte(s.ProbeJSON), &p); err != nil {
		a.Logger.Warn("decode probe", "server", s.ID, "err", err)
		return nil
	}
	return &p
}

// pathID parses the {id} path segment.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func serverURL(id int64) string { return "/servers/" + strconv.FormatInt(id, 10) }
