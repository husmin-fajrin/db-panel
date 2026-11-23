package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// gateRig is a prepared master db-master (10.0.0.1) with a certificate, a
// backup folder and a ready verified replica db-replica; db-target is a
// server outside the topology with MySQL connected (Buat replika) and
// db-old another (the old master of Bangun ulang). Every agent is online
// at the panel's version, logs in as dbpanel, and reports a healthy
// snapshot taken now; both settings marks are set. Every row of every
// action is then green.
type gateRig struct {
	e                *env
	h                *fakeHealth
	m, rep, tgt, old *fakeAgent
	topo             store.Topology
	clockMu          sync.Mutex
	clock            time.Time
	probeMu          sync.Mutex
	probes           []string // the host:port of each port probe
	probeOpen        bool
	probeWait        chan struct{} // when set, each probe waits for it
}

const gateVersion = "1.4.0"

func newGateRig(t *testing.T) *gateRig {
	t.Helper()
	e := newEnv(t)
	g := &gateRig{e: e, h: &fakeHealth{views: map[int64]monitor.View{}}, clock: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	e.r.d.Health = g.h
	e.r.d.Version = gateVersion
	e.r.now = g.now
	e.r.d.PortProbe = g.probe
	g.m, g.topo = e.readyMaster("db-master")
	g.rep = e.agent("db-replica", "10.0.0.2")
	g.tgt = e.agent("db-target", "10.0.0.3")
	g.old = e.agent("db-old", "10.0.0.4")
	if _, err := e.st.AddReplica(g.topo.ID, g.rep.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(g.rep.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaTLSVerified(g.rep.id, true); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerTLS(store.ServerTLS{ServerID: g.m.id, CertSerial: "01", NotAfter: g.clock.AddDate(1, 0, 0),
		InstalledAt: g.clock}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerBackup(g.m.id, "/srv/backup", 0); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{store.SettingAuditForwardedAt, store.SettingPanelKeyBackedUpAt} {
		if err := e.st.SetSetting(k, g.clock.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	for _, fa := range []*fakeAgent{g.m, g.rep, g.tgt, g.old} {
		if err := e.st.UpdateProbe(fa.id, proto.ProbeResult{Hostname: fa.name}, gateVersion, g.clock); err != nil {
			t.Fatal(err)
		}
		g.setAccount(fa, true, "dbpanel@localhost")
		pin := "replica"
		if fa == g.m {
			pin = "master"
		}
		g.h.set(fa.id, g.goodView(pin))
	}
	return g
}

func (g *gateRig) now() time.Time {
	g.clockMu.Lock()
	defer g.clockMu.Unlock()
	return g.clock
}

func (g *gateRig) advance(d time.Duration) {
	g.clockMu.Lock()
	g.clock = g.clock.Add(d)
	g.clockMu.Unlock()
}

func (g *gateRig) probe(ctx context.Context, hostPort string) bool {
	g.probeMu.Lock()
	g.probes = append(g.probes, hostPort)
	open, wait := g.probeOpen, g.probeWait
	g.probeMu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
		}
	}
	return open
}

func (g *gateRig) probed() []string {
	g.probeMu.Lock()
	defer g.probeMu.Unlock()
	return slices.Clone(g.probes)
}

// goodView is a healthy report taken now: role pin, the agent's clock
// equal to the panel's, performance_schema on, the disk half full and a
// backup file an hour old.
func (g *gateRig) goodView(pin string) monitor.View {
	now := g.now()
	perf := true
	return monitor.View{Status: monitor.Status{Code: monitor.StatusHealthy}, ReceivedAt: now,
		Snap: &proto.HealthSnapshot{MySQLOK: true, RolePin: pin, ClockUnixMS: now.UnixMilli(), PerfSchema: &perf,
			DiskTotal: 1000, DiskFree: 500, Backup: &proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 3600, NewestSize: 1024}}}
}

// snap changes the snapshot of fa's last report.
func (g *gateRig) snap(fa *fakeAgent, change func(*proto.HealthSnapshot)) {
	v, _ := g.h.View(fa.id)
	s := *v.Snap
	change(&s)
	v.Snap = &s
	g.h.set(fa.id, v)
}

func (g *gateRig) setAccount(fa *fakeAgent, connected bool, account string) {
	g.e.t.Helper()
	inv, err := g.e.st.GetInventory(fa.id)
	if errors.Is(err, store.ErrNotFound) {
		inv = store.Inventory{ServerID: fa.id, Version: "8.4.3", Series: "8.4", UpdatedAt: g.now()}
	} else if err != nil {
		g.e.t.Fatal(err)
	}
	inv.Connected, inv.Account = connected, account
	if err := g.e.st.SetInventory(inv); err != nil {
		g.e.t.Fatal(err)
	}
}

// input is the rig's usual input for action.
func (g *gateRig) input(action GateAction) GateInput {
	yes := true
	switch action {
	case GatePromotePlanned, GatePromoteEmergency, GateResync, GateAddDatabases, GateRemoveWithDrop:
		return GateInput{Action: action, MasterID: g.m.id, TargetID: g.rep.id}
	case GateCreateReplica:
		return GateInput{Action: action, MasterID: g.m.id, TargetID: g.tgt.id}
	case GateRebuildOldMaster:
		return GateInput{Action: action, MasterID: g.m.id, TargetID: g.old.id, Approval: &yes}
	}
	return GateInput{Action: action, MasterID: g.m.id}
}

func (g *gateRig) gate(in GateInput) GateResult {
	g.e.t.Helper()
	res, err := g.e.r.Gate(in)
	if err != nil {
		g.e.t.Fatalf("Gate(%+v): %v", in, err)
	}
	return res
}

// row is the row key of action's result.
func (g *gateRig) row(action GateAction, key string) GateRow {
	g.e.t.Helper()
	return gateRow(g.e.t, g.gate(g.input(action)), key)
}

func gateRow(t *testing.T, res GateResult, key string) GateRow {
	t.Helper()
	for _, r := range res.Rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no row %s in %+v", key, res.Rows)
	return GateRow{}
}

func wantRow(t *testing.T, got GateRow, status GateStatus, detail, fix string) {
	t.Helper()
	if got.Status != status || got.Detail != detail || got.Fix != fix {
		t.Fatalf("row %s = {%v %q %q}\nwant {%v %q %q}", got.Key, got.Status, got.Detail, got.Fix, status, detail, fix)
	}
}

func wantGreen(t *testing.T, got GateRow) {
	t.Helper()
	wantRow(t, got, GateGreen, "", "")
}

var allGateActions = []GateAction{GatePromotePlanned, GatePromoteEmergency, GateCreateReplica, GateRebuildOldMaster,
	GateResync, GateAddDatabases, GateRemoveWithDrop, GatePrepareMaster}

func TestGateRowsPerAction(t *testing.T) {
	g := newGateRig(t)
	promote := []string{"agent_version", "master_pin", "clock", "tls", "account", "backup", "master_port", "audit_forward", "panel_key"}
	replica := []string{"agent_version", "master_pin", "disk", "account", "perf_schema"}
	want := map[GateAction][]string{
		GatePromotePlanned:   promote,
		GatePromoteEmergency: promote,
		GateCreateReplica:    replica,
		GateRebuildOldMaster: {"agent_version", "tls", "disk", "account", "perf_schema", "backup", "approval", "master_port",
			"audit_forward", "panel_key"},
		GateResync:         replica,
		GateAddDatabases:   replica,
		GateRemoveWithDrop: {"agent_version", "account"},
		GatePrepareMaster:  {"agent_version", "account"},
	}
	labels := map[string]string{"agent_version": "Versi agent", "master_pin": "Pin master", "clock": "Jam (clock)",
		"tls": "Pemeriksaan sertifikat TLS", "disk": "Ruang disk", "account": "Akun panel", "perf_schema": "performance_schema",
		"backup": "Cadangan (backup)", "approval": "Persetujuan di server", "master_port": "Port MySQL master",
		"audit_forward": "Log audit", "panel_key": "Cadangan panel.key"}
	for _, a := range allGateActions {
		res := g.gate(g.input(a))
		var keys []string
		for _, r := range res.Rows {
			keys = append(keys, r.Key)
			if r.Label != labels[r.Key] {
				t.Errorf("%s: row %s label %q, want %q", a, r.Key, r.Label, labels[r.Key])
			}
			if r.Status != GateGreen || r.Detail != "" || r.Fix != "" || r.NeedsAck {
				t.Errorf("%s: row %+v, want green", a, r)
			}
		}
		if !slices.Equal(keys, want[a]) {
			t.Errorf("%s: keys %v, want %v", a, keys, want[a])
		}
	}
	if _, err := g.e.r.Gate(GateInput{Action: "drop_everything", MasterID: g.m.id}); err == nil {
		t.Fatal("Gate accepted an unknown action")
	}
	if _, err := g.e.r.Gate(GateInput{Action: GateResync, MasterID: g.m.id, TargetID: 999}); err == nil {
		t.Fatal("Gate accepted an unknown server")
	}
	if _, err := g.e.r.Gate(GateInput{Action: GatePromotePlanned, MasterID: g.rep.id, TargetID: g.m.id}); err == nil {
		t.Fatal("Gate accepted a promote of a server that masters no topology")
	}
}

const (
	wantG1f = "Perbarui agent di server itu (Pasang ulang agent di halaman server), lalu muat ulang halaman ini."
	wantG2f = "Jalankan Siapkan master di halaman server itu. Bila baru saja dijalankan, klik Periksa sekarang di halaman " +
		"server, lalu muat ulang halaman ini."
	wantG3f = "Selaraskan jam server itu dan host panel dengan NTP (di server: timedatectl set-ntp true), klik Periksa " +
		"sekarang di halaman server, lalu muat ulang halaman ini."
	wantG4f = "Pasang sertifikat MySQL di halaman server master, lalu jalankan Wajibkan pemeriksaan sertifikat TLS di " +
		"halaman master."
	wantG4g = "Jalankan Wajibkan pemeriksaan sertifikat TLS di halaman master."
	wantG5f = "Kosongkan ruang di server itu, klik Periksa sekarang di halaman server, lalu muat ulang halaman ini."
	wantG6f = "Buat akun MySQL khusus panel lalu hubungkan lagi (Panduan, \"Memperbaiki baris merah\"). Bila akunnya " +
		"sudah diganti di server, klik Segarkan di kartu MySQL halaman server itu."
	wantG7f   = "Hapus performance_schema=OFF dari konfigurasi MySQL master, lalu restart MySQL master di jam sepi."
	wantG8f   = "Jalankan cadangan master, atau betulkan folder cadangan di halaman server master."
	wantG9f   = "Jalankan perintah di kotak Persetujuan di server pada halaman ini, lalu klik Periksa lagi."
	wantG11f  = "Batasi port itu di firewall master hanya untuk alamat replika."
	wantG12f  = "Teruskan journal host panel ke server lain (systemd-journal-upload), lalu tandai di Pengaturan."
	wantG8uf  = "Isi folder cadangan di halaman server master, atau pastikan sendiri ada cadangan terbaru."
	wantG10af = "Pastikan agent di server itu berjalan dan tersambung ke panel, lalu muat ulang halaman ini."
	wantG13f  = "Cadangkan panel.key (README, \"Cadangkan kunci panel\"), lalu tandai di Pengaturan."
)

func TestGateAgentVersion(t *testing.T) {
	g := newGateRig(t)
	if err := g.e.st.UpdateProbe(g.rep.id, proto.ProbeResult{}, "1.3.9", g.now()); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GateResync, "agent_version"), GateRed, "Agent di db-replica versi 1.3.9, panel versi 1.4.0.", wantG1f)

	// Both servers: one sentence each, the master's first.
	if err := g.e.st.UpdateProbe(g.m.id, proto.ProbeResult{}, "", g.now()); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GateResync, "agent_version"), GateRed,
		"Versi agent di db-master belum diketahui. Agent di db-replica versi 1.3.9, panel versi 1.4.0.", wantG1f)

	// The agent's version is its own words: one line and cut at 32
	// characters.
	if err := g.e.st.UpdateProbe(g.rep.id, proto.ProbeResult{}, "9.9\nKlik "+strings.Repeat("x", 80), g.now()); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GateRemoveWithDrop, "agent_version"), GateRed,
		"Agent di db-replica versi 9.9 Klik "+strings.Repeat("x", 25)+"…, panel versi 1.4.0.", wantG1f)

	// A panel without a version skips the check.
	g.e.r.d.Version = ""
	wantGreen(t, g.row(GateResync, "agent_version"))
}

func TestGateMasterPin(t *testing.T) {
	g := newGateRig(t)
	g.h.set(g.m.id, monitor.View{Status: monitor.Status{Code: monitor.StatusAwaitingReport}})
	wantRow(t, g.row(GatePromotePlanned, "master_pin"), GateRed,
		"Agent master db-master tidak melaporkan pin master (yang dilaporkan: tidak ada).", wantG2f)

	g.h.set(g.m.id, g.goodView("replica"))
	wantRow(t, g.row(GateCreateReplica, "master_pin"), GateRed,
		"Agent master db-master tidak melaporkan pin master (yang dilaporkan: replica).", wantG2f)

	g.h.set(g.m.id, g.goodView(""))
	wantRow(t, g.row(GateResync, "master_pin"), GateRed,
		"Agent master db-master tidak melaporkan pin master (yang dilaporkan: tidak ada).", wantG2f)

	// The agent's value is made one line and cut at 32 characters (spaces
	// not counted, as monitor.AgentText cuts).
	g.h.set(g.m.id, g.goodView("mas\nter"+strings.Repeat("y", 40)))
	wantRow(t, g.row(GateAddDatabases, "master_pin"), GateRed,
		"Agent master db-master tidak melaporkan pin master (yang dilaporkan: mas ter"+strings.Repeat("y", 26)+"…).", wantG2f)

	g.h.set(g.m.id, g.goodView("master"))
	wantGreen(t, g.row(GatePromotePlanned, "master_pin"))
}

func TestGateClock(t *testing.T) {
	g := newGateRig(t)
	skew := func(fa *fakeAgent, d time.Duration) {
		g.snap(fa, func(s *proto.HealthSnapshot) { s.ClockUnixMS = g.now().Add(d).UnixMilli() })
	}
	skew(g.rep, 31*time.Second)
	wantRow(t, g.row(GatePromotePlanned, "clock"), GateRed,
		"Jam server db-replica berbeda 31 detik dari jam host panel (batas 30 detik).", wantG3f)
	skew(g.rep, 30*time.Second)
	wantGreen(t, g.row(GatePromotePlanned, "clock"))
	skew(g.rep, -31*time.Second)
	wantRow(t, g.row(GatePromoteEmergency, "clock"), GateRed,
		"Jam server db-replica berbeda 31 detik dari jam host panel (batas 30 detik).", wantG3f)
	skew(g.m, -3*time.Hour-50*time.Minute)
	wantRow(t, g.row(GatePromotePlanned, "clock"), GateRed,
		"Jam server db-master berbeda 3 jam dari jam host panel (batas 30 detik). "+
			"Jam server db-replica berbeda 31 detik dari jam host panel (batas 30 detik).", wantG3f)
}

func TestGateClockDiffWords(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{30*time.Second + time.Millisecond, "31 detik"},
		{31 * time.Second, "31 detik"},
		{-31 * time.Second, "31 detik"},
		{119*time.Second + 500*time.Millisecond, "120 detik"},
		{2 * time.Minute, "2 menit"},
		{5*time.Minute + 59*time.Second, "5 menit"},
		{-(119*time.Minute + 59*time.Second), "119 menit"},
		{2 * time.Hour, "2 jam"},
		{3*time.Hour + 59*time.Minute, "3 jam"},
	} {
		if got := clockDiffWords(c.d); got != c.want {
			t.Errorf("clockDiffWords(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestGateClockIgnoresBacklogReport(t *testing.T) {
	g := newGateRig(t)
	g.snap(g.rep, func(s *proto.HealthSnapshot) { s.ClockUnixMS = g.now().Add(10 * time.Minute).UnixMilli() })
	v, _ := g.h.View(g.rep.id)
	v.Backlog = true
	g.h.set(g.rep.id, v)
	wantRow(t, g.row(GatePromotePlanned, "clock"), GateYellow,
		"Jam (clock) belum diketahui: agent db-replica belum melaporkannya.", "")
}

func TestGateTLS(t *testing.T) {
	g := newGateRig(t)
	// A ready replica that does not verify the master's certificate: red
	// on promote only.
	if err := g.e.st.SetReplicaTLSVerified(g.rep.id, false); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GatePromotePlanned, "tls"), GateRed, "Replika db-replica belum memeriksa sertifikat master.", wantG4g)
	wantGreen(t, g.row(GateRebuildOldMaster, "tls"))
	if err := g.e.st.SetReplicaState(g.rep.id, store.ReplicaAttention, ""); err != nil {
		t.Fatal(err)
	}
	b := g.e.agent("db-b", "10.0.0.5")
	if _, err := g.e.st.AddReplica(g.topo.ID, b.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.e.st.SetReplicaState(b.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GatePromoteEmergency, "tls"), GateRed,
		"Replika db-replica, db-b belum memeriksa sertifikat master.", wantG4g)

	// One that needs repointing is not counted.
	for _, id := range []int64{g.rep.id, b.id} {
		if err := g.e.st.SetReplicaState(id, store.ReplicaNeedsRepoint, ""); err != nil {
			t.Fatal(err)
		}
	}
	wantGreen(t, g.row(GatePromotePlanned, "tls"))

	// No certificate on the master.
	db, err := sql.Open("sqlite", "file:"+g.e.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM server_tls WHERE server_id = ?`, g.m.id); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GateRebuildOldMaster, "tls"), GateRed, "Master db-master belum punya sertifikat MySQL dari panel.", wantG4f)
	if err := g.e.st.SetReplicaState(b.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GatePromotePlanned, "tls"), GateRed,
		"Master db-master belum punya sertifikat MySQL dari panel. Replika db-b belum memeriksa sertifikat master.", wantG4f)
}

func TestReplicaUnverified(t *testing.T) {
	for _, c := range []struct {
		state    string
		verified bool
		want     bool
	}{
		{store.ReplicaReady, false, true},
		{store.ReplicaAttention, false, true},
		{store.ReplicaReady, true, false},
		{store.ReplicaNeedsRepoint, false, false},
		{store.ReplicaNeedsRebuild, false, false},
	} {
		if got := ReplicaUnverified(store.Replica{State: c.state, TLSVerified: c.verified}); got != c.want {
			t.Errorf("ReplicaUnverified(%s, verified %v) = %v", c.state, c.verified, got)
		}
	}
}

func TestGateDisk(t *testing.T) {
	g := newGateRig(t)
	g.snap(g.rep, func(s *proto.HealthSnapshot) { s.DiskFree = 50 })
	wantRow(t, g.row(GateResync, "disk"), GateRed, "Disk db-replica terpakai 95% (ambang kritis 95%).", wantG5f)
	g.snap(g.rep, func(s *proto.HealthSnapshot) { s.DiskFree = 51 })
	wantGreen(t, g.row(GateResync, "disk"))

	if err := g.e.st.SetSetting(store.SettingDiskWarn, "80"); err != nil {
		t.Fatal(err)
	}
	if err := g.e.st.SetSetting(store.SettingDiskCrit, "90"); err != nil {
		t.Fatal(err)
	}
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.DiskFree = 80 })
	wantRow(t, g.row(GateAddDatabases, "disk"), GateRed,
		"Disk db-master terpakai 92% (ambang kritis 90%). Disk db-replica terpakai 94% (ambang kritis 90%).", wantG5f)

	// Rebuild judges the new master and the old one.
	g.snap(g.rep, func(s *proto.HealthSnapshot) { s.DiskFree = 500 })
	g.snap(g.old, func(s *proto.HealthSnapshot) { s.DiskFree = 0 })
	wantRow(t, g.row(GateRebuildOldMaster, "disk"), GateRed,
		"Disk db-master terpakai 92% (ambang kritis 90%). Disk db-old terpakai 100% (ambang kritis 90%).", wantG5f)

	// Red first, then the server not yet known.
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.DiskTotal = 0 })
	wantRow(t, g.row(GateRebuildOldMaster, "disk"), GateRed,
		"Disk db-old terpakai 100% (ambang kritis 90%). Ruang disk belum diketahui: agent db-master belum melaporkannya.", wantG5f)
}

func TestGateAccountSplitsAtLastAt(t *testing.T) {
	g := newGateRig(t)
	for _, c := range []struct {
		account string
		red     bool
	}{
		{"root@localhost", true},
		{"root@%", true},
		{"a@b@localhost", false},
		{"root@b@localhost", false},
		{"rooter@localhost", false},
		{"Root@localhost", false},
		{"root", true},
	} {
		g.setAccount(g.m, true, c.account)
		got := g.row(GatePrepareMaster, "account")
		if c.red {
			wantRow(t, got, GateRed, "Agent di db-master masuk ke MySQL sebagai root.", wantG6f)
		} else {
			wantGreen(t, got)
		}
	}
	if got := gateAccountUser("a@b@localhost"); got != "a@b" {
		t.Fatalf("gateAccountUser = %q", got)
	}

	// Both servers judged; a connected inventory without the account is not
	// known yet.
	g.setAccount(g.m, true, "")
	g.setAccount(g.rep, true, "root@localhost")
	wantRow(t, g.row(GateResync, "account"), GateRed,
		"Agent di db-replica masuk ke MySQL sebagai root. Akun panel belum diketahui: agent db-master belum melaporkannya.", wantG6f)
	g.setAccount(g.rep, true, "dbpanel@localhost")
	wantRow(t, g.row(GateResync, "account"), GateYellow, "Akun panel belum diketahui: agent db-master belum melaporkannya.", "")
}

// Ruling R30: only the target of Buat replika, whose MySQL may not be
// connected yet, is skipped without a connected inventory; for every other
// server a missing or unconnected inventory is not known yet (G10b).
func TestGateAccountSkippedForTargetWithoutMySQL(t *testing.T) {
	g := newGateRig(t)
	g.setAccount(g.tgt, false, "root@localhost")
	wantGreen(t, g.row(GateCreateReplica, "account"))

	bare := g.e.agent("db-bare", "10.0.0.6")
	if err := g.e.st.UpdateProbe(bare.id, proto.ProbeResult{}, gateVersion, g.now()); err != nil {
		t.Fatal(err)
	}
	res := g.gate(GateInput{Action: GateCreateReplica, MasterID: g.m.id, TargetID: bare.id})
	wantGreen(t, gateRow(t, res, "account"))
}

func TestGateAccountUnknownWithoutInventory(t *testing.T) {
	g := newGateRig(t)
	unknownOf := func(name string) string { return "Akun panel belum diketahui: agent " + name + " belum melaporkannya." }

	// The master of Buat replika, unconnected (its old account is not read).
	g.setAccount(g.m, false, "root@localhost")
	wantRow(t, g.row(GateCreateReplica, "account"), GateYellow, unknownOf("db-master"), "")
	wantRow(t, g.row(GatePrepareMaster, "account"), GateYellow, unknownOf("db-master"), "")
	wantRow(t, g.row(GatePromotePlanned, "account"), GateYellow, unknownOf("db-master"), "")
	g.setAccount(g.m, true, "dbpanel@localhost")

	// The target of the other actions, unconnected or without inventory.
	g.setAccount(g.rep, false, "dbpanel@localhost")
	for _, a := range []GateAction{GatePromotePlanned, GatePromoteEmergency, GateResync, GateAddDatabases, GateRemoveWithDrop} {
		wantRow(t, g.row(a, "account"), GateYellow, unknownOf("db-replica"), "")
	}
	bare := g.e.agent("db-bare", "10.0.0.6")
	if err := g.e.st.UpdateProbe(bare.id, proto.ProbeResult{}, gateVersion, g.now()); err != nil {
		t.Fatal(err)
	}
	g.h.set(bare.id, g.goodView("replica"))
	res := g.gate(GateInput{Action: GateRebuildOldMaster, MasterID: g.m.id, TargetID: bare.id, Approval: new(true)})
	wantRow(t, gateRow(t, res, "account"), GateYellow, unknownOf("db-bare"), "")
	res = g.gate(GateInput{Action: GateRemoveWithDrop, TargetID: bare.id})
	wantRow(t, gateRow(t, res, "account"), GateYellow, unknownOf("db-bare"), "")

	// A master without inventory, with a red target: red first.
	g.setAccount(g.rep, true, "root@localhost")
	res = g.gate(GateInput{Action: GateResync, MasterID: bare.id, TargetID: g.rep.id})
	wantRow(t, gateRow(t, res, "account"), GateRed,
		"Agent di db-replica masuk ke MySQL sebagai root. "+unknownOf("db-bare"), wantG6f)
}

func TestGatePerfSchema(t *testing.T) {
	g := newGateRig(t)
	off := false
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.PerfSchema = &off })
	for _, a := range []GateAction{GateCreateReplica, GateRebuildOldMaster, GateResync, GateAddDatabases} {
		wantRow(t, g.row(a, "perf_schema"), GateRed, "performance_schema mati di master db-master.", wantG7f)
	}
	// Only the master's counts.
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.PerfSchema = nil })
	g.snap(g.tgt, func(s *proto.HealthSnapshot) { s.PerfSchema = &off })
	wantRow(t, g.row(GateCreateReplica, "perf_schema"), GateYellow,
		"performance_schema belum diketahui: agent db-master belum melaporkannya.", "")
}

func TestGateBackup(t *testing.T) {
	g := newGateRig(t)
	backup := func(b *proto.BackupState) { g.snap(g.m, func(s *proto.HealthSnapshot) { s.Backup = b }) }

	backup(&proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 48*3600 + 1800, NewestSize: 10})
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateRed,
		"Berkas terbaru di folder cadangan master db-master berumur lebih dari 48 jam (batas 48 jam).", wantG8f)
	// The row's own limit.
	if err := g.e.st.SetServerBackup(g.m.id, "/srv/backup", 24); err != nil {
		t.Fatal(err)
	}
	backup(&proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 24*3600 + 60, NewestSize: 10})
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateRed,
		"Berkas terbaru di folder cadangan master db-master berumur lebih dari 24 jam (batas 24 jam).", wantG8f)
	backup(&proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 60, NewestSize: 0})
	wantRow(t, g.row(GatePromoteEmergency, "backup"), GateRed,
		"Berkas terbaru di folder cadangan master db-master kosong (0 byte).", wantG8f)
	backup(&proto.BackupState{Status: proto.BackupNone})
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateRed, "Folder cadangan master db-master tidak berisi berkas.", wantG8f)
	backup(&proto.BackupState{Status: proto.BackupUnreadable, Reason: proto.BackupReasonDenied})
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateRed,
		"Folder cadangan master db-master tidak bisa dibaca agent: izin ditolak.", wantG8f)
	backup(&proto.BackupState{Status: proto.BackupPending})
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateYellow,
		"Cadangan (backup) belum diketahui: agent db-master belum melaporkannya.", "")
	backup(nil)
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateYellow,
		"Cadangan (backup) belum diketahui: agent db-master belum melaporkannya.", "")
	backup(&proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 60, NewestSize: 10})
	wantGreen(t, g.row(GatePromotePlanned, "backup"))

	// An offline master's last report is not judged (Ruling R25): its age
	// would grow by extrapolation alone.
	g.m.detach()
	wantRow(t, g.row(GatePromotePlanned, "backup"), GateYellow,
		"Cadangan (backup) belum diketahui: agent db-master sedang tidak tersambung.", wantG10af)
	g.m.attach()

	// Unset.
	if err := g.e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	got := g.row(GatePromotePlanned, "backup")
	wantRow(t, got, GateYellow,
		"Folder cadangan master db-master belum diisi, jadi panel tidak bisa melihat umur cadangan.", wantG8uf)
	if !got.NeedsAck {
		t.Fatal("unset backup folder on a planned promote needs no ack")
	}

	// Rebuild judges the new master (the topology's), not the old one.
	if err := g.e.st.SetServerBackup(g.m.id, "/srv/backup", 0); err != nil {
		t.Fatal(err)
	}
	backup(&proto.BackupState{Status: proto.BackupNone})
	if err := g.e.st.SetServerBackup(g.old.id, "/srv/old", 0); err != nil {
		t.Fatal(err)
	}
	wantRow(t, g.row(GateRebuildOldMaster, "backup"), GateRed, "Folder cadangan master db-master tidak berisi berkas.", wantG8f)
	backup(&proto.BackupState{Status: proto.BackupOK, NewestAgeSec: 60, NewestSize: 10})
	g.snap(g.old, func(s *proto.HealthSnapshot) { s.Backup = &proto.BackupState{Status: proto.BackupNone} })
	wantGreen(t, g.row(GateRebuildOldMaster, "backup"))
}

func TestGateNeedsBackupAck(t *testing.T) {
	g := newGateRig(t)
	if err := g.e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	for _, a := range allGateActions {
		res := g.gate(g.input(a))
		want := a == GatePromotePlanned || a == GateRebuildOldMaster
		if res.NeedsBackupAck() != want {
			t.Errorf("%s: NeedsBackupAck = %v, want %v", a, res.NeedsBackupAck(), want)
		}
	}
	wantRow(t, g.row(GatePromoteEmergency, "backup"), GateYellow,
		"Folder cadangan master db-master belum diisi, jadi panel tidak bisa melihat umur cadangan.", wantG8uf)
}

func TestGateApproval(t *testing.T) {
	g := newGateRig(t)
	in := g.input(GateRebuildOldMaster)
	in.Approval = nil
	wantRow(t, gateRow(t, g.gate(in), "approval"), GateYellow, "Persetujuan di server db-old belum diperiksa.", wantG9f)
	no := false
	in.Approval = &no
	wantRow(t, gateRow(t, g.gate(in), "approval"), GateRed, "Belum disetujui di server db-old.", wantG9f)
	yes := true
	in.Approval = &yes
	wantGreen(t, gateRow(t, g.gate(in), "approval"))
}

func TestGateMarks(t *testing.T) {
	g := newGateRig(t)
	for _, k := range []string{store.SettingAuditForwardedAt, store.SettingPanelKeyBackedUpAt} {
		if err := g.e.st.SetSetting(k, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []GateAction{GatePromotePlanned, GatePromoteEmergency, GateRebuildOldMaster} {
		res := g.gate(g.input(a))
		wantRow(t, gateRow(t, res, "audit_forward"), GateYellow, "Log audit belum ditandai diteruskan ke server lain.", wantG12f)
		wantRow(t, gateRow(t, res, "panel_key"), GateYellow, "panel.key belum ditandai sudah dicadangkan.", wantG13f)
		if len(res.Red()) != 0 {
			t.Fatalf("%s: red rows %v", a, res.RedKeys())
		}
	}
}

func TestGateUnknownIsYellow(t *testing.T) {
	g := newGateRig(t)
	// The target's agent is away: its live rows are not known.
	g.tgt.detach()
	wantRow(t, g.row(GateCreateReplica, "disk"), GateYellow, "Ruang disk belum diketahui: agent db-target sedang tidak tersambung.", wantG10af)
	g.rep.detach()
	wantRow(t, g.row(GatePromotePlanned, "clock"), GateYellow,
		"Jam (clock) belum diketahui: agent db-replica sedang tidak tersambung.", wantG10af)
	// Connected, but its report lacks the value.
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.PerfSchema = nil; s.ClockUnixMS = 0 })
	wantRow(t, g.row(GateResync, "perf_schema"), GateYellow,
		"performance_schema belum diketahui: agent db-master belum melaporkannya.", "")
	wantRow(t, g.row(GatePromotePlanned, "clock"), GateYellow,
		"Jam (clock) belum diketahui: agent db-master belum melaporkannya. "+
			"Jam (clock) belum diketahui: agent db-replica sedang tidak tersambung.", wantG10af)

	// Nothing known of any server at all: only the version and the pin are
	// red.
	for _, fa := range []*fakeAgent{g.m, g.rep, g.tgt, g.old} {
		fa.detach()
		g.h.set(fa.id, monitor.View{Status: monitor.Status{Code: monitor.StatusAgentOffline}})
	}
	for _, a := range allGateActions {
		in := g.input(a)
		in.Approval = nil
		res := g.gate(in)
		for _, r := range res.Red() {
			if r.Key != "agent_version" && r.Key != "master_pin" {
				t.Errorf("%s: row %s red: %q", a, r.Key, r.Detail)
			}
		}
		// The live rows are yellow, not merely not red.
		for _, r := range res.Rows {
			switch r.Key {
			case "clock", "disk", "perf_schema", "backup":
				if r.Status != GateYellow {
					t.Errorf("%s: row %s = %v %q, want yellow", a, r.Key, r.Status, r.Detail)
				}
			}
		}
	}
	wantRow(t, g.row(GateRebuildOldMaster, "disk"), GateYellow,
		"Ruang disk belum diketahui: agent db-master sedang tidak tersambung. "+
			"Ruang disk belum diketahui: agent db-old sedang tidak tersambung.", wantG10af)
}

func TestGateResultAndError(t *testing.T) {
	res := GateResult{Rows: []GateRow{
		{Key: "agent_version", Label: "Versi agent", Status: GateRed},
		{Key: "clock", Label: "Jam (clock)", Status: GateYellow},
		{Key: "master_pin", Label: "Pin master", Status: GateRed},
		{Key: "backup", Label: "Cadangan (backup)", Status: GateYellow, NeedsAck: true},
	}}
	if got := res.RedKeys(); !slices.Equal(got, []string{"agent_version", "master_pin"}) {
		t.Fatalf("RedKeys = %v", got)
	}
	if len(res.Red()) != 2 || !res.NeedsBackupAck() {
		t.Fatalf("Red = %v, NeedsBackupAck = %v", res.Red(), res.NeedsBackupAck())
	}
	if (GateResult{Rows: res.Rows[:3]}).NeedsBackupAck() {
		t.Fatal("NeedsBackupAck without a row that needs it")
	}
	var err error = &GateError{Rows: res.Rows}
	if err.Error() != "prasyarat belum terpenuhi: Versi agent, Pin master" {
		t.Fatalf("GateError = %q", err.Error())
	}
	if !errors.Is(err, ErrGateRed) || errors.Is(err, ErrBackupAck) {
		t.Fatal("GateError does not unwrap to ErrGateRed alone")
	}
	if ErrGateRed.Error() != "prasyarat belum terpenuhi" || ErrBackupAck.Error() != "centang cadangan belum diisi" {
		t.Fatalf("errors %q, %q", ErrGateRed, ErrBackupAck)
	}
}

// failConn is an agent connection that fails the test on any command.
type failConn struct{ t *testing.T }

func (c failConn) Call(_ context.Context, cmd string, _, _ any) error {
	c.t.Errorf("Gate called the agent: %s", cmd)
	return errors.New("no agent call expected")
}
func (c failConn) Close() error { return nil }

var _ hub.Conn = failConn{}

func TestGateMakesNoAgentCall(t *testing.T) {
	g := newGateRig(t)
	for _, fa := range []*fakeAgent{g.m, g.rep, g.tgt, g.old} {
		fa.detach()
		g.e.hub.Attach(fa.id, failConn{t})
	}
	jobs, err := g.e.st.ListJobs(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range allGateActions {
		g.gate(g.input(a))
	}
	if n := len(g.h.checked()); n != 0 {
		t.Fatalf("Gate asked the monitor for %d fresh reports", n)
	}
	after, err := g.e.st.ListJobs(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(jobs) {
		t.Fatalf("Gate created jobs: %d, was %d", len(after), len(jobs))
	}
}

// Ruling R26: no folder set is judged from the store first, whether or not
// the master's agent is connected; R25 (G10a) only when a folder is set.
func TestGateBackupUnsetWhileMasterOffline(t *testing.T) {
	g := newGateRig(t)
	if err := g.e.st.SetServerBackup(g.m.id, "", 0); err != nil {
		t.Fatal(err)
	}
	g.m.detach()
	for _, a := range allGateActions {
		res := g.gate(g.input(a))
		want := a == GatePromotePlanned || a == GateRebuildOldMaster
		if res.NeedsBackupAck() != want {
			t.Errorf("%s: NeedsBackupAck = %v, want %v", a, res.NeedsBackupAck(), want)
		}
	}
	for _, a := range []GateAction{GatePromotePlanned, GatePromoteEmergency, GateRebuildOldMaster} {
		wantRow(t, g.row(a, "backup"), GateYellow,
			"Folder cadangan master db-master belum diisi, jadi panel tidak bisa melihat umur cadangan.", wantG8uf)
	}

	if err := g.e.st.SetServerBackup(g.m.id, "/srv/backup", 0); err != nil {
		t.Fatal(err)
	}
	got := g.row(GatePromotePlanned, "backup")
	wantRow(t, got, GateYellow, "Cadangan (backup) belum diketahui: agent db-master sedang tidak tersambung.", wantG10af)
	if got.NeedsAck {
		t.Fatal("a set folder on an offline master asks for the tick")
	}
}

// Ruling R28: a yellow multi-server row names the G10a fix when one of its
// servers is away, and none when every unknown is only unreported (G10b).
func TestGateUnknownFix(t *testing.T) {
	g := newGateRig(t)
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.DiskTotal = 0 })
	wantRow(t, g.row(GateResync, "disk"), GateYellow, "Ruang disk belum diketahui: agent db-master belum melaporkannya.", "")
	g.rep.detach()
	wantRow(t, g.row(GateResync, "disk"), GateYellow,
		"Ruang disk belum diketahui: agent db-master belum melaporkannya. "+
			"Ruang disk belum diketahui: agent db-replica sedang tidak tersambung.", wantG10af)
	// A red server keeps the red fix.
	g.snap(g.m, func(s *proto.HealthSnapshot) { s.DiskTotal, s.DiskFree = 100, 0 })
	wantRow(t, g.row(GateResync, "disk"), GateRed,
		"Disk db-master terpakai 100% (ambang kritis 95%). "+
			"Ruang disk belum diketahui: agent db-replica sedang tidak tersambung.", wantG5f)
	// Single-server rows too.
	g.m.detach()
	wantRow(t, g.row(GateResync, "perf_schema"), GateYellow,
		"performance_schema belum diketahui: agent db-master sedang tidak tersambung.", wantG10af)
}
