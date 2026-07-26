package executor_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

func TestClaude_Run_reportsModelThatActuallyRan(t *testing.T) {
	data := patchEvent(t, "result", func(ev map[string]any) {
		usage, ok := ev["modelUsage"].(map[string]any)
		require.True(t, ok)
		for name, u := range usage {
			delete(usage, name)
			usage["claude-sonnet-4-6"] = u
		}
	})
	path := writeFixture(t, data)
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "haiku"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, "haiku", res.RequestedModel)
	assert.Equal(t, "claude-sonnet-4-6", res.ActualModel, "--model can be silently ignored")
}

func TestClaude_Run_picksBusiestModel(t *testing.T) {
	data := patchEvent(t, "result", func(ev map[string]any) {
		ev["modelUsage"] = map[string]any{
			"claude-haiku-4-5": map[string]any{"outputTokens": 12},
			"claude-opus-5":    map[string]any{"outputTokens": 4000},
		}
	})
	path := writeFixture(t, data)
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", res.ActualModel, "map order must not decide the answer")
}

func TestClaude_Run_tokensFromUsage(t *testing.T) {
	data := patchEvent(t, "result", func(ev map[string]any) {
		ev["usage"] = map[string]any{
			"input_tokens":                7,
			"output_tokens":               11,
			"cache_read_input_tokens":     100,
			"cache_creation_input_tokens": 200,
		}
	})
	path := writeFixture(t, data)
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, 318, res.Tokens, "cache reads and writes count too")
}

func TestClaude_Run_activityEvents(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	kinds := make([]executor.EventKind, 0, len(sink.EmitCalls()))
	texts := make([]string, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		kinds = append(kinds, call.Event.Kind)
		texts = append(texts, call.Event.Text)
	}
	assert.Equal(t, executor.EventStarted, kinds[0])
	assert.Equal(t, executor.EventFinished, kinds[len(kinds)-1])
	assert.Contains(t, kinds, executor.EventActivity)
	assert.Contains(t, texts, "tool: Read")
	assert.Contains(t, texts, "thinking")
	assert.Contains(t, texts, "exit 0")
}

func TestClaude_Run_activityTextIsBounded(t *testing.T) {
	long := make([]byte, 400)
	for i := range long {
		long[i] = 'a'
	}
	data := patchEvent(t, "assistant", func(ev map[string]any) {
		ev["message"] = map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(long) + "\nsecond line"}},
		}
	})
	path := writeFixture(t, data)
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	for _, call := range sink.EmitCalls() {
		assert.LessOrEqual(t, len(call.Event.Text), 130)
		assert.NotContains(t, call.Event.Text, "second line", "only the first line is shown")
	}
}

func TestClaude_Run_ignoresUndecodableLines(t *testing.T) {
	data := append([]byte("not json at all\n{}\n\n"), cleanCapture(t)...)
	path := writeFixture(t, data)
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.NotEmpty(t, res.StructuredOutput, "garbage before the stream must not stop parsing")

	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	assert.Contains(t, out, "findings")
}
