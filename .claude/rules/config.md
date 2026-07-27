---
paths:
  - "app/config.go"
  - "app/main.go"
  - "app/introspect.go"
  - "app/archive/**"
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

Runtime knobs only: `idle-timeout`, `hard-timeout`, `stagger-delay`, `max-parallel`, `verify-groups`,
`tasks-dir`, `keep-runs`, `auto-exit`, `profile`.
The key is the long flag name verbatim, hyphens included — that is what `ini-name` is set to, and it is what
makes the key guessable from `--help`.

`tasks-dir` is a location, not review content, so it belongs here — a user who wants task directories on `/tmp`
sets it once rather than passing it on every invocation.
`keep-runs` prunes `runs/` subdirectories **within** a task directory and never the task directory itself,
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
The run proceeds, and the variable resolves to a "none provided" placeholder the shipped profile bodies
instruct the agent to read as generic severity calibration.
That guarantee lives in the prompt text, not in Go — the report header carries the title, the scope path
and the degraded banner, and says nothing about calibration.

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

**`archive.New` re-establishes that containment structurally, and it starts at the tasks root.**
`options.taskDir` validates the resolved task path, but what it returns is a path *string*, and reopening
that by name is the check-then-open window this section exists to remove — one hop further up than `runs/`.
`archive.Opts` therefore carries `TasksDir` and `Task` rather than a joined `TaskDir`, and `New` opens an
`os.Root` on the tasks root and takes the task directory, then `runs/`, then the run directory as nested
roots. An escaping symlink fails at open rather than passing a comparison, whenever it was planted.
`runs/` is the one directory revmux owns, and a checked-in `runs` symlink would make this run's artifacts
land somewhere else and `Prune` delete directories there — the one destructive primitive in the tool.

**Both roots above `runs/` are opened, never created.**
The tasks root and the task directory are the caller's, and a missing task directory is his error rather
than something for revmux to author — `options.resolveContext` already refuses it, since a task with no
`scope.md` cannot be reviewed. `New` creating one behind that check would make the first directory revmux
writes sit above `runs/`, which is the line the whole rule draws. `runs/` and the run directory are the
only two `New` may create.

Anchoring at the tasks root is stricter than `filepath.EvalSymlinks` in one case, and deliberately so:
`os.Root` resolves a symlink target *within* the root, so a task symlink with an **absolute** target is
refused even when that target sits inside the tasks root. A relative one landing inside still resolves and
works. Do not relax this back into a resolve-then-reopen — the exotic case is a task directory linked to a
sibling task, and the window it would reopen is the one the roots exist to close.

**Containment is necessary there but not sufficient, so `runs/` must additionally not be a symlink at all.**
`os.Root` follows a link that lands back *inside* the root, so `runs -> context` satisfies every containment
check and still hands `Prune` the caller's own directories to delete — the rule this whole section exists to
enforce, broken without leaving the task directory. `New` therefore `Lstat`s `runs` through the task handle
and refuses a symlink outright, before creating or opening anything.
The run directory underneath is created rather than adopted — `Mkdir` fails on an existing name, so a
symlink planted there beforehand is rejected as an already-used run name.

**Reading an entry and opening it are two operations, so the look is repeated against the open handle,
and both hops below the task directory get it.**
A symlink planted between them is followed by `os.Root` whenever it lands back inside the parent —
the `runs -> context` case the `Lstat` exists to refuse, reached by racing it rather than by defeating it.
The run directory has the same window on the other side of its `Mkdir`: renamed away and replaced with a
link to an earlier round, the handle would pin that round and every artifact this run writes would truncate
one of its artifacts — destroying exactly the bad round a reflection agent wants to read.
`New` therefore re-reads each entry after `OpenRoot`, refuses a symlink, and matches the entry against the
directory actually opened with `os.SameFile`. That is one `checkHandle`, used at both hops.
A swap after that point changes nothing: the handle stays on the directory the check accepted.

**Those handles are then held for the whole run, and that is the point.**
A path checked once and reopened by name on every write is a path another process can rename and replace
with a symlink in between; a handle keeps referring to the directory it was opened on and refuses any name
that leaves it, so containment holds for the run's lifetime rather than for the instant it was measured.
`Archive.Close` releases both, deferred in `run` once every artifact is on disk.
The tasks-root and task handles are closed inside `New`, since nothing is written through them — they only
carry the walk down to `runs/`.
Do not reintroduce a path-string `resolve` plus `filepath.EvalSymlinks` here — that is the check-then-open
window the roots exist to remove, and it needs `/var` versus `/private/var` reasoning the handles do not.

`options.taskDir` still runs, and still returns the joined path: `reviewContext.TaskDir` is what the prompt
variables and the prior-round inventory read. What it must not be is the thing the archive opens — that
takes `--tasks-dir` and `--task` and joins them itself, so the boundary is enforced by the open rather than
inherited from a string.

**Roster agent names get the same treatment, but at a different layer — do not conflate the two.**
An agent name comes from the roster and becomes one path *component*, so the empty / separator / `..` /
absolute / leading-`.` rules above apply to it, and they apply **before** any filename is built.
An agent called `events` would otherwise collide with `events.jsonl`, which is why per-agent streams live
in their own subdirectory as well.

That check is `prompt.AgentSpec.checkName`, run at **load**, not in the archive. Load is the earliest point
still ahead of any filename, it is where every other roster-entry rule already lives, and it covers the
`--lenses` override for free because that path validates through the same method.
An invalid agent name is therefore a startup error, like every other bad front-matter value.

`Archive.Writer` itself validates something else: it takes a clean **relative path** and rejects only what
was never an artifact path — empty, absolute, or climbing out lexically. Anything that leaves the run root
by following a symlink is refused by the run's own handle when it is traversed, so `Writer` does no
symlink resolution of its own. A separator is legal there and must be, since
`prompts/agents/`, `prompts/stages/`, `stages/` and `agents/` all need one.
Making `Writer` reject separators would make "`Writer` accepts `prompts/agents/x.md`" and "a separator in
a name is rejected" mutually unsatisfiable — two tests that both have to pass.

A **derived** string that becomes a filename is sanitized rather than rejected, and a verify group's label
is the one case of it. Its parts are directory names taken from the findings, so there is no author to send
an error back to and refusing one would fail a stage over the shape of a path under review. Everything
outside `[a-zA-Z0-9_.-]` collapses to a dash, leading and trailing `-` and `.` are trimmed, and an empty
result becomes `root` — so the label is safe by construction, never by validation.
Reject caller-**authored** names; sanitize revmux-**derived** ones. Do not swap the two.

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

**The findings browser is one of those rendering paths — the filter goes before `renderer.finish`, not
after it.** `runOpts.review` applies it, because `finish` is where the report crosses into `app/ui` and
anything applied in `run` afterwards arrives too late. Filtering there instead puts the TUI and stdout in
open disagreement about the same run, which is precisely what this section exists to forbid.

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

**A per-agent tee under `agents/` is the one artifact that carve-out does not cover.** It is opened, written
and closed by one agent's own goroutine, so a failure there is attributable to exactly one source, and
`finder.attempt` reports it through `sourceResult` like any other agent fault: retry once, then degrade.
That keeps a full roster's completed work rather than discarding it over one unwritable file, and the loss
is still loud — the banner names the source and `degraded` carries it.
It does **not** route through `Pipeline.fail`, and widening it to do so would break the tested degrade path.
Every whole-file artifact — manifest, composed prompts, stage snapshots, `events.jsonl`, report — does.

**A failed `Prune` is not one of those either, and must not be.** It runs after the report has been written, and an
old run directory that will not delete is housekeeping rather than a missing artifact of *this* run. It warns
on stderr and leaves the review's own exit code alone; turning a finished review into exit `2` over it would
tell a scripted caller the review failed.
For the same reason `Prune` runs last: a failed run keeps its artifacts.

`keep-runs` counts the run being written, so `10` leaves ten directories including the current one, and `0`
or `1` leaves only the current one. The run being written is never a deletion candidate whatever the number
says.

**Which entry that is comes from the run handle, never from the run name**, for the same reason every
other check down there does.
A rename between `New` and `Prune` leaves a name match excluding nothing, and this run's own archive then
becomes an ordinary candidate — at `keep-runs` 0 or 1 the only one, deleted by the pass that was meant to
make room for it.
`os.SameFile` against `root.Stat(".")` settles it instead.

**Enumerating by identity settles only half of it, because what deletes a candidate is its name.**
A deletion takes a name and there is no unlink-by-handle to take instead, so a rename landing between the
enumeration and the deletion carries the deletion to whatever now answers to that name — the run being
written among them, which is the one directory the identity check exists to spare.
Re-reading the entry and matching it against the `fs.FileInfo` it was enumerated as narrows that window but
does not close it: `Lstat` and the deletion are still two syscalls, and a rename between them is not
detectable from a name.

**So the guarantee comes from what is left for the name to do, not from how close the check sits to it.**
`Archive.clear` opens the candidate as an `os.Root` — after the same `Lstat` identity check, and matched
again against `Stat(".")` so a redirect during the open is caught — and deletes everything under it through
that handle, depth first. A handle keeps referring to the directory it was opened on however the name moves
afterwards, so nothing the recursion touches can be redirected.
Entries come off `ReadDir` as they are on disk, so a symlink is unlinked rather than followed and nothing
outside the candidate is reachable.

What the name is then left to do is a single **non-recursive** `Remove` of an empty directory, still gated
on the identity match. An entry that changed hands in that last window is at worst an empty directory, and a
live run directory refuses the unlink outright because it is not empty.
Do not reduce the candidate back to a bare name, do not hoist the match up to the enumeration, and above all
do not put `RemoveAll` back on the name — the whole point is that the recursion follows a handle and the name
carries only an unlink that cannot take a populated tree with it.

**None of this covers two revmux processes pruning one task at the same time**, and it is not meant to.
`Prune` excludes the run *this* process is writing; another process's live run is an ordinary candidate,
reachable only at `keep-runs` 0 or 1 since anything above that keeps the newest entries and a live run is
the newest thing in `runs/`.
The task directory is single-writer by design — the caller re-runs one task under successive `--run` names —
and closing this properly needs cross-process locking, which buys nothing against the symlink planting the
rest of this section is about.

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
