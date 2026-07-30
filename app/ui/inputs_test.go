package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModel_inputLines_markdown(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "# Review **scope**\n\n- inspect `app/ui`\n> keep the status visible\n\n```go\nfunc main() {}\n```",
	}}})
	m.view.mode = modeInputs

	lines := m.inputLines()
	out := strings.Join(lines, "\n")

	assert.Contains(t, out, "input/scope.md")
	assert.Contains(t, out, "# Review")
	assert.Contains(t, out, "scope")
	assert.NotContains(t, out, "**scope**")
	assert.Contains(t, out, "- inspect")
	assert.Contains(t, out, "app/ui")
	assert.NotContains(t, out, "`app/ui`")
	assert.Contains(t, out, "> keep the status visible")
	assert.NotContains(t, out, "```go")
	assert.Contains(t, out, "    func main() {}")
}

func TestModel_inputLines_markdownFenceMatchesDelimiterAndLength(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "````go\n**literal one**\n~~~\n**literal two**\n```\n**literal three**\n````\n**rendered**",
	}}})
	m.view.mode = modeInputs

	out := strings.Join(m.inputLines(), "\n")
	assert.Contains(t, out, "    **literal one**")
	assert.Contains(t, out, "    ~~~")
	assert.Contains(t, out, "    **literal two**")
	assert.Contains(t, out, "    ```")
	assert.Contains(t, out, "    **literal three**")
	assert.NotContains(t, out, "````")
	assert.NotContains(t, out, "**rendered**")
	assert.Contains(t, out, "rendered")
}

func TestModel_inputLines_verbatimAndNotice(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{
		{Label: "data.json", Path: "input/context/data.json", Content: `{"literal":"**not markdown**"}`},
		{Label: "goal", Path: "input/goal.md", Markdown: true, Notice: "not provided"},
	}})
	m.view.mode = modeInputs

	assert.Contains(t, strings.Join(m.inputLinesAt(0), "\n"), `{"literal":"**not markdown**"}`)
	goal := strings.Join(m.inputLinesAt(1), "\n")
	assert.Contains(t, goal, "input/goal.md")
	assert.Contains(t, goal, "not provided")
}

func TestModel_inputLines_wraps(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "A paragraph with enough words to wrap across several terminal rows without losing its tail.",
	}}})
	m.view.mode = modeInputs
	m.view.cols = 30

	lines := m.inputLines()
	require.Greater(t, len(lines), 4)
	assert.Contains(t, strings.Join(lines, " "), "losing its tail.")
	for _, line := range lines {
		assert.LessOrEqual(t, lipgloss.Width(line), 30)
	}
}

func TestModel_inputLines_verbatimPreservesWhitespace(t *testing.T) {
	m := New(ModelConfig{})
	m.view.cols = 20
	line := strings.Repeat("x", 19) + "  y"
	assert.Equal(t, line, strings.Join(m.verbatimLine("", line), ""),
		"hard wrapping may move whitespace to another row but must not discard it")

	code := "\tmake  target"
	wrapped := m.verbatimLine("    ", code)
	for i := range wrapped {
		wrapped[i] = strings.TrimPrefix(wrapped[i], "    ")
		assert.NotContains(t, wrapped[i], "\t", "tabs are expanded before width accounting")
		assert.LessOrEqual(t, lipgloss.Width(wrapped[i]), 16)
	}
	assert.Equal(t, "    make  target", strings.Join(wrapped, ""))

	m.cfg.Inputs = []InputDocument{{Path: "input/scope.md", Content: "word\tword", Markdown: true}}
	m.view.mode = modeInputs
	assert.NotContains(t, strings.Join(m.inputLines(), "\n"), "\t",
		"Markdown prose uses the same tab expansion before its display-width wrapper")
}

func TestModel_inputLines_sanitizesMetadata(t *testing.T) {
	m := New(ModelConfig{Task: "bad\x1b]52;c;payload\a", Run: "run\nspoof", Inputs: []InputDocument{{
		Label: "name\x1b[2J", Path: "input/\npath", Notice: "failed\rspoof",
	}}})
	m.view.mode = modeInputs

	assert.Equal(t, "bad�name", m.visibleMetadata("bad\nname"))
	out := m.View()
	assert.NotContains(t, out, "\x1b]52", "metadata must not emit an OSC command")
	assert.NotContains(t, out, "\x1b[2J", "metadata must not emit a CSI command")
	assert.Contains(t, out, "bad�]52;c;payload�/run�spoof")
	assert.Contains(t, out, "input/�path")
	assert.Contains(t, m.tabBar(), "name�[2J")
}

func TestModel_inputLines_missingTab(t *testing.T) {
	m := New(ModelConfig{})
	m.view.mode = modeInputs
	assert.Equal(t, []string{"no such input"}, m.inputLines())
}
