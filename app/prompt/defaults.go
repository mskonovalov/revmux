package prompt

import "embed"

// defaults is the bottom of the precedence chain. It is never materialized on disk: loading must not
// write to the user's config directory, and --dump-defaults is what extracts it on request.
//
//go:embed defaults
var defaults embed.FS

const defaultsRoot = "defaults"
