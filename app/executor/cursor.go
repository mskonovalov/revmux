package executor

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

const executorCursor = "cursor-agent"

// Cursor runs the Cursor Agent CLI in print mode and decodes its stream-json output.
type Cursor struct {
	proc
}

// NewCursor builds a cursor-agent executor. A nil Opts.Clock is filled with the production clock,
// because the composition root assembles Opts from flags that carry no clock at all.
func NewCursor(runner CommandRunner, opts Opts) *Cursor {
	return &Cursor{proc: newProc(executorCursor, runner, opts)}
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error — whether that degrades the source is the pipeline's call.
func (c *Cursor) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += CursorContract(req.Schema)
	spec := runSpec{
		argv: c.args(req),
		sink: sink,
		parse: func(ctx context.Context, r io.Reader) Result {
			return c.parseStream(ctx, r, sink)
		},
	}
	res, err := c.run(ctx, req, spec)
	res.RequestedModel = req.Model
	return res, err
}

// CursorContract is cursor-agent's substitute for claude's --json-schema, plus the narration that
// otherwise goes missing: print mode suppresses thinking, so without this the TUI is silent while the
// agent works and the idle watchdog has only tool_call lines to live on. Exported because Run appends
// it after the caller archived the composed prompt.
func CursorContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return ClaudeNarrationContract(schema) +
		"\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
		string(schema) + "\n"
}

// args builds the invocation. --stream-partial-output is there for the idle watchdog rather than for
// anything revmux decodes: without it a long answer arrives as one line written after it is complete,
// which is the same blind window --include-partial-messages closes for claude. --trust skips the
// headless workspace prompt that would otherwise hang a supervised run. --force is deliberately absent:
// revmux never lets an agent write. --approve-mcps is absent too: Cursor loads `.cursor/mcp.json` from
// the reviewed checkout, and auto-approving those servers would run repository-defined commands as the
// reviewer.
func (c *Cursor) args(req Request) []string {
	argv := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--trust",
	}
	if model := cursorModel(req); model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// cursorModel folds effort into the --model value, because cursor-agent has no --effort flag. The
// documented form is id[effort=high]. A model that already carries brackets is left alone, so a roster
// that pins thinking or context itself is not rewritten.
func cursorModel(req Request) string {
	if req.Model == "" {
		return ""
	}
	if req.Effort == "" || strings.Contains(req.Model, "[") {
		return req.Model
	}
	return req.Model + "[effort=" + req.Effort + "]"
}

// parseStream consumes the stream and returns what it learned. A line that fails to decode is skipped:
// a truncated stream must degrade to a partial Result rather than fail the run.
func (c *Cursor) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result {
	res := Result{}
	_ = c.readLines(ctx, r, func(line string) {
		ev, ok := c.event(line)
		if !ok {
			return
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && ev.Model != "" && res.ActualModel == "" {
				res.ActualModel = ev.Model
			}
		case "assistant":
			if ev.delta() {
				return
			}
			if text := ev.assistantText(); text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: text})
			}
		case "tool_call":
			if ev.Subtype != "started" {
				return
			}
			if note := ev.toolNote(); note != "" {
				c.emit(sink, Event{Kind: EventProgress, Text: note})
			}
		case "result":
			if ev.IsError {
				return
			}
			if out, err := extractJSON(ev.Result); err == nil {
				res.StructuredOutput = out
			}
			if ev.Model != "" {
				res.ActualModel = ev.Model
			}
		}
	})
	return res
}

func (c *Cursor) event(line string) (cursorEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return cursorEvent{}, false
	}
	var ev cursorEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return cursorEvent{}, false
	}
	return ev, ev.Type != ""
}

// cursorEvent is one stream-json line from cursor-agent. Unknown fields are ignored, which is the
// documented compatibility rule.
type cursorEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Model     string          `json:"model"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	Message   cursorMessage   `json:"message"`
	ToolCall  json.RawMessage `json:"tool_call"`
	Timestamp json.RawMessage `json:"timestamp_ms"`
	CallID    json.RawMessage `json:"model_call_id"`
}

type cursorMessage struct {
	Content []cursorContent `json:"content"`
}

type cursorContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// delta reports a --stream-partial-output text chunk. Those exist to keep the idle watchdog alive and
// must not become TUI lines: each is a few characters, and the complete assistant message arrives as a
// later event without timestamp_ms.
func (ev cursorEvent) delta() bool {
	return len(ev.Timestamp) > 0
}

func (ev cursorEvent) assistantText() string {
	var b strings.Builder
	for _, block := range ev.Message.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func (ev cursorEvent) toolNote() string {
	if len(ev.ToolCall) == 0 {
		return ""
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(ev.ToolCall, &call); err != nil {
		return "tool"
	}
	for name, raw := range call {
		var body struct {
			Args struct {
				Path string `json:"path"`
			} `json:"args"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &body)
		label := strings.TrimSuffix(name, "ToolCall")
		if label == "" {
			label = body.Name
		}
		if body.Args.Path != "" {
			return label + " " + body.Args.Path
		}
		if label != "" {
			return "tool: " + label
		}
	}
	return "tool"
}
