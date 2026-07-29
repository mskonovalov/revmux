package ui

import (
	"strings"
	"unicode"
)

func (m Model) inputLines() []string {
	return m.inputLinesAt(m.view.tab)
}

func (m Model) inputLinesAt(tab int) []string {
	if tab < 0 || tab >= len(m.cfg.Inputs) {
		return []string{"no such input"}
	}
	doc := m.cfg.Inputs[tab]
	out := []string{m.style.muted.Render(doc.Path), ""}

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
		out = append(out, Wrap("", m.style.warn.Render(doc.Notice), m.view.width())...)
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
		out = append(out, Wrap("", line, m.view.width())...)
	}
	return out
}

func (m Model) markdownInput(text string) []string {
	out := []string{}
	var fence byte
	fenceLen := 0
	for line := range strings.SplitSeq(text, "\n") {
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
			out = append(out, Wrap("    ", line, m.view.width())...)
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
