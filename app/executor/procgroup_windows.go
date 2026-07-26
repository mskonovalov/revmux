//go:build windows

package executor

import "os/exec"

// setupProcessGroup is a no-op: windows has no process groups to detach from.
func (p *proc) setupProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills only the direct child. Descendants surviving it is an accepted limitation.
func (pg *processGroupCleanup) killProcessGroup() {
	pg.killOnce.Do(func() {
		if pg.cmd.Process == nil {
			return
		}
		_ = pg.cmd.Process.Kill()
	})
}
