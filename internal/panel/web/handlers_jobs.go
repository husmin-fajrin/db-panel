package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
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
// label and badge class (.lencana in app.css; replica "failed" is the
// job's "failed"). web/src/app.css lists the classes in @source inline.
var statusBadges = map[string]struct{ Label, Class string }{
	store.ReplicaCreating:     {"Sedang dibuat", "lencana-primary"},
	store.ReplicaReady:        {"Siap", "lencana-success"},
	store.ReplicaChanging:     {"Sedang diubah", "lencana-primary"},
	store.ReplicaAttention:    {"Perlu perhatian", "lencana-warning"},
	store.JobQueued:           {"Antre", "lencana-neutral"},
	store.StepPending:         {"Antre", "lencana-neutral"},
	store.JobRunning:          {"Berjalan", "lencana-primary"},
	store.JobWaiting:          {"Menunggu persetujuan", "lencana-warning"},
	store.JobSucceeded:        {"Selesai", "lencana-success"},
	store.StepDone:            {"Selesai", "lencana-success"},
	store.JobFailed:           {"Gagal", "lencana-error"},
	store.JobPartial:          {"Selesai sebagian", "lencana-warning"},
	store.ReplicaNeedsRepoint: {"Perlu diarahkan ulang", "lencana-warning"},
	store.ReplicaNeedsRebuild: {"Perlu dibangun ulang", "lencana-error"},
	store.JobCanceled:         {"Dibatalkan", "lencana-neutral"},
	store.StepSkipped:         {"Dilewati", "lencana-neutral"},
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
	return "lencana-neutral"
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
	case orchestrator.KindResyncReplica:
		return "Resync ulang"
	case orchestrator.KindMySQLTLS:
		return "Pasang sertifikat MySQL"
	case orchestrator.KindTightenTLS:
		return "Perketat sambungan replikasi"
	case orchestrator.KindPromotePlanned:
		return "Promote terencana"
	case orchestrator.KindPromoteEmergency:
		return "Promote darurat"
	case orchestrator.KindLeftover:
		return "Periksa data tertinggal"
	case orchestrator.KindSalvage:
		return "Amankan data tertinggal"
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
	LogMax    int  // jobLogMaxLines: app.js replays missed lines up to it
	// create_replica, add_databases and remove_databases.
	Master *store.Server
	// create_replica only.
	Databases []string // empty = all
	// add_databases and remove_databases only.
	Changed                         []string // added or removed
	DropData                        bool     // removed ones are dropped on the replica
	CanApprove, CanRetry, CanCancel bool
	// Approval labels the waiting step's buttons; Superseded, when set,
	// says why the job may no longer be approved or retried.
	Approval   approval
	Superseded string
	StatusJSON string
	// DropsData: approving or retrying the job drops data, behind a
	// job_approve or job_retry token (spec 5.6). "Ulangi" then goes
	// through /retry/prepare and opens the retry dialog (OpenRetry, after
	// ?buka=ulangi), filled with Retry.
	DropsData bool
	OpenRetry bool
	Retry     *retryView
	// TypeDatabases: the databases an add_databases waiting for
	// WaitAddExisting drops once approved; the approve dialog asks for
	// each name typed (confirm_db[]), which the orchestrator compares.
	TypeDatabases []string
	// WipeNames: the databases a WaitWipe approval drops, whole (the step
	// message is cut at 4 KiB; SP-5).
	WipeNames []string
	// ApproveConns: the approval of the old master's rebuild reads its
	// connections again (SP-1); the acknowledgement it needs, tied to the
	// approve form.
	ApproveConns *retryView
}

// retryView is what the retry dialog of a job that drops data shows: the
// job's wipe list grouped on the master's inventory (as Resync ulang shows
// it), and the acknowledgement of the old master's connections a rebuild
// needs ("" when its list is complete and empty).
type retryView struct {
	Server      store.Server
	Wipe        wipeGroups
	AcceptLabel string
	// Form is the id of the form the acknowledgement belongs to when it
	// is not inside it ("" for the retry dialog).
	Form string
}

// jobParams are the fields of a job's params the job page reads.
type jobParams struct {
	MasterServerID   int64    `json:"master_server_id"`
	Databases        []string `json:"databases"`
	Change           []string `json:"change_databases"`
	DropData         bool     `json:"drop_data"`
	WaitReason       string   `json:"wait_reason"`
	WipeList         []string `json:"wipe_list"`
	RebuildOldMaster bool     `json:"rebuild_old_master"`
	AddExisting      []string `json:"add_existing"`
	WaitList         []string `json:"wait_list"`
}

// paramsOf reads job's params; unreadable params read as none.
func paramsOf(job store.Job) jobParams {
	var p jobParams
	_ = json.Unmarshal([]byte(job.ParamsJSON), &p)
	return p
}

// approval is what the job page offers for the step that waits.
type approval struct {
	Label  string // the approve button
	Danger bool   // approving destroys or interrupts something: confirm in a dialog first
	Impact string // the dialog's plain-language summary
	// Recheck labels a second button that re-checks the step (Retry)
	// instead of approving it; "" when there is none.
	Recheck string
	// ServerID: the reconnect wait links to this server's page.
	ServerID int64
}

// approvalFor labels the approval of a job waiting for reason (the
// orchestrator's jobState.WaitReason) on server.
func approvalFor(reason string, server store.Server) approval {
	switch reason {
	case orchestrator.WaitRestart:
		return approval{Label: "Restart MySQL di master sekarang", Danger: true,
			Impact: "MySQL di " + server.Name + " dimulai ulang sekarang. Aplikasi yang memakai database ini terputus " +
				"beberapa detik dan harus tersambung lagi. Lakukan di jam sepi.\n" +
				"Bila Anda lebih suka merestart sendiri nanti, tutup dialog ini, restart MySQL di server itu, lalu klik " +
				"Sudah saya restart — periksa lagi.",
			Recheck: "Sudah saya restart — periksa lagi"}
	case orchestrator.WaitWipe:
		return approval{Label: "Ya, hapus dan salin ulang", Danger: true,
			Impact: "Database yang disebut di atas DIHAPUS dari " + server.Name + " lalu disalin ulang dari master. " +
				"Isi yang hanya ada di server itu hilang dan tidak bisa dikembalikan dari panel."}
	case orchestrator.WaitForeignSource:
		return approval{Label: "Ya, hapus pengaturan replikasi lama", Danger: true,
			Impact: server.Name + " berhenti mengikuti server sumbernya yang sekarang; pengaturan replikasi itu dihapus " +
				"dan diganti replikasi dari master ini."}
	case orchestrator.WaitClone:
		return approval{Label: "Ya, CLONE menghapus semua database", Danger: true,
			Impact: "SEMUA database dan akun MySQL di " + server.Name + " dihapus dan diganti salinan lengkap master. " +
				"Tidak ada yang tersisa dari isi lama server itu."}
	case orchestrator.WaitReconnect:
		return approval{Label: "Sudah saya hubungkan ulang — lanjutkan", ServerID: server.ID}
	case orchestrator.WaitAddExisting:
		// The dialog asks for each database typed (jobPage.TypeDatabases).
		return approval{Label: "Ya, buang dan salin dari master", Danger: true}
	}
	return approval{Label: "Setujui & lanjutkan"}
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
	params := paramsOf(job)
	switch job.Status {
	case store.JobWaiting:
		p.CanApprove, p.CanCancel = true, true
		p.Approval = approvalFor(params.WaitReason, srv)
		if params.WaitReason == orchestrator.WaitAddExisting {
			p.TypeDatabases = params.AddExisting
		}
		if params.WaitReason == orchestrator.WaitWipe {
			p.WipeNames = params.WaitList
		}
	case store.JobFailed, store.JobPartial:
		p.CanRetry = true
	case store.JobQueued, store.JobRunning:
		p.CanCancel = true
	}
	// A job replaced by a newer one, or whose replica changed since, may not
	// run again (the orchestrator refuses it too).
	if p.CanApprove || p.CanRetry {
		if p.Superseded = a.Orchestrator.Superseded(job.ID); p.Superseded != "" {
			p.CanApprove, p.CanRetry = false, false
		}
	}
	if p.CanApprove || p.CanRetry {
		drops, err := a.Orchestrator.DropsData(job.ID)
		if err != nil {
			// The orchestrator refuses it unconfirmed anyway: ask for the token.
			a.Logger.Warn("drops data check", "job", job.ID, "err", logErr(err))
		}
		p.DropsData = drops || err != nil
	}
	if p.CanApprove && p.DropsData && params.RebuildOldMaster && job.Kind == orchestrator.KindCreateReplica {
		p.ApproveConns = &retryView{Server: srv, AcceptLabel: acceptLabel(a.connectionsOf(r.Context(), srv)), Form: "form-setujui"}
	}
	if p.CanRetry && p.DropsData && r.URL.Query().Get("buka") == "ulangi" {
		p.OpenRetry = true
		v, err := a.retryView(r, srv, params)
		if err != nil {
			a.internalError(w, "retry dialog", err)
			return
		}
		p.Retry = v
	}
	switch job.Kind {
	case orchestrator.KindCreateReplica, orchestrator.KindAddDatabases, orchestrator.KindRemoveDatabases:
		if job.Kind == orchestrator.KindCreateReplica {
			p.Databases = params.Databases
		}
		p.Changed, p.DropData = params.Change, params.DropData
		if m, err := a.Store.GetServer(params.MasterServerID); err == nil {
			p.Master = &m
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
	p.LogMax = jobLogMaxLines
	a.render.renderStatus(w, status, "job_detail", p)
}

// jobLogLine is a job.log line as GET /jobs/{id}/log answers it, the
// fields of the SSE job_log event.
type jobLogLine struct {
	ID      int64     `json:"id"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// jobLog answers the job's log lines with an id above ?after= as a JSON
// array, oldest first and at most 500: the job page fetches the lines an
// SSE reconnect missed.
func (a *app) jobLog(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		http.Error(w, "parameter after tidak valid", http.StatusBadRequest)
		return
	}
	job, _, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	lines, err := a.Store.JobLog(job.ID, after, 500)
	if err != nil {
		a.internalError(w, "job log", err)
		return
	}
	out := make([]jobLogLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, jobLogLine{ID: l.ID, TS: l.TS, Level: l.Level, Message: l.Message})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
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
	a.confirmedJobAction(w, r, "job_approve", a.Orchestrator.Approve)
}

func (a *app) retryJob(w http.ResponseWriter, r *http.Request) {
	a.confirmedJobAction(w, r, "job_retry", a.Orchestrator.Retry)
}

func (a *app) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, _, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	a.jobAction(w, r, job, "job_cancel", a.Orchestrator.Cancel)
}

const msgRetryRefresh = "Daftar database tidak bisa disegarkan, jadi dialog Ulangi tidak dibuka: "

// confirmedJobAction is Setujui (name "job_approve", ActJobApprove) or
// Ulangi ("job_retry", ActJobRetry) of the job of {id}; name is also the
// audit action. A job that drops data (spec 5.6) needs a fresh token for
// name on that job and its server, then, for a retry, the
// acknowledgements of its dialog: every database of the job's wipe list
// that the master lacks ticked in wipe_only[] (only names of that list),
// and accept_connections for a rebuild whose connections remain (the
// orchestrator reads them again); for an approval, the database names
// typed in confirm_db[] (the orchestrator compares them) and, for a
// rebuild, accept_connections as for a retry. A refusal leaves the job as it was: 403
// for the token, 400 for an acknowledgement, on the job page.
func (a *app) confirmedJobAction(w http.ResponseWriter, r *http.Request, name string, do func(string, orchestrator.JobAck) error) {
	job, _, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	drops, err := a.Orchestrator.DropsData(job.ID)
	if err != nil {
		a.internalError(w, "drops data check", err)
		return
	}
	var ack orchestrator.JobAck
	if drops {
		if !a.requireConfirm(w, r, jobConfirmAction(name, job.ID), auditJob, job.ServerID) {
			return
		}
		ack.Confirmed = true
		if name == ActJobApprove {
			ack.TypedDatabases = r.PostForm["confirm_db[]"]
			ack.AcceptConnections = paramsOf(job).RebuildOldMaster && r.PostFormValue("accept_connections") == "1"
		}
		if name == ActJobRetry {
			params := paramsOf(job)
			ack.WipeOnly = r.PostForm["wipe_only[]"]
			ack.AcceptConnections = params.RebuildOldMaster && r.PostFormValue("accept_connections") == "1"
			refused, err := a.retryAckRefused(params, ack.WipeOnly)
			if err != nil {
				a.internalError(w, "get inventory", err)
				return
			}
			if refused != "" {
				a.audit(r, name, auditJob, job.ServerID, map[string]any{"job_id": job.ID, "kind": job.Kind,
					"wipe_only": ack.WipeOnly}, refused)
				a.Logger.Warn("job action refused", "action", name, "job", job.ID, "err", refused)
				a.renderJob(w, r, http.StatusBadRequest, refused)
				return
			}
		}
	}
	a.jobAction(w, r, job, name, func(id string) error { return do(id, ack) })
}

// retryAckRefused checks the wipe_only[] of a retry against the job's wipe
// list and the master's stored inventory (which /retry/prepare refreshed
// for the dialog): each name once and on the list, and every listed name
// the master lacks among them. The orchestrator checks the list against a
// fresh read of the master again before anything is dropped.
func (a *app) retryAckRefused(params jobParams, only []string) (string, error) {
	seen := make(map[string]bool, len(only))
	for _, d := range only {
		if seen[d] || !slices.Contains(params.WipeList, d) {
			return msgWipeList, nil
		}
		seen[d] = true
	}
	if len(params.WipeList) == 0 {
		return "", nil
	}
	g, err := a.retryWipeGroups(params)
	if err != nil {
		return "", err
	}
	for _, d := range g.ReplicaOnly {
		if !seen[d] {
			return msgWipeList, nil
		}
	}
	return "", nil
}

// retryWipeGroups groups the job's wipe list on the master's stored
// inventory, as the Resync dialog groups the replica's databases.
func (a *app) retryWipeGroups(params jobParams) (wipeGroups, error) {
	masterInv, err := a.inventory(params.MasterServerID)
	if err != nil {
		return wipeGroups{}, err
	}
	list := make([]proto.DatabaseInfo, 0, len(params.WipeList))
	for _, d := range params.WipeList {
		list = append(list, proto.DatabaseInfo{Name: d})
	}
	return groupWipe(list, masterInv.Databases), nil
}

// retryView fills the retry dialog of a job on srv with params.
func (a *app) retryView(r *http.Request, srv store.Server, params jobParams) (*retryView, error) {
	v := &retryView{Server: srv}
	if len(params.WipeList) > 0 {
		g, err := a.retryWipeGroups(params)
		if err != nil {
			return nil, err
		}
		v.Wipe = g
	}
	if params.RebuildOldMaster {
		v.AcceptLabel = acceptLabel(a.connectionsOf(r.Context(), srv))
	}
	return v, nil
}

// prepareRetry reads the databases of the job's server and of its master
// again when the job drops a list of databases (the retry dialog groups
// them on fresh lists, spec 5.6) and returns to the job page with the
// retry dialog open. A failed read is shown there and opens nothing.
func (a *app) prepareRetry(w http.ResponseWriter, r *http.Request) {
	job, _, ok := a.jobFromPath(w, r)
	if !ok {
		return
	}
	if params := paramsOf(job); len(params.WipeList) > 0 {
		_, err := a.Orchestrator.RefreshInventory(r.Context(), job.ServerID)
		if err == nil {
			_, err = a.Orchestrator.RefreshInventory(r.Context(), params.MasterServerID)
		}
		a.audit(r, "mysql_refresh", auditJob, job.ServerID, map[string]any{"reason": "retry", "job_id": job.ID}, outcomeOf(err))
		if err != nil {
			a.Logger.Warn("refresh inventories for retry", "job", job.ID, "err", logErr(err))
			a.renderJob(w, r, http.StatusBadRequest, msgRetryRefresh+err.Error())
			return
		}
	}
	http.Redirect(w, r, jobURL(job.ID)+"?buka=ulangi", http.StatusSeeOther)
}

// jobAction runs do on job and returns to its page; a refusal is shown
// there: 400 for connections the rebuild's retry must acknowledge, 403 for
// a job that needs a confirmation, 409 otherwise (the job is not in a state
// that allows it). name is the audit action; the entry's target is the
// job's server.
func (a *app) jobAction(w http.ResponseWriter, r *http.Request, job store.Job, name string, do func(jobID string) error) {
	err := do(job.ID)
	a.audit(r, name, auditJob, job.ServerID, map[string]any{"job_id": job.ID, "kind": job.Kind}, outcomeOf(err))
	if err != nil {
		a.Logger.Warn("job action refused", "action", name, "job", job.ID, "err", logErr(err))
		status := http.StatusConflict
		switch {
		case errors.Is(err, orchestrator.ErrConnectionsRemain), errors.Is(err, orchestrator.ErrAddExistingNames):
			status = http.StatusBadRequest
		case errors.Is(err, orchestrator.ErrJobNeedsConfirm):
			status = http.StatusForbidden
		}
		a.renderJob(w, r, status, err.Error())
		return
	}
	a.Logger.Info("job action", "action", name, "job", job.ID, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(job.ID), http.StatusSeeOther)
}

func jobURL(id string) string { return "/jobs/" + id }
