package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func (p *proc) authCommand(ctx context.Context, args ...string) *exec.Cmd {
	return p.childCommand(ctx, p.bin, args...)
}

// gatewayCommand splits a configured command line on whitespace and runs it like the CLI whose
// credentials it manages.
func (p *proc) gatewayCommand(ctx context.Context, line string) *exec.Cmd {
	argv := strings.Fields(line)
	return p.childCommand(ctx, argv[0], argv[1:]...)
}

func (p *proc) childCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := p.runner.Command(ctx, name, args...)
	cmd.Env = p.childEnv()
	cmd.Dir = p.opts.WorkDir
	return cmd
}

// Interactive login stays in the terminal's process group so its prompts can read from the TTY.
// CommandContext stops the direct CLI on cancellation; a browser it opened may remain running.
func (p *proc) login(terminal io.ReadWriter, cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = terminal, terminal, terminal
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s login: %w", cmd.Args[0], err)
	}
	return nil
}

// gatewayAuthenticated runs the configured check for an external provider. Without one the provider's
// credentials cannot be inspected, so they are trusted.
func (p *proc) gatewayAuthenticated(ctx context.Context) (bool, error) {
	if p.opts.GatewayCheck == "" {
		return true, nil
	}
	err := p.gatewayCommand(ctx, p.opts.GatewayCheck).Run()
	if _, failed := errors.AsType[*exec.ExitError](err); failed {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gateway check: %w", err)
	}
	return true, nil
}

func (p *proc) gatewayLogin(ctx context.Context, terminal io.ReadWriter) error {
	if p.opts.GatewayLogin == "" {
		return errors.New("set gateway-login in the revmux config, or log in to the model provider manually")
	}
	if terminal == nil {
		return fmt.Errorf("run `%s` in a terminal", p.opts.GatewayLogin)
	}
	return p.login(terminal, p.gatewayCommand(ctx, p.opts.GatewayLogin))
}
