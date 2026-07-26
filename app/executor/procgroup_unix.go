//go:build !windows

package executor

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// setupProcessGroup detaches the child from the controlling terminal. Setsid rather than Setpgid: a
// descendant touching terminal I/O would otherwise stop the whole group with SIGTTIN/SIGTTOU.
func (p *proc) setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killProcessGroup signals the whole group, not just the direct child — node subagents and MCP servers
// are the things that actually accumulate.
func (pg *processGroupCleanup) killProcessGroup() {
	pg.killOnce.Do(func() {
		if pg.cmd.Process == nil {
			return
		}
		pgid := pg.cmd.Process.Pid
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return
			}
		}
		time.Sleep(killGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
}
