package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain prose is untouched", "the retry path drops the error", "the retry path drops the error"},
		{"a code span", "read `app/ui/view.go` first", "read \x1b[36mapp/ui/view.go\x1b[39m first"},
		{"emphasis", "this is **not** reachable", "this is \x1b[1mnot\x1b[22m reachable"},
		{"both in one line", "**bug**: `Prune` deletes it", "\x1b[1mbug\x1b[22m: \x1b[36mPrune\x1b[39m deletes it"},
		{"two spans of one kind", "`a` and `b`", "\x1b[36ma\x1b[39m and \x1b[36mb\x1b[39m"},
		{"an unpaired backtick stays literal", "a ` b", "a ` b"},
		{"a lone asterisk stays literal", "a * b", "a * b"},
		{"an empty span is not one", "``", "``"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, markdown(tc.in))
		})
	}

	t.Run("neither sequence ends in a full reset", func(t *testing.T) {
		// a reset would clear the enclosing style of the pane line clip() renders around it
		assert.NotContains(t, markdown("**a** `b`"), "\x1b[0m")
	})
}

func TestHeading(t *testing.T) {
	// literal, because this is the definition of the rendered form: bold plus accent, hashes kept
	// since the pane is showing a markdown document and a reader with the report open beside it
	// should see the same thing
	assert.Equal(t, "\x1b[1m\x1b[36m## Major\x1b[39m\x1b[22m", heading(2, "Major"))
	assert.Equal(t, "\x1b[1m\x1b[36m### a title\x1b[39m\x1b[22m", heading(3, "a title"))

	t.Run("a span inside a heading does not turn the heading off", func(t *testing.T) {
		// **the defect this pins.** A heading is bold and accented; a code span closes with "default
		// foreground" and emphasis with "bold off" — the same two attributes. Without re-opening, the
		// heading ended at the first span and the rest of the line rendered plain.
		got := heading(3, "fix **the** retry budget")
		assert.True(t, strings.HasSuffix(got, "retry budget"+ansiHeadOff),
			"the tail after the span is still inside the heading, got %q", got)
		assert.Contains(t, got, ansiBoldOff+ansiHeadOn, "the heading is re-opened where the span closed")
	})

	t.Run("a code span inside a heading behaves the same", func(t *testing.T) {
		got := heading(3, "`Prune` deletes it")
		assert.True(t, strings.HasSuffix(got, "deletes it"+ansiHeadOff), "got %q", got)
		assert.Contains(t, got, ansiCodeOff+ansiHeadOn)
	})

	t.Run("markdown outside a heading closes to the default, with nothing to re-open", func(t *testing.T) {
		assert.Equal(t, ansiBoldOn+"a"+ansiBoldOff, markdown("**a**"))
	})
}
