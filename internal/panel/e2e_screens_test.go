//go:build e2e_browser

package panel

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	healthmon "github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// screenshotTour lists the pages TestE2EScreenshotTour shoots, in order.
// Each is written once per theme and width, as
// <nn>-<halaman>-<tema>-<lebar>.png (screenshotFiles).
var screenshotTour = []string{
	"01-login", "02-dashboard", "03-servers", "04-server-detail", "05-master-detail", "06-replica-detail",
	"07-promote", "08-job-promote", "09-cutover", "10-old-master", "11-alerts", "12-audit", "13-settings",
	"14-jobs", "15-dialog-konfirmasi", "16-replikasi",
}

// screenshotFiles is every file name the tour writes: each page in the
// dark theme (the panel's main one) and the light one, at 1440 and 390 px.
func screenshotFiles() []string {
	var files []string
	for _, name := range screenshotTour {
		for _, tema := range []string{"gelap", "terang"} {
			for _, lebar := range []string{"1440", "390"} {
				files = append(files, name+"-"+tema+"-"+lebar+".png")
			}
		}
	}
	return files
}

// TestE2EScreenshotTour is a development tool, not a product test: it
// starts a panel with a master and two replicas (three real mysqld), writes
// some data, runs a planned promote and takes full-page screenshots of the
// UI in Chromium with scripts/e2e_screenshot_tour.js, in both themes and at
// 1440 and 390 px, into the directory DBPANEL_SCREENSHOT_DIR names. At the
// end it adds a second topology (a master without agent or replicas) so
// /replikasi shows its list instead of redirecting. Skipped unless that
// variable is set (make screens sets it):
//
//	DBPANEL_SCREENSHOT_DIR=/tmp/layar go test -tags e2e_browser -timeout 20m \
//	    -run TestE2EScreenshotTour ./internal/panel/
//
// It needs mysqld, Node with the playwright package and its Chromium.
func TestE2EScreenshotTour(t *testing.T) {
	out := os.Getenv("DBPANEL_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("DBPANEL_SCREENSHOT_DIR is not set")
	}
	skipWithoutE2E(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	if err := exec.Command("node", "-e", `require("playwright")`).Run(); err != nil {
		t.Skip("the playwright package is not installed for node")
	}
	out, err := filepath.Abs(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range screenshotFiles() {
		os.Remove(filepath.Join(out, name))
	}

	e := startE2EPromoteTopology(t)
	p, ui, m, ra, rb := e.p, e.ui, e.master, e.ra, e.rb

	// Some data: a second database and a few rows, and an application
	// connected to the master over TCP for the cutover screen.
	execSQL(t, m.db,
		"CREATE DATABASE toko",
		"CREATE TABLE toko.t (id INT PRIMARY KEY, v VARCHAR(64))",
		"CREATE TABLE toko.pelanggan (id INT PRIMARY KEY, nama VARCHAR(64), kota VARCHAR(64))",
		"INSERT INTO toko.pelanggan VALUES (1, 'Budi', 'Bandung'), (2, 'Siti', 'Surabaya'), (3, 'Andi', 'Medan')",
		"INSERT INTO a.t VALUES (2, 'pesanan-1'), (3, 'pesanan-2')",
		"INSERT INTO toko.t VALUES (1, 'awal')",
		"CREATE USER app_e2e@'127.0.0.1' IDENTIFIED BY 'App-e2e-pw1'",
		"GRANT SELECT, INSERT ON a.* TO app_e2e@'127.0.0.1'")
	for _, n := range []*e2eNode{ra, rb} {
		waitRow(t, n.db, "toko", 1, "awal")
	}
	appDB, err := sql.Open("mysql", fmt.Sprintf("app_e2e:App-e2e-pw1@tcp(%s)/a?tls=preferred", m.addr()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appDB.Close() })
	appConn, err := appDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("application connection to the master: %v", err)
	}
	t.Cleanup(func() { appConn.Close() })
	if err := appConn.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*e2eNode{m, ra, rb} {
		ui.post(serverPath(n.srv())+"/mysql/refresh", nil)
	}
	// One more report from each agent, so the pages show the new data.
	time.Sleep(12 * time.Second)
	waitStatus(t, p, m.srv(), healthmon.StatusHealthy)
	for _, n := range []*e2eNode{ra, rb} {
		waitStatus(t, p, n.srv(), healthmon.StatusSynced)
	}

	ids := func(kv ...string) []string {
		env := append(noProxyEnv(), "BASE="+ui.base, "PASSWORD="+ui.password, "OUT="+out,
			"MASTER_ID="+strconv.FormatInt(m.srv(), 10),
			"REPLICA_A_ID="+strconv.FormatInt(ra.srv(), 10),
			"REPLICA_B_ID="+strconv.FormatInt(rb.srv(), 10))
		return append(env, kv...)
	}
	tour := func(phase string, env []string) {
		t.Helper()
		cmd := exec.Command("node", "../../scripts/e2e_screenshot_tour.js")
		cmd.Env = append(env, "PHASE="+phase)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("screenshot tour, phase %s: %v", phase, err)
		}
	}

	tour("sebelum", ids())

	e.waitReplicasSeen(t)
	id, _ := e.startPromote(t, ra, proto.PromotePlanned, nil)
	job := waitJob(t, p, id, e2ePromoteTimeout, nil)
	if job.Kind != orchestrator.KindPromotePlanned || job.Status != store.JobSucceeded {
		t.Fatalf("promote job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, p, id))
	}
	tour("promote", ids("JOB_ID="+id))

	e.leftoverCheck(t, m)
	tour("akhir", ids("JOB_ID="+id))

	// A second topology, only in the store: a master named db-gudang with
	// no agent and no replicas is enough for /replikasi to list both.
	gudang, err := p.Store.CreateServer("db-gudang")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.CreateTopology(gudang.ID, "dbpanel_repl", nil, "10.0.0.9", 3306); err != nil {
		t.Fatal(err)
	}
	tour("replikasi", ids())

	for _, name := range screenshotFiles() {
		fi, err := os.Stat(filepath.Join(out, name))
		if err != nil || fi.Size() == 0 {
			t.Errorf("screenshot %s: %v (size %d)", name, err, sizeOf(fi))
		}
	}
}

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}
