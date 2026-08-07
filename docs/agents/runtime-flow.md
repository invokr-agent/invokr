# Invokr Runtime Flow

## Startup

1. `main.go` calls setup preflight.
2. Config loads from defaults, file, flags, and env.
3. If `sandbox.enabled` and `INVOKR_IN_SANDBOX` is unset, the process re-execs
   itself inside a container (`capability/docker_sandbox`) and the outer process exits.
4. `cmd.NewRootCmd()` builds Cobra commands.
5. `cmd/root.go` persistent pre-run bootstraps runtime except for setup paths.
6. `platform/runtime/bootstrap.go` constructs services and returns cleanup.

Runtime bootstrap builds:

- logger (with config validation and optional TUI log-buffer sink)
- OpenAI-compatible provider; model availability against `/v1/models` and
  context-window metadata from `/props` are resolved in background goroutines
  after first paint (first-listed fallback; ADR 0006)
- ordered event dispatcher
- approval broker
- optional Zerfoo memory worker (lazy model loading)
- memory manager (chat-model summarizer wired in)
- optional recall gate (`capability/memory/recallgate`, off by default)
- prompt history store
- memory event handler
- skill manager
- custom agent-definition registry (`capability/agentdef`)
- tool caller with native and toolbox tools (delegate wired to the agent registry)
- tagged-JSON syntax
- agent session (plus a chat-session factory for `chat serve`) and the
  slash-command backend (`CommandService`)

## User Message Flow

```text
user input
  -> cmd/agentui submit or plain runner
  -> turn.Session.Send
  -> agent.Session.Send
  -> agent.Runner.Run
  -> OpenAIModelGateway.Begin/Continue
  -> agentprompt.Builder.Build
      -> memory.PromptBlocks
      -> skills.Specs
      -> tools.PromptSpecs
  -> clients.LLMProvider.Completion
  -> TurnProcessor.Process
      -> final reply, or
      -> native/toolbox tool call
  -> tools.Caller.HandleCall
  -> repeat until final reply or max turns
```

`AgentConfig.MaxTurns` bounds the loop. Token usage is accumulated in the runner.
Native tool calls from one model response execute concurrently while results retain
their original order. Repeated identical call sets trigger a model nudge instead of
burning the remaining turn budget. At the turn limit, the runner makes one final
tool-disabled request for a best-effort answer while preserving the tool-outcome ledger.
Tool-call parse failures use `clients.ErrToolCallParseFailed` for typed retry handling.

## Prompt And Syntax

The prompt builder assembles configured base prompt, syntax instructions, visible tool specs, skill descriptions, and memory blocks. It emits prompt lifecycle events around memory block construction.

`TaggedJSONSyntax` supports:

- final reply JSON: `{"type":"reply","message":"..."}`
- tagged tool call block with `<|tool_call>` delimiters
- tool result JSON returned as user-role content
- forgiving fallback where malformed final text can become a reply

Native OpenAI tool calling can be enabled by config. `OpenAINativeToolCodec` maps Invokr tool specs to OpenAI tool payloads.

## Events

Events are typed and structured. Important families:

- prompt lifecycle
- response lifecycle
- tool lifecycle
- toolbox refresh
- audit
- recall lifecycle
- save lifecycle

Runtime uses the single ordered dispatcher (`platform/events/dispatcher.go`). Handlers run synchronously in registration order and panics are recovered. There is no async dispatcher; the one slow handler (memory summarization) owns its own bounded background worker inside the memory curator instead.
