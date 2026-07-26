package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/pipeline"
)

// filled is a model holding n activity lines from one agent, sized so the pane shows exactly 5.
func filled(t *testing.T, n int) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	for i := range n {
		m = feed(t, m, event(pipeline.EventAgentActivity, "bugs+impl", "line "+strconv.Itoa(i)))
	}
	return m
}

func TestModel_View(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"),
	)

	out := m.View()
	assert.Contains(t, out, "revmux · 2 agents · stage find", "the status table is on top")
	assert.Contains(t, out, "\x1b[7m 0 · all \x1b[27m", "the combined view is focused by default")
	assert.Contains(t, out, "1 · bugs+impl")
	assert.Contains(t, out, "2 · codex")
	assert.Contains(t, out, "tool: Read", "and the detail pane under it")
}

func TestModel_tabBar(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Equal(t, "\x1b[7m 0 · all \x1b[27m| 1 · bugs+impl | 2 · codex ", m.tabBar())
	m.view.tab = 2
	assert.Equal(t, " 0 · all | 1 · bugs+impl |\x1b[7m 2 · codex \x1b[27m", m.tabBar(),
		"exactly one tab is marked, and it is the focused one")
}

func TestModel_detailPane(t *testing.T) {
	t.Run("everything fits, and the pane is padded out to keep the frame's shape", func(t *testing.T) {
		m := filled(t, 3)
		m.view.tab = 1
		pane := m.detailPane()
		require.Len(t, pane, m.paneHeight())
		assert.Contains(t, pane[2], "line 2")
		assert.Empty(t, pane[3])
		assert.Equal(t, 0, m.maxScroll(), "there is nothing to scroll back to")
	})

	t.Run("a longer log shows its newest lines", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		pane := m.detailPane()
		require.Len(t, pane, m.paneHeight())
		assert.Contains(t, pane[len(pane)-1], "line 19", "a live log is read from the bottom")
	})

	t.Run("scrolling back moves the window", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		m.view.scroll = 3
		pane := m.detailPane()
		assert.Contains(t, pane[len(pane)-1], "line 16")
	})

	t.Run("a scroll past the top clamps to the oldest line", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		m.view.scroll = 500
		assert.Contains(t, m.detailPane()[0], "line 0")
	})
}

func TestModel_clip(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 12, Height: 20})

	assert.Equal(t, "hello world ", m.clip("hello world that runs on"))
	assert.Equal(t, "\x1b[36mbugs\x1b[39m", m.clip(roster()[0].Paint("bugs")),
		"a color sequence has no width and must not be counted as if it did")

	t.Run("an unsized terminal falls back to a usable default", func(t *testing.T) {
		bare := Model{}
		assert.Equal(t, defaultCols, bare.view.width())
		assert.Equal(t, defaultRows, bare.view.height())
	})
}

func TestModel_paneHeight(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 80, Height: 10})
	assert.Equal(t, 5, m.paneHeight(), "the status table and the tab bar take their rows first")
	assert.Len(t, strings.Split(m.View(), "\n"), 10, "and the frame fits the terminal")

	t.Run("a terminal too short for the table still renders a pane", func(t *testing.T) {
		tiny := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 40, Height: 2})
		assert.Equal(t, 1, tiny.paneHeight())
	})
}
