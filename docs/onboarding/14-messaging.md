# 14 — Chat Transports (Telegram / Signal)

Package: `surface/messaging/` (+ `telegram/`, `signal/`). Entry:
`invokr bot serve` (`cmd/chat_cli.go`).

## Topology

```text
transports (Listen loops) ──fan-in──▶ Router.Serve ──route──▶ chatWorker (per chat)
                                                             └─ turn.IdentifiedSession
```

- **Router** (`router.go`): starts every transport listener, fans inbound
  messages into one channel, and lazily creates one `chatWorker` per
  `<transport>:<chatID>` key (`sync.Map`, closed workers evicted with
  `CompareAndDelete` and recreated).
- **chatWorker**: an inbox (cap 8 — overflow answers "busy — try again
  shortly"), a 5-minute idle timeout, and its own agent session + approval
  broker from `Services.NewChatSession`. The session ID is persisted in
  `messaging.db` (`chat_sessions` table) so a chat resumes the same session
  across restarts.
- **Per message**: a typing indicator keeps refreshing while the turn runs,
  approvals are drained concurrently (2-minute timeout ⇒ deny), panics are
  recovered and reported as "Internal error".

## Access control — fail closed

Both the transport constructors and the router check allowlists
(`allowed_chat_ids` / `allowed_numbers`). An **empty allowlist admits
nobody**; `allow_all: true` is the explicit escape hatch (logged loudly), and
`cmd/chat_cli.go` refuses to start a transport with neither. Unauthorized
senders get "Access denied" (Telegram messages) or are silently ignored
(Signal inbound filter).

## Telegram (`telegram/telegram.go`)

Long-polls `getUpdates` (30s timeout, exponential backoff on failure, 401 is
fatal). Approvals are inline keyboards — `approve:<id>`/`deny:<id>` callback
data resolves a pending channel, and the message is edited to
✅ Approved / ❌ Denied / ⚠️ Expired so stale buttons can't misreport. Replies
chunk at 4096 runes.

## Signal (`signal/signal.go`)

JSON-RPC over the `signal-cli` UNIX socket: `subscribeReceive` on connect,
notifications parsed off one reader loop, RPC responses matched by ID to
pending channels, auto-reconnect with capped backoff. Approvals are text:
`YES <id>` / `NO <id>` (bare YES/NO works when exactly one request is pending
for that sender). Replies chunk at 2000 runes.

## Adding a transport

Implement `messaging.Transport` (`Name`, `Listen`, `Send`, `AskApproval` —
optionally the `typingTransport` extension), construct it in
`buildChatTransports` behind a fail-closed config check, and keep everything
safe for concurrent calls. The router needs nothing else.

## Gotchas

- `ChunkMessage` splits on rune boundaries only — mid-word breaks are
  accepted by design.
- Telegram callback handling does not re-check the chat allowlist; request
  IDs are UUIDs (see bugs doc for the improvement note).
- One worker per chat means one turn at a time per chat; the small inbox is
  the only buffering.
