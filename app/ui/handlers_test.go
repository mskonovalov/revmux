package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// press builds the key message for one keystroke, runes and named keys alike.
func press(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestModel_key_tabs(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"tab moves forward", []string{"tab"}, 1},
		{"and again", []string{"tab", "tab"}, 2},
		{"past the last agent it stays put", []string{"tab", "tab", "tab"}, 2},
		{"shift+tab moves back", []string{"tab", "tab", "shift+tab"}, 1},
		{"back past the combined view stays put", []string{"shift+tab"}, 0},
		// tabs are labeled from one, so the digit typed is one past the index it selects
		{"a digit selects a pane directly", []string{"3"}, 2},
		{"one returns to the combined view", []string{"3", "1"}, 0},
		{"a digit past the roster is ignored", []string{"2", "7"}, 1},
		{"zero is not a tab", []string{"2", "0"}, 1},
		{"an unbound key changes nothing", []string{"2", "z"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(ModelConfig{Roster: roster()})
			for _, k := range tt.keys {
				m = feed(t, m, press(k))
			}
			assert.Equal(t, tt.want, m.view.tab)
		})
	}
}

func TestModel_key_scroll(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"up scrolls back one line", []string{"up"}, 1},
		{"and k does the same", []string{"k", "k"}, 2},
		{"down comes back toward the newest", []string{"up", "up", "down"}, 1},
		{"it never scrolls past the newest line", []string{"down"}, 0},
		{"page up moves a screenful", []string{"pgup"}, 5},
		{"page down comes back", []string{"pgup", "pgdown"}, 0},
		{"g jumps to the oldest line", []string{"g"}, 15},
		{"G returns to the newest", []string{"g", "G"}, 0},
		{"it never scrolls past the oldest line", []string{"g", "pgup", "pgup"}, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := filled(t, 20)
			m = feed(t, m, press("2")) // the first agent's pane; tabs are labeled from one
			require.Equal(t, 15, m.maxScroll(), "20 lines in a pane showing 5")
			for _, k := range tt.keys {
				m = feed(t, m, press(k))
			}
			assert.Equal(t, tt.want, m.view.scroll)
		})
	}

	t.Run("switching panes opens the new one at its newest line", func(t *testing.T) {
		m := feed(t, filled(t, 20), press("2"), press("g"))
		require.Equal(t, 15, m.view.scroll)
		m = feed(t, m, press("1"))
		assert.Equal(t, 0, m.view.scroll)
	})

	t.Run("a pane with nothing in it does not scroll", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), press("2"), press("up"))
		assert.Equal(t, 0, m.view.scroll)
	})
}

func TestModel_key_quit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c", "esc"} {
		t.Run(k, func(t *testing.T) {
			_, cmd := New(ModelConfig{Roster: roster()}).Update(press(k))
			require.NotNil(t, cmd)
			assert.IsType(t, tea.QuitMsg{}, cmd(), "quitting stops watching the run, package main still writes the report")
		})
	}
}
