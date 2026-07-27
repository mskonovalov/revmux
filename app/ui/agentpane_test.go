package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/pipeline"
)

func TestModel_agentLines(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"),
		event(pipeline.EventAgentActivity, "bugs+impl", "Reading the executor first."),
		event(pipeline.EventAgentActivity, "codex", "Checking the teardown path."),
	)

	t.Run("the focused agent's own scrollback", func(t *testing.T) {
		m.view.tab = 1
		assert.Equal(t, []string{"16:02:11 started [bugs, impl]", "16:02:11 Reading the executor first."}, m.agentLines(),
			"timestamp then text, wrapped and markdown-rendered like the combined log")
		m.view.tab = 2
		assert.Equal(t, []string{"16:02:11 Checking the teardown path."}, m.agentLines(),
			"one pane never shows another agent's lines")
	})

	t.Run("an agent that has not reported yet", func(t *testing.T) {
		idle := New(ModelConfig{Roster: roster()})
		idle.view.tab = 2
		assert.Equal(t, []string{"waiting for codex..."}, idle.agentLines())
	})

	t.Run("a tab past the roster", func(t *testing.T) {
		m.view.tab = 9
		assert.Equal(t, []string{"no such agent"}, m.agentLines())
	})
}

func TestModel_focused(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Nil(t, m.focused(), "tab 0 is the combined view, which belongs to no agent")
	m.view.tab = 1
	assert.Equal(t, "bugs+impl", m.focused().spec.Name)
	m.view.tab = 3
	assert.Nil(t, m.focused())
}

func TestModel_agentLines_rendersAndWraps(t *testing.T) {
	// **both behaviors, exercised past their no-op form.** The pane gained markdown rendering and
	// wrapping, and a case feeding short plain text at the default width exercises neither — it passes
	// unchanged with both removed, which is what the previous assertion did.
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 60, Height: 24})
	m = feed(t, m,
		event(pipeline.EventAgentActivity, "bugs+impl",
			"checking `app/executor/rollout.go` because **the watchdog** cannot see the rollout at all"),
	)
	m.view.tab = 1

	lines := m.agentLines()
	require.Greater(t, len(lines), 1, "the entry is longer than the pane, so it wraps")
	assert.NotContains(t, strings.Join(lines, ""), "`", "the backticks are rendered, not shown")
	assert.NotContains(t, strings.Join(lines, ""), "**", "and so is the emphasis")
	assert.Contains(t, strings.Join(lines, ""), ansiCodeOn+"app/executor/rollout.go"+ansiCodeOff)

	for _, l := range lines {
		assert.LessOrEqual(t, lipgloss.Width(l), 60, "no row runs past the terminal")
	}
	assert.Contains(t, strings.Join(strings.Fields(strings.Join(lines, " ")), " "),
		"cannot see the rollout at all", "and the tail survives rather than being clipped")

	t.Run("a forensic pane is the last place to lose the end of a line", func(t *testing.T) {
		// it is where a reader went looking for the detail
		assert.NotContains(t, lines[0], "...", "wrapped, never truncated with an ellipsis")
	})
}
