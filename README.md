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

**Status: initial build in progress.** Nothing below the build commands is usable yet. The build sequence is
`docs/plans/20260726-revmux-initial-build.md`.

## Development

```
make build    # build .bin/revmux
make test     # race detector plus coverage, mocks excluded
make lint     # golangci-lint
make fmt      # gofmt and goimports
```

## License

MIT
