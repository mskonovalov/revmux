# revmux — project notes

revmux runs a structured multi-agent code review by spawning and supervising `claude --print` and
`codex exec` subprocesses, then returns findings.
It exists because agent fan-out driven from inside an AI coding session is unobservable and unrecoverable:
agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill, no retry and no progress.

A subprocess does not make the model faster.
What it buys is control — a watchdog that notices a stall, a kill and retry the caller owns,
a live view of every agent, per-agent token counts, and a run archive to debug a bad review afterwards.

**Status: the initial build is complete.**
The layout and rules below describe what is on disk; the build sequence that produced it is
`docs/plans/completed/20260726-revmux-initial-build.md`.
`README.md` is the user-facing description of the same thing — a change to a flag, a roster key, an exit
code or the JSON shape belongs in both.

## Working norms

**When writing or editing these notes — this file and `.claude/rules/*.md` — use semantic line breaks:
one sentence per line, never a giant single-line bullet.**
A 3000-character bullet is unreadable in a diff and impossible to review.

This file holds only what is specific to revmux.
If a note would be equally true of any Go project, it does not belong here.

## Build and test commands

- Build: `make build` (output: `.bin/revmux`)
- Test: `make test` (race detector + coverage, excludes mocks)
- Lint: `make lint` or `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0`
- Format: `make fmt`
- Generate mocks: `go generate ./...`
- Vendor after adding deps: `go mod vendor`

## Project structure

`app/` is the composition root (`package main`), split by concern.

- `app/main.go` — entrypoint + `run()`
- `app/config.go` — go-flags options, INI parsing, precedence, `--init` / `--dump-defaults`
- `app/defaults/config` — the embedded, fully commented-out INI template `--init` materializes;
  it lives here rather than under `app/prompt/defaults/` because it is settings, not prompt content
- `app/introspect.go` — the `revmux config` subcommand and the catalog it prints
- `app/artifacts.go` — the artifacts `package main` owns: `manifest.json`, `report.md`, `findings.json`
- `app/progress.go` — the non-TTY event subscriber (timestamped lines to stderr)
- `app/executor/` — supervised subprocess execution for claude and codex
- `app/prompt/` — front matter and roster parsing, lens composition, `{{VAR}}` substitution, `go:embed` defaults
- `app/pipeline/` — the three stages, fan-out, stagger, degrade policy, typed event channel
- `app/finding/` — `Finding` and `Report` types, the per-stage JSON schemas, markdown and JSON rendering
- `app/archive/` — per-run artifacts under the task directory's `runs/<run>/`
- `app/ui/` — bubbletea TUI, single `Model` with state grouped into sub-structs, files split by concern
- `app/*/mocks/` — moq-generated, never edited by hand

## Hard rules

**revmux runs a review and returns findings. Nothing else.**
It does NOT do scope detection, git operations, PR fetching, issue handling, or any source modification.
It has **zero VCS dependency** — no git library, no `git` subprocess, no repo walking.
All context (scope description, goal, project profile, prior rounds) is written to disk by the caller and passed in.
Agents run diff commands themselves; revmux only substitutes a path.
If a change would make revmux read a repo, the change belongs in the caller.
See `.claude/rules/pipeline.md`.

**Review context arrives as a task directory, and only as a task directory.**
`--task <id>` names a directory under `--tasks-dir` (default `./.revmux/tasks`) that the caller has already filled:

```
<tasks-root>/<id>/
├── scope.md      → {{SCOPE}}     required
├── goal.md       → {{GOAL}}      optional
├── profile.md    → {{PROFILE}}   optional
├── context/      → {{CONTEXT}}   optional, any number of files
└── runs/<run>/                   revmux-owned, see the archive rule below
```

Both names are caller-chosen and semantic: `--task pr-123 --run after-fix`.
revmux allocates neither — `--run` defaults to a UTC timestamp when omitted, and a name that already exists
is a load-time error rather than an overwrite, because a round that went badly is exactly what a reflection
agent needs to read.
A loop re-runs one task under successive run names and accumulates rounds.
There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags —
one mechanism, no precedence rules, nothing for revmux to author.
**revmux writes only under `runs/`.** Everything above it belongs to the caller and is never modified or pruned.
See `.claude/rules/config.md`.

**A run archive must be sufficient to audit the review that produced it, without re-running anything.**
Visibility is only half the job: these artifacts are also the input to a later self-reflection agent that
reads a task's history and proposes changes to the lens and profile text.
Answering "which lens text raised this finding" and "did synthesis drop something real" requires more than
the final report, so a run directory holds:

```
runs/<run>/
├── manifest.json     resolved roster, per-lens prompt provenance + content hash,
│                     requested vs. actual model per agent, timings
├── prompts/          the composed prompt each agent and stage actually received,
│   ├── agents/       post-substitution — exactly the bytes the model saw
│   └── stages/       split so a roster agent named `verify` cannot overwrite a stage prompt
├── stages/           findings after find, after synthesis, after verify
├── events.jsonl      revmux's own decisions: stalls, retries, degrades, stage changes
├── agents/           verbatim tees, own subdir for the same reason
│                     <agent>.jsonl claude stream-json, <agent>.log codex prose,
│                     <agent>.retry.jsonl the second attempt when one is retried
└── report.md, findings.json
```

Prompt text is resolved per file across three layers, so **which file won and what it contained** must be
recorded — two runs of one task can use different lens text, and a reflection agent comparing rounds needs
to see that.
Anything that makes a run un-auditable after the fact — dropping the composed prompt, keeping only the final
findings, reusing a run directory — defeats the purpose even when the review itself is fine.

**A failed archive write fails the run (exit `2`).**
A report emitted next to a half-written archive is worse than no report: it reads as complete, and the gap
only surfaces later when someone tries to audit it.
For the same reason the archive is written synchronously and is never a second subscriber on the event
channel — a Go channel distributes rather than broadcasts, so a second reader would silently take a random
half of the events. See `.claude/rules/pipeline.md`.

`--task` and `--run` are caller-supplied and become filesystem paths, so they are validated before use:
no separators, no `..`, not absolute, and containment re-checked on the resolved path because a symlink
defeats the lexical test. Roster agent names carry the same rule, applied at load in
`prompt.AgentSpec.checkName` — but not the paths `Archive.Writer` takes, which are relative and must allow
a separator because `agents/`, `stages/` and `prompts/` all need one.
Names revmux **derives** rather than reads — a verify group's label — are sanitized instead, since their
parts are directory names from the findings and there is nobody to return an error to.

**Context variables expand to paths, never to content.**
`{{SCOPE}}` is the absolute path of `scope.md`, not the text inside it; the agent reads the file itself.
revmux stats the caller's context files and never opens one, so no prompt can be bloated by a large scope
and the never-embed rule needs no per-variable judgment call.
It does read its own prior `findings.json` files to build the history inventory — that is revmux reading
what revmux wrote, and the inventory carries counts, never findings text.
See `.claude/rules/prompts.md`.

**Prior rounds are injected into every composed prompt, and never as a `{{VAR}}`.**
revmux wrote them, so it hands them over rather than making each caller copy them forward.
A variable would be opt-in per file — any lens or profile omitting it silently loses the history — so the
composer appends the block, the same way the codex executor appends its own output contract.
The block is the `runs/` path plus a generated one-line inventory per round, so an agent can tell what is
there without opening anything, and it is omitted entirely on a first round.
**The re-evaluate-independently instruction is part of that injected block, never left to the profile body.**
An agent told a prior round flagged something tends to confirm it rather than judge it — the same anchoring
failure that makes codex a peer rather than a second pass — so the data and its guard must not be separable.

**OS-level work must never live in `app/ui`.**
The pipeline runs fully headless and emits typed events on a channel; the TUI is one subscriber and `--no-tui` is another.
No `exec.Command`, no file reads, no writes to stdout, no network in `app/ui` — not even for a small helper.
This is what makes the orchestrator testable with a mocked `CommandRunner` and no terminal.
See `.claude/rules/tui.md`.

**Executor and lens are orthogonal.**
Every roster entry composes lenses; `executor` only selects which binary runs it (`claude` default, or `codex`).
There is no codex-specific prompt file — codex is an entry with `executor: codex` composing `lenses/adversarial.md`.
Lens text stays executor-agnostic; the output-contract difference (claude has `--json-schema`, codex does not)
is injected by the executor, never authored into a lens file.
A roster entry also carries an optional `color` — an ANSI-16 name or `#RRGGBB` — resolved in `app/prompt`
and handed to both renderers, so the TUI and `--no-tui` never color the same agent differently.
See `.claude/rules/prompts.md`.

**Codex is a peer source, not a second pass.**
It runs in parallel with the lens agents and its findings go through the same synthesis and verification.
Never introduce a separate codex-evaluation step or a rebuttal loop.
Primary/secondary ordering means the second reviewer sees the first's findings and anchors on them,
which destroys the independence the cross-source confidence boost depends on.
The fix-and-re-review loop lives in the caller, which re-runs revmux against the same `--task` under a new
`--run` name; revmux injects the prior rounds itself.

**Findings go to stdout, everything else to tty/stderr.**
The TUI renders to the tty, progress lines go to stderr, and only the report is written to stdout.
That is what makes `revmux --json > findings.json` work with the TUI running at the same time.
Never print a status message, warning or banner to stdout.
Gate the TUI on the tty being openable — never on stdout being a TTY, which is false whenever the report is redirected.
The single exception is `revmux config`, which prints the resolved configuration as JSON and exits before
any pipeline, archive or TUI exists — there is no report for it to collide with.

**revmux is driven by a caller model, so its configuration is machine-readable.**
`revmux config` reports what actually resolved — runtime knobs with the precedence layer that supplied
each, every profile with its full roster, every lens and stage with its `description:` one-liner, and the
`executor` and `effort` vocabularies read from the same constants that validate them.
A caller composing `--lenses` has no other way to learn what a lens covers, and a catalog describing the
embedded defaults while the user has overrides describes a review that will not happen.

**A source is a process.**
The cross-source confidence boost counts distinct processes, never tags and never lenses.
An agent carrying two lenses that flags the same issue under both is still one source — it cannot corroborate itself.
The pipeline knows which process emitted which finding, so the count is structurally correct. Keep it that way.

The wire format enforces the distinction with two fields that must never be conflated.
`Finding.sources` holds **agent names** (`["bugs+impl", "codex"]`) and is the only input to the boost.
`Finding.lenses` holds the lens names that raised it (`["bugs", "adversarial"]`) and is informational —
it answers "why was this reported", never "how many independently agreed".
A `sources` array holding lens names inflates confidence on exactly the single-agent case the rule exists to catch.

**Go assigns `sources`, never the model.**
`find` overwrites the field on every parsed finding with the executing agent's name and validates `lenses`
against that agent's configured lens set.
**No schema ever exposes `sources`** — a field the model can fill is a field it will fill, and one agent
naming itself twice is self-corroboration.
`FinderSchema` omits `verdict` for the same reason; `VerifySchema` is the one place a model returns one,
because asking for a verdict is what that stage is for.
Stamping happens in `find`, not synthesis, or `--no-synthesis` runs carry invented attribution into the report.

## Keep-in-sync conventions

- A new CLI flag needs: the `options` struct tag and the README flag table.
  An INI-backed one also needs a commented-out entry in `app/defaults/config`, the template `--init` writes —
  not `--dump-defaults`, which extracts the prompt tree and knows nothing about settings.
  It is reported by `revmux config` automatically: `knobs` is built by reflection over the `options` struct.
- A new roster key needs: the `agentYAML` field it parses into, the `AgentSpec` field it resolves to,
  front-matter validation, and the profile examples in README and `.claude/rules/prompts.md`.
- A new pipeline `EventKind` needs a case in `app/progress.go` **and** in the TUI's `apply`, or it is silently invisible in one renderer.
- A new lens file needs an entry in at least one shipped profile, or nothing will ever run it.
- A new prompt input — a variable, an injected block, a per-agent knob — needs a matching record in
  `manifest.json` or the archived prompt, or a reflection agent cannot tell what shaped the review.
- Changing any of the three stage schemas means changing the embedded JSON under `app/finding/`
  (`finder-schema.json`, `synthesis-schema.json`, `verify-schema.json` — `schema.go` only embeds them),
  the `Report.JSON` shape, the README output section, and every recorded executor fixture carrying a
  `structured_output`.
  `finder-schema.json` is the harder one: `app/executor/testdata/finder-schema.json` is the copy the live
  capture was recorded under and is authoritative, so both files move together or the executor tests assert
  a shape the CLI never emitted.

## Subsystem notes (path-scoped rules)

Detailed per-subsystem engineering notes live in `.claude/rules/*.md`, each scoped with `paths:` frontmatter.

- `.claude/rules/executor.md` — subprocess supervision, verified `claude` and `codex` CLI behavior, stream decoding
- `.claude/rules/pipeline.md` — the three-stage contract, degrade policy, stagger, event channel
- `.claude/rules/prompts.md` — front matter, roster resolution, lens composition, config precedence
- `.claude/rules/tui.md` — bubbletea conventions and the lipgloss/ANSI traps
- `.claude/rules/config.md` — go-flags plus INI, flag description style, context resolution
- `.claude/rules/testing.md` — fixtures, mocks, and why no test may spawn a real model
