package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	pmocks "github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
)

func TestPrintVersion(t *testing.T) {
	tests := []struct {
		name string
		rev  string
		want string
	}{
		{name: "stamped revision", rev: "master-abc1234-20260726T120000", want: "revmux master-abc1234-20260726T120000\n"},
		{name: "default revision", rev: revision, want: "revmux unknown\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, printVersion(buf, tt.rev))
			assert.Equal(t, tt.want, buf.String())
		})
	}

	t.Run("write failure", func(t *testing.T) {
		err := printVersion(failingWriter{}, "v1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write version")
	})
}

func TestBinary_versionOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "revmux")
	build := exec.Command("go", "build", "-ldflags", "-X main.revision=test-rev", "-o", bin, ".") //nolint:gosec // fixed argv, output path from t.TempDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	out, err = exec.Command(bin, "--version").CombinedOutput() //nolint:gosec // binary just built by this test
	require.NoError(t, err)
	assert.Equal(t, "revmux test-rev\n", string(out))
}

func TestRun_metaCommands(t *testing.T) {
	t.Run("version goes to stdout", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.Equal(t, "revmux unknown\n", r.stdout.String())
		assert.Empty(t, r.stderr.String())
	})

	t.Run("init writes the template and says so on stderr", func(t *testing.T) {
		dir := isolate(t)
		r := newRunOpts(t, options{Init: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, projectDirName, configFileName))
		assert.Empty(t, r.stdout.String(), "stdout carries the report and nothing else")
		assert.Contains(t, r.stderr.String(), "wrote")
	})

	t.Run("dump-defaults extracts the tree", func(t *testing.T) {
		dir := t.TempDir()
		r := newRunOpts(t, options{DumpDefaults: dir})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, "lenses", "bugs.md"))
		assert.Empty(t, r.stdout.String())
	})

	t.Run("dump-defaults failure exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		r := newRunOpts(t, options{DumpDefaults: filepath.Join(blocked, "out")})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "error:")
	})

	t.Run("version write failure exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write version")
	})
}

func TestRun_config(t *testing.T) {
	root := taskRoot(t)

	emitted := func(t *testing.T, o options) (catalog, *runHarness) {
		t.Helper()
		o.showConfig = true
		r := newRunOpts(t, o)
		require.Equal(t, 0, run(r.opts()))
		assert.Empty(t, r.stderr.String(), "the catalog is the whole output")

		var c catalog
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &c), "the caller model parses this")
		return c, r
	}

	t.Run("every shipped profile and lens is reported", func(t *testing.T) {
		c, r := emitted(t, options{TasksDir: root, Profile: "comprehensive"})
		assert.Contains(t, r.stdout.String(), "\n  \"knobs\"", "indented, since a human reads it occasionally")

		names := make([]string, 0, len(c.Profiles))
		for _, p := range c.Profiles {
			names = append(names, p.Name)
			assert.NotEmpty(t, p.Description, "profile %s has no description", p.Name)
			assert.NotEmpty(t, p.Roster, "profile %s reports no roster", p.Name)
			for _, a := range p.Roster {
				assert.NotEmpty(t, a.Lenses, "agent %s carries no lens", a.Name)
				assert.NotEmpty(t, a.Model, "agent %s reports no model", a.Name)
				assert.NotEmpty(t, a.Effort, "agent %s reports no effort", a.Name)
				assert.NotEmpty(t, a.Executor, "agent %s reports no executor", a.Name)
				assert.NotEmpty(t, a.Color, "agent %s reports no color, so the palette assignment is invisible", a.Name)
			}
		}
		assert.Equal(t, []string{"comprehensive", "final", "focused"}, names)

		set, err := prompt.Load(prompt.LoadOpts{})
		require.NoError(t, err)
		require.Len(t, c.Lenses, len(set.LensNames()))
		for _, l := range c.Lenses {
			assert.NotEmpty(t, l.Description, "lens %s is uncomposable without a description", l.Name)
		}
	})

	t.Run("a roster matches what a run of that profile dispatches", func(t *testing.T) {
		c, _ := emitted(t, options{TasksDir: root})

		set, err := prompt.Load(prompt.LoadOpts{})
		require.NoError(t, err)
		for _, p := range c.Profiles {
			profile, err := set.Profile(p.Name)
			require.NoError(t, err)
			want, err := profile.Roster(nil, set.LensNames())
			require.NoError(t, err)
			assert.Equal(t, want, p.Roster, "profile %s", p.Name)
		}
	})

	t.Run("nothing is written to the tasks directory", func(t *testing.T) {
		before := treeOf(t, root)
		c, _ := emitted(t, options{TasksDir: root, Task: "pr-1", Run: "round-1"})

		assert.Equal(t, before, treeOf(t, root), "the catalog runs before any archive exists")
		assert.NoDirExists(t, filepath.Join(root, "pr-1", "runs"))
		assert.Equal(t, []string{"pr-1"}, c.Paths.Tasks, "a caller picks a --run name from what is already there")
	})

	t.Run("an unreadable prompt tree exits 2", func(t *testing.T) {
		cfg := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cfg, "lenses", "bugs.md"), []byte("---\nnope:\n---\nbody\n"), 0o600))

		r := newRunOpts(t, options{showConfig: true, TasksDir: root, layers: configLayers{user: cfg}})
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "a half-written catalog would parse as a complete one")
		assert.Contains(t, r.stderr.String(), "load prompts")
	})

	t.Run("an unwritable stdout exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{showConfig: true, TasksDir: root})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write catalog")
	})

	t.Run("an unresolvable profile still reports the tree", func(t *testing.T) {
		c, _ := emitted(t, options{TasksDir: root, Profile: "nope"})
		assert.NotEmpty(t, c.Profiles, "the caller running this is the one whose --profile does not resolve")
	})
}

func TestRun_reviewGate(t *testing.T) {
	root := taskRoot(t)

	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "no task", opts: options{TasksDir: root, Profile: "focused"}, wantErr: "--task is required"},
		{name: "bad task name", opts: options{Task: "../out", TasksDir: root, Profile: "focused"}, wantErr: "path separator"},
		{name: "bad run name", opts: options{Task: "pr-1", Run: "a/b", TasksDir: root, Profile: "focused"}, wantErr: "--run"},
		{name: "missing task dir", opts: options{Task: "pr-2", TasksDir: root, Profile: "focused"}, wantErr: "task directory"},
		{name: "unknown profile", opts: options{Task: "pr-1", TasksDir: root, Profile: "nope"}, wantErr: "resolve profile"},
		{name: "unknown lens", opts: options{Task: "pr-1", TasksDir: root, Profile: "focused", Lenses: []string{"nope"}}, wantErr: "resolve roster"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunOpts(t, tt.opts)
			assert.Equal(t, 2, run(r.opts()))
			assert.Empty(t, r.stdout.String(), "nothing but the report may reach stdout")
			assert.Contains(t, r.stderr.String(), tt.wantErr)
		})
	}
}

func TestRun_review(t *testing.T) {
	// every subtest takes its own tasks root: a run name that already exists is a load-time error, so
	// two runs of round-1 under one root would collide rather than exercise what each case asserts
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, KeepRuns: 10,
			NoSynthesis: true, // these assert rendering and exit codes; the merge has its own case below
		}
	}

	t.Run("markdown to stdout, progress to stderr, exit 1", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{
			StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
				`"confidence":90,"title":"unchecked error","body":"the write error is dropped","lenses":["bugs"]}]}`),
			Tokens: 4210, ActualModel: "claude-opus-5",
		}

		assert.Equal(t, 1, run(r.opts()), "findings above the threshold exit 1")

		out := r.stdout.String()
		assert.Contains(t, out, "# Review: pr-1 / round-1")
		assert.Contains(t, out, "## Major")
		assert.Contains(t, out, "unchecked error")
		assert.Contains(t, out, "`app/main.go:42`")
		assert.Contains(t, out, "sources: lenses", "Go stamps the executing agent's name")
		assert.Contains(t, out, "claude-opus-5")
		assert.Contains(t, out, "4210")

		assert.Contains(t, r.stderr.String(), "stage find")
		// the plain renderer prefixes the agent in its own resolved color, the same one the ui uses
		assert.Contains(t, r.stderr.String(), prompt.AgentSpec{Color: "6"}.Paint("lenses")+": done, 1 findings")
	})

	t.Run("json to stdout", func(t *testing.T) {
		o := base(t)
		o.JSON = true
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"x"}]}`)}

		assert.Equal(t, 1, run(r.opts()))

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &rep))
		assert.Equal(t, "pr-1", rep.Scope.Task)
		assert.Equal(t, "round-1", rep.Scope.Run)
		assert.Equal(t, filepath.Join(o.TasksDir, "pr-1", scopeFileName), rep.Scope.ScopePath)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"lenses"}, rep.Findings[0].Sources)
	})

	t.Run("min-confidence filters the report and the exit code together", func(t *testing.T) {
		o := base(t)
		o.MinConfidence = 80
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"below the bar"}]}`)}

		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
		assert.NotContains(t, r.stdout.String(), "below the bar")
	})

	t.Run("the synthesis stage is wired and its merge reaches stdout", func(t *testing.T) {
		o := base(t)
		o.NoSynthesis = false
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"raw","lenses":["bugs"]}]}`)}
		r.synth = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"merged_ids":["lenses-1"],"file":"a.go","line":1,"severity":"major","confidence":85,` +
				`"title":"merged","body":"what it breaks"}],"open_questions":[],"pre_existing":[]}`)}

		assert.Equal(t, 1, run(r.opts()))
		out := r.stdout.String()
		assert.Contains(t, out, "merged", "the merged finding replaces the passthrough")
		assert.NotContains(t, out, "raw")
		assert.Contains(t, out, "sources: lenses", "attribution survives the merge")
		assert.Contains(t, r.stderr.String(), "stage synthesis")
	})

	t.Run("nothing found exits 0", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
	})

	t.Run("every source degraded exits 2 with no report", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.runErr = errors.New("stalled")
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "an empty report would read as a clean run")
		assert.Contains(t, r.stderr.String(), "review failed")
		assert.Contains(t, r.stderr.String(), "every source degraded")
		assert.Contains(t, r.stderr.String(), "retrying:", "the source was retried before it was given up on")
		assert.Contains(t, r.stderr.String(), "degraded:")
		assert.Equal(t, 2, r.attempts(), "one launch plus one retry, then the run stops")
	})

	t.Run("a report that cannot be written exits 2", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write markdown report")
	})
}

func TestRun_promptsCarryPathsNotContents(t *testing.T) {
	// the tasks root is outside the tree under review, which is where the never-embed rule is easiest
	// to break: nothing about these paths is relative to the repo, so a composer tempted to inline
	// would have to inline the whole file
	root := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(root, cwd+string(os.PathSeparator)), "the tasks root must be outside the repo")

	dir := filepath.Join(root, "pr-1")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, contextDirName), 0o750))
	contents := map[string]string{
		scopeFileName: "SENTINEL-SCOPE run git diff master...HEAD",
		goalFileName:  "SENTINEL-GOAL make the watchdog observable",
		profFileName:  "SENTINEL-PROFILE go, tests with testify",
		filepath.Join(contextDirName, "ticket.md"): "SENTINEL-CONTEXT the ticket body",
	}
	for name, body := range contents {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}

	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4, KeepRuns: 10, VerifyGroups: 4, NoSynthesis: true,
	})
	r.result = executor.Result{StructuredOutput: json.RawMessage(
		`{"findings":[{"file":"app/main.go","line":42,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}
	require.Equal(t, 1, run(r.opts()))

	prompts := r.prompts()
	require.Len(t, prompts, 2, "the finder and the verifier its finding produced")

	for _, want := range []string{
		filepath.Join(dir, scopeFileName), filepath.Join(dir, goalFileName),
		filepath.Join(dir, profFileName), filepath.Join(dir, contextDirName),
	} {
		assert.Contains(t, prompts[0], want, "the agent is handed the path and reads the file itself")
	}

	// the archived prompt is the bytes the process received, so a leak would show up in both
	archived, err := os.ReadFile(filepath.Join(dir, "runs", "round-1", "prompts", "agents", "lenses.md")) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	assert.Equal(t, prompts[0], string(archived))

	for _, body := range contents {
		sentinel := strings.SplitN(body, " ", 2)[0]
		for i, p := range prompts {
			assert.NotContains(t, p, sentinel, "prompt %d carries file contents, not just a path", i)
		}
		assert.NotContains(t, string(archived), sentinel)
	}
}

func TestRun_degradedSourceDoesNotAbortTheRun(t *testing.T) {
	// the whole focused roster: a claude lens agent plus a codex peer, no stagger, since the mock
	// runner emits no activity and the fake clock fires no timer, so nothing would open the gate
	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused",
		MaxParallel: 4, KeepRuns: 10, NoSynthesis: true, NoVerify: true,
	})
	r.result = executor.Result{
		StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
			`"confidence":90,"title":"unchecked error","lenses":["bugs"]}]}`),
		Tokens: 4210, ActualModel: "claude-opus-5",
	}

	var codexRuns atomic.Int64
	ro := r.opts()
	ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
		return &pmocks.RunnerMock{
			RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
				if spec.Executor == executorCodex {
					codexRuns.Add(1)
					return executor.Result{}, errors.New("killed")
				}
				return r.result, nil
			},
		}
	}

	assert.Equal(t, 1, run(ro), "one dead source must not waste every other agent's work")
	assert.EqualValues(t, 2, codexRuns.Load(), "one launch plus one retry, and never a third")

	out := r.stdout.String()
	assert.Contains(t, out, "**Degraded run**: 1 of 2 sources reported, missing codex",
		"a degraded run that reads like a complete one is the worst failure this tool has")
	assert.Contains(t, out, "unchecked error", "the surviving source still reported")
	assert.Contains(t, out, "| codex | codex |")
	assert.Contains(t, out, "| degraded |")

	stderr := r.stderr.String()
	assert.Contains(t, stderr, "retrying:")
	assert.Contains(t, stderr, "degraded:")
}

func TestRun_configComposesARunnableInvocation(t *testing.T) {
	// what a caller model actually does: read the catalog, then compose from it alone
	catalogOf := func(t *testing.T, root string) catalog {
		t.Helper()
		r := newRunOpts(t, options{showConfig: true, TasksDir: root})
		require.Equal(t, 0, run(r.opts()))
		var c catalog
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &c))
		return c
	}

	review := func(t *testing.T, o options) (*runHarness, string) {
		t.Helper()
		root := taskRoot(t)
		o.Task, o.Run, o.TasksDir = "pr-1", "round-1", root
		o.MaxParallel, o.KeepRuns, o.NoSynthesis, o.NoVerify = 4, 10, true, true
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"x"}]}`)}
		require.Equal(t, 1, run(r.opts()), "an invocation composed from the catalog must run")
		return r, root
	}

	t.Run("every lens the catalog names composes into a prompt", func(t *testing.T) {
		c := catalogOf(t, taskRoot(t))
		require.NotEmpty(t, c.Lenses)

		lenses := make([]string, 0, len(c.Lenses))
		for _, l := range c.Lenses {
			lenses = append(lenses, l.Name)
		}

		r, _ := review(t, options{Profile: c.Profiles[0].Name, Lenses: lenses})
		for _, name := range lenses {
			assert.Contains(t, r.prompts()[0], "## Lens: "+name, "lens %s is named by the catalog but never loaded", name)
		}
	})

	t.Run("a reported roster is what that profile dispatches", func(t *testing.T) {
		for _, p := range catalogOf(t, taskRoot(t)).Profiles {
			t.Run(p.Name, func(t *testing.T) {
				_, root := review(t, options{Profile: p.Name})

				// the agents that ran, read back from the prompt each one was archived under
				entries, err := os.ReadDir(filepath.Join(root, "pr-1", "runs", "round-1", "prompts", "agents"))
				require.NoError(t, err)
				dispatched := make([]string, 0, len(entries))
				for _, e := range entries {
					dispatched = append(dispatched, strings.TrimSuffix(e.Name(), ".md"))
				}

				want := make([]string, 0, len(p.Roster))
				for _, a := range p.Roster {
					want = append(want, a.Name)
				}
				slices.Sort(want)
				slices.Sort(dispatched)
				assert.Equal(t, want, dispatched)
			})
		}
	})
}

func TestRunOpts_tty(t *testing.T) {
	// a real terminal is never opened in a test, so the opener stands in for one: what matters is
	// that the gate is the opener and nothing else
	opened := func(t *testing.T) func() (*os.File, error) {
		t.Helper()
		return func() (*os.File, error) { return os.CreateTemp(t.TempDir(), "tty") }
	}

	t.Run("an openable tty is what enables the ui", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		ro.stdout = failingWriter{} // stdout is not a terminal here, and must not be the gate
		ro.openTTY = opened(t)

		tty := ro.tty()
		require.NotNil(t, tty)
		assert.NoError(t, tty.Close())
	})

	t.Run("--no-tui never opens one", func(t *testing.T) {
		r := newRunOpts(t, options{NoTUI: true})
		ro := r.opts()
		ro.openTTY = func() (*os.File, error) {
			t.Fatal("the opener must not be reached with --no-tui")
			return nil, errors.New("unreachable")
		}
		assert.Nil(t, ro.tty())
	})

	t.Run("a tty that will not open falls back to the plain renderer", func(t *testing.T) {
		assert.Nil(t, newRunOpts(t, options{}).opts().tty(), "the harness opener always fails")
	})

	t.Run("no opener at all is the same as no tty", func(t *testing.T) {
		ro := newRunOpts(t, options{}).opts()
		ro.openTTY = nil
		assert.Nil(t, ro.tty())
	})
}

func TestRunOpts_render(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs", Color: "6"}}

	// finished drives one renderer through a whole run: two events, the channel closed, then the
	// report handed over the way review does it. The channel is unbuffered and the sends are waited
	// on, because the second one returns only after the renderer applied the first — without that
	// the frame under assertion races the quit.
	finished := func(t *testing.T, ro runOpts, rep finding.Report, err error) {
		t.Helper()
		events, sent := make(chan pipeline.Event), make(chan struct{})
		go func() {
			defer close(sent)
			events <- pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: time.Now()}
			events <- pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "bugs", Text: "bugs", At: time.Now()}
			close(events)
		}()

		r := ro.render(roster, events)
		<-sent
		done := make(chan struct{})
		go func() { defer close(done); r.finish(rep, err) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the renderer must let go of the terminal once the report is in")
		}
	}

	t.Run("with no tty the plain renderer takes the events", func(t *testing.T) {
		r := newRunOpts(t, options{})
		finished(t, r.opts(), finding.Report{}, nil)
		assert.Contains(t, r.stderr.String(), "stage find")
		assert.Empty(t, r.stdout.String(), "stdout belongs to the report alone")
	})

	t.Run("with a tty the ui renders there, and a failed run gets the terminal back", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tty")
		r := newRunOpts(t, options{})
		ro := r.opts()
		ro.openTTY = ttyFile(t, path)

		finished(t, ro, finding.Report{}, errors.New("every source degraded"))

		frame, err := os.ReadFile(path) //nolint:gosec // path from t.TempDir
		require.NoError(t, err)
		assert.Contains(t, string(frame), "stage find", "the ui renders to the tty")
		assert.Empty(t, r.stdout.String(), "and never to stdout")
		assert.Empty(t, r.stderr.String(), "nor to the plain renderer's stream")
	})

	t.Run("a finished report goes to the browser, and the reader closing it ends the wait", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		ro.openTTY = ttyKeys(t, "q")

		finished(t, ro, finding.Report{Findings: []finding.Finding{{Title: "unchecked error"}}}, nil)
		assert.Empty(t, r.stdout.String(), "the browser renders the report, it does not write it")
	})
}

func TestRun_reportWrittenOnce(t *testing.T) {
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, KeepRuns: 10, NoSynthesis: true,
		}
	}
	found := executor.Result{StructuredOutput: json.RawMessage(
		`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}

	// the reader may close the browser before or after the report reaches it; either way package
	// main is the one writer, which is what these assert
	t.Run("under the tui", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = found
		ro := r.opts()
		ro.openTTY = ttyKeys(t, "q")

		assert.Equal(t, 1, run(ro))
		out := r.stdout.String()
		assert.Equal(t, 1, strings.Count(out, "# Review: pr-1 / round-1"), "written once, by package main only")
		assert.Equal(t, 1, strings.Count(out, "unchecked error"))
		assert.Empty(t, r.stderr.String(), "the ui took the events, so the plain renderer saw none")
	})

	t.Run("under --no-tui", func(t *testing.T) {
		o := base(t)
		o.NoTUI = true
		r := newRunOpts(t, o)
		r.result = found

		assert.Equal(t, 1, run(r.opts()))
		out := r.stdout.String()
		assert.Equal(t, 1, strings.Count(out, "# Review: pr-1 / round-1"))
		assert.Equal(t, 1, strings.Count(out, "unchecked error"))
		assert.Contains(t, r.stderr.String(), "stage find", "and the plain renderer took the events")
	})
}

func TestRun_ttyGate(t *testing.T) {
	// the report is redirected here, so stdout is a pipe and not a terminal: that must not decide
	// whether the ui runs, or `revmux --json > findings.json` would silently lose it
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, KeepRuns: 10, NoSynthesis: true, JSON: true,
		}
	}

	redirected := func(t *testing.T, ro runOpts) string {
		t.Helper()
		pr, pw, err := os.Pipe()
		require.NoError(t, err)
		ro.stdout = pw

		assert.Equal(t, 1, run(ro))
		require.NoError(t, pw.Close())
		out, err := io.ReadAll(pr)
		require.NoError(t, err)
		return string(out)
	}

	t.Run("an openable tty runs the ui even though stdout is a pipe", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}
		ro := r.opts()
		ro.openTTY = ttyKeys(t, "q")

		out := redirected(t, ro)

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(out), &rep), "the redirected report is still whole json")
		require.Len(t, rep.Findings, 1)
		assert.Empty(t, r.stderr.String(), "the ui took the events, so the plain renderer never ran")
	})

	t.Run("a tty that will not open falls back to the plain renderer", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}

		out := redirected(t, r.opts()) // the harness opener always fails

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(out), &rep))
		assert.Contains(t, r.stderr.String(), "stage find")
	})
}

// ttyFile stands in for the terminal a run renders to: a real file, so a test can read the frames
// back. It carries no input, so only a quit ends the program. No test opens a real tty.
func ttyFile(t *testing.T, path string) func() (*os.File, error) {
	t.Helper()
	return func() (*os.File, error) { return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) } //nolint:gosec // path from t.TempDir
}

// ttyKeys stands in for the terminal a reader types into: a pipe holding his keystrokes. Frames
// written back to it go nowhere, so a test asserting on rendered output wants ttyFile instead — one
// file cannot serve both, since the frame written at open advances the offset past the input.
func ttyKeys(t *testing.T, keys string) func() (*os.File, error) {
	t.Helper()
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	_, err = pw.WriteString(keys)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pw.Close(), pr.Close() })
	return func() (*os.File, error) { return pr, nil }
}

func TestRunOpts_runnerFactory(t *testing.T) {
	t.Run("a supplied factory wins", func(t *testing.T) {
		r := newRunOpts(t, options{})
		got := r.opts().runnerFactory(reviewContext{})(pipeline.RunnerSpec{Executor: "claude"})
		assert.IsType(t, &pmocks.RunnerMock{}, got)
	})

	t.Run("routes by the spec's executor", func(t *testing.T) {
		tests := []struct {
			name string
			spec pipeline.RunnerSpec
			want pipeline.Runner
		}{
			{"claude", pipeline.RunnerSpec{Executor: "claude"}, &executor.Claude{}},
			{"codex", pipeline.RunnerSpec{Executor: "codex"}, &executor.Codex{}},
			{"empty defaults to claude", pipeline.RunnerSpec{}, &executor.Claude{}},
		}

		r := newRunOpts(t, options{IdleTimeout: time.Minute, HardTimeout: 20 * time.Minute})
		ro := r.opts()
		ro.newRunner = nil
		factory := ro.runnerFactory(reviewContext{WorkDir: t.TempDir()})

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.IsType(t, tt.want, factory(tt.spec))
			})
		}
	})
}

// treeOf lists every path under dir, relative and sorted, so a test can assert a whole directory is
// untouched rather than naming the files it expects not to appear.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		require.NoError(t, err)
		out = append(out, rel)
		return nil
	})
	require.NoError(t, err)
	slices.Sort(out)
	return out
}

// taskRoot builds a tasks root holding one filled task directory, never the real ./.revmux/tasks.
func taskRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pr-1")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, scopeFileName), []byte("review the diff"), 0o600))
	return root
}

// runHarness holds the writers a run wrote to, so a test can assert on each stream separately, plus
// the canned executor result every agent gets back.
type runHarness struct {
	o      options
	stdout *strings.Builder
	stderr *strings.Builder
	result executor.Result
	synth  executor.Result
	runErr error
	runs   atomic.Int64

	mu   sync.Mutex
	seen []string // every prompt the runner was handed, in launch order
}

// synthesisMarker is a line only the synthesis prompt carries, so the harness answers that stage
// with its own fixture rather than handing it a finder-shaped one.
const synthesisMarker = "merging a review panel"

// attempts is how many processes the run launched, which is what proves a failing source was retried
// exactly once rather than not at all or forever.
func (r *runHarness) attempts() int { return int(r.runs.Load()) }

func newRunOpts(t *testing.T, o options) *runHarness {
	t.Helper()
	return &runHarness{o: o, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
}

func (r *runHarness) opts() runOpts {
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			return &mocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { return true },
			}
		},
	}
	return runOpts{
		opts: r.o, clock: clk, stdout: r.stdout, stderr: r.stderr,
		openTTY:   func() (*os.File, error) { return nil, errors.New("no tty in tests") },
		newRunner: r.newRunner,
	}
}

func (r *runHarness) newRunner(pipeline.RunnerSpec) pipeline.Runner {
	return &pmocks.RunnerMock{
		RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
			r.runs.Add(1)
			r.mu.Lock()
			r.seen = append(r.seen, req.Prompt)
			r.mu.Unlock()
			if req.RawOutput != nil {
				_, _ = req.RawOutput.Write([]byte(r.result.Raw))
			}
			if strings.Contains(req.Prompt, synthesisMarker) {
				return r.synth, nil
			}
			return r.result, r.runErr
		},
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
