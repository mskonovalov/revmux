# Codex CLI integration

The **Codex CLI** skill for revmux. Same workflow as the Claude Code plugin, adapted to Codex's
conventions.

## Contents

- `skills/revmux/SKILL.md` — the review skill
- `skills/revmux/references/` — `task-dir.md`, `invocation.md`, `output.md`
- `skills/revmux/scripts/` — `preflight.sh`, `task-state.sh`, `launch-revmux.sh`, `agentdeck-window.sh`

## Requirements

- `revmux` — `go install github.com/umputun/revmux/app@latest` (installs as `app`, rename it), or
  clone and `make install`
- `claude` — every lens agent and both model stages run on it by default
- `codex` — needed by any roster entry or stage declaring `executor: codex`, which every shipped
  profile does
- `jq` — optional. `preflight.sh` and `task-state.sh` use it when present and fall back without it
- a supported terminal, for overlay mode only: agterm, tmux, Zellij, herdr, kitty, wezterm, cmux,
  ghostty, iTerm2, or Emacs vterm

## Install

Clone the repository:

```bash
git clone https://github.com/umputun/revmux.git
cd revmux
```

Then copy the skill into the Codex skills directory:

```bash
cp -r plugins/codex/skills/revmux ~/.codex/skills/revmux
```

Or symlink it, so `git pull` propagates updates without re-copying:

```bash
ln -s "$PWD/plugins/codex/skills/revmux" ~/.codex/skills/revmux
```

## `/revmux`

```text
/revmux                    review the current change; scope auto-detected
/revmux this branch        branch versus its base
/revmux last 3 commits     a ref range
/revmux focused            codex peer plus the bugs lens only
/revmux final              the pre-merge profile, nothing below major
/revmux lenses docs,impl   a composed lens set
/revmux watch              run with the TUI in a terminal overlay
```

The skill resolves the scope, writes the task directory, runs revmux, reads the JSON back, and
presents the findings. Re-running after fixes is a new `--run` name against the same `--task`; revmux
carries the prior rounds into every prompt itself.

## Differences from the Claude Code plugin

- Script paths resolve through the repo root, falling back to `$CODEX_HOME` (or `~/.codex`), instead
  of `$CLAUDE_SKILL_DIR`
- `AskUserQuestion` is replaced by numbered-list prompts, the Codex convention
- `EnterPlanMode` is replaced by an inline markdown plan plus an explicit confirmation before any
  file is modified

Everything else — the task directory format, the flags, the JSON shape, the exit codes, the overlay
launcher and every reference file — is identical between the two.

## Notes

This integration is kept separate from other harnesses on purpose:

- Claude Code integration lives in `.claude-plugin/`
- Codex integration lives here in `plugins/codex/`
