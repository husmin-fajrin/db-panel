package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	pairTokenTTL      = 24 * time.Hour
	msgServerNotFound = "Server tidak ditemukan"
	msgLocalDelete    = "Server ini adalah host panel dan tidak bisa dihapus"
	msgLocalToken     = "Server ini adalah host panel; agent-nya tertanam di panel dan tidak perlu dipasang"
)

// localCertSerial marks the panel host's own server, whose agent is embedded
// in the panel process.
const localCertSerial = "local"

type dashboardPage struct {
	Page
	Total, Online, Unpaired  int
	Masters, Replicas        int
	ActiveJobs, ActiveAlerts int
	Topologies               []topologyCard
}

// topologyCard is a master and its replicas on the dashboard.
type topologyCard struct {
	Master       store.Server
	MasterStatus statusView
	Replicas     []serverStatusRow
}

// serverStatusRow is a server with its monitoring status.
type serverStatusRow struct {
	Server store.Server
	Status statusView
}

type serverRow struct {
	store.Server
	Online       bool
	MySQLVersion string
	OS           string
	Status       statusView
	Local        bool // the panel host: its agent is embedded in the panel
	// NeedsUpgrade: the agent is too old for monitoring. UpgradeCommand
	// reinstalls it; "" when no re-pairing token of this process is active
	// (the server page creates one).
	NeedsUpgrade   bool
	UpgradeCommand string
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
	Server store.Server
	Online bool
	Probe  *proto.ProbeResult // nil until the first probe
	// Command is the pairing command: always for an unpaired server, and for
	// a paired one while a re-pairing token is active.
	Command      string
	TokenExpires time.Time
	// TokenHidden: a token is active, but only its hash is stored and this
	// panel process did not create it (it was restarted since), so the
	// command cannot be shown; a new token has to be created.
	TokenHidden bool
	Local       bool // the panel host itself: no delete, no re-pairing

	// MySQL card (paired servers).
	Inventory  store.Inventory // Connected false until "Hubungkan MySQL"
	ShowMySQL  bool            // MySQL is (or may be) installed: offer the account form
	CanPrepare bool            // connected, no missing grants, not a replica
	Connect    connectForm     // the account form after a failed attempt
	ActiveJob  *store.Job      // the server's unfinished job, if any
	// PrepareAgain: the server already is a master (it has a topology), so
	// preparing it again asks for the password.
	PrepareAgain bool
	// InventoryCut: the inventory holds as many databases as the panel
	// keeps; the list may be cut.
	InventoryCut bool
	Controls     serverControls // Periksa sekarang and Jangan ganggu (paired servers)
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	active, err := a.Store.RunningJobs()
	if err != nil {
		a.internalError(w, "running jobs", err)
		return
	}
	alerts, err := a.Store.ListOpenAlerts()
	if err != nil {
		a.internalError(w, "list open alerts", err)
		return
	}
	p := dashboardPage{Page: a.page(r), Total: len(servers), ActiveJobs: len(active), ActiveAlerts: len(alerts)}
	for _, s := range servers {
		switch {
		case !s.Paired:
			p.Unpaired++
		case a.Hub.IsOnline(s.ID):
			p.Online++
		}
		switch s.Role {
		case store.RoleMaster:
			p.Masters++
		case store.RoleReplica:
			p.Replicas++
		}
	}
	if p.Topologies, err = a.topologyCards(servers); err != nil {
		a.internalError(w, "topology cards", err)
		return
	}
	a.render.Render(w, "dashboard", p)
}

// topologyCards is one card per topology, its master with its replicas.
func (a *app) topologyCards(servers []store.Server) ([]topologyCard, error) {
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	topos, err := a.Store.ListTopologies()
	if err != nil {
		return nil, err
	}
	cards := make([]topologyCard, 0, len(topos))
	for _, t := range topos {
		master := byID[t.MasterServerID]
		card := topologyCard{Master: master, MasterStatus: a.statusOf(master.ID, master.Role)}
		reps, err := a.Store.ListReplicas(t.ID)
		if err != nil {
			return nil, err
		}
		for _, rep := range reps {
			srv := byID[rep.ServerID]
			card.Replicas = append(card.Replicas, serverStatusRow{Server: srv, Status: a.statusOf(srv.ID, srv.Role)})
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func (a *app) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	p := serversPage{Page: a.page(r)}
	p.Notice = notices[r.URL.Query().Get("pesan")]
	now := a.Now()
	for _, s := range servers {
		row := serverRow{Server: s, Online: s.Paired && a.Hub.IsOnline(s.ID), Local: s.CertSerial == localCertSerial}
		if probe := a.probe(s); probe != nil {
			row.OS = probe.OS
			if probe.MySQL.Installed {
				row.MySQLVersion = probe.MySQL.Version
			}
		}
		v, ok := a.Monitor.View(s.ID)
		row.Status = newStatusView(s.ID, s.Role, v, ok, now)
		row.NeedsUpgrade = ok && v.NeedsUpgrade
		if row.NeedsUpgrade && !row.Local {
			var err error
			if row.UpgradeCommand, err = a.upgradeCommand(s.ID, now); err != nil {
				a.internalError(w, "upgrade command", err)
				return
			}
		}
		p.Rows = append(p.Rows, row)
	}
	a.render.Render(w, "servers", p)
}

// upgradeCommand is the pairing command that reinstalls the server's
// agent, or "" when this panel process holds no active token for it.
func (a *app) upgradeCommand(id int64, now time.Time) (string, error) {
	tok, found, err := a.Store.ActivePairToken(id, now)
	if err != nil || !found || tok.Token == "" {
		return "", err
	}
	sum, err := a.InstallScriptSHA256(tok.Token)
	if err != nil {
		return "", err
	}
	return pairCommand(a.PublicURL(), tok.Token, sum, a.CertInfo().Pin)
}

func (a *app) newServerForm(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, "server_new", serverNewPage{Page: a.page(r)})
}

func (a *app) createServer(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	srv, err := a.Store.CreateServer(name)
	a.audit(r, "server_create", auditServer, srv.ID, map[string]any{"name": name}, outcomeOf(err))
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

// renderServer renders the server page with v's status and messages.
func (a *app) renderServer(w http.ResponseWriter, r *http.Request, srv store.Server, v serverView) {
	p := serverDetailPage{Page: a.page(r), Server: srv, Local: srv.CertSerial == localCertSerial, Connect: v.Form}
	p.Flash, p.Notice = v.Flash, v.Notice
	now := a.Now()
	tok, found, err := a.Store.ActivePairToken(srv.ID, now)
	if err == nil && !found && !srv.Paired {
		tok, err = a.Store.CreatePairToken(srv.ID, now, pairTokenTTL)
		found = err == nil
	}
	if err != nil {
		a.internalError(w, "pair token", err)
		return
	}
	if srv.Paired {
		p.Online = a.Hub.IsOnline(srv.ID)
		p.Probe = a.probe(srv)
		if p.Inventory, err = a.inventory(srv.ID); err != nil {
			a.internalError(w, "get inventory", err)
			return
		}
		job, active, err := a.Store.ActiveJobForServer(srv.ID)
		if err != nil {
			a.internalError(w, "active job", err)
			return
		}
		if active {
			p.ActiveJob = &job
		}
		p.ShowMySQL = p.Probe == nil || p.Probe.MySQL.Installed
		p.InventoryCut = len(p.Inventory.Databases) >= inventoryCap
		p.CanPrepare = p.Inventory.Connected && len(p.Inventory.MissingGrants) == 0 && srv.Role != store.RoleReplica
		if p.CanPrepare {
			_, err := a.Store.GetTopologyByMaster(srv.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				a.internalError(w, "get topology", err)
				return
			}
			p.PrepareAgain = err == nil
		}
		p.Controls = a.controls(srv)
	}
	switch {
	case !found || p.Local:
	case tok.Token == "":
		p.TokenHidden = true
		p.TokenExpires = tok.ExpiresAt
	default:
		sum, err := a.InstallScriptSHA256(tok.Token)
		if err != nil {
			a.internalError(w, "install script hash", err)
			return
		}
		if p.Command, err = pairCommand(a.PublicURL(), tok.Token, sum, a.CertInfo().Pin); err != nil {
			a.internalError(w, "pairing command", err)
			return
		}
		p.TokenExpires = tok.ExpiresAt
	}
	a.render.renderStatus(w, v.Status, "server_detail", p)
}

// pairCommand is the command an operator runs on a new database server. It
// fetches over HTTPS with -k and --pinnedpubkey: the panel's certificate is
// not checked against a CA (the new server does not know the panel's), but
// curl accepts only the key whose pin is webPin (sha256//<base64>), so a
// machine in between cannot answer instead of the panel. It saves the
// install script to a fresh mktemp file (0600, unpredictable name:
// a fixed /tmp name could be pre-created by a local user and rewritten
// between the check and sudo), checks it against scriptSHA256 (the hash of
// the script the panel serves for this token) and only then runs it as
// root, so a script swapped on the network is never run (SEC-23). curl
// follows no redirect: the token would go along to another host.
//
// The URL with the token reaches curl as a config on stdin through a bash
// here-string, which the shell writes to a pipe or temporary file, so the
// token is in no process's argv (readable by every local user through
// /proc). Single quotes: nothing in them is expanded, and "?" would be a
// glob character in zsh. PublicURL is an IP or a validated host name, the
// token is Crockford base32, the hash is hex and the pin base64, so none
// contains a quote.
// --max-redirs is an argument rather than a config line because the config
// here-string has to stay on one line. Without a pin -k would trust any
// server, so an empty one is an error.
func pairCommand(publicURL, token, scriptSHA256, webPin string) (string, error) {
	if webPin == "" {
		return "", errors.New("perintah pasang butuh pin sertifikat panel")
	}
	return `f=$(mktemp) && curl -fsSL -k --pinnedpubkey ` + webPin + ` --max-redirs 0 -K - <<<'url = "` + publicURL + "/agent/install?token=" + token +
		`"' -o "$f" && echo "` + scriptSHA256 + `  $f" | sha256sum -c - && sudo bash "$f"`, nil
}

// newToken issues a fresh pairing token. For a paired server this starts a
// re-pairing: the current agent keeps working until an agent pairs with the
// new token, which replaces its certificate.
func (a *app) newToken(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if srv.CertSerial == localCertSerial {
		a.audit(r, "server_token", auditServer, srv.ID, nil, msgLocalToken)
		http.Error(w, msgLocalToken, http.StatusConflict)
		return
	}
	// The token itself never goes into the audit log.
	_, err := a.Store.CreatePairToken(srv.ID, a.Now(), pairTokenTTL)
	a.audit(r, "server_token", auditServer, srv.ID, map[string]any{"repair": srv.Paired}, outcomeOf(err))
	if err != nil {
		a.internalError(w, "create pair token", err)
		return
	}
	if srv.Paired {
		a.Logger.Info("re-pairing token created", "id", srv.ID, "by", a.page(r).Username)
	}
	http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
}

// deleteServer removes a server once the password is confirmed and its
// name typed. The panel host's own server cannot be deleted; the
// orchestrator refuses a master with replicas and a registered replica,
// and drops the agent's connection.
func (a *app) deleteServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok || !a.requireConfirm(w, r, ActDeleteServer, auditServer, srv.ID) {
		return
	}
	detail := map[string]any{"name": srv.Name}
	if srv.CertSerial == localCertSerial {
		a.audit(r, "server_delete", auditServer, srv.ID, detail, msgLocalDelete)
		http.Error(w, msgLocalDelete, http.StatusConflict)
		return
	}
	if !typedName(r, srv) {
		a.audit(r, "server_delete", auditServer, srv.ID, detail, msgNameMismatch)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msgNameMismatch})
		return
	}
	err := a.Orchestrator.DeleteServer(r.Context(), srv.ID)
	a.audit(r, "server_delete", auditServer, srv.ID, detail, outcomeOf(err))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return
	case errors.Is(err, orchestrator.ErrServerIsMaster), errors.Is(err, orchestrator.ErrServerIsReplica),
		errors.Is(err, orchestrator.ErrLocalServer), errors.Is(err, orchestrator.ErrBusy):
		// The orchestrator's refusals are written for the user.
		a.Logger.Warn("delete server refused", "id", srv.ID, "err", err)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	case err != nil:
		a.internalError(w, "delete server", err)
		return
	}
	a.Logger.Info("server deleted", "id", srv.ID, "name", srv.Name, "by", a.page(r).Username)
	http.Redirect(w, r, "/servers?pesan=dihapus", http.StatusSeeOther)
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
