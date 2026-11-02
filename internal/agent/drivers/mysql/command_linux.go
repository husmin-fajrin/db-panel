package mysql

import (
	"os/exec"
	"syscall"
)

// setParentDeathSignal makes cmd get SIGTERM when the agent dies, so a
// dump pipeline or an apt run does not outlive an agent restart (systemd
// KillMode=process leaves children running) and a Retry cannot start a
// second load beside it.
func setParentDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
