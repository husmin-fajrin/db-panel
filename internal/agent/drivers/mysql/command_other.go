//go:build !linux

package mysql

import "os/exec"

// setParentDeathSignal does nothing off Linux: the agent runs on Linux
// only, and this keeps the package buildable on a developer's macOS.
func setParentDeathSignal(*exec.Cmd) {}
