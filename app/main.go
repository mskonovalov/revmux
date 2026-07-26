package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/executor"
)

var revision = "unknown"

// runOpts is what run needs from its surroundings. Every one of them is injected so the whole entry
// point is drivable from a test: no real terminal, no real clock, no writes to the process streams.
type runOpts struct {
	opts    options
	clock   executor.Clock
	stdout  io.Writer
	stderr  io.Writer
	openTTY func() (*os.File, error)
}

func main() {
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		var fe *flags.Error
		if errors.As(err, &fe) && fe.Type == flags.ErrHelp {
			fmt.Fprintln(os.Stderr, fe.Message)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	os.Exit(run(runOpts{
		opts: o, clock: executor.NewClock(),
		stdout: os.Stdout, stderr: os.Stderr, openTTY: openTTY,
	}))
}

func run(o runOpts) int {
	switch {
	case o.opts.Version:
		if err := printVersion(o.stdout, revision); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.Init:
		if err := o.opts.initConfig(o.stderr); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.DumpDefaults != "":
		if err := o.opts.dumpDefaults(o.stderr); err != nil {
			return o.fail(err)
		}
		return 0
	}

	if o.opts.Task == "" {
		return o.fail(errors.New("--task is required"))
	}
	if err := o.opts.checkName("--run", o.opts.runName(o.clock)); err != nil {
		return o.fail(err)
	}
	if _, err := o.opts.resolveContext(); err != nil {
		return o.fail(err)
	}
	if _, err := o.opts.promptSet(); err != nil {
		return o.fail(err)
	}
	return 0
}

func (o runOpts) fail(err error) int {
	_, _ = fmt.Fprintf(o.stderr, "error: %v\n", err)
	return 2
}

// openTTY opens the controlling terminal for both input and output. The TUI is gated on this
// succeeding, never on stdout being a terminal, which is false whenever the report is redirected.
func openTTY() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open tty: %w", err)
	}
	return f, nil
}

func printVersion(w io.Writer, rev string) error {
	if _, err := fmt.Fprintf(w, "revmux %s\n", rev); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}
