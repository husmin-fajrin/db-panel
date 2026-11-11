package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Health is what the Runner needs of the panel's Monitor.
type Health interface {
	// CheckNow asks the agent for a fresh report (rate limited).
	CheckNow(ctx context.Context, serverID int64) error
	View(serverID int64) (monitor.View, bool)
}

// Candidate is one replica on the consideration screen (spec 5.2).
type Candidate struct {
	ServerID int64
	Name     string
	Status   string // the Monitor's status code
	LagSec   *int64
	// GapToMaster: transactions of LastMasterGTID the replica neither
	// executed nor received (0 when the master's set is unknown);
	// GapToLastReport the same against the master's last health report.
	GapToMaster     int
	GapToLastReport int
	// ApplyBacklog: received but not executed yet (applied before the
	// promotion opens writes).
	ApplyBacklog int
	// Partial: the replica follows only some databases; MissingDatabases
	// are the master's databases it does not have.
	Partial          bool
	MissingDatabases []string
	Fresh            bool          // its last report is recent enough to judge it
	ReportAgo        time.Duration // age of its last report; 0 when none
	Eligible         bool
	Reason           string // why it cannot be chosen, in Indonesian; "" when Eligible
	MostCurrent      bool   // holds every transaction any other eligible replica holds
	// Branched: the replicas are branched (PromoteView.Branched) and this
	// one holds transactions no other eligible replica covers; choosing
	// one of them needs PromoteParams.AcceptBranched.
	Branched bool
	// Writable: its last report shows super_read_only off; it may already
	// be a master the panel does not know about, and StartPromote refuses.
	Writable bool
	// ProbeOK is whether the master answered the replica's probe of its
	// pinned source (mysql.probeSource); nil when not asked (agent
	// offline) or when the probe itself failed (ProbeErr).
	ProbeOK  *bool
	ProbeErr string
}

// PromoteView is the consideration screen's data for promoting a replica
// of a master.
type PromoteView struct {
	// Mode is proto.PromotePlanned when the master's agent is online and
	// its MySQL answers, proto.PromoteEmergency otherwise.
	Mode         string
	Master       store.Server
	LastReportAt *time.Time // the master's last health report
	// LastMasterGTID is the master's gtid_executed: from its binary logs
	// when its agent answered mysql.binlogTail (exact), else from its last
	// report ("" when none).
	LastMasterGTID string
	BinlogTail     *proto.BinlogTailResult
	Candidates     []Candidate
	// Branched: no eligible replica holds every transaction the others
	// hold; a promotion is refused (spec 5.3).
	Branched   bool
	WritePause string // what the promotion means for writes, in Indonesian
}

// Reasons a replica cannot be chosen.
const (
	reasonAgentOffline  = "Agent offline"
	reasonAgentOutdated = "Agent perlu dimutakhirkan"
	// reasonStartLock: a start lock left by an earlier quarantine is still
	// in place on the candidate; promoting it would leave a master that
	// turns read-only on at its next start.
	reasonStartLock = "Kunci start dari karantina lama masih terpasang di server ini; agent-nya belum bisa melepasnya"
)

const (
	viewCheckTimeout = 10 * time.Second
	probeTimeout     = 15 * time.Second
	binlogTailTime   = 30 * time.Second
)

// PromoteView gathers the consideration data of masterServerID's
// topology: it asks the Monitor for fresh reports of the master and every
// replica, each live replica to probe its pinned source, and the master's
// agent (when online) for its binary log's GTID set; the mode follows the
// master's status.
func (r *Runner) PromoteView(ctx context.Context, masterServerID int64) (PromoteView, error) {
	master, err := r.d.Store.GetServer(masterServerID)
	if err != nil {
		return PromoteView{}, err
	}
	topo, err := r.d.Store.GetTopologyByMaster(masterServerID)
	if errors.Is(err, store.ErrNotFound) {
		return PromoteView{}, errors.New("server ini bukan master")
	}
	if err != nil {
		return PromoteView{}, err
	}
	reps, err := r.d.Store.ListReplicas(topo.ID)
	if err != nil {
		return PromoteView{}, err
	}
	v := PromoteView{Master: master}

	// Fresh reports first (the Monitor's rate limit applies).
	if r.d.Health != nil {
		var wg sync.WaitGroup
		for _, id := range append([]int64{masterServerID}, replicaIDs(reps)...) {
			if !r.d.Hub.IsOnline(id) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, viewCheckTimeout)
				defer cancel()
				if err := r.d.Health.CheckNow(cctx, id); err != nil {
					r.log.Debug("check now for promote", "server_id", id, "err", logErr(err))
				}
			}()
		}
		wg.Wait()
	}

	mv, _ := r.view(masterServerID)
	masterOnline := r.d.Hub.IsOnline(masterServerID)
	v.Mode = proto.PromoteEmergency
	if masterOnline && mv.Snap != nil && mv.Snap.MySQLOK {
		switch mv.Status.Code {
		// Menunggu laporan pertama: the last report predates a reconnect.
		case monitor.StatusDown, monitor.StatusUnreachable, monitor.StatusAgentOffline, monitor.StatusAwaitingReport:
		default:
			v.Mode = proto.PromotePlanned
		}
	}
	if !mv.ReceivedAt.IsZero() {
		t := mv.ReceivedAt
		v.LastReportAt = &t
	}
	reportGTID, masterUUID := "", ""
	if mv.Snap != nil {
		reportGTID, masterUUID = mv.Snap.GTIDExecuted, mv.Snap.ServerUUID
	}
	v.LastMasterGTID = reportGTID
	if masterOnline {
		cctx, cancel := context.WithTimeout(ctx, binlogTailTime)
		var bt proto.BinlogTailResult
		err := r.d.Hub.Call(cctx, masterServerID, "mysql.binlogTail", nil, &bt)
		cancel()
		if err == nil {
			v.BinlogTail = &bt
			if bt.GTIDExecuted != "" {
				v.LastMasterGTID = bt.GTIDExecuted
			}
		} else {
			r.log.Debug("binlog tail for promote", "server_id", masterServerID, "err", logErr(err))
		}
	}
	var masterDBs []string
	if inv, err := r.d.Store.GetInventory(masterServerID); err == nil {
		for _, d := range inv.Databases {
			masterDBs = append(masterDBs, d.Name)
		}
	}
	fresh := r.freshBound()

	// The probes of the live replicas, in parallel.
	type probe struct {
		ok  *bool
		err string
	}
	probes := make([]probe, len(reps))
	var wg sync.WaitGroup
	for i, rep := range reps {
		if !r.d.Hub.IsOnline(rep.ServerID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			var res proto.ProbeSourceResult
			if err := r.d.Hub.Call(cctx, rep.ServerID, "mysql.probeSource", nil, &res); err != nil {
				probes[i].err = truncate(err.Error(), maxLogLine)
				return
			}
			ok := res.Reachable
			probes[i].ok = &ok
		}()
	}
	wg.Wait()

	lastMaster, lastMasterOK := parseGTIDSet(v.LastMasterGTID)
	lastMasterOK = lastMasterOK && v.LastMasterGTID != ""
	report, reportOK := parseGTIDSet(reportGTID)
	reportOK = reportOK && reportGTID != ""
	now := r.now()
	sets := make([]gtidSet, len(reps)) // what each replica will hold once it applied its relay log
	for i, rep := range reps {
		srv, err := r.d.Store.GetServer(rep.ServerID)
		if err != nil {
			return PromoteView{}, err
		}
		c := Candidate{ServerID: rep.ServerID, Name: srv.Name, ProbeOK: probes[i].ok, ProbeErr: probes[i].err}
		rv, ok := r.view(rep.ServerID)
		if ok {
			c.Status, c.LagSec = rv.Status.Code, rv.Status.LagSec
			if !rv.ReceivedAt.IsZero() {
				c.ReportAgo = max(now.Sub(rv.ReceivedAt), 0)
				c.Fresh = c.ReportAgo <= fresh
			}
		}
		var executed, held gtidSet
		setsOK := false
		if rv.Snap != nil && rv.Snap.MySQLOK {
			exec, retr := rv.Snap.GTIDExecuted, ""
			if rv.Snap.Replica != nil {
				retr = rv.Snap.Replica.RetrievedGTID
				if rv.Snap.Replica.ExecutedGTID != "" {
					exec = rv.Snap.Replica.ExecutedGTID
				}
			}
			e, ok1 := parseGTIDSet(exec)
			rs, ok2 := parseGTIDSet(retr)
			c.Writable = !rv.Snap.SuperReadOnly
			if ok1 && ok2 && exec != "" {
				executed, held, setsOK = e, e.union(rs), true
				c.ApplyBacklog = rs.minusCount(e, "")
				if lastMasterOK {
					c.GapToMaster = lastMaster.minusCount(held, "")
				}
				if reportOK {
					c.GapToLastReport = report.minusCount(held, "")
				}
			}
		}
		if len(rep.Databases) > 0 {
			c.Partial = true
			for _, d := range masterDBs {
				if !slices.Contains(rep.Databases, d) {
					c.MissingDatabases = append(c.MissingDatabases, d)
				}
			}
		}
		online := r.d.Hub.IsOnline(rep.ServerID)
		if !online && c.Status == "" {
			c.Status = monitor.StatusAgentOffline
		}
		switch {
		case !online:
			c.Reason = reasonAgentOffline
		case r.agentOutdated(srv):
			c.Reason = reasonAgentOutdated
		case rv.Snap != nil && rv.Snap.StartLock != nil && rv.Snap.StartLock.Present:
			c.Reason = reasonStartLock
		case rep.State != store.ReplicaReady && rep.State != store.ReplicaAttention:
			c.Reason = "Replika " + replicaStateText(rep.State)
		case rep.Paused || c.Status == monitor.StatusPaused:
			c.Reason = "Dihentikan"
		case c.Status == monitor.StatusStopped:
			c.Reason = "Berhenti dengan error"
		case c.Status == monitor.StatusStoppedOutside:
			c.Reason = "Dihentikan di luar panel"
		case !ok || rv.ReceivedAt.IsZero():
			c.Reason = "Belum ada laporan kesehatan"
		case !c.Fresh:
			c.Reason = fmt.Sprintf("Data tidak segar (laporan terakhir %s lalu)", c.ReportAgo.Round(time.Second))
		case !setsOK:
			c.Reason = "MySQL replika tidak menjawab"
		case lastMasterOK && errantCount(executed, lastMaster, masterUUID, v.Mode) > 0:
			c.Reason = "Mempunyai transaksi di luar master"
		}
		c.Eligible = c.Reason == ""
		if c.Eligible {
			sets[i] = held
		}
		v.Candidates = append(v.Candidates, c)
	}

	// Most current: eligible replicas holding everything the other
	// eligible ones hold; none while two or more are eligible = branched.
	var eligible []int
	for i := range v.Candidates {
		if v.Candidates[i].Eligible {
			eligible = append(eligible, i)
		}
	}
	found := false
	for _, i := range eligible {
		superset := true
		for _, j := range eligible {
			if i != j && sets[j].minusCount(sets[i], "") > 0 {
				superset = false
				break
			}
		}
		v.Candidates[i].MostCurrent = superset
		found = found || superset
	}
	v.Branched = !found && len(eligible) > 1
	if v.Branched {
		// The maximal ones: no other eligible replica holds all of theirs.
		for _, i := range eligible {
			maximal := true
			for _, j := range eligible {
				if i != j && sets[i].minusCount(sets[j], "") == 0 && sets[j].minusCount(sets[i], "") > 0 {
					maximal = false
					break
				}
			}
			v.Candidates[i].Branched = maximal
		}
	}

	v.WritePause = r.writePause(v)
	return v, nil
}

// errantCount counts the replica's executed transactions the master
// lacks. In an emergency the master's set is from its last report, older
// than what the replica may have received from it since: the master's own
// transactions are left out then.
func errantCount(executed, master gtidSet, masterUUID, mode string) int {
	skip := ""
	if mode == proto.PromoteEmergency {
		if masterUUID == "" {
			return 0
		}
		skip = masterUUID
	}
	return executed.minusCount(master, skip)
}

func replicaIDs(reps []store.Replica) []int64 {
	out := make([]int64, len(reps))
	for i, rep := range reps {
		out[i] = rep.ServerID
	}
	return out
}

// view is the Monitor's view of id; the zero View when unknown.
func (r *Runner) view(id int64) (monitor.View, bool) {
	if r.d.Health == nil {
		return monitor.View{}, false
	}
	return r.d.Health.View(id)
}

// freshBound is how old a report may be to judge a replica: twice the
// check interval (spec 5.2).
func (r *Runner) freshBound() time.Duration {
	set, err := monitor.LoadSettings(r.d.Store)
	if err != nil {
		set = monitor.DefaultSettings()
	}
	return 2 * set.CheckEvery
}

// writePause says what the promotion means for the applications' writes.
func (r *Runner) writePause(v PromoteView) string {
	if v.Mode == proto.PromoteEmergency {
		return "Aplikasi sudah tidak bisa menulis sejak master hilang; penulisan berjalan lagi setelah alamat di tiap aplikasi " +
			"diganti ke master baru."
	}
	s := "Aplikasi tidak bisa menulis sejak langkah \"Kunci master\"; master baru menerima tulisan setelah langkah " +
		"\"Jadikan master baru\", dan aplikasi menulis lagi setelah alamat di tiap aplikasi diganti ke master baru."
	for _, c := range v.Candidates {
		if c.MostCurrent && c.LagSec != nil {
			s += fmt.Sprintf(" Kandidat paling mutakhir tertinggal sekitar %d detik.", *c.LagSec)
			break
		}
	}
	return s
}
