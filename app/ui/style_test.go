package ui

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestStyles_profiledAgainstTheGivenSurface(t *testing.T) {
	// lipgloss decides whether to emit color by profiling a writer, and its default renderer profiles
	// os.Stdout. Under `revmux > file` stdout is a pipe while the reader watches a terminal, so
	// a palette left on the default comes out colorless there while AgentSpec.Paint keeps emitting raw
	// ANSI regardless — a half painted frame. Passing the surface in is what prevents that.
	t.Run("a nil surface still builds a usable palette", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		assert.NotEmpty(t, m.rule(), "the frame renders with no output configured, as a test wants")
	})

	t.Run("the palette follows the surface it was given", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster(), Output: &bytes.Buffer{}})
		assert.NotContains(t, m.rule(), "\x1b[", "a plain buffer reports no color, so none is emitted")
		assert.NotContains(t, m.header(), "\x1b[", "and the header follows the same renderer")
	})
}

func TestModel_countStyle_followsTheWorstSeverity(t *testing.T) {
	tests := []struct {
		name  string
		found tally
		want  lipgloss.TerminalColor
	}{
		{"nothing above minor is the green case", tally{total: 9, minor: 9}, colOK},
		{"one major outranks nine minors", tally{total: 10, major: 1, minor: 9}, colWarn},
		{"one critical outranks the majors under it", tally{total: 10, critical: 1, major: 2, minor: 7}, colErr},
		{"a severity the model invented is counted but does not raise the color", tally{total: 1}, colOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(ModelConfig{Roster: roster()})
			m.found = tt.found
			assert.Equal(t, tt.want, m.countStyle().GetForeground())
			assert.True(t, m.countStyle().GetBold(), "the count is the line's accent whatever color it takes")
		})
	}
}

func TestModel_stateStyle_coversEveryState(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	states := []string{stateWaiting, stateRunning, stateRetrying, stateLimited, stateDone, stateDegraded}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			// rendering through the style must not drop the text, whatever the state is called
			assert.Contains(t, m.stateStyle(state).Render(state), state)
		})
	}
}
