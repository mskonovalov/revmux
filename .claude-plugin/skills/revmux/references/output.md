# Reading what revmux returns

The report goes to **stdout**, as JSON by default or markdown with `--markdown`. Nothing else is ever
written to stdout — the TUI renders to the tty, progress lines go to stderr — so
`revmux --task pr-123 > findings.json` is safe with the display running.

Prefer JSON when a model is consuming the result. Use `--markdown` only when the output is going
straight to a human without being parsed.

## The JSON shape

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

Empty lists are emitted as arrays rather than `null`, so indexing into them needs no nil check.

## Check `sources` before reading `findings`

**A degraded run can look like a clean one.** If two of four sources died, the findings list is
genuinely shorter — and nothing about the list itself says why.

```
sources.expected    how many sources the roster called for
sources.reported    how many actually returned findings
sources.degraded    names of the ones that did not
```

`expected != reported` means the review is partial. Say so when reporting to the user, name the
degraded sources, and treat "no findings" from a half-degraded run as "inconclusive", never as "the
code is clean". Offer to re-run rather than presenting a thin result as a verdict.

A run where *every* source degraded exits `2` — that is a tool error, not a clean empty report.

`agents[].requested_model` versus `agents[].actual_model` is worth a glance: `claude --model` can be
silently ignored, so a roster's model pin is a claim until it is read back. A mismatch does not
invalidate the review, but it explains a review that reads shallower than expected.

## Findings

| field | meaning |
|---|---|
| `id` | stable within this report; use it when referring to a finding |
| `file`, `line`, `end_line` | `line` is the anchor. `end_line` `0` means a single line; `line` `0` means a file-level finding that renders as the bare path |
| `severity` | `critical`, `major`, `minor` |
| `confidence` | 0-100. `--min-confidence` has already filtered on this |
| `title` | one line, the claim |
| `body` | the argument — why this is a defect, with the trigger and the consequence |
| `fix` | the suggested change |
| `sources` | **agent names** that raised it |
| `lenses` | **lens names** it was raised under |
| `verdict` | the verify stage's judgment |

### `sources` and `lenses` are not interchangeable

`sources` holds agent names — `["bugs+impl", "codex"]` — and is the only input to the cross-source
confidence boost. **A source is a process.** An agent carrying two lenses that flags the same issue
under both is still one source; it cannot corroborate itself.

`lenses` holds the lens names that raised it — `["bugs", "adversarial"]` — and is informational. It
answers "why was this reported", never "how many independently agreed".

Two entries in `sources` means two separate processes found the same thing, which is the strongest
signal in the report. Two entries in `lenses` with one entry in `sources` means one agent noticed it
twice, which is not.

Go stamps `sources` after parsing and no schema exposes it to the model, so it cannot be inflated by
an agent naming itself twice.

### Verdicts

| verdict | meaning | how to treat it |
|---|---|---|
| `confirmed` | checked against the code and stands as written | act on it |
| `refined` | real, but the description was adjusted | act on it; the body is the corrected version |
| `rejected` | checked and found not to be a defect | do not act on it |
| `immaterial` | technically true, no real consequence | mention only if asked for everything |
| `pre_existing` | real but not introduced by this change | report separately, do not fold into the change's findings |
| `unverified` | the verify stage was skipped (`--no-verify`) | every finding is a claim nobody checked; say so |

`rejected`, `immaterial` and `pre_existing` findings are moved out of `findings` into their own
top-level lists, so `findings` holds what survived.

## The other lists

- `open_questions` — things a reviewer could not resolve from the code alone. These are questions for
  the author, not defects. Surfacing them is usually valuable; they are often where the real problem
  turns out to be.
- `pre_existing` — real issues the change did not introduce. Worth reporting as a separate section so
  the author is not asked to fix unrelated things as a condition of this change.
- `immaterial` — true but inconsequential. Usually noise; keep them out of a summary unless asked.

## Reporting to a human

A useful summary, in order:

1. **Whether the run was complete** — if `sources.degraded` is non-empty, lead with it.
2. **Counts by severity**, from `findings`.
3. **Each finding**: `file:line`, severity, title, then the body's argument and the fix. Group by
   severity, not by file — a reader triages on severity.
4. **Cross-source corroboration** where `len(sources) > 1`; it is the strongest signal available.
5. **Open questions**, separately.
6. **Pre-existing issues**, separately and explicitly flagged as out of scope for this change.

Do not paraphrase a finding's body down to its title. The body carries the trigger and the
consequence, which is what makes it actionable.

## The run archive

Every run writes artifacts under `<task-dir>/runs/<run>/`. They exist so a review can be audited
without re-running it.

```
runs/after-fix/
├── manifest.json             roster, prompt provenance + content hashes, requested vs actual model, timings
├── prompts/
│   ├── agents/               composed prompt per agent, post-substitution — the bytes the model saw
│   └── stages/               synthesis.md, verify-<group>.md
├── stages/
│   ├── 1-found.json          findings as the find stage left them
│   ├── 2-synthesized.json
│   └── 3-verified.json       absent when the stage was skipped
├── events.jsonl              revmux's own decisions: stalls, retries, degrades, stage transitions
├── agents/                   verbatim tees
│   ├── bugs+impl.jsonl       claude stream-json
│   ├── bugs+impl.retry.jsonl a retried agent keeps both attempts
│   └── codex.log             codex prose
├── report.md                 the filtered report, byte for byte what the caller was shown
└── findings.json
```

Reach for these when a review looks wrong:

| question | file |
|---|---|
| why did this agent report nothing? | `agents/<name>.jsonl` — the verbatim stream |
| did an agent stall or get retried? | `events.jsonl`, and the presence of `<name>.retry.jsonl` |
| did synthesis drop something real? | diff `stages/1-found.json` against `stages/2-synthesized.json` |
| did verify reject something it should not have? | `stages/2-synthesized.json` against `3-verified.json` |
| what exactly was this agent asked? | `prompts/agents/<name>.md` — post-substitution, the real bytes |
| which lens text was used, and from which layer? | `manifest.json` prompt provenance and content hashes |
| did the model pin actually take? | `manifest.json` requested versus actual model |

`manifest.json` records which of the three precedence layers supplied each prompt file and its content
hash, because two rounds of one task can use different lens text.

A failed archive write fails the run — a report emitted next to a half-written archive reads as
complete, and the gap only surfaces when someone tries to audit it. The one exception is a per-agent
tee under `agents/`, which degrades that single source instead and is reported like any other agent
failure.

## The plain progress renderer

With `--no-tui`, events render as timestamped lines on **stderr**:

```
16:02:11 bugs+impl: started [bugs, impl]
16:02:19 arch+quality: tool: Grep
16:04:02 docs+tests: retrying: agent docs+tests stalled
16:05:12 bugs+impl: done, 6 findings
16:05:40 stage synthesis
```

Worth reading after a run that produced a surprising result — `retrying:` and `degraded` lines here
explain a thin report before the archive has to be opened.
