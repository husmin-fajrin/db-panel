package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	msgNameMismatch = "Nama server tidak cocok"
	msgDetachMode   = "Pilih cara melepas replika."
	msgSnoozeHours  = "Pilih lama Jangan ganggu: 1, 2, 8, atau 24 jam."
	msgWipeList     = "Daftar database tidak sesuai dengan inventaris; segarkan daftar lalu coba lagi"
	msgWipeRefresh  = "Daftar database replika tidak bisa disegarkan, jadi dialog Resync ulang tidak dibuka: "
)

// snoozeHours are the Jangan ganggu choices; 0 cancels one.
var snoozeHours = []int{0, 1, 2, 8, 24}

// serverControls is the Periksa sekarang and Jangan ganggu block of the
// server, master and replica pages.
type serverControls struct {
	ServerID    int64
	SnoozeUntil *time.Time // nil when no snooze is active
}

func (a *app) controls(srv store.Server) serverControls {
	c := serverControls{ServerID: srv.ID}
	if srv.SnoozeUntil != nil && srv.SnoozeUntil.After(a.Now()) {
		c.SnoozeUntil = srv.SnoozeUntil
	}
	return c
}

// typedName reports whether the confirm_name field is exactly the server's
// name: the dangerous controls run only then.
func typedName(r *http.Request, srv store.Server) bool {
	return r.PostFormValue("confirm_name") == srv.Name
}

// homeURL is the page of srv for its role, where the controls return to.
func homeURL(srv store.Server) string {
	id := strconv.FormatInt(srv.ID, 10)
	switch srv.Role {
	case store.RoleMaster:
		return "/masters/" + id
	case store.RoleReplica:
		return "/replicas/" + id
	}
	return serverURL(srv.ID)
}

func replicaURL(id int64) string { return "/replicas/" + strconv.FormatInt(id, 10) }

// binlogKeep says how long a master keeps its binary logs ("7 hari"), or
// "" when that is unknown.
func binlogKeep(sec int64) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 24*3600:
		return fmt.Sprintf("%d jam", max(1, (sec+1800)/3600))
	}
	return fmt.Sprintf("%d hari", (sec+12*3600)/(24*3600))
}

func (a *app) stopReplica(w http.ResponseWriter, r *http.Request) {
	a.replicaControl(w, r, "replica_stop", "dihentikan", a.Orchestrator.StopReplica)
}

func (a *app) startReplica(w http.ResponseWriter, r *http.Request) {
	a.replicaControl(w, r, "replica_start", "dijalankan", a.Orchestrator.StartReplica)
}

func (a *app) relockReplica(w http.ResponseWriter, r *http.Request) {
	a.replicaControl(w, r, "replica_relock", "dikunci", a.Orchestrator.Relock)
}

// replicaControl runs do on the replica of {id} and returns to its page
// with notice; a refusal is shown there with 400.
func (a *app) replicaControl(w http.ResponseWriter, r *http.Request, action, notice string,
	do func(ctx context.Context, serverID int64) error) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	err := do(r.Context(), srv.ID)
	a.audit(r, action, auditReplica, srv.ID, nil, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("replica control refused", "action", action, "server", srv.ID, "err", err)
		a.replicaError(w, r, srv, rep, err.Error())
		return
	}
	a.Logger.Info("replica control", "action", action, "server", srv.ID, "by", a.page(r).Username)
	http.Redirect(w, r, replicaURL(srv.ID)+"?pesan="+notice, http.StatusSeeOther)
}

// prepareResync reads the replica's databases again (Resync ulang opens
// its dialog on the fresh list, spec 5.4) and returns to the replica page
// with the dialog open. A failed read is shown there and opens nothing.
func (a *app) prepareResync(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	_, err := a.Orchestrator.RefreshInventory(r.Context(), srv.ID)
	a.audit(r, "mysql_refresh", auditReplica, srv.ID, map[string]any{"reason": "resync"}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("refresh replica inventory for resync", "server", srv.ID, "err", err)
		a.replicaError(w, r, srv, rep, msgWipeRefresh+err.Error())
		return
	}
	http.Redirect(w, r, replicaURL(srv.ID)+"?buka=resync", http.StatusSeeOther)
}

// validWipeList reports whether every name of list is a database of the
// replica's stored inventory (the list its dialog showed), each at most once.
func (a *app) validWipeList(serverID int64, list []string) (bool, error) {
	inv, err := a.inventory(serverID)
	if err != nil {
		return false, err
	}
	seen := make(map[string]bool, len(list))
	for _, name := range list {
		known := slices.ContainsFunc(inv.Databases, func(d proto.DatabaseInfo) bool { return d.Name == name })
		if !known || seen[name] {
			return false, nil
		}
		seen[name] = true
	}
	return true, nil
}

func (a *app) resyncReplica(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	p := orchestrator.ResyncParams{ServerID: srv.ID, ForceDump: r.PostFormValue("force_dump") != "",
		WipeList: r.PostForm["wipe_list[]"]}
	detail := map[string]any{"force_dump": p.ForceDump, "wipe_list": p.WipeList}
	refused := ""
	if !typedName(r, srv) {
		refused = msgNameMismatch
	} else if ok, err := a.validWipeList(srv.ID, p.WipeList); err != nil {
		a.internalError(w, "get inventory", err)
		return
	} else if !ok {
		refused = msgWipeList
	}
	if refused != "" {
		a.audit(r, "replica_resync", auditReplica, srv.ID, detail, refused)
		a.replicaError(w, r, srv, rep, refused)
		return
	}
	id, err := a.Orchestrator.StartResync(r.Context(), p)
	if err == nil {
		detail["job_id"] = id
	}
	a.audit(r, "replica_resync", auditReplica, srv.ID, detail, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("resync refused", "server", srv.ID, "err", err)
		a.replicaError(w, r, srv, rep, err.Error())
		return
	}
	a.Logger.Info("resync started", "server", srv.ID, "job", id, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

func (a *app) detachReplica(w http.ResponseWriter, r *http.Request) {
	srv, rep, ok := a.replicaFromPath(w, r)
	if !ok {
		return
	}
	mode := r.PostFormValue("mode")
	refused := ""
	switch {
	case !typedName(r, srv):
		refused = msgNameMismatch
	case mode != "keep" && mode != "stop":
		refused = msgDetachMode
		mode = ""
	}
	detail := map[string]any{"mode": mode}
	if refused != "" {
		a.audit(r, "replica_detach", auditReplica, srv.ID, detail, refused)
		a.replicaError(w, r, srv, rep, refused)
		return
	}
	err := a.Orchestrator.DetachReplica(r.Context(), srv.ID, mode == "stop")
	a.audit(r, "replica_detach", auditReplica, srv.ID, detail, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("detach refused", "server", srv.ID, "err", err)
		a.replicaError(w, r, srv, rep, err.Error())
		return
	}
	a.Logger.Info("replica detached", "server", srv.ID, "mode", mode, "by", a.page(r).Username)
	notice := "dilepas"
	if mode == "stop" {
		notice = "dilepas-henti"
	}
	http.Redirect(w, r, serverURL(srv.ID)+"?pesan="+notice, http.StatusSeeOther)
}

func (a *app) unlockMaster(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	var err error
	if !typedName(r, srv) {
		err = errors.New(msgNameMismatch)
	} else {
		err = a.Orchestrator.UnlockMaster(r.Context(), srv.ID)
	}
	a.audit(r, "master_unlock", auditMaster, srv.ID, nil, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("master unlock refused", "server", srv.ID, "err", err)
		p := masterPage{Page: a.page(r), Server: srv, Topology: topo}
		p.Flash = err.Error()
		a.renderMaster(w, r, http.StatusBadRequest, p)
		return
	}
	a.Logger.Info("master unlocked", "server", srv.ID, "by", a.page(r).Username)
	http.Redirect(w, r, "/masters/"+strconv.FormatInt(srv.ID, 10)+"?pesan=kunci-dibuka", http.StatusSeeOther)
}

// checkServer asks the server's agent for a fresh health report now.
func (a *app) checkServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	err := a.Monitor.CheckNow(r.Context(), srv.ID)
	a.audit(r, "server_check", auditServer, srv.ID, nil, outcomeOf(err))
	if err != nil {
		a.Logger.Info("check now refused", "server", srv.ID, "err", err)
		a.controlError(w, r, srv, err.Error())
		return
	}
	http.Redirect(w, r, homeURL(srv)+"?pesan=diperiksa", http.StatusSeeOther)
}

// snoozeServer mutes the server's notifications for the chosen hours; 0
// unmutes them.
func (a *app) snoozeServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	hours, err := strconv.Atoi(r.PostFormValue("hours"))
	if err != nil || !slices.Contains(snoozeHours, hours) {
		a.audit(r, "server_snooze", auditServer, srv.ID, nil, msgSnoozeHours)
		a.controlError(w, r, srv, msgSnoozeHours)
		return
	}
	var until *time.Time
	if hours > 0 {
		t := a.Now().Add(time.Duration(hours) * time.Hour)
		until = &t
	}
	err = a.Monitor.Snooze(srv.ID, until)
	a.audit(r, "server_snooze", auditServer, srv.ID, map[string]any{"hours": hours, "until": until}, outcomeOf(err))
	if err != nil {
		a.internalError(w, "snooze", err)
		return
	}
	a.Logger.Info("snooze", "server", srv.ID, "hours", hours, "by", a.page(r).Username)
	notice := "senyap"
	if until == nil {
		notice = "senyap-batal"
	}
	http.Redirect(w, r, homeURL(srv)+"?pesan="+notice, http.StatusSeeOther)
}

// controlError shows msg with 400 on srv's page for its role (master,
// replica or plain server page).
func (a *app) controlError(w http.ResponseWriter, r *http.Request, srv store.Server, msg string) {
	switch srv.Role {
	case store.RoleMaster:
		if topo, err := a.Store.GetTopologyByMaster(srv.ID); err == nil {
			p := masterPage{Page: a.page(r), Server: srv, Topology: topo}
			p.Flash = msg
			a.renderMaster(w, r, http.StatusBadRequest, p)
			return
		}
	case store.RoleReplica:
		if rep, err := a.Store.GetReplicaByServer(srv.ID); err == nil {
			a.replicaError(w, r, srv, rep, msg)
			return
		}
	}
	a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msg})
}

// replicaError shows msg with 400 on the replica's page.
func (a *app) replicaError(w http.ResponseWriter, r *http.Request, srv store.Server, rep store.Replica, msg string) {
	p := replicaPage{Page: a.page(r), Server: srv, Replica: rep}
	p.Flash = msg
	a.renderReplica(w, r, http.StatusBadRequest, p)
}
