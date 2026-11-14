package mysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testDefaultsDir holds what the tests write where the agent would write
// its system files: no test touches /var/lib/dbpanel or /etc/dbpanel.
var testDefaultsDir string

func init() {
	dir, err := os.MkdirTemp("", "dbpanel-agent-test-")
	if err != nil {
		panic(err)
	}
	testDefaultsDir = dir
	SeenFile = filepath.Join(dir, "replicas-seen")
	SeenPortsFile = filepath.Join(dir, "replica-ports-seen")
	StartResultFile = filepath.Join(dir, "start-result")
	DefaultSalvageDir = filepath.Join(dir, "salvage")
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(testDefaultsDir)
	os.Exit(code)
}

// TestDefaultsAwayFromSystemPaths: as the tests see them, the files a
// Driver without explicit paths uses are in the test's own directory.
func TestDefaultsAwayFromSystemPaths(t *testing.T) {
	d := &Driver{}
	for name, p := range map[string]string{
		"SeenFile": SeenFile, "SeenPortsFile": SeenPortsFile, "StartResultFile": StartResultFile,
		"DefaultSalvageDir": DefaultSalvageDir, "seenPath": d.seenPath(), "seenPortsPath": d.seenPortsPath(),
		"startResultPath": d.startResultPath(), "salvageDir": d.salvageDir(),
	} {
		if strings.HasPrefix(p, "/var/lib") || strings.HasPrefix(p, "/etc") || !strings.HasPrefix(p, testDefaultsDir+"/") {
			t.Errorf("%s = %s, want it under %s", name, p, testDefaultsDir)
		}
	}
}
