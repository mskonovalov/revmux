package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
)

// synthesizer owns the synthesis stage: one model call merging every source's findings into one set,
// splitting open questions and pre-existing issues out first.
type synthesizer struct {
	cfg  Config
	emit func(Event)
	save func(name string, data []byte)
}

// synthesized is the wire shape of one merged finding: a Finding plus the ids of the inputs it came
// from. Go derives sources and lenses from those ids, so no schema needs to expose sources — a field
// the model can fill is a field it will fill, and one agent naming itself twice is the
// self-corroboration the confidence boost would count as agreement.
type synthesized struct {
	finding.Finding
	MergedIDs []string `json:"merged_ids"`
}

// run composes the synthesis prompt, runs the stage and returns the three merged lists. The report it
// returns carries findings and the stage's own token count, never source status or timings — those
// belong to the run and are held by the caller.
func (s *synthesizer) run(ctx context.Context, sources []sourceResult) (finding.Report, error) {
	stage, err := s.cfg.Set.Stage(stageSynthesis)
	if err != nil {
		return finding.Report{}, fmt.Errorf("resolve synthesis stage: %w", err)
	}

	text, err := stage.Compose(prompt.ComposeOpts{Vars: s.vars(sources), History: s.cfg.History})
	if err != nil {
		return finding.Report{}, fmt.Errorf("compose synthesis prompt: %w", err)
	}
	s.save(path.Join(stagePromptDir, stageSynthesis+".md"), []byte(text))

	spec := RunnerSpec{Executor: stage.Executor, Model: stage.Model, Effort: stage.Effort}
	res, err := s.cfg.NewRunner(spec).Run(ctx, executor.Request{
		Prompt: text, Model: stage.Model, Effort: stage.Effort, Schema: finding.SynthesisSchema(),
	}, newSink(stageSynthesis, s.emit, nil))
	if err != nil {
		return finding.Report{}, fmt.Errorf("synthesis stage: %w", err)
	}

	rep, err := s.parse(res.StructuredOutput, s.inputs(sources))
	if err != nil {
		return finding.Report{}, err
	}
	rep.Stats.Tokens = res.Tokens
	s.emit(Event{Kind: EventFindings, Agent: stageSynthesis, Findings: rep.Findings})
	return rep, nil
}

// vars adds the two stage variables to the run's own. FINDINGS is what the model merges and SOURCES
// is the roster as data: the pipeline knows which process emitted what, and letting the model infer
// the source count from the findings themselves is how a single agent's two lenses become two votes.
func (s *synthesizer) vars(sources []sourceResult) prompt.Vars {
	out := prompt.Vars{}
	maps.Copy(out, s.cfg.Vars)
	out["FINDINGS"] = s.findingsBlock(sources)
	out["SOURCES"] = s.sourcesBlock(sources)
	return out
}

func (s *synthesizer) findingsBlock(sources []sourceResult) string {
	all := []finding.Finding{}
	for _, src := range sources {
		if src.ok() {
			all = append(all, src.findings...)
		}
	}
	if len(all) == 0 {
		return "No source reported a finding."
	}
	b, _ := json.MarshalIndent(all, "", "  ") // Finding is plain data, so encoding cannot fail
	return string(b)
}

// sourcesBlock states what actually ran. A degraded source has to be loud here as well as in the JSON
// and the markdown banner, since it is what tells the model corroboration was rarer than it looks.
func (s *synthesizer) sourcesBlock(sources []sourceResult) string {
	reported, degraded := 0, []string{}
	lines := make([]string, 0, len(sources))
	for _, src := range sources {
		entry := "- " + src.spec.Name + " (lenses: " + strings.Join(src.spec.Lenses, ", ") + ")"
		if !src.ok() {
			degraded = append(degraded, src.spec.Name)
			lines = append(lines, entry+" DEGRADED, reported nothing")
			continue
		}
		reported++
		lines = append(lines, entry+" reported "+s.emitted(src.findings))
	}

	head := strconv.Itoa(len(sources)) + " sources ran, " + strconv.Itoa(reported) + " reported."
	if len(degraded) > 0 {
		head += " This run is DEGRADED: " + strings.Join(degraded, ", ") + " reported nothing."
	}
	return head + "\n" + strings.Join(lines, "\n")
}

func (s *synthesizer) emitted(list []finding.Finding) string {
	if len(list) == 0 {
		return "no findings"
	}
	ids := make([]string, 0, len(list))
	for _, f := range list {
		ids = append(ids, f.ID)
	}
	return strconv.Itoa(len(list)) + " findings: " + strings.Join(ids, ", ")
}

// inputs keys the pre-synthesis findings by id. Those ids are unique because find stamps them
// <agent>-<n>, which is what lets attribution survive a merge across agents.
func (s *synthesizer) inputs(sources []sourceResult) map[string]finding.Finding {
	out := map[string]finding.Finding{}
	for _, src := range sources {
		if !src.ok() {
			continue
		}
		for _, f := range src.findings {
			out[f.ID] = f
		}
	}
	return out
}

func (s *synthesizer) parse(raw json.RawMessage, inputs map[string]finding.Finding) (finding.Report, error) {
	if len(raw) == 0 {
		return finding.Report{}, errors.New("synthesis returned no structured output")
	}

	var out struct {
		Findings      []synthesized `json:"findings"`
		OpenQuestions []synthesized `json:"open_questions"`
		PreExisting   []synthesized `json:"pre_existing"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return finding.Report{}, fmt.Errorf("decode synthesis output: %w", err)
	}

	rep := finding.Report{}
	findings, err := s.attribute(out.Findings, inputs)
	if err != nil {
		return finding.Report{}, err
	}
	rep.Findings = findings
	if rep.OpenQuestions, err = s.attribute(out.OpenQuestions, inputs); err != nil {
		return finding.Report{}, err
	}
	if rep.PreExisting, err = s.attribute(out.PreExisting, inputs); err != nil {
		return finding.Report{}, err
	}
	return rep, nil
}

// attribute derives sources and lenses from the merged input ids, discarding whatever the model put
// in either field. A merged id that is not an input is a hard error rather than a skip: it means the
// model invented one, and dropping it quietly produces a finding with fewer sources than it earned.
func (s *synthesizer) attribute(list []synthesized, inputs map[string]finding.Finding) ([]finding.Finding, error) {
	out := make([]finding.Finding, 0, len(list))
	for _, item := range list {
		if len(item.MergedIDs) == 0 {
			return nil, fmt.Errorf("synthesized finding %q merged no input ids", item.Title)
		}

		f := item.Finding
		f.ID, f.Sources, f.Lenses = item.MergedIDs[0], nil, nil
		for _, id := range item.MergedIDs {
			in, ok := inputs[id]
			if !ok {
				return nil, fmt.Errorf("synthesis merged unknown finding id %q", id)
			}
			f.Sources = s.union(f.Sources, in.Sources)
			f.Lenses = s.union(f.Lenses, in.Lenses)
		}
		out = append(out, f)
	}
	return out, nil
}

// union appends what into does not already hold. A source is a process, so one agent carrying two
// lenses collapses to a single name here rather than counting twice.
func (s *synthesizer) union(into, add []string) []string {
	for _, v := range add {
		if !slices.Contains(into, v) {
			into = append(into, v)
		}
	}
	return into
}
