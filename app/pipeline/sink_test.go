package pipeline

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/revmux/app/executor"
)

func TestSink_Emit(t *testing.T) {
	tests := []struct {
		name string
		in   executor.Event
		want Event
	}{
		{
			name: "activity", in: executor.Event{Kind: executor.EventActivity, Text: "tool: Read"},
			want: Event{Kind: EventAgentActivity, Agent: "bugs", Text: "tool: Read"},
		},
		{
			name: "rate limit", in: executor.Event{Kind: executor.EventRateLimit, Text: "throttled"},
			want: Event{Kind: EventRateLimit, Agent: "bugs", Text: "throttled"},
		},
		{
			name: "process start is a state change, not a second agent-started",
			in:   executor.Event{Kind: executor.EventStarted, Text: "claude"},
			want: Event{Kind: EventAgentState, Agent: "bugs", Text: "claude"},
		},
		{
			name: "process exit", in: executor.Event{Kind: executor.EventFinished, Text: "exit 0"},
			want: Event{Kind: EventAgentState, Agent: "bugs", Text: "exit 0"},
		},
		{
			name: "an unknown executor kind still reaches a renderer",
			in:   executor.Event{Kind: executor.EventKind("something-new"), Text: "?"},
			want: Event{Kind: EventAgentState, Agent: "bugs", Text: "?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Event
			s := newSink("bugs", func(ev Event) { got = append(got, ev) }, nil)
			s.Emit(tt.in)
			assert.Equal(t, []Event{tt.want}, got)
		})
	}
}

func TestSink_firstActivity(t *testing.T) {
	t.Run("fires once, before the event reaches the channel", func(t *testing.T) {
		var order []string
		first := func() { order = append(order, "first") }
		s := newSink("bugs", func(Event) { order = append(order, "emit") }, first)

		s.Emit(executor.Event{Kind: executor.EventActivity, Text: "a"})
		s.Emit(executor.Event{Kind: executor.EventActivity, Text: "b"})
		s.Emit(executor.Event{Kind: executor.EventActivity, Text: "c"})
		assert.Equal(t, []string{"first", "emit", "emit", "emit"}, order)
	})

	t.Run("no callback is fine", func(t *testing.T) {
		var got int
		s := newSink("bugs", func(Event) { got++ }, nil)
		s.Emit(executor.Event{Kind: executor.EventActivity})
		assert.Equal(t, 1, got)
	})

	t.Run("concurrent emit fires the callback exactly once", func(t *testing.T) {
		var mu sync.Mutex
		var fired, emitted int
		s := newSink("bugs", func(Event) {
			mu.Lock()
			defer mu.Unlock()
			emitted++
		}, func() {
			mu.Lock()
			defer mu.Unlock()
			fired++
		})

		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() { s.Emit(executor.Event{Kind: executor.EventActivity}) })
		}
		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, fired)
		assert.Equal(t, 16, emitted)
	})
}
