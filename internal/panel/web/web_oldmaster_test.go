package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The page of a quarantined old master after a promotion (spec 5.4) and
// the replicas left to repoint or rebuild (spec 5.6).

const salvageName = "20261105T094500Z-1.sql"

// promotedEnv is controlEnv plus replica 3 (db-lain), after a planned
// promotion of db-replika (2): db-master (1) is the quarantined old
// master, the topology waits for the cutover.
func promotedEnv(t *testing.T, mods ...func(*web.Deps)) (*env, *fakeOrch, store.Topology) {
	t.Helper()
	e, f, _ := controlEnv(t, mods...)
	topo, err := e.st.GetTopologyByMaster(1)
	if err != nil {
		t.Fatal(err)
	}
	e.addReadyReplica(t, topo, "db-lain")
	if err := e.st.SwapMaster(topo.ID, 1, 2, "10.0.0.2", 3306); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetInventory(store.Inventory{ServerID: 2, Connected: true, Version: "8.0.39", Series: "8.0",
		Databases: shopDatabases, MasterReady: true, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	servers, _ := json.Marshal([]orchestrator.PromotionServer{{ServerID: 1, Role: orchestrator.PromotionOldMaster,
		State: orchestrator.QuarantineStateDone}})
	started := time.Now().Add(-10 * time.Minute)
	finished := started.Add(2 * time.Minute)
	if _, err := e.st.AddPromotion(store.Promotion{TopologyID: topo.ID, Mode: "planned", OldMasterServerID: 1,
		NewMasterServerID: 2, Status: "done", JobID: "fedcba9876543210fedcba9876543210", StartedAt: started,
		FinishedAt: &finished, LockAt: &started, ReplicasJSON: string(servers)}); err != nil {
		t.Fatal(err)
	}
	if topo, err = e.st.GetTopology(topo.ID); err != nil {
		t.Fatal(err)
	}
	return e, f, topo
}

// setLeftover stores a leftover check on the promotion.
func setLeftover(t *testing.T, e *env, topo store.Topology, res proto.LeftoverResult) {
	t.Helper()
	p, err := e.st.LatestPromotion(topo.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(orchestrator.LeftoverCheck{LeftoverResult: res, CheckedAt: time.Now()})
	p.LeftoverJSON = string(b)
	if err := e.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
}

func threeLeftover() proto.LeftoverResult {
	from := time.Date(2026, 11, 5, 9, 41, 20, 0, time.Local)
	to := from.Add(11 * time.Second)
	return proto.LeftoverResult{GTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:121-123", Transactions: 3,
		Tables: []proto.TableTouch{{Schema: "simrs", Table: "pasien", Rows: 2}, {Schema: "simrs", Table: "kunjungan", Rows: 1}},
		From:   &from, To: &to, Binlogs: []string{"binlog.000007"}}
}

func (f *fakeOrch) setConnections(id int64, rows ...proto.ConnectionRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connections == nil {
		f.connections = map[int64]proto.ConnectionsResult{}
	}
	f.connections[id] = proto.ConnectionsResult{Rows: rows, TakenAt: time.Now()}
}

func (f *fakeOrch) setConnErr(id int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connErr == nil {
		f.connErr = map[int64]error{}
	}
	f.connErr[id] = err
}

func TestLeftoverAndSalvageButtons(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()

	// No check yet: the box, the check button, no Amankan.
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Master lama", "promote terencana ke", "db-replika", `action="/servers/1/leftover"`,
		"Periksa data tertinggal", "Belum diperiksa", `href="/masters/2/cutover"`)
	wantNoBody(t, rec, `action="/servers/1/salvage"`)

	// Nothing left over: green, still no Amankan.
	setLeftover(t, e, topo, proto.LeftoverResult{})
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Tidak ada data tertinggal", `action="/servers/1/leftover"`)
	wantNoBody(t, rec, `action="/servers/1/salvage"`)

	// Three transactions: the table and Amankan.
	setLeftover(t, e, topo, threeLeftover())
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Ada 3 transaksi yang tidak sampai ke master baru", "simrs.pasien", "simrs.kunjungan",
		"09:41:20", "09:41:31", "binlog.000007", `action="/servers/1/salvage"`, "Amankan data tertinggal")

	// The buttons start their jobs.
	rec = e.do("POST", "/servers/1/leftover", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/jobs/0123456789abcdef0123456789abcdef")
	rec = e.do("POST", "/servers/1/salvage", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "leftover 1", "salvage 1")

	// A refusal is shown on the page, in words.
	f.err = fmt.Errorf("agent master lama offline; periksa lagi setelah agent-nya tersambung")
	rec = e.do("POST", "/servers/1/leftover", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "agent master lama offline")
	f.err = nil

	// The salvage files, with download and delete behind a confirmation.
	f.salvageFiles = []proto.SalvageFile{{Name: salvageName, Size: 4096, CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(6*24*time.Hour + time.Hour)}}
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, salvageName, `action="/servers/1/salvage/`+salvageName+`"`, `data-confirm-action="salvage_download"`,
		`action="/servers/1/salvage/`+salvageName+`/delete"`, `data-confirm-action="salvage_delete"`, "6 hari")

	// Delete needs the token; with it the file goes.
	wantCode(t, e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", noForm, c), http.StatusForbidden)
	form := withToken(t, e, noForm, web.ActSalvageDelete, "server", 1)
	rec = e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "leftover 1", "salvage 1", "leftover 1", "salvage-delete 1 "+salvageName)
}

func downloadURL(name, token string) string {
	u := "/servers/1/salvage/" + name
	if token != "" {
		u += "?confirm_token=" + url.QueryEscape(token)
	}
	return u
}

func TestSalvageDownloadStreamsWithToken(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	f.chunks = [][]byte{[]byte("-- bagian satu\n"), []byte("INSERT INTO pasien VALUES (1);\n"), []byte("-- selesai\n")}

	// Without a token: 403, nothing read.
	rec := e.do("GET", downloadURL(salvageName, ""), nil, c)
	wantCode(t, rec, http.StatusForbidden)
	if f.reads != 0 {
		t.Fatalf("%d reads without a token", f.reads)
	}

	// A bad name is refused before the agent.
	for _, name := range []string{"..sql", "a%2F..%2Fb.sql", "20261105T094500Z-1.sh"} {
		tok := confirmToken(t, e, web.ActSalvageDownload, "server", 1)
		rec := e.do("GET", downloadURL(name, tok), nil, c)
		wantCode(t, rec, http.StatusBadRequest)
	}
	if f.reads != 0 {
		t.Fatalf("%d reads for bad names", f.reads)
	}

	tok := confirmToken(t, e, web.ActSalvageDownload, "server", 1)
	rec = e.do("GET", downloadURL(salvageName, tok), nil, c)
	wantCode(t, rec, http.StatusOK)
	want := string(bytes.Join(f.chunks, nil))
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
	h := rec.Header()
	if got := h.Get("Content-Disposition"); got != "attachment; filename="+salvageName {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := h.Get("Content-Length"); got != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length = %q", got)
	}
	if got := h.Get("Content-Type"); got != "application/sql" {
		t.Errorf("Content-Type = %q", got)
	}
	wantCalls(t, f, "salvage-read 1 "+salvageName+" 0",
		fmt.Sprintf("salvage-read 1 %s %d", salvageName, len(f.chunks[0])),
		fmt.Sprintf("salvage-read 1 %s %d", salvageName, len(f.chunks[0])+len(f.chunks[1])))
	// The token is used up.
	wantCode(t, e.do("GET", downloadURL(salvageName, tok), nil, c), http.StatusForbidden)

	// Audited with name, size and outcome.
	entries, err := e.st.ListAudit(0, time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range entries {
		if a.Action == "salvage_download" && a.Outcome == "ok" {
			found = strings.Contains(a.DetailJSON, salvageName) && strings.Contains(a.DetailJSON, strconv.Itoa(len(want)))
		}
	}
	if !found {
		t.Fatalf("no salvage_download audit with name and size: %+v", entries)
	}
}

func TestSalvageDownloadStopsWhenClientGone(t *testing.T) {
	e, f, _ := promotedEnv(t)
	f.chunks = [][]byte{bytes.Repeat([]byte("x"), 1024)}
	f.endless = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onRead = func(offset int64) {
		if offset > 0 {
			cancel() // the browser went away after the first chunk
		}
	}
	tok := confirmToken(t, e, web.ActSalvageDownload, "server", 1)
	req := httptest.NewRequest("GET", downloadURL(salvageName, tok), nil).WithContext(ctx)
	req.AddCookie(e.cookie())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		e.h.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not stop when the client went away")
	}
	if f.reads > 3 {
		t.Fatalf("%d reads after the client went away", f.reads)
	}
	entries, err := e.st.ListAudit(0, time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range entries {
		if a.Action == "salvage_download" {
			if a.Outcome == "ok" {
				t.Fatalf("interrupted download audited ok: %+v", a)
			}
			if !strings.Contains(a.DetailJSON, salvageName) {
				t.Fatalf("interrupted download audit lacks the name: %+v", a)
			}
			return
		}
	}
	t.Fatal("no salvage_download audit")
}

func rebuildForm(extra ...string) url.Values {
	v := url.Values{"rebuild": {"1"}, "target": {"1"}, "databases": {"toko", "gudang"}, "confirm_wipe": {"1"},
		"confirm_name": {"db-master"}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

func TestRebuildRefusedWhileCutoverPending(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	const wait = "Tekan 'Sudah saya alihkan' dulu"

	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Bangun ulang sebagai replika", wait)
	wantNoBody(t, rec, `href="/masters/2/replicas/new?bangun-ulang=1"`)

	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantBody(t, rec, wait)

	// Without a token: 403.
	wantCode(t, e.do("POST", "/masters/2/replicas", rebuildForm(), c), http.StatusForbidden)
	// With one: refused while the cutover is pending.
	rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm(), web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "Sudah saya alihkan")
	wantCalls(t, f)
}

func TestRebuildWarnsOnLeftoverAndConnections(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	f.setConnections(1, proto.ConnectionRow{Host: "10.0.0.50", User: "app", DB: "simrs", Count: 3},
		proto.ConnectionRow{Host: "10.0.0.51", User: "laporan", DB: "simrs", Count: 1})

	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, `href="/masters/2/replicas/new?bangun-ulang=1"`)

	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Bangun ulang db-master sebagai replika", "4 koneksi aplikasi", "belum diperiksa",
		`data-confirm-action="rebuild_old_master"`, `data-confirm-name="db-master"`, `name="rebuild" value="1"`)

	setLeftover(t, e, topo, threeLeftover())
	f.setConnections(1)
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantBody(t, rec, "3 transaksi", "akan hilang dari server ini")
	wantNoBody(t, rec, "koneksi aplikasi masih")

	// Agent offline: said so.
	f.setConnErr(1, proto.ErrOffline)
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantBody(t, rec, "Koneksi aplikasi ke db-master tidak bisa dibaca")

	// Without the typed name: refused.
	rec = e.do("POST", "/masters/2/replicas",
		withToken(t, e, rebuildForm("confirm_name", "salah"), web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantCalls(t, f)

	rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm(), web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "create-replica 2->1")
	if p := f.replicas[0]; !p.RebuildOldMaster || p.MasterServerID != 2 || p.TargetServerID != 1 || !p.ConfirmWipe {
		t.Fatalf("create replica params = %+v", p)
	}

	// The quarantined server is never a target of the plain form.
	rec = e.do("GET", "/masters/2/replicas/new", nil, c)
	wantNoBody(t, rec, `<option value="1"`)
}

func TestDeleteOldMasterRefusedWhileCutoverPending(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Hapus server", "tidak bisa dihapus sebelum")
	wantNoBody(t, rec, `data-open-modal="modal-hapus"`)

	form := withToken(t, e, url.Values{"confirm_name": {"db-master"}}, web.ActDeleteServer, "server", 1)
	rec = e.do("POST", "/servers/1/delete", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, "Sudah saya alihkan")
	wantCalls(t, f)

	// After the cutover, the orchestrator's refusal (quarantine pending)
	// is shown in words, never as a 500.
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	f.deleteErr = orchestrator.ErrQuarantinePending
	form = withToken(t, e, url.Values{"confirm_name": {"db-master"}}, web.ActDeleteServer, "server", 1)
	rec = e.do("POST", "/servers/1/delete", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "belum dikarantina")

	// A pending quarantine offers deleting without it, with its own words.
	p, err := e.st.LatestPromotion(topo.ID)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := json.Marshal([]orchestrator.PromotionServer{{ServerID: 1, Role: orchestrator.PromotionOldMaster,
		State: orchestrator.QuarantineStatePending}})
	p.ReplicasJSON = string(servers)
	if err := e.st.UpdatePromotion(p); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Hapus tanpa karantina", `name="unquarantined" value="1"`, "tidak pernah dikarantina")
	form = withToken(t, e, url.Values{"confirm_name": {"db-master"}, "unquarantined": {"1"}}, web.ActDeleteServer, "server", 1)
	rec = e.do("POST", "/servers/1/delete", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "delete-server 1", "delete-unquarantined 1")
}

func TestReplicaNeedsRepointButtons(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetReplicaState(3, store.ReplicaNeedsRepoint, ""); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/replicas/3", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Perlu diarahkan ulang ke master baru; akan dilakukan saat tersambung", `action="/replicas/3/repoint"`,
		"Arahkan sekarang")

	rec = e.do("POST", "/replicas/3/repoint", noForm, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantLocation(t, rec, "/replicas/3?pesan=diarahkan")
	wantCalls(t, f, "repoint 3")

	// The replica holds transactions the new master lacks: information
	// with the way out, not a failure.
	f.repointErr = fmt.Errorf("%w (abc:5)", orchestrator.ErrNeedsRebuild)
	if err := e.st.SetReplicaState(3, store.ReplicaNeedsRebuild, ""); err != nil {
		t.Fatal(err)
	}
	rec = e.do("POST", "/replicas/3/repoint", noForm, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "replika punya transaksi yang tidak ada di master baru", `action="/replicas/3/resync/prepare"`)

	rec = e.do("GET", "/replicas/3", nil, c)
	wantBody(t, rec, "Perlu dibangun ulang: replika ini punya transaksi yang tidak ada di master baru",
		`action="/replicas/3/resync/prepare"`, "Resync ulang")
	wantNoBody(t, rec, `action="/replicas/3/repoint"`)
}
