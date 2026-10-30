package web_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const mysqlSecret = "Rahasia-MySQL-9x!" // must never show up in a page or log

// fakeOrch records the calls the UI makes. Connect and RefreshInventory
// store the inventory like the real orchestrator does.
type fakeOrch struct {
	st *store.Store

	mu       sync.Mutex
	calls    []string
	connects []proto.ConnectParams
	replicas []orchestrator.CreateReplicaParams

	result proto.ConnectResult
	err    error // returned by every call when set
	nextID string
}

func (f *fakeOrch) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeOrch) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeOrch) storeInventory(serverID int64) error {
	return f.st.SetInventory(store.Inventory{
		ServerID: serverID, Connected: true, Version: f.result.Status.Version, Series: f.result.Status.Series,
		Databases: f.result.Databases, MissingGrants: f.result.MissingGrants, UpdatedAt: time.Now(),
	})
}

func (f *fakeOrch) Connect(_ context.Context, serverID int64, p proto.ConnectParams) (proto.ConnectResult, error) {
	f.record(fmt.Sprintf("connect %d", serverID))
	f.mu.Lock()
	f.connects = append(f.connects, p)
	f.mu.Unlock()
	if f.err != nil {
		return proto.ConnectResult{}, f.err
	}
	return f.result, f.storeInventory(serverID)
}

func (f *fakeOrch) RefreshInventory(_ context.Context, serverID int64) (proto.ConnectResult, error) {
	f.record(fmt.Sprintf("refresh %d", serverID))
	if f.err != nil {
		return proto.ConnectResult{}, f.err
	}
	return f.result, f.storeInventory(serverID)
}

func (f *fakeOrch) StartPrepareMaster(_ context.Context, serverID int64) (string, error) {
	f.record(fmt.Sprintf("prepare %d", serverID))
	if f.err != nil {
		return "", f.err
	}
	return f.nextID, nil
}

func (f *fakeOrch) StartCreateReplica(_ context.Context, p orchestrator.CreateReplicaParams) (string, error) {
	f.record(fmt.Sprintf("create-replica %d->%d", p.MasterServerID, p.TargetServerID))
	f.mu.Lock()
	f.replicas = append(f.replicas, p)
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.nextID, nil
}

func (f *fakeOrch) StartAddDatabases(_ context.Context, serverID int64, dbs []string) (string, error) {
	f.record(fmt.Sprintf("add-databases %d %s", serverID, strings.Join(dbs, ",")))
	if f.err != nil {
		return "", f.err
	}
	return f.nextID, nil
}

func (f *fakeOrch) StartRemoveDatabases(_ context.Context, serverID int64, dbs []string, dropData bool) (string, error) {
	f.record(fmt.Sprintf("remove-databases %d %s drop=%t", serverID, strings.Join(dbs, ","), dropData))
	if f.err != nil {
		return "", f.err
	}
	return f.nextID, nil
}

func (f *fakeOrch) Approve(id string) error { f.record("approve " + id); return f.err }
func (f *fakeOrch) Retry(id string) error   { f.record("retry " + id); return f.err }
func (f *fakeOrch) Cancel(id string) error  { f.record("cancel " + id); return f.err }

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newMySQLEnv is newEnv with a fake orchestrator and a captured log.
func newMySQLEnv(t *testing.T) (*env, *fakeOrch, *syncBuffer) {
	t.Helper()
	f := &fakeOrch{nextID: "0123456789abcdef0123456789abcdef"}
	logs := &syncBuffer{}
	e := newEnv(t, func(d *web.Deps) {
		f.st = d.Store
		d.Orchestrator = f
		d.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return e, f, logs
}

// pairedOnline creates a paired server with a live agent connection.
func (e *env) pairedOnline(t *testing.T, name string) store.Server {
	t.Helper()
	srv, err := e.st.CreateServer(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkPaired(srv.ID, "serial-"+name, "10.0.0."+fmt.Sprint(srv.ID)); err != nil {
		t.Fatal(err)
	}
	e.hub.Attach(srv.ID, fakeConn{})
	return srv
}

// makeMaster gives srv a topology and a ready inventory with dbs.
func (e *env) makeMaster(t *testing.T, srv store.Server, dbs ...proto.DatabaseInfo) store.Topology {
	t.Helper()
	if err := e.st.SetInventory(store.Inventory{ServerID: srv.ID, Connected: true, Version: "8.0.39",
		Series: "8.0", Databases: dbs, MasterReady: true, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	topo, err := e.st.CreateTopology(srv.ID, "dbpanel_repl", nil, "10.0.0.1", 3306)
	if err != nil {
		t.Fatal(err)
	}
	return topo
}

func wantNoBody(t *testing.T, rec *httptest.ResponseRecorder, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("body contains %q; body:\n%s", s, rec.Body.String())
		}
	}
}

func wantLocation(t *testing.T, rec *httptest.ResponseRecorder, loc string) {
	t.Helper()
	if got := rec.Header().Get("Location"); got != loc {
		t.Errorf("Location = %q, want %q", got, loc)
	}
}

func wantCalls(t *testing.T, f *fakeOrch, want ...string) {
	t.Helper()
	if got := f.callList(); !reflect.DeepEqual(got, want) {
		t.Errorf("orchestrator calls = %q, want %q", got, want)
	}
}

var shopDatabases = []proto.DatabaseInfo{
	{Name: "toko", Bytes: 1536 << 20, Tables: 12},
	{Name: "gudang", Bytes: 300 << 20, Tables: 4},
}

func TestConnectMySQLFormPostsAndShowsDatabases(t *testing.T) {
	e, f, logs := newMySQLEnv(t)
	c := e.cookie()
	srv := e.pairedOnline(t, "db-utama")

	// Not connected yet: the form, with a password field that is never
	// filled in or remembered.
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/servers/1/mysql/connect"`, `name="user"`, `name="host"`,
		`type="password" name="password" autocomplete="off"`, "Kosongkan untuk memakai socket lokal (disarankan)", "Hubungkan MySQL")

	// A refused login: the error as the orchestrator said it, the user name
	// kept, the password neither shown nor logged.
	f.err = errors.New("MySQL menolak login: akses ditolak untuk admin")
	form := url.Values{"user": {"admin"}, "password": {mysqlSecret}, "host": {""}}
	rec = e.do("POST", "/servers/1/mysql/connect", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "MySQL menolak login: akses ditolak untuk admin", `value="admin"`)
	wantNoBody(t, rec, mysqlSecret)

	// Without a user name nothing is sent to the agent.
	rec = e.do("POST", "/servers/1/mysql/connect", url.Values{"user": {""}, "password": {mysqlSecret}}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Nama pengguna MySQL wajib diisi")
	wantNoBody(t, rec, mysqlSecret)

	f.err = nil
	f.result = proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.0.39", Series: "8.0"}, Databases: shopDatabases}
	rec = e.do("POST", "/servers/1/mysql/connect", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/1?pesan=terhubung")
	wantNoBody(t, rec, mysqlSecret)
	f.mu.Lock()
	connects := append([]proto.ConnectParams(nil), f.connects...)
	f.mu.Unlock()
	// An empty host stays empty: the agent then uses the local socket.
	want := proto.ConnectParams{User: "admin", Password: mysqlSecret, Host: ""}
	if len(connects) != 2 || connects[1] != want {
		t.Errorf("connect params = %+v, want twice %+v", connects, want)
	}

	rec = e.do("GET", "/servers/1?pesan=terhubung", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "MySQL berhasil dihubungkan", "8.0.39", "toko", "1.5 GB", "gudang", "300.0 MB",
		`action="/servers/1/mysql/refresh"`, "Segarkan",
		`action="/servers/1/prepare-master"`, "Siapkan sebagai master", "Hubungkan ulang")
	wantNoBody(t, rec, mysqlSecret)

	rec = e.do("POST", "/servers/1/mysql/refresh", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/1?pesan=disegarkan")
	wantCalls(t, f, "connect 1", "connect 1", "refresh 1")

	if strings.Contains(logs.String(), mysqlSecret) {
		t.Errorf("password in the log:\n%s", logs.String())
	}
	if _, err := e.st.GetInventory(srv.ID); err != nil {
		t.Errorf("inventory not stored: %v", err)
	}
}

func TestConnectMySQLShowsMissingGrants(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	grant := "GRANT RELOAD, REPLICATION CLIENT ON *.* TO 'admin'@'localhost'"
	f.result = proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.2", Series: "8.4"},
		Databases: shopDatabases, MissingGrants: []string{grant}}
	form := url.Values{"user": {"admin"}, "password": {mysqlSecret}, "host": {"10.1.1.5"}}
	wantCode(t, e.do("POST", "/servers/1/mysql/connect", form, c), http.StatusSeeOther)
	// A typed host goes to the agent as typed.
	f.mu.Lock()
	connects := append([]proto.ConnectParams(nil), f.connects...)
	f.mu.Unlock()
	if want := (proto.ConnectParams{User: "admin", Password: mysqlSecret, Host: "10.1.1.5"}); len(connects) != 1 || connects[0] != want {
		t.Errorf("connect params = %+v, want %+v", connects, want)
	}

	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "8.4.2", "toko", template.HTMLEscapeString(grant), "Segarkan")
	// Not preparable until the grants are there.
	wantNoBody(t, rec, `action="/servers/1/prepare-master"`, mysqlSecret)

	// A replica is never offered as a master either.
	f.result.MissingGrants = nil
	wantCode(t, e.do("POST", "/servers/1/mysql/refresh", url.Values{}, c), http.StatusSeeOther)
	if err := e.st.SetServerRole(1, store.RoleReplica); err != nil {
		t.Fatal(err)
	}
	wantNoBody(t, e.do("GET", "/servers/1", nil, c), `action="/servers/1/prepare-master"`)
}

func TestPrepareMasterStartsJob(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	if err := e.st.SetInventory(store.Inventory{ServerID: 1, Connected: true, Version: "8.0.39",
		Databases: shopDatabases, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	rec := e.do("POST", "/servers/1/prepare-master", url.Values{}, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)

	f.err = errors.New("server ini sudah menjalankan pekerjaan lain")
	rec = e.do("POST", "/servers/1/prepare-master", url.Values{}, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server ini sudah menjalankan pekerjaan lain")
	wantCalls(t, f, "prepare 1", "prepare 1")

	wantCode(t, e.do("POST", "/servers/99/prepare-master", url.Values{}, c), http.StatusNotFound)
}

func TestNewReplicaFormListsEligibleServersAndDatabases(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	e.pairedOnline(t, "db-calon") // 2: eligible
	e.st.CreateServer("db-belum") // 3: not paired
	off, _ := e.st.CreateServer("db-mati")
	e.st.MarkPaired(off.ID, "serial-mati", "10.0.0.4") // 4: offline
	busy := e.pairedOnline(t, "db-replika-lain")       // 5: already a replica
	topo := e.makeMaster(t, master, shopDatabases...)
	if _, err := e.st.AddReplica(topo.ID, busy.ID, []string{"toko"}); err != nil {
		t.Fatal(err)
	}

	// Not a master yet: the server page instead.
	rec := e.do("GET", "/masters/2", nil, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/servers/2")

	rec = e.do("GET", "/masters/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "db-master", "8.0.39", "toko", "1.5 GB", "db-replika-lain", `href="/replicas/5"`, ">Sedang dibuat<",
		`href="/masters/1/replicas/new"`, "Buat replika")

	rec = e.do("GET", "/masters/1/replicas/new", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/masters/1/replicas"`, `<option value="2">db-calon`,
		`name="databases" value="toko" checked`, `name="databases" value="gudang" checked`,
		"1.5 GB", "300.0 MB", `name="force_dump"`, "Paksa dump", `name="confirm_wipe"`, "Saya mengerti",
		"Server tujuan harus server replika khusus: seluruh MySQL di server itu dikunci hanya-baca "+
			"(read_only + super_read_only) dan semua database lamanya dibuang. Aplikasi lain di server itu tidak bisa menulis.")
	for _, v := range []string{`<option value="1"`, `<option value="3"`, `<option value="4"`, `<option value="5"`} {
		wantNoBody(t, rec, v)
	}

	rec = e.do("GET", "/replicas/5", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "db-replika-lain", "toko", `href="/masters/1"`, "Tambah database", "Lepas database")
	wantCode(t, e.do("GET", "/replicas/2", nil, c), http.StatusNotFound)
}

func TestCreateReplicaPostsSelectedDatabases(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	e.pairedOnline(t, "db-calon")  // 2: eligible
	e.st.CreateServer("db-belum")  // 3: not paired
	e.pairedOnline(t, "db-target") // 4: made a master below
	e.makeMaster(t, master, append(shopDatabases, proto.DatabaseInfo{Name: "toko-online", Bytes: 4 << 20})...)
	if err := e.st.SetServerRole(4, store.RoleMaster); err != nil {
		t.Fatal(err)
	}

	// Names as MySQL allows them, such as toko-online, are offered and checked.
	wantBody(t, e.do("GET", "/masters/1/replicas/new", nil, c), `name="databases" value="toko-online" checked`)

	post := func(form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/1/replicas", form, c)
	}
	ok := url.Values{"target": {"2"}, "databases": {"toko", "toko-online"}, "force_dump": {"1"}, "confirm_wipe": {"1"}}

	// Refused before the orchestrator is asked.
	bad := []struct {
		change func(url.Values)
		msg    string
	}{
		{func(v url.Values) { v.Del("confirm_wipe") }, "Replika belum dibuat: centang kotak persetujuan"},
		{func(v url.Values) { v.Del("databases") }, "Pilih minimal satu database untuk direplikasi."},
		{func(v url.Values) { v["databases"] = []string{"toko", "x;DROP"} }, "Database x;DROP tidak ada di master"},
		{func(v url.Values) { v["databases"] = []string{"hilang"} }, "Database hilang tidak ada di master"},
		{func(v url.Values) { v.Del("target") }, "Pilih server tujuan replika terlebih dahulu."},
		// Not offered by the form: the master itself, unpaired, with a role, unknown.
		{func(v url.Values) { v.Set("target", "1") }, "Server tujuan tidak memenuhi syarat"},
		{func(v url.Values) { v.Set("target", "3") }, "Server tujuan tidak memenuhi syarat"},
		{func(v url.Values) { v.Set("target", "4") }, "Server tujuan tidak memenuhi syarat"},
		{func(v url.Values) { v.Set("target", "99") }, "Server tujuan tidak memenuhi syarat"},
	}
	for _, b := range bad {
		form := url.Values{}
		for k, v := range ok {
			form[k] = append([]string(nil), v...)
		}
		b.change(form)
		rec := post(form)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, b.msg)
	}
	wantCalls(t, f)

	rec := post(ok)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	want := orchestrator.CreateReplicaParams{MasterServerID: 1, TargetServerID: 2,
		Databases: []string{"toko", "toko-online"}, ForceDump: true, ConfirmWipe: true}
	f.mu.Lock()
	got := append([]orchestrator.CreateReplicaParams(nil), f.replicas...)
	f.mu.Unlock()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("create replica params = %+v, want %+v", got, want)
	}

	// The orchestrator's refusal comes back on the form.
	f.err = errors.New("server tujuan sudah berperan sebagai master")
	rec = post(ok)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server tujuan sudah berperan sebagai master", `action="/masters/1/replicas"`)
}

// newTestJob stores a job of kind on server 1 with the given steps.
func newTestJob(t *testing.T, st *store.Store, id, kind string, steps ...string) {
	t.Helper()
	if err := st.CreateJob(store.Job{ID: id, Kind: kind, ServerID: 1, CreatedAt: time.Now()}, steps); err != nil {
		t.Fatal(err)
	}
}

const (
	waitingJob = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	failedJob  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestJobDetailShowsStepsAndButtons(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	now := time.Now()
	restartMsg := "MySQL perlu dimulai ulang. bind-address akan diubah; pastikan firewall membatasi port 3306."
	newTestJob(t, e.st, waitingJob, orchestrator.KindPrepareMaster, "Periksa master", "Mulai ulang MySQL", "Verifikasi")
	e.st.SetStep(waitingJob, 0, store.StepDone, "", now)
	e.st.SetStep(waitingJob, 1, store.StepWaiting, restartMsg, now)
	if err := e.st.UpdateJob(waitingJob, store.JobWaiting, 1, "", now); err != nil {
		t.Fatal(err)
	}
	e.st.AppendJobLog(waitingJob, "info", "Pemeriksaan <b>selesai</b>", now)

	rec := e.do("GET", "/jobs/"+waitingJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Siapkan master", "db-utama", "Periksa master", "Mulai ulang MySQL", "Verifikasi",
		">Selesai<", ">Menunggu persetujuan<", ">Antre<", template.HTMLEscapeString(restartMsg),
		"Pemeriksaan &lt;b&gt;selesai&lt;/b&gt;",
		`action="/jobs/`+waitingJob+`/approve"`, "Setujui &amp; lanjutkan",
		`action="/jobs/`+waitingJob+`/cancel"`, "Batalkan",
		`x-data="jobLive" data-job-id="`+waitingJob+`"`)
	wantNoBody(t, rec, "/retry", "Ulangi dari tahap ini", "<b>selesai")

	newTestJob(t, e.st, failedJob, orchestrator.KindCreateReplica, "Periksa master", "Salin data")
	e.st.SetStep(failedJob, 0, store.StepDone, "", now)
	e.st.SetStep(failedJob, 1, store.StepFailed, "disk penuh", now)
	if err := e.st.UpdateJob(failedJob, store.JobFailed, 1, "disk penuh", now); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/jobs/"+failedJob, nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Buat replika", ">Gagal<", "disk penuh",
		`action="/jobs/`+failedJob+`/retry"`, "Ulangi dari tahap ini")
	wantNoBody(t, rec, "/approve", "Setujui", "/cancel")

	wantCode(t, e.do("GET", "/jobs/cccccccccccccccccccccccccccccccc", nil, c), http.StatusNotFound)
	wantCode(t, e.do("GET", "/jobs/bukan-id", nil, c), http.StatusNotFound)

	rec = e.do("GET", "/jobs", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `href="/jobs/`+waitingJob+`"`, `href="/jobs/`+failedJob+`"`, "Menunggu persetujuan", "Gagal", "db-utama")
}

func TestJobStatusLabels(t *testing.T) {
	for status, want := range map[string]string{
		store.JobQueued: "Antre", store.StepPending: "Antre",
		store.JobRunning: "Berjalan", store.JobWaiting: "Menunggu persetujuan",
		store.JobSucceeded: "Selesai", store.StepDone: "Selesai",
		store.JobFailed: "Gagal", store.JobCanceled: "Dibatalkan",
		store.StepSkipped: "Dilewati", "aneh": "aneh",
		store.ReplicaCreating: "Sedang dibuat", store.ReplicaReady: "Siap", // ReplicaFailed is JobFailed
		store.ReplicaChanging: "Sedang diubah",
	} {
		if got := web.StatusLabel(status); got != want {
			t.Errorf("StatusLabel(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestJobApproveRetryCancelPost(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-utama")
	newTestJob(t, e.st, waitingJob, orchestrator.KindPrepareMaster, "Periksa master")

	for _, action := range []string{"approve", "retry", "cancel"} {
		rec := e.do("POST", "/jobs/"+waitingJob+"/"+action, url.Values{}, c)
		wantCode(t, rec, http.StatusSeeOther)
		wantLocation(t, rec, "/jobs/"+waitingJob)
	}
	wantCalls(t, f, "approve "+waitingJob, "retry "+waitingJob, "cancel "+waitingJob)

	f.err = errors.New("pekerjaan tidak sedang menunggu persetujuan")
	rec := e.do("POST", "/jobs/"+waitingJob+"/approve", url.Values{}, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "pekerjaan tidak sedang menunggu persetujuan", "Periksa master")

	// Unknown jobs and cross-site posts never reach the orchestrator.
	f.err = nil
	n := len(f.callList())
	wantCode(t, e.do("POST", "/jobs/cccccccccccccccccccccccccccccccc/approve", url.Values{}, c), http.StatusNotFound)
	req := httptest.NewRequest("POST", "/jobs/"+waitingJob+"/cancel", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusForbidden)
	if len(f.callList()) != n {
		t.Errorf("orchestrator called: %q", f.callList()[n:])
	}
}

func TestJobsListRequiresLogin(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	for _, p := range []string{"/jobs", "/jobs/" + waitingJob, "/masters/1", "/masters/1/replicas/new", "/replicas/1",
		"/replicas/1/databases"} {
		rec := e.do("GET", p, nil, nil)
		wantCode(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("%s: Location = %q", p, loc)
		}
	}
	for _, p := range []string{"/servers/1/mysql/connect", "/servers/1/mysql/refresh", "/servers/1/prepare-master",
		"/masters/1/replicas", "/jobs/" + waitingJob + "/approve", "/replicas/1/databases/add",
		"/replicas/1/databases/remove"} {
		rec := e.do("POST", p, url.Values{"password": {mysqlSecret}}, nil)
		wantCode(t, rec, http.StatusSeeOther)
	}
	wantCalls(t, f)
}

// TestMySQLPagesAreCSPSafe: the new pages carry no inline script, event
// handler, javascript: URL or inline style, and never the MySQL password.
func TestMySQLPagesAreCSPSafe(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	rep := e.pairedOnline(t, "db-replika")
	e.pairedOnline(t, "db-calon")
	f.result = proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.0.39"}, Databases: shopDatabases,
		MissingGrants: []string{"GRANT RELOAD ON *.* TO 'admin'@'%'"}}
	wantCode(t, e.do("POST", "/servers/3/mysql/connect",
		url.Values{"user": {"admin"}, "password": {mysqlSecret}}, c), http.StatusSeeOther)
	topo := e.makeMaster(t, master, shopDatabases...)
	if _, err := e.st.AddReplica(topo.ID, rep.ID, nil); err != nil {
		t.Fatal(err)
	}
	subset := e.pairedOnline(t, "db-replika-sebagian")
	if _, err := e.st.AddReplica(topo.ID, subset.ID, []string{"toko"}); err != nil {
		t.Fatal(err)
	}
	e.st.SetReplicaState(subset.ID, store.ReplicaReady, "")
	newTestJob(t, e.st, waitingJob, orchestrator.KindPrepareMaster, "Periksa master")
	e.st.AppendJobLog(waitingJob, "info", `<script>alert(1)</script>`, time.Now())

	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>[^<]|\son[a-z]+=|javascript:|\sstyle=`)
	for _, p := range []string{"/", "/servers/1", "/servers/2", "/servers/3", "/masters/1", "/masters/1/replicas/new",
		"/replicas/2", "/replicas/4", "/replicas/4/databases", "/jobs", "/jobs/" + waitingJob} {
		rec := e.do("GET", p, nil, c)
		wantCode(t, rec, http.StatusOK)
		if m := inline.FindString(rec.Body.String()); m != "" {
			t.Errorf("GET %s has inline script/handler/style %q", p, m)
		}
		wantNoBody(t, rec, mysqlSecret)
	}
	rec := e.do("GET", "/", nil, c)
	wantBody(t, rec, `href="/jobs"`, "Pekerjaan", `data-stat="masters">1<`, `data-stat="replicas">2<`)
}

// subsetReplica makes server 1 a master with dbs and server 2 a ready
// replica of it following follow (none = all).
func (e *env) subsetReplica(t *testing.T, follow []string, dbs ...proto.DatabaseInfo) {
	t.Helper()
	master := e.pairedOnline(t, "db-master")
	rep := e.pairedOnline(t, "db-replika")
	topo := e.makeMaster(t, master, dbs...)
	if _, err := e.st.AddReplica(topo.ID, rep.ID, follow); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(rep.ID, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
}

func TestReplicaDatabasesPageHiddenForAll(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	e.subsetReplica(t, nil, shopDatabases...)

	rec := e.do("GET", "/replicas/2", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "semua")
	wantNoBody(t, rec, "/replicas/2/databases", "Tambah database", "Lepas database")

	rec = e.do("GET", "/replicas/2/databases", nil, c)
	wantCode(t, rec, http.StatusNotFound)
	wantBody(t, rec, "replika ini mengikuti semua database")
	for _, p := range []string{"/replicas/2/databases/add", "/replicas/2/databases/remove"} {
		rec := e.do("POST", p, url.Values{"databases": {"toko"}}, c)
		wantCode(t, rec, http.StatusNotFound)
		wantBody(t, rec, "replika ini mengikuti semua database")
	}
	// Not a replica at all.
	wantCode(t, e.do("GET", "/replicas/1/databases", nil, c), http.StatusNotFound)
	wantCode(t, e.do("GET", "/replicas/9/databases", nil, c), http.StatusNotFound)
	wantCalls(t, f)
}

var archiveDatabases = append([]proto.DatabaseInfo{{Name: "arsip", Bytes: 2 << 20}}, shopDatabases...)

func TestReplicaDatabasesPageListsAndPosts(t *testing.T) {
	e, f, logs := newMySQLEnv(t)
	c := e.cookie()
	e.subsetReplica(t, []string{"gudang", "toko"}, archiveDatabases...)

	rec := e.do("GET", "/replicas/2", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `href="/replicas/2/databases#tambah"`, `href="/replicas/2/databases#lepas"`, "Tambah database", "Lepas database")
	wantNoBody(t, rec, "disabled")

	rec = e.do("GET", "/replicas/2/databases", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "db-replika", "db-master", "arsip", "2.0 MB", ">Diikuti<", ">Tidak diikuti<",
		`action="/replicas/2/databases/add"`, `action="/replicas/2/databases/remove"`,
		`name="drop_data"`, "Hapus datanya juga di replika",
		`x-data="removeDatabases"`, `<dialog`, `class="modal"`)
	body := rec.Body.String()
	addForm, removeForm, _ := strings.Cut(body[strings.Index(body, `action="/replicas/2/databases/add"`):],
		`action="/replicas/2/databases/remove"`)
	if !strings.Contains(addForm, `name="databases" value="arsip"`) || strings.Contains(addForm, `value="toko"`) ||
		strings.Contains(addForm, `value="gudang"`) {
		t.Errorf("add form offers the wrong databases:\n%s", addForm)
	}
	if !strings.Contains(removeForm, `name="databases" value="toko"`) || !strings.Contains(removeForm, `name="databases" value="gudang"`) ||
		strings.Contains(removeForm, `value="arsip"`) {
		t.Errorf("remove form offers the wrong databases:\n%s", removeForm)
	}

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		return e.do("POST", path, form, c)
	}
	bad := []struct {
		path string
		form url.Values
		msg  string
	}{
		{"/replicas/2/databases/add", url.Values{}, "Pilih minimal satu database."},
		{"/replicas/2/databases/add", url.Values{"databases": {"toko"}}, "Database toko sudah diikuti replika ini"},
		{"/replicas/2/databases/add", url.Values{"databases": {"hilang"}}, "Database hilang tidak ada di master"},
		{"/replicas/2/databases/add", url.Values{"databases": {"x;DROP"}}, "Database x;DROP tidak ada di master"},
		{"/replicas/2/databases/remove", url.Values{}, "Pilih minimal satu database."},
		{"/replicas/2/databases/remove", url.Values{"databases": {"arsip"}}, "Database arsip tidak diikuti replika ini"},
		{"/replicas/2/databases/remove", url.Values{"databases": {"toko", "gudang"}, "drop_data": {"1"}},
			"Lepas semua database tidak didukung; hapus replika"},
	}
	for _, b := range bad {
		rec := post(b.path, b.form)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, b.msg, `action="/replicas/2/databases/add"`)
	}
	wantCalls(t, f)

	rec = post("/replicas/2/databases/add", url.Values{"databases": {"arsip"}})
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	rec = post("/replicas/2/databases/remove", url.Values{"databases": {"gudang"}, "drop_data": {"1"}})
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/"+f.nextID)
	rec = post("/replicas/2/databases/remove", url.Values{"databases": {"toko"}})
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "add-databases 2 arsip", "remove-databases 2 gudang drop=true", "remove-databases 2 toko drop=false")
	if !strings.Contains(logs.String(), "replica databases add started") {
		t.Errorf("log lacks the start line:\n%s", logs.String())
	}

	// The orchestrator's refusal is shown on the page.
	f.err = errors.New("server ini masih punya pekerjaan yang belum selesai")
	rec = post("/replicas/2/databases/add", url.Values{"databases": {"arsip"}})
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server ini masih punya pekerjaan yang belum selesai")
	f.err = nil

	// A replica that is not ready shows no forms and takes no post.
	e.st.SetReplicaState(2, store.ReplicaChanging, "")
	n := len(f.callList())
	rec = e.do("GET", "/replicas/2/databases", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Sedang diubah", "hanya bisa diubah saat replika berstatus Siap")
	wantNoBody(t, rec, `action="/replicas/2/databases/add"`, `action="/replicas/2/databases/remove"`)
	rec = e.do("GET", "/replicas/2", nil, c)
	wantBody(t, rec, "Tambah database", "disabled")
	wantNoBody(t, rec, `href="/replicas/2/databases`)
	rec = post("/replicas/2/databases/add", url.Values{"databases": {"arsip"}})
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "hanya bisa diubah saat replika berstatus Siap")
	if len(f.callList()) != n {
		t.Errorf("orchestrator called: %q", f.callList()[n:])
	}
}

func TestChangeDatabasesJobLabels(t *testing.T) {
	e, _, _ := newMySQLEnv(t)
	c := e.cookie()
	e.pairedOnline(t, "db-master")
	rep := e.pairedOnline(t, "db-replika")
	for _, j := range []struct {
		id, kind, params, step string
	}{
		{waitingJob, orchestrator.KindAddDatabases, `{"master_server_id":1,"databases":["toko"],"change_databases":["arsip"]}`,
			"Menunggu replika menyusul"},
		{failedJob, orchestrator.KindRemoveDatabases,
			`{"master_server_id":1,"databases":["arsip","toko"],"change_databases":["arsip"],"drop_data":true}`, "Kecilkan filter"},
	} {
		if err := e.st.CreateJob(store.Job{ID: j.id, Kind: j.kind, ServerID: rep.ID, ParamsJSON: j.params}, []string{j.step}); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.do("GET", "/jobs/"+waitingJob, nil, c)
	wantBody(t, rec, "Tambah database", "Menunggu replika menyusul", `href="/masters/1"`, "Database ditambahkan", "arsip")
	wantNoBody(t, rec, "dihapus di replika", "toko")
	rec = e.do("GET", "/jobs/"+failedJob, nil, c)
	wantBody(t, rec, "Lepas database", "Kecilkan filter", "Database dilepas", "arsip (datanya dihapus di replika)")
}
