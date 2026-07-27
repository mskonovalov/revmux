package ui

import "regexp"

// Inline markdown a model writes into a finding's body. Both are common in review prose: paths,
// identifiers and flags arrive in backticks, and the emphasis a model puts on the one sentence that
// matters arrives in asterisks. Left raw, the reader sees the punctuation instead of the effect.
var (
	mdCode = regexp.MustCompile("`([^`\n]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
)

// ANSI rather than lipgloss, deliberately. These are inline spans inside a line that clip() later
// renders through lipgloss, and a nested lipgloss render ends in a full reset that would clear the
// enclosing style — the trap .claude/rules/tui.md records. Each sequence therefore closes with the
// narrow "back to default" code rather than a reset, which is what AgentSpec.Paint does too.
const (
	ansiBoldOn  = "\x1b[1m"
	ansiBoldOff = "\x1b[22m"
	ansiCodeOn  = "\x1b[36m"
	ansiCodeOff = "\x1b[39m"
)

// markdown renders the inline markdown in one line of model prose. Block constructs are deliberately
// not handled: a finding's body is a paragraph or two, and the browser already supplies the structure
// around it.
func markdown(s string) string {
	s = mdBold.ReplaceAllString(s, ansiBoldOn+"$1"+ansiBoldOff)
	return mdCode.ReplaceAllString(s, ansiCodeOn+"$1"+ansiCodeOff)
}
