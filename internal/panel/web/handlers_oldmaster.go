package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// After a promotion: the application cutover screen (spec 5.5), the
// quarantined old master's page with its leftover check, salvage files,
// rebuild and delete (spec 5.4), and "Arahkan sekarang" of a replica left
// to repoint (spec 5.6).

const (
	msgOldUnreachable = "Master lama tidak terjangkau, tidak bisa membaca koneksinya"
	msgNewUnreachable = "Master baru tidak terjangkau, tidak bisa membaca koneksinya"
	msgOldNoConns     = "Tidak ada koneksi aplikasi tersisa"
	msgNewNoConns     = "Belum ada koneksi aplikasi ke master baru"
	msgConnsOldAgent  = "agent perlu dimutakhirkan: daftar koneksinya belum lengkap"
	msgConnsFailed    = "Koneksi tidak bisa dibaca; rinciannya ada di log panel."
	msgNotOldMaster   = "Server ini bukan master lama yang dikarantina."

	// msgRebuildCutover is spec 5.4's reason the rebuild waits.
	msgRebuildCutover = "Tekan 'Sudah saya alihkan' dulu: membangun ulang membuka kunci tulis sebentar saat memuat data, " +
		"dan aplikasi yang belum dialihkan bisa menulis ke sini"
	msgDeleteCutover = "Master lama ini tidak bisa dihapus sebelum aplikasi dinyatakan dialihkan: tekan 'Sudah saya alihkan' " +
		"di layar pengalihan dulu (menghapusnya melepas penjagaan baca-saja saat MySQL-nya hidup lagi)."
	// msgRebuildAccept: the rebuild form was posted without the
	// acknowledgement of the connections to the old master.
	msgRebuildAccept = "Centang pernyataan bahwa Anda paham koneksi ke master lama masih ada atau tidak bisa dipastikan, " +
		"dan tulisannya hilang, lalu kirim lagi."
	msgRebuildTarget = "Server ini bukan master lama yang dikarantina dari topologi master ini."

	msgSalvageName    = "Nama berkas data tertinggal tidak valid."
	msgSalvageOffline = "Agent server ini offline; berkas data tertinggal tidak bisa dibaca sekarang."
	// msgSalvageOldAgent: the agent's version is not the panel's.
	msgSalvageOldAgent = "Agent perlu dimutakhirkan: versinya berbeda dari panel, jadi daftar berkas data tertinggal " +
		"baru dibaca setelah agent server ini diperbarui ke versi panel."
	// msgConnsNotMember: the connections fragment of another server.
	msgConnsNotMember = "Koneksi hanya ditampilkan untuk master, master lama, atau replika topologi yang pernah dipromosikan."
	// Failed agent calls are worded by the panel: the agent's own text
	// (file paths, mysqlbinlog output) goes to the panel's log only.
	msgSalvageRead     = "Berkas data tertinggal tidak bisa dibaca; rinciannya ada di log panel."
	msgSalvageList     = "Daftar berkas data tertinggal tidak bisa dibaca; rinciannya ada di log panel."
	msgSalvageDeleteKO = "Berkas data tertinggal tidak bisa dihapus; rinciannya ada di log panel."
	// msgRepointFailed and msgRepointOffline are "Arahkan sekarang" that
	// did not happen (the error itself is for the panel's log).
	msgRepointFailed = "Replika tidak bisa diarahkan ke master baru sekarang; rinciannya ada di log panel. " +
		"Coba lagi, atau biarkan: replika diarahkan otomatis saat agent-nya tersambung lagi."
	msgRepointOffline = "Agent replika offline; replika diarahkan otomatis saat agent-nya tersambung lagi."

	// pageCallTimeout bounds an agent call made while rendering a page.
	pageCallTimeout = 5 * time.Second
	// connectionsEveryMS is how often the cutover screen reloads the
	// connections (spec 5.5: every 15 seconds).
	connectionsEveryMS = 15000
)

// Audit outcomes of an interrupted salvage download.
const (
	outcomeDownloadGone    = "unduhan dibatalkan browser"
	outcomeDownloadWrite   = "unduhan terputus"
	outcomeDownloadChanged = "berkas berubah saat diunduh"
	outcomeDownloadEmpty   = "agent tidak mengirim isi berkas"
)

// connectionsView is one side's table of the cutover screen
// (_connections.html).
type connectionsView struct {
	ServerID int64
	Name     string
	Rows     []proto.ConnectionRow
	Total    int
	// AdminTotal is how many of Total are sessions of the panel's admin
	// account (the one the agent logs in as) from other hosts.
	AdminTotal int
	TakenAt    time.Time
	// LocalAdminTotal is how many of Total are sessions of the agent's
	// account from the server itself that the agent cannot tell from its own.
	LocalAdminTotal int
	// Unreadable says why the connections could not be read ("" when
	// they were); Empty what to say when there are none.
	Unreadable string
	Empty      string
	// Uncertain is "" only for a complete list; otherwise the reason it is
	// "tidak bisa dipastikan" (an old agent, or threads the agent did not
	// record). Indistinct: the agent's own sessions cannot be told apart
	// from other sessions of its account.
	Uncertain  string
	Indistinct bool
	// clear: orchestrator.ConnectionsClear of the answer read.
	clear bool
}

// CompleteAndEmpty: the connections were read, the list is complete and
// there is none (orchestrator.ConnectionsClear, the one definition the
// form, its handler and the orchestrator share). The only state that may
// say "no connections".
func (v connectionsView) CompleteAndEmpty() bool {
	return v.clear && v.Unreadable == "" && v.Uncertain == ""
}

// acceptLabel is the acknowledgement the rebuild of the old master (and its
// retry) asks for with conns, its connections: "" only for a complete,
// empty list.
func acceptLabel(conns connectionsView) string {
	switch {
	case conns.CompleteAndEmpty():
		return ""
	case conns.NotKnown():
		return "Saya paham koneksi ke server ini tidak bisa dipastikan dan tulisan yang masih masuk hilang"
	}
	return fmt.Sprintf("Saya paham %d koneksi masih ada dan tulisannya hilang", conns.Total)
}

// NotKnown: the list cannot be trusted to be complete (it could not be
// read, or it is not complete).
func (v connectionsView) NotKnown() bool { return v.Unreadable != "" || v.Uncertain != "" }

// connectionsOf reads srv's client connections; the side follows its
// role (the quarantined old master or the new one). An unreachable agent
// is said, never an error page.
func (a *app) connectionsOf(ctx context.Context, srv store.Server) connectionsView {
	v := connectionsView{ServerID: srv.ID, Name: srv.Name, Empty: msgNewNoConns}
	unreachable := msgNewUnreachable
	if srv.Role == store.RoleQuarantined {
		v.Empty, unreachable = msgOldNoConns, msgOldUnreachable
	}
	if !a.Hub.IsOnline(srv.ID) {
		v.Unreadable = unreachable
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, pageCallTimeout)
	defer cancel()
	res, err := a.Orchestrator.Connections(ctx, srv.ID)
	switch {
	case errors.Is(err, proto.ErrOffline):
		v.Unreadable = unreachable
	case err != nil:
		a.Logger.Warn("read connections", "server", srv.ID, "err", logErr(err))
		v.Unreadable = agentFailure(err, msgConnsFailed)
	}
	if err != nil {
		return v
	}
	v.Rows, v.TakenAt, v.Indistinct = res.Rows, res.TakenAt, res.AgentIndistinct
	v.clear = orchestrator.ConnectionsClear(res)
	for _, row := range res.Rows {
		v.Total += row.Count
		if row.AdminAccount {
			v.AdminTotal += row.Count
		}
		if row.LocalAdmin {
			v.LocalAdminTotal += row.Count
		}
	}
	// The agent's own words, shown bounded (SP-11) and never matched on.
	switch {
	case res.Unreadable != "":
		v.Unreadable = monitor.AgentText(res.Unreadable, maxAgentWords)
	case !res.Typed:
		v.Uncertain = msgConnsOldAgent
	case res.Incomplete != "":
		v.Uncertain = monitor.AgentText(res.Incomplete, maxAgentWords)
	}
	return v
}

// connectionsFragment answers GET /servers/{id}/connections-fragment: the
// connections table of the server; the cutover screen reloads it every
// 15 seconds.
// Only a server the cutover screen shows is read (4b Task 10 Minor 4): a
// topology's master, an old master, or a replica of a topology that has
// had a promotion (a rebuilt old master); any other id is refused without
// an agent call.
func (a *app) connectionsFragment(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	member, err := a.cutoverMember(srv.ID)
	if err != nil {
		a.internalError(w, "connections member", err)
		return
	}
	if !member {
		http.Error(w, msgConnsNotMember, http.StatusNotFound)
		return
	}
	a.render.RenderFragment(w, "connections", a.connectionsOf(r.Context(), srv))
}

// cutoverMember reports whether serverID is a topology's master, the old
// master of a promotion, or a replica of a topology that has had one.
func (a *app) cutoverMember(serverID int64) (bool, error) {
	notFound := func(err error) bool { return errors.Is(err, store.ErrNotFound) }
	if _, err := a.Store.GetTopologyByMaster(serverID); err == nil {
		return true, nil
	} else if !notFound(err) {
		return false, err
	}
	if _, err := a.Store.PromotionByOldMaster(serverID); err == nil {
		return true, nil
	} else if !notFound(err) {
		return false, err
	}
	rep, err := a.Store.GetReplicaByServer(serverID)
	if notFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := a.Store.LatestPromotion(rep.TopologyID); err == nil {
		return true, nil
	} else if !notFound(err) {
		return false, err
	}
	return false, nil
}

// cutoverPage is the cutover screen of a new master (cutover.html).
type cutoverPage struct {
	Page
	Master   store.Server
	Topology store.Topology
	// Old is the old master (HasOld false when it was deleted).
	Old    store.Server
	HasOld bool
	// NewAddress is where the applications must write now; OldAddress
	// where they wrote before ("" when unknown).
	NewAddress, OldAddress string
	Promotion              *store.Promotion
	Planned                bool
	// WritePause is how long writes were stopped (planned only; "" when
	// unknown).
	WritePause        string
	QuarantinePending bool
	OldConns          connectionsView
	NewConns          connectionsView
	EveryMS           int
}

// cutoverPage shows GET /masters/{id}/cutover.
func (a *app) cutoverPage(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	a.renderCutover(w, r, http.StatusOK, srv, topo, "")
}

func (a *app) renderCutover(w http.ResponseWriter, r *http.Request, status int, srv store.Server, topo store.Topology, flash string) {
	p := cutoverPage{Page: a.page(r), Master: srv, Topology: topo, EveryMS: connectionsEveryMS,
		NewAddress: net.JoinHostPort(topo.SourceHost, strconv.Itoa(topo.SourcePort))}
	p.Flash = flash
	if topo.OldMasterServerID != 0 {
		old, err := a.Store.GetServer(topo.OldMasterServerID)
		switch {
		case err == nil:
			p.Old, p.HasOld = old, true
			p.OldConns = a.connectionsOf(r.Context(), old)
		case !errors.Is(err, store.ErrNotFound):
			a.internalError(w, "get old master", err)
			return
		}
	}
	p.NewConns = a.connectionsOf(r.Context(), srv)
	prom, err := a.Store.LatestPromotion(topo.ID)
	switch {
	case err == nil:
		p.Promotion = &prom
		p.Planned = prom.Mode == proto.PromotePlanned
		p.QuarantinePending = orchestrator.QuarantinePending(prom)
		if p.Planned && prom.LockAt != nil && prom.FinishedAt != nil {
			p.WritePause = fmtDuration(int64(prom.FinishedAt.Sub(*prom.LockAt).Seconds()))
		}
		p.OldAddress = a.oldAddress(prom, p.Old)
	case !errors.Is(err, store.ErrNotFound):
		a.internalError(w, "latest promotion", err)
		return
	}
	a.render.renderStatus(w, status, "cutover", p)
}

// oldAddress is the old master as the applications reached it: the
// address its replicas followed, from the promotion's job; else its
// panel address with its MySQL port.
func (a *app) oldAddress(prom store.Promotion, old store.Server) string {
	if prom.JobID != "" {
		if j, _, err := a.Store.GetJob(prom.JobID); err == nil {
			var st struct {
				Host string `json:"old_source_host"`
				Port int    `json:"old_source_port"`
			}
			if json.Unmarshal([]byte(j.ParamsJSON), &st) == nil && st.Host != "" && st.Port != 0 {
				return net.JoinHostPort(st.Host, strconv.Itoa(st.Port))
			}
		}
	}
	if old.Address == "" {
		return ""
	}
	port := 3306
	if pr := a.probe(old); pr != nil && pr.MySQL.Port != 0 {
		port = pr.MySQL.Port
	}
	return net.JoinHostPort(old.Address, strconv.Itoa(port))
}

// cutoverDone is "Sudah saya alihkan" (no token, audited, spec 5.7): the
// cutover is no longer pending. The orchestrator's warning (the old
// master was never quarantined) is shown on the master page.
func (a *app) cutoverDone(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	warning, err := a.Orchestrator.ConfirmCutover(topo.ID)
	a.audit(r, "cutover_confirmed", auditMaster, srv.ID,
		map[string]any{"topology_id": topo.ID, "old_master_server_id": topo.OldMasterServerID, "warning": warning != ""},
		outcomeOf(err))
	if err != nil {
		a.Logger.Warn("cutover confirmation refused", "master", srv.ID, "err", logErr(err))
		a.renderCutover(w, r, http.StatusConflict, srv, topo, err.Error())
		return
	}
	a.Logger.Info("cutover confirmed", "master", srv.ID, "topology", topo.ID, "by", auth.UsernameFrom(r.Context()))
	if warning == "" {
		http.Redirect(w, r, "/masters/"+strconv.FormatInt(srv.ID, 10)+"?pesan=dialihkan", http.StatusSeeOther)
		return
	}
	topo.CutoverPending = false
	p := masterPage{Page: a.page(r), Server: srv, Topology: topo}
	p.Notice, p.Flash = notices["dialihkan"], warning
	a.renderMaster(w, r, http.StatusOK, p)
}

// oldMasterBox is the "Master lama" box of a quarantined server's page.
type oldMasterBox struct {
	Promotion store.Promotion
	Planned   bool
	NewMaster store.Server
	Topology  store.Topology
	// HasTopology: the topology still names this server as its old
	// master.
	HasTopology       bool
	CutoverPending    bool
	QuarantinePending bool
	Status            statusView
	// Leftover is the latest check (nil: never run); LeftoverSummary its
	// sentence.
	Leftover *orchestrator.LeftoverCheck
	// StartLate: master_start_late is open (startLateOpen); Leftover
	// says nothing about the writes since.
	StartLate       bool
	LeftoverSummary string
	Online, Busy    bool
}

// salvageFiles is the list of salvage files of a server page.
type salvageFiles struct {
	ServerID int64
	Files    []salvageRow
	// Unreadable says why the list could not be read.
	Unreadable string
}

type salvageRow struct {
	proto.SalvageFile
	DaysLeft string
}

// oldMasterViews fills the old-master box and the salvage files of srv's
// page: the box for a quarantined server, the files for any server that
// was the old master of a promotion (rebuilt ones keep their files for 7
// days).
func (a *app) oldMasterViews(ctx context.Context, srv store.Server, p *serverDetailPage) error {
	prom, err := a.Store.PromotionByOldMaster(srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		// A quarantined server outside any promotion (a rebuild target
		// that has not yet finished) stays quarantined at a delete too.
		if srv.Role == store.RoleQuarantined {
			p.DeleteWarnings = deleteWarnings(srv, nil, nil)
		}
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case p.Online && a.Version != "" && srv.AgentVersion != a.Version:
		// An agent of another version may not know the salvage commands;
		// asking it on every view would raise AgentNeedsUpgrade again and
		// again (4b Task 10 Minor 5).
		p.Salvage = &salvageFiles{ServerID: srv.ID, Unreadable: msgSalvageOldAgent}
	case p.Online:
		sf := &salvageFiles{ServerID: srv.ID}
		lctx, cancel := context.WithTimeout(ctx, pageCallTimeout)
		files, err := a.Orchestrator.SalvageList(lctx, srv.ID)
		cancel()
		if err != nil {
			a.Logger.Warn("list salvage files", "server", srv.ID, "err", logErr(err))
			sf.Unreadable = agentFailure(err, msgSalvageList)
		}
		now := a.Now()
		for _, f := range files {
			days := int(f.ExpiresAt.Sub(now).Hours() / 24)
			sf.Files = append(sf.Files, salvageRow{SalvageFile: f, DaysLeft: fmt.Sprintf("%d hari", max(days, 0))})
		}
		p.Salvage = sf
	default:
		p.Salvage = &salvageFiles{ServerID: srv.ID, Unreadable: msgSalvageOffline}
	}
	if srv.Role != store.RoleQuarantined {
		if len(p.Salvage.Files) == 0 {
			p.Salvage = nil
		}
		p.DeleteWarnings = deleteWarnings(srv, nil, p.Salvage)
		return nil
	}
	box := &oldMasterBox{Promotion: prom, Planned: prom.Mode == proto.PromotePlanned, Online: p.Online,
		QuarantinePending: orchestrator.QuarantinePending(prom), Status: a.statusOf(srv.ID, srv.Role),
		Busy: p.ActiveJob != nil}
	if box.NewMaster, err = a.Store.GetServer(prom.NewMasterServerID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	topo, err := a.Store.GetTopologyByOldMaster(srv.ID)
	switch {
	case err == nil:
		box.Topology, box.HasTopology, box.CutoverPending = topo, true, topo.CutoverPending
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	if c, ok := orchestrator.LeftoverOf(prom); ok {
		box.Leftover = &c
		box.LeftoverSummary = orchestrator.LeftoverSummary(c.LeftoverResult)
	}
	box.StartLate = a.startLateOpen(srv.ID)
	p.OldMaster = box
	p.DeleteWarnings = deleteWarnings(srv, box, p.Salvage)
	switch {
	case box.CutoverPending:
		p.DeleteBlocked = msgDeleteCutover
	case box.QuarantinePending:
		p.DeleteUnquarantined = true
	}
	return nil
}

// deleteWarnings words what deleting srv from the panel loses: the panel can
// no longer check or salvage the old master's leftover data, and its salvage
// files can no longer be downloaded (the agent removes them after 7 days). A
// quarantined server also stays quarantined: the panel does not ask its
// agent to lift anything. box is nil for a server that is no longer a
// quarantined old master (or has no promotion record); sf for one without
// salvage files.
func deleteWarnings(srv store.Server, box *oldMasterBox, sf *salvageFiles) []string {
	var out []string
	if box != nil {
		switch {
		case box.StartLate:
			out = append(out, startLateWarning(srv)+": setelah server ini dihapus dari panel, tulisan itu tidak bisa "+
				"diperiksa atau diamankan lagi lewat panel.")
		case box.Leftover == nil:
			out = append(out, fmt.Sprintf("Data tertinggal di %s belum diperiksa: setelah server ini dihapus dari panel, "+
				"transaksi yang tidak sampai ke master baru tidak bisa diperiksa atau diamankan lagi lewat panel.", srv.Name))
		case box.Leftover.Transactions > 0:
			out = append(out, fmt.Sprintf("Pemeriksaan terakhir menemukan %d transaksi di %s yang tidak sampai ke master baru; "+
				"setelah server ini dihapus dari panel, data itu tidak bisa diamankan lagi lewat panel.",
				box.Leftover.Transactions, srv.Name))
		}
	}
	// The panel does not ask the agent to release a quarantined server's
	// role at the delete, so it stays quarantined whatever the agent is.
	if srv.Role == store.RoleQuarantined {
		out = append(out, "Server ini tetap dikarantina (quarantined) setelah dihapus dari panel: MySQL-nya tetap baca-saja "+
			"(read-only) dan kunci start (start lock) tetap terpasang. Untuk memakai server ini lagi, jalankan di server itu: "+
			"sudo dbpanel agent set-role unassigned, atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi.")
	}
	if sf == nil {
		return out
	}
	switch {
	case len(sf.Files) > 0:
		out = append(out, fmt.Sprintf("Ada %d berkas data tertinggal di %s yang tidak bisa diunduh lewat panel lagi setelah "+
			"server ini dihapus (agent menghapusnya sendiri setelah 7 hari); unduh dulu bila masih diperlukan.",
			len(sf.Files), srv.Name))
	case sf.Unreadable != "" && box != nil:
		out = append(out, fmt.Sprintf("Daftar berkas data tertinggal di %s tidak bisa dibaca sekarang; bila ada berkas, "+
			"berkas itu tidak bisa diunduh lewat panel lagi setelah server ini dihapus.", srv.Name))
	}
	return out
}

// agentFailure is what the page and the audit say about a failed agent
// call: the panel's own sentences (offline, an agent too old, a busy
// server) as they are, else the fixed generic. The error itself, which may
// carry the agent's text, is for the panel's log only.
func agentFailure(err error, generic string) string {
	return agentFailureAs(err, generic, msgSalvageOffline)
}

// agentFailureAs is agentFailure with offline, the sentence for an
// offline agent.
func agentFailureAs(err error, generic, offline string) string {
	switch {
	case errors.Is(err, proto.ErrOffline):
		return offline
	case errors.Is(err, orchestrator.ErrAgentOutdated), errors.Is(err, orchestrator.ErrBusy):
		return err.Error()
	}
	return generic
}

// oldMasterFromPath loads {id} and refuses (400 on its page) a server
// that is not a quarantined old master.
func (a *app) oldMasterFromPath(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return srv, false
	}
	if srv.Role != store.RoleQuarantined {
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msgNotOldMaster})
		return srv, false
	}
	return srv, true
}

// startLeftover is "Periksa data tertinggal" (no token: it only reads).
func (a *app) startLeftover(w http.ResponseWriter, r *http.Request) {
	a.startOldMasterJob(w, r, "leftover_check", a.Orchestrator.StartLeftover)
}

// startSalvage is "Amankan data tertinggal" (no token: it only writes a
// file the agent removes after 7 days).
func (a *app) startSalvage(w http.ResponseWriter, r *http.Request) {
	a.startOldMasterJob(w, r, "salvage", a.Orchestrator.StartSalvage)
}

func (a *app) startOldMasterJob(w http.ResponseWriter, r *http.Request, action string,
	start func(ctx context.Context, serverID int64) (string, error)) {
	srv, ok := a.oldMasterFromPath(w, r)
	if !ok {
		return
	}
	id, err := start(r.Context(), srv.ID)
	detail := map[string]any{}
	if err == nil {
		detail["job_id"] = id
	}
	a.audit(r, action, auditServer, srv.ID, detail, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("old master job refused", "action", action, "server", srv.ID, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	}
	a.Logger.Info("old master job started", "action", action, "server", srv.ID, "job", id, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// salvageDownload streams a salvage file to the browser a chunk at a time
// (nothing is kept on the panel), behind a confirmation token in the
// query issued for that file (spec 5.4). The name is checked against the
// agent's pattern before anything is sent to the agent; the transfer
// stops when the browser goes away.
func (a *app) salvageDownload(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !orchestrator.ValidSalvageName(name) {
		a.audit(r, "salvage_download", auditServer, srv.ID, nil, msgSalvageName)
		http.Error(w, msgSalvageName, http.StatusBadRequest)
		return
	}
	if !a.requireConfirmToken(w, r, r.URL.Query().Get("confirm_token"), salvageAction(ActSalvageDownload, name),
		auditServer, srv.ID) {
		return
	}
	detail := map[string]any{auditSalvageFile: name}
	ctx := r.Context()
	chunk, err := a.Orchestrator.SalvageRead(ctx, srv.ID, name, 0)
	if err != nil {
		msg := agentFailure(err, msgSalvageRead)
		a.audit(r, "salvage_download", auditServer, srv.ID, detail, msg)
		a.Logger.Warn("salvage download failed", "server", srv.ID, "file", name, "err", logErr(err))
		a.showError(w, r, srv, http.StatusBadGateway, msg)
		return
	}
	size := chunk.Size
	h := w.Header()
	h.Set("Content-Type", "application/sql")
	h.Set("Content-Disposition", "attachment; filename="+name)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	var sent int64
	outcome := auditOK
	for {
		if sent+int64(len(chunk.Data)) > size {
			outcome = outcomeDownloadChanged
			break
		}
		n, err := w.Write(chunk.Data)
		sent += int64(n)
		if err != nil {
			outcome = outcomeDownloadWrite
			break
		}
		if chunk.EOF {
			if sent != size {
				outcome = outcomeDownloadChanged
			}
			break
		}
		if ctx.Err() != nil {
			outcome = outcomeDownloadGone
			break
		}
		if len(chunk.Data) == 0 {
			outcome = outcomeDownloadEmpty
			break
		}
		if chunk, err = a.Orchestrator.SalvageRead(ctx, srv.ID, name, sent); err != nil {
			outcome = agentFailure(err, msgSalvageRead)
			if ctx.Err() != nil {
				outcome = outcomeDownloadGone
			}
			a.Logger.Warn("salvage download interrupted", "server", srv.ID, "file", name, "sent", sent, "err", logErr(err))
			break
		}
	}
	detail["size"], detail["sent"] = size, sent
	a.audit(r, "salvage_download", auditServer, srv.ID, detail, outcome)
	a.Logger.Info("salvage download", "server", srv.ID, "file", name, "size", size, "sent", sent, "outcome", outcome,
		"by", auth.UsernameFrom(r.Context()))
}

// salvageDelete deletes a salvage file (token for that file: it may be the
// only copy of the leftover data).
func (a *app) salvageDelete(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !orchestrator.ValidSalvageName(name) {
		a.audit(r, "salvage_delete", auditServer, srv.ID, nil, msgSalvageName)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msgSalvageName})
		return
	}
	if !a.requireConfirm(w, r, salvageAction(ActSalvageDelete, name), auditServer, srv.ID) {
		return
	}
	err := a.Orchestrator.SalvageDelete(r.Context(), srv.ID, name)
	msg := auditOK
	if err != nil {
		msg = agentFailure(err, msgSalvageDeleteKO)
	}
	a.audit(r, "salvage_delete", auditServer, srv.ID, map[string]any{auditSalvageFile: name}, msg)
	if err != nil {
		a.Logger.Warn("salvage delete failed", "server", srv.ID, "file", name, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msg})
		return
	}
	a.Logger.Info("salvage file deleted", "server", srv.ID, "file", name, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan=berkas-dihapus", http.StatusSeeOther)
}

// rebuildInfo is what the replica form shows when it rebuilds the
// quarantined old master as a replica of the new one (spec 5.4).
type rebuildInfo struct {
	Server         store.Server
	CutoverPending bool
	// Warnings are shown on the form and in the confirmation.
	Warnings    []string
	ConfirmText string
	// AcceptLabel is the label of the acknowledgement the form requires
	// ("" when the connection list is complete and empty and none is).
	AcceptLabel string
	// Form: see retryView.Form; always "" here.
	Form string
	// Root's approval of the rebuild on the server (spec 5.2), the box
	// "Persetujuan di server": Approval is the agent's answer, nil when it
	// could not be read (ApprovalErr says why, in the panel's words or,
	// for the agent's own refusal, in its words cut at maxAgentWords).
	// ApprovalOK (approved, or not required) enables the button.
	Approval    *proto.RebuildApprovalResult
	ApprovalErr string
	ApprovalOK  bool
	// InvalidReason is why an approval on file no longer holds, in the
	// panel's words (the agent's Invalid is never shown).
	InvalidReason string
	// ApprovalAgentMsg is the agent's own refusal, quoted as its words
	// (orchestrator.AgentQuote), shown in a block of its own.
	ApprovalAgentMsg string
}

// ApprovalMinutes is how many minutes the code to approve is still valid,
// rounded up, at least 1.
func (i *rebuildInfo) ApprovalMinutes() int64 {
	if i.Approval == nil {
		return 0
	}
	return max(1, (i.Approval.RemainingSec+59)/60)
}

// ApprovalHours and ApprovalHourMinutes are how long the approval still
// holds, in hours and the minutes after them.
func (i *rebuildInfo) ApprovalHours() int64 {
	if i.Approval == nil {
		return 0
	}
	return max(0, i.Approval.RemainingSec) / 3600
}

func (i *rebuildInfo) ApprovalHourMinutes() int64 {
	if i.Approval == nil {
		return 0
	}
	return max(0, i.Approval.RemainingSec) % 3600 / 60
}

// rebuildApproval fills info's approval from srv's agent; an offline
// agent is not asked.
func (a *app) rebuildApproval(ctx context.Context, info *rebuildInfo) {
	info.Approval, info.ApprovalErr, info.ApprovalOK = nil, "", false
	info.InvalidReason, info.ApprovalAgentMsg = "", ""
	srv := info.Server
	offline := fmt.Sprintf("Persetujuan di server tidak bisa diperiksa: agent %s offline.", srv.Name)
	if !srv.Paired || !a.Hub.IsOnline(srv.ID) {
		info.ApprovalErr = offline
		return
	}
	ctx, cancel := context.WithTimeout(ctx, pageCallTimeout)
	defer cancel()
	res, err := a.Orchestrator.RebuildApproval(ctx, srv.ID)
	if err != nil {
		a.Logger.Warn("read rebuild approval", "server", srv.ID, "err", logErr(err))
	}
	switch {
	case err == nil && !orchestrator.RebuildApprovalUsable(res):
		// The orchestrator checks this too; the page never shows a code
		// or source it cannot make sense of.
		info.ApprovalErr = msgApprovalUnreadable
	case err == nil:
		// The agent's words, shown bounded (SP-11).
		res.Code = monitor.AgentText(res.Code, maxAgentWords)
		res.Source = monitor.AgentText(res.Source, maxAgentWords)
		res.Addr = monitor.AgentText(res.Addr, maxAgentWords)
		res.RemainingSec = min(max(res.RemainingSec, 0), maxRemainingSec)
		info.InvalidReason = orchestrator.InvalidReason(res)
		res.Invalid, res.InvalidCode = "", ""
		info.Approval = &res
		info.ApprovalOK = res.Approved || !res.Required
	case errors.Is(err, proto.ErrOffline):
		info.ApprovalErr = offline
	case errors.Is(err, orchestrator.ErrAgentOutdated):
		info.ApprovalErr = fmt.Sprintf("Agent perlu dimutakhirkan: agent di %s belum mengenal persetujuan di server. Klik Pasang "+
			"ulang agent di halaman server itu, jalankan perintahnya, lalu klik Periksa lagi.", srv.Name)
	case errors.Is(err, &proto.Error{Code: proto.CodeBadParams}):
		// The agent's refusal says what to run on the server (set-source,
		// or why its pin files cannot be used): quoted as its words.
		info.ApprovalErr = fmt.Sprintf("Persetujuan di server tidak bisa diperiksa: agent %s menolak permintaannya.", srv.Name)
		info.ApprovalAgentMsg = orchestrator.AgentQuote(srv.Name, err.Error())
	default:
		info.ApprovalErr = msgApprovalUnreadable
	}
}

// maxRemainingSec bounds the agent's RemainingSec before the page counts
// minutes and hours from it (P-8).
const maxRemainingSec = 31 * 24 * 3600

const msgApprovalUnreadable = "Persetujuan di server tidak bisa diperiksa sekarang; klik Periksa lagi."

// localApprovalDetail adds to an audit detail the approval root holds on
// serverID now: "local_approval" (false when it cannot be read) and
// "local_approval_age_sec".
func (a *app) localApprovalDetail(ctx context.Context, serverID int64, detail map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, pageCallTimeout)
	defer cancel()
	res, err := a.Orchestrator.RebuildApproval(ctx, serverID)
	if err != nil {
		a.Logger.Warn("read rebuild approval for the audit", "server", serverID, "err", logErr(err))
		detail["local_approval"] = false
		return
	}
	detail["local_approval"] = res.Approved
	detail["local_approval_age_sec"] = res.AgeSec
}

// shownError is an orchestrator refusal as a page shows it: an agent's
// own text (a *proto.Error from server) quoted as its words and bounded
// (orchestrator.AgentQuote, SP-11), the panel's as it is.
func shownError(err error, server string) string {
	if _, ok := errors.AsType[*proto.Error](err); ok && !errors.Is(err, proto.ErrOffline) {
		return orchestrator.AgentQuote(server, err.Error())
	}
	return err.Error()
}

// maxAgentWords bounds an agent's sentence shown on a page (SP-11); a
// list of its file names is cut at maxAgentFiles.
const (
	maxAgentWords = 300
	maxAgentFiles = 10
)

// startLateOpen reports whether master_start_late is open for the server:
// it started writable after the last "Periksa data tertinggal" (the alert
// closes once a check after it is stored), so a stored result says
// nothing about those writes (SP-2). A store error counts as open.
func (a *app) startLateOpen(serverID int64) bool {
	_, err := a.Store.GetOpenAlert(monitor.KindMasterStartLate, serverID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.Logger.Warn("read start late alert", "server", serverID, "err", err)
		return true
	}
	return err == nil
}

// startLateWarning opens the warning of a late start on srv.
func startLateWarning(srv store.Server) string {
	return fmt.Sprintf("MySQL %s sempat bisa ditulis saat menyala setelah Periksa data tertinggal terakhir; tulisan itu belum diperiksa", srv.Name)
}

// rebuildTarget checks that id is the quarantined old master of master's
// topology and words what rebuilding it loses.
func (a *app) rebuildTarget(ctx context.Context, master store.Server, id int64) (*rebuildInfo, error) {
	srv, err := a.Store.GetServer(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if srv.Role != store.RoleQuarantined {
		return nil, nil
	}
	topo, err := a.Store.GetTopologyByOldMaster(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && topo.MasterServerID != master.ID) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := &rebuildInfo{Server: srv, CutoverPending: topo.CutoverPending}
	conns := a.connectionsOf(ctx, srv)
	switch {
	case conns.Unreadable != "":
		info.Warnings = append(info.Warnings, fmt.Sprintf("Koneksi aplikasi ke %s tidak bisa dibaca (%s); pastikan semua "+
			"aplikasi sudah memakai master baru.", srv.Name, strings.TrimSuffix(conns.Unreadable, ".")))
	default:
		if conns.Total > 0 {
			admin := ""
			if conns.AdminTotal > 0 {
				admin = fmt.Sprintf(" (%d di antaranya sesi akun admin panel dari host lain: orang atau aplikasi yang memakai akun itu)",
					conns.AdminTotal)
			}
			info.Warnings = append(info.Warnings, fmt.Sprintf("Masih ada %d koneksi aplikasi ke %s%s: aplikasi itu belum dialihkan "+
				"dan tulisannya ke server ini akan hilang.", conns.Total, srv.Name, admin))
		}
		if conns.Uncertain != "" {
			info.Warnings = append(info.Warnings, fmt.Sprintf("Daftar koneksi ke %s tidak bisa dipastikan lengkap (%s); pastikan semua "+
				"aplikasi sudah memakai master baru.", srv.Name, strings.TrimSuffix(conns.Uncertain, ".")))
		}
	}
	info.AcceptLabel = acceptLabel(conns)
	prom, err := a.Store.PromotionByOldMaster(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	check, checked := orchestrator.LeftoverOf(prom)
	switch {
	case a.startLateOpen(srv.ID):
		info.Warnings = append(info.Warnings, startLateWarning(srv)+": transaksi yang tidak sampai ke master baru akan "+
			"hilang dari server ini. Jalankan Periksa data tertinggal (dan Amankan bila ada) di halaman server ini dulu.")
	case !checked:
		info.Warnings = append(info.Warnings, "Data tertinggal belum diperiksa: transaksi yang tidak sampai ke master baru "+
			"akan hilang dari server ini. Jalankan Periksa data tertinggal (dan Amankan bila ada) di halaman server ini dulu bila ragu.")
	case check.Transactions > 0:
		info.Warnings = append(info.Warnings, fmt.Sprintf("Pemeriksaan terakhir menemukan %d transaksi yang tidak sampai ke "+
			"master baru; data itu akan hilang dari server ini. Berkas yang sudah diamankan tidak ikut dihapus.", check.Transactions))
	}
	a.rebuildApproval(ctx, info)
	info.ConfirmText = fmt.Sprintf("Bangun ulang %s sebagai replika %s: semua data MySQL di %s diganti salinan dari master baru.",
		srv.Name, master.Name, srv.Name)
	if len(info.Warnings) > 0 {
		info.ConfirmText += " " + strings.Join(info.Warnings, " ")
	}
	return info, nil
}

// repointReplica is "Arahkan sekarang" of a replica waiting to be pointed
// at the new master (no token, spec 5.7). A replica that holds
// transactions the new master lacks is information with the way out
// (Resync ulang), not a failure page.
func (a *app) repointReplica(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	err := a.Orchestrator.RepointNow(r.Context(), srv.ID)
	outcome := auditOK
	switch {
	case errors.Is(err, orchestrator.ErrNeedsRebuild):
		outcome = err.Error()
	case err != nil:
		outcome = agentFailureAs(err, msgRepointFailed, msgRepointOffline)
	}
	a.audit(r, "replica_repoint", auditReplica, srv.ID, nil, outcome)
	switch {
	case err == nil:
		a.Logger.Info("replica repointed", "server", srv.ID, "by", auth.UsernameFrom(r.Context()))
		http.Redirect(w, r, replicaURL(srv.ID)+"?pesan=diarahkan", http.StatusSeeOther)
	case errors.Is(err, orchestrator.ErrNeedsRebuild):
		if fresh, rerr := a.Store.GetReplicaByServer(srv.ID); rerr == nil {
			rep = fresh
		}
		p := replicaPage{Page: a.page(r), Server: srv, Replica: rep}
		p.RepointInfo = "Replika tidak diarahkan: " + err.Error() + "."
		a.renderReplica(w, r, http.StatusOK, p)
	default:
		a.Logger.Warn("repoint refused", "server", srv.ID, "err", logErr(err))
		a.replicaError(w, r, srv, rep, outcome)
	}
}
