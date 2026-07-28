# revmux

revmux runs a structured multi-agent code review. It spawns and supervises `claude --print` and `codex exec`
subprocesses, then returns findings on stdout as markdown or JSON.

revmux runs a review and returns findings, and does nothing else. It performs no scope detection, no git
operations, no PR fetching and no source modification. All review context is written to disk by the caller
and passed in as a task round, named with `--task` and `--run`.

## Why

**Control over the fan-out.** Agent fan-out driven from inside an AI coding session is unobservable and
unrecoverable: agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill,
no retry and no progress display. A subprocess does not make the model faster. What it buys is control: a
watchdog that notices a stall, a kill and retry the caller owns, a live view of every agent, per-agent token
counts, and a run archive to debug a bad review afterwards.

**One review standard rather than one per session.** A review assembled ad hoc varies with the prompt, the
context left in the session, and whatever the model decided to look at that time. Here the roster, the
lenses, the severity bar and the three stages are files, so two runs of a profile ask the same questions of
the code. That matters most between people: a contributor gets the review a maintainer would have run, and
the maintainer can check which lens text produced it instead of taking it on faith.

**Review rules ship with the repository.** What a project actually cares about — its conventions, what counts
as major, the mistakes it keeps repeating — usually lives in a maintainer's head and reaches contributors one
review comment at a time. Checked into `.revmux/`, it is lens and profile text: versioned, diffable, reviewed
like the rest of the code, and applied by everyone who clones the repo. A review that missed something is
fixed by editing a file, once.

**Auditable by agents, not only by people.** The run archive keeps the composed prompt each agent received,
the verbatim output each one returned, the findings after every stage, and revmux's own decisions about
stalls and retries. A person reads the report; an agent can read the entire run. That is also what makes the
review itself improvable: a later agent reads a task's history and answers what the report cannot — which
lens text raised a finding, whether synthesis dropped something real, whether a lens is earning its tokens —
then proposes edits to the lens and profile files.

**Rounds stack.** Reviewing a branch or a PR is never one review; it is a review, some fixes, and another
review. revmux keeps the rounds under one task and hands the earlier ones to every agent in the next, so a
round can tell what has already been reported, read it in full when that matters, and spend its attention on
what changed. The injected block carries its own instruction to judge independently rather than confirm,
since an agent told that a prior round flagged something tends to agree with it.

## Install

```
go install github.com/umputun/revmux/app@latest
```

The binary is installed as `app`; rename it to `revmux`, or build from a clone instead:

```
git clone https://github.com/umputun/revmux.git && cd revmux
make build        # produces .bin/revmux
make install      # and symlinks it to /usr/local/bin/revmux
```

`make install` links rather than copies, so a later `make build` is picked up without reinstalling.
Override the location with `BINDIR` when `/usr/local/bin` is not writable — `make install BINDIR=~/bin`.
`make uninstall` removes the link.

revmux drives the model CLIs as subprocesses, so both must already be installed and authenticated:

- `claude` — every lens agent and both model stages run on it by default
- `codex` — only needed when a roster entry or a stage declares `executor: codex`, which the shipped
  profiles all do

`ANTHROPIC_API_KEY` is stripped from the child environment by default so `claude` uses interactive
subscription auth; pass `--preserve-anthropic-api-key` if you authenticate by key. `CLAUDECODE` is always
stripped, since a `claude` child refuses to start when it thinks it is a nested session.

## Quick start

`revmux new` creates the round and prints every path you write to, so nothing constructs a path by hand:

```console
$ revmux new --task pr-123 --run 01-initial
{
  "task_dir": "/abs/.revmux/tasks/pr-123",
  "task_file": "/abs/.revmux/tasks/pr-123/task.md",
  "round_dir": "/abs/.revmux/tasks/pr-123/01-initial",
  "input_dir": "/abs/.revmux/tasks/pr-123/01-initial/input",
  "scope": "/abs/.revmux/tasks/pr-123/01-initial/input/scope.md",
  "goal": "/abs/.revmux/tasks/pr-123/01-initial/input/goal.md",
  "profile": "/abs/.revmux/tasks/pr-123/01-initial/input/profile.md",
  "context": "/abs/.revmux/tasks/pr-123/01-initial/input/context",
  "created": ["task_dir", "task_file", "round_dir", "input_dir"]
}
```

Take the `scope` path out of that payload rather than joining it yourself, write the scope into it, and run
the review — the same call again, since a round already scaffolded is reported rather than recreated:

```
scope=$(revmux new --task pr-123 --run 01-initial | jq -r .scope)
cat > "$scope" <<'EOF'
Review the changes on this branch against master.
Diff command: git diff master...HEAD
EOF

revmux --task pr-123 --run 01-initial
```

That runs the `comprehensive` profile, shows a live TUI, and writes the report to stdout as JSON. After
fixing something, open a new round on the same task and revmux carries the earlier rounds into every prompt:

```
revmux new --task pr-123 --run 02-after-fix     # then write its own input/scope.md
revmux --task pr-123 --run 02-after-fix > findings.json
```

## How it works

Three fixed stages. Only the roster and the severity bar vary between review shapes, so everything else is
configuration.

1. **find** — the profile's roster runs in parallel: several `claude` agents, each composing one or more
   lenses, plus a `codex` peer. Launch is staggered, agent 1 first and the rest released once it produces
   its first output. Each agent returns structured findings.
2. **synthesize** — one model call. It merges every source's findings, dedupes on `(file, line ±2)`, boosts
   confidence where distinct sources corroborate, splits out open questions and pre-existing issues, and
   drops weak singletons. It is told the true source roster as data, including which agents degraded.
3. **verify** — parallel agents grouped by directory, thin directories merged and the group count capped.
   Each verifier sees only its own group, so it cannot anchor on a neighbouring finding. Every finding comes
   back with a verdict: confirmed, refined, rejected, immaterial or pre-existing.

`--no-synthesis` passes findings through with their attribution intact. `--no-verify` marks every finding
`unverified` rather than silently claiming it was checked.

**Codex is a peer source, not a second pass.** It runs alongside the lens agents and its findings go through
the same synthesis and verification. Ordering the two would mean the second reviewer sees the first's
findings and anchors on them, which is exactly what the cross-source confidence boost assumes did not happen.

**A source is a process.** The confidence boost counts distinct processes, never lenses. An agent carrying
two lenses that flags the same issue under both is still one source — it cannot corroborate itself. Go stamps
the attribution after parsing; no schema exposes it to the model.

**Degrade, never abort.** A stalled or crashed agent is killed, retried once, and on a second failure marked
degraded while the run continues. The report banner names the missing agent, and synthesis is told the real
source count. A run where *every* source degraded is a tool error, not a clean empty report.

## Task directory

Review context reaches revmux only as a task round the caller has filled. `--task` names a task under
`--tasks-dir` (default `./.revmux/tasks`), and `--run` names one round inside it. Both names are
caller-chosen and semantic; revmux allocates neither.

```
<tasks-dir>/pr-123/               a task: one subject, reviewed over as many rounds as it takes
├── task.md                       optional; front matter identifying the task, see below
├── 01-initial/                   a round
│   ├── input/                    caller-written; the only channel review context travels through
│   │   ├── scope.md              → {{SCOPE}}    required; missing or empty is a load-time error
│   │   ├── goal.md               → {{GOAL}}     optional
│   │   ├── profile.md            → {{PROFILE}}  optional, the project's own conventions
│   │   └── context/              → {{CONTEXT}}  optional directory: ticket text, design notes, spec excerpts
│   └── …                         revmux-written artifacts, see Run archive below
└── 02-after-fix/                 the next round, with its own input/
```

**Context belongs to the round, not to the task.** Round 2 reviews the fixes for what round 1 found: a
different scope, usually a different goal. Kept at task level they would be overwritten by whoever composes
the next round, taking the record of what the previous round reviewed with them.

Variables expand to the **paths** of these files, never to their contents — the agent reads them itself.
revmux stats them and never opens one, so no prompt can be bloated by a large scope. An absent optional file
expands to `none provided`, which is not an error: the run proceeds with generic severity calibration.

There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags. One mechanism, no
precedence rules, and nothing for revmux to author.

`--run` has no default: the round holds your own context, so revmux cannot name one you have not filled. A
round that has already run is an error rather than an overwrite — a round that went badly is exactly the one
worth keeping.

Neither name may contain a path separator or `..`, be absolute, or begin with a dot. A round additionally
may not be called `task.md`: that is the one entry the task directory keeps beside its rounds, and a round
named after it would be read as the task's own metadata rather than as a round.

### `revmux new`

`revmux new --task <id> --run <name>` creates the task directory, a commented-out `task.md`, the round and
its `input/`, then prints every path you write to as JSON along with a `created` list naming which of them
this call made. It creates the tasks root itself too, so a first run on a clean checkout materializes
`./.revmux/tasks/` as well — the roots are not in `created`, which names only the four layout paths in the
payload. Everything else in revmux opens and never creates, so a typo'd `--task` on a review is an error
rather than an empty task nobody filled.

It never overwrites: an existing `task.md` is left alone, and a round that has already run is refused. Run it
again on the same task with a new `--run` and it creates only the round. A round whose review was
interrupted before it finished is not one that has run — it is scaffolded and reviewed again under the same
name, with the `input/` you wrote still in it, provided that review had not already written artifacts into
the round. If it had, `new` refuses the name and says what is in there, so it never hands back a round the
review itself would reject.

Callers should take the paths from its output rather than joining them from the tree above — the layout is
revmux's own detail, and a caller that reimplements it breaks silently when it changes.

### `task.md`

Optional, at task level, and about the task rather than about any one round:

```yaml
---
description: OAuth token exchange rework
url: https://github.com/umputun/revmux/pull/123
branch: feature/oauth
base: 4ed3259
---

Reviewing the token exchange path after the provider swap.
```

Every key is optional, as is the body and the file itself. `revmux config` reports the front matter under
`paths.tasks`, which is how a caller matches an existing task instead of guessing at an id — `pr123` opened
beside an existing `pr-123` silently forks the history into two.

**revmux stores and reports these; it never resolves one.** No git command runs against `branch` or `base`,
and nothing is fetched from `url`. They are strings you wrote and strings you read back.

### Prior rounds

**Prior rounds are injected into every composed prompt.** revmux wrote them, so it hands them over rather
than making the caller copy them forward. The injected block is the task directory path plus a generated
one-line inventory per round — name, when it ran, finding counts by severity, which sources degraded — so an
agent can judge relevance without opening anything. It carries its own re-evaluate-independently instruction,
and on a first round it is omitted entirely.

### Cleaning up

revmux deletes nothing, ever. To reclaim space, remove rounds yourself:

```
rm -rf .revmux/tasks/pr-123/01-initial
```

That is safe at any time. Nothing links rounds together: the prior-round inventory is rebuilt from whichever
round directories are present, so removing one loses that round's own record and affects nothing else.

## Run archive

Every run writes its artifacts into its own round directory, beside the `input/` it was pointed at. They
exist so a review can be audited without re-running it, which the final report alone cannot support — and
because the round holds its own context, one round read in isolation shows both what was reviewed and what
came back.

```
<tasks-dir>/pr-123/02-after-fix/
├── input/                    the scope, goal, profile and context this round was reviewed against
├── manifest.json             roster, prompt provenance + content hashes, requested vs actual model, timings
├── prompts/
│   ├── agents/               composed prompt per agent, post-substitution — the bytes the model saw
│   │   ├── bugs+impl.md
│   │   └── codex.md
│   └── stages/               separate from agents/ so an agent named `verify` cannot collide
│       ├── synthesis.md
│       ├── verify-app-executor.md      one per directory group
│       └── verify-app-pipeline.md
├── stages/                   a skipped stage writes no snapshot, so --no-verify leaves 3- absent
│   ├── 1-found.json          findings as the find stage left them
│   ├── 2-synthesized.json
│   └── 3-verified.json
├── events.jsonl              revmux's own decisions: stalls, retries, degrades, stage transitions
├── agents/                   verbatim tees; own subdir so an agent named `events` cannot collide
│   ├── bugs+impl.jsonl       claude stream-json
│   ├── bugs+impl.retry.jsonl a retried agent keeps both attempts
│   └── codex.log             codex prose
├── report.md                 the filtered report, byte for byte what the caller was shown
└── findings.json
```

`manifest.json` records which of the three precedence layers supplied each prompt file and its content hash,
because two rounds of one task can use different lens text. It also records requested-vs-actual model per
agent: `claude --model` can be silently ignored, so a roster's model pin is a claim until it is read back.
It doubles as the marker claiming the round — it is created exclusively as the run starts, which is both how
a round that has already run is detected and how a real round is told from a directory left under the task.
It is created empty and filled in when the run finishes, so a marker still empty means the run never came
back, and such a round is not counted as a prior round in the meantime.

**A round like that is re-runnable under the same name only while nothing else was written into it**, which
is narrower than it sounds: the pipeline opens `events.jsonl` before it launches an agent, so a review
interrupted at any point after it started has written something and revmux refuses the name. A round
holding `events.jsonl`, stage snapshots, agent tees or composed prompts is refused, because a second run
would leave one round holding two runs' artifacts under a manifest describing only the second. What is
re-runnable is a round claimed by a run that died before the pipeline began. The error names what it
found; nothing is deleted to make the round usable. Open the next round and copy the `input/` across.

**A round already being written by another revmux is refused too.** An empty marker is what an interrupted
run leaves *and* what a run starting right now leaves, so the size alone cannot tell them apart. revmux
holds an exclusive OS-level lock on the marker for the run's lifetime: a second invocation on the same round
is turned away rather than sharing it, and the lock is gone the moment the holding process is, so a round
nobody is writing is still re-runnable with nothing to clean up.

A failed archive write fails the run. A report emitted next to a half-written archive reads as complete, and
the gap only surfaces later when someone tries to audit it.

The one exception is a per-agent tee under `agents/`, which degrades that one source instead. It is owned by
that agent's own goroutine and it is the only artifact whose failure is attributable to a single source, so a
failure there is reported the way any other agent failure is — named in the report banner and in `degraded`
— rather than discarding the other agents' work.

Rounds accumulate and are never pruned; see [Cleaning up](#cleaning-up).

## Configuration

Two precedence chains, not one.

**Runtime knobs** — command line, then `./.revmux/config`, then `~/.config/revmux/config`, then the built-in
default. Layers merge per key, so a project config setting one knob leaves the rest alone. The project layer
is auto-detected: no flag selects it, and its absence simply drops it.

**Prompt and lens files** — `./.revmux/`, then `~/.config/revmux/`, then the `go:embed` defaults, resolved
**per file**. Overriding one lens does not orphan the other six, and deleting an override falls back to the
embedded copy rather than disabling the lens. To actually drop a lens, remove it from the profile roster.

The project layer wins over both of the others, and it supplies prompt text as well as knobs — a checked-in
`.revmux/lenses/bugs.md` replaces the shipped lens, and that text becomes the instructions a headless agent
with a shell executes. So `.revmux/` is code, and running revmux inside a repository trusts it the same way
`.claude/` or a `Makefile` there does. Review it before reviewing a branch you did not write, or run revmux
from outside the tree — the project layer is read from the process working directory, never from `--workdir`,
so an invocation that stays outside never picks up the reviewed repository's own `.revmux/`.

```
~/.config/revmux/
├── config                    INI, runtime knobs only
├── prompts/
│   ├── profiles/
│   │   ├── comprehensive.md  roster front matter + shared preamble + severity bar
│   │   ├── focused.md
│   │   └── final.md
│   ├── synthesis.md
│   └── verify.md
└── lenses/
    ├── bugs.md  impl.md  architecture.md
    └── quality.md  docs.md  tests.md  adversarial.md
```

`--config-dir` relocates the user layer. `--init` — the flag spelling of `revmux init`, same implementation —
materializes `./.revmux/`: the commented-out config template plus whatever each prompt file actually resolved
to, with the paths printed as JSON on stdout. `--dump-defaults <dir>` extracts the **embedded** prompt tree
instead, which is how a customized file is diffed against the shipped one. Neither overwrites a file you have
customized, and a normal run writes no config at all.

Paths resolve against the **process working directory** — the project config layer, and `--tasks-dir`'s
`./.revmux/tasks` default. `--workdir` is separate: it sets where the subprocesses run and what `{{WORKDIR}}`
expands to. Reviewing a repo from outside it means passing `--config-dir` and `--tasks-dir` as well.

### Profiles

A profile is roster front matter plus a body that is the shared preamble and severity bar. Top-level `model`
and `effort` are defaults; per-entry values override them.

```yaml
---
description: all six lenses across three claude agents plus an adversarial codex peer
model: opus
effort: high
agents:
  - {name: bugs+impl,    lenses: [bugs, impl],            color: cyan}
  - {name: arch+quality, lenses: [architecture, quality], color: magenta}
  - {name: docs+tests,   lenses: [docs, tests],           color: green}
  - {name: codex, executor: codex, lenses: [adversarial],
     model: gpt-5.6-sol, effort: high, color: yellow}
---
```

| key | where | accepted values |
|---|---|---|
| `description` | profile, stage, lens | a one-liner, reported by `revmux config` |
| `model` | profile, roster entry, stage | whatever the selected binary accepts |
| `effort` | profile, roster entry, stage | `low`, `medium`, `high`, `xhigh`, `max` |
| `executor` | roster entry, stage | `claude` (default), `codex` |
| `lenses` | roster entry | names of lens files, at least one |
| `color` | roster entry | an ANSI-16 name (`red`, `bright-blue`, …) or `#RRGGBB` |

Everything is validated at load. An unknown lens, executor, effort or color is a startup error, never a
silent default — a typo'd model quietly changing which model reviews your code is worse than a failed launch.

`color` sets the agent's prefix color in both the TUI and the plain renderer, filled from a palette by roster
position when omitted. It is the one presentation key in an otherwise review-shaping file.

Shipped profiles:

| profile | roster |
|---|---|
| `comprehensive` | `bugs+impl`, `arch+quality`, `docs+tests` on claude, plus an adversarial codex peer |
| `focused` | one `bugs` agent plus the codex peer, for a small or time-boxed change |
| `final` | `bugs+impl` plus the codex peer, nothing below major reported |

### Lenses

Executor and lens are orthogonal. Every roster entry composes lenses; `executor` only selects which binary
runs it. There is no codex-specific prompt file — codex is an entry with `executor: codex` composing
`lenses/adversarial.md`, so the adversarial lens runs on claude by changing one word, and `bugs` runs on
codex the same way. Lens text stays executor-agnostic: the output-contract difference (claude has
`--json-schema`, codex does not) is injected by the executor.

| lens | covers |
|---|---|
| `bugs` | correctness defects — logic and boundaries, nil and bounds, concurrency, resource lifetime, error handling |
| `impl` | goal fit — whether the change does what it set out to do, is wired up, and is proportionate |
| `architecture` | conventions and organization — the project's own rules, established patterns, dependency and interface shape |
| `quality` | style, over-engineering, error handling and accidental duplication in code that already works |
| `docs` | documentation accuracy — doc comments against the code, and the project docs the change leaves stale |
| `tests` | whether tests exist where a defect can hide, actually exercise the code, and survive concurrency |
| `adversarial` | attacks the change looking for what a sympathetic reader would accept |

`--lenses bugs,impl` replaces a profile's roster while keeping its body. It produces **one** agent carrying
every named lens, not one agent per lens: a caller asking for two lenses is asking for a viewpoint, not for
two corroborating votes. The synthesized entry inherits the profile's top-level `model` and `effort` and runs
on claude, so a roster's codex entry does not survive the override.

### Composition

One agent's prompt is the profile body plus each of its lens files, concatenated, with `{{VAR}}` substituted
and the prior-rounds block appended. The variable vocabulary is closed — `{{SCOPE}}`, `{{GOAL}}`,
`{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}`, plus `{{FINDINGS}}` for both model stages and `{{SOURCES}}` for
synthesis only — verify sees one group at a time and is never given the roster.
A prompt file naming anything else fails at load, which is what makes a typo loud instead of silent.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--task=<id>` | | name of the task directory holding the review context |
| `--run=<name>` | | name for this round of the review |
| `--lenses=<a,b>` | | lens set replacing the profile roster |
| `--workdir=<dir>` | working directory | directory the review subprocesses run in |
| `--min-confidence=<n>` | `0` | drop findings below this confidence |
| `--no-synthesis` | | skip the synthesis stage |
| `--no-verify` | | skip the verification stage |
| `--no-tui` | | disable the terminal UI |
| `--markdown` | | write the report as markdown instead of JSON |
| `--preserve-anthropic-api-key` | | pass `ANTHROPIC_API_KEY` to the model CLIs |
| `--config-dir=<dir>` | `~/.config/revmux` | directory holding the config file and the prompt tree |
| `--init` | | materialize the resolved config and prompt tree into `./.revmux/` |
| `--dump-defaults=<dir>` | | extract the embedded prompt tree into a directory |
| `--version` | | show version and exit |

The runtime knobs below also read from the config file, under the same name as the flag:

| Flag | Config key | Default | Description |
|---|---|---|---|
| `--idle-timeout=<d>` | `idle-timeout` | `2m` | kill and retry an agent after this long with no output |
| `--hard-timeout=<d>` | `hard-timeout` | `20m` | kill an agent after this long, per attempt |
| `--stagger-delay=<d>` | `stagger-delay` | `30s` | how long to wait for the first agent before releasing the rest |
| `--max-parallel=<n>` | `max-parallel` | `4` | how many agents run at once |
| `--verify-groups=<n>` | `verify-groups` | `6` | cap on the number of verifier groups |
| `--tasks-dir=<dir>` | `tasks-dir` | `./.revmux/tasks` | root directory holding task directories |
| `--auto-exit=<d>` | `auto-exit` | `0s` | close the terminal UI this long after the report arrives; `0` leaves it open until a key is pressed |
| `--profile=<name>` | `profile` | `comprehensive` | profile naming the roster to run |

`--task` and `--run` are both required for a review, and neither is a config key: a config file naming the
round to write would make the same command review different context in different directories.

Two subcommands, both of which print JSON and exit before any review starts: `revmux config` reports the
resolved configuration, `revmux new` creates a round and reports its paths.

## Output

The report goes to **stdout** as JSON, or as markdown with `--markdown`. The TUI renders to the
tty and progress lines go to stderr, so `revmux --task pr-123 > findings.json` works with the display
running. The TUI is gated on the tty being openable, never on stdout being a terminal, which is false in
exactly that invocation.

```json
{
  "scope": {"task": "pr-123", "run": "02-after-fix",
            "scope_path": "/abs/.revmux/tasks/pr-123/02-after-fix/input/scope.md"},
  "sources": {
    "expected": 4, "reported": 3, "degraded": ["docs+tests"],
    "agents": [
      {"name": "bugs+impl", "lenses": ["bugs", "impl"], "executor": "claude",
       "requested_model": "opus", "actual_model": "claude-opus-5",
       "effort": "high", "tokens": 48210, "degraded": false}
    ]
  },
  "findings": [
    {"id": "f1", "file": "app/pipeline/find.go", "line": 88, "end_line": 0,
     "severity": "major", "confidence": 90,
     "title": "…", "body": "…", "fix": "…",
     "sources": ["bugs+impl", "codex"], "lenses": ["bugs", "adversarial"],
     "verdict": "confirmed"}
  ],
  "open_questions": [], "pre_existing": [], "immaterial": [],
  "stats": {
    "started_at": "2026-07-26T16:02:11Z", "finished_at": "2026-07-26T16:07:44Z",
    "duration_ms": 333000, "tokens": 184920,
    "stages": [{"name": "find", "duration_ms": 201000}, {"name": "synthesis", "duration_ms": 62000}]
  }
}
```

`line` is the anchor and `end_line` is optional: zero means a single line, and a zero `line` means a
file-level finding that renders as the bare path.

`sources` holds **agent names** and is the only input to the confidence boost. `lenses` holds the lens names
that raised the finding and is informational — it answers "why was this reported", never "how many
independently agreed". The two are never interchangeable.

`verdict` is one of `confirmed`, `refined`, `rejected`, `immaterial`, `pre_existing`, or `unverified` when
the verify stage was skipped. Empty lists are emitted as arrays rather than `null`, so a caller can index
into them without a nil check.

`--min-confidence` filters once, before anything renders, and the printed report, the findings browser and
the exit code are all computed from the filtered set — a finding the exit code says is absent is never
listed in the TUI. Open questions, pre-existing and immaterial findings pass through untouched.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | no findings above `--min-confidence` |
| `1` | findings above `--min-confidence` |
| `2` | tool error: bad config, unreadable prompt tree, an omitted `--run`, a round with no `input/` or an empty `scope.md`, a round that has already run or is being written by another run, an unwritable run artifact, or every source degraded |

`1` is a normal outcome, not a failure. Callers script against these values.

A per-agent tee is the one artifact whose failure degrades a source rather than failing the run; every other
artifact either lands or the run exits `2`.

A run that exits `2` usually leaves no report. The exception is a failure writing the report to stdout:
that happens after the round is archived, so the round is complete and its `findings.json` is on disk —
check `manifest.json` for content before re-running.

A configuration error is caught before the round is claimed at all, so it leaves no `manifest.json` and the
name is free. Anything that fails once the pipeline has started leaves an empty marker beside what it had
written, and both `revmux new` and the run itself refuse the name and say what the round holds. Either way
the `input/` you wrote is untouched and `revmux new` reports it back unchanged.

A delivered signal — `SIGINT` or `SIGTERM` — cancels the run and exits `2`. Agent processes are started in
their own session, so the terminal never signals them: revmux tears each process group down itself on the
way out rather than leaving the model CLIs and everything they spawned running unsupervised.

`Ctrl-C` delivers that signal under `--no-tui`. While the TUI is running it does not: the terminal is in raw
mode, so the keystroke reaches revmux as a key rather than a signal and quits the findings view without
stopping the run — see below. A second `Ctrl-C`, once the TUI has restored the terminal, cancels.

## Terminal UI

A status table on top, one row per supervised process — name, state, elapsed, last activity — and one detail
pane below it. The roster fills it first, and the synthesis and verify processes take rows of their own as
they start, so the table shows what is running rather than only what the profile named. The findings count
in the header follows the same logic — the finders add to it and a later stage's merged count replaces it —
and it is rebuilt from the finished report at the end, since verify rejects findings and `--min-confidence`
filters without either emitting an event. It is shown broken down by severity when the width allows.

Tab `1 all` is the combined chronological view and is focused by default; the tabs after it are per-agent
full-detail scrollback. On completion the model switches to the findings browser, and the
agent tabs stay reachable so a reader can check why a finding was raised.

| keys | action |
|---|---|
| `tab` / `shift+tab`, `←` `→`, `h` `l` | switch pane |
| `1`-`9`, then a letter | focus that pane directly; the token is shown on the tab, and letters already bound to something else are skipped |
| `f` | jump to the findings browser |
| `↑` `↓`, `k` `j` | scroll, or move the cursor in the browser |
| `pgup` `pgdn`, `ctrl+b` `ctrl+f` | page |
| `home` `end`, `g` `G` | top, bottom |
| `enter` | fold a finding down to its summary, or open it back up |
| `/` | filter findings; `enter` accepts, `esc` clears |
| `q`, `esc`, `ctrl+c` | quit |

Quitting stops watching the run, it does not stop the run: the report is still written to stdout when the
pipeline finishes.

With `--no-tui`, or when the tty cannot be opened, the same events render as timestamped lines on stderr,
each agent in its own color and every line starting at one column:

```
16:02:11 bugs+impl     started [bugs, impl]
16:02:19 arch+quality  reading the roster resolution path
16:04:02 docs+tests    retrying: agent docs+tests stalled
16:05:12 bugs+impl     done, 6 findings
16:05:40               ── synthesis ──
16:09:03               ── complete ──
16:09:03               6m52s, sources 4/4, degraded none
16:09:03               6 findings: 1 major, 5 minor
```

The pipeline emits no completion event — it ends by closing the event channel — so the closing three lines
are written after the last one, to say what the run came to. They carry counts only: the findings
themselves go to stdout, and a degraded run names its missing sources here rather than leaving the log
looking like a complete one.

## `revmux config`

revmux is normally driven by a caller model, so the resolved configuration is machine-readable rather than
something to reconstruct from `--help` and a directory listing. `revmux config` prints it as JSON on stdout
and exits `0` — it runs no pipeline and creates nothing; the only thing it touches is a read of the tasks
root, to list what is already there.

It reports what **resolved**, never what is embedded: a user who overrode one lens and added another sees his
own tree. Each runtime knob carries the precedence layer that supplied it, so a caller can tell a deliberate
choice from a default. The `executor` and `effort` vocabularies are read from the same constants that
validate front matter, so a new effort level cannot ship working but undiscoverable.

Flags may precede the subcommand, which is how a caller asks what a given invocation *would* resolve to
rather than what a bare one does:

```console
$ revmux --stagger-delay=45s config
{
  "knobs": [
    {"name": "idle-timeout", "value": "2m0s", "source": "default"},
    {"name": "stagger-delay", "value": "45s", "source": "flag"},
    {"name": "max-parallel", "value": 2, "source": "project"},
    {"name": "profile", "value": "comprehensive", "source": "default"}
  ],
  "profiles": [
    {
      "name": "comprehensive",
      "description": "all six lenses across three claude agents plus an adversarial codex peer",
      "roster": [
        {"name": "bugs+impl", "lenses": ["bugs", "impl"], "executor": "claude",
         "model": "opus", "effort": "high", "color": "6", "color_name": "cyan"},
        {"name": "codex", "lenses": ["adversarial"], "executor": "codex",
         "model": "gpt-5.6-sol", "effort": "high", "color": "3", "color_name": "yellow"}
      ]
    }
  ],
  "lenses": [
    {"name": "adversarial", "description": "adversarial pass — attacks the change looking for what a sympathetic reader would accept"},
    {"name": "bugs", "description": "correctness defects — logic and boundaries, nil and bounds, concurrency, resource lifetime, error handling"}
  ],
  "stages": [
    {"name": "synthesis", "description": "merges every source's findings, dedupes them, boosts corroboration and drops weak singletons",
     "executor": "claude", "model": "opus", "effort": "high"},
    {"name": "verify", "description": "checks each finding against the code and returns one verdict per finding",
     "executor": "claude", "model": "opus", "effort": "high"}
  ],
  "vocabulary": {
    "executors": ["claude", "codex"],
    "efforts": ["low", "medium", "high", "xhigh", "max"]
  },
  "paths": {
    "tasks_dir": "/abs/project/.revmux/tasks",
    "config_dir": "/home/user/.config/revmux",
    "project_dir": "/abs/project/.revmux",
    "workdir": "/abs/project",
    "tasks": [
      {"id": "pr-123", "description": "OAuth token exchange rework",
       "url": "https://github.com/umputun/revmux/pull/123", "branch": "feature/oauth", "base": "4ed3259",
       "rounds": ["01-initial", "02-after-fix"]}
    ]
  }
}
```

The output is abbreviated above — a real run lists every profile, every lens and every knob.

`paths.tasks` is the task store: every task that already exists, whatever its `task.md` says about it, and
the rounds recorded under it. That is what a caller matches an in-flight review against — with an id alone it
cannot tell whether `pr-123` is the same subject and opens `pr123` beside it. A task with no `task.md` is
still listed, with the anchors empty; one whose `task.md` will not parse is listed too, with `meta_error`
saying why they are empty. Rounds are those that ran to completion, so neither a directory prepared but not
yet reviewed nor a round whose review was interrupted is one — both are still open to review under that
name, the interrupted one as long as its review left nothing behind.

An empty list always means empty. A tasks root that could not be read is reported as `paths.tasks_error`,
a task whose own directory could not be read as `rounds_error` on that entry, and a `--workdir` that would
not resolve as `paths.workdir_error` — nothing that failed is reported as nothing being there.

## Agent skills

revmux is built to be driven by a caller model, and this repository ships that caller as a skill for
two harnesses. The skill does the half revmux deliberately does not: it resolves what is being
reviewed, runs the git commands, writes the round's `input/`, launches revmux, reads the JSON back, and
opens a new round on the same task after fixes.

| harness | location | install |
|---|---|---|
| Claude Code | `.claude-plugin/skills/revmux/` | `/plugin marketplace add umputun/revmux` then `/plugin install revmux@revmux` |
| Codex CLI | `plugins/codex/skills/revmux/` | `cp -r plugins/codex/skills/revmux ~/.codex/skills/revmux` |

Both carry the same reference material — how to compose `scope.md`, `goal.md`, `profile.md` and
`context/`, the full flag and lens tables, the JSON shape and the run archive layout — and the same
scripts:

- `preflight.sh` — check revmux plus the executors the chosen profile's roster and stages need
- `task-state.sh` — resolve the tasks root from `revmux config` and report what a task holds: its
  `task.md` anchors, its rounds, and each round's `input/` state
- `launch-revmux.sh` — run revmux **with its TUI** in a terminal overlay (agterm, tmux, Zellij, herdr,
  kitty, wezterm, cmux, ghostty, iTerm2, Emacs vterm), returning the report on stdout and revmux's own
  exit code

That last one exists because an agent's shell has no tty, so the TUI never appears there. The overlay
is how a user watches a review happen; everything else about the run is identical. Under agterm it
opens as a floating panel at 80% of the pane.

The launcher forwards `PATH` into the overlay deliberately: revmux spawns `claude` and `codex` itself,
and an overlay shell inherits a server-process environment that predates the user's shell rc files, so
without it every agent degrades on a binary that is plainly installed. `ANTHROPIC_API_KEY` is not
forwarded, since an `env KEY=VAL` prefix would put it in the process argv.

## Development

```
make build    # build .bin/revmux
make install  # symlink .bin/revmux into $BINDIR (default /usr/local/bin)
make test     # race detector plus coverage, mocks excluded
make lint     # golangci-lint
make fmt      # gofmt and goimports
```

No test spawns a real model. The executors are driven through a mocked `CommandRunner` against recorded CLI
fixtures, the pipeline through mocked runners, and the TUI through synthetic bubbletea messages.

## License

MIT
