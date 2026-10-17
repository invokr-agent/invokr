# browser_agent toolbox plugin

`browser_agent` is a Playwright-backed automation plugin for Invokr.

It uses a dedicated persistent profile and runs one command per invocation using
the standard toolbox stdin/stdout JSON contract.

## Arguments

- `action`: `open` (default), `click`, `fill`, `press`, `read`, `snapshot`, `wait`, `close`
- Locator fields: `role` + optional `name`, `label`, `placeholder`, `text`, `selector`
- Navigation fields: `url`, `search`
- Input/action fields: `value`, `key`
- Optional controls: `timeout_ms`, `headless`

## State and profile

- Default state root: `~/.local/share/invokr/browser-agent`
- Override with: `INVOKR_TOOL_STATE_DIR=/custom/path` (the legacy
  `INVOKR_BROWSER_AGENT_STATE` is also accepted).
- Invokr creates a hashed plugin and `session-<id>` path below the state root.
- Browser profile lives in: `<state-root>/plugin-<id>/session-<id>/profile`
- The profile and live browser daemon are never shared across Invokr sessions.

## Runtime bootstrap

Python toolbox plugins share a virtual environment:

- `../.venv` by default from this plugin directory
- the agent installs this plugin's `requirements.txt` into the shared venv
- this plugin installs Playwright Chromium once in that shared venv

## Dependency pinning

`requirements.txt` pins `playwright` to an exact version (`==`). To upgrade: bump
the pin, re-run the shared-venv install, and re-run `playwright install` for the
matching Chromium build. Notes:

- The pin is `==`, not `--require-hashes`, so it still trusts the PyPI artifact
  for that version. The plugin runs with `network: true` and a full host env
  (default `IsolateEnv: false`); add hash-pinning if stronger supply-chain
  integrity is needed.
- The Chromium binary is pinned separately by `playwright install`, not by this
  file — keep the two in step when upgrading.
