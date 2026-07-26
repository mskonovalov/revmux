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
terminals. The TUI is one subscriber; `--no-tui` is another. The TUI never writes stdout — it carries the
final report out through its model state and `package main` writes it.

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

Precedence, per file: CLI flags > `./.revmux/` > `~/.config/revmux/` > embedded defaults.

**Task directory** — the only channel review context travels through. `--task <id>` selects one under
`--tasks-dir` (default `./.revmux/tasks`, relocatable to `/tmp` or anywhere else), and `--run <name>` names
the round inside it. Both are caller-chosen and semantic (`--task pr-123 --run after-fix`); revmux allocates
neither. A fix-and-re-review loop re-runs one task under successive run names and accumulates rounds:

```
<tasks-root>/pr-123/          # caller-owned, revmux never writes or prunes anything here
├── scope.md                  # → {{SCOPE}}    required; missing or empty is a load-time error
├── goal.md                   # → {{GOAL}}     optional
├── profile.md                # → {{PROFILE}}  optional
├── context/                  # → {{CONTEXT}}  optional dir: prior rounds, ticket text, design notes
└── runs/                     # revmux-owned: the only thing it writes, and all keep_runs prunes
    └── after-fix/            # --run, caller-named; defaults to a UTC timestamp when omitted
        ├── manifest.json     # roster, prompt provenance + hashes, requested vs actual model, timings
        ├── prompts/          # composed prompt per agent and per stage, post-substitution
        │   ├── bugs+impl.md
        │   ├── codex.md
        │   ├── synthesis.md
        │   └── verify.md
        ├── stages/           # findings after each stage
        │   ├── 1-found.json
        │   ├── 2-synthesized.json
        │   └── 3-verified.json
        ├── events.jsonl      # revmux's own decisions: stalls, retries, degrades, stage changes
        ├── bugs+impl.jsonl   # claude stream-json, verbatim tee
        ├── codex.log         # codex prose, verbatim tee
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
agents:
  - {name: bugs+impl,    lenses: [bugs, impl]}
  - {name: arch+quality, lenses: [architecture, quality]}
  - {name: docs+tests,   lenses: [docs, tests]}
  - {name: codex, executor: codex, lenses: [adversarial],
     model: gpt-5.6-sol, effort: xhigh}
---
Apply every lens you carry in full, and tag each finding with the lens that raised it.
Report problems only...
```

`executor` accepts only `claude` (default when omitted) and `codex`; anything else is a load-time config
error. `synthesis.md` and `verify.md` carry the same three keys in their own front matter.

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
{ "scope": {...},
  "sources": {"expected":4,"reported":3,"degraded":["docs+tests"]},
  "findings":[{"id","file","line","end_line","severity","confidence","title","body","fix",
               "sources":["bugs+impl","codex"], "lenses":["bugs","adversarial"],
               "verdict":"confirmed"}],
  "open_questions":[...], "pre_existing":[...],
  "stats":{"duration_ms":…,"tokens":…} }
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
model said (`<agent>.jsonl` / `.log`, verbatim tees) and what revmux decided (`events.jsonl` — stalls,
retries, degrades, stage transitions; neither is derivable from the other).

The second is a **self-reflection agent** that reads a task's accumulated runs and proposes changes to the
lens and profile text. That is a later, separate deliverable, but the data it needs must be captured from the
first run or the corpus is worthless. It has to answer two questions the final report cannot:

- *which lens text raised this finding* — needs `prompts/<agent>.md`, the composed prompt post-substitution,
  exactly the bytes the model saw, plus `manifest.json` recording which of the three precedence layers each
  lens file came from and its content hash, since two runs of one task can use different lens text
- *did synthesis or verify drop something real* — needs `stages/`, the findings snapshot after each stage.
  Reconstructing the pre-synthesis set by re-parsing every agent's `structured_output` is possible but
  fragile, and impossible at all for a degraded source

`manifest.json` also records requested-vs-actual model per agent, since `--model` can be silently ignored and
a reflection agent drawing conclusions about "the opus lens" needs to know what actually ran.

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

- [ ] scaffold module, Makefile, `.golangci.yml` and CI workflow
- [ ] set module path to `github.com/umputun/revmux` and build target to `./app`
- [ ] pin dependency versions (go 1.26, bubbletea v1.3.10, bubbles v1.0.0, lipgloss v1.1.0, go-flags v1.6.1, testify v1.11.1, yaml.v3)
- [ ] add `app/main.go` printing version and exiting, so `make build` and `make test` are green from the start
- [ ] write a smoke test asserting the binary's version output
- [ ] run `go mod vendor`
- [ ] run tests, lint and formatter - must pass before task 2

### Task 2: Finding types and report rendering

**Files:**
- Create: `app/finding/finding.go`, `app/finding/report.go`, `app/finding/schema.go`
- Create: `app/finding/finding_test.go`, `app/finding/report_test.go`, `app/finding/schema_test.go`

The data contract every other package produces or consumes. Built first so nothing downstream invents its
own shape.

**Design Contract:**

Type:
- `Finding` (exported — consumed by `app/pipeline`, `app/ui`, `package main`)
- `Report` (exported — same consumers)
- `SourceStatus` (exported — field of `Report`)
- `Stats` (exported — field of `Report`)

Methods (full signatures):
- `(r Report) Markdown(w io.Writer) error`
- `(r Report) JSON(w io.Writer) error`
- `(r Report) Above(minConfidence int) Report`
- `(r Report) ExitCode() int`
- `(s SourceStatus) Degraded() bool`

`Above` returns a filtered `Report`, not a slice — `package main` filters once and both the renderers and
`ExitCode` operate on that same filtered value, so the printed report can never disagree with the exit code.

Standalone helpers planned (justification why NOT a method):
- `Schema() json.RawMessage` — entry point returning the JSON Schema for the findings contract; consumed by
  `app/executor`, which holds no `Finding` value to call a method on

Exports (justification per item: who outside the package calls this?):
- `Finding`, `Report`, `SourceStatus`, `Stats` — `app/pipeline` builds them, `app/ui` renders them, `package main` writes them
- `Report.Markdown` / `Report.JSON` / `Report.Above` / `Report.ExitCode` — called from `app/main.go`
- `SourceStatus.Degraded` — called from `app/pipeline` synthesis and from `app/ui` for the banner
- `Schema` — called from `app/executor` to build the `--json-schema` argument

- [ ] create `app/finding/finding.go` with `Finding` (id, file, line, end_line, severity, confidence, title, body, fix, `sources` agent names, `lenses` lens names, verdict) — no `tags` field, and `sources` never holds a lens name
- [ ] `line` is the anchor and `end_line` is optional: zero means a single line, and `line` itself zero means a file-level finding with no line at all
- [ ] create `app/finding/report.go` with `Report`, `SourceStatus`, `Stats`, and `Degraded`; `Stats` carries duration and the run's token total, nothing derived from it
- [ ] implement `Report.Markdown` grouping by severity with a degraded-sources banner when `Degraded()`
- [ ] implement `Report.JSON` emitting the documented shape, plus `Above` and `ExitCode`
- [ ] create `app/finding/schema.go` with `Schema()` returning the findings JSON Schema
- [ ] write tests for markdown rendering (with and without degraded banner, empty report, all severities)
- [ ] write tests for JSON round-trip, `Above` filtering and `ExitCode` mapping (0/1)
- [ ] write a test asserting `Schema()` is valid JSON and requires the fields the renderers rely on
- [ ] write a test asserting `sources` and `lenses` are distinct fields that survive a JSON round-trip independently, so a future refactor cannot quietly merge them
- [ ] write tests for location rendering across all three shapes: single line, `line`-`end_line` range, and file-level with no line
- [ ] run tests - must pass before task 3

### Task 3: Supervised claude executor

**Files:**
- Create: `app/executor/executor.go`, `app/executor/proc.go`, `app/executor/claude.go`, `app/executor/stream.go`
- Create: `app/executor/procgroup_unix.go`, `app/executor/procgroup_windows.go`
- Create: matching `_test.go` for each
- Create: `app/executor/testdata/` fixtures, `app/executor/mocks/`

**Design Contract:**

Type:
- `CommandRunner` (exported interface — consumer-side, mocked in tests)
- `EventSink` (exported interface — implemented by an adapter in `app/pipeline`)
- `Event` (exported — one activity item emitted to the sink)
- `Opts` (exported — construction options: timeouts, working directory)
- `Request` (exported — per-run inputs: prompt, model, effort, schema)
- `Result` (exported — returned to `app/pipeline`)
- `Claude` (exported — the claude executor, embeds `proc`)
- `proc` (unexported — shared run loop, idle watchdog, process-group teardown, line reader)
- `streamEvent` (unexported — wire shape of one stream-json line)
- `processGroupCleanup` (unexported)

Methods (full signatures):
- `(c *Claude) Run(ctx context.Context, req Request, sink EventSink) (Result, error)`
- `(c *Claude) args(req Request) []string`
- `(c *Claude) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result`
- `(c *Claude) event(line string) (streamEvent, bool)`
- `(p *proc) start(ctx context.Context, argv []string, prompt string) (io.Reader, error)`
- `(p *proc) readLines(ctx context.Context, r io.Reader, handler func(string)) error`
- `(p *proc) setupProcessGroup(cmd *exec.Cmd)`
- `(p *proc) childEnv() []string`
- `(pg *processGroupCleanup) wait() error`
- `(pg *processGroupCleanup) watchForCancel(cancelCh <-chan struct{})`
- `(pg *processGroupCleanup) killProcessGroup()`

Model and effort live on `Request`, not `Opts`, so one executor instance serves roster entries with
different models. `Request.Schema` is claude-only and stays empty for codex.

Standalone helpers planned (justification why NOT a method):
- `NewClaude(runner CommandRunner, opts Opts) *Claude` — constructor
- `newProcessGroupCleanup(cmd *exec.Cmd, cancelCh <-chan struct{}) *processGroupCleanup` — constructor

Exports (justification per item: who outside the package calls this?):
- `CommandRunner`, `Opts`, `Request`, `Result`, `Claude`, `NewClaude` — constructed in `package main`, called from `app/pipeline`
- `EventSink`, `Event` — `app/pipeline`'s adapter implements the interface and consumes the event

- [ ] create `app/executor/executor.go` with `CommandRunner`, `EventSink`, `Event`, `Opts`, `Request`, `Result` and the moq `go:generate` directive
- [ ] create `app/executor/proc.go` with the shared `proc`: start, idle-timeout arming and reset, hard timeout, child-env scrubbing (`CLAUDECODE` always, `ANTHROPIC_API_KEY` unless `--preserve-anthropic-api-key`), and `readLines` as a method (no standalone `linereader.go`)
- [ ] create `app/executor/procgroup_unix.go` / `procgroup_windows.go` with `Setsid`, SIGTERM→grace→SIGKILL on the process group, early return on `ESRCH`, and kill-on-normal-exit to reap orphans
- [ ] create `app/executor/stream.go` decoding stream-json lines, extracting `structured_output`, `modelUsage`, per-model `usage` token counts and `rate_limit_event`
- [ ] create `app/executor/claude.go` embedding `proc`, building flags from `Opts` + `Request`, setting `Result.IdleTimedOut` when the derived context fired but the parent is alive
- [ ] record the five stream fixtures into `app/executor/testdata/` (clean, stalling, rate-limited, model-mismatch, truncated)
- [ ] run `go generate ./...` to produce `app/executor/mocks/`
- [ ] write tests for the clean run (findings extracted, tokens and model reported)
- [ ] write tests for the stall fixture (idle timeout fires, `IdleTimedOut` set, no error returned)
- [ ] write tests for the rate-limit and truncated-stream fixtures
- [ ] write a test asserting `Result` reports the model from `modelUsage`, not the requested one
- [ ] write a test asserting `CLAUDECODE` is absent from the child environment, that `ANTHROPIC_API_KEY` is stripped by default, and that `--preserve-anthropic-api-key` passes it through
- [ ] write tests for `args(req)` covering every flag including `--disable-slash-commands` and the schema
- [ ] verify `GOOS=windows GOARCH=amd64 go build ./...`
- [ ] run tests - must pass before task 4

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
- `Stage` (exported — a body plus model/effort; `synthesis.md` and `verify.md`, no roster)
- `AgentSpec` (exported — one roster entry)
- `Vars` (exported — `map[string]string`)
- `LoadOpts` (exported — search paths)
- `doc` (unexported — shared front-matter + body carrier behind `Profile` and `Stage`)

`AgentSpec` fields: `Name string`, `Lenses []string`, `Executor string`, `Model string`, `Effort string`.
`Executor` is a plain exported field, not a getter — the front-matter key is `executor` and the vocabulary
stays singular across all three layers.

Methods (full signatures):
- `(s *Set) Profile(name string) (*Profile, error)`
- `(s *Set) Stage(name string) (*Stage, error)`
- `(s *Set) lens(name string) (string, error)`
- `(p *Profile) Roster(lensOverride []string) ([]AgentSpec, error)`
- `(p *Profile) Compose(set *Set, spec AgentSpec, opts ComposeOpts) (string, error)`
- `(p *Profile) validate() error`
- `(st *Stage) Compose(opts ComposeOpts) (string, error)`
- `(s *Set) Provenance() []FileOrigin`

`ComposeOpts` carries `Vars` and the prior-round `History` block. It exists so `Profile.Compose` stays at
three parameters rather than four, and so the history travels by the same route into both compose paths.

`Compose` hangs off `Profile` because the composed prompt is profile body + lens files; an `AgentSpec` alone
carries no body. `Stage.Compose` needs no `AgentSpec` at all.

`Provenance` reports which precedence layer each loaded file came from plus its content hash. `app/archive`
records it in `manifest.json` — without it a later reflection agent cannot tell whether two rounds of one
task ran the same lens text.

Standalone helpers planned (justification why NOT a method):
- `Load(opts LoadOpts) (*Set, error)` — constructor
- `splitFrontMatter(b []byte) (meta []byte, body []byte, err error)` — called only by `Load`, which is itself
  a standalone constructor; parsing is eager so no `Set` method ever calls it

Exports (justification per item: who outside the package calls this?):
- `Set`, `Profile`, `Stage`, `AgentSpec`, `Vars`, `LoadOpts`, `Load` — `package main` builds `LoadOpts` via
  `options.promptOpts()` and loads the set; `app/pipeline` resolves profiles and composes prompts

- [ ] create `app/prompt/prompt.go` with `Set`, `LoadOpts`, `Load`, the unexported `doc`, and per-file precedence resolution
- [ ] create `app/prompt/defaults.go` with the `go:embed` directive over `defaults/`
- [ ] create `app/prompt/roster.go` parsing YAML front matter into `Profile` / `Stage` / `AgentSpec`, applying top-level model/effort as defaults and per-entry values as overrides
- [ ] implement `validate`: unknown executor, unknown effort, missing lens, duplicate agent name, and empty roster are all load-time errors
- [ ] implement `Profile.Roster` applying a lens override while keeping the profile body
- [ ] create `app/prompt/compose.go` with `Profile.Compose` and `Stage.Compose`, substituting `Vars` and failing on an unresolved `{{VAR}}`
- [ ] append `ComposeOpts.History` to every composed prompt after substitution — an injection, never a `{{VAR}}`, so no lens or overridden profile can omit it; skip it entirely when there are no prior rounds
- [ ] implement `Set.Provenance` returning the winning precedence layer and content hash per loaded file
- [ ] author the minimal default `focused.md` profile, `bugs.md` and `adversarial.md` lenses, and `synthesis.md` / `verify.md` stage prompts
- [ ] the profile body must tell agents that `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}` and `{{CONTEXT}}` are paths to read, not text — a path handed to an agent with no instruction is a path it may ignore
- [ ] write tests for precedence (embedded, user override, project override, single-file override) using `t.TempDir()`
- [ ] write tests for roster parsing including defaults, per-entry overrides and every validation error
- [ ] write tests for lens override, for composition with missing variables resolving to an explicit placeholder, and for the unresolved-variable failure
- [ ] write a test asserting the history block reaches a composed prompt whose profile and lenses never mention it, and is absent when there are no prior rounds
- [ ] write a test asserting the injected block carries its re-evaluate-independently instruction, so the data can never appear without the guard
- [ ] write tests for `Provenance` reporting the correct layer and a hash that changes when an override file changes
- [ ] run tests - must pass before task 5

### Task 5: Configuration and CLI options

**Files:**
- Create: `app/config.go`, `app/config_test.go`
- Modify: `app/main.go`

INI holds runtime knobs only. Everything shaping the review lives in the markdown.

**Design Contract:**

Type:
- `options` (unexported — go-flags struct, `package main` only)
- `reviewContext` (unexported — the resolved absolute paths for one task directory)

Methods (full signatures):
- `(o options) promptOpts() prompt.LoadOpts`
- `(o options) executorOpts() executor.Opts`
- `(o options) contextVars() (prompt.Vars, error)`
- `(o options) resolveContext() (reviewContext, error)`

`reviewContext` fields are `Scope`, `Goal`, `Profile`, `Context` — all absolute paths, all resolved once.
`resolveContext` returns that struct, never `(scope string, goal string, profile string, err error)`:
adjacent same-typed strings transpose silently and feed the project profile into `{{GOAL}}`.
`contextVars` builds on `resolveContext` rather than re-statting the same directory.

Standalone helpers planned (justification why NOT a method):
- `parseArgs() (options, error)` — entry point; runs before any `options` value exists

Exports (justification per item: who outside the package calls this?):
- none — `package main` internals

- [ ] create `app/config.go` with the `options` struct: `--task`, `--run`, `--tasks-dir`, `--profile`, `--lenses`, `--workdir`, `--min-confidence`, `--no-synthesis`, `--no-verify`, `--no-tui`, `--json`, `--preserve-anthropic-api-key`, `--config-dir`, `--init`, `--dump-defaults`
- [ ] `--run` is optional and defaults to a UTC timestamp; resolve it in `package main` and pass the resolved value down rather than re-deriving it
- [ ] add INI parsing via go-flags `IniParser` with precedence CLI > project > user > embedded, `no-ini:"true"` on meta flags and `ini-name` tags matching long flag names
- [ ] add runtime knobs to the INI: `idle_timeout`, `hard_timeout`, `stagger_delay`, `max_parallel`, `tasks_dir`, `keep_runs`, verifier group cap, default profile
- [ ] implement `resolveContext` against `<tasks-dir>/<task>/`: `scope.md` required (missing or empty is a load-time error), `goal.md` / `profile.md` / `context/` optional, all returned as absolute paths
- [ ] absent `goal.md` or `profile.md` is a non-error that marks the report generically calibrated; a missing task directory is an error, since revmux never creates one it did not author
- [ ] implement `contextVars` assembling `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}` as paths, substituting the "none provided" placeholder for anything absent
- [ ] implement `--init` and `--dump-defaults`, never overwriting a customized file
- [ ] write tests for config precedence across all four layers using `t.TempDir()`
- [ ] write tests for task-dir resolution: full directory, scope-only, missing `scope.md`, empty `scope.md`, missing task directory, and a `--tasks-dir` pointed outside the repo
- [ ] write a test asserting every resolved variable is an absolute path and that no file is opened during resolution
- [ ] write tests for `--init` and `--dump-defaults` against a temp dir
- [ ] run tests - must pass before task 6

### Task 6: Find stage, event channel and end-to-end slice

**Files:**
- Create: `app/pipeline/pipeline.go`, `app/pipeline/event.go`, `app/pipeline/find.go`, `app/pipeline/sink.go`
- Create: matching `_test.go` for each, plus `app/pipeline/mocks/`
- Create: `app/progress.go`, `app/progress_test.go`
- Modify: `app/main.go`

First working slice: one lens agent, no stagger, no synthesis, no verify, no TUI.
With `.revmux/tasks/demo/scope.md` written by hand,
`revmux --task demo --profile focused --lenses bugs --no-synthesis --no-verify` produces a real markdown report.

**Design Contract:**

Type:
- `Pipeline` (exported — constructed by `package main`)
- `Config` (exported — construction options, including `newRunner func(prompt.AgentSpec) agentRunner`)
- `Event` (exported — emitted on the channel)
- `EventKind` (exported — enum)
- `agentRunner` (unexported interface, consumer-side): `Run(ctx context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error)`
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
- `(r sourceResult) ok() bool`
- `(s sink) Emit(ev executor.Event)`
- `(pr *progress) run(events <-chan Event)`
- `(pr *progress) line(ev Event) string`

`Pipeline` does not implement `executor.EventSink` — the `sink` adapter does. That avoids an exported method
whose only purpose is interface satisfaction, and avoids two methods differing only by case.
`Config.newRunner` is supplied by `package main`, which keeps the interface consumer-side while letting
`finder` pick an executor per roster entry.

Standalone helpers planned (justification why NOT a method):
- `New(cfg Config) *Pipeline` — constructor

Exports (justification per item: who outside the package calls this?):
- `Pipeline`, `Config`, `New`, `Event`, `EventKind` — `package main` constructs and runs the pipeline;
  `app/progress.go` and later `app/ui` consume events

- [ ] create `app/pipeline/event.go` with `Event`, `EventKind` (agent started, tool activity, state change, agent done, agent degraded, stage change, rate limit) and a buffered channel that drops rather than blocks
- [ ] create `app/pipeline/sink.go` with the `sink` adapter tagging executor events with the agent name
- [ ] create `app/pipeline/pipeline.go` with `Config`, `New`, `Run`, `Events`, `emit`, and the `agentRunner` interface plus its moq directive
- [ ] create `app/pipeline/find.go` with `finder` running the roster sequentially for now and collecting `sourceResult` values
- [ ] create `app/progress.go` with the `progress` type subscribing to the channel and writing timestamped lines to stderr
- [ ] wire `app/main.go`: parse options, load prompt set, build the runner factory, run the pipeline, filter via `Above`, write the report to stdout, exit with `ExitCode`
- [ ] write pipeline tests with a mocked `agentRunner` asserting the emitted event sequence
- [ ] write tests for the find stage collecting results and for `sourceResult.ok`
- [ ] write tests for the stderr progress renderer against a synthetic event sequence
- [ ] write a main-level test running the whole slice with a mocked runner and asserting stdout markdown and exit code
- [ ] run tests - must pass before task 7

### Task 7: Parallel fan-out, staggered launch and degrade policy

**Files:**
- Create: `app/pipeline/stagger.go`, `app/pipeline/stagger_test.go`
- Modify: `app/pipeline/find.go`, `app/pipeline/find_test.go`, `app/config.go`

**Design Contract:**

Type:
- `stagger` (unexported — releases agents on a delay and caps concurrency)

Methods (full signatures):
- `(s *stagger) acquire(ctx context.Context, index int) error`
- `(s *stagger) release()`
- `(s *stagger) leaderStarted()`

Named for what they do: `acquire` takes a slot, `release` gives it back. `leaderStarted` is idempotent —
`finder` calls it on agent 1's first stream event and every call after the first is a no-op.

Standalone helpers planned (justification why NOT a method):
- `newStagger(cap time.Duration, maxParallel int) *stagger` — constructor; `cap` is the timeout, not a delay

Exports (justification per item: who outside the package calls this?):
- none — used only inside `app/pipeline`

- [ ] create `app/pipeline/stagger.go` releasing agent 1 immediately and the rest once `leaderStarted` fires or `stagger_delay` elapses, with a `max_parallel` semaphore
- [ ] the stagger must not group, reorder or filter agents by model or executor — it releases in roster order and nothing else
- [ ] convert `finder.run` to run the roster concurrently through the stagger, preserving deterministic result ordering
- [ ] implement retry-once on `IdleTimedOut` or process failure, emitting a retry event
- [ ] implement degrade: second failure marks the source degraded, the pipeline continues, and `SourceStatus` records it
- [ ] write tests asserting agent 1 is released before the others and that `max_parallel` is respected
- [ ] write tests for both release paths: `leaderStarted` fires and the rest launch immediately, and agent 1 never emits so the `stagger_delay` cap releases them instead
- [ ] write tests for retry-once then degrade, asserting the pipeline completes and `Degraded()` is true
- [ ] write a test asserting result ordering is deterministic regardless of completion order
- [ ] write a test asserting a degraded run still produces a report with the banner
- [ ] run tests - must pass before task 8

### Task 8: Codex executor

**Files:**
- Create: `app/executor/codex.go`, `app/executor/codex_test.go`
- Create: `app/executor/testdata/codex-*.txt` fixtures
- Modify: `app/main.go`

**Design Contract:**

Type:
- `Codex` (exported — embeds `proc`, selected by `executor: codex`)

Methods (full signatures):
- `(c *Codex) Run(ctx context.Context, req Request, sink EventSink) (Result, error)`
- `(c *Codex) args(req Request) []string`
- `(c *Codex) outputContract() string`
- `(c *Codex) extract(raw string) (json.RawMessage, error)`

Standalone helpers planned (justification why NOT a method):
- `NewCodex(runner CommandRunner, opts Opts) *Codex` — constructor

Exports (justification per item: who outside the package calls this?):
- `Codex`, `NewCodex` — constructed inside the `newRunner` factory in `package main`

- [ ] create `app/executor/codex.go` embedding `proc`, invoking `codex exec --sandbox read-only` with model and effort from `Request`
- [ ] implement `outputContract` appending the "return only JSON matching this shape" instruction, since codex has no `--json-schema`
- [ ] implement `extract` pulling the JSON block out of the final output, tolerating surrounding prose
- [ ] tick the idle watchdog on raw stdout writes rather than parsed events
- [ ] filter codex stderr to the resolved model/sandbox/effort header lines, once per process
- [ ] implement the `newRunner` factory in `package main` selecting claude or codex per `AgentSpec.Executor`
- [ ] record codex fixtures: clean JSON output, JSON wrapped in prose, no JSON at all, stalled
- [ ] write tests for `args()` and `outputContract()`
- [ ] write tests for `extract` against all four fixtures including the graceful failure path
- [ ] write a test asserting a codex roster entry routes to the codex executor and a claude entry does not
- [ ] confirm `dupl` reports no duplication between the two executors
- [ ] run tests - must pass before task 9

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
- `(s *synthesizer) parse(raw json.RawMessage) (finding.Report, error)`

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none — internal to `app/pipeline`

- [ ] create `app/pipeline/synthesize.go` composing the synthesis prompt from the `synthesis.md` stage with `{{FINDINGS}}` and `{{SOURCES}}`
- [ ] build `{{SOURCES}}` from the real roster: which agents ran, which degraded, which emitted each finding
- [ ] run the stage through the configured executor and parse its structured output into `finding.Report`
- [ ] honor `--no-synthesis` by passing findings through with their `sources` and `lenses` attribution intact
- [ ] author `synthesis.md`: split open questions and pre-existing issues first, dedupe on file plus line ±2 with similar descriptions, boost `min(99, max_conf + 10*(N-1))` over distinct sources, severity max, drop single-source below 80 without corroboration, and route would-be-drops to the verifier when the run is degraded
- [ ] write tests with a mocked runner asserting the prompt carries an accurate source roster
- [ ] write tests for the degraded path asserting no findings are dropped
- [ ] write tests for `--no-synthesis` passthrough and for malformed synthesis output
- [ ] run tests - must pass before task 10

### Task 10: Verification stage

**Files:**
- Create: `app/pipeline/verify.go`, `app/pipeline/verify_test.go`
- Modify: `app/prompt/defaults/prompts/verify.md`

**Design Contract:**

Type:
- `verifier` (unexported — owns the verify stage)
- `verifyGroup` (unexported — one directory's findings)

Methods (full signatures):
- `(v *verifier) run(ctx context.Context, rep finding.Report) (finding.Report, error)`
- `(v *verifier) groupByDir(findings []finding.Finding) []verifyGroup`
- `(v *verifier) runOne(ctx context.Context, g verifyGroup) ([]finding.Finding, error)`
- `(g verifyGroup) label() string`

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none — internal to `app/pipeline`

- [ ] create `app/pipeline/verify.go` grouping findings by directory, merging thin directories and capping group count from config
- [ ] dispatch one verifier per group in parallel through the same stagger, each seeing only its own group
- [ ] apply verdicts: confirm keeps, reject drops, refine rewrites, pre-existing and immaterial move to their own lists
- [ ] honor `--no-verify` by marking every finding unverified in the report
- [ ] author `verify.md` carrying the verdict set and the materiality test
- [ ] write tests for grouping: one directory, many directories, thin-directory merging, cap enforcement
- [ ] write tests asserting each verifier prompt contains only its own group's findings
- [ ] write tests for every verdict path and for `--no-verify`
- [ ] run tests - must pass before task 11

### Task 11: Run artifacts

**Files:**
- Create: `app/archive/archive.go`, `app/archive/archive_test.go`
- Modify: `app/pipeline/pipeline.go`, `app/pipeline/pipeline_test.go`, `app/config.go`, `app/main.go`

**Design Contract:**

Type:
- `Archive` (exported — constructed in `package main` over the task directory, injected into `app/pipeline`)
- `archiver` (unexported interface in `app/pipeline`, consumer-side, with a moq directive):
  `Writer(name string) (io.WriteCloser, error)`

Methods (full signatures):
- `(a *Archive) Writer(name string) (io.WriteCloser, error)`
- `(a *Archive) Prune() error`

`name` is a path relative to the run directory, so one method serves the per-agent tees, `events.jsonl`,
`prompts/<agent>.md`, `stages/<n>-<stage>.json`, the manifest and the rendered report without the archive
needing to know what any of them mean; it creates parent directories as needed.
Each returned writer has exactly one owner — an agent's writer belongs to that agent's goroutine, the event
log belongs to the channel subscriber, the report belongs to `package main` after the run — so no locking is
needed. `Archive.Dir()` is deliberately absent — nothing needs the path until a caller exists for it.

Standalone helpers planned (justification why NOT a method):
- `New(taskDir, run string, keep int) (*Archive, error)` — constructor; creates `<taskDir>/runs/<run>` and
  fails if it already exists
- `History(taskDir string) (string, error)` — reads prior rounds and renders the injected block. Called from
  `package main` before the pipeline exists, so it cannot be a method on the current run's `Archive`, and it
  reads sibling rounds rather than the one being written

Exports (justification per item: who outside the package calls this?):
- `Archive`, `New` — `package main` constructs it and injects it as the pipeline's `archiver`

Two consumers shape this package. A human debugging a bad review needs the raw streams and the event log.
A later self-reflection agent — out of scope here, but its data must be captured from the first run or the
corpus is worthless — needs to attribute a finding to the lens text that raised it and to see what each stage
changed. Everything below exists for one of those two.

- [ ] create `app/archive/archive.go` creating `<taskDir>/runs/<run>` and erroring if that directory already exists, so a bad round is never destroyed by a retry
- [ ] tee every agent's raw output verbatim, extension matching content: `<agent>.jsonl` for claude stream-json, `<agent>.log` for codex prose
- [ ] write `events.jsonl` from a third subscriber on the pipeline's event channel, recording revmux's own decisions — stalls, retries, degrades, stage transitions — which no per-agent stream contains
- [ ] write `prompts/<agent>.md` and `prompts/<stage>.md`: the composed prompt post-substitution, exactly the bytes handed to each process, since a reflection agent cannot judge a lens it cannot read
- [ ] write `stages/1-found.json`, `stages/2-synthesized.json`, `stages/3-verified.json` so what synthesis merged or dropped and what verify rejected is directly visible rather than reconstructed from raw streams
- [ ] write `manifest.json`: resolved roster (name, lenses, executor, requested model and effort), the model each agent *actually* ran per `modelUsage`, tokens per agent, per-file prompt provenance (which precedence layer won, plus a content hash), degraded sources, and per-stage timings
- [ ] have `package main` write `report.md` and `findings.json` into the run directory as well, so an archived run is self-contained
- [ ] implement `History(taskDir)` rendering the prior-round block: `runs/` path plus one line per round (name, timestamp, finding counts by severity, degraded sources) read from each round's `findings.json`, and the re-evaluate-independently instruction
- [ ] wire it in `package main`: resolve history once, pass it down on `pipeline.Config`, and have the pipeline hand it to every `Compose` call via `ComposeOpts`
- [ ] a prior round with a missing or unparseable `findings.json` is listed with its counts marked unknown, never dropped and never fatal — a round that failed badly is still evidence
- [ ] add the consumer-side `archiver` interface in `app/pipeline` with a moq directive
- [ ] implement `Prune` over `runs/` only, dropping oldest by mtime beyond `keep_runs`; it must never touch `scope.md`, `goal.md`, `profile.md` or `context/`
- [ ] add `tasks_dir` and `keep_runs` to the INI config with documented defaults (`./.revmux/tasks`, 10)
- [ ] write tests for run-directory creation, the already-exists error, concurrent writes from several agents, and nested-path writers
- [ ] write a test asserting the archived prompt is byte-identical to what the executor received, since a reflection agent drawing conclusions from a paraphrase is worse than one with no data
- [ ] write tests for `History`: no prior rounds, several rounds ordered oldest-first, and a round with an unreadable `findings.json` still listed
- [ ] write a test asserting `manifest.json` records the actual model when it differs from the requested one
- [ ] write tests for `Prune` ordering by mtime, and asserting it leaves caller-written context files untouched even when `keep_runs` is 0
- [ ] write a test with a failing `archiver` mock asserting the run completes with a warning
- [ ] run tests - must pass before task 12

### Task 12: TUI — status table, combined view and per-agent panes

**Files:**
- Create: `app/ui/model.go`, `app/ui/view.go`, `app/ui/handlers.go`, `app/ui/status.go`, `app/ui/agentpane.go`, `app/ui/combined.go`, `app/ui/doc.go`
- Create: matching `_test.go` for each
- Modify: `app/main.go`

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

Methods (full signatures):
- `(m Model) Init() tea.Cmd`
- `(m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)`
- `(m Model) View() string`
- `(m Model) statusTable() string`
- `(m Model) detailPane() string`
- `(m *Model) apply(ev pipeline.Event)`
- `(a *agentState) push(line string)`
- `(c *combinedState) push(e combinedEntry)`

`combinedState.push` takes a struct, not `(agent, line string)` — two adjacent strings called from
event-handling code where both are in scope is the classic transposition, and the struct gives the compact
log somewhere to carry timestamp and color without growing the signature.

No `app/ui/mocks/`: the package has no injected interfaces, because it does no OS work.

Standalone helpers planned (justification why NOT a method):
- `New(cfg ModelConfig) Model` — constructor

Exports (justification per item: who outside the package calls this?):
- `Model`, `ModelConfig`, `New` — `package main` constructs and runs the bubbletea program

- [ ] create `app/ui/model.go` with `Model`, `ModelConfig`, `New` and the state sub-structs
- [ ] create `app/ui/status.go` rendering one row per agent: name, state, elapsed, last activity
- [ ] create `app/ui/combined.go` with tab `0 · all`, chronological, agent-prefixed and colored, one compact line per tool call, state change and finding, deliberately excluding thinking text
- [ ] create `app/ui/agentpane.go` with tabs `1-9` showing full per-agent scrollback including thinking
- [ ] create `app/ui/handlers.go` for tab and number switching, scrolling, and quit
- [ ] wire `app/main.go` to run the TUI when the tty is openable and `--no-tui` is unset, rendering to the tty
- [ ] write tests driving `Update` with synthetic `pipeline.Event` values and asserting rendered output
- [ ] write tests for tab switching, scrollback bounds and the combined view's compact filtering
- [ ] write a test asserting the combined view is focused by default
- [ ] write a test asserting events for an unknown agent and events after an agent finished are tolerated
- [ ] run tests - must pass before task 13

### Task 13: TUI — findings browser

**Files:**
- Create: `app/ui/findings.go`, `app/ui/findings_test.go`
- Modify: `app/ui/model.go`, `app/ui/handlers.go`, `app/ui/view.go`, `app/main.go`

**Design Contract:**

Type:
- `findingsState` (unexported — cursor, expansion and filter state)

Methods (full signatures):
- `(m Model) findingsPane() string`
- `(m Model) Report() finding.Report`
- `(f *findingsState) move(delta int)`
- `(f *findingsState) toggle()`
- `(f *findingsState) filter(q string)`
- `(f *findingsState) visible() []finding.Finding`

All `findingsState` methods take pointer receivers — a value receiver on `visible` would copy cursor and
filter state on every render.

`Model.Report` is how the report leaves the TUI. `package main` calls it after the bubbletea program
returns and writes to stdout there. The TUI itself never writes stdout.

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- `Model.Report` — called from `app/main.go` after `Program.Run()` returns

- [ ] create `app/ui/findings.go` rendering findings grouped by severity with cursor, expansion and filter
- [ ] switch to the findings pane when the pipeline completes, keeping agent tabs reachable
- [ ] bind `j`/`k` to move, `enter` to expand body and fix, `/` to filter, `f` to jump to findings
- [ ] add `Model.Report` and have `app/main.go` write it to stdout after the program returns
- [ ] write tests for navigation, expansion, filtering and the empty-findings state
- [ ] write a test asserting `Report()` returns the final report and that main writes it exactly once
- [ ] run tests - must pass before task 14

### Task 14: Full default prompt set

**Files:**
- Create: `app/prompt/defaults/lenses/impl.md`, `architecture.md`, `quality.md`, `docs.md`, `tests.md`
- Create: `app/prompt/defaults/prompts/profiles/comprehensive.md`, `final.md`
- Modify: `app/prompt/defaults/lenses/bugs.md`, `adversarial.md`
- Modify: `app/prompt/prompt_test.go`

Content shared by every lens (output contract, severity language, project-profile calibration) belongs in
the profile bodies, not duplicated per lens.

- [ ] author the five remaining lenses: impl, architecture, quality, docs, tests
- [ ] author `comprehensive.md` with the default roster: bugs+impl, arch+quality, docs+tests, codex/adversarial
- [ ] author `final.md` as a narrow last pass: two agents, critical and major only
- [ ] move shared preamble text out of the lenses into the profile bodies, including the read-the-paths instruction for `{{SCOPE}}` / `{{GOAL}}` / `{{PROFILE}}` / `{{CONTEXT}}`
- [ ] write a test asserting every shipped profile parses, names only existing lenses and passes validation
- [ ] write a test asserting every shipped lens file is non-empty and contains no unresolved `{{VAR}}`
- [ ] write a test asserting every shipped profile body instructs the agent to read the path variables, so a lens set can never ship with context the agents silently ignore
- [ ] write a test asserting no shipped prompt file mentions prior rounds — that block is injected, and a profile duplicating it would drift from the injected text
- [ ] run tests - must pass before task 15

### Task 15: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented
- [ ] verify revmux runs no git command and imports no VCS library
- [ ] verify a full comprehensive run against a real repository produces a sane report
- [ ] verify revmux writes nothing outside the task directory's `runs/`, and that a second run of the same task allocates `runs/2` rather than overwriting
- [ ] verify a composed prompt contains only paths, no file contents, and that agents successfully read them when `--tasks-dir` points outside the repo
- [ ] verify the report and `manifest.json` show tokens per agent and a run total that matches their sum
- [ ] verify a killed agent is retried once, then degrades without aborting the run
- [ ] verify `--json` output is valid against the documented shape and `revmux --json > file` works with the TUI running
- [ ] verify exit codes 0, 1 and 2 in their respective conditions
- [ ] run full test suite: `make test`
- [ ] run `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0`
- [ ] verify test coverage meets project standard

### Task 16: [Final] Update documentation

- [ ] write README.md covering install, the three stages, the config tree, profiles and lenses, every flag, and exit codes
- [ ] update CLAUDE.md and `.claude/rules/*.md` where the build diverged from what they describe
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:

- run a full comprehensive review against a substantial change and judge the findings quality against a
  review produced by session-driven fan-out
- observe rate-limit behavior across several consecutive full runs
- confirm the TUI behaves at various terminal sizes and under `--no-tui`

**Follow-up work, out of scope here**:

- a bundled skill shipped with this repo so AI coding tools can launch revmux directly
- terminal-integration launcher for one-keystroke runs
- release workflow and distribution once the tool stabilizes

---

Smells pre-check: 27 items fixed before save — shared `proc` executor base to satisfy `dupl`, per-run
`Request` carrying model/effort so one executor serves several roster entries, single owner for executor
construction via a `newRunner` factory, `Pipeline` split into `finder`/`synthesizer`/`verifier`, `sink`
adapter instead of `Pipeline` implementing `executor.EventSink`, `Profile`/`Stage` split with composition
hung off `Profile`, `AgentSpec.Executor` field replacing a `Runner()` getter, filtering unified through
`Report.Above` so renderers and exit code agree, TUI gated on the tty rather than stdout, report returned
from the TUI instead of written inside `app/ui`, `app/run` renamed to `app/archive` with a consumer-side
`archiver` interface, `stagger.acquire`/`release` naming, struct-typed `combinedEntry` and `reviewContext`
to remove same-type swap hazards, pointer receivers on `findingsState`, and several unused or unjustified
exports removed.
