# Invokr
An agent harness focused on local model usage. It has a solid core tool set and extension capabilities. You can extend it with custom tools by asking the agent to write them.


## Quickstart 
Build from source for now. Release bundle incoming.
You need Go 1.26+ and an OpenAI-compatible endpoint.

```bash
git clone https://github.com/antonikliment/invokr && cd invokr
go build -o invokr .
./invokr setup      # provider URL, model, memory opt-in
./invokr agent      # interactive TUI
./invokr doctor     # check provider, sandbox, interpreters, MCP servers
```


## Features

| Feature | What it means |
|---|---|
| Supply-chain control | Installs resolve through allowlisted hosts, checked against allow/deny lists and a release-age cooldown, then you approve the exact set before anything is installed. |
| Tiered, sandboxed side effects | Shell and toolbox tools run in rootless Linux namespaces, refuse to run rather than silently falling back to the host, and strip the host environment. |
| Scriptable, not just interactive | `exec` emits a JSONL ledger of tool calls as the source of truth, denies approvals by default, and opts in narrowly with `--allow <tier>`. |
| Tiered long-term memory | Markdown + SQLite with optional in-process vector recall, optional Ebbinghaus decay, and archival. |
| Session resume and search | Lossless resume of prior sessions and lexical search across them. |
| Custom subagents, jobs, MCP | Spawn bounded subagents, schedule cron jobs, and wire stdio or streamable-HTTP MCP servers. |
| Telegram and Signal transports | Run the agent as a messaging bot over Telegram or Signal. |


### Against a local model

Any llama.cpp `llama-server` speaks the OpenAI API directly:

```bash
llama-server -m ./model.gguf --port 8080
./invokr --base-url=http://localhost:8080 agent
```

If you'd rather manage runtimes than flags,
[InferenceRig](https://github.com/rivt-ai/InferenceRig) is a local control plane
for llama.cpp and Apple MLX — profiles, model catalog, hardware fit estimates,
start/stop/monitor from a TUI or daemon:

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/rivt-ai/InferenceRig/main/internal/installer/install.sh
less install.sh && sh install.sh
infr                # start a profile from the TUI
```

Point Invokr at the `listen:` host/port from that profile (the sample profile
uses `127.0.0.1:8080`) and everything else is the same.

Ollama and hosted providers work too — anything with a `/v1` OpenAI-compatible
surface:

```bash
./invokr --base-url=http://localhost:11434/v1 --model=qwen3-coder:30b agent
export INVOKR_PROVIDER_API_KEY=…   # or OPENAI_API_KEY for hosted providers
```

## Demo

![Invokr TUI: a prompt, a tiered shell approval, and the answer](docs/assets/demo.gif)

## Supported platforms

| Platform | Status | Notes |
|---|---|---|
| **Linux** | First class | The only platform with the namespace sandbox for toolbox tools. Whole-agent Docker sandbox auto-builds its image here. |
| **macOS** | Supported, with limits | No namespace sandbox. Toolbox tools need either the Docker sandbox with a prebuilt `linux/amd64` image (`INVOKR_SANDBOX_IMAGE`) or an explicit `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` opt-out. Everything else works. |
| **Windows** | Unsupported | Not tested, not built for, no sandbox story. |

This is a scope decision, not an oversight: sandboxing toolbox tools relies on
Linux user namespaces (`tools/core/sandbox/sandbox_linux.go`). Run
`invokr doctor` to see what's actually available on your machine.

## Installation

One-line installer (Linux and macOS; detects OS/arch, downloads the matching
tagged release from GitHub Releases, verifies its checksum, installs to
`~/.local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/antonikliment/invokr/main/install.sh | sh
```

Or via `go install` (requires Go 1.26+):

```bash
go install github.com/antonikliment/invokr@latest
```

Or build from source:

```bash
make install        # builds with version info baked in, copies to ~/.local/bin
# or manually:
go build -o invokr .
./invokr agent
```

`invokr --version` reports the build's tag, commit, and date.

## Documentation

| Document | What it covers |
|---|---|
| [docs/security-model.md](docs/security-model.md) | What each execution path can do, what gates it, and what is explicitly not a boundary. |
| [SECURITY.md](SECURITY.md) | Reporting a vulnerability. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build, test, lint, budgets, PR process. |
| [docs/memory.md](docs/memory.md) | Memory tiers, models, forgetting. |
| [docs/agents/README.md](docs/agents/README.md) | Codebase orientation. |
| [docs/architecture/services.md](docs/architecture/services.md) | Package ownership map. |
| [docs/adr/](docs/adr/) | Design decisions. |
| [docs/reference.md](docs/reference.md) | The detailed manual. |

## License

MIT (SPDX: `MIT`) — see [LICENSE](LICENSE).

