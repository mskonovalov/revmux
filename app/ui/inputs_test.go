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

func TestModel_inputLines_missingTab(t *testing.T) {
	m := New(ModelConfig{})
	m.view.mode = modeInputs
	assert.Equal(t, []string{"no such input"}, m.inputLines())
}
