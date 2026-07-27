package ui

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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

// findingsState is the report as the browser holds it: the run's findings ordered by severity, and
// which of them the filter admits.
//
// **It renders the report and nothing more.** Folding, a cursor and per-row expansion were all tried
// and removed: a reader wants to read the review, and every one of those put part of it behind a
// keystroke while adding state to keep in step with the pane. Scrolling is the pane's own, the filter
// narrows what is rendered, and that is the whole surface.
type findingsState struct {
	rows    []finding.Finding
	matches []int
	query   string
	typing  bool
}

// severityRank orders the browser's groups; anything unrecognized sorts last rather than being
// dropped, so a typo in a model's answer stays visible.
var severityRank = map[finding.Severity]int{finding.Critical: 0, finding.Major: 1, finding.Minor: 2}

// newFindings builds the browser over a finished report, worst findings first. Open questions,
// pre-existing and immaterial findings are deliberately not listed: the report on stdout carries
// them, and mixing them in would put unranked material above a critical bug.
func newFindings(rep finding.Report) *findingsState {
	f := &findingsState{rows: slices.Clone(rep.Findings)}
	slices.SortStableFunc(f.rows, func(a, b finding.Finding) int { return f.rank(a.Severity) - f.rank(b.Severity) })
	f.filter("")
	return f
}

// filter narrows the list to findings whose title, file or body carry q, case-insensitively. The
// cursor returns to the top, since the row it sat on may no longer be listed.
func (f *findingsState) filter(q string) {
	f.query = q
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
	return m.findings.render(m.view.width())
}

// render lays the report out the way the markdown on stdout does: a severity heading, then each
// finding as its title, where it is, its body, its fix and its attribution.
func (f *findingsState) render(width int) []string {
	lines := []string{}
	if f.typing || f.query != "" {
		lines = append(lines, f.prompt())
	}

	vis := f.visible()
	if len(vis) == 0 {
		return append(lines, f.empty())
	}
	for i, v := range vis {
		if i == 0 || v.Severity != vis[i-1].Severity {
			lines = append(lines, f.heading(v.Severity))
		}
		lines = append(lines, f.row(v))
		lines = append(lines, f.detail(v, width)...)
	}
	return lines
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

// row is one finding's headline: the report's own "### title" line, carrying the confidence the report
// prints at the foot of the entry.
func (f *findingsState) row(v finding.Finding) string {
	return "  " + markdown(v.Title) + "  [" + strconv.Itoa(v.Confidence) + "]"
}

// detail is the rest of the report's entry, in the report's own order: where it is, the body, the fix,
// then the attribution. It is what the reader came for, so it is shown by default and folded away on
// request rather than the other way round.
func (f *findingsState) detail(v finding.Finding, width int) []string {
	out := f.indent(v.Location(), width)
	if v.Body != "" {
		out = append(out, "")
		out = append(out, f.indent(v.Body, width)...)
	}
	if v.Fix != "" {
		out = append(out, "")
		out = append(out, f.indent("fix: "+v.Fix, width)...)
	}
	if meta := f.meta(v); meta != "" {
		out = append(out, "")
		out = append(out, f.indent(meta, width)...)
	}
	return append(out, "")
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

// indent lays a paragraph under its heading, wrapped to the pane rather than clipped at its edge. A
// finding's body is prose several sentences long — the part a reader is actually here to read — and
// cutting it at the terminal's width leaves the half that says least.
func (f *findingsState) indent(text string, width int) []string {
	const pad = "    "
	avail := max(width-len(pad), minWrapCols)
	out := []string{}
	for para := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		for rest := para; rest != ""; {
			take := f.take(rest, avail)
			out = append(out, pad+markdown(take))
			rest = strings.TrimLeft(strings.TrimPrefix(rest, take), " ")
		}
	}
	return out
}

// take is the longest leading run of text that fits in cols, broken at a word when one is reachable.
// Runes rather than bytes, since a body carries arbitrary prose.
func (f *findingsState) take(text string, cols int) string {
	if utf8.RuneCountInString(text) <= cols {
		return text
	}
	cut := string([]rune(text)[:cols])
	if sp := strings.LastIndex(cut, " "); sp > 0 && utf8.RuneCountInString(cut[:sp]) >= cols/2 {
		return cut[:sp]
	}
	return cut
}

func (f *findingsState) rank(s finding.Severity) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return len(severityRank)
}
