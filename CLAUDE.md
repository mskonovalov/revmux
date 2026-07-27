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
- Install: `make install` (symlinks `.bin/revmux` into `$BINDIR`, default `/usr/local/bin`; `make uninstall` removes it)
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
- `app/newcmd.go` — the `revmux new` subcommand, which scaffolds a round and prints the paths it created
- `app/artifacts.go` — the artifacts `package main` owns: `manifest.json`, `report.md`, `findings.json`
- `app/progress.go` — the non-TTY event subscriber (timestamped lines to stderr)
- `app/executor/` — supervised subprocess execution for claude and codex
- `app/prompt/` — front matter and roster parsing, lens composition, `{{VAR}}` substitution, `go:embed` defaults
- `app/pipeline/` — the three stages, fan-out, stagger, degrade policy, typed event channel
- `app/finding/` — `Finding` and `Report` types, the per-stage JSON schemas, markdown and JSON rendering
- `app/task/` — the task-directory layout constants, `task.md` parsing, name validation, round scaffolding
- `app/frontmatter/` — the `---` block scanner `app/prompt` and `app/task` share, so its CRLF and
  empty-block cases have one implementation rather than two that drift
- `app/archive/` — one round's artifacts, written into `<task>/<round>/` beside the caller's `input/`
- `app/ui/` — bubbletea TUI, single `Model` with state grouped into sub-structs, files split by concern
- `app/*/mocks/` — moq-generated, never edited by hand

`.claude-plugin/` and `plugins/codex/` ship the **caller** as a skill, one tree per harness.
They contain no Go and are not built; they are documentation plus four shell scripts.
The two trees carry duplicate copies of `references/` and `scripts/` on purpose — a plugin has to be
self-contained once installed, so a shared directory is not available to them.
**A change to one must be made to the other**, and the only intended divergence is in `SKILL.md`:
script-path resolution and the harness's own way of asking a question.

## Hard rules

**revmux runs a review and returns findings. Nothing else.**
It does NOT do scope detection, git operations, PR fetching, issue handling, or any source modification.
It has **zero VCS dependency** — no git library, no `git` subprocess, no repo walking.
All context (scope description, goal, project profile, prior rounds) is written to disk by the caller and passed in.
Agents run diff commands themselves; revmux only substitutes a path.
If a change would make revmux read a repo, the change belongs in the caller.
See `.claude/rules/pipeline.md`.

**Review context arrives as a task round, and only as a task round.**
`--task <id>` names a directory under `--tasks-dir` (default `./.revmux/tasks`) and `--run <name>` names one
round inside it. The caller fills that round's `input/` before revmux is invoked:

```
<tasks-root>/<id>/
├── task.md                      optional, front matter describing the task itself
├── 01-initial/                  one round; a round is a direct child of the task
│   ├── input/                   CALLER-written, and the only channel review context travels through
│   │   ├── scope.md    → {{SCOPE}}    required
│   │   ├── goal.md     → {{GOAL}}     optional
│   │   ├── profile.md  → {{PROFILE}}  optional
│   │   └── context/    → {{CONTEXT}}  optional, any number of files
│   └── …                        revmux-owned artifacts, see the archive rule below
└── 02-after-fix/
```

**Review context belongs to the round, not to the task.**
Round 2 reviews the fixes for what round 1 found, so its scope, goal, profile and context are all different
from round 1's.
Holding them at task level makes composing round 2 overwrite the record of what round 1 actually reviewed,
and no archive can reconstruct that afterwards — an archived prompt carries the path, not the text.

Both names are caller-chosen and semantic: `--task pr-123 --run after-fix`.
revmux allocates neither, and **`--run` has no default**: the round is where the caller's own context lives,
so revmux cannot name one he has not filled.
A round that has already run is a load-time error rather than an overwrite, because a round that went badly
is exactly what a reflection agent needs to read.
A loop re-runs one task under successive run names and accumulates rounds.
There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags —
one mechanism, no precedence rules, nothing for revmux to author.

**revmux writes only inside a round, and deletes nothing at all.**
`revmux new --task <id> --run <name>` is the one thing that creates any of this, and it prints the absolute
paths the caller writes to, so no caller composes a path out of the diagram above.
Every other path opens and never creates: a typo'd `--task` is an error rather than an empty task nobody
filled.
There is no pruning and no `--keep-runs` — reclaiming space is `rm -rf <tasks-dir>/<task>/<round>`, and it is
the user's to run.

**`task.md` is stored and reported, never resolved.**
Its `description`, `url`, `branch` and `base` let a caller match an existing task instead of guessing at an id
— `pr123` beside `pr-123` silently forks the history — and `revmux config` reports them back.
revmux runs no git command and fetches nothing to check any of them.
That is the zero-VCS-dependency rule, and `task.md` is exactly where it would erode.
See `.claude/rules/config.md`.

**A run archive must be sufficient to audit the review that produced it, without re-running anything.**
Visibility is only half the job: these artifacts are also the input to a later self-reflection agent that
reads a task's history and proposes changes to the lens and profile text.
Answering "which lens text raised this finding" and "did synthesis drop something real" requires more than
the final report, so a round directory holds:

```
<tasks-root>/<id>/<run>/
├── input/            the caller's own scope, goal, profile and context for this round
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

`input/` is part of that record rather than a neighbour of it: the round the agents were pointed at is the
round on disk, so a reflection agent reading one round in isolation sees the scope it was reviewed against.

`manifest.json` doubles as the marker that claims the round.
`archive.New` creates it with `O_CREATE|O_EXCL`, which is atomically both the already-ran check and the mark
that tells a real round from a stray directory a caller left under the task.
It is created **empty** and filled in by the finished run, so the two states are distinguishable, and they
must stay that way: an empty marker is a claim its run never came back from — an interrupt, an unwritable
artifact, every source degraded.
Such a round is re-claimed rather than refused **only while it is otherwise empty**, since the caller's own
`input/` lives inside it and burning the round over a marker carrying nothing would cost him the context he
wrote.
The marker is written first and the record last, so a run that never came back may still have written
stages, prompts, tees and `events.jsonl` — and a second run over those produces one round holding two runs'
artifacts under a manifest describing only the second.
`task.CheckReclaim` refuses that round and names what it found; nothing is deleted to make it usable, and
the caller opens a new round and copies his `input/` across.
A round that ran is `task.HasRun`, and both the prior-round inventory and `revmux config` gate on that
rather than on the file merely existing, or the round being re-run appears in its own history.

Prompt text is resolved per file across three layers, so **which file won and what it contained** must be
recorded — two rounds of one task can use different lens text, and a reflection agent comparing rounds needs
to see that.
Anything that makes a round un-auditable after the fact — dropping the composed prompt, keeping only the
final findings, reusing a round directory — defeats the purpose even when the review itself is fine.

**A failed archive write fails the run (exit `2`), with exactly one carve-out.**
A report emitted next to a half-written archive is worse than no report: it reads as complete, and the gap
only surfaces later when someone tries to audit it.
For the same reason the archive is written synchronously and is never a second subscriber on the event
channel — a Go channel distributes rather than broadcasts, so a second reader would silently take a random
half of the events. See `.claude/rules/pipeline.md`.

That carve-out is about attribution, and it may not be widened.
A **per-agent tee** under `agents/` degrades that one source instead of failing the run: it is owned by that
agent's own goroutine and is the only artifact whose failure belongs to a single source, so it is reported
through the same banner and `degraded` list every other agent failure is, rather than throwing away the
other agents' completed work.
Everything else — the manifest, the composed prompts, the stage snapshots, `events.jsonl`, the report —
either lands or the run exits `2`.

`--task` and `--run` are caller-supplied and become filesystem paths, so they are validated before use:
no separators, no `..`, not absolute, and containment re-checked on the resolved path because a symlink
defeats the lexical test.
`task.CheckName` is the single definition of that rule — `package main`, `app/archive` and `revmux new` all
delegate to it rather than carrying a copy.
A round name additionally passes `task.CheckRoundName`, which refuses the entries the task directory keeps
beside its rounds: `task.md`, and the `scope.md` and `runs/` that identify the layout before rounds.
A round named after one of those makes every later review of the task — including of its legitimate rounds
— fail as an old-layout task.
Roster agent names carry the same rule, applied at load in `prompt.AgentSpec.checkName` — but not the paths
`Archive.Writer` takes, which are relative and must allow a separator because `agents/`, `stages/` and
`prompts/` all need one.
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
The block is the task directory path plus a generated one-line inventory per round, so an agent can tell what
is there without opening anything, and it is omitted entirely on a first round.
Rounds are the task directory's own children, and one is a directory whose `manifest.json` carries a run's
record of itself — `task.HasRun`. Anything else under the task, `task.md` included, is not a round, and
neither is a round claimed by a run that never came back and left that marker empty.
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

**Findings go to stdout as JSON, everything else to tty/stderr.**
revmux is driven by a caller model that parses what comes back, so the machine shape is the default and
`--markdown` opts into the rendered one. The two human-facing renderings are the TUI, on screen while
the run happens, and `report.md` in the archive, on disk afterwards — markdown on stdout served neither
and had to be parsed back out by everything that did read it.
The TUI renders to the tty, progress lines go to stderr, and only the report is written to stdout.
That is what makes `revmux > findings.json` work with the TUI running at the same time.
Never print a status message, warning or banner to stdout.
Gate the TUI on the tty being openable — never on stdout being a TTY, which is false whenever the report is redirected.
The only exceptions are the two subcommands, `revmux config` and `revmux new`, and `--version`.
All three print on stdout and exit before any pipeline, archive or TUI exists, so there is no report for
any of them to collide with.
Both write it from `runOpts` through the injected writer rather than from their own `Execute`, which go-flags
calls during parsing while stdout is still the real `os.Stdout` and nothing can capture it.

**revmux is driven by a caller model, so its configuration is machine-readable.**
`revmux config` reports what actually resolved — runtime knobs with the precedence layer that supplied
each, every profile with its full roster, every lens and stage with its `description:` one-liner, and the
`executor` and `effort` vocabularies read from the same constants that validate them.
A caller composing `--lenses` has no other way to learn what a lens covers, and a catalog describing the
embedded defaults while the user has overrides describes a review that will not happen.
`paths.tasks` carries the same idea down to the task store: each task's id, whatever its `task.md` says about
it, and the rounds already recorded under it.
That is what a caller matches an in-flight review against — an id alone leaves it minting `pr123` beside an
existing `pr-123`.

**The layout is revmux's own detail, so a caller is handed paths rather than a shape to reproduce.**
`revmux new --task <id> --run <name>` creates the round and prints every path the caller writes to, absolute,
plus which of them this call created.
A caller that constructs `<tasks-dir>/<task>/<run>/input/scope.md` itself has reimplemented the layout from a
document, and the next layout change breaks it silently.

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
- A new pipeline `EventKind` needs a case in `app/progress.go` **and** in the TUI — in `agentState.track`
  for an agent-scoped kind, or in `Model.apply` for one that is not, since `apply` dispatches everything
  else to `track` and both switches end in a `default` that renders nothing.
- A new lens file needs an entry in at least one shipped profile, or nothing will ever run it.
- A change to the task-directory layout starts at the constants in `app/task`, which `app/archive` and
  `package main` join every path from — no layout name is spelled anywhere else. What does not follow them
  is everything that *describes* the shape: `task.Paths` and its JSON field names, the two trees in README,
  the one in this file, and both skill trees.
- A new `task.md` front-matter key needs: the `Meta` field with both a `yaml` and a `json` tag, the
  commented-out line in `app/task`'s scaffolded template, and the README description of the file.
  `revmux config` reports it for free, since `taskInfo` embeds `Meta` rather than copying its fields.
  Four places enumerate the keys literally and do not: the README and `.claude/rules/prompts.md`
  descriptions of the file, the `SKILL.md` step that writes it, and the hardcoded
  `for key in description url branch base` loop in `scripts/task-state.sh` — in **both** skill trees.
- Anything the shipped skill documents — a flag, a profile, the JSON shape, an exit code, the task
  directory layout — needs the same edit in **both** skill trees, since they hold duplicate copies of
  `references/` and `scripts/`. A `diff -r` of the two `references/` and `scripts/` directories must
  come back empty; only `SKILL.md` differs.
- **The skill is documentation of this binary, so a change to the binary updates it in the same commit.**
  It states revmux's flags, profiles, JSON field names, exit codes and archive layout as fact, and an
  agent executes what it says without checking. A skill describing a flag that no longer behaves that way
  is worse than one that omits it: the caller acts on it confidently and has to recover afterwards.
  Treat `.claude-plugin/skills/` and `plugins/codex/` as consumers of `app/config.go`, `app/finding/`
  and `app/archive/` the way `README.md` is.
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
