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

The I/O plumbing is held off it the same way: `app/pipeline/artifacts.go` owns the whole-artifact writers
(`save`, `saveStage`, the sticky `fail`) and the artifact path constants.
`events.jsonl` is the deliberate exception and stays in `pipeline.go` beside `emit` — it is a stream held
open across the whole run under the mutex guarding it, not a whole-file write.

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
`AgentSpec.Name`, discarding whatever the model put there, and filters `Finding.lenses` down to the lenses
that agent actually carries — a model naming one it was never given is informational noise, and an empty
result falls back to the agent's full set, which raised the finding by definition.
**No schema exposes `sources`** — a field the model can fill is a field it will fill, and one agent naming
itself twice produces precisely the self-corroboration this rule exists to forbid.
`FinderSchema` omits `verdict` on the same grounds, but the omission is the finder's alone:
`VerifySchema` must carry one, since a verdict per finding is that stage's entire output.

**`parse` rewrites `Finding.id` in the same loop, to `<agent>-<n>`.**
The schema is one shape shared by every finder, so four agents on it each emit an id starting at `1`.
Synthesis derives each merged finding's sources union from the input ids it merged, so colliding ids do not
just look untidy — they make one agent's finding indistinguishable from another's and corrupt the source
count the whole confidence boost rests on.

Stamping happens in `find`, not in synthesis.
Deferring it to the synthesis prompt leaves `--no-synthesis` runs carrying model-invented attribution
straight into the report.

**Synthesis re-derives attribution rather than carrying it through, and `merged_ids` is how.**
`SynthesisSchema` exposes no `sources` either, for the same reason `FinderSchema` does not.
Each synthesized finding instead returns the input ids it merged, and `synthesizer.attribute` unions the
`sources` and `lenses` of those inputs, discarding whatever the model put in either field.
A merged id that is not an input is a **hard error**, never a skip: it means the model invented one, and
dropping it quietly yields a finding credited with fewer sources than it earned.

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

**A failing verifier degrades its own group, never the stage.**
Find already paid for the findings and synthesis already merged them, so a dead, unparseable or empty
verifier returns its group `unverified` — the same honest value `--no-verify` produces — and emits
`EventAgentDegraded` naming the group. A verdict outside the enum is treated as unverified too, since the
codex path has no `--json-schema` to enforce the vocabulary.
The one thing that does fail the stage is a prompt tree that will not compose: every group would hit it
identically, and `.claude/rules/prompts.md` requires an unresolved variable to be loud.
`verifyGroup` therefore carries its composed prompt, so every group is composed before any is dispatched
and that config error surfaces in one place.

Every degraded source must be loud in three places or it is effectively hidden:

1. `SourceStatus.degraded` in the JSON output
2. the banner at the top of the markdown report
3. `{{SOURCES}}` in the synthesis prompt

A quietly degraded run that reads like a complete one is the worst failure mode this tool has.

### Stagger

Agent 1 launches immediately; the rest are released once it produces its first output,
or after `stagger-delay` if it never does.

The release signal needs an explicit path, or `stagger-delay` silently becomes the only one.
The leader's `sink` carries a first-activity callback guarded by `sync.Once`, invoked on the first event it
receives and **before** that event is offered to the lossy channel — watching `Events()` instead would be
wrong twice over, since it drops events and has a single reader already.
First activity means any executor event for claude and the first raw stdout write for codex,
so a codex leader still releases the rest without waiting out the full delay.

**The gate latches open and never re-arms, and `runFind` latches it on completion.**
One `stagger` instance serves both find and verify, taken from `Pipeline` rather than constructed per stage:
a fresh instance would charge verify another `stagger-delay` to re-prove the auth find already proved.
That reuse only works because the gate latches, and it needs the third release path as well as the first two.
A single-agent roster never waits on the gate — its only agent is index 0, the leader — so nothing calls
`leaderStarted` and the gate would stay shut until the delay elapsed, leaving verify's groups blocked on a
leader that had already finished. Find running to completion is the stronger proof anyway: at least one
process finished, or the run had already failed with `errNoSources`.

**The stagger must never influence which agents run, on which models, or in what order.**
Roster composition is a review-quality decision.
It does not group agents, does not reorder them, and does not constrain per-entry `model`.

`max-parallel` caps concurrency independently of the stagger.
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
