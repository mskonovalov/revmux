package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

// progressTimeFormat keeps each line short: a run is minutes long, so the date adds nothing.
const progressTimeFormat = "15:04:05"

// progress is the plain event renderer, used with --no-tui and whenever the tty cannot be opened.
// It writes to stderr, never to stdout, which belongs to the report alone.
//
// It takes the resolved roster for the same reason the TUI does: the color is on the agent's spec, so
// a reviewer switching renderers sees the same agent in the same color.
type progress struct {
	w      io.Writer
	roster []prompt.AgentSpec
}

// run drains the event channel until the pipeline closes it.
func (pr *progress) run(events <-chan pipeline.Event) {
	for ev := range events {
		if line := pr.line(ev); line != "" {
			_, _ = fmt.Fprintln(pr.w, line)
		}
	}
}

// line renders one event. Every EventKind needs a case here and in the TUI, or it is invisible in
// one of the two renderers.
func (pr *progress) line(ev pipeline.Event) string {
	var what string
	switch ev.Kind {
	case pipeline.EventStage:
		what = "stage " + ev.Stage
	case pipeline.EventAgentStarted:
		what = "started"
		if ev.Text != "" {
			what += " [" + ev.Text + "]"
		}
	case pipeline.EventAgentActivity, pipeline.EventAgentState:
		what = ev.Text
	case pipeline.EventAgentDone:
		what = "done"
		if ev.Text != "" {
			what += ", " + ev.Text
		}
	case pipeline.EventAgentRetried:
		what = "retrying: " + ev.Text
	case pipeline.EventAgentDegraded:
		what = "degraded: " + ev.Text
	case pipeline.EventFindings:
		what = strconv.Itoa(len(ev.Findings)) + " findings emitted"
	case pipeline.EventRateLimit:
		what = "rate limited: " + ev.Text
	default:
		return ""
	}
	if what == "" {
		return ""
	}

	out := ev.At.Format(progressTimeFormat) + " "
	if ev.Agent != "" {
		out += pr.paint(ev.Agent) + ": "
	}
	return out + what
}

// paint colors the agent's name from its own roster entry. An agent the roster does not name — or a
// run with no roster at all — stays in the default foreground rather than picking a color here.
func (pr *progress) paint(agent string) string {
	for _, spec := range pr.roster {
		if spec.Name == agent {
			return spec.Paint(agent)
		}
	}
	return agent
}
