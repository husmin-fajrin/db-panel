package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
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
	// StartMySQLTLS installs a new MySQL certificate on the server;
	// StartTightenTLS gives a master and its replicas certificates and
	// makes replication verify the master's.
	StartMySQLTLS(ctx context.Context, serverID int64) (string, error)
	StartTightenTLS(ctx context.Context, masterServerID int64) (string, error)
	// Direct controls: refused while the server has an unfinished job.
	StopReplica(ctx context.Context, serverID int64) error
	StartReplica(ctx context.Context, serverID int64) error
	Relock(ctx context.Context, serverID int64) error
	UnlockMaster(ctx context.Context, serverID int64) error
	DetachReplica(ctx context.Context, serverID int64, stop bool) error
	// CancelRebuildAttempt makes an old master left by a failed rebuild a
	// plain quarantined old master again (G-2).
	CancelRebuildAttempt(ctx context.Context, serverID int64) error
	DeleteServer(ctx context.Context, serverID int64) error
	// DeleteUnquarantined deletes a quarantined old master whose
	// quarantine never arrived (a dead server); guard it with a typed
	// confirmation.
	DeleteUnquarantined(ctx context.Context, serverID int64) error
	// Approve and Retry refuse a job that drops data (DropsData) without
	// ack.Confirmed: the handler consumes a job_approve or job_retry token
	// for that job first.
	Approve(jobID string, ack orchestrator.JobAck) error
	Retry(jobID string, ack orchestrator.JobAck) error
	DropsData(jobID string) (bool, error)
	Cancel(jobID string) error
	// Superseded says why a job may no longer be approved or retried; "".
	Superseded(jobID string) string
	// Promotion of a replica to master: the consideration data, the job,
	// "Sudah saya alihkan", and "Arahkan sekarang" for a replica waiting
	// to be pointed at the new master.
	PromoteView(ctx context.Context, masterServerID int64) (orchestrator.PromoteView, error)
	StartPromote(ctx context.Context, p orchestrator.PromoteParams) (string, error)
	// ConfirmCutover's warning, when not "", must be shown: the old master
	// is not quarantined yet.
	ConfirmCutover(topologyID int64) (warning string, err error)
	RepointNow(ctx context.Context, replicaServerID int64) error
	// The quarantined old master (spec 5.4): "Periksa data tertinggal"
	// and "Amankan data tertinggal" start short jobs; the salvage files
	// are listed, read a chunk at a time and deleted directly. Connections
	// feeds the cutover screen (spec 5.5).
	StartLeftover(ctx context.Context, serverID int64) (string, error)
	StartSalvage(ctx context.Context, serverID int64) (string, error)
	Connections(ctx context.Context, serverID int64) (proto.ConnectionsResult, error)
	SalvageList(ctx context.Context, serverID int64) ([]proto.SalvageFile, error)
	SalvageRead(ctx context.Context, serverID int64, name string, offset int64) (proto.SalvageReadResult, error)
	SalvageDelete(ctx context.Context, serverID int64, name string) error
	// RebuildApproval asks the server's agent whether root on it approved
	// its rebuild ("Bangun ulang sebagai replika", spec 5.2), and the code
	// to approve while not.
	RebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error)
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

// notices are the success messages a redirect names in ?pesan=.
var notices = map[string]string{
	"terhubung":  "MySQL berhasil dihubungkan.",
	"disegarkan": "Data MySQL sudah diperbarui.",
	"sumber": "Alamat MySQL master untuk replika disimpan. Alamat ini diperiksa saat membuat replika berikutnya (replika harus bisa menjangkaunya). " +
		"Replika yang sudah ada perlu diarahkan ke alamat ini di servernya sendiri: lihat perintahnya di bawah.",
	"sumber-sertifikat": "Alamat MySQL master untuk replika disimpan, tetapi sertifikat MySQL master belum memuat alamat ini: " +
		"klik Perbarui sertifikat MySQL di halaman server master sebelum membuat replika atau Resync ulang. " +
		"Replika yang sudah ada perlu diarahkan ke alamat ini di servernya sendiri: lihat perintahnya di bawah.",
	"dihentikan": "Replikasi dihentikan. Lag membesar sampai Anda menjalankannya lagi dengan Start replika.",
	"dijalankan": "Replikasi dijalankan lagi. Status terbaru tampil setelah laporan berikutnya dari agent.",
	"dikunci":    "Replika dikunci hanya-baca lagi.",
	"percobaan-dibatalkan": "Percobaan bangun ulang dibatalkan; server ini kembali tercatat sebagai master lama yang " +
		"dikarantina.",
	"dilepas": "Replika dilepas. MySQL di server ini terus mengikuti master sampai Anda menghentikannya sendiri; " +
		"panel tidak lagi memantaunya.",
	"dilepas-henti": "Replika dilepas dan replikasinya dihentikan. MySQL di server ini tetap terkunci hanya-baca; " +
		"untuk membukanya jalankan di server itu: mysql -e \"SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF\" " +
		"(SET PERSIST: tetap terbuka walau MySQL di-restart).",
	"kunci-dibuka":       "Kunci hanya-baca master dibuka; aplikasi bisa menulis lagi.",
	"diperiksa":          "Server sudah diperiksa; status di bawah memakai laporan terbaru.",
	"senyap":             "Pemberitahuan untuk server ini ditunda (Jangan ganggu).",
	"senyap-batal":       "Jangan ganggu dibatalkan; pemberitahuan untuk server ini aktif lagi.",
	"pengaturan":         "Pengaturan disimpan.",
	"tes-terkirim":       "Pesan tes terkirim ke Telegram; periksa chat tujuannya.",
	"sertifikat-sendiri": "Sertifikat yang Anda unggah sekarang dipakai panel.",
	"sertifikat-panel":   "Panel kembali memakai sertifikatnya sendiri.",
	"sertifikat-nama":    "Nama tambahan disimpan; sertifikat panel sudah diperbarui.",
	"dialihkan": "Pengalihan aplikasi dicatat: promote selesai. Master lama sekarang bisa diperiksa, dibangun ulang sebagai " +
		"replika, atau dihapus dari halaman servernya.",
	"diarahkan":      "Replika diarahkan ke master baru. Status terbaru tampil setelah laporan berikutnya dari agent.",
	"berkas-dihapus": "Berkas data tertinggal dihapus dari server.",
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
	case form.Host != "" && !orchestrator.ValidHost(form.Host):
		fail(msgMySQLHostBad)
		return
	}
	// The host goes as typed: empty makes the agent use the local socket,
	// which also reaches root@localhost with auth_socket, a non-default
	// port and a server without networking.
	_, err := a.Orchestrator.Connect(r.Context(), srv.ID, proto.ConnectParams{User: form.User, Password: password, Host: form.Host})
	if err != nil {
		a.Logger.Warn("mysql connect failed", "server", srv.ID, "err", logErr(err))
		fail(err.Error())
		return
	}
	a.audit(r, "mysql_connect", auditServer, srv.ID, detail, auditOK)
	a.Logger.Info("mysql connected", "server", srv.ID, "mysql_user", form.User, "by", auth.UsernameFrom(r.Context()))
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
		a.Logger.Warn("mysql refresh failed", "server", srv.ID, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan=disegarkan", http.StatusSeeOther)
}

// prepareMaster starts the prepare-master job on {id}.
func (a *app) prepareMaster(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	// A quarantined old master is never prepared: the orchestrator
	// refuses it too; refused here before any confirmation is asked.
	if srv.Role == store.RoleQuarantined {
		err := orchestrator.ErrPrepareQuarantined
		a.audit(r, "prepare_master", auditServer, srv.ID, map[string]any{}, outcomeOf(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	// Preparing a server that already is a master (it has a topology)
	// again needs the password confirmed.
	_, err := a.Store.GetTopologyByMaster(srv.ID)
	switch {
	case err == nil:
		if !a.requireConfirm(w, r, ActPrepareMasterAgain, auditServer, srv.ID) {
			return
		}
	case !errors.Is(err, store.ErrNotFound):
		a.internalError(w, "get topology", err)
		return
	}
	id, err := a.Orchestrator.StartPrepareMaster(r.Context(), srv.ID)
	a.audit(r, "prepare_master", auditServer, srv.ID, map[string]any{"job_id": id}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("prepare master refused", "server", srv.ID, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	a.Logger.Info("prepare master started", "server", srv.ID, "job", id, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// replicaRow is a replica with the name of its server and its monitoring
// status.
type replicaRow struct {
	store.Replica
	ServerName string
	Status     statusView
	// Unverified: a ready replica whose replication does not verify the
	// master's certificate ("Sambungan replikasi belum diverifikasi").
	Unverified bool
}

// installMySQLTLS starts the mysql_tls job on {id} ("Pasang sertifikat
// MySQL"), with the password confirmed.
func (a *app) installMySQLTLS(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok || !a.requireConfirm(w, r, ActMySQLTLS, auditServer, srv.ID) {
		return
	}
	id, err := a.Orchestrator.StartMySQLTLS(r.Context(), srv.ID)
	a.audit(r, "mysql_tls", auditServer, srv.ID, map[string]any{"job_id": id}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("mysql certificate refused", "server", srv.ID, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	a.Logger.Info("mysql certificate job started", "server", srv.ID, "job", id, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// tightenTLS starts the tighten_tls job on the master {id} ("Perketat
// sambungan replikasi"), with the password confirmed.
func (a *app) tightenTLS(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok || !a.requireConfirm(w, r, ActTightenTLS, auditServer, srv.ID) {
		return
	}
	id, err := a.Orchestrator.StartTightenTLS(r.Context(), srv.ID)
	a.audit(r, "tighten_tls", auditMaster, srv.ID, map[string]any{"job_id": id}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("tighten replication refused", "master", srv.ID, "err", logErr(err))
		p := masterPage{Page: a.page(r), Server: srv, Topology: topo}
		p.Flash = err.Error()
		a.renderMaster(w, r, http.StatusBadRequest, p)
		return
	}
	a.Logger.Info("tighten replication started", "master", srv.ID, "job", id, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
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
	// SourceSaved: the address for replicas was just changed.
	SourceSaved bool
	// SetSource lists the replicas whose agents must be pointed at the
	// address for replicas (spec 5.5): every replica right after the
	// address was saved, otherwise those whose last report shows another
	// source.
	SetSource []setSourceRow
	// Source is the address for replicas as host:port.
	Source string
	// Locked: the monitor reports it read-only (Buka kunci master is
	// offered).
	Locked   bool
	Controls serverControls
	Status   statusView
	Facts    masterFacts
	Timeline timelineView
	// NoCert: the master has no MySQL certificate from the panel. The
	// "Perketat sambungan replikasi" box shows when it has none or a
	// replica is Unverified (Tighten).
	NoCert  bool
	Tighten bool
	// PromoteEmergency: a promotion now would run as an emergency (the
	// master is gone); Local: the master is the panel's own server.
	PromoteEmergency bool
	Local            bool
	// Tree is the topology card: the master and its replicas with their
	// live status, as on the dashboard (design spec 5.6).
	Tree replicationRow
}

// setSourceRow is a replica whose agent's pinned source must be changed
// on that server (dbpanel agent set-source).
type setSourceRow struct {
	ServerID   int64
	ServerName string
	// Following is the source its last report shows; "" when unknown.
	Following string
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
	p.SourceSaved = r.URL.Query().Get("pesan") == "sumber" || r.URL.Query().Get("pesan") == "sumber-sertifikat"
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
	case !orchestrator.ValidHost(host):
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
	a.Logger.Info("master source address changed", "master", srv.ID, "host", host, "port", port, "by", auth.UsernameFrom(r.Context()))
	notice := "sumber"
	// The master's MySQL certificate does not name the new address: the
	// next replica would refuse it (I8).
	if rec, err := a.Store.GetServerTLS(srv.ID); err == nil && rec.Hosts != nil && !orchestrator.CertNamesHost(rec.Hosts, host) {
		notice = "sumber-sertifikat"
	}
	http.Redirect(w, r, "/masters/"+strconv.FormatInt(srv.ID, 10)+"?pesan="+notice, http.StatusSeeOther)
}

// renderMaster fills p's inventory, replicas and jobs and renders it.
func (a *app) renderMaster(w http.ResponseWriter, r *http.Request, status int, p masterPage) {
	srv, topo := p.Server, p.Topology
	p.Online = a.Hub.IsOnline(srv.ID)
	p.Controls = a.controls(srv)
	v, monitored := a.Monitor.View(srv.ID)
	p.Locked = monitored && v.Status.Code == monitor.StatusLocked
	p.Status = newStatusView(srv.ID, srv.Role, v, monitored, a.Now())
	p.PromoteEmergency = promoteEmergency(p.Online, v, monitored)
	p.Local = a.isLocal(srv)
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
	p.Source = net.JoinHostPort(topo.SourceHost, strconv.Itoa(topo.SourcePort))
	if _, err := a.Store.GetServerTLS(srv.ID); errors.Is(err, store.ErrNotFound) {
		p.NoCert = true
	} else if err != nil {
		a.internalError(w, "get server tls", err)
		return
	}
	p.Tighten = p.NoCert
	for _, rep := range reps {
		row := replicaRow{Replica: rep, ServerName: names[rep.ServerID], Status: a.statusOf(rep.ServerID, store.RoleReplica)}
		// A replica being created, resynced or failed is set when its
		// replication is connected.
		row.Unverified = !rep.TLSVerified && (rep.State == store.ReplicaReady || rep.State == store.ReplicaAttention)
		p.Tighten = p.Tighten || row.Unverified
		p.Replicas = append(p.Replicas, row)
		following := ""
		if rv, ok := a.Monitor.View(rep.ServerID); ok && rv.Snap != nil && rv.Snap.MySQLOK && rv.Snap.Replica != nil &&
			rv.Snap.Replica.SourceHost != "" {
			following = net.JoinHostPort(rv.Snap.Replica.SourceHost, strconv.Itoa(rv.Snap.Replica.SourcePort))
		}
		if p.SourceSaved || (following != "" && !strings.EqualFold(following, p.Source)) {
			p.SetSource = append(p.SetSource, setSourceRow{ServerID: rep.ServerID, ServerName: names[rep.ServerID], Following: following})
		}
	}
	p.Tree = replicationRow{ID: topo.ID, Master: masterCard{Server: srv, Status: p.Status}}
	for _, row := range p.Replicas {
		p.Tree.Replicas = append(p.Tree.Replicas, replicaCard{Server: store.Server{ID: row.ServerID, Name: row.ServerName}, Status: row.Status})
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
	// Rebuild: the form rebuilds the quarantined old master Rebuild.Server
	// as a replica of master ("Bangun ulang sebagai replika", spec 5.4);
	// it is the only target. RebuildBad: the server asked for is not one.
	Rebuild    *rebuildInfo
	RebuildBad bool
}

// replicaForm builds the replica form for master. checked nil means every
// database is selected. rebuild, when not 0, is the quarantined old
// master the form rebuilds: it is then the only target.
func (a *app) replicaForm(r *http.Request, master store.Server, checked map[string]bool, rebuild int64) (replicaNewPage, error) {
	p := replicaNewPage{Page: a.page(r), Master: master}
	var err error
	if p.Inventory, err = a.inventory(master.ID); err != nil {
		return p, err
	}
	for _, d := range p.Inventory.Databases {
		p.Databases = append(p.Databases, databaseChoice{DatabaseInfo: d, Checked: checked == nil || checked[d.Name]})
	}
	if rebuild != 0 {
		if p.Rebuild, err = a.rebuildTarget(r.Context(), master, rebuild, askApproval(r)); err != nil {
			return p, err
		}
		if p.Rebuild == nil {
			p.RebuildBad = true
			p.Flash = msgRebuildTarget
			return p, nil
		}
		s := p.Rebuild.Server
		p.Target = s.ID
		if !s.Paired || !a.Hub.IsOnline(s.ID) {
			return p, nil
		}
		inv, err := a.inventory(s.ID)
		if err != nil {
			return p, err
		}
		p.Targets = []targetChoice{{Server: s, NotConnected: !inv.Connected}}
		return p, nil
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
				// An old master left by a failed rebuild takes only a new
				// rebuild (its own form; the orchestrator refuses it here,
				// G-4).
				awaiting, err := a.oldMasterAwaitingRebuild(s)
				if err != nil {
					return p, err
				}
				if !awaiting {
					p.Targets = append(p.Targets, targetChoice{Server: s, Retry: true, NotConnected: notConnected})
				}
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
	rebuild, _ := strconv.ParseInt(r.URL.Query().Get("bangun-ulang"), 10, 64)
	p, err := a.replicaForm(r, master, nil, max(rebuild, 0))
	if err != nil {
		a.internalError(w, "replica form", err)
		return
	}
	status := http.StatusOK
	if p.RebuildBad {
		status = http.StatusBadRequest
	}
	a.render.renderStatus(w, status, "replica_new", p)
}

// createReplica starts "Buat replika"; with rebuild=1 it rebuilds the
// quarantined old master posted as target, behind a confirmation token
// and its typed name, and never while the cutover is pending.
func (a *app) createReplica(w http.ResponseWriter, r *http.Request) {
	master, _, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	target, _ := strconv.ParseInt(r.PostFormValue("target"), 10, 64)
	rebuild := r.PostFormValue("rebuild") == "1"
	acceptConnections := rebuild && r.PostFormValue("accept_connections") == "1"
	if rebuild && !a.requireConfirm(w, r, ActRebuildOldMaster, auditServer, target) {
		return
	}
	selected := r.PostForm["databases"]
	checked := make(map[string]bool, len(selected))
	for _, d := range selected {
		checked[d] = true
	}
	var rebuildID int64
	if rebuild {
		rebuildID = max(target, 1)
	}
	p, err := a.replicaForm(r, master, checked, rebuildID)
	if err != nil {
		a.internalError(w, "replica form", err)
		return
	}
	p.Target = target
	p.ForceDump = r.PostFormValue("force_dump") != ""
	detail := map[string]any{"master_server_id": master.ID, "databases": selected, "force_dump": p.ForceDump,
		"rebuild_old_master": rebuild}
	failStatus := func(status int, msg string) {
		a.audit(r, "create_replica", auditReplica, p.Target, detail, msg)
		p.Flash = msg
		a.render.renderStatus(w, status, "replica_new", p)
	}
	fail := func(msg string) { failStatus(http.StatusBadRequest, msg) }
	if p.Target <= 0 {
		fail(msgReplicaTarget)
		return
	}
	if rebuild {
		switch {
		case p.Rebuild == nil:
			fail(msgRebuildTarget)
			return
		case p.Rebuild.CutoverPending:
			failStatus(http.StatusConflict, msgRebuildCutover)
			return
		case !typedName(r, p.Rebuild.Server):
			fail(msgNameMismatch)
			return
		// After the token and the name: what the rebuild throws away
		// (the form's list was read again just now).
		case p.Rebuild.AcceptLabel != "" && !acceptConnections:
			fail(msgRebuildAccept)
			return
		}
		// Only an acknowledgement the form asked for reaches the
		// orchestrator; otherwise it reads the connections again itself
		// (ruling 14).
		acceptConnections = acceptConnections && p.Rebuild.AcceptLabel != ""
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
	var seen *orchestrator.ApprovalSeen
	if rebuild {
		seen = &orchestrator.ApprovalSeen{}
	}
	id, err := a.Orchestrator.StartCreateReplica(r.Context(), orchestrator.CreateReplicaParams{
		MasterServerID: master.ID, TargetServerID: p.Target,
		Databases: selected, ForceDump: p.ForceDump, ConfirmWipe: true, RebuildOldMaster: rebuild,
		AcceptConnections: acceptConnections, Approval: seen,
	})
	if rebuild {
		localApprovalDetail(seen, detail)
	}
	if err != nil {
		a.Logger.Warn("create replica refused", "master", master.ID, "target", p.Target, "err", logErr(err))
		// The box on the page with the refusal is read again, so both say
		// the same moment.
		if errors.Is(err, orchestrator.ErrRebuildNeedsApproval) && p.Rebuild != nil {
			a.rebuildApproval(r.Context(), p.Rebuild)
		}
		fail(shownError(err, p.Targets[i].Name))
		return
	}
	detail["job_id"] = id
	a.audit(r, "create_replica", auditReplica, p.Target, detail, auditOK)
	a.Logger.Info("create replica started", "master", master.ID, "target", p.Target, "job", id, "by", auth.UsernameFrom(r.Context()))
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
	// RebuildURL is the rebuild form of an old master left by a failed
	// rebuild (oldMasterAwaitingRebuild); Resync ulang and Lepas replika
	// are not offered on it.
	RebuildURL string
	// CancelAttempt: the server is an old master left by a failed rebuild
	// (role replica): "Batalkan percobaan bangun ulang" is offered in
	// place of Lepas replika (G-2).
	CancelAttempt bool
	// CanRepoint offers "Arahkan sekarang" (needs_repoint, spec 5.6);
	// RepointInfo is why it just did not repoint (the replica must be
	// rebuilt), shown as information.
	CanRepoint  bool
	RepointInfo string
	// BinlogKeep is how long the master keeps binary logs ("7 hari"); ""
	// when unknown.
	BinlogKeep string
	// MasterBytes is the size of the master's data the replica follows.
	MasterBytes uint64
	// WipeList is the replica's databases (its inventory), which a resync
	// drops; WipeListRead is false while that inventory was never read.
	WipeList     []string
	WipeListRead bool
	// Wipe is WipeList by whether the master's stored inventory has the
	// database (MasterInventoryRead: that inventory was read).
	Wipe                wipeGroups
	MasterInventoryRead bool
	OpenResync          bool // open the resync dialog on load (after prepareResync)
	Status              statusView
	Timeline            timelineView
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
	p.MasterInventoryRead = masterInv.Connected
	p.Wipe = groupWipe(inv.Databases, masterInv.Databases)

	controllable := !p.Busy && canChangeDatabases(rep) // the orchestrator's rule for stop, start, relock
	stopped := p.Health != nil &&
		(p.Health.Status.Code == monitor.StatusStopped || p.Health.Status.Code == monitor.StatusStoppedOutside)
	p.CanStop = controllable && !rep.Paused && !stopped
	p.CanStart = controllable && (rep.Paused || stopped)
	p.CanRelock = controllable && p.Health != nil && p.Health.Status.LockLost
	p.CanResync = !p.Busy && (canChangeDatabases(rep) || rep.State == store.ReplicaFailed ||
		rep.State == store.ReplicaNeedsRebuild)
	p.CanRepoint = !p.Busy && rep.State == store.ReplicaNeedsRepoint
	p.CanDetach = !p.Busy
	// An old master left by a failed rebuild: its agent stays quarantined.
	// Resync ulang is no rebuild (no approval check before it starts, no
	// connections acknowledgement, the server stays the topology's old
	// master), and Lepas replika would leave the panel treating it as an
	// unassigned server: only the rebuild is offered.
	if p.RebuildURL, err = a.rebuildURL(srv); err != nil {
		a.internalError(w, "old master check", err)
		return
	}
	if p.RebuildURL != "" {
		p.CanResync, p.CanDetach = false, false
		p.CancelAttempt = srv.Role == store.RoleReplica
	}
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
	srv, _, ok := a.subsetReplicaFromPath(w, r)
	if !ok || !a.requireConfirm(w, r, ActRemoveDatabases, auditReplica, srv.ID) {
		return
	}
	dropData := r.PostFormValue("drop_data") != ""
	a.changeReplicaDatabases(w, r, "remove_databases", map[string]any{"drop_data": dropData}, func(p replicaDatabasesPage, selected []string) (string, string) {
		// Dropping the data also needs the replica's name typed (spec 5.6).
		if dropData && !typedName(r, p.Server) {
			return "", msgNameMismatch
		}
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
		"by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}
