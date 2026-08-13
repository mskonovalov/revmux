# Changelog

## v0.1.0

First release.

revmux runs a structured multi-agent review by spawning and supervising `claude --print` and `codex exec`
subprocesses, then returns findings on stdout as JSON or markdown. It is normally launched by a coding agent
through the shipped skill rather than typed by hand, and the subject can be a change, a plan, a document or a
filed issue.

**The review**

- Three fixed stages: parallel find across a roster of lens agents, one synthesis call that dedupes on
  `(file, line ±2)` and boosts confidence where distinct sources corroborate, then verification grouped so no
  verifier anchors on a neighbour
- A source is a process, never a lens, so an agent carrying two lenses cannot corroborate itself. revmux
  stamps the attribution itself and no schema exposes it to the model
- Supervision per finder: idle and hard timeouts, one automatic retry, and degrade rather than abort with the
  missing source named in the report

**Configuration**

- Eight shipped profiles and thirteen lenses, resolved per file across `./.revmux/`, `~/.config/revmux/` and
  the defaults built into the binary
- One `model:` string per agent or stage selects binary, model and effort together, so claude and codex mix
  inside one review
- A profile composes any number of agents with any lens sets; adding a file under `prompts/profiles/` or
  `lenses/` is all it takes to have your own

**Output and record**

- JSON on stdout by default, markdown with `--markdown`, and exit codes 0, 1 and 2 that callers script
  against
- Task rounds, with every prior round injected into every composed prompt along with an instruction to judge
  it independently
- A run archive per round: composed prompts, verbatim agent output, per-stage findings, an event log of
  stalls and retries, and a manifest recording prompt provenance and requested against actual model
- Terminal UI with a per-process status table, per-agent scrollback and a findings browser, or timestamped
  stderr lines with `--no-tui`

**Around it**

- Five subcommands, all printing JSON: `config`, `new`, `init`, `stats` and `cleanup`
- The caller ships as an agent skill for Claude Code and Codex CLI, with a launcher that runs the TUI in a
  terminal overlay
- Documentation at [revmux.com](https://revmux.com)
