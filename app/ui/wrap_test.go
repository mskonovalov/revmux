package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrap(t *testing.T) {
	t.Run("text that fits stays one row", func(t *testing.T) {
		assert.Equal(t, []string{"head reading proc.go"}, Wrap("head ", "reading proc.go", 80))
	})

	t.Run("a long entry continues under the text column", func(t *testing.T) {
		out := Wrap("12:00:00 ", "the quick brown fox jumps over the lazy dog and keeps going", 30)
		require.Greater(t, len(out), 1)
		assert.True(t, strings.HasPrefix(out[0], "12:00:00 "), "the first row carries the head")
		assert.True(t, strings.HasPrefix(out[1], strings.Repeat(" ", 9)),
			"and the rest are indented to its width, not to zero")
		for _, r := range out {
			assert.LessOrEqual(t, lipgloss.Width(r), 30)
		}
		joined := strings.Join(strings.Fields(strings.Join(out, " ")), " ")
		assert.Contains(t, joined, "keeps going", "nothing is lost off the end")
	})

	t.Run("a column too narrow to wrap into is left alone", func(t *testing.T) {
		out := Wrap(strings.Repeat("x", 70), "some text", 80)
		assert.Len(t, out, 1, "below the floor a wrapped entry is more indent than text")
	})
}

func TestWrap_ansiAndRunes(t *testing.T) {
	// **the paths that were broken.** markdown() runs before Wrap now, so the text carries escape
	// sequences; and prose is arbitrary, so it carries multi-byte and double-width runes. Trimming a
	// byte at a time while measuring display cells exits inside a rune or inside an escape.
	t.Run("a cut never lands mid-rune", func(t *testing.T) {
		for _, text := range []string{
			strings.Repeat("ä", 200),
			strings.Repeat("日本語のテキスト ", 30),
			strings.Repeat("🙂", 100),
		} {
			out := Wrap("", text, 31)
			require.Greater(t, len(out), 1, "the input must be long enough to wrap")
			for _, r := range out {
				assert.True(t, utf8.ValidString(r), "a row must stay valid UTF-8, got %q", r)
				assert.LessOrEqual(t, lipgloss.Width(r), 31, "and must fit the column")
			}
			assert.Equal(t, strings.ReplaceAll(text, " ", ""),
				strings.ReplaceAll(strings.Join(out, ""), " ", ""), "every rune survives")
		}
	})

	t.Run("a cut never lands inside an escape sequence", func(t *testing.T) {
		text := markdown("checking `app/executor/rollout.go` and **the watchdog** and rather more besides")
		out := Wrap("12:00:00 ", text, 40)
		require.Greater(t, len(out), 1)
		// a row may legitimately open a span and leave it open across the break — the span continues on
		// the next row and closes there. What it must never do is end inside the escape itself.
		partial := regexp.MustCompile(`\x1b\[?[0-9;]*$`)
		for _, r := range out {
			assert.True(t, utf8.ValidString(r))
			assert.LessOrEqual(t, lipgloss.Width(r), 40, "escapes have no width and must not be counted")
			assert.NotRegexp(t, partial, r, "a row must not end part-way through a sequence, got %q", r)
		}
		joined := strings.Join(strings.Fields(strings.Join(out, " ")), " ")
		assert.Contains(t, joined, "rather more besides", "and nothing is lost off the end")
	})

	t.Run("a rune wider than the column still makes progress", func(t *testing.T) {
		// takeCols directly: Wrap's floor short-circuits before a column this narrow is reached, and
		// what must not happen here is a cut that can never fit looping forever
		assert.Equal(t, "🙂", takeCols(strings.Repeat("🙂", 4), 1), "it overflows by a cell rather than hanging")
	})
}

func TestWrap_alwaysFitsTheWidth(t *testing.T) {
	// **every row must fit whoever called.** The TUI's panes run their rows through clipAll afterwards
	// and would hide a miss here; the plain renderer writes them straight to stderr and would not.
	long := "checking whether the stagger gate can still be opened by something other than model output"
	for _, width := range []int{200, 80, 40, 25, 19, 10, 3, 1} {
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			for _, head := range []string{"", "12:00:00 ", strings.Repeat("x", 30)} {
				for _, row := range Wrap(head, long, width) {
					assert.LessOrEqual(t, lipgloss.Width(row), width,
						"head %q at %d columns produced %q", head, width, row)
				}
			}
		})
	}

	t.Run("below the wrap floor it clips rather than passing the row through", func(t *testing.T) {
		// this is the branch the plain renderer used to clip itself, before that clip was deleted
		out := Wrap(strings.Repeat("x", 70), long, 80)
		require.Len(t, out, 1, "too narrow to wrap into")
		assert.LessOrEqual(t, lipgloss.Width(out[0]), 80, "but still bounded")
	})
}
