package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Cursor runs the Cursor agent CLI in print mode and decodes its stream-json output.
type Cursor struct {
	proc
}

// NewCursor builds a cursor executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewCursor(runner CommandRunner, opts Opts) *Cursor {
	return &Cursor{proc: newProc("agent", runner, opts)}
}

// Authenticated asks Cursor for the auth state. The agent CLI currently doesn't have a separate
// auth status command, so we assume authentication if the binary is available.
func (c *Cursor) Authenticated(ctx context.Context) (bool, error) {
	// The agent CLI doesn't have a dedicated auth status command yet.
	// Check if the binary exists and is executable.
	cmd := c.runner.Command(ctx, "agent", "--version")
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("cursor agent binary not available: %w", err)
	}
	return true, nil
}

// Login starts the provider's interactive flow on the controlling terminal.
func (c *Cursor) Login(ctx context.Context, terminal io.ReadWriter) error {
	if terminal == nil {
		return errors.New("cursor authentication requires interactive login through Cursor IDE")
	}
	return errors.New("cursor authentication must be done through Cursor IDE settings")
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error — whether that degrades the source is the pipeline's call.
func (c *Cursor) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	// Cursor agent doesn't have json-schema support yet, so we append output contract to prompt
	req.Prompt += CursorOutputContract(req.Schema)
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

// CursorOutputContract asks for structured JSON output when a schema is provided.
// Exported for the same reason ClaudeNarrationContract is: Run appends it after the prompt was archived.
func CursorOutputContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\n" +
		"As you work, briefly narrate what you are checking and why, then provide your findings in " +
		"the structured output.\n\n" +
		"Schema:\n" + string(schema) + "\n"
}

// args builds the invocation for the Cursor agent CLI.
func (c *Cursor) args(req Request) []string {
	argv := []string{
		"-p", // --print mode
		"--output-format", "stream-json",
		"--mode", "ask", // read-only mode for reviews
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	// Cursor agent uses --effort for reasoning effort levels
	if req.Effort != "" {
		argv = append(argv, "--effort", req.Effort)
	}
	return argv
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
		case "assistant":
			// Extract activity and progress from assistant events
			if text := ev.activity(); text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: text})
			}
			if note := ev.progress(); note != "" {
				c.emit(sink, Event{Kind: EventProgress, Text: note})
			}
		case "result":
			// Extract structured output and model info from result events
			res.StructuredOutput = ev.StructuredOutput
			res.ActualModel = ev.actualModel()
			res.Tokens = ev.tokens()
			res.TTFTMs = ev.TTFTMs
		case "rate_limit":
			// Handle rate limit events if present
			if ev.RateLimitInfo != nil {
				res.RateLimit = *ev.RateLimitInfo
				res.RateLimited = true
				c.emit(sink, Event{Kind: EventRateLimit, Text: "rate limited"})
			}
		}
	})
	return res
}

func (c *Cursor) event(line string) (streamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return streamEvent{}, false
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return streamEvent{}, false
	}
	return ev, ev.Type != ""
}
