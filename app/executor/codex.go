package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// readChunk is how much stdout one read takes. Codex answers in one long block, so the parser reads raw
// chunks rather than lines: the first chunk is what tells the stagger this process is alive.
const readChunk = 32 << 10

// patternTailBytes bounds how much output the tiering looks at. A review agent's findings say "rate
// limit" whenever it reviews code that handles one, so only the tail of a failed run is ever matched.
const patternTailBytes = 2 << 10

// pattern tiers, in the order the rules fix them: transient hiccups worth another attempt, then quota
// and rate limits. Matching is case-insensitive substring, so every pattern here is lowercase.
var (
	// 500 is deliberately absent: it can be a deterministic failure, so it belongs in the error tier.
	codexRetryPatterns = []string{"api error: 529", "api error: 502", "api error: 503", "api error: 504"}

	codexLimitPatterns = []string{"rate limit exceeded", "rate limit reached", "429 too many requests",
		"quota exceeded", "insufficient_quota", "you've hit your usage limit"}
)

// codexHeaderKeys are the resolved-configuration lines worth forwarding out of codex's noisy stderr.
var codexHeaderKeys = []string{"model", "sandbox", "reasoning effort"}

// Codex runs the codex CLI. Its stdout is prose rather than stream-json, so the output contract rides on
// the prompt and the answer is extracted from whatever the process printed.
type Codex struct {
	proc
}

// NewCodex builds a codex executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewCodex(runner CommandRunner, opts Opts) *Codex {
	return &Codex{proc: newProc("codex", runner, opts)}
}

// Run executes one request. A non-zero exit or an idle timeout comes back on the Result rather than as
// an error, and output holding no JSON degrades the source instead of failing the run.
func (c *Codex) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += c.outputContract(req.Schema)
	errs := newCodexStderr(func(ev Event) { c.emit(sink, ev) })

	spec := runSpec{
		argv:       c.args(req),
		sink:       sink,
		parse:      func(ctx context.Context, r io.Reader) Result { return c.drain(ctx, r, sink) },
		stderrLine: errs.line,
	}

	res, err := c.run(ctx, req, spec)
	res.RequestedModel = req.Model
	res.ActualModel = errs.model
	if err != nil {
		return res, err
	}
	if out, exErr := c.extract(res.Raw); exErr == nil {
		res.StructuredOutput = out
	}
	return c.classify(res, errs.diag, sink)
}

func (c *Codex) args(req Request) []string {
	argv := []string{"exec", "--sandbox", "read-only"}
	if req.Model != "" {
		argv = append(argv, "-m", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+req.Effort)
	}
	return argv
}

// outputContract is codex's substitute for claude's --json-schema. The schema comes off the request so a
// codex entry running synthesis or verify asks for that stage's shape rather than the finder's.
func (c *Codex) outputContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
		string(schema) + "\n"
}

// drain consumes stdout, emitting one activity event on the first bytes to arrive. Codex has no
// stream-json, so that raw write is the only signal a codex leader has to release the rest of the
// roster with, and without it stagger_delay becomes the only release path.
func (c *Codex) drain(ctx context.Context, r io.Reader, sink EventSink) Result {
	buf := make([]byte, readChunk)
	var once sync.Once
	for ctx.Err() == nil {
		n, err := r.Read(buf)
		if n > 0 {
			once.Do(func() { c.emit(sink, Event{Kind: EventActivity, Text: "output"}) })
		}
		if err != nil {
			break
		}
	}
	return Result{}
}

// extract pulls the answer out of output that may carry prose around it. Decoding starts at each brace
// in turn, and an incomplete tail ends the search rather than continuing into it: a nested object inside
// a truncated answer would otherwise come back looking like the whole answer.
func (c *Codex) extract(raw string) (json.RawMessage, error) {
	for i, ch := range raw {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(raw[i:])).Decode(&out)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil, errors.New("no JSON object in codex output")
}

// classify tiers a failed run: transient hiccup, quota, or a hard diagnostic. Patterns are consulted
// only on a non-zero exit and only against the tail plus the one stderr diagnostic line, and an idle
// timeout never becomes an error, since the caller retries that on its own.
func (c *Codex) classify(res Result, diag string, sink EventSink) (Result, error) {
	if res.ExitCode == 0 {
		return res, nil
	}
	text := c.tail(res.Raw) + "\n" + diag

	if p := c.match(text, codexRetryPatterns); p != "" && !res.IdleTimedOut {
		return res, fmt.Errorf("codex transient failure: %s", p)
	}
	if p := c.match(text, codexLimitPatterns); p != "" {
		res.RateLimited = true
		res.RateLimit = RateLimitInfo{Status: "limited", RateLimitType: p}
		c.emit(sink, Event{Kind: EventRateLimit, Text: p})
		return res, nil
	}
	if diag != "" && !res.IdleTimedOut {
		return res, fmt.Errorf("codex failed: %s", diag)
	}
	return res, nil
}

func (c *Codex) tail(raw string) string {
	if len(raw) <= patternTailBytes {
		return raw
	}
	return raw[len(raw)-patternTailBytes:]
}

func (c *Codex) match(text string, patterns []string) string {
	lower := strings.ToLower(text)
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// codexStderr filters one run's stderr down to what is worth keeping. It is per-run state and therefore
// never a field on Codex, which serves the whole roster concurrently from one instance.
type codexStderr struct {
	emit  func(Event)
	seen  map[string]bool
	model string
	diag  string
}

func newCodexStderr(emit func(Event)) *codexStderr {
	return &codexStderr{emit: emit, seen: make(map[string]bool)}
}

// line handles one stderr line: it forwards the resolved model, sandbox and effort once each, keeps the
// model so the report states what actually ran, and records the first CLI diagnostic. Diagnostics are
// kept separately from stdout because a plan-quota failure arrives here with an empty stdout.
func (s *codexStderr) line(l string) {
	if s.diag == "" && s.diagnostic(l) {
		s.diag = strings.TrimSpace(l)
	}

	key, val, ok := strings.Cut(l, ":")
	key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
	if !ok || val == "" || s.seen[key] || !slices.Contains(codexHeaderKeys, key) {
		return
	}
	s.seen[key] = true
	if key == "model" {
		s.model = val
	}
	s.emit(Event{Kind: EventActivity, Text: key + ": " + val})
}

// diagnostic reports whether a line is a codex CLI error rather than progress chatter. Gating on the
// prefix is what keeps a reasoning stream discussing a rate limit from being read as one.
func (s *codexStderr) diagnostic(l string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "error:")
}
