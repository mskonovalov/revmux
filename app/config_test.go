package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/prompt"
)

func TestParseArgs_defaults(t *testing.T) {
	home := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(home, "cfg")})
	require.NoError(t, err)

	assert.Equal(t, "pr-1", o.Task)
	assert.Equal(t, 2*time.Minute, o.IdleTimeout)
	assert.Equal(t, 20*time.Minute, o.HardTimeout)
	assert.Equal(t, 30*time.Second, o.StaggerDelay)
	assert.Equal(t, 4, o.MaxParallel)
	assert.Equal(t, 6, o.VerifyGroups)
	assert.Equal(t, "./.revmux/tasks", o.TasksDir)
	assert.Equal(t, 10, o.KeepRuns)
	assert.Equal(t, "comprehensive", o.Profile)
	assert.Equal(t, 0, o.MinConfidence)
}

func TestParseArgs_cleanInstallHasNoZeroKnob(t *testing.T) {
	home := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(home, "cfg")})
	require.NoError(t, err)

	assert.Positive(t, o.IdleTimeout, "a zero idle timeout is a disabled watchdog")
	assert.Positive(t, o.HardTimeout, "a zero hard timeout is a disabled watchdog")
	assert.GreaterOrEqual(t, o.MaxParallel, 1, "a zero max-parallel is a zero-capacity semaphore")
	assert.GreaterOrEqual(t, o.KeepRuns, 1)
	assert.GreaterOrEqual(t, o.VerifyGroups, 1)

	set, err := o.promptSet()
	require.NoError(t, err, "the default profile must resolve on a clean install")
	require.NotNil(t, set)
}

func TestParseArgs_lensesAcceptCommasAndRepetition(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "cfg")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "comma separated", args: []string{"--lenses", "bugs,impl"}, want: []string{"bugs", "impl"}},
		{name: "repeated", args: []string{"--lenses", "bugs", "--lenses", "impl"}, want: []string{"bugs", "impl"}},
		{name: "mixed with spaces", args: []string{"--lenses", "bugs, impl", "--lenses", "docs"}, want: []string{"bugs", "impl", "docs"}},
		{name: "absent", args: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parseArgs(append([]string{"--task", "pr-1", "--config-dir", cfg}, tt.args...))
			require.NoError(t, err)
			assert.Equal(t, tt.want, o.Lenses)
		})
	}
}

func TestParseArgs_unknownFlag(t *testing.T) {
	isolate(t)

	_, err := parseArgs([]string{"--nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse arguments")
}

func TestParseArgs_precedenceAcrossFourLayers(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "max-parallel = 9\nkeep-runs = 3\nstagger-delay = 1s\nprofile = user-profile\n")
	writeConfig(t, filepath.Join(dir, projectDirName), "max-parallel = 7\nprofile = project-profile\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--idle-timeout", "5m", "--max-parallel", "11"})
	require.NoError(t, err)

	assert.Equal(t, 11, o.MaxParallel, "the command line beats both files")
	assert.Equal(t, 5*time.Minute, o.IdleTimeout, "the command line beats the built-in default")
	assert.Equal(t, "project-profile", o.Profile, "the project layer beats the user layer")
	assert.Equal(t, 3, o.KeepRuns, "a user key the project layer does not set survives")
	assert.Equal(t, time.Second, o.StaggerDelay, "layers merge per key, not per file")
	assert.Equal(t, 20*time.Minute, o.HardTimeout, "a key no layer sets keeps its built-in default")
}

func TestParseArgs_knobOriginsNameTheWinningLayer(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "keep-runs = 3\nmax-parallel = 9\nprofile = user-profile\n")
	writeConfig(t, filepath.Join(dir, projectDirName), "max-parallel = 7\nprofile = project-profile\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--idle-timeout", "5m"})
	require.NoError(t, err)

	want := map[string]string{
		"idle-timeout":  originFlag,
		"profile":       originProject,
		"max-parallel":  originProject,
		"keep-runs":     originUser,
		"hard-timeout":  originDefault,
		"stagger-delay": originDefault,
		"verify-groups": originDefault,
		"tasks-dir":     originDefault,
	}
	assert.Equal(t, want, o.knobOrigins)
	assert.Len(t, o.knobOrigins, len(knobNames()), "every knob reports an origin")
}

func TestParseArgs_knobOriginIsNotFooledByStructDefaults(t *testing.T) {
	dir := isolate(t)

	// --profile passed with exactly its built-in value must still report as a flag
	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user"), "--profile", "focused"})
	require.NoError(t, err)
	assert.Equal(t, originFlag, o.knobOrigins["profile"])
	assert.Equal(t, originDefault, o.knobOrigins["keep-runs"])
}

func TestParseArgs_configDirOnTheProjectDirYieldsOneLayer(t *testing.T) {
	dir := isolate(t)
	project := filepath.Join(dir, projectDirName)
	writeConfig(t, project, "keep-runs = 5\n")

	t.Run("collapsed", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", project})
		require.NoError(t, err)
		assert.Empty(t, o.layers.project, "one directory must not load as two layers")
		assert.True(t, sameDir(o.layers.user, project))
		assert.Equal(t, 5, o.KeepRuns)
		assert.Equal(t, originUser, o.knobOrigins["keep-runs"])
	})

	t.Run("distinct", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user")})
		require.NoError(t, err)
		assert.True(t, sameDir(o.layers.project, project), "the project layer is auto-detected in the working directory")
		assert.Equal(t, originProject, o.knobOrigins["keep-runs"])
	})
}

func TestParseArgs_absentProjectDirDropsTheLayer(t *testing.T) {
	dir := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user")})
	require.NoError(t, err)
	assert.Empty(t, o.layers.project)
	assert.NotEmpty(t, o.layers.user, "the user layer is a location, present whether or not it holds a file")
}

func TestParseArgs_defaultUserDirComesFromHome(t *testing.T) {
	dir := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "home", ".config", "revmux"), o.layers.user)
}

func TestParseArgs_relativePathsFollowCwdNotWorkdir(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, filepath.Join(dir, projectDirName), "keep-runs = 5\n")
	elsewhere := t.TempDir()

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user"), "--workdir", elsewhere})
	require.NoError(t, err)

	assert.True(t, sameDir(o.layers.project, filepath.Join(dir, projectDirName)),
		"the project layer is auto-detected in the working directory, never under --workdir")
	assert.Equal(t, 5, o.KeepRuns)
	assert.Equal(t, "./.revmux/tasks", o.TasksDir, "--tasks-dir resolves against the same cwd")
}

func TestParseArgs_badConfigValue(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")

	t.Run("unparseable value", func(t *testing.T) {
		writeConfig(t, user, "max-parallel = lots\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lots", "a bad value is rejected, never silently defaulted")
	})

	t.Run("unknown key", func(t *testing.T) {
		writeConfig(t, user, "no-such-knob = 1\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no-such-knob")
	})

	t.Run("meta flag rejected as a key", func(t *testing.T) {
		writeConfig(t, user, "task = pr-9\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err, "a no-ini flag is not a config key")
	})
}

func TestDefaultConfig_holdsEveryKnobCommentedOut(t *testing.T) {
	body := string(defaultConfig)
	fields := map[string]reflect.StructField{}
	for f := range reflect.TypeFor[options]().Fields() {
		if long := f.Tag.Get("long"); long != "" {
			fields[long] = f
		}
	}

	for _, name := range knobNames() {
		want := "# " + name + " = " + fields[name].Tag.Get("default")
		assert.Contains(t, body, want, "knob %s must appear in the template with its default", name)
	}

	keys, err := iniKeys(filepath.Join("defaults", configFileName))
	require.NoError(t, err)
	assert.Empty(t, keys, "the template ships fully commented out, so --init can replace it on upgrade")
}

func TestKnobNames_iniNameMatchesLongName(t *testing.T) {
	for f := range reflect.TypeFor[options]().Fields() {
		if f.Tag.Get("long") == "" || f.Tag.Get("no-ini") != "" {
			continue
		}
		assert.Equal(t, f.Tag.Get("long"), f.Tag.Get("ini-name"), "field %s: a config key must match its flag", f.Name)
		assert.NotEmpty(t, f.Tag.Get("default"), "field %s: a knob with no default resolves to a zero value", f.Name)
	}
	assert.Equal(t, []string{"idle-timeout", "hard-timeout", "stagger-delay", "max-parallel",
		"verify-groups", "tasks-dir", "keep-runs", "profile"}, knobNames())
}

func TestResolveContext_shapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	full := taskDirWith(t, root, "full", "scope.md", "goal.md", "profile.md")
	require.NoError(t, os.MkdirAll(filepath.Join(full, contextDirName), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(full, contextDirName, "ticket.md"), []byte("t"), 0o600))

	scopeOnly := taskDirWith(t, root, "scope-only", "scope.md")

	taskDirWith(t, root, "no-scope", "goal.md")

	emptyScope := taskDirWith(t, root, "empty-scope")
	require.NoError(t, os.WriteFile(filepath.Join(emptyScope, scopeFileName), nil, 0o600))

	dirScope := taskDirWith(t, root, "dir-scope")
	require.NoError(t, os.Mkdir(filepath.Join(dirScope, scopeFileName), 0o750))

	emptyCtx := taskDirWith(t, root, "empty-context", "scope.md")
	require.NoError(t, os.Mkdir(filepath.Join(emptyCtx, contextDirName), 0o750))

	fileCtx := taskDirWith(t, root, "file-context", "scope.md")
	require.NoError(t, os.WriteFile(filepath.Join(fileCtx, contextDirName), []byte("x"), 0o600))

	tests := []struct {
		name    string
		task    string
		wantErr string
		check   func(t *testing.T, rc reviewContext)
	}{
		{name: "full directory", task: "full", check: func(t *testing.T, rc reviewContext) {
			assert.Equal(t, filepath.Join(full, scopeFileName), rc.Scope)
			assert.Equal(t, filepath.Join(full, goalFileName), rc.Goal)
			assert.Equal(t, filepath.Join(full, profFileName), rc.Profile)
			assert.Equal(t, filepath.Join(full, contextDirName), rc.Context)
			assert.Equal(t, full, rc.TaskDir)
		}},
		{name: "scope only", task: "scope-only", check: func(t *testing.T, rc reviewContext) {
			assert.Equal(t, filepath.Join(scopeOnly, scopeFileName), rc.Scope)
			assert.Empty(t, rc.Goal, "an absent goal is not an error")
			assert.Empty(t, rc.Profile)
			assert.Empty(t, rc.Context)
		}},
		{name: "empty context dir", task: "empty-context", check: func(t *testing.T, rc reviewContext) {
			assert.Empty(t, rc.Context, "an empty directory tells an agent less than the placeholder")
		}},
		{name: "missing scope", task: "no-scope", wantErr: "required and must not be empty"},
		{name: "empty scope", task: "empty-scope", wantErr: "required and must not be empty"},
		{name: "scope is a directory", task: "dir-scope", wantErr: "is a directory, want a file"},
		{name: "context is a file", task: "file-context", wantErr: "is a file, want a directory"},
		{name: "missing task directory", task: "nope", wantErr: "task directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := options{Task: tt.task, TasksDir: root}
			rc, err := o.resolveContext()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, rc)
		})
	}

	t.Run("tasks dir outside the working directory", func(t *testing.T) {
		taskDirWith(t, outside, "away", "scope.md")
		o := options{Task: "away", TasksDir: outside}
		rc, err := o.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(outside, "away", scopeFileName), rc.Scope)
	})
}

func TestResolveContext_rejectsEscapingNames(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	taskDirWith(t, root, "ok", "scope.md")
	taskDirWith(t, outside, "elsewhere", "scope.md")
	require.NoError(t, os.Symlink(filepath.Join(outside, "elsewhere"), filepath.Join(root, "linked")))

	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "parent escape", opts: options{Task: "../escape", TasksDir: root}, wantErr: "path separator"},
		{name: "bare parent", opts: options{Task: "..", TasksDir: root}, wantErr: "references a parent directory"},
		{name: "separator", opts: options{Task: "a/b", TasksDir: root}, wantErr: "path separator"},
		{name: "absolute", opts: options{Task: outside, TasksDir: root}, wantErr: "absolute"},
		{name: "empty", opts: options{Task: "", TasksDir: root}, wantErr: "--task is empty"},
		{name: "leading dot", opts: options{Task: ".hidden", TasksDir: root}, wantErr: "starts with a dot"},
		{name: "run escapes too", opts: options{Task: "ok", Run: "../out", TasksDir: root}, wantErr: "--run"},
		{name: "symlink out of the root", opts: options{Task: "linked", TasksDir: root}, wantErr: "escapes the tasks root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.opts.resolveContext()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolveContext_absolutePathsAndNoFileOpened(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable file, so the no-open proof needs an unprivileged user")
	}

	root := t.TempDir()
	dir := taskDirWith(t, root, "pr-1", "scope.md", "goal.md")
	require.NoError(t, os.Chmod(filepath.Join(dir, scopeFileName), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, scopeFileName), 0o600) })

	o := options{Task: "pr-1", TasksDir: filepath.Join(root, "..", filepath.Base(root))}
	rc, err := o.resolveContext()
	require.NoError(t, err, "resolution stats context files and never opens one")

	for name, path := range map[string]string{"scope": rc.Scope, "goal": rc.Goal, "taskdir": rc.TaskDir, "workdir": rc.WorkDir} {
		assert.True(t, filepath.IsAbs(path), "%s must be absolute, got %q", name, path)
	}
	for name, val := range rc.vars() {
		assert.True(t, filepath.IsAbs(val) || val == placeholderNone, "{{%s}} is %q, want a path or the placeholder", name, val)
	}

	_, readErr := os.ReadFile(rc.Scope)
	require.Error(t, readErr, "the fixture must be unreadable, or the assertion above proves nothing")
}

func TestResolveContext_workDir(t *testing.T) {
	root := t.TempDir()
	taskDirWith(t, root, "pr-1", "scope.md")
	work := t.TempDir()

	t.Run("explicit", func(t *testing.T) {
		rc, err := options{Task: "pr-1", TasksDir: root, WorkDir: work}.resolveContext()
		require.NoError(t, err)
		assert.True(t, sameDir(rc.WorkDir, work))
	})

	t.Run("defaults to the process working directory", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		rc, err := options{Task: "pr-1", TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, cwd, rc.WorkDir)
	})

	t.Run("missing", func(t *testing.T) {
		_, err := options{Task: "pr-1", TasksDir: root, WorkDir: filepath.Join(work, "nope")}.resolveContext()
		require.Error(t, err)
	})

	t.Run("not a directory", func(t *testing.T) {
		file := filepath.Join(work, "file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		_, err := options{Task: "pr-1", TasksDir: root, WorkDir: file}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
}

func TestReviewContext_vars(t *testing.T) {
	rc := reviewContext{Scope: "/t/scope.md", WorkDir: "/repo"}
	assert.Equal(t, prompt.Vars{
		"SCOPE": "/t/scope.md", "GOAL": placeholderNone, "PROFILE": placeholderNone,
		"CONTEXT": placeholderNone, "WORKDIR": "/repo",
	}, rc.vars())

	full := reviewContext{Scope: "/t/scope.md", Goal: "/t/goal.md", Profile: "/t/profile.md",
		Context: "/t/context", WorkDir: "/repo"}
	assert.Equal(t, prompt.Vars{
		"SCOPE": "/t/scope.md", "GOAL": "/t/goal.md", "PROFILE": "/t/profile.md",
		"CONTEXT": "/t/context", "WORKDIR": "/repo",
	}, full.vars())
}

func TestOptions_runName(t *testing.T) {
	clk := &mocks.ClockMock{NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) }}

	assert.Equal(t, "after-fix", options{Run: "after-fix"}.runName(clk))
	assert.Equal(t, "20260726T160211Z", options{}.runName(clk))
	assert.NoError(t, options{}.checkName("--run", options{}.runName(clk)), "the generated default must be a legal name")
}

func TestOptions_executorOpts(t *testing.T) {
	clk := &mocks.ClockMock{}
	o := options{IdleTimeout: time.Minute, HardTimeout: time.Hour, PreserveAPIKey: true, WorkDir: "/ignored"}

	got := o.executorOpts(reviewContext{WorkDir: "/resolved"}, clk)
	assert.Equal(t, executor.Opts{IdleTimeout: time.Minute, HardTimeout: time.Hour,
		WorkDir: "/resolved", PreserveAPIKey: true, Clock: clk}, got,
		"the subprocess runs where {{WORKDIR}} points, never where the raw flag does")
}

func TestOptions_promptOptsUsesResolvedLayers(t *testing.T) {
	o := options{layers: configLayers{project: "/p", user: "/u"}}
	assert.Equal(t, prompt.LoadOpts{ProjectDir: "/p", UserDir: "/u"}, o.promptOpts())
}

func TestOptions_promptSetUnknownProfile(t *testing.T) {
	_, err := options{Profile: "nope"}.promptSet()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve profile")
}

func TestOptions_initConfig(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, projectDirName, configFileName)
	out := &strings.Builder{}

	require.NoError(t, options{}.initConfig(out))
	written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	assert.Equal(t, defaultConfig, written)
	assert.Contains(t, out.String(), "wrote")

	t.Run("comment-only file is replaced", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte("# only a comment\n"), 0o600))
		out := &strings.Builder{}
		require.NoError(t, options{}.initConfig(out))
		written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, defaultConfig, written)
	})

	t.Run("customized file is left alone", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte("keep-runs = 42\n"), 0o600))
		out := &strings.Builder{}
		require.NoError(t, options{}.initConfig(out))
		written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, "keep-runs = 42\n", string(written))
		assert.Contains(t, out.String(), "customized")
	})
}

func TestOptions_dumpDefaults(t *testing.T) {
	dir := t.TempDir()
	out := &strings.Builder{}

	require.NoError(t, options{DumpDefaults: dir}.dumpDefaults(out))
	for _, rel := range []string{"prompts/profiles/focused.md", "prompts/synthesis.md", "prompts/verify.md",
		"lenses/bugs.md", "lenses/adversarial.md"} {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	assert.NotContains(t, out.String(), "defaults/", "the tree extracts without its embedded wrapper directory")

	t.Run("an existing file is never overwritten", func(t *testing.T) {
		path := filepath.Join(dir, "lenses", "bugs.md")
		require.NoError(t, os.WriteFile(path, []byte("mine"), 0o600))
		out := &strings.Builder{}
		require.NoError(t, options{DumpDefaults: dir}.dumpDefaults(out))
		body, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, "mine", string(body))
		assert.Contains(t, out.String(), "already present")
	})

	t.Run("unwritable target", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		err := options{DumpDefaults: filepath.Join(blocked, "out")}.dumpDefaults(&strings.Builder{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dump defaults")
	})
}

func TestIniKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := "[Application Options]\n# comment = 1\n; other = 2\n\nkeep-runs = 5\n  Max-Parallel = 7 \nnot-a-pair\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	keys, err := iniKeys(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep-runs", "max-parallel"}, keys)

	missing, err := iniKeys(filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Nil(t, missing, "an absent config file is not an error")
}

// isolate points the process at a temp directory for both the working directory and the home lookup, so
// no test can read or write the developer's own config.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return dir
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, configFileName), []byte(body), 0o600))
}

func taskDirWith(t *testing.T, root, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("content of "+f), 0o600))
	}
	return dir
}
