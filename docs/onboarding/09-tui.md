# 09 — The TUI (and plain mode)

Package: `cmd/agentui/`. Framework: Bubble Tea v2 (`charm.land/bubbletea/v2`).

## Mode selection

`agentui.Run` checks whether stdin is a terminal. Terminal → full Bubble Tea
program (alt screen). Not a terminal (pipes, tests) → `plain.go`: a line-based
loop that prints tool events to **stderr** and the response to **stdout**, so
the streams stay separable.

## The model (`model.go`)

One `model` struct owns everything: the chat viewport, `chatLog` (transcript),
textarea input, spinner, keymap, prompt history navigation, approval panel
state, log panel, and the in-flight send state (`sending`, `cancelSend`,
`stopping`).

Update loop highlights:

- **Submit** (`submit`): slash commands are intercepted first
  (`runCommand`); otherwise the message is appended, `session.Send` runs in a
  `tea.Cmd` goroutine with a cancellable context, and history is persisted.
- **Two-stage ctrl+c** (`handleInterrupt`): with work in flight (or approvals
  pending) it cancels/denies and stays open; only an idle ctrl+c quits.
- **Responses** (`updateResponse`): a streamed assistant message is finalized
  in place; errors and user stops render as distinct lines; the tool-outcome
  guard is appended to the text.
- **Scroll behavior**: the viewport auto-follows the bottom until the user
  scrolls up (`userScrolled`); End/paging back to the bottom resumes following.

## Tool blocks (`chat_log.go`, `render.go`, `tool_icons.go`)

Tool events mutate a single block in place, keyed by tool-call ID: spinner +
elapsed while running → ✔/✘ + duration + bytes (or `[code]`/`[error]`) when
finished. `INVOKR_TOOL_PROGRESS` / `/progress` picks the mode: `all` (every
block), `new` (collapse repeats of the same tool), `off` (only live spinners).
Each tool has a Nerd Font glyph and a one-line preview extracted from its
primary argument (`primaryArgKeys`).

## Events in (threading rule)

Bus events arrive on arbitrary goroutines → pushed into the mutex-guarded
`toolEventQueue` → `program.Send(toolEventsReadyMsg{})` → drained inside
`Update`. See [08-events](08-events.md). Events are filtered to the session's
ID, so subagent chatter stays out of the transcript.

## Slash commands (`commands.go`)

A single `commandRegistry` drives the TUI, the `/help` text, and plain-mode
parity (each spec optionally has a `plain` handler). Backend-needing commands
(`/agents /skills /jobs /memory`) call `CommandBackend.Execute` — the
runtime `CommandService` — asynchronously via a `tea.Cmd`. Others act on the
session directly (`/model`, `/compact`, `/context`, `/approvals`,
`/progress`, `/retry`, `/clear`, `/logs`, `/tools`).

## Approval queue panel

While `approvalQueue.Len() > 0`, key handling switches to the queue: `↑/↓/j/k`
select, `v` expands details (scrollable, 12 lines), `a`/`d` decide one,
`A`/`D`/esc decide all. Input submission is blocked until the queue empties.
Decisions append status lines to the chat. If an approval arrives while Logs is
active, the Chat tab gains a `•`; approval actions remain disabled until Tab
returns to Chat, where the visible queue locks view switching until resolved.

## Log panel

`--log-view` tees zap output into a ring buffer (`platform/logging.Buffer`)
rendered through full-width Chat and Logs tabs at every terminal width. `tab`
or `shift+tab` switches views, `/logs` opens Logs from Chat, and the active
view owns keyboard and mouse scrolling. Logs preserves the reader's position
when new records arrive and follows new records only while already at bottom.

## Gotchas

- The TUI runs in the alt screen; anything printed to stdout by other code
  corrupts it — `zap.RedirectStdLog` at bootstrap exists for this reason, and
  agent-command logging defaults to a file when interactive.
- `chatLog` mutates messages in place via indexes; if you reorder/filter
  messages, fix `findToolMessage` and the GC-zeroing patterns with it.
- Tab chrome consumes viewport height; keep new chrome inside the mode-specific
  height calculation or narrow terminals crop content.
