package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	pmocks "github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
)

func TestRun_archive(t *testing.T) {
	t.Run("the run directory holds every artifact a later reader needs", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		dir := filepath.Join(root, "pr-1", "runs", "round-1")
		for _, name := range []string{
			"report.md", "findings.json", "manifest.json", "events.jsonl",
			filepath.Join("prompts", "agents", "lenses.md"),
			filepath.Join("stages", "1-found.json"),
			filepath.Join("agents", "lenses.jsonl"),
		} {
			assert.FileExists(t, filepath.Join(dir, name))
		}

		report, err := os.ReadFile(filepath.Join(dir, "report.md")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, r.stdout.String(), string(report), "the archived report is what the caller was shown")

		var rep finding.Report
		data, err := os.ReadFile(filepath.Join(dir, "findings.json")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &rep))
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, "unchecked error", rep.Findings[0].Title)
	})

	t.Run("nothing is written outside runs/", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		entries, err := os.ReadDir(filepath.Join(root, "pr-1"))
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		slices.Sort(names)
		assert.Equal(t, []string{"runs", scopeFileName}, names, "everything above runs/ belongs to the caller")
	})

	t.Run("the manifest records what actually ran, not only what was asked for", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		var got manifest
		data, err := os.ReadFile(filepath.Join(root, "pr-1", "runs", "round-1", "manifest.json")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &got))

		assert.Equal(t, "pr-1", got.Task)
		assert.Equal(t, "round-1", got.Run)
		assert.Equal(t, "focused", got.Profile)
		assert.Equal(t, filepath.Join(root, "pr-1", scopeFileName), got.ScopePath)

		require.Len(t, got.Agents, 1, "the --lenses override runs one agent")
		agent := got.Agents[0]
		assert.Equal(t, "lenses", agent.Name)
		assert.Equal(t, []string{"bugs"}, agent.Lenses)
		assert.Equal(t, "claude", agent.Executor)
		assert.Equal(t, "opus", agent.RequestedModel)
		assert.Equal(t, "claude-opus-5", agent.ActualModel, "--model can be silently ignored, so both are recorded")
		assert.Equal(t, "high", agent.Effort)
		assert.Equal(t, 4210, agent.Tokens)
		assert.False(t, agent.Degraded)

		require.NotEmpty(t, got.Stages)
		assert.Equal(t, "find", got.Stages[0].Name)
		assert.Positive(t, got.Tokens)
		assert.False(t, got.StartedAt.IsZero())

		var lens *prompt.FileOrigin
		for i, o := range got.Prompts {
			if o.Path == "lenses/bugs.md" {
				lens = &got.Prompts[i]
			}
		}
		require.NotNil(t, lens, "provenance answers which lens text raised a finding")
		assert.Equal(t, prompt.LayerEmbedded, lens.Layer)
		assert.Len(t, lens.Hash, 64, "a content hash tells two rounds of one task apart")
	})

	t.Run("a degraded agent keeps its roster entry in the manifest", func(t *testing.T) {
		r, root := archiveRun(t)
		// the whole focused roster: a claude lens agent plus a codex peer. No stagger, since the mock
		// runner emits no activity and the fake clock fires no timer, so nothing would open the gate
		r.o.Lenses, r.o.StaggerDelay = nil, 0

		ro := r.opts()
		ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
			return &pmocks.RunnerMock{
				RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
					if spec.Executor == executorCodex {
						return executor.Result{}, errors.New("codex died")
					}
					return r.result, nil
				},
			}
		}
		require.Equal(t, 1, run(ro))

		var got manifest
		data, err := os.ReadFile(filepath.Join(root, "pr-1", "runs", "round-1", "manifest.json")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &got))

		require.Len(t, got.Agents, 2, "a source that never delivered is still part of the roster that ran")
		codex := got.Agents[1]
		assert.Equal(t, "codex", codex.Name)
		assert.Equal(t, "gpt-5.6-sol", codex.RequestedModel)
		assert.Equal(t, "xhigh", codex.Effort)
		assert.True(t, codex.Degraded)
		assert.Empty(t, codex.ActualModel, "nothing ran, so nothing is claimed to have run")
		assert.Equal(t, []string{"codex"}, got.Degraded)
	})

	t.Run("a run name that already exists fails without touching the round it names", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		report := filepath.Join(root, "pr-1", "runs", "round-1", "report.md")
		before, err := os.ReadFile(report) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)

		again, _ := archiveRun(t)
		again.o.TasksDir = root
		assert.Equal(t, 2, run(again.opts()))
		assert.Contains(t, again.stderr.String(), "create run directory")
		assert.Empty(t, again.stdout.String(), "nothing but the report reaches stdout, and there is no report")

		after, err := os.ReadFile(report) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})

	t.Run("an archive that cannot be written exits 2 with no report", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		r, root := archiveRun(t)
		task := filepath.Join(root, "pr-1")
		//nolint:gosec // a directory the run must fail to write into, restored below
		require.NoError(t, os.Chmod(task, 0o500))
		t.Cleanup(func() { _ = os.Chmod(task, 0o750) }) //nolint:gosec // restores the temp directory so cleanup can remove it

		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "open run archive")
		assert.Empty(t, r.stdout.String(), "a report next to no archive would read as auditable")
	})

	t.Run("a second round coexists with the first", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		second, _ := archiveRun(t)
		second.o.TasksDir, second.o.Run = root, "after-fix"
		require.Equal(t, 1, run(second.opts()))

		assert.FileExists(t, filepath.Join(root, "pr-1", "runs", "round-1", "report.md"))
		assert.FileExists(t, filepath.Join(root, "pr-1", "runs", "after-fix", "report.md"))
	})
}

func TestRun_writesOnlyUnderItsOwnRun(t *testing.T) {
	// every path a review could reach is watched: the caller's context above runs/, a sibling round,
	// and both config layers — loading the prompt tree must never install anything as a side effect
	r, root := archiveRun(t)
	user, project := t.TempDir(), t.TempDir()
	writeConfig(t, user, "keep-runs = 10\n")
	r.o.layers = configLayers{user: user, project: project}

	sibling := filepath.Join(root, "pr-1", "runs", "earlier")
	require.NoError(t, os.MkdirAll(sibling, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, findingsFileName), []byte(`{"findings":[]}`), 0o600))

	before := map[string][]string{"tasks": treeOf(t, root), "user": treeOf(t, user), "project": treeOf(t, project)}
	siblingBefore := treeOf(t, sibling)
	require.Equal(t, 1, run(r.opts()))

	assert.Equal(t, before["user"], treeOf(t, user), "a review reads the config tree and writes none of it")
	assert.Equal(t, before["project"], treeOf(t, project))
	assert.Equal(t, siblingBefore, treeOf(t, sibling), "an earlier round is what a reflection agent reads")

	own := filepath.Join("pr-1", "runs", "round-1")
	for _, p := range treeOf(t, root) {
		if slices.Contains(before["tasks"], p) {
			continue
		}
		assert.True(t, p == own || strings.HasPrefix(p, own+string(filepath.Separator)),
			"%q was created outside runs/<run>/", p)
	}
}

func TestRun_tokensPerAgentSumToTheRunTotal(t *testing.T) {
	// per-agent token counts are one of the things a subprocess buys, so both the report and the
	// manifest carry them and the run total has to be their sum rather than an independent number
	tokens := map[string]int{"bugs": 4210, "codex": 1234}

	r, root := archiveRun(t)
	r.o.Lenses, r.o.StaggerDelay, r.o.Profile = nil, 0, "focused"

	ro := r.opts()
	ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
		name := "bugs"
		if spec.Executor == executorCodex {
			name = "codex"
		}
		return &pmocks.RunnerMock{
			RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
				res := r.result
				res.Tokens = tokens[name]
				return res, nil
			},
		}
	}
	require.Equal(t, 1, run(ro))

	want := tokens["bugs"] + tokens["codex"]
	dir := filepath.Join(root, "pr-1", "runs", "round-1")

	var rep finding.Report
	data, err := os.ReadFile(filepath.Join(dir, "findings.json")) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &rep))

	got := 0
	require.Len(t, rep.Sources.Agents, 2)
	for _, a := range rep.Sources.Agents {
		assert.Equal(t, tokens[a.Name], a.Tokens, "agent %s", a.Name)
		got += a.Tokens
	}
	assert.Equal(t, want, got)
	assert.Equal(t, want, rep.Stats.Tokens, "the run total is what the agents actually spent")

	var got2 manifest
	data, err = os.ReadFile(filepath.Join(dir, manifestFileName)) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &got2))

	sum := 0
	require.Len(t, got2.Agents, 2)
	for _, a := range got2.Agents {
		assert.Equal(t, tokens[a.Name], a.Tokens, "agent %s", a.Name)
		sum += a.Tokens
	}
	assert.Equal(t, want, sum)
	assert.Equal(t, want, got2.Tokens, "the manifest and the report must not disagree on what a run cost")

	for name, n := range tokens {
		assert.Contains(t, r.stdout.String(), fmt.Sprintf("| %s |", name))
		assert.Contains(t, r.stdout.String(), fmt.Sprintf("| %d |", n), "the rendered report shows per-agent tokens")
	}
}

func TestRun_history(t *testing.T) {
	t.Run("prior rounds reach every composed prompt, and are read before pruning drops them", func(t *testing.T) {
		first, root := archiveRun(t)
		require.Equal(t, 1, run(first.opts()))

		second, _ := archiveRun(t)
		second.o.TasksDir, second.o.Run, second.o.KeepRuns = root, "after-fix", 1
		require.Equal(t, 1, run(second.opts()))

		composed := second.prompts()[0]
		assert.Contains(t, composed, "Prior rounds for this task: "+filepath.Join(root, "pr-1", "runs"))
		assert.Contains(t, composed, "round-1")
		assert.Contains(t, composed, "1 findings (0 critical, 1 major, 0 minor)")
		assert.Contains(t, composed, "Re-evaluate everything independently",
			"the data and its guard are inseparable, so the composer appends the guard itself")

		assert.NoDirExists(t, filepath.Join(root, "pr-1", "runs", "round-1"),
			"keep-runs 1 drops it — after the history was read, never before")
		assert.DirExists(t, filepath.Join(root, "pr-1", "runs", "after-fix"))
	})

	t.Run("a round that cannot be pruned is a warning, not a failed review", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root removes a directory it has no write permission on")
		}
		r, root := archiveRun(t)
		r.o.KeepRuns = 1

		stuck := filepath.Join(root, "pr-1", "runs", "round-0", "agents")
		require.NoError(t, os.MkdirAll(stuck, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(stuck, "bugs.jsonl"), []byte("x"), 0o600))
		//nolint:gosec // a directory Prune must fail to remove, restored below
		require.NoError(t, os.Chmod(stuck, 0o500))
		t.Cleanup(func() { _ = os.Chmod(stuck, 0o750) }) //nolint:gosec // restores the temp directory so cleanup can remove it

		assert.Equal(t, 1, run(r.opts()), "the review's own outcome is what the exit code reports")
		assert.Contains(t, r.stderr.String(), "warning:")
		assert.Contains(t, r.stdout.String(), "unchecked error", "the report still reached the caller")
	})

	t.Run("a first round carries no history block", func(t *testing.T) {
		r, _ := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))
		assert.NotContains(t, r.prompts()[0], "Prior rounds",
			"an empty block would say nothing and still cost every prompt")
	})
}

// archiveRun builds a review that really writes its archive: a fresh tasks root, one agent through the
// --lenses override, and a canned finding so the report is non-empty.
func archiveRun(t *testing.T) (*runHarness, string) {
	t.Helper()
	root := taskRoot(t)
	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4, KeepRuns: 10, NoSynthesis: true, NoVerify: true,
	})
	r.result = executor.Result{
		StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
			`"confidence":90,"title":"unchecked error","body":"the write error is dropped","lenses":["bugs"]}]}`),
		Raw: `{"type":"result"}`, Tokens: 4210, ActualModel: "claude-opus-5",
	}
	return r, root
}

// prompts is what the runner was handed, which is how a test asserts on an injected block without
// reading the archive back.
func (r *runHarness) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return []string{""}
	}
	return slices.Clone(r.seen)
}
