package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// statusTable is one row per agent: name, state, elapsed, last activity, under a line naming the
// stage the run is in.
func (m Model) statusTable() string {
	rows := make([]string, 0, len(m.agents)+2)
	rows = append(rows, m.clip(m.header()), m.clip(strings.Repeat("─", m.view.width())))

	width := 0
	for _, a := range m.agents {
		width = max(width, lipgloss.Width(a.spec.Name))
	}
	for _, a := range m.agents {
		// the name is padded before it is painted: the color sequence has no width and would
		// otherwise be counted as if it did
		name := a.spec.Paint(a.spec.Name + strings.Repeat(" ", width-lipgloss.Width(a.spec.Name)))
		rows = append(rows, m.clip(name+fmt.Sprintf("  %-9s  %7s  %s", a.state, a.runtime(), a.last)))
	}
	return strings.Join(rows, "\n")
}

func (m Model) header() string {
	head := "revmux · " + strconv.Itoa(len(m.agents)) + " agents"
	if m.stage != "" {
		head += " · stage " + m.stage
	}
	if n := m.findings(); n > 0 {
		head += " · " + strconv.Itoa(n) + " findings"
	}
	return head
}

func (m Model) findings() int {
	total := 0
	for _, a := range m.agents {
		total += a.findings
	}
	return total
}
