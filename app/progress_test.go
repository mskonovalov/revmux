package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/ui"
)

var progressAt = time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)

func TestProgress_line(t *testing.T) {
	tests := []struct {
		name string
		ev   pipeline.Event
		want string
	}{
		{name: "stage", ev: pipeline.Event{Kind: pipeline.EventStage, Stage: "find"}, want: "16:02:11 stage find"},
		{
			name: "agent started names its lenses",
			ev:   pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "bugs+impl", Text: "bugs, impl"},
			want: "16:02:11 bugs+impl: started [bugs, impl]",
		},
		{
			name: "agent started with no lenses",
			ev:   pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "solo"},
			want: "16:02:11 solo: started",
		},
		{
			name: "activity", ev: pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs", Text: "tool: Read"},
			want: "16:02:11 bugs: tool: Read",
		},
		{
			name: "state", ev: pipeline.Event{Kind: pipeline.EventAgentState, Agent: "bugs", Text: "exit 0"},
			want: "16:02:11 bugs: exit 0",
		},
		{
			name: "done", ev: pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs", Text: "3 findings"},
			want: "16:02:11 bugs: done, 3 findings",
		},
		{name: "done with no detail", ev: pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs"}, want: "16:02:11 bugs: done"},
		{
			name: "retried", ev: pipeline.Event{Kind: pipeline.EventAgentRetried, Agent: "bugs", Text: "idle timeout"},
			want: "16:02:11 bugs: retrying: idle timeout",
		},
		{
			name: "degraded", ev: pipeline.Event{Kind: pipeline.EventAgentDegraded, Agent: "bugs", Text: "stalled twice"},
			want: "16:02:11 bugs: degraded: stalled twice",
		},
		{
			name: "findings", ev: pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs", Findings: []finding.Finding{{}, {}}},
			want: "16:02:11 bugs: 2 findings emitted",
		},
		{
			name: "rate limit", ev: pipeline.Event{Kind: pipeline.EventRateLimit, Agent: "bugs", Text: "throttled"},
			want: "16:02:11 bugs: rate limited: throttled",
		},
		{name: "unknown kind renders nothing", ev: pipeline.Event{Kind: pipeline.EventKind("invented")}, want: ""},
	}

	pr := &progress{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.ev
			ev.At = progressAt
			assert.Equal(t, tt.want, pr.line(ev))
		})
	}
}

func TestProgress_paint(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs+impl", Color: "6"}, {Name: "codex", Color: "#ff8800"}}
	pr := &progress{roster: roster}

	tests := []struct {
		name string
		ev   pipeline.Event
		want string
	}{
		{
			"the agent's own color prefixes its line",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs+impl", Text: "tool: Read", At: progressAt},
			"16:02:11 \x1b[36mbugs+impl\x1b[39m: tool: Read",
		},
		{
			"a hex color reaches the line as truecolor",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: progressAt},
			"16:02:11 \x1b[38;2;255;136;0mcodex\x1b[39m: tool: Grep",
		},
		{
			"an agent the roster does not name keeps the default foreground",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "stranger", Text: "tool: Read", At: progressAt},
			"16:02:11 stranger: tool: Read",
		},
		{
			"a stage change belongs to no agent",
			pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: progressAt},
			"16:02:11 stage find",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pr.line(tt.ev))
		})
	}
}

// the roster carries the resolved color, so a reviewer switching renderers sees the same agent the
// same way. A color picked inside either renderer would exist in that one only.
func TestProgress_colorsAgreeWithTheTUI(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs+impl", Color: "6"}, {Name: "codex", Color: "#ff8800"}}
	ev := pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: progressAt}

	plain := (&progress{roster: roster}).line(ev)

	m := ui.New(ui.ModelConfig{Roster: roster})
	tui, _ := m.Update(ev)
	rendered := tui.(ui.Model).View()

	prefix := roster[1].Paint("codex")
	assert.Contains(t, plain, prefix)
	assert.Contains(t, rendered, prefix)
}

func TestProgress_run(t *testing.T) {
	t.Run("renders the whole sequence and stops on close", func(t *testing.T) {
		events := make(chan pipeline.Event, 4)
		for _, ev := range []pipeline.Event{
			{Kind: pipeline.EventStage, Stage: "find", At: progressAt},
			{Kind: pipeline.EventAgentStarted, Agent: "bugs", Text: "bugs", At: progressAt},
			{Kind: pipeline.EventKind("invented"), At: progressAt},
			{Kind: pipeline.EventAgentDone, Agent: "bugs", Text: "1 findings", At: progressAt},
		} {
			events <- ev
		}
		close(events)

		out := &strings.Builder{}
		pr := &progress{w: out}
		pr.run(events)

		assert.Equal(t, []string{
			"16:02:11 stage find",
			"16:02:11 bugs: started [bugs]",
			"16:02:11 bugs: done, 1 findings",
		}, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"))
	})

	t.Run("an already closed channel writes nothing", func(t *testing.T) {
		events := make(chan pipeline.Event)
		close(events)

		out := &strings.Builder{}
		pr := &progress{w: out}
		pr.run(events)
		assert.Empty(t, out.String())
	})
}
