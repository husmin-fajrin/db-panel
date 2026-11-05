package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
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

// TelegramClass is the badge class of the delivery state.
func (r alertRow) TelegramClass() string {
	switch r.TelegramState {
	case store.TelegramSent, store.TelegramRecovered:
		return "badge-success"
	case store.TelegramFailed:
		return "badge-error"
	}
	return "badge-ghost"
}

type alertsPage struct {
	Page
	Now     time.Time
	Active  []alertRow
	History []alertRow
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
	for _, al := range open {
		p.Active = append(p.Active, row(al))
	}
	for _, al := range recent {
		if al.ClosedAt != nil {
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
