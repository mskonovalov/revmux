# revmux — multi-agent review orchestrator, initial build

## Overview

revmux is a standalone Go binary that runs a structured multi-agent code review by spawning and
supervising `claude --print` and `codex exec` subprocesses, then returns findings.

It exists because agent fan-out driven from inside an AI coding session is unobservable and unrecoverable:
agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill, no retry
and no progress display.

A subprocess does not make the model faster. What it buys is control — a watchdog that notices a stall in
seconds and restarts that one agent, a live view of what every agent is doing, per-agent token counts,
and a run archive to debug a bad review afterwards.

**Scope boundary (hard).** revmux runs a review and returns findings. It does NOT do scope detection, git
operations, PR fetching, issue handling, or any source modification. It has **zero VCS dependency**. All
context — scope description, goal, project profile, prior rounds — is written to a task directory by the
caller and named with `--task`. Agents run diff commands themselves; revmux substitutes the *path* of
`scope.md`, never its contents, so no prompt can be bloated by a large scope.

What this buys over fan-out driven from inside a session:

- stalls are detected and retried per agent instead of hanging the whole review
- a source is a process, so cross-source counting cannot be miscounted
- live visibility into every agent, plus a per-run archive complete enough to audit a review afterwards —
  composed prompts, per-stage findings and prompt provenance, which is also the corpus a later
  self-reflection agent reads to propose changes to the lens text
- per-agent model and effort without needing a separate agent definition per combination
- a lens agent cannot invoke a skill that spawns its own subagent, so no agent-inside-an-agent
- the caller model can read the resolved configuration back out with `revmux config` and compose a custom
  lens set, instead of guessing at what the prompt tree holds

## Context (from discovery)

Verified against the real `claude` CLI during design — do not re-derive:

- `--json-schema` combined with `--output-format stream-json` works. The model is forced through a
  `StructuredOutput` tool call and the terminal `result` event carries a **pre-parsed `structured_output`
  object**. Read that field; never scrape JSON out of prose.
- the stream carries a typed `rate_limit_event` with `status`, `resetsAt`, `rateLimitType`,
  `overageStatus`. Use it rather than matching error strings.
- the `result` event carries per-model `usage` and `ttft_ms`, so per-agent token counts need no computation.
- **`--model` can be silently ignored** — a run with `--model haiku` actually executed
  `claude-sonnet-4-6`. Always read `modelUsage` from the result event and report what actually ran.
- even a trivial call carries substantial input tokens (system prompt, project instructions, skills listing),
  so per-agent token counts are worth reporting — they come free from the `result` event.

Also verified as real flags, so no task needs to check them again: `--effort`, `--json-schema`,
`--disable-slash-commands`, `--no-session-persistence`, `--disallowedTools`, `--output-format stream-json`,
`--permission-mode dontAsk`, and codex's `-m` / `-c` / `--sandbox read-only`.

Dependency versions: `go 1.26`, `bubbletea v1.3.10`, `bubbles v1.0.0`, `lipgloss v1.1.0`,
`go-flags v1.6.1`, `testify v1.11.1`, `yaml.v3` for front matter.

## Development Approach

- **testing approach**: Regular (code first, then tests within the same task)
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
1. Formatter runs clean (`gofmt -s -w` + `goimports -w`).
2. `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` reports zero issues.
3. `go test ./... -race` passes.
4. Scan the new code for the four rule classes above. Specifically:
   - Grep new function signatures: `grep -nE '^func.*\(.*,.*,.*,.*\)' app/<path>/*.go` — any hit with 4+ comma-separated params (excluding `ctx`) is a violation. Same for the return-value side.
   - For every new standalone helper, `grep -rn 'helperName(' --include='*.go'` and confirm at least one caller is NOT a method of a single type. If all callers are methods of one type, convert.
   - For every new exported identifier, grep cross-package. If no out-of-package hit, lowercase it.
5. Only after 1–4 pass: mark the task complete.

If a previous task shipped a violation (spotted later by user, reviewer, or yourself): fix it in the next commit BEFORE starting the next task. Do not let violations accumulate.

## Testing Strategy

- **unit tests**: required for every task (see Development Approach above)
- **no e2e/UI harness**: the TUI is tested through the bubbletea `Model`'s `Update`/`View` with synthetic
  messages — never by driving a real terminal.
- **the supervisor must be testable without spawning `claude`.** Every executor test drives a mocked
  `CommandRunner` returning a recorded `stream-json` fixture from `app/executor/testdata/`. Fixtures:
  clean run ending in `result` with `structured_output`; stall mid-stream; `rate_limit_event` with
  `status != allowed`; `modelUsage` disagreeing with the requested model; malformed/truncated stream.
  Only the clean capture is recorded live (see Prerequisites); the rest are derived from its bytes in a
  test helper, which keeps them real CLI output while keeping the build autonomous.
- **the pipeline must be testable without a terminal.** It emits typed events on a channel and takes agent
  runners through an interface, so pipeline tests use mocks and assert on the event sequence and report.
- **no live-model tests.** Any test that would actually spawn `claude` or `codex` is a manual verification
  step, not a test.
- **never touch the real user config directory.** Every filesystem test uses `t.TempDir()`; precedence
  tests must be pointed at temp dirs explicitly rather than relying on default lookup.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

**Pipeline — three fixed stages.** Only the roster and severity bar vary between review shapes, so the
stage structure is hardcoded and everything else is configuration.

1. **find** — the profile's roster of agents (lens-composing claude processes plus codex), all parallel,
   launched staggered. Each returns structured findings.
2. **synthesize** — one model stage. The pipeline passes it the *true* source roster as data (which
   processes ran, which degraded, which emitted each finding) rather than letting the model infer it.
3. **verify** — parallel agents grouped per directory, thin directories merged, capped. Each verifier sees
   only its own group so it cannot anchor on a neighbouring finding.

`--no-synthesis` and `--no-verify` skip a stage. Each stage is its own unexported type; `Pipeline.Run` is a
thin three-call orchestrator so no single type accumulates all three stages plus event fan-out plus I/O.

**Executor and lens are orthogonal.** Every roster entry composes lenses; `executor` only selects which
binary runs it (`claude` default, or `codex`). There is no codex-specific prompt file — codex is an entry
with `executor: codex` composing `lenses/adversarial.md`. The one real difference — claude gets its output
contract from `--json-schema`, codex needs "return only JSON matching this shape" appended — is injected by
the **executor**, never authored into a lens file.

**Codex is a peer, not a second pass.** It runs in parallel with the lens agents and its findings go
through the same synthesis and verification. Primary/secondary ordering would mean the second reviewer sees
the first's findings and anchors on them, destroying the independence the cross-source confidence boost
depends on. The fix-and-re-review loop lives in the caller, which re-runs revmux against the same `--task`
under a new `--run` name; revmux injects the prior rounds itself, with the independence instruction attached.

**Shared executor base.** `Claude` and `Codex` need the same run loop, idle watchdog, process-group
teardown and line reader. Duplicating that produces two near-identical `Run` bodies which `dupl` fails in
lint. An unexported `proc` struct holds the shared machinery; each executor supplies only its `args()` and
its output parsing. Model and effort travel on the **per-run request**, not construction-time options — one
executor instance has to serve roster entries with different models.

**Staggered launch.** Agent 1 goes immediately and the rest are released once it produces its first stream
event, or after `stagger_delay` if it never does. It must never influence which agents run, on which models,
or in what order — roster composition is a review-quality decision.

**Token accounting, not cost modelling.** The `result` event carries per-model `usage`, so revmux records
tokens per agent and summed per run and reports them. It does not estimate, optimize or reason about spend.

**Degrade, never abort.** Stall → kill → retry once → second failure marks the agent `degraded` and the
pipeline continues. The report banner names the missing lens and synthesis is told the real source count.
When the run is degraded, findings the drop rule would discard are routed to the verifier instead, because
corroboration is rarer with a source missing and the verifier is the authority anyway.

**Headless core, UI as a subscriber.** The pipeline emits typed events on a channel and knows nothing about
terminals. The TUI is one subscriber and `--no-tui` is the other — exactly one reader, never both, and the
archive is not a third. The TUI never writes stdout: `Pipeline.Run` returns the report to `package main`,
which sends it to the TUI as a completion message for display and writes it to stdout itself once the
bubbletea program has returned.

## Technical Details

**Config tree** (`go:embed` defaults, overridable per file at `./.revmux/` then `~/.config/revmux/`):

```
~/.config/revmux/
├── config                    # INI, RUNTIME KNOBS ONLY: timeouts, stagger, tasks_dir, keep_runs, max_parallel
├── prompts/
│   ├── profiles/
│   │   ├── comprehensive.md  # roster front matter + shared preamble + severity bar
│   │   ├── focused.md
│   │   └── final.md
│   ├── synthesis.md
│   └── verify.md
└── lenses/
    ├── bugs.md  impl.md  architecture.md
    └── quality.md  docs.md  tests.md  adversarial.md
```

Two precedence chains, not one. **Runtime knobs**: CLI flag > `config` at `./.revmux/` > `~/.config/revmux/`
> compiled-in default. **Prompt and lens files**: `./.revmux/` > `~/.config/revmux/` > `go:embed` default,
resolved per file — there is no per-file CLI flag, since `--profile` names a profile rather than a path and
`--config-dir` relocates the whole tree.

**Task directory** — the only channel review context travels through. `--task <id>` selects one under
`--tasks-dir` (default `./.revmux/tasks`, relocatable to `/tmp` or anywhere else), and `--run <name>` names
the round inside it. Both are caller-chosen and semantic (`--task pr-123 --run after-fix`); revmux allocates
neither. A fix-and-re-review loop re-runs one task under successive run names and accumulates rounds:

```
<tasks-root>/pr-123/          # caller-owned, revmux never writes or prunes anything here
├── scope.md                  # → {{SCOPE}}    required; missing or empty is a load-time error
├── goal.md                   # → {{GOAL}}     optional
├── profile.md                # → {{PROFILE}}  optional
├── context/                  # → {{CONTEXT}}  optional dir: ticket text, design notes, spec excerpts
│                             #   NOT prior rounds — those live in runs/ and revmux injects them
└── runs/                     # revmux-owned: the only thing it writes, and all keep_runs prunes
    └── after-fix/            # --run, caller-named; defaults to a UTC timestamp when omitted
        ├── manifest.json     # roster, prompt provenance + hashes, requested vs actual model, timings
        ├── prompts/          # composed prompt per agent and per stage, post-substitution
        │   ├── agents/       # separate from stages/ so an agent named `verify` cannot collide
        │   │   ├── bugs+impl.md
        │   │   └── codex.md
        │   └── stages/
        │       ├── synthesis.md
        │       ├── verify-app-executor.md   # one per directory group, not one per stage
        │       └── verify-app-pipeline.md
        ├── stages/           # findings after each stage
        │   ├── 1-found.json
        │   ├── 2-synthesized.json
        │   └── 3-verified.json
        ├── events.jsonl      # revmux's own decisions: stalls, retries, degrades, stage changes
        ├── agents/           # verbatim tees; own subdir so an agent named `events` cannot collide
        │   ├── bugs+impl.jsonl        # claude stream-json
        │   ├── bugs+impl.retry.jsonl  # a retried agent keeps both attempts
        │   └── codex.log              # codex prose
        ├── report.md
        └── findings.json
```

There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags. Since variables expand to
paths, a flag carrying inline text could not be substituted without revmux writing it to a file first, which
would make revmux an author of context rather than a consumer of it. One mechanism, no precedence rules.

**Profile front matter** — the roster. Top-level keys are defaults; per-entry values override:

```yaml
---
model: opus
effort: high
description: all six lenses across three agents plus an adversarial codex peer
agents:
  - {name: bugs+impl,    lenses: [bugs, impl],            color: cyan}
  - {name: arch+quality, lenses: [architecture, quality], color: magenta}
  - {name: docs+tests,   lenses: [docs, tests],           color: green}
  - {name: codex, executor: codex, lenses: [adversarial],
     model: gpt-5.6-sol, effort: xhigh, color: yellow}
---
Apply every lens you carry in full, and tag each finding with the lens that raised it.
Report problems only...
```

`executor` accepts only `claude` (default when omitted) and `codex`; anything else is a load-time config
error. `synthesis.md` and `verify.md` carry the same three keys in their own front matter.

`description` is the one-liner `revmux config` reports, and `color` is the agent's prefix color in both
renderers — an ANSI-16 name or `#RRGGBB`, filled from a palette by roster position when omitted.

**Composed prompt** for one agent = profile body + each of its lens files + `{{VAR}}` substitution.
Variables: `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}`, and for later stages
`{{FINDINGS}}` and `{{SOURCES}}`. Context variables expand to **absolute paths**, not to file contents —
`{{SCOPE}}` is the path of `scope.md` and the profile body tells agents to read it. revmux stats those files
and never opens one, so no prompt can be bloated by a large scope and the never-embed rule needs no
per-variable judgment call. The vocabulary is closed; a lens naming an unknown variable is a load-time error.

**Prior rounds are injected, not a variable.** After substitution the composer appends a history block to
every composed prompt — the task's `runs/` path plus a generated one-line inventory per round (name,
timestamp, finding counts by severity, degraded sources) so an agent can judge relevance without opening
anything. A `{{VAR}}` would be opt-in per file and any lens omitting it would silently lose the history, so
this works like the codex output contract: injected by revmux, never authored into a lens. The block carries
its own re-evaluate-independently instruction, because data and guard must not be separable — an agent told a
prior round flagged something tends to confirm it rather than judge it, which is the anchoring failure that
makes codex a peer rather than a second pass. On a first round the block is omitted entirely.

**Synthesis rules** (authored in `prompts/synthesis.md`, executed by the model): split open questions and
pre-existing issues out first; dedupe on `(file, line ±2)` with similar descriptions; confidence boost
`min(99, max_conf + 10*(N-1))` over distinct sources; severity = max; drop single-source below confidence
80 with no corroboration; when the run is degraded, route would-be-drops to the verifier instead of
dropping.

**Claude invocation:**

```
claude --print --output-format stream-json --verbose
       --model <m> --effort <e>
       --permission-mode dontAsk
       --disallowedTools "Edit,Write,NotebookEdit"
       --disable-slash-commands
       --no-session-persistence
       --json-schema <findings schema>
       < prompt
```

Strip `CLAUDECODE` from the child environment always (revmux is normally launched from inside an AI coding
session and the child refuses to start as a nested session). Strip `ANTHROPIC_API_KEY` by default.

**Codex invocation:** `codex exec --sandbox read-only -m <model> -c model_reasoning_effort=<effort>`. Its
stdout is not stream-json, so the watchdog ticks on raw writes and JSON is extracted from the final block.

**Output.** Findings to **stdout** — markdown by default, `--json` for the machine shape below. TUI to the
tty, progress to stderr, so `revmux --json > findings.json` works with the TUI running. The TUI is gated on
the tty being openable, never on stdout being a TTY.

```json
{ "scope": {"task":"pr-123","run":"after-fix","scope_path":"/abs/.../scope.md"},
  "sources": {"expected":4,"reported":3,"degraded":["docs+tests"],
              "agents":[{"name":"bugs+impl","lenses":["bugs","impl"],"executor":"claude",
                         "requested_model":"opus","actual_model":"claude-opus-5",
                         "effort":"high","tokens":48210,"degraded":false}]},
  "findings":[{"id","file","line","end_line","severity","confidence","title","body","fix",
               "sources":["bugs+impl","codex"], "lenses":["bugs","adversarial"],
               "verdict":"confirmed"}],
  "open_questions":[...], "pre_existing":[...], "immaterial":[...],
  "stats":{"started_at":"2026-07-26T16:02:11Z","finished_at":"2026-07-26T16:07:44Z",
           "duration_ms":…,"tokens":…,
           "stages":[{"name":"find","duration_ms":…},{"name":"synthesis","duration_ms":…}]} }
```

`line` is the anchor and `end_line` is optional — zero means a single line, and a zero `line` means a
file-level finding that renders as the bare path. The synthesis dedupe rule stays keyed on the anchor, so two
findings about one block merge even when they bracket it differently.

`sources` holds **agent names** and is the only input to the confidence boost; `lenses` holds the lens names
that raised the finding and is informational. The two must never be conflated — a `sources` array holding
lens names would let one agent carrying two lenses look like two corroborating sources, inflating confidence
on exactly the case the "a source is a process" rule exists to catch. There is no `tags` field: open
questions and pre-existing issues are already separate top-level lists, so nothing is left for it to hold.

`--min-confidence` filters once in `package main`; the rendered report and the exit code are both computed
from the filtered set. Exit codes: `0` no findings above threshold, `1` findings above threshold, `2` tool
error.

**Run artifacts:** written under the task directory's `runs/<run>/`, where the name comes from `--run` and
defaults to a UTC timestamp. A name that already exists is a load-time error, never an overwrite — a round
that went badly is exactly the one a reflection agent wants to read. Pruned to `keep_runs` (default 10) by
mtime, and pruning only ever touches `runs/`, never the caller's context files.

A run archive serves two consumers. The first is a human debugging a bad review, who needs to know what each
model said (`agents/<agent>.jsonl` / `.log`, verbatim tees) and what revmux decided (`events.jsonl` — stalls,
retries, degrades, stage transitions; neither is derivable from the other).

The second is a **self-reflection agent** that reads a task's accumulated runs and proposes changes to the
lens and profile text. That is a later, separate deliverable, but the data it needs must be captured from the
first run or the corpus is worthless. It has to answer two questions the final report cannot:

- *which lens text raised this finding* — needs `prompts/agents/<agent>.md`, the composed prompt post-substitution,
  exactly the bytes the model saw, plus `manifest.json` recording which of the three precedence layers each
  lens file came from and its content hash, since two runs of one task can use different lens text
- *did synthesis or verify drop something real* — needs `stages/`, the findings snapshot after each stage.
  Reconstructing the pre-synthesis set by re-parsing every agent's `structured_output` is possible but
  fragile, and impossible at all for a degraded source

`manifest.json` also records requested-vs-actual model per agent, since `--model` can be silently ignored and
a reflection agent drawing conclusions about "the opus lens" needs to know what actually ran.

## Prerequisites (one-time, before autonomous execution starts)

Two live CLI captures must exist before task 3 runs. They are the only thing in this build that cannot be
produced autonomously: a live capture has to run in a terminal session separate from the executing agent's
own tool shell, because a nested launch is blocked by the host permission layer and fails **silently** —
which would otherwise produce a fixture that looks recorded but is empty.

- `app/executor/testdata/claude-clean.jsonl` — one full run ending in a `result` event carrying
  `structured_output`. **The capture must pass `--json-schema`**, because that flag is what produces
  `structured_output` at all; a run without it yields a capture the task 3 validation rejects, and the
  build stops on a step no agent can redo. The schema passed must be the finder's, so the recorded
  `structured_output` has the shape task 3 and task 6 parse — a capture taken under some other schema makes
  both tests vacuous. Concretely:

  ```
  claude --print --output-format stream-json --verbose \
         --json-schema "$(cat app/executor/testdata/finder-schema.json)" \
         < some-review-prompt.txt > app/executor/testdata/claude-clean.jsonl
  ```

  ⚠️ `--json-schema` takes the schema JSON as the argument value, not a path — an earlier draft of this
  block passed the path and the capture failed with an empty stdout, which is indistinguishable from an
  agent that found nothing. Recorded in `.claude/rules/executor.md`.

- `app/executor/testdata/finder-schema.json` — the schema used for that capture, hand-written and committed
  alongside it. **This file is authoritative and task 2 conforms to it**, not the reverse: `FinderSchema()`
  returns these exact bytes, by embedding a copy of the file or carrying it as a raw string literal. Saying
  only "assert they match" would leave task 2's implementer facing a failing test with two equally licensed
  fixes — edit the Go schema, or edit the fixture the capture was recorded under, which silently invalidates
  the capture. Naming the authority makes the assertion a guard instead of a negotiation.
- `app/executor/testdata/codex-clean.txt` — one full `codex exec --sandbox read-only` run whose output
  contains a JSON block matching that same schema

Every other fixture is derived from the two captures in a `_test.go` helper (tasks 3 and 8), so re-recording
either regenerates its whole family and nothing else in the plan needs a human. See
`.claude/rules/testing.md` for why derived-from-real satisfies the no-hand-written-fixtures rule.

Ordering note: the schema file is a Prerequisite input but task 2 is where `FinderSchema()` is written.
Task 2 therefore carries a checkbox asserting the two agree, and that assertion is the only thing binding
the recorded fixture to the code that will later parse it.

**Task 3 starts by validating both captures and stops the build if either fails.** Silent failure is the
whole hazard here: an empty or truncated capture produces derived fixtures that are also empty, and the
executor tests then pass against nothing. The check is cheap — `claude-clean.jsonl` must parse as one
stream-json object per line and end in a `result` event whose `structured_output` is present, and
`codex-clean.txt` must be non-empty and contain an extractable JSON block. A capture failing either check
is a stop-and-ask, never a "re-record it myself" — the agent cannot run the capture.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates
- **Post-Completion** (no checkboxes): items requiring external action - manual testing, changes in consuming projects, deployment configs, third-party verifications

## Implementation Steps

### Task 1: Bootstrap the Go project

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `README.md`
- Create: `.github/workflows/ci.yml`
- Create: `app/main.go`
- Already present: `.gitignore`, `LICENSE`

Module path `github.com/umputun/revmux`, binary built from `./app`. Makefile targets: `build`, `test`
(race + coverage excluding mocks), `lint`, `fmt`, `race`, `version`, with
`-ldflags "-X main.revision=$(REV) -s -w"` revision stamping.

- [x] scaffold module, Makefile, `.golangci.yml` and CI workflow
- [x] set module path to `github.com/umputun/revmux` and build target to `./app`
- [x] record the intended versions (go 1.26, bubbletea v1.3.10, bubbles v1.0.0, lipgloss v1.1.0, go-flags v1.6.1, testify v1.11.1, yaml.v3) but **add each module at the task that first imports it**, not here. `go mod tidy` drops every requirement nothing imports, so pinning all seven now means the bubbletea pins are gone long before task 12 adds the first import. If a version turns out not to exist, that fails at the task that needs it rather than blocking task 1
- [x] add `app/main.go` printing version and exiting, so `make build` and `make test` are green from the start
- [x] write a smoke test asserting the binary's version output
- [x] run `go mod vendor`
- [x] **`vendor/` is committed, so every later task adding a first-time import must re-run `go mod vendor` before its build step.** With a `vendor/` directory present Go builds `-mod=vendor` and fails outright on any import missing from `vendor/modules.txt`. That lands on task 4 (`yaml.v3`), task 5 (`go-flags`) and task 12 (`bubbletea`, `bubbles`, `lipgloss`)
- [x] run tests, lint and formatter - must pass before task 2

### Task 2: Finding types and report rendering

**Files:**
- Create: `app/finding/finding.go`, `app/finding/report.go`, `app/finding/schema.go`
- Create: `app/finding/finding_test.go`, `app/finding/report_test.go`, `app/finding/schema_test.go`

The data contract every other package produces or consumes. Built first so nothing downstream invents its
own shape.

**Design Contract:**

Type:
- `Finding` (exported — consumed by `app/pipeline`, `app/ui`, `package main`)
- `Report` (exported — same consumers; carries `Findings`, `OpenQuestions`, `PreExisting`, `Immaterial`, all `[]Finding`, plus `Scope`, `SourceStatus`, `Stats`)
- `Scope` (exported — run identity: `Task`, `Run`, `ScopePath`; field of `Report`)
- `Severity` (exported — enum: `critical`, `major`, `minor`)
- `Verdict` (exported — enum: `confirmed`, `refined`, `rejected`, `immaterial`, `pre_existing`, `unverified`).
  `pre_existing` is in the set because task 10 moves pre-existing findings to their own list and
  `VerifySchema` is the model's only channel for saying so; without the value that routing has no input
- `SourceStatus` (exported — field of `Report`): `Expected int`, `Reported int`,
  `DegradedSources []string` tagged `json:"degraded"`, `Agents []SourceStat`.
  The Go field is **not** named `Degraded`, because `Degraded()` is a method on this same type and the two
  cannot share a name; the JSON key stays `degraded` via the tag, so the documented wire shape is unchanged
- `SourceStat` (exported — one agent's outcome for reporting: `Name`, `Lenses`, `Executor`, `RequestedModel`, `ActualModel`, `Effort`, `Tokens`, `Degraded`; slice field of `SourceStatus`).
  `Lenses` and `Effort` are here because task 11's `manifest.json` records the resolved roster and
  `package main` builds that section from `SourceStat` alone
- `Stats` (exported — field of `Report`): `StartedAt`, `FinishedAt`, `DurationMS`, `Tokens`,
  and `Stages []StageTiming`
- `StageTiming` (exported — `Name`, `DurationMS`; slice field of `Stats`).
  `manifest.json` records per-stage timings and `History` renders each prior round's timestamp; neither is
  derivable from a report that carries only a total duration

Methods (full signatures):
- `(r Report) Markdown(w io.Writer) error`
- `(r Report) JSON(w io.Writer) error`
- `(r Report) Above(minConfidence int) Report`
- `(r Report) ExitCode() int`
- `(s SourceStatus) Degraded() bool`

`Above` returns a filtered `Report`, not a slice — `package main` filters once and both the renderers and
`ExitCode` operate on that same filtered value, so the printed report can never disagree with the exit code.

`OpenQuestions`, `PreExisting` and `Immaterial` are `[]Finding` on `Report`, not separate types. `pipeline.md`
says the first two are never boosted, dropped or verified, so `Above` must pass all three through untouched —
filtering them by confidence would silently discard exactly the material the reviewer most needs to see.

`SourceStat` is what carries per-agent tokens and requested-vs-actual model out of `app/pipeline`. Without it
they are stranded on the unexported `sourceResult`, while task 11 writes them to `manifest.json` and task 16
verifies them in the report — and per-agent token counts are one of the four things the Overview says a
subprocess buys, so dropping them silently is a scope loss rather than a detail.

**Three schemas, not one.** The stages want different shapes, and a single schema cannot serve them:

Standalone helpers planned (justification why NOT a method):
- `FinderSchema() json.RawMessage` — the contract a lens agent returns. Deliberately a **subset** of
  `Finding`: `sources` and `verdict` are omitted, because Go assigns those and a field the model can fill is
  a field it will fill
- `SynthesisSchema() json.RawMessage` — findings plus `open_questions` and `pre_existing`, and per output
  finding the **input finding ids it merged**. Go derives the `sources` and `lenses` unions from those ids,
  so the merge stays semantic while attribution stays machine-assigned
- `VerifySchema() json.RawMessage` — finding id, verdict, and the fields a `refined` verdict may rewrite
- all three are entry points consumed by `app/executor`, which holds no `Finding` value to call a method on

Applying the finder's omission to all three was the error: `synthesizer.parse` returns a `finding.Report`, a
three-list shape the finder schema does not describe at all, and task 10 requires the model to return a
verdict per finding, which that schema forbids.

Exports (justification per item: who outside the package calls this?):
- `Finding`, `Report`, `Scope`, `SourceStatus`, `SourceStat`, `Stats`, `Severity`, `Verdict` — `app/pipeline` builds them, `app/ui` renders them, `package main` writes them and reads `SourceStat` for `manifest.json`
- `Report.Markdown` / `Report.JSON` / `Report.Above` / `Report.ExitCode` — called from `app/main.go`
- `SourceStatus.Degraded` — called from `app/pipeline` synthesis and from `app/ui` for the banner
- `FinderSchema` / `SynthesisSchema` / `VerifySchema` — called from `app/pipeline` to build each stage's `--json-schema` argument

- [x] create `app/finding/finding.go` with `Finding` (id, file, line, end_line, severity, confidence, title, body, fix, `sources` agent names, `lenses` lens names, verdict) — no `tags` field, and `sources` never holds a lens name
- [x] `line` is the anchor and `end_line` is optional: zero means a single line, and `line` itself zero means a file-level finding with no line at all
- [x] create `app/finding/report.go` with `Report` (`Findings`, `OpenQuestions`, `PreExisting`, `Immaterial`, `Scope`, `SourceStatus`, `Stats`), `Scope`, `SourceStatus`, `SourceStat`, `Stats`, `StageTiming`, and the `Degraded()` method; `Stats` carries start, finish, duration, the run's token total and per-stage timings, `SourceStat` carries the per-agent breakdown including lenses and effort
- [x] implement `Report.Markdown` grouping by severity with a degraded-sources banner when `Degraded()`, rendering open questions, pre-existing and immaterial as their own sections, and printing per-agent tokens and actual model from `SourceStat`
- [x] implement `Report.JSON` emitting the documented shape, plus `Above` and `ExitCode`; `Above` filters `Findings` only and passes the other three lists through untouched
- [x] create `app/finding/schema.go` with `FinderSchema()`, `SynthesisSchema()` and `VerifySchema()` — the finder's omits `sources` and `verdict`, synthesis returns merged input ids so Go derives the unions, verify returns id plus verdict
- [x] write tests for markdown rendering (with and without degraded banner, empty report, all severities, and both extra sections)
- [x] write tests for JSON round-trip, `Above` filtering and `ExitCode` mapping (0/1)
- [x] write a test asserting `Above` does not drop open questions, pre-existing or immaterial findings even below the threshold
- [x] write a test asserting all three schemas are valid JSON; that **no** schema exposes `sources`; that `FinderSchema` also omits `verdict` while `VerifySchema` requires one; that `SynthesisSchema` describes the three-list shape and carries merged input ids
- [x] `FinderSchema()` returns the bytes of `app/executor/testdata/finder-schema.json` — the file the prerequisite capture was recorded under — by embedding a copy of it under `app/finding/`. Write a test asserting the two are identical. That binding is the only thing tying the recorded `structured_output` shape to the code that parses it; if they drift, every executor and find-stage test asserts against a shape the CLI will never emit. **The fixture wins any disagreement** — changing it invalidates a capture no agent can retake
- [x] write a test asserting `Stats` round-trips start, finish and per-stage timings, since `manifest.json` and the prior-round history both read them and neither can recompute them
- [x] write a test covering every `Verdict` and `Severity` value in markdown rendering, so an unhandled enum case cannot ship silently
- [x] write a test asserting `SourceStat` survives a JSON round-trip with per-agent tokens and both model fields, since `manifest.json` and task 16 both read them
- [x] write a test asserting `sources` and `lenses` are distinct fields that survive a JSON round-trip independently, so a future refactor cannot quietly merge them
- [x] write tests for location rendering across all three shapes: single line, `line`-`end_line` range, and file-level with no line
- [x] run tests - must pass before task 3

### Task 3: Supervised claude executor

**Files:**
- Create: `app/executor/executor.go`, `app/executor/proc.go`, `app/executor/claude.go`, `app/executor/stream.go`
- Create: `app/executor/procgroup_unix.go`, `app/executor/procgroup_windows.go`
- Create: matching `_test.go` for each
- Create: `app/executor/mocks/`
- Already present from the Prerequisites, **not created here**: `app/executor/testdata/claude-clean.jsonl`,
  `app/executor/testdata/codex-clean.txt`, `app/executor/testdata/finder-schema.json`.
  Every other fixture is derived from those inside a `_test.go` helper, never written to disk
- ➕ Also captured: `app/executor/testdata/codex-clean.err.txt`, codex's stderr from the same run. Task 8
  filters that stream down to the `model:` / `sandbox:` / `reasoning effort:` header lines and reads the
  session id out of it; neither is derivable from stdout, so recording it now avoids a second live capture
  no agent can run. Home-directory paths in all three captures are rewritten to `/home/reviewer` — the repo
  ships publicly and nothing in the wire shape depends on the path
- ➕ `app/executor/procgroup.go` — `processGroupCleanup` itself, untagged so both builds see one type. The
  plan named the file pair but not the shared declaration's home

**Design Contract:**

Type:
- `CommandRunner` (exported interface — consumer-side, mocked in tests)
- `EventSink` (exported interface — implemented by an adapter in `app/pipeline`)
- `Event` (exported — one activity item emitted to the sink)
- `Opts` (exported — construction options: timeouts, working directory, `PreserveAPIKey bool`, `Clock`)
- `Clock` (exported interface, consumer-side — `Now() time.Time`, `AfterFunc(time.Duration, func()) Timer`)
- `Timer` (exported interface — `Stop() bool`, `Reset(time.Duration) bool`; the watchdog resets it per line, the teardown stops it)
- `realClock` / `realRunner` (unexported — the production implementations, reached through the constructors below)
- `Request` (exported — per-run inputs: prompt, model, effort, schema, `RawOutput io.Writer`)
- `Result` (exported — returned to `app/pipeline`)
- `Claude` (exported — the claude executor, embeds `proc`)
- `proc` (unexported — shared run loop, idle watchdog, process-group teardown, line reader; **immutable configuration only**)
- `procRun` (unexported — one process's live state: stdout, exit status, the `*processGroupCleanup` handle; task 8 adds stderr)
- `streamEvent` (unexported — wire shape of one stream-json line)
- `processGroupCleanup` (unexported)

Methods (full signatures):
- `(c *Claude) Run(ctx context.Context, req Request, sink EventSink) (Result, error)`
- `(c *Claude) args(req Request) []string`
- `(c *Claude) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result`
- `(c *Claude) event(line string) (streamEvent, bool)`
- `(p *proc) start(ctx context.Context, argv []string, prompt string) (*procRun, error)`
- `(p *proc) readLines(ctx context.Context, r io.Reader, handler func(string)) error`
- `(p *proc) setupProcessGroup(cmd *exec.Cmd)`
- `(p *proc) childEnv() []string`
- `(pg *processGroupCleanup) wait() error`
- `(pg *processGroupCleanup) watchForCancel(cancelCh <-chan struct{})`
- `(pg *processGroupCleanup) killProcessGroup()`

Model and effort live on `Request`, not `Opts`, so one executor instance serves roster entries with
different models.

`Request.Schema` is set for **both** executors and carries the stage's schema from `app/finding`.
Only the delivery differs: claude passes it to `--json-schema`, and task 8's codex renders the same bytes
into its prose contract. Leaving it empty for codex would let the two executors ask for different shapes,
and synthesis would then have to reconcile a codex finding against a schema nobody stated.

**`start` returns a per-run handle, and `proc` holds no per-run state — this is a correctness requirement,
not a style choice.** `Run` needs two things `start` would otherwise have to stash on the receiver: the
exit status (task 7 retries on process failure, task 8 tiers codex patterns only on non-zero exit) and the
`*processGroupCleanup` handle (executor.md requires killing the group on normal exit too). Task 8 adds a
third, stderr, and adds it as a field on this handle rather than as another signature change — which is
the second reason the handle exists. But `NewClaude` returns **one** instance that serves every roster
entry, and task 7 runs the
roster concurrently — so any field `start` writes is a data race on every parallel review. No planned test
would catch it: executor tests are single-run, and pipeline tests mock `pipeline.Runner`, so two concurrent
`Claude.Run` calls on one instance never happen under `-race`. Returning `*procRun` makes the race
impossible by construction and absorbs task 8's stderr addition without changing the signature again.

`Result` fields, since four tasks read them: `StructuredOutput json.RawMessage`, `Raw string`,
`ExitCode int`, `IdleTimedOut bool`, `RateLimited bool` plus the `rate_limit_event` payload,
`RequestedModel` / `ActualModel string`, `Tokens int`, `TTFTMs int`.

`Request.RawOutput` is what makes the task 11 archive possible. `proc` tees every byte to it **before**
parsing, so a caller can keep byte-identical claude stream-json or codex prose. Without it raw output is
consumed inside `proc` and the per-executor parsers, and re-serializing parsed events yields a paraphrase
rather than the artifact — the tee has to exist at this task or task 11 cannot deliver what it promises.

`Clock` is injected because `.claude/rules/testing.md` bans wall-clock waits in tests. A recorded fixture
that simply ends is EOF, not a stall, so proving the idle watchdog fires needs both a fake clock the test
advances and a `CommandRunner` that emits fixture bytes then blocks until cancellation.

Standalone helpers planned (justification why NOT a method):
- `NewClaude(runner CommandRunner, opts Opts) *Claude` — constructor
- `NewRunner() CommandRunner` — the production `exec.Cmd`-backed runner. Without it nothing in the plan ever
  creates one: every other mention is the interface, a moq directive, or "mocked in tests", so the wiring
  agent would have to invent it
- `NewClock() Clock` — the production `time`-backed clock, for the same reason. **`Opts.Clock` must never be
  left nil**: every test injects a fake, so a nil clock passes the entire suite and panics on the first
  `AfterFunc` in a real run — the watchdog path, which is the tool's stated reason for existing.
  **Decision: `NewClaude` and `NewCodex` substitute `NewClock()` when `Opts.Clock` is nil.** A required
  field would push the obligation onto every construction site, and the composition root builds
  `executor.Opts` from `options`, which has no clock on it — so the production path would ship the nil
- `newProcessGroupCleanup(cmd *exec.Cmd, cancelCh <-chan struct{}) *processGroupCleanup` — constructor

Exports (justification per item: who outside the package calls this?):
- `CommandRunner`, `Opts`, `Request`, `Result`, `Claude`, `NewClaude` — constructed in `package main`, called from `app/pipeline`
- `Clock`, `Timer`, `NewRunner`, `NewClock` — `package main` supplies the production runner and clock; `app/pipeline` and `app/archive` take the same `Clock` so one fake drives every timing path in a test
- `EventSink`, `Event` — `app/pipeline`'s adapter implements the interface and consumes the event

- [x] validate both prerequisite captures before writing any code: `testdata/claude-clean.jsonl` parses line-by-line and ends in a `result` event carrying `structured_output`, `testdata/codex-clean.txt` is non-empty and holds an extractable JSON block. Either failing stops the build and asks — a silently-empty capture makes every derived fixture empty and every executor test vacuous
- [x] create `app/executor/executor.go` with `CommandRunner`, `EventSink`, `Event`, `Clock`, `Opts`, `Request`, `Result` and the moq `go:generate` directive
- [x] create `app/executor/proc.go` with the shared `proc`: start, idle-timeout arming and reset via the injected `Clock`, hard timeout, child-env scrubbing (`CLAUDECODE` always, `ANTHROPIC_API_KEY` unless `Opts.PreserveAPIKey`), a verbatim tee to `Request.RawOutput` before parsing, and `readLines` as a method (no standalone `linereader.go`)
- [x] create `app/executor/procgroup_unix.go` / `procgroup_windows.go` with `Setsid`, SIGTERM→grace→SIGKILL on the process group, early return on `ESRCH`, and kill-on-normal-exit to reap orphans; declare `processGroupCleanup` in a shared untagged file so both builds see one type
- [x] create `app/executor/stream.go` decoding stream-json lines, extracting `structured_output`, `modelUsage`, per-model `usage` token counts and `rate_limit_event`
- [x] create `app/executor/claude.go` embedding `proc`, building flags from `Opts` + `Request`, setting `Result.IdleTimedOut` when the derived context fired but the parent is alive
- [x] derive the remaining claude fixtures from `testdata/claude-clean.jsonl` in a `_test.go` helper: truncated is the clean bytes cut mid-line, stalling is cut early, rate-limited and model-mismatch are the clean bytes with one field patched — real CLI output either way, and re-recording the clean capture regenerates all four
- [x] run `go generate ./...` to produce `app/executor/mocks/`
- [x] write tests for the clean run (findings extracted, tokens and model reported)
- [x] write tests for the stall fixture using a fake clock the test advances and a `CommandRunner` that emits fixture bytes then blocks until cancellation, asserting the idle timeout fires and `IdleTimedOut` is set with no error returned
- [x] write a test asserting `Request.RawOutput` receives bytes identical to what the runner produced, including for a stream that fails to parse
- [x] write tests for the rate-limit and truncated-stream fixtures
- [x] write a test asserting `Result` reports the model from `modelUsage`, not the requested one
- [x] write a test asserting `CLAUDECODE` is absent from the child environment, that `ANTHROPIC_API_KEY` is stripped by default, and that `Opts.PreserveAPIKey` passes it through — the flag that sets that field does not exist until task 5, which tests the flag-to-field mapping itself
- [x] write a test asserting a nil `Opts.Clock` is replaced by `NewClock()` rather than panicking on the first `AfterFunc`, since the composition root builds `Opts` from a struct that carries no clock
- [x] write a test running two `Claude.Run` calls concurrently on one instance under `-race`, proving `proc` holds no per-run state — the roster runs concurrently from task 7 on, and no other planned test exercises one executor instance from two goroutines
- [x] write tests for `args(req)` covering every flag including `--disable-slash-commands` and the schema
- [x] verify `GOOS=windows GOARCH=amd64 go build ./...`
- [x] run tests - must pass before task 4

### Task 4: Prompt loading, roster parsing and lens composition

**Files:**
- Create: `app/prompt/prompt.go`, `app/prompt/roster.go`, `app/prompt/compose.go`, `app/prompt/defaults.go`
- Create: matching `_test.go` for each
- Create: `app/prompt/defaults/prompts/profiles/focused.md`, `app/prompt/defaults/prompts/synthesis.md`, `app/prompt/defaults/prompts/verify.md`
- Create: `app/prompt/defaults/lenses/bugs.md`, `app/prompt/defaults/lenses/adversarial.md`

Ships a minimal but usable default set so the end-to-end slice in task 6 does real work. The full
seven-lens set lands in task 14.

**Design Contract:**

Type:
- `Set` (exported — the loaded prompt tree, held by `app/pipeline`)
- `Profile` (exported — a roster plus a body)
- `Stage` (exported — a body plus `executor`, `model`, `effort`; `synthesis.md` and `verify.md`, no roster)
- `AgentSpec` (exported — one roster entry)
- `Vars` (exported — `map[string]string`)
- `ComposeOpts` (exported — `Vars` plus the prior-round `History` block; built by `app/pipeline`)
- `FileOrigin` (exported — one loaded file's winning precedence layer and content hash; consumed by `app/archive`)
- `LoadOpts` (exported — search paths)
- `doc` (unexported — shared front-matter + body carrier behind `Profile` and `Stage`)
- `LensInfo` (exported — one lens's name and `description:` front matter; consumed by `package main` for `revmux config`)

`AgentSpec` fields: `Name string`, `Lenses []string`, `Executor string`, `Model string`, `Effort string`,
`Color string`.
`Executor` is a plain exported field, not a getter — the front-matter key is `executor` and the vocabulary
stays singular across all three layers.

`Color` is optional `color:` front matter in one of two forms: an ANSI-16 **name** (`black`, `red`, `green`,
`yellow`, `blue`, `magenta`, `cyan`, `white` and a `bright-` variant of each) or a `#RRGGBB` hex string.
lipgloss has no name lookup, so `app/prompt` maps names to indices `0`-`15` itself — that mapping is the
reason names exist, since an index is drawn from the user's terminal theme while hex overrides it. A raw
numeric index is not accepted: it says nothing to whoever edits the file and the equivalent name always
exists.

When omitted, `Roster` fills it from a fixed palette of those names **by roster position** rather than by
hashing the name, so two runs of one profile color an agent the same way even if the reviewer renames it.
It is resolved here rather than in `app/ui` because the plain `--no-tui` renderer prefixes agents too, and
a color picked inside the TUI would exist in one renderer only.

The **resolved** form stays a string both renderers can hand to lipgloss unchanged — an ANSI index
`"0"`-`"15"` or the original `#RRGGBB`. `app/prompt` therefore never imports lipgloss, and the rule that a
bare number fails at load applies to the *front-matter* value, not to what resolution produces.
`revmux config` reports the front-matter form where one was given, so a caller reads back `cyan` rather
than `"6"`; keep both on `AgentSpec` if one field cannot serve both.

Methods (full signatures):
- `(s *Set) Profile(name string) (*Profile, error)`
- `(s *Set) Stage(name string) (*Stage, error)`
- `(s *Set) lens(name string) (string, error)`
- `(s *Set) Lenses() []LensInfo`
- `(s *Set) LensNames() map[string]struct{}`
- `(s *Set) ProfileNames() []string`

`validate` and `Roster` take the resolved lens set because neither can otherwise satisfy its own contract:
"every lens named by a roster entry exists" is a load-time error, and `Profile` holds no back-reference to
its `Set` — if it did, `Compose(set *Set, …)` would not need the set passed in. `Load` already holds that
map and supplies it to `validate`; `Roster` is called from `package main`, which gets the same map from
`Set.LensNames()`. That accessor exists precisely so the composition root does not assemble a validation
input by hand out of `Lenses()`. Without the parameter, `--lenses nonexistent` survives load and fails later inside
`Compose`, which is exactly the "invalid values are rejected, never silently defaulted" rule inverted.
- `(p *Profile) Roster(lensOverride []string, known map[string]struct{}) ([]AgentSpec, error)`
- `(p *Profile) Compose(set *Set, spec AgentSpec, opts ComposeOpts) (string, error)`
- `(p *Profile) validate(known map[string]struct{}) error`
- `(st *Stage) Compose(opts ComposeOpts) (string, error)`
- `(s *Set) Provenance() []FileOrigin`

`Stage` carries `Executor`, `Model` and `Effort` as exported fields, exactly as `AgentSpec` does, because
`synthesis.md` and `verify.md` may name their own binary. `app/pipeline` builds a `RunnerSpec` from either
with a plain struct literal — it already imports `app/prompt`, and `app/prompt` must not import
`app/pipeline`, so no method or shared type crosses the boundary in the wrong direction.

`ComposeOpts` carries `Vars` and the prior-round `History` block. It exists so `Profile.Compose` stays at
three parameters rather than four, and so the history travels by the same route into both compose paths.

`Compose` hangs off `Profile` because the composed prompt is profile body + lens files; an `AgentSpec` alone
carries no body. `Stage.Compose` needs no `AgentSpec` at all.

`Provenance` reports which precedence layer each loaded file came from plus its content hash. `app/archive`
records it in `manifest.json` — without it a later reflection agent cannot tell whether two rounds of one
task ran the same lens text.

Every file's front matter may carry `description:`, a one-liner stored on `doc` and surfaced by `Lenses()`
and `Set.Profile`. It is what makes task 15's `revmux config` useful: a caller model composing a custom
`--lenses` set needs to know what `quality` covers without reading the lens body. It stays **optional** at
load so overriding a lens does not require re-authoring metadata; task 14 asserts every *shipped* file has
one. A description is never inherited from the embedded default when an override wins — an override is
different text, and describing it with the default's summary would be a lie.

Standalone helpers planned (justification why NOT a method):
- `Load(opts LoadOpts) (*Set, error)` — constructor
- `splitFrontMatter(b []byte) (meta []byte, body []byte, err error)` — called only by `Load`, which is itself
  a standalone constructor; parsing is eager so no `Set` method ever calls it
- `Efforts() []string` and `Executors() []string` — the accepted vocabularies, returned from the same
  package-level slices `validate` checks against. Task 15's `revmux config` reports them and `config.md`
  forbids a second hardcoded copy; without an accessor, task 15's "the catalog matches what `validate`
  accepts" test could only compare one literal to another and would verify nothing

Exports (justification per item: who outside the package calls this?):
- `Set`, `Profile`, `Stage`, `AgentSpec`, `Vars`, `LoadOpts`, `Load` — `package main` builds `LoadOpts` via
  `options.promptOpts()`, loads the set, and resolves the profile and roster; `app/pipeline` composes prompts
- `LensInfo`, `Set.Lenses`, `Set.ProfileNames`, `Efforts`, `Executors` — `package main` builds the `revmux config` catalog from them
- `Profile.Description`, `Stage.Description`, `LensInfo.Description` — exported fields, since `doc` is
  unexported and task 15 reports a description per profile, not only per lens
- `ComposeOpts` — `app/pipeline` constructs it for both compose paths
- `FileOrigin` — `app/archive` records it in `manifest.json`
- `Set.Provenance` — called from `package main` to build the manifest's prompt-provenance section
- `Set.Profile` / `Profile.Roster` / `Set.LensNames` — called from `package main`, which resolves the roster
  once and passes it on `Config.Roster`; task 6 states why it is not resolved inside the pipeline
- `Profile.Compose` / `Stage.Compose` / `Set.Stage` — called from `app/pipeline`

- [x] create `app/prompt/prompt.go` with `Set`, `LoadOpts`, `Load`, the unexported `doc`, and per-file precedence resolution
- [x] create `app/prompt/defaults.go` with the `go:embed` directive over `defaults/`
- [x] create `app/prompt/roster.go` parsing YAML front matter into `Profile` / `Stage` / `AgentSpec`, applying top-level model/effort as defaults and per-entry values as overrides; `Stage` parses `executor` too
- [x] implement `validate`: unknown executor, unknown effort, missing lens, duplicate agent name, an unparseable `color`, and empty roster are all load-time errors, for stage front matter as well as roster entries
- [x] fill an omitted `AgentSpec.Color` from a fixed palette by roster position in `Roster`, so every downstream renderer receives a resolved color and none of them picks its own
- [x] implement `Profile.Roster` applying a lens override while keeping the profile body: the override produces **one** agent carrying every named lens, inheriting the profile's top-level model and effort and running on claude, and a roster codex entry does not survive it. Name that synthesized agent `lenses` — the name reaches `Finding.sources` and becomes the filename `agents/<name>.jsonl`, so leaving it empty produces an unnamed source and a dotfile tee
- [x] reject a lens override naming a lens that does not exist, at load, using the same `known` map `validate` takes — otherwise `--lenses nonexistent` survives load and fails inside `Compose`
- [x] create `app/prompt/compose.go` with `Profile.Compose` and `Stage.Compose`, substituting `Vars` and failing on an unresolved `{{VAR}}`
- [x] **three distinct behaviors, each with one owner — do not merge them.** A `{{VAR}}` naming something outside the closed vocabulary is a load-time error (task 4). A known variable whose file is absent gets the "none provided" placeholder, and `app/config.go` puts that placeholder into `Vars` (task 5) — `Compose` never invents one. A `{{VAR}}` still present after substitution is a compose failure (task 4), which can now only mean a bug rather than missing context
- [x] append `ComposeOpts.History` to every composed prompt after substitution — an injection, never a `{{VAR}}`, so no lens or overridden profile can omit it; skip it entirely when there are no prior rounds
- [x] implement `Set.Provenance` returning the winning precedence layer and content hash per loaded file
- [x] parse the optional `description:` front-matter key on every file, expose it as an exported field on `Profile`, `Stage` and `LensInfo`, and implement `Set.Lenses` / `Set.ProfileNames` enumerating what actually resolved rather than what is embedded — a lens the user deleted still resolves from the embedded layer, and one the user added must appear
- [x] add `Efforts()` and `Executors()` returning the same package-level slices `validate` checks, so task 15 reports the vocabularies rather than duplicating them
- [x] author `synthesis.md` and `verify.md` with their own `description:` front matter alongside the profiles and lenses, since `prompts.md` requires every shipped file to carry one
- [x] author the minimal default `focused.md` profile, `bugs.md` and `adversarial.md` lenses, and `synthesis.md` / `verify.md` stage prompts
- [x] the profile body must tell agents that `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}` and `{{CONTEXT}}` are paths to read, not text — a path handed to an agent with no instruction is a path it may ignore
- [x] write tests for precedence (embedded, user override, project override, single-file override) using `t.TempDir()`
- [x] write tests for roster parsing including defaults, per-entry overrides and every validation error
- [x] write tests for `color`: every ANSI-16 name resolves to its index, a `#RRGGBB` value survives to `AgentSpec`, an omitted one is filled by roster position and is stable across two loads of the same profile, and a malformed value — including a bare number — fails at load
- [x] write a test asserting `Stage` parses and validates `executor`, so a stage naming an unknown binary fails at load rather than being silently ignored
- [x] write a test asserting `--lenses` with two lenses yields one agent carrying both, not two agents, since the alternative would change the source count
- [x] write tests for lens override, for composition with missing variables resolving to an explicit placeholder, and for the unresolved-variable failure
- [x] write a test asserting the history block reaches a composed prompt whose profile and lenses never mention it, and is absent when there are no prior rounds
- [x] write a test asserting the injected block carries its re-evaluate-independently instruction, so the data can never appear without the guard
- [x] write tests for `Provenance` reporting the correct layer and a hash that changes when an override file changes
- [x] write a test asserting `Lenses()` reports an override's own `description` rather than the embedded default's, and an empty one when the override omits it
- [x] add `gopkg.in/yaml.v3` and run `go mod vendor` — first import of it, and a committed `vendor/` makes the build fail without it
- [x] run tests - must pass before task 5

### Task 5: Configuration and CLI options

**Files:**
- Create: `app/config.go`, `app/config_test.go`
- Create: `app/defaults/config` — the commented-out INI template `--init` materializes, embedded from
  `app/config.go`. It belongs to `package main` rather than `app/prompt/defaults/`, which holds prompt
  content; putting it there would also force an exported accessor across a package boundary for no gain
- Modify: `app/main.go`

INI holds runtime knobs only. Everything shaping the review lives in the markdown.

**Design Contract:**

Type:
- `options` (unexported — go-flags struct, `package main` only)
- `reviewContext` (unexported — the resolved absolute paths for one task directory)
- `runOpts` (unexported — what `run` needs: the parsed `options`, the runner factory, the clock, the
  stdout/stderr writers, and `openTTY func() (*os.File, error)`)

`openTTY` is injected for the same reason the writers are: task 13 must assert the TUI is gated on the tty
being openable and **not** on stdout being a TTY, and `tui.md` forbids a test requiring a real terminal, so
the open has to be substitutable.

It returns `(*os.File, error)`, not `(io.Writer, error)`. bubbletea needs the tty for **input** as well as
output — the key bindings in tasks 12 and 13 otherwise depend on `os.Stdin` happening to be a terminal,
which is not a safe assumption for a binary whose stated caller is a model. One handle feeds both
`tea.WithInput` and `tea.WithOutput`, and `package main` closes it after the program returns; an
`io.Writer` gives no way to do either.

Methods (full signatures):
- `(o options) promptOpts() prompt.LoadOpts`
- `(o options) executorOpts(rc reviewContext, clk executor.Clock) executor.Opts` — the clock is a parameter
  because it lives on `runOpts`, not on `options`; without it the production path builds `executor.Opts`
  with a nil `Clock`. ➕ `rc` is a parameter for the same class of reason: the resolved `WorkDir` lives on
  `reviewContext`, and `config.md` requires the directory an agent is told to review to be the one its
  process runs in. Reading the raw `--workdir` flag here instead would give two resolutions that can
  disagree, and resolving it a second time inside `executorOpts` would need an error return it cannot have
- `(o options) resolveContext() (reviewContext, error)`
- `(o options) runName(clk executor.Clock) string` — ➕ the clock is a parameter because `testing.md` bans
  `time.Now()` in code under test, and the UTC-timestamp default is exactly what a test must pin
- `(rc reviewContext) vars() prompt.Vars`

`reviewContext` fields are `TaskDir`, `Scope`, `Goal`, `Profile`, `Context` and `WorkDir` — all absolute
paths, all resolved once. `TaskDir` is carried explicitly because `app/archive` needs it for both the run
directory and the prior-round history; re-deriving it with `filepath.Dir(Scope)` elsewhere means two
resolutions that can disagree. `WorkDir` comes from `--workdir` (defaulting to the process working
directory) rather than from the task directory, but it belongs here anyway: `vars` must emit `{{WORKDIR}}`,
and the whole justification for hanging `vars` off this struct is that every value it needs is already
resolved on it.

`resolveContext` returns that struct, never `(scope string, goal string, profile string, err error)`:
adjacent same-typed strings transpose silently and feed the project profile into `{{GOAL}}`.

`vars` hangs off `reviewContext` rather than `options` — every value it needs is already resolved there, so a
method on `options` would either re-stat the directory or take the struct as a parameter. `runName` resolves
`--run` to its timestamp default in `package main` per the composition-root rule, and the resolved value is
passed down rather than re-derived.

Standalone helpers planned (justification why NOT a method):
- `parseArgs(args []string) (options, error)` — entry point; runs before any `options` value exists.
  ➕ It takes the argv slice rather than reading `os.Args` so precedence and validation are testable
  without mutating process state
- `run(o runOpts) int` — the testable entry point. `main()` builds `runOpts` with the production runner
  factory, clock and `os.Stdout`/`os.Stderr`; tests build it with fakes. It takes an option struct rather
  than `(options, factory, stdout, stderr)` because that is four parameters, which this plan's own hard rule
  bans — and because without a stated seam the cheap alternative is a package-level `var` swapped in
  `_test.go`, which the maintainer's standing rule forbids ("never add production code that exists only to
  support tests"). Four tasks depend on this seam: task 6's whole-slice test, task 7's all-degraded exit 2,
  task 13's report-written-once and tty-gating tests, and task 15's catalog-output tests

Exports (justification per item: who outside the package calls this?):
- none — `package main` internals

- [x] create `app/config.go` with the `options` struct: `--task`, `--run`, `--tasks-dir`, `--profile`, `--lenses`, `--workdir`, `--min-confidence` (default 0), `--no-synthesis`, `--no-verify`, `--no-tui`, `--json`, `--preserve-anthropic-api-key`, `--config-dir`, `--init`, `--dump-defaults`, `--version`
- [x] **`--task` is not `required:"true"`.** go-flags enforces required flags during `Parse`, which would make `--version`, `--init`, `--dump-defaults` and task 15's `revmux config` all unparseable. Check for its presence in `run()` instead, and fail there with exit 2
- [x] `--run` is optional and defaults to a UTC timestamp; resolve it via `runName()` in `package main` and pass the resolved value down rather than re-deriving it
- [x] validate `--task` and `--run` before joining them into any path: reject empty, path separators, `..`, absolute, and leading `.`, then re-check containment on the resolved path because a symlink inside the tasks root defeats the lexical test
- [x] add INI parsing via go-flags `IniParser` in this exact order: `parser.Parse()` for the CLI, then the project INI, then the user INI, **both INI layers with `IniParser{ParseAsDefaults: true}`**. Verified against `go-flags@v1.6.1`: `IniParser.parse` calls `opt.Set(pval)` when `ParseAsDefaults` is false (ini.go:585-593), and `Set` marks the option `preventDefault` (option.go:250) — so loading an INI after `Parse()` **overwrites every CLI flag** and the precedence silently inverts. With `ParseAsDefaults` it calls `setDefault`, which returns early on `preventDefault` (option.go:286), and ini.go sets `preventDefault` after each layer, so CLI beats project beats user. This gives per-field merge for free: a project config setting one key leaves the user config's others untouched. `--config-dir` is a CLI flag, known before either INI load, so no second pass is needed
- [x] `no-ini:"true"` on meta flags and `ini-name` tags matching long flag names
- [x] auto-detect the project layer at `./.revmux/` in the process working directory — no flag selects it, its absence just drops the layer, and `./` means cwd for it exactly as it does for `--tasks-dir`'s default rather than following `--workdir`
- [x] drop the project layer when `--config-dir` resolves to the same directory, comparing absolute **symlink-evaluated** paths: a lexical compare misses `/var` vs `/private/var` on macOS, so the collision survives precisely in `t.TempDir()`-based tests. Loading one directory as two layers makes the layer-origin record wrong, which is what `revmux config` reports
- [x] record which layer supplied each knob's final value, into a `knobOrigins map[string]string` field on `options`. **`IsSet()` alone is useless here and the obvious implementation is wrong**: `ParseArgs` ends by calling `clearDefault()` on every option (parser.go:315-324), which for anything carrying a `default:` tag runs `setDefault` → `Set` → `isSet = true` (option.go:345-355, 249). Since the checkbox above requires every knob to have an explicit default, `IsSet()` is true for all of them the moment parsing ends. Use instead:
  - `flag` ⇔ `Option.IsSet() && !Option.IsSetDefault()`. An explicitly-passed flag goes through `Set` directly and never has `isSetDefault` set; a struct default arrives through `setDefault`, which sets it (option.go:294)
  - `project` / `user` ⇔ the key is present in that INI **file** and was not already claimed by a higher layer. Read each file's own key list and attribute first-writer-wins in precedence order. No exported signal distinguishes the two INI layers — both route through `setDefault` and set the same `isSetDefault` — so the file contents are the only sound source
  - `default` ⇔ everything not claimed above

  Task 15 reads this map and does not re-derive it
- [x] add runtime knobs to the INI, each with a long flag backing it so go-flags has exactly one definition per setting, and each with a stated default: `--idle-timeout` (2m), `--hard-timeout` (20m), `--stagger-delay` (30s, the cap on waiting for the leader), `--max-parallel` (4), `--verify-groups` (6), `--tasks-dir` (`./.revmux/tasks`), `--keep-runs` (10), `--profile` (**`focused`**, the only profile shipped until task 14 — defaulting to `comprehensive` here would break every run and task 5's own clean-install test for nine tasks; task 14 flips it)
- [x] **none of these may resolve to a zero value.** The config file ships fully commented out and the executor defaults both timers to disabled, so a struct tag without an explicit default means a clean install runs with no watchdog — the one capability the Overview says the tool exists for — and `max_parallel` 0, a zero-capacity semaphore
- [x] implement `resolveContext` against `<tasks-dir>/<task>/`: `scope.md` required (missing or empty is a load-time error), `goal.md` / `profile.md` / `context/` optional, all returned as absolute paths
- [x] absent `goal.md` or `profile.md` is a non-error that marks the report generically calibrated; a missing task directory is an error, since revmux never creates one it did not author
- [x] implement `reviewContext.vars` assembling `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}` as paths, substituting the "none provided" placeholder for anything absent
- [x] create `app/defaults/config`, the INI template `--init` writes: every knob from the list above present but **commented out**, so a file containing only comments is safely overwritable on upgrade while any uncommented line marks it customized
- [x] implement `--init` and `--dump-defaults`, never overwriting a customized file
- [x] write a test asserting every knob in the `options` struct appears in `app/defaults/config`, commented out — a knob missing from the template is invisible to anyone who runs `--init` to discover what is tunable
- [x] write tests for config precedence across all four layers using `t.TempDir()`, including a per-field merge where the project layer sets one key and the user layer's other keys survive
- [x] write a test asserting `--config-dir` pointed at the auto-detected `./.revmux/` yields one layer rather than two, run under `t.TempDir()` so the symlinked-temp-path case is the one actually exercised
- [x] write a test asserting each knob reports the layer that supplied it — flag, project, user, default — across a config where all four win at least once
- [x] write tests for task-dir resolution: full directory, scope-only, missing `scope.md`, empty `scope.md`, `scope.md` present as a directory, `context/` present but empty, `context/` present as a file, missing task directory, and a `--tasks-dir` pointed outside the repo
- [x] write tests rejecting `--task ../escape`, `--task a/b`, an absolute `--task`, and a symlinked task directory pointing outside the tasks root — each must fail before anything is written
- [x] write a test asserting every resolved variable is an absolute path and that no file is opened during resolution
- [x] write tests for `--init` and `--dump-defaults` against a temp dir
- [x] write a test asserting a zero-value config still yields a live idle timeout, a live hard timeout, `max_parallel >= 1` and a loadable default profile — the clean-install path no other test covers
- [x] add `github.com/jessevdk/go-flags` and run `go mod vendor` — first import of it
- [x] run tests - must pass before task 6

### Task 6: Find stage, event channel and end-to-end slice

**Files:**
- Create: `app/pipeline/pipeline.go`, `app/pipeline/event.go`, `app/pipeline/find.go`, `app/pipeline/sink.go`
- Create: matching `_test.go` for each, plus `app/pipeline/mocks/`
- Create: `app/progress.go`, `app/progress_test.go`
- Create: `app/main_test.go` — the main-level slice test below is its first content; tasks 8, 13 and 15 modify it
- Modify: `app/main.go`

First working slice: one lens agent, no stagger, no synthesis, no verify, no TUI.
With `.revmux/tasks/demo/scope.md` written by hand,
`revmux --task demo --profile focused --lenses bugs --no-synthesis --no-verify` produces a real markdown report.

The consumer-side `archiver` interface is declared here because `emit` writes through it, but `app/archive`
does not exist until task 11 — `package main` injects a no-op implementation for now. Declaring the seam at
task 6 and filling it at task 11 avoids reworking `emit` later, which is where the ordering guarantee lives.

**Design Contract:**

Type:
- `Pipeline` (exported — constructed by `package main`)
- `Config` (exported — construction options; enumerated in full because this is the injection point every
  later task adds to, and an unspecified struct is where a build stalls):
  `NewRunner func(RunnerSpec) Runner`, `Archive archiver`, `Clock executor.Clock`,
  `Set *prompt.Set`, `Profile *prompt.Profile`, `Roster []prompt.AgentSpec`, `Vars prompt.Vars`,
  `Task string`, `Run string`, `ScopePath string` (the three that populate `finding.Scope`; `ScopePath`
  comes from `reviewContext.Scope` rather than being dug back out of `Vars["SCOPE"]`),
  `NoSynthesis bool`, `NoVerify bool`,
  `StaggerDelay time.Duration`, `MaxParallel int`,
  `VerifyGroups int` (task 10), `History string` (task 11).
  Later tasks fill the fields they need; none of them redefines the struct.
  Idle and hard timeouts are deliberately **absent**: they reach the executors through `executor.Opts`,
  built by `options.executorOpts(clk)` in the composition root, and `.claude/rules/executor.md` is explicit
  that the pipeline never constructs an executor
- `Runner` (**exported** interface, consumer-side): `Run(ctx context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error)`
- `archiver` (unexported interface, consumer-side, moq): `Writer(name string) (io.WriteCloser, error)`
- `RunnerSpec` (exported — `Executor`, `Model`, `Effort`; the runner-selection input shared by `prompt.AgentSpec` and `prompt.Stage`)
- `Event` (exported — emitted on the channel)
- `EventKind` (exported — enum)
- `finder` (unexported — owns the find stage)
- `sourceResult` (unexported — one agent's outcome)
- `sink` (unexported — adapter satisfying `executor.EventSink`, tags events with an agent name)
- `progress` (unexported, `package main` — the stderr renderer)

Methods (full signatures):
- `(p *Pipeline) Run(ctx context.Context) (finding.Report, error)`
- `(p *Pipeline) Events() <-chan Event`
- `(p *Pipeline) emit(ev Event)`
- `(f *finder) run(ctx context.Context) ([]sourceResult, error)`
- `(f *finder) runAgent(ctx context.Context, spec prompt.AgentSpec) sourceResult`
- `(f *finder) parse(spec prompt.AgentSpec, raw json.RawMessage) ([]finding.Finding, error)`
- `(f *finder) report(sources []sourceResult) finding.Report`
- `(r sourceResult) ok() bool`
- `(s *sink) Emit(ev executor.Event)`
- `(pr *progress) run(events <-chan Event)`
- `(pr *progress) line(ev Event) string`

`Runner` and `RunnerSpec` are **exported even though the interface is consumer-side**, and that is not a
contradiction: the interface is still declared here, by the consumer, and exporting it only lets the supplier
name it. A `Config` field of unexported func type returning an unexported interface is unsettable and
unnameable from `package main`, so the injection point would not compile — this is the one export the
factory pattern requires. `RunnerSpec` rather than `prompt.AgentSpec` so a stage can select a runner without
fabricating a fake roster entry, which `.claude/rules/prompts.md` forbids.

`finder.parse` is what makes task 6 an end-to-end slice: it turns one agent's `structured_output` into
findings, **overwriting `sources` with the executing `AgentSpec.Name`** and validating `lenses` against that
agent's configured set. `finder.report` assembles the passthrough report so `--no-synthesis` works here
rather than waiting for task 9.

**`parse` also overwrites `Finding.id` with `<agent>-<n>`, at the same point and for the same reason.**
`id` is model-supplied, and four agents running one schema will each independently emit `1`, `2`, `3`.
Task 9's synthesis returns "the input finding ids it merged" and Go derives the `sources` union from them —
which is silently wrong the moment two agents use the same id, and `sources` is the sole input to the
cross-source confidence boost. Stamping ids here is what makes that derivation sound.

**`package main` resolves the roster once and passes it on `Config.Roster`.** `Profile.Roster` is not
called inside the pipeline: task 11's `manifest.json` needs the resolved roster, and task 12 hands the same
slice to `ui.ModelConfig` and to `app/progress.go` for agent colors. Resolving it in two places would
duplicate the validation errors and break `config.md`'s rule that downstream code takes the resolved value
rather than re-deriving it.

**Archive failure has to reach the exit code, so the seam is declared here, not at task 11.** `emit` cannot
return an error — `executor.EventSink.Emit` has no error result either — so `Pipeline` carries a sticky first
archive error: `emit` records it under the same mutex that guards the writer, and `Run` returns it. Without
that field the shape the signatures suggest is log-and-continue, which is precisely the warning-not-failure
behavior an earlier round removed. `package main` must also close every final artifact successfully **before**
writing anything to stdout, or a half-written archive ships alongside a report that reads as complete.

`Pipeline` does not implement `executor.EventSink` — the `sink` adapter does. That avoids an exported method
whose only purpose is interface satisfaction, and avoids two methods differing only by case.

Standalone helpers planned (justification why NOT a method):
- `New(cfg Config) *Pipeline` — constructor

Exports (justification per item: who outside the package calls this?):
- `Pipeline`, `Config`, `New`, `Event`, `EventKind` — `package main` constructs and runs the pipeline;
  `app/progress.go` and later `app/ui` consume events
- `Runner`, `RunnerSpec` — `package main` implements the factory and must name both

- [x] create `app/pipeline/event.go` with `Event`, `EventKind` (agent started, tool activity, state change, agent done, agent retried, agent degraded, findings emitted, stage change, rate limit) and a buffered channel that drops rather than blocks. The findings-emitted kind carries `[]finding.Finding` and the agent name, because `tui.md` requires findings in the combined view and no other transport reaches it
- [x] there is deliberately **no** pipeline-complete kind: completion must not be droppable, so `package main` signals it directly to the TUI after `Pipeline.Run` returns (task 13), rather than racing it through a channel that sheds under load
- [x] create `app/pipeline/sink.go` with the `sink` adapter tagging executor events with the agent name and exposing a `sync.Once`-guarded first-activity callback for task 7
- [x] create `app/pipeline/pipeline.go` with `Config`, `New`, `Run`, `Events`, `emit`, the exported `Runner` / `RunnerSpec`, the consumer-side `archiver`, and moq directives. **Spell the directives out with an alias for the unexported interface**: `//go:generate moq -out mocks/archiver.go -pkg mocks -skip-ensure -fmt goimports . archiver:ArchiverMock` — without the alias moq names the type `archiverMock`, unexported in package `mocks` and therefore unreachable from `app/pipeline`'s own tests
- [x] `emit` writes to the archive first and synchronously under a mutex, then offers the event to the channel; `Events()` has exactly one reader and the archive is never a second subscriber, since a Go channel distributes rather than broadcasts
- [x] `Pipeline` carries a sticky first archive error that `emit` records and `Run` returns, so a failed archive write reaches `package main` as a tool error rather than a logged warning
- [x] create `app/pipeline/find.go` with `finder` running `Config.Roster` sequentially for now, parsing each agent's structured output, stamping `sources` **and `id`**, and collecting `sourceResult` values
- [x] **`Pipeline.Run` populates `finding.Stats`** — `StartedAt` and `FinishedAt` from `Config.Clock`, `DurationMS` from their difference, and one `StageTiming` appended per stage it runs. Nothing else can: task 11 reads `Stats.Stages` for `manifest.json` and `stats.started_at` for the prior-round history, and a struct that only ever round-trips in a test is zero-valued in production while every test still passes
- [x] `finder.report` sums `Stats.Tokens` from the per-agent `SourceStat` values, so the run total and the per-agent breakdown cannot disagree
- [x] `Pipeline.Run` closes the events writer in a `defer` and folds the close error into the same sticky archive error, since a close is where a deferred write failure surfaces and nothing else can reach that handle — `archiver` hands out `io.WriteCloser` values and `package main` never sees them
- [x] each agent goroutine closes its own `RawOutput` tee and reports a close failure through `sourceResult`, for the same reason
- [x] create `app/progress.go` with the `progress` type subscribing to the channel and writing timestamped lines to stderr
- [x] wire `app/main.go`: parse options, load prompt set, **resolve the roster once via `Profile.Roster(o.Lenses, set.LensNames())`** and pass the slice on `Config.Roster`, build the `NewRunner` factory (claude only at this task; task 8 adds codex), run the pipeline, filter via `Above`, write the report to stdout, exit with `ExitCode`
- [x] write pipeline tests with a mocked `Runner` asserting the emitted event sequence
- [x] write tests for the find stage collecting results and for `sourceResult.ok`
- [x] write a test asserting `finder.parse` discards model-supplied `sources` and replaces them with the executing agent's name, including when the model invents two entries for itself
- [x] write a test asserting `finder.parse` rewrites `id` to `<agent>-<n>`, and that two agents both returning `id: "1"` end up with distinct ids — the collision that would silently corrupt task 9's `sources` derivation
- [x] write a test asserting a close failure on the events writer surfaces as a run error rather than being swallowed by the `defer`
- [x] write a test with a fake clock asserting `Stats.StartedAt`, `FinishedAt` and one `StageTiming` per stage are non-zero after a run, and that `Stats.Tokens` equals the sum of the per-agent counts
- [x] write a test asserting `emit` archives an event that the channel drops, so a slow renderer cannot cost the audit record
- [x] write a test asserting a failing archive writer makes `Run` return an error and `package main` exit 2, and that `-race` is clean when `emit` is driven concurrently from several agent goroutines
- [x] write tests for the stderr progress renderer against a synthetic event sequence
- [x] write a main-level test running the whole slice with a mocked runner and asserting stdout markdown and exit code
- [x] run tests - must pass before task 7

### Task 7: Parallel fan-out, staggered launch and degrade policy

**Files:**
- Create: `app/pipeline/stagger.go`, `app/pipeline/stagger_test.go`
- Modify: `app/pipeline/find.go`, `app/pipeline/find_test.go`, `app/pipeline/sink.go`, `app/pipeline/sink_test.go`, `app/pipeline/pipeline.go`, `app/main.go`

`sink.go` is modified because the first-activity callback lives there. `app/config.go` is **not** listed:
task 5 already adds `stagger_delay` and `max_parallel` to the INI, so there is nothing to add here.

**Design Contract:**

Type:
- `stagger` (unexported — releases agents on a delay and caps concurrency)

Methods (full signatures):
- `(s *stagger) acquire(ctx context.Context, index int) error`
- `(s *stagger) release()`
- `(s *stagger) leaderStarted()`

Named for what they do: `acquire` takes a slot, `release` gives it back. `leaderStarted` is idempotent —
every call after the first is a no-op.

`index` selects leader treatment: index 0 goes immediately, every other index waits on the gate. **The gate
latches open once opened, by either release path** — `leaderStarted` firing *or* `stagger_delay` elapsing —
and never re-arms. That is what lets task 10 reuse one instance across stages: verify's groups pass a
nonzero index and find the gate already open regardless of how find got through it. A `stagger` whose gate
re-armed per stage would charge every stage after the first another `stagger_delay` to re-prove auth the
first stage already proved.

`Pipeline` owns the instance and hands the same one to `finder` and `verifier`; neither constructs its own.

The signal needs a wire, or `stagger_delay` silently becomes the only release path. `finder` sets a
first-activity callback on the **leader's `sink`**, guarded by `sync.Once` and invoked before the event is
offered to the channel. Watching `Events()` instead would be wrong twice: it drops events, and it already
has exactly one reader. First activity is any executor event for claude and the first raw stdout write for
codex, so a codex leader still releases the rest promptly.

Standalone helpers planned (justification why NOT a method):
- `newStagger(timeout time.Duration, maxParallel int, clk executor.Clock) *stagger` — constructor. Named
  `timeout` rather than `cap` because `cap` shadows the builtin and `predeclared` flags it; the clock is
  injected so stagger tests advance time instead of sleeping

Exports (justification per item: who outside the package calls this?):
- none — used only inside `app/pipeline`

- [x] create `app/pipeline/stagger.go` releasing index 0 immediately and every other index once `leaderStarted` fires or `stagger_delay` elapses, with a `max_parallel` semaphore; the gate **latches** open on either path and never re-arms, which is what lets task 10 run a second stage through the same instance
- [x] the stagger must not group, reorder or filter agents by model or executor — it releases in roster order and nothing else
- [x] wire the leader's `sink` first-activity callback to `stagger.leaderStarted`, fired before the event reaches the channel and guarded by `sync.Once`
- [x] convert `finder.run` to run the roster concurrently through the stagger, preserving deterministic result ordering
- [x] **`Pipeline` constructs the `stagger` and passes it to `finder`; `finder` does not own it.** Task 10 runs the verify stage through the same instance, and an instance owned by one stage cannot be reached by another — deciding ownership here costs nothing and moving it later means editing `find.go` again
- [x] implement retry-once on `IdleTimedOut`, process failure, or a `rate_limit_event` whose status is not `allowed`, emitting the retry `EventKind` task 6 defined
- [x] implement degrade: second failure marks the source degraded, the pipeline continues, and `SourceStatus` records it
- [x] **every source degraded is a tool error**, not a clean empty report: `Pipeline.Run` returns it as an error and `package main` exits `2`, since a run with nothing reporting has no review in it
- [x] write tests asserting agent 1 is released before the others and that `max_parallel` is respected
- [x] write tests for both release paths: `leaderStarted` fires and the rest launch immediately, and agent 1 never emits so the `stagger_delay` timeout releases them instead
- [x] write tests for retry-once **succeeding**, and separately for retry-once then degrade asserting the pipeline completes and `Degraded()` is true
- [x] write a test asserting the retry cancels the first attempt's context before invoking `Run` a second time. It cannot assert on a process group: this test drives a mocked `pipeline.Runner`, which has no process — the kill itself is task 3's `processGroupCleanup` test
- [x] write a test asserting a rate-limited result triggers the retry path
- [x] write a test asserting a degraded source is loud in both places that exist at this task: `SourceStatus.degraded` in JSON and the markdown banner. The third place the rules require, the `{{SOURCES}}` block, is built by `synthesizer.vars` in task 9 and is asserted there
- [x] write a test asserting result ordering is deterministic regardless of completion order
- [x] write a main-level test asserting an all-degraded run exits `2` and prints no report that could be mistaken for a clean one
- [x] run tests - must pass before task 8

### Task 8: Codex executor

**Files:**
- Create: `app/executor/codex.go`, `app/executor/codex_test.go`
- Modify: `app/executor/proc.go`, `app/executor/proc_test.go`, `app/main.go`, `app/main_test.go`

`app/main_test.go` is listed because the roster-routing test targets the `NewRunner` factory, which lives
in `package main`.

No fixture file is created here. `testdata/codex-clean.txt` already exists from the Prerequisites, and the
other three are derived from it inside `codex_test.go` — same rule task 3 follows for claude.

`proc` is modified because codex needs stderr as well as stdout: its header lines are filtered from there,
and `.claude/rules/executor.md` records that plan-quota errors arrive on stderr with an **empty stdout**,
so a stdout-only check misses them entirely. It lands as a new field on task 3's `*procRun` handle, so
`start`'s signature does not change and neither executor's `Run` has to be reworked.

**Design Contract:**

Type:
- `Codex` (exported — embeds `proc`, selected by `executor: codex`)

Methods (full signatures):
- `(c *Codex) Run(ctx context.Context, req Request, sink EventSink) (Result, error)`
- `(c *Codex) args(req Request) []string`
- `(c *Codex) outputContract(schema json.RawMessage) string`
- `(c *Codex) extract(raw string) (json.RawMessage, error)`

Standalone helpers planned (justification why NOT a method):
- `NewCodex(runner CommandRunner, opts Opts) *Codex` — constructor

Exports (justification per item: who outside the package calls this?):
- `Codex`, `NewCodex` — constructed inside the `NewRunner` factory in `package main`

- [x] add stderr as a field on the `*procRun` handle task 3 returns and populate it in `proc.start`, since codex reads both streams — a new field, not a signature change
- [x] create `app/executor/codex.go` embedding `proc`, invoking `codex exec --sandbox read-only` with model and effort from `Request`
- [x] implement `outputContract(req.Schema)` appending the "return only JSON matching this shape" instruction with the schema rendered inline, since codex has no `--json-schema`. It takes the schema rather than hardcoding one because a codex entry can run any stage — `synthesis.md` and `verify.md` both accept `executor: codex`, and each carries its own schema
- [x] implement `extract` pulling the JSON block out of the final output, tolerating surrounding prose
- [x] tick the idle watchdog on raw stdout writes rather than parsed events, and tee those raw bytes to `Request.RawOutput`
- [x] **emit an activity `executor.Event` on the first raw stdout write**, so a codex leader fires the sink's first-activity callback. `pipeline.md` requires "the first raw stdout write for codex" to count as first activity, but nothing else in the plan tells codex to call `sink.Emit` — without this a codex leader never opens the stagger gate and `stagger_delay` silently becomes the only release path, which is the exact failure that wire exists to prevent
- [x] write a test asserting a codex leader releases the rest through that first stdout write rather than by timing out. It belongs here, not in task 7: `Codex` does not exist until this task, and against a mocked `pipeline.Runner` the test could only simulate an event and would assert nothing about codex
- [x] filter codex stderr to the resolved model/sandbox/effort header lines, once per process
- [x] implement the retry → limit → error pattern tiering for codex only, checked against the **tail** of output and only on non-zero exit, skipped entirely when the context was canceled — matching against the whole output self-triggers on this project's own code, which discusses rate limits
- [x] detect plan-quota errors on stderr with an empty stdout, which a stdout-only check misses
- [x] extend the `NewRunner` factory in `package main` to select codex per `RunnerSpec.Executor`
- [x] derive the codex fixtures from `testdata/codex-clean.txt` in a `_test.go` helper: prose-wrapped wraps the clean JSON in surrounding text, no-JSON strips the block, stalled cuts the capture early
- [x] write tests for `args()` and `outputContract()`, one asserting the contract for a finder request contains `FinderSchema()` and the contract for a verify request contains `VerifySchema()`
- [x] write tests for `extract` against all four fixtures including the graceful failure path
- [x] write a test asserting a codex roster entry routes to the codex executor and a claude entry does not
- [x] write a test asserting a codex prompt gets the output contract appended and a claude prompt does not
- [x] write tests for each pattern tier, including one asserting a findings body containing the words "rate limit" on a **zero** exit is not misread as a limit
- [x] write a test for plan-quota-on-stderr with empty stdout
- [x] confirm `dupl` reports no duplication between the two executors
- [x] re-verify `GOOS=windows GOARCH=amd64 go build ./...` now that a second executor exists
- [x] ➕ write a test asserting the tail bound itself holds: the same limit phrase is invisible above the
  tail of a long failed run and a match inside it. The zero-exit case only proves patterns are gated on
  exit status, not that a long findings body naming a limit early on is out of range
- [x] ➕ write a proc-level test asserting stderr is drained when an executor supplies no filter, since
  claude passes none and an unread pipe fills and blocks the child
- [x] run tests - must pass before task 9

### Task 9: Synthesis stage

**Files:**
- Create: `app/pipeline/synthesize.go`, `app/pipeline/synthesize_test.go`
- Modify: `app/prompt/defaults/prompts/synthesis.md`

**Design Contract:**

Type:
- `synthesizer` (unexported — owns the synthesis stage)

Methods (full signatures):
- `(s *synthesizer) run(ctx context.Context, sources []sourceResult) (finding.Report, error)`
- `(s *synthesizer) vars(sources []sourceResult) prompt.Vars`
- `(s *synthesizer) parse(raw json.RawMessage, inputs map[string]finding.Finding) (finding.Report, error)`

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none — internal to `app/pipeline`

**`parse` takes the pre-synthesis findings keyed by id, and that parameter is what makes the whole
attribution scheme work.** `SynthesisSchema` has the model return, per output finding, the input ids it
merged; Go then derives the `sources` and `lenses` unions from those ids. Without the map, `parse` holds
only the model's output and cannot map an id back to the agent that raised it — so it would either drop
attribution or take the model's word for it, and `sources` is the sole input to the cross-source confidence
boost. That is the hard rule "Go assigns `sources`, never the model" failing silently on the default path,
with output that looks entirely plausible.

The ids are unique because task 6's `finder.parse` stamps them as `<agent>-<n>`. Model-supplied ids would
collide across agents and the union would be wrong rather than absent, which is worse.

- [x] create `app/pipeline/synthesize.go` composing the synthesis prompt from the `synthesis.md` stage with `{{FINDINGS}}` and `{{SOURCES}}`
- [x] build `{{SOURCES}}` from the real roster: which agents ran, which degraded, which emitted each finding
- [x] run the stage through `Config.NewRunner` with a `RunnerSpec` built from the stage's own `executor` / `model` / `effort`, and parse its structured output into `finding.Report`
- [x] **derive `sources` and `lenses` in Go from the merged input ids the model returned**, never from a `sources` field in the output: build the id-keyed map from `sources []sourceResult` in `run`, hand it to `parse`, and union the attribution of every merged input onto the output finding
- [x] a merged id the map does not contain is a hard error, not a silent skip — it means the model invented an id, and quietly dropping it produces a finding with fewer sources than it earned or none at all
- [x] honor `--no-synthesis` by passing findings through with their `sources` and `lenses` attribution intact
- [x] author `synthesis.md`: split open questions and pre-existing issues first, dedupe on file plus line ±2 with similar descriptions, boost `min(99, max_conf + 10*(N-1))` over distinct sources, severity max, drop single-source below 80 without corroboration, and route would-be-drops to the verifier when the run is degraded
- [x] write tests with a mocked runner asserting the prompt carries an accurate source roster
- [x] write a test asserting the composed synthesis prompt instructs the model to route would-be-drops to the verifier when the run is degraded — the drop rule itself lives in `synthesis.md` and is executed by the model, so with a mocked runner prompt content is the only thing a Go test can assert
- [x] write a test asserting a finding merged from two agents carries both names in `sources` and the union of their lenses, and that one merged from a single agent's two lenses carries **one** source — the self-corroboration case the confidence boost exists to catch
- [x] write a test asserting an unknown merged id fails rather than being skipped
- [x] write a test asserting `{{SOURCES}}` names the degraded agents, the third of the three places the rules require a degraded source to be loud
- [x] write tests for `--no-synthesis` passthrough and for malformed synthesis output
- [x] run tests - must pass before task 10

### Task 10: Verification stage

**Files:**
- Create: `app/pipeline/verify.go`, `app/pipeline/verify_test.go`
- Modify: `app/pipeline/pipeline.go`, `app/prompt/defaults/prompts/verify.md`

`pipeline.go` is modified because `Pipeline` hands the `verifier` the same `stagger` it already handed the
`finder` in task 7. No change to `find.go` — ownership was settled there.

**Design Contract:**

Type:
- `verifier` (unexported — owns the verify stage)
- `verifyGroup` (unexported — one directory's findings)

Methods (full signatures):
- `(v *verifier) run(ctx context.Context, rep finding.Report) (finding.Report, error)`
- `(v *verifier) groupByDir(findings []finding.Finding) []verifyGroup`
- `(v *verifier) runOne(ctx context.Context, g verifyGroup) ([]finding.Finding, error)`
- `(g verifyGroup) label() string` — a **filename-safe slug**, separators collapsed to `-`, since task 11 builds `prompts/stages/verify-<label>.md` from it. A group covering `app/executor` labels as `app-executor`; leaving the separator in creates a stray nested directory that every stated check still passes

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none — internal to `app/pipeline`

- [x] create `app/pipeline/verify.go` grouping findings by directory, merging thin directories and capping group count from config
- [x] dispatch one verifier per group in parallel through **the same `stagger` instance the find stage used**, taken from `Pipeline` rather than constructed here, each group seeing only its own findings. The gate latched open during find — by `leaderStarted` or by `stagger_delay`, either one — so only the `max_parallel` semaphore applies; groups pass nonzero indices so none is treated as a leader. A fresh instance would re-arm the wait and charge verify another `stagger_delay` to re-prove auth find already proved
- [x] apply verdicts: `confirmed` keeps, `rejected` drops, `refined` rewrites, `pre_existing` and `immaterial` move to their own lists — the two list-routing verdicts are enum values the model returns through `VerifySchema`, not something Go infers
- [x] honor `--no-verify` by marking every finding unverified in the report
- [x] author `verify.md` carrying the verdict set and the materiality test
- [x] write tests for grouping: one directory, many directories, thin-directory merging, cap enforcement
- [x] write a test asserting `label()` on a nested directory and on a merged multi-directory group both yield a slug with no path separator, since task 11 turns it into a filename
- [x] write tests asserting each verifier prompt contains only its own group's findings
- [x] write tests for every verdict path and for `--no-verify`
- [x] write a test asserting verify does not wait out a second `stagger_delay`: advance the fake clock past find's release, then assert the verify groups dispatch without a further advance — the regression a per-stage `stagger` instance would cause
- [x] run tests - must pass before task 11
- ➕ **a verifier that fails degrades its group rather than the run.** The design contract left this
  open. Find already paid for the findings and synthesis already merged them, so a dead, unparseable
  or empty verifier returns its group **unverified** — the same honest value `--no-verify` produces —
  and emits `EventAgentDegraded` naming the group. Only a prompt tree that will not compose fails the
  stage: that is a config error every group would hit identically, and `prompts.md` requires an
  unresolved variable to be loud. A verdict outside the enum is treated as unverified too, since the
  codex path has no `--json-schema` to enforce one
- ➕ `verifyGroup` carries the composed prompt (`text`), so `run` composes every group before
  dispatching any of them — one place for the config error above, and the bytes task 11 archives as
  `prompts/stages/verify-<label>.md` are already on the value it names them from. `runOne` keeps its
  contract signature and reads `g.text`
- ➕ the pipeline test harness now defaults to `NoVerify: true` for the same reason it defaults to
  `NoSynthesis: true`, and gained `newHarnessWith` for tests needing a broken or customized override
  in the project layer
- ➕ ⚠️ **`runFind` latches the stagger gate open when the stage completes**, and reusing the instance
  does not work without it. The plan assumed find always opens the gate, but a single-agent roster
  never waits on it — the leader is index 0 — so nothing calls `leaderStarted` and the gate stays shut
  until `stagger_delay` elapses. Verify's groups then blocked on a leader that had already finished;
  the `app` end-to-end test deadlocked on exactly that. Find running to completion is the stronger
  proof anyway: at least one process finished, or the run had already failed with `errNoSources`

### Task 11: Run artifacts

**Files:**
- Create: `app/archive/archive.go`, `app/archive/archive_test.go`
- Modify: `app/pipeline/pipeline.go`, `app/pipeline/pipeline_test.go`, `app/config.go`, `app/main.go`

**Design Contract:**

Type:
- `Archive` (exported — constructed in `package main` over the task directory, injected into `app/pipeline`)
- `archiver` — already declared in `app/pipeline` by task 6, not created here:
  `Writer(name string) (io.WriteCloser, error)`. This task supplies the real implementation behind it

Methods (full signatures):
- `(a *Archive) Writer(name string) (io.WriteCloser, error)`
- `(a *Archive) Prune() error`

`name` is a path relative to the run directory, so one method serves the per-agent tees, `events.jsonl`,
`prompts/agents/<agent>.md`, `stages/<n>-<stage>.json`, the manifest and the rendered report without the
archive needing to know what any of them mean; it creates parent directories as needed.

**`Writer` accepts nested paths and rejects escapes — those are different checks at different layers.**
It takes a clean relative path and rejects only absolute paths, `..` components, and anything resolving
outside the run root after symlink evaluation. A separator is legal, since `prompts/`, `stages/` and
`agents/` all need one. Validating a *roster agent name* is a separate step that happens before the filename
is built; conflating the two makes the nested-path and separator-rejection tests mutually unsatisfiable.

**Ownership differs per writer, and only one has a single owner.** An agent's tee belongs to that agent's
goroutine and the report belongs to `package main` after the run, so neither needs locking. `events.jsonl`
does: it is written from `Pipeline.emit`, which after task 7 is reached concurrently from every agent
goroutine through `sink.Emit`. One writer, N producers — guard it with a mutex held across the whole line
write, or `-race` fails on task 11's own concurrent-write test and the JSONL interleaves.

`Archive.Dir()` is deliberately absent — nothing needs the path until a caller exists for it.

Standalone helpers planned (justification why NOT a method):
- `New(opts Opts) (*Archive, error)` — constructor; creates `<TaskDir>/runs/<Run>` and fails if it already
  exists. `Opts` carries `TaskDir`, `Run`, `Keep` and `Clock` rather than a positional list, because
  `New(taskDir, run string, ...)` puts two same-typed strings side by side and swapping them compiles clean
  while creating the run directory in the wrong place — the same hazard the plan already avoids on
  `reviewContext` and `combinedEntry`
- `History(taskDir string) (string, error)` — reads prior rounds and renders the **inventory** — the `runs/`
  path plus one line per round — and nothing else. The re-evaluate-independently guard is appended by
  `prompt.ComposeOpts.render` (task 4), which is what makes the data and the guard inseparable: any caller
  that could hand over one without the other reintroduces the anchoring failure. Emitting the guard here too
  would duplicate it in every composed prompt. Called from `package main` before the pipeline exists, so it
  cannot be a method on the current run's `Archive`, and it reads sibling rounds rather than the one being
  written

Exports (justification per item: who outside the package calls this?):
- `Archive`, `Opts`, `New` — `package main` constructs it and injects it as the pipeline's `archiver`
- `Archive.Writer` — satisfies `pipeline.archiver`, and `package main` also calls it directly to write `report.md`, `findings.json` and `manifest.json` after the run
- `Archive.Prune` — called from `package main` after the report is written, so a failed run keeps its artifacts
- `History` — called from `package main` before the pipeline is constructed

Two consumers shape this package. A human debugging a bad review needs the raw streams and the event log.
A later self-reflection agent — out of scope here, but its data must be captured from the first run or the
corpus is worthless — needs to attribute a finding to the lens text that raised it and to see what each stage
changed. Everything below exists for one of those two.

- [x] create `app/archive/archive.go` creating `<taskDir>/runs/<run>` and erroring if that directory already exists, so a bad round is never destroyed by a retry
- [x] tee every agent's raw output verbatim by passing an archive writer as `executor.Request.RawOutput`, extension matching content: `agents/<agent>.jsonl` for claude stream-json, `agents/<agent>.log` for codex prose
- [x] per-agent streams live in their own `agents/` subdirectory: agent names come from the roster, and an agent called `events` would otherwise collide with `events.jsonl`
- [x] **a retried agent writes a second file, never the same one.** Task 7 retries once, so name the attempts `agents/<agent>.jsonl` and `agents/<agent>.retry.jsonl`: appending would splice the retry's first line onto the stalled attempt's partial line and make the file unparseable, and truncating would discard the failed attempt — which `.claude/rules/executor.md` calls the stream most worth having on disk
- [x] archive prompts under `prompts/agents/<agent>.md` and `prompts/stages/<stage>.md`, so a roster agent named `synthesis` or `verify` cannot overwrite a stage's prompt — the same collision class already handled for `agents/`
- [x] verify fans out one agent per directory group, so write `prompts/stages/verify-<group-label>.md` per group using `verifyGroup.label()`; a single `verify.md` loses N-1 prompts and makes "what did that verifier actually see" unanswerable
- [x] validate the roster agent name **before** building any filename — reject separators, `..`, absolute and leading `.` there. `Writer` itself accepts clean nested relative paths (it must, for `prompts/`, `stages/` and `agents/`) and rejects only what resolves outside the run root after symlink evaluation
- [x] `events.jsonl` is written from `Pipeline.emit`, synchronously, before the event is offered to the channel — recording revmux's own decisions (stalls, retries, degrades, stage transitions) which no per-agent stream contains. The archive is **never** a channel reader: a Go channel distributes rather than broadcasts, so a second reader would take an arbitrary half of the events
- [x] guard the `events.jsonl` writer with a mutex held across each whole line write, since `emit` is reached concurrently from every agent goroutine
- [x] the archived prompt is the composed text post-substitution — exactly the bytes handed to each process, injected prior-round block and codex output contract included — since a reflection agent cannot judge a lens it cannot read
- [x] write `stages/1-found.json`, `stages/2-synthesized.json`, `stages/3-verified.json` so what synthesis merged or dropped and what verify rejected is directly visible rather than reconstructed from raw streams
- [x] write `manifest.json`: resolved roster (name, lenses, executor, requested model and effort) from `Config.Roster`, the model each agent *actually* ran per `modelUsage`, tokens per agent, per-file prompt provenance (which precedence layer won, plus a content hash), degraded sources, and per-stage timings read from `finding.Stats.Stages` — task 2 carries them because nothing here can recompute a stage duration after the fact
- [x] have `package main` write `report.md` and `findings.json` into the run directory as well, so an archived run is self-contained
- [x] implement `History(taskDir)` rendering the prior-round **inventory**: `runs/` path plus one line per round (name, timestamp, finding counts by severity, degraded sources) read from each round's `findings.json`. Do **not** render the re-evaluate-independently instruction here — task 4's `ComposeOpts.render` appends it to every composed prompt, so emitting it here duplicates it. The timestamp comes from `stats.started_at`, which task 2 puts in the wire shape for exactly this — a run directory's mtime is not it, since pruning and copying both rewrite that
- [x] wire it in `package main`: resolve history once, pass it down on `pipeline.Config`, and have the pipeline hand it to every `Compose` call via `ComposeOpts`
- [x] a prior round with a missing or unparseable `findings.json` is listed with its counts marked unknown, never dropped and never fatal — a round that failed badly is still evidence
- [x] confirm the consumer-side `archiver` interface and its mock already exist in `app/pipeline` from task 6 — `Archive` implements that interface, it is not redeclared here
- [x] implement `Prune` over `runs/` only, dropping oldest by mtime beyond `keep_runs`; it must never touch `scope.md`, `goal.md`, `profile.md` or `context/`
- [x] confirm `tasks_dir` and `keep_runs` are already in the INI from task 5 with documented defaults (`./.revmux/tasks`, 10) rather than adding them a second time
- [x] write tests for run-directory creation, the already-exists error, concurrent writes from several agents, and nested-path writers
- [x] write a test asserting the archived prompt is byte-identical to what the executor received, since a reflection agent drawing conclusions from a paraphrase is worse than one with no data
- [x] write tests for `History`: no prior rounds, several rounds ordered oldest-first, and a round with an unreadable `findings.json` still listed
- [x] write a test asserting `manifest.json` records the actual model when it differs from the requested one
- [x] write tests for `Prune` ordering by mtime, asserting it leaves caller-written context files untouched even when `keep_runs` is 0, never removes the run currently being written, is a no-op when fewer runs exist than `keep_runs`, tolerates an absent `runs/`, and removes run directories containing nested `prompts/`, `stages/` and `agents/` subtrees
- [x] `History` runs before `Prune`, so a round is never read after it has been deleted; state the ordering rather than leaving it to call-site accident
- [x] write tests that `Writer` **accepts** clean nested paths (`prompts/agents/x.md`, `stages/1-found.json`, `agents/x.jsonl`) and **rejects** what escapes: a `..` component, an absolute path, and a symlinked run directory pointing outside the tasks root
- [x] write tests that roster-name validation rejects an agent name containing a separator, `..`, or a leading `.`, and that an agent named `events` or `verify` cannot collide with a fixed artifact
- [x] write a test asserting a retried agent leaves both attempts intact and independently parseable
- [x] write a test asserting one verify prompt file exists per directory group, not one per stage
- [x] write a test asserting `stages/*.json` is written once per stage and `events.jsonl` actually captured a stall, a retry and a degrade — the two artifacts CLAUDE.md calls non-derivable from each other
- [x] write a test with a failing `archiver` mock asserting the run **fails with exit 2**, not a warning — a report next to a half-written archive reads as complete and the gap only surfaces when someone tries to audit it
- [x] run tests - must pass before task 12

- ➕ `app/archive` is split in two files rather than one: `archive.go` owns the run directory, `Writer`
  and `Prune`, and `history.go` owns `History` and the `round` it renders. Two concerns, and the
  one-test-file-per-source rule then gives each its own test file
- ➕ the pipeline's whole-artifact writers moved to `app/pipeline/artifacts.go` — `save`, `saveStage`
  and the sticky `fail`, plus the artifact path constants. `pipeline.md` forbids `Pipeline`
  accumulating I/O plumbing next to the three stages, and the new file is what the new tests match.
  `events.jsonl` stays in `pipeline.go` beside `emit`: it is a stream held open across the run, not a
  whole-file write
- ➕ `package main`'s side lives in `app/artifacts.go`: the `manifest` type, `archiveRun` and
  `writeArtifact`, mirroring how `app/progress.go` holds the plain renderer rather than growing
  `app/main.go`
- ➕ **roster agent names are validated at load, in `prompt.AgentSpec.checkName`, not in the archive.**
  Load is the earliest point that is still before any filename is built, it is where every other
  roster-entry rule already lives, and it covers the `--lenses` override for free since that path
  validates through the same method
- ➕ ⚠️ **`archive.Opts` carries no `Clock`.** The design contract listed one, but nothing in the
  package has a timing path: `Prune` orders by mtime and `History` reads `stats.started_at` out of a
  file. An unused field on an exported option struct is dead weight, so it is omitted rather than
  carried for symmetry
- ➕ `Prune` counts the run being written toward `keep_runs`, so `keep_runs` 10 leaves ten
  directories including the current one, and `keep_runs` 0 or 1 leaves only the current one. The run
  being written is never a candidate whatever the number says
- ➕ a `Prune` failure is a warning on stderr and leaves the review's own exit code alone. A stale run
  directory that will not delete is housekeeping, not one of this run's artifacts, and turning a
  finished review into exit `2` over it would tell a scripted caller the review failed
- ➕ the archived `report.md` and `findings.json` are the `--min-confidence` filtered report, byte for
  byte what the caller was shown, so a later reader sees the round as it was reported. The unfiltered
  set is still in `stages/3-verified.json`
- ➕ `TestRun_review`'s subtests each take their own tasks root now. They shared one, and a run name
  that already exists is a load-time error, so the second subtest to write `round-1` would fail on
  the collision rather than on what it asserts

### Task 12: TUI — status table, combined view and per-agent panes

**Files:**
- Create: `app/ui/model.go`, `app/ui/view.go`, `app/ui/handlers.go`, `app/ui/status.go`, `app/ui/agentpane.go`, `app/ui/combined.go`, `app/ui/doc.go`
- Create: matching `_test.go` for each
- Modify: `app/main.go`, `app/progress.go`, `app/progress_test.go`

`app/progress.go` is listed because the combined view needs the findings-emitted `EventKind`, and
`CLAUDE.md` requires every kind to have a case in both renderers or it is invisible in one of them.

Renders to the tty so stdout stays clean. The TUI is purely an event subscriber — it never spawns a process,
reads a file, or writes stdout.

**Design Contract:**

Type:
- `Model` (exported — the bubbletea model)
- `ModelConfig` (exported — construction options)
- `agentState` (unexported — one agent's row and scrollback)
- `viewState` (unexported — focused tab, sizes, scroll offsets)
- `combinedState` (unexported — the interleaved compact log)
- `combinedEntry` (unexported — one compact line: agent, text, timestamp)

`ModelConfig` carries the resolved roster — the same `[]prompt.AgentSpec` slice `package main` already
built for `Config.Roster` in task 6 — which is where agent colors come from. `pipeline.Event` names the
agent and nothing else, so without the roster both renderers would have to invent a color per name and
would disagree. `app/progress.go` takes the same slice for the same reason.

An agent the roster does not name still has to render — `tui.md` requires tolerating events for an unseen
agent — so an unknown name falls back to the default foreground rather than panicking on a map miss.

Methods (full signatures):
- `(m Model) Init() tea.Cmd`
- `(m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)`
- `(m Model) View() string`
- `(m Model) statusTable() string`
- `(m Model) detailPane() string`
- `(m *Model) apply(ev pipeline.Event)`
- `(a *agentState) push(line string)`
- `(c *combinedState) push(e combinedEntry)`

**Receivers are mixed here on purpose, and the reason has to be written down** or the next reader "fixes" it.
`tui.md` says keep receivers consistent per type, but bubbletea's `tea.Model` interface requires value
receivers on `Init`, `Update` and `View`, and `Update` returns the mutated model rather than mutating in
place. So `Model` carries value receivers for the interface methods and their render helpers, and pointer
receivers only for the internal mutators `Update` calls before returning. The state sub-structs
(`agentState`, `combinedState`, `findingsState`) have no such constraint and are pointer-only throughout —
a value receiver there would copy cursor and filter state on every render.

`combinedState.push` takes a struct, not `(agent, line string)` — two adjacent strings called from
event-handling code where both are in scope is the classic transposition, and the struct gives the compact
log somewhere to carry timestamp and color without growing the signature.

No `app/ui/mocks/`: the package has no injected interfaces, because it does no OS work.

Standalone helpers planned (justification why NOT a method):
- `New(cfg ModelConfig) Model` — constructor

Exports (justification per item: who outside the package calls this?):
- `Model`, `ModelConfig`, `New` — `package main` constructs and runs the bubbletea program

- [x] create `app/ui/model.go` with `Model`, `ModelConfig`, `New` and the state sub-structs
- [x] create `app/ui/status.go` rendering one row per agent: name, state, elapsed, last activity
- [x] create `app/ui/combined.go` with tab `0 · all`, chronological, agent-prefixed and colored, one compact line per tool call, state change and finding, deliberately excluding thinking text
- [x] color the agent prefix from `AgentSpec.Color` in both the status table and the combined view, emitting raw ANSI for the inline prefix rather than `lipgloss.Render()` — `tui.md` records that a nested render emits a full reset and kills the enclosing pane's background
- [x] teach `app/progress.go` the same roster so `--no-tui` prefixes each agent in its own color too, and a reviewer switching renderers sees the same agent the same way
- [x] create `app/ui/agentpane.go` with tabs `1-9` showing full per-agent scrollback including thinking
- [x] create `app/ui/handlers.go` for tab and number switching, scrolling, and quit
- [x] wire `app/main.go` to run the TUI when the tty is openable and `--no-tui` is unset, rendering to the tty
- [x] write tests driving `Update` with synthetic `pipeline.Event` values and asserting rendered output
- [x] write tests for tab switching, scrollback bounds and the combined view's compact filtering
- [x] write a test asserting the combined view is focused by default
- [x] write a test asserting events for an unknown agent and events after an agent finished are tolerated, including that an unrostered name renders in the default foreground rather than panicking
- [x] write a test asserting the TUI and the plain renderer emit the same color for the same agent
- [x] add `bubbletea`, `bubbles` and `lipgloss` and run `go mod vendor` — first import of all three
- [x] run tests - must pass before task 13

- ➕ the raw-ANSI painter is `prompt.AgentSpec.Paint`, not a helper inside `app/ui`. Both renderers
  call the one implementation, which is what "the TUI and the plain renderer emit the same color"
  actually needs — two implementations reading one resolved value can still drift. It emits
  `\x1b[39m` rather than a full reset, for the reason `tui.md` gives, and `app/prompt` still imports
  no lipgloss
- ➕ **lipgloss is used for measuring and clipping only, never for color.** Its default renderer
  detects the color profile from **stdout**, which is not where the TUI writes: under
  `revmux --json > file` it would strip every color while the reviewer sits at a terminal — the same
  class of mistake as gating the TUI on stdout being a TTY. `lipgloss.Width` and `MaxWidth` are
  profile-independent and are what keeps a clip from cutting an escape sequence in half
- ➕ elapsed time is measured between event timestamps rather than off a clock. `app/ui` takes no
  `Clock`, and a table that renders identically in a test and in a terminal needs no fake for it
- ➕ the model quits when the pipeline closes its channel, so `render` returns and `package main`
  writes the report as it already did. `q` stops watching without stopping the run. Task 13 replaces
  the first half of that with `CompletedMsg`
- ➕ `app/ui/view.go` also carries `tabBar`, the scroll window and the clipping helpers; the plan
  named the file without saying what beyond `View` lives in it. `doc.go` holds the package comment
  and no code, so it has no test file

### Task 13: TUI — findings browser

**Files:**
- Create: `app/ui/findings.go`, `app/ui/findings_test.go`
- Modify: `app/ui/model.go`, `app/ui/handlers.go`, `app/ui/view.go`, `app/main.go`, `app/main_test.go`

`app/main_test.go` is listed because the report-written-once and tty-gating tests drive `run(runOpts)`.
Both work off `runOpts.openTTY`, the seam task 5 put there — `tui.md` bans a test that needs a real tty.

**Design Contract:**

Type:
- `findingsState` (unexported — cursor, expansion and filter state)

Methods (full signatures):
- `(m Model) findingsPane() string`
- `(f *findingsState) move(delta int)`
- `(f *findingsState) toggle()`
- `(f *findingsState) filter(q string)`
- `(f *findingsState) visible() []finding.Finding`

All `findingsState` methods take pointer receivers — a value receiver on `visible` would copy cursor and
filter state on every render.

There is no `Model.Report()`. `Pipeline.Run` already returns the report, so `package main` holds it whichever
renderer ran, and a second copy inside the model would be a second source of truth that can drift — stale if
the TUI missed a late event, absent if the pipeline errored after the last one.

**The report reaches the TUI as a bubbletea message, not as a pipeline event.** After `Pipeline.Run` returns,
`package main` sends `CompletedMsg{Report finding.Report}` to the running program; `Update` stores it in
`findingsState` and switches panes. Routing completion through the event channel instead would put the one
signal that must arrive on the one path documented to drop under load — a dropped completion leaves the TUI
parked on the agent panes forever, which is a user-visible hang. The model holds the report only to render
it; `package main` remains the sole writer to stdout, so `tui.md`'s prohibition still holds.

Sequencing matters too: `package main` must not write stdout until the bubbletea program has returned, or
the report interleaves with the TUI's final frame.

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- `CompletedMsg` — `package main` constructs and sends it after `Pipeline.Run` returns, so it must be
  nameable from outside `app/ui`; everything else is task 12's `Model`, `ModelConfig` and `New`

- [x] create `app/ui/findings.go` rendering findings grouped by severity with cursor, expansion and filter
- [x] add `CompletedMsg{Report finding.Report}` and have `package main` send it after `Pipeline.Run` returns; `Update` stores the report and switches to the findings pane, keeping agent tabs reachable
- [x] bind `j`/`k` to move, `enter` to expand body and fix, `/` to filter, `f` to jump to findings
- [x] have `app/main.go` write `Pipeline.Run`'s report to stdout **after** `Program.Run()` returns, with a comment recording that the ordering is what keeps the report from interleaving with the TUI's final frame
- [x] write tests for navigation, expansion, filtering and the empty-findings state
- [x] write a main-level test asserting the report is written exactly once under the TUI path and once under `--no-tui`, and never twice
- [x] write a test asserting the TUI is gated on the tty being openable and **not** on stdout being a TTY, driving it through `runOpts.openTTY`: one case where the opener succeeds while stdout is a pipe (TUI runs), one where it fails (plain renderer). That is the `revmux --json > file` invocation, where the two conditions disagree
- [x] run tests - must pass before task 14

- ➕ `eventsDone` no longer quits, which is the other half of what `CompletedMsg` replaces. The model
  keeps the frame up until the report arrives, and the reader closes the browser himself — so under a
  tty a finished run waits for a keystroke, and a non-interactive caller passes `--no-tui`
- ➕ `package main` holds the renderer as a small `renderer` handle rather than running it inline.
  `Pipeline.Run` has to return before there is a report to hand over, so `review` starts the
  subscriber, runs the pipeline, sends `CompletedMsg` and only then waits. A failed run has nothing
  to browse, so it gets `Program.Quit()` instead and its error reaches stderr
- ➕ `findingsPane` returns `[]string`, matching `detailPane` and the pane router rather than the
  contract's `string`. `render` returns the lines **and** the line each row landed on, from one walk:
  keeping the cursor in view needs both, and locating it separately would be the same loop twice
- ➕ `Finding.location` is now exported as `Location`. The browser renders the same three anchor
  shapes as the markdown report, and a second copy in `app/ui` would be the one to drift
- ➕ the browser's tab is keyed `f`, not by number: it exists only once the report has arrived, so a
  fixed digit would name nothing for most of the run
- ➕ a tty test file cannot carry both frames and keystrokes — the frame written at open advances the
  shared offset past the input — so `main_test.go` has two openers: a file for asserting on rendered
  output, a pipe for feeding keys

### Task 14: Full default prompt set

**Files:**
- Create: `app/prompt/defaults/lenses/impl.md`, `architecture.md`, `quality.md`, `docs.md`, `tests.md`
- Create: `app/prompt/defaults/prompts/profiles/comprehensive.md`, `final.md`
- Modify: `app/prompt/defaults/lenses/bugs.md`, `adversarial.md`, `app/prompt/defaults/prompts/profiles/focused.md`
- Modify: `app/config.go`, `app/defaults/config`
- Modify: `app/prompt/defaults_test.go`

Content shared by every lens (output contract, severity language, project-profile calibration) belongs in
the profile bodies, not duplicated per lens.

- [x] author the five remaining lenses: impl, architecture, quality, docs, tests
- [x] author `comprehensive.md` with the default roster: bugs+impl, arch+quality, docs+tests, codex/adversarial, each with a distinct ANSI-16 `color` so the shipped profile does not lean on palette assignment
- [x] author `final.md` as a narrow last pass: two agents, critical and major only
- [x] lift shared preamble out of the five new lenses into the profile bodies; `bugs.md` and `adversarial.md` were already authored against the task 4 profile body, so there is nothing to move for those two
- [x] write a test asserting every shipped profile parses, names only existing lenses and passes validation
- [x] write a test asserting every shipped lens file is non-empty and contains no unresolved `{{VAR}}`
- [x] write a test asserting every shipped profile body instructs the agent to read the path variables, so a lens set can never ship with context the agents silently ignore
- [x] write a test asserting no shipped prompt file mentions prior rounds — that block is injected, and a profile duplicating it would drift from the injected text
- [x] give every shipped lens, profile **and stage prompt** a `description:` one-liner, and write a test asserting none is missing or empty — `revmux config` is the caller model's only view of the lens set, and a blank description there makes a lens uncomposable. `focused.md` and the two stage prompts were authored in task 4, so this pass edits them rather than creating them
- [x] **flip `--profile`'s default from `focused` to `comprehensive` now that it exists**, updating the struct tag and `app/defaults/config` together. Task 5 deliberately defaulted to the only profile shipped at the time; leaving it there would mean the flagship roster never runs unless asked for by name. The README flag table is **not** touched here — task 17 authors it, against whatever the default is by then
- [x] run tests - must pass before task 15

### Task 15: Config introspection command

**Files:**
- Create: `app/introspect.go`, `app/introspect_test.go`
- Modify: `app/config.go`, `app/config_test.go`, `app/main.go`, `app/main_test.go`
- Verify only: `.claude/rules/config.md` already lists `app/introspect.go` in its `paths:` frontmatter,
  so its `revmux config` section loads when that file is edited. Confirm rather than re-add

revmux is driven by a caller model, and that caller has to compose an invocation without reading the
source: which profiles exist, which lenses each one's roster carries, which models and effort levels those
agents run on, and where every value came from. `--help` lists flags and says nothing about the prompt tree,
so today the only way to answer any of it is to read `~/.config/revmux/`.

`revmux config` prints the fully resolved configuration as JSON on stdout and exits 0.

**Design Contract:**

Type:
- `configCmd` (unexported — the go-flags subcommand)
- `catalog` (unexported — the emitted document)
- `knob` (unexported — one runtime setting: `Name`, `Value`, `Source`)

Methods (full signatures):
- `(c *configCmd) Execute(args []string) error` — records the selection only
- `(o options) catalog(set *prompt.Set) catalog`
- `(o options) knobs() []knob`

Value receivers on the two `options` methods, matching `promptOpts`, `executorOpts`, `resolveContext` and
`runName` from task 5. A pointer receiver on the same type for these two alone is the inconsistency
`.golangci.yml` flags.

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none — the whole command lives in `package main`

A **subcommand**, not another meta flag, because that is what a caller types and what `--help` then
documents. It costs one line: register it with `go-flags` and set `parser.SubcommandsOptional = true`, so
`revmux --task pr-123` keeps working unchanged with no command word.

**`Execute` records the selection; `run` does the work.** go-flags invokes `Commander.Execute` from inside
`parser.Parse()`, which is inside `parseArgs()` — before `runOpts` exists, so `Execute` can reach neither
the injected stdout writer nor a loaded `*prompt.Set`. Writing the catalog there would bypass the seam task
5 exists to provide and leave task 15's own tests unable to capture the output. So `Execute` sets a field
on the enclosing `options` and returns nil; `run(o runOpts) int` loads the prompt set, builds the catalog
and writes it to `o.stdout`, then returns 0 before constructing a pipeline, archive or TUI.
`configCmd` therefore holds a back-pointer to its `options`, which is also how `catalog` reaches the
resolved knobs.

The document is machine-first — a model parses it, a human reads it rarely — so JSON is the only format and
there is no `--format` flag to add one that nobody asked for. It carries:

- **knobs** — every runtime setting with its resolved value *and* which layer supplied it (flag, project
  config, user config, compiled-in default). The value alone is not enough: a caller that wants to know
  whether `--stagger-delay` is worth passing needs to know it is currently a default rather than something
  the user chose
- **profiles** — name, description, and the resolved roster: per agent the name, lenses, executor, model,
  effort and color, exactly as `Profile.Roster(nil, set.LensNames())` returns them, so what is printed is what would run —
  including the palette color an entry without explicit `color:` front matter was given
- **lenses** — name and description per `Set.Lenses()`, which is what a caller needs to compose `--lenses`
- **stages** — `synthesis` and `verify` with their description, executor, model and effort. They carry the
  same front matter as a profile and can name their own binary, so a caller reasoning about what a run
  costs or which model judges its findings needs them reported too
- **vocabulary** — the valid `executor` and `effort` values, read from the same constants `validate` uses.
  Hardcoding a second copy here means a new effort level ships working but undiscoverable
- **paths** — resolved `tasks_dir`, `config_dir` and `workdir`, plus the existing task names under
  `tasks_dir`, since `--run` collides with an existing round and a caller cannot avoid that blind

**This is the one carve-out in "stdout belongs to the report".** `revmux config` runs no pipeline, so there
is no report to collide with and no TUI to gate — it prints and exits before any of that exists.

It reports what **resolved**, never what is embedded. A user who overrode one lens and added another must
see his tree, or the catalog describes a review that will not happen.

- [x] add `configCmd` to `app/config.go` and register it with `parser.SubcommandsOptional = true`, verifying in the same step that a plain `revmux --task x` still parses with no command word
- [x] implement `options.knobs` reading the `knobOrigins` map task 5 records during config load — do not re-derive it here, and do not add a second tracking mechanism
- [x] implement `options.catalog` assembling knobs, profiles with resolved rosters and descriptions, lenses with descriptions, the vocabularies from `prompt.Efforts()` and `prompt.Executors()`, and the resolved paths plus existing task names
- [x] emit the catalog as indented JSON to `runOpts.stdout` from `run`, returning 0 before any pipeline, archive or TUI is constructed — `Execute` itself writes nothing
- [x] write a test asserting the emitted JSON contains every shipped profile, its full roster with per-agent model and effort, and every shipped lens with a non-empty description
- [x] write a test with `t.TempDir()` config and prompt trees asserting an overridden lens is reported with the override's own description and an added lens appears, since the catalog must describe what resolves rather than what is embedded
- [x] write a test asserting a knob set by flag reports source `flag` and an untouched one reports `default`
- [x] write a test comparing the catalog's effort and executor vocabularies against `prompt.Efforts()` and `prompt.Executors()` directly. Comparing against a literal here would verify nothing — it would only assert that two hardcoded lists in the test and the catalog agree, which is the drift the accessor exists to prevent
- [x] write a test asserting `revmux config` writes nothing to the tasks directory and creates no run directory
- [x] run tests - must pass before task 16

### Task 16: Verify acceptance criteria

Everything here is checkable without spawning a model — anything needing a live agent is in Post-Completion.

- [ ] verify all requirements from Overview are implemented
- [ ] verify revmux runs no git command and imports no VCS library: `go list -deps ./... | grep -iE 'go-git|libgit2|golang.org/x/tools/go/vcs'` finds nothing, and `grep -rn 'exec.Command' app/ | grep -v '^app/executor/'` finds nothing. **Do not grep the dependency list for the bare substring `git`** — every `github.com/...` path contains it, including revmux's own module path, so that check can only ever report a false positive
- [ ] verify with a mocked runner that revmux writes nothing outside `runs/<run>/`, that a second run under a new name coexists with the first, and that reusing a name fails without overwriting
- [ ] verify a composed prompt contains only paths and no file contents, including when `--tasks-dir` points outside the repo
- [ ] verify the report and `manifest.json` show tokens per agent and a run total matching their sum
- [ ] verify a killed agent is retried once, then degrades without aborting the run, and that an all-degraded run exits 2
- [ ] verify `--json` output round-trips against the documented shape, and that `--json > file` still enables the TUI when a tty is openable
- [ ] verify exit codes 0, 1 and 2 in their respective conditions
- [ ] verify `revmux config` output is enough to compose a `--lenses` invocation without reading the prompt tree: every lens named there loads, and every profile's reported roster matches what a run of that profile actually dispatches
- [ ] run full test suite: `make test`
- [ ] run `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0`
- [ ] verify test coverage meets project standard

### Task 17: [Final] Update documentation

- [ ] write README.md covering install, the three stages, the config tree, profiles and lenses, every flag, exit codes, and `revmux config` with a sample of its output — the caller model reads that section to learn how to drive the tool
- [ ] update CLAUDE.md and `.claude/rules/*.md` where the build diverged from what they describe
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification** (needs live agents, so none of it is a plan checkbox):

- run a full comprehensive review against a real repository and confirm the report is sane
- run a full comprehensive review against a substantial change and judge the findings quality against a
  review produced by session-driven fan-out
- confirm agents actually read the path variables they are handed, including with `--tasks-dir` outside the repo
- observe rate-limit behavior across several consecutive full runs
- confirm the TUI behaves at various terminal sizes and under `--no-tui`

**Follow-up work, out of scope here**:

- a bundled skill shipped with this repo so AI coding tools can launch revmux directly
- terminal-integration launcher for one-keystroke runs
- release workflow and distribution once the tool stabilizes

---

Smells pre-check: 27 items fixed before save — shared `proc` executor base to satisfy `dupl`, per-run
`Request` carrying model/effort so one executor serves several roster entries, single owner for executor
construction via an exported `NewRunner` factory, `Pipeline` split into `finder`/`synthesizer`/`verifier`, `sink`
adapter instead of `Pipeline` implementing `executor.EventSink`, `Profile`/`Stage` split with composition
hung off `Profile`, `AgentSpec.Executor` field replacing a `Runner()` getter, filtering unified through
`Report.Above` so renderers and exit code agree, TUI gated on the tty rather than stdout, report returned
from the TUI instead of written inside `app/ui`, `app/run` renamed to `app/archive` with a consumer-side
`archiver` interface, `stagger.acquire`/`release` naming, struct-typed `combinedEntry` and `reviewContext`
to remove same-type swap hazards, pointer receivers on `findingsState`, and several unused or unjustified
exports removed.

Review round 1 (plan-review agent + codex, both against this plan, `CLAUDE.md` and `.claude/rules/*.md`):
the runner factory was unbuildable from `package main` (lowercase field returning an unexported interface),
the archive was specified as a second reader on a channel that distributes rather than broadcasts,
`sources` was left for the model to fill despite being the input to the confidence boost,
no clock was injected anywhere despite `testing.md` banning wall-clock waits,
the stagger's release signal had no wire, caller-supplied `--task` / `--run` / agent names were joined into
paths with no containment check, the archive could not reach the raw bytes it was required to tee,
a failed archive write was treated as a warning against a rule requiring auditable runs,
all-degraded had no path to exit 2, `Stage` could not name its own executor, and fixture capture was written
as a human step in a plan meant to run unattended. Two contradictions were residue from the same session's
earlier edits: a revived `runs/2` allocator and `context/` still documented as holding prior rounds.
`.claude/rules/pipeline.md` needed the same corrections as the plan for the factory shape and for scope
arriving as a path rather than a text blob.
