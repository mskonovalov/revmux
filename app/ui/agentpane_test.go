package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
