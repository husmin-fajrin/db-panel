package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The quarantined old master after a promotion (spec 5.4): "Periksa data
// tertinggal" (mysql.leftover) and "Amankan data tertinggal"
// (mysql.salvage) run as one-step jobs, so the web request returns at once
// and the job page shows progress; the salvage files are listed, read in
// chunks and deleted with direct calls; mysql.connections feeds the
// cutover screen (spec 5.5).

const (
	stepLeftover = "Periksa data tertinggal"
	stepSalvage  = "Amankan data tertinggal"

	// oldMasterJobTimeout bounds mysql.leftover and mysql.salvage (one
	// mysqlbinlog run each over the old master's binary log).
	oldMasterJobTimeout = 30 * time.Minute
	// directReadTimeout bounds mysql.connections and one salvage chunk.
	directReadTimeout = 30 * time.Second
	// maxTelegramText keeps a message well inside Telegram's 4096
	// characters.
	maxTelegramText = 3500
	// maxSummaryTables is how many tables a leftover summary names.
	maxSummaryTables = 10
)

// salvageNameRe is the agent's pattern of a salvage file name
// (internal/agent/drivers/mysql/salvage.go): <UTC time>-<server id>.sql.
var salvageNameRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9]+\.sql$`)

// ValidSalvageName reports whether name can be a salvage file's name. A
// name that is not is never sent to an agent.
func ValidSalvageName(name string) bool { return salvageNameRe.MatchString(name) }

// errBadSalvageName refuses a file name outside the pattern.
var errBadSalvageName = errors.New("nama berkas data tertinggal tidak valid")

// LeftoverCheck is the latest "Periksa data tertinggal" of a promotion's
// old master, kept in promotions.leftover_json.
type LeftoverCheck struct {
	proto.LeftoverResult
	CheckedAt time.Time `json:"checked_at"`
	JobID     string    `json:"job_id,omitempty"`
}

// LeftoverOf is the latest leftover check stored on p; false when none
// ran (or it cannot be read).
func LeftoverOf(p store.Promotion) (LeftoverCheck, bool) {
	var c LeftoverCheck
	if p.LeftoverJSON == "" || json.Unmarshal([]byte(p.LeftoverJSON), &c) != nil || c.CheckedAt.IsZero() {
		return LeftoverCheck{}, false
	}
	return c, true
}

// LeftoverSummary words a leftover result for people: "Tidak ada data
// tertinggal", or the transactions, the tables they touched with their
// rows and when they were committed.
func LeftoverSummary(res proto.LeftoverResult) string {
	if res.Transactions == 0 {
		return "Tidak ada data tertinggal"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ada %d transaksi yang tidak sampai ke master baru", res.Transactions)
	if len(res.Tables) > 0 {
		var names []string
		for i, t := range res.Tables {
			if i == maxSummaryTables {
				names = append(names, fmt.Sprintf("dan %d tabel lain", len(res.Tables)-maxSummaryTables))
				break
			}
			names = append(names, fmt.Sprintf("%s.%s (%d baris)", t.Schema, t.Table, t.Rows))
		}
		b.WriteString(", menyentuh " + strings.Join(names, ", "))
	}
	if res.From != nil && res.To != nil {
		fmt.Fprintf(&b, ", antara %s dan %s", res.From.Local().Format("15:04:05"), res.To.Local().Format("15:04:05"))
	}
	return b.String()
}

// oldMasterOf is the topology and the promotion whose quarantined old
// master serverID is. Callers hold startMu.
func (r *Runner) oldMasterOf(serverID int64) (store.Topology, store.Promotion, error) {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return store.Topology{}, store.Promotion{}, err
	}
	if srv.Role != store.RoleQuarantined {
		return store.Topology{}, store.Promotion{}, errors.New("server ini bukan master lama yang dikarantina")
	}
	topo, err := r.d.Store.GetTopologyByOldMaster(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return topo, store.Promotion{}, errors.New("server ini bukan lagi master lama sebuah topologi")
	}
	if err != nil {
		return topo, store.Promotion{}, err
	}
	prom, err := r.d.Store.PromotionByOldMaster(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return topo, prom, errors.New("catatan promote master lama ini tidak ditemukan")
	}
	return topo, prom, err
}

// CopyStartedText refuses Periksa and Amankan data tertinggal on an old
// master whose rebuild began its copy (RebuildCopyState CopyStarted); the page
// shows it in their place (G-3).
const CopyStartedText = "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah " +
	"dihapus atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel."

// CopyState is what the panel knows of the copy of an old master's
// rebuild attempts since its promotion (H-1d).
type CopyState int

const (
	// CopyNotStarted: no copy command (mysql.dumpFrom, mysql.cloneFrom)
	// was sent, or the agent refused it before any local change.
	CopyNotStarted CopyState = iota
	// CopyUncertain: a copy command was sent but nothing shows it changed
	// anything (its answer may be lost, or the copy job ended without a
	// progress report); also a job whose params cannot be read.
	CopyUncertain
	// CopyStarted: evidence that the agent began to change the old
	// master's data: the job went past the copy step, the copy job
	// reported progress (dump: after the binary log reset; CLONE: right
	// before CLONE INSTANCE) or its result.
	CopyStarted
)

// RebuildCopyState is the most advanced CopyState of the rebuild jobs of
// the old master serverID that belong to its latest promotion: by the
// promotion recorded in the job (RebuildPromotionID), or for a job stored
// before that by its creation time. Periksa and Amankan data tertinggal
// are refused only for CopyStarted. CopyNotStarted for a server that is no
// promotion's old master.
func RebuildCopyState(st *store.Store, serverID int64) (CopyState, error) {
	prom, err := st.PromotionByOldMaster(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return CopyNotStarted, nil
	}
	if err != nil {
		return CopyNotStarted, err
	}
	jobs, err := st.ListJobsForServer(serverID, 0)
	if err != nil {
		return CopyNotStarted, err
	}
	state := CopyNotStarted
	for _, j := range jobs {
		if j.Kind != KindCreateReplica {
			continue
		}
		var js jobState
		if err := json.Unmarshal([]byte(j.ParamsJSON), &js); err != nil {
			// Unreadable params: nothing is refused on corruption.
			if !j.CreatedAt.Before(prom.StartedAt) {
				state = max(state, CopyUncertain)
			}
			continue
		}
		if !js.RebuildOldMaster {
			continue
		}
		if js.RebuildPromotionID != 0 {
			if js.RebuildPromotionID != prom.ID {
				continue
			}
		} else if j.CreatedAt.Before(prom.StartedAt) {
			continue
		}
		_, steps, err := st.GetJob(j.ID)
		if err != nil {
			return CopyNotStarted, err
		}
		i := slices.IndexFunc(steps, func(s store.JobStep) bool { return s.Name == stepCopyName })
		if i < 0 {
			continue
		}
		switch {
		case j.CurrentStep > i, js.CopyBegan:
			state = max(state, CopyStarted)
		case js.RebuildPromotionID != 0 && js.CopySent:
			state = max(state, CopyUncertain)
		case js.RebuildPromotionID == 0 && j.CurrentStep == i && (js.AgentJobID != "" || js.SnapshotGTID != ""):
			// Stored before the copy flags: an agent job or a copy result
			// at the copy step is no proof either way.
			state = max(state, CopyUncertain)
		}
	}
	return state, nil
}

// refuseAfterCopy is CopyStartedText once a rebuild of serverID reached
// the copy.
func (r *Runner) refuseAfterCopy(serverID int64) error {
	state, err := RebuildCopyState(r.d.Store, serverID)
	if err != nil {
		return err
	}
	if state == CopyStarted {
		return errors.New(CopyStartedText)
	}
	return nil
}

// StartLeftover starts "Periksa data tertinggal" on the quarantined old
// master serverID: the transactions it has that the new master lacks.
func (r *Runner) StartLeftover(ctx context.Context, serverID int64) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	topo, prom, err := r.oldMasterOf(serverID)
	if err != nil {
		return "", err
	}
	if err := r.refuseAfterCopy(serverID); err != nil {
		return "", err
	}
	if !r.d.Hub.IsOnline(serverID) {
		return "", errors.New("agent master lama offline; periksa lagi setelah agent-nya tersambung")
	}
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return "", err
	}
	return r.createJob(KindLeftover, serverID, topo.ID, jobState{PromotionID: prom.ID}, []string{stepLeftover})
}

// StartSalvage starts "Amankan data tertinggal": the transactions the
// latest check found are written to a SQL file on the old master.
func (r *Runner) StartSalvage(ctx context.Context, serverID int64) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	topo, prom, err := r.oldMasterOf(serverID)
	if err != nil {
		return "", err
	}
	if err := r.refuseAfterCopy(serverID); err != nil {
		return "", err
	}
	check, ok := LeftoverOf(prom)
	switch {
	case !ok:
		return "", errors.New("jalankan Periksa data tertinggal dulu")
	case check.Transactions == 0 || check.GTIDSet == "":
		return "", errors.New("pemeriksaan terakhir tidak menemukan data tertinggal; tidak ada yang perlu diamankan")
	}
	if !r.d.Hub.IsOnline(serverID) {
		return "", errors.New("agent master lama offline; amankan setelah agent-nya tersambung")
	}
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return "", err
	}
	return r.createJob(KindSalvage, serverID, topo.ID, jobState{PromotionID: prom.ID, SalvageGTIDSet: check.GTIDSet},
		[]string{stepSalvage})
}

func (jr *jobRun) leftoverSteps() []step { return []step{{stepLeftover, jr.leftoverCheck}} }
func (jr *jobRun) salvageSteps() []step  { return []step{{stepSalvage, jr.salvageWrite}} }

// leftoverCheck runs mysql.leftover against the topology's new master,
// stores the result on the promotion and sends it to Telegram.
func (jr *jobRun) leftoverCheck(ctx context.Context, _ bool) (outcome, error) {
	r, id := jr.r, jr.job.ServerID
	topo, err := r.d.Store.GetTopologyByOldMaster(id)
	if errors.Is(err, store.ErrNotFound) {
		return outcome{}, stepError("server ini bukan lagi master lama sebuah topologi")
	}
	if err != nil {
		return outcome{}, err
	}
	password, err := r.replPassword(topo)
	if err != nil {
		return outcome{}, err
	}
	cred, err := r.peerCred(ctx, id, topo.MasterServerID, topo.SourceHost, topo.SourcePort, topo.ReplUser, password)
	if err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Membandingkan transaksi master lama dengan master baru %s", r.nameOf(topo.MasterServerID))
	var res proto.LeftoverResult
	if err := jr.callWithin(ctx, oldMasterJobTimeout, id, "mysql.leftover", proto.LeftoverParams{NewMaster: cred}, &res); err != nil {
		return outcome{}, r.oldAgentErr(id, err)
	}
	check := LeftoverCheck{LeftoverResult: res, CheckedAt: r.now(), JobID: jr.job.ID}
	b, err := json.Marshal(check)
	if err != nil {
		return outcome{}, err
	}
	r.updatePromotion(jr.st.PromotionID, func(p *store.Promotion) { p.LeftoverJSON = string(b) })
	summary := LeftoverSummary(res)
	jr.logf("info", "%s", truncate(summary, maxLogLine))
	if len(res.Binlogs) > 0 {
		jr.logf("info", "Dibaca dari binlog: %s", truncate(strings.Join(res.Binlogs, ", "), maxLogLine))
	}
	r.sendText(ctx, fmt.Sprintf("Hasil Periksa data tertinggal di master lama %s: %s.", r.nameOf(id), summary))
	return done("%s", truncate(summary, maxStepMessage)), nil
}

// salvageWrite runs mysql.salvage for the set the job was started with.
func (jr *jobRun) salvageWrite(ctx context.Context, _ bool) (outcome, error) {
	r, id := jr.r, jr.job.ServerID
	if jr.st.SalvageGTIDSet == "" {
		return outcome{}, stepError("tidak ada transaksi yang perlu diamankan")
	}
	var f proto.SalvageFile
	if err := jr.callWithin(ctx, oldMasterJobTimeout, id, "mysql.salvage", proto.SalvageParams{GTIDSet: jr.st.SalvageGTIDSet}, &f); err != nil {
		return outcome{}, r.oldAgentErr(id, err)
	}
	return done("Berkas %s dibuat (%d byte); unduh dari halaman server %s sebelum %s, setelah itu agent menghapusnya",
		f.Name, f.Size, r.nameOf(id), f.ExpiresAt.Local().Format("02-01-2006 15:04")), nil
}

// sendText sends text through Deps.SendText; a failure is only logged
// (the result is on the job and the server page).
func (r *Runner) sendText(ctx context.Context, text string) {
	if r.d.SendText == nil {
		return
	}
	text = truncate(text, maxTelegramText)
	if err := r.d.SendText(ctx, text); err != nil && !errors.Is(err, notify.ErrNotConfigured) {
		r.log.Warn("telegram text", "err", err)
	}
}

// oldAgentErr words an agent's unknown_command: its agent is older than
// this panel.
func (r *Runner) oldAgentErr(serverID int64, err error) error {
	if !errors.Is(err, &proto.Error{Code: proto.CodeUnknownCommand}) {
		return err
	}
	if r.d.Alerts != nil {
		r.d.Alerts.AgentNeedsUpgrade(serverID)
	}
	return outdatedAgentError{stepError(fmt.Sprintf("Agent di %s versi lama dan belum mengenal perintah ini. "+
		"Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu ulangi.", r.nameOf(serverID)))}
}

// ErrAgentOutdated matches oldAgentErr's error: its text is the panel's
// own sentence (no agent output), so the web layer may show it.
var ErrAgentOutdated = errors.New("agent versi lama")

// outdatedAgentError unwraps to a stepError, so a job shows it as it is.
type outdatedAgentError struct{ stepError }

func (e outdatedAgentError) Unwrap() error        { return e.stepError }
func (e outdatedAgentError) Is(target error) bool { return target == ErrAgentOutdated }

// directRead runs a short read-only agent command outside a job; an
// offline agent answers proto.ErrOffline.
func (r *Runner) directRead(ctx context.Context, serverID int64, cmd string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, directReadTimeout)
	defer cancel()
	return r.oldAgentErr(serverID, r.d.Hub.Call(ctx, serverID, cmd, params, result))
}

// Connections are the client connections of serverID's MySQL grouped by
// host, account and database (the cutover screen).
func (r *Runner) Connections(ctx context.Context, serverID int64) (proto.ConnectionsResult, error) {
	var res proto.ConnectionsResult
	err := r.directRead(ctx, serverID, "mysql.connections", nil, &res)
	return res, err
}

// SalvageList lists the salvage files on serverID, newest first.
func (r *Runner) SalvageList(ctx context.Context, serverID int64) ([]proto.SalvageFile, error) {
	var res proto.SalvageListResult
	err := r.directRead(ctx, serverID, "mysql.salvageList", nil, &res)
	return res.Files, err
}

// SalvageRead reads one chunk (at most 512 KiB) of salvage file name on
// serverID from offset.
func (r *Runner) SalvageRead(ctx context.Context, serverID int64, name string, offset int64) (proto.SalvageReadResult, error) {
	var res proto.SalvageReadResult
	if !ValidSalvageName(name) || offset < 0 {
		return res, errBadSalvageName
	}
	err := r.directRead(ctx, serverID, "mysql.salvageRead", proto.SalvageReadParams{Name: name, Offset: offset}, &res)
	return res, err
}

// SalvageDelete deletes salvage file name on serverID; refused while the
// server has an unfinished job (a salvage may be writing).
func (r *Runner) SalvageDelete(ctx context.Context, serverID int64, name string) error {
	if !ValidSalvageName(name) {
		return errBadSalvageName
	}
	if err := func() error {
		r.startMu.Lock()
		defer r.startMu.Unlock()
		return r.refuseBusy(serverID, "server ini")
	}(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return r.oldAgentErr(serverID, r.d.Hub.Call(ctx, serverID, "mysql.salvageDelete", proto.SalvageDeleteParams{Name: name}, nil))
}
