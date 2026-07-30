package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaults_EveryShippedFileHasADescription(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, l := range set.Lenses() {
		assert.NotEmpty(t, l.Description, "lens %s", l.Name)
	}
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		assert.NotEmpty(t, p.Description, "profile %s", name)
	}
	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		assert.NotEmpty(t, st.Description, "stage %s", name)
	}
}

func TestDefaults_FocusedRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("focused")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	require.Len(t, specs, 2)

	assert.Equal(t, AgentSpec{Name: "bugs", Lenses: []string{"bugs"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6", ColorName: "cyan"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "high", Color: "3", ColorName: "yellow"}, specs[1],
		"codex is a roster entry composing a lens, not a prompt file of its own")
}

func TestDefaults_ProfileBodyNamesEveryContextPathAsAPath(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)

		for _, v := range []string{"SCOPE", "GOAL", "PROFILE", "CONTEXT", "WORKDIR"} {
			assert.Contains(t, p.Body, "{{"+v+"}}", "%s: a variable the caller resolves must appear in the body", name)
		}
		assert.Contains(t, strings.ToLower(p.Body), "a **path**, not the text it names",
			"%s: a path handed to an agent with no instruction is a path it may ignore", name)
		assert.Contains(t, p.Body, "none provided", "%s: the body must say what an absent context file looks like", name)
	}
}

func TestDefaults_EveryProfileResolvesItsRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	known := set.LensNames()
	require.Len(t, set.ProfileNames(), 5, "the shipped set is comprehensive, focused, final, claude-only and codex-only")

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		specs, err := p.Roster(nil, known)
		require.NoError(t, err, "%s", name)
		require.NotEmpty(t, specs, "%s", name)

		colors := map[string]string{}
		for _, spec := range specs {
			assert.NotEmpty(t, spec.ColorName, "%s/%s: a shipped roster names its own color rather than leaning on the palette",
				name, spec.Name)
			assert.NotContains(t, colors, spec.Color, "%s: %s and %s share a color", name, colors[spec.Color], spec.Name)
			colors[spec.Color] = spec.Name
			assert.NotEmpty(t, spec.Model, "%s/%s: a roster entry with no model runs on whatever the binary defaults to",
				name, spec.Name)
			assert.NotEmpty(t, spec.Effort, "%s/%s: effort must resolve from the profile when the entry omits it",
				name, spec.Name)
			for _, l := range spec.Lenses {
				assert.Contains(t, known, l, "%s/%s: unknown lens %s", name, spec.Name, l)
			}
		}
	}
}

func TestDefaults_EveryProfileResolvesEveryStage(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		for _, stage := range []string{"synthesis", "verify"} {
			st, err := p.Stage(set, stage)
			require.NoError(t, err, "%s/%s", name, stage)
			assert.NotEmpty(t, st.Executor, "%s/%s", name, stage)
			assert.NotEmpty(t, st.Model, "%s/%s: a stage with no model runs on whatever the binary defaults to",
				name, stage)
			assert.NotEmpty(t, st.Body, "%s/%s: the body is the stage file's whether or not the profile overrides",
				name, stage)
		}
	}
}

// codex-only is the profile that would silently be two thirds claude if a profile could not name its
// stages, so it pins the whole run on one binary rather than only the find stage
func TestDefaults_CodexOnlyRunsEveryStageOnCodex(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("codex-only")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	for _, spec := range specs {
		assert.Equal(t, "codex", spec.Executor, spec.Name)
	}

	for _, stage := range []string{"synthesis", "verify"} {
		st, stErr := p.Stage(set, stage)
		require.NoError(t, stErr)
		assert.Equal(t, "codex", st.Executor, stage)
		assert.Equal(t, "gpt-5.6-sol", st.Model, "%s inherits the profile's own model", stage)
		assert.Equal(t, "high", st.Effort, "%s inherits the profile's own effort", stage)
	}

	// --lenses replaces the roster with one agent inheriting the profile's model, so an executor
	// hardcoded to claude here would ask claude for a codex model and would need the binary this
	// profile exists to do without
	override, err := p.Roster([]string{"bugs"}, set.LensNames())
	require.NoError(t, err)
	require.Len(t, override, 1)
	assert.Equal(t, "codex", override[0].Executor)
	assert.Equal(t, "gpt-5.6-sol", override[0].Model)

	claude, err := set.Profile("claude-only")
	require.NoError(t, err)
	for _, stage := range []string{"synthesis", "verify"} {
		st, stErr := claude.Stage(set, stage)
		require.NoError(t, stErr)
		assert.Equal(t, "claude", st.Executor, "%s: the mirror profile names no override and keeps the file's own", stage)
	}
}

func TestDefaults_ComprehensiveRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("comprehensive")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	require.Len(t, specs, 4)

	assert.Equal(t, AgentSpec{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6", ColorName: "cyan"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "arch+quality", Lenses: []string{"architecture", "quality"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "5", ColorName: "magenta"}, specs[1])
	assert.Equal(t, AgentSpec{Name: "docs+tests", Lenses: []string{"docs", "tests"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "2", ColorName: "green"}, specs[2])
	assert.Equal(t, AgentSpec{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "high", Color: "3", ColorName: "yellow"}, specs[3],
		"codex is a peer source in the default roster, not a second pass over the others")

	var carried []string
	for _, spec := range specs {
		carried = append(carried, spec.Lenses...)
	}
	assert.ElementsMatch(t, []string{"bugs", "impl", "architecture", "quality", "docs", "tests", "adversarial"}, carried,
		"the flagship profile carries every shipped lens exactly once")
}

func TestDefaults_FinalReportsOnlyTheTopTwoSeverities(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("final")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	assert.Len(t, specs, 2, "the last pass before merge is narrow by design")

	body := strings.ToLower(p.Body)
	assert.Contains(t, body, "critical")
	assert.Contains(t, body, "major")
	assert.NotContains(t, body, "**minor**",
		"a profile that still defines minor as a severity will be handed minor findings")
}

func TestDefaults_LensesAreSelfContained(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	lenses := set.Lenses()
	require.Len(t, lenses, 7, "the shipped set is bugs, impl, architecture, quality, docs, tests and adversarial")

	for _, l := range lenses {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		assert.NotEmpty(t, strings.TrimSpace(body), "lens %s is empty", l.Name)
		assert.Empty(t, varPattern.FindString(body),
			"lens %s names a variable; context instructions are shared preamble and belong in the profile body", l.Name)
		assert.Contains(t, body, "## Lens: "+l.Name, "lens %s must say which lens raised a finding", l.Name)
	}
}

func TestDefaults_NoShippedFileCarriesThePriorRoundBlock(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	bodies := map[string]string{}
	for _, l := range set.Lenses() {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		bodies["lenses/"+l.Name] = body
	}
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		bodies["profiles/"+name] = p.Body
	}
	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		bodies["stages/"+name] = st.Body
	}

	for path, body := range bodies {
		lower := strings.ToLower(body)
		for _, phrase := range []string{"prior round", "previous round", "earlier round", "runs/", "re-evaluate everything"} {
			assert.NotContains(t, lower, phrase,
				"%s mentions %q; the history block is injected, and a copy here drifts from the injected text", path, phrase)
		}
	}
}

func TestDefaults_LensesStayExecutorAgnostic(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, l := range set.Lenses() {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		lower := strings.ToLower(body)
		for _, banned := range []string{"json", "schema", "claude", "codex"} {
			assert.NotContains(t, lower, banned,
				"lens %s must not carry an output contract or name a binary", l.Name)
		}
	}
}

func TestDefaults_ComposeEveryAgentOfEveryProfile(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	vars := Vars{"SCOPE": "/s.md", "GOAL": "none provided", "PROFILE": "none provided",
		"CONTEXT": "none provided", "WORKDIR": "/repo", "FINDINGS": "[]", "SOURCES": "2 of 2"}

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		specs, err := p.Roster(nil, set.LensNames())
		require.NoError(t, err)
		for _, spec := range specs {
			out, err := p.Compose(set, spec, ComposeOpts{Vars: vars})
			require.NoError(t, err, "%s/%s", name, spec.Name)
			assert.NotEmpty(t, out)
		}
	}

	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		out, err := st.Compose(ComposeOpts{Vars: vars})
		require.NoError(t, err, name)
		assert.NotEmpty(t, out)
	}
}
