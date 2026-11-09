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
	msgPromoteWritable   = "Replika %s bisa ditulis (super_read_only mati) dan mungkin sudah menjadi master; promote ditolak. Periksa server itu dulu."
	msgPromoteNone       = "Tidak ada replika yang bisa dipilih saat ini; lihat kolom alasan di tabel."
	msgPromoteCutover    = "Promote sebelumnya masih menunggu pengalihan aplikasi; tekan \"Sudah saya alihkan\" setelah semua aplikasi memakai master baru."
	msgPromoteUnfinished = "Promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi."

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
}

func parsePromoteChoice(r *http.Request) promoteChoice {
	c := promoteChoice{Mode: r.PostFormValue("mode"), AcceptBranched: r.PostFormValue("accept_branched") == "1",
		ConfirmName:        strings.TrimSpace(r.PostFormValue("confirm_name")),
		ConfirmReplicaName: strings.TrimSpace(r.PostFormValue("confirm_replica_name"))}
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
	// the unfinished earlier promotion.
	Blocked, BlockedJob string
	Local               bool
	AcceptBranched      bool
	// Selected is the checked candidate (0: none); its names and
	// ConfirmText are the button's until the user picks another.
	Selected            int64
	SelectedMasterName  string
	SelectedReplicaName string
	SelectedConfirmText string
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
	if !ok || !a.requireConfirm(w, r, ActPromote, auditMaster, srv.ID) {
		return
	}
	choice := parsePromoteChoice(r)
	view, err := a.Orchestrator.PromoteView(r.Context(), srv.ID)
	if err != nil {
		a.audit(r, "promote", auditMaster, srv.ID, map[string]any{"candidate": choice.Candidate},
			agentFailureAs(err, msgPromoteView, msgPromoteViewOff))
		a.promoteViewFailed(w, r, srv, err)
		return
	}
	params, msg := checkPromote(view, choice)
	id := ""
	if msg == "" {
		if id, err = a.Orchestrator.StartPromote(r.Context(), params); err != nil {
			msg = err.Error()
		}
	}
	detail := map[string]any{"candidate": choice.Candidate, "mode": view.Mode, "accept_partial": params.AcceptPartial,
		"accept_unreachable": params.AcceptUnreachable, "accept_branched": params.AcceptBranched, "job_id": id}
	if msg != "" {
		a.audit(r, "promote", auditMaster, srv.ID, detail, msg)
		a.Logger.Warn("promote refused", "master", srv.ID, "candidate", choice.Candidate, "err", msg)
		p, err := a.newPromotePage(r, srv, topo, view, &choice)
		if err != nil {
			a.internalError(w, "promote page", err)
			return
		}
		p.Flash = msg
		a.render.renderStatus(w, http.StatusBadRequest, "promote", p)
		return
	}
	a.audit(r, "promote", auditMaster, srv.ID, detail, auditOK)
	a.Logger.Info("promote started", "master", srv.ID, "candidate", params.CandidateServerID, "mode", view.Mode,
		"job", id, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, jobURL(id), http.StatusSeeOther)
}

// checkPromote checks choice against the consideration data; it answers
// the orchestrator's parameters, or why the choice is refused.
func checkPromote(v orchestrator.PromoteView, choice promoteChoice) (orchestrator.PromoteParams, string) {
	params := orchestrator.PromoteParams{MasterServerID: v.Master.ID, CandidateServerID: choice.Candidate, Mode: v.Mode}
	if choice.Mode == "" {
		return params, msgPromoteModeGone
	}
	if choice.Mode != v.Mode {
		return params, fmt.Sprintf(msgPromoteModeMoved, modeWord(v.Mode))
	}
	i := slices.IndexFunc(v.Candidates, func(c orchestrator.Candidate) bool { return c.ServerID == choice.Candidate })
	if i < 0 {
		return params, msgPromotePick
	}
	cand := v.Candidates[i]
	for _, c := range v.Candidates {
		if c.Writable {
			return params, fmt.Sprintf(msgPromoteWritable, c.Name)
		}
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
	writable := ""
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
		case c.Writable:
			row.NotSelectable = reasonWritable
			if writable == "" {
				writable = c.Name
			}
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
		p.Rows = append(p.Rows, row)
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
	prev, err := a.Store.LatestPromotion(topo.ID)
	switch {
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return p, err
	case topo.CutoverPending:
		p.Blocked = msgPromoteCutover
		p.BlockedJob = prev.JobID
	case err == nil && (prev.Status == "running" || prev.Status == "partial"):
		p.Blocked, p.BlockedJob = msgPromoteUnfinished, prev.JobID
	case writable != "":
		p.Blocked = fmt.Sprintf(msgPromoteWritable, writable)
	case !slices.ContainsFunc(p.Rows, func(row promoteRow) bool { return row.Selectable }):
		p.Blocked = msgPromoteNone
	}
	return p, nil
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
