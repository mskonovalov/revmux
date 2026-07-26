package executor

import (
	"encoding/json"
	"strings"
)

// activityTextLimit keeps one activity line short enough for a TUI row.
const activityTextLimit = 120

// streamEvent is one line of claude's stream-json output. Only the fields revmux acts on are decoded.
type streamEvent struct {
	Type             string                `json:"type"`
	Subtype          string                `json:"subtype"`
	StructuredOutput json.RawMessage       `json:"structured_output"`
	ModelUsage       map[string]modelUsage `json:"modelUsage"`
	Usage            *tokenUsage           `json:"usage"`
	TTFTMs           int                   `json:"ttft_ms"`
	IsError          bool                  `json:"is_error"`
	RateLimitInfo    *RateLimitInfo        `json:"rate_limit_info"`
	Message          *streamMessage        `json:"message"`
}

type modelUsage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

type tokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamMessage struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// tokens is the run's whole footprint, cache included. The CLI reports it, so nothing is estimated.
func (e streamEvent) tokens() int {
	if e.Usage == nil {
		return 0
	}
	return e.Usage.InputTokens + e.Usage.OutputTokens +
		e.Usage.CacheReadInputTokens + e.Usage.CacheCreationInputTokens
}

// actualModel reports what ran rather than what was asked for: --model can be silently ignored. The
// busiest entry wins, with the name breaking ties so the answer does not depend on map order.
func (e streamEvent) actualModel() string {
	name, out := "", -1
	for m, u := range e.ModelUsage {
		if u.OutputTokens > out || (u.OutputTokens == out && m < name) {
			name, out = m, u.OutputTokens
		}
	}
	return name
}

// activity summarizes one assistant turn for the progress renderers.
func (e streamEvent) activity() string {
	if e.Message == nil {
		return ""
	}
	for _, b := range e.Message.Content {
		switch b.Type {
		case "tool_use":
			if b.Name != "" {
				return "tool: " + b.Name
			}
		case "thinking":
			return "thinking"
		case "text":
			text, _, _ := strings.Cut(strings.TrimSpace(b.Text), "\n")
			if text == "" {
				continue
			}
			if len(text) > activityTextLimit {
				return text[:activityTextLimit] + "..."
			}
			return text
		}
	}
	return ""
}
