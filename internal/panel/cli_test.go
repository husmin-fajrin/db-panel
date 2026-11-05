package panel

import (
	"bytes"
	"strings"
	"testing"
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
