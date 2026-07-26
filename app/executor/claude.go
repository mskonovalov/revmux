package executor

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// Claude runs the claude CLI in print mode and decodes its stream-json output.
type Claude struct {
	proc
}

// NewClaude builds a claude executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewClaude(runner CommandRunner, opts Opts) *Claude {
	return &Claude{proc: newProc("claude", runner, opts)}
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error — whether that degrades the source is the pipeline's call.
func (c *Claude) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
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

func (c *Claude) args(req Request) []string {
	argv := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "dontAsk",
		"--disallowedTools", "Edit,Write,NotebookEdit",
		"--disable-slash-commands",
		"--no-session-persistence",
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "--effort", req.Effort)
	}
	if len(req.Schema) > 0 {
		argv = append(argv, "--json-schema", string(req.Schema))
	}
	return argv
}

// parseStream consumes the stream and returns what it learned. A line that fails to decode is skipped:
// a truncated stream must degrade to a partial Result rather than fail the run.
func (c *Claude) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result {
	res := Result{}
	_ = c.readLines(ctx, r, func(line string) {
		ev, ok := c.event(line)
		if !ok {
			return
		}
		switch ev.Type {
		case "assistant":
			if text := ev.activity(); text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: text})
			}
		case "rate_limit_event":
			if ev.RateLimitInfo == nil {
				return
			}
			res.RateLimit = *ev.RateLimitInfo
			res.RateLimited = ev.RateLimitInfo.Status != "allowed"
			if res.RateLimited {
				c.emit(sink, Event{Kind: EventRateLimit, Text: ev.RateLimitInfo.Status})
			}
		case "result":
			res.StructuredOutput = ev.StructuredOutput
			res.ActualModel = ev.actualModel()
			res.Tokens = ev.tokens()
			res.TTFTMs = ev.TTFTMs
		}
	})
	return res
}

func (c *Claude) event(line string) (streamEvent, bool) {
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
