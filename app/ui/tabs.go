package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// the two tab stops are not interchangeable. A terminal draws a tab as eight columns, so a verbatim row
// takes termTabStop; CommonMark computes block indentation on four, so a document expanded at eight
// reparses with a different block structure — a tab-indented nested list becomes an indented code block.
const (
	termTabStop = 8
	mdTabStop   = 4
)

// expandTabs replaces one line's tabs with the spaces its caller's tab stop draws for them. It takes a
// single line and starts at column zero, since the running column is never reset on a newline. It
// measures whole segments between tabs rather than per rune, which would count a grapheme cluster once
// per component and put the following stop in the wrong place.
func expandTabs(line string, stop int) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var out strings.Builder
	out.Grow(len(line) + stop)
	column := 0
	for i, seg := range strings.Split(line, "\t") {
		if i > 0 {
			spaces := stop - column%stop
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		}
		out.WriteString(seg)
		column += ansi.StringWidth(seg)
	}
	return out.String()
}
