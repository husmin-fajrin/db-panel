package monitor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

var (
	weekFrom = time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC) // a Monday
	weekTo   = weekFrom.AddDate(0, 0, 7)
)

func weeklyServer(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	srv, err := st.CreateServer(name)
	if err != nil {
		t.Fatal(err)
	}
	return srv.ID
}

// statusAt records a status change of id at ts.
func statusAt(t *testing.T, st *store.Store, id int64, ts time.Time, from, to string) {
	t.Helper()
	if _, err := st.AddStatusEvent(store.StatusEvent{ServerID: id, TS: ts, Kind: kindStatus, FromStatus: from, ToStatus: to}); err != nil {
		t.Fatal(err)
	}
}

func replicaView(id int64, code string) View {
	return View{ServerID: id, Role: store.RoleReplica, Status: Status{Code: code}}
}

func rowOf(t *testing.T, rows []WeeklyRow, name string) WeeklyRow {
	t.Helper()
	for _, r := range rows {
		if r.ServerName == name {
			return r
		}
	}
	t.Fatalf("no row for %q in %+v", name, rows)
	return WeeklyRow{}
}

func near(a, b float64) bool { return a-b < 0.05 && b-a < 0.05 }

func TestWeeklySyncedPctFromEvents(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	// Synced for 6 days, then stopped for the last day.
	statusAt(t, st, id, weekFrom.AddDate(0, 0, 6), StatusSynced, StatusStopped)

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusStopped)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	r := rowOf(t, rows, "DB-2")
	if !near(r.SyncedPct, 85.7) {
		t.Fatalf("SyncedPct = %v, want 85.7", r.SyncedPct)
	}
	if r.Incidents != 1 || r.IncidentTotal != 24*time.Hour {
		t.Fatalf("incidents = %d for %v, want 1 for 24h", r.Incidents, r.IncidentTotal)
	}
	if r.Role != store.RoleReplica {
		t.Fatalf("role = %q", r.Role)
	}
}

func TestWeeklyLaggingCountsAsSyncedAndNoEventsUsesCurrentStatus(t *testing.T) {
	st := openStore(t)
	lag := weeklyServer(t, st, "DB-2")
	steady := weeklyServer(t, st, "DB-3")
	down := weeklyServer(t, st, "DB-4")
	statusAt(t, st, lag, weekFrom.AddDate(0, 0, 3), StatusSynced, StatusLagging)

	rows, err := Weekly(st, map[int64]View{
		lag: replicaView(lag, StatusLagging), steady: replicaView(steady, StatusSynced), down: replicaView(down, StatusStopped),
	}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, rows, "DB-2"); !near(r.SyncedPct, 100) || r.Incidents != 0 {
		t.Fatalf("lagging replica = %+v, want 100%% and no incident", r)
	}
	if r := rowOf(t, rows, "DB-3"); !near(r.SyncedPct, 100) {
		t.Fatalf("steady replica = %+v", r)
	}
	// Stopped all week with no event in it: one incident, already running.
	r := rowOf(t, rows, "DB-4")
	if r.SyncedPct != 0 || r.IncidentTotal != 7*24*time.Hour || r.Incidents != 1 {
		t.Fatalf("stopped replica = %+v, want 0%%, one incident of 7 days", r)
	}
	if got := FormatWeekly([]WeeklyRow{r}, weekFrom, weekTo); !strings.Contains(got, "sinkron 0,0% · 1 gangguan (168 jam)") {
		t.Fatalf("an outage of the whole week must show its duration:\n%s", got)
	}
}

func TestWeeklyCountsAnOutageThatBeganBeforeTheWindow(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	// Down since before Monday 07:00, back after 5 hours, later down again.
	statusAt(t, st, id, weekFrom.Add(5*time.Hour), StatusStopped, StatusSynced)
	statusAt(t, st, id, weekFrom.Add(24*time.Hour), StatusSynced, StatusUnreachable)
	statusAt(t, st, id, weekFrom.Add(25*time.Hour), StatusUnreachable, StatusSynced)

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusSynced)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, rows, "DB-2"); r.Incidents != 2 || r.IncidentTotal != 6*time.Hour {
		t.Fatalf("row = %+v, want 2 incidents for 6h", r)
	}
}

func TestWeeklyWindowUsesEventsAfterItForTheStartingStatus(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	// No event inside the window; the first one after it says the replica
	// was Synced up to then, whatever it is now.
	statusAt(t, st, id, weekTo.Add(time.Hour), StatusSynced, StatusStopped)

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusStopped)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, rows, "DB-2"); !near(r.SyncedPct, 100) || r.Incidents != 0 {
		t.Fatalf("row = %+v, want 100%% and no incident", r)
	}
}

func TestWeeklyReplicaRegisteredLateCountsFromItsFirstEvent(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	// Registered 4 days into the window, Synced from day 5 on; before that
	// it was not ready (a neutral status counts as monitored, not synced).
	statusAt(t, st, id, weekFrom.AddDate(0, 0, 4), "", StatusNotReady)
	statusAt(t, st, id, weekFrom.AddDate(0, 0, 5), StatusNotReady, StatusSynced)

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusSynced)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	// Monitored 3 days, synced 2 of them.
	if r := rowOf(t, rows, "DB-2"); !near(r.SyncedPct, 66.7) {
		t.Fatalf("SyncedPct = %v, want 66.7", r.SyncedPct)
	}
}

func TestWeeklyCountsIncidents(t *testing.T) {
	st := openStore(t)
	rep := weeklyServer(t, st, "DB-2")
	mas := weeklyServer(t, st, "DB-1")
	quiet := weeklyServer(t, st, "DB-3")

	h := func(n int) time.Time { return weekFrom.Add(time.Duration(n) * time.Hour) }
	// Replica: stopped 1 h, back; unreachable 1 h rolling into agent
	// offline 1 h (one continuous incident), back; disconnected 2 h.
	statusAt(t, st, rep, h(1), StatusSynced, StatusStopped)
	statusAt(t, st, rep, h(2), StatusStopped, StatusSynced)
	statusAt(t, st, rep, h(10), StatusSynced, StatusUnreachable)
	statusAt(t, st, rep, h(11), StatusUnreachable, StatusAgentOffline)
	statusAt(t, st, rep, h(12), StatusAgentOffline, StatusSynced)
	statusAt(t, st, rep, h(20), StatusSynced, StatusDisconnected)
	statusAt(t, st, rep, h(22), StatusDisconnected, StatusSynced)
	// Paused on purpose and lagging are no incidents.
	statusAt(t, st, rep, h(30), StatusSynced, StatusPaused)
	statusAt(t, st, rep, h(31), StatusPaused, StatusLagging)
	statusAt(t, st, rep, h(32), StatusLagging, StatusSynced)
	// Other event kinds are ignored.
	if _, err := st.AddStatusEvent(store.StatusEvent{ServerID: rep, TS: h(40), Kind: kindDisk, Detail: "Disk data terpakai 90%"}); err != nil {
		t.Fatal(err)
	}
	// Master: presumed down 30 min, then locked 30 min.
	statusAt(t, st, mas, h(5), StatusHealthy, StatusDown)
	statusAt(t, st, mas, h(5).Add(30*time.Minute), StatusDown, StatusHealthy)
	statusAt(t, st, mas, h(8), StatusHealthy, StatusLocked)
	statusAt(t, st, mas, h(8).Add(30*time.Minute), StatusLocked, StatusHealthy)

	views := map[int64]View{
		rep: replicaView(rep, StatusSynced),
		mas: {ServerID: mas, Role: store.RoleMaster, Status: Status{Code: StatusHealthy},
			Snap: &proto.HealthSnapshot{DiskTotal: 1000, DiskFree: 460}},
		quiet: {ServerID: quiet, Role: "", Status: Status{}}, // in no topology
	}
	rows, err := Weekly(st, views, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one for the replica and one for the master", rows)
	}
	r := rowOf(t, rows, "DB-2")
	if r.Incidents != 3 || r.IncidentTotal != 5*time.Hour {
		t.Fatalf("replica incidents = %d for %v, want 3 for 5h", r.Incidents, r.IncidentTotal)
	}
	m := rowOf(t, rows, "DB-1")
	if m.Role != store.RoleMaster || m.SyncedPct != 0 {
		t.Fatalf("master row = %+v", m)
	}
	if m.Incidents != 2 || m.IncidentTotal != time.Hour {
		t.Fatalf("master incidents = %d for %v, want 2 for 1h", m.Incidents, m.IncidentTotal)
	}
	if m.DiskPct != 54 {
		t.Fatalf("master DiskPct = %d, want 54", m.DiskPct)
	}
}

func TestWeeklyCountsFailedJobsInTheWindow(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	other := weeklyServer(t, st, "DB-3")
	mk := func(jid string, srv int64, status string, at time.Time) {
		t.Helper()
		if err := st.CreateJob(store.Job{ID: jid, Kind: "replica_setup", ServerID: srv, CreatedAt: at.Add(-time.Minute)}, []string{"a"}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateJob(jid, status, 0, "x", at); err != nil {
			t.Fatal(err)
		}
	}
	mk("in-window", id, store.JobFailed, weekFrom.Add(time.Hour))
	mk("in-window-2", id, store.JobFailed, weekTo.Add(-time.Hour))
	mk("too-early", id, store.JobFailed, weekFrom.Add(-time.Hour))
	mk("too-late", id, store.JobFailed, weekTo.Add(time.Minute))
	mk("succeeded", id, store.JobSucceeded, weekFrom.Add(time.Hour))
	mk("other-server", other, store.JobFailed, weekFrom.Add(time.Hour))

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusSynced), other: replicaView(other, StatusSynced)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, rows, "DB-2").FailedJobs; got != 2 {
		t.Fatalf("FailedJobs = %d, want 2", got)
	}
	if got := rowOf(t, rows, "DB-3").FailedJobs; got != 1 {
		t.Fatalf("FailedJobs of the other server = %d, want 1", got)
	}
}

func TestWeeklyUnmonitoredReplicaWithFailedJob(t *testing.T) {
	st := openStore(t)
	id := weeklyServer(t, st, "DB-2")
	quiet := weeklyServer(t, st, "DB-3")
	// Both register after the window: no monitored time in it. Only DB-2
	// has a failed job.
	statusAt(t, st, id, weekTo.Add(time.Hour), "", StatusSynced)
	statusAt(t, st, quiet, weekTo.Add(time.Hour), "", StatusSynced)
	if err := st.CreateJob(store.Job{ID: "j", Kind: "replica_setup", ServerID: id, CreatedAt: weekFrom}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob("j", store.JobFailed, 0, "x", weekFrom.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	rows, err := Weekly(st, map[int64]View{id: replicaView(id, StatusSynced), quiet: replicaView(quiet, StatusSynced)}, weekFrom, weekTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want only the one with a failed job", rows)
	}
	if r := rows[0]; !r.NotMonitored || r.FailedJobs != 1 || r.SyncedPct != 0 {
		t.Fatalf("row = %+v", r)
	}
	if got := FormatWeekly(rows, weekFrom, weekTo); strings.Contains(got, "sinkron") || !strings.Contains(got, "DB-2: belum dipantau minggu ini") {
		t.Fatalf("summary = %q", got)
	}
}

func TestFormatWeekly(t *testing.T) {
	rows := []WeeklyRow{
		{ServerName: "DB-1", Role: store.RoleMaster, DiskPct: 54},
		{ServerName: "DB-2", Role: store.RoleReplica, SyncedPct: 99.94, Incidents: 1, IncidentTotal: 4 * time.Minute, DiskPct: 71, FailedJobs: 1},
		{ServerName: "DB-3", Role: store.RoleReplica, SyncedPct: 100, DiskPct: 40, FailedJobs: 1},
		{ServerName: "DB-4", Role: store.RoleReplica, SyncedPct: 85.7, Incidents: 2, IncidentTotal: 2*time.Hour + 5*time.Minute, DiskPct: 0},
	}
	got := FormatWeekly(rows, weekFrom, weekTo)
	want := strings.Join([]string{
		"📊 Ringkasan mingguan dbpanel (26-10 s.d. 02-11)",
		"• DB-1 (master): 0 gangguan · disk 54%",
		"• DB-2: sinkron 99,9% · 1 gangguan (4 menit) · disk 71%",
		"• DB-3: sinkron 100,0% · 0 gangguan · disk 40%",
		"• DB-4: sinkron 85,7% · 2 gangguan (2 jam 5 menit)",
		"Job gagal: 2",
	}, "\n")
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.ContainsAny(got, "*_`[]") {
		t.Fatalf("summary must be plain text: %q", got)
	}
	// A replica with no monitored time shows no percentage.
	rows = []WeeklyRow{{ServerName: "DB-5", Role: store.RoleReplica, NotMonitored: true, DiskPct: 30, FailedJobs: 1}}
	want = "📊 Ringkasan mingguan dbpanel (26-10 s.d. 02-11)\n• DB-5: belum dipantau minggu ini · disk 30%\nJob gagal: 1"
	if got := FormatWeekly(rows, weekFrom, weekTo); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// No failed jobs: no last line.
	rows = []WeeklyRow{{ServerName: "DB-3", Role: store.RoleReplica, SyncedPct: 100, DiskPct: 40}}
	if got := FormatWeekly(rows, weekFrom, weekTo); strings.Contains(got, "Job gagal") {
		t.Fatalf("no failed jobs, yet: %s", got)
	}
}

func TestWeeklyDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:                "1 menit", // never "0 menit"
		4 * time.Minute:                 "4 menit",
		59*time.Minute + 40*time.Second: "1 jam",
		time.Hour:                       "1 jam",
		90 * time.Minute:                "1 jam 30 menit",
		26*time.Hour + 2*time.Minute:    "26 jam 2 menit",
	} {
		if got := weeklyDuration(d); got != want {
			t.Errorf("weeklyDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// weeklyRig is a Monitor over a store with a fake clock and a recording
// SendWeekly; start restarts the panel (a new Monitor over the same store).
type weeklyRig struct {
	t    *testing.T
	st   *store.Store
	clk  *fakeClock
	mu   sync.Mutex
	sent []string
	fail error
	logs *syncBuffer
	m    *Monitor
}

func newWeeklyRig(t *testing.T, now time.Time, set func(*Settings)) *weeklyRig {
	t.Helper()
	r := &weeklyRig{t: t, st: openStore(t), clk: &fakeClock{t: now}, logs: &syncBuffer{}}
	s := DefaultSettings()
	if set != nil {
		set(&s)
	}
	if err := SaveSettings(r.st, s); err != nil {
		t.Fatal(err)
	}
	r.start()
	return r
}

func (r *weeklyRig) start() {
	r.t.Helper()
	if r.m != nil {
		r.m.Close()
	}
	r.m = New(Deps{Store: r.st, Now: r.clk.Now, TickEvery: time.Hour, SendWeekly: r.send,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	r.t.Cleanup(r.m.Close)
}

func (r *weeklyRig) send(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, text)
	return nil
}

func (r *weeklyRig) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *weeklyRig) lastSent() string {
	v, err := r.st.GetSetting(store.SettingWeeklyLastSent)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *weeklyRig) setLastSent(at time.Time) {
	r.t.Helper()
	if err := r.st.SetSetting(store.SettingWeeklyLastSent, at.Format(time.RFC3339)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *weeklyRig) schedule() { r.m.scheduleWeekly(context.Background()) }

func TestWeeklySentOncePerOccurrence(t *testing.T) {
	r := newWeeklyRig(t, weekFrom.Add(-time.Hour), nil) // Mon 06:00
	r.setLastSent(weekFrom)

	r.schedule()
	if r.count() != 0 {
		t.Fatalf("sent before the time: %v", r.sent)
	}
	r.clk.Set(weekTo.Add(-time.Second)) // Mon 06:59:59, a week on
	r.schedule()
	if r.count() != 0 {
		t.Fatalf("sent one second early: %v", r.sent)
	}
	r.clk.Set(weekTo)
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent %d times at the time, want 1", r.count())
	}
	if !strings.Contains(r.sent[0], "(26-10 s.d. 02-11)") {
		t.Fatalf("window of the text: %q", r.sent[0])
	}
	if got := r.lastSent(); got != weekTo.Format(time.RFC3339) {
		t.Fatalf("weekly_last_sent = %q, want %q", got, weekTo.Format(time.RFC3339))
	}
	for i := 0; i < 3; i++ {
		r.clk.Add(time.Hour)
		r.schedule()
	}
	if r.count() != 1 {
		t.Fatalf("sent %d times for one occurrence", r.count())
	}
}

func TestWeeklyOnceEvenAfterRestart(t *testing.T) {
	r := newWeeklyRig(t, weekTo.Add(time.Minute), nil) // Mon 07:01
	r.setLastSent(weekFrom)

	r.start() // the panel restarts, and again, around the time
	r.schedule()
	r.start()
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent %d times across restarts, want 1", r.count())
	}
	r.clk.Add(time.Hour)
	r.start()
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent again after a restart: %d", r.count())
	}
}

func TestWeeklyMissedWhileDownIsSentOnce(t *testing.T) {
	// The panel was down at Mon 07:00 and comes up on Tuesday 09:00.
	r := newWeeklyRig(t, weekTo.Add(26*time.Hour), nil)
	r.setLastSent(weekFrom)
	r.start()
	r.schedule()
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent %d times for a missed occurrence, want 1", r.count())
	}
	if !strings.Contains(r.sent[0], "(26-10 s.d. 02-11)") {
		t.Fatalf("a missed summary covers the week it was due for: %q", r.sent[0])
	}
}

func TestWeeklyFirstRunWaitsForTheNextOccurrence(t *testing.T) {
	// A panel that never sent one starts on Tuesday: the Monday before is
	// not "missed", it predates the panel.
	r := newWeeklyRig(t, weekTo.Add(26*time.Hour), nil)
	r.schedule()
	if r.count() != 0 {
		t.Fatalf("sent on the first run: %v", r.sent)
	}
	r.clk.Set(weekTo.AddDate(0, 0, 7))
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent %d times on the next Monday, want 1", r.count())
	}
}

func TestWeeklyHonoursSettings(t *testing.T) {
	r := newWeeklyRig(t, weekTo.Add(time.Minute), func(s *Settings) { s.WeeklySummary = false })
	r.setLastSent(weekFrom)
	r.schedule()
	if r.count() != 0 {
		t.Fatalf("sent while switched off: %v", r.sent)
	}

	// A day and time of its own: Wednesday 18:30.
	r = newWeeklyRig(t, time.Date(2026, 10, 28, 18, 29, 0, 0, time.UTC), func(s *Settings) { s.WeeklySummaryAt = "Wed 18:30" })
	r.setLastSent(time.Date(2026, 10, 21, 18, 30, 0, 0, time.UTC))
	r.schedule()
	if r.count() != 0 {
		t.Fatalf("sent early: %v", r.sent)
	}
	r.clk.Add(time.Minute)
	r.schedule()
	if r.count() != 1 || !strings.Contains(r.sent[0], "(21-10 s.d. 28-10)") {
		t.Fatalf("sent = %q", r.sent)
	}

	// Without a SendWeekly dependency nothing happens, and nothing breaks.
	m := New(Deps{Store: r.st, Now: r.clk.Now})
	defer m.Close()
	m.scheduleWeekly(context.Background())
}

func TestWeeklyFailedSendIsRetriedLater(t *testing.T) {
	r := newWeeklyRig(t, weekTo.Add(time.Minute), nil)
	r.setLastSent(weekFrom)
	r.fail = errors.New("Telegram sedang tidak bisa dihubungi")

	r.schedule()
	if got := r.lastSent(); got != weekFrom.Format(time.RFC3339) {
		t.Fatalf("a failed send moved weekly_last_sent to %q", got)
	}
	r.fail = nil
	r.schedule() // too soon after the failure
	if r.count() != 0 {
		t.Fatalf("retried at once: %v", r.sent)
	}
	r.clk.Add(weeklyRetryEvery)
	r.schedule()
	if r.count() != 1 {
		t.Fatalf("sent %d times after the retry delay, want 1", r.count())
	}
	if got := r.lastSent(); got != weekTo.Format(time.RFC3339) {
		t.Fatalf("weekly_last_sent = %q", got)
	}
}

func TestWeeklyWaitsForTelegramSetup(t *testing.T) {
	r := newWeeklyRig(t, weekTo.Add(26*time.Hour), nil) // Tuesday 09:00
	r.setLastSent(weekFrom)
	r.fail = ErrWeeklyNotConfigured

	r.schedule()
	if got := r.lastSent(); got != weekFrom.Format(time.RFC3339) {
		t.Fatalf("an unsent occurrence was recorded as sent: %q", got)
	}
	// Telegram gets set up, but the retry delay has not passed.
	r.fail = nil
	r.clk.Add(weeklyDeferEvery - time.Second)
	r.schedule()
	if r.count() != 0 {
		t.Fatalf("retried before the delay: %v", r.sent)
	}
	r.clk.Add(time.Second)
	r.schedule()
	if r.count() != 1 || !strings.Contains(r.sent[0], "(26-10 s.d. 02-11)") {
		t.Fatalf("sent = %q, want last week's summary once Telegram is set up", r.sent)
	}
	if got := r.lastSent(); got != weekTo.Format(time.RFC3339) {
		t.Fatalf("weekly_last_sent = %q", got)
	}
	for i := 0; i < 3; i++ {
		r.clk.Add(weeklyDeferEvery)
		r.schedule()
	}
	if r.count() != 1 {
		t.Fatalf("sent %d times for one occurrence", r.count())
	}
}

func TestWeeklyWaitingForTelegramLogsOncePerRetry(t *testing.T) {
	r := newWeeklyRig(t, weekTo.Add(time.Minute), nil)
	r.setLastSent(weekFrom)
	r.fail = ErrWeeklyNotConfigured
	for i := 0; i < 50; i++ { // 50 ticks inside the delay
		r.schedule()
		r.clk.Add(5 * time.Second)
	}
	if n := strings.Count(r.logs.String(), "weekly summary waits"); n != 1 {
		t.Fatalf("%d log lines for 50 ticks, want 1", n)
	}
}
