package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// minWrapCols is the narrowest text column worth wrapping into: below it a wrapped entry is more rows
// of indent than of text, and clipping reads better.
const minWrapCols = 20

// Wrap lays one entry across as many rows as its text needs, continuing under the text column rather
// than under whatever head names it. The first row carries head; the rest are indented to head's width
// so the entry reads as one thing and the columns stay scannable.
//
// **There is one of these, and there were three.** The combined log, an agent's scrollback, the
// findings browser and the plain `--no-tui` renderer all wrap the same way, and three separate copies
// of this loop had already drifted — two measured display width and one counted runes, so the same
// text broke in different places depending on which pane it landed in. Two implementations of one rule
// diverging is this project's most reliable defect; the fix is for there to be one. It is exported for
// `app/progress.go`, which is the fourth caller and lives in `package main`.
//
// Clipping instead loses the end of exactly the lines worth reading: a narrated step, a command, a
// finding's body. A pane is where a reader went looking for detail, so it is the last place to throw
// the end of it away.
func Wrap(head, text string, width int) []string {
	avail := width - lipgloss.Width(head)
	if avail < minWrapCols || lipgloss.Width(text) <= avail {
		return []string{head + text}
	}

	indent := strings.Repeat(" ", lipgloss.Width(head))
	out := []string{}
	for rest := text; rest != ""; {
		take := takeCols(rest, avail)
		prefix := head
		if len(out) > 0 {
			prefix = indent
		}
		out = append(out, prefix+take)
		rest = strings.TrimLeft(strings.TrimPrefix(rest, take), " ")
	}
	return out
}

// takeCols is the longest leading run of text that fits in cols display columns, broken at a word when
// one is reachable.
//
// **It walks runes, never bytes.** Trimming a byte at a time while measuring display cells exits the
// loop with the cut sitting inside a multi-byte rune, which puts invalid UTF-8 into the pane and into
// events.jsonl — and the text reaching here now carries ANSI as well, since markdown rendering runs
// first, so a byte cut can also land inside an escape sequence and spill it as literal characters.
func takeCols(text string, cols int) string {
	if lipgloss.Width(text) <= cols {
		return text
	}

	runes := []rune(text)
	cut := ""
	for i := 1; i <= len(runes); i++ {
		next := string(runes[:i])
		if lipgloss.Width(next) > cols {
			break
		}
		cut = next
	}
	if cut == "" { // a single rune wider than the column, so take it and overflow by one cell
		cut = string(runes[0])
	}

	// break on a word where one is reachable; a break in the middle of a path or a command is worse
	// than a short row
	if sp := strings.LastIndex(cut, " "); sp > 0 && lipgloss.Width(cut[:sp]) >= cols/2 {
		return cut[:sp]
	}
	return cut
}
