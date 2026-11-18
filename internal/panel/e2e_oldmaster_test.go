package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	agentmon "github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Operator texts the old-master tests look for (spec 5.8), copied from
// the code.
const (
	// T1: master_start_late for a relock while running.
	textT1 = "MySQL master lama sempat tidak terkunci selagi berjalan; agent sudah menguncinya lagi. " +
		"Tulisan yang masuk selama itu tidak ada di master baru: jalankan Periksa data tertinggal"
	// T20: the alert kind's label.
	textT20 = "Master lama sempat bisa ditulis"
	// T22: the old master box while its rebuild runs or waits.
	textT22 = "Bangun ulang sebagai replika sedang berjalan."
	// T9: the approval of the drop list of an old master's rebuild.
	textT9 = "database berikut di master lama ini akan dihapus lalu disalin ulang dari master baru: %s"
	// T23: the replica page of a quarantined server.
	textT23 = "Server ini master lama yang dikarantina. Tindakannya ada di halaman server."
	// The reason "Periksa data tertinggal" closes master_start_late with.
	leftoverClosed = "Periksa data tertinggal sudah dijalankan"
)

// promotePlannedTo runs a planned promotion of the master to candidate over
// the UI, checks that the old master is quarantined, and presses "Sudah
// saya alihkan" on the new master.
func (e *e2ePromoteTopology) promotePlannedTo(t *testing.T, candidate *e2eNode) {
	t.Helper()
	e.waitReplicasSeen(t)
	id, _ := e.startPromote(t, candidate, proto.PromotePlanned, nil)
	if job := waitJob(t, e.p, id, e2ePromoteTimeout, nil); job.Kind != orchestrator.KindPromotePlanned || job.Status != store.JobSucceeded {
		t.Fatalf("promote job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, e.p, id))
	}
	e.wantQuarantined(t, e.master)
	e.confirmCutover(t, candidate)
}

// waitNoBinlogReaders waits until no replica reads old's binary log, as
// rebuildOldMaster does: the old replicas' dump threads end at their next
// heartbeat after a planned promote.
func waitNoBinlogReaders(t *testing.T, old *e2eNode) {
	t.Helper()
	waitForWithin(t, 2*time.Minute, "no replica reading the old master's binlog", func() bool {
		var n int
		return old.db.QueryRow("SELECT COUNT(*) FROM performance_schema.threads WHERE PROCESSLIST_COMMAND LIKE 'Binlog Dump%'").
			Scan(&n) == nil && n == 0
	})
}

// startRebuild approves the rebuild of old from newMaster's form (root's
// approve on old) and posts it; it returns the job's id.
func (e *e2ePromoteTopology) startRebuild(t *testing.T, newMaster, old *e2eNode) string {
	t.Helper()
	waitNoBinlogReaders(t, old)
	e.approveRebuild(t, newMaster, old)
	_, vals := e.replicaForm(t, newMaster, old, true)
	return e.ui.postJob(fmt.Sprintf("/masters/%d/replicas", newMaster.srv()), vals)
}

// writeByHand lifts super_read_only on n as root and runs stmt, as someone
// on the server might; the quarantine guard locks it again within a round.
// If the guard locked it between the two statements (error 1290), it tries
// again.
func writeByHand(t *testing.T, n *e2eNode, stmt string) {
	t.Helper()
	ctx := context.Background()
	for try := 1; ; try++ {
		conn, err := n.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(ctx, "SET GLOBAL super_read_only = OFF")
		if err == nil {
			_, err = conn.ExecContext(ctx, stmt)
		}
		conn.Close()
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "1290") || try == 5 {
			t.Fatalf("%s on %s with read-only lifted: %v", stmt, n.name, err)
		}
	}
}

// replicaLink is the link of a replica card or row to the replica page of
// srv.
func replicaLink(srv int64) string { return `href="/replicas/` + strconv.FormatInt(srv, 10) + `"` }

// wantNoReplicaCard checks that the dashboard and the master page of
// master list no replica card of old (spec 5.1: a quarantined server shows
// in its own box). With one topology the replication page is the master
// page (/replikasi redirects there).
func (e *e2ePromoteTopology) wantNoReplicaCard(t *testing.T, master, old *e2eNode) {
	t.Helper()
	for _, path := range []string{"/", fmt.Sprintf("/masters/%d", master.srv())} {
		if page := e.ui.get(path); strings.Contains(page, replicaLink(old.srv())) {
			t.Fatalf("%s shows %s as a replica card while it is quarantined:\n%s", path, old.name, textOf(page))
		}
	}
}

// TestE2ERebuildFailsBeforeCopyThenLeftoverThenRetry (spec 5.1, 5.10):
// after a planned promote someone writes a row on the quarantined old
// master by hand (a leftover transaction). Its rebuild fails before the
// copy (the new master's agent is down at "Periksa master"). The server
// stays quarantined in the panel and in its pins; its page offers
// "Periksa data tertinggal", which runs and finds the transaction; the
// "Ulangi dari tahap ini" dialog names it; the retry runs: while it waits
// for the approval of its drop list the box shows only T22 and its job,
// and no page lists the server as a replica card; approved, the rebuild
// ends, the server is a replica and the box is gone.
func TestE2ERebuildFailsBeforeCopyThenLeftoverThenRetry(t *testing.T) {
	skipWithoutE2E(t)
	e := startE2EPromoteTopology(t)
	p, ui, m, ra, rb := e.p, e.ui, e.master, e.ra, e.rb
	start := time.Now()
	e.promotePlannedTo(t, ra)

	// The leftover: a row written on the old master with read-only lifted
	// by hand; the guard locks it again.
	writeByHand(t, m, "INSERT INTO a.t VALUES (200, 'tertinggal')")
	waitVar(t, m.db, "super_read_only", "ON")

	// The rebuild fails before the copy: the new master's agent is down
	// when the job checks the master.
	ra.agent.stop(t)
	id := e.startRebuild(t, ra, m)
	job := waitJob(t, p, id, e2eCreateTimeout, nil)
	if job.Status != store.JobFailed {
		t.Fatalf("rebuild with the new master's agent down ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, id))
	}
	_, steps, err := p.Store.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if failed := steps[job.CurrentStep].Name; failed != "Periksa master" || !strings.Contains(job.Error, "terputus") {
		t.Fatalf("rebuild failed at %q with %q; want Periksa master, agent terputus", failed, job.Error)
	}
	t.Logf("rebuild failed before the copy at %q: %s", steps[job.CurrentStep].Name, job.Error)
	ra.agent.start(t)

	// Failed before the settle: quarantined in the panel and on the agent,
	// a failed replica row, the old master box with Periksa.
	if srv, err := p.Store.GetServer(m.srv()); err != nil || srv.Role != store.RoleQuarantined {
		t.Fatalf("old master after the failed rebuild = %+v, %v; want quarantined", srv, err)
	}
	if rep, err := p.Store.GetReplicaByServer(m.srv()); err != nil || rep.State != store.ReplicaFailed {
		t.Fatalf("replica row of the old master after the failed rebuild = %+v, %v; want failed", rep, err)
	}
	if role := readPin(t, m.agent.dir, mysql.RoleFile); role != "quarantined" {
		t.Fatalf("old master's role pin after the failed rebuild = %q", role)
	}
	page := ui.get(serverPath(m.srv()))
	for _, want := range []string{"data-old-master", `action="/servers/` + strconv.FormatInt(m.srv(), 10) + `/leftover"`,
		"Bangun ulang sebagai replika"} {
		if !strings.Contains(page, want) {
			t.Fatalf("old master's page after the failed rebuild lacks %s:\n%s", want, textOf(page))
		}
	}
	if strings.Contains(page, textT22) {
		t.Fatalf("old master's page says %q after the rebuild failed", textT22)
	}
	e.wantNoReplicaCard(t, ra, m)

	// Periksa data tertinggal runs and finds the transaction.
	c, srvPage := e.leftoverCheck(t, m)
	if c.Transactions != 1 || len(c.Tables) != 1 || c.Tables[0].Schema != "a" || c.Tables[0].Table != "t" {
		t.Fatalf("leftover after the failed rebuild = %+v; want 1 transaction on a.t", c)
	}
	if want := "Ada 1 transaksi yang tidak sampai ke master baru, menyentuh a.t (1 baris)"; !strings.Contains(srvPage, want) {
		t.Fatalf("old master's page lacks %q:\n%s", want, textOf(srvPage))
	}
	// The check is later than the relock's alert, which it closes; the
	// dialog then names what the check found.
	waitForWithin(t, e2eWait, "master_start_late closed by the check", func() bool {
		_, open := openAlert(t, p, healthmon.KindMasterStartLate, m.srv())
		return !open
	})

	// "Ulangi dari tahap ini" is still offered, and its dialog names the
	// transaction the check found.
	jobPath := "/jobs/" + id
	if loc := ui.postRedirect(jobPath+"/retry/prepare", nil); loc != jobPath+"?buka=ulangi" {
		t.Fatalf("retry/prepare redirected to %q", loc)
	}
	dialog := textOf(ui.get(jobPath + "?buka=ulangi"))
	const found = "Pemeriksaan terakhir menemukan 1 transaksi yang tidak sampai ke master baru; data itu akan hilang dari server ini."
	if !strings.Contains(dialog, "Ya, ulangi") || !strings.Contains(dialog, found) {
		t.Fatalf("retry dialog of the failed rebuild lacks %q:\n%s", found, dialog)
	}

	// Ulangi: the rebuild runs again from the failed step. It waits for the
	// approval of its drop list; meanwhile the old master's box shows T22
	// and the job, and no page lists it as a replica card.
	tok := ui.confirmToken(web.ActJobRetry+":"+id, "job", m.srv())
	if loc := ui.postRedirect(jobPath+"/retry", url.Values{"confirm_token": {tok}}); !strings.HasPrefix(loc, jobPath) {
		t.Fatalf("retry redirected to %q", loc)
	}
	sawWait := false
	job = waitJob(t, p, id, e2eCreateTimeout, func(j store.Job) bool {
		if !strings.Contains(j.ParamsJSON, `"wait_reason":"`+orchestrator.WaitWipe+`"`) {
			t.Fatalf("retried rebuild waits for something else than its drop list: %s", j.ParamsJSON)
		}
		srvPage := ui.get(serverPath(m.srv()))
		if !strings.Contains(srvPage, textT22) || !strings.Contains(srvPage, `href="/jobs/`+id+`"`) {
			t.Fatalf("old master's page while its rebuild waits lacks %q and the job link:\n%s", textT22, textOf(srvPage))
		}
		if strings.Contains(srvPage, `action="/servers/`+strconv.FormatInt(m.srv(), 10)+`/leftover"`) {
			t.Fatalf("old master's page offers Periksa while its rebuild waits:\n%s", textOf(srvPage))
		}
		e.wantNoReplicaCard(t, ra, m)
		jobPage := textOf(ui.get(jobPath))
		if want := fmt.Sprintf(textT9, "a"); !strings.Contains(jobPage, want) {
			t.Fatalf("job page of the waiting rebuild lacks %q:\n%s", want, jobPage)
		}
		if srv, err := p.Store.GetServer(m.srv()); err != nil || srv.Role != store.RoleQuarantined {
			t.Fatalf("old master while its rebuild waits = %+v, %v; want quarantined", srv, err)
		}
		// Setujui, with its dialog's leftover warning (spec 5.1: the
		// rebuild that waits takes Setujui; Periksa is no newer job).
		form := url.Values{"confirm_token": {ui.confirmToken(web.ActJobApprove+":"+id, "job", m.srv())}}
		if status, body := ui.post(jobPath+"/approve", form); status != http.StatusSeeOther {
			t.Fatalf("Setujui of the retried rebuild that waits = %d:\n%s", status, textOf(body))
		}
		if !strings.Contains(jobPage, found) {
			t.Fatalf("the Setujui dialog of the waiting rebuild lacks %q:\n%s", found, jobPage)
		}
		sawWait = true
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("retried rebuild ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, id))
	}
	if !sawWait {
		t.Fatal("the retried rebuild never waited for the approval of its drop list")
	}

	// A replica now: role, pin, row; the box is gone; it follows the new
	// master.
	if srv, err := p.Store.GetServer(m.srv()); err != nil || srv.Role != store.RoleReplica {
		t.Fatalf("old master after the retried rebuild = %+v, %v; want a replica", srv, err)
	}
	if rep, err := p.Store.GetReplicaByServer(m.srv()); err != nil || rep.State != store.ReplicaReady {
		t.Fatalf("replica row after the retried rebuild = %+v, %v; want ready", rep, err)
	}
	if role := readPin(t, m.agent.dir, mysql.RoleFile); role != "replica" {
		t.Fatalf("role pin after the retried rebuild = %q", role)
	}
	if page := ui.get(serverPath(m.srv())); strings.Contains(page, "data-old-master") {
		t.Fatalf("server page after the retried rebuild still shows the old master box:\n%s", textOf(page))
	}
	execSQL(t, ra.db, "INSERT INTO a.t VALUES (300, 'setelah-ulangi')")
	waitRow(t, m.db, "a", 300, "setelah-ulangi")
	waitRow(t, rb.db, "a", 300, "setelah-ulangi")
	t.Logf("failed-before-copy flow took %s after the topology was ready", time.Since(start).Round(time.Second))
}

// conflictDelay is how long the conflicting statement sleeps on the target.
const conflictDelay = 5 * time.Second

// conflictWriter runs, on the new master, until stop is called, a
// statement-logged INSERT of a row that exists already, which only the
// server listening on port applies (@@port is read where the statement
// runs): every one that the target receives after its copy stops its
// applier with a duplicate key (1062); the new master and the other
// replica insert nothing. On the target the statement first sleeps
// conflictDelay, so the applier is still running when "Sambungkan
// replikasi" checks that replication started, and the error comes
// during "Verifikasi" (whose START REPLICA runs it again).
func conflictWriter(t *testing.T, master *e2eNode, port int) (stop func() (int, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := master.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION binlog_format = 'STATEMENT'"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var n int
	var werr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		stmt := fmt.Sprintf("INSERT INTO a.t SELECT 1, 'konflik' FROM DUAL WHERE IF(@@port = %d, SLEEP(%d) = 0, FALSE)",
			port, int(conflictDelay/time.Second))
		for ctx.Err() == nil {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				if ctx.Err() == nil {
					werr = err
				}
				return
			}
			n++
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var once sync.Once
	stop = func() (int, error) {
		once.Do(func() {
			cancel()
			wg.Wait()
			conn.Close()
		})
		return n, werr
	}
	t.Cleanup(func() { stop() })
	return stop
}

// TestE2ERebuildFailsAfterSettle (spec 5.1, 5.10): the rebuild of the old
// master gets past "Sambungkan replikasi", where the agent ends the
// quarantine (settle), and fails at "Verifikasi": the replica's applier
// stops on a statement from the new master that conflicts only there. The
// server is then a plain failed replica: role replica in the panel and on
// the agent, no old master box, "Resync ulang" on its replica page, and
// "Ulangi dari tahap ini" on the job.
func TestE2ERebuildFailsAfterSettle(t *testing.T) {
	skipWithoutE2E(t)
	e := startE2EPromoteTopology(t)
	p, ui, m, ra := e.p, e.ui, e.master, e.ra
	start := time.Now()
	e.promotePlannedTo(t, ra)

	stop := conflictWriter(t, ra, m.inst.Port)
	id := e.startRebuild(t, ra, m)
	job := waitJob(t, p, id, e2eCreateTimeout, func(j store.Job) bool {
		ui.approveJob(j)
		return true
	})
	n, werr := stop()
	if werr != nil {
		t.Fatalf("conflicting writer on the new master: %v", werr)
	}
	t.Logf("%d conflicting statements written on the new master during the rebuild", n)
	_, steps, err := p.Store.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobFailed || steps[job.CurrentStep].Name != "Verifikasi" {
		t.Fatalf("rebuild ended %s at step %d: %s; want failed at Verifikasi\n%s", job.Status, job.CurrentStep, job.Error,
			jobLogText(t, p, id))
	}
	t.Logf("rebuild failed after the settle at %q: %s", steps[job.CurrentStep].Name, job.Error)
	if log := jobLogText(t, p, id); !strings.Contains(log, "Karantina master lama berakhir: agent kini menyimpan peran replika.") {
		t.Fatalf("the job log does not show the settle:\n%s", log)
	}

	// A plain failed replica.
	if srv, err := p.Store.GetServer(m.srv()); err != nil || srv.Role != store.RoleReplica {
		t.Fatalf("server after the rebuild failed at Verifikasi = %+v, %v; want a replica", srv, err)
	}
	if rep, err := p.Store.GetReplicaByServer(m.srv()); err != nil || rep.State != store.ReplicaFailed {
		t.Fatalf("replica row after the rebuild failed at Verifikasi = %+v, %v; want failed", rep, err)
	}
	if role := readPin(t, m.agent.dir, mysql.RoleFile); role != "replica" {
		t.Fatalf("role pin after the settle = %q, want replica", role)
	}
	if _, err := p.Store.GetTopologyByOldMaster(m.srv()); err == nil {
		t.Fatal("the topology still names the server as its old master after the settle")
	}
	if page := ui.get(serverPath(m.srv())); strings.Contains(page, "data-old-master") || strings.Contains(page, textT22) {
		t.Fatalf("server page of the failed replica shows the old master box:\n%s", textOf(page))
	}
	repPage := ui.get("/replicas/" + strconv.FormatInt(m.srv(), 10))
	if !strings.Contains(repPage, `action="/replicas/`+strconv.FormatInt(m.srv(), 10)+`/resync/prepare"`) ||
		strings.Contains(repPage, textT23) {
		t.Fatalf("replica page of the failed replica lacks Resync ulang (or says %q):\n%s", textT23, textOf(repPage))
	}
	if jobPage := textOf(ui.get("/jobs/" + id)); !strings.Contains(jobPage, "Ulangi dari tahap ini") {
		t.Fatalf("job page of the rebuild failed at Verifikasi lacks Ulangi dari tahap ini:\n%s", jobPage)
	}
	t.Logf("failed-after-settle flow took %s after the topology was ready", time.Since(start).Round(time.Second))
}

// roundWork bounds the work of one round of the agent's guard loop, which
// runs again GuardEvery after the round finished.
const roundWork = 1500 * time.Millisecond

// TestE2ERelockAlert (spec 5.2, 5.10): super_read_only switched off by
// hand on the quarantined old master: within two rounds of the agent's
// guard it is on again and the panel has opened master_start_late with
// T1 (label T20 on the alerts page); "Periksa data tertinggal" closes it.
func TestE2ERelockAlert(t *testing.T) {
	skipWithoutE2E(t)
	e := startE2EPromoteTopology(t)
	p, ui, m, ra := e.p, e.ui, e.master, e.ra
	e.promotePlannedTo(t, ra)
	if _, open := openAlert(t, p, healthmon.KindMasterStartLate, m.srv()); open {
		t.Fatal("master_start_late open on the old master before anything lifted its lock")
	}

	// Two rounds of the guard: the first locks it again and records the
	// relock, the snapshot sent after the next one carries it to the
	// panel, which opens the alert from it (the event itself is left to
	// the snapshot). GuardEvery is the pause after each round, so two
	// rounds also take their own work (start lock checks, sweeps): the
	// slack is for that, not for a slow panel.
	deadline := 2 * (agentmon.GuardEvery + roundWork)
	lifted := time.Now()
	execSQL(t, m.db, "SET GLOBAL super_read_only = OFF")
	var late store.Alert
	var relocked, alerted time.Duration
	waitForWithin(t, deadline, "the guard's relock and master_start_late within two guard rounds", func() bool {
		if relocked == 0 {
			var v string
			if m.db.QueryRow(globalVarQuery, "super_read_only").Scan(&v) == nil && v == "ON" {
				relocked = time.Since(lifted)
			}
		}
		if alerted == 0 {
			var open bool
			if late, open = openAlert(t, p, healthmon.KindMasterStartLate, m.srv()); open {
				alerted = time.Since(lifted)
			}
		}
		return relocked != 0 && alerted != 0
	})
	t.Logf("super_read_only switched off: on again after %s, master_start_late open after %s (two guard rounds: %s)",
		relocked.Round(time.Millisecond), alerted.Round(time.Millisecond), deadline)
	if late.Message != textT1 {
		t.Fatalf("master_start_late message = %q, want T1 %q", late.Message, textT1)
	}
	if v := globalVar(t, m.db, "read_only"); v != "ON" {
		t.Fatalf("read_only on the old master after the relock = %s", v)
	}
	if page := textOf(ui.get("/alerts")); !strings.Contains(page, textT20) || !strings.Contains(page, textT1) {
		t.Fatalf("alerts page lacks %q with %q:\n%s", textT20, textT1, page)
	}

	// Periksa data tertinggal closes it.
	if c, _ := e.leftoverCheck(t, m); c.Transactions != 0 {
		t.Fatalf("leftover after a lift without writes = %+v; want none", c)
	}
	waitForWithin(t, e2eWait, "master_start_late closed by Periksa data tertinggal", func() bool {
		_, open := openAlert(t, p, healthmon.KindMasterStartLate, m.srv())
		return !open
	})
	alerts, err := p.Store.ListAlerts(late.OpenedAt, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range alerts {
		if a.ID == late.ID && a.CloseReason != leftoverClosed {
			t.Fatalf("master_start_late closed with %q, want %q", a.CloseReason, leftoverClosed)
		}
	}
}
