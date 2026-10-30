package main

import (
	"bytes"
	"io"
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

func TestRunPlaceholderCommands(t *testing.T) {
	for _, name := range []string{"panel"} {
		var errb bytes.Buffer
		code := run([]string{name}, io.Discard, &errb)
		want := `perintah "` + name + `" belum diimplementasikan`
		if code != 2 || !strings.Contains(errb.String(), want) {
			t.Fatalf("%s: got %d %q", name, code, errb.String())
		}
	}
}
