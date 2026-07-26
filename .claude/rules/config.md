---
paths:
  - "app/config.go"
  - "app/main.go"
---

## Config and CLI

CLI parsing with `jessevdk/go-flags`; the config file is INI parsed through the same library's `IniParser`,
so every setting has exactly one definition — the struct tag.

Precedence: CLI flags > `./.revmux/config` > `~/.config/revmux/config` > embedded defaults.
`--config-dir` overrides the user-level location.

### What belongs in the config file

Runtime knobs only: `idle_timeout`, `hard_timeout`, `stagger_delay`, `max_parallel`, `tasks_dir`, `keep_runs`,
verifier group cap, default profile.

`tasks_dir` is a location, not review content, so it belongs here — a user who wants task directories on `/tmp`
sets it once rather than passing it on every invocation.
`keep_runs` prunes `runs/` subdirectories **within** a task directory and never the task directory itself,
because everything above `runs/` was written by the caller.

Everything that shapes a review — rosters, models, effort, prompt text, lenses — lives in markdown.
See `.claude/rules/prompts.md`. Do not add a roster or a model to the INI file.

- `no-ini:"true"` on meta flags that make no sense in a config file (`--init`, `--dump-defaults`, `--version`, `--config-dir`).
- `ini-name` tags so config keys match the long flag names; a user reading `--help` should be able to guess the key.
- Distinguish "explicitly false" from "not set" with a sentinel when a bool needs a per-field merge across layers.
  Without it, a project config can never turn off something the user config turned on.

### Flag description style

Minimal and atomic. State what the flag does, nothing more.

Never write "at startup", "on startup", "(mirrors the X toggle)", or a cross-reference to a runtime key binding
in a struct tag, the README table, or godoc.
The description says what the flag does; runtime toggles are discovered from the key bindings, not from flag help.

In documentation use the `--flag=value` form for long flags that take a value, not `--flag value`.

### Context resolution

revmux never derives context — the caller writes it to disk and names it.
`--task <id>` selects a directory under `--tasks-dir` (default `./.revmux/tasks`), and that directory is the
only channel review context travels through.
There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags:
variables resolve to **paths**, so a flag carrying inline text could not be substituted without revmux
first writing it to a file, which would make revmux an author of context rather than a consumer of it.

`options.resolveContext` stats the task directory and returns the resolved absolute paths:

1. `scope.md` — required, and a missing or empty one is a load-time error, since a review with no scope is a caller bug
2. `goal.md`, `profile.md` — optional, absent resolves to the "none provided" placeholder
3. `context/` — optional directory the caller fills with as many files as it likes

Absent `goal.md` or `profile.md` is **not an error**.
The run proceeds and the report header states that severity calibration is generic,
so a weakly-calibrated review can never masquerade as a strong one.

Return these as a struct, not as adjacent same-typed values.
`(scope string, goal string, profile string, err error)` is a transposition waiting to happen: swapping any two
compiles clean and silently feeds the project profile into `{{GOAL}}`.

`--tasks-dir` and `--config-dir` are different roots and must not be conflated:
the first holds per-review context and run artifacts, the second holds config and the prompt tree.

### Mode gating at the composition root

When a flag is meaningful in some modes and not others, resolve it to a concrete value in `package main`
through a method on `options`, and pass the resolved value down.
Downstream code takes the resolved value and must not re-derive it from the raw options.

### Filtering happens once

`--min-confidence` filters the report in `package main` before rendering, and the rendered report and the
exit code are both computed from that filtered set.
A rendering path that ignores the threshold while the exit code honors it produces a report listing
findings the exit code claims are absent.

### Exit codes

- `0` — no findings above the threshold
- `1` — findings above the threshold
- `2` — tool error (bad config, unreadable prompt tree, every source degraded)

`1` is a normal outcome, not a failure. Callers script against this, so do not repurpose these values.

### Config-management flags

- `--init` materializes `./.revmux/` with the config commented out, ready to customize.
- `--dump-defaults <dir>` extracts the embedded prompt tree for comparison or as a starting point for overrides.
- Neither ever overwrites a file the user has customized.
