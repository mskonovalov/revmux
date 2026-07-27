package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

// report is what a finished run hands the browser: three findings, one per severity, out of order so
// the sort has something to do.
func report() finding.Report {
	return finding.Report{
		Findings: []finding.Finding{
			{ID: "f1", File: "app/pipeline/find.go", Line: 12, Severity: finding.Minor, Confidence: 60,
				Title: "stale comment", Body: "the comment names the old field"},
			{ID: "f2", File: "app/main.go", Line: 42, EndLine: 48, Severity: finding.Critical, Confidence: 95,
				Title: "unchecked error", Body: "the write error is dropped\nso a short write reads as success",
				Fix: "check it", Sources: []string{"bugs+impl", "codex"}, Lenses: []string{"bugs"},
				Verdict: finding.Confirmed},
			{ID: "f3", File: "app/ui/view.go", Severity: finding.Major, Confidence: 80, Title: "pane clipping"},
		},
		OpenQuestions: []finding.Finding{{Title: "is the retry budget right"}},
	}
}

// browsed is a model with the report already in it, sized so the findings pane shows exactly 5 lines.
func browsed(t *testing.T, rep finding.Report) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	// same five-line pane the scroll tests use, so the cursor-follow expectations stay in screenfuls
	m = feed(t, m, tea.WindowSizeMsg{Width: 100, Height: len(roster()) + chromeLines + 5},
		CompletedMsg{Report: rep})
	return m
}

func TestModel_complete(t *testing.T) {
	m := browsed(t, report())

	require.NotNil(t, m.findings)
	assert.Equal(t, 3, m.view.tab, "the browser opens one past the last agent")
	assert.Equal(t, m.findingsTab(), m.view.tab)
	assert.True(t, m.browsing())

	t.Run("worst findings first", func(t *testing.T) {
		vis := m.findings.visible()
		require.Len(t, vis, 3)
		assert.Equal(t, []string{"f2", "f3", "f1"}, []string{vis[0].ID, vis[1].ID, vis[2].ID})
	})

	t.Run("the agent tabs stay reachable", func(t *testing.T) {
		back := feed(t, m, press("2")) // tabs are labeled from one, so 2 is the first agent
		assert.Equal(t, 1, back.view.tab)
		assert.False(t, back.browsing())
		assert.Contains(t, back.tabBar(), "2 bugs+impl", "and so does the browser's own tab")
		assert.Contains(t, back.tabBar(), "f findings")
	})

	t.Run("there is no browser tab before the report arrives", func(t *testing.T) {
		early := New(ModelConfig{Roster: roster()})
		assert.Equal(t, -1, early.findingsTab())
		assert.NotContains(t, early.tabBar(), "findings")
		assert.False(t, early.browsing())
		assert.Equal(t, []string{"the review is still running..."}, early.findingsPane())
	})

	t.Run("f does nothing while there is nothing to browse", func(t *testing.T) {
		early := feed(t, New(ModelConfig{Roster: roster()}), press("f"))
		assert.Equal(t, 0, early.view.tab)
	})
}

func TestModel_findingsPane(t *testing.T) {
	pane := strings.Join(browsed(t, report()).findingsPane(), "\n")

	assert.Contains(t, pane, "── CRITICAL ──")
	assert.Contains(t, pane, "── MAJOR ──")
	assert.Contains(t, pane, "── MINOR ──")
	assert.Contains(t, pane, "> app/main.go:42-48  unchecked error  [95]", "the cursor starts on the worst finding")
	assert.Contains(t, pane, "  app/ui/view.go  pane clipping  [80]", "a file-level finding renders as the bare path")
	assert.Contains(t, pane, "    the write error is dropped", "a finding opens showing its body, not just its summary")
	assert.NotContains(t, pane, "is the retry budget right", "open questions are the report's, not the browser's")

	t.Run("an unrecognized severity is grouped rather than dropped", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: "invented", Title: "odd"},
			{File: "b.go", Line: 2, Severity: finding.Critical, Title: "bad"},
		}}
		lines := browsed(t, rep).findingsPane()
		assert.Equal(t, []string{"── CRITICAL ──", "> b.go:2  bad  [0]", "── INVENTED ──", "  a.go:1  odd  [0]"}, lines)
	})

	t.Run("a finding with no severity at all still has a heading", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{File: "a.go", Line: 1, Title: "unranked"}}}
		assert.Contains(t, browsed(t, rep).findingsPane()[0], "UNSPECIFIED")
	})

	t.Run("an empty report says so", func(t *testing.T) {
		assert.Equal(t, []string{"no findings."}, browsed(t, finding.Report{}).findingsPane())
	})
}

func TestFindingsState_move(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"j moves down", []string{"j"}, 1},
		{"and down arrow does the same", []string{"down", "down"}, 2},
		{"k comes back", []string{"j", "j", "k"}, 1},
		{"it stops at the last finding", []string{"j", "j", "j", "j"}, 2},
		{"and at the first", []string{"k"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := browsed(t, report())
			for _, k := range tt.keys {
				m = feed(t, m, press(k))
			}
			assert.Equal(t, tt.want, m.findings.cursor)
			assert.Contains(t, strings.Join(m.findingsPane(), "\n"), "> "+m.findings.visible()[tt.want].Location())
		})
	}

	t.Run("an empty browser has nowhere to move", func(t *testing.T) {
		m := feed(t, browsed(t, finding.Report{}), press("j"), press("k"))
		assert.Equal(t, 0, m.findings.cursor)
	})

	t.Run("outside the browser the same keys still scroll", func(t *testing.T) {
		m := feed(t, filled(t, 20), CompletedMsg{Report: report()}, press("2"), press("k"))
		assert.Equal(t, 1, m.view.scroll, "a pane is scrolled, not a cursor")
		assert.Equal(t, 0, m.findings.cursor)
	})
}

func TestFindingsState_toggle(t *testing.T) {
	// every case builds its own model: Model copies share one findingsState pointer, so a subtest
	// reusing another's would inherit its cursor and its folds
	folded := func(t *testing.T) Model {
		t.Helper()
		return feed(t, browsed(t, report()), press("enter"))
	}

	open := strings.Join(browsed(t, report()).findingsPane(), "\n")
	assert.Contains(t, open, "    the write error is dropped", "the body is indented under its row")
	assert.Contains(t, open, "    so a short write reads as success", "and keeps its own line breaks")
	assert.Contains(t, open, "    fix: check it")
	assert.Contains(t, open, "    sources: bugs+impl, codex | lenses: bugs | verdict: confirmed")

	assert.NotContains(t, strings.Join(folded(t).findingsPane(), "\n"), "the write error is dropped",
		"enter folds the finding under the cursor down to its summary")

	t.Run("enter again opens it back up", func(t *testing.T) {
		back := feed(t, folded(t), press("enter"))
		assert.Contains(t, strings.Join(back.findingsPane(), "\n"), "the write error is dropped")
	})

	t.Run("the fold follows the finding, not the row it sat on", func(t *testing.T) {
		filtered := feed(t, folded(t), press("/"), press("s"), press("t"), press("a"), press("enter"))
		require.Len(t, filtered.findings.matches, 1, "only the stale comment matches")
		assert.Contains(t, strings.Join(filtered.findingsPane(), "\n"), "the comment names the old field",
			"the folded critical finding is filtered out, and its row does not fold a stranger")
	})

	t.Run("a row with nothing to show is one line either way", func(t *testing.T) {
		terse := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: finding.Minor, Title: "terse"}}}
		want := []string{"── MINOR ──", "> a.go:1  terse  [0]"}
		assert.Equal(t, want, browsed(t, terse).findingsPane())
		assert.Equal(t, want, feed(t, browsed(t, terse), press("enter")).findingsPane())
	})

	t.Run("an empty browser tolerates the key", func(t *testing.T) {
		empty := feed(t, browsed(t, finding.Report{}), press("enter"))
		assert.Equal(t, []string{"no findings."}, empty.findingsPane())
	})
}

func TestFindingsState_filter(t *testing.T) {
	// a fresh model per case: Model copies share one findingsState pointer, so a query typed in one
	// subtest would still be there in the next
	querying := func(t *testing.T) Model {
		t.Helper()
		return feed(t, browsed(t, report()), press("/"), press("v"), press("i"), press("e"))
	}

	open := feed(t, browsed(t, report()), press("/"))
	require.True(t, open.findings.typing)
	assert.Contains(t, open.findingsPane()[0], "filter: _", "the caret says the query is open")

	m := querying(t)
	require.Len(t, m.findings.matches, 1)
	assert.Equal(t, "pane clipping", m.findings.visible()[0].Title, "the path matches, not only the title")

	t.Run("enter accepts the query and hands the keys back", func(t *testing.T) {
		done := feed(t, querying(t), press("enter"))
		assert.False(t, done.findings.typing)
		assert.Equal(t, "vie", done.findings.query)
		assert.Contains(t, done.findingsPane()[0], "filter: vie (1 of 3)")
	})

	t.Run("esc abandons it and everything is listed again", func(t *testing.T) {
		back := feed(t, querying(t), press("esc"))
		assert.False(t, back.findings.typing)
		assert.Empty(t, back.findings.query)
		assert.Len(t, back.findings.matches, 3)
		assert.NotContains(t, back.findingsPane()[0], "filter:")
	})

	t.Run("backspace takes the last rune back", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("ü"), press("x"), press("backspace"), press("backspace"))
		assert.Empty(t, typed.findings.query, "a multi-byte rune goes back whole")
		assert.Len(t, typed.findings.matches, 3)
	})

	t.Run("backspace on an empty query is not an underflow", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("backspace"))
		assert.Empty(t, typed.findings.query)
	})

	t.Run("a space is text, not a lost keystroke", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("stale"), press("space"))
		assert.Equal(t, "stale ", typed.findings.query)
	})

	t.Run("keys that act on the browser are text while the query is open", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("j"), press("q"))
		assert.Equal(t, "jq", typed.findings.query)
		assert.Equal(t, 0, typed.findings.cursor, "j typed a letter rather than moving the cursor")
	})

	t.Run("ctrl+c is never text: a half-typed query must not be a trap", func(t *testing.T) {
		typing := feed(t, browsed(t, report()), press("/"))
		_, cmd := typing.Update(press("ctrl+c"))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	})

	t.Run("a query nothing matches says so rather than rendering blank", func(t *testing.T) {
		none := feed(t, browsed(t, report()), press("/"), press("zzz"), press("enter"))
		assert.Empty(t, none.findings.matches)
		assert.Equal(t, `nothing matches "zzz".`, none.findingsPane()[1])
	})

	t.Run("the filter is case-insensitive", func(t *testing.T) {
		up := feed(t, browsed(t, report()), press("/"), press("UNCHECKED"), press("enter"))
		require.Len(t, up.findings.matches, 1)
		assert.Equal(t, "unchecked error", up.findings.visible()[0].Title)
	})

	t.Run("a filter key outside the browser is not swallowed", func(t *testing.T) {
		agent := feed(t, browsed(t, report()), press("1"), press("/"))
		assert.False(t, agent.findings.typing)
	})
}

func TestModel_showCursor(t *testing.T) {
	// twelve findings in a pane showing five: the cursor has to drag the window with it
	many := finding.Report{}
	for i := range 12 {
		many.Findings = append(many.Findings, finding.Finding{
			File: "app/f" + string(rune('a'+i)) + ".go", Line: i + 1, Severity: finding.Major, Title: "issue"})
	}

	require.Equal(t, 5, browsed(t, many).paneHeight())
	require.Positive(t, browsed(t, many).maxScroll(), "the list is longer than the pane")

	t.Run("walking down keeps the cursor in the window", func(t *testing.T) {
		down := browsed(t, many)
		for range 11 {
			down = feed(t, down, press("j"))
		}
		assert.Equal(t, 0, down.view.scroll, "the last finding sits at the bottom of the log")
		assert.Contains(t, strings.Join(down.detailPane(), "\n"), "> app/fl.go:12")
	})

	t.Run("and walking back up brings it back into view", func(t *testing.T) {
		up := browsed(t, many)
		for range 11 {
			up = feed(t, up, press("j"))
		}
		for range 11 {
			up = feed(t, up, press("k"))
		}
		assert.Equal(t, up.maxScroll()-1, up.view.scroll, "the window stops with the first finding on its top line")
		assert.Contains(t, strings.Join(up.detailPane(), "\n"), "> app/fa.go:1")
	})

	t.Run("expanding a row scrolls its detail into view", func(t *testing.T) {
		long := finding.Report{Findings: []finding.Finding{{File: "a.go", Line: 1, Severity: finding.Critical,
			Title: "long", Body: strings.Repeat("a line\n", 10)}}}
		exp := feed(t, browsed(t, long), press("enter"))
		assert.Contains(t, strings.Join(exp.detailPane(), "\n"), "> a.go:1", "the row stays visible above its body")
	})

	t.Run("a list that fits needs no scrolling at all", func(t *testing.T) {
		short := feed(t, browsed(t, report()), press("j"), press("j"))
		assert.Equal(t, 0, short.view.scroll)
	})
}
