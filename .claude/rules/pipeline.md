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
Scope arrives as a `{{SCOPE}}` text blob the caller composed; agents run their own diff commands.
A change that makes the pipeline read a repository is a change that belongs in the caller.
This is the hardest boundary in the project and it is what keeps revmux reusable and testable.

### Source counting

**A source is a process.**
The cross-source confidence boost counts distinct processes, never tags and never lenses.
An agent carrying two lenses that flags the same issue under both is still **one** source — it cannot corroborate itself.

The pipeline knows which process emitted which finding, so the count is structurally correct.
Preserve that: pass the real roster into the synthesis prompt as data rather than letting the model infer it
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

Every degraded source must be loud in three places or it is effectively hidden:

1. `SourceStatus.degraded` in the JSON output
2. the banner at the top of the markdown report
3. `{{SOURCES}}` in the synthesis prompt

A quietly degraded run that reads like a complete one is the worst failure mode this tool has.

### Stagger

Agent 1 launches immediately; the rest are released once it produces its first stream event,
or after `stagger_delay` if it never does.

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
It emits typed events on a buffered channel; `app/progress.go` and `app/ui` are both just subscribers.

- A blocked subscriber must never stall the pipeline. Buffer, and drop or coalesce rather than block.
- Every new `EventKind` needs a case in **both** renderers, or it is invisible in one of them.
- Events carry the agent name so the TUI can route them without inferring anything.
- Executor activity reaches the channel through a small unexported adapter satisfying `executor.EventSink`.
  Do not make `Pipeline` itself satisfy that interface — it forces an exported method whose only
  purpose is interface satisfaction, and it collides with the pipeline's own emit path.

### Executor construction

`find` selects an executor per roster entry, because entries differ in `executor`, `model` and `effort`.
The pipeline must not import concrete executor types to do that.
Inject a factory on `Config` (`newRunner func(prompt.AgentSpec) agentRunner`) from `package main`,
keeping the interface consumer-side while letting `find` choose per entry.

### Verifier grouping

One agent per directory, thin directories merged, group count capped from config.

**Never hand the whole findings list to one verifier.**
Materiality is a per-claim judgment, and a verifier holding the full list anchors on the first few,
then rubber-stamps or batch-rejects the rest.
Serial verification is also the review's bottleneck — N parallel verifiers finish in the time of the slowest, not the sum.

Grouping by directory rather than per-finding is deliberate: directory approximates code locality,
so one verifier reads that area once and judges several findings against it.
Per-finding would re-read the same file N times for N findings in it.
