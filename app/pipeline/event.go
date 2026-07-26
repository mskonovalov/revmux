package pipeline

import (
	"time"

	"github.com/umputun/revmux/app/finding"
)

// eventBuffer is how many events the channel holds before emit starts dropping. A blocked renderer
// must never stall the pipeline, and a dropped event is a cosmetic gap while the archive keeps the
// complete record.
const eventBuffer = 256

// EventKind names what an Event reports. A new kind needs a case in app/progress.go and in the TUI,
// or it is silently invisible in one of the two renderers.
type EventKind string

// Event kinds. There is deliberately no pipeline-complete kind: completion must not be droppable, so
// package main signals it to the TUI directly once Run has returned.
const (
	EventAgentStarted  EventKind = "agent_started"
	EventAgentActivity EventKind = "agent_activity"
	EventAgentState    EventKind = "agent_state"
	EventAgentDone     EventKind = "agent_done"
	EventAgentRetried  EventKind = "agent_retried"
	EventAgentDegraded EventKind = "agent_degraded"
	EventFindings      EventKind = "findings"
	EventStage         EventKind = "stage"
	EventRateLimit     EventKind = "rate_limit"
)

// Event is one thing that happened during a run. Agent names the process it came from so a renderer
// routes on the name rather than inferring an owner from the text.
//
// Findings travels on the event because the TUI's combined view has to show findings as they are
// emitted and no other transport reaches it.
type Event struct {
	Kind     EventKind         `json:"kind"`
	Agent    string            `json:"agent,omitempty"`
	Stage    string            `json:"stage,omitempty"`
	Text     string            `json:"text,omitempty"`
	Findings []finding.Finding `json:"findings,omitempty"`
	At       time.Time         `json:"at"`
}
