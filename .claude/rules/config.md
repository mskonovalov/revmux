---
paths:
  - "app/config.go"
  - "app/main.go"
  - "app/introspect.go"
---

## Config and CLI

CLI parsing with `jessevdk/go-flags`; the config file is INI parsed through the same library's `IniParser`,
so every setting has exactly one definition — the struct tag.

Precedence: CLI flags > `./.revmux/config` > `~/.config/revmux/config` > embedded defaults.
`--config-dir` overrides the user-level location.

The project layer is **auto-detected**, not flag-driven: if `./.revmux/` exists it is used, and if it does
not the layer is simply absent. There is no `--project-config-dir`, because the whole point is that
checking `.revmux/` into a repo makes every invocation inside it use those settings.

`./` means the **process working directory**, not `--workdir`. Everything path-relative resolves the same
way — the project config, `--tasks-dir`'s `./.revmux/tasks` default — so a reviewer can predict where
revmux looks without tracking which flag a given path hangs off. `--workdir` sets where the *subprocesses*
run and what `{{WORKDIR}}` expands to; reviewing a repo from outside it means passing `--config-dir` and
`--tasks-dir` as well, since by the same rule both would otherwise resolve against the caller's cwd rather
than the repo under review.

**`--config-dir ./.revmux` collapses the two layers into one, and that must be detected.**
Otherwise the same directory loads twice as both the user and project layer — harmless for a scalar, but
it makes "which layer supplied this" wrong, which is exactly what `revmux config` reports.
Compare the two after resolving to absolute paths **and** evaluating symlinks: on macOS a temp dir under
`/var` is really `/private/var`, so a lexical comparison misses the collision in precisely the tests that
would catch it. When they are the same path, drop the project layer.

Layers merge **per field**, not whole-file: a project config setting one key must not discard the user
config's other keys.

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

The struct also carries `TaskDir`, the resolved root itself — `app/archive` needs it for both the run
directory and the prior-round history, and re-deriving it with `filepath.Dir(Scope)` elsewhere means two
resolutions that can disagree.

And it carries `WorkDir`, from `--workdir` and defaulting to the process working directory.
That one does not come from the task directory, but it belongs on the same struct: it is what `{{WORKDIR}}`
expands to and what `executor.Opts` sets as each subprocess's working directory, so the two must be the
same value. An agent told to review `{{WORKDIR}}` while its process runs somewhere else reads one tree and
reports on another. It is separate from `TaskDir` because `--tasks-dir` may point at `/tmp` while the code
under review is elsewhere — the review target and the context store are independent locations.

Return these as a struct, not as adjacent same-typed values.
`(scope string, goal string, profile string, err error)` is a transposition waiting to happen: swapping any two
compiles clean and silently feeds the project profile into `{{GOAL}}`.

### `--task` and `--run` are untrusted input

Both names come from the caller and are joined into filesystem paths, so they are validated before use,
not after. A name containing `..` or a path separator escapes the tasks root and lets revmux write over
caller-authored context — the exact thing "revmux writes only under `runs/`" forbids.

Reject any name that is empty, contains a path separator or `..`, is absolute, or begins with `.`.
Then verify containment on the resolved path rather than trusting the lexical check alone,
since a symlink inside the tasks root can still point outside it.

**Roster agent names get the same treatment, but at a different layer — do not conflate the two.**
An agent name comes from the roster and becomes one path *component*, so the empty / separator / `..` /
absolute / leading-`.` rules above apply to it, and they apply **before** any filename is built.
An agent called `events` would otherwise collide with `events.jsonl`, which is why per-agent streams live
in their own subdirectory as well.

`Archive.Writer` itself validates something else: it takes a clean **relative path** and rejects only what
resolves outside the run root after symlink evaluation. A separator is legal there and must be, since
`prompts/agents/`, `prompts/stages/`, `stages/` and `agents/` all need one.
Making `Writer` reject separators would make "`Writer` accepts `prompts/agents/x.md`" and "a separator in
a name is rejected" mutually unsatisfiable — two tests that both have to pass.

Any other caller-derived string that becomes a filename — a verify group's label, for instance — is
validated at the same layer as an agent name, before it reaches `Writer`.

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
- `2` — tool error: bad config, unreadable prompt tree, missing or empty `scope.md`, a `--run` name that
  already exists, an unwritable run artifact, or **every source degraded**

`1` is a normal outcome, not a failure. Callers script against this, so do not repurpose these values.

Two of those deserve spelling out, because the obvious implementation gets both wrong.

**Every source degraded is not a clean empty report.** The degrade policy continues past a dead agent, but a
run where nothing reported has no review in it, and returning `0` tells a scripted caller the code is fine.

**A failed archive write fails the run.** `CLAUDE.md` requires a run archive sufficient to audit the review
without re-running it, so a report emitted alongside a half-written archive is worse than no report: it looks
complete, and the gap only surfaces later when someone tries to audit it. Either every required artifact is
written or the run exits `2`.

### Config-management flags

- `--init` materializes `./.revmux/` with the config commented out, ready to customize.
- `--dump-defaults <dir>` extracts the embedded prompt tree for comparison or as a starting point for overrides.
- Neither ever overwrites a file the user has customized.

**Those two are the only things that write config, and a normal run writes none of it.**
Loading never installs defaults into `~/.config/revmux/` as a side effect: the embedded copy is already the
bottom of the precedence chain, so materializing it on disk buys nothing and turns a read-only invocation
into one that touches the user's home directory. A user who wants files to edit asks for them.

### `revmux config`

revmux is driven by a caller model that has to compose an invocation without reading the source, so the
resolved configuration is machine-readable output rather than something to be reconstructed from `--help`
and a directory listing. `revmux config` prints it as JSON on stdout and exits `0`.

It is a **subcommand**, not another meta flag, because that is what a caller types and what `--help` then
documents. Register it with `go-flags` and set `parser.SubcommandsOptional = true` so a bare
`revmux --task pr-123` keeps working with no command word.

**It reports what resolved, never what is embedded.** A user who overrode one lens and added another must
see his own tree, or the catalog describes a review that will not happen. For the same reason every runtime
knob is reported with the precedence layer that supplied it, not only its value: whether `--stagger-delay`
is a default or a deliberate choice changes whether a caller should pass it.

Values a caller has to match exactly — the `executor` and `effort` vocabularies — are read from the same
constants `validate` uses. A second hardcoded copy here means a new effort level ships working but
undiscoverable.

**This is the one carve-out in "stdout belongs to the report".** The command runs no pipeline, so there is
no report to collide with and no TUI to gate; it prints and exits before either exists.
