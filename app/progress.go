package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/ui"
)

// progressTimeFormat keeps each line short: a run is minutes long, so the date adds nothing.
const progressTimeFormat = "15:04:05"

// minCols is the narrowest terminal worth clipping to; anything less and a line is all prefix.
const minCols = 40

// fallbackCols is what an undetectable terminal gets — a redirected stderr, a pipe, a CI log.
const fallbackCols = 120

// progress is the plain event renderer, used with --no-tui and whenever the tty cannot be opened.
// It writes to stderr, never to stdout, which belongs to the report alone.
//
// It takes the resolved roster for the same reason the TUI does: the color is on the agent's spec, so
// a reviewer switching renderers sees the same agent in the same color.
type progress struct {
	w       io.Writer
	roster  []prompt.AgentSpec
	beats   map[string]time.Time // when each agent last put anything in the log
	pending map[string]string    // each agent's newest tool call, held for its next heartbeat
}

// due reports whether this agent has been quiet long enough that a tool call is worth a line. It only
// asks — the clock is reset by whatever line actually gets printed, so prose counts as life too. No
// lock: run drains the channel on one goroutine, which is the only caller.
func (pr *progress) due(agent string, at time.Time) bool {
	last, ok := pr.beats[agent]
	return !ok || at.Sub(last) >= ui.ProgressInterval
}

// beat records that this agent just printed something, whatever kind of line it was.
func (pr *progress) beat(agent string, at time.Time) {
	if pr.beats == nil {
		pr.beats = map[string]time.Time{}
	}
	pr.beats[agent] = at
}

// note remembers the newest tool call, which is what the next heartbeat reports.
func (pr *progress) note(agent, text string) {
	if text == "" {
		return
	}
	if pr.pending == nil {
		pr.pending = map[string]string{}
	}
	pr.pending[agent] = text
}

// takeNote hands over the remembered tool call and forgets it.
func (pr *progress) takeNote(agent string) string {
	text := pr.pending[agent]
	delete(pr.pending, agent)
	return text
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
	case pipeline.EventAgentProgress:
		// throttled rather than printed or dropped. This renderer has no status row, so every tool
		// call would repeat "Read" for pages and bury the reasoning — but dropping them all leaves an
		// agent that opens by reading twenty files looking dead, and here there is not even an elapsed
		// counter to say otherwise. One heartbeat per interval, and prose is never held back.
		pr.note(ev.Agent, ev.Text)
		if !pr.due(ev.Agent, ev.At) {
			return ""
		}
		what = pr.takeNote(ev.Agent)
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
		// dropped: the done event a moment later carries the same count, and printing both puts the
		// number on two consecutive lines
		return ""
	case pipeline.EventRateLimit:
		what = "rate limited: " + ev.Text
	default:
		return ""
	}
	if what == "" {
		return ""
	}

	// **any** line counts as life, not just a heartbeat: an agent narrating steadily is visibly alive
	// already, and charging it a tool-call line every interval buries what it is saying
	pr.beat(ev.Agent, ev.At)
	return pr.clip(ev.At.Format(progressTimeFormat) + " " + pr.prefix(ev.Agent) + what)
}

// clip cuts a line to the terminal. **This renderer has to do its own, because it is the other half of
// "display width belongs to the renderers".** The TUI clips through lipgloss against a width bubbletea
// reports; nothing reports one here, so without this the decoder's sanity bound — thousands of runes,
// not a column count — is what reaches stderr, and one narrated line wraps over half a screen.
//
// lipgloss does the measuring for the same reason it does in the TUI: the line carries the agent's
// color, and slicing runes would cut a sequence in half.
func (pr *progress) clip(line string) string {
	return lipgloss.NewStyle().MaxWidth(pr.cols()).Render(line)
}

// cols is the terminal width, COLUMNS first so a caller can pin it and a test can be deterministic.
// A width that cannot be detected is not an error: stderr is redirected as often as not, and a pipe
// has no width to ask for.
func (pr *progress) cols() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && n >= minCols {
		return n
	}
	if f, ok := pr.w.(*os.File); ok {
		if n, _, err := term.GetSize(int(f.Fd())); err == nil && n >= minCols {
			return n
		}
	}
	return fallbackCols
}

// prefix is the agent column: the name padded to the widest in the roster and colored, with the
// column itself doing the separating. An event naming no agent is indented to the same column.
//
// Padding is measured on the plain name and applied before painting, because a color sequence has no
// display width — pad afterwards and each line indents by however many bytes that agent's color takes.
func (pr *progress) prefix(agent string) string {
	if agent == "" {
		if pr.nameWidth() == 0 {
			return "" // no roster means no column to line up with, so nothing to indent past
		}
		return strings.Repeat(" ", pr.nameWidth()+2)
	}
	pad := strings.Repeat(" ", max(0, pr.nameWidth()-len([]rune(agent))))
	return pr.paint(agent) + pad + "  "
}

// nameWidth is the widest name in the roster, so every line's text starts at one column.
func (pr *progress) nameWidth() int {
	width := 0
	for _, spec := range pr.roster {
		width = max(width, len([]rune(spec.Name)))
	}
	return width
}

// paint colors the agent's name from its own roster entry. An agent the roster does not name — a
// stage, a verify group — takes its color from prompt.DerivedSpec, the same place the TUI takes it,
// so one agent is one color whichever renderer a reviewer is watching. Neither picks one itself.
func (pr *progress) paint(agent string) string {
	for _, spec := range pr.roster {
		if spec.Name == agent {
			return spec.Paint(agent)
		}
	}
	// a stage or verify group is not in the roster, and it gets its color from the same place the TUI
	// takes it, so one agent is one color whichever renderer a reviewer is watching
	return prompt.DerivedSpec(agent).Paint(agent)
}
