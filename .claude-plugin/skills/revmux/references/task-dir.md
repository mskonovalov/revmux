# The task directory

All review context reaches revmux as files the caller wrote. revmux does no scope detection.

## Layout

```
<tasks-dir>/<task-id>/          caller-owned; revmux never writes or prunes here
├── scope.md                    → {{SCOPE}}    REQUIRED. Missing or empty = exit 2
├── goal.md                     → {{GOAL}}     optional
├── profile.md                  → {{PROFILE}}  optional
├── context/                    → {{CONTEXT}}  optional directory
└── runs/                       revmux-owned
```

`<tasks-dir>` defaults to `./.revmux/tasks` but is a config knob. Resolve it with
`scripts/task-state.sh <task-id>`, never hardcode it.

## Variables expand to paths, never contents

`{{SCOPE}}` is the absolute path of `scope.md`. The agent reads the file itself; revmux only stats it.

- a large `scope.md` costs nothing at launch — length is bounded by what is worth reading
- prefer text in `context/`; an agent still has to read it
- an absent optional file expands to the literal `none provided`, not a broken path

## scope.md — required

What is being reviewed, and how to get at it. Not intent — that is `goal.md`.

1. **What changed** — branch, commits, or "uncommitted changes"
2. **The commands to see it** — revmux runs no git; agents run these themselves
3. **Scale** — so the reviewer can trade breadth against depth
4. **Which files to read in full**, and why each matters
5. **Explicit exclusions** — vendored code, generated files

````markdown
# Scope

Review the two most recent commits on the `tui-rework` branch:

```
ce8a9e9 feat(ui): break the header's findings count down by severity
995f637 feat: put JSON on stdout by default, markdown behind --markdown
```

Fifteen files, +115/-36. Small, so review it thoroughly rather than broadly.

Read the change:

```
git diff c06c558..HEAD
git log --format='%H%n%s%n%n%b' c06c558..HEAD
```

Then read the files it touches in full:

```
app/ui/model.go          the tally type and Model.count
app/ui/status.go         the header
app/main.go              runOpts.write, which picks the stdout renderer
CLAUDE.md                the stdout rule this rewrote
```

Ignore `vendor/` entirely.
````

### Write the plainest form of every command

The agent subprocess runs under a permission layer matching command **prefixes**. A leading option
changes the prefix and can turn an allowed command into a denied one.

- write `git diff master...HEAD`
- not `git -c core.pager=cat diff master...HEAD`

When an agent reports a denied command and falls back to reading files directly, check this first —
the review completes, but blind to the diff.

Put any pipe at the end (`git log ... | cat`) where it does not disturb the prefix.

## goal.md — optional, high value

What the change is meant to achieve, and what would make it wrong. Without it agents review for
internal consistency only; with it they can review for fitness. The `impl` lens and verify both use it.

1. **Intent** — what the change accomplishes, in the author's terms
2. **Success criteria** — a "this is correct only if…" list. The most useful lines in the file:
   they give a verifier something falsifiable.
3. **The severity bar for this change** — what is serious here versus noise

```markdown
# Goal

<two or three paragraphs on what the change is for and why it was made this way>

So this change is correct only if:

- nothing that consumed the old output shape is silently broken, and its replacement is discoverable
- a degraded run is as obvious to a machine consumer as it was to a human reader
- the tally's parts can never contradict its total

Weigh findings by whether they would mislead a caller or a watcher. Do not inflate style preferences
into defects.
```

With no real goal — a mechanical cleanup, a dependency bump — omit the file rather than writing a
placeholder.

## profile.md — optional, reusable across the repo

What kind of software this is and what counts as a real failure. Without it the implicit bar is
"production service with real traffic", wrong for a personal tool or a UI surface in both directions.

- **What it is** — "standalone Go CLI, personal tooling, one maintainer"
- **What a real failure looks like** — concrete. "A review that hangs with no output" is useful;
  "bugs" is not.
- **Blast radius** — who is affected when this area breaks
- **The reporting bar** — findings must be material, not merely true. Say what is noise here.
- **Where the project's rules live** — `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`, `docs/`. A
  deviation from a documented rule is always worth reporting; an agent that never finds the rules
  cannot report one.
- **Conventions that are deliberate** — so an agent does not file them as defects
- **Which languages the change actually touches** — a Go repo's conventions are not the bar for a
  commit of shell and markdown

## context/ — optional directory

Ticket text, design notes, spec excerpts, a commit list. `{{CONTEXT}}` expands to the directory path.

The escape hatch that keeps the variable vocabulary closed: there is no `--context-file` flag and no
way to add a `{{VAR}}`.

Keep it curated — every file is a tool call an agent may spend.

## Task and run names

Caller-chosen, and both become filesystem paths: no separators, no `..`, not absolute, no leading dot.

### Task ids must be reproducible

Every round after the first depends on landing in the **same** directory, and nothing enforces it. A
later session writing `pr123` where an earlier one wrote `pr-123` gets a new directory and runs as a
first round with no history — the prior-rounds block is simply omitted, exactly as on a genuine first
round.

**List before creating:**

```bash
revmux config | jq -r '.paths.tasks[]'
```

Reuse a match verbatim. `task-state.sh` cannot help here — it answers "does *this exact id* exist".

**Derive the id:**

| reviewing | task id |
|---|---|
| a pull request | `pr-<number>` |
| an issue | `issue-<number>` |
| a branch | branch name, `/` replaced by `-` |
| a commit range | `since-<short-sha>` |
| working-tree changes | `wip-<branch>` |

Prefer the most stable identifier: a branch name outlives a sha, a PR number outlives a rename.

Branch names commonly contain `/`, which revmux rejects — replace it. `task-state.sh` validates and
refuses a bad id before any context file is written to a path revmux will not accept.

### Run names: `NN-label`

`01-initial`, `02-after-fix`, `03-final`. Sorts lexically, so `ls runs/` reads in order. Take `NN`
from `task-state.sh`'s `runs:` count.

Do not mix vocabularies inside one task — `round-1` next to `after-fix` shares no ordering axis.

Omitted, `--run` defaults to a UTC timestamp: sorts correctly, says nothing about why the round ran.

**An existing `--run` name is an error, not an overwrite.**

## Reusing a task across rounds

```
revmux --task pr-123 --run 01-initial
<fix findings>
revmux --task pr-123 --run 02-after-fix
```

On later rounds revmux injects the `runs/` path plus a one-line inventory per round — name, when it
ran, counts by severity, which sources degraded. It carries its own re-evaluate-independently
instruction.

**Never paste prior findings into `scope.md`** — it duplicates the injection and anchors the agents.

Update `scope.md` between rounds only when the scope moved. Leave it when only the code changed under
a fixed range.
