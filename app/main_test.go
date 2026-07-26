package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	root := taskRoot(t)
	base := options{
		Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4,
		NoSynthesis: true, // these assert rendering and exit codes; the merge has its own case below
	}

	t.Run("markdown to stdout, progress to stderr, exit 1", func(t *testing.T) {
		r := newRunOpts(t, base)
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
		assert.Contains(t, r.stderr.String(), "lenses: done, 1 findings")
	})

	t.Run("json to stdout", func(t *testing.T) {
		o := base
		o.JSON = true
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"x"}]}`)}

		assert.Equal(t, 1, run(r.opts()))

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &rep))
		assert.Equal(t, "pr-1", rep.Scope.Task)
		assert.Equal(t, "round-1", rep.Scope.Run)
		assert.Equal(t, filepath.Join(root, "pr-1", scopeFileName), rep.Scope.ScopePath)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"lenses"}, rep.Findings[0].Sources)
	})

	t.Run("min-confidence filters the report and the exit code together", func(t *testing.T) {
		o := base
		o.MinConfidence = 80
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"below the bar"}]}`)}

		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
		assert.NotContains(t, r.stdout.String(), "below the bar")
	})

	t.Run("the synthesis stage is wired and its merge reaches stdout", func(t *testing.T) {
		o := base
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
		r := newRunOpts(t, base)
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
	})

	t.Run("every source degraded exits 2 with no report", func(t *testing.T) {
		r := newRunOpts(t, base)
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
		r := newRunOpts(t, base)
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write markdown report")
	})
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
