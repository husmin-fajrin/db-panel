package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// weeklyRetryEvery is how long a failed weekly send waits before the next
// try.
const weeklyRetryEvery = 5 * time.Minute

// WeeklyRow is one server's line of the weekly summary.
type WeeklyRow struct {
	ServerName, Role string
	// SyncedPct is the share, in percent, of the time the replica was
	// monitored in the window that it was Sinkron or Ketinggalan; 0 and
	// unused for a master.
	SyncedPct float64
	// Incidents counts the periods in a critical status; IncidentTotal is
	// their total length.
	Incidents     int
	IncidentTotal time.Duration
	DiskPct       int // data disk used now; 0 when unknown
	FailedJobs    int
}

// isIncident reports whether a server in status code is down for the
// summary: a critical status, or a replica whose IO thread is disconnected.
func isIncident(code string) bool {
	return SeverityOf(code) == SevCrit || code == StatusDisconnected
}

// Weekly builds the summary rows of the window [from, to) for the servers
// in views that are in a topology, in the order of their names. The time
// each server spent in each status is read from its status events as a
// step function: before its first event the window's status is that
// event's FromStatus (the current one when it has none), and a server
// whose first event comes from no status counts from that event.
func Weekly(s *store.Store, views map[int64]View, from, to time.Time) ([]WeeklyRow, error) {
	servers, err := s.ListServers()
	if err != nil {
		return nil, err
	}
	events, err := s.ListStatusEventsAll(from)
	if err != nil {
		return nil, err
	}
	byServer := make(map[int64][]store.StatusEvent)
	for _, e := range events {
		if e.Kind == kindStatus {
			byServer[e.ServerID] = append(byServer[e.ServerID], e)
		}
	}
	jobs, err := s.ListJobs(0)
	if err != nil {
		return nil, err
	}
	failed := make(map[int64]int)
	for _, j := range jobs {
		if j.Status == store.JobFailed && j.FinishedAt != nil && !j.FinishedAt.Before(from) && j.FinishedAt.Before(to) {
			failed[j.ServerID]++
		}
	}

	var rows []WeeklyRow
	for _, srv := range servers {
		v, ok := views[srv.ID]
		if !ok || (v.Role != store.RoleMaster && v.Role != store.RoleReplica) {
			continue
		}
		row := WeeklyRow{ServerName: srv.Name, Role: v.Role, DiskPct: diskPct(v), FailedJobs: failed[srv.ID]}
		monitored, synced := tally(&row, byServer[srv.ID], v.Status.Code, from, to)
		if v.Role == store.RoleReplica {
			if monitored > 0 {
				row.SyncedPct = 100 * float64(synced) / float64(monitored)
			} else if row.FailedJobs == 0 {
				continue // not monitored in the window: nothing to report
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// tally walks the status segments of one server in [from, to), fills the
// incident fields of row and returns the time monitored and the time synced.
func tally(row *WeeklyRow, evs []store.StatusEvent, current string, from, to time.Time) (monitored, synced time.Duration) {
	code, start := current, from
	if len(evs) > 0 {
		// The first event, even one after the window, tells what came before.
		code = evs[0].FromStatus
		if code == "" {
			start = evs[0].TS
		}
	}
	prevIncident := isIncident(code) && start.Equal(from) // a running incident began before the window
	segment := func(end time.Time) {
		if code == "" || !end.After(start) {
			return
		}
		d := end.Sub(start)
		monitored += d
		if code == StatusSynced || code == StatusLagging {
			synced += d
		}
		if isIncident(code) {
			row.IncidentTotal += d
			if !prevIncident {
				row.Incidents++
			}
		}
		prevIncident = isIncident(code)
	}
	for _, e := range evs {
		if !e.TS.Before(to) {
			break
		}
		segment(e.TS)
		code, start = e.ToStatus, e.TS
	}
	segment(to)
	return monitored, synced
}

// diskPct is the percent of the data disk used in the view's last report;
// 0 when there is none.
func diskPct(v View) int {
	if v.Snap == nil || v.Snap.DiskTotal == 0 || v.Snap.DiskFree > v.Snap.DiskTotal {
		return 0
	}
	return int((v.Snap.DiskTotal - v.Snap.DiskFree) * 100 / v.Snap.DiskTotal)
}

// FormatWeekly renders the summary as plain text for Telegram.
func FormatWeekly(rows []WeeklyRow, from, to time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Ringkasan mingguan dbpanel (%s s.d. %s)", from.Format("02-01"), to.Format("02-01"))
	failed := 0
	for _, r := range rows {
		failed += r.FailedJobs
		b.WriteString("\n• " + r.ServerName)
		if r.Role == store.RoleMaster {
			b.WriteString(" (master): ")
		} else {
			fmt.Fprintf(&b, ": sinkron %s · ", percent(r.SyncedPct))
		}
		fmt.Fprintf(&b, "%d gangguan", r.Incidents)
		if r.Incidents > 0 {
			fmt.Fprintf(&b, " (%s)", weeklyDuration(r.IncidentTotal))
		}
		if r.DiskPct > 0 {
			fmt.Fprintf(&b, " · disk %d%%", r.DiskPct)
		}
	}
	if failed > 0 {
		fmt.Fprintf(&b, "\nJob gagal: %d", failed)
	}
	return b.String()
}

// percent writes p with one decimal and a decimal comma: "99,9%".
func percent(p float64) string {
	return strings.Replace(fmt.Sprintf("%.1f", p), ".", ",", 1) + "%"
}

// weeklyDuration writes d in whole minutes, at least one: "4 menit",
// "1 jam 30 menit".
func weeklyDuration(d time.Duration) string {
	mins := max(int((d+30*time.Second)/time.Minute), 1)
	switch {
	case mins < 60:
		return fmt.Sprintf("%d menit", mins)
	case mins%60 == 0:
		return fmt.Sprintf("%d jam", mins/60)
	}
	return fmt.Sprintf("%d jam %d menit", mins/60, mins%60)
}

// lastWeeklyOccurrence is the latest time at or before now that falls on
// the day and time of at ("Mon 07:00"), in now's zone.
func lastWeeklyOccurrence(at string, now time.Time) (time.Time, bool) {
	t, err := time.Parse(weeklyLayout, at)
	if err != nil {
		return time.Time{}, false
	}
	wd, ok := weekdayOf(at)
	if !ok {
		return time.Time{}, false
	}
	back := (int(now.Weekday()) - int(wd) + 7) % 7
	occ := time.Date(now.Year(), now.Month(), now.Day()-back, t.Hour(), t.Minute(), 0, 0, now.Location())
	if occ.After(now) {
		occ = occ.AddDate(0, 0, -7)
	}
	return occ, true
}

func weekdayOf(at string) (time.Weekday, bool) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if len(at) >= 3 && strings.EqualFold(at[:3], d.String()[:3]) {
			return d, true
		}
	}
	return 0, false
}

// scheduleWeekly sends the weekly summary once the day and time of
// Settings.WeeklySummaryAt have come, once per occurrence: the setting
// weekly_last_sent keeps a restart from sending it twice, and an occurrence
// missed while the panel was down is sent at the next call. The window is
// the 7 days before the occurrence. A failed send is retried after
// weeklyRetryEvery. Nothing happens without SendWeekly.
func (m *Monitor) scheduleWeekly(ctx context.Context) {
	if m.sendWeekly == nil {
		return
	}
	now := m.now()
	m.mu.Lock()
	set := m.settings
	retryAt := m.weeklyRetryAt
	m.mu.Unlock()
	if !set.WeeklySummary || now.Before(retryAt) {
		return
	}
	occ, ok := lastWeeklyOccurrence(set.WeeklySummaryAt, now)
	if !ok {
		return
	}
	raw, err := m.st.GetSetting(store.SettingWeeklyLastSent)
	if err != nil {
		m.log.Warn("weekly summary: read weekly_last_sent", "err", err)
		return
	}
	// A panel that never sent one does not count the occurrences before its
	// start as missed.
	last := m.startedAt
	if raw != "" {
		if last, err = time.Parse(time.RFC3339, raw); err != nil {
			m.log.Warn("weekly summary: weekly_last_sent unreadable; ignored", "value", raw, "err", err)
			last = m.startedAt
		}
	}
	if !occ.After(last) {
		return
	}

	from := occ.AddDate(0, 0, -7)
	rows, err := Weekly(m.st, m.Views(), from, occ)
	if err == nil {
		err = m.sendWeekly(ctx, FormatWeekly(rows, from, occ))
	}
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down: the next start sends it
		}
		m.log.Warn("weekly summary not sent; will retry", "err", err)
		m.mu.Lock()
		m.weeklyRetryAt = now.Add(weeklyRetryEvery)
		m.mu.Unlock()
		return
	}
	m.log.Info("weekly summary sent", "window_from", from, "window_to", occ)
	if err := m.st.SetSetting(store.SettingWeeklyLastSent, occ.Format(time.RFC3339)); err != nil {
		// Sent, but unrecorded: hold it back in memory so it is not repeated
		// until the next restart.
		m.log.Warn("weekly summary: record weekly_last_sent", "err", err)
		m.mu.Lock()
		m.weeklyRetryAt = occ.AddDate(0, 0, 7)
		m.mu.Unlock()
	}
}
