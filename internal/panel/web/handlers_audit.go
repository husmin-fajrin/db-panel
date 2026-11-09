package web

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	auditDefaultDays = 7   // the audit page shows this many days unless ?sejak= says otherwise
	auditPageLimit   = 500 // most entries listed
	dateLayout       = "2006-01-02"
	// chainCheckEvery bounds how often the audit page walks the whole
	// hash chain.
	chainCheckEvery = time.Minute
)

// chainStatus is the audit page's line on the hash chain (SEC-45).
type chainStatus struct {
	OK     bool   // intact
	Failed bool   // the walk failed; Text says so
	Text   string // "Rantai utuh sampai #N", "Rantai putus di #M"
	// Label and Row are Text in two parts, so the page can set the row
	// number in Space Mono: "Rantai utuh sampai" and N. Row is 0 when
	// Failed.
	Label     string
	Row       int64
	CheckedAt time.Time
}

// chainCache keeps the last chain status for chainCheckEvery.
type chainCache struct {
	mu     sync.Mutex
	status chainStatus
}

// chainStatus returns the chain's status, walking the chain when the
// cached one is older than chainCheckEvery (or missing). A failed walk is
// logged, shown and cached like any other result.
func (a *app) chainStatus() chainStatus {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	now := a.Now()
	if c := a.chain.status; !c.CheckedAt.IsZero() && now.Sub(c.CheckedAt) < chainCheckEvery && !now.Before(c.CheckedAt) {
		return c
	}
	lastOK, brokenAt, err := a.Store.VerifyAuditChain()
	if err != nil {
		a.Logger.Error("verify audit chain", "err", err)
		a.chain.status = chainStatus{Failed: true, Text: "Rantai catatan audit tidak dapat diperiksa saat ini.", CheckedAt: now}
		return a.chain.status
	}
	st := chainStatus{OK: true, Text: fmt.Sprintf("Rantai utuh sampai #%d", lastOK), Label: "Rantai utuh sampai",
		Row: lastOK, CheckedAt: now}
	if brokenAt != 0 {
		st = chainStatus{Text: fmt.Sprintf("Rantai putus di #%d", brokenAt), Label: "Rantai putus di", Row: brokenAt,
			CheckedAt: now}
		a.Logger.Warn("audit chain broken", "broken_at", brokenAt, "last_ok", lastOK)
	}
	a.chain.status = st
	return st
}

// auditActionLabels name, for people, every action the panel writes to
// the audit log (design spec 5.6, 5.8 #5): the audit page and the
// timelines. TestAuditActionLabelsComplete scans the code for the actions
// and fails on one without a label here.
var auditActionLabels = map[string]string{
	"login":             "Masuk",
	"login_failed":      "Gagal masuk",
	"logout":            "Keluar",
	"confirm_issued":    "Konfirmasi kata sandi",
	"confirm_failed":    "Konfirmasi kata sandi gagal",
	"confirm_rejected":  "Konfirmasi ditolak",
	"agent_pair":        "Pemasangan agent ditolak",
	"server_create":     "Tambah server",
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
	"add_databases":     "Tambah database",
	"remove_databases":  "Lepas database",
	"replica_stop":      "Stop replika",
	"replica_start":     "Start replika",
	"replica_relock":    "Kunci kembali",
	"replica_resync":    "Resync ulang",
	"replica_detach":    "Lepas replika",
	"replica_repoint":   "Arahkan sekarang",
	"job_approve":       "Setujui pekerjaan",
	"job_retry":         "Ulangi pekerjaan",
	"job_cancel":        "Batalkan pekerjaan",
	"settings_update":   "Ubah pengaturan",
	"telegram_test":     "Kirim tes Telegram",
	"https_cert_upload": "Pasang sertifikat keamanan panel sendiri",
	"https_cert_panel":  "Kembali ke sertifikat panel",
	"https_cert_names":  "Ubah nama sertifikat panel",
	"leftover_check":    "Periksa data tertinggal",
	"salvage":           "Amankan data tertinggal",
	"salvage_download":  "Unduh data tertinggal",
	"salvage_delete":    "Hapus berkas data tertinggal",
	"cutover_confirmed": "Sudah saya alihkan",
}

// auditActionLabel is action's label, or action itself when it has none.
func auditActionLabel(action string) string {
	return labelOr(auditActionLabels, action)
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
	ActionLabel, Target string
	// Summary is the detail in words (auditDetailSummary); Detail the raw
	// JSON in full (scrubDetail keeps it under 2 KiB), for the row's
	// <details>.
	Summary, Detail string
	OK              bool
}

type auditPage struct {
	Page
	Rows     []auditRow
	Servers  []store.Server
	ServerID int64  // filter; 0 is every server
	Since    string // filter, YYYY-MM-DD
	Limit    int
	Chain    chainStatus
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
	p := auditPage{Page: a.page(r), Servers: servers, Limit: auditPageLimit, Since: since.Format(dateLayout),
		Chain: a.chainStatus()}

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
		p.Rows = append(p.Rows, auditRow{AuditEntry: e, ActionLabel: auditActionLabel(e.Action),
			Target: auditTarget(e, names), Summary: auditDetailSummaryNamed(e.DetailJSON, names),
			Detail: rawDetail(e.DetailJSON), OK: e.Outcome == auditOK})
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

// rawDetail is an entry's detail_json as the page shows it; "" for an
// empty one.
func rawDetail(detail string) string {
	if detail == "{}" {
		return ""
	}
	return detail
}
