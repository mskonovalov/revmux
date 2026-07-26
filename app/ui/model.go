package ui

import (
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

const (
	// scrollbackLimit bounds one agent's retained lines. A long review emits thousands of them and a
	// pane only ever shows a screenful.
	scrollbackLimit = 2000

	// timeFormat keeps a log line short: a run is minutes long, so the date adds nothing.
	timeFormat = "15:04:05"

	// sizes used until the first tea.WindowSizeMsg arrives, and whenever the terminal reports none.
	defaultCols = 100
	defaultRows = 30
)

// agent states as the status table shows them.
const (
	stateWaiting  = "waiting"
	stateRunning  = "running"
	stateRetrying = "retrying"
	stateLimited  = "limited"
	stateDone     = "done"
	stateDegraded = "degraded"
)

// ModelConfig is what package main hands the model.
//
// Roster is the same resolved slice the plain renderer takes, and it is where agent colors come from:
// pipeline.Event names the agent and nothing else, so a color picked here would exist in one renderer
// only and the two would disagree about the same agent.
type ModelConfig struct {
	Roster []prompt.AgentSpec
	Events <-chan pipeline.Event
}

// Model is the bubbletea model: a status table over one focused detail pane.
//
// Receivers are mixed on purpose. tea.Model requires value receivers on Init, Update and View, and
// Update returns the mutated model rather than mutating in place, so those and their render helpers
// take values while the internal mutators Update calls take pointers. The state sub-structs have no
// such constraint and are pointer-only, since a value receiver there would copy scrollback on every
// render.
type Model struct {
	cfg      ModelConfig
	view     viewState
	agents   []*agentState
	combined *combinedState
	findings *findingsState
	stage    string
	found    int
}

// viewState is where the reader is looking: the focused tab, the scroll offset within it and the
// terminal size.
type viewState struct {
	tab    int
	scroll int
	cols   int
	rows   int
}

// agentState is one agent's status row plus its full scrollback, thinking included.
type agentState struct {
	spec    prompt.AgentSpec
	state   string
	started time.Time
	updated time.Time
	last    string
	lines   []string
}

// eventsDone says the pipeline closed its channel, so the run is over and no further events arrive.
type eventsDone struct{}

// New builds the model over the resolved roster, one row per agent in roster order.
func New(cfg ModelConfig) Model {
	m := Model{cfg: cfg, combined: &combinedState{}, view: viewState{cols: defaultCols, rows: defaultRows}}
	m.agents = make([]*agentState, 0, len(cfg.Roster))
	for _, spec := range cfg.Roster {
		m.agents = append(m.agents, &agentState{spec: spec, state: stateWaiting})
	}
	return m
}

// Init starts watching the event channel.
func (m Model) Init() tea.Cmd { return m.next() }

// Update folds one message in and asks for the next event. Reading the channel happens inside a
// command, off the update loop, so a slow render never stalls the pipeline.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.view.cols, m.view.rows = msg.Width, msg.Height
		m.view.scroll = min(m.view.scroll, m.maxScroll())
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	case pipeline.Event:
		m.apply(msg)
		return m, m.next()
	case eventsDone:
		return m, nil
	case CompletedMsg:
		m.complete(msg.Report)
		return m, nil
	}
	return m, nil
}

// complete opens the findings browser over the finished report. The run being over is not a reason
// to quit: package main writes the report once the reader is done with the terminal, and the agent
// tabs stay reachable so he can check why a finding was raised.
func (m *Model) complete(rep finding.Report) {
	m.findings = newFindings(rep)
	m.focus(m.findingsTab())
}

// findingsTab is the browser's tab, one past the last agent, or -1 while there is no report to
// browse. Nothing ever focuses -1, so the pane router cannot reach the browser early.
func (m Model) findingsTab() int {
	if m.findings == nil {
		return -1
	}
	return len(m.agents) + 1
}

// next reads one event in a command goroutine and delivers it as a message.
func (m Model) next() tea.Cmd {
	events := m.cfg.Events
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return eventsDone{}
		}
		return ev
	}
}

// apply folds one event into the model. An event naming an agent the roster does not carry opens a
// row for it rather than panicking, and one arriving after that agent finished is just another line:
// ordering across concurrent agents is not guaranteed.
func (m *Model) apply(ev pipeline.Event) {
	if ev.Kind == pipeline.EventStage {
		m.stage = ev.Stage
		m.combined.push(combinedEntry{at: ev.At, text: "stage " + ev.Stage})
		return
	}

	a := m.agent(ev.Agent)
	if a == nil {
		return
	}
	text := a.track(ev)
	if ev.Kind == pipeline.EventFindings {
		m.count(ev)
	}
	if text == "" {
		return
	}
	a.push(ev.At.Format(timeFormat) + " " + text)
	if ev.Kind == pipeline.EventAgentActivity && text == thinkingActivity {
		return
	}
	m.combined.push(combinedEntry{agent: a.spec.Name, at: ev.At, text: text})
}

// agent returns the named agent's state, opening a row for one the roster never mentioned.
func (m *Model) agent(name string) *agentState {
	if name == "" {
		return nil
	}
	if a := m.find(name); a != nil {
		return a
	}
	a := &agentState{spec: prompt.AgentSpec{Name: name}, state: stateRunning}
	m.agents = append(m.agents, a)
	return a
}

// count folds a findings event into the header total. A roster agent adds to it, a stage process
// replaces it: synthesis merges what the finders already reported, so summing the two counts every
// finding twice and shows a number that is neither the raw total nor the merged one.
func (m *Model) count(ev pipeline.Event) {
	if m.rostered(ev.Agent) {
		m.found += len(ev.Findings)
		return
	}
	m.found = len(ev.Findings)
}

// rostered reports whether the name is a roster entry rather than a stage process. The roster is the
// authority, not the status rows, which grow to cover both.
func (m Model) rostered(name string) bool {
	for _, spec := range m.cfg.Roster {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// find looks a rostered agent up without creating one, so rendering never grows the table.
func (m Model) find(name string) *agentState {
	for _, a := range m.agents {
		if a.spec.Name == name {
			return a
		}
	}
	return nil
}

// track folds the event into the agent's row and returns the line for the log panes. An unrecognized
// kind returns empty and is dropped: a blank row would read as an agent that went quiet.
func (a *agentState) track(ev pipeline.Event) string {
	if a.started.IsZero() {
		a.started = ev.At
	}
	if ev.At.After(a.updated) {
		a.updated = ev.At
	}

	switch ev.Kind {
	case pipeline.EventAgentStarted:
		a.state, a.started = stateRunning, ev.At
		a.last = "started"
		if ev.Text != "" {
			a.last += " [" + ev.Text + "]"
		}
	case pipeline.EventAgentActivity, pipeline.EventAgentState:
		a.state, a.last = stateRunning, ev.Text
	case pipeline.EventAgentDone:
		a.state, a.last = stateDone, "done"
		if ev.Text != "" {
			a.last += ", " + ev.Text
		}
	case pipeline.EventAgentRetried:
		a.state, a.last = stateRetrying, "retrying: "+ev.Text
	case pipeline.EventAgentDegraded:
		a.state, a.last = stateDegraded, "degraded: "+ev.Text
	case pipeline.EventFindings:
		a.last = strconv.Itoa(len(ev.Findings)) + " findings emitted"
	case pipeline.EventRateLimit:
		a.state, a.last = stateLimited, "rate limited: "+ev.Text
	default:
		return ""
	}
	return a.last
}

// push appends one scrollback line, dropping the oldest once the pane's budget is spent.
func (a *agentState) push(line string) {
	a.lines = append(a.lines, line)
	if len(a.lines) > scrollbackLimit {
		a.lines = a.lines[len(a.lines)-scrollbackLimit:]
	}
}

// runtime is the agent's elapsed time as the status table shows it, measured between event timestamps
// rather than off a clock so the table renders the same in a test as in a terminal.
func (a *agentState) runtime() string {
	if a.started.IsZero() || !a.updated.After(a.started) {
		return "-"
	}
	d := a.updated.Sub(a.started).Round(time.Second)
	if d == 0 {
		return "-"
	}
	return d.String()
}

func (v viewState) width() int {
	if v.cols <= 0 {
		return defaultCols
	}
	return v.cols
}

func (v viewState) height() int {
	if v.rows <= 0 {
		return defaultRows
	}
	return v.rows
}
