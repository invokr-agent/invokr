# Changelog

## 0.6.0 — unreleased

### Breaking

- **`invokr bot` is removed**, along with its Telegram and Signal transports and
  the `messaging` config block (now ignored). Chats run through
  [agent-relay](https://github.com/invokr-agent/agent-relay), which drives the
  new `invokr acp`:

  ```sh
  curl -fsSL https://raw.githubusercontent.com/invokr-agent/agent-relay/main/install.sh | sh
  agent-relay setup telegram   # or: signal, matrix; agent command: invokr acp
  ```

  Move allowlists and tokens to `agent-relay setup`. Stored bot sessions
  (`messaging.json` in the memory dir) are not migrated.

### Added

- `invokr acp`: invokr as an [ACP](https://agentclientprotocol.com) agent over stdio.
- `request_access` tool for temporary access to a folder outside the workspace.
