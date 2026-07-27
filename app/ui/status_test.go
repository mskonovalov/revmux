package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	require.Len(t, rows, 6,
		"a header, the rule under it, a column heading, one row per agent, and the closing rule")
	assert.Equal(t, m.rule(), rows[1], "the header is separated from the table rather than reading as its first row")
	assert.Equal(t, "revmux · 2 agents · find", rows[0])
	assert.Contains(t, rows[2], "AGENT", "the column heading names what each column holds")
	assert.Contains(t, rows[2], "ACTIVITY")
	assert.Contains(t, rows[3], "running")
	assert.Contains(t, rows[3], "9s", "elapsed comes off the event timestamps, not a clock")
	assert.Contains(t, rows[3], "tool: Read", "the row shows the last activity")
	assert.Contains(t, rows[4], "waiting", "an agent that has not reported yet is still listed")
	assert.Contains(t, rows[4], "-", "and has no elapsed time")
}

func TestModel_statusTable_color(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	rows := strings.Split(m.statusTable(), "\n")

	assert.Contains(t, rows[3], roster()[0].Paint("bugs+impl"), "an ANSI-named color reaches the row")
	assert.Contains(t, rows[4], "\x1b[38;2;255;136;0m", "a hex color reaches it as truecolor")

	t.Run("names are padded before they are painted", func(t *testing.T) {
		// the color sequence has no width: padding after painting would count it as if it did and
		// leave the state column ragged
		plain := strings.Split(New(ModelConfig{Roster: []prompt.AgentSpec{
			{Name: "bugs+impl"}, {Name: "codex"},
		}}).statusTable(), "\n")
		assert.Equal(t, strings.Index(plain[3], "waiting"), strings.Index(plain[4], "waiting"))
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
			"revmux · 2 agents · synthesis",
		},
		{
			"findings are counted across agents, and broken down by severity",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Critical}, {Severity: finding.Minor}}},
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "codex", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}}},
			},
			"revmux · 2 agents · 3 findings (1 critical, 1 major, 1 minor)",
		},
		{
			"a severity the model invented counts toward the total and is named nowhere",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}, {Severity: "invented"}}},
			},
			"revmux · 2 agents · 2 findings (0 critical, 1 major, 0 minor)",
		},
		{
			"a synthesis row does not inflate the agent count's own findings total",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}, {Severity: finding.Major}}},
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "synthesis", At: at, Findings: []finding.Finding{
					{Severity: finding.Minor}}},
			},
			"revmux · 3 agents · 1 findings (0 critical, 0 major, 1 minor)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: roster()}), tt.msgs...)
			assert.Equal(t, tt.want, m.header())
		})
	}
}

func TestModel_header_degradesInsteadOfClipping(t *testing.T) {
	// the completion notice is the rightmost thing on the line, so clipping takes it first — and it is
	// the one part a reader needs at exactly the moment the line is longest
	full := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: at},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}},
		CompletedMsg{Report: finding.Report{Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}}},
	)

	for _, width := range []int{120, 70, 55, 40, 30, 12} {
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			m := feed(t, full, tea.WindowSizeMsg{Width: width, Height: 24})
			assert.LessOrEqual(t, lipgloss.Width(m.header()), width, "the header must fit rather than be cut")
		})
	}

	t.Run("the completion notice outlives everything the header can give up", func(t *testing.T) {
		// down to the width where the notice alone no longer fits, which is where clipping takes over
		for _, width := range []int{120, 70, 55, 40, 32} {
			m := feed(t, full, tea.WindowSizeMsg{Width: width, Height: 24})
			assert.Contains(t, m.header(), "complete", "at %d columns", width)
		}
	})

	t.Run("it gives up the breakdown before the notice", func(t *testing.T) {
		wide := feed(t, full, tea.WindowSizeMsg{Width: 120, Height: 24})
		assert.Contains(t, wide.header(), "(1 critical, 1 major, 1 minor)", "there is room for all of it")

		narrow := feed(t, full, tea.WindowSizeMsg{Width: 55, Height: 24})
		assert.NotContains(t, narrow.header(), "critical", "the longest part goes first")
		assert.Contains(t, narrow.header(), "3 findings", "but the total stays")
	})
}
