# revmux

revmux runs a structured multi-agent code review. It spawns and supervises `claude --print` and `codex exec`
subprocesses, then returns findings on stdout as markdown or JSON.

It exists because agent fan-out driven from inside an AI coding session is unobservable and unrecoverable:
agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill, no retry and no
progress display. A subprocess does not make the model faster. What it buys is control: a watchdog that
notices a stall, a kill and retry the caller owns, a live view of every agent, per-agent token counts, and a
run archive to debug a bad review afterwards.

revmux runs a review and returns findings, and does nothing else. It performs no scope detection, no git
operations, no PR fetching and no source modification. All review context is written to a task directory by
the caller and passed in with `--task`.

**Status: initial build in progress.** All three review stages run end to end; the run archive and the
terminal UI are not wired up yet. The build sequence is `docs/plans/20260726-revmux-initial-build.md`.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--task=<id>` | | name of the task directory holding the review context |
| `--run=<name>` | UTC timestamp | name for this round of the review |
| `--lenses=<a,b>` | | lens set replacing the profile roster |
| `--workdir=<dir>` | working directory | directory the review subprocesses run in |
| `--min-confidence=<n>` | `0` | drop findings below this confidence |
| `--no-synthesis` | | skip the synthesis stage |
| `--no-verify` | | skip the verification stage |
| `--no-tui` | | disable the terminal UI |
| `--json` | | write the report as JSON |
| `--preserve-anthropic-api-key` | | pass `ANTHROPIC_API_KEY` to the model CLIs |
| `--config-dir=<dir>` | `~/.config/revmux` | directory holding the config file and the prompt tree |
| `--init` | | write the commented-out config template to `./.revmux/` |
| `--dump-defaults=<dir>` | | extract the embedded prompt tree into a directory |
| `--version` | | show version and exit |

The runtime knobs below also read from the config file, under the same name as the flag:

| Flag | Config key | Default | Description |
|---|---|---|---|
| `--idle-timeout=<d>` | `idle-timeout` | `2m` | kill and retry an agent after this long with no output |
| `--hard-timeout=<d>` | `hard-timeout` | `20m` | kill an agent after this long in total |
| `--stagger-delay=<d>` | `stagger-delay` | `30s` | how long to wait for the first agent before releasing the rest |
| `--max-parallel=<n>` | `max-parallel` | `4` | how many agents run at once |
| `--verify-groups=<n>` | `verify-groups` | `6` | cap on the number of verifier groups |
| `--tasks-dir=<dir>` | `tasks-dir` | `./.revmux/tasks` | root directory holding task directories |
| `--keep-runs=<n>` | `keep-runs` | `10` | how many runs to keep per task |
| `--profile=<name>` | `profile` | `comprehensive` | profile naming the roster to run |

Precedence is the command line, then `./.revmux/config`, then `~/.config/revmux/config`, then the built-in
default. Layers merge per key, so a project config setting one knob leaves the rest alone. The project layer
is auto-detected: no flag selects it, and its absence simply drops it.

## Task directory

Review context reaches revmux only as a task directory the caller has filled, named with `--task`:

```
<tasks-dir>/<task>/
├── scope.md      required, describes what to review
├── goal.md       optional
├── profile.md    optional project profile
├── context/      optional, any number of files
└── runs/<run>/   written by revmux, never anything above it
```

Variables in the prompt tree expand to the **paths** of these files, never their contents — the agent reads
them itself. An absent optional file expands to `none provided`.

## Output

The report goes to **stdout** — markdown by default, the machine shape with `--json`. Everything else goes
to the terminal or to stderr, so `revmux --task pr-123 --json > findings.json` works while the progress
display is running.

Exit codes:

| Code | Meaning |
|---|---|
| `0` | no findings above `--min-confidence` |
| `1` | findings above `--min-confidence` |
| `2` | tool error: bad config, unreadable prompt tree, missing `scope.md`, an unwritable run artifact, or every source degraded |

`1` is a normal outcome, not a failure.

## Development

```
make build    # build .bin/revmux
make test     # race detector plus coverage, mocks excluded
make lint     # golangci-lint
make fmt      # gofmt and goimports
```

## License

MIT
