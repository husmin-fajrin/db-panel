package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
)

// testDefaultsDir holds what the tests write where the agent would write
// its system files: no test touches /var/lib/dbpanel or /etc/dbpanel, and
// none writes to this machine's system log (a test that wants the lines
// replaces cliSyslog itself).
var testDefaultsDir string

func init() {
	dir, err := os.MkdirTemp("", "dbpanel-agent-cli-test-")
	if err != nil {
		panic(err)
	}
	testDefaultsDir = dir
	mysql.SeenFile = filepath.Join(dir, "replicas-seen")
	mysql.SeenPortsFile = filepath.Join(dir, "replica-ports-seen")
	mysql.StartResultFile = filepath.Join(dir, "start-result")
	mysql.DefaultSalvageDir = filepath.Join(dir, "salvage")
	config.DefaultDir = filepath.Join(dir, "etc")
	cliSeenPath, cliSeenPortsPath = mysql.SeenFile, mysql.SeenPortsFile
	cliSyslog = func(string) error { return nil }
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(testDefaultsDir)
	os.Exit(code)
}

// TestDefaultsAwayFromSystemPaths: as the tests see them, the agent's
// default files and directories are in the test's own directory.
func TestDefaultsAwayFromSystemPaths(t *testing.T) {
	for name, p := range map[string]string{
		"mysql.SeenFile": mysql.SeenFile, "mysql.SeenPortsFile": mysql.SeenPortsFile,
		"mysql.StartResultFile": mysql.StartResultFile, "mysql.DefaultSalvageDir": mysql.DefaultSalvageDir,
		"config.DefaultDir": config.DefaultDir, "cliSeenPath": cliSeenPath, "cliSeenPortsPath": cliSeenPortsPath,
	} {
		if strings.HasPrefix(p, "/var/lib") || strings.HasPrefix(p, "/etc") || !strings.HasPrefix(p, testDefaultsDir+"/") {
			t.Errorf("%s = %s, want it under %s", name, p, testDefaultsDir)
		}
	}
}
