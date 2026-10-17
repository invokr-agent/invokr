# 03 — Provider Client

Package: `clients/`.

## The seam

```go
type LLMProvider interface {
    Completion(ctx, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
    Metadata(ctx) (ProviderMetadata, error)
}
```

`clients.Client` is the only implementation: a thin wrapper over the official
`openai-go` SDK aimed at OpenAI-compatible local servers (llama.cpp first).
The gateway type-asserts optional capabilities on top of it:
`CompletionOnce` (non-streaming), `CompletionWithDeltas` (streaming callback),
`ListModels`. Keep new capabilities as small optional interfaces, asserted at
the call site — don't widen `LLMProvider`.

## What Client does for you

- **Base URL normalization** — `openAIBaseURL` appends `/v1` unless already
  present; `providerBaseURL` strips it for non-OpenAI endpoints (`/props`,
  `/v1/models` raw call).
- **Streaming with a stall watchdog** — `CompletionWithDeltas` wraps the SSE
  stream; a `time.AfterFunc` cancels the context if no chunk arrives within
  `StallTimeout` (default 5 min), including the wait for the *first* chunk
  across SDK retries. Surfaces `provider stalled: no stream data for <d>`
  instead of hanging for the full 10-minute HTTP timeout. A stream that ends
  with zero chunks is an error, not an empty answer.
- **Error normalization** — `normalizeOpenAIError` flattens `openai.Error`
  into `type: message`, and classifies llama.cpp's tool-call-parse 500 into
  the typed `ErrToolCallParseFailed`. This is the one sanctioned
  string-match: llama.cpp sets no structured code for it (the comment in
  `openai.go` explains, and a test pins the wording).
- **Model listing** — `ListModels` GETs `/v1/models`; 404/405 mean "provider
  has no list endpoint" and return `nil, nil` so callers degrade gracefully.
- **Context window discovery** — `Metadata` GETs llama.cpp's `/props` and
  reads `default_generation_settings.n_ctx`. Absent endpoint → zero value,
  caller keeps the configured `context_window_tokens`.
- **`Summarize` / `Embed`** — one-shot helpers used by memory summarization
  and the recall gate (`SummaryInput.MaxTokens` caps the gate's probe).

## Startup wiring (in `platform/runtime/bootstrap.go`)

Both run in background goroutines so the TUI paints immediately:

- `refreshProviderMetadata` — 3 attempts with backoff, stores the window into
  the shared `agent.ProviderContext` (atomic; sessions read it every turn).
- `resolveModelLater` — if the provider lists models and the configured one is
  absent, fall back to the first listed (ADR 0006) unless the user already
  switched models.

## Testing

Use `support/testutil/providertest` — an in-process HTTP fixture that scripts
completions step by step (`ReplyStep`, `ToolCallStep`, `NativeToolCallStep`,
`SSEReplyStep`) and injects faults (`ErrorStep`, `MalformedBody`,
`DropAfterChunks`, `StallAfterChunks`+`StallDuration`, `RetryAfterSeconds`).
It also serves `/props`, `/v1/embeddings`, and records every request for
assertions (`Snapshot`, `AssertExhausted`, `MessagesContain`).

## Gotchas

- There are **two model paths**: this chat client, and the in-process Zerfoo
  memory worker. One config does not control both.
- SDK types (`openai.ChatCompletionMessageParamUnion`) currently flow through
  `agent/` — a known coupling. Don't spread
  them further (e.g. into `capability/*`).
- `Client.rebuild()` is only called from constructors; mutating `BaseURL` or
  `APIKey` on a live client does nothing until rebuilt.
