package main

import (
	"fmt"

	"github.com/umputun/revmux/app/archive"
)

// statsCmd is the `revmux stats` subcommand. It only records the selection: go-flags calls Execute from
// inside parseArgs, before the injected writers exist, so the corpus is read and reported by run.
type statsCmd struct {
	opts *options
}

// Execute records that the stats command was selected. Reading the corpus here would write through the
// real os.Stdout and leave nothing for a test to capture.
func (c *statsCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showStats = true
	return nil
}

// writeStats aggregates every round that ran under the tasks root and prints it as JSON. Like the catalog
// and the scaffolded round it is a carve-out in "stdout belongs to the report": no pipeline, archive or
// TUI exists yet, so there is nothing for it to collide with.
//
// The query is built from --tasks-dir and --task, the flags a review already carries. A second --task
// declared on the subcommand would put `revmux --task x stats` and `revmux stats --task x` in different
// fields, and the one a caller passed would be the one nothing read.
func (o runOpts) writeStats() error {
	corpus, err := archive.CollectStats(archive.StatsQuery{TasksDir: o.opts.TasksDir, Task: o.opts.Task})
	if err != nil {
		return fmt.Errorf("collect stats: %w", err)
	}
	return o.writeJSON(corpus, "stats")
}
