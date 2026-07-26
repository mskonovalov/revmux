package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
)

func TestSynthesizer_run(t *testing.T) {
	t.Run("the prompt carries the real roster and the findings to merge", func(t *testing.T) {
		h := newHarness(t)
		var got executor.Request
		h.cfg.NewRunner = stageRunner(&got, executor.Result{StructuredOutput: synthJSON(
			`{"merged_ids":["bugs+impl-1","codex-1"],"file":"a.go","line":10,"severity":"critical","confidence":90,"title":"leak","body":"b"}`,
		), Tokens: 300}, nil)

		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(Event) {}}
		rep, err := s.run(context.Background(), synthSources())
		require.NoError(t, err)

		assert.Contains(t, got.Prompt, "2 sources ran, 2 reported.")
		assert.Contains(t, got.Prompt, "- bugs+impl (lenses: bugs, impl) reported 2 findings: bugs+impl-1, bugs+impl-2")
		assert.Contains(t, got.Prompt, "- codex (lenses: adversarial) reported 1 findings: codex-1")
		assert.Contains(t, got.Prompt, `"id": "codex-1"`, "the findings block carries the ids the model must copy")
		assert.JSONEq(t, string(finding.SynthesisSchema()), string(got.Schema))
		assert.Equal(t, 300, rep.Stats.Tokens, "the stage's own tokens are reported")
	})

	t.Run("the runner spec comes from the stage's own front matter", func(t *testing.T) {
		h := newHarness(t)
		var spec RunnerSpec
		h.cfg.NewRunner = func(rs RunnerSpec) Runner {
			spec = rs
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{StructuredOutput: synthJSON()}, nil
			}}
		}

		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(Event) {}}
		_, err := s.run(context.Background(), synthSources())
		require.NoError(t, err)
		assert.Equal(t, RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}, spec)
	})

	t.Run("emits the merged findings so a renderer sees them", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = stageRunner(&executor.Request{}, executor.Result{StructuredOutput: synthJSON(
			`{"merged_ids":["codex-1"],"file":"a.go","line":10,"severity":"critical","confidence":70,"title":"leak","body":"b"}`,
		)}, nil)

		var seen []Event
		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(ev Event) { seen = append(seen, ev) }}
		_, err := s.run(context.Background(), synthSources())
		require.NoError(t, err)

		require.Len(t, seen, 1)
		assert.Equal(t, EventFindings, seen[0].Kind)
		assert.Equal(t, stageSynthesis, seen[0].Agent)
		assert.Len(t, seen[0].Findings, 1)
	})

	t.Run("a stage failure is not a silently unsynthesized report", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = stageRunner(&executor.Request{}, executor.Result{}, errors.New("stalled twice"))

		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(Event) {}}
		_, err := s.run(context.Background(), synthSources())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stalled twice")
	})

	t.Run("no structured output", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = stageRunner(&executor.Request{}, executor.Result{}, nil)

		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(Event) {}}
		_, err := s.run(context.Background(), synthSources())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no structured output")
	})

	t.Run("malformed synthesis output", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = stageRunner(&executor.Request{}, executor.Result{StructuredOutput: json.RawMessage(`{"findings":`)}, nil)

		s := &synthesizer{cfg: h.cfg, save: h.save, emit: func(Event) {}}
		_, err := s.run(context.Background(), synthSources())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode synthesis output")
	})
}

func TestSynthesizer_vars(t *testing.T) {
	t.Run("a degraded source is named in the sources block", func(t *testing.T) {
		h := newHarness(t)
		s := &synthesizer{cfg: h.cfg}
		got := s.vars(append(synthSources(), sourceResult{
			spec: prompt.AgentSpec{Name: "docs+tests", Lenses: []string{"docs", "tests"}},
			err:  errors.New("stalled"),
		}))

		assert.Contains(t, got["SOURCES"], "3 sources ran, 2 reported.")
		assert.Contains(t, got["SOURCES"], "This run is DEGRADED: docs+tests reported nothing.")
		assert.Contains(t, got["SOURCES"], "- docs+tests (lenses: docs, tests) DEGRADED, reported nothing")
		assert.NotContains(t, got["FINDINGS"], "docs+tests", "a degraded source contributed no findings to merge")
	})

	t.Run("keeps the run's own variables", func(t *testing.T) {
		h := newHarness(t)
		s := &synthesizer{cfg: h.cfg}
		got := s.vars(synthSources())
		assert.Equal(t, "/abs/scope.md", got["SCOPE"])
		assert.NotContains(t, h.cfg.Vars, "FINDINGS", "the stage variables do not leak back into the run's")
	})

	t.Run("a source that reported nothing says so", func(t *testing.T) {
		s := &synthesizer{}
		got := s.vars([]sourceResult{{spec: prompt.AgentSpec{Name: "bugs", Lenses: []string{"bugs"}}}})
		assert.Contains(t, got["SOURCES"], "- bugs (lenses: bugs) reported no findings")
		assert.Equal(t, "No source reported a finding.", got["FINDINGS"])
	})
}

func TestSynthesizer_prompt_dropRule(t *testing.T) {
	h := newHarness(t)
	stage, err := h.cfg.Set.Stage(stageSynthesis)
	require.NoError(t, err)

	s := &synthesizer{cfg: h.cfg}
	text, err := stage.Compose(prompt.ComposeOpts{Vars: s.vars(synthSources())})
	require.NoError(t, err)

	// the drop rule is executed by the model, so the composed prompt is the only thing a Go test can assert
	assert.Contains(t, text, "route it to the verifier")
	assert.Contains(t, text, "degraded")
	assert.Contains(t, text, "min(99, highest confidence + 10 * (distinct sources - 1))")
	assert.Contains(t, text, "2 sources ran, 2 reported.")
}

func TestSynthesizer_attribution(t *testing.T) {
	s := &synthesizer{}
	inputs := s.inputs(synthSources())

	t.Run("two agents merging into one finding carry both names and the union of their lenses", func(t *testing.T) {
		rep, err := s.parse(synthJSON(
			`{"merged_ids":["bugs+impl-1","codex-1"],"file":"a.go","line":10,"severity":"critical","confidence":90,"title":"leak","body":"b"}`,
		), inputs)
		require.NoError(t, err)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"bugs+impl", "codex"}, rep.Findings[0].Sources)
		assert.Equal(t, []string{"bugs", "adversarial"}, rep.Findings[0].Lenses)
		assert.Equal(t, "bugs+impl-1", rep.Findings[0].ID, "the id traces back to the input corpus")
	})

	t.Run("one agent's two lenses are one source, not two corroborating votes", func(t *testing.T) {
		rep, err := s.parse(synthJSON(
			`{"merged_ids":["bugs+impl-1","bugs+impl-2"],"file":"a.go","line":10,"severity":"major","confidence":80,"title":"leak","body":"b"}`,
		), inputs)
		require.NoError(t, err)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"bugs+impl"}, rep.Findings[0].Sources, "an agent cannot corroborate itself")
		assert.Equal(t, []string{"bugs", "impl"}, rep.Findings[0].Lenses, "both lenses still say why it was raised")
	})

	t.Run("a model-supplied sources field is discarded", func(t *testing.T) {
		rep, err := s.parse(synthJSON(
			`{"merged_ids":["codex-1"],"file":"a.go","line":10,"severity":"major","confidence":70,"title":"x","body":"b","sources":["a","b","c"]}`,
		), inputs)
		require.NoError(t, err)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"codex"}, rep.Findings[0].Sources, "Go assigns sources, never the model")
	})

	t.Run("attribution is derived in every list, not only findings", func(t *testing.T) {
		raw := json.RawMessage(`{"findings":[],"open_questions":[` +
			`{"merged_ids":["bugs+impl-2"],"file":"a.go","line":11,"severity":"minor","confidence":60,"title":"q","body":"b"}` +
			`],"pre_existing":[` +
			`{"merged_ids":["codex-1"],"file":"a.go","line":10,"severity":"critical","confidence":70,"title":"old","body":"b"}` +
			`]}`)
		rep, err := s.parse(raw, inputs)
		require.NoError(t, err)
		require.Len(t, rep.OpenQuestions, 1)
		require.Len(t, rep.PreExisting, 1)
		assert.Equal(t, []string{"bugs+impl"}, rep.OpenQuestions[0].Sources)
		assert.Equal(t, []string{"codex"}, rep.PreExisting[0].Sources)
	})

	t.Run("an invented id fails rather than being skipped", func(t *testing.T) {
		_, err := s.parse(synthJSON(
			`{"merged_ids":["bugs+impl-1","ghost-9"],"file":"a.go","line":10,"severity":"major","confidence":80,"title":"x","body":"b"}`,
		), inputs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown finding id "ghost-9"`)
	})

	t.Run("an unattributed output fails", func(t *testing.T) {
		_, err := s.parse(synthJSON(
			`{"merged_ids":[],"file":"a.go","line":10,"severity":"major","confidence":80,"title":"orphan","body":"b"}`,
		), inputs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"orphan" merged no input ids`)
	})

	t.Run("a degraded source contributes no ids", func(t *testing.T) {
		sources := append(synthSources(), sourceResult{
			spec:     prompt.AgentSpec{Name: "docs+tests", Lenses: []string{"docs"}},
			findings: []finding.Finding{{ID: "docs+tests-1", Sources: []string{"docs+tests"}}},
			err:      errors.New("stalled"),
		})
		_, err := s.parse(synthJSON(
			`{"merged_ids":["docs+tests-1"],"file":"a.go","line":1,"severity":"minor","confidence":50,"title":"x","body":"b"}`,
		), s.inputs(sources))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown finding id "docs+tests-1"`)
	})
}

func TestPipeline_Run_synthesis(t *testing.T) {
	t.Run("the merged set replaces the find stage's passthrough", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NoSynthesis = false
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"a.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`), Tokens: 100},
			"impl": {StructuredOutput: findingsJSON(
				`{"file":"a.go","line":11,"severity":"minor","confidence":60,"title":"same leak","lenses":["impl"]}`), Tokens: 40},
			stageSynthesis: {StructuredOutput: synthJSON(
				`{"merged_ids":["bugs-1","impl-1"],"file":"a.go","line":10,"severity":"major","confidence":90,"title":"leak","body":"b"}`,
			), Tokens: 200},
		})

		p := New(h.cfg)
		events := drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 1, "two agents' findings merged into one")
		assert.Equal(t, []string{"bugs", "impl"}, rep.Findings[0].Sources)
		assert.Equal(t, 340, rep.Stats.Tokens, "the synthesis stage's tokens count toward the run")
		require.Len(t, rep.Stats.Stages, 2)
		assert.Equal(t, stageSynthesis, rep.Stats.Stages[1].Name)
		assert.Positive(t, rep.Stats.Stages[1].DurationMS)

		var stages []string
		for _, ev := range <-events {
			if ev.Kind == EventStage {
				stages = append(stages, ev.Stage)
			}
		}
		assert.Equal(t, []string{stageFind, stageSynthesis}, stages)
	})

	t.Run("--no-synthesis passes findings through with their attribution intact", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"a.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
			"impl": {StructuredOutput: findingsJSON(
				`{"file":"b.go","line":4,"severity":"minor","confidence":60,"title":"naming","lenses":["impl"]}`)},
		})

		p := New(h.cfg)
		events := drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 2, "nothing merged, nothing dropped")
		assert.Equal(t, []string{"bugs"}, rep.Findings[0].Sources)
		assert.Equal(t, []string{"bugs"}, rep.Findings[0].Lenses)
		assert.Equal(t, []string{"impl"}, rep.Findings[1].Sources)
		require.Len(t, rep.Stats.Stages, 1, "the skipped stage records no timing")

		for _, ev := range <-events {
			assert.NotEqual(t, stageSynthesis, ev.Stage, "a skipped stage is not announced")
		}
	})

	t.Run("a synthesis failure fails the run", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NoSynthesis = false
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs":         {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"minor","confidence":50,"title":"x"}`)},
			"impl":         {StructuredOutput: findingsJSON(``)},
			stageSynthesis: {StructuredOutput: json.RawMessage(`{"findings":`)},
		})

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode synthesis output")
		assert.Empty(t, rep.Findings, "an unsynthesized report must not ship as a synthesized one")
	})
}

// synthesisMarker is a line only the synthesis prompt carries, so the harness runner can tell the
// stage apart from a roster agent without the caller labeling every request.
const synthesisMarker = "merging a review panel"

// stageRunner answers whatever asks it and records the request, for a synthesizer driven directly.
func stageRunner(got *executor.Request, res executor.Result, err error) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
			*got = req
			return res, err
		}}
	}
}

// synthSources is one find stage's outcome: an agent carrying two lenses and a codex peer, both
// reporting on the same file so a merge across sources and a merge within one agent are both testable.
func synthSources() []sourceResult {
	return []sourceResult{
		{
			spec: prompt.AgentSpec{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude"},
			stat: finding.SourceStat{Name: "bugs+impl"},
			findings: []finding.Finding{
				{ID: "bugs+impl-1", File: "a.go", Line: 10, Severity: finding.Major, Confidence: 80,
					Title: "leak", Sources: []string{"bugs+impl"}, Lenses: []string{"bugs"}},
				{ID: "bugs+impl-2", File: "a.go", Line: 11, Severity: finding.Minor, Confidence: 60,
					Title: "same leak", Sources: []string{"bugs+impl"}, Lenses: []string{"impl"}},
			},
		},
		{
			spec: prompt.AgentSpec{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex"},
			stat: finding.SourceStat{Name: "codex"},
			findings: []finding.Finding{
				{ID: "codex-1", File: "a.go", Line: 10, Severity: finding.Critical, Confidence: 70,
					Title: "leak again", Sources: []string{"codex"}, Lenses: []string{"adversarial"}},
			},
		},
	}
}

// synthJSON wraps zero or more merged findings in the synthesis schema's three-list envelope.
func synthJSON(items ...string) json.RawMessage {
	return json.RawMessage(`{"findings":[` + strings.Join(items, ",") + `],"open_questions":[],"pre_existing":[]}`)
}
