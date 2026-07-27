# Reading what revmux returns

The report goes to **stdout**, JSON by default or markdown with `--markdown`. Nothing else is written
to stdout, so `revmux --task pr-123 > findings.json` is safe with the TUI running.

Prefer JSON when a model consumes it. Use `--markdown` only for output going straight to a human.

**Never `2>&1` into the report file** — stderr is the progress renderer and makes the JSON unparseable.

## The JSON shape

```json
{
  "scope": {"task": "pr-123", "run": "02-after-fix", "scope_path": "/abs/.revmux/tasks/pr-123/scope.md"},
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

Empty lists are arrays, never `null`.

## Check `sources` before reading `findings`

A degraded run looks like a clean one: the findings list is genuinely shorter and nothing in it says why.

```
sources.expected    how many sources the roster called for
sources.reported    how many returned findings
sources.degraded    names of the ones that did not
```

`expected != reported` means partial. Lead with it, name the degraded sources, and treat "no findings"
as inconclusive rather than clean. Offer to re-run.

Every source degrading exits `2` — a tool error, not a clean empty report.

`requested_model` vs `actual_model`: `claude --model` can be silently ignored, so a model pin is a
claim until read back. A mismatch does not invalidate the review but explains a shallow one.

## Findings

| field | meaning |
|---|---|
| `id` | stable within this report |
| `file`, `line`, `end_line` | `end_line` `0` = single line; `line` `0` = file-level |
| `severity` | `critical`, `major`, `minor` |
| `confidence` | 0-100, already filtered by `--min-confidence` |
| `title` | the claim, one line |
| `body` | the argument — trigger and consequence |
| `fix` | the suggested change |
| `sources` | **agent names** that raised it |
| `lenses` | **lens names** it was raised under |
| `verdict` | verify's judgment |

### `sources` and `lenses` are not interchangeable

`sources` holds agent names and is the only input to the cross-source confidence boost. **A source is
a process** — an agent carrying two lenses that flags the same issue under both is still one source.

`lenses` is informational: why it was reported, never how many independently agreed.

Two entries in `sources` is the strongest signal in the report. Two in `lenses` with one in `sources`
is not.

Go stamps `sources` after parsing; no schema exposes it to the model.

### Verdicts

| verdict | how to treat it |
|---|---|
| `confirmed` | act on it |
| `refined` | act on it; the body is the corrected version |
| `rejected` | do not act on it |
| `immaterial` | mention only if asked for everything |
| `pre_existing` | report separately, not this change's responsibility |
| `unverified` | verify was skipped; every finding is unchecked, say so |

`rejected`, `immaterial` and `pre_existing` move out of `findings` into their own top-level lists.

## The other lists

- `open_questions` — questions for the author, not defects. Often where the real problem is.
- `pre_existing` — report as a separate section so the author is not asked to fix unrelated things.
- `immaterial` — usually noise; keep out of a summary unless asked.

## Reporting to a human

1. **Whether the run was complete** — lead with `sources.degraded` if non-empty
2. **Counts by severity**
3. **Each finding**: `file:line`, severity, title, then the body's argument and the fix. Group by
   severity, not by file.
4. **Cross-source corroboration** where `len(sources) > 1`
5. **Open questions**, separately
6. **Pre-existing issues**, separately and flagged out of scope

Do not paraphrase a body down to its title — the body carries the trigger and consequence.

## The run archive

Under `<task-dir>/runs/<run>/`:

```
runs/02-after-fix/
├── manifest.json             roster, prompt provenance + hashes, requested vs actual model, timings
├── prompts/
│   ├── agents/               composed prompt per agent, post-substitution
│   └── stages/               synthesis.md, verify-<group>.md
├── stages/
│   ├── 1-found.json
│   ├── 2-synthesized.json
│   └── 3-verified.json       absent when the stage was skipped
├── events.jsonl              stalls, retries, degrades, stage transitions
├── agents/
│   ├── bugs+impl.jsonl       claude stream-json
│   ├── bugs+impl.retry.jsonl second attempt when one was retried
│   └── codex.log             codex prose
├── report.md
└── findings.json
```

Both `report.md` and `findings.json` are always written, whichever renderer went to stdout. This is
the recovery path when a run's stdout was lost.

| question | file |
|---|---|
| why did this agent report nothing? | `agents/<name>.jsonl` |
| did an agent stall or get retried? | `events.jsonl`, plus a `<name>.retry.jsonl` |
| did synthesis drop something real? | `stages/1-found.json` vs `2-synthesized.json` |
| did verify reject wrongly? | `stages/2-synthesized.json` vs `3-verified.json` |
| what was this agent asked? | `prompts/agents/<name>.md` |
| which lens text, from which layer? | `manifest.json` |

A failed archive write fails the run. The exception is a per-agent tee, which degrades that source.

`--keep-runs` (default 10) prunes old rounds by mtime, so archived reports are not permanent. Pruning
only reads `runs/`; the caller-owned files are never candidates.

## The plain progress renderer

With `--no-tui`, events render as timestamped lines on **stderr**:

```
16:02:11 bugs+impl: started [bugs, impl]
16:02:19 arch+quality: tool: Grep
16:04:02 docs+tests: retrying: agent docs+tests stalled
16:05:12 bugs+impl: done, 6 findings
16:05:40 stage synthesis
```

`tail -f` on this file is what a user watches during a headless run. Read its tail plus
`events.jsonl` to answer a status request; do not guess.
