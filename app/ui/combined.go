package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// combinedLimit bounds the compact log the same way scrollbackLimit bounds a pane.
const combinedLimit = 2000

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
		head := m.style.muted.Render(e.at.Format(timeFormat)) + " " + m.prefix(e.agent)
		if e.agent == "" {
			// a stage change is the coarsest thing that happens in a run and the one line worth finding
			// while scrolling, so it is banded rather than left to read as another agent line
			out = append(out, head+m.style.stage.Render(" "+e.text+" "))
			continue
		}
		out = append(out, Wrap(head, markdown(e.text), m.view.width())...)
	}
	return out
}

// prefix is the agent column: the name padded to the widest in the roster and colored, with the
// column itself doing the separating. A stage line names no agent and is indented to the same column.
//
// Padding happens before painting and the width is measured on the plain name, because a color
// sequence has no display width — pad the painted string and every line indents by however many bytes
// that agent's color happens to take, which is what left the log ragged.
func (m Model) prefix(agent string) string {
	if agent == "" {
		return strings.Repeat(" ", m.nameWidth()+2)
	}
	pad := strings.Repeat(" ", max(0, m.nameWidth()-lipgloss.Width(agent)))
	return m.paint(agent) + pad + "  "
}

// paint colors an agent's name from its own resolved spec, which is the same value the plain renderer
// reads, so one agent is one color in both.
func (m Model) paint(agent string) string {
	if a := m.find(agent); a != nil {
		return a.spec.Paint(agent)
	}
	return agent
}
