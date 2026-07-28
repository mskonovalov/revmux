# Loop mode — review, fix, re-review until clean

One round of revmux is a review. The loop is review → fix → commit → review again, until a review comes
back with nothing gating. revmux has no loop of its own: each iteration is a new round on the same task,
and revmux injects the prior ones itself.

Entered from Step 6, when the user picks the autonomous option — or straight away when he asked for a
loop up front, in which case round 1 is still reported before any fixing and Step 6 skips the question.

## Before starting

- **Local changes only.** A fetched PR branch is somebody else's; the loop commits, so it never runs
  against one. Offer a single re-review round instead.
- **The working tree must be clean.** Refuse to start otherwise — the loop's own commits have to be
  separable from whatever was already uncommitted.
- **Record the starting commit** and report it. Every round reviews the cumulative diff from it, and it
  is what undoes the whole loop: `git reset --hard <sha>`.

## What gates

Gating is `critical` and `major` in `findings`. Nothing else:

- `minor` never gates — it is what the loop is expected to leave behind.
- `immaterial`, `pre_existing` and `open_questions` are not in `findings` and never gate. An open
  question is a decision for the user, so the loop must not edit code to resolve one.
- A `degraded` run gates nothing either way: it is not evidence. Stop and report it.

**A round in which you fixed something is never a clean exit.** Clean means a *review* came back with
zero gating findings. After fixing, always run the confirming round.

## The iteration

1. Fix every gating finding, plus any minor co-discovered alongside them. A minor alone never starts an
   iteration.
2. Run the project's tests and linter before committing. A fix that breaks the build is not committed.
3. Commit locally. **Never push** — that stays the user's decision, and it is what makes the whole loop
   revertible.
4. Open the next round and run it. Its `scope` is the cumulative diff from the starting commit, not just
   this round's fixes.

Profile per round follows Step 7: `final` when the fixes stayed inside what the last round flagged,
`comprehensive` when they spilled into tests, docs or structure.

## Stopping

Stop on the first of these:

| condition | what to report |
|---|---|
| a review returns zero gating findings | clean, plus whatever minors are left |
| gating count did not drop from the previous round | not converging — name what keeps coming back and stop |
| five rounds | cap reached, with what is still open |
| a run exits `2`, or `sources.degraded` is non-empty | the failure, not a verdict — `1` is findings and is the normal case |

On a clean exit with minors left, ask **once** whether to sweep them. Fix them if so and stop either
way — no review round runs after that question.

## While it runs

Autonomous between rounds: no questions until it stops. The commits are the exception to the usual
draft-and-confirm, because opting into the loop is the authorization and nothing is pushed.

Report each round in plain language — "round 2: 3 findings, fixing 2 majors", "round 3: clean". The user
should be able to follow it without knowing any of the vocabulary on this page.
