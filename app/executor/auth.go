package executor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Authenticator checks the CLI credentials and runs its interactive login when needed.
// Both model executors implement it; authentication happens before a review claims a round.
type Authenticator interface {
	Authenticated(context.Context) (bool, error)
	Login(context.Context, io.ReadWriter) error
}

func (p *proc) authCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := p.runner.Command(ctx, p.bin, args...)
	cmd.Env = p.childEnv()
	cmd.Dir = p.opts.WorkDir
	return cmd
}

func (p *proc) login(ctx context.Context, terminal io.ReadWriter, args ...string) error {
	cmd := p.authCommand(ctx, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = terminal, terminal, terminal
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s login: %w", p.bin, err)
	}
	return nil
}
