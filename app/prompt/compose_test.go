package prompt

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// composeTree builds a tree whose profile and lenses are known text, so a composed prompt can be
// asserted exactly rather than by fragment.
func composeTree(t *testing.T, files map[string]string) *Set {
	t.Helper()
	tree := map[string]string{
		"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs, adversarial]}\n---\nPROFILE BODY",
		"lenses/bugs.md":             "BUGS LENS",
		"lenses/adversarial.md":      "ADVERSARIAL LENS",
	}
	maps.Copy(tree, files)
	set, err := Load(LoadOpts{ProjectDir: writeTree(t, t.TempDir(), tree)})
	require.NoError(t, err)
	return set
}

func composeAgent(t *testing.T, set *Set, opts ComposeOpts) string {
	t.Helper()
	p, err := set.Profile("custom")
	require.NoError(t, err)
	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	out, err := p.Compose(set, specs[0], opts)
	require.NoError(t, err)
	return out
}

func TestProfile_Compose(t *testing.T) {
	set := composeTree(t, nil)
	assert.Equal(t, "PROFILE BODY\n\nBUGS LENS\n\nADVERSARIAL LENS", composeAgent(t, set, ComposeOpts{}))
}

func TestProfile_Compose_Substitution(t *testing.T) {
	set := composeTree(t, map[string]string{
		"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\n" +
			"scope {{SCOPE}} goal {{GOAL}} profile {{PROFILE}} context {{CONTEXT}} workdir {{WORKDIR}}",
		"lenses/bugs.md": "lens repeats {{SCOPE}}",
	})

	out := composeAgent(t, set, ComposeOpts{Vars: Vars{
		"SCOPE": "/abs/scope.md", "GOAL": "none provided", "PROFILE": "/abs/profile.md",
		"CONTEXT": "/abs/context", "WORKDIR": "/abs/repo",
	}})

	assert.Equal(t, "scope /abs/scope.md goal none provided profile /abs/profile.md "+
		"context /abs/context workdir /abs/repo\n\nlens repeats /abs/scope.md", out)
}

func TestProfile_Compose_UnresolvedVariableFails(t *testing.T) {
	set := composeTree(t, map[string]string{
		"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\nread {{SCOPE}} and {{GOAL}}",
	})

	p, err := set.Profile("custom")
	require.NoError(t, err)
	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)

	_, err = p.Compose(set, specs[0], ComposeOpts{Vars: Vars{"SCOPE": "/abs/scope.md"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved variable {{GOAL}}")
	assert.Contains(t, err.Error(), "agent a")
}

func TestProfile_Compose_UnknownLens(t *testing.T) {
	set := composeTree(t, nil)
	p, err := set.Profile("custom")
	require.NoError(t, err)

	_, err = p.Compose(set, AgentSpec{Name: "ghost", Lenses: []string{"nosuch"}}, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent ghost: unknown lens "nosuch"`)
}

func TestCompose_History(t *testing.T) {
	inventory := "Prior rounds for this task: /abs/tasks/pr-123/\n  round-1  2026-07-26T14:30Z  8 findings  sources 4/4"

	t.Run("reaches a prompt that never mentions it", func(t *testing.T) {
		set := composeTree(t, nil)
		out := composeAgent(t, set, ComposeOpts{History: inventory})

		assert.Contains(t, out, inventory)
		assert.Contains(t, out, "Re-evaluate everything independently",
			"the guard travels with the data, so no lens or overridden profile can drop it")
		assert.True(t, strings.HasPrefix(out, "PROFILE BODY"), "the block is appended, never prepended")
	})

	t.Run("absent on a first round", func(t *testing.T) {
		set := composeTree(t, nil)
		out := composeAgent(t, set, ComposeOpts{})
		assert.NotContains(t, out, "Prior rounds")
		assert.NotContains(t, out, "Re-evaluate everything independently")
	})

	t.Run("stage prompts receive it too", func(t *testing.T) {
		set := composeTree(t, map[string]string{"prompts/synthesis.md": "SYNTH BODY"})
		st, err := set.Stage("synthesis")
		require.NoError(t, err)

		out, err := st.Compose(ComposeOpts{History: inventory})
		require.NoError(t, err)
		assert.Contains(t, out, "SYNTH BODY")
		assert.Contains(t, out, inventory)
		assert.Contains(t, out, "Re-evaluate everything independently")
	})
}

func TestStage_Compose(t *testing.T) {
	set := composeTree(t, map[string]string{"prompts/synthesis.md": "sources {{SOURCES}} findings {{FINDINGS}}"})
	st, err := set.Stage("synthesis")
	require.NoError(t, err)

	out, err := st.Compose(ComposeOpts{Vars: Vars{"SOURCES": "3 of 4", "FINDINGS": "[]"}})
	require.NoError(t, err)
	assert.Equal(t, "sources 3 of 4 findings []", out)

	_, err = st.Compose(ComposeOpts{Vars: Vars{"SOURCES": "3 of 4"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stage synthesis: unresolved variable {{FINDINGS}}")
}

func TestCompose_SubstitutedValueIsNotRescanned(t *testing.T) {
	// findings text is model-authored and routinely quotes template syntax, so a value carrying
	// {{token}} must not read as an unresolved variable of the prompt that received it
	findings := `[{"title": "unescaped {{message}} in template", "file": "web/tpl.html"}]`

	t.Run("stage", func(t *testing.T) {
		set := composeTree(t, map[string]string{"prompts/synthesis.md": "sources {{SOURCES}} findings {{FINDINGS}}"})
		st, err := set.Stage("synthesis")
		require.NoError(t, err)

		out, err := st.Compose(ComposeOpts{Vars: Vars{"SOURCES": "3 of 4", "FINDINGS": findings}})
		require.NoError(t, err)
		assert.Contains(t, out, "{{message}}", "the injected value survives verbatim")
	})

	t.Run("agent", func(t *testing.T) {
		set := composeTree(t, map[string]string{
			"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\nread {{SCOPE}}",
			"lenses/bugs.md":             "lens",
		})
		p, err := set.Profile("custom")
		require.NoError(t, err)
		specs, err := p.Roster(nil, set.LensNames())
		require.NoError(t, err)

		out, err := p.Compose(set, specs[0], ComposeOpts{Vars: Vars{"SCOPE": "/abs/{{weird}}/scope.md"}})
		require.NoError(t, err)
		assert.Contains(t, out, "/abs/{{weird}}/scope.md")
	})
}
