package ui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) inputLines() []string {
	return m.inputLinesAt(m.view.tab)
}

func (m Model) inputLinesAt(tab int) []string {
	if tab < 0 || tab >= len(m.cfg.Inputs) {
		return []string{"no such input"}
	}
	doc := m.cfg.Inputs[tab]
	out := []string{m.style.muted.Render(m.visibleMetadata(doc.Path)), ""}

	switch {
	case doc.Content == "" && doc.Notice == "":
		out = append(out, m.style.muted.Render("(empty file)"))
	case doc.Markdown:
		out = append(out, m.markdownInput(doc.Content)...)
	default:
		out = append(out, m.verbatimInput(doc.Content)...)
	}
	if doc.Notice != "" {
		if doc.Content != "" {
			out = append(out, "")
		}
		out = append(out, Wrap("", m.style.warn.Render(m.visibleMetadata(doc.Notice)), m.view.width())...)
	}
	return out
}

func (m Model) verbatimInput(text string) []string {
	out := []string{}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, m.verbatimLine("", line)...)
	}
	return out
}

func (m Model) markdownInput(text string) []string {
	out := []string{}
	var fence byte
	fenceLen := 0
	for line := range strings.SplitSeq(text, "\n") {
		line = m.expandTabs(line, 0)
		trimmed := strings.TrimSpace(line)
		marker, markerLen := m.markdownFence(trimmed)
		if fence == 0 && markerLen >= 3 {
			fence, fenceLen = marker, markerLen
			continue
		}
		if fence != 0 {
			if marker == fence && markerLen >= fenceLen && strings.TrimSpace(trimmed[markerLen:]) == "" {
				fence, fenceLen = 0, 0
				continue
			}
			out = append(out, m.verbatimLine("    ", line)...)
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if level, title := m.markdownHeading(trimmed); level > 0 {
			out = append(out, Wrap("", heading(level, title), m.view.width())...)
			continue
		}
		if head, body := m.markdownList(line); head != "" {
			out = append(out, Wrap(head, markdown(body), m.view.width())...)
			continue
		}
		if strings.HasPrefix(trimmed, "> ") {
			indent := line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
			out = append(out, Wrap(indent+"> ", markdown(strings.TrimPrefix(trimmed, "> ")), m.view.width())...)
			continue
		}
		out = append(out, Wrap("", markdown(line), m.view.width())...)
	}
	return out
}

// verbatimLine hard-wraps input and fenced code without the word wrapper's whitespace folding.
// Tabs are expanded first because terminal tab stops consume columns that lipgloss and ansi do not
// count, which otherwise lets a valid input row escape the frame.
func (m Model) verbatimLine(head, line string) []string {
	headWidth := lipgloss.Width(head)
	line = m.expandTabs(line, headWidth)
	width := max(1, m.view.width()-headWidth)
	wrapped := strings.Split(ansi.Hardwrap(line, width, true), "\n")
	out := make([]string, 0, len(wrapped))
	for _, part := range wrapped {
		out = append(out, head+part)
	}
	return out
}

func (m Model) expandTabs(line string, column int) string {
	const tabStop = 8
	var out strings.Builder
	for _, r := range line {
		if r == '\t' {
			spaces := tabStop - column%tabStop
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		out.WriteRune(r)
		column += lipgloss.Width(string(r))
	}
	return out.String()
}

// visibleMetadata replaces every control character before caller-provided identity or filesystem
// metadata reaches the terminal. Content is validated separately by the snapshotter.
func (Model) visibleMetadata(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, text)
}

func (Model) markdownFence(line string) (byte, int) {
	if line == "" || line[0] != '`' && line[0] != '~' {
		return 0, 0
	}
	n := 1
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
}

func (m Model) markdownHeading(line string) (int, string) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(line[level+1:])
}

func (m Model) markdownList(line string) (string, string) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	indent := line[:len(line)-len(trimmed)]
	for _, marker := range []string{"- ", "* ", "+ "} {
		if body, ok := strings.CutPrefix(trimmed, marker); ok {
			return indent + marker, body
		}
	}

	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits > 0 && strings.HasPrefix(trimmed[digits:], ". ") {
		marker := trimmed[:digits+2]
		return indent + marker, strings.TrimPrefix(trimmed, marker)
	}
	return "", ""
}
