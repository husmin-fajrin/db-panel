//go:build linux

package mysql_test

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
)

// recordCommands collects the child processes the agent builds until the
// test ends.
func recordCommands(t *testing.T) *[]*exec.Cmd {
	t.Helper()
	var cmds []*exec.Cmd
	t.Cleanup(mysql.SetCommandHook(func(c *exec.Cmd) { cmds = append(cmds, c) }))
	return &cmds
}

// wantPdeathsig fails unless every command in cmds (at least one) gets
// SIGTERM when the agent dies, so a dump pipeline or an apt run does not
// outlive an agent restart (Plan 2 residual 4).
func wantPdeathsig(t *testing.T, cmds []*exec.Cmd) {
	t.Helper()
	if len(cmds) == 0 {
		t.Fatal("no command was built through the hook")
	}
	for _, c := range cmds {
		if c.SysProcAttr == nil || c.SysProcAttr.Pdeathsig != syscall.SIGTERM {
			t.Errorf("%v: SysProcAttr = %+v, want Pdeathsig SIGTERM", c.Args, c.SysProcAttr)
		}
	}
}

func TestExecSetsPdeathsig(t *testing.T) {
	cmds := recordCommands(t)
	h := mysql.NewHost(mysql.Layout{}, nil, nil, probe.RealSystem(), nil)
	if err := h.Exec(context.Background(), nil, io.Discard, "true"); err != nil {
		t.Fatal(err)
	}
	wantPdeathsig(t, *cmds)
}

func TestInstallRunSetsPdeathsig(t *testing.T) {
	cmds := recordCommands(t)
	ins := mysql.NewRealInstaller(probe.RealSystem(), nil)
	if err := ins.Run(context.Background(), nil, nil, func(string) {}, "true"); err != nil {
		t.Fatal(err)
	}
	wantPdeathsig(t, *cmds)
}
