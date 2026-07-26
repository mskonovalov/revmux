---
paths:
  - "app/prompt/**"
---

## Prompts — profiles, lenses and composition

Everything that shapes a review lives in markdown; the INI config holds only runtime knobs.
The two must not overlap: a reviewer should be able to copy the prompt tree to another machine
and get the same review, and change a timeout without touching a prompt.

### Layout

```
prompts/profiles/comprehensive.md   focused.md   final.md
prompts/synthesis.md   prompts/verify.md
lenses/bugs.md  impl.md  architecture.md  quality.md  docs.md  tests.md  adversarial.md
```

Precedence, per file: CLI flag > `./.revmux/` > `~/.config/revmux/` > `go:embed` defaults.
Per-**file** fallback, not per-directory — overriding one lens must not orphan the other six.

`.md`, not `.txt`: these carry YAML front matter, so editors fold the metadata and highlight the body.

### Profiles declare the roster

A profile is roster front matter plus a body that is the shared preamble and severity bar.

```yaml
---
model: opus
effort: high
agents:
  - {name: bugs+impl,    lenses: [bugs, impl]}
  - {name: arch+quality, lenses: [architecture, quality]}
  - {name: docs+tests,   lenses: [docs, tests]}
  - {name: codex, executor: codex, lenses: [adversarial],
     model: gpt-5.6-sol, effort: xhigh}
---
```

Top-level `model` and `effort` are defaults; per-entry values override them.
`synthesis.md` and `verify.md` carry the same three keys in their own front matter —
they are different jobs from a finder and want their own model and effort.

`--lenses bugs,impl` overrides a profile's roster while keeping its body.
That is the whole mechanism for a caller composing a lens set per change — no extra concept needed.

Profiles and stage prompts share a parsed shape but not an interface:
a stage prompt has no roster, so it must not expose a roster method,
and composing a stage prompt must not require fabricating a fake roster entry.

### Executor and lens are orthogonal

`executor` accepts only `claude` (default when omitted) and `codex`.
Anything else is a **load-time** error with a clear message, never a runtime surprise.

There is no codex-specific prompt file and no per-entry prompt-path override.
Codex is an entry with `executor: codex` composing `lenses/adversarial.md`.
Consequences worth preserving: the adversarial lens can run on claude by changing one word,
and the `bugs` lens can run on codex.

Lens text must stay executor-agnostic.
The output-contract difference — claude has `--json-schema`, codex does not — is injected by the executor.
Never write "return JSON shaped like…" into a lens file.

Keep the vocabulary singular: the front-matter key is `executor`, so do not call the same concept
a "runner" in one layer and an "executor" in another.

### Composition

One agent's prompt = profile body + each of its lens files, concatenated, with `{{VAR}}` substituted.

Variables: `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}`,
plus `{{FINDINGS}}` and `{{SOURCES}}` for the synthesis and verify stages.

**Context variables expand to absolute paths, never to file contents.**
`{{SCOPE}}`, `{{GOAL}}` and `{{PROFILE}}` become paths to files in the task directory,
`{{CONTEXT}}` becomes the path to its `context/` directory, and the profile body instructs agents to read them.
revmux therefore only ever stats those files — it never opens one, so there is no size guard,
no encoding handling and no way for a large scope to bloat a prompt.

### Prior rounds are injected, not a variable

revmux already wrote the task's earlier rounds under `runs/`, so it hands them to every process rather than
making each caller copy them forward. This does not weaken "revmux never derives context":
it surfaces what revmux itself produced, under a path it owns, and never reads a repository to do it.

**This is an injection, not a `{{VAR}}`.**
A variable is opt-in per file — any lens or profile omitting it silently loses the history, including
user-overridden files written before the feature existed.
The composer appends the block to every composed prompt instead, the same way the codex executor appends its
own output contract rather than trusting a lens to carry it.
The vocabulary therefore stays closed at the variables listed above.

A bare directory path tells an agent nothing about whether the contents are worth opening, so the block is
the path plus a generated one-line inventory per round — name, when it ran, finding counts by severity, and
which sources degraded:

```
Prior rounds for this task: /abs/.revmux/tasks/pr-123/runs/
  round-1    2026-07-26T14:30Z   8 findings (1 critical, 3 major, 4 minor)   sources 4/4
  after-fix  2026-07-26T16:02Z   2 findings (0 critical, 1 major, 1 minor)   sources 3/4, docs+tests degraded
Each round holds report.md (rendered) and findings.json (machine shape). Read the rounds you judge relevant.

Re-evaluate everything independently. A prior round reporting an issue is not evidence that it is real,
and a prior round missing one is not evidence that it is absent.
```

The inventory is metadata revmux computes, not review content lifted off disk — findings stay in the files.
On a first round the block is omitted entirely rather than injected empty.

Finders, synthesis and verify all receive it.
**The independence instruction is part of the injected block, never left to the profile body.**
An agent told a prior round flagged something at a location tends to confirm it rather than judge it,
which is the same anchoring failure that makes codex a peer rather than a second pass.
Injecting the data without the guard, or letting an overridden profile drop the guard, reintroduces exactly
the dependence the cross-source boost assumes is absent.

- An unresolved `{{VAR}}` left in a composed prompt is a bug, not a warning. Fail loudly.
- A missing variable resolves to an explicit placeholder ("none provided"), never to a path that does not exist —
  an agent whose `Read` fails cannot tell absence from a broken run.
- The vocabulary is closed: `SCOPE`, `GOAL`, `PROFILE`, `CONTEXT`, `WORKDIR` and the two stage variables.
  A lens naming anything else is a load-time error, which is what makes a typo'd variable loud rather than silent.
  Arbitrary extra material goes in `context/`, which is why that one is a directory.
- Shared text belongs in the profile body, never duplicated across lenses.
  If two lenses say the same thing, it is preamble.
- Composition needs the profile body, so it hangs off the profile, not off a bare roster entry.

### Never embed content

Prompts carry paths, refs and instructions. The agent fetches the diff and reads the files itself.
Embedding a diff makes prompts enormous and slows every launch.
Because every context variable is a path, this rule needs no judgment call —
there is no per-variable decision about what is small enough to inline.

### Shipping defaults

The `config` file installs fully commented out, so a file containing only comments can be safely
overwritten on upgrade while any uncommented line marks it customized and preserves it.

Prompt and lens markdown is content, not settings, so it ships live.
Overriding means copying the file and editing it; the embedded version stays the fallback for every file not copied.
Deleting a lens file on disk does not disable it — the embedded one is used.
To actually drop a lens, remove it from the profile roster.

### Validation at load

- every lens named by a roster entry exists
- `executor` is `claude` or `codex`
- `effort` is one of `low`, `medium`, `high`, `xhigh`, `max`
- no duplicate agent names in one roster
- front matter parses, and a profile with no `agents` is an error rather than an empty run

Invalid values are rejected, never silently defaulted.
A typo'd model quietly changing which model reviews your code is worse than a startup error.
