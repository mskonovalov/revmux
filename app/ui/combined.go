package ui

import "time"

// combinedLimit bounds the compact log the same way scrollbackLimit bounds a pane.
const combinedLimit = 2000

// thinkingActivity is how an executor summarizes a thinking block. The combined view drops it on
// purpose: four concurrent agents thinking scroll it faster than anyone can read, and it stops being
// the situational-awareness view it exists to be. The per-agent panes keep it.
const thinkingActivity = "thinking"

// combinedEntry is one compact line. It takes a struct rather than adjacent strings because agent and
// text are both in scope where it is pushed, which is where a transposition would go unnoticed.
type combinedEntry struct {
	agent string
	text  string
	at    time.Time
}

// combinedState is the interleaved compact log behind tab 0.
type combinedState struct {
	entries []combinedEntry
}

func (c *combinedState) push(e combinedEntry) {
	c.entries = append(c.entries, e)
	if len(c.entries) > combinedLimit {
		c.entries = c.entries[len(c.entries)-combinedLimit:]
	}
}

// combinedLines renders the compact log in arrival order, each line prefixed with its agent in the
// agent's own color. An entry from an agent the roster never named keeps the default foreground.
func (m Model) combinedLines() []string {
	if len(m.combined.entries) == 0 {
		return []string{"waiting for the first agent..."}
	}
	out := make([]string, 0, len(m.combined.entries))
	for _, e := range m.combined.entries {
		line := e.at.Format(timeFormat) + " "
		if e.agent != "" {
			line += m.paint(e.agent) + ": "
		}
		out = append(out, line+e.text)
	}
	return out
}

// paint colors an agent's name from its own resolved spec, which is the same value the plain renderer
// reads, so one agent is one color in both.
func (m Model) paint(agent string) string {
	if a := m.find(agent); a != nil {
		return a.spec.Paint(agent)
	}
	return agent
}
