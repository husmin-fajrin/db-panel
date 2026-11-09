package web_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// dashboardEnv is a panel with a fake monitor and the clock at monNow,
// without any server.
func dashboardEnv(t *testing.T) (*env, *fakeMonitor) {
	t.Helper()
	mon := &fakeMonitor{}
	e := newEnv(t, func(d *web.Deps) {
		d.Monitor = mon
		d.Now = func() time.Time { return monNow }
	})
	return e, mon
}

// syncedView is a replica judged Sinkron with lag sec.
func syncedView(id, lag int64) monitor.View {
	return monitor.View{ServerID: id, Role: store.RoleReplica, ReceivedAt: monNow.Add(-5 * time.Second),
		Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK,
			Detail: "Replikasi berjalan dan mengikuti master", LagSec: ptr(lag)}}
}

// masterView is a healthy master whose disk has free of 100 GB free.
func masterView(id int64, free uint64) monitor.View {
	return monitor.View{ServerID: id, Role: store.RoleMaster, ReceivedAt: monNow.Add(-7 * time.Second),
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK, Detail: "Master menjawab dan bisa ditulisi"},
		Snap:   &proto.HealthSnapshot{MySQLOK: true, DiskTotal: 100 << 30, DiskFree: free << 30}}
}

// gaugeOf is the markup of the dashboard gauge name.
func gaugeOf(body, name string) string {
	g := between(body, `data-gauge="`+name+`"`, `data-gauge=`)
	if i := strings.Index(g, "</section>"); i >= 0 {
		g = g[:i]
	}
	return g
}

func TestDashboardGaugesAggregate(t *testing.T) {
	e, mon := dashboardEnv(t)
	ma := e.pairedOnline(t, "db-master-a") // 1
	ta := e.makeMaster(t, ma, shopDatabases...)
	e.addReadyReplica(t, ta, "db-a1")      // 2
	e.addReadyReplica(t, ta, "db-a2")      // 3
	mb := e.pairedOnline(t, "db-master-b") // 4
	tb := e.makeMaster(t, mb, shopDatabases...)
	e.addReadyReplica(t, tb, "db-b1") // 5
	e.addReadyReplica(t, tb, "db-b2") // 6

	mon.setView(masterView(1, 59)) // 41 %
	mon.setView(masterView(4, 23)) // 77 %
	mon.setView(syncedView(2, 3))
	mon.setView(syncedView(3, 0))
	mon.setView(syncedView(5, 12))
	mon.setView(monitor.View{ServerID: 6, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusStopped, Severity: monitor.SevCrit, Detail: "Replikasi berhenti karena error 1062"}})
	if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindReplicaLagging, ServerID: 2, TopologyID: ta.ID,
		OpenedAt: monNow.Add(-time.Hour), Message: "Replika ketinggalan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindReplicaStopped, ServerID: 6, TopologyID: tb.ID,
		OpenedAt: monNow.Add(-time.Minute), Message: "Replika berhenti"}); err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantBody(t, rec, "grid-cols-2 lg:grid-cols-4")

	for name, want := range map[string][]string{
		"sinkron":    {">3/4<", "nada-error"},
		"lag":        {">12s<", "db-b1", "nada-success"},
		"disk":       {">77%<", "db-master-b", "23.0 GB bebas", "nada-success"},
		"peringatan": {">2<", "Replika berhenti", "nada-error"},
	} {
		g := gaugeOf(body, name)
		if g == "" {
			t.Errorf("no gauge %q", name)
			continue
		}
		for _, s := range want {
			if !strings.Contains(g, s) {
				t.Errorf("gauge %q lacks %q:\n%s", name, s, g)
			}
		}
	}
	if g := gaugeOf(body, "lag"); strings.Contains(g, "db-a1") {
		t.Errorf("lag gauge names a replica that is not the highest:\n%s", g)
	}
	if g := gaugeOf(body, "disk"); strings.Contains(g, "db-master-a") || strings.Contains(g, "41%") {
		t.Errorf("disk gauge is not the worst master:\n%s", g)
	}
	// Space Mono for the lag value (the gauge's centre).
	if !strings.Contains(gaugeOf(body, "lag"), "gauge-nilai") {
		t.Error("lag value is not in the gauge's Space Mono centre")
	}
}

func TestDashboardGaugeTones(t *testing.T) {
	e, mon, topo := monitorEnv(t)
	if err := e.st.SetSetting(store.SettingLagThreshold, "30"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting(store.SettingDiskWarn, "70"); err != nil {
		t.Fatal(err)
	}
	mon.setView(masterView(1, 20)) // 80 %: over disk_warn 70, under disk_crit 95
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusLagging, Severity: monitor.SevWarn, Detail: "Tertinggal 45 detik", LagSec: ptr[int64](45)}})
	if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindReplicaLagging, ServerID: 2, TopologyID: topo.ID,
		OpenedAt: monNow, Message: "Replika ketinggalan"}); err != nil {
		t.Fatal(err)
	}
	body := e.do("GET", "/", nil, e.cookie()).Body.String()
	for name, want := range map[string]string{"sinkron": "nada-warning", "lag": "nada-warning",
		"disk": "nada-warning", "peringatan": "nada-warning"} {
		if g := gaugeOf(body, name); !strings.Contains(g, want) {
			t.Errorf("gauge %q is not %s:\n%s", name, want, g)
		}
	}
	if g := gaugeOf(body, "peringatan"); !strings.Contains(g, "Replika ketinggalan") {
		t.Errorf("alert gauge does not name the newest alert:\n%s", g)
	}
}

func TestDashboardTabsHiddenForOneTopology(t *testing.T) {
	e, _, _ := monitorEnv(t)
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, `role="tablist"`, `role="tab"`)
	wantBody(t, rec, `href="/masters/1/replicas/new"`, "Tambah replika", `href="/masters/1/promote"`, "Ganti master…")

	other := e.pairedOnline(t, "db-master-2")
	topo := e.makeMaster(t, other, shopDatabases...)
	e.addReadyReplica(t, topo, "db-replika-2")
	rec = e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `x-data="topoTabs"`, `role="tablist"`, `role="tab"`, `aria-selected="true"`, `aria-selected="false"`,
		`data-tab="1"`, `data-tab="2"`, `role="tabpanel"`, `href="/masters/3/replicas/new"`)
	if n := strings.Count(rec.Body.String(), `role="tab"`); n != 2 {
		t.Errorf("%d tabs, want 2", n)
	}
	// The first topology's panel shows, the second waits hidden.
	body := rec.Body.String()
	if p := between(body, `id="topologi-2"`, ">"); !strings.Contains(p, "hidden") {
		t.Errorf("second panel not hidden: %q", p)
	}
}

func TestDashboardChartSparseNote(t *testing.T) {
	e, _, _ := monitorEnv(t)
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	// No samples in 24 hours: a sparse chart without data, said in words.
	wantBody(t, rec, `class="grafik-lag`, "grafik jarang", "Belum ada data lag 24 jam terakhir", "overflow-x-auto", "min-w-[480px]")

	if err := e.st.AddHealthSample(store.HealthSample{ServerID: 2, TS: monNow.Add(-10 * time.Minute),
		LagSec: ptr[int64](7), MySQLOK: true}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/", nil, e.cookie())
	wantNoBody(t, rec, "grafik jarang", "Belum ada data lag 24 jam terakhir")
	wantBody(t, rec, "puncak 7 dtk", "db-replika")
}

func TestDashboardEmptyState(t *testing.T) {
	e, _ := dashboardEnv(t)
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-empty", "Belum ada replika", "Tambah replika", `href="/servers"`)
	wantNoBody(t, rec, `role="tablist"`, `class="grafik-lag`)
}

func TestDashboardHeaderAndLists(t *testing.T) {
	e, mon, topo := monitorEnv(t)
	mon.setView(masterView(1, 50))
	mon.setView(syncedView(2, 0))
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "<h1>Dashboard</h1>", "Replikasi berjalan normal. Tidak ada tindakan yang menunggu Anda.",
		"Diperbarui 5 detik lalu", "Periksa sekarang", "Belum ada peringatan.", "Belum ada pekerjaan.")

	if _, err := e.st.OpenAlert(store.Alert{Kind: monitor.KindDiskCrit, ServerID: 1, TopologyID: topo.ID,
		OpenedAt: monNow.Add(-2 * time.Minute), Message: "Disk db-master 96%"}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/", nil, e.cookie())
	wantBody(t, rec, "1 peringatan terbuka", "Disk kritis", "lencana-error", ">Kritis<", `href="/alerts"`, `href="/jobs"`)
	wantNoBody(t, rec, "Replikasi berjalan normal")
	// Badges are the shared partial, never daisyUI's.
	if main := between(rec.Body.String(), "data-page-head", "</main>"); strings.Contains(main, `class="badge`) {
		t.Error("dashboard uses a daisyUI badge")
	}
	// Recent jobs: Status second, so a 390 px screen shows it (UX3).
	if err := e.st.CreateJob(store.Job{ID: "dashjob0123456789", Kind: "prepare_master", ServerID: 1,
		ParamsJSON: "{}", CreatedAt: monNow}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/", nil, e.cookie())
	wantBody(t, rec, "<th>Pekerjaan</th><th>Status</th><th>Server</th><th>Dibuat</th>")
}

func TestDashboardBannersKept(t *testing.T) {
	e, _, _ := promotedEnv(t)
	rec := e.do("GET", "/", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "data-promote-cutover", "Promote menunggu pengalihan aplikasi", `href="/masters/2/cutover"`,
		"Buka layar pengalihan", `class="banner banner-warning"`)

	e2, _, _ := controlEnv(t)
	topo, err := e2.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.st.OpenAlert(store.Alert{Kind: monitor.KindPromotePartial, ServerID: 2, TopologyID: topo.ID,
		OpenedAt: time.Now(), Message: "Promote <selesai> sebagian"}); err != nil {
		t.Fatal(err)
	}
	rec = e2.do("GET", "/", nil, e2.cookie())
	wantBody(t, rec, "data-promote-partial", "Promote belum tuntas", "Promote &lt;selesai&gt; sebagian",
		`href="/alerts"`, "Lihat peringatan", `class="banner banner-error"`)
}

// TestDashboardMengejarCountsAsSynced: the display-only Mengejar label
// does not change the Sinkron gauge, which counts the monitor's code.
func TestDashboardMengejarCountsAsSynced(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(masterView(1, 50))
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow.Add(-3 * time.Second),
		Status: monitor.Status{Code: monitor.StatusSynced, Severity: monitor.SevOK,
			Detail: "Replikasi berjalan dan mengikuti master", LagSec: ptr[int64](0), GTIDGap: 8, GTIDGapKnown: true}})
	body := e.do("GET", "/", nil, e.cookie()).Body.String()
	g := gaugeOf(body, "sinkron")
	for _, s := range []string{">1/1<", "nada-success", "semua mengikuti master · 1 mengejar"} {
		if !strings.Contains(g, s) {
			t.Errorf("sinkron gauge lacks %q:\n%s", s, g)
		}
	}
	if strings.Contains(g, "perlu perhatian") {
		t.Errorf("a catching-up replica needs attention:\n%s", g)
	}
	// The row still says Mengejar.
	if row := between(body, `data-status-for="2"`, `</li>`); !strings.Contains(row, ">Mengejar<") {
		t.Errorf("row is not Mengejar:\n%s", row)
	}
}

// TestDashboardDiskRoundsDown: 94.6 % used reads 94 %, as the disk alerts
// judge it, so it is not red under disk_crit_pct 95.
func TestDashboardDiskRoundsDown(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusHealthy, Severity: monitor.SevOK},
		Snap:   &proto.HealthSnapshot{MySQLOK: true, DiskTotal: 1000 << 30, DiskFree: 54 << 30}})
	g := gaugeOf(e.do("GET", "/", nil, e.cookie()).Body.String(), "disk")
	for _, s := range []string{">94%<", "nada-warning", ">Disk master<"} {
		if !strings.Contains(g, s) {
			t.Errorf("disk gauge lacks %q:\n%s", s, g)
		}
	}
	if strings.Contains(g, ">95%<") || strings.Contains(g, "nada-error") {
		t.Errorf("disk gauge rounds 94.6%% up to critical:\n%s", g)
	}
}

// TestDashboardSparseFromSeries: a replica whose newest sample is two
// hours old has a chart, but a sparse one.
func TestDashboardSparseFromSeries(t *testing.T) {
	e, _, _ := monitorEnv(t)
	if err := e.st.AddHealthSample(store.HealthSample{ServerID: 2, TS: monNow.Add(-2 * time.Hour),
		LagSec: ptr[int64](4), MySQLOK: true}); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/", nil, e.cookie())
	wantBody(t, rec, "grafik jarang", "puncak 4 dtk", ">Peringatan<")
	wantNoBody(t, rec, "Belum ada data lag 24 jam terakhir")
}
