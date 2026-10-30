package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
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
	StartAddDatabases(ctx context.Context, replicaServerID int64, databases []string) (string, error)
	StartRemoveDatabases(ctx context.Context, replicaServerID int64, databases []string, dropData bool) (string, error)
	Approve(jobID string) error
	Retry(jobID string) error
	Cancel(jobID string) error
}

const (
	msgMySQLUserRequired = "Nama pengguna MySQL wajib diisi"
	msgMySQLFieldTooLong = "Nama pengguna, kata sandi, atau host MySQL terlalu panjang"
	msgReplicaNotFound   = "Replika tidak ditemukan"
	msgReplicaTarget     = "Pilih server tujuan replika terlebih dahulu."
	msgReplicaDatabases  = "Pilih minimal satu database untuk direplikasi."
	msgReplicaTargetBad  = "Server tujuan tidak memenuhi syarat"
	msgReplicaConfirm    = "Replika belum dibuat: centang kotak persetujuan bahwa server tujuan dikunci hanya-baca dan database lamanya dibuang."
	msgReplicaAll        = "replika ini mengikuti semua database"
	msgReplicaNotReady   = "Database replika hanya bisa diubah saat replika berstatus Siap."
	msgPickDatabase      = "Pilih minimal satu database."
	msgRemoveAll         = "Lepas semua database tidak didukung; hapus replika ini bila tidak diperlukan lagi."

	maxMySQLUser     = 32 // MySQL's limit for a user name
	maxMySQLPassword = 256
	maxMySQLHost     = 255
)

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
	// The host goes as typed: empty makes the agent use the local socket,
	// which also reaches root@localhost with auth_socket, a non-default
	// port and a server without networking.
	_, err := a.Orchestrator.Connect(r.Context(), srv.ID, proto.ConnectParams{User: form.User, Password: password, Host: form.Host})
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
	// Only a server the form offers (a stale form or a crafted post may
	// name another one).
	if !slices.ContainsFunc(p.Targets, func(c targetChoice) bool { return c.ID == p.Target }) {
		fail(msgReplicaTargetBad)
		return
	}
	// Only databases the master has; the orchestrator and the agent check
	// the names again.
	for _, d := range selected {
		if !slices.ContainsFunc(p.Inventory.Databases, func(m proto.DatabaseInfo) bool { return m.Name == d }) {
			fail("Database " + d + " tidak ada di master")
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
	Ready   bool // its databases can be changed
	Master  store.Server
	Jobs    []jobRow
}

// replicaFromPath loads the server of {id} and its replica row, answering
// 404 (or 500) and returning false when it cannot.
func (a *app) replicaFromPath(w http.ResponseWriter, r *http.Request) (store.Server, store.Replica, bool) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return store.Server{}, store.Replica{}, false
	}
	rep, err := a.Store.GetReplicaByServer(srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgReplicaNotFound, http.StatusNotFound)
		return store.Server{}, store.Replica{}, false
	}
	if err != nil {
		a.internalError(w, "get replica", err)
		return store.Server{}, store.Replica{}, false
	}
	return srv, rep, true
}

// replicaMaster is the master server of rep.
func (a *app) replicaMaster(rep store.Replica) (store.Server, error) {
	topo, err := a.Store.GetTopology(rep.TopologyID)
	if err != nil {
		return store.Server{}, err
	}
	return a.Store.GetServer(topo.MasterServerID)
}

func (a *app) replicaDetail(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	p := replicaPage{Page: a.page(r), Server: srv, Online: a.Hub.IsOnline(srv.ID), Replica: rep,
		Ready: rep.State == store.ReplicaReady}
	var err error
	if p.Master, err = a.replicaMaster(rep); err != nil {
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

// replicaDatabase is one database on the replica's databases page.
type replicaDatabase struct {
	Name     string
	Bytes    uint64
	OnMaster bool // the master's inventory lists it
	Followed bool
}

type replicaDatabasesPage struct {
	Page
	Server     store.Server
	Master     store.Server
	Replica    store.Replica
	Ready      bool
	Databases  []replicaDatabase // the master's, then followed ones it no longer has
	Unfollowed []replicaDatabase // can be added
	Followed   []replicaDatabase // can be removed
}

// subsetReplicaFromPath is replicaFromPath for a replica that follows a
// subset of the master's databases; one that follows all of them has no
// databases page (404).
func (a *app) subsetReplicaFromPath(w http.ResponseWriter, r *http.Request) (store.Server, store.Replica, bool) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if ok && len(rep.Databases) == 0 {
		http.Error(w, msgReplicaAll, http.StatusNotFound)
		return store.Server{}, store.Replica{}, false
	}
	return srv, rep, ok
}

func (a *app) replicaDatabasesForm(r *http.Request, srv store.Server, rep store.Replica) (replicaDatabasesPage, error) {
	p := replicaDatabasesPage{Page: a.page(r), Server: srv, Replica: rep, Ready: rep.State == store.ReplicaReady}
	var err error
	if p.Master, err = a.replicaMaster(rep); err != nil {
		return p, err
	}
	inv, err := a.inventory(p.Master.ID)
	if err != nil {
		return p, err
	}
	for _, d := range inv.Databases {
		p.Databases = append(p.Databases, replicaDatabase{Name: d.Name, Bytes: d.Bytes, OnMaster: true,
			Followed: slices.Contains(rep.Databases, d.Name)})
	}
	for _, d := range rep.Databases {
		if !slices.ContainsFunc(inv.Databases, func(m proto.DatabaseInfo) bool { return m.Name == d }) {
			p.Databases = append(p.Databases, replicaDatabase{Name: d, Followed: true})
		}
	}
	for _, d := range p.Databases {
		if d.Followed {
			p.Followed = append(p.Followed, d)
		} else {
			p.Unfollowed = append(p.Unfollowed, d)
		}
	}
	return p, nil
}

func (a *app) replicaDatabases(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.subsetReplicaFromPath(w, r)
	if !ok {
		return
	}
	p, err := a.replicaDatabasesForm(r, srv, rep)
	if err != nil {
		a.internalError(w, "replica databases", err)
		return
	}
	a.render.Render(w, "replica_databases", p)
}

func (a *app) addReplicaDatabases(w http.ResponseWriter, r *http.Request) {
	a.changeReplicaDatabases(w, r, "add", func(p replicaDatabasesPage, selected []string) (string, string) {
		for _, d := range selected {
			i := slices.IndexFunc(p.Databases, func(rd replicaDatabase) bool { return rd.Name == d })
			switch {
			case i < 0 || !p.Databases[i].OnMaster:
				return "", "Database " + d + " tidak ada di master"
			case p.Databases[i].Followed:
				return "", "Database " + d + " sudah diikuti replika ini"
			}
		}
		id, err := a.Orchestrator.StartAddDatabases(r.Context(), p.Server.ID, selected)
		if err != nil {
			return "", err.Error()
		}
		return id, ""
	})
}

func (a *app) removeReplicaDatabases(w http.ResponseWriter, r *http.Request) {
	dropData := r.PostFormValue("drop_data") != ""
	a.changeReplicaDatabases(w, r, "remove", func(p replicaDatabasesPage, selected []string) (string, string) {
		for _, d := range selected {
			if !slices.Contains(p.Replica.Databases, d) {
				return "", "Database " + d + " tidak diikuti replika ini"
			}
		}
		if !slices.ContainsFunc(p.Replica.Databases, func(d string) bool { return !slices.Contains(selected, d) }) {
			return "", msgRemoveAll
		}
		id, err := a.Orchestrator.StartRemoveDatabases(r.Context(), p.Server.ID, selected, dropData)
		if err != nil {
			return "", err.Error()
		}
		return id, ""
	})
}

// changeReplicaDatabases handles the add and remove posts of the databases
// page: start checks the selection like the orchestrator does and starts
// the job, answering its id or the reason it refused.
func (a *app) changeReplicaDatabases(w http.ResponseWriter, r *http.Request, action string,
	start func(p replicaDatabasesPage, selected []string) (jobID, refusal string)) {
	srv, rep, ok := a.subsetReplicaFromPath(w, r)
	if !ok {
		return
	}
	p, err := a.replicaDatabasesForm(r, srv, rep)
	if err != nil {
		a.internalError(w, "replica databases", err)
		return
	}
	selected := r.PostForm["databases"]
	refusal := ""
	id := ""
	switch {
	case !p.Ready:
		refusal = msgReplicaNotReady
	case len(selected) == 0:
		refusal = msgPickDatabase
	default:
		id, refusal = start(p, selected)
	}
	if refusal != "" {
		a.Logger.Warn("replica databases "+action+" refused", "server", srv.ID, "err", refusal)
		p.Flash = refusal
		a.render.renderStatus(w, http.StatusBadRequest, "replica_databases", p)
		return
	}
	a.Logger.Info("replica databases "+action+" started", "server", srv.ID, "databases", selected, "job", id,
		"by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}
