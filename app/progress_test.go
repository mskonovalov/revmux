package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
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
