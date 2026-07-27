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
// It fires on model output alone — activity or progress, prose or a tool call, both being assistant
// turns. Nothing else counts: a resolved-configuration line arrives as EventInfo because codex prints
// its banner before contacting a model, and proc deliberately emits nothing at all when the fork
// succeeds. Releasing on either would open the gate before a byte had been read and leave the stagger
// proving nothing about auth or quota.
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
// The leader gate opens on the first sign of life, and **progress counts as life**. A review agent
// commonly spends its opening stretch reading files without saying a word, so gating on prose alone
// would leave the leader silent and make stagger_delay the only release path — the exact failure the
// first-activity wire exists to prevent.
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
