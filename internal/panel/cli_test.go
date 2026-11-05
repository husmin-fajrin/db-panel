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
