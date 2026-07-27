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
		out = append(out, m.wrap(head, e.text)...)
	}
	return out
}

// wrap lays one entry across as many rows as its text needs, continuing under the text column rather
// than under the timestamp.
//
// **Clipping loses the end of exactly the lines worth reading.** A narrated step or a Bash command is
// the informative part of the log and the part most likely to run long, and cutting it at the edge
// leaves a reader with the half that says least. Continuation rows carry no timestamp and no agent
// name, so the entry still reads as one thing and the columns stay scannable.
func (m Model) wrap(head, text string) []string {
	avail := m.view.width() - lipgloss.Width(head)
	if avail < minWrapCols || lipgloss.Width(text) <= avail {
		return []string{head + text}
	}

	indent := strings.Repeat(" ", lipgloss.Width(head))
	out := []string{}
	for rest := text; rest != ""; {
		take := m.take(rest, avail)
		prefix := head
		if len(out) > 0 {
			prefix = indent
		}
		out = append(out, prefix+take)
		rest = strings.TrimPrefix(rest, take)
		rest = strings.TrimLeft(rest, " ")
	}
	return out
}

// take is the longest leading run of text that fits in cols, broken at a space when one is close
// enough to the edge to be worth breaking at. lipgloss does the measuring for the same reason it does
// everywhere else here: the text may carry color, and counting bytes would count the escapes.
func (m Model) take(text string, cols int) string {
	if lipgloss.Width(text) <= cols {
		return text
	}
	cut := text
	for lipgloss.Width(cut) > cols {
		cut = cut[:len(cut)-1]
	}
	// break on a word where one is reachable; a break in the middle of a path or a command is worse
	// than a short row
	if sp := strings.LastIndex(cut, " "); sp > 0 && lipgloss.Width(cut[:sp]) >= cols/2 {
		return cut[:sp]
	}
	return cut
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
