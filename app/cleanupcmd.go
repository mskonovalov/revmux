package main

import (
	"fmt"

	"github.com/umputun/revmux/app/archive"
)

// cleanupCmd is the `revmux cleanup` subcommand. Like every other one it records the selection only:
// go-flags calls Execute from inside parseArgs, and removing a task there would happen before the injected
// writers exist, with nothing able to capture what it reported.
type cleanupCmd struct {
	opts *options
}

// Execute records that the cleanup command was selected.
func (c *cleanupCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showCleanup = true
	return nil
}

// writeCleanup removes the task named by --task and prints what went as JSON. It is the one destructive
// path in revmux, reached only by a command whose entire purpose is stated in its name — nothing removes
// anything as a side effect of doing something else.
//
// The query is built from --tasks-dir and --task exactly as writeStats builds its own, so
// `revmux --task pr-1 cleanup` and `revmux cleanup --task pr-1` name the same task. A second --task
// declared on the subcommand would put them in different fields, and the one a caller passed would be the
// one nothing read — which here would remove nothing while reporting success.
func (o runOpts) writeCleanup() error {
	res, err := archive.Cleanup(archive.CleanupQuery{TasksDir: o.opts.TasksDir, Task: o.opts.Task})
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	return o.writeJSON(res, "cleanup")
}
