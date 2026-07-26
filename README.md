# revmux

revmux runs a structured multi-agent code review. It spawns and supervises `claude --print` and `codex exec`
subprocesses, then returns findings on stdout as markdown or JSON.

It exists because agent fan-out driven from inside an AI coding session is unobservable and unrecoverable:
agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill, no retry and no
progress display. A subprocess does not make the model faster. What it buys is control: a watchdog that
notices a stall, a kill and retry the caller owns, a live view of every agent, per-agent token counts, and a
run archive to debug a bad review afterwards.

revmux runs a review and returns findings, and does nothing else. It performs no scope detection, no git
operations, no PR fetching and no source modification. All review context is written to a task directory by
the caller and passed in with `--task`.

## Install

```
go install github.com/umputun/revmux/app@latest
```

The binary is installed as `app`; rename it to `revmux`, or build from a clone instead:

```
git clone https://github.com/umputun/revmux.git && cd revmux
make build        # produces .bin/revmux
```

revmux drives the model CLIs as subprocesses, so both must already be installed and authenticated:

- `claude` — every lens agent and both model stages run on it by default
- `codex` — only needed when a roster entry or a stage declares `executor: codex`, which the shipped
  profiles all do

`ANTHROPIC_API_KEY` is stripped from the child environment by default so `claude` uses interactive
subscription auth; pass `--preserve-anthropic-api-key` if you authenticate by key. `CLAUDECODE` is always
stripped, since a `claude` child refuses to start when it thinks it is a nested session.

## Quick start

```
mkdir -p .revmux/tasks/pr-123
cat > .revmux/tasks/pr-123/scope.md <<'EOF'
Review the changes on this branch against master.
Diff command: git diff master...HEAD
EOF

revmux --task pr-123
```

That runs the `comprehensive` profile, shows a live TUI, and writes the markdown report to stdout. Re-run
after fixing something, under a new round name, and revmux carries the earlier rounds into every prompt:

```
revmux --task pr-123 --run after-fix --json > findings.json
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

Review context reaches revmux only as a task directory the caller has filled. `--task` names one under
`--tasks-dir` (default `./.revmux/tasks`), and `--run` names the round inside it. Both names are
caller-chosen and semantic; revmux allocates neither.

```
<tasks-dir>/pr-123/           caller-owned, revmux never writes or prunes anything here
├── scope.md                  → {{SCOPE}}    required; missing or empty is a load-time error
├── goal.md                   → {{GOAL}}     optional
├── profile.md                → {{PROFILE}}  optional, the project's own conventions
├── context/                  → {{CONTEXT}}  optional directory: ticket text, design notes, spec excerpts
└── runs/                     revmux-owned; the only thing it writes, and all --keep-runs prunes
    └── after-fix/
```

Variables expand to the **paths** of these files, never to their contents — the agent reads them itself.
revmux stats them and never opens one, so no prompt can be bloated by a large scope. An absent optional file
expands to `none provided`, which is not an error: the run proceeds with generic severity calibration.

There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags. One mechanism, no
precedence rules, and nothing for revmux to author.

`--run` defaults to a UTC timestamp. A name that already exists is an error rather than an overwrite: a round
that went badly is exactly the one worth keeping.

**Prior rounds are injected into every composed prompt.** revmux wrote them, so it hands them over rather
than making the caller copy them forward. The injected block is the `runs/` path plus a generated one-line
inventory per round — name, when it ran, finding counts by severity, which sources degraded — so an agent can
judge relevance without opening anything. It carries its own re-evaluate-independently instruction, and on a
first round it is omitted entirely.

## Run archive

Every run writes its artifacts under `runs/<run>/`. They exist so a review can be audited without re-running
it, which the final report alone cannot support.

```
runs/after-fix/
├── manifest.json             roster, prompt provenance + content hashes, requested vs actual model, timings
├── prompts/
│   ├── agents/               composed prompt per agent, post-substitution — the bytes the model saw
│   │   ├── bugs+impl.md
│   │   └── codex.md
│   └── stages/               separate from agents/ so an agent named `verify` cannot collide
│       ├── synthesis.md
│       ├── verify-app-executor.md      one per directory group
│       └── verify-app-pipeline.md
├── stages/
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

A failed archive write fails the run. A report emitted next to a half-written archive reads as complete, and
the gap only surfaces later when someone tries to audit it.

Old rounds are pruned to `--keep-runs` by modification time. Pruning only ever reads `runs/`, so `scope.md`,
`goal.md`, `profile.md` and `context/` are never candidates however aggressive the setting is.

## Configuration

Two precedence chains, not one.

**Runtime knobs** — command line, then `./.revmux/config`, then `~/.config/revmux/config`, then the built-in
default. Layers merge per key, so a project config setting one knob leaves the rest alone. The project layer
is auto-detected: no flag selects it, and its absence simply drops it.

**Prompt and lens files** — `./.revmux/`, then `~/.config/revmux/`, then the `go:embed` defaults, resolved
**per file**. Overriding one lens does not orphan the other six, and deleting an override falls back to the
embedded copy rather than disabling the lens. To actually drop a lens, remove it from the profile roster.

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

`--config-dir` relocates the user layer. `--init` writes the commented-out config template to `./.revmux/`,
and `--dump-defaults <dir>` extracts the embedded prompt tree; neither overwrites a file you have customized,
and a normal run writes no config at all.

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
     model: gpt-5.6-sol, effort: xhigh, color: yellow}
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
`{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}`, plus `{{FINDINGS}}` and `{{SOURCES}}` for the two model stages.
A prompt file naming anything else fails at load, which is what makes a typo loud instead of silent.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--task=<id>` | | name of the task directory holding the review context |
| `--run=<name>` | UTC timestamp | name for this round of the review |
| `--lenses=<a,b>` | | lens set replacing the profile roster |
| `--workdir=<dir>` | working directory | directory the review subprocesses run in |
| `--min-confidence=<n>` | `0` | drop findings below this confidence |
| `--no-synthesis` | | skip the synthesis stage |
| `--no-verify` | | skip the verification stage |
| `--no-tui` | | disable the terminal UI |
| `--json` | | write the report as JSON |
| `--preserve-anthropic-api-key` | | pass `ANTHROPIC_API_KEY` to the model CLIs |
| `--config-dir=<dir>` | `~/.config/revmux` | directory holding the config file and the prompt tree |
| `--init` | | write the commented-out config template to `./.revmux/` |
| `--dump-defaults=<dir>` | | extract the embedded prompt tree into a directory |
| `--version` | | show version and exit |

The runtime knobs below also read from the config file, under the same name as the flag:

| Flag | Config key | Default | Description |
|---|---|---|---|
| `--idle-timeout=<d>` | `idle-timeout` | `2m` | kill and retry an agent after this long with no output |
| `--hard-timeout=<d>` | `hard-timeout` | `20m` | kill an agent after this long in total |
| `--stagger-delay=<d>` | `stagger-delay` | `30s` | how long to wait for the first agent before releasing the rest |
| `--max-parallel=<n>` | `max-parallel` | `4` | how many agents run at once |
| `--verify-groups=<n>` | `verify-groups` | `6` | cap on the number of verifier groups |
| `--tasks-dir=<dir>` | `tasks-dir` | `./.revmux/tasks` | root directory holding task directories |
| `--keep-runs=<n>` | `keep-runs` | `10` | how many runs to keep per task |
| `--profile=<name>` | `profile` | `comprehensive` | profile naming the roster to run |

One subcommand: `revmux config` prints the resolved configuration as JSON and exits.

## Output

The report goes to **stdout** — markdown by default, the machine shape with `--json`. The TUI renders to the
tty and progress lines go to stderr, so `revmux --task pr-123 --json > findings.json` works with the display
running. The TUI is gated on the tty being openable, never on stdout being a terminal, which is false in
exactly that invocation.

```json
{
  "scope": {"task": "pr-123", "run": "after-fix", "scope_path": "/abs/.revmux/tasks/pr-123/scope.md"},
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

`--min-confidence` filters once, before rendering, and both the report and the exit code are computed from
the filtered set. Open questions, pre-existing and immaterial findings pass through untouched.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | no findings above `--min-confidence` |
| `1` | findings above `--min-confidence` |
| `2` | tool error: bad config, unreadable prompt tree, missing or empty `scope.md`, a `--run` name that already exists, an unwritable run artifact, or every source degraded |

`1` is a normal outcome, not a failure. Callers script against these values.

## Terminal UI

A status table on top, one row per agent — name, state, elapsed, last activity — and one detail pane below
it. Tab `0 · all` is the combined chronological view and is focused by default; tabs `1`-`9` are per-agent
full-detail scrollback including thinking. On completion the model switches to the findings browser, and the
agent tabs stay reachable so a reader can check why a finding was raised.

| keys | action |
|---|---|
| `tab` / `shift+tab`, `←` `→`, `h` `l` | switch pane |
| `0`-`9` | focus that pane directly |
| `f` | jump to the findings browser |
| `↑` `↓`, `k` `j` | scroll, or move the cursor in the browser |
| `pgup` `pgdn`, `ctrl+b` `ctrl+f` | page |
| `home` `end`, `g` `G` | top, bottom |
| `enter` | expand or collapse a finding |
| `/` | filter findings; `enter` accepts, `esc` clears |
| `q`, `esc`, `ctrl+c` | quit |

Quitting stops watching the run, it does not stop the run: the report is still written to stdout when the
pipeline finishes.

With `--no-tui`, or when the tty cannot be opened, the same events render as timestamped lines on stderr:

```
16:02:11 bugs+impl: started [bugs, impl]
16:02:19 arch+quality: tool: Grep
16:04:02 docs+tests: retrying: agent docs+tests stalled
16:05:12 bugs+impl: done, 6 findings
16:05:40 stage synthesis
```

## `revmux config`

revmux is normally driven by a caller model, so the resolved configuration is machine-readable rather than
something to reconstruct from `--help` and a directory listing. `revmux config` prints it as JSON on stdout
and exits `0` — it runs no pipeline, writes no run directory and touches nothing under the tasks root.

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
         "model": "gpt-5.6-sol", "effort": "xhigh", "color": "3", "color_name": "yellow"}
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
    "tasks": ["pr-123"]
  }
}
```

The output is abbreviated above — a real run lists every profile, every lens and every knob. `paths.tasks`
lists the task directories that already exist, since a `--run` name collides with an existing round and a
caller cannot avoid that blind.

## Development

```
make build    # build .bin/revmux
make test     # race detector plus coverage, mocks excluded
make lint     # golangci-lint
make fmt      # gofmt and goimports
```

No test spawns a real model. The executors are driven through a mocked `CommandRunner` against recorded CLI
fixtures, the pipeline through mocked runners, and the TUI through synthetic bubbletea messages.

## License

MIT
