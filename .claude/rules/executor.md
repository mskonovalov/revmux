---
paths:
  - "app/executor/**"
---

## Executor — supervised subprocesses

Every rule below was learned by running these CLIs under supervision in anger.
They read like trivia until one of them bites, and each one costs a debugging session to rediscover.

### Verified `claude` CLI behavior

Measured directly against the real CLI. Do not re-derive, and do not assume any of it from documentation.

- `--json-schema` works together with `--output-format stream-json`.
  The model is forced through a `StructuredOutput` tool call,
  and the terminal `result` event carries a **pre-parsed `structured_output` object** alongside the raw `result` string.
  Read `structured_output`. Never scrape JSON out of prose, and never parse the `result` string when the object is present.
- The stream carries a typed `rate_limit_event`:
  `{"type":"rate_limit_event","rate_limit_info":{"status","resetsAt","rateLimitType","overageStatus"}}`.
  Use it. It is strictly better than matching error strings.
- The `result` event also carries per-model `usage`, `ttft_ms` and `permission_denials`.
  Read the token counts off `usage` — never estimate or recompute them.
- **`--model` can be silently ignored.**
  A run with `--model haiku` actually executed `claude-sonnet-4-6`.
  Always read the model back from `modelUsage` in the result event and report what actually ran.
  A roster full of per-agent model pins is a claim, not a fact, until it is verified this way.
- Even a trivial call carries substantial input tokens — system prompt, project instructions, skills listing.
  Worth reporting per agent, which is all revmux does with it.

### Flags every claude invocation carries

```
claude --print --output-format stream-json --verbose
       --model <m> --effort <e>
       --permission-mode dontAsk
       --disallowedTools "Edit,Write,NotebookEdit"
       --disable-slash-commands
       --no-session-persistence
       --json-schema <findings schema>
       < prompt
```

- `--permission-mode dontAsk` so a headless run never blocks on a permission prompt.
- `--disallowedTools` removes the edit tools from the agent's context — revmux never modifies source.
  This is best-effort, not a sandbox; the prompt must state the constraint too.
- `--disable-slash-commands` prevents a lens agent invoking a skill that spawns its own subagent,
  which would put an agent inside the agent.
  The call site cannot prevent that any other way — it is a property of the invoked skill, not of the caller.
- `--no-session-persistence` avoids leaving one saved transcript per lens per run.
- The prompt goes in on **stdin**, never as an argv positional.
  Windows `cmd.exe` caps a command line at 8191 characters and a composed lens prompt blows past that instantly.

### Child environment

- **Always strip `CLAUDECODE` from the child environment.**
  revmux is often launched from inside an AI coding session, so the variable is inherited
  and the child refuses to start as a nested session.
  There is no case where passing it through is correct.
- Strip `ANTHROPIC_API_KEY` by default so the child uses the interactive subscription auth rather than per-token billing.
  Expose a `--preserve-anthropic-api-key` escape hatch for users who authenticate by API key.
- Never pass `--bare`. It forces API-key auth and skips project-instruction discovery,
  which changes billing and strips the project context every lens agent depends on.

### Idle timeout and hard timeout

Two independent timers catching two different failures.

- **Idle timeout** is the one that matters: derive a cancellable context, arm `time.AfterFunc(idleTimeout, cancel)`,
  and reset it on **every output line** through a touch closure passed into the stream parser.
  When the derived context is canceled but the parent context is alive, that is an idle timeout, not an error —
  set `Result.IdleTimedOut` so the caller can retry rather than fail the run.
- **Hard timeout** is a plain `context.WithTimeout` over the whole call,
  catching the slow-but-alive case where the agent keeps emitting output forever.
- Both default to disabled at the executor level; the composition root sets them from config when it builds
  each executor. Not the pipeline — it never constructs one, it receives an injected factory.
- **Both timers come from an injected clock, never from `time.AfterFunc` directly.**
  `.claude/rules/testing.md` forbids wall-clock waits in tests, and an idle-timeout test that actually sleeps
  is either slow or flaky. A recorded fixture that simply ends is EOF, not a stall — proving the watchdog
  fires needs a fake runner that emits fixture bytes and then blocks until cancellation, plus a clock the
  test advances itself.

### Process groups

- Set `SysProcAttr{Setsid: true}` **before** `cmd.Start()`.
  `Setsid` (not just `Setpgid`) fully detaches the child from the controlling terminal,
  preventing SIGTTIN/SIGTTOU from stopping the child's process group when a descendant touches terminal I/O.
- Kill the whole **process group** (`-pid`), SIGTERM first, then SIGKILL after a short grace delay.
  Killing only the direct child leaves node subagents and MCP servers orphaned, and they accumulate across a run.
- Kill on normal exit too, not only on cancellation — that is what reaps the orphans.
- Return early on `ESRCH` from SIGTERM so a normal exit does not pay the grace sleep on every call.
- Guard both the wait and the kill with `sync.Once`; the wait must be safe to call repeatedly.

### Shared base, thin executors

`Claude` and `Codex` both need the same run loop, idle watchdog, process-group teardown and line reader.
Duplicating that gives two near-identical `Run` bodies, which `dupl` will fail in lint.

Put the shared machinery on an unexported `proc` struct that both embed.
Each executor supplies only its own `args()` and its own output parsing.
Model and effort belong on the **per-run request**, not on construction-time options —
a single executor instance has to serve roster entries with different models.

### Raw output belongs to the caller, not just the parser

`Request` carries an optional `RawOutput io.Writer`, and `proc` tees every byte to it **before** parsing.

Without this the archive cannot do its job. Raw stdout is consumed inside `proc` and the per-executor
parsers, so a caller holding only parsed events can never reconstruct byte-identical claude stream-json or
codex prose. Re-serializing parsed events is not the same artifact: a reflection agent reading a paraphrase
of what the model emitted is worse off than one with no data, because it cannot tell the difference.

Tee before parse, not after — a stream that fails to parse is exactly the one worth having on disk.

### Codex differences

Codex is a peer executor, not a special case in the pipeline — but the executor itself is genuinely different.

- **Codex has no `stream-json` equivalent.**
  Assistant text and tool dispatch land only in its session rollout file, whose path derives from a session id
  printed in the stderr header banner.
  If per-agent activity for codex is ever wanted in the TUI, tail that rollout — do not try to parse stderr prose.
- **Codex has no `--json-schema`.**
  The executor appends its own "return only JSON matching this shape" contract to the composed prompt,
  rendering `Request.Schema` inline. That field is set for **both** executors and carries the running
  stage's schema, so a codex entry running synthesis or verify asks for that stage's shape.
  Hardcoding a finder-shaped contract here breaks the moment a stage prompt declares `executor: codex`.
  The wrapper text lives in the executor, never in a lens file, which must stay executor-agnostic.
- The idle watchdog ticks on raw stdout writes rather than parsed events.
- Extraction must tolerate JSON wrapped in surrounding prose; finding no JSON is a degraded source, not a crash.
- Codex stderr is noisy — startup banner, exec echo, hook lifecycle lines, reasoning stream.
  Forward at most the resolved `model:` / `sandbox:` / `reasoning effort:` header lines, once per process, and suppress the rest.
- Plan-quota errors arrive on **stderr with an empty stdout**, so a stdout-only error check misses them entirely.
- `--sandbox read-only` always. revmux never lets an agent write.

### Error and limit patterns

claude gets its rate-limit signal from the typed `rate_limit_event`, so string matching is only needed for codex.
Where patterns are used, tier them: **retry → limit → error**.

- Retry tier covers transient server hiccups: `API Error: 529`, `502`, `503`, `504`.
  `500` is deliberately excluded — it can be a deterministic failure and belongs in the error tier.
- **Never match patterns against the whole output.**
  A review agent's findings text will literally contain the words "rate limit" and "API Error"
  when it is reviewing code that handles rate limits — including this project's own code.
  Check only the tail, and only when the process exited non-zero.
- Skip pattern checks entirely when the context was canceled — a canceled run's tail is meaningless.

### Cross-platform

- Platform-specific code goes in `foo_unix.go` (`//go:build !windows`) and `foo_windows.go` (`//go:build windows`).
- Methods split across build-tagged files are fine, so platform code does not force standalone helpers.
- Windows has no process groups; the stub kills the direct process only, and that is an accepted limitation.
- Verify with `GOOS=windows GOARCH=amd64 go build ./...` before shipping anything touching this package.

### Capturing real streams for fixtures

To learn what the upstream CLIs actually emit, do not launch `claude` or `codex` from an AI agent's own tool shell —
nested launches are commonly blocked by the host tool's permission layer, and the capture silently fails.
Run the capture in a separate, independent terminal session, redirect stdout and stderr to files, and inspect those.
Recorded captures belong in `app/executor/testdata/` as fixtures; see `.claude/rules/testing.md`.
