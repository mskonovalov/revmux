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
	// the report's own shape: the title is the heading and the location sits under it, as "### title"
	// followed by its `file:line` does on stdout
	assert.Contains(t, pane, "  unchecked error  [95]", "the worst finding is first")
	assert.Contains(t, pane, "    app/main.go:42-48", "with where it is on the line under it")
	assert.Contains(t, pane, "  pane clipping  [80]")
	assert.Contains(t, pane, "    app/ui/view.go", "a file-level finding renders as the bare path")
	assert.Contains(t, pane, "    the write error is dropped", "a finding opens showing its body, not just its summary")
	assert.NotContains(t, pane, "is the retry budget right", "open questions are the report's, not the browser's")

	t.Run("an unrecognized severity is grouped rather than dropped", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: "invented", Title: "odd"},
			{File: "b.go", Line: 2, Severity: finding.Critical, Title: "bad"},
		}}
		lines := browsed(t, rep).findingsPane()
		assert.Equal(t, []string{
			"── CRITICAL ──", "  bad  [0]", "    b.go:2", "",
			"── INVENTED ──", "  odd  [0]", "    a.go:1", "",
		}, lines)
	})

	t.Run("a finding with no severity at all still has a heading", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{File: "a.go", Line: 1, Title: "unranked"}}}
		assert.Contains(t, browsed(t, rep).findingsPane()[0], "UNSPECIFIED")
	})

	t.Run("an empty report says so", func(t *testing.T) {
		assert.Equal(t, []string{"no findings."}, browsed(t, finding.Report{}).findingsPane())
	})
}

func TestFindingsState_scroll(t *testing.T) {
	// the browser has no cursor: it renders the report and the pane's own scrolling reads it, which is
	// the same scrolling every other pane has
	t.Run("the arrow and vi keys scroll it like any other pane", func(t *testing.T) {
		m := browsed(t, report())
		require.Positive(t, m.maxScroll(), "the report is longer than the pane, or there is nothing to test")

		// it opens at the top, so forward is the only way to go and j is what goes there
		down := feed(t, m, press("j"))
		assert.Equal(t, m.view.scroll-1, down.view.scroll, "j reads forward through the report")

		back := feed(t, down, press("k"))
		assert.Equal(t, m.view.scroll, back.view.scroll, "and k comes back")
	})

	t.Run("it opens on the worst finding rather than at the end of the last one", func(t *testing.T) {
		m := browsed(t, report())
		assert.Equal(t, m.maxScroll(), m.view.scroll, "a report is read from its top, unlike a live log")
		assert.Contains(t, strings.Join(m.detailPane(), "\n"), "CRITICAL",
			"so the first thing on screen is the worst finding")
	})

	t.Run("outside the browser the same keys still scroll their own pane", func(t *testing.T) {
		m := feed(t, filled(t, 20), CompletedMsg{Report: report()}, press("2"), press("k"))
		assert.Equal(t, 1, m.view.scroll, "the agent pane scrolled, not the report")
	})
}

func TestFindingsState_rendersTheWholeReport(t *testing.T) {
	// the browser shows the report, not an index of it: every part the markdown on stdout carries is on
	// screen without a keystroke. Folding was tried and removed — it put the review behind a keypress
	// per finding and needed state kept in step with the pane.
	pane := strings.Join(browsed(t, report()).findingsPane(), "\n")

	assert.Contains(t, pane, "unchecked error", "the title, as the report's ### heading")
	assert.Contains(t, pane, "    app/main.go:42-48", "where it is, on the line under it")
	assert.Contains(t, pane, "    the write error is dropped", "the body, indented under that")
	assert.Contains(t, pane, "    so a short write reads as success", "keeping its own line breaks")
	assert.Contains(t, pane, "    fix: check it")
	assert.Contains(t, pane, "    sources: bugs+impl, codex | lenses: bugs | verdict: confirmed")

	t.Run("enter is not a fold and does nothing here", func(t *testing.T) {
		after := feed(t, browsed(t, report()), press("enter"))
		assert.Equal(t, browsed(t, report()).findingsPane(), after.findingsPane())
	})

	t.Run("a finding with no body still shows where it is", func(t *testing.T) {
		terse := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: finding.Minor, Title: "terse"}}}
		assert.Equal(t, []string{"── MINOR ──", "  terse  [0]", "    a.go:1", ""},
			browsed(t, terse).findingsPane())
	})

	t.Run("an empty report says so", func(t *testing.T) {
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
		assert.Equal(t, typed.maxScroll(), typed.view.scroll,
			"j went into the query rather than scrolling, so the narrowed report is still at its top")
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

func TestModel_findingsScrollWindow(t *testing.T) {
	// twelve findings in a five-line pane: the report is far longer than the window, and reading it is
	// the pane's own scrolling rather than anything the browser tracks
	many := finding.Report{}
	for i := range 12 {
		many.Findings = append(many.Findings, finding.Finding{
			File: "app/f" + string(rune('a'+i)) + ".go", Line: i + 1, Severity: finding.Major,
			Title: "issue " + string(rune('a'+i))})
	}

	m := browsed(t, many)
	require.Equal(t, 5, m.paneHeight())
	require.Positive(t, m.maxScroll(), "the report is longer than the pane")
	assert.Contains(t, strings.Join(m.detailPane(), "\n"), "issue a", "it opens on the first finding")

	t.Run("reading forward reaches the last one", func(t *testing.T) {
		down := m
		for down.view.scroll > 0 {
			down = feed(t, down, press("j"))
		}
		assert.Contains(t, strings.Join(down.detailPane(), "\n"), "issue l")
	})

	t.Run("and reading back returns to the first", func(t *testing.T) {
		up := m
		for range 40 {
			up = feed(t, up, press("k"))
		}
		assert.Equal(t, up.maxScroll(), up.view.scroll, "it stops at the top rather than running past it")
		assert.Contains(t, strings.Join(up.detailPane(), "\n"), "issue a")
	})
}
