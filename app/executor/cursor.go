package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Cursor runs the Cursor agent CLI in print mode and decodes its stream-json output.
//
// Authentication: The agent CLI uses the same Cursor session login as the desktop IDE (agent login),
// stored in ~/.cursor/auth.json. It does NOT use CURSOR_API_KEY or CURSOR_AUTH_TOKEN environment
// variables. In fact, CURSOR_AUTH_TOKEN (type session JWT) overrides the stored login and breaks
// the CLI even after a real login. revmux strips CURSOR_AUTH_TOKEN from cursor agents automatically
// via --strip-env (default includes CLAUDECODE, ANTHROPIC_API_KEY, CURSOR_AUTH_TOKEN).
//
// Model IDs: The Cursor CLI uses effort-suffixed model names (e.g. gpt-5.3-codex-low) rather than
// bracket syntax like model[effort=high]. Model strings from the profile are passed as-is to --model.
//
// Output format: The agent CLI returns stream-json, but findings are embedded in the result text field
// rather than a separate structured_output field. The parseStream method extracts JSON from that text.
type Cursor struct {
	proc
}

// NewCursor builds a cursor executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewCursor(runner CommandRunner, opts Opts) *Cursor {
	return &Cursor{proc: newProc("agent", runner, opts)}
}

// Authenticated asks Cursor for the auth state. The agent CLI stores login credentials in
// ~/.cursor/auth.json (or ~/.config/cursor/auth.json), so we check for the presence of that file
// as a proxy for being logged in.
func (c *Cursor) Authenticated(ctx context.Context) (bool, error) {
	// Check if the agent binary is available first
	cmd := c.runner.Command(ctx, "agent", "--version")
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("cursor agent binary not available: %w", err)
	}

	// Check for auth file in standard locations
	home := os.Getenv("HOME")
	if home == "" {
		return false, errors.New("HOME not set, cannot locate cursor auth")
	}

	authPaths := []string{
		filepath.Join(home, ".cursor", "auth.json"),
		filepath.Join(home, ".config", "cursor", "auth.json"),
	}

	for _, path := range authPaths {
		cleanPath := filepath.Clean(path)
		info, err := os.Stat(cleanPath)
		if err == nil && info.Size() > 0 {
			return true, nil
		}
	}

	return false, nil
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
		"-f", // force-allow workspace trust (revmux already chose the workdir)
		"--output-format", "stream-json",
		"--mode", "ask", // read-only mode for reviews
	}
	// Cursor CLI uses effort-suffixed model ids (e.g. composer-2.5-low, kimi-k3-high).
	// If the model name doesn't already end with the effort level, append it.
	if req.Model != "" {
		modelID := req.Model
		if req.Effort != "" && !strings.HasSuffix(strings.ToLower(req.Model), "-"+strings.ToLower(req.Effort)) {
			modelID = req.Model + "-" + req.Effort
		}
		argv = append(argv, "--model", modelID)
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
			// Cursor agent CLI embeds findings in the result text field when structured_output is empty
			if len(res.StructuredOutput) == 0 && ev.Result != "" {
				if out, err := c.extract(ev.Result); err == nil {
					res.StructuredOutput = out
				}
			}
			res.ActualModel = ev.actualModel()
			res.Tokens = ev.tokens()
			res.TTFTMs = ev.TTFTMs
		case "rate_limit_event":
			// Handle rate limit events. Only status "rejected" means the request failed;
			// "allowed_warning" rides along on successful responses.
			if ev.RateLimitInfo != nil {
				res.RateLimit = *ev.RateLimitInfo
				res.RateLimited = ev.RateLimitInfo.Status == rateLimitRejected
				if res.RateLimited {
					c.emit(sink, Event{Kind: EventRateLimit, Text: "rate limited"})
				}
			}
		}
	})
	return res
}

// extract pulls the findings JSON out of result text that may carry prose around it. Decoding starts at
// each brace in turn, and an incomplete tail ends the search rather than continuing into it: a nested
// object inside a truncated answer would otherwise come back looking like the whole answer.
func (c *Cursor) extract(result string) (json.RawMessage, error) {
	for i, ch := range result {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(result[i:])).Decode(&out)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil, errors.New("no JSON object in cursor result text")
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
