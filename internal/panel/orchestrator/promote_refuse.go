package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// otherReplicaReadTimeout bounds the live read (one mysql.status) of each
// other replica before a promote (spec 5.7, R-198); Runner.otherReadTimeout
// in tests.
const otherReplicaReadTimeout = 10 * time.Second

// Refusals of a promote over another replica of the topology (spec 5.11).
const (
	// textOtherWritable is T24: the replica's name three times.
	textOtherWritable = "Replika %s melaporkan bisa ditulis (super_read_only mati), jadi mungkin sudah menjadi master; " +
		"promote ditolak, juga promote darurat. Bila %s memang bukan master (misalnya dibuka dengan tangan untuk " +
		"perbaikan): kunci lagi di halaman replikanya, atau lepas dengan Lepas replika, lalu buka halaman ini lagi. " +
		"Bila aplikasi sudah menulis ke %s: jangan promote; periksa server itu dulu."
	// textOtherWritableSilent is T24b: the name twice.
	textOtherWritableSilent = "Replika %s terakhir melaporkan bisa ditulis dan sekarang tidak menjawab, jadi panel tidak " +
		"bisa memastikannya; promote ditolak, juga promote darurat. Bila Anda yakin %s bukan master: lepas dengan " +
		"Lepas replika, lalu buka halaman ini lagi."
	// textOtherUnreadable is T24c.
	textOtherUnreadable = "Replika %s tidak bisa dibaca sekarang, jadi panel tidak bisa memastikan ia masih baca-saja; " +
		"promote terencana ditolak. Sambungkan agent replika itu, atau lepas dengan Lepas replika, lalu buka halaman " +
		"ini lagi."
	// textOtherUnreadNote is T24d, the emergency screen's note: the names.
	textOtherUnreadNote = "Replika berikut tidak bisa dibaca sekarang; panel memakai laporan terakhirnya (baca-saja), " +
		"atau tidak punya laporan sama sekali: %s."
)

// PromoteWritableError is a promote refused over another replica.
type PromoteWritableError struct {
	Code string // writable (T24) | writable_silent (T24b) | unreadable (T24c)
	Name string
}

func (e *PromoteWritableError) Error() string {
	switch e.Code {
	case "writable":
		return fmt.Sprintf(textOtherWritable, e.Name, e.Name, e.Name)
	case "writable_silent":
		return fmt.Sprintf(textOtherWritableSilent, e.Name, e.Name)
	}
	return fmt.Sprintf(textOtherUnreadable, e.Name)
}

// UnreadNote is T24d for an emergency view naming its Unread replicas; ""
// for a planned view or when every replica was read.
func (v PromoteView) UnreadNote() string {
	if v.Mode != proto.PromoteEmergency || len(v.Unread) == 0 {
		return ""
	}
	return fmt.Sprintf(textOtherUnreadNote, strings.Join(v.Unread, ", "))
}

// replicaRead is how PromoteView learned whether a replica is read-only.
type replicaRead struct {
	// known: it answered (or, planned, its live report said both flags
	// ON); readOnly and superReadOnly are the answer.
	known, readOnly, superReadOnly bool
}

// readReplicas starts the live reads of reps for a promote in mode (spec
// 5.7, R-198), each one mysql.status bounded by otherReadTimeout, all at
// once on wg; the slot of each is filled once wg is done. Planned: every
// replica whose report is not live, is missing, or does not say both
// flags ON is read; one with a live report of both ON is not read again
// (its report stands as the answer). Emergency: every replica whose agent
// is connected is read. Only the answer's typed read_only and
// super_read_only are used.
func (r *Runner) readReplicas(ctx context.Context, wg *sync.WaitGroup, reps []store.Replica, mode string) []replicaRead {
	reads := make([]replicaRead, len(reps))
	for i, rep := range reps {
		if mode == proto.PromotePlanned {
			if rv, live := r.liveView(rep.ServerID); live && reportNoWrites(rv) {
				reads[i] = replicaRead{known: true, readOnly: true, superReadOnly: true}
				continue
			}
		}
		if !r.d.Hub.IsOnline(rep.ServerID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, r.otherReadTimeout)
			defer cancel()
			var st proto.MySQLStatus
			if err := r.d.Hub.Call(cctx, rep.ServerID, "mysql.status", nil, &st); err != nil {
				r.log.Debug("replica read for promote", "server_id", rep.ServerID, "err", logErr(err))
				return
			}
			reads[i] = replicaRead{known: true, readOnly: st.ReadOnly, superReadOnly: st.SuperReadOnly}
		}()
	}
	return reads
}

// judge is what the read says of a replica whose last report is rv:
// writable when it answered anything but both flags ON (noWrites); when
// it could not be read, writableSilent if its last report may take
// writes, else unread.
func (rd replicaRead) judge(rv monitor.View) (writable, writableSilent, unread bool) {
	switch {
	case rd.known:
		return !noWrites(rd.readOnly, rd.superReadOnly), false, false
	case reportMayWrite(rv):
		return false, true, false
	}
	return false, false, true
}

// reportNoWrites and reportMayWrite: whether the report says both flags
// ON (noWrites) or anything else; neither when it has no answer of MySQL.
func reportNoWrites(rv monitor.View) bool {
	return rv.Snap != nil && rv.Snap.MySQLOK && noWrites(rv.Snap.ReadOnly, rv.Snap.SuperReadOnly)
}

func reportMayWrite(rv monitor.View) bool {
	return rv.Snap != nil && rv.Snap.MySQLOK && !noWrites(rv.Snap.ReadOnly, rv.Snap.SuperReadOnly)
}

// OtherReplicaRefusal is the refusal a promote of this view meets over a
// replica of the topology, nil when none: the first one read writable
// (T24, both modes), else the first one not readable whose last report
// says writable (T24b, both modes), else on a planned promote the first
// one not readable (T24c). The consideration screen shows it before
// anything is typed; promoteRefusals refuses with it.
func (v PromoteView) OtherReplicaRefusal() error {
	for _, c := range v.Candidates {
		if c.Writable {
			return &PromoteWritableError{Code: "writable", Name: c.Name}
		}
	}
	for _, c := range v.Candidates {
		if c.WritableSilent {
			return &PromoteWritableError{Code: "writable_silent", Name: c.Name}
		}
	}
	if v.Mode != proto.PromoteEmergency {
		for _, c := range v.Candidates {
			if c.Unread {
				return &PromoteWritableError{Code: "unreadable", Name: c.Name}
			}
		}
	}
	return nil
}

// promoteRefusals is every refusal of StartPromote between the read of
// the topology and the gate, for p against view; callers hold startMu.
// Its order is StartPromote's own as it was: a cutover still pending, an
// unlock of the master on its way (P11; StartPromote also asks before it
// reads the agents), the mode, the emergency's tick that the master is
// dead (D5), a planned promote's old master not yet settled (T4), the
// candidate a replica of this topology, branched replicas, another
// replica writable or not readable (T24, T24b, T24c), an earlier
// promotion unfinished, the promote lock (an emergency's clock rule, a
// planned promote's lock records or locked master), the candidate's
// eligibility, the master's agent version (SEC-62), a partial candidate,
// unreachable replicas, and another job of the topology or its servers.
// It sends nothing to an agent: it judges the view it is given. own is
// the promotion of the job a Retry runs again (0 for a start): it is not
// "an earlier promotion" (I-1). cand is the chosen candidate's row, mode
// the promotion's.
func (r *Runner) promoteRefusals(p PromoteParams, view PromoteView, topo store.Topology, own int64) (cand Candidate, mode string, err error) {
	if topo.CutoverPending {
		return Candidate{}, "", errCutoverStillPending
	}
	if err := r.refuseUnlockInFlight(topo.MasterServerID); err != nil {
		return Candidate{}, "", err
	}
	mode = view.Mode
	switch p.Mode {
	case "", mode:
	case proto.PromotePlanned:
		return Candidate{}, "", errors.New("promote terencana butuh agent master tersambung dan MySQL master hidup; status master sekarang hanya mengizinkan promote darurat")
	case proto.PromoteEmergency:
		return Candidate{}, "", errors.New("master masih sehat; jalankan promote terencana")
	default:
		return Candidate{}, "", fmt.Errorf("mode promote tidak dikenal: %q", p.Mode)
	}
	// An emergency while the old master still takes writes makes two
	// masters: the user ticks that it is dead or cut off (D5).
	if mode == proto.PromoteEmergency && !p.OldMasterDeadAck {
		return Candidate{}, "", ErrOldMasterAck
	}
	// An emergency is never held back by it: the master is gone.
	if mode == proto.PromotePlanned {
		if err := r.refuseOldMasterPending(topo); err != nil {
			return Candidate{}, "", err
		}
	}
	rep, err := r.d.Store.GetReplicaByServer(p.CandidateServerID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rep.TopologyID != topo.ID) {
		return Candidate{}, "", errors.New("kandidat bukan replika master ini")
	}
	if err != nil {
		return Candidate{}, "", err
	}
	i := slices.IndexFunc(view.Candidates, func(c Candidate) bool { return c.ServerID == p.CandidateServerID })
	if i < 0 {
		return Candidate{}, "", errors.New("kandidat bukan replika master ini")
	}
	cand = view.Candidates[i]
	if view.Branched && !p.AcceptBranched {
		return Candidate{}, "", errors.New("replika bercabang: masing-masing punya transaksi yang tidak dimiliki yang lain, " +
			"sehingga tidak ada replika yang memuat semuanya. Pilih satu dan setujui bahwa replika lain yang bercabang " +
			"dibangun ulang dari master baru")
	}
	// A replica that takes writes may be a master the panel does not
	// know about (an earlier promotion whose outcome was lost): read live
	// by PromoteView, never passed, also not by an emergency (R-198).
	if err := view.OtherReplicaRefusal(); err != nil {
		return Candidate{}, "", err
	}
	if prev, err := r.d.Store.LatestPromotion(topo.ID); err == nil && (own == 0 || prev.ID != own) &&
		(prev.Status == promotionRunning || prev.Status == promotionPartial) {
		return Candidate{}, "", errors.New("promote sebelumnya di topologi ini belum tuntas; buka pekerjaannya dan tekan Ulangi")
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Candidate{}, "", err
	}
	if mode == proto.PromoteEmergency {
		if err := r.refuseWhileLockMayStand(p.MasterServerID, verbEmergency); err != nil {
			return Candidate{}, "", err
		}
	} else if err := r.refusePlannedWhileLockStands(p.MasterServerID); err != nil {
		return Candidate{}, "", err
	}
	if !cand.Eligible {
		return Candidate{}, "", fmt.Errorf("%s tidak bisa dipromosikan: %s", cand.Name, cand.Reason)
	}
	// SEC-62: on a planned promotion the master's agent takes part too.
	if mode == proto.PromotePlanned && r.agentOutdated(view.Master) {
		return Candidate{}, "", fmt.Errorf("agent master %s: Agent perlu dimutakhirkan", view.Master.Name)
	}
	if cand.Partial && !p.AcceptPartial {
		missing := "sebagian database"
		if len(cand.MissingDatabases) > 0 {
			missing = strings.Join(nameList(cand.MissingDatabases), ", ")
		}
		return Candidate{}, "", fmt.Errorf("%s hanya mereplikasi sebagian database; %s tidak akan ada di master baru. "+
			"Setujui itu dulu untuk melanjutkan", cand.Name, missing)
	}
	var unreachable []string
	for _, c := range view.Candidates {
		if c.ServerID != cand.ServerID && candidateUnreachable(c) && !slices.Contains(p.AcceptUnreachable, c.ServerID) {
			unreachable = append(unreachable, c.Name)
		}
	}
	if len(unreachable) > 0 {
		return Candidate{}, "", fmt.Errorf("replika berikut tidak terjangkau: %s. Setujui bahwa replika itu diarahkan ulang ke master baru "+
			"saat tersambung lagi untuk melanjutkan", strings.Join(unreachable, ", "))
	}
	if _, busy, err := r.d.Store.ActiveJobForTopology(topo.ID); err != nil {
		return Candidate{}, "", err
	} else if busy {
		return Candidate{}, "", fmt.Errorf("topologi ini %w", ErrBusy)
	}
	if err := r.refuseBusy(p.MasterServerID, "master"); err != nil {
		return Candidate{}, "", err
	}
	reps, err := r.d.Store.ListReplicas(topo.ID)
	if err != nil {
		return Candidate{}, "", err
	}
	for _, x := range reps {
		who := "replika "
		if srv, err := r.d.Store.GetServer(x.ServerID); err == nil && srv.Role == store.RoleQuarantined {
			who = "master lama " // an old master's row (its rebuild), no replica (promoteReplicas)
		}
		if err := r.refuseBusy(x.ServerID, who+r.nameOf(x.ServerID)); err != nil {
			return Candidate{}, "", err
		}
	}
	return cand, mode, nil
}
