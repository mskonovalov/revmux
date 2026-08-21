package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

func cursorCleanCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cursor-clean.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

func TestCursor_args(t *testing.T) {
	path := writeFixture(t, cursorCleanCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review this", Model: "composer-2.5", Effort: "high",
		Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "cursor-agent", call.Name)
	assert.Equal(t, []string{
		"--print", "--output-format", "stream-json", "--stream-partial-output",
		"--trust",
		"--model", "composer-2.5[effort=high]",
	}, call.Args)
	assert.NotContains(t, call.Args, "--force", "revmux never lets an agent write")
	assert.NotContains(t, call.Args, "--effort", "cursor-agent has no --effort flag")
	assert.NotContains(t, call.Args, "--approve-mcps",
		"project MCP belongs to the reviewed checkout and must not auto-run")
}

func TestCursor_args_modelAlreadyBracketed(t *testing.T) {
	path := writeFixture(t, cursorCleanCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	req := executor.Request{Prompt: "review this", Model: "grok-4.6[thinking=true]", Effort: "high"}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)
	assert.Contains(t, runner.CommandCalls()[0].Args, "grok-4.6[thinking=true]")
	assert.NotContains(t, strings.Join(runner.CommandCalls()[0].Args, " "), "effort=high")
}

func TestCursor_args_optionalFlagsOmitted(t *testing.T) {
	path := writeFixture(t, cursorCleanCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "review this"}, discardSink())
	require.NoError(t, err)
	assert.NotContains(t, runner.CommandCalls()[0].Args, "--model")
}

func TestCursor_Run_clean(t *testing.T) {
	path := writeFixture(t, cursorCleanCapture(t))
	sink := discardSink()
	c := executor.NewCursor(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "composer-2.5"}, sink)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "composer-2.5", res.RequestedModel)
	assert.Equal(t, "Composer 2.5", res.ActualModel)

	var out struct {
		Findings []struct {
			File string `json:"file"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "sample.go", out.Findings[0].File)

	kinds := make([]executor.EventKind, 0, len(sink.EmitCalls()))
	texts := make([]string, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		kinds = append(kinds, call.Event.Kind)
		texts = append(texts, call.Event.Text)
	}
	assert.Contains(t, kinds, executor.EventActivity)
	assert.Contains(t, kinds, executor.EventProgress)
	assert.Contains(t, texts, "checking whether Cache.Add locks before writing")
	assert.Contains(t, texts, "read sample.go")
}

func TestCursor_skipsPartialDeltas(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"Composer 2.5"}`,
		`{"type":"assistant","timestamp_ms":1,"message":{"content":[{"type":"text","text":"che"}]}}`,
		`{"type":"assistant","timestamp_ms":2,"model_call_id":"x","message":{"content":[{"type":"text","text":"checking"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"checking the lock"}]}}`,
		`{"type":"result","is_error":false,"result":"{\"findings\":[]}"}`,
		"",
	}, "\n")
	sink := discardSink()
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, sink)
	require.NoError(t, err)
	require.NotEmpty(t, res.StructuredOutput)

	var activity []string
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventActivity {
			activity = append(activity, call.Event.Text)
		}
	}
	assert.Equal(t, []string{"checking the lock"}, activity)
}

func TestCursor_errorResultCarriesNoAnswer(t *testing.T) {
	stream := `{"type":"result","subtype":"error","is_error":true,"result":"{\"findings\":[]}"}` + "\n"
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, discardSink())
	require.NoError(t, err)
	assert.Empty(t, res.StructuredOutput)
}

func TestCursor_truncatedStreamDegrades(t *testing.T) {
	data := cursorCleanCapture(t)
	cut := len(data) - 40
	require.Positive(t, cut)
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, data[:cut])), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, discardSink())
	require.NoError(t, err)
	assert.Empty(t, res.StructuredOutput, "a cut before the result event has no findings")
	assert.NotEmpty(t, res.Raw, "the partial stream is still available for the archive")
}

func TestCursor_contractAppended(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	got := executor.CursorContract(schema)
	assert.Contains(t, got, "Return ONLY a JSON object")
	assert.Contains(t, got, string(schema))
	assert.Contains(t, got, "narrate what you are doing")
	assert.Empty(t, executor.CursorContract(nil))
}

func TestPromptContract_routesByExecutor(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	assert.Equal(t, executor.ClaudeNarrationContract(schema), executor.PromptContract("claude", schema))
	assert.Equal(t, executor.CodexOutputContract(schema), executor.PromptContract("codex", schema))
	assert.Equal(t, executor.CursorContract(schema), executor.PromptContract("cursor-agent", schema))
}

func TestCursor_Run_keepsCursorAPIKey(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "cursor-secret")
	t.Setenv("CLAUDECODE", "1")

	c := executor.NewCursor(fakeRunner("env", "-"), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Contains(t, res.Raw, "CURSOR_API_KEY=cursor-secret")
	assert.NotContains(t, res.Raw, "CLAUDECODE=", "the nested-session marker is still stripped")
}

func TestCursor_skipsUnparseableLines(t *testing.T) {
	stream := strings.Join([]string{
		"not json",
		`{"type":"system","subtype":"init","model":"Composer 2.5"}`,
		`{"no-type":true}`,
		`{"type":"result","is_error":false,"result":"{\"findings\":[]}"}`,
		"",
	}, "\n")
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, discardSink())
	require.NoError(t, err)
	require.NotEmpty(t, res.StructuredOutput)
	assert.Equal(t, "Composer 2.5", res.ActualModel)
}

func TestCursor_toolNote(t *testing.T) {
	tests := []struct {
		name, line, want string
	}{
		{
			name: "read with path",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"readToolCall":{"args":{"path":"sample.go"}}}}`,
			want: "read sample.go",
		},
		{
			name: "write with path",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"writeToolCall":{"args":{"path":"out.txt"}}}}`,
			want: "write out.txt",
		},
		{
			name: "named tool without path",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"grepToolCall":{"args":{}}}}`,
			want: "tool: grep",
		},
		{
			name: "empty payload",
			line: `{"type":"tool_call","subtype":"started","tool_call":{}}`,
			want: "tool",
		},
		{
			name: "malformed payload",
			line: `{"type":"tool_call","subtype":"started","tool_call":"not-an-object"}`,
			want: "tool",
		},
		{
			name: "completed is silent",
			line: `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{"args":{"path":"sample.go"}}}}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := tt.line + "\n"
			sink := discardSink()
			c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
			_, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, sink)
			require.NoError(t, err)
			var progress []string
			for _, call := range sink.EmitCalls() {
				if call.Event.Kind == executor.EventProgress {
					progress = append(progress, call.Event.Text)
				}
			}
			if tt.want == "" {
				assert.Empty(t, progress)
				return
			}
			assert.Equal(t, []string{tt.want}, progress)
		})
	}
}

func TestCursor_resultModelWins(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"Composer 2.5"}`,
		`{"type":"result","is_error":false,"model":"Grok 4.6","result":"{\"findings\":[]}"}`,
		"",
	}, "\n")
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, "Grok 4.6", res.ActualModel)
}

func TestCursor_extractsFromResultNotRaw(t *testing.T) {
	// the stream's first brace is system/init; findings live in the result string, possibly after prose
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"Composer 2.5"}`,
		`{"type":"result","is_error":false,"result":"checking the lock\n{\"findings\":[{\"file\":\"sample.go\"}]}"}`,
		"",
	}, "\n")
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, []byte(stream))), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "review"}, discardSink())
	require.NoError(t, err)
	var out struct {
		Findings []struct {
			File string `json:"file"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "sample.go", out.Findings[0].File)
	assert.Contains(t, res.Raw, `"type":"system"`, "Raw still holds the whole stream")
}
