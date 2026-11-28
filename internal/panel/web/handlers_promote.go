package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The consideration screen of a promotion (spec 5.2): GET shows what the
// orchestrator's PromoteView found, POST starts the job with the user's
// choice. The page states every condition; the post checks the same
// conditions again (the orchestrator checks them a third time) so a stale
// or hand-made form cannot skip one.

const (
	// msgLocalMaster is shown wherever the panel's own server is (or is
	// about to be) the master.
	msgLocalMaster = "Panel berjalan di server ini; bila server ini mati, promote darurat tidak bisa dilakukan dari panel"

	// msgPromoteView and msgPromoteViewOff: the consideration data could
	// not be gathered (the error itself is for the panel's log).
	msgPromoteView       = "Data pertimbangan promote tidak bisa dikumpulkan; rinciannya ada di log panel. Muat ulang halaman ini sebentar lagi."
	msgPromoteViewOff    = "Agent server ini offline; data pertimbangan promote tidak bisa dikumpulkan sekarang."
	msgPromotePick       = "Pilih replika yang akan dijadikan master baru."
	msgPromoteModeGone   = "Formulir promote tidak lengkap; muat ulang halaman ini lalu pilih ulang."
	msgPromoteModeMoved  = "Status master berubah sejak halaman dibuka: sekarang hanya promote %s yang tersedia. Periksa layar ini lagi lalu pilih ulang."
	msgPromoteNotPick    = "%s tidak bisa dipilih: %s"
	msgPromoteBranched   = "Replika bercabang: masing-masing punya transaksi yang tidak dimiliki yang lain. Centang persetujuan bahwa replika lain yang bercabang dibangun ulang dari master baru untuk melanjutkan."
	msgPromotePartial    = "%s hanya mereplikasi sebagian database; %s tidak akan ada di master baru. Centang persetujuan di kotak merah untuk melanjutkan."
	msgPromoteReach      = "Setujui bahwa replika %s diarahkan ulang ke master baru saat tersambung lagi (centang kotaknya) untuk melanjutkan."
	msgPromoteTypeMaster = "Ketik nama master lama %s untuk melanjutkan."
	msgPromoteTypeRep    = "Ketik nama replika %s untuk melanjutkan."
	msgPromoteNone       = "Tidak ada replika yang bisa dipilih saat ini; lihat kolom alasan di tabel."
	msgPromoteCutover    = "Promote sebelumnya masih menunggu pengalihan aplikasi; tekan \"Sudah saya alihkan\" setelah semua aplikasi memakai master baru."
	msgPromoteUnfinished = "Promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi."
	// msgPromoteJobOpen: a job of the topology is unfinished, which the
	// promote's POST refuses on (ruling D61).
	msgPromoteJobOpen = "Topologi ini masih punya pekerjaan yang belum selesai. Buka pekerjaan itu dan tekan Batalkan, lalu kembali ke sini."
	// msgOldMasterAck (D7): an emergency promote without the D5 tick that
	// the old master is dead or cut off from the applications.
	msgOldMasterAck = `Centang "Saya sudah memastikan master lama mati atau terputus dari aplikasi" untuk melanjutkan promote darurat.`

	// reasonWritable: a replica read writable, or not answering with a
	// writable last report (T24, T24b name it in the blocker).
	reasonWritable = "Bisa ditulis (super_read_only mati); mungkin sudah menjadi master"
	// reasonAgentOffline is the orchestrator's reason for a replica whose
	// agent is offline (an unreachable replica).
	reasonAgentOffline = "Agent offline"
)

// promoteChoice is what the user posted.
type promoteChoice struct {
	Candidate int64
	Mode      string
	// AcceptPartial are the candidates whose partial coverage was
	// accepted (boxes of other candidates stay posted when hidden).
	AcceptPartial     []int64
	AcceptUnreachable []int64
	AcceptBranched    bool
	// ConfirmName is the typed old master's name, ConfirmReplicaName the
	// typed candidate's name, both trimmed.
	ConfirmName        string
	ConfirmReplicaName string
	// OldMasterDead: the D5 tick (old_master_dead=1) that the old master
	// is dead or cut off from the applications; an emergency asks for it.
	OldMasterDead bool
}

func parsePromoteChoice(r *http.Request) promoteChoice {
	c := promoteChoice{Mode: r.PostFormValue("mode"), AcceptBranched: r.PostFormValue("accept_branched") == "1",
		ConfirmName:        strings.TrimSpace(r.PostFormValue("confirm_name")),
		ConfirmReplicaName: strings.TrimSpace(r.PostFormValue("confirm_replica_name")),
		OldMasterDead:      r.PostFormValue("old_master_dead") == "1"}
	c.Candidate, _ = strconv.ParseInt(r.PostFormValue("candidate"), 10, 64)
	c.AcceptPartial = formIDs(r, "accept_partial")
	c.AcceptUnreachable = formIDs(r, "accept_unreachable")
	return c
}

// formIDs are the distinct ids posted under name, in order.
func formIDs(r *http.Request, name string) []int64 {
	var ids []int64
	for _, s := range r.PostForm[name] {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// promoteRow is one replica of the candidate table.
type promoteRow struct {
	orchestrator.Candidate
	Lag, Position, Backlog, Freshness string
	// Selectable: it may be chosen; otherwise NotSelectable says why.
	Selectable    bool
	NotSelectable string
	Unreachable   bool
	// MasterName / ReplicaName are the names the confirmation asks to type
	// when this replica is chosen ("" for none); ConfirmText the
	// confirmation's sentence.
	MasterName  string
	ReplicaName string
	ConfirmText string
	Checked     bool
	// AcceptPartial / AcceptUnreachable: the boxes as posted.
	AcceptPartial     bool
	AcceptUnreachable bool
	// StatusTone is the badge tone of Status; Badges the markers shown
	// under the name.
	StatusTone string
	Badges     []Badge
	// Gate is the precondition list of a promote to this replica; nil
	// when it cannot be chosen now.
	Gate *gateView
}

// promoteStep is one step of the "Urutan langkah" card.
type promoteStep struct {
	Num        int
	Name, Text string
}

// promoteScreenSteps are the steps of the promotion the screen lists, in
// order: the job's steps (orchestrator.PromoteStepNames) the user sees
// happen, without the bookkeeping ones, then the user's own step, moving
// the applications. TestPromotePageStepsList checks the names against
// the job's.
func promoteScreenSteps(mode string) []promoteStep {
	var steps []promoteStep
	if mode != proto.PromoteEmergency {
		steps = append(steps,
			promoteStep{0, "Kunci master", "Master dijadikan hanya-baca; aplikasi tidak bisa menulis mulai langkah ini."},
			promoteStep{0, "Replika mengejar", "Replika terpilih menerapkan semua transaksi master."})
	}
	steps = append(steps,
		promoteStep{0, "Jadikan master baru", "Replika terpilih menjadi master dan menerima tulisan."},
		promoteStep{0, "Karantina master lama", "Master lama dijadikan baca-saja di bawah master baru; bila tidak terjangkau, karantina menyusul saat tersambung."},
		promoteStep{0, "Arahkan replika lain", "Replika lain mengikuti master baru; yang tidak terjangkau diarahkan saat tersambung lagi; yang punya transaksi di luar master baru perlu dibangun ulang."},
		promoteStep{0, "Alihkan aplikasi", "Anda mengganti alamat database di tiap aplikasi ke master baru, di layar pengalihan."})
	for i := range steps {
		steps[i].Num = i + 1
	}
	return steps
}

// severityTone is the badge tone of a monitor status code.
func severityTone(code string) string {
	switch monitor.SeverityOf(code) {
	case monitor.SevOK:
		return "success"
	case monitor.SevWarn:
		return "warning"
	case monitor.SevCrit:
		return "error"
	}
	return "neutral"
}

// rowBadges are the markers of a candidate, shown under its name.
func rowBadges(row promoteRow) []Badge {
	var b []Badge
	if row.MostCurrent {
		b = append(b, Badge{"success", "Paling mutakhir"})
	}
	if row.Partial {
		b = append(b, Badge{"warning", "Replika sebagian"})
	}
	if row.Unreachable {
		b = append(b, Badge{"warning", "Tidak terjangkau"})
	}
	if row.Branched {
		b = append(b, Badge{"error", "Bercabang"})
	}
	if row.Writable || row.WritableSilent {
		b = append(b, Badge{"error", "Bisa ditulis"})
	}
	return b
}

type promotePage struct {
	Page
	Master    store.Server
	View      orchestrator.PromoteView
	Emergency bool
	// MasterStatus is the master's monitoring status ("" when unknown).
	MasterStatus string
	Rows         []promoteRow
	Unreachable  []promoteRow
	// The replicas' probes of the master (emergency only).
	ProbeSilent, ProbeAnswered []string
	// LastReport is the master's last report time (HH:MM:SS), LastReportAgo
	// its age in words; "" when none.
	LastReport, LastReportAgo string
	// LostKnown: the master's binlog was read and a most current replica
	// exists; Lost is how many of its transactions reached no replica.
	LostKnown bool
	Lost      int
	// Blocked says why the promotion cannot start now; BlockedJob links
	// the job that blocks it (an unfinished earlier promotion, or another
	// unfinished job of the topology), BlockedJobLink the link's words.
	Blocked, BlockedJob string
	BlockedJobLink      string
	Local               bool
	AcceptBranched      bool
	// Selected is the checked candidate (0: none); its names and
	// ConfirmText are the button's until the user picks another.
	Selected            int64
	SelectedMasterName  string
	SelectedReplicaName string
	SelectedConfirmText string
	// The "Apa yang akan terjadi" card: the steps, the write pause in
	// short (the full sentence is View.WritePause) and the most current
	// candidate (nil: none).
	Steps          []promoteStep
	WritePauseFrom string
	MostCurrent    *promoteRow
	// NeedsAck: a candidate's backup row asks for the tick (D3); RedNote:
	// a candidate's gate has a red row (D2 is drawn, shown while it is
	// the chosen one).
	NeedsAck, RedNote bool
}

// SelectedGate is the precondition list of the chosen candidate; nil when
// none is chosen.
func (p promotePage) SelectedGate() *gateView {
	for _, row := range p.Rows {
		if row.Checked {
			return row.Gate
		}
	}
	return nil
}

// promoteForm shows the consideration screen of master {id}.
func (a *app) promoteForm(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	view, err := a.Orchestrator.PromoteView(r.Context(), srv.ID)
	if err != nil {
		a.promoteViewFailed(w, r, srv, err)
		return
	}
	p, err := a.newPromotePage(r, srv, topo, view, nil)
	if err != nil {
		a.internalError(w, "promote page", err)
		return
	}
	a.render.Render(w, "promote", p)
}

func (a *app) promoteViewFailed(w http.ResponseWriter, r *http.Request, srv store.Server, err error) {
	a.Logger.Warn("promote view", "master", srv.ID, "err", logErr(err))
	a.showError(w, r, srv, http.StatusServiceUnavailable, agentFailureAs(err, msgPromoteView, msgPromoteViewOff))
}

// startPromote starts the promotion of the chosen replica of master {id}.
func (a *app) startPromote(w http.ResponseWriter, r *http.Request) {
	srv, topo, ok := a.masterFromPath(w, r)
	if !ok {
		return
	}
	choice := parsePromoteChoice(r)
	backupAck := r.PostFormValue("backup_ack") == "1"
	// Before the token: a form whose mode is missing or no longer the
	// master's, or an emergency without the D5 tick, is refused (the post
	// is checked again against the consideration data below, and by the
	// orchestrator); then the precondition gate of a planned promote. An
	// emergency is never refused by a row. A post without a candidate is
	// refused below, as before.
	refuse := func(status int, flash, outcome string) {
		detail := map[string]any{"candidate": choice.Candidate, "mode": choice.Mode}
		if backupAck {
			detail["backup_ack"] = true
		}
		if choice.Mode == proto.PromoteEmergency {
			detail["old_master_dead"] = choice.OldMasterDead
		}
		a.audit(r, "promote", auditMaster, srv.ID, detail, outcome)
		view, err := a.Orchestrator.PromoteView(r.Context(), srv.ID)
		if err != nil {
			a.promoteViewFailed(w, r, srv, err)
			return
		}
		p, err := a.newPromotePage(r, srv, topo, view, &choice)
		if err != nil {
			a.internalError(w, "promote page", err)
			return
		}
		p.Flash = flash
		a.render.renderStatus(w, status, "promote", p)
	}
	if msg := checkPromotePosted(a.Orchestrator.PromoteMode(srv.ID), choice); msg != "" {
		a.Logger.Warn("promote refused", "master", srv.ID, "candidate", choice.Candidate, "err", msg)
		refuse(http.StatusBadRequest, msg, msg)
		return
	}
	if choice.Mode == proto.PromotePlanned && choice.Candidate > 0 {
		in := orchestrator.GateInput{Action: orchestrator.GatePromotePlanned, MasterID: srv.ID, TargetID: choice.Candidate}
		if g := a.gateCheck(in, &backupAck); g.status != 0 {
			refuse(g.status, g.flash, g.outcome)
			return
		}
	}
	if !a.requireConfirm(w, r, ActPromote, auditMaster, srv.ID) {
		return
	}
	view, err := a.Orchestrator.PromoteView(r.Context(), srv.ID)
	if err != nil {
		a.audit(r, "promote", auditMaster, srv.ID, map[string]any{"candidate": choice.Candidate},
			agentFailureAs(err, msgPromoteView, msgPromoteViewOff))
		a.promoteViewFailed(w, r, srv, err)
		return
	}
	params, msg := checkPromote(view, choice)
	params.BackupAck = backupAck && view.Mode == proto.PromotePlanned
	emergency := view.Mode == proto.PromoteEmergency
	detail := map[string]any{"candidate": choice.Candidate, "mode": view.Mode, "accept_partial": params.AcceptPartial,
		"accept_unreachable": params.AcceptUnreachable, "accept_branched": params.AcceptBranched}
	if params.BackupAck {
		detail["backup_ack"] = true
	}
	if emergency {
		// What the user ticked, and when the panel last heard from the
		// master (its own clock, not the agent's).
		detail["old_master_dead"] = params.OldMasterDeadAck
		detail["master_last_report"] = ""
		if view.LastReportAt != nil {
			detail["master_last_report"] = view.LastReportAt.UTC().Format(time.RFC3339)
		}
	}
	id, flashJob := "", ""
	status, outcome := http.StatusBadRequest, msg
	if msg == "" {
		if emergency {
			// The rows no emergency is refused by, red as it starts: their
			// keys only (a row's detail may quote an agent).
			detail["skipped_red"] = a.skippedRed(srv.ID, choice.Candidate)
		}
		if id, err = a.Orchestrator.StartPromote(r.Context(), params); err != nil {
			g := a.startRefused(err, http.StatusBadRequest)
			status, msg, outcome, flashJob = g.status, g.flash, g.outcome, g.flashJob
		}
	}
	detail["job_id"] = id
	if msg != "" {
		a.audit(r, "promote", auditMaster, srv.ID, detail, outcome)
		a.Logger.Warn("promote refused", "master", srv.ID, "candidate", choice.Candidate, "err", msg)
		p, err := a.newPromotePage(r, srv, topo, view, &choice)
		if err != nil {
			a.internalError(w, "promote page", err)
			return
		}
		p.Flash, p.FlashJob = msg, flashJob
		a.render.renderStatus(w, status, "promote", p)
		return
	}
	a.audit(r, "promote", auditMaster, srv.ID, detail, auditOK)
	args := []any{"master", srv.ID, "candidate", params.CandidateServerID, "mode", view.Mode, "job", id,
		"by", auth.UsernameFrom(r.Context())}
	if emergency {
		args = append(args, "skipped_red", detail["skipped_red"])
	}
	a.Logger.Info("promote started", args...)
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// skippedRed is the keys of the rows of an emergency promote of master to
// candidate that are red now, in the list's order (none: an empty list).
// The gate never refuses an emergency; when it cannot judge, nil (null in
// the audit: not known) and the start goes on.
func (a *app) skippedRed(master, candidate int64) []string {
	in := orchestrator.GateInput{Action: orchestrator.GatePromoteEmergency, MasterID: master, TargetID: candidate}
	res, err := a.Orchestrator.Gate(in)
	if err != nil {
		a.Logger.Warn("gate not judged", "action", in.Action, "master", master, "target", candidate, "err", logErr(err))
		return nil
	}
	keys := res.RedKeys()
	if keys == nil {
		keys = []string{}
	}
	return keys
}

// checkPromote checks choice against the consideration data; it answers
// the orchestrator's parameters, or why the choice is refused.
func checkPromote(v orchestrator.PromoteView, choice promoteChoice) (orchestrator.PromoteParams, string) {
	params := orchestrator.PromoteParams{MasterServerID: v.Master.ID, CandidateServerID: choice.Candidate, Mode: v.Mode}
	if msg := checkPromotePosted(v.Mode, choice); msg != "" {
		return params, msg
	}
	params.OldMasterDeadAck = v.Mode == proto.PromoteEmergency && choice.OldMasterDead
	i := slices.IndexFunc(v.Candidates, func(c orchestrator.Candidate) bool { return c.ServerID == choice.Candidate })
	if i < 0 {
		return params, msgPromotePick
	}
	cand := v.Candidates[i]
	if err := v.OtherReplicaRefusal(); err != nil {
		return params, err.Error() // T24, T24b, T24c
	}
	if !cand.Eligible {
		return params, fmt.Sprintf(msgPromoteNotPick, cand.Name, cand.Reason)
	}
	if v.Branched {
		if !choice.AcceptBranched {
			return params, msgPromoteBranched
		}
		params.AcceptBranched = true
	}
	if cand.Partial {
		if !slices.Contains(choice.AcceptPartial, cand.ServerID) {
			return params, fmt.Sprintf(msgPromotePartial, cand.Name, missingText(cand))
		}
		params.AcceptPartial = true
	}
	var missing []string
	for _, c := range v.Candidates {
		if c.ServerID == cand.ServerID || !unreachable(c) {
			continue
		}
		if slices.Contains(choice.AcceptUnreachable, c.ServerID) {
			params.AcceptUnreachable = append(params.AcceptUnreachable, c.ServerID)
		} else {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) > 0 {
		return params, fmt.Sprintf(msgPromoteReach, strings.Join(missing, ", "))
	}
	if want := masterNameToType(v); want != "" && choice.ConfirmName != want {
		return params, fmt.Sprintf(msgPromoteTypeMaster, want)
	}
	if want := replicaNameToType(v, cand); want != "" && choice.ConfirmReplicaName != want {
		return params, fmt.Sprintf(msgPromoteTypeRep, want)
	}
	return params, ""
}

// checkPromotePosted is why choice is refused against the master's mode,
// from the form alone: its mode missing or no longer the master's, or an
// emergency without the D5 tick (D7); "" when it may go on.
func checkPromotePosted(mode string, choice promoteChoice) string {
	if choice.Mode == "" {
		return msgPromoteModeGone
	}
	if choice.Mode != mode {
		return fmt.Sprintf(msgPromoteModeMoved, modeWord(mode))
	}
	if mode == proto.PromoteEmergency && !choice.OldMasterDead {
		return msgOldMasterAck
	}
	return ""
}

// unreachable is the orchestrator's test for a replica that must be
// accepted to be repointed later: its agent is offline or its probe of the
// master failed.
func unreachable(c orchestrator.Candidate) bool {
	return c.Reason == reasonAgentOffline || c.ProbeErr != ""
}

// masterNameToType is the old master's name, which an emergency promote
// asks to type (confirm_name); "" for a planned one.
func masterNameToType(v orchestrator.PromoteView) string {
	if v.Mode == proto.PromoteEmergency {
		return v.Master.Name
	}
	return ""
}

// replicaNameToType is the candidate's name, which the confirmation asks
// to type (confirm_replica_name) when it is partial or the replicas are
// branched; "" otherwise. In an emergency both names are typed (spec 5.2).
func replicaNameToType(v orchestrator.PromoteView, cand orchestrator.Candidate) string {
	if cand.Partial || v.Branched {
		return cand.Name
	}
	return ""
}

func confirmText(v orchestrator.PromoteView, cand orchestrator.Candidate) string {
	s := fmt.Sprintf("Promote %s %s menjadi master baru menggantikan %s.", modeWord(v.Mode), cand.Name, v.Master.Name)
	if v.Mode == proto.PromoteEmergency {
		s += " Ketik nama master lama untuk menyatakan bahwa master itu memang sudah tidak dipakai aplikasi."
	}
	switch {
	case cand.Partial:
		s += " Ketik nama replika: database yang tidak diikutinya tidak akan ada di master baru."
	case v.Branched:
		s += " Ketik nama replika: replika lain yang bercabang dibangun ulang dari master baru."
	}
	return s
}

func modeWord(mode string) string {
	if mode == proto.PromoteEmergency {
		return "darurat"
	}
	return "terencana"
}

func missingText(c orchestrator.Candidate) string {
	if len(c.MissingDatabases) == 0 {
		return "database yang tidak diikutinya"
	}
	return strings.Join(c.MissingDatabases, ", ")
}

// newPromotePage builds the screen from the consideration data; choice is
// the refused post to show again (nil on GET).
func (a *app) newPromotePage(r *http.Request, srv store.Server, topo store.Topology, v orchestrator.PromoteView,
	choice *promoteChoice) (promotePage, error) {
	if v.Master.ID == 0 {
		v.Master = srv
	}
	p := promotePage{Page: a.page(r), Master: srv, View: v, Emergency: v.Mode == proto.PromoteEmergency,
		Local: a.isLocal(srv)}
	if mv, ok := a.Monitor.View(srv.ID); ok {
		p.MasterStatus = mv.Status.Code
	}
	now := a.Now()
	if v.LastReportAt != nil {
		p.LastReport = v.LastReportAt.Local().Format("15:04:05")
		p.LastReportAgo = agoWords(now.Sub(*v.LastReportAt))
	}
	for _, c := range v.Candidates {
		row := promoteRow{Candidate: c, Lag: "-", Freshness: "belum ada laporan", Unreachable: unreachable(c),
			MasterName: masterNameToType(v), ReplicaName: replicaNameToType(v, c), ConfirmText: confirmText(v, c)}
		if c.LagSec != nil {
			row.Lag = fmt.Sprintf("%d detik", *c.LagSec)
		}
		if c.ReportAgo > 0 {
			row.Freshness = "laporan " + agoWords(c.ReportAgo)
		}
		row.Position, row.Backlog = positionText(v, c, p.LastReport), backlogText(c)
		switch {
		case c.Writable || c.WritableSilent:
			row.NotSelectable = reasonWritable
		case !c.Eligible:
			row.NotSelectable = c.Reason
		default:
			row.Selectable = true
		}
		if choice != nil {
			row.AcceptPartial = slices.Contains(choice.AcceptPartial, c.ServerID)
			row.AcceptUnreachable = slices.Contains(choice.AcceptUnreachable, c.ServerID)
		}
		if p.Emergency && c.ProbeOK != nil {
			if *c.ProbeOK {
				p.ProbeAnswered = append(p.ProbeAnswered, c.Name)
			} else {
				p.ProbeSilent = append(p.ProbeSilent, c.Name)
			}
		}
		if c.MostCurrent && p.Emergency && v.BinlogTail != nil && v.BinlogTail.GTIDExecuted != "" {
			p.LostKnown, p.Lost = true, c.GapToMaster
		}
		row.StatusTone = severityTone(c.Status)
		row.Badges = rowBadges(row)
		p.Rows = append(p.Rows, row)
	}
	p.Steps = promoteScreenSteps(v.Mode)
	p.WritePauseFrom = "sejak Kunci master"
	if p.Emergency {
		p.WritePauseFrom = "sejak master hilang"
	}
	for i := range p.Rows {
		if p.Rows[i].MostCurrent {
			p.MostCurrent = &p.Rows[i]
			break
		}
	}
	if choice != nil {
		p.AcceptBranched = choice.AcceptBranched
		if slices.ContainsFunc(p.Rows, func(row promoteRow) bool { return row.ServerID == choice.Candidate && row.Selectable }) {
			p.Selected = choice.Candidate
		}
	} else {
		p.Selected = defaultCandidate(v, p.Rows)
	}
	for i := range p.Rows {
		row := &p.Rows[i]
		row.Checked = row.ServerID == p.Selected
		if row.Checked {
			p.SelectedMasterName, p.SelectedReplicaName, p.SelectedConfirmText = row.MasterName, row.ReplicaName, row.ConfirmText
		}
		if row.Unreachable {
			p.Unreachable = append(p.Unreachable, *row)
		}
	}

	// Why the promotion cannot start now, if it cannot.
	other := v.OtherReplicaRefusal()
	prev, err := a.Store.LatestPromotion(topo.ID)
	switch {
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return p, err
	case topo.CutoverPending:
		p.Blocked = msgPromoteCutover
		p.BlockedJob = prev.JobID
	case err == nil && (prev.Status == "running" || prev.Status == "partial"):
		p.Blocked, p.BlockedJob = msgPromoteUnfinished, prev.JobID
	case other != nil:
		p.Blocked = other.Error() // T24, T24b, T24c
	case !slices.ContainsFunc(p.Rows, func(row promoteRow) bool { return row.Selectable }):
		p.Blocked = msgPromoteNone
	case !p.Emergency && a.plannedLockRefusal(&p):
	default:
		// The job the POST refuses on (StartPromote's ActiveJobForTopology).
		j, busy, err := a.Store.ActiveJobForTopology(topo.ID)
		if err != nil {
			return p, err
		}
		if busy {
			p.Blocked, p.BlockedJob = msgPromoteJobOpen, j.ID
			if j.Kind != orchestrator.KindPromotePlanned && j.Kind != orchestrator.KindPromoteEmergency {
				p.BlockedJobLink = "Buka pekerjaan"
			}
		}
	}
	if p.BlockedJob != "" && p.BlockedJobLink == "" {
		p.BlockedJobLink = "Buka pekerjaan promote"
	}
	// The precondition list of each candidate that can be chosen now
	// (plan 5b-2b): the stored data only, no agent call.
	if p.Blocked == "" && v.OldMasterRefusal() == "" {
		action := orchestrator.GatePromotePlanned
		if p.Emergency {
			action = orchestrator.GatePromoteEmergency
		}
		for i := range p.Rows {
			row := &p.Rows[i]
			if !row.Selectable {
				continue
			}
			row.Gate = a.gateFor(orchestrator.GateInput{Action: action, MasterID: srv.ID, TargetID: row.ServerID}, false)
			p.NeedsAck = p.NeedsAck || row.Gate.NeedsAck
			p.RedNote = p.RedNote || row.Gate.RedNote()
		}
	}
	return p, nil
}

// plannedLockRefusal sets p's blocker to the refusal StartPromote gives a
// planned promote of p's master while a lock may stand (S-2): T12c with
// the newest standing record's job, or T12d while the master reads
// Terkunci without one; it reports whether it did. An emergency keeps the
// clock rule only and is not blocked here. It writes nothing (K10).
func (a *app) plannedLockRefusal(p *promotePage) bool {
	recs, err := a.Orchestrator.LockRecords(p.Master.ID)
	if err != nil {
		a.Logger.Warn("lock records", "master", p.Master.ID, "err", logErr(err))
		return false // StartPromote reads them again and refuses
	}
	var refusal *orchestrator.LockStandsError
	switch {
	case len(recs) > 0:
		refusal = &orchestrator.LockStandsError{Code: "planned", Master: p.Master.Name, JobID: recs[0].JobID}
	case p.MasterStatus == monitor.StatusLocked:
		refusal = &orchestrator.LockStandsError{Code: "master_locked", Master: p.Master.Name}
	default:
		return false
	}
	p.Blocked, p.BlockedJob = refusal.Error(), refusal.JobID
	return true
}

// defaultCandidate is the choice the screen starts with: the most current
// complete replica, else the complete one closest to the master; never
// one that did not answer (Unreachable); none when the replicas are
// branched or no such replica exists (the user decides).
func defaultCandidate(v orchestrator.PromoteView, rows []promoteRow) int64 {
	if v.Branched {
		return 0
	}
	var best *promoteRow
	for i := range rows {
		row := &rows[i]
		if !row.Selectable || row.Partial || row.Unreachable {
			continue
		}
		if best == nil || (row.MostCurrent && !best.MostCurrent) ||
			(row.MostCurrent == best.MostCurrent && row.GapToMaster < best.GapToMaster) {
			best = row
		}
	}
	if best == nil {
		return 0
	}
	return best.ServerID
}

// positionText is the replica's position against the master (spec 5.2).
func positionText(v orchestrator.PromoteView, c orchestrator.Candidate, lastReport string) string {
	if !c.Eligible && c.Reason != "Mempunyai transaksi di luar master" {
		return "tidak diketahui"
	}
	switch {
	case v.Mode == proto.PromotePlanned && v.LastMasterGTID == "":
		return "tidak diketahui: master belum melapor"
	case v.Mode == proto.PromotePlanned && c.GapToMaster == 0:
		return "sudah menerima semua transaksi master"
	case v.Mode == proto.PromotePlanned:
		return fmt.Sprintf("tertinggal %d transaksi dari master", c.GapToMaster)
	case v.BinlogTail != nil && v.BinlogTail.GTIDExecuted != "" && c.GapToMaster == 0:
		return "sudah menerima semua transaksi di binlog master"
	case v.BinlogTail != nil && v.BinlogTail.GTIDExecuted != "":
		return fmt.Sprintf("tertinggal %d transaksi dari binlog master (pasti)", c.GapToMaster)
	case lastReport == "" || v.LastMasterGTID == "":
		return "tidak diketahui: master belum pernah melapor"
	case c.GapToLastReport == 0:
		return "sudah menerima semua yang master laporkan pukul " + lastReport
	}
	return fmt.Sprintf("tertinggal %d transaksi dari laporan terakhir master", c.GapToLastReport)
}

func backlogText(c orchestrator.Candidate) string {
	switch {
	case !c.Eligible:
		return "-"
	case c.ApplyBacklog == 0:
		return "semua yang diterima sudah diterapkan"
	}
	return fmt.Sprintf("semua diterima, %d belum diterapkan; akan diterapkan sebelum promote", c.ApplyBacklog)
}

// agoWords is an age in words: "12 detik lalu", "3 menit lalu".
func agoWords(d time.Duration) string {
	sec := int64(max(d, 0) / time.Second)
	switch {
	case sec >= 86400:
		return fmt.Sprintf("%d hari lalu", sec/86400)
	case sec >= 3600:
		return fmt.Sprintf("%d jam lalu", sec/3600)
	case sec >= 60:
		return fmt.Sprintf("%d menit lalu", sec/60)
	}
	return fmt.Sprintf("%d detik lalu", sec)
}

// promoteEmergency reports whether a promotion of the master would run as
// an emergency, as the orchestrator decides it (PromoteView.Mode): unless
// its agent is online, its MySQL answers and its status is not one of a
// master gone (or of one whose agent connected again but has not reported
// since: its last report is old).
func promoteEmergency(online bool, v monitor.View, monitored bool) bool {
	if !online || !monitored || v.Snap == nil || !v.Snap.MySQLOK {
		return true
	}
	switch v.Status.Code {
	case monitor.StatusDown, monitor.StatusUnreachable, monitor.StatusAgentOffline, monitor.StatusAwaitingReport:
		return true
	}
	return false
}
