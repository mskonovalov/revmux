package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

// cleanCapture is the one live recording. Every other claude fixture below is derived from its bytes,
// so re-recording it regenerates the whole family.
func cleanCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "claude-clean.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

// truncatedCapture cuts the recording mid-line, which is what a killed CLI leaves behind.
func truncatedCapture(t *testing.T) []byte {
	t.Helper()
	data := cleanCapture(t)
	cut := len(data) - 200
	require.Positive(t, cut)
	require.NotEqual(t, byte('\n'), data[cut-1], "the cut must land inside a line")
	return data[:cut]
}

// stallCapture ends after the first assistant event, so a run over it produces activity and then nothing.
func stallCapture(t *testing.T) []byte {
	t.Helper()
	out := &bytes.Buffer{}
	for line := range bytes.SplitAfterSeq(cleanCapture(t), []byte("\n")) {
		out.Write(line)
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(line), &ev)
		if ev.Type == "assistant" {
			break
		}
	}
	require.Contains(t, out.String(), `"assistant"`)
	return out.Bytes()
}

// patchEvent rewrites the first event of the given type, leaving every other byte of the recording alone.
func patchEvent(t *testing.T, evType string, patch func(map[string]any)) []byte {
	t.Helper()
	out := &bytes.Buffer{}
	patched := false
	for line := range bytes.SplitSeq(cleanCapture(t), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal(line, &ev))
		if !patched && ev["type"] == evType {
			patch(ev)
			patched = true
			line, _ = json.Marshal(ev)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	require.True(t, patched, "no %s event in the recording", evType)
	return out.Bytes()
}

func writeFixture(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestClaude_args(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewClaude(runner, executor.Opts{})

	req := executor.Request{Prompt: "review this", Model: "opus", Effort: "high", Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "claude", call.Name)

	want := []string{
		"--print", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "dontAsk",
		"--disallowedTools", "Edit,Write,NotebookEdit",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--model", "opus",
		"--effort", "high",
		"--json-schema", `{"type":"object"}`,
	}
	assert.Equal(t, want, call.Args)
	assert.NotContains(t, call.Args, "--bare", "--bare forces api-key auth and drops project instructions")
}

func TestClaude_args_optionalFlagsOmitted(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewClaude(runner, executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)

	args := runner.CommandCalls()[0].Args
	assert.NotContains(t, args, "--model")
	assert.NotContains(t, args, "--effort")
	assert.NotContains(t, args, "--json-schema")
	assert.Contains(t, args, "--disable-slash-commands")
}

func TestClaude_Run_clean(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "opus"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.False(t, res.RateLimited)

	var out struct {
		Findings []struct {
			File     string   `json:"file"`
			Severity string   `json:"severity"`
			Lenses   []string `json:"lenses"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	assert.NotEmpty(t, out.Findings)
	assert.NotEmpty(t, out.Findings[0].File)
	assert.NotEmpty(t, out.Findings[0].Lenses)

	assert.Equal(t, "opus", res.RequestedModel)
	assert.NotEmpty(t, res.ActualModel)
	assert.Positive(t, res.Tokens)
	assert.Positive(t, res.TTFTMs)
	assert.NotEmpty(t, sink.EmitCalls())
}

func TestClaude_Run_truncatedStream(t *testing.T) {
	path := writeFixture(t, truncatedCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a malformed stream degrades rather than failing the run")
	assert.Empty(t, res.StructuredOutput, "the result event never arrived intact")
	assert.NotEmpty(t, res.Raw, "the partial stream is still available for the archive")
}

func TestClaude_Run_rateLimited(t *testing.T) {
	data := patchEvent(t, "rate_limit_event", func(ev map[string]any) {
		info, ok := ev["rate_limit_info"].(map[string]any)
		require.True(t, ok)
		info["status"] = "rejected"
	})
	path := writeFixture(t, data)
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)
	assert.True(t, res.RateLimited)
	assert.Equal(t, "rejected", res.RateLimit.Status)
	assert.NotEmpty(t, res.RateLimit.RateLimitType)
	assert.Positive(t, res.RateLimit.ResetsAt)

	kinds := make([]executor.EventKind, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		kinds = append(kinds, call.Event.Kind)
	}
	assert.Contains(t, kinds, executor.EventRateLimit)
}

func TestClaude_Run_rateLimitAllowed(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.False(t, res.RateLimited, "the recorded run was allowed")
	assert.Equal(t, "allowed", res.RateLimit.Status)
}

func TestClaude_Run_rateLimitAllowedWarning(t *testing.T) {
	data := patchEvent(t, "rate_limit_event", func(ev map[string]any) {
		info, ok := ev["rate_limit_info"].(map[string]any)
		require.True(t, ok)
		info["status"] = "allowed_warning"
	})
	path := writeFixture(t, data)
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)
	assert.False(t, res.RateLimited, "allowed_warning rides a successful response, discarding it would throw the run away")
	assert.Equal(t, "allowed_warning", res.RateLimit.Status)
	assert.NotEmpty(t, res.StructuredOutput, "the run completed and its findings must survive")

	for _, call := range sink.EmitCalls() {
		assert.NotEqual(t, executor.EventRateLimit, call.Event.Kind, "a warning is not a limit")
	}
}

func TestClaude_Run_idleTimeout(t *testing.T) {
	path := writeFixture(t, stallCapture(t))

	fired := make(chan func(), 1)
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(_ time.Duration, f func()) executor.Timer {
			fired <- f
			return &mocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { return true },
			}
		},
	}

	seen := make(chan struct{})
	var once sync.Once
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		if ev.Kind == executor.EventActivity {
			once.Do(func() { close(seen) })
		}
	}}

	go func() {
		expire := <-fired
		<-seen
		expire()
	}()

	c := executor.NewClaude(fakeRunner("stall", path), executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)

	require.NoError(t, err, "an idle timeout is a retryable outcome, not a failure")
	assert.True(t, res.IdleTimedOut)
	assert.Empty(t, res.StructuredOutput)
	assert.NotEmpty(t, res.Raw, "whatever the agent managed to emit is kept")
	assert.NotEmpty(t, clk.AfterFuncCalls())
}
