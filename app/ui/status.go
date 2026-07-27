package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusTable is one row per agent — name, state, elapsed, last activity — under the run header and
// a column heading, all of it inside a full-width rule.
func (m Model) statusTable() string {
	rows := make([]string, 0, len(m.agents)+4)
	rows = append(rows, m.clip(m.header()), m.rule(), m.clip(m.columns()))
	for _, a := range m.agents {
		rows = append(rows, m.clip(m.agentRow(a)))
	}
	rows = append(rows, m.rule())
	return strings.Join(rows, "\n")
}

// nameWidth is the widest agent name, so the columns line up whatever the roster is called.
func (m Model) nameWidth() int {
	width := len("AGENT")
	for _, a := range m.agents {
		width = max(width, lipgloss.Width(a.spec.Name))
	}
	return width
}

// columns labels the table. Muted, because it is chrome a reader stops seeing after the first glance.
func (m Model) columns() string {
	return m.style.muted.Render(fmt.Sprintf("%-*s  %-9s  %7s  %s", m.nameWidth(), "AGENT", "STATE", "TIME", "ACTIVITY"))
}

// agentRow is one agent's line. Each cell is painted separately and padded before it is painted: a
// color sequence has no display width, so padding a painted string counts the escape bytes as if they
// did and the columns drift apart by exactly the length of the sequence.
func (m Model) agentRow(a *agentState) string {
	name := a.spec.Paint(a.spec.Name + strings.Repeat(" ", m.nameWidth()-lipgloss.Width(a.spec.Name)))
	state := m.stateStyle(a.state).Render(fmt.Sprintf("%-9s", a.state))
	elapsed := m.style.muted.Render(fmt.Sprintf("%7s", a.runtime(m.now)))
	return name + "  " + state + "  " + elapsed + "  " + a.last
}

// header names the run and what it is doing. The findings count is the one number worth finding at a
// glance, so it carries the only accent on the line.
func (m Model) header() string {
	head := m.style.title.Render("revmux")
	head += m.style.muted.Render(" · " + strconv.Itoa(len(m.agents)) + " agents")
	if m.stage != "" {
		head += m.style.muted.Render(" · ") + m.stage
	}
	if m.found > 0 {
		head += m.style.muted.Render(" · ") + m.style.count.Render(strconv.Itoa(m.found)+" findings")
	}
	return head + m.closing()
}

// closing is what the header says once the run is over: that it is over, and how to leave. A finished
// review otherwise looks identical to a stalled one — every row reads "done" and nothing moves — and a
// reader who does not already know the key has no way to find it.
func (m Model) closing() string {
	if !m.done {
		return ""
	}
	if m.exitIn > 0 {
		secs := strconv.Itoa(int(m.exitIn.Round(time.Second).Seconds()))
		return m.style.muted.Render(" · ") +
			m.style.warn.Render("complete, closing in "+secs+"s") +
			m.style.muted.Render(" · any key to stay")
	}
	return m.style.muted.Render(" · ") + m.style.ok.Render("complete") + m.style.muted.Render(" · q to quit")
}
