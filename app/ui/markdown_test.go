package ui

import (
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
