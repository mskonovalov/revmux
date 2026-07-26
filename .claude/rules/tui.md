---
paths:
  - "app/ui/**"
---

## TUI — bubbletea model

Built on bubbletea, bubbles and lipgloss.
A single `Model` with state grouped into sub-structs (`cfg`, `layout`, `agents`, `view`, `findings`),
methods split across files by concern, each source file with a matching `_test.go`.

### This package does no OS work. Ever.

No `exec.Command`, no file reads or writes, no network, no git, no writes to stdout — not even in a small helper.
The pipeline runs headless and emits typed events; the TUI is one subscriber and the plain progress renderer is another.

The cost of a new package is lower than keeping `app/ui` entangled with OS boundaries.
When the TUI appears to need OS work, extract it and consume it through a consumer-side interface.

**The final report is not written from here.**
The model carries it out through its final state; `package main` writes it after the bubbletea program returns.
That is also what makes "the report is emitted exactly once on quit" testable without a terminal.

### Output streams

Render to the **tty**, never to stdout. stdout belongs to the report alone.

Gate the TUI on the tty being openable, **never** on stdout being a TTY.
With `revmux --json > findings.json` the stdout check is false while the user is sitting at a terminal
expecting to watch the run — that check would silently disable the TUI in one of the most common invocations.

`--no-tui` and a non-openable tty both fall back to the plain stderr renderer over the same event channel.

### Layout

A status table on top, one row per agent: name, state, elapsed, last activity.
Below it, one focused detail pane, switched with tab and number keys.

- Tab `0 · all` is the combined chronological view and is **focused by default**.
  It is deliberately compact: tool calls, state transitions and findings emitted, one line each, agent-prefixed and colored.
  It must NOT carry thinking text — four concurrent agents make it scroll faster than anyone can read,
  and it stops being the situational-awareness view it exists to be.
- Tabs `1-9` are per-agent full-detail scrollback, thinking included. Those are the forensic views.
- On completion the model switches to the findings browser; agent tabs stay reachable
  so a reader can check *why* a finding was raised.

### lipgloss and ANSI traps

- `lipgloss.Render()` emits a full reset (`\033[0m`), which kills an enclosing style's background.
  For a styled substring inside a lipgloss container — a status separator, an agent-name prefix, a severity chip —
  emit raw ANSI sequences instead.
  Never call `lipgloss.NewStyle().Render()` for an inline element inside a lipgloss-rendered parent.
- Pane rendering and viewport padding emit plain spaces after a reset, so themed panes show the terminal's
  default background in the gaps. Pad lines to full width before assembly.
- A factory returning a typed nil pointer through an interface return type produces a non-nil interface.
  Guard explicitly: `if x == nil { return nil }`.

### Events

- Events carry the agent name; route on that, never infer an owner from message content.
- A new `EventKind` needs a case here **and** in the plain renderer, or it is invisible in one of them.
- The model must tolerate events for an agent it has not seen yet and events arriving after an agent finished.
  Ordering across concurrent agents is not guaranteed.
- Never block on the event channel inside `Update` — a slow render must not stall the pipeline.

### Receivers

Keep receivers consistent per type.
A value receiver on a state sub-struct copies cursor and filter state on every render, which is both
wasteful and a source of "why did my mutation vanish" bugs.
Mutating and reading methods on the same state struct should both take pointers.

### Testing

Drive `Update` with synthetic messages and assert on `View()` output.
Never drive a real terminal, never spawn a process, never require a tty in a test.
