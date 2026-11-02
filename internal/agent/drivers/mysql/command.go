package mysql

import (
	"context"
	"os/exec"
)

// newCommand builds the commands of command; tests wrap it to see them.
var newCommand = exec.CommandContext

// command builds the processes the agent starts through Host.Exec (the
// mysqldump | mysql pipeline, the client tools) and the installer (apt,
// dnf, rpm). On Linux it asks for SIGTERM when the agent dies (see
// setParentDeathSignal). Children of probe.System.Output are short-lived
// and intentionally left unchanged.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := newCommand(ctx, name, args...)
	setParentDeathSignal(cmd)
	return cmd
}
