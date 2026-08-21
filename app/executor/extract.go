package executor

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// extractJSON pulls the first complete JSON object out of output that may carry prose around it.
// Decoding starts at each brace in turn, and an incomplete tail ends the search rather than continuing
// into it: a nested object inside a truncated answer would otherwise come back looking like the whole
// answer. Codex and cursor-agent both need this, because neither has --json-schema.
func extractJSON(raw string) (json.RawMessage, error) {
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
	return nil, errors.New("no JSON object in output")
}
