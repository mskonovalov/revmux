package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View assembles the frame: the status table on top, the tab bar under it, one focused detail pane
// filling the rest.
func (m Model) View() string {
	return m.statusTable() + "\n" + m.tabBar() + "\n" + m.rule() + "\n" + strings.Join(m.detailPane(), "\n")
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

// paneHeight is what the chrome leaves for the pane: one row per agent plus five fixed lines — the
// header, the column heading, the rule under the table, the tab bar and the rule under that.
func (m Model) paneHeight() int { return max(1, m.view.height()-len(m.agents)-chromeLines) }

// maxScroll is how far back the focused pane can be scrolled, zero when it all fits.
func (m Model) maxScroll() int { return max(0, len(m.paneLines())-m.paneHeight()) }

// tabBar names every pane, the focused one accented and marked. The browser is keyed f rather than by
// its number: it appears only once the report has arrived, so a fixed digit would name nothing for
// most of the run.
func (m Model) tabBar() string {
	// numbered from one, because that is what the keys are: nobody reaches for 0 first, and the
	// combined view being the leftmost tab makes it tab one
	names := make([]string, 0, len(m.agents)+2)
	names = append(names, m.tabToken(0)+" all")
	for i, a := range m.agents {
		names = append(names, m.tabToken(i+1)+" "+a.spec.Name)
	}
	if m.findings != nil {
		names = append(names, "f findings")
	}

	// clipping alone is not enough: it cuts the right-hand tabs off mid-word, so a reader cannot tell
	// how many panes exist or what is hiding past the edge. There is no horizontal scroll on this
	// line, so when the full bar does not fit the unfocused tabs drop to their token and the focused
	// one keeps its name — which is the only one whose content is on screen anyway.
	if bar := m.tabRow(names, false); lipgloss.Width(bar) <= m.view.width() {
		return bar
	}
	return m.clip(m.tabRow(names, true))
}

// tabRow renders the bar, either in full or with every unfocused tab reduced to its leading token —
// a digit for the first nine panes and a letter after that.
// The short form also drops the padding around the separator: the padding is most of the cost once
// the names are gone, and keeping it would clip the rightmost tabs anyway, which is the whole thing
// being avoided.
func (m Model) tabRow(names []string, short bool) string {
	// the marker and the blank are the same width, so the names line up whichever tab is focused
	mark, pad, sep := "▸ ", "  ", "  │"
	if short {
		mark, pad, sep = "▸", "", "│"
	}
	labels := make([]string, 0, len(names))
	for i, n := range names {
		if i == m.view.tab {
			labels = append(labels, m.style.tabOn.Render(mark+n))
			continue
		}
		if short {
			n, _, _ = strings.Cut(n, " ")
		}
		labels = append(labels, m.style.tabOff.Render(pad+n))
	}
	return strings.Join(labels, m.style.muted.Render(sep))
}

// tabLetters carry on where the digits stop. **Every letter already bound to something is missing
// from this list on purpose** — f opens the findings browser, h and l switch panes, j and k scroll, g
// goes to the top and q quits. Those keys are matched before the token lookup is ever reached, so a
// tab assigned one of them would simply be unreachable, and only on a run with enough panes to get
// that far.
const tabLetters = "abcdeimnoprstuvwxyz"

// tabToken is the single character that selects a pane: 1-9, then the letters above.
//
// One character, always. A two-digit token costs a column in every tab on the bar and reads as two
// numbers beside a name that may itself end in one. Panes past the tokens have none — they are still
// reachable with tab and the arrows, and a run with that many panes has bigger problems.
func (m Model) tabToken(idx int) string {
	switch {
	case idx < 9:
		return strconv.Itoa(idx + 1)
	case idx-9 < len(tabLetters):
		return string(tabLetters[idx-9])
	}
	return " "
}

// tabIndex is tabToken read back: the pane a keystroke selects, or -1 for a key that names none.
func (m Model) tabIndex(key string) int {
	if len(key) != 1 {
		return -1
	}
	if c := key[0]; c >= '1' && c <= '9' {
		return int(c - '1')
	}
	if i := strings.IndexByte(tabLetters, key[0]); i >= 0 {
		return i + 9
	}
	return -1
}

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
