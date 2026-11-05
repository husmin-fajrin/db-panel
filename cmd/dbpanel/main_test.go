package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out, io.Discard)
	if code != 0 || !strings.Contains(out.String(), "dbpanel dev") {
		t.Fatalf("got %d %q", code, out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"apa"}, io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "Penggunaan:") {
		t.Fatalf("got %d %q", code, errb.String())
	}
}

func TestRunNoArgsPrintsUsage(t *testing.T) {
	var errb bytes.Buffer
	code := run(nil, io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "Penggunaan:") {
		t.Fatalf("got %d %q", code, errb.String())
	}
}

func TestRunAgentWithoutConfig(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"agent", "--dir", t.TempDir()}, io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "agent belum dipasangkan") {
		t.Fatalf("got %d %q", code, errb.String())
	}
}

func TestRunAgentVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"agent", "version"}, &out, io.Discard)
	if code != 0 || !strings.Contains(out.String(), "dbpanel agent dev") {
		t.Fatalf("got %d %q", code, out.String())
	}
}

func TestRunPanelBadFlag(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"panel", "--no-such-flag"}, io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "dbpanel panel reset-password") {
		t.Fatalf("got %d %q", code, errb.String())
	}
}

func TestRunPanelHelp(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"panel", "help"}, &out, io.Discard)
	if code != 0 || !strings.Contains(out.String(), "dbpanel panel [--data-dir DIR]") {
		t.Fatalf("got %d %q", code, out.String())
	}
}

// TestPanelRefusesRootWithoutFlag: as root, "dbpanel panel" stops before
// touching anything and points at install.sh (the panel runs as dbpanel).
func TestPanelRefusesRootWithoutFlag(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := t.TempDir()
	var errb bytes.Buffer
	// Unusable addresses: a run that got past the check binds nothing.
	code := run([]string{"panel", "--data-dir", dir + "/data", "--config-dir", dir + "/etc", "--http", "bad", "--agent-listen", "bad"},
		io.Discard, &errb)
	if code != 1 || !strings.Contains(errb.String(), "install.sh") {
		t.Fatalf("got %d %q", code, errb.String())
	}
	if _, err := os.Stat(dir + "/data"); !os.IsNotExist(err) {
		t.Fatalf("data dir created by a refused run: %v", err)
	}
}
