package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
)

var revision = "unknown"

// runOpts is what run needs from its surroundings. Every one of them is injected so the whole entry
// point is drivable from a test: no real terminal, no real clock, no writes to the process streams.
type runOpts struct {
	opts      options
	clock     executor.Clock
	stdout    io.Writer
	stderr    io.Writer
	openTTY   func() (*os.File, error)
	newRunner func(pipeline.RunnerSpec) pipeline.Runner
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

	cfg, err := o.pipelineConfig()
	if err != nil {
		return o.fail(err)
	}

	rep, err := o.review(cfg)
	if err != nil {
		return o.fail(err)
	}

	rep = rep.Above(o.opts.MinConfidence)
	if err := o.write(rep); err != nil {
		return o.fail(err)
	}
	return rep.ExitCode()
}

// pipelineConfig resolves everything the pipeline needs. The roster is resolved exactly once, here:
// the archive manifest and both renderers take that same slice rather than re-deriving it.
func (o runOpts) pipelineConfig() (pipeline.Config, error) {
	if o.opts.Task == "" {
		return pipeline.Config{}, errors.New("--task is required")
	}
	runName := o.opts.runName(o.clock)
	if err := o.opts.checkName("--run", runName); err != nil {
		return pipeline.Config{}, err
	}

	rc, err := o.opts.resolveContext()
	if err != nil {
		return pipeline.Config{}, err
	}
	set, err := o.opts.promptSet()
	if err != nil {
		return pipeline.Config{}, err
	}
	profile, err := set.Profile(o.opts.Profile)
	if err != nil {
		return pipeline.Config{}, fmt.Errorf("resolve profile: %w", err)
	}
	roster, err := profile.Roster(o.opts.Lenses, set.LensNames())
	if err != nil {
		return pipeline.Config{}, fmt.Errorf("resolve roster: %w", err)
	}

	return pipeline.Config{
		NewRunner: o.runnerFactory(rc),
		Archive:   discardArchive{},
		Clock:     o.clock,
		Set:       set, Profile: profile, Roster: roster, Vars: rc.vars(),
		Task: o.opts.Task, Run: runName, ScopePath: rc.Scope,
		NoSynthesis: o.opts.NoSynthesis, NoVerify: o.opts.NoVerify,
		StaggerDelay: o.opts.StaggerDelay, MaxParallel: o.opts.MaxParallel,
		VerifyGroups: o.opts.VerifyGroups,
	}, nil
}

// review runs the pipeline with the plain renderer subscribed to its events, and returns only once
// that renderer has drained the channel.
func (o runOpts) review(cfg pipeline.Config) (finding.Report, error) {
	p := pipeline.New(cfg)

	rendered := make(chan struct{})
	go func() {
		defer close(rendered)
		pr := &progress{w: o.stderr}
		pr.run(p.Events())
	}()

	rep, err := p.Run(context.Background())
	<-rendered
	if err != nil {
		return finding.Report{}, fmt.Errorf("review failed: %w", err)
	}
	return rep, nil
}

// runnerFactory builds the per-spec executor factory. A caller-supplied one wins, so a test drives
// the whole slice without spawning a model CLI.
func (o runOpts) runnerFactory(rc reviewContext) func(pipeline.RunnerSpec) pipeline.Runner {
	if o.newRunner != nil {
		return o.newRunner
	}
	claude := executor.NewClaude(executor.NewRunner(), o.opts.executorOpts(rc, o.clock))
	return func(pipeline.RunnerSpec) pipeline.Runner { return claude }
}

func (o runOpts) write(rep finding.Report) error {
	render := rep.Markdown
	if o.opts.JSON {
		render = rep.JSON
	}
	if err := render(o.stdout); err != nil {
		return fmt.Errorf("report to stdout: %w", err)
	}
	return nil
}

func (o runOpts) fail(err error) int {
	_, _ = fmt.Fprintf(o.stderr, "error: %v\n", err)
	return 2
}

// discardArchive stands in until app/archive exists. It keeps the pipeline's write path exercised
// while producing no artifacts.
type discardArchive struct{}

func (discardArchive) Writer(string) (io.WriteCloser, error) { return discardCloser{io.Discard}, nil }

type discardCloser struct{ io.Writer }

func (discardCloser) Close() error { return nil }

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
