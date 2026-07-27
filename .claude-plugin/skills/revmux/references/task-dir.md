# The task directory — how to write review context revmux can use

revmux performs no scope detection, no git operations and no PR fetching. Everything the reviewing
agents know about the change arrives as files the caller wrote. Composing those files well is the
single largest lever on review quality — a vague `scope.md` produces a vague review, and no flag
compensates for it.

## Layout

```
<tasks-dir>/<task-id>/          caller-owned; revmux never writes or prunes anything here
├── scope.md                    → {{SCOPE}}    REQUIRED. Missing or empty is a load-time error (exit 2)
├── goal.md                     → {{GOAL}}     optional
├── profile.md                  → {{PROFILE}}  optional
├── context/                    → {{CONTEXT}}  optional directory, any number of files
└── runs/                       revmux-owned; the only thing it writes
```

`<tasks-dir>` defaults to `./.revmux/tasks` but is a config knob. Resolve it with
`scripts/task-state.sh <task-id>` rather than hardcoding the default — a project or user config can
move it, and writing to the wrong root produces "task not found" on a directory that visibly exists.

## Variables expand to paths, never to contents

`{{SCOPE}}` becomes the **absolute path** of `scope.md`. The agent reads the file itself with its own
tools. revmux stats these files and never opens one.

Three consequences worth acting on:

- A large `scope.md` costs nothing at launch. Length is bounded by what is useful to read, not by a
  prompt budget.
- Binary or oddly encoded files in `context/` are harmless to revmux, but an agent still has to read
  them. Prefer text.
- An absent optional file expands to the literal string `none provided`, not to a path that fails to
  open. That is deliberate: an agent whose `Read` failed could not tell absence from a broken run.

## scope.md — required

**What it answers: what am I reviewing, and how do I get at it?**

This is the one file that must exist. It is not a description of intent (that is `goal.md`) — it is
the concrete boundary of the review plus the commands that expose it.

Include, in roughly this order:

1. **What changed**, in a sentence or two — branch, commits, or "uncommitted changes".
2. **The commands to see it.** revmux runs no git; the agents run these themselves.
3. **Scale**, so the reviewer can calibrate breadth against depth ("nine files, +115/-36. Small, so
   review it thoroughly rather than broadly").
4. **Which files to read in full**, with a word on why each matters. A diff shows what changed, not
   what it changed *around*, and most real defects live in the interaction.
5. **Explicit exclusions**, if any — vendored code, generated files, an unrelated commit that is in
   the range for mechanical reasons.

Worked example:

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

An agent subprocess runs under a permission layer that matches command **prefixes**. A leading option
changes the prefix and can turn an allowed command into a denied one:

- write `git diff master...HEAD`
- not `git -c core.pager=cat diff master...HEAD`

The second form is the same command to a human and a different string to a prefix matcher. When an
agent reports that a command was denied and it is falling back to reading files directly, this is the
first thing to check — the review still completes, but it completes blind to the diff.

Pipe to `cat` only if pagination is a real problem, and put it at the end (`git log ... | cat`) where
it does not disturb the prefix.

## goal.md — optional, high value

**What it answers: what is this change trying to achieve, and what would make it wrong?**

Without it, agents review for internal consistency: does the code do what the code says. With it they
can review for fitness: does the code do what it was *for*. The `impl` lens and the verify stage both
lean on this, and the difference in output is large.

A good `goal.md` has three parts:

1. **Intent** — what the change is meant to accomplish, in the author's terms.
2. **Success criteria** — a short list of "this change is correct only if…" statements. These are the
   most useful lines in the file, because they give a verifier something falsifiable.
3. **The severity bar for this change specifically** — what would be serious here versus noise.

Example shape:

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

When there is genuinely no goal — a mechanical cleanup, a dependency bump — omit the file rather than
writing a placeholder. `none provided` is a valid state and the run proceeds with generic calibration.

## profile.md — optional, reusable

**What it answers: what kind of software is this, and what counts as a real failure here?**

This is the calibration file, and it is the one most worth writing once and reusing across every task
in a repository. Without it every project gets the same implicit bar, which tends to be "production
service with real traffic" — wrong for a personal tool, a UI surface, or a one-off script, in both
directions.

Cover:

- **What it is** — "standalone Go CLI, personal tooling, one maintainer" / "internal REST service
  handling customer records".
- **What a real failure looks like here** — be concrete. "A review that hangs with no output or
  silently loses an agent's findings" is useful; "bugs" is not.
- **Blast radius** — who or what is affected when this area breaks.
- **The reporting bar** — findings must be material, not merely true. Say what is noise here.
- **Where the project's own rules live** — point at `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`,
  `docs/`, whatever records the conventions. A deviation from a documented rule is always worth
  reporting; an agent that never finds the rules cannot report one.
- **Conventions that are deliberate** — list them, so an agent does not file them as defects. Private
  by default, wrapped errors, injected clocks, one test file per source file, and so on.

## context/ — optional directory

Anything else worth having: ticket text, a design note, a spec excerpt, the output of a command that
took a while to produce, the commit list. `{{CONTEXT}}` expands to the directory path, and the agents
list and read it themselves.

This is the escape hatch that keeps the variable vocabulary closed. There is no `--context-file` flag
and no way to add a new `{{VAR}}`; arbitrary extra material goes here.

Keep it curated. Everything in this directory is a candidate for an agent to spend a tool call on.

## Task and run names

Both are caller-chosen and semantic. revmux allocates neither.

- `--task pr-123`, `--task tui-rework`, `--task since-c06c558` — names the body of work. One task
  directory accumulates rounds.
- `--run round-1`, `--run after-fix`, `--run final` — names this round. Defaults to a UTC timestamp
  when omitted.

Both become filesystem paths, so both are validated: no path separators, no `..`, not absolute.

**A `--run` name that already exists is an error, not an overwrite.** A round that went badly is
exactly the one a later reflection pass needs to read. Call `scripts/task-state.sh <task-id>` and pick
a name not in its `runs:` list.

## Reusing a task across rounds

The intended loop is one task, many runs:

```
revmux --task pr-123 --run round-1     # review
<fix findings>
revmux --task pr-123 --run after-fix   # re-review
```

On the second run revmux injects a block into every composed prompt: the `runs/` path plus a
one-line inventory per prior round — name, when it ran, finding counts by severity, which sources
degraded. The caller does not copy anything forward, and must not paste prior findings into
`scope.md`; that duplicates what revmux already injects and anchors the agents on it.

The injected block carries its own instruction to re-evaluate independently, because an agent told
that a prior round flagged something tends to confirm it rather than judge it.

**Update `scope.md` between rounds when the scope genuinely moved** — new commits, a wider range.
Leave it alone when only the code changed under a fixed range.
