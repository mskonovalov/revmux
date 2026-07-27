package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

var at = time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)

// roster is the two-agent roster most cases render, one ANSI-named color and one hex.
func roster() []prompt.AgentSpec {
	return []prompt.AgentSpec{
		{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Color: "6"},
		{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex", Color: "#ff8800"},
	}
}

// feed drives Update the way bubbletea would and hands back the mutated model.
func feed(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		updated, ok := next.(Model)
		require.True(t, ok, "Update must return the same model type")
		m = updated
	}
	return m
}

// event builds one agent event at the shared timestamp.
func event(kind pipeline.EventKind, agent, text string) pipeline.Event {
	return pipeline.Event{Kind: kind, Agent: agent, Text: text, At: at}
}

func TestNew(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	require.Len(t, m.agents, 2)
	assert.Equal(t, "bugs+impl", m.agents[0].spec.Name, "rows follow roster order")
	assert.Equal(t, "codex", m.agents[1].spec.Name)
	assert.Equal(t, stateWaiting, m.agents[0].state)
	assert.Equal(t, 0, m.view.tab, "the combined view is focused by default")
	assert.NotNil(t, m.combined)

	t.Run("an empty roster still renders", func(t *testing.T) {
		empty := New(ModelConfig{})
		assert.Empty(t, empty.agents)
		assert.Contains(t, empty.View(), "0 agents")
	})
}

func TestModel_apply(t *testing.T) {
	tests := []struct {
		name      string
		ev        pipeline.Event
		wantState string
		wantLast  string
	}{
		{"started", event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"), stateRunning, "started [bugs, impl]"},
		{"started with no lenses", event(pipeline.EventAgentStarted, "bugs+impl", ""), stateRunning, "started"},
		{"activity", event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"), stateRunning, "tool: Read"},
		{"state", event(pipeline.EventAgentState, "bugs+impl", "exit 0"), stateRunning, "exit 0"},
		{"done", event(pipeline.EventAgentDone, "bugs+impl", "3 findings"), stateDone, "done, 3 findings"},
		{"done with no detail", event(pipeline.EventAgentDone, "bugs+impl", ""), stateDone, "done"},
		{"retried", event(pipeline.EventAgentRetried, "bugs+impl", "idle timeout"), stateRetrying, "retrying: idle timeout"},
		{"degraded", event(pipeline.EventAgentDegraded, "bugs+impl", "stalled twice"), stateDegraded, "degraded: stalled twice"},
		{"rate limited", event(pipeline.EventRateLimit, "bugs+impl", "throttled"), stateLimited, "rate limited: throttled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: roster()}), tt.ev)
			assert.Equal(t, tt.wantState, m.agents[0].state)
			assert.Equal(t, tt.wantLast, m.agents[0].last)
			assert.Equal(t, []string{"16:02:11 " + tt.wantLast}, m.agents[0].lines)
			require.Len(t, m.combined.entries, 1)
			assert.Equal(t, combinedEntry{agent: "bugs+impl", text: tt.wantLast, at: at}, m.combined.entries[0])
		})
	}

	t.Run("findings count up without changing the state", func(t *testing.T) {
		ev := pipeline.Event{Kind: pipeline.EventFindings, Agent: "codex", At: at,
			Findings: []finding.Finding{{Title: "one"}, {Title: "two"}}}
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentStarted, "codex", ""), ev)

		assert.Equal(t, 2, m.found)
		assert.Equal(t, stateRunning, m.agents[1].state, "emitting findings is not finishing")
		assert.Equal(t, "2 findings emitted", m.agents[1].last)
	})

	t.Run("a stage process replaces the count rather than adding to it", func(t *testing.T) {
		found := func(agent string, n int) pipeline.Event {
			list := make([]finding.Finding, n)
			return pipeline.Event{Kind: pipeline.EventFindings, Agent: agent, At: at, Findings: list}
		}
		m := feed(t, New(ModelConfig{Roster: roster()}),
			found("bugs+impl", 5), found("codex", 4), found("synthesis", 3))

		assert.Equal(t, 3, m.found, "synthesis merged what the finders reported, so its count supersedes theirs")
		assert.Contains(t, m.header(), "3 findings")
	})

	t.Run("a stage event names the stage and carries no agent", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}),
			pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at})

		assert.Equal(t, "find", m.stage)
		require.Len(t, m.combined.entries, 1)
		assert.Equal(t, combinedEntry{text: "stage find", at: at}, m.combined.entries[0])
		assert.Empty(t, m.agents[0].lines, "a stage change belongs to no agent")
	})

	t.Run("an unknown kind is dropped rather than rendered blank", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventKind("invented"), "bugs+impl", "x"))
		assert.Empty(t, m.combined.entries)
		assert.Empty(t, m.agents[0].lines)
		assert.Equal(t, stateWaiting, m.agents[0].state)
	})

	t.Run("an event with no agent is dropped", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentActivity, "", "orphan"))
		assert.Len(t, m.agents, 2, "no row is opened for an unnamed agent")
		assert.Empty(t, m.combined.entries)
	})
}

func TestModel_apply_unknownAgent(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentActivity, "stranger", "tool: Read"))

	require.Len(t, m.agents, 3, "an unrostered agent gets its own row rather than a panic")
	assert.Equal(t, "stranger", m.agents[2].spec.Name)
	assert.Equal(t, stateRunning, m.agents[2].state)

	var row string
	for l := range strings.SplitSeq(m.statusTable(), "\n") {
		if strings.Contains(l, "stranger") {
			row = l
		}
	}
	require.NotEmpty(t, row, "the unrostered agent still gets a status row")

	// a stage or verify group is unrostered but still worth telling apart in the combined log, so it
	// gets a color from prompt.DerivedSpec. This package must not pick one itself — the plain renderer
	// would then paint the same agent differently — so the assertion is that the row carries exactly
	// what prompt derived, never merely that it is colored
	// the name is padded before it is painted, so the row does not hold Paint("stranger") verbatim —
	// what it must hold is the sequence prompt resolved, applied to the name
	open, _, _ := strings.Cut(prompt.DerivedSpec("stranger").Paint("stranger"), "stranger")
	require.NotEmpty(t, open, "prompt must resolve a color for a derived agent")
	assert.Contains(t, row, open+"stranger",
		"the color comes from the spec, and both renderers take it from there")
	// padded out to the widest name like any other, so one unrostered agent does not break the column
	assert.Equal(t, "16:02:11 "+prompt.DerivedSpec("stranger").Paint("stranger")+"   tool: Read",
		m.combinedLines()[0])
}

func TestModel_apply_afterDone(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		event(pipeline.EventAgentDone, "bugs+impl", "3 findings"),
		event(pipeline.EventAgentActivity, "bugs+impl", "a late line"),
	)

	assert.Equal(t, "a late line", m.agents[0].last, "ordering across concurrent agents is not guaranteed")
	assert.Len(t, m.agents[0].lines, 2)
	assert.Len(t, m.agents, 2, "a late event does not open a second row")
}

func TestModel_Update(t *testing.T) {
	t.Run("a window size resizes the panes", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 40, Height: 12})
		assert.Equal(t, 40, m.view.width())
		assert.Equal(t, 12, m.view.height())
	})

	t.Run("a resize clamps a scroll offset the shorter pane can no longer reach", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		for i := range 40 {
			m = feed(t, m, event(pipeline.EventAgentActivity, "bugs+impl", "line "+strings.Repeat("x", i%3)))
		}
		m = feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
		require.Positive(t, m.view.scroll)

		m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 200})
		assert.Equal(t, 0, m.view.scroll, "everything fits now, so there is nothing to scroll back to")
	})

	t.Run("an event asks for the next one", func(t *testing.T) {
		events := make(chan pipeline.Event, 1)
		m := New(ModelConfig{Roster: roster(), Events: events})
		_, cmd := m.Update(event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"))
		assert.NotNil(t, cmd, "the model keeps watching the channel")
	})

	t.Run("a closed channel does not quit, the report is still coming", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update(eventsDone{})
		assert.Nil(t, cmd, "quitting here would drop the findings browser before it opened")
		assert.Nil(t, next.(Model).findings)
	})

	t.Run("an unrelated message changes nothing", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update("something else")
		assert.Nil(t, cmd)
		assert.Equal(t, m.View(), next.(Model).View())
	})
}

func TestModel_Init(t *testing.T) {
	t.Run("delivers events until the channel closes", func(t *testing.T) {
		events := make(chan pipeline.Event, 1)
		events <- event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read")
		m := New(ModelConfig{Roster: roster(), Events: events})

		cmd := m.Init()
		require.NotNil(t, cmd)
		assert.Equal(t, event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"), cmd())

		close(events)
		assert.Equal(t, eventsDone{}, cmd())
	})

	t.Run("no channel means nothing to wait for", func(t *testing.T) {
		assert.Nil(t, New(ModelConfig{Roster: roster()}).Init())
	})
}

func TestAgentState_push(t *testing.T) {
	a := &agentState{}
	for i := range scrollbackLimit + 10 {
		a.push("line " + strings.Repeat("x", i%2))
	}

	assert.Len(t, a.lines, scrollbackLimit, "the oldest lines are dropped, the pane stays bounded")
}

func TestAgentState_runtime(t *testing.T) {
	tests := []struct {
		name             string
		started, updated time.Time
		want             string
	}{
		{"never started", time.Time{}, time.Time{}, "-"},
		{"nothing since it started", at, at, "-"},
		{"sub-second rounds away", at, at.Add(400 * time.Millisecond), "-"},
		{"seconds", at, at.Add(12 * time.Second), "12s"},
		{"minutes", at, at.Add(62 * time.Second), "1m2s"},
		{"an out-of-order timestamp is not negative time", at, at.Add(-time.Minute), "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &agentState{started: tt.started, updated: tt.updated}
			assert.Equal(t, tt.want, a.runtime())
		})
	}
}

func TestModel_autoExit(t *testing.T) {
	t.Run("without the option the browser waits for a keypress", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update(CompletedMsg{Report: report()})
		m = next.(Model)
		assert.True(t, m.done)
		assert.Zero(t, m.exitIn, "nothing is counting down")
		assert.Nil(t, cmd, "and no tick was scheduled")
	})

	t.Run("the countdown runs down a second per tick and then quits", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster(), AutoExit: 3 * time.Second})
		next, cmd := m.Update(CompletedMsg{Report: report()})
		m = next.(Model)
		require.Equal(t, 3*time.Second, m.exitIn)
		require.NotNil(t, cmd, "completion schedules the first tick")

		for want := 2 * time.Second; want > 0; want -= time.Second {
			next, cmd = m.Update(exitTick{})
			m = next.(Model)
			require.Equal(t, want, m.exitIn)
			require.NotNil(t, cmd, "each tick schedules the next one")
		}

		next, cmd = m.Update(exitTick{})
		m = next.(Model)
		assert.LessOrEqual(t, m.exitIn, time.Duration(0))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd(), "the last tick quits")
	})

	t.Run("a keypress cancels it, and a tick already in flight does not quit", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster(), AutoExit: time.Second}), CompletedMsg{Report: report()})
		m = feed(t, m, press("j"))
		assert.Zero(t, m.exitIn, "the reader is reading")

		_, cmd := m.Update(exitTick{})
		assert.Nil(t, cmd, "the stale tick neither quits nor reschedules")
	})
}

func TestModel_closing(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	assert.Empty(t, m.closing(), "a running review says nothing about closing")

	t.Run("a finished review says it is finished and how to leave", func(t *testing.T) {
		done := feed(t, m, CompletedMsg{Report: report()})
		assert.Contains(t, done.View(), "complete")
		assert.Contains(t, done.View(), "q to quit")
	})

	t.Run("a counting-down one shows the seconds left and how to stop it", func(t *testing.T) {
		auto := feed(t, New(ModelConfig{Roster: roster(), AutoExit: 5 * time.Second}), CompletedMsg{Report: report()})
		assert.Contains(t, auto.View(), "closing in 5s")
		assert.Contains(t, auto.View(), "any key to stay")
	})
}
