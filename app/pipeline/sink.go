package pipeline

import (
	"sync"

	"github.com/umputun/revmux/app/executor"
)

// sink adapts executor.EventSink to the pipeline channel, tagging every event with the agent that
// produced it. Pipeline itself does not implement the interface: that would force an exported method
// whose only purpose is interface satisfaction and collide with the pipeline's own emit path.
//
// first fires once, before the event is offered to the lossy channel, so the stagger's leader can
// release the rest of the roster on real activity rather than on the delay expiring.
//
// It fires on activity alone. A process starting is not activity: proc emits that the instant the fork
// succeeds, before a byte of output has been read, so releasing on it would open the gate on every
// launch and leave the stagger proving nothing about auth or quota. Neither is a resolved-configuration
// line — codex prints its banner before contacting a model, so those arrive as EventInfo.
type sink struct {
	agent string
	emit  func(Event)
	first func()
	once  sync.Once
}

func newSink(agent string, emit func(Event), first func()) *sink {
	return &sink{agent: agent, emit: emit, first: first}
}

// Emit forwards one executor event. The pipeline owns the agent lifecycle kinds, so a process
// starting or exiting arrives here as a state change rather than as a second agent-started.
func (s *sink) Emit(ev executor.Event) {
	if s.first != nil && ev.Kind == executor.EventActivity {
		s.once.Do(s.first)
	}
	s.emit(Event{Kind: s.kind(ev.Kind), Agent: s.agent, Text: ev.Text})
}

func (s *sink) kind(k executor.EventKind) EventKind {
	switch k {
	case executor.EventActivity:
		return EventAgentActivity
	case executor.EventRateLimit:
		return EventRateLimit
	case executor.EventStarted, executor.EventInfo, executor.EventFinished:
		return EventAgentState
	}
	return EventAgentState
}
