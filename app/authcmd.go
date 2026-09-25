package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// authCmd selects authentication without requiring a task or claiming a review round.
type authCmd struct{ opts *options }

func (c *authCmd) Execute([]string) error { //nolint:unparam // go-flags Commander signature
	c.opts.showAuth = true
	return nil
}

func (o runOpts) writeAuth() error {
	set, err := o.opts.promptSet()
	if err != nil {
		return err
	}
	profile, err := set.Profile(o.opts.Profile)
	if err != nil {
		return fmt.Errorf("resolve profile: %w", err)
	}
	roster, err := profile.Roster(o.opts.Lenses, set.LensNames())
	if err != nil {
		return fmt.Errorf("resolve roster: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return o.authenticate(ctx, reviewContext{WorkDir: o.opts.WorkDir}, set, profile, roster)
}

func (o runOpts) runAuth() int {
	if err := o.writeAuth(); err != nil {
		return o.fail(err)
	}
	return 0
}
