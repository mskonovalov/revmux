package prompt

import (
	"embed"
	"fmt"
	"io/fs"
)

// defaults is the bottom of the precedence chain. It is never materialized on disk: loading must not
// write to the user's config directory, and --dump-defaults is what extracts it on request.
//
//go:embed defaults
var defaults embed.FS

const defaultsRoot = "defaults"

// Defaults returns the embedded prompt tree rooted at the tree's own top level, so a caller walking
// it writes prompts/ and lenses/ rather than a defaults/ wrapper. --dump-defaults is its only caller.
func Defaults() fs.FS {
	sub, err := fs.Sub(defaults, defaultsRoot)
	if err != nil {
		panic(fmt.Sprintf("embedded defaults are unreadable: %v", err))
	}
	return sub
}
