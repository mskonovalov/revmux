---
name: revmux
description: Run a supervised multi-agent code review by composing a task directory and driving the revmux CLI, then report or act on the findings it returns. revmux spawns and watches parallel claude and codex subprocesses with stall detection, retry, per-agent progress and a full run archive; this skill is the caller that writes the review context, launches it, reads the JSON back, and re-runs it after fixes. Also answers questions about revmux itself — profiles, lenses, task directories, flags, the JSON shape, exit codes and the run archive. Activates on "revmux", "run revmux", "multi-agent review", "supervised review", "review with revmux", "revmux this branch", "revmux the last commit", "run a revmux round", "re-review after fixes", "revmux profiles", "revmux lenses", "what does revmux return", "revmux exit codes", "revmux task directory".
argument-hint: 'optional: what to review, plus "focused" / "final" / "lenses a,b"'
allowed-tools: [Bash, Read, Edit, Write, Grep, Glob]
---

# revmux — supervised multi-agent code review

revmux runs a structured review across several model subprocesses and returns findings. It spawns and
supervises `claude --print` and `codex exec`, watches each one for stalls, kills and retries what hangs,
and writes a full archive of the run.

**revmux does exactly that and nothing else.** It performs no scope detection, no git operations, no
PR fetching and no source modification. It has zero VCS dependency. Every piece of review context
reaches it as files on disk that *this skill* writes.

So the division of labour is fixed:

| this skill does | revmux does |
|---|---|
| work out what is being reviewed | supervise the agents |
| run the git commands, gather context | stagger, watch, retry, degrade |
| write `scope.md`, `goal.md`, `profile.md`, `context/` | compose prompts and archive them |
| choose profile, lenses, flags | merge, dedupe, verify |
| launch revmux and wait | return findings on stdout |
| read the JSON, present it, fix, re-run | inject prior rounds into the next round |

## Script path resolution

Resolve the script directory from the repo first, then fall back to Codex home:

```bash
SCRIPT_DIR="$(git rev-parse --show-toplevel 2>/dev/null)/plugins/codex/skills/revmux/scripts"
if [ ! -d "$SCRIPT_DIR" ]; then
    SCRIPT_DIR="${CODEX_HOME:-$HOME/.codex}/skills/revmux/scripts"
fi
```

Use `$SCRIPT_DIR` in place of every script path below.

## Asking the user

This skill has two decision points where the user has to choose: an ambiguous scope, and headless
versus overlay. Codex has no structured question tool, so present a **numbered list** and ask for a
number:

```
Which scope?
  1. branch vs master (all 7 commits)
  2. just the uncommitted changes
```

Where the Claude Code version enters plan mode before applying fixes, write the plan inline as
markdown and ask for explicit confirmation before touching any file.

## Activation triggers

- "revmux", "run revmux", "review with revmux"
- "multi-agent review", "supervised review", "parallel agent review"
- "revmux this branch", "revmux the last commit", "revmux the uncommitted changes"
- "another revmux round", "re-review after fixes"
- questions: "revmux profiles", "what lenses are there", "what does revmux return", "revmux exit codes"

## Answering questions without running anything

If the user is asking **about** revmux rather than asking for a review, answer from the references and
do not launch a run — a review costs several minutes and real tokens.

- `references/task-dir.md` — the four context inputs, what makes each good, worked examples
- `references/invocation.md` — flags, profiles, lenses, stages, timing, config precedence, trust
- `references/output.md` — the JSON shape field by field, verdicts, exit codes, the run archive

For anything about what is *currently configured* — which profiles exist, what a lens covers, where
the tasks root resolved to — run `revmux config` and read the answer rather than quoting the
references. It reports what resolved, including the user's own overrides, and it runs no pipeline.

## Non-negotiables

Four things cause most misuse. They are stated here rather than in a reference because getting any of
them wrong wastes a fifteen-minute run.

**1. Exit `1` means findings were reported. It is a success.** `0` = ran fine, nothing found. `1` =
ran fine, findings returned. `2` = tool error, nothing usable. Never treat `1` as a failure and never
re-run on it — the report on stdout is complete.

**2. Run it in the background.** A review takes three to fifteen minutes, past most foreground command
caps. Launch it with the harness's background mechanism, redirect stdout to a file, and wait for the
completion notification. Do not poll in a loop, do not sleep-and-check. This applies to the overlay
launcher too — it blocks for the whole review.

**3. Check `sources.degraded` before believing the findings.** A run where two of four agents died
returns a short list, and nothing about the list says why. If `expected != reported`, the review is
partial — say so, and never report "no findings" from a degraded run as "the code is clean".

**4. `.revmux/` in a repository is executable code.** A checked-in `.revmux/lenses/*.md` replaces the
shipped lens, and that text becomes instructions a headless agent with a shell executes. Before
reviewing code from an untrusted author, either read `.revmux/` first or run revmux from outside the
tree with explicit `--workdir`, `--tasks-dir` and `--config-dir`. See `references/invocation.md`.

## Workflow

### Step 0: Preflight

```bash
$SCRIPT_DIR/preflight.sh [profile]
```

Checks that `revmux` is installed and that every executor the chosen profile's roster and stages need
(`claude`, `codex`) is on PATH. Exits `1` with a named missing binary rather than letting the run
launch agents and degrade all of them.

If revmux is missing, guide installation:

```
go install github.com/umputun/revmux/app@latest    # installs as 'app'; rename to 'revmux'
git clone https://github.com/umputun/revmux.git && cd revmux && make install
```

### Step 1: Resolve what is being reviewed

revmux does no scope detection, so this is entirely this skill's job. Work it out from the user's
words, the session so far, and the repository state.

Common shapes, and what each resolves to:

| the user says | scope resolves to |
|---|---|
| nothing, on a feature branch | branch versus its base — `git diff <base>...HEAD` |
| nothing, on master with uncommitted work | the working tree — `git diff` and `git diff --staged` |
| nothing, on master and clean | the last commit — `git diff HEAD~1` |
| "the last N commits" | `git diff HEAD~N` |
| "since <ref>" | `git diff <ref>..HEAD` |
| "this PR" | fetch it first, then a ref range; revmux will not fetch it |
| a path | that subtree, still expressed as a diff plus a read list |

Run the git commands **here** to learn scale and file list — that is what makes a good `scope.md`.
When the change is one this session just produced, the intent is already known and Step 2 costs
nothing.

Ask the user only when genuinely ambiguous — a feature branch with uncommitted work is the standard
case where "branch diff" and "just my uncommitted changes" are both plausible.

### Step 2: Compose the task directory

Read `references/task-dir.md` before writing these. It has worked examples and the rules that matter.

Pick a semantic task id (`pr-123`, `tui-rework`, `since-c06c558`) and check what already exists:

```bash
$SCRIPT_DIR/task-state.sh <task-id>
```

It resolves the tasks root from `revmux config` — do not hardcode `./.revmux/tasks`, since a config
can move it — and lists which context files are present and which run names are taken.

Then write, under the reported `task_dir`:

- **`scope.md`** — required. What changed, the commands to see it, its scale, which files to read in
  full, what to ignore. Write commands in their plainest form: `git diff master...HEAD`, never
  `git -c core.pager=cat diff ...`, because a leading option defeats the child's permission prefix
  matching and the agent silently loses access to the diff.
- **`goal.md`** — optional, high value. What the change is for, and a short "this is correct only
  if…" list. This is what lets agents review for fitness rather than only for internal consistency.
- **`profile.md`** — optional, reusable across every task in the repo. What kind of software this is,
  what a real failure looks like here, where the project's documented rules live, which conventions
  are deliberate. Without it the review is calibrated to a generic production service.
- **`context/`** — optional. Ticket text, design notes, a commit list, anything else worth reading.

If `scope.md` already exists and this is a re-review, leave it alone unless the scope genuinely moved.
Never overwrite caller-owned files without saying so.

### Step 3: Choose profile and flags

| profile | roster | when |
|---|---|---|
| `comprehensive` (default) | `bugs+impl`, `arch+quality`, `docs+tests` on claude, plus an adversarial codex peer | a real change with real risk |
| `focused` | one `bugs` agent plus the codex peer | small or time-boxed, correctness is the concern |
| `final` | `bugs+impl` plus the codex peer, nothing below major | last look before merging |

`--lenses a,b` narrows to a specific viewpoint, but produces **one** agent carrying both lenses and
drops the codex peer — so it also drops every cross-source confidence boost. Prefer a profile unless
there is a specific reason, such as a documentation-only change (`--lenses docs`).

Worth setting: `--min-confidence=70` when the user wants only actionable findings, `--no-verify` when
a human will read every finding anyway and speed matters.

### Step 4: Run it — headless or in an overlay

Two ways to run the same review. Both return the same report on stdout and the same exit code; they
differ only in whether the TUI is on screen while it happens.

**Headless — the default.** No terminal needed, nothing on screen:

```bash
revmux --task <id> --run <name> --no-tui > /tmp/revmux-<id>-<run>.json 2> /tmp/revmux-<id>-<run>.log
```

Launch in the background and wait for the completion notification. Do not poll. The stderr log carries
timestamped progress lines if anything needs diagnosing afterwards.

**Overlay — when the user wants to watch.** revmux has a live TUI: a status table with one row per
agent showing state, elapsed and last activity, per-agent scrollback tabs, and a findings browser at
the end. It needs a terminal, which an agent's own shell does not have, so a launcher opens it in a
terminal overlay:

```bash
$SCRIPT_DIR/launch-revmux.sh --task <id> --run <name> [any other revmux flag]
```

It detects the terminal (agterm, tmux, zellij, herdr, kitty, wezterm/kaku, cmux, ghostty, iTerm2,
Emacs vterm), runs revmux with its TUI in an overlay, and returns **the report on stdout and revmux's
own exit code** — so it drops into the same Step 5 as headless mode:

```bash
$SCRIPT_DIR/launch-revmux.sh --task pr-123 --run round-1 > /tmp/revmux-pr-123.json
```

Under agterm it opens as a floating panel at 80% of the pane, so the session stays visible around it.
Do not pass `--no-tui` to the launcher; that is what headless mode is for and the script rejects it.

Notes that matter for the overlay path:

- **It blocks for the whole review**, which is longer than most foreground command caps. Run it in the
  background exactly like the headless form, or raise the timeout to the maximum the harness allows.
- The launcher forwards `PATH` into the overlay. This is not optional plumbing: revmux spawns `claude`
  and `codex` itself, and an overlay shell inherits a server-process environment that predates the
  user's shell rc files, so without it every agent degrades on a binary that is plainly installed.
- It gives the TUI `--auto-exit=30s` unless a value was passed, so the overlay closes itself rather
  than blocking on a keypress nobody is there to press. `REVMUX_AUTO_EXIT=0` restores waiting.
- `ANTHROPIC_API_KEY` is deliberately not forwarded — an `env KEY=VAL` prefix would put it in the
  process argv where any `ps` can read it. Overlay runs use interactive subscription auth; use
  headless mode for key-based auth.
- Sizes are overridable: `REVMUX_AGTERM_PERCENT` (default `80`), `REVMUX_POPUP_WIDTH` /
  `REVMUX_POPUP_HEIGHT` (default `90%`) for tmux, zellij and wezterm.
- Under tmux, `REVMUX_TMUX_WINDOW=1` uses a server-owned window instead of a popup, so the review
  survives a dropped SSH or tmux client. Worth suggesting for a long review over a flaky link. Under
  agent-deck this is automatic — its control-mode client cannot render a popup at all.

**Which to use:** headless unless the user asked to watch, said "show me", or is clearly sitting at
the terminal. When unsure and the choice matters, ask.

**If the launcher dies, the report is not lost.** revmux archives every run, so the report is on disk
at `<task-dir>/runs/<run>/findings.json` and `report.md` regardless of what happened to the launcher.
Read from there rather than re-running a review that already completed.

`--run` names this round and defaults to a UTC timestamp. A name that already exists is an error
rather than an overwrite, deliberately — a round that went badly is the one worth keeping. Use
semantic names across rounds: `round-1`, `after-fix`, `final`.

Tell the user it is running and roughly how long it will take.

### Step 5: Read the result

Read the JSON file. Read `references/output.md` for the full field-by-field shape.

In order:

1. **Exit code.** `2` means nothing usable — read the stderr log, which names the cause, and fix it
   rather than re-running blind. `0` and `1` both mean the review completed.
2. **`sources`.** If `degraded` is non-empty, the review is partial. Lead with that when reporting.
3. **`findings`.** Group by severity. Each carries `file`, `line`, `title`, `body`, `fix`, `sources`,
   `lenses` and a `verdict`.
4. **`open_questions`** and **`pre_existing`** — report separately; the second is explicitly not this
   change's responsibility.

Two fields are routinely confused and must not be: `sources` holds **agent names** and is the only
evidence of independent corroboration; `lenses` holds the lens names that raised it and is
informational. One agent flagging something under two lenses is still one source.

### Step 6: Present

Report to the user. Lead with completeness, then severity counts, then each finding with its location,
its argument and its suggested fix. Call out findings with more than one entry in `sources` — two
separate processes agreeing is the strongest signal in the report.

Do not compress a finding to its title. The body carries the trigger and the consequence.

Stop here unless fixing was requested. A review produces findings; turning them into edits is a
separate decision that belongs to the user.

### Step 7: Fix and re-run, if asked

The fix-and-re-review loop lives here, in the caller — revmux has no loop mode.

1. Agree with the user which findings to act on. Present them as a list; a `rejected` or `immaterial`
   verdict means revmux already checked and dismissed it.
2. Make the fixes.
3. Re-run the **same task** under a **new run name**:

```bash
revmux --task <id> --run after-fix --no-tui \
    > /tmp/revmux-<id>-after-fix.json 2> /tmp/revmux-<id>-after-fix.log
```

**Never `2>&1` into the report file.** stdout is the report and stderr is the progress renderer;
merging them puts timestamped lines inside the JSON and nothing can parse it.

revmux injects the prior rounds into every composed prompt itself — the `runs/` path plus a one-line
inventory per round, carrying its own instruction to re-evaluate independently. **Do not paste prior
findings into `scope.md`**: it duplicates what revmux already injects and anchors the agents on
conclusions they are supposed to re-derive.

Repeat until the findings that matter are gone. `--profile final` is a good last round.

## Debugging a review that looks wrong

Every run archives itself under `<task-dir>/runs/<run>/`. Reach for it rather than re-running:

| question | where |
|---|---|
| why did this agent report nothing? | `agents/<name>.jsonl` — the verbatim stream |
| did an agent stall or get retried? | `events.jsonl`; a `<name>.retry.jsonl` means it did |
| did synthesis drop something real? | `stages/1-found.json` versus `stages/2-synthesized.json` |
| did verify reject something wrongly? | `stages/2-synthesized.json` versus `3-verified.json` |
| what was this agent actually asked? | `prompts/agents/<name>.md` — post-substitution |
| which lens text was used, from which layer? | `manifest.json` |

## Example sessions

```
User: "revmux this branch"
→ preflight.sh → revmux, claude, codex all present
→ git: on tui-rework, 7 commits vs master, 22 files, +840/-310
→ task-state.sh tui-rework → does not exist yet
→ write scope.md (diff commands, file list, scale), goal.md (from this session), profile.md
→ revmux --task tui-rework --run round-1 --no-tui > /tmp/…json  (background)
→ ~9 minutes, exit 1
→ sources: expected 4, reported 4, degraded []
→ 6 findings: 1 major, 5 minor; 2 corroborated across bugs+impl and codex
→ present grouped by severity, with each body and fix
```

```
User: "fix the major one and run it again"
→ fix applied
→ revmux --task tui-rework --run after-fix --no-tui  (same task, new run)
→ revmux injects round-1's inventory into every prompt
→ exit 0, no findings above threshold
→ "clean; round-1's major is gone and nothing new was raised"
```

```
User: "review the last commit, quick"
→ --profile focused: one bugs agent plus the codex peer
→ revmux --task last-commit --run round-1 --profile focused --no-tui
→ ~4 minutes, exit 1, 2 minor findings
```

```
User: "revmux the branch, I want to watch it"
→ same Steps 0-3 as any other review
→ $SCRIPT_DIR/launch-revmux.sh --task tui-rework --run round-1 > /tmp/…json   (background)
→ agterm detected: floating overlay at 80%, TUI renders the status table live
→ user watches agents work, findings browser opens at the end, overlay self-closes after 30s
→ the launcher returns the same JSON on stdout and the same exit code
→ Step 5 onward is identical
```

```
User: "what lenses does revmux have?"
→ no run; `revmux config` and report .lenses[] with each description
```
