package pipeline

import (
	"sync"

	"github.com/umputun/revmux/app/executor"
)

// sink adapts executor.EventSink to the pipeline channel, tagging every event with the agent that
// produced it. Pipeline itself does not implement the interface: that would force an exported method
// whose only purpose is interface satisfaction. first fires once, before the event is offered to the
// lossy channel, and on model output alone — anything a process emits before reaching a model would
// open the gate with nothing proved about auth or quota.
type sink struct {
	agent string
	emit  func(Event)
	first func()
	once  sync.Once
}

func newSink(agent string, emit func(Event), first func()) *sink {
	return &sink{agent: agent, emit: emit, first: first}
}

// Emit forwards one executor event. The pipeline owns the agent lifecycle kinds, so a process starting
// or exiting arrives here as a state change rather than a second agent-started. Progress counts as life
// for the gate: an agent that opens by reading twenty files says nothing for its first minute.
func (s *sink) Emit(ev executor.Event) {
	if s.first != nil && (ev.Kind == executor.EventActivity || ev.Kind == executor.EventProgress) {
		s.once.Do(s.first)
	}
	s.emit(Event{Kind: s.kind(ev.Kind), Agent: s.agent, Text: ev.Text})
}

func (s *sink) kind(k executor.EventKind) EventKind {
	switch k {
	case executor.EventActivity:
		return EventAgentActivity
	case executor.EventProgress:
		return EventAgentProgress
	case executor.EventRateLimit:
		return EventRateLimit
	case executor.EventFinished:
		// an exit code is a status detail, never a log line: the pipeline emits its own done event a
		// moment later carrying what the agent actually produced, and "exit 0" beside it says nothing
		return EventAgentProgress
	case executor.EventInfo:
		return EventAgentState
	}
	return EventAgentState
}
