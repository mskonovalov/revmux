package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View assembles the frame: the status table on top, the tab bar under it, one focused detail pane
// filling the rest.
func (m Model) View() string {
	return m.statusTable() + "\n" + m.tabBar() + "\n" + strings.Join(m.detailPane(), "\n")
}

// detailPane is the focused tab's window into its own log, scrolled by the reader. It is always
// exactly the pane's height, padded out, so the frame keeps its shape as the log grows.
func (m Model) detailPane() []string {
	lines, height := m.paneLines(), m.paneHeight()
	if len(lines) > height {
		end := min(max(len(lines)-m.view.scroll, height), len(lines))
		lines = lines[end-height : end]
	}

	out := m.clipAll(lines)
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// paneLines is what the focused tab shows: the compact combined log on tab 0, the findings browser
// on the tab the report opens, one agent's full scrollback on the rest.
func (m Model) paneLines() []string {
	switch m.view.tab {
	case 0:
		return m.combinedLines()
	case m.findingsTab():
		return m.findingsPane()
	}
	return m.agentLines()
}

// paneHeight is what the status table and the tab bar leave for the pane.
func (m Model) paneHeight() int { return max(1, m.view.height()-len(m.agents)-3) }

// maxScroll is how far back the focused pane can be scrolled, zero when it all fits.
func (m Model) maxScroll() int { return max(0, len(m.paneLines())-m.paneHeight()) }

// tabBar names every pane, the focused one in reverse video. The browser is keyed f rather than by
// its number: it appears only once the report has arrived, so a fixed digit would name nothing for
// most of the run.
func (m Model) tabBar() string {
	labels := make([]string, 0, len(m.agents)+2)
	labels = append(labels, " 0 · all ")
	for i, a := range m.agents {
		labels = append(labels, " "+strconv.Itoa(i+1)+" · "+a.spec.Name+" ")
	}
	if m.findings != nil {
		labels = append(labels, " f · findings ")
	}
	if m.view.tab < len(labels) {
		labels[m.view.tab] = m.highlight(labels[m.view.tab])
	}
	return m.clip(strings.Join(labels, "|"))
}

// highlight marks the focused tab with raw reverse video. lipgloss is not used for an inline element
// inside an assembled frame: its render ends in a full reset that would clear whatever encloses it.
func (m Model) highlight(s string) string { return "\x1b[7m" + s + "\x1b[27m" }

// clip cuts a line to the pane width. lipgloss does the measuring because a line carries color
// sequences and slicing runes would cut one in half.
func (m Model) clip(s string) string {
	return lipgloss.NewStyle().MaxWidth(m.view.width()).Render(s)
}

func (m Model) clipAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, m.clip(l))
	}
	return out
}
