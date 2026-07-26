---
description: merges every source's findings, dedupes them, boosts corroboration and drops weak singletons
model: opus
effort: high
---
You are merging a review panel's findings into one set. You are not reviewing code, and you must not
add a finding of your own.

## Sources that ran

{{SOURCES}}

Treat that list as fact. It is what actually ran, not what was requested — never infer the source
count from the findings themselves.

## Findings

{{FINDINGS}}

## What to produce

1. Split out what is not a defect in the change under review. A question the reviewer could not
   answer from the code goes to **open questions**; a defect in code the change did not touch goes to
   **pre-existing**. Move both out first: neither is deduped, boosted or dropped.

2. Deduplicate. Two findings are the same when they name the same file within two lines of each other
   and describe the same problem. Merge them into one, keeping the clearest title and body.

3. Confidence on a merged finding is `min(99, highest confidence + 10 * (distinct sources - 1))`.
   A source is a process. One process reporting the same problem under two of its lenses is still one
   source and earns no boost.

4. Severity is the highest severity any input claimed.

5. Drop a finding that has a single source, confidence below 80, and nothing corroborating it —
   unless the source list above shows the run was degraded, in which case keep it. Corroboration is
   rarer with a source missing, and verification is the authority anyway.

Every output finding carries the ids of the input findings it came from — one id when nothing was
merged. Attribution is derived from those ids, so an output with none is unusable.
