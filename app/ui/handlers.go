package ui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// keys are the bindings the detail panes answer to. Number keys are matched separately, since the
// digit selects the tab and listing ten bindings here would say the same thing ten times.
var keys = struct {
	quit, nextTab, prevTab, up, down, pageUp, pageDown, top, bottom key.Binding
}{
	quit:     key.NewBinding(key.WithKeys("q", "ctrl+c", "esc")),
	nextTab:  key.NewBinding(key.WithKeys("tab", "right", "l")),
	prevTab:  key.NewBinding(key.WithKeys("shift+tab", "left", "h")),
	up:       key.NewBinding(key.WithKeys("up", "k")),
	down:     key.NewBinding(key.WithKeys("down", "j")),
	pageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
	pageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
	top:      key.NewBinding(key.WithKeys("home", "g")),
	bottom:   key.NewBinding(key.WithKeys("end", "G")),
}

// key handles one keystroke. Quitting stops watching the run, it does not stop the run: package main
// still holds the report and writes it once the program has returned.
func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.quit):
		return m, tea.Quit
	case key.Matches(msg, keys.nextTab):
		m.focus(m.view.tab + 1)
	case key.Matches(msg, keys.prevTab):
		m.focus(m.view.tab - 1)
	case key.Matches(msg, keys.up):
		m.scroll(1)
	case key.Matches(msg, keys.down):
		m.scroll(-1)
	case key.Matches(msg, keys.pageUp):
		m.scroll(m.paneHeight())
	case key.Matches(msg, keys.pageDown):
		m.scroll(-m.paneHeight())
	case key.Matches(msg, keys.top):
		m.view.scroll = m.maxScroll()
	case key.Matches(msg, keys.bottom):
		m.view.scroll = 0
	default:
		if n, err := strconv.Atoi(msg.String()); err == nil {
			m.focus(n)
		}
	}
	return m, nil
}

// focus switches panes, ignoring a tab that does not exist so a stray digit cannot blank the view.
// The new pane opens at its newest line, which is where a live log is worth reading from.
func (m *Model) focus(tab int) {
	if tab < 0 || tab > len(m.agents) {
		return
	}
	m.view.tab, m.view.scroll = tab, 0
}

// scroll moves the window back through the log by n lines, clamped to what the pane actually holds.
func (m *Model) scroll(n int) {
	m.view.scroll = min(max(m.view.scroll+n, 0), m.maxScroll())
}
