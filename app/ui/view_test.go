package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

// filled is a model holding n activity lines from one agent, sized so the pane shows exactly 5.
func filled(t *testing.T, n int) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	// height leaves a five-line pane: two agent rows plus the frame's chrome. The scroll expectations
	// downstream are all measured in screenfuls, so the pane size is what has to stay fixed here.
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: len(roster()) + chromeLines + 5})
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
	assert.Contains(t, out, "AGENT", "under a column heading")
	assert.Contains(t, out, "▸ 1 all", "tabs are numbered from one, and the combined view is focused by default")
	assert.Contains(t, out, "2 bugs+impl")
	assert.Contains(t, out, "3 codex")
	assert.Contains(t, out, "tool: Read", "and the detail pane under it")
}

func TestModel_tabBar(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Equal(t, "▸ 1 all  │  2 bugs+impl  │  3 codex", m.tabBar())
	m.view.tab = 2
	assert.Equal(t, "  1 all  │  2 bugs+impl  │▸ 3 codex", m.tabBar(),
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
	assert.Equal(t, 3, m.paneHeight(), "the table, its heading, both rules and the tab bar take their rows first")
	assert.Len(t, strings.Split(m.View(), "\n"), 10, "and the frame still fits the terminal exactly")

	t.Run("a terminal too short for the table still renders a pane", func(t *testing.T) {
		tiny := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 40, Height: 2})
		assert.Equal(t, 1, tiny.paneHeight())
	})
}

func TestModel_tabBar_collapsesWhenItCannotFit(t *testing.T) {
	// a real roster plus the verify groups runs past 80 columns easily, and there is no horizontal
	// scroll on this line: clipping alone cuts the right-hand tabs mid-word, so a reader cannot tell
	// how many panes exist or what is hiding past the edge
	wide := []prompt.AgentSpec{
		{Name: "bugs+impl"}, {Name: "arch+quality"}, {Name: "docs+tests"}, {Name: "codex"},
		{Name: "synthesis"}, {Name: "verify 1"}, {Name: "verify 2"}, {Name: "verify 3"},
	}
	m := feed(t, New(ModelConfig{Roster: wide}), tea.WindowSizeMsg{Width: 40, Height: 24})
	m.view.tab = 3

	bar := m.tabBar()
	assert.LessOrEqual(t, lipgloss.Width(bar), 40, "the bar must fit the terminal, not be cut off by it")
	assert.Contains(t, bar, "4 docs+tests", "the focused tab keeps its name — its content is what is on screen")
	assert.NotContains(t, bar, "bugs+impl", "the rest drop to their number rather than being sliced in half")
	for _, n := range []string{"1", "2", "5", "9"} {
		assert.Contains(t, bar, n, "every tab stays reachable, so the count is still visible")
	}

	t.Run("a bar that fits is left alone", func(t *testing.T) {
		roomy := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 120, Height: 24})
		assert.Contains(t, roomy.tabBar(), "2 bugs+impl", "nothing is abbreviated when there is room")
	})
}

func TestTabToken(t *testing.T) {
	m0 := New(ModelConfig{})
	assert.Equal(t, "1", m0.tabToken(0), "the combined view is tab one")
	assert.Equal(t, "9", m0.tabToken(8))
	assert.Equal(t, "a", m0.tabToken(9), "letters carry on where the digits stop")
	assert.Equal(t, " ", m0.tabToken(9+len(tabLetters)), "past the tokens a pane has none, and is reached with tab")

	t.Run("every token round-trips to the pane it names", func(t *testing.T) {
		for i := range 9 + len(tabLetters) {
			assert.Equal(t, i, m0.tabIndex(m0.tabToken(i)), "token %q must select pane %d", m0.tabToken(i), i)
		}
	})

	t.Run("no token collides with a key already bound to something else", func(t *testing.T) {
		// a bound key is matched before the token lookup is reached, so a tab assigned one would be
		// unreachable — silently, and only on a run with enough panes to get that far
		for _, b := range []key.Binding{keys.quit, keys.nextTab, keys.prevTab, keys.up, keys.down,
			keys.pageUp, keys.pageDown, keys.top, keys.bottom, keys.findings, keys.expand, keys.startFilter} {
			for _, k := range b.Keys() {
				assert.Equal(t, -1, m0.tabIndex(k), "key %q is bound already and must not also name a tab", k)
			}
		}
	})

	t.Run("a key naming nothing is ignored rather than blanking the view", func(t *testing.T) {
		for _, k := range []string{"0", "", "ab", "ctrl+c", "Z"} {
			assert.Equal(t, -1, m0.tabIndex(k))
		}
	})
}
