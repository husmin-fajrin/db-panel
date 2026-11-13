package agent

import (
	"log/slog"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// TestEveryRegisteredCommandClassified: strict mode knows every command a
// running agent registers, and no other; the commands it leaves to the role
// table are refused by it on a quarantined server.
func TestEveryRegisteredCommandClassified(t *testing.T) {
	reg := commands.NewRegistry()
	probe.Register(reg, noMySQLSystem{}, "test")
	runner := jobs.NewRunner(nil, nil)
	runner.Register(reg)
	drv := RegisterMySQL(reg, config.Config{}, t.TempDir(), noMySQLSystem{}, runner, slog.New(slog.DiscardHandler))
	monitor.New(drv, nil, slog.New(slog.DiscardHandler), monitor.Options{Pins: drv.Pins()}).Register(reg)

	classes := mysql.StrictClassNames()
	registered := map[string]bool{}
	for _, name := range reg.Names() {
		registered[name] = true
		if _, ok := classes[name]; !ok {
			t.Errorf("%s registered but not classified", name)
		}
	}
	for name := range classes {
		if !registered[name] {
			t.Errorf("%s classified but not registered", name)
		}
	}

	p, err := mysql.LoadPins(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetQuarantined("10.0.0.1:3306"); err != nil {
		t.Fatal(err)
	}
	for name, class := range classes {
		switch class {
		case "allowed", "protected":
		case "role-refused":
			if err := p.Allow(name, nil); err == nil {
				t.Errorf("%s is role-refused, but a quarantined server's pins allow it", name)
			}
		default:
			t.Errorf("%s: unknown class %q", name, class)
		}
	}
}
