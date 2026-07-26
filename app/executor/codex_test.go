package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
)

// the prose a codex run puts around its answer when it does not honor the contract to the letter.
const (
	codexPrologue = "I read the file and applied both lenses.\n\n"
	codexEpilogue = "\n\nThat is the complete list.\n"
)

// codexCapture is the one live codex recording. Every other codex fixture below is derived from its
// bytes, so re-recording it regenerates the whole family.
func codexCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-clean.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return bytes.TrimSpace(data)
}

// codexStderrCapture is the stderr of that same run: the resolved-configuration banner, the echoed
// prompt and the tool chatter, none of which stdout carries.
func codexStderrCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-clean.err.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

func codexProseCapture(t *testing.T) []byte {
	t.Helper()
	return []byte(codexPrologue + string(codexCapture(t)) + codexEpilogue)
}

// codexNoJSONCapture is the prose-wrapped run with the answer block stripped out.
func codexNoJSONCapture(t *testing.T) []byte {
	t.Helper()
	out := bytes.ReplaceAll(codexProseCapture(t), codexCapture(t), nil)
	require.NotContains(t, string(out), "{")
	return out
}

// codexStalledCapture cuts the recording inside the answer, which is what a killed process leaves.
func codexStalledCapture(t *testing.T) []byte {
	t.Helper()
	data := codexCapture(t)
	cut := len(data) / 2
	require.Positive(t, cut)
	return data[:cut]
}

func eventTexts(sink *mocks.EventSinkMock, kind executor.EventKind) []string {
	out := []string{}
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == kind {
			out = append(out, call.Event.Text)
		}
	}
	return out
}

func eventKinds(sink *mocks.EventSinkMock) []executor.EventKind {
	out := make([]executor.EventKind, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		out = append(out, call.Event.Kind)
	}
	return out
}

func TestCodex_args(t *testing.T) {
	path := writeFixture(t, codexCapture(t))

	t.Run("model and effort from the request", func(t *testing.T) {
		runner := fakeRunner("emit", path)
		c := executor.NewCodex(runner, executor.Opts{})
		req := executor.Request{Prompt: "review this", Model: "gpt-5.6-sol", Effort: "xhigh"}
		_, err := c.Run(context.Background(), req, discardSink())
		require.NoError(t, err)

		require.Len(t, runner.CommandCalls(), 1)
		call := runner.CommandCalls()[0]
		assert.Equal(t, "codex", call.Name)
		want := []string{"exec", "--sandbox", "read-only", "-m", "gpt-5.6-sol", "-c", "model_reasoning_effort=xhigh"}
		assert.Equal(t, want, call.Args)
	})

	t.Run("optional flags omitted", func(t *testing.T) {
		runner := fakeRunner("emit", path)
		c := executor.NewCodex(runner, executor.Opts{})
		_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)

		args := runner.CommandCalls()[0].Args
		assert.Equal(t, []string{"exec", "--sandbox", "read-only"}, args)
		assert.Contains(t, args, "read-only", "codex never gets a writable sandbox")
	})
}

func TestCodex_outputContract(t *testing.T) {
	tests := []struct {
		name   string
		schema json.RawMessage
		want   string
	}{
		{"finder stage", finding.FinderSchema(), "revmux finder findings"},
		{"verify stage", finding.VerifySchema(), "verdict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, codexCapture(t))
			c := executor.NewCodex(fakeRunner("echo", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "review this", Schema: tt.schema}, discardSink())
			require.NoError(t, err)

			assert.Contains(t, res.Raw, "review this")
			assert.Contains(t, res.Raw, "Return ONLY a JSON object matching the schema below")
			assert.Contains(t, res.Raw, string(tt.schema), "the stage's own schema is rendered inline")
			assert.Contains(t, res.Raw, tt.want)
		})
	}

	t.Run("no schema, no contract", func(t *testing.T) {
		path := writeFixture(t, codexCapture(t))
		c := executor.NewCodex(fakeRunner("echo", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "review this"}, discardSink())
		require.NoError(t, err)
		assert.Equal(t, "review this", res.Raw)
	})

	t.Run("a claude prompt is never wrapped", func(t *testing.T) {
		path := writeFixture(t, codexCapture(t))
		c := executor.NewClaude(fakeRunner("echo", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "review this", Schema: finding.FinderSchema()}, discardSink())
		require.NoError(t, err)
		assert.Equal(t, "review this", res.Raw, "claude gets its contract from --json-schema")
	})
}

func TestCodex_extract(t *testing.T) {
	tests := []struct {
		name    string
		output  []byte
		wantErr bool
	}{
		{name: "clean", output: codexCapture(t)},
		{name: "prose wrapped", output: codexProseCapture(t)},
		{name: "no json", output: codexNoJSONCapture(t), wantErr: true},
		{name: "stalled mid-answer", output: codexStalledCapture(t), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.output)
			c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
			require.NoError(t, err, "output holding no answer degrades the source, it does not fail the run")

			if tt.wantErr {
				assert.Empty(t, res.StructuredOutput)
				return
			}
			var out struct {
				Findings []struct {
					File   string   `json:"file"`
					Lenses []string `json:"lenses"`
				} `json:"findings"`
			}
			require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
			assert.NotEmpty(t, out.Findings)
			assert.NotEmpty(t, out.Findings[0].File)
			assert.NotEmpty(t, out.Findings[0].Lenses)
		})
	}
}

func TestCodex_Run_clean(t *testing.T) {
	data := codexCapture(t)
	path := writeFixture(t, data)
	raw := &bytes.Buffer{}
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
	req := executor.Request{Prompt: "x", Model: "gpt-5.6-sol", RawOutput: raw}
	res, err := c.Run(context.Background(), req, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.False(t, res.RateLimited)
	assert.NotEmpty(t, res.StructuredOutput)
	assert.Equal(t, "gpt-5.6-sol", res.RequestedModel)
	assert.Equal(t, data, raw.Bytes(), "the archive keeps the bytes the process produced")
}

func TestCodex_Run_firstStdoutWriteEmitsActivity(t *testing.T) {
	path := writeFixture(t, codexCapture(t))
	sink := discardSink()
	clk := &mocks.ClockMock{}

	c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{Clock: clk})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	kinds := eventKinds(sink)
	activity := slices.Index(kinds, executor.EventActivity)
	require.GreaterOrEqual(t, activity, 0, "the raw stdout write is a codex leader's only release signal")
	assert.Less(t, activity, slices.Index(kinds, executor.EventFinished),
		"activity arrives on the write, not when the process is reaped")
	assert.Empty(t, clk.AfterFuncCalls(), "the release came from the write, never from a timer")
}

func TestCodex_Run_stderrHeader(t *testing.T) {
	path := writeFixture(t, codexCapture(t))
	errPath := writeFixture(t, codexStderrCapture(t))
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("emit", path, errPath), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "requested"}, sink)
	require.NoError(t, err)

	assert.Equal(t, "requested", res.RequestedModel)
	assert.Equal(t, "gpt-5.6-sol", res.ActualModel, "the report states what actually ran")

	texts := eventTexts(sink, executor.EventActivity)
	assert.Contains(t, texts, "model: gpt-5.6-sol")
	assert.Contains(t, texts, "sandbox: read-only")
	assert.Contains(t, texts, "reasoning effort: high")

	assert.Equal(t, 1, slices.Index(texts, "model: gpt-5.6-sol")+1, "the header is forwarded once per process")
	for _, text := range texts {
		assert.NotContains(t, text, "session id", "the rest of the banner is suppressed")
		assert.NotContains(t, text, "provider")
		assert.NotContains(t, text, "hook:")
	}
}

func TestCodex_Run_patternTiers(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		stderr      string
		wantErr     string
		wantLimited bool
	}{
		{name: "transient server hiccup is a retryable failure", stdout: "stream failed\nAPI Error: 503 upstream unavailable\n",
			wantErr: "codex transient failure: api error: 503"},
		{name: "quota diagnostic is a limit", stderr: "ERROR: You've hit your usage limit.\n", wantLimited: true},
		{name: "rate limit in the output tail is a limit", stdout: "429 Too Many Requests\n", wantLimited: true},
		{name: "any other diagnostic is a hard error", stderr: "ERROR: unexpected argument --nope\n",
			wantErr: "codex failed: ERROR: unexpected argument --nope"},
		{name: "500 is not transient", stderr: "ERROR: API Error: 500 internal\n",
			wantErr: "codex failed: ERROR: API Error: 500 internal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, []byte(tt.stdout))
			errPath := writeFixture(t, []byte(tt.stderr))
			sink := discardSink()

			c := executor.NewCodex(fakeRunner("fail", path, errPath), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)

			assert.Equal(t, 3, res.ExitCode)
			assert.Equal(t, tt.wantLimited, res.RateLimited)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Contains(t, eventKinds(sink), executor.EventRateLimit)
				assert.NotEmpty(t, res.RateLimit.RateLimitType)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCodex_Run_cleanExitIsNeverAPatternMatch(t *testing.T) {
	// a findings body reviewing rate-limit handling contains the words itself
	body := append(codexCapture(t), []byte("\nRate limit exceeded is unhandled in the reviewed code.\n")...)
	path := writeFixture(t, body)
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.RateLimited, "patterns are only consulted when the process failed")
	assert.NotContains(t, eventKinds(sink), executor.EventRateLimit)
	assert.NotEmpty(t, res.StructuredOutput, "the answer is still extracted")
}

func TestCodex_Run_patternsSeeOnlyTheTail(t *testing.T) {
	// a long findings body naming a limit early on, then 64k of ordinary review prose after it
	const phrase = "Rate limit exceeded is unhandled at line 40.\n"
	filler := bytes.Repeat([]byte("the handler returns the wrapped error to its caller.\n"), 1200)

	t.Run("a match buried above the tail is invisible", func(t *testing.T) {
		path := writeFixture(t, append([]byte(phrase), filler...))
		sink := discardSink()

		c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
		require.NoError(t, err)

		assert.Equal(t, 3, res.ExitCode)
		assert.False(t, res.RateLimited, "matching the whole output self-triggers on findings about rate limits")
		assert.NotContains(t, eventKinds(sink), executor.EventRateLimit)
	})

	t.Run("the same phrase in the tail is a limit", func(t *testing.T) {
		path := writeFixture(t, append(filler, []byte(phrase)...))

		c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)

		assert.True(t, res.RateLimited)
		assert.Equal(t, "rate limit exceeded", res.RateLimit.RateLimitType)
	})
}

func TestCodex_Run_planQuotaOnStderrWithEmptyStdout(t *testing.T) {
	path := writeFixture(t, nil)
	errPath := writeFixture(t, []byte("OpenAI Codex v0.145.0\nERROR: You've hit your usage limit.\n"))
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("fail", path, errPath), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.Empty(t, res.Raw, "a stdout-only check would miss this entirely")
	assert.True(t, res.RateLimited)
	assert.Equal(t, "you've hit your usage limit", res.RateLimit.RateLimitType)
	assert.Contains(t, eventKinds(sink), executor.EventRateLimit)
}

func TestCodex_Run_canceledContextSkipsPatterns(t *testing.T) {
	path := writeFixture(t, []byte("429 Too Many Requests\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
	res, err := c.Run(ctx, executor.Request{Prompt: "x"}, discardSink())
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, res.RateLimited, "a canceled run's tail is meaningless")
}
