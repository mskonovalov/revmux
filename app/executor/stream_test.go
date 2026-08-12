package executor_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestClaude_Run_activityTruncatesOnRuneBoundary(t *testing.T) {
	// assistant prose is arbitrary text; a byte-indexed cut lands mid-rune and puts invalid UTF-8
	// into the TUI row, the --no-tui stderr line and events.jsonl
	long := strings.Repeat("ä", 400)
	data := patchEvent(t, "assistant", func(ev map[string]any) {
		msg, ok := ev["message"].(map[string]any)
		require.True(t, ok)
		msg["content"] = []any{map[string]any{"type": "text", "text": long}}
	})
	path := writeFixture(t, data)
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var got string
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventActivity && strings.HasPrefix(call.Event.Text, "ä") {
			got = call.Event.Text
		}
	}
	require.NotEmpty(t, got, "the patched assistant text must reach the sink")
	assert.True(t, utf8.ValidString(got), "a truncated activity line must stay valid UTF-8, got %q", got)
	assert.Equal(t, long, got, "400 runes is well inside the sanity bound, so nothing is cut at all")
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
	var activity, progress []string
	for _, call := range sink.EmitCalls() {
		kinds = append(kinds, call.Event.Kind)
		texts = append(texts, call.Event.Text)
		switch call.Event.Kind {
		case executor.EventActivity:
			activity = append(activity, call.Event.Text)
		case executor.EventProgress:
			progress = append(progress, call.Event.Text)
		case executor.EventInfo, executor.EventRateLimit, executor.EventFinished:
			// lifecycle kinds, asserted through kinds and texts above
		}
	}
	assert.Contains(t, kinds, executor.EventActivity)
	assert.NotContains(t, texts, "exit 0", "a clean exit is not worth a log line")
	assert.NotContains(t, kinds, executor.EventFinished, "and it emits no lifecycle event either")

	// activity is the model's prose and nothing else; a tool name reaching it is the regression that
	// buried the reasoning under pages of "tool: Read"
	assert.Contains(t, activity, "I'll read the file and review it under the bugs lens.")
	assert.NotContains(t, activity, "Read", "a tool name is progress, never activity")
	assert.NotContains(t, activity, "thinking", "thinking is progress, never activity")
	for _, text := range activity {
		assert.NotContains(t, text, "tool: ", "the tool: prefix must not exist at all any more")
	}

	// progress is tool calls and nothing else. A thinking turn used to emit a bare "thinking", which
	// reported nothing — the blocks arrive encrypted and empty — and read as noise in every log line
	// it took
	assert.NotContains(t, progress, "thinking", "a thinking turn is not progress worth a line")
	// the tool's argument rides along with its name: "Read" repeated down a log says only that the
	// agent is alive, while the path says what it is actually looking at
	require.Contains(t, progress, "Read revmux-capture/sample.go",
		"a tool call carries what it is acting on, and that is the useful half")
}

func TestClaude_Run_activityTextIsBounded(t *testing.T) {
	// two separate properties, and asserting both on one input hides both: a text long enough to prove
	// the bound is long enough that the clamp eats the trailing line the flattening exists to keep
	emit := func(t *testing.T, text string) string {
		t.Helper()
		data := patchEvent(t, "assistant", func(ev map[string]any) {
			ev["message"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		})
		sink := discardSink()
		c := executor.NewClaude(fakeRunner("emit", writeFixture(t, data)), executor.Opts{})
		_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
		require.NoError(t, err)

		// the patched block specifically: the recording carries its own closing summary as a later
		// text block, and taking the last activity would assert against that instead
		var got string
		for _, call := range sink.EmitCalls() {
			if call.Event.Kind == executor.EventActivity && strings.HasPrefix(call.Event.Text, "aaa") {
				got = call.Event.Text
			}
		}
		require.NotEmpty(t, got, "the patched assistant text must reach the sink")
		return got
	}

	t.Run("later lines are folded in rather than discarded", func(t *testing.T) {
		// a closing summary opens with a lead-in and everything worth reading is on the lines after
		// it, so cutting at the first newline threw the whole body away
		got := emit(t, "aaa opening line\nsecond line\n\nthird line")
		assert.Equal(t, "aaa opening line second line third line", got)
		assert.NotContains(t, got, "\n", "an event is one line by construction")
	})

	t.Run("a pathological block is still bounded", func(t *testing.T) {
		got := emit(t, strings.Repeat("a", 2400))
		assert.LessOrEqual(t, utf8.RuneCountInString(got), 2003, "the sanity bound still applies")
		assert.True(t, strings.HasSuffix(got, "..."), "and says it was cut")
	})
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

func TestClaude_Run_denialLineIsBounded(t *testing.T) {
	// each command is clamped on its own, so the joined line is only bounded if it is clamped too:
	// enough distinct denials and the line runs far past what any one of them is allowed
	denials := make([]any, 0, 60)
	for i := range 60 {
		denials = append(denials, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_" + strconv.Itoa(i),
			"tool_input": map[string]any{"command": strings.Repeat("x", 100) + strconv.Itoa(i)},
		})
	}
	data := patchEvent(t, "result", func(ev map[string]any) { ev["permission_denials"] = denials })

	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", writeFixture(t, data)), executor.Opts{})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var line string
	for _, call := range sink.EmitCalls() {
		if strings.HasPrefix(call.Event.Text, "denied ") {
			line = call.Event.Text
		}
	}
	require.NotEmpty(t, line, "60 refusals must still be reported")
	assert.Contains(t, line, "denied 60 tool call(s)", "the count is every denial, however many are named")
	assert.LessOrEqual(t, utf8.RuneCountInString(line), 2003, "and the line itself is bounded")
	assert.True(t, strings.HasSuffix(line, "..."), "and says it was cut")
}
