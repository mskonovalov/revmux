---
worth: later
where: .claude-plugin/skills/revmux/SKILL.md
added: 2026-08-09
---
# SKILL.md is long enough that its later steps vanish after a compaction

Auto-compaction re-attaches only the **first 5,000 tokens** of each invoked skill, under a 25,000-token
budget shared across every skill invoked in the session — documented in Claude Code's skills reference
under "Skill content lifecycle", alongside the guidance to keep a SKILL.md under 500 lines.

Both trees are well past that:

```
SKILL.md          746 lines, ~46 KB   (~11.6k tokens)
re-attach keeps   first ~5k tokens    (~line 290)
Step 5 onward     below that line
```

So in any session that compacts, the agent finishes the review against a copy of its own instructions
that stops partway through Step 4. Nothing fails loudly; it improvises, and the improvisation looks like
a plausible review turn.

This is latent rather than observed. The presentation defect that surfaced it (a triage that buried the
verdict) was **not** caused by it — that session, `0945cab2` under
`~/.claude/projects/-Users-umputun-dev-umputun-ralphex/`, had zero compact boundaries and peaked at 156k
on a 1M window, so every rule was in context verbatim.

Partly mitigated already: presentation moved into `references/present.md` in 0.3.3, which is read at the
moment of use and therefore unaffected by the ceiling. The remaining exposure is everything else below
~line 290 — Step 5's reading rules, Step 6's outcome table, Step 7, the self mode.

The fix is the same shape: move the steps that matter at a specific moment into references the agent
reads then, and leave SKILL.md as dispatch plus the non-negotiables. Deferred because it is a rewrite of
the whole file across two trees, and because nothing has been observed failing this way yet.
