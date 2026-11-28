package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	pairTokenTTL = 24 * time.Hour
	// repairTokenTTL is how long a paired server's re-pairing token lasts
	// (spec 5.1): its command is shown once, so a lost one is made again.
	repairTokenTTL    = 15 * time.Minute
	msgServerNotFound = "Server tidak ditemukan"
	msgLocalDelete    = "Server ini adalah host panel dan tidak bisa dihapus"
	msgLocalToken     = "Server ini adalah host panel; agent-nya dipasangkan oleh install.sh panel"
	// msgRepairConfirm is T3 (spec 5.11), the confirmation of a
	// re-pairing; %s is the server's name.
	msgRepairConfirm = "Pasang ulang agent di %s? Agent yang sekarang tetap bekerja sampai agent baru terpasang. " +
		"Perintah pemasangannya ditampilkan sekali dan berlaku 15 menit."
	// msgRepairActive is T3b (spec 5.11), what the server page and the
	// server list say while a re-pairing token is active; %s is the time
	// it ends.
	msgRepairActive = "Token pasang ulang aktif sampai pukul %s. Perintahnya hanya ditampilkan sekali; bila hilang, buat token baru."
)

// repairActiveText is T3b for a re-pairing token that ends at expires.
func repairActiveText(expires time.Time) string {
	return fmt.Sprintf(msgRepairActive, expires.Local().Format("15:04"))
}

// isLocal reports whether srv is the panel host's own server.
func (a *app) isLocal(srv store.Server) bool {
	return a.LocalServerID != 0 && srv.ID == a.LocalServerID
}

// dashboardPage is the dashboard (design spec 5.6, 8.2): four gauges
// over every topology, a tab per topology with its lag chart and its
// card, the newest alerts and jobs, and the promote banners.
type dashboardPage struct {
	Page
	Gauges     [4]Gauge
	Topologies []topologyPanel
	Alerts     []alertRow
	Jobs       []jobRow
	// Empty: no topology yet; the page offers to start one.
	Empty bool
	// Summary is the line under the title; Updated says how long ago the
	// newest health report came ("" before any).
	Summary, Updated string
	// Banners are the promote_partial and cutover_pending messages above
	// the page (Tasks 9 and 10 of plan 4b).
	Banners []Banner
	Now     time.Time // the lists' "N menit lalu"
}

// topologyPanel is one topology's tab: its card and its lag chart. Name
// is the master's.
type topologyPanel struct {
	ID       int64
	Name     string
	Master   masterCard
	Replicas []replicaCard
	Chart    LagChart
}

// masterCard is the master of a topology with its status and, when its
// agent reported one, its data directory's disk.
type masterCard struct {
	Server   store.Server
	Status   statusView
	DiskPct  int // percent used; -1 when unknown
	DiskFree uint64
}

// replicaCard is a replica of a topology with its status; LagSec is the
// monitor's lag in seconds, nil when unknown.
type replicaCard struct {
	Server store.Server
	Status statusView
	LagSec *int64
}

// Badge is the badge of an alert on the dashboard: Kritis or
// Peringatan while open, Pulih or Ditutup once closed.
func (r alertRow) Badge() Badge {
	switch {
	case r.ClosedAt != nil && r.CloseReason != "":
		return Badge{Tone: "neutral", Text: "Ditutup"}
	case r.ClosedAt != nil:
		return Badge{Tone: "success", Text: "Pulih"}
	case notify.Critical(r.Kind):
		return Badge{Tone: "error", Text: "Kritis"}
	}
	return Badge{Tone: "warning", Text: "Peringatan"}
}

// Badge is the job's status as a badge.
func (r jobRow) Badge() Badge {
	return Badge{Tone: strings.TrimPrefix(statusClass(r.Status), "lencana-"), Text: statusLabel(r.Status)}
}

type serverRow struct {
	store.Server
	Online       bool
	MySQLVersion string
	OS           string
	Status       statusView
	Local        bool // the panel host: its agent is installed with the panel
	// NeedsUpgrade: the agent is too old for monitoring; the server page
	// makes the command that reinstalls it. RepairActive is T3b while a
	// re-pairing token is active, else "": the list never shows the
	// command (spec 5.1).
	NeedsUpgrade bool
	RepairActive string
}

type serversPage struct {
	Page
	Rows []serverRow
}

type serverNewPage struct {
	Page
	Name string
}

type serverDetailPage struct {
	Page
	Server store.Server
	Online bool
	Probe  *proto.ProbeResult // nil until the first probe
	// Command is the pairing command: for an unpaired server while its
	// token is active, and for a paired one only in the answer to the post
	// that created its re-pairing token (spec 5.1: shown once).
	Command      string
	TokenExpires time.Time
	// TokenHidden (unpaired servers): a token is active, but only its hash
	// is stored and this panel process did not create it (it was restarted
	// since), so the command cannot be shown; a new token has to be
	// created.
	TokenHidden bool
	// RepairActive (paired servers) is T3b while a re-pairing token is
	// active and the page is not the answer that shows its command; "".
	RepairActive string
	// RepairConfirm is T3, the text of the confirmed re-pairing buttons.
	RepairConfirm string
	Local         bool // the panel host itself: no delete, no re-pairing

	// MySQL card (paired servers).
	Inventory  store.Inventory // Connected false until "Hubungkan MySQL"
	ShowMySQL  bool            // MySQL is (or may be) installed: offer the account form
	CanPrepare bool            // connected, no missing grants, not a replica
	Connect    connectForm     // the account form after a failed attempt
	ActiveJob  *store.Job      // the server's unfinished job, if any
	// PrepareAgain: the server already is a master (it has a topology), so
	// preparing it again asks for the password.
	PrepareAgain bool
	// PrepareGate is the precondition list of Siapkan sebagai master (red
	// rows only); nil when it is not offered.
	PrepareGate *gateView
	// InventoryCut: the inventory holds as many databases as the panel
	// keeps; the list may be cut.
	InventoryCut bool
	Controls     serverControls // Periksa sekarang and Jangan ganggu (paired servers)
	// MySQLCert is the MySQL certificate the panel installed; nil when
	// none. CanInstallCert offers "Pasang sertifikat MySQL" (connected, no
	// missing grants).
	MySQLCert      *store.ServerTLS
	CanInstallCert bool

	// After a promotion (spec 5.4): the box of a quarantined old master
	// (nil otherwise) and its salvage files (nil when it never was one).
	OldMaster *oldMasterBox
	Salvage   *salvageFiles
	// DeleteBlocked says why Hapus server is not offered now ("" when it
	// is); DeleteUnquarantined: the delete is "Hapus tanpa karantina".
	DeleteBlocked       string
	DeleteUnquarantined bool
	// Pins is what the agent's last report says of its pins (Detail
	// teknis); nil before a report that carries them.
	Pins *pinFacts
	// StartLock is the start lock as the agent last reported it (Detail
	// teknis); nil without one.
	StartLock *startLockFacts
	// ReportsDropped says how many health reports the hub dropped over its
	// rate limit in the last hour (Detail teknis); "" when none.
	ReportsDropped string
	// DeleteWarnings: what deleting the server from the panel loses (an
	// old master's leftover data, its salvage files); shown in the modal
	// and the confirmation.
	DeleteWarnings []string
	// ServiceLine is the MySQL card's Layanan row from the probe's systemd
	// unit and the last health report (design spec 5.8 #3); nil without a
	// report that says whether MySQL answers (none yet, or no MySQL
	// account connected), when the row shows the unit alone.
	ServiceLine *serviceLine
	// Backup is the backup card (paired servers): the folder form and the
	// folder's state; nil on an unpaired server.
	Backup *backupCard
}

// dashboardListLen is how many alerts and jobs the dashboard lists.
const dashboardListLen = 3

// alertTitleMax is the most characters of the newest alert's title under
// the alerts gauge.
const alertTitleMax = 48

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	open, err := a.Store.ListOpenAlerts()
	if err != nil {
		a.internalError(w, "list open alerts", err)
		return
	}
	topos, err := a.Store.ListTopologies()
	if err != nil {
		a.internalError(w, "list topologies", err)
		return
	}
	now := a.Now()
	p := dashboardPage{Page: a.page(r), Now: now}
	var cutover int
	if p.Banners, cutover, err = a.dashboardBanners(servers, topos, open); err != nil {
		a.internalError(w, "dashboard banners", err)
		return
	}
	set := a.monitorSettings()
	if p.Topologies, err = a.topologyPanels(servers, topos, now, int(set.Lag/time.Second)); err != nil {
		a.internalError(w, "topology panels", err)
		return
	}
	p.Empty = len(p.Topologies) == 0
	p.Gauges = dashboardGauges(p.Topologies, open, set)

	recent, err := a.Store.ListAlerts(now.AddDate(0, 0, -alertHistoryDays), dashboardListLen)
	if err != nil {
		a.internalError(w, "list alerts", err)
		return
	}
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	for _, al := range recent {
		row := alertRow{Alert: al, KindLabel: alertKindLabel(al.Kind)}
		if srv, ok := byID[al.ServerID]; ok {
			row.ServerName, row.ServerKnown = srv.Name, true
		}
		p.Alerts = append(p.Alerts, row)
	}
	jobs, err := a.Store.ListJobs(dashboardListLen)
	if err != nil {
		a.internalError(w, "list jobs", err)
		return
	}
	if p.Jobs, err = a.jobRows(jobs); err != nil {
		a.internalError(w, "job rows", err)
		return
	}
	running, err := a.Store.RunningJobs()
	if err != nil {
		a.internalError(w, "running jobs", err)
		return
	}
	waiting := 0
	for _, j := range running {
		if j.Status == store.JobWaiting {
			waiting++
		}
	}
	p.Summary = dashboardSummary(len(open), waiting+cutover, p.Empty)
	var newest time.Time
	for _, v := range a.Monitor.Views() {
		if v.ReceivedAt.After(newest) {
			newest = v.ReceivedAt
		}
	}
	if !newest.IsZero() {
		p.Updated = "Diperbarui " + agoWords(now.Sub(newest))
	}
	a.render.Render(w, "dashboard", p)
}

// dashboardSummary is the line under the dashboard's title: the open
// alerts and the actions waiting for the user (jobs waiting for an
// approval, promotions waiting for the cutover).
func dashboardSummary(alerts, actions int, empty bool) string {
	switch {
	case alerts == 0 && actions == 0 && empty:
		return "Belum ada replikasi yang dipantau. Tidak ada tindakan yang menunggu Anda."
	case alerts == 0 && actions == 0:
		return "Replikasi berjalan normal. Tidak ada tindakan yang menunggu Anda."
	case actions == 0:
		return fmt.Sprintf("%d peringatan terbuka. Tidak ada tindakan yang menunggu Anda.", alerts)
	case alerts == 0:
		return fmt.Sprintf("Tidak ada peringatan terbuka. %d tindakan menunggu Anda.", actions)
	}
	return fmt.Sprintf("%d peringatan terbuka dan %d tindakan menunggu Anda.", alerts, actions)
}

// dashboardBanners are the messages above the dashboard: a promotion
// waiting for the applications to be cut over (topologies.cutover_pending,
// returned counted) and a promotion that stopped partway (open
// promote_partial alerts, with the job to retry it from).
func (a *app) dashboardBanners(servers []store.Server, topos []store.Topology, open []store.Alert) ([]Banner, int, error) {
	var out []Banner
	cutover := 0
	for _, t := range topos {
		if !t.CutoverPending {
			continue
		}
		i := slices.IndexFunc(servers, func(s store.Server) bool { return s.ID == t.MasterServerID })
		if i < 0 {
			continue
		}
		cutover++
		m := servers[i]
		out = append(out, Banner{Tone: "warning", Kind: BannerCutover, Name: m.Name, ServerID: m.ID})
	}
	for _, al := range open {
		if al.Kind != monitor.KindPromotePartial {
			continue
		}
		b := Banner{Tone: "error", Kind: BannerPartial, Message: al.Message}
		if prom, err := a.Store.LatestPromotion(al.TopologyID); err == nil && prom.JobID != "" {
			b.JobID = prom.JobID
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, cutover, nil
}

// topologyPanels is one panel per topology: its master and replicas with
// their status, and its replicas' lag over the last 24 hours.
func (a *app) topologyPanels(servers []store.Server, topos []store.Topology, now time.Time, thresholdSec int) ([]topologyPanel, error) {
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	panels := make([]topologyPanel, 0, len(topos))
	for _, t := range topos {
		master := byID[t.MasterServerID]
		v, ok := a.Monitor.View(master.ID)
		mc := masterCard{Server: master, Status: newStatusView(master.ID, master.Role, v, ok, now), DiskPct: -1}
		if pct, known := monitor.DiskPercent(v.Snap); ok && known {
			// Rounded down as the disk alerts judge it, so the gauge
			// never turns red without a disk_crit alert.
			mc.DiskPct, mc.DiskFree = pct, v.Snap.DiskFree
		}
		panel := topologyPanel{ID: t.ID, Name: master.Name, Master: mc}
		reps, err := a.listedReplicas(t.ID, byID)
		if err != nil {
			return nil, err
		}
		for _, rep := range reps {
			srv := byID[rep.ServerID]
			v, ok := a.Monitor.View(srv.ID)
			rc := replicaCard{Server: srv, Status: newStatusView(srv.ID, srv.Role, v, ok, now)}
			if ok && v.Status.Code != "" {
				rc.LagSec = v.Status.LagSec
			}
			panel.Replicas = append(panel.Replicas, rc)
		}
		if panel.Chart, err = a.lagChart(panel.Replicas, now, thresholdSec); err != nil {
			return nil, err
		}
		panels = append(panels, panel)
	}
	return panels, nil
}

// listedReplicas are the replica rows of topology topoID that the replica
// lists (dashboard, Replikasi, master page) show: a quarantined server (an
// old master whose rebuild runs or failed before its settle) is shown by
// its own Master lama box, not as a replica card (spec 5.1). byID holds
// the servers.
func (a *app) listedReplicas(topoID int64, byID map[int64]store.Server) ([]store.Replica, error) {
	reps, err := a.Store.ListReplicas(topoID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(reps, func(rep store.Replica) bool {
		return byID[rep.ServerID].Role == store.RoleQuarantined
	}), nil
}

// chartBuckets is how many buckets the dashboard's lag chart spans: 24
// hours of chartBucket (design spec 5.7, at most 288 points per series).
const chartBuckets = int(24 * time.Hour / chartBucket)

// chartFrom is the start of the lag chart ending at now: the bucket
// boundary chartBuckets-1 buckets before now's bucket, so the window holds
// exactly chartBuckets buckets, now's included.
func chartFrom(now time.Time) time.Time {
	return now.Truncate(chartBucket).Add(-time.Duration(chartBuckets-1) * chartBucket)
}

// lagChart is the lag of reps over the last 24 hours in buckets of
// chartBucket (design spec 5.7); it is sparse when a replica has no sample
// in the last hour, read from the same buckets (a bucket that ends after
// now−1h counts, so up to one bucket of slack).
func (a *app) lagChart(reps []replicaCard, now time.Time, thresholdSec int) (LagChart, error) {
	from := chartFrom(now)
	ids := make([]int64, 0, len(reps))
	for _, rc := range reps {
		ids = append(ids, rc.Server.ID)
	}
	byServer, err := a.Store.LagSeries(ids, from, now, chartBucket)
	if err != nil {
		return LagChart{}, err
	}
	sparse := false
	series := make([]ChartSeries, 0, len(reps))
	for _, rc := range reps {
		s := ChartSeries{Name: rc.Server.Name}
		for _, p := range byServer[rc.Server.ID] {
			s.Points = append(s.Points, ChartPoint{T: p.T, Lag: p.Lag})
		}
		series = append(series, s)
		pts := byServer[rc.Server.ID]
		recent := len(pts) > 0 && pts[len(pts)-1].T.Add(chartBucket).After(now.Add(-time.Hour))
		sparse = sparse || !recent
	}
	return newLagChart(series, from, now, thresholdSec, sparse), nil
}

// monitorSettings are the stored monitoring settings (thresholds); the
// defaults when they cannot be read.
func (a *app) monitorSettings() monitor.Settings {
	set, err := monitor.LoadSettings(a.Store)
	if err != nil {
		a.Logger.Warn("read monitor settings", "err", err)
		return monitor.DefaultSettings()
	}
	return set
}

// dashboardGauges are the dashboard's four gauges over every topology
// (design spec 8.2): replicas Sinkron of all; the highest lag and its
// replica against lag_threshold_sec; the fullest master disk and its
// master against disk_warn_pct and disk_crit_pct; the open alerts and the
// newest one's title.
func dashboardGauges(topos []topologyPanel, openAlerts []store.Alert, set monitor.Settings) [4]Gauge {
	lagThreshold := int64(set.Lag / time.Second)
	names := map[int64]string{}
	var (
		synced, total int
		catchingUp    int
		critical      bool
		top           *replicaCard
		worst         *masterCard
	)
	for i := range topos {
		t := &topos[i]
		names[t.Master.Server.ID] = t.Master.Server.Name
		if t.Master.DiskPct >= 0 && (worst == nil || t.Master.DiskPct > worst.DiskPct) {
			worst = &t.Master
		}
		for j := range t.Replicas {
			rc := &t.Replicas[j]
			names[rc.Server.ID] = rc.Server.Name
			total++
			// The monitor's code, not the label: a replica shown
			// "Mengejar" is Sinkron to the monitor and the alerts.
			if rc.Status.Code == monitor.StatusSynced {
				synced++
				if rc.Status.Label == labelCatchingUp {
					catchingUp++
				}
			}
			critical = critical || rc.Status.Severity == monitor.SevCrit
			if rc.LagSec != nil && (top == nil || *rc.LagSec > *top.LagSec) {
				top = rc
			}
		}
	}
	var g [4]Gauge

	g[0] = Gauge{Label: "Replika sinkron", Value: fmt.Sprintf("%d/%d", synced, total),
		Aria: fmt.Sprintf("Replika sinkron: %d dari %d", synced, total)}
	switch {
	case total == 0:
		g[0].Tone, g[0].Sub = "neutral", "belum ada replika"
	case critical:
		g[0].Tone = "error"
	case synced < total:
		g[0].Tone = "warning"
	default:
		g[0].Tone = "success"
	}
	if total > 0 {
		g[0].Percent = float64(synced) * 100 / float64(total)
		if synced == total {
			g[0].Sub = "semua mengikuti master"
		} else {
			g[0].Sub = fmt.Sprintf("%d replika perlu perhatian", total-synced)
		}
		if catchingUp > 0 {
			g[0].Sub += fmt.Sprintf(" · %d mengejar", catchingUp)
		}
	}

	g[1] = Gauge{Label: "Lag tertinggi", Value: "-", Tone: "neutral", Sub: "belum ada data lag",
		Aria: "Lag tertinggi: belum ada data"}
	if top != nil {
		lag := *top.LagSec
		g[1].Value = fmt.Sprintf("%ds", lag)
		g[1].Sub = fmt.Sprintf("%s · ambang %d dtk", top.Server.Name, lagThreshold)
		g[1].Aria = fmt.Sprintf("Lag tertinggi %d detik di %s; ambang %d detik", lag, top.Server.Name, lagThreshold)
		g[1].Tone = "success"
		if lag >= lagThreshold {
			g[1].Tone = "warning"
		}
		if lagThreshold > 0 {
			g[1].Percent = float64(lag) * 100 / float64(lagThreshold)
		}
	}

	g[2] = Gauge{Label: "Disk master", Value: "-", Tone: "neutral", Sub: "belum ada laporan disk",
		Aria: "Disk master terburuk: belum ada laporan"}
	if worst != nil {
		pct := worst.DiskPct
		g[2].Value, g[2].Percent = fmt.Sprintf("%d%%", pct), float64(pct)
		g[2].Sub = worst.Server.Name + " · " + fmtBytes(worst.DiskFree) + " bebas"
		g[2].Aria = fmt.Sprintf("Disk master terburuk %d%% terpakai di %s, %s bebas", pct, worst.Server.Name, fmtBytes(worst.DiskFree))
		switch {
		case pct >= set.DiskCrit:
			g[2].Tone = "error"
		case pct >= set.DiskWarn:
			g[2].Tone = "warning"
		default:
			g[2].Tone = "success"
		}
	}

	n := len(openAlerts)
	g[3] = Gauge{Label: "Peringatan", Value: strconv.Itoa(n), Tone: "success", Percent: 100,
		Sub: "tidak ada peringatan terbuka", Aria: fmt.Sprintf("Peringatan terbuka: %d", n)}
	if n > 0 {
		g[3].Tone = "warning"
		for _, al := range openAlerts {
			if notify.Critical(al.Kind) {
				g[3].Tone = "error"
			}
		}
		newest := openAlerts[0]
		for _, al := range openAlerts[1:] {
			if !al.OpenedAt.Before(newest.OpenedAt) {
				newest = al
			}
		}
		title := alertKindLabel(newest.Kind)
		if name, ok := names[newest.ServerID]; ok {
			title += " · " + name
		}
		g[3].Sub = truncateRunes(title, alertTitleMax)
		g[3].Aria += "; terbaru: " + title
	}
	return g
}

// truncateRunes cuts s to at most n characters, ending with "…" when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func (a *app) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	p := serversPage{Page: a.page(r)}
	p.Notice = notices[r.URL.Query().Get("pesan")]
	now := a.Now()
	for _, s := range servers {
		row := serverRow{Server: s, Online: s.Paired && a.Hub.IsOnline(s.ID), Local: a.isLocal(s)}
		if probe := a.probe(s); probe != nil {
			row.OS = probe.OS
			if probe.MySQL.Installed {
				row.MySQLVersion = probe.MySQL.Version
			}
		}
		v, ok := a.Monitor.View(s.ID)
		row.Status = newStatusView(s.ID, s.Role, v, ok, now)
		row.NeedsUpgrade = ok && v.NeedsUpgrade
		if row.NeedsUpgrade && !row.Local {
			tok, found, err := a.Store.ActivePairToken(s.ID, now)
			if err != nil {
				a.internalError(w, "pair token", err)
				return
			}
			if found {
				row.RepairActive = repairActiveText(tok.ExpiresAt)
			}
		}
		p.Rows = append(p.Rows, row)
	}
	a.render.Render(w, "servers", p)
}

func (a *app) newServerForm(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, "server_new", serverNewPage{Page: a.page(r)})
}

func (a *app) createServer(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	srv, err := a.Store.CreateServer(name)
	a.audit(r, "server_create", auditServer, srv.ID, map[string]any{"name": name}, a.outcomeOf(err))
	if err != nil {
		// The store returns unwrapped, user-facing errors for validation
		// (empty or duplicate name) and wrapped ones for database failures.
		if errors.Unwrap(err) != nil {
			a.internalError(w, "create server", err)
			return
		}
		p := serverNewPage{Page: a.page(r), Name: name}
		p.Flash = err.Error()
		a.render.renderStatus(w, http.StatusBadRequest, "server_new", p)
		return
	}
	if _, err := a.Store.CreatePairToken(srv.ID, a.Now(), pairTokenTTL); err != nil {
		a.internalError(w, "create pair token", err)
		return
	}
	a.Logger.Info("server created", "id", srv.ID, "name", srv.Name, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
}

// pinFacts are the agent's pins in words (spec 5.12).
type pinFacts struct {
	// Role is "master", "replica", "dikarantina", "belum ada"; "-" while
	// unreadable; for any other value "tidak dikenal" and the agent's
	// value, labelled (orchestrator.AgentQuoteN) and cut at maxShownPin.
	Role string
	// Source is "tidak ada" without a source pin, "-" while unreadable,
	// a well-formed host:port as it is (hostPortPin), and for any other
	// value "tidak dikenal" and the agent's value, labelled and cut at
	// maxShownPin.
	Source string
	// Error is why the pin files cannot be read, as the agent words it:
	// labelled, one line and cut at maxAgentWords; "" when they can.
	Error string
}

// maxShownPin bounds the agent's value of a role pin the panel does not
// know, and of a source pin that is not host:port.
const maxShownPin = 32

// newPinFacts reads the pins of snap, an agent's report of the server
// named server; nil without a report, or from an agent that reports none.
// The pin error, an unknown role pin and a source pin that is not
// host:port are the agent's text: none is MySQL's (redact.Quotes, which
// censors MySQL's row data in the report's MySQL errors, has nothing to
// censor in them), and each is labelled as the agent's words, one line
// and bounded. Only the page reads these words; the monitor reads the
// report's own pins.
func newPinFacts(server string, snap *proto.HealthSnapshot) *pinFacts {
	if snap == nil || (snap.RolePin == "" && snap.PinError == "") {
		return nil
	}
	f := &pinFacts{Role: snap.RolePin, Source: snap.SourcePin, Error: agentLine(server, snap.PinError)}
	switch f.Role {
	case "":
		f.Role = "-"
	case store.RoleMaster, store.RoleReplica:
	case "quarantined":
		f.Role = "dikarantina"
	case "unassigned":
		f.Role = "belum ada"
	default:
		f.Role = "tidak dikenal (" + orchestrator.AgentQuoteN(server, f.Role, maxShownPin) + ")"
	}
	switch {
	case f.Source == "" && f.Error != "":
		f.Source = "-"
	case f.Source == "":
		f.Source = "tidak ada"
	case !hostPortPin(f.Source):
		f.Source = "tidak dikenal (" + orchestrator.AgentQuoteN(server, f.Source, maxShownPin) + ")"
	}
	return f
}

// sourceHostRe is the agent's rule for a source host (sourceHostRe and
// validSourceHost in internal/agent/drivers/mysql): an IPv4/IPv6 literal
// or a host name of at most 255 characters, with no quote, space, comma
// or zone. A host holding ":" must also be an IP (net.ParseIP).
var sourceHostRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)

// hostPortPin reports whether a source pin reads as host:port: it splits
// (net.SplitHostPort), its host passes the agent's rule for a source host
// (sourceHostRe; with ":" only as an IP) and its port is a number 1-65535
// written without sign or leading zeros. Such a pin is shown as it is; any
// other is the agent's text.
func hostPortPin(pin string) bool {
	host, port, err := net.SplitHostPort(pin)
	if err != nil || !sourceHostRe.MatchString(host) || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

// startLockFacts is the start lock of a quarantined server in words.
type startLockFacts struct {
	Locked bool
	// Reason is the agent's own text for a lock that is not guaranteed,
	// shown as given.
	Reason string
	// OperatorInitFiles are the operator's init_file options MySQL does not
	// run while the lock is in place; data from the agent.
	OperatorInitFiles []string
}

// startLockHolds reports whether the page's latest report says the start
// lock holds (no report, or no start lock in it, counts as not).
func (p *serverDetailPage) startLockHolds() bool {
	return p.StartLock != nil && p.StartLock.Locked
}

// newStartLockFacts reads the start lock of snap; nil without a report or
// from an agent that reports none.
func newStartLockFacts(snap *proto.HealthSnapshot) *startLockFacts {
	if snap == nil || snap.StartLock == nil {
		return nil
	}
	sl := snap.StartLock
	f := &startLockFacts{Locked: sl.Locked, Reason: monitor.AgentText(sl.Reason, maxAgentWords)}
	for i, file := range sl.OperatorInitFiles {
		if i == maxAgentFiles {
			f.OperatorInitFiles = append(f.OperatorInitFiles, fmt.Sprintf("… dan %d lagi", len(sl.OperatorInitFiles)-maxAgentFiles))
			break
		}
		f.OperatorInitFiles = append(f.OperatorInitFiles, monitor.AgentText(file, maxAgentWords))
	}
	return f
}

// renderServer renders the server page with v's status and messages.
func (a *app) renderServer(w http.ResponseWriter, r *http.Request, srv store.Server, v serverView) {
	a.renderServerWith(w, r, srv, v, nil)
}

// pairCommandFor is the pairing command of tok, whose plaintext is known.
func (a *app) pairCommandFor(tok store.PairToken) (string, error) {
	sum, err := a.InstallScriptSHA256(tok.Token)
	if err != nil {
		return "", fmt.Errorf("install script hash: %w", err)
	}
	return pairCommand(a.PublicURL(), tok.Token, sum, a.CertInfo().Pin)
}

// renderServerWith is renderServer with the backup form as posted
// (backupForm's Dir and MaxAgeHours) in place of the stored values; nil
// shows the stored ones.
func (a *app) renderServerWith(w http.ResponseWriter, r *http.Request, srv store.Server, v serverView, backupForm *backupCard) {
	p := serverDetailPage{Page: a.page(r), Server: srv, Local: a.isLocal(srv), Connect: v.Form,
		RepairConfirm: fmt.Sprintf(msgRepairConfirm, srv.Name)}
	p.Flash, p.Notice = v.Flash, v.Notice
	now := a.Now()
	tok, found, err := a.Store.ActivePairToken(srv.ID, now)
	// The panel host's agent pairs with the token the panel wrote for it;
	// a new one here would replace that token.
	if err == nil && !found && !srv.Paired && !p.Local {
		tok, err = a.Store.CreatePairToken(srv.ID, now, pairTokenTTL)
		found = err == nil
	}
	if err != nil {
		a.internalError(w, "pair token", err)
		return
	}
	if srv.Paired {
		p.Online = a.Hub.IsOnline(srv.ID)
		p.Probe = a.probe(srv)
		if p.Inventory, err = a.inventory(srv.ID); err != nil {
			a.internalError(w, "get inventory", err)
			return
		}
		job, active, err := a.Store.ActiveJobForServer(srv.ID)
		if err != nil {
			a.internalError(w, "active job", err)
			return
		}
		if active {
			p.ActiveJob = &job
		}
		p.ShowMySQL = p.Probe == nil || p.Probe.MySQL.Installed
		p.InventoryCut = len(p.Inventory.Databases) >= inventoryCap
		// Not on a quarantined old master either: it waits for Bangun ulang or
		// Hapus (design spec 5.6), and a topology of its own would be a
		// second writable master.
		p.CanPrepare = p.Inventory.Connected && len(p.Inventory.MissingGrants) == 0 &&
			srv.Role != store.RoleReplica && srv.Role != store.RoleQuarantined
		if p.CanPrepare {
			_, err := a.Store.GetTopologyByMaster(srv.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				a.internalError(w, "get topology", err)
				return
			}
			p.PrepareAgain = err == nil
			p.PrepareGate = a.gateFor(orchestrator.GateInput{Action: orchestrator.GatePrepareMaster, MasterID: srv.ID}, true)
		}
		p.Controls = a.controls(srv)
		p.CanInstallCert = p.Inventory.Connected && len(p.Inventory.MissingGrants) == 0
		cert, err := a.Store.GetServerTLS(srv.ID)
		switch {
		case err == nil:
			p.MySQLCert = &cert
		case !errors.Is(err, store.ErrNotFound):
			a.internalError(w, "get server tls", err)
			return
		}
		if v, ok := a.Monitor.View(srv.ID); ok {
			p.Pins = newPinFacts(srv.Name, v.Snap)
			p.StartLock = newStartLockFacts(v.Snap)
			p.ReportsDropped = reportsDroppedText(v.ReportsDropped)
			if p.Probe != nil && v.Snap != nil && v.Snap.MySQLError != mysqlNotConnected {
				sl := serviceLineOf(p.Probe.MySQL.ServiceName, p.Probe.MySQL.ServiceActive, v.Snap.MySQLOK)
				p.ServiceLine = &sl
			}
		}
		mv, _ := a.Monitor.View(srv.ID) // none yet: the zero view, no report
		backup := a.newBackupCard(srv, mv)
		if backupForm != nil {
			backup.Dir, backup.MaxAgeHours = backupForm.Dir, backupForm.MaxAgeHours
		}
		p.Backup = &backup
		if err := a.oldMasterViews(r.Context(), srv, &p); err != nil {
			a.internalError(w, "old master", err)
			return
		}
	}
	switch {
	case !found || p.Local:
	case srv.Paired && v.Repair != nil && v.Repair.Token != "":
		// The answer to the post that created this re-pairing token: the
		// only page that shows its command (spec 5.1).
		if p.Command, err = a.pairCommandFor(*v.Repair); err != nil {
			a.internalError(w, "pairing command", err)
			return
		}
		p.TokenExpires = v.Repair.ExpiresAt
	case srv.Paired:
		// Any other page of a paired server never rebuilds the command.
		p.RepairActive = repairActiveText(tok.ExpiresAt)
	case tok.Token == "":
		p.TokenHidden = true
		p.TokenExpires = tok.ExpiresAt
	default:
		if p.Command, err = a.pairCommandFor(tok); err != nil {
			a.internalError(w, "pairing command", err)
			return
		}
		p.TokenExpires = tok.ExpiresAt
	}
	a.render.renderStatus(w, v.Status, "server_detail", p)
}

// pairCommand is the command an operator runs on a new database server. It
// fetches over HTTPS with -k and --pinnedpubkey: the panel's certificate is
// not checked against a CA (the new server does not know the panel's), but
// curl accepts only the key whose pin is webPin (sha256//<base64>), so a
// machine in between cannot answer instead of the panel. It saves the
// install script to a fresh mktemp file (0600, unpredictable name:
// a fixed /tmp name could be pre-created by a local user and rewritten
// between the check and sudo), checks it against scriptSHA256 (the hash of
// the script the panel serves for this token) and only then runs it as
// root, so a script swapped on the network is never run (SEC-23). curl
// follows no redirect: the token would go along to another host.
//
// The URL with the token reaches curl as a config on stdin through a bash
// here-string, which the shell writes to a pipe or temporary file, so the
// token is in no process's argv (readable by every local user through
// /proc). Single quotes: nothing in them is expanded, and "?" would be a
// glob character in zsh. PublicURL is an IP or a validated host name, the
// token is Crockford base32, the hash is hex and the pin base64, so none
// contains a quote.
// --max-redirs is an argument rather than a config line because the config
// here-string has to stay on one line. Without a pin -k would trust any
// server, so an empty one is an error.
func pairCommand(publicURL, token, scriptSHA256, webPin string) (string, error) {
	if webPin == "" {
		return "", errors.New("perintah pasang butuh pin sertifikat panel")
	}
	return `f=$(mktemp) && curl -fsSL -k --pinnedpubkey ` + webPin + ` --max-redirs 0 -K - <<<'url = "` + publicURL + "/agent/install?token=" + token +
		`"' -o "$f" && echo "` + scriptSHA256 + `  $f" | sha256sum -c - && sudo bash "$f"`, nil
}

// newToken issues a fresh pairing token. For a paired server this starts a
// re-pairing (spec 5.1): it needs a light confirmation (ActRepairToken),
// the token lasts repairTokenTTL, and the answer is the server page with
// the command, the only page that shows it. The current agent keeps
// working until an agent pairs with the new token, which replaces its
// certificate. An unpaired server's token needs no confirmation: no agent
// is replaced.
func (a *app) newToken(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if srv.Paired && !a.requireConfirmLight(w, r, ActRepairToken, auditServer, srv.ID) {
		return
	}
	if a.isLocal(srv) {
		a.audit(r, "server_token", auditServer, srv.ID, nil, msgLocalToken)
		http.Error(w, msgLocalToken, http.StatusConflict)
		return
	}
	ttl := pairTokenTTL
	if srv.Paired {
		ttl = repairTokenTTL
	}
	// The token itself never goes into the audit log.
	tok, err := a.Store.CreatePairToken(srv.ID, a.Now(), ttl)
	a.audit(r, "server_token", auditServer, srv.ID, map[string]any{"repair": srv.Paired}, a.outcomeOf(err))
	if err != nil {
		a.internalError(w, "create pair token", err)
		return
	}
	if !srv.Paired {
		http.Redirect(w, r, serverURL(srv.ID), http.StatusSeeOther)
		return
	}
	a.Logger.Info("re-pairing token created", "id", srv.ID, "by", auth.UsernameFrom(r.Context()))
	a.renderServer(w, r, srv, serverView{Status: http.StatusOK, Repair: &tok})
}

// deleteServer removes a server once the password is confirmed and its
// name typed. The panel host's own server cannot be deleted; the
// orchestrator refuses a master with replicas and a registered replica,
// and drops the agent's connection. A quarantined old master is refused
// while its cutover is pending; one whose quarantine never arrived is
// deleted only through "Hapus tanpa karantina" (unquarantined=1).
func (a *app) deleteServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok || !a.requireConfirm(w, r, ActDeleteServer, auditServer, srv.ID) {
		return
	}
	unquarantined := srv.Role == store.RoleQuarantined && r.PostFormValue("unquarantined") == "1"
	detail := map[string]any{"name": srv.Name, "unquarantined": unquarantined}
	if a.isLocal(srv) {
		a.audit(r, "server_delete", auditServer, srv.ID, detail, msgLocalDelete)
		http.Error(w, msgLocalDelete, http.StatusConflict)
		return
	}
	if !typedName(r, srv) {
		a.audit(r, "server_delete", auditServer, srv.ID, detail, msgNameMismatch)
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: msgNameMismatch})
		return
	}
	if srv.Role == store.RoleQuarantined {
		topo, err := a.Store.GetTopologyByOldMaster(srv.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.internalError(w, "get topology", err)
			return
		}
		if err == nil && topo.CutoverPending {
			msg := orchestrator.ErrCutoverPending.Error()
			a.audit(r, "server_delete", auditServer, srv.ID, detail, msg)
			a.renderServer(w, r, srv, serverView{Status: http.StatusConflict, Flash: msg})
			return
		}
	}
	del := a.Orchestrator.DeleteServer
	if unquarantined {
		del = a.Orchestrator.DeleteUnquarantined
	}
	err := del(r.Context(), srv.ID)
	a.audit(r, "server_delete", auditServer, srv.ID, detail, a.outcomeOf(err))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return
	case errors.Is(err, orchestrator.ErrServerIsMaster), errors.Is(err, orchestrator.ErrMasterHasOldMaster),
		errors.Is(err, orchestrator.ErrServerIsReplica),
		errors.Is(err, orchestrator.ErrLocalServer), errors.Is(err, orchestrator.ErrBusy),
		errors.Is(err, orchestrator.ErrCutoverPending), errors.Is(err, orchestrator.ErrQuarantinePending),
		errors.Is(err, orchestrator.ErrNotQuarantinePending):
		// The orchestrator's refusals are written for the user.
		a.Logger.Warn("delete server refused", "id", srv.ID, "err", logErr(err))
		a.renderServer(w, r, srv, serverView{Status: http.StatusBadRequest, Flash: err.Error()})
		return
	case err != nil:
		a.internalError(w, "delete server", err)
		return
	}
	a.Logger.Info("server deleted", "id", srv.ID, "name", srv.Name, "unquarantined", unquarantined, "by", auth.UsernameFrom(r.Context()))
	http.Redirect(w, r, "/servers?pesan=dihapus", http.StatusSeeOther)
}

// serverFromPath loads the server named by {id}, answering 404 (or 500) and
// returning false when it cannot.
func (a *app) serverFromPath(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return store.Server{}, false
	}
	srv, err := a.Store.GetServer(id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, msgServerNotFound, http.StatusNotFound)
		return store.Server{}, false
	}
	if err != nil {
		a.internalError(w, "get server", err)
		return store.Server{}, false
	}
	return srv, true
}

// probe decodes the server's last probe result, or nil when there is none
// (or it cannot be read, which is logged).
func (a *app) probe(s store.Server) *proto.ProbeResult {
	if s.ProbeJSON == "" {
		return nil
	}
	var p proto.ProbeResult
	if err := json.Unmarshal([]byte(s.ProbeJSON), &p); err != nil {
		a.Logger.Warn("decode probe", "server", s.ID, "err", err)
		return nil
	}
	return &p
}

// pathID parses the {id} path segment.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func serverURL(id int64) string { return "/servers/" + strconv.FormatInt(id, 10) }
