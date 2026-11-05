package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	auditDefaultDays = 7   // the audit page shows this many days unless ?sejak= says otherwise
	auditPageLimit   = 500 // most entries listed
	auditDetailRunes = 120 // the detail column shows this much of detail_json
	dateLayout       = "2006-01-02"
)

// auditActionLabels name each audited action for the UI.
var auditActionLabels = map[string]string{
	"login":            "Masuk",
	"login_failed":     "Gagal masuk",
	"logout":           "Keluar",
	"confirm_issued":   "Konfirmasi kata sandi",
	"confirm_failed":   "Konfirmasi kata sandi gagal",
	"server_create":    "Tambah server",
	"server_token":     "Token pemasangan baru",
	"server_delete":    "Hapus server",
	"server_check":     "Periksa sekarang",
	"server_snooze":    "Jangan ganggu",
	"mysql_connect":    "Hubungkan MySQL",
	"mysql_refresh":    "Segarkan data MySQL",
	"prepare_master":   "Siapkan master",
	"master_source":    "Ubah alamat MySQL master",
	"master_unlock":    "Buka kunci master",
	"create_replica":   "Buat replika",
	"add_databases":    "Tambah database",
	"remove_databases": "Lepas database",
	"replica_stop":     "Stop replika",
	"replica_start":    "Start replika",
	"replica_relock":   "Kunci kembali replika",
	"replica_resync":   "Resync ulang",
	"replica_detach":   "Lepas replika",
	"job_approve":      "Setujui pekerjaan",
	"job_retry":        "Ulangi pekerjaan",
	"job_cancel":       "Batalkan pekerjaan",
	"settings_update":  "Ubah pengaturan",
	"telegram_test":    "Kirim tes Telegram",
}

// auditTargetLabels name the target kinds.
var auditTargetLabels = map[string]string{
	auditServer:   "Server",
	auditMaster:   "Master",
	auditReplica:  "Replika",
	auditJob:      "Pekerjaan di",
	auditSession:  "Sesi",
	auditSettings: "Pengaturan",
}

// auditRow is an audit entry as the audit page shows it.
type auditRow struct {
	store.AuditEntry
	ActionLabel, Target, Detail string
	OK                          bool
}

type auditPage struct {
	Page
	Rows     []auditRow
	Servers  []store.Server
	ServerID int64  // filter; 0 is every server
	Since    string // filter, YYYY-MM-DD
	Limit    int
}

func (a *app) auditLog(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	names := make(map[int64]string, len(servers))
	for _, s := range servers {
		names[s.ID] = s.Name
	}
	q := r.URL.Query()
	y, m, d := a.Now().Local().AddDate(0, 0, -auditDefaultDays).Date()
	since := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	p := auditPage{Page: a.page(r), Servers: servers, Limit: auditPageLimit, Since: since.Format(dateLayout)}

	bad := ""
	if v := q.Get("server"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			bad = "server"
		}
		p.ServerID = id
	}
	if v := q.Get("sejak"); v != "" {
		t, err := time.ParseInLocation(dateLayout, v, time.Local)
		if err != nil {
			bad = "tanggal"
		}
		since, p.Since = t, v
	}
	if bad != "" {
		p.Flash = "Filter tidak valid: " + bad + " tidak dikenali."
		a.render.renderStatus(w, http.StatusBadRequest, "audit", p)
		return
	}

	entries, err := a.Store.ListAudit(p.ServerID, since, auditPageLimit)
	if err != nil {
		a.internalError(w, "list audit", err)
		return
	}
	for _, e := range entries {
		p.Rows = append(p.Rows, auditRow{AuditEntry: e, ActionLabel: labelOr(auditActionLabels, e.Action),
			Target: auditTarget(e, names), Detail: shortDetail(e.DetailJSON), OK: e.Outcome == auditOK})
	}
	a.render.Render(w, "audit", p)
}

func labelOr(labels map[string]string, key string) string {
	if l, ok := labels[key]; ok {
		return l
	}
	return key
}

// auditTarget words an entry's target: "Replika db-2", or the kind alone
// when the target id is 0.
func auditTarget(e store.AuditEntry, names map[int64]string) string {
	label := labelOr(auditTargetLabels, e.TargetKind)
	if e.TargetID == 0 {
		return label
	}
	name, ok := names[e.TargetID]
	if !ok {
		name = "server #" + strconv.FormatInt(e.TargetID, 10) // since deleted
	}
	return label + " " + name
}

// shortDetail is the first auditDetailRunes characters of an entry's
// detail; "" for an empty one.
func shortDetail(detail string) string {
	if detail == "{}" {
		return ""
	}
	if runes := []rune(detail); len(runes) > auditDetailRunes {
		return string(runes[:auditDetailRunes]) + "…"
	}
	return detail
}
