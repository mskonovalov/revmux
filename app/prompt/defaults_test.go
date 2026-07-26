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
		Model: "gpt-5.6-sol", Effort: "xhigh", Color: "3", ColorName: "yellow"}, specs[1],
		"codex is a roster entry composing a lens, not a prompt file of its own")
}

func TestDefaults_ProfileBodyNamesEveryContextPathAsAPath(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("focused")
	require.NoError(t, err)

	for _, v := range []string{"SCOPE", "GOAL", "PROFILE", "CONTEXT", "WORKDIR"} {
		assert.Contains(t, p.Body, "{{"+v+"}}", "a variable the caller resolves must appear in the body")
	}
	assert.Contains(t, strings.ToLower(p.Body), "a **path**, not the text it names",
		"a path handed to an agent with no instruction is a path it may ignore")
	assert.Contains(t, p.Body, "none provided", "the body must say what an absent context file looks like")
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
