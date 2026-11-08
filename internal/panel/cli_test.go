package panel

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// TestMainRefusesRoot: "dbpanel panel" does not run as root without
// --allow-root; install.sh sets up the dbpanel user.
func TestMainRefusesRoot(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }
	var stderr bytes.Buffer
	// An unusable address: a run that got past the check fails at once.
	code := Main([]string{"panel", "--data-dir", t.TempDir(), "--config-dir", t.TempDir(), "--http", "bad"}, nil, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "install.sh") || !strings.Contains(stderr.String(), "--allow-root") {
		t.Fatalf("exit %d, stderr %q; want 1 and a hint at install.sh and --allow-root", code, stderr.String())
	}
}

// TestResetLocalAgent: "panel reset-local-agent" clears the local server's
// pairing (same row and id), so the next start writes a new token for the
// panel host's agent; it refuses root (panel.db must stay the panel
// user's) and a database without a local server.
func TestResetLocalAgent(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 1000 }

	o := testOptions(t)
	p1, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := p1.localID
	if err := p1.Store.MarkPaired(id, "abc123", "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	p1.Close()

	var stdout, stderr bytes.Buffer
	if code := Main([]string{"panel", "reset-local-agent", "--data-dir", o.DataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	p := newPanel(t, o)
	if p.localID != id {
		t.Fatalf("local id = %d, want %d", p.localID, id)
	}
	srv, err := p.Store.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Paired || srv.CertSerial != "" || srv.Address != "192.0.2.7" {
		t.Fatalf("local server after reset = %+v, want unpaired, address kept", srv)
	}
	readLocalPairToken(t, o) // the start wrote a new token

	geteuid = func() int { return 0 }
	stderr.Reset()
	if code := Main([]string{"panel", "reset-local-agent", "--data-dir", o.DataDir}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "sudo -u dbpanel") {
		t.Fatalf("as root: exit %d, stderr %q", code, stderr.String())
	}
	geteuid = func() int { return 1000 }
	stderr.Reset()
	if code := Main([]string{"panel", "reset-local-agent", "--data-dir", t.TempDir()}, &stdout, &stderr); code != 1 {
		t.Fatalf("no database: exit %d, stderr %q", code, stderr.String())
	}
}

// TestAuditVerifyCLI: "panel audit-verify" exits 0 with the last intact
// entry, and 1 with the first broken one after a row is changed directly
// in SQLite; it refuses root and a missing database.
func TestAuditVerifyCLI(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 1000 }

	dir := t.TempDir()
	path := filepath.Join(dir, dbFile)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"login", "server_create", "logout"} {
		if _, err := st.AddAudit(store.AuditEntry{TS: time.Now(), Username: "admin", Action: action, Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Main(append([]string{"panel", "audit-verify"}, args...), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, out, errOut := run("--data-dir", dir); code != 0 || !strings.Contains(out, "Rantai utuh sampai #3") {
		t.Fatalf("intact: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET username = 'orang-lain' WHERE id = 2`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	if code, out, errOut := run("--data-dir", dir); code != 1 || !strings.Contains(out+errOut, "Rantai putus di #2") {
		t.Fatalf("tampered: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	if code, _, errOut := run("--data-dir", t.TempDir()); code != 1 || !strings.Contains(errOut, "tidak ditemukan") {
		t.Fatalf("no database: exit %d, stderr %q", code, errOut)
	}
	if code, _, _ := run("--data-dir", dir, "extra"); code != 2 {
		t.Fatalf("positional argument: exit %d, want 2", code)
	}
	geteuid = func() int { return 0 }
	if code, _, errOut := run("--data-dir", dir); code != 1 || !strings.Contains(errOut, "sudo -u dbpanel") {
		t.Fatalf("as root: exit %d, stderr %q", code, errOut)
	}
}
