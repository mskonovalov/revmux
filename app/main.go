package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
)

var revision = "unknown"

// executorCodex is the one roster executor that is not claude, which is also the default.
const executorCodex = "codex"

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

	cfg, arc, err := o.pipelineConfig()
	if err != nil {
		return o.fail(err)
	}

	rep, err := o.review(cfg)
	if err != nil {
		return o.fail(err)
	}

	rep = rep.Above(o.opts.MinConfidence)
	if err := o.archiveRun(arc, cfg, rep); err != nil {
		return o.fail(err)
	}
	if err := o.write(rep); err != nil {
		return o.fail(err)
	}
	o.prune(arc)
	return rep.ExitCode()
}

// pipelineConfig resolves everything the pipeline needs, plus the archive package main writes its own
// artifacts through. The roster is resolved exactly once, here: the archive manifest and both
// renderers take that same slice rather than re-deriving it.
func (o runOpts) pipelineConfig() (pipeline.Config, *archive.Archive, error) {
	if o.opts.Task == "" {
		return pipeline.Config{}, nil, errors.New("--task is required")
	}
	runName := o.opts.runName(o.clock)
	if err := o.opts.checkName("--run", runName); err != nil {
		return pipeline.Config{}, nil, err
	}

	rc, err := o.opts.resolveContext()
	if err != nil {
		return pipeline.Config{}, nil, err
	}
	set, err := o.opts.promptSet()
	if err != nil {
		return pipeline.Config{}, nil, err
	}
	profile, err := set.Profile(o.opts.Profile)
	if err != nil {
		return pipeline.Config{}, nil, fmt.Errorf("resolve profile: %w", err)
	}
	roster, err := profile.Roster(o.opts.Lenses, set.LensNames())
	if err != nil {
		return pipeline.Config{}, nil, fmt.Errorf("resolve roster: %w", err)
	}

	// resolved before the run directory exists, and before Prune ever runs: a round read after
	// pruning is a round that no longer exists
	history, err := archive.History(rc.TaskDir)
	if err != nil {
		return pipeline.Config{}, nil, fmt.Errorf("read prior rounds: %w", err)
	}
	arc, err := archive.New(archive.Opts{TaskDir: rc.TaskDir, Run: runName, Keep: o.opts.KeepRuns})
	if err != nil {
		return pipeline.Config{}, nil, fmt.Errorf("open run archive: %w", err)
	}

	return pipeline.Config{
		NewRunner: o.runnerFactory(rc),
		Archive:   arc,
		Clock:     o.clock,
		Set:       set, Profile: profile, Roster: roster, Vars: rc.vars(), History: history,
		Task: o.opts.Task, Run: runName, ScopePath: rc.Scope,
		NoSynthesis: o.opts.NoSynthesis, NoVerify: o.opts.NoVerify,
		StaggerDelay: o.opts.StaggerDelay, MaxParallel: o.opts.MaxParallel,
		VerifyGroups: o.opts.VerifyGroups,
	}, arc, nil
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
// the whole slice without spawning a model CLI. Both executors are built once and shared: each holds
// immutable configuration only, and the roster runs them concurrently.
func (o runOpts) runnerFactory(rc reviewContext) func(pipeline.RunnerSpec) pipeline.Runner {
	if o.newRunner != nil {
		return o.newRunner
	}
	runner, eo := executor.NewRunner(), o.opts.executorOpts(rc, o.clock)
	claude, codex := executor.NewClaude(runner, eo), executor.NewCodex(runner, eo)
	return func(spec pipeline.RunnerSpec) pipeline.Runner {
		if spec.Executor == executorCodex {
			return codex
		}
		return claude
	}
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

// prune drops the oldest rounds beyond keep-runs, after the report has been written so a failed run
// keeps its artifacts. A failure is reported and the review's own exit code kept: an undeletable old
// round is housekeeping, not a missing artifact of this run.
func (o runOpts) prune(a *archive.Archive) {
	if err := a.Prune(); err != nil {
		_, _ = fmt.Fprintf(o.stderr, "warning: %v\n", err)
	}
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
