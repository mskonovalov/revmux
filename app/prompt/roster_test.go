package prompt

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadProfile builds a one-profile tree over the standard lens set and returns the parsed profile.
func loadProfile(t *testing.T, frontMatter string) (*Set, *Profile) {
	t.Helper()
	project := writeTree(t, t.TempDir(), map[string]string{
		"prompts/profiles/custom.md": "---\n" + frontMatter + "---\nprofile body",
	})
	set, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)
	p, err := set.Profile("custom")
	require.NoError(t, err)
	return set, p
}

func TestParseProfile_DefaultsAndOverrides(t *testing.T) {
	_, p := loadProfile(t, `description: a custom roster
model: opus
effort: high
agents:
  - {name: inherits, lenses: [bugs]}
  - {name: overrides, lenses: [bugs, adversarial], executor: codex, model: gpt-5.6-sol, effort: xhigh}
`)

	assert.Equal(t, "a custom roster", p.Description)
	assert.Equal(t, "profile body", p.Body)

	specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}, "adversarial": {}})
	require.NoError(t, err)
	require.Len(t, specs, 2)

	assert.Equal(t, AgentSpec{Name: "inherits", Lenses: []string{"bugs"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "overrides", Lenses: []string{"bugs", "adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "xhigh", Color: "5"}, specs[1])
}

func TestProfile_ValidationErrors(t *testing.T) {
	tests := []struct {
		name, frontMatter, want string
	}{
		{"unknown executor", "agents:\n  - {name: a, lenses: [bugs], executor: gemini}\n", `unknown executor "gemini"`},
		{"unknown effort", "agents:\n  - {name: a, lenses: [bugs], effort: turbo}\n", `unknown effort "turbo"`},
		{"missing lens", "agents:\n  - {name: a, lenses: [nosuch]}\n", `unknown lens "nosuch"`},
		{"no lenses", "agents:\n  - {name: a, lenses: []}\n", "no lenses"},
		{"no name", "agents:\n  - {lenses: [bugs]}\n", "roster entry has no name"},
		{"duplicate name", "agents:\n  - {name: a, lenses: [bugs]}\n  - {name: a, lenses: [adversarial]}\n", `duplicate agent name "a"`},
		{"bad color name", "agents:\n  - {name: a, lenses: [bugs], color: puce}\n", `invalid color "puce"`},
		{"numeric color", "agents:\n  - {name: a, lenses: [bugs], color: \"12\"}\n", `invalid color "12"`},
		{"short hex color", "agents:\n  - {name: a, lenses: [bugs], color: \"#fff\"}\n", `invalid color "#fff"`},
		{"empty roster", "description: x\n", "roster is empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := writeTree(t, t.TempDir(), map[string]string{
				"prompts/profiles/custom.md": "---\n" + tt.frontMatter + "---\nbody",
			})
			_, err := Load(LoadOpts{ProjectDir: project})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestProfile_Roster_Colors(t *testing.T) {
	t.Run("every ansi name resolves to its index", func(t *testing.T) {
		for name, idx := range ansiColors {
			_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs], color: "+name+"}\n")
			specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
			require.NoError(t, err, name)
			assert.Equal(t, name, specs[0].ColorName)
			assert.Equal(t, strconv.Itoa(idx), specs[0].Color, name)
		}
	})

	t.Run("hex survives to the spec", func(t *testing.T) {
		_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs], color: \"#A1b2C3\"}\n")
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		assert.Equal(t, "#A1b2C3", specs[0].Color)
		assert.Equal(t, "#A1b2C3", specs[0].ColorName)
	})

	t.Run("omitted is filled by position and stable across loads", func(t *testing.T) {
		fm := "agents:\n  - {name: a, lenses: [bugs]}\n  - {name: b, lenses: [bugs]}\n  - {name: c, lenses: [bugs]}\n"
		known := map[string]struct{}{"bugs": {}}

		_, first := loadProfile(t, fm)
		firstSpecs, err := first.Roster(nil, known)
		require.NoError(t, err)
		assert.Equal(t, []string{"6", "5", "2"}, []string{firstSpecs[0].Color, firstSpecs[1].Color, firstSpecs[2].Color})
		assert.Empty(t, firstSpecs[0].ColorName, "a palette color was never authored")

		_, second := loadProfile(t, fm)
		secondSpecs, err := second.Roster(nil, known)
		require.NoError(t, err)
		assert.Equal(t, firstSpecs, secondSpecs)
	})

	t.Run("palette wraps past its length", func(t *testing.T) {
		fm := strings.Builder{}
		fm.WriteString("agents:\n")
		for i := range len(colorPalette) + 1 {
			fm.WriteString("  - {name: a" + strconv.Itoa(i) + ", lenses: [bugs]}\n")
		}
		_, p := loadProfile(t, fm.String())
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		assert.Equal(t, specs[0].Color, specs[len(colorPalette)].Color)
	})
}

func TestProfile_Roster_LensOverride(t *testing.T) {
	known := map[string]struct{}{"bugs": {}, "adversarial": {}}
	_, p := loadProfile(t, `model: opus
effort: high
agents:
  - {name: bugs, lenses: [bugs], color: red}
  - {name: codex, executor: codex, lenses: [adversarial]}
`)

	specs, err := p.Roster([]string{"bugs", "adversarial"}, known)
	require.NoError(t, err)
	require.Len(t, specs, 1, "two lenses yield one agent carrying both, not two sources")
	assert.Equal(t, AgentSpec{Name: "lenses", Lenses: []string{"bugs", "adversarial"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6"}, specs[0])
}

func TestProfile_Roster_LensOverrideRejectsUnknownLens(t *testing.T) {
	_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")
	_, err := p.Roster([]string{"bugs", "nosuch"}, map[string]struct{}{"bugs": {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown lens "nosuch"`)
}

func TestProfile_Roster_DoesNotAliasTheProfile(t *testing.T) {
	known := map[string]struct{}{"bugs": {}}
	_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")

	specs, err := p.Roster(nil, known)
	require.NoError(t, err)
	specs[0].Lenses[0] = "mutated"
	specs[0].Color = "#000000"

	again, err := p.Roster(nil, known)
	require.NoError(t, err)
	assert.Equal(t, []string{"bugs"}, again[0].Lenses)
	assert.Equal(t, "6", again[0].Color)
}

func TestStage_FrontMatter(t *testing.T) {
	t.Run("parsed", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/synthesis.md": "---\ndescription: d\nexecutor: codex\nmodel: gpt-5.6-sol\neffort: xhigh\n---\nbody",
		})
		set, err := Load(LoadOpts{ProjectDir: project})
		require.NoError(t, err)

		st, err := set.Stage("synthesis")
		require.NoError(t, err)
		assert.Equal(t, "d", st.Description)
		assert.Equal(t, "codex", st.Executor)
		assert.Equal(t, "gpt-5.6-sol", st.Model)
		assert.Equal(t, "xhigh", st.Effort)
	})

	tests := []struct {
		name, frontMatter, want string
	}{
		{"unknown executor", "executor: gemini\n", `stage synthesis: unknown executor "gemini"`},
		{"unknown effort", "effort: turbo\n", `stage synthesis: unknown effort "turbo"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := writeTree(t, t.TempDir(), map[string]string{
				"prompts/synthesis.md": "---\n" + tt.frontMatter + "---\nbody",
			})
			_, err := Load(LoadOpts{ProjectDir: project})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestVocabularies(t *testing.T) {
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, Efforts())
	assert.Equal(t, []string{"claude", "codex"}, Executors())

	Efforts()[0] = "mutated"
	Executors()[0] = "mutated"
	assert.Equal(t, "low", Efforts()[0], "the accessor must not hand out the package slice")
	assert.Equal(t, "claude", Executors()[0])
}
