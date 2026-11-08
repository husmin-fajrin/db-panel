package web

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// notMonitored is the label of a server the monitor does not judge yet.
const notMonitored = "Belum dipantau"

// mysqlNotConnected is the agent's MySQLError while no MySQL account is
// stored (internal/agent/drivers/mysql/health.go).
const mysqlNotConnected = "belum terhubung"

// statusView is what _status.html shows of one server's health.
type statusView struct {
	ServerID int64
	Role     string // store.RoleMaster, store.RoleReplica or unassigned
	Label    string
	Severity monitor.Severity
	// Detail is the status sentence; never MySQL's own error text, which
	// only "Detail teknis" shows.
	Detail   string
	LockLost bool
	Lag      string    // "tertinggal 12 dtk" / "tertinggal 3 transaksi"; "" when unknown
	Received time.Time // when the last report was taken; zero when none
	Now      time.Time
	// Explain reads a stopped replica's error, or a status a promotion
	// left (old master, replica to repoint or rebuild), in plain
	// language; nil otherwise.
	Explain *monitor.Explanation
	// DownGuide: a master presumed down shows the manual steps.
	DownGuide bool
}

// ShowDetailInRow: compact rows repeat the sentence for masters and for
// anything not OK.
func (s statusView) ShowDetailInRow() bool {
	return s.Detail != "" && (s.Role == store.RoleMaster || s.Severity != monitor.SevOK)
}

// statusFragment is the data of the status-fragment template.
type statusFragment struct {
	statusView
	Row bool // the compact variant of the dashboard and lists
}

// newStatusView builds the status of server id in role (store.Server.Role)
// from the monitor's view v (ok false when there is none).
func newStatusView(id int64, role string, v monitor.View, ok bool, now time.Time) statusView {
	sv := statusView{ServerID: id, Role: role, Now: now, Severity: monitor.SevNeutral, Received: v.ReceivedAt}
	switch {
	case ok && v.NeedsUpgrade && (v.Role == "" || v.Status.Code == ""):
		sv.Label, sv.Detail = monitor.StatusNeedsUpgrade, "Versi agent perlu dimutakhirkan untuk pemantauan."
		return sv
	case ok && v.Role == "":
		sv.Label, sv.Detail = notMonitored, "Server ini belum menjadi master atau replika."
		return sv
	case !ok || v.Status.Code == "":
		sv.Label, sv.Detail = notMonitored, "Panel belum menerima laporan pemantauan dari server ini."
		return sv
	case v.Status.Code == monitor.StatusUnreachable && v.Snap != nil && v.Snap.MySQLError == mysqlNotConnected:
		sv.Label, sv.Detail = notMonitored, "MySQL server ini belum dihubungkan ke panel."
		return sv
	}
	st := v.Status
	sv.Label, sv.Severity, sv.Detail, sv.LockLost = st.Code, st.Severity, st.Detail, st.LockLost
	if st.Code == monitor.StatusUnreachable {
		sv.Detail = "MySQL tidak menjawab" // st.Detail may be MySQL's raw error
	}
	switch {
	case st.LagSec != nil && (*st.LagSec > 0 || st.GTIDGap == 0):
		sv.Lag = "tertinggal " + strconv.FormatInt(*st.LagSec, 10) + " dtk"
	case st.GTIDGap > 0:
		sv.Lag = "tertinggal " + strconv.Itoa(st.GTIDGap) + " transaksi"
	}
	if role == store.RoleReplica && st.Code == monitor.StatusStopped && v.Snap != nil && v.Snap.Replica != nil {
		e := monitor.Explain(v.Snap.Replica.LastSQLErrno, v.Snap.Replica.LastIOErrno)
		sv.Explain = &e
	}
	if e, ok := monitor.ExplainStatus(st.Code, v.QuarantinePending); ok { // the statuses a promotion leaves
		sv.Explain = &e
	}
	sv.DownGuide = role == store.RoleMaster && st.Code == monitor.StatusDown
	return sv
}

// statusOf is the status of server id in role from the monitor.
func (a *app) statusOf(id int64, role string) statusView {
	v, ok := a.Monitor.View(id)
	return newStatusView(id, role, v, ok, a.Now())
}

// statusFragment answers GET /servers/{id}/status-fragment: the status
// block of the server, compact with ?tampilan=baris. Pages fetch it on
// health_update.
func (a *app) statusFragment(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	a.render.RenderFragment(w, "status-fragment",
		statusFragment{statusView: a.statusOf(srv.ID, srv.Role), Row: r.URL.Query().Get("tampilan") == "baris"})
}

// masterFacts is the master page's reading of its last report.
type masterFacts struct {
	Snap       *proto.HealthSnapshot // nil before the first report
	Registered int                   // replicas the panel knows of
	BinlogKeep string                // binlog_expire_logs_seconds worded; "" when unknown
	DiskUsed   uint64
}

func newMasterFacts(v monitor.View, ok bool, registered int) masterFacts {
	f := masterFacts{Registered: registered}
	if !ok || v.Snap == nil {
		return f
	}
	f.Snap = v.Snap
	f.BinlogKeep = binlogKeep(v.Snap.BinlogExpireSec)
	f.DiskUsed = v.Snap.DiskTotal - min(v.Snap.DiskFree, v.Snap.DiskTotal)
	return f
}

// reportsDroppedText is the Detail teknis line about health reports the
// hub dropped over its rate limit in the last hour (spec 5.12); "" for
// none.
func reportsDroppedText(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n) + " laporan dibuang dalam 1 jam terakhir (agent mengirim laporan terlalu sering)"
}

// Timeline lengths: the default and the extended one (?hari=90; status
// events are kept events_retention_days, 180 by default).
const (
	timelineDays     = 7
	timelineLongDays = 90
	timelineLimit    = 300 // entries of each source, and of the merge
)

// timelineEntry is one line of the server's timeline.
type timelineEntry struct {
	At     time.Time
	Icon   string // timeline-icon variant: status, lock, restart, disk, connections, agent, action
	Tone   string // text colour class of the icon
	Title  string
	Detail string
}

// timelineView is what _timeline.html shows.
type timelineView struct {
	Entries []timelineEntry
	Days    int
	Long    bool   // the 90-day view
	Path    string // the page, for the 7/90-day switch
}

// timelineDaysOf reads ?hari: 90 for the long view, else 7.
func timelineDaysOf(r *http.Request) int {
	if r.URL.Query().Get("hari") == strconv.Itoa(timelineLongDays) {
		return timelineLongDays
	}
	return timelineDays
}

// timeline merges the server's status events and its audit entries of
// the last days, newest first.
func (a *app) timeline(r *http.Request, serverID int64) (timelineView, error) {
	days := timelineDaysOf(r)
	since := a.Now().Add(-time.Duration(days) * 24 * time.Hour)
	tv := timelineView{Days: days, Long: days == timelineLongDays, Path: r.URL.Path}
	events, err := a.Store.ListStatusEvents(serverID, since, timelineLimit)
	if err != nil {
		return tv, err
	}
	audits, err := a.Store.ListAudit(serverID, since, timelineLimit)
	if err != nil {
		return tv, err
	}
	for _, e := range events {
		tv.Entries = append(tv.Entries, eventEntry(e))
	}
	for _, e := range audits {
		if e.TargetKind == auditSession { // target_id is no server id
			continue
		}
		tv.Entries = append(tv.Entries, auditEntry(e))
	}
	slices.SortStableFunc(tv.Entries, func(x, y timelineEntry) int { return y.At.Compare(x.At) })
	if len(tv.Entries) > timelineLimit {
		tv.Entries = tv.Entries[:timelineLimit]
	}
	return tv, nil
}

// toneOf is the text colour of a severity.
func toneOf(s monitor.Severity) string {
	switch s {
	case monitor.SevOK:
		return "text-success"
	case monitor.SevWarn:
		return "text-warning"
	case monitor.SevCrit:
		return "text-error"
	}
	return "text-base-content/60"
}

// eventEntry words a status event (kinds of internal/panel/monitor/state.go).
func eventEntry(e store.StatusEvent) timelineEntry {
	t := timelineEntry{At: e.TS, Icon: e.Kind, Tone: toneOf(monitor.SevNeutral), Title: e.Detail}
	switch e.Kind {
	case "status":
		from := e.FromStatus
		if from == "" {
			from = notMonitored
		}
		t.Title = "Status: " + from + " → " + e.ToStatus
		t.Detail = e.Detail
		t.Tone = toneOf(monitor.SeverityOf(e.ToStatus))
	case "lock":
		t.Tone = toneOf(monitor.SevOK)
		if e.Detail == "Kunci terlepas" {
			t.Tone = toneOf(monitor.SevCrit)
		}
	case "disk", "connections":
		t.Tone = toneOf(monitor.SevWarn)
	case "restart", "agent":
	default:
		t.Icon = "status"
	}
	return t
}

// auditEntry words an audit entry: the action and who did it. A failure
// shows as "Gagal" only; its text may be an internal error.
func auditEntry(e store.AuditEntry) timelineEntry {
	t := timelineEntry{At: e.TS, Icon: "action", Tone: "text-info", Title: auditActionLabel(e.Action), Detail: "oleh " + e.Username}
	if e.Outcome != auditOK {
		t.Tone = "text-error"
		t.Detail = "Gagal · " + t.Detail
	}
	return t
}

// auditActions word the audit log's actions for people.
var auditActions = map[string]string{
	"confirm_issued":    "Konfirmasi kata sandi",
	"confirm_failed":    "Konfirmasi kata sandi gagal",
	"server_create":     "Server ditambahkan",
	"server_token":      "Perintah pemasangan dibuat",
	"server_delete":     "Hapus server",
	"server_check":      "Periksa sekarang",
	"server_snooze":     "Jangan ganggu",
	"mysql_connect":     "Hubungkan MySQL",
	"mysql_refresh":     "Segarkan data MySQL",
	"prepare_master":    "Siapkan sebagai master",
	"mysql_tls":         "Pasang sertifikat MySQL",
	"tighten_tls":       "Perketat sambungan replikasi",
	"master_source":     "Ubah alamat untuk replika",
	"master_unlock":     "Buka kunci master",
	"promote":           "Promote replika",
	"create_replica":    "Buat replika",
	"replica_stop":      "Stop replika",
	"replica_start":     "Start replika",
	"replica_relock":    "Kunci kembali",
	"replica_resync":    "Resync ulang",
	"replica_detach":    "Lepas replika",
	"add_databases":     "Tambah database",
	"remove_databases":  "Lepas database",
	"job_approve":       "Setujui pekerjaan",
	"job_retry":         "Ulangi pekerjaan",
	"job_cancel":        "Batalkan pekerjaan",
	"leftover_check":    "Periksa data tertinggal",
	"salvage":           "Amankan data tertinggal",
	"salvage_download":  "Unduh data tertinggal",
	"salvage_delete":    "Hapus berkas data tertinggal",
	"cutover_confirmed": "Sudah saya alihkan",
	"replica_repoint":   "Arahkan sekarang",
}

func auditActionLabel(action string) string {
	if l, ok := auditActions[action]; ok {
		return l
	}
	return action
}
