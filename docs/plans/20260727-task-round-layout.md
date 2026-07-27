# Task/round layout — move caller context into the round

## Overview

Review context is per-round but stored per-task. That is the bug.

A task accumulates rounds: round 1 reviews a commit, round 2 reviews the fixes for what round 1 found.
Those are different scopes. Today both read one `scope.md` at task level, so composing round 2 overwrites
round 1's scope — and with it the record of what round 1 actually reviewed.

Hit live during this session: round 1 of the `skill-ship` task ran against commit `4ed3259`; composing
round 2 overwrote `scope.md`. It was recoverable only because one agent happened to echo the file into
its stream tee — `codex.log` had nothing, and archived prompts carry the *path*, not the text.

This contradicts a documented hard rule: *a run archive must be sufficient to audit the review that
produced it, without re-running anything*. The archive cannot satisfy that while the scope the agents
were pointed at lives outside it and is mutable.

All four caller inputs drift with what is under review:

- `scope.md` — what this round reviews
- `goal.md` — round 2 also asks "did the fixes land?"
- `profile.md` — gains a "this change is shell and markdown, not Go" section when the subject changes
- `context/` — a per-round commit list

So the split is not immutable-vs-mutable: **all caller context belongs to the round.**

Benefits: each round becomes self-describing and auditable; a later reflection agent can read any round
in isolation; the caller stops constructing paths, so the layout becomes revmux's private detail.

### ⚠️ OPEN DECISION — pruning now deletes caller-authored context

Moving `input/` into the round makes it a prune candidate. `--keep-runs` defaults to `10`, so the 11th
round of a task would silently delete round 1's `scope.md` — **the exact artifact whose loss motivated
this plan**.

This inverts a CLAUDE.md hard rule verbatim: *"revmux writes only under `runs/`. Everything above it
belongs to the caller and is never modified or pruned"*, and contradicts README's *"Pruning only ever
reads `runs/`, so `scope.md`… are never touched"*.

Three ways to resolve it, to be decided before Task 3:

- **A. Prune the whole round.** Simple and consistent — the round is gone, its inputs go with it. Caller
  work becomes prunable, which it never was.
- **B. Prune artifacts, keep `input/`.** The round directory survives holding only its inputs. Preserves
  the caller's authored context forever; leaves skeleton directories that are no longer runs and would
  need excluding from the prior-round inventory.
- **C. Default `--keep-runs` to unlimited.** Nothing is deleted unless asked. Sidesteps the conflict at
  the cost of unbounded growth, and `keep-runs` still deletes caller input when set.

Whichever is chosen, Task 9 must rewrite the CLAUDE.md hard rule and the README line rather than leaving
them contradicting the code.

## Context (from discovery)

Files involved:

- `app/archive/archive.go` — `New`, `Prune`, `remove`, `clear`, `checkHandle`, `checkRunsEntry` (:383).
  Nested `os.Root` handles, symlink refusal, identity-based pruning. The security-critical part.
- `app/config.go` — `resolveContext` (:311), `taskDir` (:345), `runName` (:288), `checkNames` (:441),
  `checkName` (:451), layout constants (:42-46), `showConfig` (:82), `initConfig` (:468)
- `app/introspect.go` — `configCmd` (:18), `Execute` (:77), the model for a second subcommand
- `app/main.go` — `writeCatalog` (:112), `runName` call site (:132), `History` call site (:156)
- `app/artifacts.go` — `manifestFileName` (:19)
- `app/archive/history.go` — prior-round inventory (:33)
- `.claude/rules/config.md` — "Context resolution", "`--task` and `--run` are untrusted input", pruning
- `app/prompt/prompt.go:267` — `splitFrontMatter`, the existing front-matter helper

Patterns observed:

- front matter is parsed with `yaml` + `dec.KnownFields(true)`, so unknown keys are load-time errors
- subcommands register through go-flags with `SubcommandsOptional = true` (config.go:108)
- **`configCmd.Execute` does not print.** go-flags calls it from inside `parseArgs`, before the injected
  writers exist, so it only sets `opts.showConfig` and `runOpts.writeCatalog` does the writing. Any new
  subcommand follows the same shape or it writes to the real `os.Stdout` and cannot be tested.
- `initConfig` (config.go:468) is the precedent for writing a template without overwriting a custom file
- every filesystem test uses `t.TempDir()`; archive tests delete, so a test pointed at the real tasks
  root would destroy review history that cannot be regenerated

Architectural guidance (go-architect):

1. "Every directory at task level is a round" is a *convention*, not a structure — a caller adding
   `notes/` or `.git` makes it a recursive-delete candidate. Replace the positional guarantee with an
   **ownership** one: a prune candidate must contain `manifest.json`, an artifact revmux writes.
2. `Mkdir` conflated collision detection and symlink refusal; split them. Lstat through the task handle
   refusing a symlink → `OpenRoot` → `checkHandle` → require `input/` → collision via
   `OpenFile("manifest.json", O_CREATE|O_EXCL)`, which is the new atomic "already ran" test.
3. `task.md` belongs in a new `app/task` package. `app/prompt` owns front matter *for prompts*;
   importing it for a non-prompt widens its contract.
4. `revmux new` is sound as a subcommand that exits before any pipeline, under two constraints:
   `resolveContext` must never gain a create-on-missing fallback, and `new` must refuse to overwrite.

## Development Approach

- **testing approach**: TDD for `app/archive` (the destructive path — the guarantees are the deliverable,
  so the tests are written first and must fail before the change). Regular elsewhere.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change

## Code-Quality Rules (HARD — verify against every task before marking complete)

These rules supplement project CLAUDE.md and are NOT optional. They are the gate for marking any task complete. If a rule is violated, the task is not done — refactor, re-test, then mark complete.

**Signatures (hard limits):**
- No function or method has 4+ parameters. `ctx context.Context` does not count toward the budget. If you need 4+, use an option struct (e.g., `type fooOpts struct { ... }`).
- No function or method has 4+ return values. Split the function into two single-purpose ones, or return a struct.
- Multiple adjacent same-type parameters (`oldLine, newLine int`) are a swap hazard — review whether they belong on a struct.

**Methods vs standalone helpers (project rule, hard):**
- If a function is called only from methods of a single struct, it MUST be a method on that struct. Calling pattern decides, not field access.
- Standalone helpers are reserved for: (a) constructors and entry points (`Parse...`, `New...`, `Decorate...`), (b) utilities shared by multiple unrelated types or by both standalone functions AND methods, (c) tiny cross-cutting helpers.
- Before adding any standalone helper, mentally walk its callers. If every caller is a method of one type, make the helper a method on that type.

**Visibility (private by default, hard):**
- Lowercase identifiers by default. Only export when an out-of-package caller exists.
- Exception (per CLAUDE.md): methods called by other structs in the same package CAN be exported for inter-component API clarity. This is the only exception. It does not extend to types, functions, constants, or variables.
- Before exporting any new identifier, grep for cross-package callers. If none, lowercase it.

**Comments (default: none, hard):**
- Default to writing no comments. Add one only when the WHY is non-obvious (a hidden invariant, a workaround, behavior that would surprise a reader).
- Exported items get godoc comments starting with the name. Unexported items get lowercase non-godoc comments — or no comment at all.
- Never describe WHAT the code does when the code itself is self-evident. Never write multi-paragraph comments on routine helpers.

**Per-task gate (before marking ANY checkbox complete):**
1. Formatter runs clean (`~/.claude/format.sh` or `gofmt -s -w` + `goimports -w`).
2. `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` reports zero issues.
3. `go test ./... -race` passes.
4. Scan the new code for the four rule classes above. Specifically:
   - Grep new function signatures: `grep -nE '^func.*\(.*,.*,.*,.*\)' app/<path>/*.go` — any hit with 4+ comma-separated params (excluding `ctx`) is a violation. Same for the return-value side.
   - For every new standalone helper, `grep -rn 'helperName(' --include='*.go'` and confirm at least one caller is NOT a method of a single type. If all callers are methods of one type, convert.
   - For every new exported identifier, grep cross-package. If no out-of-package hit, lowercase it.
5. Only after 1–4 pass: mark the task complete.

If a previous task shipped a violation (spotted later by user, reviewer, or yourself): fix it in the next commit BEFORE starting the next task. Do not let violations accumulate.

## Testing Strategy

- **unit tests**: required for every task
- **TDD for `app/archive`**: the symlink, collision and pruning tests are written first and must fail
  before the implementation lands. These guarantees are the deliverable, not a side effect of it.
- no e2e/UI suite in this project; the end-to-end check is a real review run (Post-Completion)
- every filesystem test uses `t.TempDir()` — archive tests delete, so a test pointed at the real tasks
  root would destroy review history that cannot be regenerated
- the pruning tests carry the most weight: `runs/` gave a structural guarantee being replaced by an
  ownership check, so "a directory without `manifest.json` is never pruned" is what proves the
  replacement holds
- one regression test has a **silent** failure mode and must exist: if `reviewContext.TaskDir` becomes
  the round directory, `History` finds no rounds, returns `""`, and the prior-round block is omitted
  with no error — breaking a hard rule invisibly

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## Solution Overview

```
<tasks-root>/<task-id>/
├── task.md                  front matter + prose; identifies the task for matching
├── 01-initial/
│   ├── input/               CALLER-written
│   │   ├── scope.md         required
│   │   ├── goal.md          optional
│   │   ├── profile.md       optional
│   │   └── context/         optional
│   ├── prompts/{agents,stages}/
│   ├── stages/  agents/  events.jsonl
│   └── manifest.json  report.md  findings.json
└── 02-after-fix/
```

Key decisions:

**Caller context moves into the round.** Each round becomes self-describing, which is what the archive
rule already demanded.

**`runs/` is removed.** It existed only to separate revmux-owned from caller-owned inside a task. With
every caller file inside a round, that boundary moved down a level.

**Pruning switches from a positional guarantee to an ownership one.** A candidate must contain
`manifest.json`. A caller's stray directory at task level has none and is never touched — independent of
caller discipline, which the positional rule was not.

**Collision detection becomes an exclusive create.** `OpenFile("manifest.json", O_CREATE|O_EXCL)` is
atomic, replaces the atomicity lost with `Mkdir`, and writes the same marker pruning keys on.

**`task.md` carries a formal anchor.** `url`/`branch`/`base` let a caller match an existing task exactly
instead of guessing at an id (`pr123` beside `pr-123` silently forks the history). revmux stores and
reports these and never resolves them — no git, no fetch. The zero-VCS-dependency rule is unchanged.

**`revmux new` returns paths so the caller never constructs one.** The layout becomes revmux's private
detail rather than a structure every caller reimplements from a skill doc, and a later layout change
stops being a breaking change for callers.

## Technical Details

### New package `app/task`

Owns the task-directory layout, `task.md`, name validation and scaffolding. Layout constants live here
so `app/archive` and `package main` share one definition instead of three.

```go
// MetaFile and the names below are the task-directory layout that app/archive and package main
// both join paths from.
const (
    MetaFile     = "task.md"
    InputDir     = "input"
    ScopeFile    = "scope.md"
    GoalFile     = "goal.md"
    ProfileFile  = "profile.md"
    ContextDir   = "context"
    ManifestFile = "manifest.json"
)
```

### Front matter

```yaml
---
description: OAuth token exchange rework
url: https://github.com/umputun/revmux/pull/123
branch: feature/oauth
base: 4ed3259
---

Reviewing the token exchange path after the provider swap.
```

Every key optional, body optional, file optional. Closed vocabulary via `dec.KnownFields(true)`.

`Meta` carries **both** yaml and json tags: it is parsed from front matter and marshalled into
`revmux config` output. Untagged fields would emit `URL` rather than `url` and break the shape below.

### Name validation has one owner

`options.checkName` (config.go:451) and `archive.checkComponent` (archive.go:432) are already two copies
of one security-relevant rule, and `app/task` can import neither. `task.CheckName` becomes the single
definition and both delegate to it — three divergent copies is the wrong end state for a rule
`.claude/rules/config.md` treats as a boundary.

### `revmux new` output

```console
$ revmux new --task pr-123 --run 01-initial
{
  "task_dir": "/abs/.revmux/tasks/pr-123",
  "task_file": "/abs/.revmux/tasks/pr-123/task.md",
  "round_dir": "/abs/.revmux/tasks/pr-123/01-initial",
  "input_dir": "/abs/.revmux/tasks/pr-123/01-initial/input",
  "scope": "…/input/scope.md", "goal": "…/input/goal.md",
  "profile": "…/input/profile.md", "context": "…/input/context",
  "created": ["task_dir", "task_file", "round_dir", "input_dir"]
}
```

### Archive handle sequence

```
tasksRoot = OpenRoot(tasksDir)              opened, never created
taskRoot  = tasksRoot.OpenRoot(task)        opened, never created; HELD for the run (Prune uses it)
  Lstat(round) through taskRoot             refuse a symlink outright
roundRoot = taskRoot.OpenRoot(round)        opened, never created
  checkHandle(taskRoot, roundRoot)          os.SameFile against the entry that was read
  Stat(input/)                              must exist, else the caller has not prepared the round
  OpenFile(manifest.json, O_CREATE|O_EXCL)  atomic collision check + ownership marker
```

### The review path still opens-never-creates

`resolveContext` must never gain a create-on-missing fallback. A typo'd `--task` errors rather than
silently producing an empty task. Creation lives only in `revmux new`.

## What Goes Where

- **Implementation Steps** (`[ ]`): code, tests, docs inside this repo
- **Post-Completion** (no checkboxes): the manual end-to-end review run and the stale `.revmux/tasks/`
  working data

## Implementation Steps

### Task 1: `app/task` package — layout, `task.md` parsing, name validation

**Design Contract:**

Type:
- `Meta` (exported — `app/introspect.go` embeds it in the `revmux config` payload, an out-of-package caller)

Methods (full signatures):
- none. `Meta` is a data carrier; nothing operates on it beyond marshalling.

Standalone helpers planned (justification why NOT a method):
- `Load(dir string) (Meta, error)` — package entry point and what *produces* the `Meta`; there is no
  receiver to hang it on
- `CheckName(what, name string) error` — cross-cutting validator shared by `package main` and
  `app/archive`, neither of which is a method of one type here

Exports (justification per item: who outside the package calls this?):
- `Meta` — `app/introspect.go`
- `Load` — `app/introspect.go`, `app/config.go`
- `CheckName` — `app/config.go` (`options.checkNames`), `app/archive/archive.go` (`checkComponent`)
- layout constants (`MetaFile`, `InputDir`, `ScopeFile`, `GoalFile`, `ProfileFile`, `ContextDir`,
  `ManifestFile`) — `app/archive` and `package main` join paths from them

**Files:**
- Create: `app/task/task.go`
- Create: `app/task/task_test.go`

- [ ] create `app/task/task.go` with the layout constants, `Meta` (yaml + json tags) and `CheckName`
- [ ] implement `Load` reading `<dir>/task.md`, reusing the front-matter split pattern from
      `app/prompt/prompt.go:267`, with `dec.KnownFields(true)` so an unknown key is a load-time error
- [ ] a missing `task.md` returns a zero `Meta` and no error; a malformed one is an error
- [ ] write tests for `Load`: full front matter, partial, body-only with no front matter, absent file,
      unknown key rejected, malformed YAML rejected
- [ ] write tests for `CheckName`: empty, absolute, separator, `..`, leading dot, valid
- [ ] run tests — must pass before task 2

### Task 2: archive — failing tests for the new layout (TDD)

**Files:**
- Modify: `app/archive/archive_test.go`

- [ ] write a test that a round reached through a symlink is refused
- [ ] write a test that a round already holding `manifest.json` is refused as already-run
- [ ] write a test that a round containing only `input/` is accepted
- [ ] write a test that a missing round errors with a message naming the round to create
- [ ] write a test that a missing `input/` errors naming the path the caller must create
- [ ] write a test that a task-level directory without `manifest.json` is never pruned at `--keep-runs=0`
- [ ] write a test that `task.md` survives `--keep-runs=0`
- [ ] run tests — they MUST fail, proving they exercise the new behaviour before it exists

### Task 3: archive — `New` for the round layout

**Files:**
- Modify: `app/archive/archive.go`
- Modify: `app/artifacts.go`

- [ ] drop `runsDir`; the round is a direct child of the task directory
- [ ] hold the task handle for the archive's lifetime; `Close` releases task + round handles
- [ ] `Lstat` the round entry through the task handle and refuse a symlink before opening
- [ ] `OpenRoot` the round, then `checkHandle` it against the task handle
- [ ] require `input/` to exist, erroring with the path the caller must create
- [ ] create `manifest.json` with `O_CREATE|O_EXCL` as the atomic already-ran check
- [ ] keep a structural old-layout backstop (`scope.md` or `runs/` at task level); the user-facing
      message fires earlier, in Task 5
- [ ] rename `checkRunsEntry` → `checkRoundEntry`; there is no `runs` entry left to check
- [ ] delegate `checkComponent` to `task.CheckName`
- [ ] replace `manifestFileName` in `app/artifacts.go` with `task.ManifestFile`; `reportFileName` and
      `findingsFileName` stay local, since only `package main` writes them
- [ ] refresh godoc naming `runs/`: package doc, `Opts`, `Archive` (including the handle field comment),
      `runEntry`, `New`, `Close`
- [ ] run the Task 2 tests — must now pass

### Task 4: archive — `Prune` by ownership

**Files:**
- Modify: `app/archive/archive.go`

- [ ] enumerate the task handle instead of the removed `runs/` handle
- [ ] skip non-directories, and skip any directory without `manifest.json`
- [ ] keep the identity exclusion of the run being written, unchanged
- [ ] keep `clear` → `enumerated` → non-recursive `Remove`, with the task handle as parent
- [ ] apply the Overview's open decision on whether `input/` is deleted with the round
- [ ] refresh `Prune`, `remove` and `clear` godoc — they describe `runs/` and the positional guarantee
- [ ] run the Task 2 pruning tests — must now pass
- [ ] re-run the existing archive suite: symlink, rename and containment tests must still pass

### Task 5: context resolution from `input/`

**Files:**
- Modify: `app/config.go`
- Modify: `app/main.go`
- Modify: `app/config_test.go`

- [ ] `resolveContext` reads `<task>/<run>/input/{scope.md,goal.md,profile.md,context/}` via `app/task`
- [ ] **detect the old layout here** (`scope.md` or `runs/` at task level) and error naming both shapes.
      `resolveContext` runs before `archive.New`, so a check only in the archive is unreachable from the
      CLI and the migration message would never fire
- [ ] `reviewContext.TaskDir` stays the **task** directory — `archive.History` enumerates sibling rounds
      from it. Making it the round directory yields an empty inventory with no error
- [ ] an omitted `--run` is a load-time error naming the round and the `revmux new` call that creates it
- [ ] delete `runName` and `runTimeFormat`; `main.go:132` reads `o.opts.Run` directly and drops the
      `executor.Clock` argument
- [ ] `checkNames` errors on an empty `--run` instead of returning nil; delegate to `task.CheckName`
- [ ] `resolveContext` gains no create-on-missing fallback
- [ ] write tests: each file resolved from `input/`, missing/empty `scope.md` errors, absent optionals
      resolve to the placeholder, omitted `--run` errors, old layout errors naming both shapes
- [ ] run tests — must pass before task 6

### Task 6: prior-round inventory without `runs/`

**Files:**
- Modify: `app/archive/history.go`
- Modify: `app/archive/history_test.go`

- [ ] enumerate sibling round directories under the task directory, skipping `task.md` and any directory
      without `manifest.json`
- [ ] inventory line unchanged: name, when it ran, counts by severity, degraded sources
- [ ] refresh the `History` godoc, which describes "the `runs/` path plus one line per round"
- [ ] write tests: rounds discovered without `runs/`, `task.md` not listed, a stray directory not listed,
      first round omits the block entirely
- [ ] write the regression test for the silent failure: with a prior round present, the block resolved
      from `reviewContext.TaskDir` is non-empty
- [ ] run tests — must pass before task 7

### Task 7: `revmux new` subcommand

**Design Contract:**

Type:
- `Opts` (exported, `app/task` — `package main` constructs it to call `Scaffold`)
- `Paths` (exported, `app/task` — returned to `package main` and marshalled to the command's JSON)
- `newCmd` (unexported, `package main` — go-flags subcommand struct mirroring `configCmd`)

Methods (full signatures):
- `(c *newCmd) Execute([]string) error` — records the selection only, setting `opts.showNew`. Mirrors
  `configCmd.Execute` (introspect.go:77): go-flags calls it before the injected writers exist.
- `(o runOpts) writeTaskPaths() error` — scaffolds and writes the JSON through the injected stdout,
  mirroring `runOpts.writeCatalog` (main.go:112). It lives on `runOpts` because `Paths` is defined in
  `app/task` and Go forbids a method on a non-local type; a `task.Paths` method would also bypass the
  injected writer and leave nothing for a test to capture.

Standalone helpers planned (justification why NOT a method):
- `Scaffold(opts Opts) (Paths, error)` — package entry point and what *produces* the `Paths`; there is
  no receiver to hang it on

Exports (justification per item: who outside the package calls this?):
- `Opts`, `Paths`, `Scaffold` — `package main` calls `Scaffold` from `writeTaskPaths`

**Files:**
- Modify: `app/task/task.go`
- Modify: `app/task/task_test.go`
- Modify: `app/config.go`
- Modify: `app/main.go`
- Create: `app/newcmd.go`
- Create: `app/newcmd_test.go`

- [ ] add `Opts`, `Paths` and `Scaffold` to `app/task`, creating task dir, `task.md` template, round dir
      and `input/`, reporting which were created
- [ ] ship the `task.md` template fully commented out, mirroring `initConfig` (config.go:468); never
      overwrite an existing `task.md`
- [ ] refuse a round that already holds `manifest.json`
- [ ] validate `--task` and `--run` via `task.CheckName` before creating anything
- [ ] add `showNew` beside `showConfig` (config.go:82); `newCmd.Execute` sets it and prints nothing
- [ ] add `runOpts.writeTaskPaths`, dispatched from `run` beside the `showConfig` case (main.go:75),
      before any pipeline, archive or TUI exists
- [ ] write tests for `Scaffold`: fresh task, second round on an existing task, existing `task.md`
      preserved, round with artifacts refused, invalid names refused before any mkdir
- [ ] write tests for `writeTaskPaths` against an injected buffer: JSON shape and `created` contents
- [ ] run tests — must pass before task 8

### Task 8: `revmux config` reports tasks with their metadata

**Design Contract:**

Type:
- `taskInfo` (unexported, `package main` — matches the existing convention of `pathInfo`, `knob`,
  `stageInfo`, `profileInfo` in `app/introspect.go`; no out-of-package caller)

Methods (full signatures):
- none. It is a JSON payload struct, like its siblings in that file.

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none

**Files:**
- Modify: `app/introspect.go`
- Modify: `app/introspect_test.go`

- [ ] `.paths.tasks` changes from `[]string` to `[]taskInfo`: `id`, `description`, `url`, `branch`,
      `base`, `rounds`
- [ ] `taskInfo` embeds `task.Meta` rather than copying fields, so a new front-matter key surfaces once
- [ ] read each task's `task.md` via `task.Load`; a task without one reports empty fields
- [ ] write tests: task with full front matter, task with none, task with no rounds, several tasks ordered
- [ ] run tests — must pass before task 9

### Task 9: project documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `.claude/rules/config.md`
- Modify: `.claude/rules/prompts.md`
- Modify: `README.md`
- Modify: `app/defaults/config`
- Modify: `app/config.go` (the `--keep-runs` struct-tag description)

- [ ] `CLAUDE.md` — the task-directory hard rule, the archive rule, keep-in-sync conventions, **plus
      three sections the first draft missed**: "Findings go to stdout as JSON" (`revmux config` is no
      longer the single stdout exception), "Prior rounds are injected" (describes the `runs/` path), and
      Project structure (needs `app/task/`, `app/newcmd.go`, and the `app/archive/` line naming `runs/`)
- [ ] rewrite the "revmux writes only under `runs/`… never modified or pruned" hard rule per the
      Overview's open decision
- [ ] `.claude/rules/config.md` — context resolution, `--task`/`--run`, and the entire pruning argument,
      written in terms of `runs/` throughout; state the ownership guarantee that replaced it; fix the
      `reviewContext.TaskDir` paragraph
- [ ] `.claude/rules/prompts.md` — `task.md` as a fourth front-matter kind, and that revmux never
      resolves `branch`/`base`
- [ ] `README.md` — task directory, run archive, configuration, `revmux config` output, `revmux new`,
      **and the flag table**: `--run` default column (:289) and `--keep-runs` (:313), the "Pruning only
      ever reads `runs/`" line (:166), and the exit-code row naming "a `--run` name that already exists" (:374)
- [ ] `app/defaults/config` — the `keep-runs` comment, whose semantics change from "revmux artifacts"
      to "the whole round including caller input"
- [ ] verify no doc still describes `runs/` or task-level `scope.md`

### Task 10: both skill trees

**Files:**
- Modify: `.claude-plugin/skills/revmux/SKILL.md`
- Modify: `.claude-plugin/skills/revmux/references/{task-dir,invocation,output}.md`
- Modify: `.claude-plugin/skills/revmux/scripts/task-state.sh`
- Modify: `plugins/codex/skills/revmux/**` (mirror)

- [ ] `SKILL.md` Step 2: call `revmux new`, then write the four files at the paths it returns. No path
      construction, no `mkdir`, no hardcoded layout anywhere in the skill
- [ ] match tasks via `task.md` front matter (`url`, `branch`); the derive-the-id table becomes a
      fallback for naming a *new* task, not a way to find an existing one
- [ ] `references/task-dir.md`: rewrite around what each file should contain, not where it lives
- [ ] `references/output.md`: new archive layout
- [ ] `references/invocation.md`: `--run` now required, and the `--keep-runs` behaviour decided above
- [ ] `scripts/task-state.sh`: report rounds and their `input/` state, and read `task.md`
- [ ] mirror into `plugins/codex/`; `diff -r` of both `references/` and `scripts/` must be empty
- [ ] run `shellcheck` on all four scripts — must be clean

### Task 11: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented
- [ ] verify edge cases: symlinked round, stray task-level directory, old-layout task, missing `input/`
- [ ] run full test suite: `make test`
- [ ] run `make lint` — zero issues
- [ ] verify coverage has not regressed on `app/archive`

### Task 12: [Final] Update documentation

- [ ] update README.md if anything drifted during implementation
- [ ] update CLAUDE.md if new patterns were discovered
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention — no checkboxes, informational only*

**Manual verification:**
- a real end-to-end review run on the new layout: `revmux new`, write `scope.md`, run the review,
  confirm the archive and the prior-round inventory are correct on a second round
- confirm the shipped skill drives it correctly through the symlinked install at `~/.claude/skills/revmux`

**External system updates:**
- the 13 existing tasks under `.revmux/tasks/` use the old layout. That directory is gitignored working
  data, so wiping is acceptable. No automatic migration: the old layout has one scope for N rounds and
  there is no correct way to assign it, so revmux refuses it with a message naming both shapes.

---

Smells pre-check: 13 items fixed before save — non-compiling `(p Paths) write` method replaced with
`runOpts.writeTaskPaths`; `newCmd.Execute` corrected to the record-only `configCmd` pattern; old-layout
refusal moved to the reachable path; `task.CheckName` named as the single owner of name validation;
`app/artifacts.go`, `app/main.go`, `app/defaults/config` and two test files added to Files lists; godoc
refresh bullets added to Tasks 3, 4 and 6; `reviewContext.TaskDir` disambiguation plus its
silent-failure regression test added; const-block godoc, `Meta` yaml+json tags and a `taskInfo` Design
Contract added; `checkRunsEntry` rename added; three unnamed CLAUDE.md sections and the README flag
table added to Task 9. One item raised as an open decision rather than fixed: `--keep-runs` now deleting
caller-authored `input/` (see Overview).
