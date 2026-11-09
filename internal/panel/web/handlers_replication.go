package web

import (
	"net/http"
	"strconv"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// replicationPage is the Replikasi page (design spec 5.4): every topology
// with its master, its replicas and their status, and a link to its
// master's page.
type replicationPage struct {
	Page
	Topologies []replicationRow
}

// replicationRow is one topology of the Replikasi page.
type replicationRow struct {
	ID       int64
	Master   masterCard
	Replicas []replicaCard
}

// replication answers GET /replikasi: the list of topologies, or the
// master page of the only one.
func (a *app) replication(w http.ResponseWriter, r *http.Request) {
	topos, err := a.Store.ListTopologies()
	if err != nil {
		a.internalError(w, "list topologies", err)
		return
	}
	if len(topos) == 1 {
		http.Redirect(w, r, "/masters/"+strconv.FormatInt(topos[0].MasterServerID, 10), http.StatusSeeOther)
		return
	}
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	p := replicationPage{Page: a.page(r)}
	for _, t := range topos {
		master := byID[t.MasterServerID]
		row := replicationRow{ID: t.ID, Master: masterCard{Server: master, Status: a.statusOf(master.ID, master.Role)}}
		reps, err := a.Store.ListReplicas(t.ID)
		if err != nil {
			a.internalError(w, "list replicas", err)
			return
		}
		for _, rep := range reps {
			srv := byID[rep.ServerID]
			row.Replicas = append(row.Replicas, replicaCard{Server: srv, Status: a.statusOf(srv.ID, srv.Role)})
		}
		p.Topologies = append(p.Topologies, row)
	}
	a.render.Render(w, "replication", p)
}
