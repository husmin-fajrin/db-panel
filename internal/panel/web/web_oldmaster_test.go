package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
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
	// The inventory the promote leaves on the new master: connected as a
	// replica, then marked MasterReady by the swap step (the orchestrator's
	// TestPromoteNewMasterReadyForReplicas goes through that swap).
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
	// The old master's agent is of the panel's version: its salvage
	// files are listed (TestSalvageListSkippedForOldAgent).
	if err := e.st.UpdateProbe(1, proto.ProbeResult{}, "v-test", time.Now()); err != nil {
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
	f.connections[id] = proto.ConnectionsResult{Rows: rows, TakenAt: time.Now(), Typed: true, Source: "performance_schema"}
}

// setConnectionsResult scripts a whole result (an old agent, Incomplete,
// Unreadable, ...).
func (f *fakeOrch) setConnectionsResult(id int64, res proto.ConnectionsResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connections == nil {
		f.connections = map[int64]proto.ConnectionsResult{}
	}
	if res.TakenAt.IsZero() {
		res.TakenAt = time.Now()
	}
	f.connections[id] = res
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
	wantBody(t, rec, salvageName, `action="/servers/1/salvage/`+salvageName+`"`, `data-confirm-action="salvage_download:`+salvageName+`"`,
		`action="/servers/1/salvage/`+salvageName+`/delete"`, `data-confirm-action="salvage_delete:`+salvageName+`"`, "6 hari")

	// Delete needs the token; with it the file goes.
	wantCode(t, e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", noForm, c), http.StatusForbidden)
	form := withToken(t, e, noForm, salvageAct(web.ActSalvageDelete, salvageName), "server", 1)
	rec = e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "leftover 1", "salvage 1", "leftover 1", "salvage-delete 1 "+salvageName)
}

// salvageAct is the confirmation action of act for salvage file name.
func salvageAct(act, name string) string { return act + ":" + name }

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

	// A bad name is refused before the token and the agent (no token can
	// be issued for it).
	for _, name := range []string{"..sql", "a%2F..%2Fb.sql", "20261105T094500Z-1.sh"} {
		rec := e.do("GET", downloadURL(name, "x"), nil, c)
		wantCode(t, rec, http.StatusBadRequest)
	}
	if f.reads != 0 {
		t.Fatalf("%d reads for bad names", f.reads)
	}

	tok := confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
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
	tok := confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
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
		proto.ConnectionRow{Host: "10.0.0.51", User: "laporan", DB: "simrs", Count: 1},
		proto.ConnectionRow{Host: "10.0.0.52", User: "root", DB: "simrs", Count: 2, AdminAccount: true})

	rec := e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, `href="/masters/2/replicas/new?bangun-ulang=1"`)

	// The admin account's sessions from another host count, and are said
	// (SEC-67).
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Bangun ulang db-master sebagai replika", "6 koneksi aplikasi", "2 di antaranya sesi akun admin panel", "belum diperiksa",
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

	// The old master cannot be read: the rebuild needs the acknowledgement.
	rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm("accept_connections", "1"), web.ActRebuildOldMaster, "server", 1), c)
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

	// Any other failure is the panel's fixed sentence, on the page and in
	// the audit; the error's text is for the log only (SEC-72).
	if err := e.st.SetReplicaState(3, store.ReplicaNeedsRepoint, ""); err != nil {
		t.Fatal(err)
	}
	f.repointErr = errors.New("mysql.repoint: dial tcp 10.9.9.9:3306: sandi-bocor")
	rec = e.do("POST", "/replicas/3/repoint", noForm, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Replika tidak bisa diarahkan ke master baru sekarang; rinciannya ada di log panel.")
	wantNoBody(t, rec, "sandi-bocor")
	entries, err := e.st.ListAudit(0, time.Time{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "replica_repoint" || strings.Contains(entries[0].Outcome, "sandi-bocor") ||
		!strings.Contains(entries[0].Outcome, "rinciannya ada di log panel") {
		t.Errorf("audit of a failed repoint = %+v", entries)
	}
}

// Deleting an old master after the cutover warns, in the modal and in the
// confirmation, about what the panel can no longer reach once the server
// is gone: a leftover check never run, transactions found, salvage files.
func TestDeleteOldMasterWarnsOnLeftover(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	const confirm = `data-confirm-text="Hapus server db-master dari panel. `

	// Never checked.
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `data-open-modal="modal-hapus"`, "Perhatikan sebelum menghapus",
		"<li>Data tertinggal di db-master belum diperiksa", confirm+"Data tertinggal di db-master belum diperiksa")

	// Nothing left over and no files: only the sentence about the start
	// lock (TestDeleteQuarantinedDialogSaysLockStays) remains.
	setLeftover(t, e, topo, proto.LeftoverResult{})
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, confirm+"Bila MySQL server ini sedang mati")
	wantNoBody(t, rec, "belum diperiksa", "yang tidak sampai ke master baru", "berkas data tertinggal di db-master")

	// Three transactions found and two salvage files never downloaded.
	setLeftover(t, e, topo, threeLeftover())
	exp := time.Now().Add(6 * 24 * time.Hour)
	f.salvageFiles = []proto.SalvageFile{{Name: salvageName, Size: 10, CreatedAt: time.Now(), ExpiresAt: exp},
		{Name: "20261105T100000Z-2.sql", Size: 10, CreatedAt: time.Now(), ExpiresAt: exp}}
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "<li>Pemeriksaan terakhir menemukan 3 transaksi di db-master",
		confirm+"Pemeriksaan terakhir menemukan 3 transaksi di db-master",
		"Ada 2 berkas data tertinggal di db-master yang tidak bisa diunduh lewat panel lagi")

	// The salvage list cannot be read: said too.
	f.salvageFiles = nil
	f.err = errors.New("agent gagal")
	rec = e.do("GET", "/servers/1", nil, c)
	f.err = nil
	wantBody(t, rec, "Daftar berkas data tertinggal di db-master tidak bisa dibaca sekarang")
}

// A salvage token is issued for one file: it does not open another one,
// and /confirm issues none for an invalid name or without a name.
func TestSalvageTokenBoundToFile(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	f.chunks = [][]byte{[]byte("-- isi\n")}
	const other = "20261105T100000Z-2.sql"

	tok := confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
	wantCode(t, e.do("GET", downloadURL(other, tok), nil, c), http.StatusForbidden)
	form := withToken(t, e, noForm, salvageAct(web.ActSalvageDelete, salvageName), "server", 1)
	wantCode(t, e.do("POST", "/servers/1/salvage/"+other+"/delete", form, c), http.StatusForbidden)
	// A download token does not delete, even the same file.
	form = withToken(t, e, noForm, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
	wantCode(t, e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", form, c), http.StatusForbidden)
	wantCalls(t, f)
	if f.reads != 0 {
		t.Fatalf("%d reads with a token for another file", f.reads)
	}

	for _, action := range []string{web.ActSalvageDownload, web.ActSalvageDelete,
		web.ActSalvageDownload + ":", web.ActSalvageDownload + ":../etc/passwd", web.ActSalvageDelete + ":x.sql",
		web.ActSalvageDownload + ":" + salvageName + "/..", web.ActResync + ":" + salvageName} {
		if code, _ := e.postConfirm(action, "server", 1, testPassword, c); code != http.StatusBadRequest {
			t.Errorf("POST /confirm %q: status %d, want 400", action, code)
		}
	}
	// The other actions are unchanged.
	confirmToken(t, e, web.ActDeleteServer, "server", 1)
}

// An agent's error text (paths, mysqlbinlog output) never reaches the
// page or the audit; it is in the panel's log.
func TestSalvageAgentErrorTextNotShown(t *testing.T) {
	logs := &syncBuffer{}
	e, f, _ := promotedEnv(t, func(d *web.Deps) {
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	c := e.cookie()
	const secret = "/var/lib/mysql/binlog.000007"
	agentErr := errors.New("mysqlbinlog: " + secret + ": Permission denied")

	f.setConnErr(1, agentErr)
	rec := e.do("GET", "/masters/2/cutover", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Koneksi tidak bisa dibaca; rinciannya ada di log panel.")
	wantNoBody(t, rec, secret)

	f.err = agentErr
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Daftar berkas data tertinggal tidak bisa dibaca; rinciannya ada di log panel.")
	wantNoBody(t, rec, secret)

	tok := confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
	rec = e.do("GET", downloadURL(salvageName, tok), nil, c)
	wantCode(t, rec, http.StatusBadGateway)
	wantBody(t, rec, "Berkas data tertinggal tidak bisa dibaca; rinciannya ada di log panel.")
	wantNoBody(t, rec, secret)

	form := withToken(t, e, noForm, salvageAct(web.ActSalvageDelete, salvageName), "server", 1)
	rec = e.do("POST", "/servers/1/salvage/"+salvageName+"/delete", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Berkas data tertinggal tidak bisa dihapus; rinciannya ada di log panel.")
	wantNoBody(t, rec, secret)

	// A download that fails after its first chunk.
	f.err = nil
	f.chunks = [][]byte{[]byte("-- satu\n"), []byte("-- dua\n")}
	f.onRead = func(offset int64) {
		if offset > 0 {
			f.mu.Lock()
			f.err = agentErr
			f.mu.Unlock()
		}
	}
	tok = confirmToken(t, e, salvageAct(web.ActSalvageDownload, salvageName), "server", 1)
	e.do("GET", downloadURL(salvageName, tok), nil, c)

	entries, err := e.st.ListAudit(0, time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range entries {
		if strings.Contains(a.Outcome, secret) || strings.Contains(a.DetailJSON, secret) {
			t.Errorf("agent text in the audit: %+v", a)
		}
		if a.Action == "salvage_download" || a.Action == "salvage_delete" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d salvage audit entries, want 3", n)
	}
	if !strings.Contains(logs.String(), secret) {
		t.Fatal("the agent's text is not in the panel's log")
	}
}

// TestRebuiltOldMasterKeepsSalvageFiles: an old master rebuilt as a
// replica has no old-master box, but its salvage files, while there are
// any, are still listed with Unduh and Hapus.
func TestRebuiltOldMasterKeepsSalvageFiles(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerRole(1, store.RoleReplica); err != nil {
		t.Fatal(err)
	}
	f.salvageFiles = []proto.SalvageFile{{Name: salvageName, Size: 4096, CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * 24 * time.Hour)}}
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Berkas data tertinggal", salvageName, `action="/servers/1/salvage/`+salvageName+`"`,
		`action="/servers/1/salvage/`+salvageName+`/delete"`, "Unduh", "Hapus")
	wantNoBody(t, rec, "Periksa data tertinggal", `action="/servers/1/salvage"`)

	f.salvageFiles = nil
	rec = e.do("GET", "/servers/1", nil, c)
	wantNoBody(t, rec, "Berkas data tertinggal")
}

func (f *fakeOrch) callsOf(m map[int64]int, id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[id]
}

// TestConnectionsFragmentOnlyTopologyMembers (4b Task 10 Minor 4): the
// connections fragment asks an agent only for a topology's master, an old
// master, or a replica of a topology that has had a promotion (a rebuilt
// old master); any other server id is refused without calling its agent.
func TestConnectionsFragmentOnlyTopologyMembers(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	// Another topology, never promoted, and a server with no role.
	m2 := e.pairedOnline(t, "db-master-2")
	topo2 := e.makeMaster(t, m2, shopDatabases...)
	r2 := e.pairedOnline(t, "db-replika-2")
	if _, err := e.st.AddReplica(topo2.ID, r2.ID, nil); err != nil {
		t.Fatal(err)
	}
	loose := e.pairedOnline(t, "db-lepas")

	for _, id := range []int64{1, 2, 3, m2.ID} {
		rec := e.do("GET", fmt.Sprintf("/servers/%d/connections-fragment", id), nil, c)
		wantCode(t, rec, http.StatusOK)
		if n := f.callsOf(f.connCalls, id); n != 1 {
			t.Errorf("server %d: %d Connections calls, want 1", id, n)
		}
	}
	for _, id := range []int64{r2.ID, loose.ID} {
		rec := e.do("GET", fmt.Sprintf("/servers/%d/connections-fragment", id), nil, c)
		wantCode(t, rec, http.StatusNotFound)
		wantBody(t, rec, "Koneksi hanya ditampilkan untuk master, master lama, atau replika topologi yang pernah dipromosikan.")
		if n := f.callsOf(f.connCalls, id); n != 0 {
			t.Errorf("server %d: %d Connections calls, want none", id, n)
		}
	}
}

// TestSalvageListSkippedForOldAgent (4b Task 10 Minor 5): the old master's
// page does not ask an agent of another version than the panel for its
// salvage files (each view would raise AgentNeedsUpgrade again); it says
// the agent needs updating instead.
func TestSalvageListSkippedForOldAgent(t *testing.T) {
	e, f, _ := promotedEnv(t)
	c := e.cookie()
	if err := e.st.UpdateProbe(1, proto.ProbeResult{}, "v-lama", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.salvageFiles = []proto.SalvageFile{{Name: salvageName, Size: 4096, CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(6 * 24 * time.Hour)}}
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Agent perlu dimutakhirkan", "Master lama")
	wantNoBody(t, rec, salvageName)
	if n := f.callsOf(f.listCalls, 1); n != 0 {
		t.Fatalf("%d SalvageList calls for an agent of another version, want none", n)
	}

	// Of the panel's version: listed.
	if err := e.st.UpdateProbe(1, proto.ProbeResult{}, "v-test", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, salvageName)
	if n := f.callsOf(f.listCalls, 1); n != 1 {
		t.Fatalf("%d SalvageList calls, want 1", n)
	}
}

// TestOldMasterCardTone: the old master's box is a card with a warning
// edge (design spec 5.6), Bangun ulang is a neutral button and Hapus the
// pink one, which opens the danger variant of the confirmation.
func TestOldMasterCardTone(t *testing.T) {
	e, _, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	body := e.do("GET", "/servers/1", nil, c).Body.String()
	head := regexp.MustCompile(`<[a-z]+ class="([^"]*)"[^>]*data-old-master`).FindStringSubmatch(body)
	if head == nil || !strings.Contains(head[1], "card") || !strings.Contains(head[1], "border-warning") {
		t.Fatalf("old master box is not a warning card: %v", head)
	}
	card := between(body, "data-old-master", "data-old-master-end")
	rebuild := regexp.MustCompile(`<a class="([^"]*)" href="/masters/2/replicas/new\?bangun-ulang=1">`).FindStringSubmatch(card)
	if rebuild == nil || strings.Contains(rebuild[1], "btn-warning") || strings.Contains(rebuild[1], "btn-error") ||
		strings.Contains(rebuild[1], "btn-primary") {
		t.Errorf("Bangun ulang is not neutral: %v", rebuild)
	}
	del := regexp.MustCompile(`<button type="button" class="([^"]*)" data-open-modal="modal-hapus">`).FindStringSubmatch(card)
	if del == nil || !strings.Contains(del[1], "btn-error") || strings.Contains(del[1], "btn-outline") {
		t.Errorf("Hapus in the old master card is not btn-error: %v\n%s", del, card)
	}
	if n := strings.Count(body, `data-open-modal="modal-hapus"`); n != 1 {
		t.Errorf("Hapus triggers = %d, want 1", n)
	}
	if !regexp.MustCompile(`data-confirm-action="delete_server"[^>]*data-confirm-danger="1"`).MatchString(body) {
		t.Error("Ya, hapus does not open the danger confirmation")
	}
	// An old master is not offered to be prepared as a master again.
	if strings.Contains(body, `action="/servers/1/prepare-master"`) {
		t.Error("quarantined old master offers Siapkan sebagai master")
	}
}

// TestPrepareQuarantinedRefused: a crafted Siapkan sebagai master on a
// quarantined old master is refused in words and starts nothing.
func TestPrepareQuarantinedRefused(t *testing.T) {
	e, f, _ := promotedEnv(t)
	rec := e.do("POST", "/servers/1/prepare-master", noForm, e.cookie())
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "server ini master lama yang dikarantina; bangun ulang sebagai replika atau hapus")
	wantCalls(t, f)
}

// TestExplainBannerTone: the Penyebab/Saran box is pink only for a
// critical status; an old master in quarantine (expected) is not.
func TestExplainBannerTone(t *testing.T) {
	mon := &fakeMonitor{}
	e, _, _ := promotedEnv(t, func(d *web.Deps) { d.Monitor = mon })
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleQuarantined, ReceivedAt: time.Now(),
		Status: monitor.DeriveQuarantined(false)})
	body := e.do("GET", "/servers/1", nil, e.cookie()).Body.String()
	if !strings.Contains(body, "data-explain") {
		t.Fatalf("old master page has no explanation:\n%s", body)
	}
	if strings.Contains(body, "banner-error") {
		t.Errorf("quarantined old master shows a pink banner:\n%s", between(body, "data-old-master", "data-old-master-end"))
	}

	// A replica stopped on error 1062 is critical: still pink.
	mon.setView(stoppedOn1062(3))
	body = e.do("GET", "/replicas/3", nil, e.cookie()).Body.String()
	if !regexp.MustCompile(`class="banner banner-error[^"]*" data-explain`).MatchString(body) {
		t.Errorf("stopped replica's explanation is not pink:\n%s", body)
	}
}

// The delete dialog of a quarantined server says that, with MySQL down,
// the quarantine and its start lock stay on the server after the panel
// forgets it.
func TestDeleteQuarantinedDialogSaysLockStays(t *testing.T) {
	e, _, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	const sentence = "Bila MySQL server ini sedang mati, karantina dan kunci start-nya tetap ada di server itu setelah dihapus " +
		"dari panel; MySQL-nya akan menyala hanya-baca sampai seseorang mengubah perannya di server itu."
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "<li>"+sentence, `data-confirm-text="Hapus server db-master dari panel.`, sentence)

	// A server that is no longer a quarantined old master has no lock to
	// speak of: not said on its page.
	rec = e.do("GET", "/servers/2", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantNoBody(t, rec, "kunci start-nya tetap ada")
}

func TestOldMasterPageShowsStartLockAndOperatorInitFiles(t *testing.T) {
	mon := &fakeMonitor{}
	e, _, _ := promotedEnv(t, func(d *web.Deps) { d.Monitor = mon })
	c := e.cookie()
	view := func(sl *proto.StartLockState) {
		mon.setView(monitor.View{ServerID: 1, Role: store.RoleQuarantined, ReceivedAt: time.Now(),
			Status: monitor.DeriveQuarantined(false), Snap: &proto.HealthSnapshot{MySQLOK: true, StartLock: sl}})
	}

	view(&proto.StartLockState{Present: true, Locked: true})
	rec := e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "Detail teknis", "Kunci start: Terkunci")
	wantNoBody(t, rec, "belum terjamin", "tidak dijalankan selama karantina")

	// Not guaranteed: the agent's reason as given (any text, escaped), and
	// the operator's own init_file options.
	view(&proto.StartLockState{Present: true, Locked: false, Reason: "alasan <b>bebas</b> dari agent",
		OperatorInitFiles: []string{"/etc/mysql/conf.d/ops<init>.sql", "/srv/mysql/init2.sql"}})
	rec = e.do("GET", "/servers/1", nil, c)
	wantBody(t, rec, "Kunci start: belum terjamin: alasan &lt;b&gt;bebas&lt;/b&gt; dari agent",
		"init_file milik operator berikut tidak dijalankan selama karantina:",
		"/etc/mysql/conf.d/ops&lt;init&gt;.sql", "/srv/mysql/init2.sql")
	wantNoBody(t, rec, "<b>bebas</b>", "ops<init>", "Kunci start: Terkunci")

	// An agent that reports no start lock: no row.
	view(nil)
	rec = e.do("GET", "/servers/1", nil, c)
	wantNoBody(t, rec, "Kunci start:")
}

// TestRebuildRefusedWithoutAcceptConnections: the rebuild form of an old
// master with connections carries the acknowledgement; a post with token
// and typed name but without it is refused by the server (spec 5.5).
func TestRebuildRefusedWithoutAcceptConnections(t *testing.T) {
	e, f, topo := promotedEnv(t)
	c := e.cookie()
	if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
		t.Fatal(err)
	}
	f.setConnections(1, proto.ConnectionRow{Host: "10.0.0.50", User: "app", DB: "simrs", Count: 3, Kind: "sql"})
	setLeftover(t, e, topo, threeLeftover())

	rec := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="accept_connections"`, "Saya paham 3 koneksi masih ada dan tulisannya hilang")
	wantNoBody(t, rec, "tidak bisa dipastikan dan tulisan yang masih masuk hilang")

	post := func(extra ...string) *httptest.ResponseRecorder {
		return e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm(extra...), web.ActRebuildOldMaster, "server", 1), c)
	}
	// Token and name are right; the tick is missing, or not the form's.
	for _, extra := range [][]string{nil, {"accept_connections", ""}, {"accept_connections", "0"}} {
		rec = post(extra...)
		wantCode(t, rec, http.StatusBadRequest)
		wantBody(t, rec, "Centang pernyataan")
	}
	wantCalls(t, f)
	// Token and name still come first: a wrong name is the name's error.
	rec = e.do("POST", "/masters/2/replicas",
		withToken(t, e, rebuildForm("confirm_name", "salah", "accept_connections", "1"), web.ActRebuildOldMaster, "server", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantCalls(t, f)
	wantCode(t, e.do("POST", "/masters/2/replicas", rebuildForm("accept_connections", "1"), c), http.StatusForbidden)

	rec = post("accept_connections", "1")
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "create-replica 2->1")
	if p := f.replicas[0]; !p.RebuildOldMaster || !p.AcceptConnections {
		t.Fatalf("create replica params = %+v", p)
	}
}

// TestRebuildAcceptanceByConnectionState: whether the form and the server
// ask for the acknowledgement, for every state of the connection list.
func TestRebuildAcceptanceByConnectionState(t *testing.T) {
	const (
		countLabel   = "Saya paham 3 koneksi masih ada dan tulisannya hilang"
		unknownLabel = "Saya paham koneksi ke server ini tidak bisa dipastikan dan tulisan yang masih masuk hilang"
	)
	app := proto.ConnectionRow{Host: "10.0.0.50", User: "app", DB: "simrs", Count: 3, Kind: "sql"}
	local := proto.ConnectionRow{Host: "localhost", User: "dbpanel_admin", Count: 1, Kind: "sql", LocalAdmin: true}
	cases := []struct {
		name  string
		res   *proto.ConnectionsResult // nil when the command fails with err
		err   error
		label string // "" = no acknowledgement asked
	}{
		{name: "complete and empty", res: &proto.ConnectionsResult{Typed: true}},
		{name: "rows", res: &proto.ConnectionsResult{Typed: true, Rows: []proto.ConnectionRow{app}}, label: countLabel},
		{name: "only local admin sessions", res: &proto.ConnectionsResult{Typed: true, Rows: []proto.ConnectionRow{local}, LocalAdmin: 1},
			label: "Saya paham 1 koneksi masih ada dan tulisannya hilang"},
		{name: "old agent", res: &proto.ConnectionsResult{}, label: unknownLabel},
		{name: "incomplete", res: &proto.ConnectionsResult{Typed: true, Incomplete: "thread hilang"}, label: unknownLabel},
		{name: "unreadable", res: &proto.ConnectionsResult{Typed: true, Unreadable: "gagal"}, label: unknownLabel},
		{name: "offline", err: proto.ErrOffline, label: unknownLabel},
		{name: "command error", err: errors.New("login admin gagal"), label: unknownLabel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f, topo := promotedEnv(t)
			c := e.cookie()
			if err := e.st.SetCutoverPending(topo.ID, false); err != nil {
				t.Fatal(err)
			}
			setLeftover(t, e, topo, threeLeftover())
			if tc.err != nil {
				f.setConnErr(1, tc.err)
			} else {
				f.setConnectionsResult(1, *tc.res)
			}
			rec := e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
			wantCode(t, rec, http.StatusOK)
			if tc.label == "" {
				wantNoBody(t, rec, `name="accept_connections"`, "Saya paham")
			} else {
				wantBody(t, rec, `name="accept_connections"`, tc.label)
			}
			rec = e.do("POST", "/masters/2/replicas", withToken(t, e, rebuildForm(), web.ActRebuildOldMaster, "server", 1), c)
			if tc.label == "" {
				wantCode(t, rec, http.StatusSeeOther)
				return
			}
			wantCode(t, rec, http.StatusBadRequest)
			wantCalls(t, f)
		})
	}
}
