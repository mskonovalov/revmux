# revmux init, revmux stats, and `/revmux self`

## Overview

Three changes that together let revmux improve its own review configuration from the record of its past runs.

**`revmux init`** materializes `./.revmux/` so there is something local to edit. Today `--init` writes only the commented-out INI settings template, and `--dump-defaults` writes the **embedded** prompt text even when the user has a customized `~/.config/revmux/` copy — so neither gives a faithful starting point. `revmux init` writes what actually *resolved*.

**`revmux stats`** reads what revmux already wrote under the tasks root and emits aggregated JSON: per-agent yield and reliability, stage attrition, per-lens verdict mix. One task with five rounds is ~880K of structured JSON, so this is Go arithmetic rather than a model reading archives — the same division the project already applies when Go stamps `Finding.sources` and the model never does.

**`/revmux self`** is the skill mode that reads those two commands and proposes one configuration change at a time, with its evidence, for the user to accept or skip.

Benefit: the review configuration stops being a thing authored once and never revisited. Integration is additive — two read-mostly subcommands and a skill mode; no change to the pipeline, the archive format, or the wire shape of a finding.

## Context (from discovery)

Files and components involved:

- `app/config.go` — `initConfig` (:468) writes the commented template to `./.revmux/config`; `dumpDefaults` (:495) walks `prompt.Defaults()` and writes embedded bytes to an arbitrary directory. **Both print to `o.stderr`, not stdout.** Subcommands are declared at :73-74 with `SubcommandsOptional = true` (:105). `options.Task` (:48) already exists and is `no-ini`.
- `app/newcmd.go` — the model for a subcommand: `Execute` only records the selection because go-flags calls it from inside `parseArgs`, before the injected writers exist; `runOpts.writeTaskPaths` does the work and prints JSON via `o.stdout`, building `task.Round` from `o.opts`.
- `app/introspect.go` — the same shape for `revmux config`. `(o options) tasks(root)` (:203-236) is the **only** task enumerator, and its containment through an `os.Root` is deliberate: it lists exactly what `archive.New` can open, excluding a link the archive could not walk.
- `app/main.go` — `run()` (:60) is a flat switch over five selection cases before the pipeline path.
- `app/prompt/prompt.go` — `Set.Provenance() []FileOrigin` (:168) already resolves per-file precedence across `LayerProject`/`LayerUser`/`LayerEmbedded` (:31-35). **`Set` (:77) keeps only `profiles`, `stages`, `lenses`, `origins`** — the `files map[string]fileRef` holding raw bytes is a local in `Load` (:101) and is discarded.
- `app/archive/history.go` — `History` reads prior `findings.json` gated by `task.Rounds`/`HasRun`, decoding into a local partial `record` struct rather than importing the writer's type. The precedent for both reading revmux's own output and for shape duplication.
- `app/task/task.go` — layout constants (:28-38): `InputDir`, `ScopeFile`, `GoalFile`, `ProfileFile`, `ContextDir`, `ManifestFile`, `FindingsFile`, `ReportFile`, `metaFile`.
- `app/pipeline/pipeline.go:25`, `app/pipeline/artifacts.go:16-21`, `app/pipeline/find.go:262` — spell seven more layout names `app/task` does not have: `events.jsonl`, `prompts/agents`, `prompts/stages`, `stages/1-found.json`, `stages/2-synthesized.json`, `stages/3-verified.json`, `agents`.
- `app/finding/report.go` — `SourceStatus.DegradedNames()` (:76) merges the explicit degraded list with the per-agent flags, because the two records disagree whenever one is written and the other is not.

Patterns found:

- Subcommands print JSON on stdout and exit before any pipeline, archive or TUI exists. CLAUDE.md sanctions this as the one carve-out in "stdout belongs to the report".
- Option structs at 4+ parameters; private by default; a function called only from one struct's methods is a method of that struct.
- `revmux config` reports what **resolved**, never what is embedded.

Dependencies identified:

- **A stage snapshot is a full `finding.Report`.** `stages/3-verified.json` carries `scope`, `sources`, `findings`, `open_questions`, `pre_existing`, `immaterial`, `stats` — including `sources.agents[]` with each agent's `lenses`, `tokens` and `degraded`. Stats therefore needs **no `manifest.json` dependency**: everything but retry counts comes from the stage snapshots, which are `Report`-shaped and so decode the way `history.go` already decodes one.
- `revmux stats` must still read `events.jsonl` for retry counts, so the archive layout names must move to `app/task` first, or the project gains a second copy of every archive path — the drift CLAUDE.md's "no layout name is spelled anywhere else" rule exists to prevent.
- `revmux init` needs the winning file's bytes, which neither `FileOrigin` nor `Set` retains.

Corpus reality check, from the only archive that exists (5 rounds of `since-1f21e93`): 638 `agent_progress`, 466 `agent_activity`, 32 `agent_started`, 32 `agent_done`, 21 `agent_state`, 19 `findings`, 15 `stage` — and **zero** `agent_retried`, `agent_degraded` or `rate_limit`. The reliability counters will be uniformly zero on a healthy corpus, which the skill must treat as "nothing to say" rather than a finding.

Stage attrition on round `03-followups`, for calibration: 26 found → 10 findings + 7 pre-existing synthesized → 9 + 8 verified. `findings.json` matched stage 3 exactly **only because that round ran with `min-confidence: 0`**.

## Development Approach

- **testing approach**: Regular — implementation first, then its tests, within the same task; nothing moves on until they pass.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility

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

### Project-specific additions

- Settings and prompt text are different kinds of thing and must never be materialized the same way. Settings ship commented out so an upgrade can overwrite them; prompt markdown ships live.
- Every stats field's godoc must name **which artifact the number came from**. That distinction is what the plan review caught wrong twice.
- No test spawns a real model. Every filesystem test uses `t.TempDir()` and points `--tasks-dir` at it (`.claude/rules/testing.md`).
- Both skill trees stay byte-identical in `references/` and `scripts/`; only `SKILL.md` may differ, and only in script-path resolution and how each harness asks a question. `make check-plugins` enforces it.

## Testing Strategy

- **unit tests**: required for every task (see Development Approach above)
- **e2e tests**: the project has none; the equivalent gate is `make test` (race + coverage) plus `make check-plugins`
- stats fixtures come from a hand-built temp task tree, never the developer's real `./.revmux/tasks/`

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

`revmux init` and `revmux stats` join `config` and `new` as go-flags subcommands using the established split: `Execute` records the selection, `run()` does the work through injected writers. Both print JSON on stdout and exit before any pipeline exists.

Materialization reuses `Set.Provenance()`, which already answers "which layer won for this file". `Set` gains a retained copy of the winning bytes, because it currently discards them and the only surviving alternative — the parsed `doc.Body` — has had its front matter stripped. Materializing from that would write lens files without `description:` and profiles without `agents:`, so the next `prompt.Load` over the freshly written `./.revmux/` would fail validation: `revmux init` would break the project it just initialized.

Aggregation lives in `app/archive`, already the package that reads what revmux wrote about a round. It decodes the three stage snapshots as `finding.Report` values — the same tolerant decoding `History` performs — which supplies the roster, per-agent tokens and degrade records without any `manifest.json` dependency. Only retry counts require `events.jsonl`. Rendering stays in `package main`, like `introspect.go`.

Two single-source moves come first: the archive path names from `app/pipeline` to `app/task`, and task enumeration from `app/introspect.go` to `app/task`.

Key decisions and rationale:

- **`init` writes resolved prompts but the commented-out settings template.** Writing resolved settings would freeze every current default as an explicit value and silently stop tracking upstream changes to all of them.
- **`stats`, not `reflect` or `history`.** revmux computes statistics; the skill reflects on them. `history` already means the prior-round inventory injected into prompts.
- **`Survived` comes from `stages/3-verified.json`, not `findings.json`.** The archived report is the `--min-confidence`-filtered one, and survivors are split across four arrays. Reading `.findings` from `findings.json` counted 9 of 17 real survivors on round `03-followups`. That number feeds the skill's "drop an agent that produces nothing" candidate, so a miscount removes a working agent.
- **Per-lens numbers carry an explicit `Ambiguous` count, computed from `stages/1-found.json` only.** `finder.lenses` (`app/pipeline/find.go:237`) falls back to the agent's entire lens set when the model names no valid lens. After synthesis a finding's `lenses` is a union across merged findings from different agents, so the test is meaningless there.
- **The top type is `archive.Corpus`, not `archive.Stats`.** `finding.Stats` already exists and `package main` imports both packages.
- **No evidence floors, tiers, or validation re-runs in the skill.** The user judges each suggestion from the evidence shown; `.revmux/` prompt files are git-tracked (`.gitignore` covers `.revmux/tasks/` only), so a bad edit reverts with `git checkout`.

## Technical Details

**Archive layout constants** move to `app/task` and are referenced from `app/pipeline`:

```go
// EventsFile is the run's decision record: stalls, retries, degrades and stage transitions.
const EventsFile = "events.jsonl"

// AgentsDir holds one verbatim stream tee per agent.
const AgentsDir = "agents"

// AgentPromptDir and StagePromptDir hold composed prompts, split so a roster agent named after a
// stage cannot overwrite a stage prompt.
const (
    AgentPromptDir = "prompts/agents"
    StagePromptDir = "prompts/stages"
)

// FoundFile, SynthesizedFile and VerifiedFile are the per-stage findings snapshots. Each is a
// finding.Report, so each carries the full source roster alongside that stage's findings.
const (
    FoundFile       = "stages/1-found.json"
    SynthesizedFile = "stages/2-synthesized.json"
    VerifiedFile    = "stages/3-verified.json"
)
```

**Task enumeration** moves to `app/task` so one definition serves both `revmux config` and `revmux stats`:

```go
// List names every task under root, decided through an os.Root so it reports exactly what
// archive.New can open — a link that cannot be walked is left out rather than reported and then
// failing later.
func List(root string) ([]string, error)
```

**Winning bytes from the prompt set.** `Set` gains a retained `files` map, populated in `Load`'s existing loop beside the `origins` append. Re-reading from disk instead would be a TOCTOU against `Provenance` and could not reach the embedded layer at all:

```go
type Set struct {
    profiles map[string]*Profile
    stages   map[string]*Stage
    lenses   map[string]doc
    origins  []FileOrigin
    files    map[string]fileRef // raw winning bytes, keyed by the same rel path as origins
}

// Content is the unparsed bytes of the file that won the precedence chain for relPath, front matter
// included. revmux init writes these, so anything stripped here produces a tree that fails to load.
func (s *Set) Content(relPath string) ([]byte, error)
```

**Stats shapes** (`app/archive`). Only `Corpus`, `StatsQuery` and `CollectStats` are exported — `package main` names those three and reaches the rest through `Corpus`, which `encoding/json` walks regardless of type-name visibility:

```go
// Corpus is every task's review record under one tasks root.
type Corpus struct {
    Tasks  []taskStats `json:"tasks"`
    Totals taskStats   `json:"totals"`
}

type taskStats struct {
    ID     string        `json:"id,omitempty"` // empty on Totals
    Rounds int           `json:"rounds"`       // rounds task.HasRun accepts
    Agents []agentStats  `json:"agents"`
    Lenses []lensStats   `json:"lenses"`
    Stages []stageFlow   `json:"stages"`
}

type agentStats struct {
    Name           string `json:"name"`
    Raised         int    `json:"raised"`          // findings in stages/1-found.json naming this agent in sources
    Survived       int    `json:"survived"`        // all four arrays of stages/3-verified.json
    Corroborated   int    `json:"corroborated"`    // survived findings whose sources has more than one entry
    DegradedRounds int    `json:"degraded_rounds"` // rounds where SourceStatus.DegradedNames names it
    Retries        int    `json:"retries"`         // agent_retried events in events.jsonl
    Tokens         int    `json:"tokens"`          // summed from sources.agents[].tokens
}

type lensStats struct {
    Name      string                 `json:"name"`
    Raised    int                    `json:"raised"`    // findings in stages/1-found.json naming this lens
    Ambiguous int                    `json:"ambiguous"` // of Raised, those attributable only by the agent's lens set
    Verdicts  map[finding.Verdict]int `json:"verdicts"` // from stages/3-verified.json
}

type stageFlow struct {
    Name string `json:"name"`
    In   int    `json:"in"`
    Out  int    `json:"out"`
}

// StatsQuery selects what CollectStats reads. Two adjacent string parameters would be a swap hazard,
// and swapping these silently reads the wrong directory.
type StatsQuery struct {
    TasksDir string
    Task     string // empty means every task under TasksDir
}

// CollectStats aggregates every round that ran under the query's tasks root.
func CollectStats(q StatsQuery) (Corpus, error)
```

**Processing flow.** Per round, decode `stages/1-found.json`, `stages/2-synthesized.json` and `stages/3-verified.json` as `finding.Report`, then scan `events.jsonl` for retries. `Raised` and `Ambiguous` come from stage 1; `Survived`, `Corroborated` and `Verdicts` from stage 3; `stageFlow` from the counts across all three plus the report's own findings; roster, tokens and degrade records from any stage snapshot's `sources`. Decoding is tolerant the way `History` is — an unreadable or half-written round is skipped, never fatal, because an interrupted run leaves exactly that.

**`Ambiguous`** counts a stage-1 finding whose `lenses` array equals the raising agent's full configured lens set **and** whose agent carries more than one lens — the shape `finder.lenses` produces on its fallback path. It over-counts the case where a model genuinely named every lens it carries; that is stated in the field's godoc rather than papered over. Verdict attribution goes through each verified finding's own `sources`/`lenses`, never through an ID match back to stage 1: synthesis merges findings and drops absorbed IDs.

**Event vocabulary.** `app/archive` decodes `events.jsonl` into a local partial struct, as `history.record` already does for `findings.json`, rather than importing `app/pipeline` and pointing the artifact package at the orchestrator. The cost is that `"agent_retried"` is spelled in a second place, which Task 11 adds to CLAUDE.md's keep-in-sync bullet for `EventKind`.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): everything in this codebase — Go, both skill trees, README, CLAUDE.md, `.claude/rules/`.
- **Post-Completion** (no checkboxes): running `/revmux self` against the real archive to judge whether its suggestions are worth acting on. That is a judgment call on live data, not a test.

## Implementation Steps

### Task 1: Move archive layout constants into app/task

**Files:**
- Modify: `app/task/task.go`
- Modify: `app/pipeline/pipeline.go`
- Modify: `app/pipeline/artifacts.go`
- Modify: `app/pipeline/find.go`
- Modify: `app/pipeline/artifacts_test.go`

- [x] add the seven constants from Technical Details to `app/task/task.go`, beside `ManifestFile` / `FindingsFile` / `ReportFile`, each with the godoc shown
- [x] replace `eventsFile` in `app/pipeline/pipeline.go` and the five names in `app/pipeline/artifacts.go` with the `app/task` constants, deleting the local ones
- [x] update `app/pipeline/find.go:262` to join from `task.AgentsDir` rather than the literal `"agents"`
- [x] write a test asserting each archive path the pipeline writes equals its `app/task` constant, so a rename in one place cannot silently diverge
- [x] run tests - must pass before next task

### Task 2: Lift task enumeration into app/task

**Files:**
- Modify: `app/task/task.go`
- Modify: `app/task/task_test.go`
- Modify: `app/introspect.go`
- Modify: `app/introspect_test.go`

- [x] move the body of `(o options) tasks(root)` (`app/introspect.go:203-236`) to `task.List(root)`, preserving the `os.Root` containment exactly — it is what makes the list agree with what `archive.New` can open
- [x] have `revmux config` call `task.List` so there is one enumerator; `revmux stats` will call the same one in Task 7
- [x] write tests for `task.List`: ordinary tasks, a non-directory entry ignored, `task.md` not reported as a task, a symlink pointing out of the root excluded, a missing root yielding an empty list rather than an error
- [x] write a test asserting `revmux config`'s task list is unchanged by the move
- [x] run tests - must pass before next task

### Task 3: Retain and expose the winning file's bytes

**Design Contract:**

Type:
- no new type — `Set` gains a `files map[string]fileRef` field (unexported, `fileRef` already exists)

Methods (full signatures):
- `(s *Set) Content(relPath string) ([]byte, error)`

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- `Set.Content` — called by `runOpts.materializePrompts` in `package main` (Task 4)

**Files:**
- Modify: `app/prompt/prompt.go`
- Modify: `app/prompt/prompt_test.go`

- [x] add the `files` field to `Set` and populate it inside `Load`'s existing loop, beside the `set.origins` append, so `Content` and `Provenance` cannot disagree about which layer won
- [x] implement `Content` returning the retained bytes, and an error naming the path when it is not part of the loaded tree — never empty bytes
- [x] write a test asserting `Content` returns bytes **including front matter**, since a stripped body would produce a tree that fails the next `Load`
- [x] write tests for a project-layer win, a user-layer win and an embedded win, each asserting the bytes match that layer's file
- [x] write a test asserting `Content` and `Provenance` agree on every entry of a three-layer tree, and one asserting an unknown path errors
- [x] run tests - must pass before next task

### Task 4: Add the `revmux init` subcommand

**Design Contract:**

Type:
- `initCmd` (unexported — go-flags reaches it through the `options` struct, no out-of-package caller)
- `initPaths`, `initFile` (unexported — the JSON payload, like `catalog` in `app/introspect.go:28`)

Methods (full signatures):
- `(c *initCmd) Execute(args []string) error`
- `(o runOpts) writeInitPaths() error`
- `(o runOpts) materializePrompts(set *prompt.Set) ([]initFile, error)`

Standalone helpers planned (justification why NOT a method):
- none — `materializePrompts` is pre-declared as a method precisely because its only caller is `writeInitPaths`, a `runOpts` method

Exports (justification per item: who outside the package calls this?):
- none

**Files:**
- Create: `app/initcmd.go`
- Create: `app/initcmd_test.go`
- Modify: `app/config.go`
- Modify: `app/main.go`

- [x] create `app/initcmd.go` with `initCmd` mirroring `newCmd` — `Execute` records `o.opts.showInit` only, because go-flags calls it before the injected writers exist
- [x] register the `init` subcommand in the `options` struct and add the `showInit` selection field
- [x] implement `materializePrompts`: for each `Set.Provenance()` entry write `Set.Content` to `./.revmux/<path>` unless a project file is already there, recording the path, the layer it resolved from, and whether this call created it
- [x] implement `writeInitPaths`: reuse `initConfig` for the settings template, call `materializePrompts`, encode `initPaths` to `o.stdout`; add the case to `run()`
- [x] write tests: fresh directory creates config plus 12 prompt files; a second run creates nothing; an existing project file is left byte-identical; a customized config is left alone; a user-layer file wins over the embedded one
- [x] write a test asserting the materialized tree loads — `prompt.Load` over the written `./.revmux/` must succeed, which is what catches a front-matter-stripped write
- [x] write a test asserting nothing is written outside `./.revmux/`
- [x] run tests - must pass before next task

### Task 5: Fold `--init` into the subcommand and correct every description of it

**Files:**
- Modify: `app/config.go`
- Modify: `app/main.go`
- Modify: `app/config_test.go`
- Modify: `README.md`
- Modify: `.claude/rules/config.md`
- Modify: `.claude-plugin/skills/revmux/references/invocation.md`
- Modify: `plugins/codex/skills/revmux/references/invocation.md`
- Modify: `.claude-plugin/skills/revmux/scripts/launch-revmux.sh`
- Modify: `plugins/codex/skills/revmux/scripts/launch-revmux.sh`

- [x] make `--init` take the same path as `revmux init`, so there is one materialization implementation rather than two that can drift
- [x] leave `--dump-defaults` unchanged — it stays the escape hatch for embedded text at an arbitrary path, which is how a customized lens is diffed against the shipped one
- [x] update the `--init` flag description (`app/config.go:69`), `README.md:337` and `:433`, and `.claude/rules/config.md:405`: it now materializes the whole tree and prints JSON on **stdout**
- [x] update `references/invocation.md:265` and `:348` in **both** trees, and the comment at `scripts/launch-revmux.sh:182` in **both** trees — that comment states `--init` writes to stderr with stdout untouched, and it is load-bearing for the empty-report branch that re-codes exit 0 to `RC_LAUNCH_FAIL`
- [x] write a test asserting `--init` and `revmux init` produce identical files, and one asserting `--init` writes JSON to stdout and nothing to stderr
- [x] write a test asserting `--dump-defaults` still writes embedded bytes even when a project override exists
- [x] run `make check-plugins` and tests - must pass before next task

### Task 6: Read one round's numbers in app/archive

**Design Contract:**

Type:
- `Corpus`, `StatsQuery` (exported — named by `package main`'s stats subcommand)
- `taskStats`, `agentStats`, `lensStats`, `stageFlow` (unexported — reached through `Corpus` and marshaled by field; no out-of-package caller names them)
- `roundReader`, `roundStats` (unexported — per-round accumulation, used only inside the package)

Methods (full signatures):
- `(r *roundReader) read() error`
- `(r *roundReader) readEvents() error`
- `(r *roundReader) addRaised(found []finding.Finding)`
- `(r *roundReader) addSurvived(survived []finding.Finding)`
- `(r *roundReader) stats() roundStats`

Standalone helpers planned (justification why NOT a method):
- `newRoundReader(dir string) *roundReader` — constructor, explicitly exempt

Exports (justification per item: who outside the package calls this?):
- `Corpus`, `StatsQuery` — `runOpts.writeStats` in `package main` (Task 8)
- `CollectStats` — same (declared in Task 7)

**Files:**
- Create: `app/archive/stats.go`
- Create: `app/archive/collect.go`
- Create: `app/archive/stats_test.go`
- Create: `app/archive/collect_test.go`

- [x] define the shapes from Technical Details in `app/archive/stats.go`, each field's godoc naming the artifact its number comes from
- [x] implement `roundReader` in `app/archive/collect.go` with the round directory as a **field** set by `newRoundReader`, not a per-call argument that two methods could disagree about
- [x] decode the three stage snapshots as `finding.Report`: `Raised`/`Ambiguous` from stage 1, `Survived`/`Corroborated`/`Verdicts` from stage 3 as the union of all four arrays, roster and tokens from `sources.agents[]`
- [x] take degraded from `SourceStatus.DegradedNames()` rather than the event stream alone, since that method exists precisely because the two records disagree
- [x] implement `readEvents` for retry counts using a local partial struct like `history.record`, not an `app/pipeline` import
- [x] decode tolerantly like `History` — an unreadable or half-written round is skipped, never fatal
- [x] write tests over a hand-built temp round: agent tallies, lens tallies including an ambiguous case and a genuinely-both-lenses case, stage attrition, a round with no `events.jsonl`, and a round where `findings.json` is filtered smaller than stage 3 (asserting `Survived` follows stage 3)
- [x] run tests - must pass before next task

**Decisions taken while implementing (nothing in the plan settled them):**

- **Survivors come from the last stage snapshot the round carries, which is `stages/3-verified.json` for a
  full pipeline.** `--no-synthesis` and `--no-verify` each write no snapshot for the stage they skip, and
  counting `Survived` as zero for every agent on such a round is exactly the undercount the plan warns
  about. `findings.json` is still never a survivor source.
- **A survivor with no verdict counts as `finding.Unverified`.** Pre-existing issues and open questions are
  split off before verification and carry `""`, which would emit a `"": n` key in the verdict map;
  `unverified` is what "was not checked" already means in the vocabulary.
- **A fourth `stageFlow` entry named `report` carries the `--min-confidence` attrition**, since Technical
  Details lists "the report's own findings" among the counts. `find` has no entry: nothing goes into it.
- **`namedReport` is a fourth unexported type beyond the Design Contract's list**, holding one findings
  artifact and the stage that produced it, so the attrition chain runs over only the artifacts on disk.
- **`stageFind`/`stageSynthesis`/`stageVerify`/`stageReport` are spelled in `app/archive`**, the same
  duplication the plan already sanctions for `agent_retried`, and for the same reason: the artifact package
  must not import the orchestrator.

### Task 7: Aggregate rounds and tasks

**Design Contract:**

Type:
- no new type

Methods (full signatures):
- `(t *taskStats) add(o taskStats)`
- `CollectStats(q StatsQuery) (Corpus, error)` — standalone entry point, the package's exported API

Standalone helpers planned (justification why NOT a method):
- `CollectStats` — the package entry point, called from `package main`; explicitly exempt as an entry point

Exports (justification per item: who outside the package calls this?):
- `CollectStats` — `runOpts.writeStats` in `package main` (Task 8)

**Files:**
- Modify: `app/archive/collect.go`
- Modify: `app/archive/collect_test.go`

- [x] implement `CollectStats` — enumerate via `task.List` (Task 2) or the single `q.Task`, gated by `task.Rounds` so only rounds that ran are counted
- [x] implement the fold as `(t *taskStats) add(o taskStats)` accumulating into name-keyed maps, never a two-parameter merge where argument order silently decides which `ID` survives
- [x] return an empty `Corpus` rather than an error when the tasks root does not exist, so a project that has never run a review is not a failure
- [x] write tests: two tasks with differing rosters aggregate correctly; `q.Task` narrows; a prepared-but-never-run round is excluded; a missing tasks root yields an empty result
- [x] write a test asserting only temp dirs are read, never the developer's real tasks root
- [x] run tests - must pass before next task

**Decisions taken while implementing (nothing in the plan settled them):**

- **`q.Task` narrows by looking the id up in `task.List`'s output rather than by joining a path of its own.**
  One enumerator then decides what a task is for both the whole-corpus and the single-task reading, so a
  task reached through a relative symlink is aggregated exactly where `revmux config` reports it, and a name
  that is not one directory under the root — a separator, a parent hop — matches nothing by construction
  rather than by a second copy of `task.CheckName`.
- **A `--task` naming no task under the root is an error, not an empty document.** The empty-`Corpus` rule
  covers the tasks root itself; a typo'd id answered with zeros reads as a task with no history, which is
  the `pr123`-beside-`pr-123` failure CLAUDE.md names.
- **An unreadable tasks root or task directory fails the call rather than reporting no tasks or no rounds**,
  the same way `revmux config` refuses to report `"tasks": []` for a root it could not read. A round whose
  artifacts will not decode is still skipped, per Task 6.
- **A task with rounds nobody has run yet is still reported, at zero.** `revmux stats` and `revmux config`
  have to name the same task set — `/revmux self` reads both, and a task missing from one is a silent
  disagreement.
- **`Rounds` counts the rounds the numbers were read from, so a skipped undecodable round is not one**, and
  Task 6's godoc for the field was amended to say so: it is the denominator of everything beside it.
- **[deviation] `newTaskStats` and three private `add*` methods exist beyond the Design Contract's list.**
  `add` delegates to `addAgents`, `addLenses` and `addStages` rather than carrying three index maps inline,
  and `newTaskStats` is a constructor (explicitly exempt) whose non-nil slices keep an empty task marshaling
  as `[]` rather than `null`. `addLenses` starts a fresh verdict map for a lens it has not seen: taking the
  other side's map would alias one between a task entry and the totals, and the next task's verdicts would
  land in both.

### Task 8: Add the `revmux stats` subcommand

**Design Contract:**

Type:
- `statsCmd` (unexported — reached by go-flags through the `options` struct)

Methods (full signatures):
- `(c *statsCmd) Execute(args []string) error`
- `(o runOpts) writeStats() error`

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none

**Files:**
- Create: `app/statscmd.go`
- Create: `app/statscmd_test.go`
- Modify: `app/config.go`
- Modify: `app/main.go`

- [x] create `app/statscmd.go` with `statsCmd` following the same `Execute`-records-only split
- [x] register the `stats` subcommand and the `showStats` selection field — **declare no new `--task` flag**; build `StatsQuery` from the existing `o.opts.TasksDir` and `o.opts.Task`, as `writeTaskPaths` already does for `task.Round`
- [x] implement `(o runOpts) writeStats()` calling `archive.CollectStats` and encoding to `o.stdout`; add the case to `run()`
- [x] write tests: JSON lands on stdout with nothing on stderr; `--task` narrows; an empty tasks root produces a valid empty document rather than an error
- [x] run tests - must pass before next task

**Decisions taken while implementing (nothing in the plan settled them):**

- **The one-`--task`-field rule gets its own test, asserting `revmux --task pr-1 stats` and
  `revmux stats --task pr-1` both fill `options.Task`.** A second declaration is the failure the plan warns
  about, and nothing else in the suite would notice it: both spellings parse either way, and only the field
  they land in differs.
- **A corpus is asserted through a JSON shape declared in the test, not through the `archive` types.**
  Everything under `Corpus` is unexported, so `package main` reads the payload the way the caller model
  does, which is also what makes the field names part of what the test pins.

### Task 9: Add `/revmux self` to both skill trees

**Files:**
- Modify: `.claude-plugin/skills/revmux/SKILL.md`
- Modify: `plugins/codex/skills/revmux/SKILL.md`
- Modify: `.claude-plugin/skills/revmux/references/invocation.md`
- Modify: `plugins/codex/skills/revmux/references/invocation.md`

- [ ] add a `self` mode to both `SKILL.md`: read `revmux stats` and `revmux config`, build candidates, present one suggestion at a time with its evidence and reasoning, act on the choice, repeat
- [ ] state the candidate kinds: drop or keep an agent producing nothing that survives; split a lens pair that never corroborates; create a profile matching actual usage; retune a knob from reliability counts; rewrite a lens whose findings get rejected, shown as a diff
- [ ] state the rules: writes are project-local only, never `~/.config/revmux/` and never the embedded tree; run `revmux init` first if the tree is not local; a counter that is zero across the whole corpus means there is nothing to say, not a finding; a per-lens number is only as good as its `ambiguous` share, which must be quoted alongside it
- [ ] document `revmux init` and `revmux stats` in `references/invocation.md`, keeping both copies byte-identical
- [ ] diverge only in the ask mechanism — AskUserQuestion in the claude tree, a numbered list in the codex tree, matching how Step 6 already differs
- [ ] add activation triggers (`revmux self`, `self-improve`, `tune revmux`) to both trees
- [ ] run `make check-plugins` - must pass before next task

### Task 10: Verify acceptance criteria

- [ ] verify `revmux init` with a `~/.config/revmux/` override materializes the override, not embedded text, and that `prompt.Load` over the result succeeds
- [ ] verify `revmux init` twice in a row changes nothing the second time
- [ ] verify `revmux stats` against the real `./.revmux/tasks/` produces numbers consistent with those rounds' reports, spot-checked by hand against round `03-followups` (26 found → 10+7 synthesized → 9+8 verified)
- [ ] verify the reliability counters read zero on the current corpus and that this reads as absence rather than as a finding
- [ ] verify `revmux config` and `revmux stats` report the same task set
- [ ] run full test suite: `make test`
- [ ] run `make check-plugins` and `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0`
- [ ] verify test coverage meets the project standard (80%+ excluding mocks)

### Task 11: [Final] Update documentation

- [ ] document `revmux init` and `revmux stats` in README.md — the flag table and a subcommand section each
- [ ] update CLAUDE.md: the project-structure list gains `app/initcmd.go`, `app/statscmd.go`, `app/archive/stats.go` and `app/archive/collect.go`; the `app/archive` one-line description grows beyond "one round's artifacts" to cover corpus-wide aggregation; the stdout carve-out sentence grows from two subcommands to four; the layout-constants rule notes `app/pipeline` now joins from `app/task`
- [ ] add a keep-in-sync bullet to CLAUDE.md for the `EventKind` vocabulary: `app/progress.go`, the TUI, **and** `app/archive`'s local event struct
- [ ] update `.claude/rules/config.md` for the new subcommands and the stdout carve-out list
- [ ] update `.claude/rules/prompts.md` for `Set.Content`, the retained bytes, and what `revmux init` materializes
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification:**

- Run `/revmux self` against the real archive and judge whether its suggestions are worth acting on. Whether the numbers produce *useful* advice cannot be asserted in a test — it is the point of the feature and the only real acceptance criterion.
- The current corpus is five rounds of one task on one codebase. Expect per-agent yield and stage attrition to say something, and reliability to say nothing at all.

**Follow-on, deliberately out of scope:**

- A drift check reporting which local `.revmux/` prompt files are byte-identical to the embedded ones, so a user can delete them and resume tracking upstream. Only worth building if materialized files actually go stale in practice.
- Validation runs for a proposed lens rewrite (A/B across two fresh tasks). Considered and rejected: the user judges the diff, and git reverts a bad edit.
- Making lens attribution unambiguous by recording whether it was model-supplied or defaulted. Rejected as too expensive for the value — three schemas, the README, both skill trees and every recorded fixture.

---

Smells pre-check: 22 items fixed before save — two false premises corrected against the code (`Set` discards raw bytes, so `Content` needs a retained map; `Survived` must come from `stages/3-verified.json` rather than the min-confidence-filtered `findings.json`), the `roundReader` method set redesigned (directory as a field, `countFindings(…, bool)` split into `addRaised`/`addSurvived`, `fs` parameter renamed to avoid shadowing `io/fs`), four nested types unexported, `manifest.json` dependency removed entirely once stage snapshots proved to be full `finding.Report` values, task enumeration lifted to `task.List` so `config` and `stats` cannot disagree, the duplicate `--task` flag dropped, `--init`'s six stale documentation sites folded into the task that changes it, `archive.Stats` renamed `Corpus` to avoid colliding with `finding.Stats`, `Verdicts` typed `map[finding.Verdict]int`, godoc naming each stat's source artifact, Design Contracts added to the two tasks that were missing them, and `rate_limit` corrected from `agent_rate_limit`.
