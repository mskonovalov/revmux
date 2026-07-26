package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/prompt"
)

func TestOptions_knobs(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "keep-runs = 3\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--stagger-delay", "5s"})
	require.NoError(t, err)

	got := map[string]knob{}
	for _, k := range o.knobs() {
		got[k.Name] = k
	}

	require.Len(t, got, len(knobNames()), "every knob is reported")
	assert.Equal(t, knob{Name: "stagger-delay", Value: "5s", Source: originFlag}, got["stagger-delay"])
	assert.Equal(t, knob{Name: "hard-timeout", Value: (20 * time.Minute).String(), Source: originDefault}, got["hard-timeout"])
	assert.Equal(t, knob{Name: "keep-runs", Value: 3, Source: originUser}, got["keep-runs"])
	assert.Equal(t, knob{Name: "max-parallel", Value: 4, Source: originDefault}, got["max-parallel"])
	assert.Equal(t, knob{Name: "profile", Value: "comprehensive", Source: originDefault}, got["profile"])

	t.Run("no meta flag is reported as a knob", func(t *testing.T) {
		for _, k := range o.knobs() {
			assert.NotContains(t, []string{"task", "run", "json", "config-dir", "version"}, k.Name)
		}
	})
}

func TestOptions_catalogReportsTheResolvedTree(t *testing.T) {
	cfg := t.TempDir()
	lenses := filepath.Join(cfg, "lenses")
	require.NoError(t, os.MkdirAll(lenses, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(lenses, "bugs.md"),
		[]byte("---\ndescription: my own take on bugs\n---\nlook for bugs\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(lenses, "perf.md"),
		[]byte("---\ndescription: hot paths and allocations\n---\nlook for slowness\n"), 0o600))

	o := options{layers: configLayers{user: cfg}}
	set, err := prompt.Load(o.promptOpts())
	require.NoError(t, err)

	got := map[string]string{}
	for _, l := range o.catalog(set).Lenses {
		got[l.Name] = l.Description
	}

	assert.Equal(t, "my own take on bugs", got["bugs"], "an override is different text, never the default's summary")
	assert.Equal(t, "hot paths and allocations", got["perf"], "an added lens is composable, so it must be listed")
	assert.Contains(t, got, "tests", "overriding one lens does not orphan the others")
}

func TestOptions_catalogVocabulariesComeFromPrompt(t *testing.T) {
	set, err := prompt.Load(prompt.LoadOpts{})
	require.NoError(t, err)

	// compared against the accessors rather than a literal: a literal here would only assert that two
	// hardcoded lists agree, which is the drift the accessors exist to prevent
	c := options{}.catalog(set)
	assert.Equal(t, prompt.Executors(), c.Vocabulary.Executors)
	assert.Equal(t, prompt.Efforts(), c.Vocabulary.Efforts)
}

func TestOptions_catalogStages(t *testing.T) {
	set, err := prompt.Load(prompt.LoadOpts{})
	require.NoError(t, err)

	stages := map[string]stageInfo{}
	for _, s := range (options{}).catalog(set).Stages {
		stages[s.Name] = s
	}

	require.Len(t, stages, 2)
	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		assert.Equal(t, stageInfo{Name: name, Description: st.Description,
			Executor: st.Executor, Model: st.Model, Effort: st.Effort}, stages[name])
		assert.NotEmpty(t, stages[name].Description, "a stage with no description hides which model judges the findings")
	}
}

func TestOptions_paths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pr-1", "pr-2"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o750))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o600))
	work := t.TempDir()

	o := options{TasksDir: root, WorkDir: work, layers: configLayers{project: "/p", user: "/u"}}
	got := o.paths()

	assert.Equal(t, root, got.TasksDir)
	assert.Equal(t, work, got.WorkDir)
	assert.Equal(t, "/u", got.ConfigDir)
	assert.Equal(t, "/p", got.ProjectDir)
	assert.Equal(t, []string{"pr-1", "pr-2"}, got.Tasks, "only directories are tasks, and a --run collides with an existing round")

	t.Run("a relative tasks root resolves against the working directory", func(t *testing.T) {
		dir := isolate(t)
		got := options{TasksDir: "./.revmux/tasks"}.paths()
		assert.Equal(t, filepath.Join(dir, ".revmux", "tasks"), got.TasksDir)
		assert.Empty(t, got.Tasks, "a clean install has no tasks and is not an error")
	})

	t.Run("an unresolvable workdir falls back to the raw value", func(t *testing.T) {
		got := options{TasksDir: root, WorkDir: filepath.Join(root, "absent")}.paths()
		assert.Equal(t, filepath.Join(root, "absent"), got.WorkDir)
	})
}
