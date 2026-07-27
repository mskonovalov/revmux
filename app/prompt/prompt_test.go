package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree materializes a prompt tree under root; keys are slash-separated relative paths.
func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	return root
}

func TestLoad_EmbeddedDefaults(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	assert.Equal(t, []string{"comprehensive", "final", "focused"}, set.ProfileNames())
	for _, l := range set.Lenses() {
		assert.NotEmpty(t, l.Description, "lens %s", l.Name)
	}

	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err, name)
		assert.Equal(t, "claude", st.Executor, "omitted executor defaults to claude")
		assert.NotEmpty(t, st.Body, name)
	}

	assert.Equal(t, map[string]struct{}{"adversarial": {}, "architecture": {}, "bugs": {}, "docs": {},
		"impl": {}, "quality": {}, "tests": {}}, set.LensNames())
}

func TestLoad_MissingDirsAreAbsentLayers(t *testing.T) {
	set, err := Load(LoadOpts{ProjectDir: filepath.Join(t.TempDir(), "nope"), UserDir: filepath.Join(t.TempDir(), "also-nope")})
	require.NoError(t, err)
	assert.Equal(t, []string{"comprehensive", "final", "focused"}, set.ProfileNames())

	for _, o := range set.Provenance() {
		assert.Equal(t, LayerEmbedded, o.Layer, o.Path)
	}
}

func TestLoad_Precedence(t *testing.T) {
	user := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":       "---\ndescription: user bugs\n---\nuser bugs body",
		"lenses/user-only.md":  "---\ndescription: only the user has this\n---\nuser only body",
		"prompts/synthesis.md": "---\ndescription: user synthesis\n---\nuser synthesis body",
	})
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md": "---\ndescription: project bugs\n---\nproject bugs body",
	})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	body, err := set.lens("bugs")
	require.NoError(t, err)
	assert.Equal(t, "project bugs body", body, "project beats user")

	body, err = set.lens("user-only")
	require.NoError(t, err)
	assert.Equal(t, "user only body", body, "a lens only the user has must appear")

	body, err = set.lens("adversarial")
	require.NoError(t, err)
	assert.Contains(t, body, "Lens: adversarial", "an un-overridden lens keeps the embedded text")

	st, err := set.Stage("synthesis")
	require.NoError(t, err)
	assert.Equal(t, "user synthesis body", st.Body, "user beats embedded")

	p, err := set.Profile("focused")
	require.NoError(t, err)
	assert.Contains(t, p.Body, "Severity bar", "overriding one lens does not orphan the profile")
}

func TestLoad_Provenance(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md": "---\ndescription: project bugs\n---\nproject bugs body",
	})
	user := writeTree(t, t.TempDir(), map[string]string{
		"prompts/verify.md": "---\ndescription: user verify\n---\nuser verify body",
	})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	byPath := map[string]FileOrigin{}
	for _, o := range set.Provenance() {
		byPath[o.Path] = o
	}

	assert.Equal(t, LayerProject, byPath["lenses/bugs.md"].Layer)
	assert.Equal(t, filepath.Join(project, "lenses", "bugs.md"), byPath["lenses/bugs.md"].Source)
	assert.Equal(t, LayerUser, byPath["prompts/verify.md"].Layer)
	assert.Equal(t, LayerEmbedded, byPath["lenses/adversarial.md"].Layer)
	assert.Empty(t, byPath["lenses/adversarial.md"].Source, "an embedded file has no on-disk source")
	assert.Len(t, byPath["lenses/bugs.md"].Hash, 64)

	first := byPath["lenses/bugs.md"].Hash
	writeTree(t, project, map[string]string{"lenses/bugs.md": "---\ndescription: project bugs\n---\nedited body"})
	set, err = Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)
	for _, o := range set.Provenance() {
		if o.Path == "lenses/bugs.md" {
			assert.NotEqual(t, first, o.Hash, "editing an override must change its hash")
		}
	}
}

func TestSet_LensDescriptionIsNotInherited(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":        "---\ndescription: my own bugs lens\n---\nbody",
		"lenses/adversarial.md": "no front matter at all",
	})

	set, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)

	got := map[string]string{}
	for _, l := range set.Lenses() {
		got[l.Name] = l.Description
	}
	assert.Equal(t, "my own bugs lens", got["bugs"], "an override reports its own description, never the embedded default's")
	assert.Empty(t, got["adversarial"], "an override with no front matter reports no description rather than inheriting one")
	assert.NotEmpty(t, got["quality"], "a lens nobody overrode keeps the embedded description")
}

func TestSet_UnknownNames(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	_, err = set.Profile("nope")
	require.ErrorContains(t, err, "unknown profile")
	_, err = set.Stage("nope")
	require.ErrorContains(t, err, "unknown stage")
	_, err = set.lens("nope")
	require.ErrorContains(t, err, "unknown lens")
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unknown variable", map[string]string{"lenses/bugs.md": "read {{DIFF}} first"}, "unknown variable {{DIFF}}"},
		{"unterminated front matter", map[string]string{"lenses/bugs.md": "---\ndescription: x\nbody"}, "never closed"},
		{"bad yaml", map[string]string{"lenses/bugs.md": "---\ndescription: [1,\n---\nbody"}, "parse front matter"},
		{"unknown front matter key", map[string]string{"lenses/bugs.md": "---\ndescriptio: typo\n---\nbody"}, "parse front matter"},
		{
			"missing lens",
			map[string]string{"prompts/profiles/focused.md": "---\nagents:\n  - {name: a, lenses: [nosuch]}\n---\nbody"},
			`unknown lens "nosuch"`,
		},
		{
			"empty roster",
			map[string]string{"prompts/profiles/focused.md": "---\ndescription: x\n---\nbody"},
			"roster is empty",
		},
		{
			// it reads like the model: and effort: defaults beside it, so accepting and ignoring it would
			// run every agent on claude while the file says codex
			"profile-level executor",
			map[string]string{
				"prompts/profiles/focused.md": "---\nexecutor: codex\nagents:\n  - {name: a, lenses: [bugs]}\n---\nbody",
			},
			"parse front matter",
		},
		{
			"lens naming a runner it does not select",
			map[string]string{"lenses/bugs.md": "---\ndescription: x\nmodel: opus\n---\nbody"},
			"parse front matter",
		},
		{
			"stage declaring a roster it cannot have",
			map[string]string{"prompts/verify.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\nbody"},
			"parse front matter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(LoadOpts{ProjectDir: writeTree(t, t.TempDir(), tt.files)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoad_StageVariablesAreAllowed(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"prompts/synthesis.md": "---\ndescription: x\n---\n{{SOURCES}} then {{FINDINGS}}",
	})
	_, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)
}
