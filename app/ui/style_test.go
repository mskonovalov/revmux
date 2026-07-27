package ui

import (
	"bytes"
	"testing"

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
