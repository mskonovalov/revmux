package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

// cursorCleanCapture returns a minimal valid Cursor agent stream-json output.
// Using the same format as Claude's stream-json since Cursor agent uses the same protocol.
func cursorCleanCapture() []byte {
	return []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Checking the code for issues"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"test.go"}}]}}
{"type":"result","structured_output":{"findings":[{"title":"test finding","severity":"minor","lenses":["bugs"]}]},"modelUsage":{"claude-sonnet-4.5":{"inputTokens":100,"outputTokens":50}},"usage":{"input_tokens":100,"output_tokens":50},"ttft_ms":250}
`)
}

func writeCursorFixture(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-stream.jsonl")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestCursor_args(t *testing.T) {
	path := writeCursorFixture(t, cursorCleanCapture())
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{
		Prompt: "review this",
		Model:  "claude-sonnet-4.5",
		Effort: "high",
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "agent", call.Name)

	argv := call.Args
	assert.Contains(t, argv, "-p")
	assert.Contains(t, argv, "-f")
	assert.Contains(t, argv, "--output-format")
	assert.Contains(t, argv, "stream-json")
	assert.Contains(t, argv, "--mode")
	assert.Contains(t, argv, "ask")
	assert.Contains(t, argv, "--model")
	// Cursor CLI uses effort-suffixed model ids; effort is appended to model name
	assert.Contains(t, argv, "claude-sonnet-4.5-high")
	assert.NotContains(t, argv, "--effort")
	assert.NotContains(t, argv, "[effort=")
}

func TestCursor_argsMinimal(t *testing.T) {
	path := writeCursorFixture(t, cursorCleanCapture())
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review this"}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	argv := call.Args

	assert.Contains(t, argv, "-p")
	assert.Contains(t, argv, "-f")
	assert.Contains(t, argv, "--output-format")
	assert.Contains(t, argv, "stream-json")
	assert.Contains(t, argv, "--mode")
	assert.Contains(t, argv, "ask")
	assert.NotContains(t, argv, "--model")
}

func TestCursor_argsComposerNoEffort(t *testing.T) {
	path := writeCursorFixture(t, cursorCleanCapture())
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	tests := []struct {
		name      string
		model     string
		effort    string
		wantModel string
	}{
		{
			name:      "composer-2.5 with medium effort should not append",
			model:     "composer-2.5",
			effort:    "medium",
			wantModel: "composer-2.5",
		},
		{
			name:      "composer-2.5-fast with low effort should not append",
			model:     "composer-2.5-fast",
			effort:    "low",
			wantModel: "composer-2.5-fast",
		},
		{
			name:      "Composer-2.5 uppercase with high effort should not append",
			model:     "Composer-2.5",
			effort:    "high",
			wantModel: "Composer-2.5",
		},
		{
			name:      "kimi-k3 with high effort should append",
			model:     "kimi-k3",
			effort:    "high",
			wantModel: "kimi-k3-high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := executor.Request{
				Prompt: "review this",
				Model:  tt.model,
				Effort: tt.effort,
			}
			_, err := c.Run(context.Background(), req, discardSink())
			require.NoError(t, err)

			require.NotEmpty(t, runner.CommandCalls())
			call := runner.CommandCalls()[len(runner.CommandCalls())-1]
			argv := call.Args

			assert.Contains(t, argv, "--model")
			assert.Contains(t, argv, tt.wantModel)
		})
	}
}

func TestCursor_parseStreamActivity(t *testing.T) {
	path := writeCursorFixture(t, cursorCleanCapture())
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review"}
	res, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	// Should have extracted structured output
	require.NotNil(t, res.StructuredOutput)
	var out struct {
		Findings []struct {
			Title    string `json:"title"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "test finding", out.Findings[0].Title)

	// Should have captured model and tokens
	assert.Equal(t, "claude-sonnet-4.5", res.ActualModel)
	assert.Equal(t, 150, res.Tokens) // 100 input + 50 output
	assert.Equal(t, 250, res.TTFTMs)
}

func TestCursor_extractFromResultText(t *testing.T) {
	// Simulate real Cursor agent CLI output with JSON in the result field
	resultWithProseAndJSON := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Checking the code"}]}}
{"type":"result","subtype":"success","result":"I reviewed the code and found the following:\n\n{\n  \"findings\": [{\"title\": \"extracted finding\", \"severity\": \"major\", \"lenses\": [\"bugs\"]}]\n}","usage":{"input_tokens":120,"output_tokens":60},"ttft_ms":300}
`)
	path := writeCursorFixture(t, resultWithProseAndJSON)
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review"}
	res, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	// Should have extracted structured output from the result text
	require.NotNil(t, res.StructuredOutput)
	var out struct {
		Findings []struct {
			Title    string `json:"title"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "extracted finding", out.Findings[0].Title)
	assert.Equal(t, "major", out.Findings[0].Severity)

	// Should have captured model stats
	assert.Equal(t, 180, res.Tokens) // 120 input + 60 output
	assert.Equal(t, 300, res.TTFTMs)
}

func TestCursor_extractFromResultWithFencedJSON(t *testing.T) {
	// Simulate Cursor output with JSON in fenced code blocks
	resultFenced := []byte(`{"type":"result","subtype":"success","result":"Analysis complete.\n\n` + "```" + `json\n{\"findings\": [{\"title\": \"fenced finding\", \"severity\": \"minor\", \"lenses\": [\"impl\"]}]}\n` + "```" + `","usage":{"input_tokens":100,"output_tokens":40}}
`)
	path := writeCursorFixture(t, resultFenced)
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review"}
	res, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	// Should extract JSON even when fenced
	require.NotNil(t, res.StructuredOutput)
	var out struct {
		Findings []struct {
			Title string `json:"title"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "fenced finding", out.Findings[0].Title)
}

func TestCursor_parseStreamTruncated(t *testing.T) {
	truncated := cursorCleanCapture()[:50] // Cut mid-stream
	path := writeCursorFixture(t, truncated)
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review"}
	res, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	// Truncated stream should degrade gracefully, not error
	assert.Nil(t, res.StructuredOutput)
	assert.Empty(t, res.ActualModel)
	assert.Zero(t, res.Tokens)
}

func TestCursor_authenticated(t *testing.T) {
	// Authentication check assumes binary is available - detailed testing
	// would require mocking the entire command execution flow
	t.Skip("Authentication testing requires live binary or complex mocking")
}

func TestCursor_outputContract(t *testing.T) {
	tests := []struct {
		name   string
		schema json.RawMessage
		want   string
	}{
		{
			name:   "no schema",
			schema: nil,
			want:   "",
		},
		{
			name:   "empty schema",
			schema: json.RawMessage{},
			want:   "",
		},
		{
			name:   "with schema",
			schema: json.RawMessage(`{"type":"object"}`),
			want:   "Return ONLY a JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executor.CursorOutputContract(tt.schema)
			if tt.want == "" {
				assert.Empty(t, got)
			} else {
				assert.Contains(t, got, tt.want)
				assert.Contains(t, got, string(tt.schema))
			}
		})
	}
}

func TestCursor_requestedModelPreserved(t *testing.T) {
	path := writeCursorFixture(t, cursorCleanCapture())
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{
		Prompt: "review",
		Model:  "gpt-5.6-sol",
	}
	res, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	assert.Equal(t, "gpt-5.6-sol", res.RequestedModel)
	assert.Equal(t, "claude-sonnet-4.5", res.ActualModel)
}
