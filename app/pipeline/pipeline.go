// Package pipeline orchestrates the three review stages — find, synthesize, verify — over a roster of
// supervised model processes.
//
// It is headless: it emits typed events on a channel and never touches a terminal, a repository or a
// concrete executor. Everything that reaches the outside world arrives through Config.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
)

//go:generate moq -out mocks/runner.go -pkg mocks -skip-ensure -fmt goimports . Runner
//go:generate moq -out mocks/archiver.go -pkg mocks -skip-ensure -fmt goimports . archiver:ArchiverMock

// eventsFile is the run's decision record: stalls, retries, degrades and stage transitions, none of
// which any per-agent stream contains.
const eventsFile = "events.jsonl"

// stage names, used for both the stage events and the recorded timings.
const stageFind = "find"

// Runner runs one supervised process. It is declared here, by the consumer, and exported only so
// package main can name it when it supplies the factory.
type Runner interface {
	Run(ctx context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error)
}

// RunnerSpec selects which binary runs a piece of work, and how. Both a roster entry and a stage
// prompt produce one, so a stage never has to fabricate a fake roster entry to pick a runner.
type RunnerSpec struct {
	Executor string
	Model    string
	Effort   string
}

// archiver is the run archive as the pipeline needs it: one writer per relative artifact path.
type archiver interface {
	Writer(name string) (io.WriteCloser, error)
}

// Config is everything the pipeline takes from its surroundings. Idle and hard timeouts are
// deliberately absent — they reach the executors through executor.Opts, built by the composition
// root, because the pipeline never constructs an executor.
type Config struct {
	NewRunner func(RunnerSpec) Runner
	Archive   archiver
	Clock     executor.Clock

	Set     *prompt.Set
	Profile *prompt.Profile
	Roster  []prompt.AgentSpec
	Vars    prompt.Vars
	History string

	Task      string
	Run       string
	ScopePath string

	NoSynthesis bool
	NoVerify    bool

	StaggerDelay time.Duration
	MaxParallel  int
	VerifyGroups int
}

// Pipeline runs one review. Run is a thin stage orchestrator: the stages own their own logic so no
// single type accumulates all three plus event fan-out plus I/O.
type Pipeline struct {
	cfg     Config
	events  chan Event
	stagger *stagger

	mu  sync.Mutex
	log io.WriteCloser
	enc *json.Encoder
	err error // first archive failure, sticky: a half-written archive fails the run
}

// New builds a pipeline over cfg. The event channel is buffered and lossy by design.
//
// The stagger is built here and handed to each stage rather than owned by one: its gate latches open
// on the first release and never re-arms, so a later stage runs through the same instance instead of
// paying a second stagger delay to re-prove auth the first stage already proved.
func New(cfg Config) *Pipeline {
	return &Pipeline{
		cfg: cfg, events: make(chan Event, eventBuffer),
		stagger: newStagger(cfg.StaggerDelay, cfg.MaxParallel, cfg.Clock),
	}
}

// Events returns the event channel. It has exactly one reader — the active renderer — because a Go
// channel distributes rather than broadcasts, so a second subscriber would take an arbitrary half of
// the events. The archive is written from emit instead.
func (p *Pipeline) Events() <-chan Event { return p.events }

// Run executes the stages and returns the report. A failed archive write comes back as an error
// rather than a logged warning: a report next to a half-written archive reads as complete.
func (p *Pipeline) Run(ctx context.Context) (rep finding.Report, err error) {
	defer func() {
		p.closeEvents()
		close(p.events)
		if archiveErr := p.archiveErr(); archiveErr != nil && err == nil {
			rep, err = finding.Report{}, archiveErr
		}
	}()

	p.openEvents()
	started := p.cfg.Clock.Now()

	rep, err = p.runFind(ctx)
	if err != nil {
		return finding.Report{}, err
	}

	rep.Scope = finding.Scope{Task: p.cfg.Task, Run: p.cfg.Run, ScopePath: p.cfg.ScopePath}
	rep.Stats.StartedAt = started
	rep.Stats.FinishedAt = p.cfg.Clock.Now()
	rep.Stats.DurationMS = rep.Stats.FinishedAt.Sub(started).Milliseconds()
	return rep, nil
}

// runFind runs the find stage and appends its own timing to the report it returns. Each stage
// records its own, since manifest.json reads them back and nothing can recompute one afterwards.
func (p *Pipeline) runFind(ctx context.Context) (finding.Report, error) {
	p.emit(Event{Kind: EventStage, Stage: stageFind})
	at := p.cfg.Clock.Now()

	f := &finder{cfg: p.cfg, emit: p.emit, stagger: p.stagger}
	sources, err := f.run(ctx)
	if err != nil {
		return finding.Report{}, err
	}

	rep := f.report(sources)
	rep.Stats.Stages = append(rep.Stats.Stages,
		finding.StageTiming{Name: stageFind, DurationMS: p.cfg.Clock.Now().Sub(at).Milliseconds()})
	return rep, nil
}

// emit records the event in the archive first and synchronously, then offers it to the channel. That
// ordering is what makes events.jsonl complete while the display stays droppable.
func (p *Pipeline) emit(ev Event) {
	if ev.At.IsZero() {
		ev.At = p.cfg.Clock.Now()
	}
	p.archive(ev)
	select {
	case p.events <- ev:
	default:
	}
}

func (p *Pipeline) archive(ev Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enc == nil || p.err != nil {
		return
	}
	if err := p.enc.Encode(ev); err != nil {
		p.err = fmt.Errorf("archive event: %w", err)
	}
}

func (p *Pipeline) openEvents() {
	w, err := p.cfg.Archive.Writer(eventsFile)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.err = fmt.Errorf("open %s: %w", eventsFile, err)
		return
	}
	p.log, p.enc = w, json.NewEncoder(w)
}

// closeEvents folds a deferred write failure into the same sticky error: a close is where one
// surfaces, and nothing outside the pipeline holds this handle.
func (p *Pipeline) closeEvents() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return
	}
	if err := p.log.Close(); err != nil && p.err == nil {
		p.err = fmt.Errorf("close %s: %w", eventsFile, err)
	}
	p.log, p.enc = nil, nil
}

func (p *Pipeline) archiveErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
