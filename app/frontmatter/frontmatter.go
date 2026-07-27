// Package frontmatter splits a leading `---` delimited YAML block from the body of a markdown file.
//
// It is what app/prompt reads for every prompt file kind and app/task for task.md, and it handles two
// edge cases the caller never sees: an empty block, and a CRLF file whose opening delimiter would
// otherwise read as `---\r`.
package frontmatter

import (
	"bytes"
	"errors"
)

// Split separates a leading `---` delimited YAML block from the body. A file with no front matter is
// all body, which is what makes every key of every file kind optional.
//
// CR is stripped first so a file saved with CRLF line endings parses. Without that the opening
// delimiter reads as `---\r`, the whole file becomes body, and the failure surfaces far from its cause —
// as "roster is empty" for a prompt file, or as a task whose anchors match nothing.
func Split(in []byte) (meta, body []byte, err error) {
	const marker = "---"
	b := bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(b, []byte(marker+"\n")) {
		return nil, b, nil
	}

	rest := b[len(marker)+1:]
	// an empty block closes on the very first line, where the scan below has nothing to find: it looks for
	// the newline preceding a closing delimiter, and the opening one already consumed it. Without this an
	// override carrying `---\n---\n` fails as unterminated and loses its whole body.
	if bytes.HasPrefix(rest, []byte(marker)) && (len(rest) == len(marker) || rest[len(marker)] == '\n') {
		return nil, bytes.TrimPrefix(rest[len(marker):], []byte("\n")), nil
	}
	for off := 0; off < len(rest); {
		i := bytes.Index(rest[off:], []byte("\n"+marker))
		if i < 0 {
			break
		}
		at := off + i
		after := at + 1 + len(marker)
		if after == len(rest) || rest[after] == '\n' {
			return rest[:at+1], bytes.TrimPrefix(rest[after:], []byte("\n")), nil
		}
		off = at + 1
	}
	return nil, nil, errors.New("front matter is opened but never closed")
}
