# 14 — Chat relays (agent-relay) and the ACP agent

Package: `surface/acpagent/`. Entry: `invokr acp` (`cmd/chat_cli.go`). Relays live
outside this repository: [agent-relay](https://github.com/invokr-agent/agent-relay)
(v0.2.0+) carries Telegram, Signal and Matrix to invokr over ACP.

## Topology

```text
Telegram / Signal / Matrix ──▶ agent-relay ──ACP stdio──▶ invokr acp
                                                              └─ chatSession (per ACP session)
                                                                   ├─ turn.IdentifiedSession
                                                                   └─ approval.Broker
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
invokr relay start                   # = agent-relay service install; also: stop, status
agent-relay update                   # later: install the newest release
```

The relay spawns `invokr acp` in its configured `cwd`, which must be the
workspace: `NewSession` refuses any other directory. While an `agent-relay`
process runs, the TUI header shows a `relay ●` pill (polled every 15s).

The relay owns the allowlists, the credentials and the answer formatting;
invokr only sees prompts and permission requests.
