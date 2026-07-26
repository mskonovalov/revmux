package ui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/umputun/revmux/app/finding"
)

// CompletedMsg carries the finished report into the model.
//
// It arrives as a bubbletea message rather than as a pipeline event because the event channel drops
// under load, and a dropped completion would park the reader on the agent panes forever. The model
// holds the report only to render it: package main still owns it and remains the only writer to
// stdout.
type CompletedMsg struct {
	Report finding.Report
}

// findingsState is the findings browser: the run's findings ordered by severity, which of them the
// filter admits, where the cursor sits and which rows are expanded.
type findingsState struct {
	rows     []finding.Finding
	matches  []int
	expanded map[int]bool
	cursor   int
	query    string
	typing   bool
}

// severityRank orders the browser's groups; anything unrecognized sorts last rather than being
// dropped, so a typo in a model's answer stays visible.
var severityRank = map[finding.Severity]int{finding.Critical: 0, finding.Major: 1, finding.Minor: 2}

// newFindings builds the browser over a finished report, worst findings first. Open questions,
// pre-existing and immaterial findings are deliberately not listed: the report on stdout carries
// them, and mixing them in would put unranked material above a critical bug.
func newFindings(rep finding.Report) *findingsState {
	f := &findingsState{rows: slices.Clone(rep.Findings), expanded: map[int]bool{}}
	slices.SortStableFunc(f.rows, func(a, b finding.Finding) int { return f.rank(a.Severity) - f.rank(b.Severity) })
	f.filter("")
	return f
}

// move walks the cursor through the filtered list, clamped at both ends.
func (f *findingsState) move(delta int) {
	f.cursor = min(max(f.cursor+delta, 0), max(len(f.matches)-1, 0))
}

// toggle expands or collapses the finding under the cursor. Expansion is keyed on the finding rather
// than on its position, so narrowing the filter cannot leave a different row expanded.
func (f *findingsState) toggle() {
	if f.cursor >= len(f.matches) {
		return
	}
	row := f.matches[f.cursor]
	f.expanded[row] = !f.expanded[row]
}

// filter narrows the list to findings whose title, file or body carry q, case-insensitively. The
// cursor returns to the top, since the row it sat on may no longer be listed.
func (f *findingsState) filter(q string) {
	f.query, f.cursor = q, 0
	needle := strings.ToLower(q)
	f.matches = make([]int, 0, len(f.rows))
	for i, r := range f.rows {
		if needle == "" || strings.Contains(strings.ToLower(r.Title+" "+r.File+" "+r.Body), needle) {
			f.matches = append(f.matches, i)
		}
	}
}

// visible is what the browser lists under the active filter.
func (f *findingsState) visible() []finding.Finding {
	out := make([]finding.Finding, 0, len(f.matches))
	for _, i := range f.matches {
		out = append(out, f.rows[i])
	}
	return out
}

// findingsPane is the findings browser, which holds nothing until the report arrives.
func (m Model) findingsPane() []string {
	if m.findings == nil {
		return []string{"the review is still running..."}
	}
	lines, _ := m.findings.render()
	return lines
}

// cursorLine is where the cursor's row landed in the rendered pane, which is what keeps it in view
// once headings and expanded rows have pushed it down.
func (f *findingsState) cursorLine() int {
	_, rows := f.render()
	if f.cursor < len(rows) {
		return rows[f.cursor]
	}
	return 0
}

// render walks the browser once and returns both the pane's lines and the line each listed finding
// landed on. Both come from one walk because the cursor has to be kept in view, and locating it
// separately would be this loop written twice.
func (f *findingsState) render() (lines []string, rows []int) {
	lines, rows = []string{}, make([]int, 0, len(f.matches))
	if f.typing || f.query != "" {
		lines = append(lines, f.prompt())
	}

	vis := f.visible()
	if len(vis) == 0 {
		return append(lines, f.empty()), rows
	}
	for i, v := range vis {
		if i == 0 || v.Severity != vis[i-1].Severity {
			lines = append(lines, f.heading(v.Severity))
		}
		rows = append(rows, len(lines))
		lines = append(lines, f.row(i, v))
		if f.expanded[f.matches[i]] {
			lines = append(lines, f.detail(v)...)
		}
	}
	return lines, rows
}

// prompt is the filter line, showing the caret while the query is being typed and what it kept once
// it is not.
func (f *findingsState) prompt() string {
	if f.typing {
		return "filter: " + f.query + "_"
	}
	return "filter: " + f.query + " (" + strconv.Itoa(len(f.matches)) + " of " + strconv.Itoa(len(f.rows)) + ")"
}

func (f *findingsState) empty() string {
	if len(f.rows) == 0 {
		return "no findings."
	}
	return "nothing matches " + strconv.Quote(f.query) + "."
}

func (f *findingsState) heading(s finding.Severity) string {
	if s == "" {
		return "── UNSPECIFIED ──"
	}
	return "── " + strings.ToUpper(string(s)) + " ──"
}

// row is one finding on one line: the cursor mark, where it is, what it says and how sure the panel
// was.
func (f *findingsState) row(i int, v finding.Finding) string {
	mark := "  "
	if i == f.cursor {
		mark = "> "
	}
	return mark + v.Location() + "  " + v.Title + "  [" + strconv.Itoa(v.Confidence) + "]"
}

// detail is what expanding a row adds: the body, the fix, and the attribution the single line has no
// room for.
func (f *findingsState) detail(v finding.Finding) []string {
	out := []string{}
	if v.Body != "" {
		out = append(out, f.indent(v.Body)...)
	}
	if v.Fix != "" {
		out = append(out, f.indent("fix: "+v.Fix)...)
	}
	if meta := f.meta(v); meta != "" {
		out = append(out, f.indent(meta)...)
	}
	return out
}

func (f *findingsState) meta(v finding.Finding) string {
	parts := []string{}
	if len(v.Sources) > 0 {
		parts = append(parts, "sources: "+strings.Join(v.Sources, ", "))
	}
	if len(v.Lenses) > 0 {
		parts = append(parts, "lenses: "+strings.Join(v.Lenses, ", "))
	}
	if v.Verdict != "" {
		parts = append(parts, "verdict: "+string(v.Verdict))
	}
	return strings.Join(parts, " | ")
}

func (f *findingsState) indent(text string) []string {
	out := []string{}
	for l := range strings.SplitSeq(text, "\n") {
		out = append(out, "    "+l)
	}
	return out
}

func (f *findingsState) rank(s finding.Severity) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return len(severityRank)
}
