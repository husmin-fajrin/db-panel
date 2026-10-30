package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	msgJobNotFound = "Pekerjaan tidak ditemukan"

	jobsListLimit   = 100
	jobHistoryLimit = 10
	// jobLogMaxLines caps the log rendered on a job page; the oldest lines
	// are shown, later ones arrive live.
	jobLogMaxLines = 5000
)

// jobIDPattern is the form of the ids the orchestrator creates (16 random
// bytes in hex).
var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// statusBadges maps job, step and replica states to their Indonesian
// label and daisyUI badge class (replica "failed" is the job's "failed").
// web/src/app.css lists the classes for Tailwind.
var statusBadges = map[string]struct{ Label, Class string }{
	store.ReplicaCreating: {"Sedang dibuat", "badge-info"},
	store.ReplicaReady:    {"Siap", "badge-success"},
	store.ReplicaChanging: {"Sedang diubah", "badge-info"},
	store.JobQueued:       {"Antre", "badge-ghost"},
	store.StepPending:     {"Antre", "badge-ghost"},
	store.JobRunning:      {"Berjalan", "badge-info"},
	store.JobWaiting:      {"Menunggu persetujuan", "badge-warning"},
	store.JobSucceeded:    {"Selesai", "badge-success"},
	store.StepDone:        {"Selesai", "badge-success"},
	store.JobFailed:       {"Gagal", "badge-error"},
	store.JobCanceled:     {"Dibatalkan", "badge-neutral"},
	store.StepSkipped:     {"Dilewati", "badge-ghost"},
}

// statusBadgesJSON is statusBadges for app.js, which updates badges live.
var statusBadgesJSON = func() string {
	b, err := json.Marshal(statusBadges)
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// statusLabel is the Indonesian label of a job, step or replica status; an
// unknown status is shown as is.
func statusLabel(status string) string {
	if b, ok := statusBadges[status]; ok {
		return b.Label
	}
	return status
}

func statusClass(status string) string {
	if b, ok := statusBadges[status]; ok {
		return b.Class
	}
	return "badge-ghost"
}

// kindLabel names a job kind for the UI.
func kindLabel(kind string) string {
	switch kind {
	case orchestrator.KindPrepareMaster:
		return "Siapkan master"
	case orchestrator.KindCreateReplica:
		return "Buat replika"
	case orchestrator.KindAddDatabases:
		return "Tambah database"
	case orchestrator.KindRemoveDatabases:
		return "Lepas database"
	}
	return kind
}

// jobRow is a job with the name of its server.
type jobRow struct {
	store.Job
	ServerName string
}

type jobsPage struct {
	Page
	Jobs []jobRow
}

type jobPage struct {
	Page
	Job       store.Job
	Server    store.Server
	Steps     []store.JobStep
	Waiting   *store.JobStep // the step waiting for the user, if any
	Log       []store.JobLogLine
	LastLogID int64
	LogCut    bool // more than jobLogMaxLines lines
	// create_replica, add_databases and remove_databases.
	Master *store.Server
	// create_replica only.
	Databases []string // empty = all
	// add_databases and remove_databases only.
	Changed                         []string // added or removed
	DropData                        bool     // removed ones are dropped on the replica
	CanApprove, CanRetry, CanCancel bool
	StatusJSON                      string
}

func (a *app) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.Store.ListJobs(jobsListLimit)
	if err != nil {
		a.internalError(w, "list jobs", err)
		return
	}
	rows, err := a.jobRows(jobs)
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	a.render.Render(w, "jobs", jobsPage{Page: a.page(r), Jobs: rows})
}

// jobRows adds the server names to jobs.
func (a *app) jobRows(jobs []store.Job) ([]jobRow, error) {
	names, err := a.serverNames()
	if err != nil {
		return nil, err
	}
	rows := make([]jobRow, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, jobRow{Job: j, ServerName: names[j.ServerID]})
	}
	return rows, nil
}

func (a *app) serverNames() (map[int64]string, error) {
	servers, err := a.Store.ListServers()
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(servers))
	for _, s := range servers {
		names[s.ID] = s.Name
	}
	return names, nil
}

func (a *app) jobDetail(w http.ResponseWriter, r *http.Request) {
	a.renderJob(w, r, http.StatusOK, "")
}

// renderJob renders the job page for {id} with status and flash.
func (a *app) renderJob(w http.ResponseWriter, r *http.Request, status int, flash string) {
	job, steps, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	p := jobPage{Page: a.page(r), Job: job, Steps: steps, StatusJSON: statusBadgesJSON}
	p.Flash = flash
	srv, err := a.Store.GetServer(job.ServerID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, "get server", err)
		return
	}
	p.Server = srv
	for i := range steps {
		if steps[i].Status == store.StepWaiting {
			p.Waiting = &steps[i]
		}
	}
	switch job.Status {
	case store.JobWaiting:
		p.CanApprove, p.CanCancel = true, true
	case store.JobFailed:
		p.CanRetry = true
	case store.JobQueued, store.JobRunning:
		p.CanCancel = true
	}
	switch job.Kind {
	case orchestrator.KindCreateReplica, orchestrator.KindAddDatabases, orchestrator.KindRemoveDatabases:
		var params struct {
			MasterServerID int64    `json:"master_server_id"`
			Databases      []string `json:"databases"`
			Change         []string `json:"change_databases"`
			DropData       bool     `json:"drop_data"`
		}
		if err := json.Unmarshal([]byte(job.ParamsJSON), &params); err == nil {
			if job.Kind == orchestrator.KindCreateReplica {
				p.Databases = params.Databases
			}
			p.Changed, p.DropData = params.Change, params.DropData
			if m, err := a.Store.GetServer(params.MasterServerID); err == nil {
				p.Master = &m
			}
		}
	}
	for len(p.Log) < jobLogMaxLines {
		lines, err := a.Store.JobLog(job.ID, p.LastLogID, 0)
		if err != nil {
			a.internalError(w, "job log", err)
			return
		}
		if len(lines) == 0 {
			break
		}
		p.Log = append(p.Log, lines...)
		p.LastLogID = lines[len(lines)-1].ID
	}
	p.LogCut = len(p.Log) >= jobLogMaxLines
	a.render.renderStatus(w, status, "job_detail", p)
}

// jobFromPath loads the job named by {id}, answering 404 (or 500) and
// returning false when it cannot.
func (a *app) jobFromPath(w http.ResponseWriter, r *http.Request) (store.Job, []store.JobStep, bool) {
	id := r.PathValue("id")
	if !jobIDPattern.MatchString(id) {
		http.Error(w, msgJobNotFound, http.StatusNotFound)
		return store.Job{}, nil, false
	}
	job, steps, err := a.Store.GetJob(id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgJobNotFound, http.StatusNotFound)
		return store.Job{}, nil, false
	}
	if err != nil {
		a.internalError(w, "get job", err)
		return store.Job{}, nil, false
	}
	return job, steps, true
}

func (a *app) approveJob(w http.ResponseWriter, r *http.Request) {
	a.jobAction(w, r, "approve", a.Orchestrator.Approve)
}

func (a *app) retryJob(w http.ResponseWriter, r *http.Request) {
	a.jobAction(w, r, "retry", a.Orchestrator.Retry)
}

func (a *app) cancelJob(w http.ResponseWriter, r *http.Request) {
	a.jobAction(w, r, "cancel", a.Orchestrator.Cancel)
}

// jobAction runs do on the job of {id} and returns to its page; a refusal
// (the job is not in a state that allows it) is shown there with 409.
func (a *app) jobAction(w http.ResponseWriter, r *http.Request, name string, do func(jobID string) error) {
	job, _, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	if err := do(job.ID); err != nil {
		a.Logger.Warn("job action refused", "action", name, "job", job.ID, "err", err)
		a.renderJob(w, r, http.StatusConflict, err.Error())
		return
	}
	a.Logger.Info("job action", "action", name, "job", job.ID, "by", a.page(r).Username)
	http.Redirect(w, r, jobURL(job.ID), http.StatusSeeOther)
}

func jobURL(id string) string { return "/jobs/" + id }
