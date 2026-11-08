package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// monNow is the panel's clock in the monitoring tests.
var monNow = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

// monitorEnv is a master (server 1, db-master) with a ready replica
// (server 2, db-replika) following toko and gudang, a fake monitor and the
// panel's clock fixed at monNow.
func monitorEnv(t *testing.T) (*env, *fakeMonitor, store.Topology) {
	t.Helper()
	mon := &fakeMonitor{}
	e := newEnv(t, func(d *web.Deps) {
		d.Monitor = mon
		d.Now = func() time.Time { return monNow }
	})
	master := e.pairedOnline(t, "db-master")
	topo := e.makeMaster(t, master, shopDatabases...)
	e.addReadyReplica(t, topo, "db-replika", "toko", "gudang")
	return e, mon, topo
}

// addReadyReplica adds a paired, online server name as a ready replica of
// topo.
func (e *env) addReadyReplica(t *testing.T, topo store.Topology, name string, dbs ...string) store.Server {
	t.Helper()
	srv := e.pairedOnline(t, name)
	if _, err := e.st.AddReplica(topo.ID, srv.ID, dbs); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(srv.ID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	return srv
}

func ptr[T any](v T) *T { return &v }

// between returns the part of s from the first from to the next to after
// it (to excluded); "" when from is missing.
func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest[len(from):], to); j >= 0 {
		return rest[:len(from)+j]
	}
	return rest
}

// rawSQLError is MySQL's own message, which only "Detail teknis" shows.
const rawSQLError = "Could not execute Write_rows event on table toko.pesanan; Duplicate entry 7 for key PRIMARY, Error_code: 1062"

// stoppedOn1062 is a replica stopped by a duplicate-key error.
func stoppedOn1062(id int64) monitor.View {
	return monitor.View{ServerID: id, Role: store.RoleReplica,
		Status:     monitor.Status{Code: monitor.StatusStopped, Severity: monitor.SevCrit, Detail: "Replikasi berhenti karena error 1062"},
		ReceivedAt: monNow.Add(-5 * time.Second),
		Snap: &proto.HealthSnapshot{MySQLOK: true, SuperReadOnly: true, Replica: &proto.ReplicaStatus{
			IORunning: "Yes", SQLRunning: "No", SourceHost: "10.0.0.1", SourcePort: 3306,
			LastSQLErrno: 1062, LastSQLError: rawSQLError,
			RetrievedGTID: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-120",
			ExecutedGTID:  "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-117",
			Filters:       []string{"toko.%", "gudang.%"},
		}}}
}

func TestDashboardShowsStatusPerReplica(t *testing.T) {
	e, mon, topo := monitorEnv(t)
	e.addReadyReplica(t, topo, "db-cadangan")
	e.addReadyReplica(t, topo, "db-baru")
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow.Add(-2 * time.Second),
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK, Detail: "Master menjawab dan bisa ditulisi"}})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow.Add(-12 * time.Second),
		Status: monitor.Status{Code: monitor.StatusLagging, Severity: monitor.SevWarn, Detail: "Tertinggal 42 detik",
			LockLost: true, LagSec: ptr[int64](42)}})
	mon.setView(monitor.View{ServerID: 3, Role: store.RoleReplica, ReceivedAt: monNow.Add(-3 * time.Minute),
		Status: monitor.Status{Code: monitor.StatusLagging, Severity: monitor.SevWarn, Detail: "Tertinggal 5 transaksi",
			LagSec: ptr[int64](0), GTIDGap: 5}})
	// Server 4 has no view yet.
	if _, err := e.st.OpenAlert(store.Alert{Kind: "replica_stopped", ServerID: 2, TopologyID: topo.ID,
		OpenedAt: monNow, Message: "Replika berhenti"}); err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantBody(t, rec, `data-stat="masters">1<`, `data-stat="replicas">3<`, `data-stat="alerts">1<`, `href="/alerts"`,
		`href="/masters/1"`, `href="/replicas/2"`, `href="/replicas/3"`, `href="/replicas/4"`)

	master := between(body, `data-status-for="1"`, `data-status-for=`)
	for _, s := range []string{"Sehat", "badge-success", "Master menjawab dan bisa ditulisi"} {
		if !strings.Contains(master, s) {
			t.Errorf("master row lacks %q:\n%s", s, master)
		}
	}
	rep2 := between(body, `data-status-for="2"`, `data-status-for=`)
	for _, s := range []string{"Ketinggalan", "badge-warning", "Kunci terlepas", "tertinggal 42 dtk", "dicek 12 dtk lalu"} {
		if !strings.Contains(rep2, s) {
			t.Errorf("replica 2 row lacks %q:\n%s", s, rep2)
		}
	}
	rep3 := between(body, `data-status-for="3"`, `data-status-for=`)
	for _, s := range []string{"Ketinggalan", "tertinggal 5 transaksi", "dicek 3 menit lalu"} {
		if !strings.Contains(rep3, s) {
			t.Errorf("replica 3 row lacks %q:\n%s", s, rep3)
		}
	}
	if strings.Contains(rep3, "Kunci terlepas") {
		t.Errorf("replica 3 row shows a lost lock:\n%s", rep3)
	}
	rep4 := between(body, `data-status-for="4"`, `</li>`)
	if !strings.Contains(rep4, "Belum dipantau") || !strings.Contains(rep4, "badge-ghost") {
		t.Errorf("replica 4 row is not Belum dipantau:\n%s", rep4)
	}
	// Rows ask for the compact fragment on live updates.
	wantBody(t, rec, `data-status-for="2" data-status-variant="baris"`)
}

func TestReplicaPageShowsExplanationAndPrimaryButton(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(stoppedOn1062(2))
	rec := e.do("GET", "/replicas/2", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	status := between(rec.Body.String(), `data-status-for="2"`, `<h2 class="card-title">Kontrol`)
	for _, s := range []string{"Berhenti", "badge-error", "Replikasi berhenti karena error 1062",
		"Data ganda: ada baris yang sudah ada di replika.", "Saran:", "Resync ulang"} {
		if !strings.Contains(status, s) {
			t.Errorf("status section lacks %q:\n%s", s, status)
		}
	}
	// The explanation's advice is Resync ulang: its button is the primary
	// one, Start replika is outlined.
	resync := regexp.MustCompile(`<button type="submit" class="([^"]*)">Resync ulang</button>`).FindStringSubmatch(rec.Body.String())
	if resync == nil || !strings.Contains(resync[1], "btn-primary") {
		t.Errorf("Resync ulang button is not btn-primary: %v", resync)
	}
	start := regexp.MustCompile(`<button type="button" class="([^"]*)" data-open-modal="modal-start">`).FindStringSubmatch(rec.Body.String())
	if start == nil || !strings.Contains(start[1], "btn-outline") || strings.Contains(start[1], "btn-primary") {
		t.Errorf("Start replika button is not outlined: %v", start)
	}

	// Error 1594: Start replika is the primary button.
	v := stoppedOn1062(2)
	v.Snap.Replica.LastSQLErrno = 1594
	mon.setView(v)
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	wantBody(t, rec, "Relay log di replika rusak.")
	start = regexp.MustCompile(`<button type="button" class="([^"]*)" data-open-modal="modal-start">`).FindStringSubmatch(rec.Body.String())
	if start == nil || !strings.Contains(start[1], "btn-primary") {
		t.Errorf("Start replika button is not btn-primary for 1594: %v", start)
	}

	// IO error 2003: the master's page is the way forward.
	v = stoppedOn1062(2)
	v.Snap.Replica.LastSQLErrno, v.Snap.Replica.LastIOErrno = 0, 2003
	mon.setView(v)
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	wantBody(t, rec, "Master tidak bisa dihubungi dari replika.")
	if !regexp.MustCompile(`<a class="[^"]*btn-primary[^"]*" href="/masters/1">`).MatchString(rec.Body.String()) {
		t.Errorf("no primary link to the master page for 2003:\n%s", rec.Body.String())
	}

	// A synced replica: no explanation, no highlighted button.
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK, Detail: "Replikasi berjalan dan mengikuti master", LagSec: ptr[int64](0)}})
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	wantBody(t, rec, "Sinkron", "badge-success", "tertinggal 0 dtk")
	wantNoBody(t, rec, "Saran:", "btn-primary\">Resync ulang")
}

func TestReplicaPageTechnicalDetailsHoldRawError(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(stoppedOn1062(2))
	rec := e.do("GET", "/replicas/2", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	tech := between(body, "<details", "</details>")
	if !strings.Contains(tech, "Detail teknis") {
		t.Fatalf("no Detail teknis section:\n%s", body)
	}
	for _, s := range []string{rawSQLError, "1062", "10.0.0.1:3306", "toko.%", "gudang.%", "Berhenti"} {
		if !strings.Contains(tech, s) {
			t.Errorf("Detail teknis lacks %q:\n%s", s, tech)
		}
	}
	if n := strings.Count(body, rawSQLError); n != 1 {
		t.Errorf("raw error appears %d times, want once (in Detail teknis)", n)
	}
	if h1 := between(body, "<h1", "</h1>"); strings.Contains(h1, "Duplicate") {
		t.Errorf("raw error in the title: %s", h1)
	}
	// Facts: GTID received against executed and the gap; a gap never
	// computed (no master GTID set to compare with) is not "0 transaksi".
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	wantBody(t, rec, "Belum diterima dari master</th><td>belum dihitung")
	wantNoBody(t, rec, "0 transaksi")
	mon.setView(func() monitor.View {
		v := stoppedOn1062(2)
		v.Status.GTIDGap, v.Status.GTIDGapKnown = 3, true
		return v
	}())
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	wantBody(t, rec, "GTID diterima", "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-120", "GTID dieksekusi",
		"3e11fa47-71ca-11e1-9e33-c80aa9429562:1-117", "3 transaksi")

	// MySQL's own connection error stays out of the status sentence too.
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusUnreachable, Severity: monitor.SevCrit, Detail: "dial tcp 127.0.0.1:3306: connect: connection refused"},
		Snap:   &proto.HealthSnapshot{MySQLError: "dial tcp 127.0.0.1:3306: connect: connection refused"}})
	rec = e.do("GET", "/replicas/2", nil, e.cookie())
	body = rec.Body.String()
	if status := between(body, `data-status-for="2"`, `<h2 class="card-title">Kontrol`); strings.Contains(status, "dial tcp") ||
		!strings.Contains(status, "MySQL tidak menjawab") {
		t.Errorf("status section of an unreachable replica:\n%s", status)
	}
	if !strings.Contains(between(body, "<details", "</details>"), "dial tcp 127.0.0.1:3306") {
		t.Errorf("Detail teknis lacks MySQL's error:\n%s", body)
	}
}

// masterSnap is a healthy master's report.
func masterSnap() *proto.HealthSnapshot {
	return &proto.HealthSnapshot{MySQLOK: true, Version: "8.0.39", UptimeSec: 90061, ThreadsConnected: 12,
		MaxConnections: 151, BinlogDumpThreads: 1, BinlogTotalBytes: 3 << 30,
		BinlogOldestAgeSec: ptr[int64](3 * 86400), BinlogExpireSec: 7 * 86400,
		DiskTotal: 100 << 30, DiskFree: 29 << 30, MemTotal: 16 << 30, MemAvailable: 6 << 30, Load1: 0.42}
}

func TestMasterPageDownShowsManualGuidance(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	down := "Diduga tumbang (agent offline): agent tidak terhubung 3 menit dan 1 replika kehilangan koneksi"
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow.Add(-3 * time.Minute),
		Status: monitor.Status{Code: monitor.StatusDown, Severity: monitor.SevCrit, Detail: down}, Snap: masterSnap()})
	rec := e.do("GET", "/masters/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	status := between(rec.Body.String(), `data-status-for="1"`, `<h2 class="card-title">Kontrol`)
	for _, s := range []string{"Diduga tumbang", "badge-error", down, "Yang bisa dilakukan sekarang",
		"STOP REPLICA; RESET REPLICA ALL; SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF;",
		"tetap terbuka walau MySQL di-restart",
		"Executed_Gtid_Set", "panel belum bisa"} {
		if !strings.Contains(status, s) {
			t.Errorf("status section lacks %q:\n%s", s, status)
		}
	}

	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK, Detail: "Master menjawab dan bisa ditulisi"}, Snap: masterSnap()})
	rec = e.do("GET", "/masters/1", nil, e.cookie())
	wantBody(t, rec, "Sehat", "badge-success")
	wantNoBody(t, rec, "Yang bisa dilakukan sekarang", "RESET REPLICA ALL")
}

func TestMasterPageShowsFacts(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow.Add(-7 * time.Second),
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK, Detail: "Master menjawab dan bisa ditulisi"}, Snap: masterSnap()})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK, LagSec: ptr[int64](0)}})
	rec := e.do("GET", "/masters/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "dicek 7 dtk lalu", "8.0.39", "1 hari 1 jam", "1 dari 1", "3 hari", "7 hari",
		"12 / 151", "71%", "100.0 GB", "6.0 GB", "16.0 GB", "0.42",
		// The replica list carries each replica's live status.
		`data-status-for="2"`, "Sinkron")
}

func TestTimelineMergesEventsAndAudit(t *testing.T) {
	e, _, topo := monitorEnv(t)
	add := func(ev store.StatusEvent) {
		t.Helper()
		ev.ServerID, ev.TopologyID = 2, topo.ID
		if _, err := e.st.AddStatusEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	add(store.StatusEvent{TS: monNow.Add(-time.Hour), Kind: "status", FromStatus: "Sinkron", ToStatus: "Berhenti",
		Detail: "Replikasi berhenti karena error 1062"})
	add(store.StatusEvent{TS: monNow.Add(-30 * time.Minute), Kind: "lock", Detail: "Kunci terlepas"})
	add(store.StatusEvent{TS: monNow.Add(-30 * 24 * time.Hour), Kind: "restart", Detail: "MySQL dimulai ulang"})
	for _, a := range []store.AuditEntry{
		{TS: monNow.Add(-10 * time.Minute), Username: "admin", Action: "replica_stop", TargetKind: "replica", TargetID: 2, Outcome: "ok"},
		{TS: monNow.Add(-5 * time.Minute), Username: "admin", Action: "replica_start", TargetKind: "replica", TargetID: 2,
			Outcome: "agent: sql: connection is already closed"},
		// Another server's entry and a session entry stay out.
		{TS: monNow.Add(-time.Minute), Username: "admin", Action: "server_check", TargetKind: "server", TargetID: 1, Outcome: "ok"},
	} {
		if _, err := e.st.AddAudit(a); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.do("GET", "/replicas/2", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	tl := between(rec.Body.String(), `id="garis-waktu"`, `</section>`)
	order := []string{"Start replika", "Stop replika", "Kunci terlepas", "Sinkron → Berhenti"}
	last := -1
	for _, s := range order {
		i := strings.Index(tl, s)
		if i < 0 {
			t.Fatalf("timeline lacks %q:\n%s", s, tl)
		}
		if i < last {
			t.Errorf("timeline out of order at %q:\n%s", s, tl)
		}
		last = i
	}
	for _, s := range []string{"admin", "Gagal", "Replikasi berhenti karena error 1062", "7 hari"} {
		if !strings.Contains(tl, s) {
			t.Errorf("timeline lacks %q", s)
		}
	}
	for _, s := range []string{"MySQL dimulai ulang", "Periksa sekarang", "connection is already closed"} {
		if strings.Contains(tl, s) {
			t.Errorf("7-day timeline contains %q:\n%s", s, tl)
		}
	}

	rec = e.do("GET", "/replicas/2?hari=90", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	tl = between(rec.Body.String(), `id="garis-waktu"`, `</section>`)
	if !strings.Contains(tl, "MySQL dimulai ulang") || !strings.Contains(tl, "90 hari") {
		t.Errorf("90-day timeline lacks the old restart:\n%s", tl)
	}

	// The master's page has its own timeline.
	rec = e.do("GET", "/masters/1", nil, e.cookie())
	tl = between(rec.Body.String(), `id="garis-waktu"`, `</section>`)
	if !strings.Contains(tl, "Periksa sekarang") || strings.Contains(tl, "Stop replika") {
		t.Errorf("master timeline:\n%s", tl)
	}
}

func TestStatusFragmentEndpoint(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	mon.setView(stoppedOn1062(2))

	rec := e.do("GET", "/servers/2/status-fragment", nil, nil)
	wantCode(t, rec, http.StatusSeeOther) // to the login

	rec = e.do("GET", "/servers/2/status-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	wantBody(t, rec, "Berhenti", "badge-error", "Data ganda", "dicek 5 dtk lalu")
	wantNoBody(t, rec, "<html", "<nav", rawSQLError)

	// The compact variant for rows: no explanation.
	rec = e.do("GET", "/servers/2/status-fragment?tampilan=baris", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Berhenti", "badge-error")
	wantNoBody(t, rec, "Data ganda", "<html")

	// A master's fragment.
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusLocked, Severity: monitor.SevCrit, Detail: "Master dikunci"}})
	rec = e.do("GET", "/servers/1/status-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Terkunci", "badge-error", "Master dikunci")

	// MySQL not connected to the panel yet: not monitored.
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusUnreachable, Severity: monitor.SevCrit, Detail: "belum terhubung"},
		Snap:   &proto.HealthSnapshot{MySQLError: "belum terhubung"}})
	rec = e.do("GET", "/servers/1/status-fragment", nil, c)
	wantBody(t, rec, "Belum dipantau", "badge-ghost")
	wantNoBody(t, rec, "Tidak terjangkau")

	// A server outside any topology, and one whose agent is too old.
	srv := e.pairedOnline(t, "db-lain")
	rec = e.do("GET", "/servers/3/status-fragment", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Belum dipantau", "badge-ghost", "belum menerima laporan")
	mon.setView(monitor.View{ServerID: srv.ID, ReceivedAt: monNow.Add(-4 * time.Second)})
	rec = e.do("GET", "/servers/3/status-fragment", nil, c)
	wantBody(t, rec, "Belum dipantau", "belum menjadi master atau replika", "dicek 4 dtk lalu")
	mon.setView(monitor.View{ServerID: srv.ID, NeedsUpgrade: true})
	rec = e.do("GET", "/servers/3/status-fragment", nil, c)
	wantBody(t, rec, "Agent perlu dimutakhirkan")

	for _, p := range []string{"/servers/99/status-fragment", "/servers/x/status-fragment"} {
		rec = e.do("GET", p, nil, c)
		wantCode(t, rec, http.StatusNotFound)
	}
}

func TestServersListNeedsUpgradeBadge(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	if err := e.st.UpdateProbe(1, proto.ProbeResult{OS: "Ubuntu 24.04"}, "v0.2.0", monNow); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateProbe(2, proto.ProbeResult{OS: "Ubuntu 24.04"}, "v0.3.0", monNow); err != nil {
		t.Fatal(err)
	}
	other := e.pairedOnline(t, "db-lain")
	if err := e.st.UpdateProbe(other.ID, proto.ProbeResult{OS: "Debian 12"}, "v0.2.0", monNow); err != nil {
		t.Fatal(err)
	}
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, NeedsUpgrade: true,
		Status: monitor.Status{Code: monitor.StatusNeedsUpgrade, Severity: monitor.SevNeutral}})
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK}})
	mon.setView(monitor.View{ServerID: other.ID, NeedsUpgrade: true})
	// Server 1 has a re-pairing token from this process: its command is shown.
	if _, err := e.st.CreatePairToken(1, monNow, time.Hour); err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", "/servers", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantBody(t, rec, "Status pemantauan", "Versi agent")
	row1 := between(body, `href="/servers/1"`, "</tr>")
	for _, s := range []string{"v0.2.0", "perlu dimutakhirkan", "Agent perlu dimutakhirkan", "/agent/install?token=", "sha256sum -c"} {
		if !strings.Contains(row1, s) {
			t.Errorf("row 1 lacks %q:\n%s", s, row1)
		}
	}
	row2 := between(body, `href="/servers/2"`, "</tr>")
	if !strings.Contains(row2, "Sinkron") || !strings.Contains(row2, "v0.3.0") || strings.Contains(row2, "perlu dimutakhirkan") {
		t.Errorf("row 2:\n%s", row2)
	}
	row3 := between(body, `href="/servers/3"`, "</tr>")
	for _, s := range []string{"perlu dimutakhirkan", `href="/servers/3"`, "halaman server"} {
		if !strings.Contains(row3, s) {
			t.Errorf("row 3 lacks %q:\n%s", s, row3)
		}
	}
	if strings.Contains(row3, "/agent/install") {
		t.Errorf("row 3 shows a command without a token:\n%s", row3)
	}
}

// TestTemplatesParse: every page parses with the shared status and
// timeline fragments, which the fragment endpoint renders on their own.
func TestTemplatesParse(t *testing.T) {
	r, err := web.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"status-fragment", "status-full", "status-row", "status-badge-health", "timeline"} {
		if !web.HasFragment(r, name) {
			t.Errorf("fragment %q not defined", name)
		}
		for _, page := range []string{"dashboard", "servers", "master_detail", "replica_detail"} {
			if !web.HasPageTemplate(r, page, name) {
				t.Errorf("page %s lacks template %q", page, name)
			}
		}
	}
}

func TestMonitorPagesHaveNoInlineScript(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusDown, Severity: monitor.SevCrit, Detail: "Diduga tumbang"}, Snap: masterSnap()})
	mon.setView(stoppedOn1062(2))
	if _, err := e.st.AddStatusEvent(store.StatusEvent{ServerID: 2, TS: monNow.Add(-time.Minute), Kind: "lock", Detail: "Kunci terlepas"}); err != nil {
		t.Fatal(err)
	}
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>[^<]|\son[a-z]+=|javascript:|\sstyle=`)
	for _, p := range []string{"/", "/servers", "/masters/1", "/replicas/2", "/replicas/2?hari=90",
		"/servers/1/status-fragment", "/servers/2/status-fragment?tampilan=baris"} {
		rec := e.do("GET", p, nil, c)
		wantCode(t, rec, http.StatusOK)
		if m := inline.FindString(rec.Body.String()); m != "" {
			t.Errorf("GET %s has inline script/handler/style %q", p, m)
		}
	}
}

func TestMonitorFormatters(t *testing.T) {
	for sec, want := range map[int64]string{-5: "0 dtk", 0: "0 dtk", 59: "59 dtk", 60: "1 menit", 3599: "59 menit",
		3600: "1 jam 0 menit", 3*3600 + 25*60: "3 jam 25 menit", 86400: "1 hari 0 jam", 2*86400 + 5*3600 + 59: "2 hari 5 jam"} {
		if got := web.FmtDuration(sec); got != want {
			t.Errorf("fmtDuration(%d) = %q, want %q", sec, got, want)
		}
	}
	for _, c := range []struct {
		used, total uint64
		want        string
	}{{0, 0, "-"}, {0, 100, "0%"}, {71, 100, "71%"}, {2, 3, "67%"}, {100 << 30, 100 << 30, "100%"}} {
		if got := web.FmtPct(c.used, c.total); got != c.want {
			t.Errorf("fmtPct(%d, %d) = %q, want %q", c.used, c.total, got, c.want)
		}
	}
	for d, want := range map[time.Duration]string{-time.Second: "0 dtk lalu", 12 * time.Second: "12 dtk lalu",
		3*time.Minute + 5*time.Second: "3 menit lalu", 2 * time.Hour: "2 jam lalu", 49 * time.Hour: "2 hari lalu"} {
		if got := web.Ago(monNow.Add(-d), monNow); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestMasterPageSetSourceGuidance (spec 5.5): after the address for
// replicas changes, the master page lists, per replica, the set-source
// command to run on it; later it keeps listing the replicas whose last
// report follows another address.
func TestMasterPageSetSourceGuidance(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	following := func(host string, port int) {
		snap := masterSnap()
		snap.Replica = &proto.ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", SourceHost: host, SourcePort: port}
		mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow, Snap: snap,
			Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK}})
	}
	wantNoBody(t, e.do("GET", "/masters/1", nil, c), "data-set-source", "dbpanel agent set-source")
	following("10.0.0.1", 3306)
	wantNoBody(t, e.do("GET", "/masters/1", nil, c), "data-set-source", "dbpanel agent set-source")

	rec := e.do("POST", "/masters/1/source", url.Values{"source_host": {"192.168.5.9"}, "source_port": {"3307"}}, c)
	wantLocation(t, rec, "/masters/1?pesan=sumber")
	rec = e.do("GET", "/masters/1?pesan=sumber", nil, c)
	wantBody(t, rec, "data-set-source", `href="/replicas/2">db-replika</a>`, "sudo dbpanel agent set-source 192.168.5.9:3307",
		"Resync ulang")
	// Until the replica follows the new address, the block stays.
	rec = e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, "data-set-source", "(sekarang mengikuti 10.0.0.1:3306)", "sudo dbpanel agent set-source 192.168.5.9:3307")
	following("192.168.5.9", 3307)
	wantNoBody(t, e.do("GET", "/masters/1", nil, c), "data-set-source", "dbpanel agent set-source")
}

// TestQuarantinePendingExplanation: an old master not yet quarantined is
// explained as unprotected, a quarantined one as locked.
func TestQuarantinePendingExplanation(t *testing.T) {
	v := monitor.View{ServerID: 4, Role: store.RoleQuarantined, Status: monitor.DeriveQuarantined(true), QuarantinePending: true}
	e := web.StatusExplain(4, store.RoleQuarantined, v)
	if e == nil || strings.Contains(e.Text, "Panel mengunci") || !strings.Contains(e.Advice, "Jangan hidupkan") {
		t.Fatalf("pending explanation = %+v", e)
	}
	v.Status, v.QuarantinePending = monitor.DeriveQuarantined(false), false
	if e := web.StatusExplain(4, store.RoleQuarantined, v); e == nil || !strings.Contains(e.Text, "Panel mengunci") {
		t.Fatalf("quarantined explanation = %+v", e)
	}
}

// TestServerPageShowsPins: the server page's Detail teknis says what the
// agent's last report says of its pins, and why a pin file cannot be
// read; without pins in the report there is no such section.
func TestServerPageShowsPins(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	snap := masterSnap()
	snap.RolePin = "master"
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: snap,
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK}})
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	tech := between(rec.Body.String(), "Detail teknis", "</details>")
	for _, want := range []string{"Peran dipin: master", "Sumber dipin: tidak ada"} {
		if !strings.Contains(tech, want) {
			t.Errorf("Detail teknis lacks %q:\n%s", want, tech)
		}
	}

	snap = masterSnap()
	snap.RolePin, snap.SourcePin = "quarantined", "10.0.0.2:3306"
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: snap})
	rec = e.do("GET", "/servers/1", nil, e.cookie())
	wantBody(t, rec, "Peran dipin: dikarantina", "Sumber dipin: 10.0.0.2:3306")

	snap = masterSnap()
	snap.PinError = "role harus milik root; jalankan: chown root:root role"
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: snap})
	rec = e.do("GET", "/servers/1", nil, e.cookie())
	wantBody(t, rec, "Peran dipin: -", "Berkas pin tidak bisa dibaca", "role harus milik root")

	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: masterSnap()})
	rec = e.do("GET", "/servers/1", nil, e.cookie())
	wantNoBody(t, rec, "Peran dipin")
}

// TestServerPageShowsDroppedReports (spec 5.12): reports the hub dropped
// for flooding in the last hour show in the server's Detail teknis, also
// from an agent that reports no pins.
func TestServerPageShowsDroppedReports(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: masterSnap(), ReportsDropped: 12})
	rec := e.do("GET", "/servers/1", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	tech := between(rec.Body.String(), "Detail teknis", "</details>")
	if !strings.Contains(tech, "12 laporan dibuang dalam 1 jam terakhir") {
		t.Fatalf("Detail teknis lacks the dropped reports:\n%s", tech)
	}
	wantNoBody(t, rec, "Peran dipin")

	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: masterSnap()})
	wantNoBody(t, e.do("GET", "/servers/1", nil, e.cookie()), "laporan dibuang")
}
