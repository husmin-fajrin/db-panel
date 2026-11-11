package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

const (
	alertHistoryDays  = 90  // spec 5.8: the alert history covers 90 days
	alertHistoryLimit = 500 // most closed alerts listed
)

// alertKindLabels name each alert kind for the UI.
var alertKindLabels = map[string]string{
	monitor.KindReplicaStopped:      "Replika berhenti",
	monitor.KindReplicaDisconnected: "Replika terputus",
	monitor.KindReplicaLagging:      "Replika ketinggalan",
	monitor.KindReplicaUnlocked:     "Kunci replika terlepas",
	monitor.KindMasterLocked:        "Master terkunci",
	monitor.KindMasterDown:          "Master diduga tumbang",
	monitor.KindMySQLUnreachable:    "MySQL tidak terjangkau",
	monitor.KindAgentOffline:        "Agent offline",
	monitor.KindDiskWarn:            "Disk hampir penuh",
	monitor.KindDiskCrit:            "Disk kritis",
	monitor.KindConnectionsHigh:     "Koneksi MySQL tinggi",
	monitor.KindBinlogDeadline:      "Batas umur binlog master",
	monitor.KindJobFailed:           "Pekerjaan gagal",
	monitor.KindTLSExpiring:         "Sertifikat MySQL hampir kedaluwarsa",
	monitor.KindWebTLSExpiring:      "Sertifikat HTTPS panel hampir kedaluwarsa",
	monitor.KindPromoteCutover:      "Promote menunggu pengalihan aplikasi",
	monitor.KindPromotePartial:      "Promote selesai sebagian",
	monitor.KindReplicaNeedsRepoint: "Replika perlu diarahkan ulang",
	monitor.KindReplicaNeedsRebuild: "Replika perlu dibangun ulang",
	monitor.KindMasterQuarantined:   "Master lama dikarantina",
	monitor.KindMasterLockExpired:   "Batas waktu kunci promote",
	monitor.KindMasterUnpinned:      "Master tidak dipin",

	monitor.KindMasterStartUnguarded: "Master lama belum terjamin terkunci saat menyala",
	monitor.KindMasterStartLate:      "Master lama sempat bisa ditulis saat menyala",
	monitor.KindMasterStartBypassed:  "Kunci start master lama tidak dipakai MySQL",
	monitor.KindMasterStartBlocked:   "MySQL master lama gagal menyala karena kunci start",
	monitor.KindMasterLockKept:       "Kunci promote master dipertahankan",
}

func alertKindLabel(kind string) string {
	if l, ok := alertKindLabels[kind]; ok {
		return l
	}
	return kind
}

// alertRow is an alert with what the alerts page shows of it.
type alertRow struct {
	store.Alert
	KindLabel  string
	ServerName string
	// ServerKnown: the server still exists, so the Jangan ganggu form is
	// offered.
	ServerKnown bool
	// SnoozeUntil is set while the server's notifications are muted.
	SnoozeUntil *time.Time
	DurationSec int64 // closed alerts: how long the condition lasted
}

// TelegramLabel words the alert's Telegram delivery state.
func (r alertRow) TelegramLabel() string {
	switch r.TelegramState {
	case store.TelegramPending:
		return "Menunggu kirim"
	case store.TelegramSent:
		return "Terkirim"
	case store.TelegramFailed:
		// The notifier stores the reason without the bot token.
		return "Gagal: " + r.TelegramError
	case store.TelegramRecovered:
		if r.CloseReason != "" {
			// Closed without recovering (a resync started, Lepas).
			return "Ditutup: " + r.CloseReason
		}
		return "Pulih terkirim"
	}
	return r.TelegramState
}

// TelegramClass is the badge class (.lencana) of the delivery state.
func (r alertRow) TelegramClass() string {
	switch r.TelegramState {
	case store.TelegramSent, store.TelegramRecovered:
		return "lencana-success"
	case store.TelegramFailed:
		return "lencana-error"
	}
	return "lencana-neutral"
}

// Level is the alert's level as a badge: Kritis (notify.Critical) or
// Peringatan; the alerts page's filter chips and its open alerts use it.
func (r alertRow) Level() Badge {
	if notify.Critical(r.Kind) {
		return Badge{Tone: "error", Text: "Kritis"}
	}
	return Badge{Tone: "warning", Text: "Peringatan"}
}

// Alert levels the alerts page filters by (?tingkat=); "" is every level.
const (
	levelCritical = "kritis"
	levelWarning  = "peringatan"
)

// alertChip is a filter chip of the alerts page.
type alertChip struct {
	Value, Label string
	On           bool
}

type alertsPage struct {
	Page
	Now     time.Time
	Active  []alertRow
	History []alertRow
	Level   string // the filter: "", levelCritical or levelWarning
	Chips   []alertChip
}

func (a *app) alertsPage(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers()
	if err != nil {
		a.internalError(w, "list servers", err)
		return
	}
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	now := a.Now()
	row := func(al store.Alert) alertRow {
		ar := alertRow{Alert: al, KindLabel: alertKindLabel(al.Kind)}
		if srv, ok := byID[al.ServerID]; ok {
			ar.ServerName, ar.ServerKnown = srv.Name, true
			if srv.SnoozeUntil != nil && now.Before(*srv.SnoozeUntil) {
				ar.SnoozeUntil = srv.SnoozeUntil
			}
		} else {
			ar.ServerName = fmt.Sprintf("server %d", al.ServerID)
		}
		if al.ClosedAt != nil {
			ar.DurationSec = int64(al.ClosedAt.Sub(al.OpenedAt) / time.Second)
		}
		return ar
	}

	open, err := a.Store.ListOpenAlerts()
	if err != nil {
		a.internalError(w, "list open alerts", err)
		return
	}
	recent, err := a.Store.ListAlerts(now.AddDate(0, 0, -alertHistoryDays), alertHistoryLimit)
	if err != nil {
		a.internalError(w, "list alerts", err)
		return
	}
	p := alertsPage{Page: a.page(r), Now: now}
	switch lv := r.URL.Query().Get("tingkat"); lv {
	case levelCritical, levelWarning:
		p.Level = lv // anything else shows every level
	}
	for _, c := range []alertChip{{"", "Semua", false}, {levelCritical, "Kritis", false}, {levelWarning, "Peringatan", false}} {
		c.On = c.Value == p.Level
		p.Chips = append(p.Chips, c)
	}
	keep := func(al store.Alert) bool {
		return p.Level == "" || notify.Critical(al.Kind) == (p.Level == levelCritical)
	}
	for _, al := range open {
		if keep(al) {
			p.Active = append(p.Active, row(al))
		}
	}
	for _, al := range recent {
		if al.ClosedAt != nil && keep(al) {
			p.History = append(p.History, row(al))
		}
	}
	a.render.Render(w, "alerts", p)
}

// alertCount answers {"count": N}, the number of open alerts, for the
// navbar badge.
func (a *app) alertCount(w http.ResponseWriter, r *http.Request) {
	open, err := a.Store.ListOpenAlerts()
	if err != nil {
		a.internalError(w, "list open alerts", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"count": len(open)})
}
