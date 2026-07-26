package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

func TestModel_statusTable(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"),
		pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs+impl", Text: "tool: Read", At: at.Add(9 * time.Second)},
	)

	rows := strings.Split(m.statusTable(), "\n")
	require.Len(t, rows, 4, "a header, a rule and one row per agent")
	assert.Equal(t, "revmux · 2 agents · stage find", rows[0])
	assert.Contains(t, rows[2], "running")
	assert.Contains(t, rows[2], "9s", "elapsed comes off the event timestamps, not a clock")
	assert.Contains(t, rows[2], "tool: Read", "the row shows the last activity")
	assert.Contains(t, rows[3], "waiting", "an agent that has not reported yet is still listed")
	assert.Contains(t, rows[3], "-", "and has no elapsed time")
}

func TestModel_statusTable_color(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	rows := strings.Split(m.statusTable(), "\n")

	assert.Contains(t, rows[2], roster()[0].Paint("bugs+impl"), "an ANSI-named color reaches the row")
	assert.Contains(t, rows[3], "\x1b[38;2;255;136;0m", "a hex color reaches it as truecolor")

	t.Run("names are padded before they are painted", func(t *testing.T) {
		// the color sequence has no width: padding after painting would count it as if it did and
		// leave the state column ragged
		plain := strings.Split(New(ModelConfig{Roster: []prompt.AgentSpec{
			{Name: "bugs+impl"}, {Name: "codex"},
		}}).statusTable(), "\n")
		assert.Equal(t, strings.Index(plain[2], "waiting"), strings.Index(plain[3], "waiting"))
	})
}

func TestModel_statusTable_header(t *testing.T) {
	tests := []struct {
		name string
		msgs []tea.Msg
		want string
	}{
		{"nothing has happened yet", nil, "revmux · 2 agents"},
		{
			"a stage is named once it opens",
			[]tea.Msg{pipeline.Event{Kind: pipeline.EventStage, Stage: "synthesis", At: at}},
			"revmux · 2 agents · stage synthesis",
		},
		{
			"findings are counted across agents",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{{}, {}}},
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "codex", At: at, Findings: []finding.Finding{{}}},
			},
			"revmux · 2 agents · 3 findings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: roster()}), tt.msgs...)
			assert.Equal(t, tt.want, m.header())
		})
	}
}
