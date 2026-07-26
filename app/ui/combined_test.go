package ui

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
)

func TestModel_combinedLines(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"),
		pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: at.Add(time.Second)},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at.Add(2 * time.Second),
			Findings: []finding.Finding{{Title: "one"}}},
		pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs+impl", Text: "1 findings", At: at.Add(3 * time.Second)},
	)

	lines := m.combinedLines()
	require.Len(t, lines, 5, "tool calls, state changes and findings each get one line")
	assert.Equal(t, "16:02:11 stage find", lines[0], "a stage change carries no agent prefix")
	assert.Equal(t, "16:02:11 "+roster()[0].Paint("bugs+impl")+": started [bugs, impl]", lines[1])
	assert.Equal(t, "16:02:12 "+roster()[1].Paint("codex")+": tool: Grep", lines[2], "chronological, not grouped by agent")
	assert.Contains(t, lines[3], "1 findings emitted")
	assert.Contains(t, lines[4], "done, 1 findings")
}

func TestModel_combinedLines_noThinking(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		event(pipeline.EventAgentActivity, "bugs+impl", thinkingActivity),
		event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"),
	)

	require.Len(t, m.combinedLines(), 1, "thinking would scroll the situational-awareness view past reading speed")
	assert.Contains(t, m.combinedLines()[0], "tool: Read")

	m.view.tab = 1
	assert.Equal(t, []string{"16:02:11 " + thinkingActivity, "16:02:11 tool: Read"}, m.agentLines(),
		"the forensic pane keeps it")
}

func TestModel_combinedLines_empty(t *testing.T) {
	assert.Equal(t, []string{"waiting for the first agent..."}, New(ModelConfig{Roster: roster()}).combinedLines())
}

func TestCombinedState_push(t *testing.T) {
	c := &combinedState{}
	for i := range combinedLimit + 5 {
		c.push(combinedEntry{agent: "bugs", text: strconv.Itoa(i), at: at})
	}

	require.Len(t, c.entries, combinedLimit, "the compact log is bounded like a pane")
	assert.Equal(t, "5", c.entries[0].text, "the oldest entries are the ones dropped")
}

func TestModel_paint(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Equal(t, "\x1b[36mbugs+impl\x1b[39m", m.paint("bugs+impl"))
	assert.Equal(t, "stranger", m.paint("stranger"), "an agent with no spec keeps the default foreground")
}
