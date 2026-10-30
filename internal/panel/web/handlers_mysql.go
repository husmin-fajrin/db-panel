package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Orchestrator runs the MySQL operations the UI starts; the panel passes
// its *orchestrator.Runner.
type Orchestrator interface {
	Connect(ctx context.Context, serverID int64, p proto.ConnectParams) (proto.ConnectResult, error)
	RefreshInventory(ctx context.Context, serverID int64) (proto.ConnectResult, error)
	StartPrepareMaster(ctx context.Context, serverID int64) (string, error)
	StartCreateReplica(ctx context.Context, p orchestrator.CreateReplicaParams) (string, error)
	Approve(jobID string) error
	Retry(jobID string) error
	Cancel(jobID string) error
}

const (
	msgMySQLUserRequired  = "Nama pengguna MySQL wajib diisi"
	msgMySQLFieldTooLong  = "Nama pengguna, kata sandi, atau host MySQL terlalu panjang"
	msgReplicaNotFound    = "Replika tidak ditemukan"
	msgReplicaTarget      = "Pilih server tujuan replika terlebih dahulu."
	msgReplicaDatabases   = "Pilih minimal satu database untuk direplikasi."
	msgReplicaBadDatabase = "Nama database tidak valid: "
	msgReplicaConfirm     = "Replika belum dibuat: centang kotak persetujuan bahwa server tujuan dikunci hanya-baca dan database lamanya dibuang."

	defaultMySQLHost = "127.0.0.1"
	maxMySQLUser     = 32 // MySQL's limit for a user name
	maxMySQLPassword = 256
	maxMySQLHost     = 255
)

// databaseName is what the UI accepts as a database name; the orchestrator
// checks the selection against the master's databases as well.
var databaseName = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)

// notices are the success messages a redirect names in ?pesan=.
var notices = map[string]string{
	"terhubung":  "MySQL berhasil dihubungkan.",
	"disegarkan": "Data MySQL sudah diperbarui.",
}

// connectForm is what the MySQL account form shows again after an error.
// The password is never part of it.
type connectForm struct {
	User, Host string
	Open       bool // expand the form (it was just submitted)
}

// serverView is what a server page shows besides the server itself.
type serverView struct {
	Status        int
	Flash, Notice string
	Form          connectForm
}

func (a *app) serverDetail(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	a.renderServer(w, r, srv, serverView{Status: http.StatusOK, Notice: notices[r.URL.Query().Get("pesan")]})
}

// inventory returns the server's MySQL inventory; a zero one (Connected
// false) when none is stored yet.
func (a *app) inventory(serverID int64) (store.Inventory, error) {
	inv, err := a.Store.GetInventory(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Inventory{ServerID: serverID}, nil
	}
	return inv, err
}

func (a *app) connectMySQL(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	form := connectForm{
		User: strings.TrimSpace(r.PostFormValue("user")),
		Host: strings.TrimSpace(r.PostFormValue("host")),
		Open: true,
	}
	password := r.PostFormValue("password")
	fail := func(msg string) {
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msg, Form: form})
	}
	switch {
	case form.User == "":
		fail(msgMySQLUserRequired)
		return
	case len(form.User) > maxMySQLUser || len(password) > maxMySQLPassword || len(form.Host) > maxMySQLHost:
		fail(msgMySQLFieldTooLong)
		return
	}
	host := form.Host
	if host == "" {
		host = defaultMySQLHost
	}
	_, err := a.Orchestrator.Connect(r.Context(), srv.ID, proto.ConnectParams{User: form.User, Password: password, Host: host})
	if err != nil {
		a.Logger.Warn("mysql connect failed", "server", srv.ID, "err", err)
		fail(err.Error())
		return
	}
	a.Logger.Info("mysql connected", "server", srv.ID, "mysql_user", form.User, "by", a.page(r).Username)
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan=terhubung", http.StatusSeeOther)
}

func (a *app) refreshMySQL(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if _, err := a.Orchestrator.RefreshInventory(r.Context(), srv.ID); err != nil {
		a.Logger.Warn("mysql refresh failed", "server", srv.ID, "err", err)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan=disegarkan", http.StatusSeeOther)
}

func (a *app) prepareMaster(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	id, err := a.Orchestrator.StartPrepareMaster(r.Context(), srv.ID)
	if err != nil {
		a.Logger.Warn("prepare master refused", "server", srv.ID, "err", err)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	a.Logger.Info("prepare master started", "server", srv.ID, "job", id, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// replicaRow is a replica with the name of its server.
type replicaRow struct {
	store.Replica
	ServerName string
}

type masterPage struct {
	Page
	Server    store.Server
	Online    bool
	Topology  store.Topology
	Inventory store.Inventory
	Replicas  []replicaRow
	Jobs      []jobRow
}

// masterFromPath loads the server of {id} and its topology. A server that
// is not a master (yet) is redirected to its server page.
func (a *app) masterFromPath(w http.ResponseWriter, r *http.Request) (store.Server, store.Topology, bool) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return store.Server{}, store.Topology{}, false
	}
	topo, err := a.Store.GetTopologyByMaster(srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
		return store.Server{}, store.Topology{}, false
	}
	if err != nil {
		a.internalError(w, "get topology", err)
		return store.Server{}, store.Topology{}, false
	}
	return srv, topo, true
}

func (a *app) masterDetail(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	p := masterPage{Page: a.page(r), Server: srv, Online: a.Hub.IsOnline(srv.ID), Topology: topo}
	var err error
	if p.Inventory, err = a.inventory(srv.ID); err != nil {
		a.internalError(w, "get inventory", err)
		return
	}
	reps, err := a.Store.ListReplicas(topo.ID)
	if err != nil {
		a.internalError(w, "list replicas", err)
		return
	}
	names, err := a.serverNames()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	for _, rep := range reps {
		p.Replicas = append(p.Replicas, replicaRow{Replica: rep, ServerName: names[rep.ServerID]})
	}
	// The master's own jobs and those creating its replicas.
	jobs, err := a.Store.ListJobs(jobsListLimit)
	if err != nil {
		a.internalError(w, "list jobs", err)
		return
	}
	for _, j := range jobs {
		if len(p.Jobs) == jobHistoryLimit {
			break
		}
		if j.ServerID == srv.ID || j.TopologyID == topo.ID {
			p.Jobs = append(p.Jobs, jobRow{Job: j, ServerName: names[j.ServerID]})
		}
	}
	a.render.Render(w, "master_detail", p)
}

// targetChoice is a server the new replica can be created on.
type targetChoice struct {
	store.Server
	Retry bool // its earlier replica attempt failed; a new one replaces it
}

// databaseChoice is one database checkbox of the replica form.
type databaseChoice struct {
	proto.DatabaseInfo
	Checked bool
}

type replicaNewPage struct {
	Page
	Master    store.Server
	Inventory store.Inventory
	Targets   []targetChoice
	Databases []databaseChoice
	Target    int64
	ForceDump bool
}

// replicaForm builds the replica form for master. checked nil means every
// database is selected.
func (a *app) replicaForm(r *http.Request, master store.Server, checked map[string]bool) (replicaNewPage, error) {
	p := replicaNewPage{Page: a.page(r), Master: master}
	var err error
	if p.Inventory, err = a.inventory(master.ID); err != nil {
		return p, err
	}
	for _, d := range p.Inventory.Databases {
		p.Databases = append(p.Databases, databaseChoice{DatabaseInfo: d, Checked: checked == nil || checked[d.Name]})
	}
	servers, err := a.Store.ListServers()
	if err != nil {
		return p, err
	}
	for _, s := range servers {
		if s.ID == master.ID || !s.Paired || !a.Hub.IsOnline(s.ID) {
			continue
		}
		switch s.Role {
		case store.RoleUnassigned:
			p.Targets = append(p.Targets, targetChoice{Server: s})
		case store.RoleReplica:
			rep, err := a.Store.GetReplicaByServer(s.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return p, err
			}
			if err == nil && rep.State == store.ReplicaFailed {
				p.Targets = append(p.Targets, targetChoice{Server: s, Retry: true})
			}
		}
	}
	return p, nil
}

func (a *app) newReplicaForm(w http.ResponseWriter, r *http.Request) {
	master, _, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	p, err := a.replicaForm(r, master, nil)
	if err != nil {
		a.internalError(w, "replica form", err)
		return
	}
	a.render.Render(w, "replica_new", p)
}

func (a *app) createReplica(w http.ResponseWriter, r *http.Request) {
	master, _, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	selected := r.PostForm["databases"]
	checked := make(map[string]bool, len(selected))
	for _, d := range selected {
		checked[d] = true
	}
	p, err := a.replicaForm(r, master, checked)
	if err != nil {
		a.internalError(w, "replica form", err)
		return
	}
	p.Target, _ = strconv.ParseInt(r.PostFormValue("target"), 10, 64)
	p.ForceDump = r.PostFormValue("force_dump") != ""
	fail := func(msg string) {
		p.Flash = msg
		a.render.renderStatus(w, http.StatusBadRequest, "replica_new", p)
	}
	if p.Target <= 0 {
		fail(msgReplicaTarget)
		return
	}
	for _, d := range selected {
		if !databaseName.MatchString(d) {
			fail(msgReplicaBadDatabase + d)
			return
		}
	}
	// Nothing selected would mean "all databases" to the orchestrator.
	if len(selected) == 0 && len(p.Inventory.Databases) > 0 {
		fail(msgReplicaDatabases)
		return
	}
	if r.PostFormValue("confirm_wipe") == "" {
		fail(msgReplicaConfirm)
		return
	}
	id, err := a.Orchestrator.StartCreateReplica(r.Context(), orchestrator.CreateReplicaParams{
		MasterServerID: master.ID, TargetServerID: p.Target,
		Databases: selected, ForceDump: p.ForceDump, ConfirmWipe: true,
	})
	if err != nil {
		a.Logger.Warn("create replica refused", "master", master.ID, "target", p.Target, "err", err)
		fail(err.Error())
		return
	}
	a.Logger.Info("create replica started", "master", master.ID, "target", p.Target, "job", id, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

type replicaPage struct {
	Page
	Server  store.Server
	Online  bool
	Replica store.Replica
	Master  store.Server
	Jobs    []jobRow
}

func (a *app) replicaDetail(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	rep, err := a.Store.GetReplicaByServer(srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgReplicaNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "get replica", err)
		return
	}
	p := replicaPage{Page: a.page(r), Server: srv, Online: a.Hub.IsOnline(srv.ID), Replica: rep}
	topo, err := a.Store.GetTopology(rep.TopologyID)
	if err == nil {
		p.Master, err = a.Store.GetServer(topo.MasterServerID)
	}
	if err != nil {
		a.internalError(w, "get master", err)
		return
	}
	jobs, err := a.Store.ListJobsForServer(srv.ID, jobHistoryLimit)
	if err == nil {
		p.Jobs, err = a.jobRows(jobs)
	}
	if err != nil {
		a.internalError(w, "list jobs", err)
		return
	}
	a.render.Render(w, "replica_detail", p)
}
