package frontmatter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name, in, meta, body string
		wantErr              bool
	}{
		{name: "no front matter", in: "just a body\n", body: "just a body\n"},
		{name: "meta and body", in: "---\na: 1\n---\nbody\n", meta: "a: 1\n", body: "body\n"},
		{name: "meta only", in: "---\na: 1\n---\n", meta: "a: 1\n"},
		{name: "meta only no trailing newline", in: "---\na: 1\n---", meta: "a: 1\n"},
		{name: "body has a rule", in: "---\na: 1\n---\nbody\n\n---\n\nmore\n", meta: "a: 1\n", body: "body\n\n---\n\nmore\n"},
		{name: "longer marker is not a terminator", in: "---\na: 1\n----\n", wantErr: true},
		// the scan steps past a `----` line and keeps looking, so a real terminator after one still closes
		{name: "longer marker then a real terminator", in: "---\na: 1\n----\nb: 2\n---\nbody\n",
			meta: "a: 1\n----\nb: 2\n", body: "body\n"},
		// an override may open and close the block without authoring any keys; the body must survive it
		{name: "empty block and body", in: "---\n---\nbody\n", body: "body\n"},
		{name: "empty block only", in: "---\n---\n"},
		{name: "empty block no trailing newline", in: "---\n---"},
		{name: "empty block then a rule", in: "---\n---\n\n---\n\nmore\n", body: "\n---\n\nmore\n"},
		{name: "unterminated", in: "---\na: 1\nbody\n", wantErr: true},
		{name: "empty", in: "", body: ""},
		// a file saved by a Windows editor: without CR stripping the whole file is body, and the failure
		// surfaces as "roster is empty" rather than anything about line endings
		{name: "crlf line endings", in: "---\r\na: 1\r\n---\r\nbody\r\n", meta: "a: 1\n", body: "body\n"},
		{name: "crlf meta only", in: "---\r\na: 1\r\n---\r\n", meta: "a: 1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, body, err := Split([]byte(tt.in))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.meta, string(meta))
			assert.Equal(t, tt.body, string(body))
		})
	}
}
