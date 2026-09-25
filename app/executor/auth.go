package executor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

func (p *proc) authCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := p.runner.Command(ctx, p.bin, args...)
	cmd.Env = p.childEnv()
	cmd.Dir = p.opts.WorkDir
	return cmd
}

// Interactive login stays in the terminal's process group so its prompts can read from the TTY.
// CommandContext stops the direct CLI on cancellation; a browser it opened may remain running.
func (p *proc) login(ctx context.Context, terminal io.ReadWriter, args ...string) error {
	cmd := p.authCommand(ctx, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = terminal, terminal, terminal
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s login: %w", p.bin, err)
	}
	return nil
}
