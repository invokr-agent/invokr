# 14 — Chat relays (agent-relay) and the ACP agent

Package: `surface/acpagent/`. Entry: `invokr acp` (`cmd/chat_cli.go`). Relays live
outside this repository: [agent-relay](https://github.com/invokr-agent/agent-relay)
(v0.4.0+) carries Telegram, Signal and Matrix to invokr over ACP.

## Topology

```text
Telegram / Signal / Matrix ──▶ agent-relay ─ACP stdio─▶ invokr acp
                                                              └─ chatSession (per ACP session)
                                                                   ├─ turn.IdentifiedSession
                                                                   └─ approval.Broker

a running TUI ─/relay open─▶ $XDG_RUNTIME_DIR/invokr/relay.sock ◀agent-relay
                                    └─ the same chatSession, approval queue and transcript
```

invokr owns no transport, no allowlist and no chat→session store. A relay maps
conversations to sessions itself, and its `setup --apply telegram` writes
`"agent": ["invokr", "acp"]`.

## Boundaries

- One process, one session map: sessions live only as long as the process, so a
  relay restart starts fresh sessions. `capability/sessionstore.Resume` is the
  CLI-side replay of a stored transcript; ACP does not resume.
- The workspace is invokr's own cwd at start; `NewSession` refuses a different
  `cwd` rather than serving a second tree.
- Approval requests carry tool kind and title to the client; the client answers
  with allow-once or reject-once. A failed, cancelled or unanswered request
  denies, and no standing grant is possible over ACP.
- stdout carries the protocol, so `acp` refuses a `log.output = stdout` config
  and moves `os.Stdout` to stderr before serving.
- The reply is the model's message plus a one-line tool trailer that merges
  evidence, failures and denials per call.

## Relay setup

```sh
curl -fsSL https://raw.githubusercontent.com/invokr-agent/agent-relay/main/install.sh | sh
cd ~/my-project                      # the workspace invokr serves
agent-relay setup telegram           # or: signal, matrix; agent command: invokr acp
invokr relay start                   # background, for this directory; also: stop, status, --replace
invokr relay service install         # optional: a login service that restarts on failure
agent-relay update                   # later: install the newest release
```

The relay spawns `invokr acp` in its configured `cwd`, which must be the
workspace: `NewSession` refuses any other directory. While an `agent-relay`
process runs, the TUI header shows a `relay ●` pill (polled every 15s).

`/relay` in the TUI prints the same facts as `invokr relay status` — whether the
relay runs, the directory it serves (agent-relay's own `status` text) and whether
*this* session shares itself. `/relay open` opens that share, `/relay close` closes
it, and quitting the TUI closes it too; either way the relay goes back to driving
its own `invokr acp`.

## Sharing a live session

`/relay open` listens on `$XDG_RUNTIME_DIR/invokr/relay.sock` (dir `0700`, socket
`0600`; `~/.invokr/run/relay.sock` when `XDG_RUNTIME_DIR` is unset), writes that
path into agent-relay's `agent_socket`, and restarts the relay for this directory,
which then dials the socket instead of spawning `invokr acp`. The reply names the
directory the relay now serves. `/relay close`, and quitting the TUI, remove the
socket, clear `agent_socket` and restart the relay back onto `invokr acp`.

A TUI killed mid-share leaves `agent_socket` set and a socket nobody answers. agent-relay
dials it, fails that check and falls back to spawning `invokr acp` for the directory, so
the relay keeps working while the share is dead; the next `/relay open` removes the stale
socket and rewrites `agent_socket`.

The socket is the whole trust boundary, so every one of these is checked before a
session is served: the socket file and its directory deny group and other writes,
the file belongs to this uid, and the process on the other end of the connection
runs as this uid (peer credentials; a connection whose uid cannot be read is refused).
A socket file another session answers is reported, not taken over: that session's
contacts would otherwise start talking to this one. A stale socket (nobody listening)
is removed.

Chat contacts then talk to *this* session: `session/new` and `session/load` resolve
to the TUI's own session, so their turns land in the transcript, mode and approval
queue the human is looking at, and they are serialised with the human's turns rather
than running beside them. A chat contact gets no session of its own, so it cannot
switch cwd or mode.
Changing mode, model or reasoning effort at the TUI is sent to the contacts, so the
relay's pinned status (agent-relay with chat status) follows the host.

A turn a chat started answers for its own approvals, and only its own: nothing a
turn the human at the terminal started is ever offered to a chat contact, so chat
never sees an approval it did not raise (agent-relay edits its own approval prompt
to "✅ Approved — <tool>" when a contact taps a button, which would otherwise
read as a decision the contact never made). A call the execution owner classified
as a read — the shell's read-only tier, the code interpreter's read-workspace tier,
a native call that marks itself `ReadOnly` — runs without asking: prompting for
reads only floods the chat. Anything that writes, execs opaquely, publishes or
reaches the network is offered in the chat as allow-once or reject-once and also
shows in the TUI's queue; the first answer wins, and an unanswered call denies.
Plan's block, standing denials and the refusal to auto-approve a read outside the
workspace still apply.

`relay.chat_follows_mode` (default false; `INVOKR_RELAY_CHAT_FOLLOWS_MODE`) opts a
single-user setup out of that boundary: a chat turn then uses this session's mode,
standing grants and the Auto publishing/network toggles, exactly like a TUI turn.
Plan's block and standing denials still refuse. It is opt-in because it drops the
threat model the pin protects: a hijacked chat message can then write and exec
whatever this session already allows.

The relay owns the allowlists, the credentials and the answer formatting;
invokr only sees prompts and permission requests.
