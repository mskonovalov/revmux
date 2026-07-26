package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/ui"
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
	case o.opts.showConfig:
		if err := o.writeCatalog(); err != nil {
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
	// the report goes to stdout only here, after review returned and with it the bubbletea program:
	// writing it while the TUI still owns the terminal interleaves it with the final frame
	if err := o.write(rep); err != nil {
		return o.fail(err)
	}
	o.prune(arc)
	return rep.ExitCode()
}

// writeCatalog prints the resolved configuration as JSON. It is the one carve-out in "stdout belongs
// to the report": no pipeline, archive or TUI exists yet, so there is nothing for it to collide with.
//
// The prompt tree is loaded directly rather than through promptSet: a caller running this to discover
// which profiles exist is exactly the caller whose --profile does not resolve.
func (o runOpts) writeCatalog() error {
	set, err := prompt.Load(o.opts.promptOpts())
	if err != nil {
		return fmt.Errorf("load prompts: %w", err)
	}
	enc := json.NewEncoder(o.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(o.opts.catalog(set)); err != nil {
		return fmt.Errorf("write catalog: %w", err)
	}
	return nil
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

// review runs the pipeline with a renderer subscribed to its events, hands the finished report to
// that renderer, and returns only once the renderer has let go of the terminal.
func (o runOpts) review(cfg pipeline.Config) (finding.Report, error) {
	p := pipeline.New(cfg)
	r := o.render(cfg.Roster, p.Events())

	rep, err := p.Run(context.Background())
	r.finish(rep, err)
	if err != nil {
		return finding.Report{}, fmt.Errorf("review failed: %w", err)
	}
	return rep, nil
}

// renderer is the active event subscriber, held so the finished report can be handed to it and the
// run can wait for it to release the terminal. prog is nil under the plain renderer, which has
// nothing to browse and needs no report.
type renderer struct {
	prog *tea.Program
	done chan struct{}
}

// render subscribes the active renderer to the run's events — the TUI when the tty opens, the plain
// stderr renderer otherwise. Exactly one of them reads the channel: a Go channel distributes rather
// than broadcasts, so a second reader would take an arbitrary half of the events.
func (o runOpts) render(roster []prompt.AgentSpec, events <-chan pipeline.Event) *renderer {
	r := &renderer{done: make(chan struct{})}

	tty := o.tty()
	if tty == nil {
		go func() {
			defer close(r.done)
			(&progress{w: o.stderr, roster: roster}).run(events)
		}()
		return r
	}

	m := ui.New(ui.ModelConfig{Roster: roster, Events: events})
	r.prog = tea.NewProgram(m, tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen())
	go func() {
		defer close(r.done)
		defer func() { _ = tty.Close() }()
		if _, err := r.prog.Run(); err != nil {
			_, _ = fmt.Fprintf(o.stderr, "warning: terminal ui: %v\n", err)
		}
	}()
	return r
}

// finish hands the report to the findings browser and waits for the reader to close it. A failed run
// has nothing to browse and its error belongs on stderr, so the program is asked to quit instead.
//
// The wait is what keeps the report off stdout until the terminal is free: writing it while the TUI
// still owns the screen would interleave it with the final frame.
func (r *renderer) finish(rep finding.Report, err error) {
	switch {
	case r.prog == nil:
	case err != nil:
		r.prog.Quit()
	default:
		r.prog.Send(ui.CompletedMsg{Report: rep})
	}
	<-r.done
}

// tty opens the terminal the TUI renders to, nil when there is none to render on. The gate is the tty
// opening, never stdout being a terminal — that is false whenever the report is redirected, which is
// one of the most common invocations.
func (o runOpts) tty() *os.File {
	if o.opts.NoTUI || o.openTTY == nil {
		return nil
	}
	f, err := o.openTTY()
	if err != nil {
		return nil
	}
	return f
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
