package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
)

// executorCodex names the one roster executor whose output is prose rather than stream-json, so its
// verbatim tee gets a different extension.
const executorCodex = "codex"

// errNoSources is what a run with nothing reporting returns. A clean empty report would tell a
// scripted caller the code is fine.
var errNoSources = errors.New("every source degraded, no review to report")

// finder owns the find stage: every roster entry runs its own process and returns structured
// findings.
type finder struct {
	cfg  Config
	emit func(Event)
}

// sourceResult is one agent's outcome. It carries the spec because the report needs the roster entry
// alongside what the process actually did.
type sourceResult struct {
	spec     prompt.AgentSpec
	findings []finding.Finding
	stat     finding.SourceStat
	err      error
}

// ok reports whether the agent delivered. A source that did not is degraded, and degraded is the
// only thing the report and the synthesis prompt need to know about it.
func (r sourceResult) ok() bool { return r.err == nil }

func (f *finder) run(ctx context.Context) ([]sourceResult, error) {
	if len(f.cfg.Roster) == 0 {
		return nil, errors.New("roster is empty")
	}

	out := make([]sourceResult, 0, len(f.cfg.Roster))
	for _, spec := range f.cfg.Roster {
		out = append(out, f.runAgent(ctx, spec))
	}

	for _, r := range out {
		if r.ok() {
			return out, nil
		}
	}
	return nil, errNoSources
}

func (f *finder) runAgent(ctx context.Context, spec prompt.AgentSpec) sourceResult {
	res := sourceResult{spec: spec, stat: finding.SourceStat{
		Name: spec.Name, Lenses: spec.Lenses, Executor: spec.Executor,
		RequestedModel: spec.Model, Effort: spec.Effort,
	}}
	f.emit(Event{Kind: EventAgentStarted, Agent: spec.Name, Text: strings.Join(spec.Lenses, ", ")})

	text, err := f.cfg.Profile.Compose(f.cfg.Set, spec, prompt.ComposeOpts{Vars: f.cfg.Vars, History: f.cfg.History})
	if err != nil {
		return f.degrade(res, err)
	}

	raw, err := f.cfg.Archive.Writer(f.rawName(spec))
	if err != nil {
		return f.degrade(res, fmt.Errorf("open raw output for %s: %w", spec.Name, err))
	}

	result, runErr := f.cfg.NewRunner(RunnerSpec{Executor: spec.Executor, Model: spec.Model, Effort: spec.Effort}).
		Run(ctx, executor.Request{
			Prompt: text, Model: spec.Model, Effort: spec.Effort,
			Schema: finding.FinderSchema(), RawOutput: raw,
		}, newSink(spec.Name, f.emit, nil))

	res.stat.ActualModel, res.stat.Tokens = result.ActualModel, result.Tokens
	if closeErr := raw.Close(); closeErr != nil && runErr == nil {
		runErr = fmt.Errorf("close raw output for %s: %w", spec.Name, closeErr)
	}
	if runErr != nil {
		return f.degrade(res, runErr)
	}

	res.findings, err = f.parse(spec, result.StructuredOutput)
	if err != nil {
		return f.degrade(res, err)
	}

	f.emit(Event{Kind: EventFindings, Agent: spec.Name, Findings: res.findings})
	f.emit(Event{Kind: EventAgentDone, Agent: spec.Name, Text: strconv.Itoa(len(res.findings)) + " findings"})
	return res
}

// parse turns one agent's structured output into findings, assigning both attribution fields in Go.
//
// sources is overwritten with the executing agent's name, discarding whatever the model put there:
// a source is a process, and one agent naming itself twice is self-corroboration the confidence
// boost would count as agreement. id is rewritten for the same reason — four agents on one schema
// each emit "1", and synthesis derives the sources union from the ids it merged.
func (f *finder) parse(spec prompt.AgentSpec, raw json.RawMessage) ([]finding.Finding, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("agent %s returned no structured output", spec.Name)
	}

	var out struct {
		Findings []finding.Finding `json:"findings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode findings from %s: %w", spec.Name, err)
	}

	for i := range out.Findings {
		out.Findings[i].ID = spec.Name + "-" + strconv.Itoa(i+1)
		out.Findings[i].Sources = []string{spec.Name}
		out.Findings[i].Lenses = f.lenses(spec, out.Findings[i].Lenses)
	}
	return out.Findings, nil
}

// lenses keeps only lens names the agent actually carries. A model naming one it was never given is
// informational noise, and an empty result falls back to the agent's full set, which raised it by
// definition.
func (f *finder) lenses(spec prompt.AgentSpec, named []string) []string {
	out := make([]string, 0, len(named))
	for _, l := range named {
		if slices.Contains(spec.Lenses, l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return slices.Clone(spec.Lenses)
	}
	return out
}

// rawName is where the agent's verbatim tee goes. Per-agent streams live under agents/ so an agent
// named events cannot collide with events.jsonl.
func (f *finder) rawName(spec prompt.AgentSpec) string {
	ext := ".jsonl"
	if spec.Executor == executorCodex {
		ext = ".log"
	}
	return path.Join("agents", spec.Name+ext)
}

func (f *finder) degrade(res sourceResult, err error) sourceResult {
	res.err = err
	f.emit(Event{Kind: EventAgentDegraded, Agent: res.spec.Name, Text: err.Error()})
	return res
}

// report assembles the passthrough report, which is what makes --no-synthesis work at this stage
// rather than waiting for the synthesis one.
func (f *finder) report(sources []sourceResult) finding.Report {
	rep := finding.Report{Sources: finding.SourceStatus{Expected: len(sources)}}
	for _, s := range sources {
		stat := s.stat
		stat.Degraded = !s.ok()
		rep.Sources.Agents = append(rep.Sources.Agents, stat)
		rep.Stats.Tokens += stat.Tokens
		if stat.Degraded {
			rep.Sources.DegradedSources = append(rep.Sources.DegradedSources, s.spec.Name)
			continue
		}
		rep.Sources.Reported++
		rep.Findings = append(rep.Findings, s.findings...)
	}
	return rep
}
