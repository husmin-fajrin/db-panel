package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// waitingJobFor stores a job of kind on server 1 waiting at step 0 with
// wait reason reason.
func waitingJobFor(t *testing.T, st *store.Store, id, kind, reason string) {
	t.Helper()
	params := fmt.Sprintf(`{"wait_reason":%q}`, reason)
	if err := st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: 1, ParamsJSON: params, CreatedAt: time.Now()},
		[]string{"Tahap"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStep(id, 0, store.StepWaiting, "pesan tahap", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob(id, store.JobWaiting, 0, "", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestJobApprovalLabelledPerStep: each waiting step has its own button;
// the dangerous ones open a dialog that says what happens; the restart
// wait offers "Sudah saya restart — periksa lagi" and the reconnect wait
// links to the server page.
func TestJobApprovalLabelledPerStep(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	for i, tc := range []struct {
		kind, reason string
		want, absent []string
	}{
		{orchestrator.KindPrepareMaster, orchestrator.WaitRestart,
			[]string{"Restart MySQL di master sekarang", `x-data="confirmApproval"`, `class="modal"`, "terputus beberapa detik",
				"/retry", "Sudah saya restart — periksa lagi"}, nil},
		{orchestrator.KindCreateReplica, orchestrator.WaitWipe,
			[]string{"Ya, hapus dan salin ulang", `x-data="confirmApproval"`, "DIHAPUS dari db-utama"}, []string{"/retry"}},
		{orchestrator.KindCreateReplica, orchestrator.WaitClone,
			[]string{"Ya, CLONE menghapus semua database", `x-data="confirmApproval"`, "SEMUA database dan akun MySQL di db-utama"}, nil},
		{orchestrator.KindCreateReplica, orchestrator.WaitForeignSource,
			[]string{"Ya, hapus pengaturan replikasi lama", `x-data="confirmApproval"`}, nil},
		{orchestrator.KindCreateReplica, orchestrator.WaitReconnect,
			[]string{"Sudah saya hubungkan ulang — lanjutkan", `href="/servers/1"`, "Hubungkan MySQL"},
			[]string{`x-data="confirmApproval"`, "/retry"}},
		{orchestrator.KindCreateReplica, "", []string{"Setujui &amp; lanjutkan"}, []string{`x-data="confirmApproval"`}},
	} {
		id := strings.Repeat(fmt.Sprintf("%x", i+1), 32)
		waitingJobFor(t, e.st, id, tc.kind, tc.reason)
		rec := e.do("GET", "/jobs/"+id, nil, c)
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, append([]string{`action="/jobs/` + id + `/approve"`}, tc.want...)...)
		wantNoBody(t, rec, tc.absent...)
	}
}

// TestSupersededJobHidesRetry: a job the orchestrator calls superseded
// shows why and offers neither Ulangi nor approval.
func TestSupersededJobHidesRetry(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	newTestJob(t, e.st, failedJob, orchestrator.KindCreateReplica, "Salin data")
	if err := e.st.UpdateJob(failedJob, store.JobFailed, 0, "gagal", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/jobs/"+failedJob, nil, c)
	wantBody(t, rec, "Ulangi dari tahap ini")
	f.superseded = map[string]string{failedJob: "server ini sudah punya pekerjaan yang lebih baru"}
	rec = e.do("GET", "/jobs/"+failedJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "tidak bisa diulang atau disetujui lagi: server ini sudah punya pekerjaan yang lebih baru")
	wantNoBody(t, rec, "Ulangi dari tahap ini", "/retry", "/approve")
}

// TestConnectFormHostAndPlainHTTPWarning: the form warns that the password
// crosses plain HTTP, and a host that is not an address or a name is
// refused before the agent sees it.
func TestConnectFormHostAndPlainHTTPWarning(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Halaman ini dibuka lewat HTTP biasa", "tunnel SSH")
	for _, host := range []string{"db/x?allowAllFiles=true", "a b", "user@db", "db:3306"} {
		rec := e.do("POST", "/servers/1/mysql/connect", url.Values{"user": {"root"}, "password": {mysqlSecret}, "host": {host}}, c)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Host MySQL tidak valid")
		wantNoBody(t, rec, mysqlSecret)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("orchestrator called: %q", calls)
	}
	f.result = proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.0.39"}}
	for _, host := range []string{"", "10.0.0.5", "db-1.lan", "::1"} {
		rec := e.do("POST", "/servers/1/mysql/connect", url.Values{"user": {"root"}, "host": {host}}, c)
		wantCode(t, rec, http.StatusSeeOther)
	}
}

// TestMasterSourceAddressEdit: the master page edits the address replicas
// reach it at, validated like the agent validates it.
func TestMasterSourceAddressEdit(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	topo := e.makeMaster(t, master)
	rec := e.do("GET", "/masters/1", nil, c)
	wantBody(t, rec, `action="/masters/1/source"`, `name="source_host" value="10.0.0.1"`, `name="source_port" value="3306"`,
		"Ubah alamat untuk replika")

	for _, bad := range []url.Values{
		{"source_host": {"10.0.0.9 --x"}, "source_port": {"3306"}},
		{"source_host": {"db:3306"}, "source_port": {"3306"}},
		{"source_host": {"192.168.5.9"}, "source_port": {"0"}},
		{"source_host": {"192.168.5.9"}, "source_port": {"70000"}},
		{"source_host": {""}, "source_port": {"3306"}},
	} {
		rec := e.do("POST", "/masters/1/source", bad, c)
		wantCode(t, rec, http.StatusBadRequest)
	}
	if got, _ := e.st.GetTopology(topo.ID); got.SourceHost != "10.0.0.1" || got.SourcePort != 3306 {
		t.Fatalf("refused edits changed the topology: %+v", got)
	}
	rec = e.do("POST", "/masters/1/source", url.Values{"source_host": {"192.168.5.9"}, "source_port": {"3307"}}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/masters/1?pesan=sumber")
	if got, _ := e.st.GetTopology(topo.ID); got.SourceHost != "192.168.5.9" || got.SourcePort != 3307 {
		t.Fatalf("topology = %+v", got)
	}
	rec = e.do("GET", "/masters/1?pesan=sumber", nil, c)
	wantBody(t, rec, "192.168.5.9:3307", "diperiksa saat membuat replika")
}

// TestReplicaFormsNotices: the create form says the master is locked
// briefly at the start (quiet hours); the add-databases form also says the
// replica is writable during the load.
func TestReplicaFormsNotices(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.subsetReplica(t, []string{"toko"}, shopDatabases...)
	rec := e.do("GET", "/masters/1/replicas/new", nil, c)
	wantBody(t, rec, "dikunci sesaat", "di jam sepi")

	rec = e.do("GET", "/replicas/2/databases", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "di jam sepi", "bisa ditulis")
}

// TestReplicaFormRefusesUnconnectedTarget: a target whose MySQL is
// installed but not connected is offered disabled, with a link to connect
// it, and a post naming it is refused.
func TestReplicaFormRefusesUnconnectedTarget(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	target := e.pairedOnline(t, "db-calon")
	e.makeMaster(t, master, shopDatabases...)
	if err := e.st.UpdateProbe(target.ID, proto.ProbeResult{MySQL: proto.MySQLInfo{Installed: true}}, "t", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/masters/1/replicas/new", nil, c)
	wantBody(t, rec, `<option value="2" disabled>db-calon`, "MySQL belum dihubungkan", `href="/servers/2"`, "klik Hubungkan MySQL")
	rec = e.do("POST", "/masters/1/replicas", url.Values{"target": {"2"}, "databases": {"toko"}, "confirm_wipe": {"1"}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "belum dihubungkan")
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("orchestrator called: %q", calls)
	}
}
