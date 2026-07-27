---
name: revmux
description: Run a supervised multi-agent code review by composing a task directory and driving the revmux CLI, then report or act on the findings it returns. revmux spawns and watches parallel claude and codex subprocesses with stall detection, retry, per-agent progress and a full run archive; this skill is the caller that writes the review context, launches it, reads the JSON back, and re-runs it after fixes. Also answers questions about revmux itself — profiles, lenses, task directories, flags, the JSON shape, exit codes and the run archive. Activates on "revmux", "run revmux", "multi-agent review", "supervised review", "review with revmux", "revmux this branch", "revmux the last commit", "run a revmux round", "re-review after fixes", "revmux profiles", "revmux lenses", "what does revmux return", "revmux exit codes", "revmux task directory".
argument-hint: 'optional: what to review, plus "focused" / "final" / "lenses a,b"'
allowed-tools: [Bash, Read, Edit, Write, Grep, Glob, AskUserQuestion]
---

# revmux — supervised multi-agent code review

revmux spawns and supervises parallel `claude --print` and `codex exec` subprocesses, watches each for
stalls, retries what hangs, and returns findings on stdout.

It does no scope detection, no git, no PR fetching, no source modification. This skill does that half.

| this skill | revmux |
|---|---|
| resolve what is under review | supervise, stagger, retry, degrade |
| run git, gather context | compose and archive prompts |
| write `scope.md`, `goal.md`, `profile.md`, `context/` | merge, dedupe, verify |
| choose profile, lenses, flags | return findings on stdout |
| read the JSON, present, fix, re-run | inject prior rounds |

## Activation triggers

- "revmux", "run revmux", "review with revmux"
- "multi-agent review", "supervised review", "parallel agent review"
- "revmux this branch", "revmux the last commit", "revmux the uncommitted changes"
- "another revmux round", "re-review after fixes"
- questions: "revmux profiles", "what lenses are there", "revmux exit codes"

## Answering questions without running anything

If asked **about** revmux rather than for a review, answer from the references and do not launch a run.

- `references/task-dir.md` — composing the four context inputs
- `references/invocation.md` — flags, profiles, lenses, overlay backends, config precedence
- `references/output.md` — JSON shape, verdicts, exit codes, run archive

For anything about current configuration, run `revmux config` and read the answer. It reports what
resolved including user overrides, runs no pipeline, and is always safe to call.

## Non-negotiables

**1. Exit `1` means findings were reported — a success.** `0` none, `1` findings, `2` tool error.
Never treat `1` as failure. Never re-run on it.

**2. Run it in the background.** A review takes 3-15 minutes. Redirect stdout to a file, wait for the
completion notification. Do not poll, do not sleep-and-check. Applies to the overlay launcher too.

**3. Check `sources.degraded` before believing the findings.** If `expected != reported` the review is
partial. Say so. Never report "no findings" from a degraded run as "the code is clean".

**4. `.revmux/` in a repository is executable code.** A checked-in `.revmux/lenses/*.md` becomes
instructions a headless agent with a shell executes. Before reviewing untrusted code, either read
`.revmux/` first or run from outside the tree with explicit `--workdir`, `--tasks-dir`, `--config-dir`.

**5. Never `2>&1` into the report file.** stdout is the report, stderr is progress. Merging them makes
the JSON unparseable.

## Workflow

### Step 0: Preflight

```bash
${CLAUDE_SKILL_DIR}/scripts/preflight.sh [profile]
```

Checks revmux plus every executor the profile's roster and stages need. Exits `1` naming what is
missing.

If revmux is absent:

```
go install github.com/umputun/revmux/app@latest    # installs as 'app'; rename to 'revmux'
git clone https://github.com/umputun/revmux.git && cd revmux && make install
```

### Step 1: Resolve what is being reviewed

| the user says | scope |
|---|---|
| nothing, on a feature branch | `git diff <base>...HEAD` |
| nothing, on master with uncommitted work | `git diff` and `git diff --staged` |
| nothing, on master and clean | `git diff HEAD~1` |
| "the last N commits" | `git diff HEAD~N` |
| "since <ref>" | `git diff <ref>..HEAD` |
| "this PR" | fetch it first, then a ref range — revmux will not fetch |
| a path | that subtree, as a diff plus a read list |

Run the git commands here to learn scale and file list.

Ask only when genuinely ambiguous — a feature branch with uncommitted work is the standard case. Use
AskUserQuestion, here and at the headless-versus-overlay choice in Step 4.

### Step 2: Compose the task directory

Read `references/task-dir.md` first.

**List existing tasks before creating one.** A task accumulates rounds; every round after the first
depends on landing in the same directory, and nothing enforces it.

```bash
revmux config | jq -r '.paths.tasks[]'
```

Reuse a match verbatim. Only mint a new id when none matches.

**Derive the id:**

| reviewing | task id |
|---|---|
| a pull request | `pr-<number>` |
| an issue | `issue-<number>` |
| a branch | branch name with `/` replaced by `-` |
| a commit range | `since-<short-sha>` |
| working-tree changes | `wip-<branch>` |

No path separators, no `..`, no leading dot, not absolute — revmux rejects those at load.

```bash
${CLAUDE_SKILL_DIR}/scripts/task-state.sh <task-id>
```

Resolves the tasks root from `revmux config` — never hardcode `./.revmux/tasks` — validates the id,
and lists which context files and run names exist.

**Name runs `NN-label`:** `01-initial`, `02-after-fix`, `03-final`. Take `NN` from the `runs:` count.
Do not mix vocabularies across rounds of one task.

Then write, under the reported `task_dir`:

- **`scope.md`** — required. What changed, the commands to see it, its scale, which files to read in
  full, what to ignore. Write commands in plainest form: `git diff master...HEAD`, never
  `git -c core.pager=cat diff ...` — a leading option defeats the child's permission prefix matching.
- **`goal.md`** — optional. What the change is for, plus a "this is correct only if…" list.
- **`profile.md`** — optional, reusable across the repo. What the software is, what a real failure
  looks like, where the project's rules live, which conventions are deliberate.
- **`context/`** — optional. Ticket text, design notes, commit list.

If `scope.md` exists and this is a re-review, leave it unless the scope moved. Never overwrite
caller-owned files silently.

### Step 3: Choose profile and flags

| profile | roster | when |
|---|---|---|
| `comprehensive` (default) | `bugs+impl`, `arch+quality`, `docs+tests`, codex peer | real change, real risk |
| `focused` | one `bugs` agent plus codex peer | small or time-boxed |
| `final` | `bugs+impl` plus codex peer, nothing below major | pre-merge |

`--lenses a,b` produces **one** agent carrying both lenses and drops the codex peer, losing every
cross-source confidence boost. Prefer a profile unless narrowing is specifically wanted.

Also useful: `--min-confidence=70` for actionable-only, `--no-verify` when a human reads everything.

### Step 4: Run it — headless or overlay

Both return the same report on stdout and the same exit code.

**Headless — the default:**

```bash
revmux --task <id> --run <name> --no-tui > /tmp/revmux-<id>-<run>.json 2> /tmp/revmux-<id>-<run>.log
```

Launch in the background, wait for the notification.

**Before yielding, tell the user three things** — otherwise they sit for 10+ minutes with no signal:

1. what is running (task, profile, roster size) and the rough duration
2. the stderr log path, and that `tail -f <path>` shows live per-agent progress
3. that they can ask for status any time

On a status request, read the tail of the stderr log and `<task-dir>/runs/<run>/events.jsonl`. Report
the stage, which agents are active, and elapsed. Never guess.

**Overlay — when the user wants to watch:**

```bash
${CLAUDE_SKILL_DIR}/scripts/launch-revmux.sh --task <id> --run <name> [any revmux flag]
```

Detects the terminal (agterm, tmux, zellij, herdr, kitty, wezterm/kaku, cmux, ghostty, iTerm2, Emacs
vterm), runs revmux with its TUI in an overlay, returns the report on stdout. Under agterm: floating
panel at 80% of the pane. Do not pass `--no-tui`; the script rejects it.

- it blocks for the whole review — background it exactly like the headless form
- **its exit codes: `0`/`1`/`2` are revmux's, `3` is a launcher failure, `127` is revmux not
  installed.** A `3` means no review happened — that is the one to retry.
- overrides: `REVMUX_AGTERM_PERCENT` (80), `REVMUX_POPUP_WIDTH`/`HEIGHT` (90%), `REVMUX_AUTO_EXIT`
  (30s; `0` waits for a keypress), `REVMUX_TMUX_WINDOW=1` for a disconnect-resilient tmux window

**Choose headless** unless the user asked to watch or is clearly at the terminal.

**If the launcher dies the report is not lost** — it is at `<task-dir>/runs/<run>/findings.json` and
`report.md`. Read from there rather than re-running.

`--run` defaults to a UTC timestamp. A name that already exists is an error, not an overwrite.

### Step 5: Read the result

Read `references/output.md` for the full shape.

1. **Exit code.** `2` means nothing usable — read the stderr log, which names the cause. `0` and `1`
   both completed.
2. **`sources`.** Non-empty `degraded` means partial; lead with that.
3. **`findings`.** Group by severity.
4. **`open_questions`** and **`pre_existing`** — report separately.

`sources` holds **agent names** and is the only evidence of independent corroboration. `lenses` holds
lens names and is informational. One agent flagging under two lenses is still one source.

### Step 6: Present

Lead with completeness, then severity counts, then each finding with location, argument and fix. Call
out findings with more than one entry in `sources`.

Do not compress a finding to its title — the body carries the trigger and consequence.

Stop here unless fixing was requested.

### Step 7: Fix and re-run, if asked

1. Agree which findings to act on. A `rejected` or `immaterial` verdict means revmux already dismissed it.
2. Make the fixes.
3. Re-run the same task under the next run name:

```bash
revmux --task <id> --run 02-after-fix --no-tui \
    > /tmp/revmux-<id>-02-after-fix.json 2> /tmp/revmux-<id>-02-after-fix.log
```

revmux injects the prior rounds itself. **Do not paste prior findings into `scope.md`** — it
duplicates the injection and anchors agents on conclusions they should re-derive.

`--profile final` is a good last round.

## Debugging a review that looks wrong

Archive is at `<task-dir>/runs/<run>/`:

| question | where |
|---|---|
| why did this agent report nothing? | `agents/<name>.jsonl` |
| did an agent stall or get retried? | `events.jsonl`; a `<name>.retry.jsonl` means it did |
| did synthesis drop something? | `stages/1-found.json` vs `2-synthesized.json` |
| did verify reject wrongly? | `stages/2-synthesized.json` vs `3-verified.json` |
| what was this agent asked? | `prompts/agents/<name>.md` |
| which lens text, from which layer? | `manifest.json` |

## Example sessions

```
User: "revmux this branch"
→ preflight.sh → all present
→ git: on tui-rework, 7 commits vs master, 22 files, +840/-310
→ revmux config → no matching task; derive `tui-rework`
→ task-state.sh tui-rework → does not exist
→ write scope.md, goal.md, profile.md
→ revmux --task tui-rework --run 01-initial --no-tui > /tmp/…json  (background)
→ tell user: ~9 min, tail -f /tmp/…log for live progress
→ exit 1, sources 4/4, degraded []
→ 6 findings: 1 major, 5 minor; 2 corroborated across bugs+impl and codex
```

```
User: "fix the major one and run it again"
→ fix applied
→ revmux --task tui-rework --run 02-after-fix --no-tui
→ exit 0, nothing above threshold
```

```
User: "revmux the branch, I want to watch it"
→ launch-revmux.sh --task tui-rework --run 01-initial > /tmp/…json  (background)
→ agterm: floating overlay at 80%, TUI live, self-closes 30s after the report
→ same JSON, same exit code, Step 5 onward identical
```

```
User: "what lenses does revmux have?"
→ no run; `revmux config`, report .lenses[] with descriptions
```
