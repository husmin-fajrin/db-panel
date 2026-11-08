package notify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Messages are plain text (no Markdown, so nothing needs escaping). They
// carry server names, statuses, error numbers, plain explanations, times
// and panel links; MySQL's own error text never leaves the panel (spec
// 5.3), and the alert engine's messages do not hold it either.

const (
	iconCrit      = "⛔"
	iconWarn      = "⚠️"
	iconRecovered = "✅"
	iconClosed    = "ℹ️"
	iconInfo      = "ℹ️"
	sep           = " · "
)

// critKinds are the alerts that need action now; the others are warnings.
var critKinds = map[string]bool{
	monitor.KindReplicaStopped:      true,
	monitor.KindReplicaUnlocked:     true,
	monitor.KindMasterLocked:        true,
	monitor.KindMasterDown:          true,
	monitor.KindDiskCrit:            true,
	monitor.KindAgentOffline:        true,
	monitor.KindMySQLUnreachable:    true,
	monitor.KindBinlogDeadline:      true,
	monitor.KindJobFailed:           true,
	monitor.KindPromotePartial:      true,
	monitor.KindReplicaNeedsRebuild: true,
}

// infoKinds only inform: nothing is wrong (spec 5.14, "Info").
var infoKinds = map[string]bool{monitor.KindMasterQuarantined: true}

// kindNames name each kind of alert in plain words, for the recovery
// message.
var kindNames = map[string]string{
	monitor.KindReplicaStopped:      "replika berhenti",
	monitor.KindReplicaDisconnected: "replika terputus",
	monitor.KindReplicaLagging:      "replika ketinggalan",
	monitor.KindReplicaUnlocked:     "kunci replika terlepas",
	monitor.KindMasterLocked:        "master terkunci",
	monitor.KindMasterDown:          "master diduga tumbang",
	monitor.KindMySQLUnreachable:    "MySQL tidak terjangkau",
	monitor.KindAgentOffline:        "agent offline",
	monitor.KindDiskWarn:            "disk hampir penuh",
	monitor.KindDiskCrit:            "disk kritis",
	monitor.KindConnectionsHigh:     "koneksi MySQL tinggi",
	monitor.KindBinlogDeadline:      "batas umur binlog master",
	monitor.KindJobFailed:           "pekerjaan gagal",
	monitor.KindTLSExpiring:         "sertifikat MySQL hampir kedaluwarsa",
	monitor.KindWebTLSExpiring:      "sertifikat HTTPS panel hampir kedaluwarsa",
	monitor.KindPromoteCutover:      "pengalihan aplikasi setelah promote",
	monitor.KindPromotePartial:      "promote selesai sebagian",
	monitor.KindReplicaNeedsRepoint: "replika perlu diarahkan ulang",
	monitor.KindReplicaNeedsRebuild: "replika perlu dibangun ulang",
	monitor.KindMasterQuarantined:   "master lama dikarantina",
	monitor.KindMasterLockExpired:   "batas waktu kunci promote",
	monitor.KindMasterUnpinned:      "master tidak dipin",
}

// masterKinds and replicaKinds link to the master's or the replica's page;
// promote_cutover links to the new master's cutover page (spec 5.5); the
// other kinds link to the server's page.
var (
	masterKinds = map[string]bool{monitor.KindMasterLocked: true, monitor.KindMasterDown: true,
		monitor.KindMasterLockExpired: true}
	replicaKinds = map[string]bool{
		monitor.KindReplicaStopped: true, monitor.KindReplicaDisconnected: true, monitor.KindReplicaLagging: true,
		monitor.KindReplicaUnlocked: true, monitor.KindMySQLUnreachable: true, monitor.KindBinlogDeadline: true,
		monitor.KindReplicaNeedsRepoint: true, monitor.KindReplicaNeedsRebuild: true,
	}
)

func icon(kind string) string {
	switch {
	case critKinds[kind]:
		return iconCrit
	case infoKinds[kind]:
		return iconInfo
	}
	return iconWarn
}

func kindName(kind string) string {
	if n, ok := kindNames[kind]; ok {
		return n
	}
	return kind
}

// errnoRe finds the error number in a stopped replica's alert message
// ("Replikasi berhenti karena error 1062.").
var errnoRe = regexp.MustCompile(`\berror (\d+)\b`)

// Format is the Telegram message of an open alert (spec 5.3). A stopped
// replica reads:
//
//	⛔ Replika RS-2 berhenti
//	Data ganda (error 1062): ada baris yang sudah ada di replika.
//	Saran: Resync ulang.
//	Master: RS-1 · Sejak: 02:14 · http://panel.local/replicas/3
//
// any other alert is its icon and message, then the server, the time it
// opened and the link. masterName may be "" (the "Master:" part is left
// out), and so may baseURL (no link).
func Format(a store.Alert, serverName, masterName, baseURL string, now time.Time) string {
	var lines, tail []string
	if a.Kind == monitor.KindReplicaStopped {
		lines = append([]string{iconCrit + " Replika " + serverName + " berhenti"}, stoppedLines(a.Message)...)
		if masterName != "" {
			tail = append(tail, "Master: "+masterName)
		}
	} else {
		lines = []string{icon(a.Kind) + " " + a.Message}
		tail = append(tail, "Server: "+serverName)
	}
	tail = append(tail, "Sejak: "+clock(a.OpenedAt, now))
	if link := alertLink(a, baseURL); link != "" {
		tail = append(tail, link)
	}
	return strings.Join(append(lines, strings.Join(tail, sep)), "\n")
}

// stoppedLines explain a stopped replica: the first sentence of the plain
// explanation of its error, with the error number, and the advice. Without
// an error number the alert's message stands as it is.
func stoppedLines(msg string) []string {
	m := errnoRe.FindStringSubmatch(msg)
	if m == nil {
		return []string{msg}
	}
	errno, err := strconv.Atoi(m[1])
	if err != nil {
		return []string{msg}
	}
	exp := monitor.Explain(errno, 0)
	text := exp.Text
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1] // the rest is background for the panel's page
	}
	tag := fmt.Sprintf(" (error %d)", errno)
	if head, rest, ok := strings.Cut(text, ":"); ok {
		text = head + tag + ":" + rest
	} else {
		text = strings.TrimSuffix(text, ".") + tag + "."
	}
	lines := []string{text}
	if exp.Advice != "" {
		lines = append(lines, "Saran: "+exp.Advice+".")
	}
	return lines
}

// alertLink is the panel page of the alert's server; "" without baseURL.
// Replica and master pages are keyed by the server id, like server pages.
func alertLink(a store.Alert, baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}
	page := "servers"
	switch {
	case a.Kind == monitor.KindPromoteCutover:
		return fmt.Sprintf("%s/masters/%d/cutover", baseURL, a.ServerID)
	case masterKinds[a.Kind]:
		page = "masters"
	case replicaKinds[a.Kind]:
		page = "replicas"
	}
	return fmt.Sprintf("%s/%s/%d", baseURL, page, a.ServerID)
}

// clock is t on the panel's clock: "HH:MM", or "DD-MM HH:MM" when it is not
// today's.
func clock(t, now time.Time) string {
	t, now = t.In(time.Local), now.In(time.Local)
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("02-01 15:04")
}

// resolvedLines word the closing of the promote kinds, where nothing
// broken recovered: an operator's step or a job finished.
var resolvedLines = map[string]string{
	monitor.KindPromoteCutover:      "pengalihan aplikasi dikonfirmasi",
	monitor.KindPromotePartial:      "promote kini tuntas",
	monitor.KindReplicaNeedsRepoint: "replika sudah diarahkan ulang ke master baru",
	monitor.KindReplicaNeedsRebuild: "replika sudah dibangun ulang dari master baru",
}

// FormatRecovered is the message of a closed alert: "✅ <server> pulih:
// <kind in words> selesai setelah <duration>"; for the promote kinds
// "✅ <server>: <what was done> setelah <duration>".
func FormatRecovered(a store.Alert, serverName string, closedAt time.Time) string {
	if line, ok := resolvedLines[a.Kind]; ok {
		return fmt.Sprintf("%s %s: %s setelah %s", iconRecovered, serverName, line, duration(closedAt.Sub(a.OpenedAt)))
	}
	return fmt.Sprintf("%s %s pulih: %s selesai setelah %s", iconRecovered, serverName, kindName(a.Kind),
		duration(closedAt.Sub(a.OpenedAt)))
}

// FormatFlapping replaces the open messages of an alert kind that keeps
// reopening: "⚠️ <server>: <kind in words> berkedip N kali dalam 5 menit
// terakhir", then the link when baseURL is set.
func FormatFlapping(a store.Alert, serverName string, count int, baseURL string) string {
	text := fmt.Sprintf("%s %s: %s berkedip %d kali dalam %s terakhir", iconWarn, serverName, kindName(a.Kind), count,
		duration(flapWindow))
	if link := alertLink(a, baseURL); link != "" {
		text += "\n" + link
	}
	return text
}

// FormatClosed is the message of an alert closed because its server is no
// longer judged, which is no recovery: "ℹ️ <server>: peringatan <kind in
// words> ditutup: <reason>".
func FormatClosed(a store.Alert, serverName string) string {
	return fmt.Sprintf("%s %s: peringatan %s ditutup: %s", iconClosed, serverName, kindName(a.Kind), a.CloseReason)
}

// duration words d in its two largest units: "45 detik", "12 menit",
// "2 jam 5 menit", "2 hari 2 jam".
func duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%d detik", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d menit", int(d/time.Minute))
	}
	two := func(big int, bigUnit string, small int, smallUnit string) string {
		s := fmt.Sprintf("%d %s", big, bigUnit)
		if small > 0 {
			s += fmt.Sprintf(" %d %s", small, smallUnit)
		}
		return s
	}
	if d < 24*time.Hour {
		return two(int(d/time.Hour), "jam", int(d%time.Hour/time.Minute), "menit")
	}
	return two(int(d/(24*time.Hour)), "hari", int(d%(24*time.Hour)/time.Hour), "jam")
}
