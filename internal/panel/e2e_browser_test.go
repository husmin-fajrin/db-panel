//go:build e2e_browser

package panel

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// TestE2EBrowserConfirm runs scripts/e2e_browser_confirm.js against a
// panel with a master and a replica: in Chromium, "Resync ulang" goes
// through the password confirmation dialog and posts the form with a
// confirmation token, and the resync job succeeds. Not part of go test:
//
//	go test -tags e2e_browser -run TestE2EBrowserConfirm ./internal/panel/
//
// It needs mysqld, Node with the playwright package (require("playwright"))
// and its Chromium.
func TestE2EBrowserConfirm(t *testing.T) {
	skipWithoutE2E(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	if err := exec.Command("node", "-e", `require("playwright")`).Run(); err != nil {
		t.Skip("the playwright package is not installed for node")
	}
	e := startE2ETopology(t)
	cmd := exec.Command("node", "../../scripts/e2e_browser_confirm.js")
	cmd.Env = append(noProxyEnv(),
		"BASE="+e.ui.base,
		"PASSWORD="+e.ui.password,
		"REPLICA_ID="+strconv.FormatInt(e.replicaSrv, 10),
		"REPLICA_NAME="+e.replicaName,
	)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("browser check: %v", err)
	}
	id, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "/jobs/")
	if !ok {
		t.Fatalf("browser check ended on %q, want a job page", out)
	}
	job := waitJob(t, e.p, id, e2eCreateTimeout, nil)
	if job.Kind != "resync_replica" || job.Status != store.JobSucceeded {
		t.Fatalf("job %s ended %s: %s\n%s", job.Kind, job.Status, job.Error, jobLogText(t, e.p, id))
	}
}
