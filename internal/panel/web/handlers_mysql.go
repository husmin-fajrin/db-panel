package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
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
	StartResync(ctx context.Context, p orchestrator.ResyncParams) (string, error)
	// Direct controls: refused while the server has an unfinished job.
	StopReplica(ctx context.Context, serverID int64) error
	StartReplica(ctx context.Context, serverID int64) error
	Relock(ctx context.Context, serverID int64) error
	UnlockMaster(ctx context.Context, serverID int64) error
	DetachReplica(ctx context.Context, serverID int64, stop bool) error
	DeleteServer(ctx context.Context, serverID int64) error
	Approve(jobID string) error
	Retry(jobID string) error
	Cancel(jobID string) error
	// Superseded says why a job may no longer be approved or retried; "".
	Superseded(jobID string) string
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
	msgReplicaNotReady   = "Database replika hanya bisa diubah saat replika berstatus Siap atau Perlu perhatian."
	msgNoMasterInventory = "daftar database master belum ada; hubungkan MySQL master dulu"
	msgPickDatabase      = "Pilih minimal satu database."
	msgRemoveAll         = "Lepas semua database tidak didukung; hapus replika ini bila tidak diperlukan lagi."

	msgMySQLHostBad      = "Host MySQL tidak valid: isi alamat IP atau nama host saja (tanpa spasi, garis miring, atau @), atau kosongkan untuk socket lokal."
	msgSourceHostBad     = "Alamat master tidak valid: isi alamat IP atau nama host saja."
	msgSourcePortBad     = "Port MySQL master harus angka antara 1 dan 65535."
	msgTargetUnconnected = "MySQL di server tujuan sudah terpasang tetapi belum dihubungkan. Buka halaman server itu dan jalankan Hubungkan MySQL dulu."

	maxMySQLUser     = 32 // MySQL's limit for a user name
	maxMySQLPassword = 256
	maxMySQLHost     = 255

	// inventoryCap is the orchestrator's cap on an inventory's databases;
	// a list that long may be cut.
	inventoryCap = 5000
)

// hostRe is a MySQL host the panel passes on: an IP address or a host name,
// as the agent checks it too (SEC-33).
var hostRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)

// validHost reports whether h is an address or name hostRe admits; one
// with a colon must be an IPv6 address.
func validHost(h string) bool {
	return hostRe.MatchString(h) && (!strings.Contains(h, ":") || net.ParseIP(h) != nil)
}

// notices are the success messages a redirect names in ?pesan=.
var notices = map[string]string{
	"terhubung":  "MySQL berhasil dihubungkan.",
	"disegarkan": "Data MySQL sudah diperbarui.",
	"sumber":     "Alamat MySQL master untuk replika disimpan. Alamat ini diperiksa saat membuat replika berikutnya (replika harus bisa menjangkaunya).",
	"dihentikan": "Replikasi dihentikan. Lag membesar sampai Anda menjalankannya lagi dengan Start replika.",
	"dijalankan": "Replikasi dijalankan lagi. Status terbaru tampil setelah laporan berikutnya dari agent.",
	"dikunci":    "Replika dikunci hanya-baca lagi.",
	"dilepas": "Replika dilepas. MySQL di server ini terus mengikuti master sampai Anda menghentikannya sendiri; " +
		"panel tidak lagi memantaunya.",
	"dilepas-henti": "Replika dilepas dan replikasinya dihentikan. MySQL di server ini tetap terkunci hanya-baca; " +
		"untuk membukanya jalankan di server itu: mysql -e \"SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF\"",
	"kunci-dibuka": "Kunci hanya-baca master dibuka; aplikasi bisa menulis lagi.",
	"diperiksa":    "Server sudah diperiksa; status di bawah memakai laporan terbaru.",
	"senyap":       "Pemberitahuan untuk server ini ditunda (Jangan ganggu).",
	"senyap-batal": "Jangan ganggu dibatalkan; pemberitahuan untuk server ini aktif lagi.",
	"dihapus": "Server dihapus dari panel. Agent di server itu masih terpasang dan terus mencoba terhubung; " +
		"matikan dengan: sudo systemctl disable --now dbpanel-agent",
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
	// The password never goes into the audit log.
	detail := map[string]any{"mysql_user": form.User, "host": form.Host}
	fail := func(msg string) {
		a.audit(r, "mysql_connect", auditServer, srv.ID, detail, msg)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msg, Form: form})
	}
	switch {
	case form.User == "":
		fail(msgMySQLUserRequired)
		return
	case len(form.User) > maxMySQLUser || len(password) > maxMySQLPassword || len(form.Host) > maxMySQLHost:
		fail(msgMySQLFieldTooLong)
		return
	case form.Host != "" && !validHost(form.Host):
		fail(msgMySQLHostBad)
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
	a.audit(r, "mysql_connect", auditServer, srv.ID, detail, auditOK)
	a.Logger.Info("mysql connected", "server", srv.ID, "mysql_user", form.User, "by", a.page(r).Username)
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan=terhubung", http.StatusSeeOther)
}

func (a *app) refreshMySQL(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	_, err := a.Orchestrator.RefreshInventory(r.Context(), srv.ID)
	a.audit(r, "mysql_refresh", auditServer, srv.ID, nil, outcomeOf(err))
	if err != nil {
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
	a.audit(r, "prepare_master", auditServer, srv.ID, map[string]any{"job_id": id}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("prepare master refused", "server", srv.ID, "err", err)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	a.Logger.Info("prepare master started", "server", srv.ID, "job", id, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// replicaRow is a replica with the name of its server and its monitoring
// status.
type replicaRow struct {
	store.Replica
	ServerName string
	Status     statusView
}

type masterPage struct {
	Page
	Server    store.Server
	Online    bool
	Topology  store.Topology
	Inventory store.Inventory
	Replicas  []replicaRow
	Jobs      []jobRow
	// The source address form after a refused post.
	SourceHost string
	SourcePort string
	SourceOpen bool
	// Locked: the monitor reports it read-only (Buka kunci master is
	// offered).
	Locked   bool
	Controls serverControls
	Status   statusView
	Facts    masterFacts
	Timeline timelineView
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
	p.Notice = notices[r.URL.Query().Get("pesan")]
	a.renderMaster(w, r, http.StatusOK, p)
}

// setMasterSource changes the address replicas reach the master's MySQL
// at (multi-homed hosts, NAT, VPN). It is checked from each replica when
// the next one is created (its preflight's reachability check).
func (a *app) setMasterSource(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	host := strings.TrimSpace(r.PostFormValue("source_host"))
	portText := strings.TrimSpace(r.PostFormValue("source_port"))
	p := masterPage{Page: a.page(r), Server: srv, Online: a.Hub.IsOnline(srv.ID), Topology: topo,
		SourceHost: host, SourcePort: portText, SourceOpen: true}
	port, err := strconv.Atoi(portText)
	switch {
	case !validHost(host):
		p.Flash = msgSourceHostBad
	case err != nil || port < 1 || port > 65535:
		p.Flash = msgSourcePortBad
	}
	detail := map[string]any{"source_host": host, "source_port": portText}
	if p.Flash != "" {
		a.audit(r, "master_source", auditMaster, srv.ID, detail, p.Flash)
		a.renderMaster(w, r, http.StatusBadRequest, p)
		return
	}
	err = a.Store.SetTopologySource(topo.ID, host, port)
	a.audit(r, "master_source", auditMaster, srv.ID, detail, outcomeOf(err))
	if err != nil {
		a.internalError(w, "set topology source", err)
		return
	}
	a.Logger.Info("master source address changed", "master", srv.ID, "host", host, "port", port, "by", a.page(r).Username)
	http.Redirect(w, r, "/masters/"+strconv.FormatInt(srv.ID, 10)+"?pesan=sumber", http.StatusSeeOther)
}

// renderMaster fills p's inventory, replicas and jobs and renders it.
func (a *app) renderMaster(w http.ResponseWriter, r *http.Request, status int, p masterPage) {
	srv, topo := p.Server, p.Topology
	p.Online = a.Hub.IsOnline(srv.ID)
	p.Controls = a.controls(srv)
	v, monitored := a.Monitor.View(srv.ID)
	p.Locked = monitored && v.Status.Code == monitor.StatusLocked
	p.Status = newStatusView(srv.ID, srv.Role, v, monitored, a.Now())
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
		p.Replicas = append(p.Replicas, replicaRow{Replica: rep, ServerName: names[rep.ServerID],
			Status: a.statusOf(rep.ServerID, store.RoleReplica)})
	}
	p.Facts = newMasterFacts(v, monitored, len(reps))
	if p.Timeline, err = a.timeline(r, srv.ID); err != nil {
		a.internalError(w, "timeline", err)
		return
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
	a.render.renderStatus(w, status, "master_detail", p)
}

// targetChoice is a server the new replica can be created on.
type targetChoice struct {
	store.Server
	Retry bool // its earlier replica attempt failed; a new one replaces it
	// NotConnected: its MySQL is installed but not connected; it is shown
	// disabled, with a link to its page (Hubungkan MySQL).
	NotConnected bool
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
		notConnected := false
		if orchestrator.ProbeSaysMySQLInstalled(s) {
			inv, err := a.inventory(s.ID)
			if err != nil {
				return p, err
			}
			notConnected = !inv.Connected
		}
		switch s.Role {
		case store.RoleUnassigned:
			p.Targets = append(p.Targets, targetChoice{Server: s, NotConnected: notConnected})
		case store.RoleReplica:
			rep, err := a.Store.GetReplicaByServer(s.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return p, err
			}
			if err == nil && rep.State == store.ReplicaFailed {
				p.Targets = append(p.Targets, targetChoice{Server: s, Retry: true, NotConnected: notConnected})
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
	detail := map[string]any{"master_server_id": master.ID, "databases": selected, "force_dump": p.ForceDump}
	fail := func(msg string) {
		a.audit(r, "create_replica", auditReplica, p.Target, detail, msg)
		p.Flash = msg
		a.render.renderStatus(w, http.StatusBadRequest, "replica_new", p)
	}
	if p.Target <= 0 {
		fail(msgReplicaTarget)
		return
	}
	// Only a server the form offers (a stale form or a crafted post may
	// name another one).
	i := slices.IndexFunc(p.Targets, func(c targetChoice) bool { return c.ID == p.Target })
	if i < 0 {
		fail(msgReplicaTargetBad)
		return
	}
	if p.Targets[i].NotConnected {
		fail(msgTargetUnconnected)
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
	detail["job_id"] = id
	a.audit(r, "create_replica", auditReplica, p.Target, detail, auditOK)
	a.Logger.Info("create replica started", "master", master.ID, "target", p.Target, "job", id, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

type replicaPage struct {
	Page
	Server    store.Server
	Online    bool
	Replica   store.Replica
	CanChange bool // its databases can be changed now
	Master    store.Server
	Jobs      []jobRow
	// Health is the monitor's view of the replica; nil before its first
	// report.
	Health   *monitor.View
	Controls serverControls
	// Busy: the server has an unfinished job; the controls wait for it.
	Busy bool
	// The controls that make sense now.
	CanStop, CanStart, CanRelock, CanResync, CanDetach bool
	// BinlogKeep is how long the master keeps binary logs ("7 hari"); ""
	// when unknown.
	BinlogKeep string
	// MasterBytes is the size of the master's data the replica follows.
	MasterBytes uint64
	// WipeList is the replica's databases (its inventory), which a resync
	// drops; WipeListRead is false while that inventory was never read.
	WipeList     []string
	WipeListRead bool
	OpenResync   bool // open the resync dialog on load (after prepareResync)
	Status       statusView
	Timeline     timelineView
}

// ButtonClass is the extra class of the control an explanation's Primary
// names ("resync", "start", "master"): btn-primary for the advised one and
// btn-outline for the others; "" while no advice is shown.
func (p replicaPage) ButtonClass(name string) string {
	switch e := p.Status.Explain; {
	case e == nil || e.Primary == "":
		return ""
	case e.Primary == name:
		return "btn-primary"
	}
	return "btn-outline"
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
	p := replicaPage{Page: a.page(r), Server: srv, Replica: rep}
	p.Notice = notices[r.URL.Query().Get("pesan")]
	// After Resync ulang read the replica's databases (prepareResync), the
	// page opens its dialog on them. A GET itself changes nothing.
	p.OpenResync = r.URL.Query().Get("buka") == "resync"
	a.renderReplica(w, r, http.StatusOK, p)
}

// renderReplica fills p from p.Server and p.Replica and renders it.
func (a *app) renderReplica(w http.ResponseWriter, r *http.Request, status int, p replicaPage) {
	srv, rep := p.Server, p.Replica
	p.Online = a.Hub.IsOnline(srv.ID)
	p.CanChange = canChangeDatabases(rep)
	p.Controls = a.controls(srv)
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
	if _, p.Busy, err = a.Store.ActiveJobForServer(srv.ID); err != nil {
		a.internalError(w, "active job", err)
		return
	}
	v, monitored := a.Monitor.View(srv.ID)
	if monitored {
		p.Health = &v
	}
	p.Status = newStatusView(srv.ID, srv.Role, v, monitored, a.Now())
	if p.Timeline, err = a.timeline(r, srv.ID); err != nil {
		a.internalError(w, "timeline", err)
		return
	}
	if mv, ok := a.Monitor.View(p.Master.ID); ok && mv.Snap != nil {
		p.BinlogKeep = binlogKeep(mv.Snap.BinlogExpireSec)
	}
	masterInv, err := a.inventory(p.Master.ID)
	if err != nil {
		a.internalError(w, "get inventory", err)
		return
	}
	for _, d := range masterInv.Databases {
		if len(rep.Databases) == 0 || slices.Contains(rep.Databases, d.Name) {
			p.MasterBytes += d.Bytes
		}
	}
	inv, err := a.inventory(srv.ID)
	if err != nil {
		a.internalError(w, "get inventory", err)
		return
	}
	p.WipeListRead = inv.Connected
	for _, d := range inv.Databases {
		p.WipeList = append(p.WipeList, d.Name)
	}

	controllable := !p.Busy && canChangeDatabases(rep) // the orchestrator's rule for stop, start, relock
	stopped := p.Health != nil &&
		(p.Health.Status.Code == monitor.StatusStopped || p.Health.Status.Code == monitor.StatusStoppedOutside)
	p.CanStop = controllable && !rep.Paused && !stopped
	p.CanStart = controllable && (rep.Paused || stopped)
	p.CanRelock = controllable && p.Health != nil && p.Health.Status.LockLost
	p.CanResync = !p.Busy && (canChangeDatabases(rep) || rep.State == store.ReplicaFailed)
	p.CanDetach = !p.Busy
	a.render.renderStatus(w, status, "replica_detail", p)
}

// canChangeDatabases reports whether the replica's databases can be
// changed now: it is ready, or its last change failed (attention).
func canChangeDatabases(rep store.Replica) bool {
	return rep.State == store.ReplicaReady || rep.State == store.ReplicaAttention
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
	Server      store.Server
	Master      store.Server
	Replica     store.Replica
	CanChange   bool
	NoInventory bool              // the master's inventory was never read
	Databases   []replicaDatabase // the master's, then followed ones it no longer has
	Unfollowed  []replicaDatabase // can be added
	Followed    []replicaDatabase // can be removed
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
	p := replicaDatabasesPage{Page: a.page(r), Server: srv, Replica: rep, CanChange: canChangeDatabases(rep)}
	var err error
	if p.Master, err = a.replicaMaster(rep); err != nil {
		return p, err
	}
	inv, err := a.Store.GetInventory(p.Master.ID)
	if errors.Is(err, store.ErrNotFound) {
		p.NoInventory, err = true, nil
	}
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
	a.changeReplicaDatabases(w, r, "add_databases", nil, func(p replicaDatabasesPage, selected []string) (string, string) {
		if p.NoInventory {
			return "", msgNoMasterInventory
		}
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
	a.changeReplicaDatabases(w, r, "remove_databases", map[string]any{"drop_data": dropData}, func(p replicaDatabasesPage, selected []string) (string, string) {
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
// the job, answering its id or the reason it refused. action is the audit
// action; detail (may be nil) goes into its entry.
func (a *app) changeReplicaDatabases(w http.ResponseWriter, r *http.Request, action string, detail map[string]any,
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
	msg := ""
	id := ""
	switch {
	case !p.CanChange:
		msg = msgReplicaNotReady
	case len(selected) == 0:
		msg = msgPickDatabase
	default:
		id, msg = start(p, selected)
	}
	verb := strings.TrimSuffix(action, "_databases") // add or remove
	if detail == nil {
		detail = map[string]any{}
	}
	detail["databases"] = selected
	if id != "" {
		detail["job_id"] = id
	}
	a.audit(r, action, auditReplica, srv.ID, detail, outcomeMsg(msg))
	if msg != "" {
		a.Logger.Warn("replica databases "+verb+" refused", "server", srv.ID, "err", msg)
		p.Flash = msg
		a.render.renderStatus(w, http.StatusBadRequest, "replica_databases", p)
		return
	}
	a.Logger.Info("replica databases "+verb+" started", "server", srv.ID, "databases", selected, "job", id,
		"by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}
