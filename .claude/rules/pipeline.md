---
paths:
  - "app/pipeline/**"
---

## Pipeline — the three-stage contract

Stage structure is **hardcoded**: find → synthesize → verify.
Everything that varies between review shapes is the roster and the severity bar, and both are configuration.
Do not turn this into a generic DAG engine — that was considered and rejected as over-engineering.

### Stage boundaries

- **find** — the profile's roster, all agents in parallel, launched staggered. Each returns structured findings.
- **synthesize** — one model call. Merges, dedups, boosts confidence, splits open questions and pre-existing issues.
- **verify** — parallel agents grouped by directory. Each confirms, rejects, refines, or reclassifies its own group.

`--no-synthesis` passes findings through with their `sources` and `lenses` attribution intact.
`--no-verify` marks every finding unverified rather than silently claiming it was checked.

Each stage is its own unexported type (`finder`, `synthesizer`, `verifier`) owning its own methods and test file.
`Pipeline.Run` is a thin three-call orchestrator.
Do not let `Pipeline` accumulate stage logic — it becomes a god object holding three stages, event fan-out and I/O plumbing.

### No VCS. None.

This package must never import a git library, shell out to `git`, or walk a repo looking for `.git`.
Scope arrives as `{{SCOPE}}`, the **absolute path** of a `scope.md` the caller authored;
the pipeline passes that path through and never opens the file.
Agents read it and run their own diff commands.
A change that makes the pipeline read a repository — or read the scope file — is a change that belongs in the caller.
This is the hardest boundary in the project and it is what keeps revmux reusable and testable.

### Source counting

**A source is a process.**
The cross-source confidence boost counts distinct processes, never tags and never lenses.
An agent carrying two lenses that flags the same issue under both is still **one** source — it cannot corroborate itself.

The pipeline knows which process emitted which finding, so the count is structurally correct.
**That only holds if Go assigns the attribution, never the model.**

`finder.parse` overwrites `Finding.sources` on every parsed finding with exactly the executing
`AgentSpec.Name`, discarding whatever the model put there, and validates `Finding.lenses` against that
agent's configured lens set.
**No schema exposes `sources`** — a field the model can fill is a field it will fill, and one agent naming
itself twice produces precisely the self-corroboration this rule exists to forbid.
`FinderSchema` omits `verdict` on the same grounds, but the omission is the finder's alone:
`VerifySchema` must carry one, since a verdict per finding is that stage's entire output.

Stamping happens in `find`, not in synthesis.
Deferring it to the synthesis prompt leaves `--no-synthesis` runs carrying model-invented attribution
straight into the report.

Pass the real roster into the synthesis prompt as data rather than letting the model infer it
from the findings themselves.
`{{SOURCES}}` must state which agents ran, which degraded, and which emitted each finding.

### Confidence and drop rules

Implemented in `prompts/synthesis.md`, not in Go.
Synthesis is a full model stage: deciding that two differently-worded findings describe one issue is semantic work,
and matching on file and line in Go would both merge unrelated findings and split identical ones.

- dedupe on `(file, line ±2)` with similar descriptions
- boost: `min(99, max_conf + 10*(N-1))` over distinct sources
- severity: max across sources
- drop: single-source, confidence below 80, no corroboration
- open questions and pre-existing issues are split out **first** and are never boosted, dropped, or verified for fixing

**Degraded runs do not drop.**
With a source dead, corroboration is rarer, so the drop rule starts eating findings the missing source would have confirmed.
When `SourceStatus.Degraded()` is true, route would-be-drops to the verifier instead.
The verifier is the authority anyway; dropping is only a cost optimization.

### Degrade policy

Stall or crash → kill → retry **once** → second failure marks the source `degraded` and the pipeline **continues**.
Never abort the whole run because one agent died — one flaky agent would waste every other agent's work and tokens.

**Every source degrading is the one exception.** A run with zero reporting sources has no review to report,
so it is a tool error and exits `2`, not a clean report with an empty findings list.
See `.claude/rules/config.md` on exit codes.

Every degraded source must be loud in three places or it is effectively hidden:

1. `SourceStatus.degraded` in the JSON output
2. the banner at the top of the markdown report
3. `{{SOURCES}}` in the synthesis prompt

A quietly degraded run that reads like a complete one is the worst failure mode this tool has.

### Stagger

Agent 1 launches immediately; the rest are released once it produces its first output,
or after `stagger_delay` if it never does.

The release signal needs an explicit path, or `stagger_delay` silently becomes the only one.
The leader's `sink` carries a first-activity callback guarded by `sync.Once`, invoked on the first event it
receives and **before** that event is offered to the lossy channel — watching `Events()` instead would be
wrong twice over, since it drops events and has a single reader already.
First activity means any executor event for claude and the first raw stdout write for codex,
so a codex leader still releases the rest without waiting out the full delay.

**The stagger must never influence which agents run, on which models, or in what order.**
Roster composition is a review-quality decision.
It does not group agents, does not reorder them, and does not constrain per-entry `model`.

`max_parallel` caps concurrency independently of the stagger.
Result ordering must stay deterministic regardless of completion order, or reports become diff-noisy between runs.

Name the primitive for what it does: `acquire` takes a slot, `release` gives it back.

### Token accounting

The claude `result` event carries per-model `usage`, so per-agent totals cost nothing to collect.
Record tokens per agent and summed per run, and stop there —
revmux reports what was spent, it does not model or optimize it.

### Event channel

The pipeline is headless and knows nothing about terminals.
`Events()` returns one buffered channel with **exactly one reader**: the active renderer,
either `app/progress.go` or `app/ui`, never both.

- A blocked renderer must never stall the pipeline. Buffer, and drop or coalesce rather than block.
- Every new `EventKind` needs a case in **both** renderers, or it is invisible in one of them.
- Events carry the agent name so the TUI can route them without inferring anything.
- Executor activity reaches the channel through a small unexported adapter satisfying `executor.EventSink`.
  Do not make `Pipeline` itself satisfy that interface — it forces an exported method whose only
  purpose is interface satisfaction, and it collides with the pipeline's own emit path.

**The archive is not a second subscriber on that channel.**
A Go channel distributes, it does not broadcast: two readers would each receive an arbitrary subset,
and the drop-rather-than-block policy makes the split nondeterministic.
Adding a reader to `Events()` would therefore corrupt both the display and `events.jsonl` at once.

`emit` writes to the archive **first and synchronously**, then offers the event to the channel.
That ordering is what makes `events.jsonl` a complete decision record while the display stays droppable:
a dropped event is a cosmetic gap, a missing archive line is a permanently unauditable run.
An archive write that fails is a run failure, not a warning — see `.claude/rules/config.md` on exit codes.

### Executor construction

`find` selects an executor per roster entry, because entries differ in `executor`, `model` and `effort`.
The stages select one too, since `synthesis.md` and `verify.md` carry their own `executor` key.
The pipeline must not import concrete executor types to do that.

Inject a factory on `Config` from `package main`.
**Both the field and the factory's return type must be exported**, or `package main` cannot supply it:
a lowercase field is unsettable from another package, and a function returning an unexported interface
is unnamable and therefore unassignable there.

```go
type Runner interface {                                  // exported: package main names it
    Run(context.Context, executor.Request, executor.EventSink) (executor.Result, error)
}
type RunnerSpec struct{ Executor, Model, Effort string } // shared by AgentSpec and Stage
type Config struct {
    NewRunner func(RunnerSpec) Runner
    ...
}
```

Consumer-side and exported are not in tension — the interface is still declared here, by the consumer;
exporting it only lets the supplier name it.
`RunnerSpec` exists so a stage can select a runner without fabricating a fake roster entry,
which `.claude/rules/prompts.md` forbids.

The archive is the exception to `Config` injection being enough — see the event channel rule above.

### Verifier grouping

One agent per directory, thin directories merged, group count capped from config.

**Never hand the whole findings list to one verifier.**
Materiality is a per-claim judgment, and a verifier holding the full list anchors on the first few,
then rubber-stamps or batch-rejects the rest.
Serial verification is also the review's bottleneck — N parallel verifiers finish in the time of the slowest, not the sum.

Grouping by directory rather than per-finding is deliberate: directory approximates code locality,
so one verifier reads that area once and judges several findings against it.
Per-finding would re-read the same file N times for N findings in it.
